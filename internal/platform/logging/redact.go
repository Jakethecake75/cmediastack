// Package logging provides structured JSON logging with mandatory redaction.
//
// Requirements §8: "Automatic redaction in logs and in any 'copy diagnostics'
// feature — API keys, tokens, passwords, proxy creds, and personal paths."
// Redaction here is a property of the handler, not of each call site, because
// a redaction rule that depends on every caller remembering it is not a rule.
package logging

import (
	"log/slog"
	"regexp"
	"strings"
)

// Redacted is what replaces a sensitive value.
const Redacted = "[REDACTED]"

// sensitiveKeys are attribute keys whose values are never logged. Matching is
// case-insensitive and substring-based: "indexer_api_key" matches "api_key".
var sensitiveKeys = []string{
	"password",
	"passwd",
	"secret",
	"token",
	"api_key",
	"apikey",
	"authorization",
	"cookie",
	"session_id",
	"refresh",
	"totp",
	"private_key",
	"master_key",
	"recovery_code",
	"credential",
	"proxy_user",
	"proxy_pass",
	"client_secret",
}

// inlinePatterns catch secrets embedded in free-text messages and URLs, where
// a key-based rule cannot reach them.
var inlinePatterns = []*regexp.Regexp{
	// URL userinfo: scheme://user:password@host
	regexp.MustCompile(`(?i)([a-z0-9+.-]+://[^\s:/@]+):([^\s@]+)@`),
	// Query-string secrets: ?apikey=... &token=... &passkey=...
	regexp.MustCompile(`(?i)([?&](?:api_?key|token|passkey|secret|password)=)[^&\s"']+`),
	// Bearer tokens
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]{8,}`),
	// A feed token in a feed's address (ADR-0041): the path is the credential.
	regexp.MustCompile(`(/feeds/)[^/\s"']+`),
	// A feed token on its own.
	regexp.MustCompile(`()cms_feed_[A-Za-z0-9_-]+`),
}

// IsSensitiveKey reports whether an attribute key must have its value redacted.
func IsSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, s := range sensitiveKeys {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// RedactString removes secrets embedded in free text.
func RedactString(s string) string {
	for _, re := range inlinePatterns {
		s = re.ReplaceAllString(s, "${1}"+Redacted)
	}
	return s
}

// redactHandler wraps an slog.Handler and rewrites every record before it is
// emitted. Because it sits at the handler layer, no call site can bypass it.
type redactHandler struct {
	inner slog.Handler
}

// NewRedactHandler wraps h so that sensitive attributes and inline secrets are
// removed from every record it handles.
func NewRedactHandler(h slog.Handler) slog.Handler {
	return &redactHandler{inner: h}
}

func (h *redactHandler) Enabled(ctx contextT, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *redactHandler) Handle(ctx contextT, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, RedactString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

func (h *redactHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = redactAttr(a)
	}
	return &redactHandler{inner: h.inner.WithAttrs(out)}
}

func (h *redactHandler) WithGroup(name string) slog.Handler {
	return &redactHandler{inner: h.inner.WithGroup(name)}
}

// redactAttr redacts one attribute, recursing into groups.
func redactAttr(a slog.Attr) slog.Attr {
	if IsSensitiveKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		src := v.Group()
		out := make([]slog.Attr, len(src))
		for i, ga := range src {
			out[i] = redactAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	}
	if v.Kind() == slog.KindString {
		return slog.String(a.Key, RedactString(v.String()))
	}
	return slog.Attr{Key: a.Key, Value: v}
}

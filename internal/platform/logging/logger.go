package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

// contextT is an alias so redact.go can reference context.Context without
// importing it separately in a file that is otherwise pure string handling.
type contextT = context.Context

// Options configure the root logger.
type Options struct {
	Level  string            // debug | info | warn | error
	Format string            // json | text
	Module map[string]string // per-module level overrides
	// Ring, when set, also keeps the recent records — redacted, like the
	// output — for the administrator's logs screen (ADR-0040).
	Ring *Ring
}

// New builds the root logger. Every logger in the application descends from
// this one, so the redaction handler cannot be bypassed by constructing a
// logger elsewhere.
func New(w io.Writer, opts Options) *slog.Logger {
	lvl := parseLevel(opts.Level)

	var base slog.Handler
	handlerOpts := &slog.HandlerOptions{Level: lvl}
	if strings.EqualFold(opts.Format, "text") {
		base = slog.NewTextHandler(w, handlerOpts)
	} else {
		base = slog.NewJSONHandler(w, handlerOpts)
	}

	if opts.Ring != nil {
		base = fanout{base, &ringHandler{ring: opts.Ring, level: lvl}}
	}
	return slog.New(NewRedactHandler(base))
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyLogger
)

// WithRequestID attaches a request ID that propagates into logs, and across
// the Unix socket to the downloader process, so records stay correlatable
// across the process boundary described in ADR-0007.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID, id)
}

// RequestID returns the request ID attached to ctx, or "".
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}

// WithLogger attaches a logger to ctx.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKeyLogger, l)
}

// FromContext returns the logger attached to ctx, or the default logger.
// If a request ID is present it is added automatically.
func FromContext(ctx context.Context) *slog.Logger {
	l, ok := ctx.Value(ctxKeyLogger).(*slog.Logger)
	if !ok || l == nil {
		l = slog.Default()
	}
	if id := RequestID(ctx); id != "" {
		l = l.With(slog.String("request_id", id))
	}
	return l
}

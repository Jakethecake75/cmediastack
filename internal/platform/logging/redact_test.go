package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// These are negative tests: each asserts that a secret which SHOULD NOT reach
// the log does not reach it. Requirements §12: security-relevant logic ships
// with the attack that must fail.

func capture(t *testing.T, fn func(l *slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	l := New(&buf, Options{Level: "debug", Format: "json"})
	fn(l)
	return buf.String()
}

func TestSensitiveAttributeKeysAreRedacted(t *testing.T) {
	secrets := map[string]string{
		"password":         "hunter2-correct-horse",
		"api_key":          "abcd1234efgh5678",
		"indexer_api_key":  "nested-key-name-still-matches",
		"authorization":    "Basic dXNlcjpwYXNz",
		"proxy_password":   "s0cks-cred",
		"totp_secret":      "JBSWY3DPEHPK3PXP",
		"refresh_token":    "rt_9f8e7d6c",
		"client_secret":    "oidc-shhh",
		"recovery_code":    "aaaa-bbbb-cccc",
		"session_id":       "sess_0123456789",
		"MASTER_KEY":       "uppercase-key-name",
		"db_credential":    "pg://x",
		"Cookie":           "cms_session=abc",
		"private_key_pem":  "-----BEGIN PRIVATE KEY-----",
		"passwd_confirm":   "another-one",
		"apikey":           "no-underscore-variant",
		"proxy_user":       "nord-user",
		"some_token_field": "tok_zzz",
	}

	for key, value := range secrets {
		out := capture(t, func(l *slog.Logger) {
			l.Info("configured", slog.String(key, value))
		})
		if strings.Contains(out, value) {
			t.Errorf("attribute %q leaked its value into the log:\n%s", key, out)
		}
		if !strings.Contains(out, Redacted) {
			t.Errorf("attribute %q was not marked redacted:\n%s", key, out)
		}
	}
}

func TestNonSensitiveAttributesSurvive(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Info("import finished",
			slog.Int64("media_item_id", 4821),
			slog.String("quality", "Bluray-1080p"),
			slog.String("indexer", "example-tracker"),
		)
	})
	for _, want := range []string{"4821", "Bluray-1080p", "example-tracker"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q to survive redaction:\n%s", want, out)
		}
	}
}

func TestURLUserinfoIsRedacted(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Info("dialing socks5://norduser:supersecret@proxy.example:1080")
	})
	if strings.Contains(out, "supersecret") {
		t.Errorf("URL password leaked:\n%s", out)
	}
	if !strings.Contains(out, "norduser") {
		t.Errorf("username should survive so the log stays useful:\n%s", out)
	}
}

func TestQueryStringSecretsAreRedacted(t *testing.T) {
	cases := []struct {
		msg    string
		secret string
	}{
		{"GET https://tracker.example/api?apikey=deadbeefcafe&t=search", "deadbeefcafe"},
		{"GET https://tracker.example/api?t=search&token=zzz999yyy", "zzz999yyy"},
		{"GET https://tracker.example/rss?passkey=abc123def456", "abc123def456"},
	}
	for _, tc := range cases {
		out := capture(t, func(l *slog.Logger) { l.Info(tc.msg) })
		if strings.Contains(out, tc.secret) {
			t.Errorf("query secret %q leaked:\n%s", tc.secret, out)
		}
	}
}

func TestBearerTokenIsRedacted(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Warn("upstream rejected header Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig")
	})
	if strings.Contains(out, "eyJhbGciOiJIUzI1NiJ9") {
		t.Errorf("bearer token leaked:\n%s", out)
	}
}

func TestRedactionSurvivesWithAttrsAndGroups(t *testing.T) {
	// A logger built with .With() must not become a bypass.
	var buf bytes.Buffer
	l := New(&buf, Options{Level: "debug", Format: "json"})
	child := l.With(slog.String("api_key", "leaked-via-with"))
	child.Info("using indexer")

	if strings.Contains(buf.String(), "leaked-via-with") {
		t.Errorf("WithAttrs bypassed redaction:\n%s", buf.String())
	}

	buf.Reset()
	l.Info("nested", slog.Group("proxy",
		slog.String("host", "proxy.example"),
		slog.String("password", "leaked-via-group"),
	))
	if strings.Contains(buf.String(), "leaked-via-group") {
		t.Errorf("group attribute bypassed redaction:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "proxy.example") {
		t.Errorf("non-sensitive group member should survive:\n%s", buf.String())
	}
}

func TestOutputIsValidJSON(t *testing.T) {
	out := capture(t, func(l *slog.Logger) {
		l.Info("hello", slog.String("k", "v"))
	})
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil {
		t.Fatalf("log line is not valid JSON: %v\n%s", err, out)
	}
	if m["msg"] != "hello" {
		t.Errorf("msg = %v", m["msg"])
	}
}

func TestRequestIDPropagatesThroughContext(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, Options{Level: "debug", Format: "json"})
	ctx := WithLogger(WithRequestID(context.Background(), "req-abc123"), l)

	FromContext(ctx).Info("handling")

	if !strings.Contains(buf.String(), "req-abc123") {
		t.Errorf("request id missing from log:\n%s", buf.String())
	}
}

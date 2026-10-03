package logging

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// ADR-0040, decision 2: the ring holds what the output holds — redacted —
// newest first, bounded, filtered by level and text.
func TestTheRingKeepsRecentRedactedRecords(t *testing.T) {
	var out bytes.Buffer
	ring := NewRing(3)
	log := New(&out, Options{Level: "info", Format: "text", Ring: ring})

	log.Debug("below the level")
	log.Info("first", slog.String("password", "hunter2"))
	log.With(slog.String("task", "import")).WithGroup("file").
		Warn("second", slog.String("name", "Dune.mkv"))
	log.Error("third")
	log.Info("fourth, and the first is pushed out")

	all := ring.Records(slog.LevelDebug, "", 10)
	if len(all) != 3 || all[0].Message != "fourth, and the first is pushed out" || all[2].Message != "second" {
		t.Fatalf("records %+v", all)
	}
	second := all[2]
	if got := fmt.Sprint(second.Attrs); got != "[{task import} {file.name Dune.mkv}]" {
		t.Errorf("attributes %s", got)
	}
	if warn := ring.Records(slog.LevelWarn, "", 10); len(warn) != 2 {
		t.Errorf("warn and above: %+v", warn)
	}
	if hit := ring.Records(slog.LevelDebug, "DUNE", 10); len(hit) != 1 || hit[0].Message != "second" {
		t.Errorf("text search: %+v", hit)
	}
	if one := ring.Records(slog.LevelDebug, "", 1); len(one) != 1 {
		t.Errorf("the limit: %+v", one)
	}

	// Redaction reaches the ring as it reaches the output.
	ring = NewRing(10)
	log = New(&out, Options{Level: "info", Format: "text", Ring: ring})
	log.Info("login", slog.String("password", "hunter2"), slog.String("token", "cms_pat_abc"))
	for _, kv := range ring.Records(slog.LevelInfo, "", 10)[0].Attrs {
		if strings.Contains(kv.Value, "hunter2") || strings.Contains(kv.Value, "cms_pat_abc") {
			t.Errorf("the ring kept a secret: %+v", kv)
		}
	}
	if strings.Contains(out.String(), "hunter2") {
		t.Error("the output kept a secret")
	}
}

// ADR-0041: a feed token is in an address, and addresses are logged.
func TestAFeedTokenIsRedacted(t *testing.T) {
	for _, in := range []string{
		"GET /api/v1/feeds/cms_feed_abcDEF123_-xyz/calendar.ics",
		"token cms_feed_abcDEF123_-xyz leaked",
		"/api/v1/feeds/anything-at-all/rss",
	} {
		out := RedactString(in)
		if strings.Contains(out, "abcDEF123") || strings.Contains(out, "anything-at-all") {
			t.Errorf("%q -> %q", in, out)
		}
	}
	if got := RedactString("/api/v1/feeds/cms_feed_x/rss"); got != "/api/v1/feeds/[REDACTED]/rss" {
		t.Errorf("the address shape is lost: %q", got)
	}
}

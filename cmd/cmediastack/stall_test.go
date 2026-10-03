package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// Each stall is on the record, by system:download, saying what was done about
// it and never the download link (ADR-0034).
func TestAStallIsWrittenToTheAuditLog(t *testing.T) {
	database, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "a.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	log := audit.New(database, func() time.Time { return now })
	summary := reportStalls(context.Background(), log, []download.Stall{
		{Record: download.Record{InfoHash: strings.Repeat("a", 40), Title: "Machine.S01.1080p-GRP"},
			Since: now.Add(-26 * time.Hour), GivenUp: true},
		{Record: download.Record{InfoHash: strings.Repeat("b", 40), Title: "Person.S01E01.1080p-GRP"},
			Since: now.Add(-30 * time.Hour)},
	}, now)
	if !strings.Contains(summary, "gave up 1 stalled automatic download(s): Machine.S01.1080p-GRP") ||
		!strings.Contains(summary, "1 download(s) a person grabbed have stopped moving: Person.S01E01.1080p-GRP") {
		t.Errorf("summary %q", summary)
	}

	rows, err := database.QueryContext(context.Background(), `
		SELECT actor_label, action, target_id, detail FROM audit_event
		WHERE action = 'acquisition.stalled' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var label, action, target, detail string
		if err := rows.Scan(&label, &action, &target, &detail); err != nil {
			t.Fatal(err)
		}
		if label != "system:download" {
			t.Errorf("actor %q", label)
		}
		got = append(got, target[:1]+" "+detail)
	}
	if len(got) != 2 ||
		!strings.Contains(got[0], "Machine.S01.1080p-GRP | no progress for 26h0m0s") || !strings.Contains(got[0], "given up") ||
		!strings.Contains(got[1], "no progress for 30h0m0s") || !strings.Contains(got[1], "left running") {
		t.Errorf("audit lines %q", got)
	}
}

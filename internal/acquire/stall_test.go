package acquire

import (
	"context"
	"fmt"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/indexer"
)

// A download automatic acquisition grabbed and then gave up on, because it
// stopped moving, no longer holds its item — and the queue, which is the
// blocklist, keeps that release from being grabbed again (ADR-0034).
func TestAGivenUpDownloadFreesItsItem(t *testing.T) {
	r := newRig(t, Config{})
	sev, epID := wantSeverance(r)
	r.client.feed = []indexer.Result{rel(good, 1, 25)}
	r.runRecent()
	if got := r.targets(); len(got) != 1 {
		t.Fatalf("grabbed %v, want the one release", got)
	}
	if r.inFlight()[Key{ItemID: sev, Season: 2, Episode: 3}] != true {
		t.Fatal("the grab does not hold its episode")
	}

	// It never moves; the stall task gives up on it.
	if marked, err := r.queueDB.MarkStalled(context.Background(), hash(1), true); err != nil || !marked {
		t.Fatalf("marked=%v err=%v", marked, err)
	}
	if r.inFlight()[Key{ItemID: sev, Season: 2, Episode: 3}] {
		t.Fatal("a given-up download still holds its episode; nothing else would ever be fetched")
	}

	// The same release is offered again beside another: the other is grabbed.
	r.client.feed = []indexer.Result{rel(good, 1, 90), rel("Severance.S02E03.720p.WEB-DL.H264-OTHER", 2, 5)}
	summary := r.runRecent()
	got := r.targets()
	if len(got) != 2 || got[1] != fmt.Sprintf("%d S02E03", sev) {
		t.Fatalf("grabbed %v (summary: %s); want another release for S02E03", got, summary)
	}
	if last := r.queue.added()[1]; last.Title == good {
		t.Error("the release that was given up on was grabbed again")
	}
	_ = epID
}

func (r *rig) inFlight() map[Key]bool {
	r.t.Helper()
	m, err := r.store.InFlight(r.ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

// Automatic is who the stall rule gives up on: no person, and automatic
// acquisition's label. A person who happens to be called that is a person.
func TestOnlyAutomaticAcquisitionsDownloadsAreAutomatic(t *testing.T) {
	person := int64(1)
	for _, tc := range []struct {
		rec  download.Record
		want bool
	}{
		{download.Record{AddedLabel: Label}, true},
		{download.Record{AddedLabel: Label, AddedBy: &person}, false},
		{download.Record{AddedLabel: "jacob", AddedBy: &person}, false},
		{download.Record{AddedLabel: ""}, false},
	} {
		if got := Automatic(tc.rec); got != tc.want {
			t.Errorf("%+v: automatic = %v, want %v", tc.rec, got, tc.want)
		}
	}
}

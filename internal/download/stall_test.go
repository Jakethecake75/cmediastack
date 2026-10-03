package download

import (
	"strings"
	"testing"
	"time"
)

// Stalled downloads (ADR-0034).

var stallStart = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func TestAStallIsNoProgressForTheThreshold(t *testing.T) {
	day := 24 * time.Hour
	added := stallStart.Add(-72 * time.Hour)
	for _, tc := range []struct {
		name    string
		rec     Record
		now     time.Time
		started time.Time
		after   time.Duration
		want    bool
	}{
		{"never moved, a day since added", Record{Status: StatusDownloading, AddedAt: added},
			stallStart, added, day, true},
		{"never moved, not yet a day", Record{Status: StatusDownloading, AddedAt: stallStart.Add(-23 * time.Hour)},
			stallStart, added, day, false},
		{"moved an hour ago", Record{Status: StatusDownloading, AddedAt: added, ProgressedAt: stallStart.Add(-time.Hour)},
			stallStart, added, day, false},
		{"moved two days ago", Record{Status: StatusQueued, AddedAt: added, ProgressedAt: stallStart.Add(-48 * time.Hour)},
			stallStart, added, day, true},
		// The clock runs only while the engine does: an instance that was off
		// has not watched anything fail to arrive.
		{"engine started an hour ago", Record{Status: StatusDownloading, AddedAt: added},
			stallStart, stallStart.Add(-time.Hour), day, false},
		{"off at zero", Record{Status: StatusDownloading, AddedAt: added}, stallStart, added, 0, false},
		{"finished", Record{Status: StatusComplete, AddedAt: added}, stallStart, added, day, false},
		{"stopped by a person", Record{Status: StatusStopped, AddedAt: added}, stallStart, added, day, false},
	} {
		got, _ := stallVerdict(tc.rec, tc.now, tc.started, tc.after)
		if got != tc.want {
			t.Errorf("%s: stalled = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestProgressIsKeptAndClearsTheMark(t *testing.T) {
	s, _ := testStore(t)
	clock := stallStart
	s.now = func() time.Time { return clock }
	h := strings.Repeat("a", 40)
	if err := s.Put(t.Context(), Record{InfoHash: h, Title: "x", Magnet: "magnet:?xt=urn:btih:" + h,
		Status: StatusDownloading}); err != nil {
		t.Fatal(err)
	}

	if err := s.NoteProgress(t.Context(), h, 100); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(t.Context(), h)
	if got.ProgressBytes != 100 || !got.ProgressedAt.Equal(clock) {
		t.Fatalf("progress %d at %v, want 100 at %v", got.ProgressBytes, got.ProgressedAt, clock)
	}

	clock = clock.Add(25 * time.Hour)
	if marked, err := s.MarkStalled(t.Context(), h, false); err != nil || !marked {
		t.Fatalf("marked=%v err=%v", marked, err)
	}
	if marked, _ := s.MarkStalled(t.Context(), h, false); marked {
		t.Error("a stall was marked twice; it is reported once")
	}
	got, _ = s.Get(t.Context(), h)
	if !got.StalledAt.Equal(clock) || got.Status != StatusDownloading {
		t.Fatalf("stalled at %v status %s", got.StalledAt, got.Status)
	}

	// Fewer bytes than recorded — a restart re-verifying — is not progress.
	clock = clock.Add(time.Hour)
	if err := s.NoteProgress(t.Context(), h, 50); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Get(t.Context(), h); got.StalledAt.IsZero() || got.ProgressBytes != 100 {
		t.Errorf("progress %d, stalled at %v: fewer bytes cleared the mark", got.ProgressBytes, got.StalledAt)
	}
	if err := s.NoteProgress(t.Context(), h, 200); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Get(t.Context(), h); !got.StalledAt.IsZero() || got.ProgressBytes != 200 ||
		!got.ProgressedAt.Equal(clock) {
		t.Errorf("progress %d at %v, stalled at %v: moving again must clear the mark",
			got.ProgressBytes, got.ProgressedAt, got.StalledAt)
	}
}

func TestAMachinesStallIsGivenUpAPersonsIsReported(t *testing.T) {
	s, _ := testStore(t)
	clock := stallStart
	s.now = func() time.Time { return clock }
	machine, persons, moving := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	for _, r := range []Record{
		{InfoHash: machine, Title: "Machine.S01.1080p-GRP", AddedLabel: "system:acquire"},
		{InfoHash: persons, Title: "Person.S01E01.1080p-GRP", AddedLabel: "jacob"},
		{InfoHash: moving, Title: "Moving.S01E01.1080p-GRP", AddedLabel: "system:acquire"},
	} {
		r.Magnet, r.Status = "magnet:?xt=urn:btih:"+r.InfoHash, StatusDownloading
		if err := s.Put(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	m := &Manager{store: s, started: stallStart, now: func() time.Time { return clock }}
	giveUp := func(r Record) bool { return r.AddedBy == nil && r.AddedLabel == "system:acquire" }
	var stopped []string
	stop := func(h string) error { stopped = append(stopped, h); return nil }

	clock = clock.Add(20 * time.Hour)
	transfers := []Transfer{{InfoHash: machine}, {InfoHash: persons}, {InfoHash: moving, Completed: 10}}
	if got, err := m.judgeStalls(t.Context(), transfers, stop, 24*time.Hour, giveUp); err != nil || len(got) != 0 {
		t.Fatalf("after 20 hours: %+v %v; nothing has stalled yet", got, err)
	}

	clock = clock.Add(5 * time.Hour)
	transfers[2].Completed = 20 // still moving
	got, err := m.judgeStalls(t.Context(), transfers, stop, 24*time.Hour, giveUp)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("stalls %+v, want the machine's and the person's", got)
	}
	for _, st := range got {
		switch st.Record.InfoHash {
		case machine:
			if !st.GivenUp {
				t.Error("the machine's stalled download was not given up")
			}
		case persons:
			if st.GivenUp {
				t.Error("a person's download was given up; it is theirs to remove")
			}
		default:
			t.Errorf("%s stalled while moving", st.Record.Title)
		}
		if !st.Since.Equal(stallStart) {
			t.Errorf("%s: since %v, want %v", st.Record.Title, st.Since, stallStart)
		}
	}
	if strings.Join(stopped, ",") != machine {
		t.Errorf("stopped %v, want only the machine's", stopped)
	}
	if r, _ := s.Get(t.Context(), machine); r.Status != StatusStopped || r.StalledAt.IsZero() {
		t.Errorf("machine's row: %s, stalled at %v", r.Status, r.StalledAt)
	}
	if r, _ := s.Get(t.Context(), persons); r.Status != StatusDownloading || r.StalledAt.IsZero() {
		t.Errorf("person's row: %s, stalled at %v", r.Status, r.StalledAt)
	}

	// Once each.
	clock = clock.Add(time.Hour)
	if again, _ := m.judgeStalls(t.Context(), transfers, stop, 24*time.Hour, giveUp); len(again) != 0 {
		t.Errorf("reported again: %+v", again)
	}
}

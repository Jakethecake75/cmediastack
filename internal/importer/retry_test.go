package importer

import (
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// A download that was SKIPPED is not offered to the importer every minute.
//
// The import task runs every minute and used to re-attempt anything not yet
// imported. A skip for a reason that will not change — a disc image, a season
// pack, a file that is not the episode it was grabbed for — therefore added a
// row and an INFO line every minute for as long as the download existed.

// skippable is a download the importer will always skip: a disc image.
func (r *rig) skippable() Source {
	r.t.Helper()
	return r.download(hash(0), "Film.2019.1080p.BluRay-GRP", map[string]int64{
		"Film.2019.1080p.BluRay-GRP.iso": 20 * mib,
	})
}

func TestASkippedDownloadIsNotRetriedEveryMinute(t *testing.T) {
	r := newRig(t)
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	r.store.now = func() time.Time { return clock }

	src := r.skippable()
	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeSkipped {
		t.Fatalf("outcome = %q; the fixture should be a skip", res.Outcome)
	}

	clock = clock.Add(time.Minute)
	if again, err := r.store.ShouldAttempt(r.ctx, src.InfoHash); err != nil || again {
		t.Errorf("a minute after a skip: attempt=%v err=%v; want no attempt", again, err)
	}
	clock = clock.Add(SkipRetryInterval)
	if again, err := r.store.ShouldAttempt(r.ctx, src.InfoHash); err != nil || !again {
		t.Errorf("an hour after a skip: attempt=%v err=%v; a skip can stop being true, "+
			"so it must be looked at again", again, err)
	}
}

// Retrying a skip that has not changed moves the record's time; it does not add
// a row.
func TestRetryingAnUnchangedSkipDoesNotGrowTheRecord(t *testing.T) {
	r := newRig(t)
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	r.store.now = func() time.Time { return clock }

	src := r.skippable()
	for i := 0; i < 3; i++ {
		if _, err := r.imp.Import(r.ctx, src); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(2 * SkipRetryInterval)
	}
	recs, err := r.store.RecordsFor(r.ctx, src.InfoHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("%d records for three identical skips, want 1", len(recs))
	}
	if want := time.Date(2026, 9, 26, 16, 0, 0, 0, time.UTC); !recs[0].OccurredAt.Equal(want) {
		t.Errorf("the record says %v; it should carry the LATEST attempt, %v", recs[0].OccurredAt, want)
	}
}

// A different outcome is a new fact and gets its own row.
func TestADifferentOutcomeIsANewRecord(t *testing.T) {
	r := newRig(t)
	src := r.skippable()
	if _, err := r.imp.Import(r.ctx, src); err != nil {
		t.Fatal(err)
	}
	if err := r.store.RecordOutcome(r.ctx, Record{
		InfoHash: src.InfoHash, Outcome: OutcomeSkipped, Detail: "a different reason",
	}); err != nil {
		t.Fatal(err)
	}
	recs, err := r.store.RecordsFor(r.ctx, src.InfoHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Errorf("%d records; a changed reason must be kept, not folded into the old one", len(recs))
	}
}

// A FAILURE is retried at once: it is usually transient, and it is what an
// operator is waiting to see resolve.
func TestAFailedImportIsRetriedAtOnce(t *testing.T) {
	base := t.TempDir()
	database, err := db.Open(db.Options{Path: filepath.Join(base, "l.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := NewStore(database, nil)
	imp := New(store, library.NewRootStore(database, filepath.Join(base, "dl"), nil),
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	src := Source{InfoHash: hash(1), Dir: base, ReleaseTitle: "Film.2019.1080p.BluRay-GRP",
		Files: []Candidate{{"film.mkv", 20 * mib}}}
	if res, _ := imp.Import(adminCtx(), src); res.Outcome != OutcomeFailed {
		t.Fatalf("outcome = %q; with no root folder this should fail", res.Outcome)
	}
	if again, err := store.ShouldAttempt(adminCtx(), src.InfoHash); err != nil || !again {
		t.Errorf("attempt=%v err=%v; a failure must be retried on the next pass", again, err)
	}
}

// An imported download is never offered again, and one never tried is offered.
func TestImportedIsFinalAndUntriedIsOffered(t *testing.T) {
	r := newRig(t)
	if again, err := r.store.ShouldAttempt(r.ctx, hash(2)); err != nil || !again {
		t.Errorf("an untried download: attempt=%v err=%v", again, err)
	}
	src := r.download(hash(3), "Film.2019.1080p.BluRay.x264-GRP", map[string]int64{
		"Film.2019.1080p.BluRay.x264-GRP.mkv": 20 * mib,
	})
	if _, err := r.imp.Import(r.ctx, src); err != nil {
		t.Fatal(err)
	}
	if again, err := r.store.ShouldAttempt(r.ctx, src.InfoHash); err != nil || again {
		t.Errorf("an imported download: attempt=%v err=%v; it would be imported forever", again, err)
	}
}

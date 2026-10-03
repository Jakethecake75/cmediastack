package download

import (
	"strings"
	"testing"
	"time"
)

func seedRow(t *testing.T, s *Store, hash string, ratio float64, dur time.Duration) {
	t.Helper()
	if err := s.Put(t.Context(), Record{
		InfoHash: hash, Title: "x", Magnet: "magnet:?xt=urn:btih:" + hash,
		SeedRatio: ratio, SeedTime: dur,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(t.Context(), hash, StatusComplete); err != nil {
		t.Fatal(err)
	}
}

// The headline for seeding: the upload counter is per-process, so a policy
// judged against it alone forgives everything already uploaded on every
// restart. An instance that seeded back three times over would report near zero
// after a reboot and keep distributing — for weeks longer than the operator
// chose.
func TestSeedingTotalsSurviveARestartedUploadCounter(t *testing.T) {
	s, _ := testStore(t)
	h := strings.Repeat("a", 40)
	seedRow(t, s, h, 0, 0)

	// First run uploads 100, then 250 cumulative.
	if err := s.BumpSeeding(t.Context(), h, 100, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.BumpSeeding(t.Context(), h, 250, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(t.Context(), h)
	if got.Uploaded != 250 {
		t.Fatalf("uploaded = %d, want 250", got.Uploaded)
	}

	// The process restarts: the engine's counter is back to zero and climbs
	// again. The total must ADD to what came before, not reset to it.
	if err := s.BumpSeeding(t.Context(), h, 40, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(t.Context(), h)
	if got.Uploaded != 290 {
		t.Errorf("uploaded = %d, want 290: a restart forgave what was already uploaded", got.Uploaded)
	}
	if got.Seeded != 3*time.Minute {
		t.Errorf("seeded = %v, want 3m", got.Seeded)
	}
}

// Whichever threshold is reached first ends the obligation, which is how
// trackers state them.
func TestEitherThresholdEndsTheObligation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rec      Record
		transfer Transfer
		want     string
	}{
		{"ratio met", Record{SeedRatio: 2, Uploaded: 2000}, Transfer{Bytes: 1000}, ReasonRatioMet},
		{"ratio not met", Record{SeedRatio: 2, Uploaded: 1999}, Transfer{Bytes: 1000}, ""},
		{"time met", Record{SeedTime: time.Hour, Seeded: time.Hour}, Transfer{Bytes: 1000}, ReasonTimeMet},
		{"time not met", Record{SeedTime: time.Hour, Seeded: 59 * time.Minute}, Transfer{Bytes: 1000}, ""},
		{"ratio wins when both set", Record{SeedRatio: 1, SeedTime: time.Hour, Uploaded: 1000},
			Transfer{Bytes: 1000}, ReasonRatioMet},
	} {
		if got := seedingVerdict(tc.rec, tc.transfer, true); got != tc.want {
			t.Errorf("%s: verdict = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// An unknown requirement is not the same as no requirement. Stopping early is
// the failure that costs a private-tracker account, so a row with no recorded
// obligation seeds indefinitely rather than stopping at some invented default.
func TestNoRecordedObligationSeedsIndefinitely(t *testing.T) {
	r := Record{SeedRatio: 0, SeedTime: 0, Uploaded: 0, Seeded: 0}
	if got := seedingVerdict(r, Transfer{Bytes: 1000}, true); got != "" {
		t.Errorf("verdict = %q, want to keep seeding", got)
	}
	// Even after a great deal of uploading and time, with no threshold set.
	r.Uploaded, r.Seeded = 1<<40, 1000*time.Hour
	if got := seedingVerdict(r, Transfer{Bytes: 1000}, true); got != "" {
		t.Errorf("verdict = %q, want to keep seeding", got)
	}
}

// The operator's one switch. Turning seeding off must stop everything, whatever
// any indexer's obligation says: that decision is theirs, in writing, and it
// outranks a tracker's preference.
func TestDisablingSeedingStopsEverything(t *testing.T) {
	r := Record{SeedRatio: 10, SeedTime: 1000 * time.Hour}
	if got := seedingVerdict(r, Transfer{Bytes: 1000}, false); got != ReasonNoSeed {
		t.Errorf("verdict = %q, want %q", got, ReasonNoSeed)
	}
}

// A torrent whose size is unknown must keep seeding rather than have an
// obligation declared met on no evidence.
func TestAnUnknownSizeDoesNotDeclareTheRatioMet(t *testing.T) {
	r := Record{SeedRatio: 1, Uploaded: 999999}
	if got := seedingVerdict(r, Transfer{Bytes: 0}, true); got != "" {
		t.Errorf("verdict = %q: a ratio was declared met against an unknown size", got)
	}
}

// Once seeding is finished the row drops out of the seeding set, so the policy
// does not keep re-evaluating something it already stopped.
func TestAFinishedObligationLeavesTheSeedingSet(t *testing.T) {
	s, _ := testStore(t)
	h := strings.Repeat("b", 40)
	seedRow(t, s, h, 1, 0)

	rows, err := s.Seeding(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("seeding rows = %d, want 1", len(rows))
	}

	if err := s.FinishSeeding(t.Context(), h, ReasonRatioMet); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.Seeding(t.Context())
	if len(rows) != 0 {
		t.Errorf("a finished obligation is still in the seeding set: %+v", rows)
	}

	got, _ := s.Get(t.Context(), h)
	if got.SeedingDoneReason != ReasonRatioMet {
		t.Errorf("reason = %q", got.SeedingDoneReason)
	}
}

// Only completed transfers accrue an obligation. A download still in flight is
// not seeding.
func TestOnlyCompletedTransfersSeed(t *testing.T) {
	s, _ := testStore(t)
	h := strings.Repeat("c", 40)

	if err := s.Put(t.Context(), Record{
		InfoHash: h, Title: "x", Magnet: "m", Status: StatusDownloading,
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Seeding(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("an unfinished download is in the seeding set: %+v", rows)
	}
}

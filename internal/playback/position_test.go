package playback

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

func mins(n float64) time.Duration { return time.Duration(n * float64(time.Minute)) }

// The finished rule, checked at the three lengths it has to be right for. Both
// halves are needed: the cap is what saves long films, the percentage is what
// saves short ones.
func TestFinishedMeansNearEnoughTheEndAtEveryLength(t *testing.T) {
	for _, tc := range []struct {
		name     string
		position time.Duration
		duration time.Duration
		want     bool
		why      string
	}{
		// A 22-minute episode: finishes with about 66 seconds left.
		{"sitcom, mid-episode", mins(11), mins(22), false, ""},
		{"sitcom, 60s left", mins(21), mins(22), true, "the credits are rolling"},
		{"sitcom, 3 minutes left", mins(19), mins(22), false,
			"three minutes is the whole last act of a sitcom — a flat cap would " +
				"call this watched"},

		// A 45-minute drama: about 2¼ minutes.
		{"drama, 2 minutes left", mins(43), mins(45), true, ""},
		{"drama, 5 minutes left", mins(40), mins(45), false, ""},

		// A three-hour film: the cap binds, not the percentage.
		{"epic, 2 minutes left", mins(178), mins(180), true, ""},
		{"epic, 9 minutes left", mins(171), mins(180), false,
			"9 minutes is 5% of three hours; without the cap this would be " +
				"called watched with a reel to go"},
		{"epic, 15 minutes left", mins(165), mins(180), false, ""},

		// Edges.
		{"exactly at the end", mins(90), mins(90), true, ""},
		{"past the end", mins(95), mins(90), true, ""},
		{"at the very start", 0, mins(90), false, ""},
		{"unknown duration", mins(30), 0, false,
			"guessing finished with no duration would silently lose a place"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsFinished(tc.position, tc.duration); got != tc.want {
				t.Errorf("IsFinished(%s of %s) = %v, want %v%s",
					tc.position, tc.duration, got, tc.want,
					map[bool]string{true: " — " + tc.why, false: ""}[tc.why != ""])
			}
		})
	}
}

// Pressing play on something finished must start it again, not drop the viewer
// into the credits.
func TestAFinishedFilmStartsFromTheBeginning(t *testing.T) {
	p := Position{Position: mins(89), Duration: mins(90), Finished: true}
	at, resumed := p.ResumeAt(mins(90))
	if resumed || at != 0 {
		t.Errorf("ResumeAt = %s (resumed=%v), want 0 — pressing play on something "+
			"watched last week should not open the credits", at, resumed)
	}
}

// Forty seconds in is not a place worth returning to.
func TestBarelyStartedIsNotAResume(t *testing.T) {
	p := Position{Position: 40 * time.Second, Duration: mins(90)}
	if at, resumed := p.ResumeAt(mins(90)); resumed {
		t.Errorf("resumed at %s; below the floor a viewer pressed play and "+
			"changed their mind", at)
	}
	// And just over it is.
	p.Position = 90 * time.Second
	if at, resumed := p.ResumeAt(mins(90)); !resumed || at != 90*time.Second {
		t.Errorf("ResumeAt = %s (resumed=%v), want a resume at 1m30s", at, resumed)
	}
}

// The reason the duration is stored alongside the position: a file can be
// replaced, and 01:12:30 into the old one is not 01:12:30 into the new one.
func TestAPositionInADifferentCutIsNotTrusted(t *testing.T) {
	// Recorded against a 90-minute file.
	p := Position{Position: mins(72), Duration: mins(90)}

	// The same film, re-encoded, a few seconds shorter: still the same film.
	if at, resumed := p.ResumeAt(mins(90) - 8*time.Second); !resumed || at != mins(72) {
		t.Errorf("a few seconds of drift cost the viewer their place: %s %v", at, resumed)
	}

	// The extended cut. Twenty minutes longer is not the same film, and
	// 01:12:00 into it is a different scene.
	if at, resumed := p.ResumeAt(mins(110)); resumed {
		t.Errorf("resumed at %s into a file 20 minutes longer than the one the "+
			"position was recorded against", at)
	}

	// And a file that is now shorter than the recorded position.
	short := Position{Position: mins(72), Duration: mins(90)}
	if _, resumed := short.ResumeAt(mins(60)); resumed {
		t.Error("resumed past the end of the file as it is now")
	}
}

// An unprobed file has no duration, and that must not cost somebody their place.
func TestAnUnknownDurationDoesNotDiscardAPosition(t *testing.T) {
	p := Position{Position: mins(40), Duration: 0}
	if at, resumed := p.ResumeAt(0); !resumed || at != mins(40) {
		t.Errorf("ResumeAt = %s (resumed=%v); with nothing to contradict the "+
			"position, the lenient answer is the right one", at, resumed)
	}
}

// ---------------------------------------------------------------------------
// Storage
// ---------------------------------------------------------------------------

func (r *storeRig) positions() *Positions {
	return NewPositions(r.db, func() time.Time { return fixedNow })
}

// The shape of the table: per person, not per file.
func TestTwoPeopleKeepSeparatePlaces(t *testing.T) {
	r := newStoreRig(t)
	pos := r.positions()

	// A second account, so there is somebody to be confused with.
	stamp := fixedNow.Format(time.RFC3339Nano)
	if _, err := r.db.ExecContext(r.ctx, `
		INSERT INTO app_user (username, email, password_hash, state, role_id,
		                      rating_ceiling, created_at, updated_at)
		VALUES ('sam', 'sam@example.com', 'x', 'active', 1, 0, ?, ?)`,
		stamp, stamp); err != nil {
		t.Fatal(err)
	}

	jacob := r.asUser(1)
	sam := r.asUser(2)

	if _, err := pos.Save(jacob, r.fileID, mins(30), mins(90)); err != nil {
		t.Fatal(err)
	}
	if _, err := pos.Save(sam, r.fileID, mins(70), mins(90)); err != nil {
		t.Fatal(err)
	}

	got, err := pos.Get(jacob, r.fileID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Position != mins(30) {
		t.Errorf("jacob is at %s, want 30m — somebody else's viewing moved him",
			got.Position)
	}
	got, err = pos.Get(sam, r.fileID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Position != mins(70) {
		t.Errorf("sam is at %s, want 70m", got.Position)
	}
}

// The user comes from the context. There is no argument a handler could pass to
// write somebody else's history.
func TestAPositionIsRecordedForTheCallerAndNobodyElse(t *testing.T) {
	r := newStoreRig(t)
	pos := r.positions()

	saved, err := pos.Save(r.asUser(1), r.fileID, mins(10), mins(90))
	if err != nil {
		t.Fatal(err)
	}
	if saved.UserID != 1 {
		t.Errorf("recorded against user %d", saved.UserID)
	}

	// The other account sees nothing, because nothing was written for it.
	if _, err := pos.Get(r.asUser(2), r.fileID); !errors.Is(err, ErrNoPosition) {
		t.Errorf("err = %v, want ErrNoPosition", err)
	}
}

// Saving decides "finished" once and records it, rather than leaving every
// reader to recompute a rule that has two halves.
func TestSavingDecidesWhetherItWasFinished(t *testing.T) {
	r := newStoreRig(t)
	pos := r.positions()
	ctx := r.asUser(1)

	saved, err := pos.Save(ctx, r.fileID, mins(30), mins(90))
	if err != nil {
		t.Fatal(err)
	}
	if saved.Finished {
		t.Error("a third of the way in was recorded as finished")
	}

	saved, err = pos.Save(ctx, r.fileID, mins(89), mins(90))
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Finished {
		t.Error("one minute from the end was not recorded as finished")
	}
	back, err := pos.Get(ctx, r.fileID)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Finished {
		t.Error("the finished decision did not survive the round trip")
	}
}

// A player reporting a position past the end is reporting the end.
func TestAPositionPastTheEndIsClampedToIt(t *testing.T) {
	r := newStoreRig(t)
	saved, err := r.positions().Save(r.asUser(1), r.fileID, mins(95), mins(90))
	if err != nil {
		t.Fatal(err)
	}
	if saved.Position != mins(90) {
		t.Errorf("stored %s for a 90-minute file", saved.Position)
	}
}

// Somebody who watched something should be able to un-record it without
// waiting for the retention window.
func TestAPositionCanBeForgotten(t *testing.T) {
	r := newStoreRig(t)
	pos := r.positions()
	ctx := r.asUser(1)

	if _, err := pos.Save(ctx, r.fileID, mins(30), mins(90)); err != nil {
		t.Fatal(err)
	}
	if err := pos.Forget(ctx, r.fileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pos.Get(ctx, r.fileID); !errors.Is(err, ErrNoPosition) {
		t.Errorf("err = %v, want ErrNoPosition after forgetting", err)
	}
}

// Playback history is a record of what somebody watched and when, which the
// threat model lists as an asset. It is purged on a schedule rather than kept.
func TestOldPositionsArePurgedAndRecentOnesAreNot(t *testing.T) {
	r := newStoreRig(t)
	ctx := r.asUser(1)

	old := NewPositions(r.db, func() time.Time { return fixedNow.AddDate(-1, 0, 0) })
	if _, err := old.Save(ctx, r.fileID, mins(30), mins(90)); err != nil {
		t.Fatal(err)
	}

	pos := r.positions() // "now" is fixedNow
	n, err := pos.Purge(ctx, 180*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("purged %d rows, want 1", n)
	}
	if _, err := pos.Get(ctx, r.fileID); !errors.Is(err, ErrNoPosition) {
		t.Errorf("err = %v, want the year-old position to be gone", err)
	}

	// And something watched yesterday stays.
	recent := NewPositions(r.db, func() time.Time { return fixedNow.Add(-24 * time.Hour) })
	if _, err := recent.Save(ctx, r.fileID, mins(30), mins(90)); err != nil {
		t.Fatal(err)
	}
	if n, err = pos.Purge(ctx, 180*24*time.Hour); err != nil || n != 0 {
		t.Errorf("purged %d rows (err %v); yesterday is not 180 days ago", n, err)
	}
}

// A zero retention window means "unset", not "delete everything".
func TestPurgingWithNoWindowIsRefused(t *testing.T) {
	r := newStoreRig(t)
	ctx := r.asUser(1)
	if _, err := r.positions().Save(ctx, r.fileID, mins(30), mins(90)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.positions().Purge(ctx, 0); err == nil {
		t.Fatal("a zero retention window deleted history")
	}
	if _, err := r.positions().Get(ctx, r.fileID); err != nil {
		t.Errorf("the position was removed anyway: %v", err)
	}
}

// Watching is browsing, and browsing is a permission.
func TestPositionsNeedThePermissionToBrowse(t *testing.T) {
	r := newStoreRig(t)
	pos := r.positions()
	nobody := authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 3, Username: "nobody", State: authz.StateActive,
		MFASatisfied: true,
		Role:         authz.Role{ID: 9, Name: "Pending", Rank: 0},
	})

	if _, err := pos.Save(nobody, r.fileID, mins(10), mins(90)); err == nil {
		t.Error("a principal with no permissions recorded a position")
	}
	if _, err := pos.Get(nobody, r.fileID); err == nil {
		t.Error("a principal with no permissions read a position")
	}
}

// Deleting a file takes its positions with it, so nobody's history outlives the
// thing it was about.
func TestDeletingAFileForgetsEverybodysPlaceInIt(t *testing.T) {
	r := newStoreRig(t)
	if _, err := r.positions().Save(r.asUser(1), r.fileID, mins(30), mins(90)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(r.ctx,
		`DELETE FROM media_file WHERE id = ?`, r.fileID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := r.db.QueryRowContext(r.ctx,
		`SELECT COUNT(*) FROM playback_position`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d position(s) survived the file being deleted", n)
	}
}

package playback

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// Where somebody got to, and the two judgements that go with it: whether they
// finished, and whether a stored position is still worth returning to.

// Position is one person's place in one file.
type Position struct {
	UserID    int64
	FileID    int64
	Position  time.Duration
	Duration  time.Duration
	Finished  bool
	UpdatedAt time.Time
}

// The rules. Both are conservative in the direction that costs a viewer less:
// being told to start again is a minor annoyance, being dropped into the wrong
// scene or into the last two minutes is not.
const (
	// ResumeFloor is how far in a position has to be before it is worth
	// returning to. Below it, a viewer pressed play and changed their mind, and
	// resuming forty seconds into a film is more irritating than starting it.
	ResumeFloor = 60 * time.Second

	// FinishedFraction and FinishedCap decide "near enough the end".
	//
	// Both are needed, and each fixes what the other gets wrong. A percentage
	// alone is wrong for long films: 8% of three hours is fourteen minutes, so
	// stopping a quarter-hour before the end would mark it watched. A fixed
	// time alone is wrong for short ones: three minutes left in a
	// twenty-two-minute episode is the whole last act.
	//
	// Taking the SMALLER of the two gives: a 22-minute episode finishes with
	// ~66 seconds left, a 45-minute one with ~2¼ minutes, and a three-hour film
	// with 3 minutes — which is about where the credits start in each case.
	FinishedFraction = 0.05
	FinishedCap      = 3 * time.Minute

	// DurationDrift is how much a file's length may differ from the length
	// recorded with a position before the position is disbelieved. A re-encode
	// that trims a few frames is the same film; one that differs by more is
	// something else, and 01:12:30 into it is not the same scene.
	DurationDrift = 0.05
)

// IsFinished reports whether a position counts as having watched the thing.
func IsFinished(position, duration time.Duration) bool {
	if duration <= 0 {
		// Nothing to be near the end of. An unknown duration is the case where
		// guessing "finished" would silently lose somebody's place.
		return false
	}
	if position >= duration {
		return true
	}
	remaining := duration - position
	threshold := time.Duration(float64(duration) * FinishedFraction)
	if threshold > FinishedCap {
		threshold = FinishedCap
	}
	return remaining <= threshold
}

// ResumeAt reports where playback should actually start, and whether that is a
// resume rather than a fresh start.
//
// currentDuration is the file's length NOW, which is what makes a stale
// position detectable.
func (p Position) ResumeAt(currentDuration time.Duration) (time.Duration, bool) {
	switch {
	case p.Finished:
		// Finished means finished. Resuming at 99% would make pressing play on
		// something you watched last week drop you into the credits.
		return 0, false
	case p.Position < ResumeFloor:
		return 0, false
	case !p.describesDuration(currentDuration):
		// The file is not the length it was. The position is a number of
		// seconds into a file that no longer exists.
		return 0, false
	case currentDuration > 0 && p.Position >= currentDuration:
		// Past the end of the file as it is now.
		//
		// Guarded on currentDuration being KNOWN. Without the guard, an
		// unprobed file — duration zero — makes every position "past the end",
		// so the one case describesDuration deliberately treats leniently was
		// thrown away two lines later. Found by the test for exactly that case.
		return 0, false
	}
	return p.Position, true
}

// describesDuration reports whether this position was recorded against a file
// of about the current length.
func (p Position) describesDuration(current time.Duration) bool {
	if p.Duration <= 0 || current <= 0 {
		// One of them is unknown, so there is nothing to contradict. Trusting
		// the position here is the lenient choice and it is the right one: an
		// unprobed file should not cost somebody their place.
		return true
	}
	diff := p.Duration - current
	if diff < 0 {
		diff = -diff
	}
	return float64(diff) <= float64(current)*DurationDrift
}

// Positions stores playback positions.
type Positions struct {
	db  *db.DB
	now func() time.Time
}

// NewPositions builds the repository.
func NewPositions(database *db.DB, now func() time.Time) *Positions {
	if now == nil {
		now = time.Now
	}
	return &Positions{db: database, now: now}
}

// ErrNoPosition means this person has no recorded place in this file.
var ErrNoPosition = errors.New("playback: no recorded position")

// Save records where the caller got to.
//
// The USER comes from the context, never from the caller's arguments. A
// signature taking a user id would be one where a handler could write somebody
// else's history by passing a different number, and no amount of care at the
// call site is as good as not being able to express it.
func (p *Positions) Save(ctx context.Context, fileID int64,
	position, duration time.Duration) (Position, error) {

	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return Position{}, err
	}
	actor := authz.FromContext(ctx)
	if actor == nil || actor.UserID <= 0 {
		return Position{}, fmt.Errorf("playback: no principal to record a position for")
	}

	if position < 0 {
		position = 0
	}
	// A player that reports a position past the end is reporting a position at
	// the end. Storing the larger number would make the file look longer than
	// it is the next time the position is judged against it.
	if duration > 0 && position > duration {
		position = duration
	}

	rec := Position{
		UserID: actor.UserID, FileID: fileID,
		Position: position, Duration: duration,
		Finished:  IsFinished(position, duration),
		UpdatedAt: p.now().UTC(),
	}

	if _, err := p.db.ExecContext(ctx, `
		INSERT INTO playback_position
		  (user_id, media_file_id, position_ms, duration_ms, finished, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, media_file_id) DO UPDATE SET
		  position_ms = excluded.position_ms,
		  duration_ms = excluded.duration_ms,
		  finished    = excluded.finished,
		  updated_at  = excluded.updated_at`,
		rec.UserID, rec.FileID, rec.Position.Milliseconds(),
		rec.Duration.Milliseconds(), boolInt(rec.Finished),
		ts(rec.UpdatedAt)); err != nil {
		return Position{}, fmt.Errorf("playback: recording a position: %w", err)
	}
	return rec, nil
}

// Get returns the caller's place in a file.
func (p *Positions) Get(ctx context.Context, fileID int64) (Position, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return Position{}, err
	}
	actor := authz.FromContext(ctx)
	if actor == nil || actor.UserID <= 0 {
		return Position{}, ErrNoPosition
	}

	var (
		rec                    Position
		positionMS, durationMS int64
		finished               int
		updated                string
	)
	err := p.db.QueryRowContext(ctx, `
		SELECT position_ms, duration_ms, finished, updated_at
		  FROM playback_position
		 WHERE user_id = ? AND media_file_id = ?`,
		actor.UserID, fileID).Scan(&positionMS, &durationMS, &finished, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Position{}, ErrNoPosition
	}
	if err != nil {
		return Position{}, fmt.Errorf("playback: reading a position: %w", err)
	}

	rec.UserID = actor.UserID
	rec.FileID = fileID
	rec.Position = time.Duration(positionMS) * time.Millisecond
	rec.Duration = time.Duration(durationMS) * time.Millisecond
	rec.Finished = finished == 1
	rec.UpdatedAt = parseTS(updated)
	return rec, nil
}

// Forget removes the caller's place in a file.
//
// The counterpart to a history that is kept at all: somebody who watched
// something should be able to un-record that without waiting for the retention
// window.
func (p *Positions) Forget(ctx context.Context, fileID int64) error {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return err
	}
	actor := authz.FromContext(ctx)
	if actor == nil || actor.UserID <= 0 {
		return nil
	}
	if _, err := p.db.ExecContext(ctx,
		`DELETE FROM playback_position WHERE user_id = ? AND media_file_id = ?`,
		actor.UserID, fileID); err != nil {
		return fmt.Errorf("playback: forgetting a position: %w", err)
	}
	return nil
}

// Purge removes positions older than the retention window.
//
// Playback history is a record of what somebody watched and when, which
// docs/THREAT-MODEL.md lists as an asset in its own right. Keeping it forever
// is a choice nobody made; the retention window is configuration, and this is
// the task that honours it.
//
// Runs with the scheduler's system principal, so it takes no user.
func (p *Positions) Purge(ctx context.Context, olderThan time.Duration) (int64, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return 0, err
	}
	if olderThan <= 0 {
		// A zero window would delete everything, which is not what an unset
		// value means. The config lint has a default; this is the second line.
		return 0, fmt.Errorf("playback: refusing to purge with no retention window")
	}
	cutoff := p.now().UTC().Add(-olderThan)
	res, err := p.db.ExecContext(ctx,
		`DELETE FROM playback_position WHERE updated_at < ?`, ts(cutoff))
	if err != nil {
		return 0, fmt.Errorf("playback: purging positions: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

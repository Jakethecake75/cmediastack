package download

import (
	"context"
	"log/slog"
	"time"
)

// Stalled downloads (ADR-0034).
//
// A torrent nobody seeds never finishes, and a download that never finishes
// holds what it was grabbed for. Stalled is no verified progress for a
// threshold, counted only while this process's engine has been watching. A
// download automatic acquisition grabbed is given up — stopped, and left in the
// queue, which is the blocklist — because a machine undoing its own choice
// overrides nobody. A person's is marked and left running: theirs to remove.

// StallLabel is who the audit log says found a download stalled.
const StallLabel = "system:download"

// Stall is one download found stalled.
type Stall struct {
	Record Record
	// Since is when it last moved, or the engine started if later.
	Since time.Time
	// GivenUp is true when it was stopped.
	GivenUp bool
}

// stallVerdict says whether a transfer has stalled, and since when it has not
// moved. Only a queued or downloading transfer can stall; after at zero turns
// the judgement off.
func stallVerdict(r Record, now, started time.Time, after time.Duration) (bool, time.Time) {
	if after <= 0 || (r.Status != StatusQueued && r.Status != StatusDownloading) {
		return false, time.Time{}
	}
	since := r.ProgressedAt
	if since.IsZero() {
		since = r.AddedAt
	}
	if started.After(since) {
		since = started
	}
	return now.Sub(since) >= after, since
}

// SyncStalls records what the engine's transfers have verified and finds the
// ones that have not moved for after. giveUp says which to stop.
func (m *Manager) SyncStalls(ctx context.Context, after time.Duration,
	giveUp func(Record) bool) ([]Stall, error) {

	if m.store == nil || after <= 0 {
		return nil, nil
	}
	return m.judgeStalls(ctx, m.engine.List(), m.engine.Remove, after, giveUp)
}

// judgeStalls is SyncStalls over a given view of the engine, so the judgement
// can be tested without one.
func (m *Manager) judgeStalls(ctx context.Context, transfers []Transfer, stop func(string) error,
	after time.Duration, giveUp func(Record) bool) ([]Stall, error) {

	rows, err := m.store.Resumable(ctx)
	if err != nil {
		return nil, err
	}
	byHash := make(map[string]Transfer, len(transfers))
	for _, t := range transfers {
		byHash[t.InfoHash] = t
	}
	now := m.now()

	var out []Stall
	for _, r := range rows {
		t, running := byHash[r.InfoHash]
		if !running || t.Done {
			// Not in the engine — it could not be added, and says so
			// elsewhere — or finished, which the completion task records.
			continue
		}
		if t.Completed > r.ProgressBytes {
			if err := m.store.NoteProgress(ctx, r.InfoHash, t.Completed); err != nil {
				return out, err
			}
			continue
		}
		// A stall already found is not reported again: MarkStalled marks a
		// row once, and says whether this was the time.
		stalled, since := stallVerdict(r, now, m.started, after)
		if !stalled {
			continue
		}
		give := giveUp != nil && giveUp(r)
		if give {
			if err := stop(r.InfoHash); err != nil {
				m.log.Error("a stalled download could not be stopped",
					slog.String("info_hash", r.InfoHash), slog.String("error", err.Error()))
				continue
			}
		}
		marked, err := m.store.MarkStalled(ctx, r.InfoHash, give)
		if err != nil {
			return out, err
		}
		if marked {
			out = append(out, Stall{Record: r, Since: since, GivenUp: give})
		}
	}
	return out, nil
}

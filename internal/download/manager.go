package download

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// Manager is the engine plus its persistence.
//
// It exists so that "add a transfer" and "record that we added a transfer"
// cannot come apart. If the API held an Engine and a Store separately, every
// new call site would have to remember to write the row, and the one that
// forgot would produce a transfer that silently vanishes on the next restart
// with its bytes still on disk. Here there is one method and it does both.
//
// The Engine remains database-free and independently testable; the Manager is
// the only thing that knows both halves exist.
type Manager struct {
	engine *Engine
	store  *Store
	log    *slog.Logger
	// started is when this process's engine began watching; a stall is
	// counted from no earlier (ADR-0034).
	started time.Time
	now     func() time.Time
}

// NewManager binds an engine to a store. A nil store yields a manager that
// runs transfers without remembering them, which is what a test wants and what
// production must never have — main wires a real store.
func NewManager(e *Engine, s *Store, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{engine: e, store: s, log: log, started: time.Now(), now: time.Now}
}

// persistTimeout bounds a queue write.
const persistTimeout = 5 * time.Second

// detached returns a context for persistence that does NOT inherit the
// caller's cancellation.
//
// This is deliberate and it is not a workaround. By the time the row is
// written the transfer is already running in the engine: if the operator's
// browser gave up a moment ago, cancelling the write would leave bytes
// arriving on disk with nothing recording why. The write must outlive the
// request that caused it.
func detached() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(context.Background()), persistTimeout)
}

// Meta is what the grab path knows about a transfer that the engine does not.
type Meta struct {
	Title       string
	IndexerID   int64
	IndexerName string
	AddedBy     *int64
	AddedLabel  string
	SeedRatio   float64
	SeedTime    time.Duration
	// Target is what the grab was FOR, when it came from an episode search.
	Target *Target
}

// AddMagnet starts a transfer from a magnet URI and records it.
func (m *Manager) AddMagnet(ctx context.Context, magnet string, meta Meta) (Transfer, error) {
	t, err := m.engine.Add(ctx, magnet)
	if err != nil {
		return Transfer{}, err
	}
	m.record(Record{
		InfoHash: t.InfoHash, Magnet: magnet, Status: StatusDownloading,
		Title:     titleOr(meta.Title, t.Name, t.InfoHash),
		IndexerID: meta.IndexerID, IndexerName: meta.IndexerName,
		AddedBy: meta.AddedBy, AddedLabel: meta.AddedLabel,
		SeedRatio: meta.SeedRatio, SeedTime: meta.SeedTime,
		Target: meta.Target,
	})
	return t, nil
}

// AddTorrent starts a transfer from .torrent bytes and records them.
func (m *Manager) AddTorrent(data []byte, meta Meta) (Transfer, error) {
	t, err := m.engine.AddTorrentBytes(data)
	if err != nil {
		return Transfer{}, err
	}
	m.record(Record{
		InfoHash: t.InfoHash, Torrent: data, Status: StatusDownloading,
		Title:     titleOr(meta.Title, t.Name, t.InfoHash),
		IndexerID: meta.IndexerID, IndexerName: meta.IndexerName,
		AddedBy: meta.AddedBy, AddedLabel: meta.AddedLabel,
		SeedRatio: meta.SeedRatio, SeedTime: meta.SeedTime,
		Target: meta.Target,
	})
	return t, nil
}

// record writes a queue row, logging rather than failing the transfer.
//
// The transfer is already running. Refusing the grab because a row could not be
// written would stop nothing — the bytes are arriving — and would tell the
// operator the grab failed when it did not. So the failure is loud in the log
// and the caller is told the truth about what happened.
func (m *Manager) record(r Record) {
	if m.store == nil {
		return
	}
	ctx, cancel := detached()
	defer cancel()

	if err := m.store.Put(ctx, r); err != nil {
		m.log.Error("the download queue row could not be written: this transfer "+
			"will not survive a restart",
			slog.String("info_hash", r.InfoHash),
			slog.String("title", r.Title),
			slog.String("error", err.Error()))
	}
}

// titleOr picks the best name available, in order of how much the operator
// will recognise it: what the indexer published, then the torrent's own
// declared name, then the info hash. A magnet that has not resolved has
// neither of the first two, and a row titled with a hash is still better than
// a row titled with nothing.
func titleOr(indexerTitle, torrentName, hash string) string {
	if indexerTitle != "" {
		return indexerTitle
	}
	if torrentName != "" {
		return torrentName
	}
	return hash
}

// Restore re-adds every unfinished transfer to the engine.
//
// It replays from the payloads stored locally and contacts no indexer: a
// tracker that is down, rate-limiting, or deleted must not cost an operator
// their queue. Rows that fail to re-add are logged and skipped rather than
// aborting the restore, because one corrupt row must not keep the rest of the
// queue from resuming.
func (m *Manager) Restore(ctx context.Context) (int, error) {
	if m.store == nil {
		return 0, nil
	}
	rows, err := m.store.Resumable(ctx)
	if err != nil {
		return 0, err
	}

	restored := 0
	for _, r := range rows {
		var rerr error
		if r.Magnet != "" {
			_, rerr = m.engine.Add(ctx, r.Magnet)
		} else {
			_, rerr = m.engine.AddTorrentBytes(r.Torrent)
		}
		if rerr != nil {
			m.log.Error("a queued transfer could not be restored",
				slog.String("info_hash", r.InfoHash),
				slog.String("title", r.Title),
				slog.String("error", rerr.Error()))
			continue
		}
		// Best effort: metadata may not have arrived for a magnet yet, in which
		// case there is nothing to start until it does.
		_ = m.engine.Start(r.InfoHash)
		restored++
	}
	return restored, nil
}

// ---------------------------------------------------------------------------
// The API's DownloadEngine surface
// ---------------------------------------------------------------------------

// Add satisfies the API's engine interface for a magnet with no grab metadata.
// The grab path uses AddMagnet instead, which records who asked and what for.
func (m *Manager) Add(ctx context.Context, magnetOrHash string) (Transfer, error) {
	return m.AddMagnet(ctx, magnetOrHash, Meta{})
}

// AddTorrentBytes satisfies the API's engine interface.
func (m *Manager) AddTorrentBytes(data []byte) (Transfer, error) {
	return m.AddTorrent(data, Meta{})
}

func (m *Manager) Start(hash string) error { return m.engine.Start(hash) }

// FilesOf lists what a transfer contains.
func (m *Manager) FilesOf(hash string) ([]TransferFile, error) { return m.engine.FilesOf(hash) }

// FilesOnDisk lists what a transfer left on disk; see Engine.FilesOnDisk.
func (m *Manager) FilesOnDisk(hash string) ([]TransferFile, error) { return m.engine.FilesOnDisk(hash) }

// DataPathFor is where a transfer's files live on disk.
func (m *Manager) DataPathFor(hash string) (string, error) { return m.engine.DataPathFor(hash) }
func (m *Manager) List() []Transfer                        { return m.engine.List() }
func (m *Manager) Notes() []string                         { return m.engine.Notes() }

// Remove stops a transfer, deletes its folder in the download directory and
// marks the row stopped (ADR-0070). The folder is the download's own copy: an
// import hard-links or copies into the library, so an imported title keeps its
// file.
//
// The row is kept rather than deleted: the queue is automatic acquisition's
// blocklist, and a removed release must not be grabbed again by itself. A row
// the engine is not running, a finished download, is removed the same way.
func (m *Manager) Remove(hash string) error {
	if err := m.engine.Remove(hash); err != nil {
		if !errors.Is(err, ErrNotFound) || !m.recorded(hash) {
			return err
		}
	}
	path, err := m.engine.DataPathFor(hash)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("download: deleting the files of %s: %w", hash, err)
	}
	if m.store != nil {
		ctx, cancel := detached()
		defer cancel()
		if err := m.store.SetStatus(ctx, hash, StatusStopped); err != nil &&
			!errors.Is(err, ErrRowNotFound) {
			m.log.Error("a removed transfer could not be marked stopped: it will "+
				"be restarted on the next restart",
				slog.String("info_hash", hash), slog.String("error", err.Error()))
		}
	}
	return nil
}

// recorded reports whether the queue holds a row for hash.
func (m *Manager) recorded(hash string) bool {
	if m.store == nil {
		return false
	}
	ctx, cancel := detached()
	defer cancel()
	_, err := m.store.Get(ctx, hash)
	return err == nil
}

// Close shuts the engine down.
func (m *Manager) Close() error { return m.engine.Close() }

// Connections reports the engine's outgoing peer connections (ADR-0066).
func (m *Manager) Connections() ConnStats { return m.engine.Connections() }

// ListenPort reports the engine's port.
func (m *Manager) ListenPort() int { return m.engine.ListenPort() }

// Records returns the persisted queue, which carries what the engine does not
// know: the release name the indexer published, who grabbed it, and when.
func (m *Manager) Records(ctx context.Context) ([]Record, error) {
	if m.store == nil {
		return nil, nil
	}
	return m.store.List(ctx)
}

// SyncCompletion marks finished transfers complete.
//
// Called from the scheduler rather than from a callback inside the torrent
// library: a poll that reads the engine's own view is simple, has no ordering
// hazards, and cannot wedge the library's event loop if the database is slow.
// The cost is that "complete" is recorded within one tick rather than
// instantly, which nothing downstream depends on.
func (m *Manager) SyncCompletion(ctx context.Context) (int, error) {
	if m.store == nil {
		return 0, nil
	}
	rows, err := m.store.Resumable(ctx)
	if err != nil {
		return 0, err
	}
	byHash := make(map[string]Transfer, len(rows))
	for _, t := range m.engine.List() {
		byHash[t.InfoHash] = t
	}

	done := 0
	for _, r := range rows {
		t, ok := byHash[r.InfoHash]
		if !ok || !t.Done {
			continue
		}
		if err := m.store.SetStatus(ctx, r.InfoHash, StatusComplete); err != nil {
			m.log.Error("a completed transfer could not be marked complete",
				slog.String("info_hash", r.InfoHash), slog.String("error", err.Error()))
			continue
		}
		m.engine.Seal(r.InfoHash)
		m.log.Info("a download finished",
			slog.String("title", r.Title), slog.String("info_hash", r.InfoHash))
		done++
	}
	return done, nil
}

// ---------------------------------------------------------------------------
// Seeding obligations
// ---------------------------------------------------------------------------

// Reasons a transfer stopped seeding, recorded on the row.
const (
	ReasonRatioMet = "ratio met"
	ReasonTimeMet  = "seed time met"
	ReasonNoSeed   = "seeding is disabled in the configuration"
)

// SyncSeeding advances seeding obligations and stops the ones that are met.
//
// # Why this is tracked in the database rather than read off the engine
//
// The torrent client's upload counter is per-process. Judged against it alone,
// every restart silently forgives everything already uploaded — so an instance
// that has genuinely seeded back three times over reports a ratio near zero
// after a reboot, keeps seeding, and an operator who thought they had met an
// obligation has instead been distributing for weeks longer than they chose.
// The durable totals are the ones the policy reads.
//
// # The default, stated plainly
//
// When an indexer records NO obligation — both seed_ratio and seed_secs zero —
// this seeds indefinitely, exactly as long as download.seed says to. It does
// not stop at some invented threshold. The reasoning: a private tracker's
// requirement is the whole reason seeding matters to an operator, an unknown
// requirement is not the same as no requirement, and stopping early is the
// failure that costs an account. An operator who does not want to distribute at
// all sets download.seed to false, which is a decision they make once, in
// writing, rather than one this code makes for them by guessing.
//
// Seeding is distribution. That is worth saying out loud in the place where the
// policy lives: it is the part of this software that sends content to
// strangers, and §13 puts the legality of that on the operator.
func (m *Manager) SyncSeeding(ctx context.Context, tick time.Duration, seedingEnabled bool) (int, error) {
	if m.store == nil {
		return 0, nil
	}
	rows, err := m.store.Seeding(ctx)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}

	byHash := make(map[string]Transfer)
	for _, t := range m.engine.List() {
		byHash[t.InfoHash] = t
	}

	stopped := 0
	for _, r := range rows {
		t, running := byHash[r.InfoHash]
		if !running {
			// Not in the engine, so it is not seeding and cannot accrue. Left
			// alone rather than marked done: it may be restored later, and
			// claiming an obligation was met when nothing was uploaded would be
			// the one lie this record must not tell.
			continue
		}

		if err := m.store.BumpSeeding(ctx, r.InfoHash, t.Uploaded, tick); err != nil {
			m.log.Error("seeding progress could not be recorded",
				slog.String("info_hash", r.InfoHash), slog.String("error", err.Error()))
			continue
		}

		// Re-read the freshly bumped totals rather than recomputing them here:
		// the delta logic that handles a restarted counter lives in one place,
		// in SQL, and a second copy of it in Go would be the copy that drifts.
		updated, err := m.store.Get(ctx, r.InfoHash)
		if err != nil {
			continue
		}

		reason := seedingVerdict(updated, t, seedingEnabled)
		if reason == "" {
			continue
		}

		// Stop seeding by removing the torrent from the client. The files stay
		// on disk: they are the library's copy, and unlinking them is an effect
		// with its own permission that this path does not hold.
		if err := m.engine.Remove(r.InfoHash); err != nil && !errors.Is(err, ErrNotFound) {
			m.log.Error("a seeded transfer could not be stopped",
				slog.String("info_hash", r.InfoHash), slog.String("error", err.Error()))
			continue
		}
		if err := m.store.FinishSeeding(ctx, r.InfoHash, reason); err != nil {
			m.log.Error("the end of seeding could not be recorded",
				slog.String("info_hash", r.InfoHash), slog.String("error", err.Error()))
			continue
		}
		m.log.Info("stopped seeding",
			slog.String("title", updated.Title),
			slog.String("reason", reason),
			slog.Float64("ratio", ratioOf(updated, t)),
			slog.Duration("seeded", updated.Seeded))
		stopped++
	}
	return stopped, nil
}

// seedingVerdict reports why seeding should stop, or "" to keep going.
//
// An obligation of zero is NOT treated as "stop now" — see SyncSeeding for the
// reasoning. Both thresholds are honoured independently: whichever is reached
// first ends the obligation, which is how trackers state them.
func seedingVerdict(r Record, t Transfer, seedingEnabled bool) string {
	if !seedingEnabled {
		return ReasonNoSeed
	}
	if r.SeedRatio > 0 && ratioOf(r, t) >= r.SeedRatio {
		return ReasonRatioMet
	}
	if r.SeedTime > 0 && r.Seeded >= r.SeedTime {
		return ReasonTimeMet
	}
	return ""
}

// ratioOf computes upload ratio from the DURABLE total against the torrent's
// size. A torrent whose size is not yet known reports 0 rather than dividing by
// zero — and reporting 0 is the safe direction, because it keeps seeding rather
// than declaring an obligation met on no evidence.
func ratioOf(r Record, t Transfer) float64 {
	if t.Bytes <= 0 {
		return 0
	}
	return float64(r.Uploaded) / float64(t.Bytes)
}

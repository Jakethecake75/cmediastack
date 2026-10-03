package download

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// Status values a queue row may hold. They match the CHECK constraint in
// migration 0005; a value not listed here will be refused by the database
// rather than stored and puzzled over later.
const (
	StatusQueued      = "queued"
	StatusDownloading = "downloading"
	StatusComplete    = "complete"
	StatusStopped     = "stopped"
)

// ErrRowNotFound is returned when a queue row does not exist.
var ErrRowNotFound = errors.New("download: no such queue row")

// Record is one persisted transfer.
//
// It carries the payload verbatim — the magnet URI or the .torrent bytes — so
// a restart replays the queue from local state alone. The indexer may be down,
// rate-limiting, or deleted, and none of that should cost an operator the
// transfers they already started.
//
// It deliberately has no DownloadURL field. On many trackers that URL carries
// the indexer's API key in its query string, and the bytes it would fetch are
// already here, so keeping it would be storing a credential for no remaining
// purpose.
type Record struct {
	InfoHash    string
	Title       string
	IndexerID   int64
	IndexerName string
	Magnet      string
	Torrent     []byte
	AddedBy     *int64
	AddedLabel  string
	Status      string
	AddedAt     time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
	SeedRatio   float64
	SeedTime    time.Duration

	// Cumulative seeding progress, durable across restarts. See SeedProgress.
	Uploaded          int64
	Seeded            time.Duration
	UploadedMark      int64
	SeedingDoneReason string

	// Target is what the transfer was grabbed FOR. Nil for a grab from the
	// general search.
	Target *Target

	// What it had verified when it last moved, and when (ADR-0034). A zero
	// ProgressedAt means it has not moved since it was added.
	ProgressBytes int64
	ProgressedAt  time.Time
	// StalledAt is when it was found stalled; zero when it is not.
	StalledAt time.Time
}

// Target is what a targeted search matched a release to — one episode of one
// series (ADR-0023), or one film (ADR-0026) — sealed into its grab ticket and
// carried here, so the importer can file the download under that item rather
// than guessing one from the release name.
type Target struct {
	ItemID  int64
	Season  int
	Episode int
	// Film marks a whole film: the item, and no season or episode.
	Film bool
	// Pack marks a whole season: the series and the season, and no episode
	// (ADR-0033).
	Pack bool
	// LastSeason is a pack's last season when it holds several (ADR-0057);
	// zero for one season.
	LastSeason int
	// Album marks one album of the artist that is the item, and no season or
	// episode (ADR-0046).
	Album int64
	// Book marks a book: the item and nothing else (ADR-0049).
	Book bool
}

// Target kinds, as the queue row's target_kind column spells them.
//
// Not named targetEpisode and targetFilm: Put has local variables for each
// target column, and a constant of the same name is shadowed by one of them —
// the first version of this stored every episode's kind as NULL that way.
const (
	kindEpisode = "episode"
	kindFilm    = "film"
	kindSeason  = "season"
	kindAlbum   = "album"
	kindBook    = "book"
)

// valid reports whether a target names something, in exactly one of its two
// shapes. An episode is all three numbers or nothing: a target missing its
// episode would import into the series and then refuse every file as "not the
// episode it was grabbed for". A film is its item and nothing else: a film
// with an episode number is neither, and the import could not tell which was
// meant.
//
// A season is its series and season number and no episode: the import files
// each file of the download under the episode its own name says (ADR-0033).
func (t Target) valid() bool {
	switch {
	case t.ItemID <= 0 || (t.Film && t.Pack) || t.Album < 0:
		return false
	case t.LastSeason != 0 && (!t.Pack || t.LastSeason <= t.Season):
		return false
	case t.Book:
		return !t.Film && !t.Pack && t.Album == 0 && t.Season == 0 && t.Episode == 0
	case t.Album > 0:
		return !t.Film && !t.Pack && t.Season == 0 && t.Episode == 0
	case t.Film:
		return t.Season == 0 && t.Episode == 0
	case t.Pack:
		return t.Season >= 0 && t.Episode == 0
	}
	return t.Season >= 0 && t.Episode > 0
}

// Store persists the download queue.
type Store struct {
	db  *db.DB
	now func() time.Time
}

// NewStore builds a queue store.
func NewStore(database *db.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: database, now: now}
}

const timeLayout = time.RFC3339Nano

// Put records a transfer, or updates the one already recorded for that hash.
//
// Re-grabbing a release already in the queue is an ordinary thing to do — an
// operator retrying something that stalled — and it must not fail or duplicate.
// The payload is refreshed while added_at and added_by are kept, so the row
// still says who first asked and when.
func (s *Store) Put(ctx context.Context, r Record) error {
	if !IsInfoHash(r.InfoHash) {
		return fmt.Errorf("download: %q is not an info hash", r.InfoHash)
	}
	if (r.Magnet == "") == (len(r.Torrent) == 0) {
		// The database enforces this too. Catching it here names the caller's
		// mistake instead of surfacing a constraint violation.
		return errors.New("download: a queue row needs exactly one of a magnet or torrent bytes")
	}
	if r.Status == "" {
		r.Status = StatusQueued
	}
	// The three shapes migration 0015 lists, and nothing else.
	var targetKind, targetItem, targetSeason, targetEpisode, targetAlbum, targetLast any
	switch {
	case r.Target == nil:
	case !r.Target.valid():
		return fmt.Errorf("download: %+v is not a target", *r.Target)
	case r.Target.Album > 0:
		targetKind, targetItem, targetAlbum = kindAlbum, r.Target.ItemID, r.Target.Album
	case r.Target.Book:
		targetKind, targetItem = kindBook, r.Target.ItemID
	case r.Target.Film:
		targetKind, targetItem = kindFilm, r.Target.ItemID
	case r.Target.Pack:
		targetKind, targetItem, targetSeason = kindSeason, r.Target.ItemID, r.Target.Season
		if r.Target.LastSeason > 0 {
			targetLast = r.Target.LastSeason
		}
	default:
		targetKind, targetItem = kindEpisode, r.Target.ItemID
		targetSeason, targetEpisode = r.Target.Season, r.Target.Episode
	}
	now := s.now().UTC()

	var magnet, torrent any
	if r.Magnet != "" {
		magnet = r.Magnet
	} else {
		torrent = r.Torrent
	}

	// A re-grab WITH a target replaces the whole target, kind and all; one
	// WITHOUT keeps it. The same release grabbed again from the general search
	// is not a statement that it is no longer for what it was grabbed for.
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO download_queue (
			info_hash, title, indexer_id, indexer_name, magnet, torrent,
			added_by, added_label, status, added_at, updated_at,
			seed_ratio, seed_secs, target_kind, target_item_id, target_season, target_episode,
			target_album_id, target_last_season)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(info_hash) DO UPDATE SET
			title        = excluded.title,
			indexer_id   = excluded.indexer_id,
			indexer_name = excluded.indexer_name,
			magnet       = excluded.magnet,
			torrent      = excluded.torrent,
			status       = excluded.status,
			updated_at   = excluded.updated_at,
			seed_ratio   = excluded.seed_ratio,
			seed_secs    = excluded.seed_secs,
			target_kind    = CASE WHEN excluded.target_item_id IS NULL
			                      THEN download_queue.target_kind ELSE excluded.target_kind END,
			target_item_id = COALESCE(excluded.target_item_id, download_queue.target_item_id),
			target_season  = CASE WHEN excluded.target_item_id IS NULL
			                      THEN download_queue.target_season ELSE excluded.target_season END,
			target_episode = CASE WHEN excluded.target_item_id IS NULL
			                      THEN download_queue.target_episode ELSE excluded.target_episode END,
			target_album_id = CASE WHEN excluded.target_item_id IS NULL
			                      THEN download_queue.target_album_id ELSE excluded.target_album_id END,
			target_last_season = CASE WHEN excluded.target_item_id IS NULL
			                      THEN download_queue.target_last_season ELSE excluded.target_last_season END`,
		r.InfoHash, r.Title, nullInt(r.IndexerID), r.IndexerName, magnet, torrent,
		r.AddedBy, r.AddedLabel, r.Status, now.Format(timeLayout), now.Format(timeLayout),
		r.SeedRatio, int64(r.SeedTime/time.Second), targetKind, targetItem, targetSeason, targetEpisode, targetAlbum, targetLast)
	if err != nil {
		return fmt.Errorf("download: recording the queue row: %w", err)
	}
	return nil
}

// SetStatus moves a row's status along.
func (s *Store) SetStatus(ctx context.Context, hash, status string) error {
	if !IsInfoHash(hash) {
		return fmt.Errorf("download: %q is not an info hash", hash)
	}
	now := s.now().UTC()

	var completed any
	if status == StatusComplete {
		completed = now.Format(timeLayout)
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE download_queue
		SET status = ?, updated_at = ?,
		    completed_at = COALESCE(?, completed_at)
		WHERE info_hash = ?`,
		status, now.Format(timeLayout), completed, hash)
	if err != nil {
		return fmt.Errorf("download: updating the queue row: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRowNotFound
	}
	return nil
}

// Delete forgets a transfer. The files on disk are untouched: unlinking bytes
// is a separate effect with a separate permission, and a store must not be the
// thing that quietly performs it.
func (s *Store) Delete(ctx context.Context, hash string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM download_queue WHERE info_hash = ?`, hash)
	if err != nil {
		return fmt.Errorf("download: deleting the queue row: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRowNotFound
	}
	return nil
}

// Get returns one row.
func (s *Store) Get(ctx context.Context, hash string) (Record, error) {
	rows, err := s.query(ctx, `WHERE info_hash = ?`, hash)
	if err != nil {
		return Record{}, err
	}
	if len(rows) == 0 {
		return Record{}, ErrRowNotFound
	}
	return rows[0], nil
}

// List returns every row, oldest first.
func (s *Store) List(ctx context.Context) ([]Record, error) {
	return s.query(ctx, `ORDER BY added_at ASC`)
}

// Resumable returns the rows a restart should re-add to the engine.
//
// Stopped rows are excluded: an operator who stopped a transfer would be
// entitled to be angry if restarting the service started it again. Complete
// rows are excluded because there is nothing left to fetch — though they stay
// in the table, because "what has this instance acquired" is a question the
// operator is answerable for and deleting the answer on completion is not this
// layer's decision to make.
func (s *Store) Resumable(ctx context.Context) ([]Record, error) {
	return s.query(ctx, `WHERE status IN (?, ?) ORDER BY added_at ASC`,
		StatusQueued, StatusDownloading)
}

func (s *Store) query(ctx context.Context, where string, args ...any) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT info_hash, title, COALESCE(indexer_id, 0), indexer_name,
		       COALESCE(magnet, ''), torrent, added_by, added_label, status,
		       added_at, updated_at, completed_at, seed_ratio, seed_secs,
		       uploaded_bytes, seeded_secs, uploaded_mark, seeding_done_reason,
		       target_kind, target_item_id, target_season, target_episode, target_album_id,
		       target_last_season, progress_bytes, progressed_at, stalled_at
		FROM download_queue `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("download: reading the queue: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Record, 0)
	for rows.Next() {
		var r Record
		var added, updated string
		var completed sql.NullString
		var seedSecs, seededSecs int64
		var tKind sql.NullString
		var tItem, tSeason, tEpisode, tAlbum, tLast sql.NullInt64
		var progressed, stalled sql.NullString
		if err := rows.Scan(&r.InfoHash, &r.Title, &r.IndexerID, &r.IndexerName,
			&r.Magnet, &r.Torrent, &r.AddedBy, &r.AddedLabel, &r.Status,
			&added, &updated, &completed, &r.SeedRatio, &seedSecs,
			&r.Uploaded, &seededSecs, &r.UploadedMark, &r.SeedingDoneReason,
			&tKind, &tItem, &tSeason, &tEpisode, &tAlbum, &tLast,
			&r.ProgressBytes, &progressed, &stalled); err != nil {
			return nil, fmt.Errorf("download: reading the queue: %w", err)
		}
		r.Target = targetOf(tKind, tItem, tSeason, tEpisode, tAlbum, tLast)
		if progressed.Valid {
			r.ProgressedAt, _ = time.Parse(timeLayout, progressed.String)
		}
		if stalled.Valid {
			r.StalledAt, _ = time.Parse(timeLayout, stalled.String)
		}
		r.Seeded = time.Duration(seededSecs) * time.Second
		r.AddedAt, _ = time.Parse(timeLayout, added)
		r.UpdatedAt, _ = time.Parse(timeLayout, updated)
		if completed.Valid {
			if t, perr := time.Parse(timeLayout, completed.String); perr == nil {
				r.CompletedAt = &t
			}
		}
		r.SeedTime = time.Duration(seedSecs) * time.Second
		out = append(out, r)
	}
	return out, rows.Err()
}

// targetOf reads a row's target back in one of its two shapes, or none.
//
// A row whose columns fit neither shape has no target, rather than whichever
// half of one could be salvaged: an import told "this is for item 12" with no
// way to know whether that is a film or which episode would file the download
// on a guess, and guessing is what a target exists to stop. Put never writes
// such a row; this is the read side refusing to trust that.
//
// An episode is its three numbers under the kind "episode" — or under no kind
// at all, which is how every targeted row was written before migration 0015.
// The migration marks those rows, so a NULL kind with an item is not expected;
// read, it is the episode it always was.
func targetOf(kind sql.NullString, item, season, episode, album, last sql.NullInt64) *Target {
	if !item.Valid || item.Int64 <= 0 {
		return nil
	}
	if last.Valid && (!kind.Valid || kind.String != kindSeason) {
		// Several seasons under another kind (ADR-0057).
		return nil
	}
	if album.Valid != (kind.Valid && kind.String == kindAlbum) {
		// An album id under another kind, or an album kind without one.
		return nil
	}
	switch {
	case album.Valid:
		if season.Valid || episode.Valid || album.Int64 <= 0 {
			return nil
		}
		return &Target{ItemID: item.Int64, Album: album.Int64}
	case kind.Valid && kind.String == kindBook:
		if season.Valid || episode.Valid {
			return nil
		}
		return &Target{ItemID: item.Int64, Book: true}
	case kind.Valid && kind.String == kindFilm:
		if season.Valid || episode.Valid {
			return nil
		}
		return &Target{ItemID: item.Int64, Film: true}
	case kind.Valid && kind.String == kindSeason:
		if !season.Valid || episode.Valid {
			return nil
		}
		t := Target{ItemID: item.Int64, Season: int(season.Int64), Pack: true, LastSeason: int(last.Int64)}
		if !t.valid() {
			return nil
		}
		return &t
	case !kind.Valid || kind.String == kindEpisode:
		if !season.Valid || !episode.Valid {
			return nil
		}
		t := Target{ItemID: item.Int64, Season: int(season.Int64), Episode: int(episode.Int64)}
		if !t.valid() {
			return nil
		}
		return &t
	}
	return nil
}

// nullInt keeps a zero id out of the column, so "no indexer" is NULL rather
// than a reference to an indexer that cannot exist.
func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// SeedProgress is the durable half of a seeding obligation.
//
// The engine's own upload counter is per-process and resets on restart; these
// totals do not. A policy judged against the in-memory number alone forgives
// everything already uploaded every time the service restarts, which on a
// private tracker is the difference between meeting a ratio and being told you
// never did.
type SeedProgress struct {
	InfoHash string
	Uploaded int64
	Seeded   time.Duration
	// Mark is the engine's counter as of the last tick, used to compute a
	// delta without double-counting.
	Mark   int64
	Reason string
}

// BumpSeeding adds a delta to a row's cumulative totals.
//
// engineUploaded is the CURRENT in-process counter. If it is below the stored
// mark the process restarted (or the transfer was re-added), so the whole
// current value is the delta rather than the difference — the alternative is a
// negative delta that silently reduces a total the tracker already credited.
func (s *Store) BumpSeeding(ctx context.Context, hash string, engineUploaded int64,
	elapsed time.Duration) error {

	if !IsInfoHash(hash) {
		return fmt.Errorf("download: %q is not an info hash", hash)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE download_queue
		SET uploaded_bytes = uploaded_bytes +
		        CASE WHEN ? < uploaded_mark THEN ? ELSE ? - uploaded_mark END,
		    uploaded_mark  = ?,
		    seeded_secs    = seeded_secs + ?,
		    updated_at     = ?
		WHERE info_hash = ?`,
		engineUploaded, engineUploaded, engineUploaded, engineUploaded,
		int64(elapsed/time.Second), s.now().UTC().Format(timeLayout), hash)
	if err != nil {
		return fmt.Errorf("download: recording seeding progress: %w", err)
	}
	return nil
}

// NoteProgress records that a transfer has verified more bytes than it had,
// and clears a stalled mark: it is moving again (ADR-0034). Fewer bytes than
// recorded — a restart re-verifying what is on disk — is not progress, and
// changes nothing.
func (s *Store) NoteProgress(ctx context.Context, hash string, completed int64) error {
	if !IsInfoHash(hash) {
		return fmt.Errorf("download: %q is not an info hash", hash)
	}
	now := s.now().UTC().Format(timeLayout)
	if _, err := s.db.ExecContext(ctx, `
		UPDATE download_queue
		SET progress_bytes = ?, progressed_at = ?, stalled_at = NULL, updated_at = ?
		WHERE info_hash = ? AND ? > progress_bytes`,
		completed, now, now, hash, completed); err != nil {
		return fmt.Errorf("download: recording progress: %w", err)
	}
	return nil
}

// MarkStalled marks a transfer found stalled, and stops it when stop is set.
// It reports whether the row was newly marked: a stall is found once.
func (s *Store) MarkStalled(ctx context.Context, hash string, stop bool) (bool, error) {
	if !IsInfoHash(hash) {
		return false, fmt.Errorf("download: %q is not an info hash", hash)
	}
	now := s.now().UTC().Format(timeLayout)
	res, err := s.db.ExecContext(ctx, `
		UPDATE download_queue
		SET stalled_at = ?, updated_at = ?,
		    status = CASE WHEN ? THEN ? ELSE status END
		WHERE info_hash = ? AND stalled_at IS NULL`,
		now, now, stop, StatusStopped, hash)
	if err != nil {
		return false, fmt.Errorf("download: marking a stall: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// FinishSeeding records that an obligation has been met and why.
func (s *Store) FinishSeeding(ctx context.Context, hash, reason string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE download_queue SET seeding_done_reason = ?, updated_at = ?
		WHERE info_hash = ?`,
		reason, s.now().UTC().Format(timeLayout), hash)
	if err != nil {
		return fmt.Errorf("download: recording the end of seeding: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRowNotFound
	}
	return nil
}

// Seeding returns the rows that are complete and still under obligation.
func (s *Store) Seeding(ctx context.Context) ([]Record, error) {
	return s.query(ctx, `WHERE status = ? AND seeding_done_reason = '' ORDER BY added_at ASC`,
		StatusComplete)
}

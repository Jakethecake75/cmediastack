package music

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// KindArtist is a followed artist's media_item kind.
const KindArtist = "artist"

const timeLayout = time.RFC3339Nano

// ErrNoSuchAlbum is an album that does not exist or the caller may not see.
var ErrNoSuchAlbum = errors.New("music: no such album")

// Album is one album of an artist, with what the library holds of it.
type Album struct {
	ID        int64
	ItemID    int64
	Artist    string
	MBID      string
	ReleaseID string
	Title     string
	Type      string
	Released  string
	Monitored bool
	// Known is how many tracks its track list has; Have how many a file holds.
	Known, Have int
	// TracksKnown is whether the track list has been fetched.
	TracksKnown bool
	Tracks      []Track
}

// Track is one track of an album.
type Track struct {
	ID       int64
	Disc     int
	Number   int
	Title    string
	LengthMS int
	MBID     string
	FileID   int64
}

// Store keeps the catalogue.
type Store struct {
	db  *db.DB
	now func() time.Time
}

// NewStore builds one.
func NewStore(database *db.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: database, now: now}
}

func (s *Store) ts() string { return s.now().UTC().Format(timeLayout) }

// ArtistInput is an artist about to be followed.
type ArtistInput struct {
	MBID         string
	Name         string
	SortName     string
	RootFolderID int64
	Folder       string
}

// AddArtist writes the artist's media_item in the caller's transaction. It
// refuses one already followed, and a folder another title occupies, as
// adding a series does (ADR-0025).
func (s *Store) AddArtist(ctx context.Context, tx db.Execer, in ArtistInput) (int64, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return 0, err
	}
	if err := importer.CheckFolderName(in.Folder); err != nil {
		return 0, err
	}
	sortTitle := importer.SortTitle(in.Name)
	if in.SortName != "" {
		sortTitle = strings.ToLower(in.SortName)
	}
	now := s.ts()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO media_item (kind, title, sort_title, root_folder_id, folder, musicbrainz_id,
		                        added_at, updated_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM media_item WHERE kind = ? AND musicbrainz_id = ?)
		  AND NOT EXISTS (SELECT 1 FROM media_item WHERE root_folder_id = ? AND folder = ?)`,
		KindArtist, in.Name, sortTitle, in.RootFolderID, in.Folder, in.MBID, now, now,
		KindArtist, in.MBID, in.RootFolderID, in.Folder)
	if err != nil {
		return 0, fmt.Errorf("music: adding the artist: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Unscoped: which condition refused the add, whoever can see the title.
		var id int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM media_item WHERE kind = ? AND musicbrainz_id = ?`,
			KindArtist, in.MBID).Scan(&id); err == nil {
			return 0, &importer.ConflictError{Err: importer.ErrAlreadyInLibrary,
				Existing: importer.Item{ID: id, Kind: KindArtist, Title: in.Name}}
		}
		return 0, &importer.ConflictError{Err: importer.ErrFolderTaken,
			Existing: importer.Item{RootFolderID: in.RootFolderID, Folder: in.Folder}}
	}
	return res.LastInsertId()
}

// UpsertAlbums records an artist's albums: new ones added, known ones renamed
// and redated as the provider now has them. An album the provider no longer
// lists is kept: it may be on disk.
func (s *Store) UpsertAlbums(ctx context.Context, tx db.Execer, itemID int64, groups []ReleaseGroup) error {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return err
	}
	now := s.ts()
	for _, g := range groups {
		var released any
		if g.Released != "" {
			released = g.Released
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO album (item_id, musicbrainz_id, title, album_type, released_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(item_id, musicbrainz_id) DO UPDATE SET
			    title = excluded.title, album_type = excluded.album_type,
			    released_at = excluded.released_at, updated_at = excluded.updated_at`,
			itemID, g.MBID, g.Title, g.Type, released, now, now); err != nil {
			return fmt.Errorf("music: recording an album: %w", err)
		}
	}
	return nil
}

// Monitoring choices when an artist is added (ADR-0044, decision 3).
const (
	MonitorAll    = "all"
	MonitorFuture = "future"
	MonitorLatest = "latest"
	MonitorNone   = "none"
)

// ErrNoSuchMonitoring refuses anything but the four choices, and no choice.
var ErrNoSuchMonitoring = errors.New("music: monitoring must be all, future, latest or none")

// released reports whether a MusicBrainz date is on or before today. A year or
// a month counts as released once it has begun.
func released(date string, today time.Time) bool {
	if date == "" {
		return false
	}
	return date <= today.UTC().Format("2006-01-02")[:len(date)]
}

// ApplyMonitoring sets which of an artist's albums are wanted.
func (s *Store) ApplyMonitoring(ctx context.Context, tx db.Execer, itemID int64, mode string) error {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return err
	}
	all, err := albumDates(ctx, tx, itemID)
	if err != nil {
		return err
	}
	today := s.now()
	latest := ""
	for _, r := range all {
		if released(r.date, today) && r.date > latest {
			latest = r.date
		}
	}
	for _, r := range all {
		var on bool
		switch mode {
		case MonitorAll:
			on = true
		case MonitorFuture:
			on = !released(r.date, today)
		case MonitorLatest:
			on = !released(r.date, today) || (latest != "" && r.date == latest)
		case MonitorNone:
			on = false
		default:
			return ErrNoSuchMonitoring
		}
		if _, err := tx.ExecContext(ctx, `UPDATE album SET monitored = ? WHERE id = ?`, boolInt(on), r.id); err != nil {
			return err
		}
	}
	return nil
}

type albumDate struct {
	id   int64
	date string
}

func albumDates(ctx context.Context, tx db.Execer, itemID int64) ([]albumDate, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(released_at, '') FROM album WHERE item_id = ?`, itemID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var all []albumDate
	for rows.Next() {
		var r albumDate
		if err := rows.Scan(&r.id, &r.date); err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	return all, rows.Err()
}

// heldTracks maps an album's tracks, by disc and number, to their files.
func heldTracks(ctx context.Context, tx db.Execer, albumID int64) (map[[2]int]int64, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT disc, number, file_id FROM track WHERE album_id = ? AND file_id IS NOT NULL`, albumID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	files := map[[2]int]int64{}
	for rows.Next() {
		var d, n int
		var f int64
		if err := rows.Scan(&d, &n, &f); err != nil {
			return nil, err
		}
		files[[2]int{d, n}] = f
	}
	return files, rows.Err()
}

// SetTracks replaces an album's track list with one release's, keeping the
// file of every track that is still there by disc and number.
func (s *Store) SetTracks(ctx context.Context, albumID int64, releaseID string, tracks []TrackInfo) error {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx db.Execer) error {
		files, err := heldTracks(ctx, tx, albumID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM track WHERE album_id = ?`, albumID); err != nil {
			return err
		}
		for _, t := range tracks {
			var file, length any
			if f, ok := files[[2]int{t.Disc, t.Number}]; ok {
				file = f
			}
			if t.LengthMS > 0 {
				length = t.LengthMS
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO track (album_id, disc, number, title, length_ms, musicbrainz_id, file_id)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				albumID, t.Disc, t.Number, t.Title, length, nullString(t.MBID), file); err != nil {
				return fmt.Errorf("music: recording a track: %w", err)
			}
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE album SET release_id = ?, tracks_refreshed_at = ?, updated_at = ? WHERE id = ?`,
			nullString(releaseID), s.ts(), s.ts(), albumID)
		return err
	})
}

const albumSelect = `
	SELECT a.id, a.item_id, i.title, a.musicbrainz_id, COALESCE(a.release_id, ''), a.title,
	       a.album_type, COALESCE(a.released_at, ''), a.monitored, a.tracks_refreshed_at IS NOT NULL,
	       (SELECT COUNT(*) FROM track t WHERE t.album_id = a.id),
	       (SELECT COUNT(*) FROM track t WHERE t.album_id = a.id AND t.file_id IS NOT NULL)
	FROM album a JOIN media_item i ON i.id = a.item_id`

func scanAlbum(row interface{ Scan(...any) error }) (Album, error) {
	var a Album
	var monitored, known int
	err := row.Scan(&a.ID, &a.ItemID, &a.Artist, &a.MBID, &a.ReleaseID, &a.Title, &a.Type,
		&a.Released, &monitored, &known, &a.Known, &a.Have)
	a.Monitored, a.TracksKnown = monitored == 1, known == 1
	return a, err
}

func (s *Store) queryAlbums(ctx context.Context, where string, args ...any) ([]Album, error) {
	visible, vargs := library.Visible(ctx, "i")
	rows, err := s.db.QueryContext(ctx, albumSelect+` WHERE `+visible+` AND `+where,
		append(vargs, args...)...)
	if err != nil {
		return nil, fmt.Errorf("music: reading albums: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Album, 0)
	for rows.Next() {
		a, err := scanAlbum(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Albums lists an artist's albums, newest last, for a caller who may see the
// artist (ADR-0037).
func (s *Store) Albums(ctx context.Context, itemID int64) ([]Album, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	return s.queryAlbums(ctx, `a.item_id = ? ORDER BY COALESCE(a.released_at, '9999'), a.title`, itemID)
}

// Album reads one album with its tracks.
func (s *Store) Album(ctx context.Context, albumID int64) (Album, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return Album{}, err
	}
	list, err := s.queryAlbums(ctx, `a.id = ?`, albumID)
	if err != nil {
		return Album{}, err
	}
	if len(list) == 0 {
		return Album{}, ErrNoSuchAlbum
	}
	a := list[0]
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, disc, number, title, COALESCE(length_ms, 0), COALESCE(musicbrainz_id, ''),
		       COALESCE(file_id, 0)
		FROM track WHERE album_id = ? ORDER BY disc, number`, albumID)
	if err != nil {
		return Album{}, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var t Track
		if err := rows.Scan(&t.ID, &t.Disc, &t.Number, &t.Title, &t.LengthMS, &t.MBID, &t.FileID); err != nil {
			return Album{}, err
		}
		a.Tracks = append(a.Tracks, t)
	}
	return a, rows.Err()
}

// SetAlbumMonitored turns an album on or off.
func (s *Store) SetAlbumMonitored(ctx context.Context, albumID int64, on bool) (Album, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return Album{}, err
	}
	if _, err := s.Album(ctx, albumID); err != nil {
		return Album{}, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE album SET monitored = ?, updated_at = ? WHERE id = ?`,
		boolInt(on), s.ts(), albumID); err != nil {
		return Album{}, err
	}
	return s.Album(ctx, albumID)
}

// WantedAlbums lists the albums the library should have and does not: those
// monitored, released, and missing at least one track — or whose track list is
// not known yet and of which nothing is held.
func (s *Store) WantedAlbums(ctx context.Context, limit int) ([]Album, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	today := s.now().UTC().Format("2006-01-02")
	return s.queryAlbums(ctx, `a.monitored = 1
		AND a.released_at IS NOT NULL AND a.released_at <= ?
		AND (NOT EXISTS (SELECT 1 FROM track t WHERE t.album_id = a.id)
		     OR EXISTS (SELECT 1 FROM track t WHERE t.album_id = a.id AND t.file_id IS NULL))
		ORDER BY a.released_at DESC, a.title LIMIT ?`, today, limit)
}

// AlbumsWithoutTracks lists albums whose track list was never fetched, oldest
// first, for the refresh task.
func (s *Store) AlbumsWithoutTracks(ctx context.Context, limit int) ([]Album, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	return s.queryAlbums(ctx, `a.tracks_refreshed_at IS NULL AND a.monitored = 1
		ORDER BY a.created_at LIMIT ?`, limit)
}

// ArtistRef is a followed artist, as the refresh needs it.
type ArtistRef struct {
	ID   int64
	Name string
	MBID string
}

// ArtistsToRefresh lists followed artists whose albums were last asked for
// longer ago than every, oldest first.
func (s *Store) ArtistsToRefresh(ctx context.Context, every time.Duration, limit int) ([]ArtistRef, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	stale := s.now().UTC().Add(-every).Format(timeLayout)
	// Unscoped: the refresh keeps every artist current, whoever can see them.
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, title, musicbrainz_id FROM media_item
		WHERE kind = ? AND musicbrainz_id IS NOT NULL
		  AND (episodes_refreshed_at IS NULL OR episodes_refreshed_at < ?)
		ORDER BY COALESCE(episodes_refreshed_at, ''), id LIMIT ?`, KindArtist, stale, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ArtistRef
	for rows.Next() {
		var a ArtistRef
		var mbid sql.NullString
		if err := rows.Scan(&a.ID, &a.Name, &mbid); err != nil {
			return nil, err
		}
		a.MBID = mbid.String
		out = append(out, a)
	}
	return out, rows.Err()
}

// MarkRefreshed records when an artist's albums were last asked for. The
// column is the one series use for the same purpose.
func (s *Store) MarkRefreshed(ctx context.Context, tx db.Execer, itemID int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE media_item SET episodes_refreshed_at = ? WHERE id = ?`, s.ts(), itemID)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

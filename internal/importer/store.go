package importer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// Item kinds.
const (
	KindMovie  = "movie"
	KindSeries = "series"
	// KindBook is a book (ADR-0048).
	KindBook = "book"
)

// Import outcomes, as recorded.
const (
	OutcomeImported = "imported"
	OutcomeSkipped  = "skipped"
	OutcomeFailed   = "failed"
)

// Errors callers distinguish.
var (
	ErrItemNotFound = errors.New("importer: no such library item")
	// ErrNoSuchProfile means a title was given a quality profile that does
	// not exist.
	ErrNoSuchProfile = errors.New("importer: no such quality profile")
	ErrFileNotFound  = errors.New("importer: no such media file")
	// ErrAlreadyInLibrary means an item of that kind already carries that
	// provider id.
	ErrAlreadyInLibrary = errors.New("importer: that title is already in the library")
	// ErrFolderTaken means another item already occupies that folder in that
	// root folder.
	ErrFolderTaken = errors.New("importer: another library item already occupies that folder")
	// ErrNotAFilm means an item asked for as a film is a series.
	ErrNotAFilm = errors.New("importer: that library item is a series, not a film")
)

// ConflictError names the item an add ran into, so the answer can point at it
// rather than merely refuse.
type ConflictError struct {
	// Err is ErrAlreadyInLibrary or ErrFolderTaken.
	Err      error
	Existing Item
}

func (e *ConflictError) Error() string {
	name := e.Existing.Title
	if e.Existing.Year > 0 {
		name = fmt.Sprintf("%s (%d)", name, e.Existing.Year)
	}
	return fmt.Sprintf("%v: %s, item %d, in the folder %q", e.Err, name, e.Existing.ID, e.Existing.Folder)
}

func (e *ConflictError) Unwrap() error { return e.Err }

// Item is a film or a series.
type Item struct {
	ID           int64
	Kind         string
	Title        string
	Year         int
	SortTitle    string
	RootFolderID int64
	Folder       string
	// TMDBID and IMDbID are what identification attached, zero and empty when
	// nothing has been attached yet. Carried on the read path deliberately:
	// without them nothing that reads a library can tell an identified item
	// from an unidentified one, or find the poster for either.
	TMDBID    int64
	IMDbID    string
	AddedAt   time.Time
	UpdatedAt time.Time
	// Monitored says a film is wanted while it has no file (ADR-0030). A
	// film is added monitored. Meaningless on a series, which is monitored
	// episode by episode (ADR-0022).
	Monitored bool
	// SeasonFolders says whether a series' episodes are filed in season
	// folders (ADR-0063); read, never written by an upsert.
	SeasonFolders bool
	// QualityProfileID is the profile this title's searches are judged by;
	// zero is the instance's default (ADR-0035).
	QualityProfileID int64
	// Certification is the title's rating ("PG-13", "TV-MA"), empty when it is
	// unrated, and RatingRank its rank, 0 when unrated. RatingSource says who
	// set it: "provider", "person", or "" (ADR-0037).
	Certification string
	RatingRank    int
	RatingSource  string
	// Author and OpenLibraryID are a book's (ADR-0048); empty on anything else.
	Author        string
	OpenLibraryID string
}

// File is one file on disk that the library holds.
type File struct {
	ID           int64
	ItemID       int64
	Season       *int
	Episode      *int
	EpisodeLast  *int
	RootFolderID int64
	RelPath      string
	SizeBytes    int64
	Quality      string
	Revision     int
	ReleaseTitle string
	ReleaseGroup string
	InfoHash     string
	Hardlinked   bool
	ImportedAt   time.Time
}

// Record is what happened to one completed download.
type Record struct {
	ID          int64
	InfoHash    string
	Outcome     string
	Detail      string
	SourcePath  string
	MediaFileID *int64
	OccurredAt  time.Time
}

// Store persists the library.
type Store struct {
	db  *db.DB
	now func() time.Time
}

// NewStore builds the store.
func NewStore(database *db.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: database, now: now}
}

const timeLayout = time.RFC3339Nano

// SortTitle moves a leading article to the end and lowercases, so "The Matrix"
// files under M.
//
// Stored rather than computed per query because the rule is language-dependent
// and an operator may want to override one — a film genuinely called "The The"
// should not become "The, The".
func SortTitle(title string) string {
	t := strings.TrimSpace(title)
	lower := strings.ToLower(t)
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(lower, article) {
			return strings.TrimSpace(lower[len(article):]) + ", " + strings.TrimSpace(article)
		}
	}
	return lower
}

// UpsertItem finds or creates the item a file belongs to.
//
// Keyed on (root folder, folder name) rather than on (title, year): the folder
// is what exists on disk, and two releases that produce the same folder ARE the
// same item however their titles were punctuated. Matching on title would make
// "Blade Runner 2049" and "Blade Runner 2049." two items pointing at one
// directory, each believing it owns the contents.
func (s *Store) UpsertItem(ctx context.Context, it Item) (Item, error) {
	now := s.now().UTC()
	if it.SortTitle == "" {
		it.SortTitle = SortTitle(it.Title)
	}

	existing, err := s.itemByFolder(ctx, it.RootFolderID, it.Folder)
	switch {
	case err == nil:
		return existing, nil
	case !errors.Is(err, ErrItemNotFound):
		return Item{}, err
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO media_item (kind, title, year, sort_title, root_folder_id,
		                        folder, added_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?)`,
		it.Kind, it.Title, nullYear(it.Year), it.SortTitle, it.RootFolderID,
		it.Folder, now.Format(timeLayout), now.Format(timeLayout))
	if err != nil {
		return Item{}, fmt.Errorf("importer: creating the library item: %w", err)
	}
	it.ID, _ = res.LastInsertId()
	it.AddedAt, it.UpdatedAt = now, now
	// What the row now says: every item is created monitored (migration 0018).
	it.Monitored = true
	return it, nil
}

// AddItem creates the item for a title a person chose from a provider, before
// any of it is on disk (ADR-0025).
//
// It runs in the caller's transaction: an added series is its item, its
// identification and its episodes, written together or not at all.
//
// # One statement decides, and the loser of a race is told it lost
//
// The insert carries its own conditions — no item of this kind with this
// provider id, and none in this folder of this root — and inserts nothing when
// either fails. Being a write, it takes the database's write lock BEFORE it
// reads, so a second add waits for the first to commit and then judges the
// conditions against what the first wrote.
//
// Checking first and inserting second does not create a duplicate under
// SQLite's WAL — measured, not assumed — but it fails worse: the second add's
// read opens a snapshot, the first commits, and the second's insert is refused
// with SQLITE_BUSY_SNAPSHOT ("database is locked", 517), which reaches the
// operator as an internal error for what is only "already added".
// TestAnAddThatLosesARaceIsToldSo interleaves two adds by hand to hold this.
//
// The provider id is not a UNIQUE index, deliberately. A library that already
// holds two items with one id — two folders of the same film, both identified
// — is a real state, and an index would refuse to be created on it, failing
// the upgrade at startup. The rule applies to adding, which is where a
// duplicate would be created by this software rather than found by it.
func (s *Store) AddItem(ctx context.Context, tx db.Execer, it Item) (Item, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return Item{}, err
	}
	it.Title = strings.TrimSpace(it.Title)
	switch {
	case it.Kind != KindMovie && it.Kind != KindSeries:
		return Item{}, fmt.Errorf("importer: %q is not a kind of library item", it.Kind)
	case it.Title == "":
		return Item{}, fmt.Errorf("importer: an added item needs a title")
	case it.TMDBID <= 0:
		return Item{}, fmt.Errorf("importer: an added item needs the provider id it was chosen by")
	case it.RootFolderID <= 0:
		return Item{}, fmt.Errorf("importer: an added item needs a root folder")
	}
	// The folder is checked again here, whoever built it: this is the last
	// point before it is stored, and a stored folder name is joined to a root
	// by every import that follows.
	if err := CheckFolderName(it.Folder); err != nil {
		return Item{}, err
	}
	if it.SortTitle == "" {
		it.SortTitle = SortTitle(it.Title)
	}
	now := s.now().UTC()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO media_item (kind, title, year, sort_title, root_folder_id,
		                        folder, tmdb_id, imdb_id, added_at, updated_at)
		SELECT ?,?,?,?,?,?,?,?,?,?
		WHERE NOT EXISTS (SELECT 1 FROM media_item WHERE kind = ? AND tmdb_id = ?)
		  AND NOT EXISTS (SELECT 1 FROM media_item WHERE root_folder_id = ? AND folder = ?)`,
		it.Kind, it.Title, nullYear(it.Year), it.SortTitle, it.RootFolderID,
		it.Folder, it.TMDBID, nullString(it.IMDbID),
		now.Format(timeLayout), now.Format(timeLayout),
		it.Kind, it.TMDBID, it.RootFolderID, it.Folder)
	if err != nil {
		return Item{}, fmt.Errorf("importer: adding the library item: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Which condition refused it. Read on the same transaction, which the
		// insert has already made a writing one, so this is the state the
		// insert was judged against.
		if dup, qerr := s.queryItemsOn(ctx, tx, everything, `WHERE kind = ? AND tmdb_id = ? ORDER BY id LIMIT 1`,
			it.Kind, it.TMDBID); qerr == nil && len(dup) > 0 {
			return Item{}, &ConflictError{Err: ErrAlreadyInLibrary, Existing: dup[0]}
		}
		if occ, qerr := s.queryItemsOn(ctx, tx, everything, `WHERE root_folder_id = ? AND folder = ?`,
			it.RootFolderID, it.Folder); qerr == nil && len(occ) > 0 {
			return Item{}, &ConflictError{Err: ErrFolderTaken, Existing: occ[0]}
		}
		return Item{}, fmt.Errorf("importer: the library item was not added, and nothing says why")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Item{}, fmt.Errorf("importer: adding the library item: %w", err)
	}
	it.ID = id
	it.AddedAt, it.UpdatedAt = now, now
	// What the row now says: a film is added wanted (ADR-0030), and the column
	// is 1 for every new item.
	it.Monitored = true
	return it, nil
}

// ItemWithProviderID returns the item of a kind that carries a provider id.
//
// Adding asks this before it spends any request on the provider, so a second
// add of the same series costs nothing. It is a courtesy, not the rule: the
// rule is enforced by AddItem's insert.
func (s *Store) ItemWithProviderID(ctx context.Context, kind string, tmdbID int64) (Item, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return Item{}, err
	}
	items, err := s.queryItems(ctx, visibleTo(ctx), `WHERE kind = ? AND tmdb_id = ? ORDER BY id LIMIT 1`, kind, tmdbID)
	if err != nil {
		return Item{}, err
	}
	if len(items) == 0 {
		return Item{}, ErrItemNotFound
	}
	return items[0], nil
}

func (s *Store) itemByFolder(ctx context.Context, rootID int64, folder string) (Item, error) {
	// Everything: the scan and the import ask what already owns a folder, and
	// the answer must not depend on who is asking.
	items, err := s.queryItems(ctx, everything, `WHERE root_folder_id = ? AND folder = ?`, rootID, folder)
	if err != nil {
		return Item{}, err
	}
	if len(items) == 0 {
		return Item{}, ErrItemNotFound
	}
	return items[0], nil
}

// GetItem returns one item.
func (s *Store) GetItem(ctx context.Context, id int64) (Item, error) {
	items, err := s.queryItems(ctx, visibleTo(ctx), `WHERE id = ?`, id)
	if err != nil {
		return Item{}, err
	}
	if len(items) == 0 {
		return Item{}, ErrItemNotFound
	}
	return items[0], nil
}

// Film returns one film, for a search to be run for it (ADR-0026).
//
// Reading the library is browsing, so that is what it checks — the permission
// to spend the indexers' time on it is the route's, as for an episode.
func (s *Store) Film(ctx context.Context, id int64) (Item, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return Item{}, err
	}
	items, err := s.queryItems(ctx, visibleTo(ctx), `WHERE id = ?`, id)
	if err != nil {
		return Item{}, err
	}
	if len(items) == 0 {
		return Item{}, ErrItemNotFound
	}
	if items[0].Kind != KindMovie {
		return Item{}, fmt.Errorf("%w: %s", ErrNotAFilm, items[0].Title)
	}
	return items[0], nil
}

// WantedFilms returns the monitored films in the library with no file
// (ADR-0026, ADR-0030).
//
// Adding a film is wanting it: a film is added monitored, and unmonitoring it
// — "keep it, but stop looking for it" — is the one way to want it less short
// of removing it. That switch arrived with automatic acquisition, which is the
// first thing it changes. There is no release-date condition, because the date
// is the provider's answer at the moment the film was added and nothing
// refreshes it — a rule on a stale date would hide a film added before it had
// one, for good. Alphabetical, as the library is.
func (s *Store) WantedFilms(ctx context.Context, limit int) ([]Item, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	return s.queryItems(ctx, visibleTo(ctx), `
		WHERE kind = ?
		  AND monitored = 1
		  AND NOT EXISTS (SELECT 1 FROM media_file f WHERE f.item_id = media_item.id)
		ORDER BY sort_title ASC, id ASC
		LIMIT ?`, KindMovie, limit)
}

// SetFilmMonitored says whether a film is wanted while it has no file
// (ADR-0030). Refused for a series, which is monitored by season and episode.
//
// Gated on editing the library, as the season and episode switches are: with
// automatic acquisition on, monitoring is what decides what gets downloaded.
func (s *Store) SetFilmMonitored(ctx context.Context, id int64, on bool) (Item, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return Item{}, err
	}
	items, err := s.queryItems(ctx, visibleTo(ctx), `WHERE id = ?`, id)
	if err != nil {
		return Item{}, err
	}
	if len(items) == 0 {
		return Item{}, ErrItemNotFound
	}
	// A book is wanted or not as a film is (ADR-0048, decision 3).
	if items[0].Kind != KindMovie && items[0].Kind != KindBook {
		return Item{}, fmt.Errorf("%w: %s", ErrNotAFilm, items[0].Title)
	}
	v := 0
	if on {
		v = 1
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE media_item SET monitored = ?, updated_at = ? WHERE id = ?`,
		v, s.now().UTC().Format(timeLayout), id); err != nil {
		return Item{}, fmt.Errorf("importer: setting a film's monitoring: %w", err)
	}
	items[0].Monitored = on
	return items[0], nil
}

// SetQualityProfile names the profile a title is judged by, or with 0 the
// instance's default (ADR-0035).
func (s *Store) SetQualityProfile(ctx context.Context, id, profileID int64) (Item, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return Item{}, err
	}
	items, err := s.queryItems(ctx, visibleTo(ctx), `WHERE id = ?`, id)
	if err != nil {
		return Item{}, err
	}
	if len(items) == 0 {
		return Item{}, ErrItemNotFound
	}
	var value any
	if profileID > 0 {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM quality_profile WHERE id = ?`,
			profileID).Scan(&n); err != nil {
			return Item{}, fmt.Errorf("importer: reading quality profiles: %w", err)
		}
		if n == 0 {
			return Item{}, ErrNoSuchProfile
		}
		value = profileID
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE media_item SET quality_profile_id = ?, updated_at = ? WHERE id = ?`,
		value, s.now().UTC().Format(timeLayout), id); err != nil {
		return Item{}, fmt.Errorf("importer: setting a title's quality profile: %w", err)
	}
	items[0].QualityProfileID = max(profileID, 0)
	return items[0], nil
}

// HoldsProviderID reports whether a title the caller may see carries the
// provider id (ADR-0037).
func (s *Store) HoldsProviderID(ctx context.Context, tmdbID int64) (bool, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return false, err
	}
	items, err := s.queryItems(ctx, visibleTo(ctx), `WHERE tmdb_id = ? LIMIT 1`, tmdbID)
	if err != nil {
		return false, err
	}
	return len(items) > 0, nil
}

// HasTitle reports whether a title of a kind with a provider id is one the
// caller may see (ADR-0043).
func (s *Store) HasTitle(ctx context.Context, kind string, tmdbID int64) (bool, error) {
	_, err := s.ItemWithProviderID(ctx, kind, tmdbID)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrItemNotFound):
		return false, nil
	}
	return false, err
}

// ItemForQueue returns an item a download is for, whoever may see it.
//
// The queue is an operations surface (ADR-0037, decision 4): a download's
// release name already says what it is, so the queue names its title to
// whoever manages downloads, as the import does.
func (s *Store) ItemForQueue(ctx context.Context, id int64) (Item, error) {
	if err := authz.RequirePermission(ctx, authz.PermManageQueue); err != nil {
		return Item{}, err
	}
	items, err := s.queryItems(ctx, everything, `WHERE id = ?`, id)
	if err != nil {
		return Item{}, err
	}
	if len(items) == 0 {
		return Item{}, ErrItemNotFound
	}
	return items[0], nil
}

// ListItems returns the library, ordered for display.
func (s *Store) ListItems(ctx context.Context, kind string) ([]Item, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	if kind == "" {
		return s.queryItems(ctx, visibleTo(ctx), `ORDER BY kind ASC, sort_title ASC`)
	}
	return s.queryItems(ctx, visibleTo(ctx), `WHERE kind = ? ORDER BY sort_title ASC`, kind)
}

// PutFile records an imported file, replacing any record of the same path.
func (s *Store) PutFile(ctx context.Context, f File) (File, error) {
	now := s.now().UTC()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO media_file (item_id, season, episode, episode_last,
		        root_folder_id, relative_path, size_bytes, quality, revision,
		        release_title, release_group, info_hash, hardlinked, imported_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(root_folder_id, relative_path) DO UPDATE SET
			item_id       = excluded.item_id,
			season        = excluded.season,
			episode       = excluded.episode,
			episode_last  = excluded.episode_last,
			size_bytes    = excluded.size_bytes,
			quality       = excluded.quality,
			revision      = excluded.revision,
			release_title = excluded.release_title,
			release_group = excluded.release_group,
			info_hash     = excluded.info_hash,
			hardlinked    = excluded.hardlinked,
			imported_at   = excluded.imported_at`,
		f.ItemID, f.Season, f.Episode, f.EpisodeLast, f.RootFolderID, f.RelPath,
		f.SizeBytes, f.Quality, f.Revision, f.ReleaseTitle, f.ReleaseGroup,
		nullString(f.InfoHash), boolInt(f.Hardlinked), now.Format(timeLayout))
	if err != nil {
		return File{}, fmt.Errorf("importer: recording the media file: %w", err)
	}
	f.ImportedAt = now
	if id, lerr := res.LastInsertId(); lerr == nil && id > 0 {
		f.ID = id
	}
	return f, nil
}

// FilesFor returns an item's files.
func (s *Store) FilesFor(ctx context.Context, itemID int64) ([]File, error) {
	return s.queryFiles(ctx, visibleTo(ctx), `WHERE item_id = ? ORDER BY season ASC, episode ASC, relative_path ASC`, itemID)
}

// FileAt returns the file recorded at a path, if any.
func (s *Store) FileAt(ctx context.Context, rootID int64, relPath string) (File, error) {
	files, err := s.queryFiles(ctx, everything, `WHERE root_folder_id = ? AND relative_path = ?`, rootID, relPath)
	if err != nil {
		return File{}, err
	}
	if len(files) == 0 {
		return File{}, ErrFileNotFound
	}
	return files[0], nil
}

// ExistingEpisode finds the file already held for an episode, if any.
//
// This is what an upgrade decision is made against: "do we already have S02E05,
// and is what we have worse than what just arrived".
func (s *Store) ExistingEpisode(ctx context.Context, itemID int64, season, episode int) (File, error) {
	files, err := s.queryFiles(ctx, everything,
		`WHERE item_id = ? AND season = ? AND episode = ?`, itemID, season, episode)
	if err != nil {
		return File{}, err
	}
	if len(files) == 0 {
		return File{}, ErrFileNotFound
	}
	return files[0], nil
}

// ExistingMovie finds the file already held for a film, if any.
func (s *Store) ExistingMovie(ctx context.Context, itemID int64) (File, error) {
	files, err := s.queryFiles(ctx, everything, `WHERE item_id = ? AND season IS NULL`, itemID)
	if err != nil {
		return File{}, err
	}
	if len(files) == 0 {
		return File{}, ErrFileNotFound
	}
	return files[0], nil
}

// filesInRoot lists every recorded file in one root folder.
//
// Unexported: this is the scan's view, and it is deliberately not on the public
// surface. An API that could enumerate a root's files by path would be handing
// out the operator's directory layout to anyone who could reach it.
func (s *Store) filesInRoot(ctx context.Context, rootID int64) ([]File, error) {
	return s.queryFiles(ctx, everything, `WHERE root_folder_id = ?`, rootID)
}

// ForgetFile removes a file's RECORD. The bytes on disk are not touched here:
// unlinking is the vault's job and carries its own permission.
func (s *Store) ForgetFile(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM media_file WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("importer: forgetting the media file: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrFileNotFound
	}
	return nil
}

// ForgetItem removes an item's record. Its files cascade.
//
// The bytes are not touched here: unlinking is the vault's job and carries its
// own permission. An item forgotten while its files are still on disk is picked
// up again by the next scan, which is the correct behaviour — the library
// describes the disk, not the other way round.
func (s *Store) ForgetItem(ctx context.Context, id int64) error {
	// Unscoped: forgetting a row after its files are gone; the caller already read the item in scope.
	res, err := s.db.ExecContext(ctx, `DELETE FROM media_item WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("importer: forgetting the library item: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrItemNotFound
	}
	return nil
}

// RecordOutcome writes what happened to a download.
//
// Written on failure and on skip as well as on success. "It downloaded and then
// nothing happened" is the complaint this category of software earns, and this
// table is the answer to it.
//
// The same outcome observed again is the same fact, and moves the existing
// record's time forward rather than adding a row. A download skipped for a
// reason that will not change — a disc image, a season pack, a file that is not
// the episode it was grabbed for — is retried, and before this each retry added
// a row and an INFO line: one a minute, for as long as the download existed.
func (s *Store) RecordOutcome(ctx context.Context, r Record) error {
	now := s.now().UTC().Format(timeLayout)

	if r.Outcome != OutcomeImported {
		var lastID int64
		var outcome, detail, source string
		// The last record of the same file: a season pack records each of its
		// files, and the last row of the download is usually another file's.
		err := s.db.QueryRowContext(ctx, `
			SELECT id, outcome, detail, source_path FROM import_record
			WHERE info_hash = ? AND source_path = ?
			ORDER BY occurred_at DESC, id DESC LIMIT 1`,
			r.InfoHash, r.SourcePath).Scan(&lastID, &outcome, &detail, &source)
		switch {
		case err == nil && outcome == r.Outcome && detail == r.Detail && source == r.SourcePath:
			if _, err := s.db.ExecContext(ctx,
				`UPDATE import_record SET occurred_at = ? WHERE id = ?`, now, lastID); err != nil {
				return fmt.Errorf("importer: recording the import outcome: %w", err)
			}
			return nil
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("importer: reading the last import outcome: %w", err)
		}
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO import_record (info_hash, outcome, detail, source_path,
		                           media_file_id, occurred_at)
		VALUES (?,?,?,?,?,?)`,
		r.InfoHash, r.Outcome, r.Detail, r.SourcePath, r.MediaFileID, now)
	if err != nil {
		return fmt.Errorf("importer: recording the import outcome: %w", err)
	}
	return nil
}

// SkipRetryInterval is how long a SKIPPED download waits before it is tried
// again.
//
// Not forever: some skips stop being true — "already in the library at a
// better quality" is lifted by deleting that file. Not every minute either,
// which is what the import task's own interval would otherwise make it.
const SkipRetryInterval = time.Hour

// ShouldAttempt reports whether a completed download should be offered to the
// importer now.
//
//   - Imported: never again.
//   - Skipped: once SkipRetryInterval has passed since it was last looked at.
//   - Failed, or never tried: now. A failure is usually transient — a disk
//     that filled, a root folder not yet configured — and is exactly what an
//     operator is waiting to see resolve.
func (s *Store) ShouldAttempt(ctx context.Context, infoHash string) (bool, error) {
	done, err := s.AlreadyImported(ctx, infoHash)
	if err != nil || done {
		return false, err
	}
	var outcome, at string
	err = s.db.QueryRowContext(ctx, `
		SELECT outcome, occurred_at FROM import_record
		WHERE info_hash = ? ORDER BY occurred_at DESC, id DESC LIMIT 1`,
		infoHash).Scan(&outcome, &at)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("importer: reading the last import outcome: %w", err)
	}
	if outcome != OutcomeSkipped {
		return true, nil
	}
	last, perr := time.Parse(timeLayout, at)
	if perr != nil {
		// An unreadable time is not a reason to stop trying for ever.
		return true, nil //nolint:nilerr // the answer is "retry", which is not a failure
	}
	return s.now().Sub(last) >= SkipRetryInterval, nil
}

// ShouldAttemptPack is ShouldAttempt for a download grabbed for a whole season,
// whose files are recorded one by one (ADR-0033, decision 7). Judged by each
// file's latest record:
//
//   - any failed: now, because a failure is usually transient and only the
//     files not yet imported are attempted again;
//   - every one imported: never again;
//   - otherwise, some skipped: once SkipRetryInterval has passed since the
//     download was last looked at, because the provider may list an episode
//     later.
//
// A record for the whole download rather than a file — the series gone, the
// download unreadable — counts only while it is the newest thing recorded: a
// later pass that got as far as the files has answered it.
func (s *Store) ShouldAttemptPack(ctx context.Context, infoHash string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_path, outcome, occurred_at FROM import_record
		WHERE info_hash = ? ORDER BY occurred_at DESC, id DESC`, infoHash)
	if err != nil {
		return false, fmt.Errorf("importer: reading a pack's import records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	latest := map[string]string{}
	var newest string
	first := true
	for rows.Next() {
		var source, outcome, at string
		if err := rows.Scan(&source, &outcome, &at); err != nil {
			return false, fmt.Errorf("importer: reading a pack's import records: %w", err)
		}
		if first {
			newest, first = at, false
			if source == "" {
				// The whole download's answer is the newest: it stands alone.
				latest = map[string]string{"": outcome}
				break
			}
		}
		if source == "" {
			continue
		}
		if _, seen := latest[source]; !seen {
			latest[source] = outcome
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("importer: reading a pack's import records: %w", err)
	}
	if len(latest) == 0 {
		return true, nil
	}
	allImported := true
	for _, outcome := range latest {
		switch outcome {
		case OutcomeFailed:
			return true, nil
		case OutcomeImported:
		default:
			allImported = false
		}
	}
	if allImported {
		return false, nil
	}
	last, perr := time.Parse(timeLayout, newest)
	if perr != nil {
		return true, nil //nolint:nilerr // the answer is "retry", which is not a failure
	}
	return s.now().Sub(last) >= SkipRetryInterval, nil
}

// ImportedFrom is the files of a download that have been imported, by their
// path in the download.
func (s *Store) ImportedFrom(ctx context.Context, infoHash string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT source_path FROM import_record
		WHERE info_hash = ? AND outcome = ? AND source_path <> ''`, infoHash, OutcomeImported)
	if err != nil {
		return nil, fmt.Errorf("importer: reading what a download imported: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("importer: reading what a download imported: %w", err)
		}
		out[p] = true
	}
	return out, rows.Err()
}

// EpisodeAiredOn is the one episode of a series that aired on a day,
// YYYY-MM-DD (ADR-0064). ok is false when it aired none that day, or more than
// one: a dated release cannot say which of two it is.
func (s *Store) EpisodeAiredOn(ctx context.Context, itemID int64, day string) (season, number int, ok bool, err error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT season_number, number FROM episode
		WHERE item_id = ? AND season_number > 0 AND substr(aired_at, 1, 10) = ?
		LIMIT 2`, itemID, day)
	if err != nil {
		return 0, 0, false, fmt.Errorf("importer: reading the episode of %s: %w", day, err)
	}
	defer func() { _ = rows.Close() }()
	n := 0
	for rows.Next() {
		if err := rows.Scan(&season, &number); err != nil {
			return 0, 0, false, fmt.Errorf("importer: reading the episode of %s: %w", day, err)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return 0, 0, false, fmt.Errorf("importer: reading the episode of %s: %w", day, err)
	}
	return season, number, n == 1, nil
}

// SeasonEpisodes is the episode numbers the provider lists for one season of a
// series, as the episode refresh stored them.
func (s *Store) SeasonEpisodes(ctx context.Context, itemID int64, season int) (map[int]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT number FROM episode WHERE item_id = ? AND season_number = ?`, itemID, season)
	if err != nil {
		return nil, fmt.Errorf("importer: reading season %d's episodes: %w", season, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int]bool{}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("importer: reading season %d's episodes: %w", season, err)
		}
		out[n] = true
	}
	return out, rows.Err()
}

// RecordsFor returns what happened to one download, newest first.
func (s *Store) RecordsFor(ctx context.Context, infoHash string) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, info_hash, outcome, detail, source_path, media_file_id, occurred_at
		FROM import_record WHERE info_hash = ? ORDER BY occurred_at DESC`, infoHash)
	if err != nil {
		return nil, fmt.Errorf("importer: reading import records: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Record, 0)
	for rows.Next() {
		var r Record
		var at string
		if err := rows.Scan(&r.ID, &r.InfoHash, &r.Outcome, &r.Detail,
			&r.SourcePath, &r.MediaFileID, &at); err != nil {
			return nil, fmt.Errorf("importer: reading import records: %w", err)
		}
		r.OccurredAt, _ = time.Parse(timeLayout, at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AlreadyImported reports whether a download has a successful record.
//
// Checked before doing the work, so a completion poll that fires every thirty
// seconds does not re-import the same file forever.
func (s *Store) AlreadyImported(ctx context.Context, infoHash string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM import_record WHERE info_hash = ? AND outcome = ?`,
		infoHash, OutcomeImported).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("importer: checking for a previous import: %w", err)
	}
	return n > 0, nil
}

// ---------------------------------------------------------------------------

// scope is which rows of media_item a read may return (ADR-0037). Every read
// of an item or a file takes one, so a new read has to choose: visibleTo for
// anything answering a person, everything for work that must see the whole
// library whoever started it — the scan, the import, a conflict check.
type scope struct {
	clause string
	args   []any
}

// everything is every row, whoever is asking.
var everything = scope{clause: "1"}

// visibleTo is the rows the context's principal may see: its libraries, and
// titles rated within its ceiling. Background work sees everything anyway.
func visibleTo(ctx context.Context) scope {
	clause, args := library.Visible(ctx, "")
	return scope{clause: clause, args: args}
}

func (s *Store) queryItems(ctx context.Context, sc scope, where string, args ...any) ([]Item, error) {
	return s.queryItemsOn(ctx, s.db, sc, where, args...)
}

// queryItemsOn reads items from a filtered view named media_item, so a
// caller's WHERE, ORDER BY and correlated subqueries read as they always did.
func (s *Store) queryItemsOn(ctx context.Context, q db.Execer, sc scope, where string, args ...any) ([]Item, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, kind, title, COALESCE(year, 0), sort_title, root_folder_id,
		       folder, COALESCE(tmdb_id, 0), COALESCE(imdb_id, ''),
		       added_at, updated_at, monitored, COALESCE(quality_profile_id, 0),
		       COALESCE(certification, ''), COALESCE(rating_rank, 0),
		       COALESCE(rating_source, ''), COALESCE(author, ''), COALESCE(openlibrary_id, ''),
		       season_folders
		FROM (SELECT * FROM media_item WHERE `+sc.clause+`) AS media_item `+where,
		append(slices.Clone(sc.args), args...)...)
	if err != nil {
		return nil, fmt.Errorf("importer: reading library items: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]Item, 0)
	for rows.Next() {
		var it Item
		var added, updated string
		var monitored, seasonFolders int
		if err := rows.Scan(&it.ID, &it.Kind, &it.Title, &it.Year, &it.SortTitle,
			&it.RootFolderID, &it.Folder, &it.TMDBID, &it.IMDbID,
			&added, &updated, &monitored, &it.QualityProfileID,
			&it.Certification, &it.RatingRank, &it.RatingSource, &it.Author, &it.OpenLibraryID,
			&seasonFolders); err != nil {
			return nil, fmt.Errorf("importer: reading library items: %w", err)
		}
		it.AddedAt, _ = time.Parse(timeLayout, added)
		it.UpdatedAt, _ = time.Parse(timeLayout, updated)
		it.Monitored = monitored == 1
		it.SeasonFolders = seasonFolders == 1
		out = append(out, it)
	}
	return out, rows.Err()
}

// queryFiles reads files whose item the scope admits, from a filtered view
// named media_file.
func (s *Store) queryFiles(ctx context.Context, sc scope, where string, args ...any) ([]File, error) {
	from := "media_file"
	if sc.clause != everything.clause {
		from = `(SELECT * FROM media_file WHERE item_id IN
		           (SELECT id FROM media_item WHERE ` + sc.clause + `)) AS media_file`
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, item_id, season, episode, episode_last, root_folder_id,
		       relative_path, size_bytes, quality, revision, release_title,
		       release_group, COALESCE(info_hash, ''), hardlinked, imported_at
		FROM `+from+` `+where, append(slices.Clone(sc.args), args...)...)
	if err != nil {
		return nil, fmt.Errorf("importer: reading media files: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]File, 0)
	for rows.Next() {
		var f File
		var season, episode, episodeLast sql.NullInt64
		var hardlinked int
		var imported string
		if err := rows.Scan(&f.ID, &f.ItemID, &season, &episode, &episodeLast,
			&f.RootFolderID, &f.RelPath, &f.SizeBytes, &f.Quality, &f.Revision,
			&f.ReleaseTitle, &f.ReleaseGroup, &f.InfoHash, &hardlinked, &imported); err != nil {
			return nil, fmt.Errorf("importer: reading media files: %w", err)
		}
		f.Season = nullableInt(season)
		f.Episode = nullableInt(episode)
		f.EpisodeLast = nullableInt(episodeLast)
		f.Hardlinked = hardlinked != 0
		f.ImportedAt, _ = time.Parse(timeLayout, imported)
		out = append(out, f)
	}
	return out, rows.Err()
}

func nullableInt(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}

func nullYear(y int) any {
	if y <= 0 {
		return nil
	}
	return y
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Arrival is a file that arrived in the library, with its title (ADR-0041).
type Arrival struct {
	Item Item
	File File
}

// RecentArrivals returns the most recent imports of titles the caller may
// see (ADR-0037), newest first.
func (s *Store) RecentArrivals(ctx context.Context, limit int) ([]Arrival, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	files, err := s.queryFiles(ctx, visibleTo(ctx), `ORDER BY imported_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Arrival, 0, len(files))
	for _, f := range files {
		it, err := s.GetItem(ctx, f.ItemID)
		if err != nil {
			continue
		}
		out = append(out, Arrival{Item: it, File: f})
	}
	return out, nil
}

package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// Seasons and episodes: what a series HAS, and what it is missing.
//
// # The rule this file is built around
//
// A row here exists because a PROVIDER said the episode exists. Nothing in this
// file creates one from a filename, a release name, or a file on disk, and
// nothing outside it may either — Upsert is the only writer, and it takes
// provider data or nothing (ADR-0022).
//
// The reason is worth restating where the code is rather than only in the ADR.
// A season assembled from the files an instance holds is 100% complete by
// construction, and a "missing episodes" list built the same way is always
// empty. Software that confidently tells an operator they have everything, when
// what it means is "here is a list of the things I have", is worse than
// software that says nothing: the first is trusted and wrong.

const episodeTimeLayout = time.RFC3339Nano

// ErrNotFound means the title is not in the library — or not in the part of it
// the caller may see, which is deliberately the same answer (ADR-0037).
var ErrNotFound = errors.New("library: not found")

// requireVisible refuses a title the caller may not see as if it did not exist.
func requireVisible(ctx context.Context, q db.Execer, itemID int64) error {
	clause, args := Visible(ctx, "")
	var n int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media_item WHERE id = ? AND `+clause,
		append([]any{itemID}, args...)...).Scan(&n); err != nil {
		return fmt.Errorf("library: reading the library: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ErrNotASeries means the item is a film, which has no seasons.
var ErrNotASeries = errors.New("library: that item is not a series")

// Season is one season as this instance holds it.
type Season struct {
	ID       int64
	ItemID   int64
	Number   int
	Name     string
	Overview string
	// EpisodeCount is what the provider said, which is not the same as the
	// number of episode rows: a refresh that failed part-way leaves fewer.
	EpisodeCount int
	Aired        time.Time
	Monitored    bool
}

// Episode is one episode, whether or not this instance has the file.
type Episode struct {
	ID           int64
	ItemID       int64
	SeasonID     int64
	SeasonNumber int
	Number       int
	Title        string
	Overview     string
	// Aired is the zero time for an ANNOUNCED episode — one the provider lists
	// with no date. Such an episode is never wanted; see Wanted.
	Aired           time.Time
	RuntimeMinutes  int
	ProviderEpisode int64
	Monitored       bool

	// HaveFileID is the file covering this episode, zero when there is none.
	//
	// Not a stored column. Files are matched to episodes by NUMBER at read
	// time, because one file can hold several episodes — a double-length pilot,
	// or S01E01E02 — and a foreign key can only point at one of them. It also
	// keeps the scan and the importer entirely ignorant of episodes.
	HaveFileID int64
	// HavePath is that file's path inside its root, for display.
	HavePath string
}

// Have reports whether this instance holds a file covering the episode.
func (e Episode) Have() bool { return e.HaveFileID != 0 }

// Announced reports an episode the provider lists with no air date.
//
// A different fact from "not aired yet", and a useful one: it is why the wanted
// list does not fill up with every unannounced episode of every running show.
func (e Episode) Announced() bool { return e.Aired.IsZero() }

// EpisodeStore reads and writes seasons and episodes.
type EpisodeStore struct {
	db  *db.DB
	now func() time.Time
}

// NewEpisodeStore builds one.
func NewEpisodeStore(database *db.DB, now func() time.Time) *EpisodeStore {
	if now == nil {
		now = time.Now
	}
	return &EpisodeStore{db: database, now: now}
}

// SeasonInput is a season as a provider described it.
type SeasonInput struct {
	Number       int
	Name         string
	Overview     string
	EpisodeCount int
	Aired        time.Time
	Episodes     []EpisodeInput
}

// EpisodeInput is an episode as a provider described it.
type EpisodeInput struct {
	ProviderID int64
	Number     int
	Title      string
	Overview   string
	Aired      time.Time
	Runtime    int
}

// Upsert records what the provider says a series contains.
//
// The ONLY writer of these tables, and it takes provider data by construction:
// there is no argument by which a caller could pass it a file.
//
// seasons is the provider's COMPLETE list for the series. A season in it with
// nil Episodes was not asked about this time and keeps what it has; a season
// missing from it is gone from the provider.
//
// Episodes and seasons the provider no longer lists are DELETED. A renumbering
// or a merge otherwise leaves a phantom that sits on the wanted list forever and
// that no amount of downloading will satisfy. Deleting the row deletes the
// belief that the episode exists; it does not touch a file, and a file that
// matched it stays exactly where it is and simply stops being attributed to
// anything.
func (s *EpisodeStore) Upsert(ctx context.Context, itemID int64, seasons []SeasonInput) error {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return err
	}
	return s.db.InTx(ctx, func(tx db.Execer) error {
		return s.upsert(ctx, tx, itemID, seasons)
	})
}

// UpsertIn is Upsert inside a transaction the caller already holds.
//
// For adding a series (ADR-0025), where the item, its identification and every
// episode the provider lists are written together or not at all. The input is
// the same provider-only type, so this is no second way in for a row that did
// not come from a provider.
func (s *EpisodeStore) UpsertIn(ctx context.Context, tx db.Execer, itemID int64, seasons []SeasonInput) error {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return err
	}
	return s.upsert(ctx, tx, itemID, seasons)
}

func (s *EpisodeStore) upsert(ctx context.Context, tx db.Execer, itemID int64, seasons []SeasonInput) error {
	now := s.now().UTC().Format(episodeTimeLayout)

	// Whether the operator has stopped following this series: it has regular
	// seasons, and every one of them is unmonitored. Read once, before this
	// call adds anything, so the answer is about the operator's choices and
	// not about seasons this refresh is in the middle of creating.
	//
	// There is no series-level switch, so this is where "I have stopped
	// watching this" lives. It matters because a finished series is now asked
	// about weekly precisely so that a RENEWAL is noticed — and without this, a
	// show the operator had switched off season by season would come back
	// monitored the day it was renewed, and its new season would land on the
	// wanted list.
	var regular, following int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(monitored), 0) FROM season
		  WHERE item_id = ? AND number > 0`, itemID).Scan(&regular, &following); err != nil {
		return fmt.Errorf("library: reading monitoring: %w", err)
	}
	stoppedFollowing := regular > 0 && following == 0
	// And whether the operator has said not to take on new seasons at all
	// (ADR-0061).
	// Unscoped: the refresh writes for the whole library, and the item is the one being refreshed.
	var followNew int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE((SELECT follow_new_seasons FROM media_item WHERE id = ?), 1)`, itemID).Scan(&followNew); err != nil {
		return fmt.Errorf("library: reading monitoring: %w", err)
	}

	listed := make([]any, 0, len(seasons))
	for _, in := range seasons {
		if in.Number < 0 {
			continue
		}
		listed = append(listed, in.Number)

		// The default for a season seen for the FIRST time. Specials start
		// unmonitored — "everything ever released, including recap episodes and
		// convention panels" is not what following a show means — and so does
		// a new season of a series the operator has stopped following, or
		// whose new seasons they have said not to follow. Every other season
		// starts monitored. After that the operator's choice wins: the ON
		// CONFLICT clause below never touches it.
		monitored := 1
		if in.Number == 0 || stoppedFollowing || followNew == 0 {
			monitored = 0
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO season (item_id, number, name, overview, episode_count,
			                    aired_at, monitored, updated_at)
			VALUES (?,?,?,?,?,?,?,?)
			ON CONFLICT(item_id, number) DO UPDATE SET
				name          = excluded.name,
				overview      = excluded.overview,
				episode_count = excluded.episode_count,
				aired_at      = excluded.aired_at,
				updated_at    = excluded.updated_at`,
			itemID, in.Number, in.Name, in.Overview, in.EpisodeCount,
			nullableTime(in.Aired), monitored, now); err != nil {
			return fmt.Errorf("library: recording season %d: %w", in.Number, err)
		}

		// Read back the season's monitored flag as well as its id, and give it
		// to every NEW episode below.
		//
		// This is the reason seasons have a table (ADR-0022, decision 2), and
		// the first version wrote the flag and never read it: new episodes took
		// the default computed from the season NUMBER, so a season the operator
		// had unmonitored gained a monitored episode the day the provider
		// announced one, and it went straight onto the wanted list. The ADR,
		// the migration and this file's own comments all described the
		// inheritance. None of them implemented it; a test did.
		var seasonID int64
		var seasonMonitored int
		if err := tx.QueryRowContext(ctx,
			`SELECT id, monitored FROM season WHERE item_id = ? AND number = ?`,
			itemID, in.Number).Scan(&seasonID, &seasonMonitored); err != nil {
			return fmt.Errorf("library: reading back season %d: %w", in.Number, err)
		}

		// A season the caller did not fetch episodes for is left alone rather
		// than emptied. The refresh deliberately skips seasons that cannot have
		// changed, and treating "not asked about" as "has no episodes" would
		// delete a finished season's entire contents on every run.
		if in.Episodes == nil {
			continue
		}

		keep := make([]any, 0, len(in.Episodes))
		for _, e := range in.Episodes {
			if e.Number < 0 {
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO episode (item_id, season_id, season_number, number,
				                     title, overview, aired_at, runtime_minutes,
				                     provider_episode_id, monitored, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(item_id, season_number, number) DO UPDATE SET
					season_id           = excluded.season_id,
					title               = excluded.title,
					overview            = excluded.overview,
					aired_at            = excluded.aired_at,
					runtime_minutes     = excluded.runtime_minutes,
					provider_episode_id = excluded.provider_episode_id,
					updated_at          = excluded.updated_at`,
				itemID, seasonID, in.Number, e.Number, e.Title, e.Overview,
				nullableTime(e.Aired), e.Runtime, nullableID(e.ProviderID),
				seasonMonitored, now); err != nil {
				return fmt.Errorf("library: recording S%02dE%02d: %w",
					in.Number, e.Number, err)
			}
			keep = append(keep, e.Number)
		}

		// Everything else in this season is gone from the provider.
		if err := deleteMissing(ctx, tx, itemID, in.Number, keep); err != nil {
			return err
		}
	}

	// Seasons the provider no longer lists go the same way, for the same
	// reason: a merged or renumbered season left in place keeps its episodes
	// on the wanted list forever. Their episodes follow by ON DELETE CASCADE,
	// and no file is touched.
	//
	// ADR-0022 promised this ("a split that was merged") from the start, and
	// the first version pruned episodes within a season and never a season.
	if err := deleteUnlisted(ctx, tx, itemID, listed); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE media_item SET episodes_refreshed_at = ? WHERE id = ?`,
		now, itemID); err != nil {
		return fmt.Errorf("library: recording the refresh time: %w", err)
	}
	return nil
}

// deleteMissing removes episodes of a season the provider no longer lists.
func deleteMissing(ctx context.Context, tx db.Execer, itemID int64, season int, keep []any) error {
	if len(keep) == 0 {
		_, err := tx.ExecContext(ctx,
			`DELETE FROM episode WHERE item_id = ? AND season_number = ?`,
			itemID, season)
		if err != nil {
			return fmt.Errorf("library: pruning season %d: %w", season, err)
		}
		return nil
	}
	q := `DELETE FROM episode WHERE item_id = ? AND season_number = ? AND number NOT IN (?` +
		repeatPlaceholders(len(keep)-1) + `)`
	args := append([]any{itemID, season}, keep...)
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("library: pruning season %d: %w", season, err)
	}
	return nil
}

// deleteUnlisted removes a series' seasons that are not in listed.
//
// An EMPTY list removes nothing. A series losing every season in one answer is
// far more likely to be a provider answering badly than a show that stopped
// existing, and taking it literally would erase the operator's monitoring
// choices for the whole series in a single refresh — the same reason a scan
// refuses when most of a root vanishes at once.
func deleteUnlisted(ctx context.Context, tx db.Execer, itemID int64, listed []any) error {
	if len(listed) == 0 {
		return nil
	}
	q := `DELETE FROM season WHERE item_id = ? AND number NOT IN (?` +
		repeatPlaceholders(len(listed)-1) + `)`
	args := append([]any{itemID}, listed...)
	if _, err := tx.ExecContext(ctx, q, args...); err != nil {
		return fmt.Errorf("library: removing seasons the provider no longer lists: %w", err)
	}
	return nil
}

func repeatPlaceholders(n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += ",?"
	}
	return out
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(episodeTimeLayout)
}

func nullableID(id int64) any {
	if id <= 0 {
		return nil
	}
	return id
}

// Seasons returns a series' seasons with their episodes, and which of those
// episodes this instance holds.
func (s *EpisodeStore) Seasons(ctx context.Context, itemID int64) ([]Season, map[int][]Episode, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, nil, err
	}
	if err := requireVisible(ctx, s.db, itemID); err != nil {
		return nil, nil, err
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, item_id, number, name, overview, episode_count, aired_at, monitored
		FROM season WHERE item_id = ? ORDER BY number`, itemID)
	if err != nil {
		return nil, nil, fmt.Errorf("library: reading seasons: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var seasons []Season
	for rows.Next() {
		var sn Season
		var aired sql.NullString
		var monitored int
		if err := rows.Scan(&sn.ID, &sn.ItemID, &sn.Number, &sn.Name, &sn.Overview,
			&sn.EpisodeCount, &aired, &monitored); err != nil {
			return nil, nil, fmt.Errorf("library: reading seasons: %w", err)
		}
		sn.Aired = parseNullTime(aired)
		sn.Monitored = monitored == 1
		seasons = append(seasons, sn)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("library: reading seasons: %w", err)
	}

	eps, err := s.episodesOf(ctx, itemID)
	if err != nil {
		return nil, nil, err
	}
	bySeason := map[int][]Episode{}
	for _, e := range eps {
		bySeason[e.SeasonNumber] = append(bySeason[e.SeasonNumber], e)
	}
	return seasons, bySeason, nil
}

// episodeSelect is the join that makes "have" mean something.
//
// The BETWEEN is the whole reason there is no episode_id on media_file: a file
// can cover a range, and this attributes it to every episode in that range
// rather than to one of them.
const episodeSelect = `
	SELECT e.id, e.item_id, e.season_id, e.season_number, e.number, e.title,
	       e.overview, e.aired_at, e.runtime_minutes, e.provider_episode_id,
	       e.monitored,
	       COALESCE(f.id, 0), COALESCE(f.relative_path, '')
	FROM episode e
	LEFT JOIN media_file f
	       ON f.item_id = e.item_id
	      AND f.season  = e.season_number
	      AND e.number BETWEEN f.episode AND COALESCE(f.episode_last, f.episode)`

func (s *EpisodeStore) episodesOf(ctx context.Context, itemID int64) ([]Episode, error) {
	rows, err := s.db.QueryContext(ctx,
		episodeSelect+` WHERE e.item_id = ? ORDER BY e.season_number, e.number`, itemID)
	if err != nil {
		return nil, fmt.Errorf("library: reading episodes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanEpisodes(rows)
}

func scanEpisodes(rows *sql.Rows) ([]Episode, error) {
	var out []Episode
	for rows.Next() {
		var e Episode
		var aired, path sql.NullString
		var providerID sql.NullInt64
		var monitored int
		if err := rows.Scan(&e.ID, &e.ItemID, &e.SeasonID, &e.SeasonNumber, &e.Number,
			&e.Title, &e.Overview, &aired, &e.RuntimeMinutes, &providerID,
			&monitored, &e.HaveFileID, &path); err != nil {
			return nil, fmt.Errorf("library: reading episodes: %w", err)
		}
		e.Aired = parseNullTime(aired)
		e.ProviderEpisode = providerID.Int64
		e.Monitored = monitored == 1
		e.HavePath = path.String
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("library: reading episodes: %w", err)
	}
	return out, nil
}

func parseNullTime(v sql.NullString) time.Time {
	if !v.Valid || v.String == "" {
		return time.Time{}
	}
	t, err := time.Parse(episodeTimeLayout, v.String)
	if err != nil {
		return time.Time{}
	}
	return t
}

// WantedEpisode is one episode an instance should have and does not.
type WantedEpisode struct {
	Episode
	// SeriesTitle is carried because a wanted list is read across the whole
	// library, where an episode number on its own means nothing.
	SeriesTitle string
}

// Wanted returns every monitored episode that has aired and is not held.
//
// Three conditions, and the second is the one that matters:
//
//   - monitored, at the episode level (a season toggle writes through to them)
//   - aired: a date the provider actually gave, and in the past. An episode
//     with NO date is announced, not overdue, and putting it here would fill the
//     list with every unannounced episode of every running show on day one
//   - not held by any file, by the same range-aware join as everywhere else
func (s *EpisodeStore) Wanted(ctx context.Context, limit int) ([]WantedEpisode, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	visible, vargs := Visible(ctx, "i")

	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.item_id, e.season_id, e.season_number, e.number, e.title,
		       e.overview, e.aired_at, e.runtime_minutes, e.provider_episode_id,
		       e.monitored, 0, '', i.title
		FROM episode e
		JOIN media_item i ON i.id = e.item_id
		WHERE `+visible+`
		  AND e.monitored = 1
		  AND e.aired_at IS NOT NULL
		  AND e.aired_at <= ?
		  AND NOT EXISTS (
		        SELECT 1 FROM media_file f
		        WHERE f.item_id = e.item_id
		          AND f.season  = e.season_number
		          AND e.number BETWEEN f.episode AND COALESCE(f.episode_last, f.episode))
		ORDER BY e.aired_at DESC, e.item_id, e.season_number, e.number
		LIMIT ?`, append(vargs, s.now().UTC().Format(episodeTimeLayout), limit)...)
	if err != nil {
		return nil, fmt.Errorf("library: reading the wanted list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []WantedEpisode
	for rows.Next() {
		var e Episode
		var aired, path sql.NullString
		var providerID sql.NullInt64
		var monitored int
		var seriesTitle string
		if err := rows.Scan(&e.ID, &e.ItemID, &e.SeasonID, &e.SeasonNumber, &e.Number,
			&e.Title, &e.Overview, &aired, &e.RuntimeMinutes, &providerID,
			&monitored, &e.HaveFileID, &path, &seriesTitle); err != nil {
			return nil, fmt.Errorf("library: reading the wanted list: %w", err)
		}
		e.Aired = parseNullTime(aired)
		e.ProviderEpisode = providerID.Int64
		e.Monitored = monitored == 1
		out = append(out, WantedEpisode{Episode: e, SeriesTitle: seriesTitle})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("library: reading the wanted list: %w", err)
	}
	return out, nil
}

// SetSeasonMonitored turns a season on or off, and every episode in it.
//
// Both, in one transaction. A season row that says "not monitored" over
// episodes that say otherwise is a disagreement the wanted query would resolve
// in favour of the episodes, which is the opposite of what the operator just
// asked for.
func (s *EpisodeStore) SetSeasonMonitored(ctx context.Context, itemID, seasonNumber int64, on bool) error {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return err
	}
	flag := 0
	if on {
		flag = 1
	}
	now := s.now().UTC().Format(episodeTimeLayout)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("library: setting monitoring: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireVisible(ctx, tx, itemID); err != nil {
		return err
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE season SET monitored = ?, updated_at = ? WHERE item_id = ? AND number = ?`,
		flag, now, itemID, seasonNumber)
	if err != nil {
		return fmt.Errorf("library: setting monitoring: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: season %d of item %d", ErrNotFound, seasonNumber, itemID)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE episode SET monitored = ?, updated_at = ? WHERE item_id = ? AND season_number = ?`,
		flag, now, itemID, seasonNumber); err != nil {
		return fmt.Errorf("library: setting monitoring: %w", err)
	}
	return tx.Commit()
}

// SeriesFlag is one of a series' own switches.
type SeriesFlag int

const (
	// FollowNewSeasons: a season first listed after now starts monitored
	// (ADR-0061).
	FollowNewSeasons SeriesFlag = iota
	// SeasonFolders: an episode is filed in its season's folder (ADR-0063).
	SeasonFolders
	// Daily: the series is released by date, and searched by date (ADR-0064).
	Daily
)

// seriesFlagSQL is each switch's statement: a fixed list, so no column name
// is ever put together from a value.
var seriesFlagSQL = map[SeriesFlag]string{
	FollowNewSeasons: `UPDATE media_item SET follow_new_seasons = ?, updated_at = ? WHERE id = ? AND kind = 'series'`,
	SeasonFolders:    `UPDATE media_item SET season_folders = ?, updated_at = ? WHERE id = ? AND kind = 'series'`,
	Daily:            `UPDATE media_item SET daily = ?, updated_at = ? WHERE id = ? AND kind = 'series'`,
}

// SetSeriesFlag turns one of a series' switches on or off. Anything but a
// visible series is not found.
func (s *EpisodeStore) SetSeriesFlag(ctx context.Context, itemID int64, flag SeriesFlag, on bool) error {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return err
	}
	stmt, ok := seriesFlagSQL[flag]
	if !ok {
		return fmt.Errorf("library: no series switch %d", flag)
	}
	value := 0
	if on {
		value = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("library: setting a series switch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireVisible(ctx, tx, itemID); err != nil {
		return err
	}
	// Unscoped: requireVisible above has just checked this item is the caller's to see.
	res, err := tx.ExecContext(ctx, stmt, value, s.now().UTC().Format(episodeTimeLayout), itemID)
	if err != nil {
		return fmt.Errorf("library: setting a series switch: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: item %d is not a series", ErrNotFound, itemID)
	}
	return tx.Commit()
}

// SeriesSettings are a series' own switches.
type SeriesSettings struct {
	FollowNewSeasons bool
	SeasonFolders    bool
	Daily            bool
}

// SeriesSettings reads a visible series' switches.
func (s *EpisodeStore) SeriesSettings(ctx context.Context, itemID int64) (SeriesSettings, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return SeriesSettings{}, err
	}
	clause, args := Visible(ctx, "")
	var follow, folders, daily int
	err := s.db.QueryRowContext(ctx, `SELECT follow_new_seasons, season_folders, daily FROM media_item
		WHERE id = ? AND kind = 'series' AND `+clause, append([]any{itemID}, args...)...).Scan(&follow, &folders, &daily)
	if errors.Is(err, sql.ErrNoRows) {
		return SeriesSettings{}, ErrNotFound
	}
	if err != nil {
		return SeriesSettings{}, fmt.Errorf("library: reading a series' switches: %w", err)
	}
	return SeriesSettings{FollowNewSeasons: follow == 1, SeasonFolders: folders == 1, Daily: daily == 1}, nil
}

// SetEpisodeMonitored turns one episode on or off.
func (s *EpisodeStore) SetEpisodeMonitored(ctx context.Context, episodeID int64, on bool) error {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return err
	}
	flag := 0
	if on {
		flag = 1
	}
	visible, vargs := Visible(ctx, "")
	res, err := s.db.ExecContext(ctx,
		`UPDATE episode SET monitored = ?, updated_at = ? WHERE id = ?
		   AND item_id IN (SELECT id FROM media_item WHERE `+visible+`)`,
		append([]any{flag, s.now().UTC().Format(episodeTimeLayout), episodeID}, vargs...)...)
	if err != nil {
		return fmt.Errorf("library: setting monitoring: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: episode %d", ErrNotFound, episodeID)
	}
	return nil
}

// Monitoring is which of a series' episodes are wanted, chosen when the series
// is added (ADR-0025, decision 7).
//
// Each choice is expressed with the season and episode flags that already
// exist, and each leaves Upsert's renewal rule doing what was chosen: a series
// with any regular season on follows a renewal, one with every regular season
// off does not. There is no series-level switch (ADR-0022).
type Monitoring string

const (
	// MonitorAll wants every episode that has aired.
	MonitorAll Monitoring = "all"
	// MonitorFuture wants what is still to come. The latest season that has
	// aired stays ON — switching it off would read as "stopped following" and
	// a renewal would arrive off — with its aired episodes off; earlier seasons
	// are off.
	MonitorFuture Monitoring = "future"
	// MonitorLatest wants the latest season that has aired, and everything
	// after it.
	MonitorLatest Monitoring = "latest"
	// MonitorNone wants nothing: the series is tracked, and a renewal arrives
	// off.
	MonitorNone Monitoring = "none"
)

// ErrNoSuchMonitoring refuses anything but the four choices.
var ErrNoSuchMonitoring = errors.New("library: monitoring must be all, future, latest or none")

// ParseMonitoring reads a choice. An empty one is refused rather than given a
// default: "all" puts a whole back catalogue on the wanted list and "future"
// quietly leaves it off, and neither is safe to assume on somebody's behalf.
func ParseMonitoring(s string) (Monitoring, error) {
	switch m := Monitoring(strings.ToLower(strings.TrimSpace(s))); m {
	case MonitorAll, MonitorFuture, MonitorLatest, MonitorNone:
		return m, nil
	}
	return "", fmt.Errorf("%w; got %q", ErrNoSuchMonitoring, s)
}

// MonitoringResult is what a series looks like once a choice is applied.
type MonitoringResult struct {
	Seasons   int
	Episodes  int
	Monitored int
	// Wanted is monitored, aired and not held — the wanted list's own rule.
	Wanted int
	// Latest is the latest regular season with an episode that has aired, or
	// zero when none has.
	Latest int
}

// ApplyMonitoring sets every season and episode flag of a series to a choice.
//
// EVERY flag, including the ones a choice leaves on, so the outcome depends on
// the choice and the provider's data and on nothing the flags held before.
// Specials go off under every choice (ADR-0022, decision 5); an operator can
// turn them on afterwards like any season.
//
// In the caller's transaction, because it is the last step of adding a series
// and the add is all or nothing (ADR-0025, decision 6).
func (s *EpisodeStore) ApplyMonitoring(ctx context.Context, tx db.Execer, itemID int64, m Monitoring) (MonitoringResult, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return MonitoringResult{}, err
	}
	if _, err := ParseMonitoring(string(m)); err != nil {
		return MonitoringResult{}, err
	}
	now := s.now().UTC()
	nowText := now.Format(episodeTimeLayout)
	// An air date is a date. One dated today counts as still to come: the
	// provider's day is usually the broadcast day in the origin country, which
	// is often already yesterday wherever this server is.
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Format(episodeTimeLayout)

	var res MonitoringResult
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(season_number), 0) FROM episode
		WHERE item_id = ? AND season_number > 0
		  AND aired_at IS NOT NULL AND aired_at <= ?`, itemID, nowText).Scan(&res.Latest); err != nil {
		return res, fmt.Errorf("library: finding the latest season: %w", err)
	}

	// following is whether any regular season is on; from is the first one
	// that is. from is never below 1, and that is the whole of the specials
	// rule: season 0 is below it under every choice, so it goes off.
	following, from := 1, 1
	switch m {
	case MonitorNone:
		following = 0
	case MonitorLatest, MonitorFuture:
		if res.Latest > 0 {
			from = res.Latest
		}
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE season SET
			monitored  = CASE WHEN ? = 1 AND number >= ? THEN 1 ELSE 0 END,
			updated_at = ?
		WHERE item_id = ?`, following, from, nowText, itemID); err != nil {
		return res, fmt.Errorf("library: applying monitoring to seasons: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE episode SET
			monitored  = CASE WHEN ? = 1 AND season_number >= ? THEN 1 ELSE 0 END,
			updated_at = ?
		WHERE item_id = ?`, following, from, nowText, itemID); err != nil {
		return res, fmt.Errorf("library: applying monitoring to episodes: %w", err)
	}
	if m == MonitorFuture {
		// Off at the episode level only; the season stays on, which is what
		// keeps a renewal followed.
		if _, err := tx.ExecContext(ctx, `
			UPDATE episode SET monitored = 0, updated_at = ?
			WHERE item_id = ? AND aired_at IS NOT NULL AND aired_at < ?`,
			nowText, itemID, today); err != nil {
			return res, fmt.Errorf("library: applying monitoring to aired episodes: %w", err)
		}
	}

	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM season WHERE item_id = ?`, itemID).Scan(&res.Seasons); err != nil {
		return res, fmt.Errorf("library: counting seasons: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(e.monitored), 0),
		       COALESCE(SUM(CASE WHEN e.monitored = 1
		                          AND e.aired_at IS NOT NULL AND e.aired_at <= ?
		                          AND NOT EXISTS (
		                                SELECT 1 FROM media_file f
		                                WHERE f.item_id = e.item_id
		                                  AND f.season  = e.season_number
		                                  AND e.number BETWEEN f.episode AND COALESCE(f.episode_last, f.episode))
		                         THEN 1 ELSE 0 END), 0)
		FROM episode e WHERE e.item_id = ?`, nowText, itemID).Scan(
		&res.Episodes, &res.Monitored, &res.Wanted); err != nil {
		return res, fmt.Errorf("library: counting episodes: %w", err)
	}
	return res, nil
}

// ErrNoSuchEpisode means the episode does not exist.
var ErrNoSuchEpisode = errors.New("library: no such episode")

// SearchSubject is what a search for one episode needs: the episode, and the
// series it belongs to.
type SearchSubject struct {
	Episode     Episode
	SeriesTitle string
	// SeriesYear is the series' first-air year, zero when unknown.
	SeriesYear int
	// TMDBID is zero for a series that has not been identified — which cannot
	// have episodes, since episodes come from the provider, but is checked
	// rather than assumed by the caller.
	TMDBID int64
	// QualityProfileID is the series' own profile; zero is the default
	// (ADR-0035).
	QualityProfileID int64
	// Daily says the series is searched by air date (ADR-0064).
	Daily bool
}

// ForSearch returns one episode and its series.
func (s *EpisodeStore) ForSearch(ctx context.Context, episodeID int64) (SearchSubject, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return SearchSubject{}, err
	}
	var sub SearchSubject
	e := &sub.Episode
	var aired, path sql.NullString
	var providerID sql.NullInt64
	var monitored int
	visible, vargs := Visible(ctx, "i")
	err := s.db.QueryRowContext(ctx, `
		SELECT e.id, e.item_id, e.season_id, e.season_number, e.number, e.title,
		       e.overview, e.aired_at, e.runtime_minutes, e.provider_episode_id,
		       e.monitored,
		       COALESCE(f.id, 0), COALESCE(f.relative_path, ''),
		       i.title, COALESCE(i.year, 0), COALESCE(i.tmdb_id, 0),
		       COALESCE(i.quality_profile_id, 0), i.daily
		FROM episode e
		JOIN media_item i ON i.id = e.item_id
		LEFT JOIN media_file f
		       ON f.item_id = e.item_id
		      AND f.season  = e.season_number
		      AND e.number BETWEEN f.episode AND COALESCE(f.episode_last, f.episode)
		WHERE e.id = ? AND `+visible+`
		LIMIT 1`, append([]any{episodeID}, vargs...)...).Scan(
		&e.ID, &e.ItemID, &e.SeasonID, &e.SeasonNumber, &e.Number, &e.Title,
		&e.Overview, &aired, &e.RuntimeMinutes, &providerID, &monitored,
		&e.HaveFileID, &path, &sub.SeriesTitle, &sub.SeriesYear, &sub.TMDBID, &sub.QualityProfileID, &sub.Daily)
	if errors.Is(err, sql.ErrNoRows) {
		return SearchSubject{}, ErrNoSuchEpisode
	}
	if err != nil {
		return SearchSubject{}, fmt.Errorf("library: reading an episode: %w", err)
	}
	e.Aired = parseNullTime(aired)
	e.ProviderEpisode = providerID.Int64
	e.Monitored = monitored == 1
	e.HavePath = path.String
	return sub, nil
}

// RefreshPolicy says which series a scheduled refresh asks about.
type RefreshPolicy struct {
	// Recent: a series with an episode that aired within this long, or one
	// still to air, can change at any moment and is offered on every run.
	Recent time.Duration

	// Every: every other identified series is offered at least this often.
	//
	// The first version had no floor, on the reasoning that a finished series
	// cannot change. It can: it can be RENEWED. A show between seasons — for
	// most shows a gap of a year or more — has no recent episode and nothing
	// still to air, so it was never asked about again and its next season
	// would never have appeared. Noticing that is most of what following a
	// show means.
	Every time.Duration
}

// ErrNoRefreshPolicy refuses a policy with a zero duration in it. A zero Every
// would offer every series on every run, which is the rate-limit spend this
// selection exists to prevent, and it is the value a caller gets by forgetting.
var ErrNoRefreshPolicy = errors.New("library: a refresh policy needs both durations")

// SeriesNeedingRefresh returns the series worth asking the provider about.
//
// A series is offered when it has never been asked about; when it has an
// episode still to air or one that aired within policy.Recent; when it holds a
// season with no episodes in it — an ANNOUNCED season, which is exactly the
// one about to gain some, or a refresh that failed part-way; or when it was
// last asked about longer ago than policy.Every. Anything else is skipped this
// run, and the least recently asked go first.
func (s *EpisodeStore) SeriesNeedingRefresh(ctx context.Context, policy RefreshPolicy, limit int) ([]int64, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	if policy.Recent <= 0 || policy.Every <= 0 {
		return nil, ErrNoRefreshPolicy
	}
	if limit <= 0 {
		limit = 50
	}
	now := s.now().UTC()
	recent := now.Add(-policy.Recent).Format(episodeTimeLayout)
	stale := now.Add(-policy.Every).Format(episodeTimeLayout)

	// Unscoped: the scheduled refresh keeps every series current, whoever can
	// see it.
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id FROM media_item i
		WHERE i.kind = 'series' AND i.tmdb_id IS NOT NULL AND i.tmdb_id > 0
		  AND (
		        i.episodes_refreshed_at IS NULL
		     OR i.episodes_refreshed_at < ?
		     OR EXISTS (SELECT 1 FROM episode e WHERE e.item_id = i.id
		                 AND (e.aired_at IS NULL OR e.aired_at >= ?))
		     OR EXISTS (SELECT 1 FROM season sn WHERE sn.item_id = i.id
		                 AND NOT EXISTS (SELECT 1 FROM episode e
		                                  WHERE e.season_id = sn.id))
		  )
		ORDER BY COALESCE(i.episodes_refreshed_at, '') ASC
		LIMIT ?`, stale, recent, limit)
	if err != nil {
		return nil, fmt.Errorf("library: choosing series to refresh: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("library: choosing series to refresh: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("library: choosing series to refresh: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

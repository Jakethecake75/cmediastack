package acquire

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/release"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// timeLayout is how this package writes times: the library's layout.
const timeLayout = time.RFC3339Nano

// episodeTimeLayout is how an episode's air date is stored, and compared.
const episodeTimeLayout = "2006-01-02T15:04:05Z"

// maxRange bounds the episodes one release is taken to cover. A release name is
// written by an uploader, and "S01E01-E9999" must not become ten thousand keys.
const maxRange = 50

// Store reads what is wanted and what is in flight, and keeps what each search
// found.
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

// The Wanted screen's two conditions, written once here for the whole list and
// once for a single item (StillWanted). The screen's own queries are capped for
// display, and a pass must see everything; TestTheWantedListIsTheWantedScreens
// holds this package to the same answer as the screen.
const (
	wantedEpisodeWhere = `
		e.monitored = 1
		AND e.aired_at IS NOT NULL
		AND e.aired_at <= ?
		AND NOT EXISTS (
		      SELECT 1 FROM media_file f
		      WHERE f.item_id = e.item_id
		        AND f.season  = e.season_number
		        AND e.number BETWEEN f.episode AND COALESCE(f.episode_last, f.episode))`
	wantedFilmWhere = `
		i.kind = 'movie'
		AND i.monitored = 1
		AND NOT EXISTS (SELECT 1 FROM media_file f WHERE f.item_id = i.id)`
)

// Wanted returns everything the Wanted screen lists, without its limits:
// monitored episodes that have aired and are not on disk (ADR-0022), and
// monitored films with no file (ADR-0030).
func (s *Store) Wanted(ctx context.Context) ([]Want, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	episodes, err := s.wantedEpisodes(ctx)
	if err != nil {
		return nil, err
	}
	films, err := s.wantedFilms(ctx)
	if err != nil {
		return nil, err
	}
	return append(episodes, films...), nil
}

func (s *Store) wantedEpisodes(ctx context.Context) ([]Want, error) {
	// Unscoped: automatic acquisition works for the whole Wanted list (ADR-0030), not for one person's view of it.
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.item_id, e.season_number, e.number, e.aired_at,
		       i.title, COALESCE(i.year, 0), COALESCE(i.tmdb_id, 0),
		       COALESCE(i.quality_profile_id, 0), i.daily
		FROM episode e
		JOIN media_item i ON i.id = e.item_id
		WHERE`+wantedEpisodeWhere+`
		ORDER BY e.aired_at DESC, e.item_id, e.season_number, e.number`,
		s.airedBy())
	if err != nil {
		return nil, fmt.Errorf("acquire: reading the wanted episodes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Want
	for rows.Next() {
		var w Want
		var aired string
		if err := rows.Scan(&w.EpisodeID, &w.ItemID, &w.Season, &w.Episode, &aired,
			&w.Title, &w.Year, &w.TMDBID, &w.ProfileID, &w.Daily); err != nil {
			return nil, fmt.Errorf("acquire: reading the wanted episodes: %w", err)
		}
		w.When, _ = time.Parse(episodeTimeLayout, aired)
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("acquire: reading the wanted episodes: %w", err)
	}
	return out, nil
}

func (s *Store) wantedFilms(ctx context.Context) ([]Want, error) {
	// Unscoped: automatic acquisition works for the whole Wanted list (ADR-0030), not for one person's view of it.
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.title, COALESCE(i.year, 0), COALESCE(i.tmdb_id, 0), i.added_at,
		       COALESCE(i.quality_profile_id, 0)
		FROM media_item i
		WHERE`+wantedFilmWhere+`
		ORDER BY i.added_at DESC, i.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("acquire: reading the wanted films: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Want
	for rows.Next() {
		w := Want{Film: true}
		var added string
		if err := rows.Scan(&w.ItemID, &w.Title, &w.Year, &w.TMDBID, &added, &w.ProfileID); err != nil {
			return nil, fmt.Errorf("acquire: reading the wanted films: %w", err)
		}
		w.When, _ = time.Parse(timeLayout, added)
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("acquire: reading the wanted films: %w", err)
	}
	return out, nil
}

// Upgradable is every monitored episode and film whose file could be upgraded
// (ADR-0036): the file held, as a release, for the profile to compare against.
// Whether it is below its cutoff is the profile's question, asked by the pass.
// A file covering more than one episode is left out: a release of one of them
// would supersede the file both are in.
func (s *Store) Upgradable(ctx context.Context) ([]Want, error) {
	// Unscoped: upgrades are for the whole library (ADR-0036), whoever can see a title.
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	out, err := s.upgradableEpisodes(ctx)
	if err != nil {
		return nil, err
	}

	films, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.title, COALESCE(i.year, 0), COALESCE(i.tmdb_id, 0), i.added_at,
		       COALESCE(i.quality_profile_id, 0), f.quality, f.release_title, f.revision
		FROM media_item i
		JOIN media_file f ON f.item_id = i.id
		WHERE i.kind = 'movie' AND i.monitored = 1
		ORDER BY i.id`)
	if err != nil {
		return nil, fmt.Errorf("acquire: reading what could be upgraded: %w", err)
	}
	defer func() { _ = films.Close() }()
	for films.Next() {
		w := Want{Film: true, Upgrade: true}
		var added, quality, title string
		var revision int
		if err := films.Scan(&w.ItemID, &w.Title, &w.Year, &w.TMDBID, &added, &w.ProfileID,
			&quality, &title, &revision); err != nil {
			return nil, fmt.Errorf("acquire: reading what could be upgraded: %w", err)
		}
		w.When, _ = time.Parse(timeLayout, added)
		w.Have = held(quality, title, revision)
		out = append(out, w)
	}
	return out, films.Err()
}

// upgradableEpisodes is Upgradable's episodes: each monitored episode with a
// file of its own.
func (s *Store) upgradableEpisodes(ctx context.Context) ([]Want, error) {
	// Unscoped: upgrades are for the whole library (ADR-0036), whoever can see a title.
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.item_id, e.season_number, e.number, e.aired_at,
		       i.title, COALESCE(i.year, 0), COALESCE(i.tmdb_id, 0), COALESCE(i.quality_profile_id, 0),
		       f.quality, f.release_title, f.revision
		FROM episode e
		JOIN media_item i ON i.id = e.item_id
		JOIN media_file f ON f.item_id = e.item_id AND f.season = e.season_number
		                 AND f.episode = e.number AND COALESCE(f.episode_last, f.episode) = f.episode
		WHERE e.monitored = 1
		ORDER BY e.item_id, e.season_number, e.number`)
	if err != nil {
		return nil, fmt.Errorf("acquire: reading what could be upgraded: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Want
	for rows.Next() {
		w := Want{Upgrade: true}
		var aired sql.NullString
		var quality, title string
		var revision int
		if err := rows.Scan(&w.EpisodeID, &w.ItemID, &w.Season, &w.Episode, &aired,
			&w.Title, &w.Year, &w.TMDBID, &w.ProfileID, &quality, &title, &revision); err != nil {
			return nil, fmt.Errorf("acquire: reading what could be upgraded: %w", err)
		}
		w.When, _ = time.Parse(episodeTimeLayout, aired.String)
		w.Have = held(quality, title, revision)
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("acquire: reading what could be upgraded: %w", err)
	}
	return out, nil
}

// held is a file in the library as the release a profile compares against:
// the quality the import recorded — not re-read from the name, which for a
// file from a pack is the pack's — its revision, and its release name for the
// preferred terms.
func held(quality, title string, revision int) release.Parsed {
	p := release.Parsed{Raw: title, Revision: revision, Season: -1}
	for _, q := range release.DefaultLadder {
		if strings.EqualFold(q.Name, quality) {
			p.Source, p.Resolution = q.Source, q.Resolution
			break
		}
	}
	return p
}

// StillUpgradable asks again, just before an upgrade's grab, whether the item
// is still monitored and still holds a file.
func (s *Store) StillUpgradable(ctx context.Context, w Want) (bool, error) {
	// Unscoped: a check automatic acquisition makes before a grab, for the whole library.
	var n int
	var err error
	switch {
	case w.Album > 0:
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM album a WHERE a.id = ? AND`+upgradableAlbumWhere,
			w.Album).Scan(&n)
	case w.Film:
		err = s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM media_item i WHERE i.id = ? AND i.monitored = 1
			  AND EXISTS (SELECT 1 FROM media_file f WHERE f.item_id = i.id)`, w.ItemID).Scan(&n)
	default:
		err = s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM episode e WHERE e.id = ? AND e.monitored = 1
			  AND EXISTS (SELECT 1 FROM media_file f WHERE f.item_id = e.item_id
			              AND f.season = e.season_number AND f.episode = e.number)`, w.EpisodeID).Scan(&n)
	}
	if err != nil {
		return false, fmt.Errorf("acquire: checking %s can still be upgraded: %w", w.Name(), err)
	}
	return n > 0, nil
}

// StillWanted asks again about one item, just before a grab. A search takes
// seconds, and an episode a person unmonitored while it ran must not be
// fetched on the strength of a list read before they did.
func (s *Store) StillWanted(ctx context.Context, w Want) (bool, error) {
	// Unscoped: a check automatic acquisition makes before a grab, for the whole library.
	var n int
	var err error
	switch {
	case w.Album > 0:
		err = s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM album a WHERE a.id = ? AND`+wantedAlbumWhere,
			w.Album, s.releasedBy()).Scan(&n)
	case w.Book:
		err = s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM media_item i WHERE i.id = ? AND`+wantedBookWhere,
			w.ItemID).Scan(&n)
	case w.Film:
		err = s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM media_item i WHERE i.id = ? AND`+wantedFilmWhere,
			w.ItemID).Scan(&n)
	default:
		err = s.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM episode e WHERE e.id = ? AND`+wantedEpisodeWhere,
			w.EpisodeID, s.airedBy()).Scan(&n)
	}
	if err != nil {
		return false, fmt.Errorf("acquire: checking %s is still wanted: %w", w.Name(), err)
	}
	return n > 0, nil
}

// airedBy is "now" as an episode's air date is compared.
func (s *Store) airedBy() string { return s.now().UTC().Format(episodeTimeLayout) }

// InFlight returns the items a download is already under way for (ADR-0030,
// decision 3): queued, downloading, or finished and not yet imported.
//
// Finished and not imported has one exception. When the import looked at the
// download and SKIPPED it — not the episode it was grabbed for, a disc image,
// nothing playable — the release is the problem, so the item is wanted again
// and the next pass looks for another (the queue keeps this one from being
// grabbed again: Queued). An import that FAILED is different: a root folder
// missing, a disk full. That is the instance's problem, the importer retries
// it, and grabbing another release would fail the same way — so the item stays
// in flight until a person fixes it or removes the download.
//
// An episode download covers every episode its release name spans, so a
// double episode grabbed for E01 keeps E02 from being grabbed separately.
func (s *Store) InFlight(ctx context.Context) (map[Key]bool, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT q.target_kind, q.target_item_id,
		       COALESCE(q.target_season, 0), COALESCE(q.target_episode, 0), q.title,
		       COALESCE(q.target_album_id, 0)
		FROM download_queue q
		WHERE q.target_item_id IS NOT NULL
		  AND COALESCE(q.target_kind, '') <> 'season'
		  AND (q.status IN ('queued', 'downloading')
		       OR (q.status = 'complete'
		           AND NOT EXISTS (SELECT 1 FROM import_record r
		                           WHERE r.info_hash = q.info_hash AND r.outcome = 'imported')
		           AND COALESCE((SELECT r.outcome FROM import_record r
		                         WHERE r.info_hash = q.info_hash
		                         ORDER BY r.occurred_at DESC, r.id DESC LIMIT 1), '') <> 'skipped'))`)
	if err != nil {
		return nil, fmt.Errorf("acquire: reading the queue: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[Key]bool{}
	for rows.Next() {
		var kind sql.NullString
		var k Key
		var title string
		var album int64
		if err := rows.Scan(&kind, &k.ItemID, &k.Season, &k.Episode, &title, &album); err != nil {
			return nil, fmt.Errorf("acquire: reading the queue: %w", err)
		}
		if kind.String == "album" {
			// An album download holds the album and no episode (ADR-0047).
			if album > 0 {
				out[Key{Album: album}] = true
			}
			continue
		}
		if kind.String == "book" {
			// A book download holds the book, never an episode of its item
			// (ADR-0050, decision 4).
			out[Key{Book: true, ItemID: k.ItemID}] = true
			continue
		}
		if kind.String == "film" {
			out[Key{Film: true, ItemID: k.ItemID}] = true
			continue
		}
		for _, c := range covered(release.Parse(title), k) {
			out[c] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("acquire: reading the queue: %w", err)
	}
	if err := s.seasonsInFlight(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

// seasonsInFlight adds every episode of each season a download is under way
// for (ADR-0033, decision 6): queued, downloading, or finished and its import
// not done. A pack's import is done when every file has been imported or
// skipped; while any file is failing, or nothing has been recorded, it holds
// the season. What a done pack did not hold is wanted again.
func (s *Store) seasonsInFlight(ctx context.Context, out map[Key]bool) error {
	all, err := s.seasonDownloads(ctx)
	if err != nil {
		return err
	}
	for _, h := range all {
		if h.status == "complete" {
			done, err := s.packImported(ctx, h.hash)
			if err != nil {
				return err
			}
			if done {
				continue
			}
		}
		numbers, err := s.seasonNumbers(ctx, h.sk)
		if err != nil {
			return err
		}
		for _, n := range numbers {
			out[Key{ItemID: h.sk.ItemID, Season: h.sk.Season, Episode: n}] = true
		}
	}
	return nil
}

// seasonDownload is a queue row grabbed for a whole season.
type seasonDownload struct {
	hash, status string
	sk           seasonKey
}

// seasonDownloads reads the queue's season rows that may hold their season.
// Read in full before anything else is asked, so the next queries do not run
// with this one still open.
func (s *Store) seasonDownloads(ctx context.Context) ([]seasonDownload, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT q.info_hash, q.status, q.target_item_id, q.target_season,
		       COALESCE(q.target_last_season, q.target_season)
		FROM download_queue q
		WHERE q.target_kind = 'season' AND q.target_item_id IS NOT NULL
		  AND q.target_season IS NOT NULL
		  AND q.status IN ('queued', 'downloading', 'complete')`)
	if err != nil {
		return nil, fmt.Errorf("acquire: reading the queue's seasons: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var all []seasonDownload
	for rows.Next() {
		var h seasonDownload
		var last int
		if err := rows.Scan(&h.hash, &h.status, &h.sk.ItemID, &h.sk.Season, &last); err != nil {
			return nil, fmt.Errorf("acquire: reading the queue's seasons: %w", err)
		}
		// A pack of several seasons holds each of them (ADR-0057, decision 5).
		for ; h.sk.Season <= last; h.sk.Season++ {
			all = append(all, h)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("acquire: reading the queue's seasons: %w", err)
	}
	return all, nil
}

// packImported reports whether a pack's import is done: every file's latest
// record imported or skipped, or the whole download skipped. A record for the
// whole download counts only while it is the newest.
func (s *Store) packImported(ctx context.Context, hash string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_path, outcome FROM import_record
		WHERE info_hash = ? ORDER BY occurred_at DESC, id DESC`, hash)
	if err != nil {
		return false, fmt.Errorf("acquire: reading a pack's import: %w", err)
	}
	defer func() { _ = rows.Close() }()
	latest := map[string]string{}
	first := true
	for rows.Next() {
		var source, outcome string
		if err := rows.Scan(&source, &outcome); err != nil {
			return false, fmt.Errorf("acquire: reading a pack's import: %w", err)
		}
		if first {
			first = false
			if source == "" {
				return outcome == "skipped", nil
			}
		}
		if _, seen := latest[source]; !seen && source != "" {
			latest[source] = outcome
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("acquire: reading a pack's import: %w", err)
	}
	if len(latest) == 0 {
		return false, nil
	}
	for _, outcome := range latest {
		if outcome == "failed" {
			return false, nil
		}
	}
	return true, nil
}

// seasonNumbers is the episode numbers the provider lists for one season.
func (s *Store) seasonNumbers(ctx context.Context, sk seasonKey) ([]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT number FROM episode WHERE item_id = ? AND season_number = ? ORDER BY number`,
		sk.ItemID, sk.Season)
	if err != nil {
		return nil, fmt.Errorf("acquire: reading a season's episodes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("acquire: reading a season's episodes: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// settledFor is how long after its last episode aired a season is settled:
// the week the episode refresh also waits before it stops asking about a
// season (tv.Refresher), because titles, numbering and late additions settle
// in that time.
const settledFor = 7 * 24 * time.Hour

// settledKeys is which seasons are settled, read in full before their
// episodes are asked for.
func (s *Store) settledKeys(ctx context.Context) (map[seasonKey]bool, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.item_id, s.number, s.episode_count, COUNT(e.id), COUNT(e.aired_at), MAX(e.aired_at)
		FROM season s JOIN episode e ON e.season_id = s.id
		WHERE s.number > 0
		GROUP BY s.id`)
	if err != nil {
		return nil, fmt.Errorf("acquire: reading which seasons are settled: %w", err)
	}
	defer func() { _ = rows.Close() }()
	settled := map[seasonKey]bool{}
	cutoff := s.now().UTC().Add(-settledFor)
	for rows.Next() {
		var sk seasonKey
		var declared, listed, dated int
		var last sql.NullString
		if err := rows.Scan(&sk.ItemID, &sk.Season, &declared, &listed, &dated, &last); err != nil {
			return nil, fmt.Errorf("acquire: reading which seasons are settled: %w", err)
		}
		if listed == 0 || listed != declared || dated != listed || !last.Valid {
			continue
		}
		at, perr := time.Parse(episodeTimeLayout, last.String)
		if perr != nil || !at.Before(cutoff) {
			continue
		}
		settled[sk] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("acquire: reading which seasons are settled: %w", err)
	}
	return settled, nil
}

// SettledSeasons is every settled season's episode numbers (ADR-0033, decision
// 5): every episode the provider lists has aired, as many are listed as the
// provider said the season has, and the last aired more than a week ago.
// Season 0 never: specials are not a season anybody releases as one.
func (s *Store) SettledSeasons(ctx context.Context) (map[seasonKey][]int, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	settled, err := s.settledKeys(ctx)
	if err != nil {
		return nil, err
	}
	out := map[seasonKey][]int{}
	for sk := range settled {
		numbers, err := s.seasonNumbers(ctx, sk)
		if err != nil {
			return nil, err
		}
		out[sk] = numbers
	}
	return out, nil
}

// covered is every episode a release spans, when its name spans the episode it
// was grabbed for; otherwise that episode alone.
func covered(p release.Parsed, k Key) []Key {
	if k.Film || p.Season != k.Season || len(p.Episodes) == 0 {
		return []Key{k}
	}
	first, last := p.Episodes[0], p.Episodes[len(p.Episodes)-1]
	if k.Episode < first || k.Episode > last || last-first >= maxRange {
		return []Key{k}
	}
	out := make([]Key, 0, last-first+1)
	for e := first; e <= last; e++ {
		out = append(out, Key{ItemID: k.ItemID, Season: k.Season, Episode: e})
	}
	return out
}

// Queued reports whether a release — by its info hash — is in the queue at all,
// in any state. The queue is the blocklist: a release already grabbed is never
// grabbed again automatically, whether it finished, failed to import, or was
// removed by a person who did not want it.
func (s *Store) Queued(ctx context.Context, infoHash string) (bool, error) {
	h := strings.ToLower(strings.TrimSpace(infoHash))
	if h == "" {
		return false, nil
	}
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM download_queue WHERE info_hash = ?`, h).Scan(&n); err != nil {
		return false, fmt.Errorf("acquire: reading the queue: %w", err)
	}
	return n > 0, nil
}

// Namesake is a library item a release's title could also name.
type Namesake struct {
	ItemID int64
	Film   bool
	Title  string
	Year   int
}

// Name is how the operator would say it: "The Office (2005)".
func (n Namesake) Name() string {
	if n.Year > 0 {
		return fmt.Sprintf("%s (%d)", n.Title, n.Year)
	}
	return n.Title
}

// Namesakes indexes every library item by its folded title, so a pass can see
// that "The.Office.S02E03" names two series in this library and grab it for
// neither (ADR-0030, decision 3).
func (s *Store) Namesakes(ctx context.Context) (map[string][]Namesake, error) {
	// Unscoped: a name that fits two titles is ambiguous whoever can see them (ADR-0030).
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, title, COALESCE(year, 0) FROM media_item`)
	if err != nil {
		return nil, fmt.Errorf("acquire: reading the library's titles: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[string][]Namesake{}
	for rows.Next() {
		var n Namesake
		var kind string
		if err := rows.Scan(&n.ItemID, &kind, &n.Title, &n.Year); err != nil {
			return nil, fmt.Errorf("acquire: reading the library's titles: %w", err)
		}
		n.Film = kind == "movie"
		if key := search.NormalizeTitle(n.Title); key != "" {
			out[key] = append(out[key], n)
		}
	}
	return out, rows.Err()
}

// States returns what automatic acquisition last did about each wanted item.
func (s *Store) States(ctx context.Context) (map[StateKey]State, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	// Unscoped: automatic acquisition's own record, read for the whole library;
	// the item is joined only to tell a book's state from a film's (ADR-0050).
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(s.episode_id, 0), COALESCE(s.item_id, 0), COALESCE(s.album_id, 0),
		       COALESCE(s.searched_at, ''), COALESCE(s.next_at, ''), s.fruitless,
		       COALESCE(s.outcome, ''), s.detail, COALESCE(i.kind, '')
		FROM acquire_state s LEFT JOIN media_item i ON i.id = s.item_id`)
	if err != nil {
		return nil, fmt.Errorf("acquire: reading search state: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[StateKey]State{}
	for rows.Next() {
		var episodeID, itemID, albumID int64
		var searched, next, kind string
		var st State
		if err := rows.Scan(&episodeID, &itemID, &albumID, &searched, &next, &st.Fruitless,
			&st.Outcome, &st.Detail, &kind); err != nil {
			return nil, fmt.Errorf("acquire: reading search state: %w", err)
		}
		st.SearchedAt, _ = time.Parse(timeLayout, searched)
		st.NextAt, _ = time.Parse(timeLayout, next)
		switch {
		case albumID != 0:
			out[StateKey{Album: true, ID: albumID}] = st
		case kind == "book":
			// A book's state is its item's row, as a film's is (ADR-0050).
			out[StateKey{Book: true, ID: itemID}] = st
		case episodeID != 0:
			out[StateKey{ID: episodeID}] = st
		default:
			out[StateKey{Film: true, ID: itemID}] = st
		}
	}
	return out, rows.Err()
}

// ErrNoID means a want names nothing a state can be kept against.
var ErrNoID = errors.New("acquire: no id to record against")

// Record keeps what automatic acquisition did about one wanted item.
func (s *Store) Record(ctx context.Context, w Want, st State) error {
	if err := authz.RequirePermission(ctx, authz.PermManageQueue); err != nil {
		return err
	}
	var episodeID, itemID, albumID any
	col := "episode_id"
	var id int64
	switch {
	case w.Album > 0:
		col, id, albumID = "album_id", w.Album, w.Album
	case w.Film, w.Book:
		col, id, itemID = "item_id", w.ItemID, w.ItemID
	default:
		id, episodeID = w.EpisodeID, w.EpisodeID
	}
	if id <= 0 {
		return fmt.Errorf("%w: %s", ErrNoID, w.Name())
	}
	switch st.Outcome {
	case OutcomeGrabbed, OutcomeNothing, OutcomeFailed:
	default:
		return fmt.Errorf("acquire: %q is not an outcome", st.Outcome)
	}
	searched := nullTime(st.SearchedAt)
	next := nullTime(st.NextAt)
	detail := clip(st.Detail, maxDetail)

	return s.db.InTx(ctx, func(tx db.Execer) error {
		res, err := tx.ExecContext(ctx, `UPDATE acquire_state
			SET searched_at = ?, next_at = ?, fruitless = ?, outcome = ?, detail = ?
			WHERE `+col+` = ?`, // #nosec G202 -- col is "episode_id", "item_id" or "album_id", chosen above
			searched, next, st.Fruitless, st.Outcome, detail, id)
		if err != nil {
			return fmt.Errorf("acquire: recording what happened to %s: %w", w.Name(), err)
		}
		if n, err := res.RowsAffected(); err != nil || n > 0 {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO acquire_state
			(episode_id, item_id, album_id, searched_at, next_at, fruitless, outcome, detail)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			episodeID, itemID, albumID, searched, next, st.Fruitless, st.Outcome, detail); err != nil {
			return fmt.Errorf("acquire: recording what happened to %s: %w", w.Name(), err)
		}
		return nil
	})
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(timeLayout)
}

// maxDetail bounds a recorded explanation. It is built from release names an
// uploader wrote, and a sentence is what the screen needs.
const maxDetail = 600

// clip shortens s to at most n bytes without splitting a character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

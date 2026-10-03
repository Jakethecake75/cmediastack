// Package migrate reads another application's database and brings across the
// part of it this software cannot cheaply recreate.
//
// For Radarr that is the IDENTITIES — which folder is which TMDB title. The
// files are already on disk and Importer.Scan already finds them; what costs
// real time is a provider lookup per film and a person's decision on every
// ambiguous one. Radarr made those decisions years ago. See ADR-0021.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// MinRadarrVersion is the oldest schema this reads.
//
// Migration 207 (Radarr v4.0, 2022) moved TmdbId, ImdbId, Title and Year out of
// Movies into MovieMetadata and deleted the originals. Everything before it is
// a different shape, and supporting every historical one is unbounded work for
// an operation run once — Radarr upgrades its own database far better than this
// could. An older database is refused with its version and that instruction.
const MinRadarrVersion = 207

// ErrTooOld means the source database predates the schema this understands.
var ErrTooOld = errors.New("migrate: this Radarr database is too old to read")

// ErrNotRadarr means the file opened but is not a Radarr database.
var ErrNotRadarr = errors.New("migrate: that file is not a Radarr database")

// RadarrMovie is one film as Radarr holds it.
//
// Folder is the LAST SEGMENT of Radarr's path and nothing else. Radarr stores
// an absolute path on whatever machine it ran on — "/movies/Arrival (2016)",
// "D:\Media\Arrival (2016)" — and none of that means anything here. The last
// segment is the portable part, and it is the only part kept (ADR-0021).
type RadarrMovie struct {
	Folder string
	TmdbID int64
	ImdbID string
	Title  string
	Year   int
}

// RadarrSource is an open Radarr database.
type RadarrSource struct {
	db      *sql.DB
	version int
}

// OpenRadarr opens a Radarr database read-only.
//
// Read-only because this software has no business writing to another
// application's database, and because the operator may well have copied this
// out of a running instance.
func OpenRadarr(ctx context.Context, path string) (*RadarrSource, error) {
	// The immutable flag is deliberately NOT set. It would be faster and it
	// tells SQLite the file cannot change underneath it — which is a promise
	// nobody can make about a file an operator just copied out of a running
	// container.
	dsn := "file:" + url.PathEscape(path) + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("migrate: opening %s: %w", filepath.Base(path), err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: opening %s: %w", filepath.Base(path), err)
	}

	s := &RadarrSource{db: db}
	if !s.hasTable(ctx, "Movies") {
		_ = db.Close()
		return nil, fmt.Errorf("%w: it has no Movies table", ErrNotRadarr)
	}
	s.version = s.schemaVersion(ctx)

	// Detected from the COLUMNS, not from the version number.
	//
	// The version is read and reported because it makes this message
	// actionable, but a database is understood by what it actually contains.
	// A version table is a claim; PRAGMA table_info is the thing itself.
	if !s.hasTable(ctx, "MovieMetadata") || !s.hasColumn(ctx, "Movies", "MovieMetadataId") {
		_ = db.Close()
		return nil, fmt.Errorf(
			"%w: it reports schema version %d and has no MovieMetadata table, so "+
				"it predates Radarr v4 (migration %d). Upgrade Radarr first and let "+
				"it migrate its own database, then export again",
			ErrTooOld, s.version, MinRadarrVersion)
	}
	return s, nil
}

// Close releases the database.
func (s *RadarrSource) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Version is the schema version Radarr recorded for itself.
func (s *RadarrSource) Version() int { return s.version }

// schemaVersion reads FluentMigrator's own table, the same query Radarr uses on
// itself (Datastore/Database.cs). Zero when it cannot be read, which is not an
// error on its own: the column checks are what decide whether this is readable.
func (s *RadarrSource) schemaVersion(ctx context.Context) int {
	var v int
	row := s.db.QueryRowContext(ctx, `SELECT "Version" FROM "VersionInfo" ORDER BY "Version" DESC LIMIT 1`)
	if err := row.Scan(&v); err != nil {
		return 0
	}
	return v
}

func (s *RadarrSource) hasTable(ctx context.Context, name string) bool {
	var got string
	err := s.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`,
		name).Scan(&got)
	return err == nil
}

// hasColumn asks the database what a table contains.
func (s *RadarrSource) hasColumn(ctx context.Context, table, column string) bool {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false
		}
		if name == column {
			return true
		}
	}
	// A listing that stopped early is not a listing that lacked the column,
	// but either way this answers "not readable as Radarr", which is safe.
	_ = rows.Err()
	return false
}

// Movies reads every film Radarr holds.
//
// The join is the point: since migration 207 the identifiers live in
// MovieMetadata and Movies keeps only MovieMetadataId. Reading Movies.TmdbId —
// which is what an importer written from memory does — finds no such column on
// any current install.
func (s *RadarrSource) Movies(ctx context.Context) ([]RadarrMovie, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m."Path", md."TmdbId", COALESCE(md."ImdbId", ''),
		       md."Title", COALESCE(md."Year", 0)
		FROM "Movies" m
		JOIN "MovieMetadata" md ON md."Id" = m."MovieMetadataId"`)
	if err != nil {
		return nil, fmt.Errorf("migrate: reading movies: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []RadarrMovie
	for rows.Next() {
		var path, imdb, title string
		var tmdb int64
		var year int
		if err := rows.Scan(&path, &tmdb, &imdb, &title, &year); err != nil {
			return nil, fmt.Errorf("migrate: reading movies: %w", err)
		}
		folder := LastSegment(path)
		if folder == "" {
			// A movie with no path has never been placed on disk, so there is
			// nothing here to match it to.
			continue
		}
		out = append(out, RadarrMovie{
			Folder: folder, TmdbID: tmdb,
			ImdbID: strings.TrimSpace(imdb),
			Title:  strings.TrimSpace(title), Year: year,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrate: reading movies: %w", err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Folder < out[j].Folder })
	return out, nil
}

// LastSegment takes the final component of a path written on any platform.
//
// filepath.Base is not enough: it uses THIS platform's separator, so on Linux
// it returns the whole of `D:\Media\Movies\Arrival (2016)` unchanged. An
// operator moving from a Windows Radarr to a Linux instance is the common case,
// not the odd one.
func LastSegment(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimRight(p, `/\`)
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		p = p[i+1:]
	}
	return strings.TrimSpace(p)
}

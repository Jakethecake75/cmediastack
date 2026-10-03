package db

import (
	"context"
	"path/filepath"
	"testing"
)

// Migration 0026 rebuilds media_item to admit artists and books (ADR-0044).
// Checked on a database as it stood before, with a title of each kind and rows
// that reference them: every title, every column and every reference survives,
// and the new kinds are admitted.
func TestTheMusicMigrationKeepsEveryTitle(t *testing.T) {
	ctx := context.Background()
	d, err := Open(Options{Path: filepath.Join(t.TempDir(), "m26.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.migrateTo(ctx, 25); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO root_folder (id, path, kind, created_at, updated_at) VALUES (1, '/media/films', 'movies', 'x', 'x')`,
		`INSERT INTO media_item (id, kind, title, year, sort_title, root_folder_id, folder, tmdb_id,
		     added_at, updated_at, monitored, certification, rating_rank, rating_source)
		 VALUES (7, 'movie', 'Dune', 2021, 'dune', 1, 'Dune (2021)', 438631, 'a', 'b', 0, 'PG-13', 3, 'person'),
		        (8, 'series', 'Severance', 2022, 'severance', 1, 'Severance', 95396, 'a', 'b', 1, NULL, NULL, NULL)`,
		`INSERT INTO media_file (item_id, root_folder_id, relative_path, size_bytes, quality, revision,
		     release_title, imported_at) VALUES (7, 1, 'Dune (2021)/Dune.mkv', 1, 'WEBDL-1080p', 0, 'x', 'x')`,
		`INSERT INTO season (item_id, number, name, episode_count, updated_at)
		 VALUES (8, 1, 'Season 1', 9, 'x')`,
	} {
		if _, err := d.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var title, cert, source string
	var year, rank, monitored int
	if err := d.QueryRowContext(ctx, `SELECT title, year, certification, rating_rank, rating_source, monitored
		FROM media_item WHERE id = 7`).Scan(&title, &year, &cert, &rank, &source, &monitored); err != nil {
		t.Fatal(err)
	}
	if title != "Dune" || year != 2021 || cert != "PG-13" || rank != 3 || source != "person" || monitored != 0 {
		t.Errorf("the film came through as %q %d %q %d %q %d", title, year, cert, rank, source, monitored)
	}
	for table, want := range map[string]int{"media_item": 2, "media_file": 1, "season": 1} {
		var n int
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil || n != want {
			t.Errorf("%s: %d rows, want %d (%v)", table, n, want, err)
		}
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO media_item (kind, title, sort_title, root_folder_id, folder,
		musicbrainz_id, added_at, updated_at) VALUES ('artist', 'Radiohead', 'radiohead', 1, 'Radiohead',
		'a74b1b7f-71a5-4011-9441-d0b5e4122711', 'x', 'x')`); err != nil {
		t.Errorf("an artist is not admitted: %v", err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO media_item (kind, title, sort_title, root_folder_id, folder,
		added_at, updated_at) VALUES ('podcast', 'x', 'x', 1, 'x2', 'x', 'x')`); err == nil {
		t.Error("an unknown kind was admitted")
	}
	// The references still hold: deleting the film takes its file with it.
	if _, err := d.ExecContext(ctx, `DELETE FROM media_item WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	var files int
	_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM media_file`).Scan(&files)
	if files != 0 {
		t.Errorf("the file outlived its title: the reference was lost in the rebuild")
	}
}

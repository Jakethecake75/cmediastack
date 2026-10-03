package db

import (
	"context"
	"path/filepath"
	"testing"
)

// Migration 0028 lets automatic acquisition keep a state for an album
// (ADR-0047): every episode's and film's row comes through the rebuilt table,
// and a row is still exactly one of the three.
func TestTheAlbumStateMigrationKeepsEveryRow(t *testing.T) {
	ctx := context.Background()
	d, err := Open(Options{Path: filepath.Join(t.TempDir(), "m28.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.migrateTo(ctx, 27); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO root_folder (id, path, kind, label, created_at, updated_at)
		 VALUES (1, '/m', 'series', 'M', 'x', 'x')`,
		`INSERT INTO media_item (id, kind, title, sort_title, root_folder_id, folder, added_at, updated_at)
		 VALUES (1, 'series', 'S', 's', 1, 'S', 'x', 'x'), (2, 'movie', 'F', 'f', 1, 'F', 'x', 'x'),
		        (3, 'artist', 'A', 'a', 1, 'A', 'x', 'x')`,
		`INSERT INTO season (id, item_id, number, updated_at) VALUES (5, 1, 1, 'x')`,
		`INSERT INTO episode (id, item_id, season_id, season_number, number, updated_at)
		 VALUES (10, 1, 5, 1, 1, 'x')`,
		`INSERT INTO album (id, item_id, musicbrainz_id, title, album_type, created_at, updated_at)
		 VALUES (30, 3, 'mb', 'Dummy', 'album', 'x', 'x')`,
		`INSERT INTO acquire_state (id, episode_id, searched_at, fruitless, outcome, detail)
		 VALUES (1, 10, '2026-09-01', 2, 'nothing', 'none')`,
		`INSERT INTO acquire_state (id, item_id, outcome, detail) VALUES (2, 2, 'grabbed', 'got it')`,
	} {
		if _, err := d.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO acquire_state (album_id) VALUES (30)`); err == nil {
		t.Fatal("before 0028 an album state was accepted; the test is not testing the migration")
	}

	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var n, fruitless int
	var detail string
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM acquire_state`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("%d rows, %v", n, err)
	}
	if err := d.QueryRowContext(ctx, `SELECT fruitless, detail FROM acquire_state WHERE episode_id = 10`).
		Scan(&fruitless, &detail); err != nil || fruitless != 2 || detail != "none" {
		t.Errorf("the episode's row: %d %q %v", fruitless, detail, err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO acquire_state (album_id, outcome) VALUES (30, 'nothing')`); err != nil {
		t.Errorf("an album state was refused: %v", err)
	}
	for _, bad := range []string{
		`INSERT INTO acquire_state (album_id) VALUES (30)`,             // a second for the same album
		`INSERT INTO acquire_state (album_id, item_id) VALUES (30, 2)`, // two at once
		`INSERT INTO acquire_state (detail) VALUES ('nothing at all')`, // none
		`INSERT INTO acquire_state (album_id) VALUES (31)`,             // no such album
	} {
		if _, err := d.ExecContext(ctx, bad); err == nil {
			t.Errorf("accepted: %s", bad)
		}
	}
	if _, err := d.ExecContext(ctx, `DELETE FROM album WHERE id = 30`); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM acquire_state WHERE album_id IS NOT NULL`).Scan(&n); err != nil || n != 0 {
		t.Errorf("an album's state outlived it: %d %v", n, err)
	}
}

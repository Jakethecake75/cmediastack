package db

import (
	"context"
	"path/filepath"
	"testing"
)

// Migration 0037 (ADR-0066): a title's id is never used again. Checked on a
// database as it stood before, with a title carrying every later column and a
// row that references it: everything survives, and a deleted title's id is
// not handed to the next one.
func TestATitlesIDIsNeverUsedAgain(t *testing.T) {
	ctx := context.Background()
	d, err := Open(Options{Path: filepath.Join(t.TempDir(), "m37.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.migrateTo(ctx, 36); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO root_folder (id, path, kind, created_at, updated_at) VALUES (1, '/media/tv', 'series', 'x', 'x')`,
		`INSERT INTO media_item (id, kind, title, year, sort_title, root_folder_id, folder, tmdb_id, added_at,
		     updated_at, monitored, follow_new_seasons, season_folders, daily)
		 VALUES (1, 'series', 'The Daily Show', 1996, 'daily show', 1, 'The Daily Show (1996)', 2224, 'a', 'b', 0, 0, 0, 1)`,
		`INSERT INTO season (item_id, number, name, episode_count, updated_at) VALUES (1, 30, 'Season 30', 2, 'x')`,
	} {
		if _, err := d.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var title string
	var monitored, follow, folders, daily, seasons int
	if err := d.QueryRowContext(ctx, `SELECT title, monitored, follow_new_seasons, season_folders, daily,
		(SELECT COUNT(*) FROM season WHERE item_id = 1) FROM media_item WHERE id = 1`).
		Scan(&title, &monitored, &follow, &folders, &daily, &seasons); err != nil {
		t.Fatal(err)
	}
	if title != "The Daily Show" || monitored != 0 || follow != 0 || folders != 0 || daily != 1 || seasons != 1 {
		t.Errorf("the series came through as %q monitored=%d follow=%d folders=%d daily=%d seasons=%d",
			title, monitored, follow, folders, daily, seasons)
	}

	insert := func(folder string) int64 {
		t.Helper()
		res, err := d.ExecContext(ctx, `INSERT INTO media_item (kind, title, sort_title, root_folder_id, folder,
			added_at, updated_at) VALUES ('movie', ?, ?, 1, ?, 'x', 'x')`, folder, folder, folder)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	second := insert("Heat (1995)")
	if _, err := d.ExecContext(ctx, `DELETE FROM media_item WHERE id = ?`, second); err != nil {
		t.Fatal(err)
	}
	if third := insert("Dune (2021)"); third <= second {
		t.Errorf("a deleted title's id %d was used again (%d)", second, third)
	}
}

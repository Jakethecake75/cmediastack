package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// Migration 0027 lets a queue row's target be an album (ADR-0046): every
// existing kind comes through the rebuilt column, and the album id is its own
// column.
func TestTheAlbumTargetMigrationKeepsEveryRow(t *testing.T) {
	ctx := context.Background()
	d, err := Open(Options{Path: filepath.Join(t.TempDir(), "m27.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.migrateTo(ctx, 26); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		hash       string
		kind, item any
		s, e       any
	}{
		{strings.Repeat("a", 40), "episode", int64(4), int64(2), int64(3)},
		{strings.Repeat("b", 40), "film", int64(7), nil, nil},
		{strings.Repeat("c", 40), "season", int64(4), int64(1), nil},
		{strings.Repeat("d", 40), nil, nil, nil, nil},
	}
	for _, row := range rows {
		if _, err := d.ExecContext(ctx, `
			INSERT INTO download_queue (info_hash, title, magnet, added_at, updated_at,
			                            target_kind, target_item_id, target_season, target_episode)
			VALUES (?, 'x', 'magnet:?xt=urn:btih:'||?, '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z', ?, ?, ?, ?)`,
			row.hash, row.hash, row.kind, row.item, row.s, row.e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.ExecContext(ctx, `UPDATE download_queue SET target_kind = 'album' WHERE info_hash = ?`,
		strings.Repeat("a", 40)); err == nil {
		t.Fatal("before 0027 the column already accepted an album; the test is not testing the migration")
	}

	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		var kind, item, s, e, album any
		if err := d.QueryRowContext(ctx, `
			SELECT target_kind, target_item_id, target_season, target_episode, target_album_id
			FROM download_queue WHERE info_hash = ?`, row.hash).Scan(&kind, &item, &s, &e, &album); err != nil {
			t.Fatal(err)
		}
		if kind != row.kind || item != row.item || s != row.s || e != row.e || album != nil {
			t.Errorf("%s…: %v %v %v %v %v after the migration", row.hash[:4], kind, item, s, e, album)
		}
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO download_queue (info_hash, title, magnet, added_at, updated_at,
		                            target_kind, target_item_id, target_album_id)
		VALUES (?, 'x', 'magnet:?xt=urn:btih:x', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z', 'album', 3, 30)`,
		strings.Repeat("e", 40)); err != nil {
		t.Errorf("an album target was refused after the migration: %v", err)
	}
	for _, bad := range []string{
		`UPDATE download_queue SET target_kind = 'trailer'`,
		`UPDATE download_queue SET target_album_id = 0`,
	} {
		if _, err := d.ExecContext(ctx, bad); err == nil {
			t.Errorf("accepted: %s", bad)
		}
	}
}

// Migration 0029 widens the queue's target to books (ADR-0049): every row
// comes through, and a book row is accepted after it and not before.
func TestTheBookTargetMigrationKeepsEveryRow(t *testing.T) {
	ctx := context.Background()
	d, err := Open(Options{Path: filepath.Join(t.TempDir(), "m29.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.migrateTo(ctx, 28); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO download_queue (info_hash, title, magnet, added_at, updated_at,
		                            target_kind, target_item_id, target_album_id)
		VALUES (?, 'x', 'magnet:?xt=urn:btih:a', 'x', 'x', 'album', 3, 30)`, strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `UPDATE download_queue SET target_kind = 'book'`); err == nil {
		t.Fatal("before 0029 the column already accepted a book")
	}
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var kind string
	var album int64
	if err := d.QueryRowContext(ctx, `SELECT target_kind, target_album_id FROM download_queue`).Scan(&kind, &album); err != nil ||
		kind != "album" || album != 30 {
		t.Errorf("the album row: %q %d %v", kind, album, err)
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO download_queue (info_hash, title, magnet, added_at, updated_at, target_kind, target_item_id)
		VALUES (?, 'x', 'magnet:?xt=urn:btih:b', 'x', 'x', 'book', 9)`, strings.Repeat("b", 40)); err != nil {
		t.Errorf("a book target was refused: %v", err)
	}
	if _, err := d.ExecContext(ctx, `UPDATE download_queue SET target_kind = 'trailer'`); err == nil {
		t.Error("the rebuilt column accepted a kind that is not one")
	}
}

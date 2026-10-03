package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// OpenTest returns a migrated database in a temporary directory.
func OpenTest(t *testing.T) *DB {
	t.Helper()
	d, err := Open(Options{Path: filepath.Join(t.TempDir(), "test.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if _, err := d.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return d
}

func TestMigrationsApplyCleanly(t *testing.T) {
	ctx := context.Background()
	d, err := Open(Options{Path: filepath.Join(t.TempDir(), "m.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	applied, err := d.Migrate(ctx)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(applied) == 0 {
		t.Fatal("no migrations applied")
	}

	v, err := d.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != len(applied) {
		t.Errorf("schema version = %d, want %d", v, len(applied))
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "idem.db")

	d, err := Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	first, err := d.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Errorf("second run applied %v, want nothing", second)
	}
	if len(first) == 0 {
		t.Error("first run applied nothing")
	}
	_ = d.Close()
}

func TestMigrationVersionsAreContiguous(t *testing.T) {
	ms, err := LoadMigrations()
	if err != nil {
		t.Fatalf("LoadMigrations: %v", err)
	}
	for i, m := range ms {
		if m.Version != i+1 {
			t.Errorf("migration at index %d has version %d", i, m.Version)
		}
		if m.Checksum == "" {
			t.Errorf("migration %d has no checksum", m.Version)
		}
	}
}

// Foreign keys default to OFF in SQLite. If the pragma does not take effect
// every reference in the schema becomes decorative, so this is asserted rather
// than assumed.
func TestForeignKeysAreEnforced(t *testing.T) {
	d := OpenTest(t)
	ctx := context.Background()

	_, err := d.ExecContext(ctx,
		`INSERT INTO root_folder_grant (user_id, root_folder_id, granted_at) VALUES (9999, 8888, '2026-01-01T00:00:00Z')`)
	if err == nil {
		t.Fatal("insert with dangling foreign keys succeeded; foreign_keys is not enforced")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Errorf("expected a foreign key error, got: %v", err)
	}
}

func TestUniqueConstraintsHold(t *testing.T) {
	d := OpenTest(t)
	ctx := context.Background()

	seedRole(t, d)

	insert := func(username, email string) error {
		_, err := d.ExecContext(ctx,
			`INSERT INTO app_user (username, email, password_hash, state, role_id, created_at, updated_at)
			 VALUES (?, ?, 'x', 'active', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			username, email)
		return err
	}

	if err := insert("jacob", "jacob@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := insert("jacob", "other@example.com"); err == nil {
		t.Error("duplicate username was accepted")
	}
	// Usernames and emails are NOCASE: "Jacob" must collide with "jacob".
	if err := insert("JACOB", "different@example.com"); err == nil {
		t.Error("case-variant duplicate username was accepted")
	}
	if err := insert("other", "JACOB@EXAMPLE.COM"); err == nil {
		t.Error("case-variant duplicate email was accepted")
	}
}

// Only one pending request per email, but a denied request must not block a
// resubmission.
func TestPendingAccountRequestUniqueness(t *testing.T) {
	d := OpenTest(t)
	ctx := context.Background()

	insert := func(state string) error {
		_, err := d.ExecContext(ctx,
			`INSERT INTO account_request (username, email, password_hash, state, created_at, expires_at)
			 VALUES ('someone', 'a@example.com', 'hash', ?, '2026-01-01T00:00:00Z', '2026-01-15T00:00:00Z')`,
			state)
		return err
	}

	if err := insert("pending"); err != nil {
		t.Fatal(err)
	}
	if err := insert("pending"); err == nil {
		t.Error("a second pending request for the same email was accepted")
	}
	if err := insert("denied"); err != nil {
		t.Errorf("a denied request for the same email should be allowed: %v", err)
	}
}

func TestInTxRollsBackOnError(t *testing.T) {
	d := OpenTest(t)
	ctx := context.Background()
	seedRole(t, d)

	wantErr := context.Canceled
	err := d.InTx(ctx, func(tx Execer) error {
		_, e := tx.ExecContext(ctx,
			`INSERT INTO app_user (username, email, password_hash, state, role_id, created_at, updated_at)
			 VALUES ('rolled', 'rolled@example.com', 'x', 'active', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
		if e != nil {
			return e
		}
		return wantErr
	})
	// Identity, not errors.Is: InTx must hand back the callback's own error,
	// not a wrapping of it that happens to match.
	if err != wantErr { //nolint:errorlint // the identity is the assertion
		t.Fatalf("InTx returned %v, want %v", err, wantErr)
	}

	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_user WHERE username = 'rolled'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("transaction was not rolled back: found %d rows", n)
	}
}

func TestInTxRollsBackOnPanic(t *testing.T) {
	d := OpenTest(t)
	ctx := context.Background()
	seedRole(t, d)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic did not propagate")
			}
		}()
		_ = d.InTx(ctx, func(tx Execer) error {
			_, _ = tx.ExecContext(ctx,
				`INSERT INTO app_user (username, email, password_hash, state, role_id, created_at, updated_at)
				 VALUES ('panicked', 'p@example.com', 'x', 'active', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
			panic("boom")
		})
	}()

	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_user WHERE username = 'panicked'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("panicking transaction was not rolled back: found %d rows", n)
	}
}

func TestIntegrityCheckAndBackup(t *testing.T) {
	d := OpenTest(t)
	ctx := context.Background()

	if err := d.IntegrityCheck(ctx); err != nil {
		t.Fatalf("integrity check on a fresh database failed: %v", err)
	}

	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := d.BackupTo(ctx, backup); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// The backup must itself be a valid, complete database.
	restored, err := Open(Options{Path: backup})
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer restored.Close()

	if err := restored.IntegrityCheck(ctx); err != nil {
		t.Errorf("restored database failed integrity check: %v", err)
	}
	v, err := restored.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v == 0 {
		t.Error("restored database has no schema version")
	}
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(Options{Path: ""}); err == nil {
		t.Fatal("Open accepted an empty path")
	}
}

func seedRole(t *testing.T, d *DB) {
	t.Helper()
	_, err := d.ExecContext(context.Background(),
		`INSERT INTO role (id, name, rank, builtin, created_at, updated_at)
		 VALUES (1, 'Admin', 100, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
}

// Migration 0015 gives the download queue a target kind, and every row that
// already carried a target was grabbed from an episode search — the only kind
// there was. Checked on a database built as it stood before the migration,
// because this is the part that runs once, on somebody's real queue.
func TestMigration15MarksEveryTargetedRowAsAnEpisode(t *testing.T) {
	ctx := context.Background()
	d, err := Open(Options{Path: filepath.Join(t.TempDir(), "m15.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.migrateTo(ctx, 14); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		hash  string
		item  any
		s, e  any
		title string
	}{
		{strings.Repeat("a", 40), int64(4), 2, 3, "Severance.S02E03.1080p.WEB.H264-GRP"},
		{strings.Repeat("b", 40), nil, nil, nil, "Dune.2021.1080p.BluRay.x264-GRP"},
	} {
		if _, err := d.ExecContext(ctx, `
			INSERT INTO download_queue (info_hash, title, magnet, added_at, updated_at,
			                            target_item_id, target_season, target_episode)
			VALUES (?, ?, 'magnet:?xt=urn:btih:'||?, '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z', ?, ?, ?)`,
			row.hash, row.title, row.hash, row.item, row.s, row.e); err != nil {
			t.Fatal(err)
		}
	}

	applied, err := d.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) == 0 || applied[0] != 15 {
		t.Fatalf("applied %v, want 15 first", applied)
	}
	for hash, want := range map[string]any{strings.Repeat("a", 40): "episode", strings.Repeat("b", 40): nil} {
		var kind any
		if err := d.QueryRowContext(ctx, `SELECT target_kind FROM download_queue WHERE info_hash = ?`,
			hash).Scan(&kind); err != nil {
			t.Fatal(err)
		}
		if kind != want {
			t.Errorf("%s…: target_kind = %v, want %v", hash[:4], kind, want)
		}
	}
	// And the column refuses a kind that is not one.
	if _, err := d.ExecContext(ctx, `UPDATE download_queue SET target_kind = 'trailer'`); err == nil {
		t.Error("the column accepted a kind that is not episode or film")
	}
}

// Migration 0019 lets a queue row's target be a season (ADR-0033). SQLite cannot
// widen a CHECK in place, so the column is rebuilt; this is the part that runs
// once on somebody's real queue, so every existing kind must come through it.
func TestTheSeasonTargetMigrationKeepsEveryRow(t *testing.T) {
	ctx := context.Background()
	d, err := Open(Options{Path: filepath.Join(t.TempDir(), "m19.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.migrateTo(ctx, 18); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		hash       string
		kind, item any
		s, e       any
	}{
		{strings.Repeat("a", 40), "episode", int64(4), 2, 3},
		{strings.Repeat("b", 40), "film", int64(7), nil, nil},
		{strings.Repeat("c", 40), nil, nil, nil, nil},
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
	if _, err := d.ExecContext(ctx, `UPDATE download_queue SET target_kind = 'season' WHERE info_hash = ?`,
		strings.Repeat("a", 40)); err == nil {
		t.Fatal("before 0019 the column already accepted a season; the test is not testing the migration")
	}

	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		var kind, item, s, e any
		if err := d.QueryRowContext(ctx, `
			SELECT target_kind, target_item_id, target_season, target_episode
			FROM download_queue WHERE info_hash = ?`, row.hash).Scan(&kind, &item, &s, &e); err != nil {
			t.Fatal(err)
		}
		if kind != row.kind || item != row.item {
			t.Errorf("%s…: kind %v item %v after the migration, want %v %v", row.hash[:4], kind, item, row.kind, row.item)
		}
	}
	if _, err := d.ExecContext(ctx, `
		INSERT INTO download_queue (info_hash, title, magnet, added_at, updated_at,
		                            target_kind, target_item_id, target_season)
		VALUES (?, 'x', 'magnet:?xt=urn:btih:x', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z', 'season', 4, 2)`,
		strings.Repeat("d", 40)); err != nil {
		t.Errorf("a season target was refused after the migration: %v", err)
	}
	if _, err := d.ExecContext(ctx, `UPDATE download_queue SET target_kind = 'trailer'`); err == nil {
		t.Error("the rebuilt column accepted a kind that is not episode, film or season")
	}
}

// Migration 0016 gives an instance created before it the default a new one
// gets: its built-in HD-1080p. If that profile is gone, it gets none rather
// than a guess.
func TestMigration16MakesTheBuiltinHD1080pTheDefault(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		profiles [][2]any // name, builtin
		want     any
	}{
		"the built-in is there":    {[][2]any{{"HD-720p", 1}, {"HD-1080p", 1}, {"Mine", 0}}, "HD-1080p"},
		"the built-in was renamed": {[][2]any{{"HD-720p", 1}, {"1080p please", 1}}, nil},
		"only a copy by that name": {[][2]any{{"HD-1080p", 0}}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			d, err := Open(Options{Path: filepath.Join(t.TempDir(), "m16.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			if _, err := d.migrateTo(ctx, 15); err != nil {
				t.Fatal(err)
			}
			for _, p := range tc.profiles {
				if _, err := d.ExecContext(ctx, `
					INSERT INTO quality_profile (name, allowed, cutoff, builtin, created_at, updated_at)
					VALUES (?, '["WEBDL-1080p"]', 'WEBDL-1080p', ?, '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z')`,
					p[0], p[1]); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := d.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			var got any
			err = d.QueryRowContext(ctx, `SELECT name FROM quality_profile WHERE is_default = 1`).Scan(&got)
			if err != nil && tc.want != nil {
				t.Fatalf("no default: %v", err)
			}
			if tc.want != nil && got != tc.want {
				t.Errorf("default = %v, want %v", got, tc.want)
			}
			if tc.want == nil && err == nil {
				t.Errorf("default = %v, want none", got)
			}
		})
	}
}

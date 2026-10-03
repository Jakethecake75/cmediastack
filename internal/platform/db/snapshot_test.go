package db

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// "file:" + path is a URI. Before the path was escaped, a directory named with
// a '#' in it made SQLite open — and create — a different file: everything up
// to the '#', in the parent directory.
func TestADatabaseUnderAnAwkwardPathIsThatFile(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "a b#c?d%e")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cms.db")

	d, err := Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()

	if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
		t.Fatalf("the database is not at the path it was given: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "a b#c?d%e" {
			t.Errorf("a stray %q was created beside the directory", e.Name())
		}
	}

	s, err := OpenSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.CheckMigrations(t.Context(), true); err != nil {
		t.Fatalf("the snapshot open did not find the same database: %v", err)
	}
}

// A relative path is resolved, not read as a URI's authority.
func TestARelativeDatabasePathOpens(t *testing.T) {
	t.Chdir(t.TempDir())
	d, err := Open(Options{Path: "rel.db"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat("rel.db"); err != nil {
		t.Fatal(err)
	}
}

// snapshotOf takes a migrated database's snapshot into a file created 0600
// first, which is how the backup service takes one.
func snapshotOf(t *testing.T, d *DB) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snap.db")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := d.BackupTo(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestASnapshotIsCheckedWithoutBeingTouched(t *testing.T) {
	d := OpenTest(t)
	path := snapshotOf(t, d)

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %v; VACUUM INTO did not keep the file it was given", fi.Mode().Perm())
	}
	before := fi.ModTime()

	s, err := OpenSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.IntegrityCheck(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.ForeignKeyCheck(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A write that the live database accepts, refused for being a write.
	const write = `INSERT INTO setting (key, value, updated_at) VALUES ('x', 'y', '2026-01-01T00:00:00Z')`
	if _, err := s.ExecContext(t.Context(), write); err == nil ||
		!strings.Contains(err.Error(), "readonly") {
		t.Fatalf("a snapshot accepted a write, or refused it for another reason: %v", err)
	}
	if _, err := d.ExecContext(t.Context(), write); err != nil {
		t.Fatalf("the same write on the live database: %v", err)
	}
	_ = s.Close()

	for _, side := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + side); err == nil {
			t.Errorf("checking the snapshot created %s", filepath.Base(path+side))
		}
	}
	if fi, err := os.Stat(path); err != nil || !fi.ModTime().Equal(before) {
		t.Errorf("checking the snapshot changed it: %v", err)
	}
}

func TestOpenSnapshotDoesNotCreateAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if s, err := OpenSnapshot(path); err == nil {
		_ = s.Close()
		t.Fatal("OpenSnapshot opened a file that does not exist")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("OpenSnapshot created the file")
	}
}

func TestBackupToWillNotOverwrite(t *testing.T) {
	d := OpenTest(t)
	path := filepath.Join(t.TempDir(), "taken.db")
	if err := os.WriteFile(path, []byte("somebody's file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.BackupTo(t.Context(), path); err == nil {
		t.Fatal("BackupTo wrote over a file that had content")
	}
	if b, _ := os.ReadFile(path); string(b) != "somebody's file" {
		t.Fatal("the file was changed")
	}
}

func TestCheckMigrations(t *testing.T) {
	ours, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	latest := len(ours)

	t.Run("a fully migrated database", func(t *testing.T) {
		d := OpenTest(t)
		for _, complete := range []bool{true, false} {
			s, err := d.CheckMigrations(t.Context(), complete)
			if err != nil || s.Version != latest || s.Latest != latest {
				t.Fatalf("complete=%v: %+v, %v", complete, s, err)
			}
		}
	})

	t.Run("an older database is behind, which only a backup being taken refuses", func(t *testing.T) {
		d, err := Open(Options{Path: filepath.Join(t.TempDir(), "old.db")})
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if _, err := d.migrateTo(t.Context(), latest-2); err != nil {
			t.Fatal(err)
		}
		s, err := d.CheckMigrations(t.Context(), false)
		if err != nil || s.Version != latest-2 {
			t.Fatalf("%+v, %v", s, err)
		}
		if _, err := d.CheckMigrations(t.Context(), true); !errors.Is(err, ErrMigrationMissing) {
			t.Fatalf("err = %v, want ErrMigrationMissing", err)
		}
	})

	refused := map[string]struct {
		change string
		want   error
	}{
		"a migration edited after it was applied": {
			`UPDATE schema_migration SET checksum = 'deadbeef' WHERE version = 3`, ErrMigrationChanged},
		"a migration from a newer build": {
			`INSERT INTO schema_migration VALUES (999, 'from_the_future', 'abc', '2030-01-01T00:00:00Z')`,
			ErrUnknownMigration},
		"a gap":                  {`DELETE FROM schema_migration WHERE version = 3`, ErrMigrationGap},
		"no migrations recorded": {`DELETE FROM schema_migration`, ErrNotOurs},
	}
	for name, tc := range refused {
		t.Run(name, func(t *testing.T) {
			d := OpenTest(t)
			if _, err := d.ExecContext(t.Context(), tc.change); err != nil {
				t.Fatal(err)
			}
			for _, complete := range []bool{true, false} {
				if _, err := d.CheckMigrations(t.Context(), complete); !errors.Is(err, tc.want) {
					t.Fatalf("complete=%v: err = %v, want %v", complete, err, tc.want)
				}
			}
		})
	}

	t.Run("a database that is not ours at all", func(t *testing.T) {
		d, err := Open(Options{Path: filepath.Join(t.TempDir(), "other.db")})
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if _, err := d.ExecContext(t.Context(), `CREATE TABLE notes (body TEXT)`); err != nil {
			t.Fatal(err)
		}
		if _, err := d.CheckMigrations(t.Context(), false); !errors.Is(err, ErrNotOurs) {
			t.Fatalf("err = %v, want ErrNotOurs", err)
		}
	})
}

// The live database enforces its references, so a broken one in a snapshot is
// damage — a structurally sound page holding the wrong thing — that the
// integrity check does not look for.
func TestForeignKeyCheckFindsABrokenReference(t *testing.T) {
	d := OpenTest(t)
	conn, err := d.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(),
		`INSERT INTO recovery_code (user_id, code_hash, created_at) VALUES (4242, 'x', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}

	err = d.ForeignKeyCheck(t.Context())
	if err == nil || !strings.Contains(err.Error(), "1 broken reference") ||
		!strings.Contains(err.Error(), "recovery_code") {
		t.Fatalf("err = %v", err)
	}
}

func TestTheCensusCountsWhatIsThere(t *testing.T) {
	d := OpenTest(t)
	c, err := d.TakeCensus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if c != (Census{}) {
		t.Fatalf("a fresh database counted %+v", c)
	}

	if _, err := d.ExecContext(t.Context(), `
		INSERT INTO audit_event (occurred_at, actor_label, action, outcome)
		VALUES ('2026-09-27T10:00:00Z', 'x', 'a', 'success'),
		       ('2026-09-27T11:30:00.5Z', 'x', 'a', 'success')`); err != nil {
		t.Fatal(err)
	}
	c, err = d.TakeCensus(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if c.AuditEvents != 2 || c.LastActivity.Format("15:04:05.0") != "11:30:00.5" {
		t.Fatalf("census = %+v", c)
	}

	// An older schema without a table counts it as empty.
	old, err := Open(Options{Path: filepath.Join(t.TempDir(), "old.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if _, err := old.migrateTo(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := old.TakeCensus(t.Context()); err != nil {
		t.Fatalf("an older schema could not be counted: %v", err)
	}
}

// The database holds password hashes, sealed credentials and the audit log.
// SQLite would create it 0644 less the umask, and its -wal and -shm take the
// database's mode — so it is created 0600, and all three stay that way.
func TestANewDatabaseIsReadableByItsOwnerAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cms.db")
	d := openAt(t, path)
	if _, err := d.ExecContext(t.Context(),
		`INSERT INTO setting (key, value, updated_at) VALUES ('k', 'v', 'x')`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(p), err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v", filepath.Base(p), fi.Mode().Perm())
		}
	}
}

// One created before this was fixed is narrowed when it is next opened.
func TestAnExistingDatabaseOthersCouldReadIsNarrowed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cms.db")
	d := openAt(t, path)
	_ = d.Close()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+"-wal", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+"-wal", 0o644); err != nil {
		t.Fatal(err)
	}

	d = openAt(t, path)
	for _, p := range []string{path, path + "-wal"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s is still %v", filepath.Base(p), fi.Mode().Perm())
		}
	}
	if _, err := d.CheckMigrations(t.Context(), true); err != nil {
		t.Fatalf("the narrowed database no longer works: %v", err)
	}
}

func openAt(t *testing.T, path string) *DB {
	t.Helper()
	d, err := Open(Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return d
}

package backup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

func TestAVerifiedBackupReportsWhatItHolds(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	tk := r.take()

	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	rep, err := Verify(t.Context(), tk.Path, r.pass)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SHA256 != tk.SHA256 || rep.Size != tk.Size || !rep.TakenAt.Equal(tk.TakenAt) {
		t.Fatalf("report = %+v, taken = %+v", rep, tk)
	}
	if rep.Contents.Schema != tk.Contents.Schema {
		t.Fatalf("schema = %+v", rep.Contents.Schema)
	}
	// The backup's own creation is not in it — it was recorded after the
	// snapshot — but the marker row and the schema are.
	if rep.Contents.Census.AuditEvents != 0 {
		t.Fatalf("census = %+v", rep.Contents.Census)
	}
	// The plaintext went into a private temporary directory, and is gone.
	if left := names(t, tmp); len(left) != 0 {
		t.Fatalf("verifying left %v in the temporary directory", left)
	}
}

func TestABackupThatCannotBeOpenedSaysWhy(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	tk := r.take()
	raw, err := os.ReadFile(tk.Path)
	if err != nil {
		t.Fatal(err)
	}
	other := newRig(t, Policy{Interval: 24 * time.Hour})

	variant := func(name string, body []byte) string {
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	flipped := bytes.Clone(raw)
	flipped[len(flipped)-10] ^= 0x01
	cases := map[string]struct {
		file, pass string
		want       error
		mention    string
	}{
		"another instance's key": {tk.Path, other.pass, ErrWrongKey, ""},
		"a plain database":       {r.dbPath, r.pass, ErrNotABackup, ""},
		"an empty file":          {variant("empty.age", nil), r.pass, ErrNotABackup, ""},
		"a truncated copy":       {variant("cut.age", raw[:len(raw)-100]), r.pass, ErrDamaged, ""},
		"a flipped bit":          {variant("flip.age", flipped), r.pass, ErrDamaged, ""},
		"something appended":     {variant("long.age", append(bytes.Clone(raw), 0)), r.pass, ErrDamaged, ""},
		"only the header":        {variant("head.age", raw[:len(ageMagic)+20]), r.pass, ErrDamaged, ""},
		// 2^19: above MaxWorkFactor, below age's own default ceiling of 2^22,
		// so it is this package's ceiling that refuses it.
		"too much work demanded": {
			variant("slow.age", bytes.Replace(raw, []byte(" 15\n"), []byte(" 19\n"), 1)), r.pass, ErrDamaged,
			"work factor"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			started := time.Now()
			_, err := Verify(t.Context(), tc.file, tc.pass)
			if !errors.Is(err, tc.want) || !strings.Contains(fmt.Sprint(err), tc.mention) {
				t.Fatalf("err = %v, want %v mentioning %q", err, tc.want, tc.mention)
			}
			// A file asking for 2^25 is refused before any of that work.
			if time.Since(started) > 5*time.Second {
				t.Fatalf("took %s to refuse", time.Since(started))
			}
		})
	}
}

func TestARestoreWritesANewDatabaseAndNothingElse(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	tk := r.take()

	to := filepath.Join(t.TempDir(), "restored.db")
	rep, err := Restore(t.Context(), tk.Path, to, r.pass, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SHA256 != tk.SHA256 {
		t.Fatalf("report = %+v", rep)
	}
	fi, err := os.Stat(to)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("restored file: %v, %v", fi.Mode().Perm(), err)
	}

	// It is the server's database: it opens the way the server opens it.
	d, err := db.Open(db.Options{Path: to})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if applied, err := d.Migrate(t.Context()); err != nil || len(applied) != 0 {
		t.Fatalf("migrations on a restored current backup: %v, %v", applied, err)
	}
	var got string
	if err := d.QueryRowContext(t.Context(),
		`SELECT value FROM setting WHERE key = 'test.marker'`).Scan(&got); err != nil || got != marker {
		t.Fatalf("restored %q, %v", got, err)
	}
}

func TestARestoreNeverWritesOverAnything(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	tk := r.take()

	for _, side := range []string{"", "-wal", "-shm", "-journal"} {
		t.Run("with "+side, func(t *testing.T) {
			to := filepath.Join(t.TempDir(), "cms.db")
			if err := os.WriteFile(to+side, []byte("the operator's"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Restore(t.Context(), tk.Path, to, r.pass, nil); !errors.Is(err, ErrTargetExists) {
				t.Fatalf("err = %v, want ErrTargetExists", err)
			}
			if b, err := os.ReadFile(to + side); err != nil || string(b) != "the operator's" {
				t.Fatal("the existing file was touched")
			}
			if side != "" {
				if _, err := os.Stat(to); err == nil {
					t.Fatal("a database was restored beside a stale " + side)
				}
			}
		})
	}
}

// sealDB encrypts a database file as a backup would be, without the checks a
// backup being taken applies: how a test gets a backup of a database this
// build would never have backed up.
func sealDB(t *testing.T, r *rig, d *db.DB, name string) string {
	t.Helper()
	snap := filepath.Join(t.TempDir(), "snap.db")
	if err := createEmpty(snap); err != nil {
		t.Fatal(err)
	}
	if err := d.BackupTo(t.Context(), snap); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), name)
	if _, err := seal(snap, out, r.pass); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestABackupFromANewerBuildIsNotRestored(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	if _, err := r.db.ExecContext(t.Context(),
		`INSERT INTO schema_migration VALUES (999, 'from_the_future', 'abc', '2030-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	file := sealDB(t, r, r.db, "future.db.age")

	to := filepath.Join(t.TempDir(), "restored.db")
	_, err := Restore(t.Context(), file, to, r.pass, nil)
	if !errors.Is(err, db.ErrUnknownMigration) || !strings.Contains(err.Error(), "newer version") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(to); err == nil {
		t.Fatal("a backup this build cannot read was left at the target")
	}

	// And a backup being taken of it is refused too: a snapshot this build
	// would misread is not a backup of this instance.
	if _, err := r.svc.Take(r.admin, "", ""); !errors.Is(err, db.ErrUnknownMigration) {
		t.Fatalf("Take err = %v", err)
	}
}

func TestAnOlderBackupIsRestoredAndMigratedByTheServer(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	ours, err := db.LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	old, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "old.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if _, err := old.ExecContext(t.Context(), `CREATE TABLE schema_migration (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	// The database as the build before last would have left it.
	for _, m := range ours[:len(ours)-2] {
		if _, err := old.ExecContext(t.Context(), m.SQL); err != nil {
			t.Fatal(err)
		}
		if _, err := old.ExecContext(t.Context(), `INSERT INTO schema_migration VALUES (?, ?, ?, '2026-01-01T00:00:00Z')`,
			m.Version, m.Name, m.Checksum); err != nil {
			t.Fatal(err)
		}
	}
	file := sealDB(t, r, old, "old.db.age")

	to := filepath.Join(t.TempDir(), "restored.db")
	rep, err := Restore(t.Context(), file, to, r.pass, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Contents.Schema.Version != len(ours)-2 || rep.Contents.Schema.Latest != len(ours) {
		t.Fatalf("schema = %+v", rep.Contents.Schema)
	}
	d, err := db.Open(db.Options{Path: to})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if applied, err := d.Migrate(t.Context()); err != nil || len(applied) != 2 {
		t.Fatalf("the server's migrations on the restored backup: %v, %v", applied, err)
	}
}

// A backup holds who was signed in and which tokens worked when it was taken.
// Restored as it is, a session stolen and ended since — or a token that leaked
// and was revoked since — would work again. So a restore ends them all, and
// says so in the restored database's own audit log.
func TestARestoredDatabaseIsFencedBeforeItIsHandedOver(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	for _, stmt := range []string{
		`INSERT INTO role (id, name, rank, builtin, created_at, updated_at) VALUES (9, 'r', 1, 0, 'x', 'x')`,
		`INSERT INTO app_user (id, username, email, password_hash, state, role_id, created_at, updated_at)
		 VALUES (7, 'sam', 'sam@example.com', 'h', 'active', 9, 'x', 'x')`,
		`INSERT INTO session (id, user_id, refresh_hash, created_at, last_seen_at, idle_expires_at, absolute_expires_at)
		 VALUES ('live-1', 7, 'h1', 'x', 'x', 'x', 'x'), ('live-2', 7, 'h2', 'x', 'x', 'x', 'x')`,
		`INSERT INTO session (id, user_id, refresh_hash, created_at, last_seen_at, idle_expires_at, absolute_expires_at,
		 revoked_at, revoked_reason) VALUES ('ended', 7, 'h3', 'x', 'x', 'x', 'x', '2026-09-01T00:00:00Z', 'logout')`,
		`INSERT INTO api_token (user_id, name, token_hash, created_at) VALUES (7, 'script', 'th1', 'x')`,
	} {
		if _, err := r.db.ExecContext(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	tk := r.take()

	to := filepath.Join(t.TempDir(), "restored.db")
	restoredAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	rep, err := Restore(t.Context(), tk.Path, to, r.pass, func() time.Time { return restoredAt })
	if err != nil {
		t.Fatal(err)
	}
	if rep.Fenced != (Fenced{SessionsEnded: 2, TokensRevoked: 1}) {
		t.Fatalf("fenced = %+v", rep.Fenced)
	}
	for _, side := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(to + side); err == nil {
			t.Errorf("the restore left %s beside the database", side)
		}
	}

	d, err := db.OpenSnapshot(to)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var live int
	if err := d.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM session WHERE revoked_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("%d sessions live in the restored database, %v", live, err)
	}
	var reason string
	if err := d.QueryRowContext(t.Context(),
		`SELECT revoked_reason FROM session WHERE id = 'ended'`).Scan(&reason); err != nil || reason != "logout" {
		t.Fatalf("a session already ended was rewritten: %q, %v", reason, err)
	}
	if err := d.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM api_token WHERE revoked_at IS NULL`).Scan(&live); err != nil || live != 0 {
		t.Fatalf("%d tokens live in the restored database, %v", live, err)
	}
	var actor, detail, when string
	if err := d.QueryRowContext(t.Context(), `SELECT actor_label, detail, occurred_at FROM audit_event
		WHERE action = 'system.backup.restored'`).Scan(&actor, &detail, &when); err != nil {
		t.Fatal(err)
	}
	if actor != "console:restore" || !strings.Contains(detail, tk.Name) || !strings.Contains(detail, tk.SHA256) ||
		!strings.Contains(detail, "2 session(s) ended and 1 API token(s) revoked") ||
		!strings.HasPrefix(when, "2026-10-01T12:00:00") {
		t.Fatalf("restore record: %s / %s / %s", actor, detail, when)
	}

	// The live database is untouched by any of it.
	if err := r.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM session WHERE revoked_at IS NULL`).Scan(&live); err != nil || live != 2 {
		t.Fatalf("the live database changed: %d, %v", live, err)
	}
}

// Verifying changes nothing: it is a question, not a restore.
func TestVerifyingFencesNothing(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	tk := r.take()
	rep, err := Verify(t.Context(), tk.Path, r.pass)
	if err != nil || rep.Fenced != (Fenced{}) {
		t.Fatalf("%+v, %v", rep, err)
	}
}

// A backup whose database fails SQLite's own integrity check is refused, not
// restored. The damage is made the way SQLite's own tests make it — an index
// whose definition names a column its entries were not built from — and made
// in the snapshot itself, after VACUUM INTO: VACUUM rebuilds every index, so
// damage in the live database would be repaired on the way into the backup.
func TestABackupOfADamagedDatabaseIsNotRestored(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	if _, err := r.db.ExecContext(t.Context(), `CREATE TABLE damage (a INTEGER, b INTEGER);
		CREATE INDEX damage_a ON damage (a);
		INSERT INTO damage VALUES (1, 100), (2, 200), (3, 300)`); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(t.TempDir(), "snap.db")
	if err := createEmpty(snap); err != nil {
		t.Fatal(err)
	}
	if err := r.db.BackupTo(t.Context(), snap); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(db.Options{Path: snap})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := d.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`PRAGMA writable_schema = ON`,
		`UPDATE sqlite_schema SET sql = 'CREATE INDEX damage_a ON damage (b)' WHERE name = 'damage_a'`,
		`PRAGMA writable_schema = OFF`,
	} {
		if _, err := conn.ExecContext(t.Context(), stmt); err != nil {
			t.Fatal(stmt, err)
		}
	}
	_ = conn.Close()
	_ = d.Close()
	file := filepath.Join(t.TempDir(), "damaged.db.age")
	if _, err := seal(snap, file, r.pass); err != nil {
		t.Fatal(err)
	}

	to := filepath.Join(t.TempDir(), "restored.db")
	if _, err := Restore(t.Context(), file, to, r.pass, nil); err == nil ||
		!strings.Contains(err.Error(), "integrity_check") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(to); err == nil {
		t.Fatal("a damaged backup was left at the target")
	}
	if _, err := Verify(t.Context(), file, r.pass); err == nil || !strings.Contains(err.Error(), "integrity_check") {
		t.Fatalf("verify err = %v", err)
	}
}

package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

// marker is a value put in the database so a test can look for it: absent from
// every encrypted byte, present in whatever the backup decrypts to.
const marker = "plaintext-marker-5f1c9e"

type rig struct {
	t      *testing.T
	root   string
	dbPath string
	db     *db.DB
	audit  *audit.Logger
	svc    *Service
	pass   string
	clock  time.Time
	admin  context.Context
}

func newRig(t *testing.T, p Policy) *rig {
	t.Helper()
	r := &rig{t: t, root: t.TempDir(), clock: time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)}
	data := filepath.Join(r.root, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	r.dbPath = filepath.Join(data, "cms.db")
	d, err := db.Open(db.Options{Path: r.dbPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(t.Context(),
		`INSERT INTO setting (key, value, updated_at) VALUES ('test.marker', ?, '2026-09-27T09:00:00Z')`,
		marker); err != nil {
		t.Fatal(err)
	}
	r.db = d

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	c, err := secrets.NewCipherFromBase64(key)
	if err != nil {
		t.Fatal(err)
	}
	r.pass = c.BackupPassphrase()

	if p.Dir == "" {
		p.Dir = filepath.Join(r.root, "backups")
		p.CreateDir = true
	}
	if p.Keep == 0 {
		p.Keep = 7 * 24 * time.Hour
	}
	if p.KeepMin == 0 {
		p.KeepMin = 3
	}
	r.audit = audit.New(d, func() time.Time { return r.clock })
	r.svc, err = New(d, r.dbPath, r.pass, p, r.audit, func() time.Time { return r.clock })
	if err != nil {
		t.Fatal(err)
	}
	r.admin = r.as(1, "jacob", authz.PermSystemSettings, authz.PermViewAuditLog)
	return r
}

func (r *rig) as(id int64, name string, perms ...authz.Permission) context.Context {
	return authz.WithPrincipal(r.t.Context(), &authz.Principal{
		UserID: id, Username: name, State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 1, Name: "role", Rank: 100, Permissions: authz.NewPermissionSet(perms...)},
	})
}

func (r *rig) take() Taken {
	r.t.Helper()
	tk, err := r.svc.Take(r.admin, "192.0.2.10", "test-agent")
	if err != nil {
		r.t.Fatal(err)
	}
	return tk
}

// events returns the audit log's records of one action, newest first.
func (r *rig) events(a audit.Action) []audit.Event {
	r.t.Helper()
	evs, err := r.audit.List(r.admin, audit.Query{Action: a})
	if err != nil {
		r.t.Fatal(err)
	}
	return evs
}

// names lists a directory.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// plant makes a file that looks like a backup taken at t, for tests of what
// is kept and deleted: those read names, never contents.
func (r *rig) plant(at time.Time) string {
	r.t.Helper()
	name := namePrefix + at.UTC().Format(stampLayout) + nameSuffix
	if err := os.MkdirAll(r.svc.policy.Dir, 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.svc.policy.Dir, name), []byte("x"), 0o600); err != nil {
		r.t.Fatal(err)
	}
	return name
}

func TestABackupIsEncryptedCheckedAndRecorded(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	tk := r.take()

	if !namePattern.MatchString(tk.Name) || !tk.TakenAt.Equal(r.clock) {
		t.Fatalf("taken = %+v", tk.Backup)
	}
	if tk.Contents.Schema.Version == 0 || tk.Contents.Schema.Version != tk.Contents.Schema.Latest {
		t.Fatalf("schema = %+v; a backup is of the fully migrated database", tk.Contents.Schema)
	}

	// Private: the directory 0700 and the file 0600.
	di, err := os.Stat(r.svc.policy.Dir)
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("backup directory: %v, %v", di.Mode().Perm(), err)
	}
	raw, err := os.ReadFile(tk.Path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(tk.Path)
	if err != nil || fi.Mode().Perm() != 0o600 || fi.Size() != tk.Size {
		t.Fatalf("backup file: %v, size %d vs %d, %v", fi.Mode().Perm(), fi.Size(), tk.Size, err)
	}

	// Encrypted: an age file, with nothing of the database readable in it.
	if !bytes.HasPrefix(raw, ageMagic) {
		t.Fatal("the backup is not an age file")
	}
	// One scrypt stanza, at the work factor ADR-0029 chose — low enough for a
	// small machine, and under the most MaxWorkFactor lets a file demand.
	header, _, _ := bytes.Cut(raw, []byte("\n---"))
	if !bytes.Contains(header, []byte("\n-> scrypt ")) || !bytes.HasSuffix(bytes.SplitN(header, []byte("\n"), 3)[1], []byte(" 15")) ||
		bytes.Count(header, []byte("\n-> ")) != 1 {
		t.Fatalf("header = %q", header)
	}
	for _, plain := range []string{marker, "SQLite format 3", "schema_migration"} {
		if bytes.Contains(raw, []byte(plain)) {
			t.Fatalf("%q is readable in the encrypted backup", plain)
		}
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != tk.SHA256 {
		t.Fatalf("recorded sha256 %s, file's %s", tk.SHA256, got)
	}

	// And it decrypts — with age itself, not with this package — to a
	// database holding what the live one held.
	id, err := age.NewScryptIdentity(r.pass)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := age.Decrypt(bytes.NewReader(raw), id)
	if err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored.db")
	body, err := io.ReadAll(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restored, body, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := db.OpenSnapshot(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var got string
	if err := s.QueryRowContext(t.Context(),
		`SELECT value FROM setting WHERE key = 'test.marker'`).Scan(&got); err != nil || got != marker {
		t.Fatalf("the backup holds %q, %v", got, err)
	}

	// Nothing left behind: no plaintext beside the database, no partial file.
	for _, n := range names(t, filepath.Dir(r.dbPath)) {
		if strings.Contains(n, "snapshot") {
			t.Errorf("the plaintext snapshot %s was left behind", n)
		}
	}
	if n := names(t, r.svc.policy.Dir); len(n) != 1 || n[0] != tk.Name {
		t.Errorf("the backup directory holds %v", n)
	}

	// Recorded: who, from where, and the hash a copy can be checked against.
	evs := r.events(audit.ActionBackupCreated)
	if len(evs) != 1 {
		t.Fatalf("%d backup records", len(evs))
	}
	e := evs[0]
	if e.ActorLabel != "jacob" || e.ActorUserID == nil || *e.ActorUserID != 1 ||
		e.SourceIP != "192.0.2.10" || e.TargetID != tk.Name || e.Outcome != audit.OutcomeSuccess ||
		!strings.Contains(e.Detail, tk.SHA256) {
		t.Fatalf("record = %+v", e)
	}
}

func TestOnlyAnAdministratorCanTakeOrListBackups(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	for name, ctx := range map[string]context.Context{
		"anonymous":             t.Context(),
		"a user":                r.as(2, "sam", authz.PermBrowse, authz.PermStream),
		"a manager of indexers": r.as(3, "pat", authz.PermManageIndexers, authz.PermViewAuditLog),
	} {
		if _, err := r.svc.Take(ctx, "", ""); !authz.IsDenied(err) {
			t.Errorf("%s: Take err = %v, want a denial", name, err)
		}
		if _, err := r.svc.Status(ctx); !authz.IsDenied(err) {
			t.Errorf("%s: Status err = %v, want a denial", name, err)
		}
	}
	if _, err := os.Stat(r.svc.policy.Dir); err == nil {
		t.Fatal("a refused backup still created the directory")
	}
}

func TestASnapshotThatFailsItsChecksIsNotKept(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	var checked string
	var mode os.FileMode
	r.svc.check = func(_ context.Context, path string) (Contents, error) {
		checked = path
		if fi, err := os.Stat(path); err == nil {
			mode = fi.Mode().Perm()
		}
		return Contents{}, errors.New("db: integrity_check reported: *** in database main ***")
	}

	_, err := r.svc.Take(r.admin, "", "")
	if err == nil || !strings.Contains(err.Error(), "failed its checks, so nothing was kept") {
		t.Fatalf("err = %v", err)
	}
	if checked == "" || filepath.Dir(checked) != filepath.Dir(r.dbPath) {
		t.Fatalf("the snapshot was checked at %q, want beside the database", checked)
	}
	// The plaintext holds everything the database does, so it is private from
	// the moment it exists — not 0644 less the umask, which is what SQLite
	// would have created.
	if mode != 0o600 {
		t.Fatalf("the snapshot was %v while it existed", mode)
	}
	if _, err := os.Stat(checked); err == nil {
		t.Error("the plaintext snapshot of a failed backup was left behind")
	}
	if n := names(t, r.svc.policy.Dir); len(n) != 0 {
		t.Errorf("a failed backup left %v", n)
	}
	evs := r.events(audit.ActionBackupCreated)
	if len(evs) != 1 || evs[0].Outcome != audit.OutcomeFailure ||
		!strings.Contains(evs[0].Detail, "integrity_check") {
		t.Fatalf("records = %+v", evs)
	}
}

func TestTheHourlyCheckTakesABackupOnlyWhenOneIsDue(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	ctx := t.Context() // the schedule: no person behind it

	sum, err := r.svc.RunScheduled(ctx)
	if err != nil || !strings.HasPrefix(sum, "took cmediastack-") || !strings.HasSuffix(sum, "1 kept") {
		t.Fatalf("first run: %q, %v", sum, err)
	}
	if evs := r.events(audit.ActionBackupCreated); len(evs) != 1 || evs[0].ActorLabel != systemActor ||
		evs[0].ActorUserID != nil {
		t.Fatalf("the schedule's backup was recorded as %+v", evs)
	}

	r.clock = r.clock.Add(3*time.Hour + 12*time.Minute)
	sum, err = r.svc.RunScheduled(ctx)
	if err != nil || sum != "the newest backup is 3h12m old; the next is due in 20h48m; 1 kept" {
		t.Fatalf("not yet due: %q, %v", sum, err)
	}

	r.clock = r.clock.Add(21 * time.Hour)
	if sum, err = r.svc.RunScheduled(ctx); err != nil || !strings.HasPrefix(sum, "took ") ||
		!strings.HasSuffix(sum, "2 kept") {
		t.Fatalf("due again: %q, %v", sum, err)
	}
}

// The disk remembers when the last backup was; the process does not. A
// restart, which resets every ticker, changes nothing here.
func TestTheScheduleIsKeptByTheBackupsOnDisk(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	r.plant(r.clock.Add(-2 * time.Hour))

	restarted, err := New(r.db, r.dbPath, r.pass, r.svc.policy, r.audit, func() time.Time { return r.clock })
	if err != nil {
		t.Fatal(err)
	}
	sum, err := restarted.RunScheduled(t.Context())
	if err != nil || !strings.HasPrefix(sum, "the newest backup is 2h old") {
		t.Fatalf("a fresh process took a backup that was not due: %q, %v", sum, err)
	}
}

func TestAFutureDatedBackupDoesNotStopTheSchedule(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	r.plant(r.clock.Add(72 * time.Hour)) // the clock was wrong when it was taken, or is now
	sum, err := r.svc.RunScheduled(t.Context())
	if err != nil || !strings.HasPrefix(sum, "took ") {
		t.Fatalf("%q, %v", sum, err)
	}
}

func TestScheduledBackupsCanBeOffWhilePruningStaysOn(t *testing.T) {
	r := newRig(t, Policy{Interval: 0})
	for d := 1; d <= 5; d++ {
		r.plant(r.clock.Add(-time.Duration(d) * 10 * 24 * time.Hour))
	}
	sum, err := r.svc.RunScheduled(t.Context())
	if err != nil || sum != "scheduled backups are off (backup.interval is 0s); deleted 2 older than 7 days; 3 kept" {
		t.Fatalf("%q, %v", sum, err)
	}
	// A person can still take one.
	r.take()
}

func TestPruningGoesByAgeKeepsTheNewestAndTouchesNothingElse(t *testing.T) {
	// The newest two are kept however old; the third is kept for being
	// younger than a week; the last two go for being older.
	r := newRig(t, Policy{Interval: 0, Keep: 7 * 24 * time.Hour, KeepMin: 2})
	day := 24 * time.Hour
	keep := []string{r.plant(r.clock.Add(-1 * day)), r.plant(r.clock.Add(-2 * day)), r.plant(r.clock.Add(-3 * day))}
	gone := []string{r.plant(r.clock.Add(-9 * day)), r.plant(r.clock.Add(-10 * day))}

	dir := r.svc.policy.Dir
	outside := filepath.Join(r.root, "precious.db")
	if err := os.WriteFile(outside, []byte("not a backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := r.clock.Add(-400 * day).UTC().Format(stampLayout)
	bystanders := map[string]func(string) error{
		"notes.txt": func(p string) error { return os.WriteFile(p, []byte("mine"), 0o600) },
		// Close to a backup's name, and not one.
		"cmediastack-" + old + ".db.age.bak":  func(p string) error { return os.WriteFile(p, []byte("mine"), 0o600) },
		"cmediastack-20250101T000000Z.db.age": func(p string) error { return os.WriteFile(p, []byte("mine"), 0o600) },
		// Named exactly like an old backup, and a symlink: not ours to follow.
		"cmediastack-" + old + ".db.age": func(p string) error { return os.Symlink(outside, p) },
		// Named like an old backup, and a directory.
		"cmediastack-" + r.clock.Add(-401*day).UTC().Format(stampLayout) + ".db.age": func(p string) error { return os.Mkdir(p, 0o700) },
	}
	for name, create := range bystanders {
		if err := create(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}

	sum, err := r.svc.RunScheduled(t.Context())
	if err != nil || sum != "scheduled backups are off (backup.interval is 0s); deleted 2 older than 7 days; 3 kept" {
		t.Fatalf("%q, %v", sum, err)
	}
	for _, n := range keep {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s was deleted: it is one of the newest two, or younger than a week", n)
		}
	}
	for _, n := range gone {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			t.Errorf("%s was kept: it is older than a week and not one of the newest two", n)
		}
	}
	for n := range bystanders {
		if _, err := os.Lstat(filepath.Join(dir, n)); err != nil {
			t.Errorf("%s is not a backup and was deleted", n)
		}
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "not a backup" {
		t.Fatal("a symlink's target was touched")
	}

	evs := r.events(audit.ActionBackupPruned)
	if len(evs) != 2 {
		t.Fatalf("%d prune records, want 2", len(evs))
	}
	for _, e := range evs {
		if e.ActorLabel != systemActor || !strings.Contains(e.Detail, "older than backup.keep (7 days)") {
			t.Errorf("record = %+v", e)
		}
	}
}

// A schedule that stopped working must not delete the last good backups on its
// way out: they are the ones worth having.
func TestAFailingScheduleKeepsItsLastBackups(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour, Keep: 7 * 24 * time.Hour, KeepMin: 3})
	day := 24 * time.Hour
	var planted []string
	for d := 30; d <= 33; d++ {
		planted = append(planted, r.plant(r.clock.Add(-time.Duration(d)*day)))
	}
	r.svc.check = func(context.Context, string) (Contents, error) {
		return Contents{}, errors.New("disk trouble")
	}

	sum, err := r.svc.RunScheduled(t.Context())
	if err == nil || !strings.Contains(err.Error(), "disk trouble") ||
		sum != "deleted 1 older than 7 days; 3 kept" {
		t.Fatalf("%q, %v", sum, err)
	}
	for _, n := range planted[:3] {
		if _, err := os.Stat(filepath.Join(r.svc.policy.Dir, n)); err != nil {
			t.Errorf("%s was deleted while backups were failing", n)
		}
	}
}

// Pruning is the schedule's, by age. Taking backups — however many, however
// fast, by whoever holds an administrator's session — deletes none.
func TestTakingBackupsDeletesNothing(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour, Keep: 7 * 24 * time.Hour, KeepMin: 1})
	old := r.plant(r.clock.Add(-30 * 24 * time.Hour))
	for i := 0; i < 5; i++ {
		r.take()
		r.clock = r.clock.Add(time.Second)
	}
	if _, err := os.Stat(filepath.Join(r.svc.policy.Dir, old)); err != nil {
		t.Fatal("taking backups deleted an old one")
	}
	if n := len(r.events(audit.ActionBackupPruned)); n != 0 {
		t.Fatalf("%d prune records", n)
	}
}

func TestOneBackupAtATime(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	r.svc.mu.Lock()
	if _, err := r.svc.Take(r.admin, "", ""); !errors.Is(err, ErrBusy) {
		t.Errorf("Take err = %v, want ErrBusy", err)
	}
	if sum, err := r.svc.RunScheduled(t.Context()); err != nil || sum != "a backup is already being taken" {
		t.Errorf("RunScheduled = %q, %v", sum, err)
	}
	r.svc.mu.Unlock()
	r.take()
}

func TestWhatACrashLeftBehindIsSweptAndNothingElse(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	data := filepath.Dir(r.dbPath)
	stamp := r.clock.Add(-time.Hour).UTC().Format(stampLayout)
	if err := os.MkdirAll(r.svc.policy.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := []string{
		filepath.Join(data, ".cmediastack-snapshot-"+stamp+".db"),
		filepath.Join(data, ".cmediastack-snapshot-"+stamp+".db-journal"),
		filepath.Join(r.svc.policy.Dir, ".cmediastack-"+stamp+".db.age.partial"),
	}
	kept := []string{
		filepath.Join(data, ".cmediastack-snapshot-notes.db"),
		filepath.Join(data, "cms.db.bak"),
		filepath.Join(r.svc.policy.Dir, ".cmediastack-notes.partial"),
	}
	for _, p := range append(append([]string{}, stale...), kept...) {
		if err := os.WriteFile(p, []byte("left behind"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	r.take()
	for _, p := range stale {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was not swept", filepath.Base(p))
		}
	}
	for _, p := range kept {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was swept, and was not ours", filepath.Base(p))
		}
	}
	if err := r.db.PingContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// A directory the operator named is never created: one that has vanished is
// more often an unmounted disk than a forgotten mkdir, and creating it would
// put the backups back on this host's disk under the mount point.
func TestANamedBackupDirectoryIsNeverCreated(t *testing.T) {
	root := t.TempDir()
	nas := filepath.Join(root, "mnt", "nas", "cms-backups")
	r := newRig(t, Policy{Dir: nas, CreateDir: false, Interval: 24 * time.Hour})

	_, err := r.svc.Take(r.admin, "", "")
	if err == nil || !strings.Contains(err.Error(), "does not exist") ||
		!strings.Contains(err.Error(), "not mounted") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "mnt")); err == nil {
		t.Fatal("the directory was created anyway")
	}

	if err := os.MkdirAll(nas, 0o755); err != nil {
		t.Fatal(err)
	}
	r.take()
}

func TestStatusShowsThePolicyAndWhenTheNextIsDue(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	st, err := r.svc.Status(r.admin)
	if err != nil || len(st.Backups) != 0 || st.NextDue == nil || !st.NextDue.Equal(r.clock) {
		t.Fatalf("no backups yet: %+v, %v", st, err)
	}
	tk := r.take()
	r.clock = r.clock.Add(time.Hour)
	st, err = r.svc.Status(r.admin)
	if err != nil || len(st.Backups) != 1 || st.Backups[0].Name != tk.Name ||
		!st.NextDue.Equal(tk.TakenAt.Add(24*time.Hour)) || st.KeepMin != 3 {
		t.Fatalf("%+v, %v", st, err)
	}

	off := newRig(t, Policy{Interval: 0})
	if st, err := off.svc.Status(off.admin); err != nil || st.NextDue != nil {
		t.Fatalf("scheduled backups off: %+v, %v", st, err)
	}
}

func TestShortDurationsReadNaturally(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second:              "under a minute",
		3*time.Hour + 12*time.Minute:  "3h12m",
		20*time.Hour + 48*time.Minute: "20h48m",
		2 * time.Hour:                 "2h",
		24 * time.Hour:                "1 day",
		7 * 24 * time.Hour:            "7 days",
		25*time.Hour + 30*time.Second: "25h1m",
		45 * time.Minute:              "45m",
	} {
		if got := short(d); got != want {
			t.Errorf("short(%s) = %q, want %q", d, got, want)
		}
	}
}

// Damage the integrity check cannot see — a structurally sound page holding a
// reference to nothing — still stops a backup being kept as if it were good.
func TestADatabaseWithABrokenReferenceIsNotBackedUpAsGood(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	conn, err := r.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`PRAGMA foreign_keys = OFF`,
		`INSERT INTO recovery_code (user_id, code_hash, created_at) VALUES (4242, 'x', 'x')`,
		`PRAGMA foreign_keys = ON`,
	} {
		if _, err := conn.ExecContext(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	_ = conn.Close()

	_, err = r.svc.Take(r.admin, "", "")
	if err == nil || !strings.Contains(err.Error(), "foreign_key_check") {
		t.Fatalf("err = %v", err)
	}
	if n := names(t, r.svc.policy.Dir); len(n) != 0 {
		t.Fatalf("kept %v", n)
	}
}

// A backup is of the fully migrated database. An instance always migrates
// before it serves, so a live database behind its binary is one something is
// wrong with.
func TestABackupIsOfTheFullyMigratedDatabase(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	if _, err := r.db.ExecContext(t.Context(),
		`DELETE FROM schema_migration WHERE version = (SELECT MAX(version) FROM schema_migration)`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Take(r.admin, "", ""); !errors.Is(err, db.ErrMigrationMissing) {
		t.Fatalf("err = %v, want ErrMigrationMissing", err)
	}
}

// confirm is what stands between an encrypted file and the name of a backup:
// the bytes on disk must be the bytes written, and they must decrypt to the
// snapshot that was checked.
func TestConfirmCatchesAFileThatIsNotWhatWasWritten(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	dir := t.TempDir()
	src := filepath.Join(dir, "snap.db")
	if err := os.WriteFile(src, []byte("a snapshot, for this purpose"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "out.age")
	sums, err := seal(src, dst, r.pass)
	if err != nil {
		t.Fatal(err)
	}
	if err := confirm(dst, r.pass, sums); err != nil {
		t.Fatalf("a good file: %v", err)
	}

	otherPlain := sums
	otherPlain.plain = bytes.Repeat([]byte{1}, 32)
	if err := confirm(dst, r.pass, otherPlain); err == nil || !strings.Contains(err.Error(), "not to the snapshot") {
		t.Errorf("a file decrypting to something else: %v", err)
	}
	otherCipher := sums
	otherCipher.cipher = bytes.Repeat([]byte{2}, 32)
	if err := confirm(dst, r.pass, otherCipher); err == nil || !strings.Contains(err.Error(), "not the file that was written") {
		t.Errorf("a file whose bytes changed: %v", err)
	}
	shorter := sums
	shorter.size--
	if err := confirm(dst, r.pass, shorter); err == nil {
		t.Error("a file of another length was confirmed")
	}
}

// Backups beside the database share its disk. The listing says so, because
// that is the one thing about the default that an operator most needs to hear.
func TestTheListingSaysWhetherBackupsShareTheDatabasesFilesystem(t *testing.T) {
	r := newRig(t, Policy{Interval: 24 * time.Hour})
	st, err := r.svc.Status(r.admin)
	if err != nil || st.SameFilesystem != nil {
		t.Fatalf("before any backup, with no directory: %+v, %v", st.SameFilesystem, err)
	}
	r.take()
	st, err = r.svc.Status(r.admin)
	if err != nil || st.SameFilesystem == nil || !*st.SameFilesystem {
		t.Fatalf("backups beside the database: %v, %v", st.SameFilesystem, err)
	}
}

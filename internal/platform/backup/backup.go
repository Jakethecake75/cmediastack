// Package backup takes, checks, keeps and restores encrypted backups of the
// database (ADR-0029).
//
// A backup is a VACUUM INTO snapshot that has passed SQLite's integrity and
// foreign-key checks and carries exactly this build's migrations, encrypted
// with age to a passphrase derived from the master key, then read back,
// decrypted and compared with the snapshot before it is given a backup's name.
//
// There is no route that serves a backup and none that restores one. The
// operator copies the directory off the host; a restore happens on the host,
// from the command line, into a new file.
package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

const (
	// stampLayout is when a backup was taken, in UTC, in its name. No colons:
	// backups get copied to SMB shares, and Windows refuses a colon in a name.
	// Milliseconds, so two backups taken in the same second are two files.
	stampLayout = "20060102T150405.000Z"

	namePrefix = "cmediastack-"
	nameSuffix = ".db.age"

	// systemActor is the audit log's name for the hourly check.
	systemActor = "system:backup"
)

var (
	// namePattern is a backup. Only files named like this are ever listed,
	// counted, or deleted; nothing else in the directory is touched.
	namePattern = regexp.MustCompile(`^cmediastack-(\d{8}T\d{6}\.\d{3}Z)\.db\.age$`)
	// partialPattern is a backup still being written and checked. A crash
	// leaves one behind, and the next backup removes it.
	partialPattern = regexp.MustCompile(`^\.cmediastack-\d{8}T\d{6}\.\d{3}Z\.db\.age\.partial$`)
	// snapshotPattern is a plaintext snapshot, beside the live database, and
	// the journal SQLite may keep beside it while writing it.
	snapshotPattern = regexp.MustCompile(`^\.cmediastack-snapshot-\d{8}T\d{6}\.\d{3}Z\.db(-journal)?$`)
)

// ErrBusy is a backup asked for while another is being taken.
var ErrBusy = errors.New("backup: a backup is already being taken")

// Policy is where backups go, when one is due, and how long they are kept.
type Policy struct {
	// Dir holds the encrypted backups.
	Dir string
	// CreateDir allows Dir to be created when it does not exist. Only the
	// default directory is created: see missingDir.
	CreateDir bool
	// Interval is how old the newest backup may get before the hourly check
	// takes another. Zero means never on a schedule.
	Interval time.Duration
	// Keep is how long a backup is kept.
	Keep time.Duration
	// KeepMin is how many of the newest backups are kept however old.
	KeepMin int
}

// Backup is one encrypted backup on disk.
type Backup struct {
	Name    string
	Path    string
	TakenAt time.Time
	Size    int64
}

// Contents is what a checked database holds.
type Contents struct {
	Schema db.Schema
	Census db.Census
}

// Taken is a backup just taken.
type Taken struct {
	Backup
	// SHA256 is the encrypted file's, hex: what the audit log records, so a
	// copy can be checked against it without the key.
	SHA256   string
	Contents Contents
}

// Status is the directory and the policy, as the admin screen shows them.
type Status struct {
	Dir      string
	Interval time.Duration
	Keep     time.Duration
	KeepMin  int
	// Backups are newest first.
	Backups []Backup
	// NextDue is when the hourly check will next find a backup due. Nil when
	// scheduled backups are off.
	NextDue *time.Time
	// SameFilesystem says the backups share a filesystem with the database, so
	// they will not survive losing it. Nil when it cannot be told — no backup
	// directory yet, or a platform that does not say.
	SameFilesystem *bool
}

// Service takes and prunes the backups of one database.
type Service struct {
	db *db.DB
	// workDir is the live database's own directory, where the plaintext
	// snapshot is written and checked. Never the backup directory: that may be
	// a NAS, and the plaintext should not travel.
	workDir string
	pass    string
	policy  Policy
	audit   *audit.Logger
	now     func() time.Time

	// mu makes backups one at a time. A second is refused rather than queued:
	// queueing it behind a slow one means an operator clicks, sees nothing
	// happen, and clicks again.
	mu sync.Mutex

	// check is what a snapshot must pass before it is encrypted. A field so a
	// test can make a snapshot fail without corrupting a database by hand.
	check func(ctx context.Context, path string) (Contents, error)
}

// New builds the service for the database at databasePath.
func New(database *db.DB, databasePath, passphrase string, p Policy,
	log *audit.Logger, now func() time.Time) (*Service, error) {

	switch {
	case database == nil:
		return nil, errors.New("backup: no database")
	case passphrase == "":
		return nil, errors.New("backup: no passphrase: backups are never written unencrypted")
	case strings.TrimSpace(p.Dir) == "":
		return nil, errors.New("backup: no backup directory")
	case p.Interval < 0, p.Keep <= 0, p.KeepMin < 1:
		return nil, fmt.Errorf("backup: interval %s, keep %s and keep_min %d are not a policy",
			p.Interval, p.Keep, p.KeepMin)
	}
	if now == nil {
		now = time.Now
	}
	s := &Service{
		db: database, workDir: filepath.Dir(databasePath), pass: passphrase,
		policy: p, audit: log, now: now,
	}
	s.check = func(ctx context.Context, path string) (Contents, error) {
		// complete: a backup is of the fully migrated database, or it is not
		// a backup of this instance as it stands.
		return inspect(ctx, path, true)
	}
	return s, nil
}

// Take takes a backup now, for someone who may change system settings — the
// admin screen's button.
//
// It deletes nothing. Pruning belongs to the hourly check and goes by age, so
// taking backups never pushes an older one out; a stolen administrator session
// taking fifty in a row costs disk, not history.
func (s *Service) Take(ctx context.Context, sourceIP, userAgent string) (Taken, error) {
	if err := authz.RequirePermission(ctx, authz.PermSystemSettings); err != nil {
		return Taken{}, err
	}
	if !s.mu.TryLock() {
		return Taken{}, ErrBusy
	}
	defer s.mu.Unlock()

	who := actorOf(ctx)
	who.ip, who.ua = sourceIP, userAgent
	return s.take(ctx, who)
}

// Status lists the backups and the policy.
func (s *Service) Status(ctx context.Context) (Status, error) {
	if err := authz.RequirePermission(ctx, authz.PermSystemSettings); err != nil {
		return Status{}, err
	}
	list, err := s.list()
	if err != nil {
		return Status{}, err
	}
	st := Status{
		Dir: s.policy.Dir, Interval: s.policy.Interval,
		Keep: s.policy.Keep, KeepMin: s.policy.KeepMin, Backups: list,
	}
	if s.policy.Interval > 0 {
		next := s.now()
		if len(list) > 0 {
			if due := list[0].TakenAt.Add(s.policy.Interval); due.After(next) {
				next = due
			}
		}
		st.NextDue = &next
	}
	if same, known := sameFilesystem(s.workDir, s.policy.Dir); known {
		st.SameFilesystem = &same
	}
	return st, nil
}

// RunScheduled is the hourly check: a backup if one is due, then the backups
// past backup.keep deleted.
//
// # Why this takes no principal
//
// What it can do is bounded by the policy, not by who calls it, for the reason
// FulfilOnDisk gives in the request package. It takes a backup only when the
// newest on disk is older than the interval, and deletes only by age, never
// one of the newest KeepMin. Anything able to call it could make it do exactly
// what the schedule would have done within the hour. A person who triggers the
// task from the admin screen is recorded as themselves; the schedule is
// recorded as system:backup.
func (s *Service) RunScheduled(ctx context.Context) (string, error) {
	if !s.mu.TryLock() {
		return "a backup is already being taken", nil
	}
	defer s.mu.Unlock()
	who := actorOf(ctx)

	list, err := s.list()
	if err != nil {
		return "", err
	}

	var parts []string
	var takeErr error
	if due, why := s.due(list); due {
		t, err := s.take(ctx, who)
		if err != nil {
			takeErr = err
		} else {
			parts = append(parts, fmt.Sprintf("took %s (%s, schema version %d)",
				t.Name, size(t.Size), t.Contents.Schema.Version))
			list = append([]Backup{t.Backup}, list...)
		}
	} else {
		parts = append(parts, why)
	}

	// Pruned even when the backup failed. KeepMin is what makes that safe: a
	// schedule that has stopped working keeps its newest backups however old
	// they get, and they are the ones worth having.
	pruned, pruneErr := s.prune(ctx, who, list)
	if len(pruned) > 0 {
		parts = append(parts, fmt.Sprintf("deleted %d older than %s", len(pruned), short(s.policy.Keep)))
	}
	parts = append(parts, fmt.Sprintf("%d kept", len(list)-len(pruned)))
	summary := strings.Join(parts, "; ")

	if err := errors.Join(takeErr, pruneErr); err != nil {
		// A failed run shows its error rather than its summary, so the summary
		// travels inside it.
		return summary, fmt.Errorf("%w (%s)", err, summary)
	}
	return summary, nil
}

// due reports whether the hourly check should take a backup, and when it
// should not, why not.
func (s *Service) due(list []Backup) (bool, string) {
	if s.policy.Interval <= 0 {
		return false, "scheduled backups are off (backup.interval is 0s)"
	}
	if len(list) == 0 {
		return true, ""
	}
	age := s.now().Sub(list[0].TakenAt)
	switch {
	case age >= s.policy.Interval:
		return true, ""
	case age < -s.policy.Interval:
		// The newest backup says it was taken more than an interval in the
		// future: the clock went back, or a name lies. Waiting for the clock
		// to catch up could mean no backup for a year, so take one.
		return true, ""
	}
	if age < 0 {
		age = 0
	}
	return false, fmt.Sprintf("the newest backup is %s old; the next is due in %s",
		short(age), short(s.policy.Interval-age))
}

// take writes a backup and records what happened, either way.
func (s *Service) take(ctx context.Context, who actor) (Taken, error) {
	t, err := s.write(ctx)
	if err != nil {
		_ = s.record(ctx, who, audit.Event{
			Action: audit.ActionBackupCreated, Outcome: audit.OutcomeFailure,
			TargetKind: "backup", Detail: "no backup was kept: " + err.Error(),
		})
		return Taken{}, err
	}
	err = s.record(ctx, who, audit.Event{
		Action: audit.ActionBackupCreated, TargetKind: "backup", TargetID: t.Name,
		Detail: fmt.Sprintf("%s: %s, schema version %d, checked, encrypted and decrypted "+
			"again to confirm it; sha256 %s", t.Name, size(t.Size), t.Contents.Schema.Version, t.SHA256),
		After: map[string]any{
			"file": t.Name, "bytes": t.Size, "sha256": t.SHA256,
			"schema_version": t.Contents.Schema.Version,
		},
	})
	if err != nil {
		return t, fmt.Errorf("backup: %s was taken and kept, but could not be recorded "+
			"in the audit log: %w", t.Name, err)
	}
	return t, nil
}

// write takes the snapshot, checks it, encrypts it, confirms the encrypted
// file, and only then gives it a backup's name.
func (s *Service) write(ctx context.Context) (Taken, error) {
	if err := s.prepareDir(); err != nil {
		return Taken{}, err
	}
	s.sweep()

	stamp := s.now().UTC().Format(stampLayout)
	takenAt, err := time.Parse(stampLayout, stamp)
	if err != nil {
		return Taken{}, fmt.Errorf("backup: %w", err)
	}
	name := namePrefix + stamp + nameSuffix
	final := filepath.Join(s.policy.Dir, name)
	if _, err := os.Lstat(final); err == nil {
		return Taken{}, fmt.Errorf("backup: %s already exists", name)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Taken{}, fmt.Errorf("backup: %w", err)
	}

	// 1. The snapshot, beside the live database and created 0600 before
	// SQLite writes into it: a file SQLite creates is 0644 less the umask, and
	// this one holds everything the database does.
	snapshot := filepath.Join(s.workDir, ".cmediastack-snapshot-"+stamp+".db")
	if err := createEmpty(snapshot); err != nil {
		return Taken{}, fmt.Errorf("backup: preparing the snapshot: %w", err)
	}
	defer removeQuietly(snapshot, snapshot+"-journal")
	if err := s.db.BackupTo(ctx, snapshot); err != nil {
		return Taken{}, fmt.Errorf("backup: %w", err)
	}

	// 2. Checked before anything is kept.
	contents, err := s.check(ctx, snapshot)
	if err != nil {
		return Taken{}, fmt.Errorf("backup: the snapshot failed its checks, so nothing was kept: %w", err)
	}

	// 3. Encrypted into a file no listing counts as a backup...
	partial := filepath.Join(s.policy.Dir, "."+name+".partial")
	sums, err := seal(snapshot, partial, s.pass)
	if err != nil {
		removeQuietly(partial)
		return Taken{}, fmt.Errorf("backup: encrypting: %w", err)
	}
	// 4. ...read back from disk, decrypted, and compared with the snapshot...
	if err := confirm(partial, s.pass, sums); err != nil {
		removeQuietly(partial)
		return Taken{}, fmt.Errorf("backup: the encrypted file was not what was written, "+
			"so it was not kept: %w", err)
	}
	// 5. ...and only then named as one.
	if err := os.Rename(partial, final); err != nil {
		removeQuietly(partial)
		return Taken{}, fmt.Errorf("backup: %w", err)
	}
	syncDir(s.policy.Dir)

	return Taken{
		Backup:   Backup{Name: name, Path: final, TakenAt: takenAt, Size: sums.size},
		SHA256:   fmt.Sprintf("%x", sums.cipher),
		Contents: contents,
	}, nil
}

// prepareDir makes sure the backup directory is there.
//
// Only the default directory is ever created. A directory the operator named
// is expected to exist already, because a named directory that has vanished is
// far more often a disk that is not mounted than one they forgot to create —
// and creating it would put the backups on this host's own disk, under the
// mount point, while the operator believed they were going somewhere else.
func (s *Service) prepareDir() error {
	fi, err := os.Stat(s.policy.Dir)
	switch {
	case err == nil && fi.IsDir():
		return nil
	case err == nil:
		return fmt.Errorf("backup: %s is not a directory", s.policy.Dir)
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("backup: %w", err)
	case !s.policy.CreateDir:
		return fmt.Errorf("backup: %s does not exist. Only the default backup directory is "+
			"created; a directory named in backup.dir is expected to be there already, because "+
			"a missing one is more often a disk that is not mounted — and creating it would put "+
			"the backups on this host's own disk instead", s.policy.Dir)
	}
	if err := os.MkdirAll(s.policy.Dir, 0o700); err != nil {
		return fmt.Errorf("backup: creating %s: %w", s.policy.Dir, err)
	}
	return nil
}

// sweep removes what a crash left behind: a plaintext snapshot beside the
// database, and a half-written backup. Only files with those exact names.
func (s *Service) sweep() {
	removeMatching(s.workDir, snapshotPattern)
	removeMatching(s.policy.Dir, partialPattern)
}

// prune deletes the backups older than Keep, except the newest KeepMin. list is
// newest first.
func (s *Service) prune(ctx context.Context, who actor, list []Backup) ([]Backup, error) {
	cutoff := s.now().Add(-s.policy.Keep)
	var pruned []Backup
	var errs []error
	for i, b := range list {
		if i < s.policy.KeepMin || !b.TakenAt.Before(cutoff) {
			continue
		}
		if err := os.Remove(b.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("backup: deleting %s: %w", b.Name, err))
			continue
		}
		pruned = append(pruned, b)
		if err := s.record(ctx, who, audit.Event{
			Action: audit.ActionBackupPruned, TargetKind: "backup", TargetID: b.Name,
			Detail: fmt.Sprintf("deleted %s, taken %s: older than backup.keep (%s); "+
				"the newest %d are kept however old they are",
				b.Name, b.TakenAt.Format(time.RFC3339), short(s.policy.Keep), s.policy.KeepMin),
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return pruned, errors.Join(errs...)
}

// list reads the backup directory: backups only, newest first.
func (s *Service) list() ([]Backup, error) {
	entries, err := os.ReadDir(s.policy.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: reading %s: %w", s.policy.Dir, err)
	}
	var out []Backup
	for _, e := range entries {
		// Regular files only. A symlink named like a backup is not one of
		// ours, and nothing here should follow it anywhere, least of all to
		// delete what it points at.
		if !e.Type().IsRegular() {
			continue
		}
		taken, ok := takenAt(e.Name())
		if !ok {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // gone between the listing and now
		}
		out = append(out, Backup{
			Name: e.Name(), Path: filepath.Join(s.policy.Dir, e.Name()),
			TakenAt: taken, Size: info.Size(),
		})
	}
	// The stamp sorts as the time does, and a name is unique where a time
	// might not be.
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// takenAt reads the time out of a backup's name.
func takenAt(name string) (time.Time, bool) {
	m := namePattern.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.Parse(stampLayout, m[1])
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

type actor struct {
	id     *int64
	label  string
	ip, ua string
}

// actorOf is the person behind ctx, or the schedule when there is none.
func actorOf(ctx context.Context) actor {
	if p := authz.FromContext(ctx); p != nil && !authz.IsSystem(p) {
		id := p.UserID
		return actor{id: &id, label: p.Username}
	}
	return actor{label: systemActor}
}

func (s *Service) record(ctx context.Context, who actor, e audit.Event) error {
	if s.audit == nil {
		return nil
	}
	e.ActorUserID, e.ActorLabel = who.id, who.label
	e.SourceIP, e.UserAgent = who.ip, who.ua
	return s.audit.Write(ctx, e)
}

// size is a byte count for a person.
func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// short is a duration for a person: whole minutes, no trailing zeros.
func short(d time.Duration) string {
	if d < time.Minute {
		return "under a minute"
	}
	if d%(24*time.Hour) == 0 {
		if days := d / (24 * time.Hour); days != 1 {
			return fmt.Sprintf("%d days", days)
		}
		return "1 day"
	}
	out := d.Round(time.Minute).String() // e.g. 3h12m0s
	out = strings.TrimSuffix(out, "0s")
	if strings.HasSuffix(out, "h0m") {
		out = strings.TrimSuffix(out, "0m")
	}
	return out
}

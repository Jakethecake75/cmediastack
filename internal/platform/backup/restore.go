package backup

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// ErrTargetExists is a restore aimed at a path that is already there.
var ErrTargetExists = errors.New("backup: the restore target already exists")

// Report is what checking a backup file found.
type Report struct {
	File string
	// Size and SHA256 are the encrypted file's. The SHA-256 is what the audit
	// log recorded when the backup was taken, so a copy can be matched to
	// its record.
	Size   int64
	SHA256 string
	// TakenAt is read from the file's name; zero when it has been renamed.
	TakenAt  time.Time
	Contents Contents
	// Fenced is what a restore ended in the restored database before handing
	// it over. Zero for a verification, which changes nothing.
	Fenced Fenced
}

// Fenced is what a restore ends before it hands the database over.
type Fenced struct {
	// SessionsEnded were live when the backup was taken. Everyone signs in
	// again — with their authenticator, which the backup holds.
	SessionsEnded int64
	// TokensRevoked were live when the backup was taken. Among them, any that
	// was revoked after the backup — because it leaked, say — would otherwise
	// be live again. Their owners issue new ones.
	TokensRevoked int64
}

// Verify decrypts a backup into a private temporary directory, checks what it
// decrypts to as a backup is checked when it is taken, and removes the
// plaintext: `cmediastack -verify-backup FILE`.
//
// A database behind this build passes — it is an older backup, and the server
// migrates it when it starts on it. One this build cannot read does not.
func Verify(ctx context.Context, file, passphrase string) (Report, error) {
	dir, err := os.MkdirTemp("", "cmediastack-verify-") // 0700
	if err != nil {
		return Report{}, fmt.Errorf("backup: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	return open(ctx, file, filepath.Join(dir, "backup.db"), passphrase)
}

// Restore decrypts a backup into a new database file at to, and checks it
// there: `cmediastack -restore-backup FILE -restore-to PATH`.
//
// It never writes over anything. The target, and the -wal, -shm and -journal
// files SQLite keeps beside a database, must not exist — a -wal file left
// beside a restored database is replayed into it the first time it is opened,
// which would quietly put back some of what the restore was meant to undo.
// Nothing is left at to unless the whole backup decrypted and passed.
//
// The restored database is then fenced before it is handed over: every
// session in it is ended, every API token revoked, and the restore written
// into its audit log. A backup holds who was signed in and which tokens worked
// when it was taken; restored as it is, a session stolen and ended since, or a
// token that leaked and was revoked since, would work again.
func Restore(ctx context.Context, file, to, passphrase string, now func() time.Time) (Report, error) {
	for _, p := range []string{to, to + "-wal", to + "-shm", to + "-journal"} {
		if _, err := os.Lstat(p); err == nil {
			return Report{File: file}, fmt.Errorf("%w: %s", ErrTargetExists, p)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return Report{File: file}, fmt.Errorf("backup: %w", err)
		}
	}
	rep, err := open(ctx, file, to, passphrase)
	if err != nil {
		if !errors.Is(err, ErrTargetExists) {
			removeQuietly(to)
		}
		return rep, err
	}
	if now == nil {
		now = time.Now
	}
	fenced, err := fence(ctx, to, rep, now)
	if err != nil {
		removeQuietly(to, to+"-wal", to+"-shm", to+"-journal")
		return rep, fmt.Errorf("backup: the backup decrypted and passed its checks, but the "+
			"restored database could not be fenced, so it was removed: %w", err)
	}
	rep.Fenced = fenced
	return rep, nil
}

// fence ends what a restored database would otherwise bring back to life, and
// records the restore in it. See Restore.
//
// It writes the identity tables directly — the one place outside that package
// that does — because it acts on a file no server has opened yet, from the host
// console, before there is a server to ask.
func fence(ctx context.Context, path string, rep Report, now func() time.Time) (Fenced, error) {
	d, err := db.Open(db.Options{Path: path})
	if err != nil {
		return Fenced{}, err
	}
	defer func() { _ = d.Close() }()

	at := now().UTC().Format(time.RFC3339Nano)
	var f Fenced
	err = d.InTx(ctx, func(tx db.Execer) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE session SET revoked_at = ?, revoked_reason = 'restored_from_backup' WHERE revoked_at IS NULL`, at)
		if err != nil {
			return err
		}
		if f.SessionsEnded, err = res.RowsAffected(); err != nil {
			return err
		}
		res, err = tx.ExecContext(ctx,
			`UPDATE api_token SET revoked_at = ? WHERE revoked_at IS NULL`, at)
		if err != nil {
			return err
		}
		f.TokensRevoked, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return Fenced{}, err
	}

	taken := "at a time its name no longer says"
	if !rep.TakenAt.IsZero() {
		taken = rep.TakenAt.Format(time.RFC3339)
	}
	err = audit.New(d, now).Write(ctx, audit.Event{
		// Whoever had a shell on the host, as for break-glass recovery.
		ActorLabel: "console:restore",
		Action:     audit.ActionBackupRestored,
		TargetKind: "backup",
		TargetID:   filepath.Base(rep.File),
		Detail: fmt.Sprintf("this database was restored from %s (sha256 %s), taken %s. "+
			"Everything recorded between then and this line happened, and is not in it. "+
			"%d session(s) ended and %d API token(s) revoked by the restore.",
			filepath.Base(rep.File), rep.SHA256, taken, f.SessionsEnded, f.TokensRevoked),
		After: map[string]any{
			"file": filepath.Base(rep.File), "sha256": rep.SHA256,
			"schema_version": rep.Contents.Schema.Version,
			"sessions_ended": f.SessionsEnded,
			"tokens_revoked": f.TokensRevoked,
		},
	})
	if err != nil {
		return Fenced{}, err
	}
	return f, nil
}

// open decrypts file into plain, which must not exist, and checks the result.
func open(ctx context.Context, file, plain, passphrase string) (Report, error) {
	rep := Report{File: file}
	if t, ok := takenAt(filepath.Base(file)); ok {
		rep.TakenAt = t
	}

	in, err := os.Open(file) // #nosec G304 -- the operator's own -verify-backup or -restore-backup argument
	if err != nil {
		return rep, fmt.Errorf("backup: %w", err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(plain, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- see above
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return rep, fmt.Errorf("%w: %s", ErrTargetExists, plain)
		}
		return rep, fmt.Errorf("backup: %w", err)
	}

	cipher := sha256.New()
	counted := &countingReader{r: io.TeeReader(in, cipher)}
	decrypted, err := decrypt(counted, passphrase)
	if err == nil {
		if _, cerr := io.Copy(out, decrypted); cerr != nil {
			err = fmt.Errorf("%w: %w", ErrDamaged, cerr)
		} else if _, derr := io.Copy(io.Discard, counted); derr != nil {
			err = fmt.Errorf("backup: %w", derr)
		}
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	rep.Size = counted.n
	rep.SHA256 = fmt.Sprintf("%x", cipher.Sum(nil))
	if err != nil {
		return rep, err
	}

	contents, err := inspect(ctx, plain, false)
	rep.Contents = contents
	if err != nil {
		return rep, fmt.Errorf("backup: it decrypted, but what it decrypted to failed its checks: %w", err)
	}
	return rep, nil
}

// inspect checks a database file that nothing else has open: SQLite's own
// integrity check, its foreign-key check, and the migrations it records
// against this build's. complete requires every one of this build's
// migrations, which a backup being taken must have and an older one being
// restored need not.
func inspect(ctx context.Context, path string, complete bool) (Contents, error) {
	d, err := db.OpenSnapshot(path)
	if err != nil {
		return Contents{}, err
	}
	defer func() { _ = d.Close() }()

	if err := d.IntegrityCheck(ctx); err != nil {
		return Contents{}, err
	}
	if err := d.ForeignKeyCheck(ctx); err != nil {
		return Contents{}, err
	}
	schema, err := d.CheckMigrations(ctx, complete)
	if err != nil {
		return Contents{Schema: schema}, err
	}
	census, err := d.TakeCensus(ctx)
	if err != nil {
		return Contents{Schema: schema}, err
	}
	return Contents{Schema: schema, Census: census}, nil
}

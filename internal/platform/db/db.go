// Package db owns the database connection, migrations and the transaction
// helper. Nothing outside this package and the per-domain repositories may
// build SQL.
//
// ADR-0004: SQLite (WAL) only for v1, via modernc.org/sqlite so the binary
// stays pure Go (ADR-0002).
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, registered as "sqlite"
)

// DB wraps *sql.DB with the pragmas and helpers the application relies on.
type DB struct {
	*sql.DB
}

// Options configure the connection.
//
// There is no read-only option. There was one, and it could never have worked:
// Open turns on WAL mode, which writes to the file's header, and a read-only
// connection refuses that write — so it failed to open any database that was
// not already in WAL mode, a backup's snapshot included. A file that is only to
// be read and checked is opened with OpenSnapshot.
type Options struct {
	Path        string
	BusyTimeout time.Duration
}

// uri is the SQLite URI for a file.
//
// Built, not formatted. "file:" + path is a URI, so a '?' or '#' in the path
// ends the path early: a database under a directory named "a#b" opened — and
// created — a file named "a" instead, and reported "no such table". The path
// is made absolute first because a relative one would be read as the URI's
// authority.
func uri(path, query string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("db: resolving %s: %w", path, err)
	}
	u := url.URL{Scheme: "file", Path: abs, RawQuery: query}
	return u.String(), nil
}

// Open connects, applies pragmas and verifies the connection.
//
// The pragmas are not tuning preferences:
//   - foreign_keys=ON makes the declared references actually enforced. SQLite
//     defaults it OFF, which silently turns every FK into a comment.
//   - journal_mode=WAL is required for concurrent readers alongside a writer.
//   - busy_timeout stops the single-writer model surfacing as SQLITE_BUSY.
//   - synchronous=NORMAL is the correct pairing with WAL: durable across
//     process crashes, and only at risk from an OS-level crash.
func Open(opts Options) (*DB, error) {
	if opts.Path == "" {
		return nil, fmt.Errorf("db: path is required")
	}
	if opts.BusyTimeout <= 0 {
		opts.BusyTimeout = 5 * time.Second
	}

	if err := private(opts.Path); err != nil {
		return nil, err
	}
	dsn, err := uri(opts.Path, fmt.Sprintf(
		"_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)&_pragma=synchronous(1)",
		opts.BusyTimeout.Milliseconds()))
	if err != nil {
		return nil, err
	}

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", opts.Path, err)
	}

	// WAL permits one writer. Bounding the pool makes contention explicit and
	// keeps SQLITE_BUSY inside the busy_timeout rather than surfacing as an
	// error under load.
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetConnMaxLifetime(time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("db: ping %s: %w", opts.Path, err)
	}

	d := &DB{DB: sqlDB}
	if err := d.verifyPragmas(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return d, nil
}

// private makes the database file readable by its owner alone.
//
// It holds everything: password hashes, authenticator secrets and credentials
// (sealed, but still), email addresses, what people watched, the audit log.
// SQLite creates a new database 0644 less the umask — found by listing the
// container's /config while testing backups, where the backups were 0600 and
// the database they were taken from was readable by every user on the host.
// SQLite gives the -wal and -shm files the database's own mode, so creating
// the database 0600 is enough for all three.
//
// A new file is created empty, which SQLite takes as a new database. An
// existing one that others can read is narrowed, and so are its -wal and -shm;
// that is best effort, because a filesystem that refuses a chmod (some network
// mounts do) should not stop the server, and it makes nothing worse.
func private(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- the operator's configured database path
	if err == nil {
		return f.Close()
	}
	if !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("db: creating %s: %w", path, err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o077 != 0 {
			_ = os.Chmod(p, fi.Mode().Perm()&0o700)
		}
	}
	return nil
}

// verifyPragmas asserts the pragmas actually took effect. A DSN typo that
// silently leaves foreign_keys off would disable every referential guarantee
// in the schema, so this is checked rather than assumed.
func (d *DB) verifyPragmas(ctx context.Context) error {
	var fk int
	if err := d.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		return fmt.Errorf("db: read foreign_keys pragma: %w", err)
	}
	if fk != 1 {
		return fmt.Errorf("db: foreign_keys pragma is off; referential integrity would not be enforced")
	}
	return nil
}

// BackupTo writes a consistent snapshot to path using VACUUM INTO: a single
// file that is a valid database, taken inside a read transaction, so writers
// carry on (ADR-0029).
//
// path must not exist, or must be an empty file; SQLite refuses anything else
// ("output file already exists"). Creating it empty first is how a caller
// chooses its permissions: a file SQLite creates itself is 0644 less the
// umask, and a snapshot holds everything the database does. The snapshot is in
// rollback-journal mode, not WAL, whatever the database's own mode.
func (d *DB) BackupTo(ctx context.Context, path string) error {
	if _, err := d.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("db: backup to %s: %w", path, err)
	}
	return nil
}

// OpenSnapshot opens a database file to be checked and read, never written: a
// backup's snapshot, or a database just restored from one.
//
// It is opened immutable. SQLite then takes no locks, creates no -wal or -shm
// file beside it, and refuses every write. That is only a safe promise about a
// file nothing else has open, which is the only kind this is for — never the
// live database.
func OpenSnapshot(path string) (*DB, error) {
	dsn, err := uri(path, "mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	return &DB{DB: sqlDB}, nil
}

// ForeignKeyCheck reports the first row whose reference points at nothing.
//
// The live database enforces its foreign keys, so a violation here is damage
// the integrity check cannot see: a page that is structurally sound and holds
// the wrong thing.
func (d *DB) ForeignKeyCheck(ctx context.Context) error {
	rows, err := d.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("db: foreign_key_check: %w", err)
	}
	defer func() { _ = rows.Close() }()

	violations := 0
	var first string
	for rows.Next() {
		var table, parent string
		var rowid sql.NullInt64
		var fkid int64
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return fmt.Errorf("db: foreign_key_check: %w", err)
		}
		if violations == 0 {
			first = fmt.Sprintf("a row of %s (rowid %d) refers to a %s that does not exist",
				table, rowid.Int64, parent)
		}
		violations++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("db: foreign_key_check: %w", err)
	}
	if violations > 0 {
		return fmt.Errorf("db: foreign_key_check found %d broken reference(s); the first: %s",
			violations, first)
	}
	return nil
}

// IntegrityCheck runs SQLite's own consistency check. Used by the
// database.integrity task and by every backup before it is kept.
func (d *DB) IntegrityCheck(ctx context.Context) error {
	var result string
	if err := d.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("db: integrity_check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("db: integrity_check reported: %s", result)
	}
	return nil
}

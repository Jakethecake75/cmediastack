package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration is one numbered schema change.
type Migration struct {
	Version int
	Name    string
	SQL     string
	// Checksum detects a migration that was edited after being applied, which
	// silently desynchronises deployments.
	Checksum string
}

// LoadMigrations reads and orders the embedded migrations.
func LoadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("db: read migrations: %w", err)
	}

	var out []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		name := e.Name()
		numPart, rest, ok := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
		if !ok {
			return nil, fmt.Errorf("db: migration %q is not named <version>_<name>.sql", name)
		}
		version, err := strconv.Atoi(numPart)
		if err != nil {
			return nil, fmt.Errorf("db: migration %q has a non-numeric version: %w", name, err)
		}

		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, fmt.Errorf("db: read migration %q: %w", name, err)
		}
		sum := sha256.Sum256(body)

		out = append(out, Migration{
			Version:  version,
			Name:     rest,
			SQL:      string(body),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })

	for i, m := range out {
		if m.Version != i+1 {
			return nil, fmt.Errorf("db: migration versions must be contiguous from 1; found %d at position %d",
				m.Version, i+1)
		}
	}
	return out, nil
}

// Migrate applies every pending migration inside a transaction each, and
// records what it applied.
//
// Each migration runs in its own transaction so a failure leaves the database
// at the last good version rather than half-way through the batch. SQLite
// supports transactional DDL, so a failed migration rolls back cleanly.
func (d *DB) Migrate(ctx context.Context) (applied []int, err error) {
	return d.migrateTo(ctx, math.MaxInt)
}

// migrateTo applies pending migrations up to and including version upTo.
//
// Unexported, and not a knob: an instance always runs every migration. It
// exists so a test can build the database as it stood BEFORE a migration, put
// rows in it, and then prove what the migration does to them — which is the
// part of a migration that runs once, on somebody's real data, and cannot be
// retried.
func (d *DB) migrateTo(ctx context.Context, upTo int) (applied []int, err error) {
	if err := d.ensureMigrationTable(ctx); err != nil {
		return nil, err
	}

	migrations, err := LoadMigrations()
	if err != nil {
		return nil, err
	}

	done, err := d.appliedMigrations(ctx)
	if err != nil {
		return nil, err
	}

	for _, m := range migrations {
		if m.Version > upTo {
			break
		}
		if prev, ok := done[m.Version]; ok {
			if prev != m.Checksum {
				return applied, fmt.Errorf(
					"db: migration %d (%s) was modified after it was applied "+
						"(recorded checksum %s, current %s); create a new migration instead of editing an applied one",
					m.Version, m.Name, prev[:12], m.Checksum[:12])
			}
			continue
		}

		if err := d.apply(ctx, m); err != nil {
			return applied, err
		}
		applied = append(applied, m.Version)
	}
	return applied, nil
}

// danglingReferences is what PRAGMA foreign_key_check finds, in words.
func danglingReferences(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var table, parent string
		var rowid, fkid any
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s row %v references a missing %s", table, rowid, parent))
	}
	return out, rows.Err()
}

// ForeignKeysOffDirective, as a migration's first line, runs it with foreign
// keys off (ADR-0044).
//
// It is what rebuilding a table other tables reference needs: SQLite cannot
// alter a CHECK constraint, and dropping the old table with foreign keys on
// would cascade-delete every referencing row. The directive is SQLite's own
// rebuild procedure, checked rather than trusted: the migration runs on one
// connection, and it is rolled back unless PRAGMA foreign_key_check finds no
// reference left dangling.
const ForeignKeysOffDirective = "-- cms: foreign_keys=off"

// apply runs one migration and records it, in one transaction.
func (d *DB) apply(ctx context.Context, m Migration) error {
	record := func(tx Execer) error {
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			return fmt.Errorf("db: migration %d (%s): %w", m.Version, m.Name, err)
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migration (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
			m.Version, m.Name, m.Checksum, time.Now().UTC().Format(time.RFC3339Nano))
		return err
	}
	if !strings.HasPrefix(m.SQL, ForeignKeysOffDirective+"\n") {
		return d.InTx(ctx, record)
	}

	conn, err := d.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	// The pragma is per connection and cannot change inside a transaction, so
	// it is set on this connection before the transaction and put back after,
	// whatever happens: a pooled connection left with foreign keys off would
	// disable them for whatever used it next.
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), `PRAGMA foreign_keys = ON`) }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := record(tx); err != nil {
		return err
	}
	dangling, err := danglingReferences(ctx, tx)
	if err != nil {
		return err
	}
	if len(dangling) > 0 {
		return fmt.Errorf("db: migration %d (%s) would leave %d dangling reference(s), and was rolled back: %s",
			m.Version, m.Name, len(dangling), strings.Join(dangling[:min(len(dangling), 5)], "; "))
	}
	return tx.Commit()
}

func (d *DB) ensureMigrationTable(ctx context.Context) error {
	const stmt = `
CREATE TABLE IF NOT EXISTS schema_migration (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    checksum   TEXT NOT NULL,
    applied_at TEXT NOT NULL
)`
	if _, err := d.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("db: create schema_migration: %w", err)
	}
	return nil
}

func (d *DB) appliedMigrations(ctx context.Context) (map[int]string, error) {
	rows, err := d.QueryContext(ctx, `SELECT version, checksum FROM schema_migration`)
	if err != nil {
		return nil, fmt.Errorf("db: read schema_migration: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := map[int]string{}
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			return nil, err
		}
		out[v] = sum
	}
	return out, rows.Err()
}

// SchemaVersion returns the highest applied migration version, or 0.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	if err := d.ensureMigrationTable(ctx); err != nil {
		return 0, err
	}
	var v *int
	if err := d.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migration`).Scan(&v); err != nil {
		return 0, fmt.Errorf("db: read schema version: %w", err)
	}
	if v == nil {
		return 0, nil
	}
	return *v, nil
}

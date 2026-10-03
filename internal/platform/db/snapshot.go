package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Errors from CheckMigrations. Each is a different answer to "was this
// database written by this build", and each wants a different thing from the
// operator.
var (
	// ErrNotOurs is a database that records no migrations at all.
	ErrNotOurs = errors.New("db: this is not a CMediaStack database: it records no migrations")
	// ErrUnknownMigration is a database that records a migration this build
	// does not have: a newer build wrote it.
	ErrUnknownMigration = errors.New("db: the database records a migration this build does not have")
	// ErrMigrationChanged is a recorded migration whose checksum is not this
	// build's for the same number: a different build wrote it.
	ErrMigrationChanged = errors.New("db: a recorded migration is not this build's migration of that number")
	// ErrMigrationGap is a database whose recorded migrations are not 1 to N
	// without a gap, which the migrator never produces.
	ErrMigrationGap = errors.New("db: the recorded migrations have a gap")
	// ErrMigrationMissing is a database that lacks a migration this build has,
	// where it was required to have them all.
	ErrMigrationMissing = errors.New("db: the database lacks a migration this build has")
)

// Schema is how a database's recorded migrations compare with this build's.
type Schema struct {
	// Version is the highest migration the database records.
	Version int
	// Latest is the highest migration this build has. A database behind it is
	// brought up to date the next time the server opens it.
	Latest int
}

// CheckMigrations compares the migrations a database records with this build's,
// number by number and checksum by checksum.
//
// Recording a migration this build does not have, or one whose checksum
// differs, is always refused: those are databases this build would misread. A
// database BEHIND this build is refused only when complete is set — a backup
// being taken must be of the fully migrated database, while an older backup
// being restored is legitimately behind and is migrated when the server next
// starts on it.
//
// It reads schema_migration and creates nothing, so it works on a snapshot
// opened with OpenSnapshot.
func (d *DB) CheckMigrations(ctx context.Context, complete bool) (Schema, error) {
	ours, err := LoadMigrations()
	if err != nil {
		return Schema{}, err
	}
	s := Schema{Latest: len(ours)} // LoadMigrations guarantees 1..N contiguous

	var present int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'schema_migration'`).
		Scan(&present); err != nil {
		return s, fmt.Errorf("db: reading the schema: %w", err)
	}
	if present == 0 {
		return s, ErrNotOurs
	}
	recorded, err := d.appliedMigrations(ctx)
	if err != nil {
		return s, err
	}
	if len(recorded) == 0 {
		return s, ErrNotOurs
	}

	versions := make([]int, 0, len(recorded))
	for v := range recorded {
		versions = append(versions, v)
	}
	sort.Ints(versions)

	for _, v := range versions {
		if v < 1 || v > len(ours) {
			return s, fmt.Errorf("%w: it records migration %d, and this build has 1 to %d — "+
				"a newer version of CMediaStack wrote it", ErrUnknownMigration, v, len(ours))
		}
		m := ours[v-1]
		if recorded[v] != m.Checksum {
			return s, fmt.Errorf("%w: migration %d (%s)", ErrMigrationChanged, v, m.Name)
		}
		s.Version = v
	}
	if len(versions) != s.Version {
		return s, fmt.Errorf("%w: it records %d migrations and the highest is %d",
			ErrMigrationGap, len(versions), s.Version)
	}
	if complete && s.Version < s.Latest {
		return s, fmt.Errorf("%w: it is at %d of %d", ErrMigrationMissing, s.Version, s.Latest)
	}
	return s, nil
}

// Census is what a database holds, counted: enough to recognise a backup, and
// to notice one that is empty when it should not be.
type Census struct {
	Accounts    int64
	Items       int64
	Files       int64
	Requests    int64
	AuditEvents int64
	// LastActivity is when the newest audit record was written — the nearest
	// thing a database has to "when was this taken". Zero when there is none.
	LastActivity time.Time
}

// TakeCensus counts what the database holds. A table an older schema does not
// have yet counts as empty rather than failing: an older backup is still a
// backup.
func (d *DB) TakeCensus(ctx context.Context) (Census, error) {
	// Unscoped: a backup's census counts every row in the database.
	var c Census
	// Whole statements rather than a table name spliced into one: a placeholder
	// cannot stand for a table, and nothing here should look as though it
	// could take one from outside.
	counts := []struct {
		table, query string
		into         *int64
	}{
		{"app_user", `SELECT COUNT(*) FROM app_user`, &c.Accounts},
		{"media_item", `SELECT COUNT(*) FROM media_item`, &c.Items},
		{"media_file", `SELECT COUNT(*) FROM media_file`, &c.Files},
		{"media_request", `SELECT COUNT(*) FROM media_request`, &c.Requests},
		{"audit_event", `SELECT COUNT(*) FROM audit_event`, &c.AuditEvents},
	}
	for _, n := range counts {
		var present int
		if err := d.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, n.table).
			Scan(&present); err != nil {
			return c, fmt.Errorf("db: census of %s: %w", n.table, err)
		}
		if present == 0 {
			continue
		}
		if err := d.QueryRowContext(ctx, n.query).Scan(n.into); err != nil {
			return c, fmt.Errorf("db: census of %s: %w", n.table, err)
		}
	}
	if c.AuditEvents > 0 {
		var last sql.NullString
		if err := d.QueryRowContext(ctx, `SELECT MAX(occurred_at) FROM audit_event`).
			Scan(&last); err != nil {
			return c, fmt.Errorf("db: census of audit_event: %w", err)
		}
		if last.Valid {
			if t, err := time.Parse(time.RFC3339Nano, last.String); err == nil {
				c.LastActivity = t
			}
		}
	}
	return c, nil
}

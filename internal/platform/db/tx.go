package db

import (
	"context"
	"database/sql"
	"fmt"
)

// Execer is the subset of *sql.Tx used by migrations and repositories,
// satisfied by *DB, *sql.DB and *sql.Tx. Repository methods take an Execer so
// the same method works inside or outside a transaction.
//
// It is exported because InTx hands one to its callback, and those callbacks
// live in other packages.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// InTx runs fn inside a transaction, rolling back on error or on panic.
//
// The rollback-on-panic matters: a panic mid-transaction that left the tx open
// would hold the WAL write lock until the connection was reaped, stalling
// every writer behind it.
func (d *DB) InTx(ctx context.Context, fn func(Execer) error) (err error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	return nil
}

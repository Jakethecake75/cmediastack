package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/platform/backup"
	"github.com/jakethecake75/cmediastack/internal/platform/config"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

// The backup commands (ADR-0029). Like -recover, they live on the host rather
// than behind a route: they need the master key, which only the host has, and
// a restore that could be driven over HTTP would let a stolen administrator
// session roll the whole instance back.

// backupPassphrase derives the passphrase from the configured master key.
func backupPassphrase(cfg config.Config) (string, error) {
	cipher, err := secrets.NewCipherFromBase64(os.Getenv(cfg.Secrets.MasterKeyEnv))
	if err != nil {
		return "", fmt.Errorf("secrets: %w", err)
	}
	return cipher.BackupPassphrase(), nil
}

// explain adds what an operator can do about an error that has a remedy.
func explain(cfg config.Config, err error) error {
	switch {
	case errors.Is(err, backup.ErrWrongKey):
		return fmt.Errorf("%w. It decrypts only with the master key of the instance that took it: "+
			"check that %s holds that key, and not a newer one", err, cfg.Secrets.MasterKeyEnv)
	case errors.Is(err, backup.ErrNotABackup):
		return fmt.Errorf("%w: backups are the cmediastack-*.db.age files in the backup directory", err)
	case errors.Is(err, db.ErrUnknownMigration):
		return fmt.Errorf("%w. Restore it with that version of CMediaStack or a later one", err)
	}
	return err
}

// runVerifyBackup is -verify-backup FILE: decrypt into a private temporary
// directory, check, report, and remove the plaintext.
func runVerifyBackup(cfg config.Config, file string, out io.Writer) error {
	pass, err := backupPassphrase(cfg)
	if err != nil {
		return err
	}
	rep, err := backup.Verify(context.Background(), file, pass)
	if err != nil {
		return explain(cfg, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s: good.\n", file)
	describe(&b, rep)
	b.WriteString("\nIt was decrypted into a private temporary directory to be checked, and that\n" +
		"copy has been removed. Nothing was changed.\n")
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("the backup is good, but this report could not be written: %w", err)
	}
	return nil
}

// runRestoreBackup is -restore-backup FILE -restore-to PATH: decrypt into a
// new database file, check it, fence it, and say how to put it in place. The
// running server, and its database, are not touched.
func runRestoreBackup(cfg config.Config, file, to string, out io.Writer) error {
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("-restore-backup needs -restore-to PATH: a new file for the restored " +
			"database. Nothing is ever restored over an existing one")
	}
	pass, err := backupPassphrase(cfg)
	if err != nil {
		return err
	}
	rep, err := backup.Restore(context.Background(), file, to, pass, time.Now)
	if err != nil {
		return explain(cfg, err)
	}

	live := cfg.Database.Path
	var b strings.Builder
	fmt.Fprintf(&b, "Restored %s to %s: decrypted, checked, and fenced.\n", filepath.Base(file), to)
	describe(&b, rep)
	fmt.Fprintf(&b, "  fenced:        %d session(s) ended, so everyone signs in again; %d API token(s)\n"+
		"                 revoked, so any revoked since the backup is not live again —\n"+
		"                 their owners issue new ones\n", rep.Fenced.SessionsEnded, rep.Fenced.TokensRevoked)
	fmt.Fprintf(&b, "\nNothing else has changed. To put it in place:\n"+
		"  1. Stop the server.\n"+
		"  2. Move the current database aside: %s, and %s-wal and %s-shm if they are there.\n"+
		"     A -wal file left beside the restored database is replayed into it when it is\n"+
		"     opened, putting back part of what the restore was meant to undo.\n"+
		"  3. Move %s to %s.\n"+
		"  4. Start the server.\n", live, live, live, to, live)
	b.WriteString("\nEverything recorded after the backup was taken is gone, the audit log included;\n" +
		"the restored audit log says so in its newest line. Accounts suspended and passwords\n" +
		"changed since then are as they were in the backup: review the accounts after starting.\n")
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("the database was restored to %s, but this report could not be written: %w", to, err)
	}
	return nil
}

// describe writes what a backup is and holds.
func describe(b *strings.Builder, rep backup.Report) {
	if rep.TakenAt.IsZero() {
		b.WriteString("  taken:         not known — the file has been renamed\n")
	} else {
		fmt.Fprintf(b, "  taken:         %s\n", rep.TakenAt.Format("2006-01-02 15:04:05 UTC"))
	}
	fmt.Fprintf(b, "  size:          %d bytes\n", rep.Size)
	fmt.Fprintf(b, "  sha256:        %s\n"+
		"                 (the audit log's system.backup.created record carries the same)\n", rep.SHA256)
	b.WriteString("  decrypts:      with this instance's master key, every chunk authenticated\n")
	b.WriteString("  checks:        SQLite's integrity check and foreign-key check pass\n")
	s := rep.Contents.Schema
	if s.Version == s.Latest {
		fmt.Fprintf(b, "  schema:        version %d, this build's, checksum for checksum\n", s.Version)
	} else {
		fmt.Fprintf(b, "  schema:        version %d of this build's %d: an older backup, brought up to\n"+
			"                 date by the server when it next starts on it\n", s.Version, s.Latest)
	}
	c := rep.Contents.Census
	fmt.Fprintf(b, "  holds:         %d account(s), %d library item(s), %d file(s), %d request(s),\n"+
		"                 %d audit record(s)\n", c.Accounts, c.Items, c.Files, c.Requests, c.AuditEvents)
	if !c.LastActivity.IsZero() {
		fmt.Fprintf(b, "  last activity: %s\n", c.LastActivity.UTC().Format("2006-01-02 15:04:05 UTC"))
	}
}

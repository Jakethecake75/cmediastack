package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/egressproxy"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/notify"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/config"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
	"github.com/jakethecake75/cmediastack/internal/subtitles"
)

// Rotating the master key (ADR-0054): on the host, with the server stopped,
// every sealed value re-sealed in one transaction or none.

// newKeySuffix is added to the master key's variable name to name the new key.
const newKeySuffix = "_NEW"

// sealedRow is one stored secret: where it lives, what it is sealed under,
// and how to write it back.
type sealedRow struct {
	kind    string // what it is, for the count and for an error
	id      string // which one
	context string
	sealed  []byte
	write   func(ctx context.Context, tx db.Execer, sealed []byte) error
}

// rotatedKinds are the stored secrets, in the order they are counted. A test
// holds every call that seals with the cipher to this list, or to the two
// short-lived ones that expire instead (ADR-0054, decision 3).
var rotatedKinds = []string{"authenticator secret", "indexer API key", "metadata provider token",
	"Discord webhook", "OpenSubtitles API key", "OpenSubtitles password", "Cardigann indexer settings", "SOCKS5 proxy password"}

// sealedRows reads every stored secret.
func sealedRows(ctx context.Context, tx db.Execer) ([]sealedRow, error) {
	var out []sealedRow
	read := func(kind, query string, contextOf func(int64) string, update string) error {
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id int64
			var sealed []byte
			if err := rows.Scan(&id, &sealed); err != nil {
				return err
			}
			out = append(out, sealedRow{kind: kind, id: fmt.Sprint(id), context: contextOf(id), sealed: sealed,
				write: func(ctx context.Context, tx db.Execer, s []byte) error {
					_, err := tx.ExecContext(ctx, update, s, id)
					return err
				}})
		}
		return rows.Err()
	}
	if err := read(rotatedKinds[0], `SELECT id, totp_secret_enc FROM app_user WHERE totp_secret_enc IS NOT NULL`,
		identity.TOTPSealContext, `UPDATE app_user SET totp_secret_enc = ? WHERE id = ?`); err != nil {
		return nil, err
	}
	if err := read(rotatedKinds[1], `SELECT id, api_key_enc FROM indexer WHERE api_key_enc IS NOT NULL`,
		indexer.KeyContext, `UPDATE indexer SET api_key_enc = ? WHERE id = ?`); err != nil {
		return nil, err
	}
	if err := read(rotatedKinds[6], `SELECT id, settings_enc FROM indexer WHERE settings_enc IS NOT NULL`,
		indexer.SettingsContext, `UPDATE indexer SET settings_enc = ? WHERE id = ?`); err != nil {
		return nil, err
	}
	// The settings hold base64; empty is unset.
	for _, s := range []struct{ kind, key, context string }{
		{rotatedKinds[2], metadata.SealedSetting, metadata.SealedContext},
		{rotatedKinds[3], notify.SealedSetting, notify.SealedContext},
		{rotatedKinds[4], subtitles.SealedKey, subtitles.SealedKeyContext},
		{rotatedKinds[5], subtitles.SealedPassword, subtitles.SealedPasswordContext},
		{rotatedKinds[7], egressproxy.SealedSetting, egressproxy.SealedContext},
	} {
		var value string
		err := tx.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, s.key).Scan(&value)
		if err != nil || strings.TrimSpace(value) == "" {
			continue //nolint:nilerr // no row, or an empty one, is a secret never stored
		}
		sealed, derr := base64.StdEncoding.DecodeString(value)
		if derr != nil {
			return nil, fmt.Errorf("the setting %s is not base64: %w", s.key, derr)
		}
		key := s.key
		out = append(out, sealedRow{kind: s.kind, id: key, context: s.context, sealed: sealed,
			write: func(ctx context.Context, tx db.Execer, b []byte) error {
				_, err := tx.ExecContext(ctx, `UPDATE setting SET value = ? WHERE key = ?`,
					base64.StdEncoding.EncodeToString(b), key)
				return err
			}})
	}
	return out, nil
}

// rotate re-seals every stored secret from one key to the other, in one
// transaction: a value that will not open under the old key changes nothing
// and is named. It returns how many of each kind were re-sealed.
func rotate(ctx context.Context, database *db.DB, from, to *secrets.Cipher) (map[string]int, error) {
	counts := map[string]int{}
	err := database.InTx(ctx, func(tx db.Execer) error {
		rows, err := sealedRows(ctx, tx)
		if err != nil {
			return err
		}
		for _, r := range rows {
			plain, err := from.Decrypt(r.sealed, r.context)
			if err != nil {
				return fmt.Errorf("the %s %s does not open under the current key, so nothing was "+
					"changed: is it the key this database was sealed with?", r.kind, r.id)
			}
			sealed, err := to.Encrypt(plain, r.context)
			if err != nil {
				return err
			}
			if err := r.write(ctx, tx, sealed); err != nil {
				return err
			}
			counts[r.kind]++
		}
		return nil
	})
	return counts, err
}

// runRotateKey is -rotate-key.
func runRotateKey(cfg config.Config, running func() bool, out io.Writer) error {
	oldName := cfg.Secrets.MasterKeyEnv
	newName := oldName + newKeySuffix
	from, err := secrets.NewCipherFromBase64(os.Getenv(oldName))
	if err != nil {
		return fmt.Errorf("the current key, %s: %w", oldName, err)
	}
	to, err := secrets.NewCipherFromBase64(os.Getenv(newName))
	if err != nil {
		return fmt.Errorf("the new key, %s: %w (generate one with `make genkey`)", newName, err)
	}
	if strings.TrimSpace(os.Getenv(oldName)) == strings.TrimSpace(os.Getenv(newName)) {
		return errors.New("the new key is the current key; nothing to rotate")
	}
	if running() {
		return errors.New("the server is running: it holds the current key and could not read what this " +
			"re-seals. Stop it, rotate, put the new key where it reads it, and start it")
	}

	database, err := db.Open(db.Options{Path: cfg.Database.Path, BusyTimeout: cfg.Database.BusyTimeout})
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	ctx := context.Background()
	if _, err := database.Migrate(ctx); err != nil {
		return err
	}

	counts, err := rotate(ctx, database, from, to)
	if err != nil {
		return err
	}
	parts := make([]string, 0, len(rotatedKinds))
	total := 0
	for _, k := range rotatedKinds {
		parts = append(parts, fmt.Sprintf("%d %s(s)", counts[k], k))
		total += counts[k]
	}
	summary := "the master key was rotated: " + strings.Join(parts, ", ") + " re-sealed"
	auditErr := audit.New(database, time.Now).Write(ctx, audit.Event{
		ActorLabel: "host", Action: audit.ActionMasterKeyRotated, Outcome: audit.OutcomeSuccess,
		TargetKind: "secrets", Detail: summary,
	})

	var b strings.Builder
	fmt.Fprintf(&b, "Rotated: %d sealed value(s) re-sealed under %s.\n", total, newName)
	for _, k := range rotatedKinds {
		fmt.Fprintf(&b, "  %-26s %d\n", k+"s:", counts[k])
	}
	fmt.Fprintf(&b, "\nNow:\n"+
		"  1. Put the new key where the server reads it: %s=<the value of %s>.\n"+
		"  2. Start the server.\n"+
		"  3. KEEP THE OLD KEY with your existing backups. They are encrypted under it,\n"+
		"     and -verify-backup and -restore-backup read them with whichever key is in\n"+
		"     %s. The next backup is written under the new one.\n"+
		"Grab tickets and signup challenges issued before now are refused; search or\n"+
		"fetch a new one.\n", oldName, newName, oldName)
	if auditErr != nil {
		fmt.Fprintf(&b, "\nTHE ROTATION WAS NOT WRITTEN TO THE AUDIT LOG: %v\n", auditErr)
	}
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("the key was rotated, but this summary could not be written: %w", err)
	}
	return auditErr
}

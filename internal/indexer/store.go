package indexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

// ErrNotFound is returned for an indexer that does not exist, and for one the
// caller may not see. The two are deliberately the same.
var ErrNotFound = errors.New("indexer: not found")

// Store persists indexer definitions.
//
// # About the API key
//
// The key is sealed with AES-256-GCM under the context "indexer:<id>:apikey",
// and the context is bound into the AEAD as additional data. That makes the
// sealed value non-relocatable: lifting the blob out of one indexer's row and
// into another's yields a decryption failure rather than a working credential
// pointed at the wrong tracker. It is the same construction the TOTP secrets
// use, for the same reason.
//
// Because the id is part of the context, a new indexer is written in two steps
// inside one transaction: the row is inserted to obtain an id, then the sealed
// key is written against it. Doing it in one statement would mean sealing
// against an id that does not exist yet.
type Store struct {
	db     *db.DB
	cipher *secrets.Cipher
	now    func() time.Time
}

// NewStore builds the store.
func NewStore(database *db.DB, cipher *secrets.Cipher, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: database, cipher: cipher, now: now}
}

// KeyContext is what an indexer's API key is sealed under, for the key
// rotation (ADR-0054) as for the store.
func KeyContext(id int64) string {
	return fmt.Sprintf("indexer:%d:apikey", id)
}

// SettingsContext binds an indexer's sealed Cardigann settings to it
// (ADR-0059).
func SettingsContext(id int64) string {
	return fmt.Sprintf("indexer:%d:settings", id)
}

// sealSettings writes an indexer's settings, sealed; nothing to write keeps
// what is stored.
func (s *Store) sealSettings(ctx context.Context, tx db.Execer, id int64, settings map[string]string) error {
	if len(settings) == 0 {
		return nil
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	sealed, err := s.cipher.Encrypt(raw, SettingsContext(id))
	if err != nil {
		return fmt.Errorf("indexer: sealing the settings: %w", err)
	}
	_, err = tx.ExecContext(ctx, `UPDATE indexer SET settings_enc = ? WHERE id = ?`, sealed, id)
	return err
}

// Create stores a new indexer and returns its id.
//
// Authorization is checked here, at the data layer, rather than only on the
// route. A route check protects one path; this protects the operation however
// it is reached.
func (s *Store) Create(ctx context.Context, d Definition) (int64, error) {
	if err := authz.RequirePermission(ctx, authz.PermManageIndexers); err != nil {
		return 0, err
	}
	if err := d.Validate(); err != nil {
		return 0, err
	}
	if d.Kind == KindCardigann && d.Cardigann == "" {
		return 0, errors.New("indexer: a Cardigann indexer needs its definition")
	}

	var id int64
	err := s.db.InTx(ctx, func(tx db.Execer) error {
		now := s.now().UTC().Format(time.RFC3339Nano)
		res, err := tx.ExecContext(ctx, `
			INSERT INTO indexer (name, kind, base_url, categories, enabled, priority,
			                     seed_ratio, seed_time_secs, created_at, updated_at, definition)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			strings.TrimSpace(d.Name), string(d.Kind), strings.TrimSpace(d.BaseURL),
			joinInts(d.Categories), boolToInt(d.Enabled), d.Priority,
			d.SeedRatio, int64(d.SeedTime.Seconds()), now, now, definitionOf(d))
		if err != nil {
			return fmt.Errorf("indexer: create: %w", err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}

		if err := s.sealSettings(ctx, tx, id, d.Settings); err != nil {
			return err
		}
		// The key is sealed only now, because the context contains the id.
		if d.APIKey != "" {
			sealed, err := s.cipher.EncryptString(d.APIKey, KeyContext(id))
			if err != nil {
				return fmt.Errorf("indexer: sealing the api key: %w", err)
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE indexer SET api_key_enc = ? WHERE id = ?`, sealed, id); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

// Update changes an indexer.
//
// An empty APIKey means "leave the stored key alone", which is what an admin
// form submits when the operator did not retype a secret they cannot see. The
// alternative — treating empty as "clear it" — silently breaks every indexer
// the moment somebody edits its name.
func (s *Store) Update(ctx context.Context, d Definition) error {
	if err := authz.RequirePermission(ctx, authz.PermManageIndexers); err != nil {
		return err
	}
	if err := d.Validate(); err != nil {
		return err
	}

	return s.db.InTx(ctx, func(tx db.Execer) error {
		// An empty definition keeps the stored one, as an empty key does; a
		// kind other than Cardigann has none.
		res, err := tx.ExecContext(ctx, `
			UPDATE indexer SET name = ?, kind = ?, base_url = ?, categories = ?,
			                   enabled = ?, priority = ?, seed_ratio = ?,
			                   seed_time_secs = ?, updated_at = ?,
			                   definition = CASE WHEN ? <> 'cardigann' THEN NULL
			                                     ELSE COALESCE(?, definition) END,
			                   settings_enc = CASE WHEN ? <> 'cardigann' THEN NULL ELSE settings_enc END
			WHERE id = ?`,
			strings.TrimSpace(d.Name), string(d.Kind), strings.TrimSpace(d.BaseURL),
			joinInts(d.Categories), boolToInt(d.Enabled), d.Priority,
			d.SeedRatio, int64(d.SeedTime.Seconds()),
			s.now().UTC().Format(time.RFC3339Nano), string(d.Kind), definitionOf(d), string(d.Kind), d.ID)
		if err != nil {
			return fmt.Errorf("indexer: update: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return ErrNotFound
		}
		if d.Kind == KindCardigann {
			var stored sql.NullString
			if err := tx.QueryRowContext(ctx, `SELECT definition FROM indexer WHERE id = ?`, d.ID).
				Scan(&stored); err != nil {
				return err
			}
			if stored.String == "" {
				return errors.New("indexer: a Cardigann indexer needs its definition")
			}
			// Settings given beside the stored definition are checked
			// against it, as against a pasted one.
			if len(d.Settings) > 0 {
				def, err := parseCardigann(stored.String)
				if err != nil {
					return err
				}
				if err := checkSettings(def, d.Settings); err != nil {
					return err
				}
			}
			if err := s.sealSettings(ctx, tx, d.ID, d.Settings); err != nil {
				return err
			}
		}

		if d.APIKey == "" {
			return nil
		}
		sealed, err := s.cipher.EncryptString(d.APIKey, KeyContext(d.ID))
		if err != nil {
			return fmt.Errorf("indexer: sealing the api key: %w", err)
		}
		_, err = tx.ExecContext(ctx, `UPDATE indexer SET api_key_enc = ? WHERE id = ?`, sealed, d.ID)
		return err
	})
}

// Delete removes an indexer.
func (s *Store) Delete(ctx context.Context, id int64) error {
	if err := authz.RequirePermission(ctx, authz.PermManageIndexers); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM indexer WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("indexer: delete: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns every indexer WITHOUT its API key.
//
// This is what the admin surface calls. The key is not merely omitted from the
// response — it is never decrypted, so there is no window in which it exists in
// a value bound for JSON encoding.
func (s *Store) List(ctx context.Context) ([]Definition, error) {
	if err := authz.RequirePermission(ctx, authz.PermManageIndexers); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, kind, base_url, categories, enabled, priority,
		       seed_ratio, seed_time_secs, COALESCE(definition, ''), settings_enc IS NOT NULL
		FROM indexer ORDER BY priority ASC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Definition
	for rows.Next() {
		var d Definition
		var kind, cats string
		var enabled int
		var seedSecs int64
		if err := rows.Scan(&d.ID, &d.Name, &kind, &d.BaseURL, &cats,
			&enabled, &d.Priority, &d.SeedRatio, &seedSecs, &d.Cardigann, &d.HasSettings); err != nil {
			return nil, err
		}
		d.Kind = Kind(kind)
		d.Categories = splitInts(cats)
		d.Enabled = enabled != 0
		d.SeedTime = time.Duration(seedSecs) * time.Second
		out = append(out, d)
	}
	return out, rows.Err()
}

// Health is an indexer's current state, for the admin surface.
type Health struct {
	IndexerID           int64
	LastCheckedAt       *time.Time
	LastError           string
	ConsecutiveFailures int
}

// HealthOf returns the recorded health of every indexer.
func (s *Store) HealthOf(ctx context.Context) (map[int64]Health, error) {
	if err := authz.RequirePermission(ctx, authz.PermManageIndexers); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, last_checked_at, last_error, consecutive_failures FROM indexer`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[int64]Health{}
	for rows.Next() {
		var h Health
		var checked, lastErr sql.NullString
		if err := rows.Scan(&h.IndexerID, &checked, &lastErr, &h.ConsecutiveFailures); err != nil {
			return nil, err
		}
		if checked.Valid {
			if t, err := time.Parse(time.RFC3339Nano, checked.String); err == nil {
				h.LastCheckedAt = &t
			}
		}
		h.LastError = lastErr.String
		out[h.IndexerID] = h
	}
	return out, rows.Err()
}

// RecordResult updates an indexer's health after a search.
//
// It takes no permission check on purpose: it is called by the background sync
// task, which has no user principal, and it writes nothing a caller controls
// beyond an error string that is truncated here.
func (s *Store) RecordResult(ctx context.Context, id int64, searchErr error) error {
	now := s.now().UTC().Format(time.RFC3339Nano)
	if searchErr == nil {
		_, err := s.db.ExecContext(ctx, `
			UPDATE indexer SET last_checked_at = ?, last_error = NULL,
			                   consecutive_failures = 0
			WHERE id = ?`, now, id)
		return err
	}

	// The message can contain indexer-controlled text, so it is bounded before
	// it reaches a column and a UI.
	msg := searchErr.Error()
	if len(msg) > 512 {
		msg = msg[:512]
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE indexer SET last_checked_at = ?, last_error = ?,
		                   consecutive_failures = consecutive_failures + 1
		WHERE id = ?`, now, msg, id)
	return err
}

// Enabled returns the indexers a search should use, WITH their API keys
// decrypted.
//
// This is the only method that unseals a key, and it is not reachable from the
// admin surface: the admin surface calls List. Keeping the two apart means the
// question "can this endpoint return an API key?" is answered by which method
// it calls, not by remembering to strip a field.
func (s *Store) Enabled(ctx context.Context) ([]Definition, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, kind, base_url, api_key_enc, categories, priority,
		       seed_ratio, seed_time_secs, COALESCE(definition, ''), settings_enc
		FROM indexer WHERE enabled = 1 ORDER BY priority ASC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Definition
	for rows.Next() {
		var d Definition
		var kind, cats string
		var sealed, sealedSettings []byte
		var seedSecs int64
		if err := rows.Scan(&d.ID, &d.Name, &kind, &d.BaseURL, &sealed, &cats,
			&d.Priority, &d.SeedRatio, &seedSecs, &d.Cardigann, &sealedSettings); err != nil {
			return nil, err
		}
		d.Kind = Kind(kind)
		d.Categories = splitInts(cats)
		d.Enabled = true
		d.SeedTime = time.Duration(seedSecs) * time.Second

		if len(sealed) > 0 {
			key, err := s.cipher.DecryptString(sealed, KeyContext(d.ID))
			if err != nil {
				// A key that will not unseal means the master key changed or
				// the row was tampered with. Skipping the indexer is right:
				// querying it without credentials would fail anyway, and doing
				// so repeatedly looks like an attack from the far end.
				return nil, fmt.Errorf("indexer %q: api key will not decrypt "+
					"(master key changed, or the row was altered): %w", d.Name, err)
			}
			d.APIKey = key
		}
		if len(sealedSettings) > 0 {
			raw, err := s.cipher.Decrypt(sealedSettings, SettingsContext(d.ID))
			if err != nil {
				return nil, fmt.Errorf("indexer %q: its settings will not decrypt "+
					"(master key changed, or the row was altered): %w", d.Name, err)
			}
			if err := json.Unmarshal(raw, &d.Settings); err != nil {
				return nil, fmt.Errorf("indexer %q: its settings: %w", d.Name, err)
			}
			d.HasSettings = true
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------

// definitionOf is the definition column's value: NULL for another kind, or
// for an empty one, which an update reads as "keep the stored one".
func definitionOf(d Definition) any {
	if d.Kind != KindCardigann || d.Cardigann == "" {
		return nil
	}
	return d.Cardigann
}

func joinInts(in []int) string {
	if len(in) == 0 {
		return ""
	}
	parts := make([]string, len(in))
	for i, n := range in {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ",")
}

func splitInts(s string) []int {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []int
	for _, part := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

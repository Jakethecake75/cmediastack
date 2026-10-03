package release

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// ErrProfileNotFound is returned for a profile that does not exist.
var ErrProfileNotFound = errors.New("release: quality profile not found")

// ErrDefaultProfile refuses deleting the default profile (ADR-0027, decision 4).
var ErrDefaultProfile = errors.New("release: that is the default profile; choose another default first")

// DefaultProfileName is the built-in profile a new instance judges searches by
// (ADR-0027): the one DefaultProfiles already calls the default default.
const DefaultProfileName = "HD-1080p"

// ProfileStore persists quality profiles.
//
// The allowed list and the cutoff are stored as names, not as foreign keys into
// a quality table. The ladder is defined in code, and a database that disagreed
// with the code would be the worse authority — a row naming a quality the code
// has never heard of is a profile that silently rejects everything. Compile()
// validates the names on the way in, so a stored row can only hold names the
// code recognises at the time it was saved.
type ProfileStore struct {
	db  *db.DB
	now func() time.Time
}

// NewProfileStore builds the store.
func NewProfileStore(database *db.DB, now func() time.Time) *ProfileStore {
	if now == nil {
		now = time.Now
	}
	return &ProfileStore{db: database, now: now}
}

// StoredProfile is a profile with its database identity.
type StoredProfile struct {
	ID      int64
	Builtin bool
	// Default marks the profile that judges an interactive search when none
	// is chosen (ADR-0027). At most one is.
	Default bool
	Profile Profile
}

// EnsureDefaults seeds the built-in profiles if they are absent.
//
// It is additive and idempotent: a profile the operator has edited is left
// alone, and one they deleted stays deleted. Re-seeding on every boot would
// quietly undo their decisions, which is the behaviour people find infuriating
// and cannot explain.
func (s *ProfileStore) EnsureDefaults(ctx context.Context) error {
	return s.db.InTx(ctx, func(tx db.Execer) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM quality_profile`).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return nil
		}

		now := s.now().UTC().Format(time.RFC3339Nano)
		for _, p := range DefaultProfiles() {
			if err := p.Compile(); err != nil {
				// A shipped profile that does not compile is a bug in this
				// build, not a configuration problem.
				return fmt.Errorf("release: built-in profile %q is invalid: %w", p.Name, err)
			}
			allowed, _ := json.Marshal(p.Allowed)
			preferred, _ := json.Marshal(p.Preferred)
			required, _ := json.Marshal(p.Required)
			forbidden, _ := json.Marshal(p.Forbidden)

			// The default is marked here, when the profiles are first made,
			// and never again: seeding is once, so an operator who later
			// chose another default — or none — keeps that choice.
			isDefault := 0
			if p.Name == DefaultProfileName {
				isDefault = 1
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO quality_profile
					(name, allowed, cutoff, preferred, required, forbidden, builtin,
					 is_default, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`,
				p.Name, string(allowed), p.Cutoff, string(preferred),
				string(required), string(forbidden), isDefault, now, now); err != nil {
				return fmt.Errorf("release: seeding profile %q: %w", p.Name, err)
			}
		}
		return nil
	})
}

// scoredTermJSON is the serialised form. ScoredTerm's compiled pattern is
// unexported and must not be marshalled, so the shape is declared explicitly
// rather than relying on the struct's field tags.
type scoredTermJSON struct {
	Term  string `json:"term"`
	Score int    `json:"score"`
}

// List returns every profile, compiled and ready to use.
//
// A row that will not compile is returned as an error rather than skipped: a
// profile that silently vanishes is a queue that silently stops, and the
// operator has no way to see why.
func (s *ProfileStore) List(ctx context.Context) ([]StoredProfile, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, allowed, cutoff, preferred, required, forbidden, builtin, is_default
		FROM quality_profile ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []StoredProfile
	for rows.Next() {
		sp, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// Get returns one profile, compiled.
func (s *ProfileStore) Get(ctx context.Context, id int64) (StoredProfile, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, allowed, cutoff, preferred, required, forbidden, builtin, is_default
		FROM quality_profile WHERE id = ?`, id)

	sp, err := scanProfile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredProfile{}, ErrProfileNotFound
	}
	return sp, err
}

// Default returns the profile an interactive search is judged by when none is
// chosen, and false when the instance has none (ADR-0027).
//
// No permission check, as for List and Get: a profile is policy rather than a
// secret, and every search reads it.
func (s *ProfileStore) Default(ctx context.Context) (StoredProfile, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, allowed, cutoff, preferred, required, forbidden, builtin, is_default
		FROM quality_profile WHERE is_default = 1`)
	sp, err := scanProfile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredProfile{}, false, nil
	}
	if err != nil {
		return StoredProfile{}, false, err
	}
	return sp, true, nil
}

// SetDefault makes a profile the default, or — with id 0 — leaves the instance
// with none. It returns what the default was before, for the audit line.
//
// One transaction: the old flag is cleared and the new one set together, so
// there is never a moment with two defaults (the partial unique index would
// refuse it anyway) or, when a new one was asked for, with none.
func (s *ProfileStore) SetDefault(ctx context.Context, id int64) (previous string, err error) {
	if err := authz.RequirePermission(ctx, authz.PermSystemSettings); err != nil {
		return "", err
	}
	if id < 0 {
		return "", fmt.Errorf("%w: %d", ErrProfileNotFound, id)
	}
	err = s.db.InTx(ctx, func(tx db.Execer) error {
		var was sql.NullString
		if qerr := tx.QueryRowContext(ctx,
			`SELECT name FROM quality_profile WHERE is_default = 1`).Scan(&was); qerr != nil &&
			!errors.Is(qerr, sql.ErrNoRows) {
			return qerr
		}
		previous = was.String
		now := s.now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `
			UPDATE quality_profile SET is_default = 0, updated_at = ? WHERE is_default = 1`, now); err != nil {
			return err
		}
		if id == 0 {
			return nil
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE quality_profile SET is_default = 1, updated_at = ? WHERE id = ?`, now, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return ErrProfileNotFound
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("release: setting the default profile: %w", err)
	}
	return previous, nil
}

type scanner interface{ Scan(...any) error }

func scanProfile(row scanner) (StoredProfile, error) {
	var (
		sp                                      StoredProfile
		allowed, preferred, required, forbidden string
		builtin, isDefault                      int
	)
	if err := row.Scan(&sp.ID, &sp.Profile.Name, &allowed, &sp.Profile.Cutoff,
		&preferred, &required, &forbidden, &builtin, &isDefault); err != nil {
		return StoredProfile{}, err
	}
	sp.Builtin = builtin != 0
	sp.Default = isDefault != 0

	if err := json.Unmarshal([]byte(allowed), &sp.Profile.Allowed); err != nil {
		return StoredProfile{}, fmt.Errorf("release: profile %q has an unreadable allowed list: %w",
			sp.Profile.Name, err)
	}
	var terms []scoredTermJSON
	if err := json.Unmarshal([]byte(preferred), &terms); err == nil {
		for _, t := range terms {
			sp.Profile.Preferred = append(sp.Profile.Preferred, ScoredTerm{Term: t.Term, Score: t.Score})
		}
	}
	_ = json.Unmarshal([]byte(required), &sp.Profile.Required)
	_ = json.Unmarshal([]byte(forbidden), &sp.Profile.Forbidden)

	if err := sp.Profile.Compile(); err != nil {
		return StoredProfile{}, fmt.Errorf("release: stored profile %q does not compile: %w",
			sp.Profile.Name, err)
	}
	return sp, nil
}

// Save creates or updates a profile.
//
// It compiles BEFORE writing, so an invalid profile is refused at the moment
// the operator saves it, with a message naming the problem — rather than being
// stored and then rejecting every release at 3am with no explanation.
func (s *ProfileStore) Save(ctx context.Context, sp StoredProfile) (int64, error) {
	if err := authz.RequirePermission(ctx, authz.PermSystemSettings); err != nil {
		return 0, err
	}
	if err := sp.Profile.Compile(); err != nil {
		return 0, err
	}

	allowed, _ := json.Marshal(sp.Profile.Allowed)
	terms := make([]scoredTermJSON, 0, len(sp.Profile.Preferred))
	for _, t := range sp.Profile.Preferred {
		terms = append(terms, scoredTermJSON{Term: t.Term, Score: t.Score})
	}
	preferred, _ := json.Marshal(terms)
	required, _ := json.Marshal(sp.Profile.Required)
	forbidden, _ := json.Marshal(sp.Profile.Forbidden)
	now := s.now().UTC().Format(time.RFC3339Nano)

	if sp.ID == 0 {
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO quality_profile
				(name, allowed, cutoff, preferred, required, forbidden, builtin, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)`,
			sp.Profile.Name, string(allowed), sp.Profile.Cutoff,
			string(preferred), string(required), string(forbidden), now, now)
		if err != nil {
			return 0, fmt.Errorf("release: saving profile: %w", err)
		}
		return res.LastInsertId()
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE quality_profile SET name = ?, allowed = ?, cutoff = ?, preferred = ?,
		                           required = ?, forbidden = ?, updated_at = ?
		WHERE id = ?`,
		sp.Profile.Name, string(allowed), sp.Profile.Cutoff,
		string(preferred), string(required), string(forbidden), now, sp.ID)
	if err != nil {
		return 0, fmt.Errorf("release: saving profile: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return 0, ErrProfileNotFound
	}
	return sp.ID, nil
}

// Delete removes a profile.
//
// Not the default one (ADR-0027, decision 4): deleting it would leave every
// search unjudged without anybody having decided that. The condition is part
// of the statement, so the check and the delete cannot be separated by a
// concurrent change of default.
func (s *ProfileStore) Delete(ctx context.Context, id int64) error {
	if err := authz.RequirePermission(ctx, authz.PermSystemSettings); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM quality_profile WHERE id = ? AND is_default = 0`, id)
	if err != nil {
		return fmt.Errorf("release: deleting profile: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		if sp, gerr := s.Get(ctx, id); gerr == nil && sp.Default {
			return ErrDefaultProfile
		}
		return ErrProfileNotFound
	}
	return nil
}

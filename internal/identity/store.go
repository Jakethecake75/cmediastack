package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

// ErrNotFound is returned when a lookup finds nothing. Callers on
// authentication paths must not surface it to a client: whether an account
// exists is exactly what an enumeration attack wants to learn.
var ErrNotFound = errors.New("identity: not found")

// ErrAccountExists means the username or email is already registered.
//
// It is only ever returned to somebody approving an account request, who can
// already see both the applicant's name and the user list, so reporting it
// discloses nothing. Signup never surfaces it — that would turn the public form
// into an enumeration oracle (§7.2).
var ErrAccountExists = errors.New("identity: that username or email is already registered")

// User is a stored account.
type User struct {
	ID              int64
	Username        string
	Email           string
	PasswordHash    string
	State           authz.UserState
	RoleID          int64
	RoleName        string
	RoleRank        int
	TOTPSecretEnc   []byte
	TOTPEnrolledAt  *time.Time
	TOTPLastCounter uint64
	RatingCeiling   int
	// AllLibraries is whether the account sees every library, whatever it is
	// granted (ADR-0037).
	AllLibraries bool
	CreatedAt    time.Time
	LastLoginAt  *time.Time
}

// Enrolled reports whether the user has an authenticator configured.
func (u *User) Enrolled() bool { return len(u.TOTPSecretEnc) > 0 && u.TOTPEnrolledAt != nil }

// AccountRequest is a submitted signup. It is not a user: it holds no session,
// no token and no permission, and it cannot authenticate.
type AccountRequest struct {
	ID           int64
	Username     string
	Email        string
	PasswordHash string
	Note         string
	SourceIP     string
	UserAgent    string
	InviteID     *int64
	State        string // pending | approved | denied
	CreatedAt    time.Time
	ExpiresAt    time.Time
	DecidedAt    *time.Time
	DecidedBy    *int64
	DenyReason   string
}

// Store is the identity repository. It is the only place identity SQL lives.
type Store struct {
	db     *db.DB
	cipher *secrets.Cipher
	params Argon2Params
	now    func() time.Time
}

// NewStore builds the repository.
func NewStore(database *db.DB, cipher *secrets.Cipher, params Argon2Params, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: database, cipher: cipher, params: params, now: now}
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func parseTSPtr(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTS(s.String)
	return &t
}

// ---------------------------------------------------------------------------
// Roles
// ---------------------------------------------------------------------------

// EnsureBuiltinRoles seeds Admin, Manager and User if they are absent, and
// reconciles their permission sets with the code. Reconciling matters: a
// permission added in a release must reach the Admin role, or the new feature
// is unreachable by anyone.
//
// Custom roles are never touched.
func (s *Store) EnsureBuiltinRoles(ctx context.Context) error {
	return s.db.InTx(ctx, func(tx db.Execer) error {
		for _, r := range authz.BuiltinRoles() {
			var id int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM role WHERE name = ?`, r.Name).Scan(&id)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				res, err := tx.ExecContext(ctx,
					`INSERT INTO role (name, rank, builtin, created_at, updated_at) VALUES (?, ?, 1, ?, ?)`,
					r.Name, r.Rank, ts(s.now()), ts(s.now()))
				if err != nil {
					return fmt.Errorf("identity: seed role %s: %w", r.Name, err)
				}
				if id, err = res.LastInsertId(); err != nil {
					return err
				}
			case err != nil:
				return err
			}

			// A role whose permissions a person chose keeps them (ADR-0039).
			// Admin is rewritten whatever it says: it holds every permission.
			if r.Name != authz.RoleAdmin {
				chosen, err := roleChosen(ctx, tx, id)
				if err != nil {
					return err
				}
				if chosen {
					continue
				}
			}

			if _, err := tx.ExecContext(ctx, `DELETE FROM role_permission WHERE role_id = ?`, id); err != nil {
				return err
			}
			for _, p := range r.Permissions.Slice() {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO role_permission (role_id, permission) VALUES (?, ?)`, id, string(p)); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// RoleByName loads a role and its permissions.
func (s *Store) RoleByName(ctx context.Context, name string) (authz.Role, error) {
	var r authz.Role
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, rank, builtin FROM role WHERE name = ?`, name).
		Scan(&r.ID, &r.Name, &r.Rank, &r.Builtin)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.Permissions, err = s.rolePermissions(ctx, r.ID)
	return r, err
}

// RoleByID loads a role by id.
func (s *Store) RoleByID(ctx context.Context, id int64) (authz.Role, error) {
	var r authz.Role
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, rank, builtin FROM role WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.Rank, &r.Builtin)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.Permissions, err = s.rolePermissions(ctx, r.ID)
	return r, err
}

func (s *Store) rolePermissions(ctx context.Context, roleID int64) (authz.PermissionSet, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT permission FROM role_permission WHERE role_id = ?`, roleID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	set := authz.PermissionSet{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		set[authz.Permission(p)] = struct{}{}
	}
	return set, rows.Err()
}

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

const userSelect = `
SELECT u.id, u.username, u.email, u.password_hash, u.state, u.role_id,
       r.name, r.rank, u.totp_secret_enc, u.totp_enrolled_at, u.totp_last_counter,
       u.rating_ceiling, u.all_libraries, u.created_at, u.last_login_at
FROM app_user u JOIN role r ON r.id = u.role_id `

func (s *Store) scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var enrolled, lastLogin sql.NullString
	var createdAt string
	var all int
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.State, &u.RoleID,
		&u.RoleName, &u.RoleRank, &u.TOTPSecretEnc, &enrolled, &u.TOTPLastCounter,
		&u.RatingCeiling, &all, &createdAt, &lastLogin)
	u.AllLibraries = all == 1
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.CreatedAt = parseTS(createdAt)
	u.TOTPEnrolledAt = parseTSPtr(enrolled)
	u.LastLoginAt = parseTSPtr(lastLogin)
	return &u, nil
}

// UserByUsername looks up by username, case-insensitively.
func (s *Store) UserByUsername(ctx context.Context, username string) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, userSelect+`WHERE u.username = ? COLLATE NOCASE`, username))
}

// UserByID looks up by id.
func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return s.scanUser(s.db.QueryRowContext(ctx, userSelect+`WHERE u.id = ?`, id))
}

// CountUsers returns the number of accounts. Zero means first run.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_user`).Scan(&n)
	return n, err
}

// NewUser describes an account to create.
type NewUser struct {
	Username      string
	Email         string
	PasswordHash  string
	RoleID        int64
	RatingCeiling int
	// AllLibraries grants every library; otherwise LibraryIDs are the root
	// folders the account may see (ADR-0037).
	AllLibraries bool
	LibraryIDs   []int64
	ApprovedBy   *int64
}

// CreateUser inserts an account in the awaiting_mfa state.
//
// Every account starts un-enrolled, including the first Admin. There is no
// path that produces an account able to act before it has an authenticator.
func (s *Store) CreateUser(ctx context.Context, nu NewUser) (int64, error) {
	var id int64
	err := s.db.InTx(ctx, func(tx db.Execer) error {
		now := ts(s.now())

		// Both columns are UNIQUE, so the database is the real guarantee. This
		// check exists to give the caller a usable answer instead of a driver
		// string: signup deliberately accepts a request for an existing name
		// rather than becoming an enumeration oracle, so a duplicate reaching
		// the approval queue is an ordinary event, not a corrupt state. Inside
		// the write transaction there is no race between the two.
		var taken int
		switch err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM app_user WHERE username = ? OR email = ? LIMIT 1`,
			nu.Username, nu.Email).Scan(&taken); {
		case err == nil:
			return ErrAccountExists
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}

		res, err := tx.ExecContext(ctx, `
			INSERT INTO app_user (username, email, password_hash, state, role_id,
			                      rating_ceiling, all_libraries, created_at, updated_at,
			                      approved_by_user_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			nu.Username, nu.Email, nu.PasswordHash, string(authz.StateAwaitingMFA),
			nu.RoleID, nu.RatingCeiling, boolInt(nu.AllLibraries), now, now, nu.ApprovedBy)
		if err != nil {
			return fmt.Errorf("identity: create user: %w", err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		if nu.AllLibraries {
			return nil
		}
		return writeGrants(ctx, tx, id, nu.LibraryIDs, now)
	})
	return id, err
}

// LibraryGrants returns the root folders a user is granted (ADR-0037). They
// matter only when the account is not granted every library.
func (s *Store) LibraryGrants(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT root_folder_id FROM root_folder_grant WHERE user_id = ? ORDER BY root_folder_id`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// TOTPSealContext is what an account's authenticator secret is sealed under,
// for the key rotation (ADR-0054) as for the store.
func TOTPSealContext(userID int64) string { return fmt.Sprintf("user:%d:totp", userID) }

// SetTOTPSecret stores an encrypted authenticator secret and marks the account
// active. This is the only transition out of awaiting_mfa.
func (s *Store) SetTOTPSecret(ctx context.Context, userID int64, secret string) error {
	sealed, err := s.cipher.EncryptString(secret, TOTPSealContext(userID))
	if err != nil {
		return err
	}
	now := ts(s.now())
	_, err = s.db.ExecContext(ctx, `
		UPDATE app_user SET totp_secret_enc = ?, totp_enrolled_at = ?, state = ?, updated_at = ?
		WHERE id = ?`,
		sealed, now, string(authz.StateActive), now, userID)
	return err
}

// ClearTOTPEnrollment discards an account's authenticator and everything tied
// to it, returning the account to awaiting_mfa.
//
// The recovery codes go with it. They were issued against the enrollment being
// discarded, and a code printed on paper years ago must not still open an
// account whose authenticator has since been replaced — that would make the
// codes a permanent second credential rather than a one-time bypass.
//
// The replay counter is reset in the same statement. It is protection scoped to
// a secret (totp_last_counter refuses any code at or below the last one used),
// and carrying a high-water mark across to a NEW secret would refuse the first
// legitimate code at some times and not others — the kind of intermittent
// failure nobody diagnoses correctly. Zero is the pre-enrollment value.
//
// This is the only write in the package that UNDOES enrollment. It has no
// permission check because it has no caller with a principal: Service.Recover
// runs from the host console, and a structural test keeps it there.
func (s *Store) ClearTOTPEnrollment(ctx context.Context, userID int64) error {
	return s.db.InTx(ctx, func(tx db.Execer) error {
		now := ts(s.now())
		if _, err := tx.ExecContext(ctx, `
			UPDATE app_user
			SET totp_secret_enc = NULL, totp_enrolled_at = NULL, totp_last_counter = 0,
			    state = ?, updated_at = ?
			WHERE id = ?`, string(authz.StateAwaitingMFA), now, userID); err != nil {
			return fmt.Errorf("identity: clearing enrollment: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM recovery_code WHERE user_id = ?`, userID); err != nil {
			return fmt.Errorf("identity: clearing recovery codes: %w", err)
		}
		return nil
	})
}

// TOTPSecret decrypts a user's authenticator secret.
func (s *Store) TOTPSecret(u *User) (string, error) {
	if len(u.TOTPSecretEnc) == 0 {
		return "", ErrNotFound
	}
	return s.cipher.DecryptString(u.TOTPSecretEnc, TOTPSealContext(u.ID))
}

// ConsumeTOTPCounter records a used time step and refuses a repeat.
//
// A TOTP code is valid for its whole step, so verification alone permits replay
// within that window. The UPDATE is conditional, which makes the check atomic
// under concurrent requests: only one can move the counter forward.
func (s *Store) ConsumeTOTPCounter(ctx context.Context, userID int64, counter uint64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE app_user SET totp_last_counter = ? WHERE id = ? AND totp_last_counter < ?`,
		counter, userID, counter)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrTOTPReplayed
	}
	return nil
}

// ErrTOTPReplayed means the code was already used for its time step.
var ErrTOTPReplayed = errors.New("identity: authenticator code already used")

// SetUserState changes an account's lifecycle state.
func (s *Store) SetUserState(ctx context.Context, userID int64, state authz.UserState) error {
	_, err := s.db.ExecContext(ctx, `UPDATE app_user SET state = ?, updated_at = ? WHERE id = ?`,
		string(state), ts(s.now()), userID)
	return err
}

// SetUserRole changes an account's role.
//
// Authorization lives in the service layer (authz.CanModifyUser); this is the
// bare write. Note what it does NOT do: it does not touch that user's API
// tokens, because tokens re-intersect their scope with the user's current role
// on every use, so a demotion narrows them automatically.
func (s *Store) SetUserRole(ctx context.Context, userID, roleID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE app_user SET role_id = ?, updated_at = ? WHERE id = ?`,
		roleID, ts(s.now()), userID)
	return err
}

// TouchLogin records a successful sign-in.
func (s *Store) TouchLogin(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE app_user SET last_login_at = ? WHERE id = ?`,
		ts(s.now()), userID)
	return err
}

// StoreRecoveryCodes replaces a user's recovery codes with hashes of the given
// plaintext codes. The plaintext is never stored.
func (s *Store) StoreRecoveryCodes(ctx context.Context, userID int64, codes []string) error {
	return s.db.InTx(ctx, func(tx db.Execer) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_code WHERE user_id = ?`, userID); err != nil {
			return err
		}
		for _, c := range codes {
			h, err := HashPassword(c, s.params)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO recovery_code (user_id, code_hash, created_at) VALUES (?, ?, ?)`,
				userID, h, ts(s.now())); err != nil {
				return err
			}
		}
		return nil
	})
}

// ConsumeRecoveryCode verifies a code and marks it used. Codes are single-use.
func (s *Store) ConsumeRecoveryCode(ctx context.Context, userID int64, code string) error {
	normalized := NormalizeRecoveryCode(code)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, code_hash FROM recovery_code WHERE user_id = ? AND used_at IS NULL`, userID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	type candidate struct {
		id   int64
		hash string
	}
	var all []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.hash); err != nil {
			return err
		}
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Every candidate is checked, with no early return, so that the time taken
	// does not reveal the position of a matching code in the set.
	matched := int64(-1)
	for _, c := range all {
		if err := VerifyPassword(normalized, c.hash); err == nil && matched < 0 {
			matched = c.id
		}
	}
	if matched < 0 {
		return ErrPasswordMismatch
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE recovery_code SET used_at = ? WHERE id = ? AND used_at IS NULL`, ts(s.now()), matched)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrPasswordMismatch
	}
	return nil
}

// UnusedRecoveryCodeCount reports how many codes remain.
func (s *Store) UnusedRecoveryCodeCount(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recovery_code WHERE user_id = ? AND used_at IS NULL`, userID).Scan(&n)
	return n, err
}

// ---------------------------------------------------------------------------
// Account requests
// ---------------------------------------------------------------------------

// CreateAccountRequest inserts a pending request.
func (s *Store) CreateAccountRequest(ctx context.Context, r AccountRequest) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO account_request (username, email, password_hash, note, source_ip,
		                             user_agent, invite_id, state, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		r.Username, r.Email, r.PasswordHash, nullStr(r.Note), nullStr(r.SourceIP),
		nullStr(r.UserAgent), r.InviteID, ts(r.CreatedAt), ts(r.ExpiresAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CountPendingRequests is the cap check for open registration.
func (s *Store) CountPendingRequests(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM account_request WHERE state = 'pending'`).Scan(&n)
	return n, err
}

// PendingRequests lists the approval queue.
func (s *Store) PendingRequests(ctx context.Context, limit int) ([]AccountRequest, error) {
	if err := authz.RequirePermission(ctx, authz.PermApproveAccounts); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, username, email, COALESCE(note,''), COALESCE(source_ip,''),
		       COALESCE(user_agent,''), state, created_at, expires_at
		FROM account_request WHERE state = 'pending'
		ORDER BY created_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []AccountRequest
	for rows.Next() {
		var r AccountRequest
		var created, expires string
		if err := rows.Scan(&r.ID, &r.Username, &r.Email, &r.Note, &r.SourceIP,
			&r.UserAgent, &r.State, &created, &expires); err != nil {
			return nil, err
		}
		r.CreatedAt, r.ExpiresAt = parseTS(created), parseTS(expires)
		out = append(out, r)
	}
	return out, rows.Err()
}

// AccountRequestByID loads one pending request, including its password hash so
// approval can promote it without asking the applicant to choose again.
func (s *Store) AccountRequestByID(ctx context.Context, id int64) (*AccountRequest, error) {
	if err := authz.RequirePermission(ctx, authz.PermApproveAccounts); err != nil {
		return nil, err
	}
	var r AccountRequest
	var created, expires string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, username, email, password_hash, COALESCE(note,''), COALESCE(source_ip,''),
		       COALESCE(user_agent,''), invite_id, state, created_at, expires_at
		FROM account_request WHERE id = ?`, id).
		Scan(&r.ID, &r.Username, &r.Email, &r.PasswordHash, &r.Note, &r.SourceIP,
			&r.UserAgent, &r.InviteID, &r.State, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.CreatedAt, r.ExpiresAt = parseTS(created), parseTS(expires)
	return &r, nil
}

// DecideRequest marks a request approved or denied.
func (s *Store) DecideRequest(ctx context.Context, id int64, state string, deciderID int64, reason string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE account_request SET state = ?, decided_at = ?, decided_by_user_id = ?, deny_reason = ?
		WHERE id = ? AND state = 'pending'`,
		state, ts(s.now()), deciderID, nullStr(reason), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeExpiredRequests deletes requests past their TTL. Called by the scheduler.
func (s *Store) PurgeExpiredRequests(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM account_request WHERE state = 'pending' AND expires_at < ?`, ts(s.now()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------------------
// Throttling
// ---------------------------------------------------------------------------

// RecordAttempt appends to the throttling ledger.
func (s *Store) RecordAttempt(ctx context.Context, kind, key string, success bool) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO auth_attempt (key, kind, occurred_at, success) VALUES (?, ?, ?, ?)`,
		key, kind, ts(s.now()), success)
	return err
}

// RecentFailures counts failures for a key inside a window.
func (s *Store) RecentFailures(ctx context.Context, kind, key string, window time.Duration) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM auth_attempt
		WHERE kind = ? AND key = ? AND success = 0 AND occurred_at > ?`,
		kind, key, ts(s.now().Add(-window))).Scan(&n)
	return n, err
}

// PurgeOldAttempts trims the ledger.
func (s *Store) PurgeOldAttempts(ctx context.Context, olderThan time.Duration) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM auth_attempt WHERE occurred_at < ?`, ts(s.now().Add(-olderThan)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

// Setting reads a setting value.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SetSetting writes a setting value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO setting (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, ts(s.now()))
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ListRoles returns every role with its permissions, ordered by descending
// rank. It applies no authorization of its own: callers filter by what the
// actor may actually assign (see Service.AssignableRoles).
func (s *Store) ListRoles(ctx context.Context) ([]authz.Role, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, rank, builtin FROM role ORDER BY rank DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []authz.Role
	for rows.Next() {
		var r authz.Role
		if err := rows.Scan(&r.ID, &r.Name, &r.Rank, &r.Builtin); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Permissions are loaded in a second pass rather than by a join, so that a
	// role with no permissions still appears.
	for i := range out {
		if out[i].Permissions, err = s.rolePermissions(ctx, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ListUsers returns every account, most recently created first.
//
// Deliberately unfiltered by state: an administrator managing accounts needs to
// see the suspended ones, because reactivating one is the commonest reason to
// be on that screen at all.
//
// No permission check here — this is the data layer and the caller is
// Service.ListUsers, which checks. Putting it in both places would be the
// arrangement where one of them is eventually removed as redundant.
func (s *Store) ListUsers(ctx context.Context, limit int) ([]*User, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, userSelect+`ORDER BY u.created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]*User, 0)
	for rows.Next() {
		u, serr := s.scanUser(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountActiveAdmins returns how many usable accounts hold system settings.
//
// "Usable" is the whole point: an account that is suspended, or that has never
// enrolled an authenticator, cannot sign in and therefore cannot rescue an
// instance. Counting it as an administrator is how somebody ends up locked out
// of their own server with a row in the database that says otherwise.
func (s *Store) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM app_user u
		JOIN role_permission rp ON rp.role_id = u.role_id
		WHERE u.state = ?
		  AND u.totp_secret_enc IS NOT NULL
		  AND u.totp_enrolled_at IS NOT NULL
		  AND rp.permission = ?`,
		string(authz.StateActive), string(authz.PermSystemSettings)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("identity: counting administrators: %w", err)
	}
	return n, nil
}

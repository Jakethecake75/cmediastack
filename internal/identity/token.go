package identity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// Token errors.
var (
	ErrTokenInvalid = errors.New("identity: api token is invalid, expired or revoked")
	// ErrTokenScopeTooWide means the requested scope exceeds what the issuing
	// user holds. A token can never be a privilege escalation.
	ErrTokenScopeTooWide = errors.New("identity: token scope exceeds the issuer's own permissions")
)

// TokenPrefix marks a CMediaStack API token in logs and secret scanners.
//
// A recognisable prefix is a feature: it lets gitleaks and GitHub's scanning
// match on it, so a token pasted into a public repository can be found.
const TokenPrefix = "cms_pat_"

// APIToken is a scoped, revocable, non-interactive credential.
type APIToken struct {
	ID          int64
	UserID      int64
	Name        string
	Permissions []authz.Permission
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func marshalPerms(perms []authz.Permission) string {
	if perms == nil {
		perms = []authz.Permission{}
	}
	b, _ := json.Marshal(perms)
	return string(b)
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

// CreateAPIToken stores a token and returns its plaintext exactly once.
func (s *Store) CreateAPIToken(ctx context.Context, t APIToken) (string, int64, error) {
	raw, err := randomToken(32)
	if err != nil {
		return "", 0, err
	}
	token := TokenPrefix + raw

	var expires any
	if t.ExpiresAt != nil {
		expires = ts(*t.ExpiresAt)
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO api_token (user_id, name, token_hash, permissions_json, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		t.UserID, t.Name, hashToken(token), marshalPerms(t.Permissions),
		ts(t.CreatedAt), expires)
	if err != nil {
		return "", 0, fmt.Errorf("identity: create api token: %w", err)
	}
	id, err := res.LastInsertId()
	return token, id, err
}

// ResolveAPIToken validates a token and returns it with its owner.
//
// It also records last use. That write is best-effort: a failure to update a
// timestamp must not deny an otherwise valid request.
func (s *Store) ResolveAPIToken(ctx context.Context, token string) (*APIToken, *User, error) {
	if !strings.HasPrefix(strings.TrimSpace(token), TokenPrefix) {
		return nil, nil, ErrTokenInvalid
	}

	var t APIToken
	var permsJSON, created string
	var expires, lastUsed, revoked sql.NullString

	err := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, name, permissions_json, created_at, expires_at, last_used_at, revoked_at
		FROM api_token WHERE token_hash = ?`, hashToken(token)).
		Scan(&t.ID, &t.UserID, &t.Name, &permsJSON, &created, &expires, &lastUsed, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, nil, err
	}

	if revoked.Valid && revoked.String != "" {
		return nil, nil, ErrTokenInvalid
	}
	t.CreatedAt = parseTS(created)
	t.ExpiresAt = parseTSPtr(expires)
	t.LastUsedAt = parseTSPtr(lastUsed)
	if t.ExpiresAt != nil && s.now().After(*t.ExpiresAt) {
		return nil, nil, ErrTokenInvalid
	}
	_ = json.Unmarshal([]byte(permsJSON), &t.Permissions)

	user, err := s.UserByID(ctx, t.UserID)
	if err != nil {
		return nil, nil, ErrTokenInvalid
	}
	// A token is the user's credential, so it dies with the user's access.
	if user.State != authz.StateActive {
		return nil, nil, ErrTokenInvalid
	}

	_, _ = s.db.ExecContext(ctx,
		`UPDATE api_token SET last_used_at = ? WHERE id = ?`, ts(s.now()), t.ID)

	return &t, user, nil
}

// ListAPITokens returns a user's tokens. The token values are not stored, so
// they cannot be listed.
func (s *Store) ListAPITokens(ctx context.Context, userID int64) ([]APIToken, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, name, permissions_json, created_at, expires_at, last_used_at, revoked_at
		FROM api_token WHERE user_id = ? AND revoked_at IS NULL
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []APIToken
	for rows.Next() {
		var t APIToken
		var permsJSON, created string
		var expires, lastUsed, revoked sql.NullString
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &permsJSON, &created,
			&expires, &lastUsed, &revoked); err != nil {
			return nil, err
		}
		t.CreatedAt = parseTS(created)
		t.ExpiresAt = parseTSPtr(expires)
		t.LastUsedAt = parseTSPtr(lastUsed)
		t.RevokedAt = parseTSPtr(revoked)
		_ = json.Unmarshal([]byte(permsJSON), &t.Permissions)
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeOwnedAPIToken revokes a token, but only if it belongs to userID.
//
// As with sessions, ownership is a predicate in the WHERE clause rather than a
// preceding lookup, so it cannot be skipped and a foreign id is
// indistinguishable from an unknown one.
func (s *Store) RevokeOwnedAPIToken(ctx context.Context, userID, tokenID int64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE api_token SET revoked_at = ?
		WHERE id = ? AND user_id = ? AND revoked_at IS NULL`,
		ts(s.now()), tokenID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeAllUserTokens kills every token a user holds. Called on suspension so
// that §7.2's "immediately revokes all sessions, API tokens" is literal.
func (s *Store) RevokeAllUserTokens(ctx context.Context, userID int64, reason string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_token SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`,
		ts(s.now()), userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PurgeExpiredTokens removes dead token rows.
func (s *Store) PurgeExpiredTokens(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM api_token
		WHERE (expires_at IS NOT NULL AND expires_at < ?)
		   OR (revoked_at IS NOT NULL AND revoked_at < ?)`,
		ts(s.now()), ts(s.now().Add(-30*24*time.Hour)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// BuildTokenPrincipal assembles a principal for an API token.
//
// The scope is intersected with the user's CURRENT role permissions, not the
// set captured at issuance. That matters: if the user is demoted, every token
// they hold narrows on its next use, without anyone having to remember to go
// and revoke them.
func (s *Store) BuildTokenPrincipal(ctx context.Context, t *APIToken, user *User) (*authz.Principal, error) {
	role, err := s.RoleByID(ctx, user.RoleID)
	if err != nil {
		return nil, err
	}
	grants, err := s.LibraryGrants(ctx, user.ID)
	if err != nil {
		return nil, err
	}

	effective := authz.PermissionSet{}
	for _, p := range t.Permissions {
		if role.Permissions.Has(p) {
			effective[p] = struct{}{}
		}
	}

	scoped := role
	scoped.Permissions = effective

	return &authz.Principal{
		UserID:                user.ID,
		Username:              user.Username,
		Role:                  scoped,
		State:                 user.State,
		SessionID:             fmt.Sprintf("token:%d", t.ID),
		LibraryIDs:            grants,
		UnrestrictedLibraries: user.AllLibraries || effective.Has(authz.PermSystemSettings),
		RatingCeiling:         user.RatingCeiling,
		// A token is issued by a session that had already satisfied MFA, and a
		// token cannot itself reach any credential-management route (those are
		// marked session-only), so treating it as satisfied does not weaken
		// the second factor.
		MFASatisfied: true,
		Credential:   authz.CredentialToken,
	}, nil
}

// ---------------------------------------------------------------------------
// Service
// ---------------------------------------------------------------------------

// IssueTokenInput describes a token to create.
type IssueTokenInput struct {
	Name        string
	Permissions []authz.Permission
	TTL         time.Duration
	SourceIP    string
	UserAgent   string
}

// IssueAPIToken creates a scoped token for the calling user.
//
// The scope must be a subset of what the caller currently holds. Without that
// check a User could mint themselves an admin token, which would make the whole
// role system decorative.
func (svc *Service) IssueAPIToken(ctx context.Context, in IssueTokenInput) (string, *APIToken, error) {
	p := authz.FromContext(ctx)
	if p == nil || !p.CanAct() {
		return "", nil, ErrLoginFailed
	}

	if strings.TrimSpace(in.Name) == "" {
		in.Name = "unnamed token"
	}
	if len(in.Permissions) == 0 {
		return "", nil, ErrTokenScopeTooWide
	}

	requested := authz.NewPermissionSet(in.Permissions...)
	if !requested.IsSubsetOf(p.Role.Permissions) {
		var missing []string
		for perm := range requested {
			if !p.Role.Permissions.Has(perm) {
				missing = append(missing, string(perm))
			}
		}
		_ = svc.audit.Write(ctx, audit.Event{
			ActorUserID: &p.UserID, ActorLabel: p.Username,
			Action: audit.ActionAPITokenIssued, Outcome: audit.OutcomeDenied,
			SourceIP: in.SourceIP, UserAgent: in.UserAgent,
			Detail: "requested scope exceeds the issuer's permissions: " + strings.Join(missing, ", "),
		})
		return "", nil, ErrTokenScopeTooWide
	}

	now := svc.now()
	t := APIToken{
		UserID:      p.UserID,
		Name:        in.Name,
		Permissions: in.Permissions,
		CreatedAt:   now,
	}
	if in.TTL > 0 {
		exp := now.Add(in.TTL)
		t.ExpiresAt = &exp
	}

	raw, id, err := svc.store.CreateAPIToken(ctx, t)
	if err != nil {
		return "", nil, err
	}
	t.ID = id

	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &p.UserID, ActorLabel: p.Username,
		Action: audit.ActionAPITokenIssued, TargetKind: "api_token",
		TargetID: fmt.Sprintf("%d", id), SourceIP: in.SourceIP, UserAgent: in.UserAgent,
		After: map[string]any{
			"name": t.Name, "permissions": in.Permissions, "expires_at": t.ExpiresAt,
		},
	})
	return raw, &t, nil
}

// RevokeAPIToken revokes one of the caller's own tokens.
func (svc *Service) RevokeAPIToken(ctx context.Context, tokenID int64, sourceIP, userAgent string) error {
	p := authz.FromContext(ctx)
	if p == nil || !p.CanAct() {
		return ErrLoginFailed
	}
	if err := svc.store.RevokeOwnedAPIToken(ctx, p.UserID, tokenID); err != nil {
		return err
	}
	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &p.UserID, ActorLabel: p.Username,
		Action: audit.ActionAPITokenRevoked, TargetKind: "api_token",
		TargetID: fmt.Sprintf("%d", tokenID), SourceIP: sourceIP, UserAgent: userAgent,
	})
	return nil
}

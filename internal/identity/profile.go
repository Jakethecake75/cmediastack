package identity

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// An account an administrator creates, a pending request read alone, and a
// person's own email (ADR-0039).

var (
	// ErrInvalidEmail refuses something that is not one plain address.
	ErrInvalidEmail = errors.New("identity: that is not an email address")
	// ErrInvalidUsername refuses a username outside the allowed shape.
	ErrInvalidUsername = errors.New("identity: a username is 3 to 32 letters, digits, dots, dashes or underscores")
)

// NewAccountLinkTTL is how long the link that sets a new account's password
// lasts: long enough to pass on out of band, short enough not to linger.
const NewAccountLinkTTL = 72 * time.Hour

// ValidEmail reports whether s is one plain address, with no display name.
func ValidEmail(s string) error {
	s = strings.TrimSpace(s)
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" || len(s) > 254 {
		return ErrInvalidEmail
	}
	return nil
}

// ValidUsername reports whether s is a username this instance accepts.
func ValidUsername(s string) error {
	if n := len(s); n < 3 || n > 32 {
		return ErrInvalidUsername
	}
	for _, r := range s {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' || r == '_'
		if r > unicode.MaxASCII || !ok {
			return ErrInvalidUsername
		}
	}
	return nil
}

// PendingRequest returns one account request still waiting for a decision.
// A decided, expired or unknown request is ErrNotFound alike.
func (svc *Service) PendingRequest(ctx context.Context, id int64) (*AccountRequest, error) {
	req, err := svc.store.AccountRequestByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if req.State != "pending" || !svc.now().Before(req.ExpiresAt) {
		return nil, ErrNotFound
	}
	req.PasswordHash = ""
	return req, nil
}

// NewAccount is what an administrator chooses for an account they create.
type NewAccount struct {
	Username  string
	Email     string
	RoleID    int64
	Grant     authz.Grant
	SourceIP  string
	UserAgent string
}

// CreateAccount creates an account on an administrator's authority, and returns
// a one-time link token that sets its password. The administrator never knows
// the password: the account starts with a random one nobody holds.
func (svc *Service) CreateAccount(ctx context.Context, in NewAccount) (int64, string, time.Time, error) {
	actor := authz.FromContext(ctx)
	if err := authz.RequirePermission(ctx, authz.PermManageUsers); err != nil {
		return 0, "", time.Time{}, err
	}
	username := strings.TrimSpace(in.Username)
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if err := ValidUsername(username); err != nil {
		return 0, "", time.Time{}, err
	}
	if err := ValidEmail(email); err != nil {
		return 0, "", time.Time{}, err
	}
	role, err := svc.store.RoleByID(ctx, in.RoleID)
	if err != nil {
		return 0, "", time.Time{}, err
	}
	for _, check := range []error{authz.CanAssignRole(ctx, role), authz.CanGrant(ctx, in.Grant)} {
		if check != nil {
			if d, ok := authz.AsDenial(check); ok {
				svc.audit.AuthzDenied(ctx, "user.create", d, in.SourceIP, in.UserAgent)
			}
			return 0, "", time.Time{}, check
		}
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return 0, "", time.Time{}, err
	}
	hash, err := HashPassword(base64.RawStdEncoding.EncodeToString(secret), svc.policy.Argon2)
	if err != nil {
		return 0, "", time.Time{}, err
	}
	nu := NewUser{Username: username, Email: email, PasswordHash: hash, RoleID: role.ID,
		RatingCeiling: in.Grant.RatingCeiling, AllLibraries: in.Grant.AllLibraries,
		ApprovedBy: &actor.UserID}
	if !in.Grant.AllLibraries {
		nu.LibraryIDs = in.Grant.RootFolderIDs
	}
	id, err := svc.store.CreateUser(ctx, nu)
	if err != nil {
		return 0, "", time.Time{}, err
	}
	token, expires, err := svc.store.CreateResetToken(ctx, id, NewAccountLinkTTL, in.SourceIP, &actor.UserID)
	if err != nil {
		return 0, "", time.Time{}, err
	}
	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &actor.UserID, ActorLabel: actor.Username,
		Action: audit.ActionUserCreated, TargetKind: "user", TargetID: fmt.Sprintf("%d", id),
		SourceIP: in.SourceIP, UserAgent: in.UserAgent,
		After: map[string]any{"username": username, "role": role.Name, "all_libraries": in.Grant.AllLibraries,
			"library_ids": in.Grant.RootFolderIDs, "rating_ceiling": in.Grant.RatingCeiling},
		Detail: username + " created as " + role.Name + ", " + DescribeGrant(in.Grant) +
			"; a link to set its password was issued",
	})
	return id, token, expires, nil
}

// ChangeEmail changes the caller's own address. The current password is
// required, as for a new password: an open session is not enough to redirect
// where a reset would go.
func (svc *Service) ChangeEmail(ctx context.Context, current, email, sourceIP, userAgent string) error {
	p := authz.FromContext(ctx)
	if p == nil || !p.CanAct() {
		return ErrLoginFailed
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if err := ValidEmail(email); err != nil {
		return err
	}
	user, err := svc.store.UserByID(ctx, p.UserID)
	if err != nil {
		return err
	}
	if err := VerifyPassword(current, user.PasswordHash); err != nil {
		_ = svc.store.RecordAttempt(ctx, "login", fmt.Sprintf("user:%s", strings.ToLower(user.Username)), false)
		_ = svc.audit.Write(ctx, audit.Event{
			ActorUserID: &user.ID, ActorLabel: user.Username, Action: audit.ActionUserUpdated,
			Outcome: audit.OutcomeFailure, SourceIP: sourceIP, UserAgent: userAgent,
			Detail: "email change: current password did not verify",
		})
		return ErrLoginFailed
	}
	if strings.EqualFold(user.Email, email) {
		return nil
	}
	if err := svc.store.SetEmail(ctx, user.ID, email); err != nil {
		return err
	}
	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &user.ID, ActorLabel: user.Username, Action: audit.ActionUserUpdated,
		TargetKind: "user", TargetID: fmt.Sprintf("%d", user.ID),
		SourceIP: sourceIP, UserAgent: userAgent, Detail: "changed their email address",
	})
	return nil
}

// SetEmail stores an account's address; a taken one is ErrAccountExists.
func (s *Store) SetEmail(ctx context.Context, userID int64, email string) error {
	var taken int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM app_user WHERE email = ? AND id <> ?`, email, userID).Scan(&taken); err != nil {
		return err
	}
	if taken > 0 {
		return ErrAccountExists
	}
	_, err := s.db.ExecContext(ctx, `UPDATE app_user SET email = ?, updated_at = ? WHERE id = ?`,
		email, ts(s.now()), userID)
	return err
}

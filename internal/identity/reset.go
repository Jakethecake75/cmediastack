package identity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// Reset errors.
var (
	ErrResetInvalid = errors.New("identity: reset token is invalid, expired or already used")
	// ErrNoResetDelivery means a reset token was created but there is no
	// transport configured to send it. The caller must NOT surface this to an
	// anonymous requester: doing so would confirm the account exists.
	ErrNoResetDelivery = errors.New("identity: no password-reset delivery transport is configured")
)

// ResetTTL is how long a reset token stays usable. Short on purpose: the token
// is a bearer credential that bypasses the password.
const ResetTTL = 30 * time.Minute

func hashResetToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// ResetDelivery sends a reset token to its owner.
//
// There is no mail transport in this build, so the default implementation
// refuses. The flow is still complete and correct: an administrator mints a
// token with MintResetToken and passes it to the user out of band, which is a
// reasonable path for an instance with a handful of accounts and avoids
// standing up an SMTP dependency nobody asked for.
type ResetDelivery interface {
	Deliver(ctx context.Context, user *User, token string, expires time.Time) error
}

// NoResetDelivery is the default: it sends nothing and says so.
type NoResetDelivery struct{}

// Deliver always fails.
func (NoResetDelivery) Deliver(context.Context, *User, string, time.Time) error {
	return ErrNoResetDelivery
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

// CreateResetToken issues a single-use reset token.
func (s *Store) CreateResetToken(ctx context.Context, userID int64, ttl time.Duration, sourceIP string, issuedBy *int64) (string, time.Time, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", time.Time{}, err
	}
	now := s.now()
	expires := now.Add(ttl)

	// Outstanding tokens for this user are invalidated first. Two live reset
	// tokens means two chances for a leaked one to work.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE password_reset SET used_at = ? WHERE user_id = ? AND used_at IS NULL`,
		ts(now), userID); err != nil {
		return "", time.Time{}, err
	}

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO password_reset (user_id, token_hash, created_at, expires_at, source_ip, issued_by_user_id)
		VALUES (?, ?, ?, ?, ?, ?)`,
		userID, hashResetToken(token), ts(now), ts(expires), nullStr(sourceIP), issuedBy); err != nil {
		return "", time.Time{}, fmt.Errorf("identity: create reset token: %w", err)
	}
	return token, expires, nil
}

// ConsumeResetToken validates a token and marks it used, returning its user.
//
// The UPDATE is conditional on the token still being unused, so single-use is
// enforced by the database rather than by a check-then-act.
func (s *Store) ConsumeResetToken(ctx context.Context, token string) (*User, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrResetInvalid
	}

	var id, userID int64
	var expires string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, expires_at FROM password_reset WHERE token_hash = ? AND used_at IS NULL`,
		hashResetToken(token)).Scan(&id, &userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrResetInvalid
	}
	if err != nil {
		return nil, err
	}
	if s.now().After(parseTS(expires)) {
		return nil, ErrResetInvalid
	}

	res, err := s.db.ExecContext(ctx,
		`UPDATE password_reset SET used_at = ? WHERE id = ? AND used_at IS NULL`, ts(s.now()), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrResetInvalid
	}

	return s.UserByID(ctx, userID)
}

// SetPassword replaces a user's credential.
func (s *Store) SetPassword(ctx context.Context, userID int64, hash string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE app_user SET password_hash = ?, updated_at = ? WHERE id = ?`,
		hash, ts(s.now()), userID)
	return err
}

// PurgeExpiredResetTokens removes dead tokens.
func (s *Store) PurgeExpiredResetTokens(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM password_reset WHERE expires_at < ? OR used_at IS NOT NULL`, ts(s.now()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------------------
// Service
// ---------------------------------------------------------------------------

// InitiateReset starts a password reset.
//
// It returns nil whether or not the account exists, and whether or not
// delivery worked. Every observable — status, body, and the work done — must be
// the same for a registered and an unregistered address, or the endpoint
// becomes the enumeration oracle that the signup form was carefully built not
// to be.
func (svc *Service) InitiateReset(ctx context.Context, username, sourceIP, userAgent string) error {
	n, err := svc.store.RecentFailures(ctx, "reset", "ip:"+sourceIP, time.Hour)
	if err != nil {
		return err
	}
	if n >= 5 {
		return ErrThrottled
	}
	_ = svc.store.RecordAttempt(ctx, "reset", "ip:"+sourceIP, false)

	user, err := svc.store.UserByUsername(ctx, username)
	if err != nil {
		// The same answer whatever the reason — the caller's response must not
		// say whether the account exists — but the audit line says which.
		detail := "no such account"
		if !errors.Is(err, ErrNotFound) {
			detail = "the account could not be looked up: " + err.Error()
		}
		_ = svc.audit.Write(ctx, audit.Event{
			ActorLabel: "anonymous", Action: audit.ActionPasswordResetRequested,
			Outcome: audit.OutcomeFailure, SourceIP: sourceIP, UserAgent: userAgent,
			Detail: detail,
		})
		return nil //nolint:nilerr // an identical answer for every username is the design
	}

	token, expires, err := svc.store.CreateResetToken(ctx, user.ID, ResetTTL, sourceIP, nil)
	if err != nil {
		return err
	}

	deliveryErr := svc.delivery.Deliver(ctx, user, token, expires)

	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &user.ID, ActorLabel: user.Username,
		Action: audit.ActionPasswordResetRequested, SourceIP: sourceIP, UserAgent: userAgent,
		Detail: deliveryDetail(deliveryErr),
	})
	return nil
}

func deliveryDetail(err error) string {
	if err == nil {
		return "token delivered"
	}
	if errors.Is(err, ErrNoResetDelivery) {
		return "token created but NOT delivered: no transport configured; " +
			"an administrator must mint a link instead"
	}
	return "delivery failed: " + err.Error()
}

// MintResetToken lets an administrator create a reset link for a user.
//
// This is the working delivery path while there is no mail transport. It is an
// administrative action against another account, so it goes through the same
// escalation guard as any other: a Manager cannot mint a reset for an Admin.
func (svc *Service) MintResetToken(ctx context.Context, targetID int64, sourceIP, userAgent string) (string, time.Time, error) {
	actor := authz.FromContext(ctx)

	target, err := svc.store.UserByID(ctx, targetID)
	if err != nil {
		return "", time.Time{}, err
	}
	targetRole, err := svc.store.RoleByID(ctx, target.RoleID)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := authz.CanModifyUser(ctx, authz.TargetUser{UserID: target.ID, Role: targetRole}); err != nil {
		if d, ok := authz.AsDenial(err); ok {
			svc.audit.AuthzDenied(ctx, "user.reset_link", d, sourceIP, userAgent)
		}
		return "", time.Time{}, err
	}

	token, expires, err := svc.store.CreateResetToken(ctx, target.ID, ResetTTL, sourceIP, &actor.UserID)
	if err != nil {
		return "", time.Time{}, err
	}

	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &actor.UserID, ActorLabel: actor.Username,
		Action: audit.ActionPasswordResetMinted, TargetKind: "user",
		TargetID: fmt.Sprintf("%d", targetID), SourceIP: sourceIP, UserAgent: userAgent,
		Detail: "administrator minted a reset link",
	})
	return token, expires, nil
}

// CompleteReset consumes a token and sets a new password.
//
// Two things it deliberately does NOT do:
//
//   - It does not sign the user in. They must log in, and then present their
//     second factor. A reset recovers a forgotten password; it is not a way
//     around the authenticator.
//   - It does not preserve sessions. Every session is revoked, because the
//     usual reason to reset is that the old credential may be compromised.
func (svc *Service) CompleteReset(ctx context.Context, token, newPassword, sourceIP, userAgent string) error {
	if err := svc.acceptablePassword(ctx, newPassword); err != nil {
		return err
	}

	user, err := svc.store.ConsumeResetToken(ctx, token)
	if err != nil {
		_ = svc.audit.Write(ctx, audit.Event{
			ActorLabel: "anonymous", Action: audit.ActionPasswordChanged,
			Outcome: audit.OutcomeFailure, SourceIP: sourceIP, UserAgent: userAgent,
			Detail: "invalid or expired reset token",
		})
		return ErrResetInvalid
	}

	hash, err := HashPassword(newPassword, svc.policy.Argon2)
	if err != nil {
		return err
	}
	if err := svc.store.SetPassword(ctx, user.ID, hash); err != nil {
		return err
	}
	revoked, err := svc.store.RevokeAllUserSessions(ctx, user.ID, "password_reset")
	if err != nil {
		return err
	}

	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &user.ID, ActorLabel: user.Username,
		Action: audit.ActionPasswordChanged, SourceIP: sourceIP, UserAgent: userAgent,
		Detail: fmt.Sprintf("via reset token; %d sessions revoked", revoked),
	})
	return nil
}

// ChangePassword updates the caller's own password.
//
// The current password is required even though the caller already holds a
// session: a session left open on a shared machine should not be enough to
// take the account over permanently.
func (svc *Service) ChangePassword(ctx context.Context, current, next, sourceIP, userAgent string) error {
	p := authz.FromContext(ctx)
	if p == nil || !p.CanAct() {
		return ErrLoginFailed
	}
	if err := svc.acceptablePassword(ctx, next); err != nil {
		return err
	}

	user, err := svc.store.UserByID(ctx, p.UserID)
	if err != nil {
		return err
	}
	if err := VerifyPassword(current, user.PasswordHash); err != nil {
		_ = svc.store.RecordAttempt(ctx, "login", fmt.Sprintf("user:%s", strings.ToLower(user.Username)), false)
		_ = svc.audit.Write(ctx, audit.Event{
			ActorUserID: &user.ID, ActorLabel: user.Username,
			Action: audit.ActionPasswordChanged, Outcome: audit.OutcomeFailure,
			SourceIP: sourceIP, UserAgent: userAgent, Detail: "current password did not verify",
		})
		return ErrLoginFailed
	}

	hash, err := HashPassword(next, svc.policy.Argon2)
	if err != nil {
		return err
	}
	if err := svc.store.SetPassword(ctx, user.ID, hash); err != nil {
		return err
	}

	// Other sessions die; this one survives, so changing a password does not
	// log you out of the browser you are doing it in.
	revoked, err := svc.store.RevokeOtherUserSessions(ctx, user.ID, p.SessionID, "password_changed")
	if err != nil {
		return err
	}

	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &user.ID, ActorLabel: user.Username,
		Action: audit.ActionPasswordChanged, SourceIP: sourceIP, UserAgent: userAgent,
		Detail: fmt.Sprintf("self-service; %d other sessions revoked", revoked),
	})
	return nil
}

// Reauthenticate asks the signed-in account for its password and a current
// authenticator code again, before a change a stolen session must not be able
// to make (ADR-0065). A recovery code is not accepted: it would be spent on a
// settings change, and it is the account's way back in, not a confirmation.
// Failures count toward the sign-in lockout as a failed sign-in does.
func (svc *Service) Reauthenticate(ctx context.Context, password, code, sourceIP, userAgent string) error {
	p := authz.FromContext(ctx)
	if p == nil || !p.CanAct() {
		return ErrLoginFailed
	}
	user, err := svc.store.UserByID(ctx, p.UserID)
	if err != nil {
		return err
	}

	userKey := "user:" + strings.ToLower(user.Username)
	n, err := svc.store.RecentFailures(ctx, "login", userKey, svc.policy.LoginWindow)
	if err != nil {
		return err
	}
	if n >= svc.policy.LoginMaxAttempts {
		return ErrThrottled
	}
	if err := VerifyPassword(password, user.PasswordHash); err != nil {
		_ = svc.store.RecordAttempt(ctx, "login", userKey, false)
		_ = svc.audit.Write(ctx, audit.Event{
			ActorUserID: &user.ID, ActorLabel: user.Username,
			Action: audit.ActionLoginFailed, Outcome: audit.OutcomeFailure,
			SourceIP: sourceIP, UserAgent: userAgent, Detail: "re-authentication: password did not verify",
		})
		return ErrLoginFailed
	}

	secret, err := svc.store.TOTPSecret(user)
	now := svc.now()
	if err == nil {
		if err = VerifyTOTP(secret, code, now); err == nil {
			// A code stays valid for its whole step; consuming the counter is
			// what stops it being offered twice.
			err = svc.store.ConsumeTOTPCounter(ctx, user.ID, ConsumedCounter(now))
		}
	}
	if err != nil {
		_ = svc.store.RecordAttempt(ctx, "login", userKey, false)
		_ = svc.audit.Write(ctx, audit.Event{
			ActorUserID: &user.ID, ActorLabel: user.Username,
			Action: audit.ActionMFAFailed, Outcome: audit.OutcomeFailure,
			SourceIP: sourceIP, UserAgent: userAgent, Detail: "re-authentication: authenticator code refused",
		})
		return ErrLoginFailed
	}
	return nil
}

// RegenerateRecoveryCodes issues a fresh set, invalidating the old ones.
func (svc *Service) RegenerateRecoveryCodes(ctx context.Context, currentPassword, sourceIP, userAgent string) ([]string, error) {
	p := authz.FromContext(ctx)
	if p == nil || !p.CanAct() {
		return nil, ErrLoginFailed
	}

	user, err := svc.store.UserByID(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	// Re-authentication: recovery codes are a second factor, and minting new
	// ones from a hijacked session would be a complete account takeover.
	if err := VerifyPassword(currentPassword, user.PasswordHash); err != nil {
		return nil, ErrLoginFailed
	}

	codes, err := GenerateRecoveryCodes(svc.policy.RecoveryCodeCount)
	if err != nil {
		return nil, err
	}
	if err := svc.store.StoreRecoveryCodes(ctx, user.ID, codes); err != nil {
		return nil, err
	}

	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &user.ID, ActorLabel: user.Username,
		Action:   audit.ActionRecoveryCodesRegenerated,
		SourceIP: sourceIP, UserAgent: userAgent,
		Detail: fmt.Sprintf("%d codes issued; all previous codes invalidated", len(codes)),
	})
	return codes, nil
}

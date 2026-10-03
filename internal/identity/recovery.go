package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// Break-glass recovery, from the host console only.
//
// # The problem this solves
//
// Every role assignment goes through authz.CanAssignRole, which is strictly
// downward, so an instance has exactly one administrator: the one the setup
// wizard made. Lose that account's authenticator AND its recovery codes and
// there is no path back in. The instance is intact, the data is intact, and
// nobody can administer it.
//
// # Why not simply allow a second administrator
//
// That was the first answer, and it is worse. Promoting somebody to Admin makes
// them a PEER, and authz.CanModifyUser refuses action on a peer — so the
// promotion cannot be undone from inside the application. A stolen admin
// session today lasts as long as the session: it cannot change the password
// (that needs the old one) and the real operator can revoke it. With peer
// promotion available, the same stolen session mints a second administrator
// with its own password and its own authenticator, and revoking sessions no
// longer helps. That trades a recovery problem for a persistence problem, and
// the persistence problem is worse: the recovery problem needs bad luck, the
// persistence problem needs an attacker.
//
// So recovery lives where an operator's authority actually comes from — the
// host — rather than inside the application that may be the thing compromised.
// Using it requires a shell on the machine, the data directory, and the master
// key. That is strictly stronger authentication than anything this application
// can offer, and it adds no new reachable surface: there is no route, no
// permission and no principal that leads here.
//
// # What keeps it out of the application
//
// TestOnlyTheCommandLineCanRecoverAnAccount reads the source of every package
// and fails the build if Recover is called from anywhere but cmd/cmediastack.
// The same technique as authz.SystemPrincipal, and for the same reason: a
// runtime check cannot prove that a handler does not call this.
//
// # What it deliberately does NOT do
//
//   - It does not hand back a working session. The account returns to
//     awaiting_mfa and must enroll an authenticator through the normal flow. MFA
//     is mandatory (§13) and a recovery path that bypasses it would be the way
//     around that requirement, not an exception to it.
//   - It does not grant, change or escalate a role. An operator recovering
//     their administrator gets their administrator back; it cannot be used to
//     make one.
//   - It does not read the old secret or the old password. Neither is
//     recoverable, only replaceable.

// RecoveryResult reports what the console actually changed, so it can say so
// rather than the operator having to infer it.
type RecoveryResult struct {
	Username        string
	Role            string
	MFACleared      bool
	PasswordChanged bool
	SessionsRevoked int64
	TokensRevoked   int64
	// WasEnrolled records the state found, which is how an operator learns
	// whether they recovered the account they meant to.
	WasEnrolled   bool
	PreviousState authz.UserState
}

// ErrNoSuchAccount is returned when the named account does not exist.
//
// Unlike every other lookup in this package, this one says so plainly: there is
// no enumeration concern at a console that already has the database file open.
var ErrNoSuchAccount = errors.New("identity: no account with that username")

// Recover restores access to an account from the host console.
//
// newPassword may be empty, in which case the password is left alone — the
// common case is a lost authenticator, and replacing a password the operator
// still knows is gratuitous. When it is supplied it must satisfy the same
// policy as any other password; a recovery console is not a way to set a weak
// one.
//
// Sessions and API tokens are revoked either way, and that is not housekeeping.
// If this is being run because the account was compromised rather than merely
// locked out, leaving the attacker's session alive would make the recovery
// pointless.
func (svc *Service) Recover(ctx context.Context, username, newPassword string) (RecoveryResult, error) {
	user, err := svc.store.UserByUsername(ctx, username)
	if errors.Is(err, ErrNotFound) {
		return RecoveryResult{}, fmt.Errorf("%w: %q", ErrNoSuchAccount, username)
	}
	if err != nil {
		return RecoveryResult{}, err
	}

	res := RecoveryResult{
		Username:      user.Username,
		Role:          user.RoleName,
		WasEnrolled:   user.Enrolled(),
		PreviousState: user.State,
	}

	if newPassword != "" {
		if err := svc.policy.Password.Validate(newPassword); err != nil {
			return RecoveryResult{}, err
		}
		hash, err := HashPassword(newPassword, svc.policy.Argon2)
		if err != nil {
			return RecoveryResult{}, err
		}
		if err := svc.store.SetPassword(ctx, user.ID, hash); err != nil {
			return RecoveryResult{}, err
		}
		res.PasswordChanged = true
	}

	// Clearing the enrollment also drops the recovery codes, because they
	// belong to the enrollment being discarded. Leaving them would mean a code
	// printed years ago still opens an account whose authenticator has been
	// replaced.
	if err := svc.store.ClearTOTPEnrollment(ctx, user.ID); err != nil {
		return RecoveryResult{}, err
	}
	res.MFACleared = true

	if res.SessionsRevoked, err = svc.store.RevokeAllUserSessions(ctx, user.ID, "console_recovery"); err != nil {
		return RecoveryResult{}, err
	}
	if res.TokensRevoked, err = svc.store.RevokeAllUserTokens(ctx, user.ID, "console_recovery"); err != nil {
		return RecoveryResult{}, err
	}

	// The most security-sensitive operation this software performs, so the
	// record is written before the operator is told it worked, and a failure to
	// write it fails the whole thing. Everything else in this package treats an
	// audit failure as non-fatal; this does not. An unrecorded credential reset
	// on an administrator is indistinguishable from an attack.
	if err := svc.audit.Write(ctx, audit.Event{
		// No ActorUserID: there is no account behind this. The actor is whoever
		// had a shell on the host, and the audit log cannot know who that was —
		// saying so is more honest than attributing it to the account it acted on.
		ActorLabel: "console:recovery",
		Action:     audit.ActionAccountRecovered,
		TargetKind: "user",
		TargetID:   fmt.Sprintf("%d", user.ID),
		Detail: fmt.Sprintf("break-glass recovery of %q (role %s) from the host console",
			user.Username, user.RoleName),
		Before: map[string]any{
			"state": string(res.PreviousState), "enrolled": res.WasEnrolled,
		},
		After: map[string]any{
			"state": string(authz.StateAwaitingMFA), "enrolled": false,
			"password_changed": res.PasswordChanged,
			"sessions_revoked": res.SessionsRevoked,
			"tokens_revoked":   res.TokensRevoked,
		},
	}); err != nil {
		return RecoveryResult{}, fmt.Errorf("recovery succeeded but could not be audited, "+
			"which is not an acceptable state: %w", err)
	}

	return res, nil
}

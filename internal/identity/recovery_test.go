package identity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// ---------------------------------------------------------------------------
// The structural half
// ---------------------------------------------------------------------------

// TestOnlyTheCommandLineCanRecoverAnAccount is what makes Recover safe to
// exist.
//
// Recover takes no principal and checks no permission, because there is no
// account behind it — the authority is a shell on the host. That is exactly
// right for a console and catastrophic behind a route: one handler calling it
// would be an unauthenticated MFA reset for any named account.
//
// A runtime check cannot prove such a handler does not exist, so this reads the
// source of every package instead. Same technique as
// authz.TestOnlySchedulingCodeCanMintASystemPrincipal and
// library.TestNothingWritesOutsideAVault; this is the fifth place it is used,
// and the second time it has been used to confine authority rather than an
// effect.
func TestOnlyTheCommandLineCanRecoverAnAccount(t *testing.T) {
	// The allowlist. Adding to it should feel like a decision, because it is.
	allowed := map[string]bool{
		// The host console, which is the whole point.
		filepath.Join("..", "..", "cmd", "cmediastack", "recover.go"): true,
		// The declaration itself.
		filepath.Join("..", "..", "internal", "identity", "recovery.go"): true,
	}

	var offenders []string
	// A file this guard cannot read fails it: skipping would be exactly how an
	// offender went unseen.
	err := filepath.Walk(filepath.Join("..", ".."), func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		src := stripComments(string(body))
		// ".Recover(" rather than "Recover(" — recover() is a Go builtin and
		// every deferred panic handler in the tree uses it.
		if !strings.Contains(src, ".Recover(") && !strings.Contains(src, "func (svc *Service) Recover(") {
			return nil
		}
		if !allowed[p] {
			offenders = append(offenders, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range offenders {
		t.Errorf("%s calls Recover. Break-glass recovery resets an "+
			"authenticator with NO principal and NO permission check — behind a "+
			"route that is an unauthenticated MFA reset for any named account. "+
			"It belongs on the host console and nowhere else", o)
	}
}

// And the mirror of it: nothing may clear an enrollment except the recovery
// path. ClearTOTPEnrollment is the only write in the package that undoes MFA,
// and it has no permission check for the same reason Recover has none.
func TestOnlyRecoveryCanClearAnEnrollment(t *testing.T) {
	allowed := map[string]bool{
		filepath.Join("..", "..", "internal", "identity", "recovery.go"): true,
		filepath.Join("..", "..", "internal", "identity", "store.go"):    true,
	}

	var offenders []string
	err := filepath.Walk(filepath.Join("..", ".."), func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		body, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if strings.Contains(stripComments(string(body)), "ClearTOTPEnrollment(") && !allowed[p] {
			offenders = append(offenders, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range offenders {
		t.Errorf("%s clears an authenticator enrollment. That is the one write "+
			"that turns a two-factor account back into a one-factor one", o)
	}
}

// stripComments removes // and /* */ so that a mention in a comment — including
// the ones above, which name these functions repeatedly — is not mistaken for a
// call.
func stripComments(src string) string {
	var b strings.Builder
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case inBlock:
			if i := strings.Index(trimmed, "*/"); i >= 0 {
				inBlock = false
				b.WriteString(trimmed[i+2:])
			}
		case strings.HasPrefix(trimmed, "//"):
			// drop
		case strings.HasPrefix(trimmed, "/*"):
			if !strings.Contains(trimmed[2:], "*/") {
				inBlock = true
			}
		default:
			if i := strings.Index(line, "//"); i >= 0 {
				b.WriteString(line[:i])
			} else {
				b.WriteString(line)
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// What it does
// ---------------------------------------------------------------------------

func TestRecoveryRestoresAccessWithoutBypassingMFA(t *testing.T) {
	f := newAdminFixture(t)
	admin := f.account("jacob", authz.RoleAdmin, authz.StateActive, true)

	// Precondition: this is a working, enrolled administrator.
	if !admin.Enrolled() || admin.State != authz.StateActive {
		t.Fatalf("precondition: %s / enrolled=%v", admin.State, admin.Enrolled())
	}
	if err := f.store.StoreRecoveryCodes(t.Context(), admin.ID,
		[]string{"code-one", "code-two"}); err != nil {
		t.Fatal(err)
	}

	res, err := f.svc.Recover(t.Context(), "jacob", "")
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if !res.WasEnrolled || res.PreviousState != authz.StateActive {
		t.Errorf("the result misreports what it found: %+v", res)
	}
	if res.PasswordChanged {
		t.Error("the password was changed when none was supplied")
	}

	after, err := f.store.UserByID(t.Context(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}

	// The account can sign in again — and lands in enrollment, not in the app.
	if after.State != authz.StateAwaitingMFA {
		t.Errorf("state = %q, want awaiting_mfa: recovery must NOT produce an "+
			"account that can act without a second factor", after.State)
	}
	if after.Enrolled() {
		t.Error("the old authenticator survived recovery")
	}

	// The old recovery codes went with the old enrollment. Leaving them would
	// make a code printed years ago a permanent second credential.
	n, err := f.store.UnusedRecoveryCodeCount(t.Context(), admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d recovery codes survived the enrollment they belonged to", n)
	}

	// And the enrollment flow is actually open again, which is the thing the
	// operator needs and the thing a half-cleared row would break.
	if _, err := f.svc.BeginEnrollment(after); err != nil {
		t.Errorf("the recovered account cannot enroll: %v", err)
	}
}

func TestRecoveryCanReplaceAPasswordButNotAWeakOne(t *testing.T) {
	f := newAdminFixture(t)
	f.svc.policy.Password = PasswordPolicy{MinLength: 12}
	f.account("jacob", authz.RoleAdmin, authz.StateActive, true)

	if _, err := f.svc.Recover(t.Context(), "jacob", "short"); err == nil {
		t.Error("the recovery console accepted a password that fails policy; " +
			"a console is not a way to set a weak one")
	}

	res, err := f.svc.Recover(t.Context(), "jacob", "a-perfectly-fine-passphrase")
	if err != nil {
		t.Fatalf("recover with password: %v", err)
	}
	if !res.PasswordChanged {
		t.Fatal("the result does not report the password change")
	}

	after, err := f.store.UserByUsername(t.Context(), "jacob")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword("a-perfectly-fine-passphrase", after.PasswordHash); err != nil {
		t.Errorf("the new password does not verify: %v", err)
	}
}

// Recovery is also the answer to a compromise, not only to bad luck. Leaving an
// attacker's session or token alive would make it pointless.
func TestRecoveryRevokesEverythingTheAccountHeld(t *testing.T) {
	f := newAdminFixture(t)
	u := f.account("jacob", authz.RoleAdmin, authz.StateActive, true)

	cfg := SessionConfig{IdleTimeout: 12 * time.Hour, AbsoluteTimeout: 24 * time.Hour}
	cookie, _, err := f.store.CreateSession(t.Context(), u.ID, true, cfg, "203.0.113.10", "agent")
	if err != nil {
		t.Fatal(err)
	}
	// The session really does resolve before the recovery, or the assertion
	// below would pass whether or not anything was revoked.
	if _, _, _, err := f.store.ResolveSession(t.Context(), cookie, cfg); err != nil {
		t.Fatalf("precondition: the session does not resolve: %v", err)
	}

	res, err := f.svc.Recover(t.Context(), "jacob", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsRevoked < 1 {
		t.Errorf("sessions revoked = %d, want at least 1", res.SessionsRevoked)
	}
	if _, _, _, err := f.store.ResolveSession(t.Context(), cookie, cfg); err == nil {
		t.Error("a session survived the recovery of its account — if this was " +
			"run because the account was compromised, the attacker is still in")
	}
}

func TestRecoveringAnUnknownAccountSaysSo(t *testing.T) {
	f := newAdminFixture(t)
	f.account("jacob", authz.RoleAdmin, authz.StateActive, true)

	_, err := f.svc.Recover(t.Context(), "nobody", "")
	if err == nil {
		t.Fatal("recovering a non-existent account succeeded")
	}
	// Deliberately NOT the opaque answer the login path gives: there is no
	// enumeration concern at a console holding the database file open, and an
	// operator mistyping a username at 2am deserves to be told.
	if !strings.Contains(err.Error(), "nobody") {
		t.Errorf("the error does not name the account: %v", err)
	}
}

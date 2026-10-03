package identity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// Re-authentication (ADR-0065): a change a stolen session must not be able to
// make asks for the password and a fresh authenticator code again.

const reauthSecret = "JBSWY3DPEHPK3PXP"

// reauthAccount is an enrolled, active administrator with a real password,
// and a context carrying their signed-in session.
func reauthAccount(t *testing.T, f *adminFixture) (context.Context, *User) {
	t.Helper()
	hash, err := HashPassword("correct-horse-battery", testParams())
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.store.CreateUser(t.Context(), NewUser{
		Username: "jacob", Email: "jacob@example.com", PasswordHash: hash, RoleID: f.roles[authz.RoleAdmin].ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetTOTPSecret(t.Context(), id, reauthSecret); err != nil {
		t.Fatal(err)
	}
	u, err := f.store.UserByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.policy.LoginMaxAttempts, f.svc.policy.LoginWindow = 3, time.Hour
	ctx := authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: u.ID, Username: u.Username, State: authz.StateActive, MFASatisfied: true,
		Role: f.roles[authz.RoleAdmin],
	})
	return ctx, u
}

func codeNow(t *testing.T, f *adminFixture) string {
	t.Helper()
	code, err := TOTPCode(reauthSecret, f.svc.now())
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func TestReauthenticationNeedsBothThePasswordAndACode(t *testing.T) {
	f := newAdminFixture(t)
	ctx, _ := reauthAccount(t, f)

	if err := f.svc.Reauthenticate(ctx, "wrong-password", codeNow(t, f), "", ""); !errors.Is(err, ErrLoginFailed) {
		t.Errorf("a wrong password: %v, want ErrLoginFailed", err)
	}
	if err := f.svc.Reauthenticate(ctx, "correct-horse-battery", "000000", "", ""); !errors.Is(err, ErrLoginFailed) {
		t.Errorf("a wrong code: %v, want ErrLoginFailed", err)
	}
	if err := f.svc.Reauthenticate(t.Context(), "correct-horse-battery", codeNow(t, f), "", ""); !errors.Is(err, ErrLoginFailed) {
		t.Errorf("no session: %v, want ErrLoginFailed", err)
	}
	if err := f.svc.Reauthenticate(ctx, "correct-horse-battery", codeNow(t, f), "", ""); err != nil {
		t.Errorf("the right password and code: %v", err)
	}
}

func TestReauthenticationRefusesAReplayedCodeAndRecoveryCodes(t *testing.T) {
	f := newAdminFixture(t)
	ctx, u := reauthAccount(t, f)

	code := codeNow(t, f)
	if err := f.svc.Reauthenticate(ctx, "correct-horse-battery", code, "", ""); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if err := f.svc.Reauthenticate(ctx, "correct-horse-battery", code, "", ""); !errors.Is(err, ErrLoginFailed) {
		t.Errorf("the same code again in its step: %v, want ErrLoginFailed", err)
	}

	if err := f.store.StoreRecoveryCodes(t.Context(), u.ID, []string{"AAAAA-BBBBB"}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Reauthenticate(ctx, "correct-horse-battery", "AAAAA-BBBBB", "", ""); !errors.Is(err, ErrLoginFailed) {
		t.Errorf("a recovery code: %v, want ErrLoginFailed", err)
	}
	if n, _ := f.store.UnusedRecoveryCodeCount(t.Context(), u.ID); n != 1 {
		t.Errorf("%d recovery codes unused, want 1: offering one must not spend it", n)
	}
}

func TestReauthenticationFailuresCountTowardLockout(t *testing.T) {
	f := newAdminFixture(t)
	ctx, u := reauthAccount(t, f)

	before, err := f.svc.audit.LastID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.svc.Reauthenticate(ctx, "correct-horse-battery", "000000", "", "")
	_ = f.svc.Reauthenticate(ctx, "wrong-password", codeNow(t, f), "", "")
	records, err := f.svc.audit.After(ctx, before, 10)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[audit.Action]bool{}
	for _, r := range records {
		if r.Outcome == audit.OutcomeFailure && r.ActorUserID != nil && *r.ActorUserID == u.ID {
			seen[r.Action] = true
		}
	}
	if !seen[audit.ActionMFAFailed] || !seen[audit.ActionLoginFailed] {
		t.Errorf("audited failures %v, want a refused code and a refused password", seen)
	}

	for range 3 {
		_ = f.svc.Reauthenticate(ctx, "wrong-password", codeNow(t, f), "", "")
	}
	if err := f.svc.Reauthenticate(ctx, "correct-horse-battery", codeNow(t, f), "", ""); !errors.Is(err, ErrThrottled) {
		t.Errorf("after the attempts are spent: %v, want ErrThrottled", err)
	}
}

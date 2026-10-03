package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identity"
)

// End-to-end tests for increment 1c: invites, session management, password
// reset and self-service credentials.

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (c *client) del(path string) response { return c.do(http.MethodDelete, path, nil) }

func (r *rig) roleID(name string) int64 {
	r.t.Helper()
	role, err := r.store.RoleByName(r.t.Context(), name)
	if err != nil {
		r.t.Fatal(err)
	}
	return role.ID
}

// issueInvite has the given client mint an auto-approving invite.
func (r *rig) issueInvite(as *client, roleName string, autoApprove bool) (code string, id int64) {
	r.t.Helper()
	res := as.post("/api/v1/invites", map[string]any{
		"role_id": r.roleID(roleName), "library_ids": []int64{},
		"rating_ceiling": 0, "auto_approve": autoApprove, "ttl_hours": 168,
	})
	if res.Code != http.StatusCreated {
		r.t.Fatalf("issue invite: %d %s", res.Code, res.Raw)
	}
	code, _ = res.Body["code"].(string)
	if code == "" {
		r.t.Fatalf("no code returned: %s", res.Raw)
	}
	return code, int64(res.Body["id"].(float64))
}

// signUpAndEnroll redeems an invite and completes enrollment, returning a
// signed-in client.
func (r *rig) redeemAndEnroll(code, username, password string) *client {
	r.t.Helper()
	c := r.client()
	c.visitPage("/signup")

	res := c.post("/api/v1/auth/signup", map[string]any{
		"username": username, "email": username + "@example.com",
		"password": password, "invite_code": code,
	})
	if res.Code != http.StatusCreated || res.Body["status"] != "approved" {
		r.t.Fatalf("redeem invite: %d %s", res.Code, res.Raw)
	}

	c.visitPage("/login")
	if res := c.post("/api/v1/auth/login", map[string]any{
		"username": username, "password": password,
	}); res.Code != http.StatusOK {
		r.t.Fatalf("login after redeem: %d %s", res.Code, res.Raw)
	}
	enroll := c.get("/api/v1/auth/mfa/enroll")
	secret, _ := enroll.Body["secret"].(string)
	if res := c.post("/api/v1/auth/mfa/enroll/confirm", map[string]any{
		"secret": secret, "code": r.mustCode(secret),
	}); res.Code != http.StatusOK {
		r.t.Fatalf("enroll after redeem: %d %s", res.Code, res.Raw)
	}
	return c
}

// ---------------------------------------------------------------------------
// Invites
// ---------------------------------------------------------------------------

func TestInviteRedemptionCreatesAnAccountDirectly(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	friend := r.redeemAndEnroll(code, "friend", "invited-passphrase-1")

	res := friend.get("/api/v1/me")
	if res.Code != http.StatusOK {
		t.Fatalf("invited user cannot read own profile: %d %s", res.Code, res.Raw)
	}
	if res.Body["role"] != authz.RoleUser {
		t.Errorf("role = %v, want User", res.Body["role"])
	}

	// The invite pre-approved a User, and User really is limited.
	if res := friend.get("/api/v1/admin/users"); res.Code != http.StatusNotFound {
		t.Errorf("invited user reached an admin route: %d", res.Code)
	}

	// No pending request was created: an auto-approving invite skips the queue.
	if n, err := r.store.CountPendingRequests(t.Context()); err != nil || n != 0 {
		t.Errorf("pending requests = %d (err %v), want 0", n, err)
	}
}

func TestInviteIsSingleUse(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)

	r.redeemAndEnroll(code, "first", "invited-passphrase-1")

	second := r.client()
	second.visitPage("/signup")
	res := second.post("/api/v1/auth/signup", map[string]any{
		"username": "second", "email": "second@example.com",
		"password": "invited-passphrase-2", "invite_code": code,
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("an invite was redeemed twice: %d %s (THE ATTACK SUCCEEDED)", res.Code, res.Raw)
	}
}

func TestExpiredAndRevokedInvitesAreRefused(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	// Both invites are issued before the clock moves: advancing past the
	// invite TTL also passes the admin's own session idle timeout, so anything
	// needing the admin has to happen first.
	expiring, _ := r.issueInvite(admin, authz.RoleUser, true)
	revoked, id := r.issueInvite(admin, authz.RoleUser, true)

	// Revocation takes effect immediately.
	if res := admin.del("/api/v1/invites/" + itoa(int(id))); res.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", res.Code, res.Raw)
	}
	c := r.client()
	c.visitPage("/signup")
	if res := c.post("/api/v1/auth/signup", map[string]any{
		"username": "revoked", "email": "revoked@example.com",
		"password": "invited-passphrase-4", "invite_code": revoked,
	}); res.Code != http.StatusBadRequest {
		t.Errorf("a revoked invite was accepted: %d %s", res.Code, res.Raw)
	}

	// Now expire the other one.
	r.clk.advance(169 * time.Hour) // ttl_hours was 168
	c2 := r.client()
	c2.visitPage("/signup")
	if res := c2.post("/api/v1/auth/signup", map[string]any{
		"username": "late", "email": "late@example.com",
		"password": "invited-passphrase-3", "invite_code": expiring,
	}); res.Code != http.StatusBadRequest {
		t.Errorf("an expired invite was accepted: %d %s", res.Code, res.Raw)
	}
}

// An invite is an approval made in advance, so the escalation guard that
// governs approval must govern issuance too.
func TestManagerCannotIssueAnAdminInvite(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	mgrCode, _ := r.issueInvite(admin, authz.RoleManager, true)
	mgr := r.redeemAndEnroll(mgrCode, "manager", "manager-passphrase-x")

	for _, roleName := range []string{authz.RoleAdmin, authz.RoleManager} {
		res := mgr.post("/api/v1/invites", map[string]any{
			"role_id": r.roleID(roleName), "library_ids": []int64{},
			"rating_ceiling": 0, "auto_approve": true, "ttl_hours": 24,
		})
		if res.Code != http.StatusNotFound {
			t.Errorf("MANAGER ISSUED A %s INVITE: %d %s (THE ATTACK SUCCEEDED)", roleName, res.Code, res.Raw)
		}
	}

	// Issuing a User invite still works, so the refusals are not blanket.
	if res := mgr.post("/api/v1/invites", map[string]any{
		"role_id": r.roleID(authz.RoleUser), "library_ids": []int64{},
		"rating_ceiling": 0, "auto_approve": true, "ttl_hours": 24,
	}); res.Code != http.StatusCreated {
		t.Errorf("a Manager could not issue a User invite: %d %s", res.Code, res.Raw)
	}
}

// An invite carries the issuer's authority. If that authority is withdrawn,
// the outstanding grants go with it.
func TestSuspendingAnIssuerRevokesTheirOutstandingInvites(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	mgrCode, _ := r.issueInvite(admin, authz.RoleManager, true)
	mgr := r.redeemAndEnroll(mgrCode, "manager", "manager-passphrase-x")

	// The Manager issues an invite of their own, then is suspended.
	pending, _ := r.issueInvite(mgr, authz.RoleUser, true)

	mgrUser, err := r.store.UserByUsername(t.Context(), "manager")
	if err != nil {
		t.Fatal(err)
	}
	if res := admin.post("/api/v1/admin/users/"+itoa(int(mgrUser.ID))+"/suspend",
		map[string]any{"reason": "testing"}); res.Code != http.StatusOK {
		t.Fatalf("suspend: %d %s", res.Code, res.Raw)
	}

	c := r.client()
	c.visitPage("/signup")
	res := c.post("/api/v1/auth/signup", map[string]any{
		"username": "orphan", "email": "orphan@example.com",
		"password": "invited-passphrase-5", "invite_code": pending,
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("an invite issued by a suspended Manager still worked: %d %s", res.Code, res.Raw)
	}
}

// Codes are stored hashed, so the management view cannot show them even if
// somebody asked it to.
func TestInviteListNeverContainsCodes(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)

	res := admin.get("/api/v1/invites")
	if res.Code != http.StatusOK {
		t.Fatalf("list invites: %d %s", res.Code, res.Raw)
	}
	if strings.Contains(res.Raw, code) {
		t.Fatalf("the invite list leaked a code: %s", res.Raw)
	}
	if !strings.Contains(res.Raw, "active") {
		t.Errorf("expected an active invite in the list: %s", res.Raw)
	}
}

// In invite-only mode the signup endpoint gives an anonymous caller with no
// code exactly the same answer as a route that does not exist.
func TestInviteOnlyModeRefusesUncodedSignup(t *testing.T) {
	r := newRigWith(t, func(p *identity.Policy) { p.RegistrationMode = "invite" })
	admin := r.bootstrapAdmin()

	c := r.client()
	c.visitPage("/signup")
	res := c.post("/api/v1/auth/signup", map[string]any{
		"username": "nocode", "email": "nocode@example.com", "password": "some-passphrase-x",
	})
	if res.Code != http.StatusNotFound {
		t.Errorf("invite-only signup without a code returned %d, want 404", res.Code)
	}

	// With a code it works.
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	r.redeemAndEnroll(code, "invited", "invited-passphrase-6")
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

func TestUserSeesOnlyTheirOwnSessions(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	friend := r.redeemAndEnroll(code, "friend", "invited-passphrase-1")

	res := friend.get("/api/v1/me/sessions")
	if res.Code != http.StatusOK {
		t.Fatalf("session list: %d %s", res.Code, res.Raw)
	}
	sessions, _ := res.Body["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("friend sees %d sessions, want 1 (their own)", len(sessions))
	}
	if first := sessions[0].(map[string]any); first["current"] != true {
		t.Errorf("the only session should be marked current: %v", first)
	}

	// The admin's own list is separate and does not include the friend's.
	res = admin.get("/api/v1/me/sessions")
	adminSessions, _ := res.Body["sessions"].([]any)
	if len(adminSessions) != 1 {
		t.Errorf("admin sees %d sessions, want 1", len(adminSessions))
	}
}

// The object-level check that matters: session IDs are opaque, but "opaque" is
// not an authorization control. User A must not be able to revoke user B's
// session even holding B's session ID.
func TestUserCannotRevokeAnotherUsersSession(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	codeA, _ := r.issueInvite(admin, authz.RoleUser, true)
	alice := r.redeemAndEnroll(codeA, "alice", "alice-passphrase-99")
	codeB, _ := r.issueInvite(admin, authz.RoleUser, true)
	bob := r.redeemAndEnroll(codeB, "bob", "bob-passphrase-1234")

	// Alice learns Bob's session ID (assume the worst).
	bobList := bob.get("/api/v1/me/sessions")
	bobSessions, _ := bobList.Body["sessions"].([]any)
	bobSessionID := bobSessions[0].(map[string]any)["id"].(string)

	res := alice.del("/api/v1/me/sessions/" + bobSessionID)
	if res.Code != http.StatusNotFound {
		t.Fatalf("ALICE REVOKED BOB'S SESSION: %d %s (THE ATTACK SUCCEEDED)", res.Code, res.Raw)
	}

	// Bob is unaffected.
	if res := bob.get("/api/v1/me"); res.Code != http.StatusOK {
		t.Errorf("bob's session was killed by another user: %d", res.Code)
	}

	// An admin cannot do it through this endpoint either — it is self-service
	// only, and takes the caller's own user ID, never one from the request.
	if res := admin.del("/api/v1/me/sessions/" + bobSessionID); res.Code != http.StatusNotFound {
		t.Errorf("an admin revoked another user's session via the self-service route: %d", res.Code)
	}
	if res := bob.get("/api/v1/me"); res.Code != http.StatusOK {
		t.Errorf("bob's session was killed via the admin's self-service route: %d", res.Code)
	}
}

func TestUserCanRevokeTheirOwnOtherSession(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	pass := "invited-passphrase-1"
	first := r.redeemAndEnroll(code, "friend", pass)

	// A second sign-in from another device.
	second := r.client()
	second.ip = "198.51.100.77"
	second.visitPage("/login")
	second.post("/api/v1/auth/login", map[string]any{"username": "friend", "password": pass})
	user, _ := r.store.UserByUsername(t.Context(), "friend")
	secretSess, err := r.store.UserSessions(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(secretSess) != 2 {
		t.Fatalf("expected 2 live sessions, got %d", len(secretSess))
	}

	// From the first device, find and revoke the other one.
	list := first.get("/api/v1/me/sessions")
	var otherID string
	for _, raw := range list.Body["sessions"].([]any) {
		s := raw.(map[string]any)
		if s["current"] != true {
			otherID = s["id"].(string)
		}
	}
	if otherID == "" {
		t.Fatal("could not find the other session")
	}

	res := first.del("/api/v1/me/sessions/" + otherID)
	if res.Code != http.StatusOK {
		t.Fatalf("revoking own session: %d %s", res.Code, res.Raw)
	}
	if res.Body["was_current"] != false {
		t.Errorf("was_current = %v, want false", res.Body["was_current"])
	}

	// The revoked device is out; the current one still works.
	if res := second.get("/api/v1/me"); res.Code != http.StatusNotFound {
		t.Errorf("a revoked session still worked: %d", res.Code)
	}
	if res := first.get("/api/v1/me"); res.Code != http.StatusOK {
		t.Errorf("the revoking session was killed: %d", res.Code)
	}
}

// ---------------------------------------------------------------------------
// Password reset
// ---------------------------------------------------------------------------

// The reset endpoint must not become the enumeration oracle the signup form
// was carefully built not to be.
func TestResetInitiateIsNotAnEnumerationOracle(t *testing.T) {
	r := newRig(t)
	r.bootstrapAdmin()

	c := r.client()
	c.visitPage("/reset")

	known := c.post("/api/v1/auth/reset/initiate", map[string]any{"username": "jacob"})
	unknown := c.post("/api/v1/auth/reset/initiate", map[string]any{"username": "nobody-at-all"})

	if known.Code != unknown.Code {
		t.Errorf("status differs by account existence: %d vs %d", known.Code, unknown.Code)
	}
	if known.Raw != unknown.Raw {
		t.Errorf("body differs by account existence:\n  known: %s\nunknown: %s", known.Raw, unknown.Raw)
	}
	// And it must not admit that delivery is unimplemented, which would itself
	// be a signal.
	if strings.Contains(strings.ToLower(known.Raw), "transport") ||
		strings.Contains(strings.ToLower(known.Raw), "not configured") {
		t.Errorf("the response leaked delivery state: %s", known.Raw)
	}
}

func TestAdminMintedResetChangesThePassword(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	friend := r.redeemAndEnroll(code, "friend", "invited-passphrase-1")

	user, _ := r.store.UserByUsername(t.Context(), "friend")
	res := admin.post("/api/v1/admin/users/"+itoa(int(user.ID))+"/reset-link", nil)
	if res.Code != http.StatusCreated {
		t.Fatalf("mint reset link: %d %s", res.Code, res.Raw)
	}
	token, _ := res.Body["token"].(string)
	if token == "" {
		t.Fatal("no token returned")
	}

	// Completing the reset revokes every session, including the live one.
	anon := r.client()
	anon.visitPage("/reset")
	res = anon.post("/api/v1/auth/reset/complete", map[string]any{
		"token": token, "password": "brand-new-passphrase",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("complete reset: %d %s", res.Code, res.Raw)
	}
	if res := friend.get("/api/v1/me"); res.Code != http.StatusNotFound {
		t.Errorf("a live session survived a password reset: %d", res.Code)
	}

	// The old password no longer works; the new one does.
	c := r.client()
	c.visitPage("/login")
	if res := c.post("/api/v1/auth/login", map[string]any{
		"username": "friend", "password": "invited-passphrase-1",
	}); res.Code != http.StatusUnauthorized {
		t.Errorf("the old password still works: %d", res.Code)
	}
	if res := c.post("/api/v1/auth/login", map[string]any{
		"username": "friend", "password": "brand-new-passphrase",
	}); res.Code != http.StatusOK {
		t.Fatalf("the new password does not work: %d %s", res.Code, res.Raw)
	}
}

// A reset recovers a forgotten password. It is not a way around the
// authenticator.
func TestResetDoesNotBypassMFA(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	r.redeemAndEnroll(code, "friend", "invited-passphrase-1")

	user, _ := r.store.UserByUsername(t.Context(), "friend")
	minted := admin.post("/api/v1/admin/users/"+itoa(int(user.ID))+"/reset-link", nil)
	token, _ := minted.Body["token"].(string)

	anon := r.client()
	anon.visitPage("/reset")
	anon.post("/api/v1/auth/reset/complete", map[string]any{
		"token": token, "password": "brand-new-passphrase",
	})

	// The reset itself must not have signed anyone in.
	if res := anon.get("/api/v1/me"); res.Code != http.StatusNotFound {
		t.Errorf("completing a reset produced a usable session: %d %s", res.Code, res.Raw)
	}

	// Logging in with the new password lands on the second factor, not the app.
	c := r.client()
	c.visitPage("/login")
	res := c.post("/api/v1/auth/login", map[string]any{
		"username": "friend", "password": "brand-new-passphrase",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("login: %d %s", res.Code, res.Raw)
	}
	if res.Body["next"] != "mfa" {
		t.Errorf("next = %v, want mfa: a reset must not clear the authenticator", res.Body["next"])
	}
	if res := c.get("/api/v1/me"); res.Code != http.StatusNotImplemented && res.Code == http.StatusOK {
		t.Errorf("a post-reset session reached the app without a second factor: %d", res.Code)
	}
}

func TestResetTokenIsSingleUseAndExpires(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	r.redeemAndEnroll(code, "friend", "invited-passphrase-1")
	user, _ := r.store.UserByUsername(t.Context(), "friend")

	// Single use.
	minted := admin.post("/api/v1/admin/users/"+itoa(int(user.ID))+"/reset-link", nil)
	token, _ := minted.Body["token"].(string)

	anon := r.client()
	anon.visitPage("/reset")
	if res := anon.post("/api/v1/auth/reset/complete", map[string]any{
		"token": token, "password": "first-new-passphrase",
	}); res.Code != http.StatusOK {
		t.Fatalf("first use: %d %s", res.Code, res.Raw)
	}
	if res := anon.post("/api/v1/auth/reset/complete", map[string]any{
		"token": token, "password": "second-new-passphrase",
	}); res.Code != http.StatusBadRequest {
		t.Fatalf("A RESET TOKEN WAS REUSED: %d %s (THE ATTACK SUCCEEDED)", res.Code, res.Raw)
	}

	// Expiry.
	minted = admin.post("/api/v1/admin/users/"+itoa(int(user.ID))+"/reset-link", nil)
	token2, _ := minted.Body["token"].(string)
	r.clk.advance(31 * time.Minute) // ResetTTL is 30 minutes
	if res := anon.post("/api/v1/auth/reset/complete", map[string]any{
		"token": token2, "password": "third-new-passphrase",
	}); res.Code != http.StatusBadRequest {
		t.Errorf("an expired reset token was accepted: %d %s", res.Code, res.Raw)
	}
}

// Minting a reset for another account is an administrative act, so the
// escalation guard applies.
func TestManagerCannotMintAResetForAnAdmin(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	mgrCode, _ := r.issueInvite(admin, authz.RoleManager, true)
	mgr := r.redeemAndEnroll(mgrCode, "manager", "manager-passphrase-x")

	adminUser, _ := r.store.UserByUsername(t.Context(), "jacob")
	res := mgr.post("/api/v1/admin/users/"+itoa(int(adminUser.ID))+"/reset-link", nil)
	if res.Code != http.StatusNotFound {
		t.Fatalf("MANAGER MINTED A RESET FOR AN ADMIN: %d %s (THE ATTACK SUCCEEDED)", res.Code, res.Raw)
	}
}

// ---------------------------------------------------------------------------
// Self-service credentials
// ---------------------------------------------------------------------------

func TestChangePasswordRequiresTheCurrentOne(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	if res := admin.post("/api/v1/me/password", map[string]any{
		"current_password": "not-the-right-password", "new_password": "a-replacement-passphrase",
	}); res.Code != http.StatusUnauthorized {
		t.Fatalf("changed a password without the current one: %d %s", res.Code, res.Raw)
	}

	if res := admin.post("/api/v1/me/password", map[string]any{
		"current_password": "correct-horse-battery", "new_password": "a-replacement-passphrase",
	}); res.Code != http.StatusOK {
		t.Fatalf("change password: %d %s", res.Code, res.Raw)
	}

	// The old password is dead.
	c := r.client()
	c.visitPage("/login")
	if res := c.post("/api/v1/auth/login", map[string]any{
		"username": "jacob", "password": "correct-horse-battery",
	}); res.Code != http.StatusUnauthorized {
		t.Errorf("the old password still works: %d", res.Code)
	}
}

// Changing a password kills other sessions but not the one doing the change.
func TestChangePasswordRevokesOtherSessionsOnly(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	other := r.client()
	other.ip = "198.51.100.5"
	other.visitPage("/login")
	other.post("/api/v1/auth/login", map[string]any{
		"username": "jacob", "password": "correct-horse-battery",
	})

	if res := admin.post("/api/v1/me/password", map[string]any{
		"current_password": "correct-horse-battery", "new_password": "a-replacement-passphrase",
	}); res.Code != http.StatusOK {
		t.Fatalf("change password: %d %s", res.Code, res.Raw)
	}

	if res := admin.get("/api/v1/me"); res.Code != http.StatusOK {
		t.Errorf("the session that changed the password was revoked: %d", res.Code)
	}
	if res := other.get("/api/v1/auth/mfa/enroll"); res.Code == http.StatusOK {
		t.Errorf("another session survived a password change: %d", res.Code)
	}
}

func TestRecoveryCodeRegenerationRequiresThePassword(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	if res := admin.post("/api/v1/me/mfa/recovery-codes", map[string]any{
		"current_password": "wrong-password-here",
	}); res.Code != http.StatusUnauthorized {
		t.Fatalf("regenerated recovery codes without the password: %d %s", res.Code, res.Raw)
	}

	res := admin.post("/api/v1/me/mfa/recovery-codes", map[string]any{
		"current_password": "correct-horse-battery",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("regenerate: %d %s", res.Code, res.Raw)
	}
	codes, _ := res.Body["recovery_codes"].([]any)
	if len(codes) != 10 {
		t.Errorf("got %d codes, want 10", len(codes))
	}
}

// A regenerated set must invalidate the previous one, or a leaked old code
// stays a permanent second factor.
func TestRegeneratingRecoveryCodesInvalidatesTheOldSet(t *testing.T) {
	r := newRig(t)
	c := r.client()
	c.visitPage("/setup")
	c.post("/api/v1/setup", map[string]any{
		"username": "jacob", "email": "j@example.com", "password": "correct-horse-battery",
	})
	c.visitPage("/login")
	c.post("/api/v1/auth/login", map[string]any{"username": "jacob", "password": "correct-horse-battery"})
	enroll := c.get("/api/v1/auth/mfa/enroll")
	secret, _ := enroll.Body["secret"].(string)
	confirm := c.post("/api/v1/auth/mfa/enroll/confirm", map[string]any{
		"secret": secret, "code": r.mustCode(secret),
	})
	oldCodes, _ := confirm.Body["recovery_codes"].([]any)
	oldCode := oldCodes[0].(string)

	if res := c.post("/api/v1/me/mfa/recovery-codes", map[string]any{
		"current_password": "correct-horse-battery",
	}); res.Code != http.StatusOK {
		t.Fatalf("regenerate: %d %s", res.Code, res.Raw)
	}

	// A fresh sign-in, presenting a code from the retired set.
	fresh := r.client()
	fresh.visitPage("/login")
	fresh.post("/api/v1/auth/login", map[string]any{"username": "jacob", "password": "correct-horse-battery"})
	if res := fresh.post("/api/v1/auth/login/mfa", map[string]any{"code": oldCode}); res.Code != http.StatusUnauthorized {
		t.Fatalf("A RETIRED RECOVERY CODE STILL WORKED: %d %s (THE ATTACK SUCCEEDED)", res.Code, res.Raw)
	}
}

// A recovery code is single-use.
func TestRecoveryCodeIsSingleUse(t *testing.T) {
	r := newRig(t)
	c := r.client()
	c.visitPage("/setup")
	c.post("/api/v1/setup", map[string]any{
		"username": "jacob", "email": "j@example.com", "password": "correct-horse-battery",
	})
	c.visitPage("/login")
	c.post("/api/v1/auth/login", map[string]any{"username": "jacob", "password": "correct-horse-battery"})
	enroll := c.get("/api/v1/auth/mfa/enroll")
	secret, _ := enroll.Body["secret"].(string)
	confirm := c.post("/api/v1/auth/mfa/enroll/confirm", map[string]any{
		"secret": secret, "code": r.mustCode(secret),
	})
	codes, _ := confirm.Body["recovery_codes"].([]any)
	code := codes[0].(string)

	first := r.client()
	first.visitPage("/login")
	first.post("/api/v1/auth/login", map[string]any{"username": "jacob", "password": "correct-horse-battery"})
	if res := first.post("/api/v1/auth/login/mfa", map[string]any{"code": code}); res.Code != http.StatusOK {
		t.Fatalf("a recovery code was refused on first use: %d %s", res.Code, res.Raw)
	}

	second := r.client()
	second.visitPage("/login")
	second.post("/api/v1/auth/login", map[string]any{"username": "jacob", "password": "correct-horse-battery"})
	if res := second.post("/api/v1/auth/login/mfa", map[string]any{"code": code}); res.Code != http.StatusUnauthorized {
		t.Fatalf("A RECOVERY CODE WAS REUSED: %d %s (THE ATTACK SUCCEEDED)", res.Code, res.Raw)
	}
}

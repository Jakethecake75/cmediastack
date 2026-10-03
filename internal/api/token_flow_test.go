package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identity"
)

// End-to-end tests for scoped API tokens.
//
// The properties that matter: a token can never exceed its issuer, it narrows
// when its issuer is demoted, it dies when its issuer is suspended, and it can
// never touch credential management — because a token lives in a script or a
// config file, and a leaked one must be a scoped, revocable grant rather than a
// permanent account takeover.

// bearer returns a client that authenticates with a token instead of a cookie.
func (r *rig) bearer(token string) *client {
	c := r.client()
	c.bearer = token
	return c
}

func (r *rig) mintToken(as *client, name string, perms ...authz.Permission) (string, int64) {
	r.t.Helper()
	list := make([]string, len(perms))
	for i, p := range perms {
		list[i] = string(p)
	}
	res := as.post("/api/v1/me/tokens", map[string]any{
		"name": name, "permissions": list, "ttl_days": 30,
	})
	if res.Code != http.StatusCreated {
		r.t.Fatalf("issue token: %d %s", res.Code, res.Raw)
	}
	tok, _ := res.Body["token"].(string)
	if !strings.HasPrefix(tok, identity.TokenPrefix) {
		r.t.Fatalf("token has no recognisable prefix: %q", tok)
	}
	return tok, int64(res.Body["id"].(float64))
}

// ---------------------------------------------------------------------------
// Issuance and use
// ---------------------------------------------------------------------------

func TestAPITokenAuthenticatesWithinItsScope(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	token, _ := r.mintToken(admin, "read-only", authz.PermBrowse, authz.PermStream)
	tc := r.bearer(token)

	// Inside scope: reaches the handler, which searches the library.
	if res := tc.get("/api/v1/search?q=dune"); res.Code != http.StatusOK {
		t.Errorf("in-scope route returned %d, want 200 (authorized)", res.Code)
	}
	if res := tc.get("/api/v1/me"); res.Code != http.StatusOK {
		t.Fatalf("token could not read its own profile: %d %s", res.Code, res.Raw)
	}

	// Outside scope: refused, even though the ISSUER is an Admin who holds
	// these permissions. The token's scope is the ceiling, not the user's.
	for _, path := range []string{"/api/v1/admin/audit", "/api/v1/admin/indexers", "/api/v1/accounts/requests"} {
		if res := tc.get(path); res.Code == http.StatusNotImplemented || res.Code == http.StatusOK {
			t.Errorf("TOKEN REACHED OUT-OF-SCOPE %s: %d (THE ATTACK SUCCEEDED)", path, res.Code)
		}
	}
}

// A token can never be a privilege escalation.
func TestTokenScopeCannotExceedTheIssuer(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	user := r.redeemAndEnroll(code, "friend", "invited-passphrase-1")

	for _, perm := range []authz.Permission{
		authz.PermSystemSettings, authz.PermManageUsers, authz.PermDeleteMediaFiles,
		authz.PermManageIndexers, authz.PermViewAuditLog,
	} {
		res := user.post("/api/v1/me/tokens", map[string]any{
			"name": "escalate", "permissions": []string{string(perm)}, "ttl_days": 30,
		})
		if res.Code != http.StatusForbidden {
			t.Errorf("A USER MINTED A TOKEN WITH %q: %d %s (THE ATTACK SUCCEEDED)", perm, res.Code, res.Raw)
		}
	}

	// Mixing a held permission with an unheld one is still refused whole.
	if res := user.post("/api/v1/me/tokens", map[string]any{
		"name": "mixed", "permissions": []string{string(authz.PermBrowse), string(authz.PermManageUsers)},
		"ttl_days": 30,
	}); res.Code != http.StatusForbidden {
		t.Errorf("a partially-over-scoped token was issued: %d %s", res.Code, res.Raw)
	}

	// A token within scope works, so the refusals are not blanket.
	if res := user.post("/api/v1/me/tokens", map[string]any{
		"name": "fine", "permissions": []string{string(authz.PermBrowse)}, "ttl_days": 30,
	}); res.Code != http.StatusCreated {
		t.Errorf("an in-scope token was refused: %d %s", res.Code, res.Raw)
	}
}

// A token's scope is intersected with the user's CURRENT role on every use, so
// demoting someone narrows every token they hold without anyone having to
// remember to revoke them.
func TestTokenNarrowsWhenItsOwnerIsDemoted(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	token, _ := r.mintToken(admin, "wide", authz.PermViewAuditLog, authz.PermBrowse)
	tc := r.bearer(token)

	if res := tc.get("/api/v1/admin/audit"); !reachable(res) {
		t.Fatalf("admin token could not reach the audit log: %d %s", res.Code, res.Raw)
	}

	// Demote the owner to User, who does not hold PermViewAuditLog.
	adminUser, err := r.store.UserByUsername(t.Context(), "jacob")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetUserRole(t.Context(), adminUser.ID, r.roleID(authz.RoleUser)); err != nil {
		t.Fatal(err)
	}

	// The token was never touched, but it is now narrower.
	if res := tc.get("/api/v1/admin/audit"); res.Code != http.StatusNotFound {
		t.Fatalf("A TOKEN OUTLIVED ITS OWNER'S PERMISSIONS: %d %s (THE ATTACK SUCCEEDED)", res.Code, res.Raw)
	}
	// What the demoted user still holds still works.
	if res := tc.get("/api/v1/media"); !reachable(res) {
		t.Errorf("a still-held permission stopped working: %d %s", res.Code, res.Raw)
	}
}

// reachable reports whether a request got PAST authorization to a handler.
//
// Asserted as "not denied" rather than as one exact status, deliberately. These
// tests use a browse endpoint as a probe for whether a credential still works,
// and pinning the probe to the status its handler happened to return — 501
// while it was a stub — makes every one of them fail the day that handler is
// implemented. The property under test is "the credential was accepted", and
// 401, 403 and 404 are the three ways this surface says it was not.
func reachable(res response) bool {
	switch res.Code {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// Credential management is session-only
// ---------------------------------------------------------------------------

// A token must not be able to change the password, replace the second factor,
// mint further tokens, or manage sessions. Otherwise one leaked token is a
// permanent takeover instead of a revocable grant.
func TestTokenCannotReachCredentialManagement(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	// Deliberately the widest possible token: every permission its Admin owner
	// holds. Scope is not the control here — the route class is.
	token, _ := r.mintToken(admin, "everything", authz.AllPermissions...)
	tc := r.bearer(token)

	cases := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/v1/me/password", map[string]any{
			"current_password": "correct-horse-battery", "new_password": "token-set-this-passphrase"}},
		{http.MethodPost, "/api/v1/me/mfa/recovery-codes", map[string]any{
			"current_password": "correct-horse-battery"}},
		{http.MethodPost, "/api/v1/me/tokens", map[string]any{
			"name": "child", "permissions": []string{string(authz.PermBrowse)}, "ttl_days": 1}},
		{http.MethodGet, "/api/v1/me/tokens", nil},
		{http.MethodGet, "/api/v1/me/sessions", nil},
		{http.MethodDelete, "/api/v1/me/sessions/whatever", nil},
		{http.MethodGet, "/api/v1/auth/mfa/enroll", nil},
		{http.MethodPost, "/api/v1/auth/mfa/enroll/confirm", map[string]any{"secret": "x", "code": "123456"}},
		{http.MethodPost, "/api/v1/auth/logout", nil},
	}

	for _, tc2 := range cases {
		res := tc.do(tc2.method, tc2.path, tc2.body)
		if res.Code == http.StatusOK || res.Code == http.StatusCreated {
			t.Errorf("A TOKEN REACHED %s %s: %d %s (THE ATTACK SUCCEEDED)",
				tc2.method, tc2.path, res.Code, res.Raw)
		}
	}

	// The password is unchanged: the attempt above did nothing.
	fresh := r.client()
	fresh.visitPage("/login")
	if res := fresh.post("/api/v1/auth/login", map[string]any{
		"username": "jacob", "password": "correct-horse-battery",
	}); res.Code != http.StatusOK {
		t.Errorf("the original password stopped working: %d %s", res.Code, res.Raw)
	}

	// The same routes still work from the interactive session.
	if res := admin.get("/api/v1/me/sessions"); res.Code != http.StatusOK {
		t.Errorf("the session was denied its own session list: %d %s", res.Code, res.Raw)
	}
}

// ---------------------------------------------------------------------------
// Revocation and expiry
// ---------------------------------------------------------------------------

func TestRevokedTokenStopsImmediately(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	token, id := r.mintToken(admin, "temp", authz.PermBrowse)
	tc := r.bearer(token)
	if res := tc.get("/api/v1/media"); !reachable(res) {
		t.Fatalf("token did not work before revocation: %d %s", res.Code, res.Raw)
	}

	if res := admin.del("/api/v1/me/tokens/" + itoa(int(id))); res.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", res.Code, res.Raw)
	}

	// No clock advance: revocation is immediate.
	if res := tc.get("/api/v1/media"); res.Code != http.StatusNotFound {
		t.Fatalf("A REVOKED TOKEN STILL WORKED: %d %s (THE ATTACK SUCCEEDED)", res.Code, res.Raw)
	}
}

func TestExpiredTokenIsRefused(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	token, _ := r.mintToken(admin, "shortlived", authz.PermBrowse)
	tc := r.bearer(token)

	r.clk.advance(31 * 24 * time.Hour) // ttl_days was 30
	if res := tc.get("/api/v1/media"); res.Code != http.StatusNotFound {
		t.Fatalf("an expired token was accepted: %d %s", res.Code, res.Raw)
	}
}

// §7.2: suspension revokes "all sessions, API tokens, and in-flight playback
// sessions". Tokens are the part most easily forgotten.
func TestSuspensionKillsAPITokens(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	friend := r.redeemAndEnroll(code, "friend", "invited-passphrase-1")

	token, _ := r.mintToken(friend, "friend-token", authz.PermBrowse)
	tc := r.bearer(token)
	if res := tc.get("/api/v1/media"); !reachable(res) {
		t.Fatalf("token did not work before suspension: %d %s", res.Code, res.Raw)
	}

	user, _ := r.store.UserByUsername(t.Context(), "friend")
	if res := admin.post("/api/v1/admin/users/"+itoa(int(user.ID))+"/suspend",
		map[string]any{"reason": "testing"}); res.Code != http.StatusOK {
		t.Fatalf("suspend: %d %s", res.Code, res.Raw)
	}

	if res := tc.get("/api/v1/media"); res.Code != http.StatusNotFound {
		t.Fatalf("A SUSPENDED USER'S TOKEN STILL WORKED: %d %s (THE ATTACK SUCCEEDED)", res.Code, res.Raw)
	}
}

// The same object-level check as sessions: a token id is not an authorization
// control.
func TestUserCannotRevokeAnotherUsersToken(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	codeA, _ := r.issueInvite(admin, authz.RoleUser, true)
	alice := r.redeemAndEnroll(codeA, "alice", "alice-passphrase-99")
	codeB, _ := r.issueInvite(admin, authz.RoleUser, true)
	bob := r.redeemAndEnroll(codeB, "bob", "bob-passphrase-1234")

	bobToken, bobTokenID := r.mintToken(bob, "bob-token", authz.PermBrowse)

	if res := alice.del("/api/v1/me/tokens/" + itoa(int(bobTokenID))); res.Code != http.StatusNotFound {
		t.Fatalf("ALICE REVOKED BOB'S TOKEN: %d %s (THE ATTACK SUCCEEDED)", res.Code, res.Raw)
	}
	if res := r.bearer(bobToken).get("/api/v1/media"); !reachable(res) {
		t.Errorf("bob's token was killed by another user: %d", res.Code)
	}

	// Alice cannot see it either.
	list := alice.get("/api/v1/me/tokens")
	if strings.Contains(list.Raw, "bob-token") {
		t.Errorf("alice can see bob's tokens: %s", list.Raw)
	}
}

// ---------------------------------------------------------------------------
// Credential handling
// ---------------------------------------------------------------------------

// A request that presents a Bearer token must not be silently upgraded to the
// cookie's session when the token is bad. Falling through would give an
// attacker holding a stale token plus a live browser cookie the session's
// privileges on a route they meant to scope down.
func TestBadBearerTokenDoesNotFallBackToTheCookie(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	// A client with BOTH a valid session cookie and a junk bearer token.
	admin.bearer = identity.TokenPrefix + "completely-made-up-value"

	if res := admin.get("/api/v1/me"); res.Code != http.StatusNotFound {
		t.Fatalf("a junk bearer token fell back to the session cookie: %d %s", res.Code, res.Raw)
	}

	// Removing the bad token restores the session.
	admin.bearer = ""
	if res := admin.get("/api/v1/me"); res.Code != http.StatusOK {
		t.Errorf("the session stopped working: %d %s", res.Code, res.Raw)
	}
}

func TestTokenListNeverContainsTheTokenValue(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	token, _ := r.mintToken(admin, "listed", authz.PermBrowse)

	res := admin.get("/api/v1/me/tokens")
	if res.Code != http.StatusOK {
		t.Fatalf("list tokens: %d %s", res.Code, res.Raw)
	}
	if strings.Contains(res.Raw, token) {
		t.Fatalf("the token list leaked a token value: %s", res.Raw)
	}
	if !strings.Contains(res.Raw, "listed") {
		t.Errorf("expected the token name in the list: %s", res.Raw)
	}
	// The scope is visible, which is the point of the list.
	if !strings.Contains(res.Raw, string(authz.PermBrowse)) {
		t.Errorf("expected the scope in the list: %s", res.Raw)
	}
}

// An empty scope is refused: a token with no permissions is either a mistake or
// an attempt to get a credential whose scope widens later.
func TestTokenWithNoScopeIsRefused(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	if res := admin.post("/api/v1/me/tokens", map[string]any{
		"name": "empty", "permissions": []string{}, "ttl_days": 30,
	}); res.Code != http.StatusForbidden {
		t.Errorf("a token with no scope was issued: %d %s", res.Code, res.Raw)
	}
}

// A token principal must be marked as such, or the session-only guard silently
// stops working: CredentialSession is the zero value.
func TestTokenPrincipalIsMarkedAsAToken(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	token, _ := r.mintToken(admin, "marked", authz.PermBrowse)

	tok, user, err := r.store.ResolveAPIToken(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.store.BuildTokenPrincipal(t.Context(), tok, user)
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsToken() {
		t.Fatal("a token principal was not marked as a token; the session-only guard would not fire")
	}
	if p.Credential != authz.CredentialToken {
		t.Errorf("credential = %q, want %q", p.Credential, authz.CredentialToken)
	}
}

// The CSRF exemption for Bearer requests has to actually work, or tokens are
// unusable for every state-changing call. This is the positive half of the
// pair: TestStateChangingRequestWithoutCSRFTokenIsRefused is the negative half,
// and it still holds for cookie-authenticated requests.
func TestTokenCanPostWithoutACSRFToken(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	token, _ := r.mintToken(admin, "writer", authz.PermSubmitRequest)
	tc := r.bearer(token)
	if _, ok := tc.cookies[CSRFCookieName]; ok {
		t.Fatal("the token client should hold no CSRF cookie")
	}

	// In scope: reaches the handler, rather than being blocked at 403 by CSRF.
	// An empty body is deliberate — what is being tested is that the request
	// gets THROUGH, so the handler's own answer (400, for a body with no title)
	// is a pass. Pinning a specific success code here is what broke this test
	// the last two times a handler behind it was implemented.
	res := tc.post("/api/v1/requests", map[string]any{})
	if res.Code == http.StatusForbidden {
		t.Fatalf("CSRF blocked a Bearer-authenticated POST: %d %s", res.Code, res.Raw)
	}
	if !reachable(res) {
		t.Errorf("in-scope POST was refused: %d %s", res.Code, res.Raw)
	}

	// Out of scope is still refused, so the exemption did not widen anything.
	if res := tc.post("/api/v1/invites", map[string]any{"role_id": 1}); reachable(res) {
		t.Errorf("an out-of-scope POST was authorized: %d", res.Code)
	}
}

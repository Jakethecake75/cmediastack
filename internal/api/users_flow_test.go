package api

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// End-to-end tests for the administration surface: who may be listed, who may
// be acted on, and — the part these exist for — which paths out of "this
// instance has a working administrator" are closed.

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// uid renders a user id for a URL path.
func uid(n int64) string { return strconv.FormatInt(n, 10) }

// userRow finds one account in a /api/v1/admin/users response by username.
func userRow(t *testing.T, res response, username string) map[string]any {
	t.Helper()
	rows, _ := res.Body["users"].([]any)
	for _, r := range rows {
		row, _ := r.(map[string]any)
		if row["username"] == username {
			return row
		}
	}
	t.Fatalf("no row for %q in %s", username, res.Raw)
	return nil
}

func (r *rig) userID(username string) int64 {
	r.t.Helper()
	u, err := r.store.UserByUsername(r.t.Context(), username)
	if err != nil {
		r.t.Fatal(err)
	}
	return u.ID
}

func (r *rig) activeAdmins() int {
	r.t.Helper()
	n, err := r.store.CountActiveAdmins(r.t.Context())
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) state(username string) authz.UserState {
	r.t.Helper()
	u, err := r.store.UserByUsername(r.t.Context(), username)
	if err != nil {
		r.t.Fatal(err)
	}
	return u.State
}

// ---------------------------------------------------------------------------
// The listing
// ---------------------------------------------------------------------------

func TestTheUserListSaysWhoMayBeActedOn(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	code, _ := r.issueInvite(admin, authz.RoleManager, true)
	r.redeemAndEnroll(code, "morgan", "manager-passphrase-1")
	code, _ = r.issueInvite(admin, authz.RoleUser, true)
	r.redeemAndEnroll(code, "sam", "regular-passphrase-1")

	res := admin.get("/api/v1/admin/users")
	if res.Code != http.StatusOK {
		t.Fatalf("list users: %d %s", res.Code, res.Raw)
	}
	if n, _ := res.Body["count"].(float64); n != 3 {
		t.Errorf("count = %v, want 3: %s", res.Body["count"], res.Raw)
	}

	// You may not act on yourself, and you may act on anyone below you. Said
	// per row rather than left to the client to infer.
	if got := userRow(t, res, "jacob")["manageable"]; got != false {
		t.Errorf("the admin's own row is manageable=%v, want false", got)
	}
	for _, name := range []string{"morgan", "sam"} {
		if got := userRow(t, res, name)["manageable"]; got != true {
			t.Errorf("%s manageable=%v, want true", name, got)
		}
	}

	// §8 PII minimisation: an account-management screen acts on usernames. It
	// has no reason to hold everybody's address, so it does not carry one.
	for _, row := range res.Body["users"].([]any) {
		if _, ok := row.(map[string]any)["email"]; ok {
			t.Errorf("the user list leaks an email address: %s", res.Raw)
			break
		}
	}
}

// A Manager holds PermSuspendUser but not PermManageUsers, so the listing is
// not merely refused — it is invisible.
func TestTheUserListIsInvisibleToAManager(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleManager, true)
	manager := r.redeemAndEnroll(code, "morgan", "manager-passphrase-1")

	if res := manager.get("/api/v1/admin/users"); res.Code != http.StatusNotFound {
		t.Errorf("manager GET /api/v1/admin/users = %d, want 404", res.Code)
	}
	if res := manager.patch("/api/v1/admin/users/1", map[string]any{
		"state": "suspended",
	}); res.Code != http.StatusNotFound {
		t.Errorf("manager PATCH /api/v1/admin/users/1 = %d, want 404", res.Code)
	}
}

// ---------------------------------------------------------------------------
// Lockout
// ---------------------------------------------------------------------------

// TestTheLastAdministratorCannotBeRemoved walks every path an operator could
// take to end up with an instance nobody can administer, and asserts each one
// is closed.
//
// This tests the PATHS rather than the guard. identity.guardLastAdmin exists
// and is correct (identity.TestTheLastAdminGuardBites proves that in
// isolation), but today it never fires, because authz.CanModifyUser refuses
// every route to it first. A test that only exercised the guard would pass
// while a new endpoint quietly reopened the hole beside it.
func TestTheLastAdministratorCannotBeRemoved(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	code, _ := r.issueInvite(admin, authz.RoleManager, true)
	manager := r.redeemAndEnroll(code, "morgan", "manager-passphrase-1")

	adminID := r.userID("jacob")
	userRoleID := r.roleID(authz.RoleUser)

	if r.activeAdmins() != 1 {
		t.Fatalf("precondition: %d administrators, want exactly 1", r.activeAdmins())
	}

	// Each of these would leave nobody able to administer the instance.
	//
	// Deferred rather than evaluated into a slice, because the attempts are not
	// independent: if one of them SUCCEEDS, the admin's session dies with it and
	// every later attempt returns 404 for the wrong reason — a passing test
	// built on a successful lockout. Each runs, then the count is checked, then
	// the next one runs.
	attempts := []struct {
		what string
		do   func() response
	}{
		{"the admin suspends itself with PATCH", func() response {
			return admin.patch("/api/v1/admin/users/"+uid(adminID), map[string]any{
				"state": "suspended", "reason": "testing",
			})
		}},
		{"the admin suspends itself with the suspend endpoint", func() response {
			return admin.post("/api/v1/admin/users/"+uid(adminID)+"/suspend", map[string]any{
				"reason": "testing",
			})
		}},
		{"the admin demotes itself to User", func() response {
			return admin.patch("/api/v1/admin/users/"+uid(adminID), map[string]any{
				"role_id": userRoleID,
			})
		}},
		{"a manager suspends the admin", func() response {
			return manager.post("/api/v1/admin/users/"+uid(adminID)+"/suspend", map[string]any{
				"reason": "escalation by denial",
			})
		}},
	}
	for _, a := range attempts {
		// A refusal is 404 when authorization denied it (writeAuthzAware makes
		// "you may not" and "no such thing" the same answer) and 409 when the
		// actor had the authority and the instance refused the outcome. Both
		// are refusals; which one arrives depends on which rule fired first,
		// and pinning that would make this test about the rules rather than
		// about the outcome.
		got := a.do()
		if got.Code != http.StatusNotFound && got.Code != http.StatusConflict {
			t.Errorf("%s: %d %s — WANT a refusal (404 or 409)", a.what, got.Code, got.Raw)
		}
		// The assertion that matters, checked after EVERY attempt rather than
		// once at the end.
		if n := r.activeAdmins(); n != 1 {
			t.Fatalf("after %q there are %d administrators who can sign in, want 1",
				a.what, n)
		}
	}
	if s := r.state("jacob"); s != authz.StateActive {
		t.Fatalf("the administrator is %q", s)
	}
	if res := admin.get("/api/v1/admin/users"); res.Code != http.StatusOK {
		t.Fatalf("the administrator can no longer administer: %d %s", res.Code, res.Raw)
	}
}

// TestAnInstanceCanNeverHaveASecondAdministrator records a real operational
// limitation rather than a protection, and is here so that it is a decision
// somebody made rather than something nobody noticed.
//
// Every path that puts an account into a role goes through authz.CanAssignRole,
// which is strictly downward: rank must be BELOW the actor's. Admin is the top
// builtin rank and role editing is not implemented, so an Admin cannot mint
// another Admin by invite, by approving a request, or by promotion. The setup
// wizard creates exactly one and then closes permanently.
//
// The consequence is a bus factor of one: if the sole administrator loses both
// their authenticator and their recovery codes, the recovery path is a database
// edit. That is a known gap, recorded in PROGRESS.md, not an accident.
func TestAnInstanceCanNeverHaveASecondAdministrator(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	adminRoleID := r.roleID(authz.RoleAdmin)

	// Not by invite.
	res := admin.post("/api/v1/invites", map[string]any{
		"role_id": adminRoleID, "library_ids": []int64{},
		"rating_ceiling": 0, "auto_approve": true, "ttl_hours": 168,
	})
	if res.Code == http.StatusCreated {
		t.Errorf("an Admin minted an invite into the Admin role: %s", res.Raw)
	}

	// Not by promotion either.
	code, _ := r.issueInvite(admin, authz.RoleManager, true)
	r.redeemAndEnroll(code, "morgan", "manager-passphrase-1")

	res = admin.patch("/api/v1/admin/users/"+uid(r.userID("morgan")), map[string]any{
		"role_id": adminRoleID,
	})
	if res.Code == http.StatusOK {
		t.Errorf("an Admin promoted somebody into the Admin role: %s", res.Raw)
	}
	if n := r.activeAdmins(); n != 1 {
		t.Errorf("administrators = %d, want 1", n)
	}
}

// ---------------------------------------------------------------------------
// Suspension and reactivation
// ---------------------------------------------------------------------------

func TestSuspensionTakesEffectOnTheNextRequest(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")

	if res := sam.get("/api/v1/me"); res.Code != http.StatusOK {
		t.Fatalf("precondition: sam cannot read their own profile: %d", res.Code)
	}

	res := admin.patch("/api/v1/admin/users/"+uid(r.userID("sam")), map[string]any{
		"state": "suspended", "reason": "seeding ratio",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("suspend: %d %s", res.Code, res.Raw)
	}

	// Sessions are server-side records re-read on every request (ADR-0009), so
	// this does not wait for a token to expire.
	if res := sam.get("/api/v1/me"); res.Code == http.StatusOK {
		t.Errorf("a suspended account still reads its own profile: %s", res.Raw)
	}

	// And back again.
	res = admin.patch("/api/v1/admin/users/"+uid(r.userID("sam")), map[string]any{
		"state": "active",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("reactivate: %d %s", res.Code, res.Raw)
	}
	if got := r.state("sam"); got != authz.StateActive {
		t.Errorf("state after reactivation = %q, want active", got)
	}
}

// Reactivation restores the access somebody had. It does not grant access they
// never established: an account that never enrolled a second factor goes back
// to awaiting_mfa, not to active.
func TestReactivationDoesNotHandBackAccessNobodyEverHad(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)

	// Sign up and stop: no enrollment.
	c := r.client()
	c.visitPage("/signup")
	if res := c.post("/api/v1/auth/signup", map[string]any{
		"username": "halfway", "email": "halfway@example.com",
		"password": "half-finished-passphrase", "invite_code": code,
	}); res.Code != http.StatusCreated {
		t.Fatalf("signup: %d %s", res.Code, res.Raw)
	}
	if got := r.state("halfway"); got != authz.StateAwaitingMFA {
		t.Fatalf("precondition: state is %q, want awaiting_mfa", got)
	}

	id := uid(r.userID("halfway"))
	if res := admin.patch("/api/v1/admin/users/"+id, map[string]any{
		"state": "suspended", "reason": "never finished setup",
	}); res.Code != http.StatusOK {
		t.Fatalf("suspend: %d %s", res.Code, res.Raw)
	}
	if res := admin.patch("/api/v1/admin/users/"+id, map[string]any{
		"state": "active",
	}); res.Code != http.StatusOK {
		t.Fatalf("reactivate: %d %s", res.Code, res.Raw)
	}

	if got := r.state("halfway"); got != authz.StateAwaitingMFA {
		t.Errorf("reactivation put an un-enrolled account into %q — "+
			"THE SECOND FACTOR WAS BYPASSED", got)
	}
}

// ---------------------------------------------------------------------------
// Role changes
// ---------------------------------------------------------------------------

func TestARoleChangeAppliesToAnAlreadyOpenSession(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")

	// A User may not see the account queue.
	if res := sam.get("/api/v1/accounts/requests"); res.Code == http.StatusOK {
		t.Fatalf("precondition: a User already reads the account queue")
	}

	res := admin.patch("/api/v1/admin/users/"+uid(r.userID("sam")), map[string]any{
		"role_id": r.roleID(authz.RoleManager),
	})
	if res.Code != http.StatusOK {
		t.Fatalf("promote: %d %s", res.Code, res.Raw)
	}

	// No new login: the session record is re-read and carries the new role.
	if res := sam.get("/api/v1/accounts/requests"); res.Code != http.StatusOK {
		t.Errorf("the promotion did not reach the open session: %d %s", res.Code, res.Raw)
	}

	// Demotion must reach it just as fast, which is the direction that matters.
	res = admin.patch("/api/v1/admin/users/"+uid(r.userID("sam")), map[string]any{
		"role_id": r.roleID(authz.RoleUser),
	})
	if res.Code != http.StatusOK {
		t.Fatalf("demote: %d %s", res.Code, res.Raw)
	}
	if res := sam.get("/api/v1/accounts/requests"); res.Code == http.StatusOK {
		t.Errorf("a demoted account kept its old authority on an open session")
	}
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// A refused escalation must leave a record. §8 asks for authz denials in the
// audit log, and this is the category that most needs to be there: an operator
// reading it is trying to tell a mis-click apart from somebody probing what
// they can reach, and only the log can answer that.
//
// This test exists because the binary said otherwise. Suspension logged its
// denials and role changes did not, so "can I promote myself to Admin?" was
// refused and then invisible — found by reading audit_event out of a running
// instance after exercising every refusal by hand.
func TestARefusedEscalationIsRecorded(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	r.redeemAndEnroll(code, "sam", "regular-passphrase-1")

	// Three different refusals, each decided inside the service rather than at
	// the route — which is why the route-level middleware cannot log them.
	admin.patch("/api/v1/admin/users/"+uid(r.userID("jacob")), map[string]any{
		"role_id": r.roleID(authz.RoleUser), // demote self
	})
	admin.patch("/api/v1/admin/users/"+uid(r.userID("sam")), map[string]any{
		"role_id": r.roleID(authz.RoleAdmin), // promote beyond own rank
	})
	admin.post("/api/v1/admin/users/"+uid(r.userID("jacob"))+"/suspend", map[string]any{
		"reason": "suspend self",
	})

	events, err := r.audit.List(
		authz.WithPrincipal(t.Context(), adminPrincipalFor(t, r)),
		audit.Query{Action: audit.ActionAuthzDenied})
	if err != nil {
		t.Fatal(err)
	}

	routes := map[string]bool{}
	for _, e := range events {
		routes[e.TargetID] = true
		if e.Outcome != audit.OutcomeDenied {
			t.Errorf("denial recorded with outcome %q", e.Outcome)
		}
		// §8: actor, source IP, user agent. A denial with no provenance says
		// something was refused without saying who to.
		if e.ActorUserID == nil {
			t.Errorf("denial on %q has no actor", e.TargetID)
		}
		if e.SourceIP == "" {
			t.Errorf("denial on %q has no source IP", e.TargetID)
		}
	}

	for _, want := range []string{"user.role.change", "user.role.assign", "user.suspend"} {
		if !routes[want] {
			t.Errorf("no audit record for a refused %s; recorded: %v", want, routes)
		}
	}
}

func TestAPatchMustDoExactlyOneThing(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	r.redeemAndEnroll(code, "sam", "regular-passphrase-1")
	id := uid(r.userID("sam"))

	for _, body := range []map[string]any{
		{}, // neither
		{"state": "suspended", "role_id": r.roleID(authz.RoleUser)}, // both
		{"state": "deleted"}, // not a state this endpoint sets
	} {
		res := admin.patch("/api/v1/admin/users/"+id, body)
		if res.Code != http.StatusBadRequest {
			t.Errorf("PATCH %v = %d %s, want 400", body, res.Code, res.Raw)
		}
	}

	// None of that changed anything.
	if got := r.state("sam"); got != authz.StateActive {
		t.Errorf("state = %q after four refused patches, want active", got)
	}
}

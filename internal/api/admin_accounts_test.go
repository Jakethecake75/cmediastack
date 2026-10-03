package api

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

func (r *rig) auditDetails(t *testing.T, action string) []string {
	t.Helper()
	rows, err := r.database.QueryContext(t.Context(),
		`SELECT detail FROM audit_event WHERE action = ? AND outcome = 'success' ORDER BY id`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// ADR-0039, decision 1.
func TestAPendingRequestIsReadAlone(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	c := r.client()
	c.visitPage("/signup")
	if res := c.post("/api/v1/auth/signup", map[string]any{
		"username": "cousin", "email": "cousin@example.com", "password": "a-long-passphrase-7",
	}); res.Code/100 != 2 {
		t.Fatalf("signup: %d %s", res.Code, res.Raw)
	}
	list := admin.get("/api/v1/accounts/requests")
	id := strconv.FormatInt(int64(list.Body["requests"].([]any)[0].(map[string]any)["id"].(float64)), 10)

	res := admin.get("/api/v1/accounts/requests/" + id)
	if res.Code != http.StatusOK || res.Body["username"] != "cousin" || res.Body["email"] != "cousin@example.com" {
		t.Fatalf("one request: %d %s", res.Code, res.Raw)
	}
	if strings.Contains(res.Raw, "argon2") || strings.Contains(res.Raw, "password") {
		t.Errorf("the request carries its password hash: %s", res.Raw)
	}
	if res := admin.get("/api/v1/accounts/requests/424242"); res.Code != http.StatusNotFound {
		t.Errorf("an unknown request: %d", res.Code)
	}
	if res := admin.post("/api/v1/accounts/requests/"+id+"/deny", map[string]any{"reason": "no"}); res.Code != http.StatusOK {
		t.Fatalf("deny: %d %s", res.Code, res.Raw)
	}
	if res := admin.get("/api/v1/accounts/requests/" + id); res.Code != http.StatusNotFound {
		t.Errorf("a decided request: %d, want 404", res.Code)
	}
}

// ADR-0039, decision 2: the list.
func TestRolesAreListed(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	res := admin.get("/api/v1/admin/roles")
	if res.Code != http.StatusOK || res.Body["count"] != float64(3) {
		t.Fatalf("roles: %d %s", res.Code, res.Raw)
	}
	for _, row := range res.Body["roles"].([]any) {
		m := row.(map[string]any)
		if m["defaults"] != true {
			t.Errorf("%v is not on its defaults on a new instance", m["name"])
		}
		if (m["name"] == authz.RoleAdmin) == (m["editable"] == true) {
			t.Errorf("%v editable = %v", m["name"], m["editable"])
		}
		if m["name"] == authz.RoleAdmin && m["holders"] != float64(1) {
			t.Errorf("Admin holders = %v", m["holders"])
		}
	}
	if !strings.Contains(res.Raw, `"reserved_for_admin":["admin.system","admin.users","admin.network"]`) {
		t.Errorf("the reserved permissions are not said: %s", res.Raw)
	}
}

// ADR-0039, decision 2: the edit.
func TestARoleIsEditedWithinLimits(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	user := r.redeemAndEnroll(code, "friend", "invited-passphrase-1")
	userRole := "/api/v1/admin/roles/" + strconv.FormatInt(r.roleID(authz.RoleUser), 10)

	if res := user.get("/api/v1/queue"); res.Code != http.StatusForbidden {
		t.Fatalf("a User reached the queue before the edit: %d", res.Code)
	}
	perms := []string{"auth.login", "media.stream", "media.browse", "request.submit", "acquisition.queue"}
	res := admin.do(http.MethodPatch, userRole, map[string]any{"permissions": perms})
	if res.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", res.Code, res.Raw)
	}
	if res := user.get("/api/v1/queue"); res.Code == http.StatusForbidden || res.Code == http.StatusNotFound {
		t.Errorf("the edit did not apply to the holder's next request: %d", res.Code)
	}

	for name, body := range map[string]map[string]any{
		"admin.system":  {"permissions": append(slices.Clone(perms), "admin.system")},
		"admin.users":   {"permissions": append(slices.Clone(perms), "admin.users")},
		"admin.network": {"permissions": append(slices.Clone(perms), "admin.network")},
		"no login":      {"permissions": perms[1:]},
		"unknown":       {"permissions": append(slices.Clone(perms), "media.everything")},
		"neither":       {},
	} {
		if res := admin.do(http.MethodPatch, userRole, body); res.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, res.Code, res.Raw)
		}
	}
	adminRole := "/api/v1/admin/roles/" + strconv.FormatInt(r.roleID(authz.RoleAdmin), 10)
	if res := admin.do(http.MethodPatch, adminRole, map[string]any{"permissions": []string{"auth.login"}}); res.Code != http.StatusConflict {
		t.Errorf("the Admin role was edited: %d", res.Code)
	}
	if res := user.do(http.MethodPatch, userRole, map[string]any{"defaults": true}); res.Code != http.StatusNotFound {
		t.Errorf("a User reached the role editor: %d", res.Code)
	}

	res = admin.do(http.MethodPatch, userRole, map[string]any{"defaults": true})
	if res.Code != http.StatusOK {
		t.Fatalf("defaults: %d %s", res.Code, res.Raw)
	}
	if res := user.get("/api/v1/queue"); res.Code != http.StatusForbidden {
		t.Errorf("the defaults did not take the queue back: %d", res.Code)
	}
	lines := r.auditDetails(t, "user.role_permissions.changed")
	if len(lines) != 2 || lines[0] != "User: added acquisition.queue" ||
		!strings.Contains(lines[1], "put back to the built-in permissions") {
		t.Errorf("audit lines %q", lines)
	}
}

// ADR-0039, decision 3.
func TestAnAdministratorCreatesAnAccount(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	res := admin.post("/api/v1/admin/users", map[string]any{
		"username": "cousin", "email": "Cousin@Example.com", "role_id": r.roleID(authz.RoleUser),
	})
	if res.Code != http.StatusCreated || res.Body["state"] != "awaiting_mfa" {
		t.Fatalf("create: %d %s", res.Code, res.Raw)
	}
	link, _ := res.Body["link"].(string)
	if !strings.HasPrefix(link, "/reset?token=") {
		t.Fatalf("link %q", link)
	}
	token := strings.TrimPrefix(link, "/reset?token=")

	// Nobody knows a password for it until the link sets one.
	c := r.client()
	c.visitPage("/reset")
	if res := c.post("/api/v1/auth/reset/complete", map[string]any{
		"token": token, "password": "cousins-own-passphrase"}); res.Code/100 != 2 {
		t.Fatalf("setting the password: %d %s", res.Code, res.Raw)
	}
	c.visitPage("/login")
	if res := c.post("/api/v1/auth/login", map[string]any{"username": "cousin",
		"password": "cousins-own-passphrase"}); res.Code != http.StatusOK || res.Body["next"] != "enroll" {
		t.Errorf("first sign-in: %d %s", res.Code, res.Raw)
	}
	if res := c.post("/api/v1/auth/reset/complete", map[string]any{
		"token": token, "password": "another-passphrase-1"}); res.Code/100 == 2 {
		t.Error("the link worked twice")
	}
	u, err := r.store.UserByUsername(t.Context(), "cousin")
	if err != nil || u.Email != "cousin@example.com" || !u.AllLibraries {
		t.Errorf("the account: %+v %v", u, err)
	}

	for name, body := range map[string]map[string]any{
		"taken":    {"username": "cousin", "email": "x@example.com", "role_id": r.roleID(authz.RoleUser)},
		"email":    {"username": "second", "email": "not an address", "role_id": r.roleID(authz.RoleUser)},
		"username": {"username": "a b", "email": "b@example.com", "role_id": r.roleID(authz.RoleUser)},
	} {
		want := http.StatusBadRequest
		if name == "taken" {
			want = http.StatusConflict
		}
		if res := admin.post("/api/v1/admin/users", body); res.Code != want {
			t.Errorf("%s: %d %s, want %d", name, res.Code, res.Raw, want)
		}
	}
	// Strictly downward: not a second administrator.
	if res := admin.post("/api/v1/admin/users", map[string]any{"username": "peer",
		"email": "peer@example.com", "role_id": r.roleID(authz.RoleAdmin)}); res.Code/100 == 2 {
		t.Errorf("an administrator created an administrator: %d", res.Code)
	}
	if lines := r.auditDetails(t, "user.created"); len(lines) != 1 || !strings.HasPrefix(lines[0], "cousin created as User") {
		t.Errorf("audit lines %q", lines)
	}
}

// ADR-0039, decision 4.
func TestAnEmailIsChangedWithThePassword(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	user := r.redeemAndEnroll(code, "friend", "invited-passphrase-1")

	if res := user.do(http.MethodPatch, "/api/v1/me", map[string]any{
		"email": "new@example.com", "current_password": "wrong-passphrase"}); res.Code != http.StatusForbidden {
		t.Errorf("a wrong password: %d", res.Code)
	}
	if res := user.do(http.MethodPatch, "/api/v1/me", map[string]any{
		"email": "jacob@example.com", "current_password": "invited-passphrase-1"}); res.Code != http.StatusConflict {
		t.Errorf("a taken address: %d", res.Code)
	}
	if res := user.do(http.MethodPatch, "/api/v1/me", map[string]any{
		"email": "nope", "current_password": "invited-passphrase-1"}); res.Code != http.StatusBadRequest {
		t.Errorf("not an address: %d", res.Code)
	}
	if res := user.do(http.MethodPatch, "/api/v1/me", map[string]any{
		"email": "New@Example.com", "current_password": "invited-passphrase-1"}); res.Code != http.StatusOK {
		t.Fatalf("change: %d %s", res.Code, res.Raw)
	}
	if u, _ := r.store.UserByUsername(t.Context(), "friend"); u.Email != "new@example.com" {
		t.Errorf("stored %q", u.Email)
	}

	tok, _ := r.mintToken(user, "script", authz.PermBrowse)
	if res := r.bearer(tok).do(http.MethodPatch, "/api/v1/me", map[string]any{
		"email": "token@example.com", "current_password": "invited-passphrase-1"}); res.Code/100 == 2 {
		t.Errorf("an API token changed the email: %d", res.Code)
	}
}

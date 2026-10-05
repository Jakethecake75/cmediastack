package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// This file contains Phase 1 acceptance criteria (a), (b), (c) and (d):
//
//	(a) an automated route-enumeration test proves every route outside the
//	    §7.1 allowlist rejects anonymous requests
//	(b) a pending account cannot obtain a session or reach any resource
//	(c) a low-privilege user cannot reach any admin route by any API path
//	(d) a Manager cannot approve an account into Admin or escalate
//
// (d)'s engine-level proof lives in internal/authz; what is proved here is that
// the HTTP layer routes to that engine and honours its answer.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

type fakeAuth struct{ principal *authz.Principal }

func (f fakeAuth) Authenticate(http.ResponseWriter, *http.Request) (*authz.Principal, error) {
	return f.principal, nil
}

type recordingSink struct {
	denials []string
}

func (s *recordingSink) AuthzDenied(_ context.Context, route string, d *authz.Denial, _, _ string) {
	s.denials = append(s.denials, route+" "+d.Reason)
}

func buildRouter(principal *authz.Principal, sink AuditSink) *Router {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt := NewRouter(
		[]Middleware{
			Recovery(logger),
			RequestContext(logger),
			ClientIPResolver(nil),
			SecurityHeaders(365 * 24 * time.Hour),
		},
		[]Middleware{
			Authenticate(fakeAuth{principal: principal}, sink),
		},
	)
	RegisterRoutes(rt, nil)
	return rt
}

func roleNamed(name string) authz.Role {
	for _, r := range authz.BuiltinRoles() {
		if r.Name == name {
			return r
		}
	}
	panic("unknown role " + name)
}

func activePrincipal(id int64, roleName string, libraries ...int64) *authz.Principal {
	r := roleNamed(roleName)
	return &authz.Principal{
		UserID:                id,
		Username:              strings.ToLower(roleName),
		Role:                  r,
		State:                 authz.StateActive,
		SessionID:             "sess",
		LibraryIDs:            libraries,
		UnrestrictedLibraries: r.Permissions.Has(authz.PermSystemSettings),
		MFASatisfied:          true,
	}
}

var placeholder = regexp.MustCompile(`\{[^}]*\}`)

// prefixSamples names a real resource under the routes registered as a
// directory prefix. Requesting the bare prefix would 404 from the handler,
// which is correct behaviour but indistinguishable in a test from being
// blocked by the middleware.
var prefixSamples = map[string]string{
	"/assets/auth/": "/assets/auth/auth.css",
	"/assets/app/":  "/assets/app/app.css",
}

// concretePath turns a route pattern into a requestable path.
func concretePath(pattern string) string {
	if sample, ok := prefixSamples[pattern]; ok {
		return sample
	}
	// "{$}" is an end-of-path anchor, not a wildcard segment: it must be
	// removed rather than filled in.
	pattern = strings.ReplaceAll(pattern, "{$}", "")
	return placeholder.ReplaceAllString(pattern, "1")
}

// browserRedirectRoute is the single route allowed to answer a denial with a
// redirect instead of a 404. §2 requires that a visitor with no session is
// shown a login screen, and the root is where a visitor arrives; the
// disclosure is nil because every origin has a root and /login is already
// anonymous. If this ever becomes a list, that is the moment to re-argue it.
const browserRedirectRoute = "GET /{$}"

// assertLoginRedirect checks the §2 carve-out is a bare redirect that leaks
// nothing: fixed destination, no body, no "?next=" for an attacker to aim.
func assertLoginRedirect(t *testing.T, rec *httptest.ResponseRecorder, routeID string) {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Errorf("%s returned %d, want 303", routeID, rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != LoginPath {
		t.Errorf("%s redirected to %q, want %q", routeID, loc, LoginPath)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "" {
		t.Errorf("%s redirect carried a body: %q", routeID, body)
	}
}

func doRequest(t *testing.T, rt *Router, method, pattern string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, concretePath(pattern), nil)
	req.RemoteAddr = "203.0.113.7:44444"
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// (a) Route enumeration — the headline test
// ---------------------------------------------------------------------------

func TestEveryNonAllowlistedRouteRejectsAnonymous(t *testing.T) {
	sink := &recordingSink{}
	rt := buildRouter(nil, sink) // nil principal: anonymous
	routes := rt.Routes()

	if len(routes) < 40 {
		t.Fatalf("only %d routes registered; the enumeration test is not covering a real surface", len(routes))
	}

	var checked int
	for _, route := range routes {
		if route.Access == AccessAnonymous {
			continue
		}
		checked++

		rec := doRequest(t, rt, route.Method, route.Pattern)

		// An anonymous caller must learn nothing. Not 401, not 403, not a
		// redirect: 404, identical to a route that does not exist. The root is
		// the single, deliberate exception — see browserRedirectRoute.
		if route.ID() == browserRedirectRoute {
			assertLoginRedirect(t, rec, route.ID())
			continue
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("ANONYMOUS REACHED %s: status %d, want 404\nbody: %s",
				route.ID(), rec.Code, rec.Body.String())
		}

		body := rec.Body.String()
		for _, leak := range []string{"forbidden", "unauthorized", "login", "permission", "admin"} {
			if strings.Contains(strings.ToLower(body), leak) {
				t.Errorf("%s leaked %q to an anonymous caller: %s", route.ID(), leak, body)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no protected routes were checked")
	}
	t.Logf("verified %d protected routes reject anonymous requests", checked)

	// The denials must still be recorded internally: 404 to the client, the
	// real reason in the audit log.
	if len(sink.denials) != checked {
		t.Errorf("audit sink recorded %d denials for %d rejections", len(sink.denials), checked)
	}
}

func TestAllowlistedRoutesAreReachableAnonymously(t *testing.T) {
	rt := buildRouter(nil, nil)

	for _, route := range rt.Routes() {
		if route.Access != AccessAnonymous {
			continue
		}
		rec := doRequest(t, rt, route.Method, route.Pattern)
		if rec.Code == http.StatusNotFound {
			t.Errorf("allowlisted route %s returned 404 to an anonymous caller", route.ID())
		}
	}
}

// The allowlist and the registrations must describe the same set. A route in
// the allowlist that nobody registered is dead configuration that will be
// wrongly trusted the day someone adds the route.
func TestAllowlistAndRegistrationsAgree(t *testing.T) {
	rt := buildRouter(nil, nil)

	registered := map[string]bool{}
	for _, route := range rt.Routes() {
		if route.Access == AccessAnonymous {
			registered[route.ID()] = true
		}
	}
	for id := range AnonymousAllowlist {
		if !registered[id] {
			t.Errorf("allowlist entry %q is not registered as an anonymous route", id)
		}
	}
	for id := range registered {
		if _, ok := AnonymousAllowlist[id]; !ok {
			t.Errorf("route %q is registered anonymous but absent from the allowlist", id)
		}
	}

	// 14 since 4ag: the calendar and the feed, whose token is in the path
	// (ADR-0041). 15 since 6a: signup's proof-of-work challenge, sealed, keeping
	// nothing, and 404 whenever signup is (ADR-0051). 17 since 7l: a
	// Chromecast's two cast links, signed for one file and account (ADR-0077).
	if len(AnonymousAllowlist) != 17 {
		t.Errorf("the anonymous surface has changed size: %d entries. "+
			"That is a security decision — update this assertion deliberately.", len(AnonymousAllowlist))
	}
}

// Registering an anonymous route that is not in the allowlist must be
// impossible, not merely discouraged.
func TestRegisteringUnlistedAnonymousRoutePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registering an unlisted anonymous route was permitted")
		}
	}()
	rt := NewRouter(nil, nil)
	rt.Anonymous(http.MethodGet, "/api/v1/admin/users", notImplemented)
}

func TestRegisteringDuplicateRoutePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate route registration was permitted")
		}
	}()
	rt := NewRouter(nil, nil)
	rt.Authenticated(http.MethodGet, "/api/v1/dup", notImplemented)
	rt.Authenticated(http.MethodGet, "/api/v1/dup", notImplemented)
}

// ---------------------------------------------------------------------------
// (b) A pending account cannot reach anything
// ---------------------------------------------------------------------------

// A pending account request is not a user, so it cannot produce a principal at
// all. The strongest form of this test is that the anonymous path IS the
// pending path: there is no principal to construct.
func TestPendingAccountIsIndistinguishableFromAnonymous(t *testing.T) {
	rt := buildRouter(nil, nil)
	for _, route := range rt.Routes() {
		if route.Access == AccessAnonymous {
			continue
		}
		rec := doRequest(t, rt, route.Method, route.Pattern)
		if route.ID() == browserRedirectRoute {
			assertLoginRedirect(t, rec, route.ID())
			continue
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("pending/anonymous reached %s with status %d", route.ID(), rec.Code)
		}
	}
}

// A suspended account must not be able to detect that its credential is still
// recognized. The root is the one route that answers a denial with something
// other than 404, so it is the one route where such an oracle could exist: a
// suspended caller and an unknown caller must get byte-identical responses
// there.
func TestSuspendedAccountIsIndistinguishableFromAnonymousAtTheRoot(t *testing.T) {
	anon := doRequest(t, buildRouter(nil, nil), http.MethodGet, "/{$}")

	for _, state := range []authz.UserState{authz.StateSuspended, authz.StateDisabled} {
		p := activePrincipal(9, authz.RoleAdmin)
		p.State = state
		got := doRequest(t, buildRouter(p, nil), http.MethodGet, "/{$}")

		if got.Code != anon.Code {
			t.Errorf("%s got status %d at the root, anonymous got %d", state, got.Code, anon.Code)
		}
		if got.Header().Get("Location") != anon.Header().Get("Location") {
			t.Errorf("%s was redirected to %q, anonymous to %q",
				state, got.Header().Get("Location"), anon.Header().Get("Location"))
		}
		if got.Body.String() != anon.Body.String() {
			t.Errorf("%s got a different body from anonymous at the root", state)
		}
	}
}

// An approved account that has not enrolled an authenticator reaches the
// enrollment routes and nothing else.
func TestAwaitingMFAReachesOnlyEnrollment(t *testing.T) {
	p := activePrincipal(5, authz.RoleAdmin) // deliberately an Admin
	p.State = authz.StateAwaitingMFA
	p.MFASatisfied = false

	rt := buildRouter(p, nil)

	var enrollment, blocked int
	for _, route := range rt.Routes() {
		if route.Access == AccessAnonymous {
			continue
		}
		rec := doRequest(t, rt, route.Method, route.Pattern)

		if route.Access == AccessEnrollment {
			enrollment++
			if rec.Code == http.StatusConflict || rec.Code == http.StatusNotFound {
				t.Errorf("enrollment route %s was blocked with %d", route.ID(), rec.Code)
			}
			continue
		}
		blocked++
		// The root sends them to the one page they can use rather than
		// answering a navigation with a JSON error code. It is still blocked:
		// the shell is not rendered and nothing is disclosed.
		if route.ID() == browserRedirectRoute {
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != EnrollPath {
				t.Errorf("an un-enrolled admin at the root got %d -> %q, want 303 -> %q",
					rec.Code, rec.Header().Get("Location"), EnrollPath)
			}
			continue
		}
		if rec.Code != http.StatusConflict {
			t.Errorf("an un-enrolled ADMIN reached %s with status %d, want 409 mfa_enrollment_required",
				route.ID(), rec.Code)
		}
	}

	if enrollment == 0 || blocked == 0 {
		t.Fatalf("test covered %d enrollment and %d blocked routes", enrollment, blocked)
	}
}

func TestSuspendedAndDisabledReachNothing(t *testing.T) {
	for _, state := range []authz.UserState{authz.StateSuspended, authz.StateDisabled} {
		p := activePrincipal(6, authz.RoleAdmin)
		p.State = state
		rt := buildRouter(p, nil)

		for _, route := range rt.Routes() {
			if route.Access == AccessAnonymous {
				continue
			}
			rec := doRequest(t, rt, route.Method, route.Pattern)
			if route.ID() == browserRedirectRoute {
				assertLoginRedirect(t, rec, route.ID())
				continue
			}
			if rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
				t.Errorf("a %s admin reached %s with status %d", state, route.ID(), rec.Code)
			}
		}
	}
}

// A session that never presented a second factor cannot act, even if the
// account is active. This is what stops a stolen first-factor session.
func TestSessionWithoutSecondFactorReachesNothing(t *testing.T) {
	p := activePrincipal(7, authz.RoleAdmin)
	p.MFASatisfied = false
	rt := buildRouter(p, nil)

	for _, route := range rt.Routes() {
		if route.Access == AccessAnonymous || route.Access == AccessEnrollment {
			continue
		}
		if rec := doRequest(t, rt, route.Method, route.Pattern); rec.Code == http.StatusNotImplemented {
			t.Errorf("a session without a second factor reached %s", route.ID())
		}
	}
}

// ---------------------------------------------------------------------------
// (c) A low-privilege user cannot reach any admin route
// ---------------------------------------------------------------------------

func TestUserCannotReachAnyAdminRouteAndGets404(t *testing.T) {
	rt := buildRouter(activePrincipal(20, authz.RoleUser, 1), nil)

	var admin int
	for _, route := range rt.Routes() {
		if !route.Hidden {
			continue
		}
		admin++
		rec := doRequest(t, rt, route.Method, route.Pattern)

		// 404, not 403: the administration surface must be invisible, not
		// merely forbidden (§7.3). A 403 confirms the route exists.
		if rec.Code != http.StatusNotFound {
			t.Errorf("USER REACHED ADMIN ROUTE %s: status %d (want 404)", route.ID(), rec.Code)
		}
		if strings.Contains(strings.ToLower(rec.Body.String()), "forbidden") {
			t.Errorf("%s returned a distinguishable 'forbidden' body to a user", route.ID())
		}
	}
	if admin < 15 {
		t.Fatalf("only %d admin routes covered", admin)
	}
	t.Logf("verified %d admin routes are invisible to a User", admin)
}

func TestManagerCannotReachAdminOnlyRoutes(t *testing.T) {
	rt := buildRouter(activePrincipal(10, authz.RoleManager), nil)

	adminOnly := []string{
		"GET /api/v1/admin/indexers",
		"POST /api/v1/admin/indexers",
		"GET /api/v1/admin/egress",
		"PATCH /api/v1/admin/egress",
		"POST /api/v1/admin/egress/leak-test",
		"DELETE /api/v1/admin/media/{id}",
		"GET /api/v1/admin/audit",
		"GET /api/v1/admin/audit/summary",
		"GET /api/v1/admin/notifications",
		"PUT /api/v1/admin/notifications/webhook",
		"PUT /api/v1/admin/notifications/categories",
		"POST /api/v1/admin/notifications/test",
		"GET /api/v1/admin/system/settings",
		"PATCH /api/v1/admin/system/settings",
		"POST /api/v1/admin/system/backup",
		"GET /api/v1/admin/system/backups",
		"GET /api/v1/admin/rootfolders",
		"GET /api/v1/admin/users",
		"GET /health/detail",
	}

	byID := map[string]Route{}
	for _, route := range rt.Routes() {
		byID[route.ID()] = route
	}

	for _, id := range adminOnly {
		route, ok := byID[id]
		if !ok {
			t.Fatalf("expected route %q to be registered", id)
		}
		if rec := doRequest(t, rt, route.Method, route.Pattern); rec.Code != http.StatusNotFound {
			t.Errorf("MANAGER REACHED %s: status %d (want 404)", id, rec.Code)
		}
	}
}

// The Manager's legitimate surface must still work, or the test above would
// pass trivially by denying a Manager everything.
func TestManagerReachesItsOwnSurface(t *testing.T) {
	rt := buildRouter(activePrincipal(10, authz.RoleManager), nil)

	for _, id := range []string{
		"GET /api/v1/accounts/requests",
		"POST /api/v1/accounts/requests/{id}/approve",
		"GET /api/v1/queue",
		"POST /api/v1/releases/search",
		"GET /api/v1/me",
	} {
		var route Route
		for _, rr := range rt.Routes() {
			if rr.ID() == id {
				route = rr
			}
		}
		if route.Pattern == "" {
			t.Fatalf("route %q not registered", id)
		}
		rec := doRequest(t, rt, route.Method, route.Pattern)
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusForbidden {
			t.Errorf("Manager was denied its own route %s: status %d", id, rec.Code)
		}
	}
}

func TestAdminReachesAdminSurface(t *testing.T) {
	rt := buildRouter(activePrincipal(1, authz.RoleAdmin), nil)

	for _, route := range rt.Routes() {
		if !route.Hidden {
			continue
		}
		rec := doRequest(t, rt, route.Method, route.Pattern)
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusForbidden {
			t.Errorf("Admin was denied admin route %s: status %d", route.ID(), rec.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// Middleware behaviour
// ---------------------------------------------------------------------------

func TestSecurityHeadersArePresent(t *testing.T) {
	rt := buildRouter(nil, nil)
	rec := doRequest(t, rt, http.MethodGet, "/login")

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}

	csp := rec.Header().Get("Content-Security-Policy")
	for _, frag := range []string{"frame-ancestors 'none'", "object-src 'none'", "base-uri 'none'", "nonce-"} {
		if !strings.Contains(csp, frag) {
			t.Errorf("CSP missing %q: %s", frag, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP permits unsafe execution: %s", csp)
	}
}

// X-Forwarded-For must be ignored when no trusted proxy is configured, or
// every per-IP limit becomes spoofable by setting a header.
func TestForwardedHeaderIsIgnoredWithoutTrustedProxies(t *testing.T) {
	var seen string
	h := ClientIPResolver(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = ClientIP(r.Context())
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.5:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "198.51.100.5" {
		t.Errorf("client IP = %q; a spoofed X-Forwarded-For was trusted", seen)
	}
}

func TestForwardedHeaderIsHonouredFromATrustedProxy(t *testing.T) {
	var seen string
	h := ClientIPResolver([]string{"10.0.0.0/8"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = ClientIP(r.Context())
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.RemoteAddr = "10.1.2.3:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.1.2.3")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "203.0.113.9" {
		t.Errorf("client IP = %q, want the right-most untrusted entry 203.0.113.9", seen)
	}
}

func TestRateLimiterWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	rl := NewRateLimiter(func() time.Time { return now })

	for i := 0; i < 3; i++ {
		if !rl.Allow("k", 3, time.Minute) {
			t.Fatalf("attempt %d was limited early", i+1)
		}
	}
	if rl.Allow("k", 3, time.Minute) {
		t.Error("the fourth attempt was allowed past a limit of 3")
	}

	now = now.Add(2 * time.Minute)
	if !rl.Allow("k", 3, time.Minute) {
		t.Error("the window did not reset")
	}

	rl.Reap()
}

func TestCSRFRejectsMissingAndMismatchedTokens(t *testing.T) {
	h := CSRF()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Safe method: no token needed.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET was blocked by CSRF: %d", rec.Code)
	}

	// No token at all.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("POST without a token returned %d, want 403", rec.Code)
	}

	// Mismatched.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "aaaaaaaa"})
	req.Header.Set(CSRFHeaderName, "bbbbbbbb")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("mismatched token returned %d, want 403", rec.Code)
	}

	// Matching.
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: "matching-value"})
	req.Header.Set(CSRFHeaderName, "matching-value")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("matching token returned %d, want 200", rec.Code)
	}
}

func TestPanicIsContainedAndNotLeaked(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt := NewRouter([]Middleware{Recovery(logger), RequestContext(logger)}, nil)
	rt.Authenticated(http.MethodGet, "/api/v1/boom", func(http.ResponseWriter, *http.Request) {
		panic("secret internal detail")
	})

	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret internal detail") {
		t.Errorf("the panic value leaked to the client: %s", rec.Body.String())
	}
}

func TestUnmatchedPathIs404ForAnonymous(t *testing.T) {
	rt := buildRouter(nil, nil)
	rec := doRequest(t, rt, http.MethodGet, "/definitely/not/a/route")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unmatched path returned %d, want 404", rec.Code)
	}
}

// A path that matches nothing and a route the caller may not have must be
// indistinguishable — not merely both 404, but the same bytes and the same
// headers. A difference in either is a fingerprint: probe a path, and the shape
// of the refusal tells you whether the route exists.
//
// This exists because adding Cache-Control to writeProblem silently created
// exactly that oracle, and only a test comparing the two caught it.
func TestUnmatchedPathIsByteIdenticalToADeniedHiddenRoute(t *testing.T) {
	rt := buildRouter(nil, nil)

	unmatched := doRequest(t, rt, http.MethodGet, "/definitely/not/a/route")
	hidden := doRequest(t, rt, http.MethodGet, "/api/v1/admin/users")

	if unmatched.Code != hidden.Code {
		t.Errorf("status: unmatched %d, hidden %d", unmatched.Code, hidden.Code)
	}
	if unmatched.Body.String() != hidden.Body.String() {
		t.Errorf("body differs:\n  unmatched: %q\n  hidden:    %q",
			unmatched.Body.String(), hidden.Body.String())
	}
	for _, h := range []string{"Content-Type", "Cache-Control"} {
		if a, b := unmatched.Header().Get(h), hidden.Header().Get(h); a != b {
			t.Errorf("%s differs: unmatched %q, hidden %q", h, a, b)
		}
	}

	// And the same for a method that exists on a different verb of a real path.
	wrongVerb := doRequest(t, rt, http.MethodGet, "/api/v1/admin/users/1")
	if wrongVerb.Body.String() != hidden.Body.String() {
		t.Errorf("a wrong-method probe differs: %q", wrongVerb.Body.String())
	}
}

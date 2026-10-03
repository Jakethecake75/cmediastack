// Package api owns the HTTP surface: the router, the middleware chain and the
// handlers.
//
// The router records every route it registers so that the middleware chain can
// enforce the anonymous allowlist by route identity rather than by URL
// pattern-matching, and so that a test can enumerate the whole surface and
// assert the security property holds for all of it.
package api

import (
	"fmt"
	"net/http"
	"reflect"
	"sort"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// Access classifies what a route requires.
type Access int

const (
	// AccessAnonymous routes are reachable without a session. Registering one
	// requires a matching entry in AnonymousAllowlist.
	AccessAnonymous Access = iota
	// AccessAuthenticated routes require an active, MFA-satisfied principal.
	AccessAuthenticated
	// AccessEnrollment routes require a session but tolerate the
	// awaiting_mfa state. Only the MFA enrollment endpoints use this.
	AccessEnrollment
	// AccessPermission routes require a specific permission.
	AccessPermission
)

// Route is one registered endpoint.
type Route struct {
	Method  string
	Pattern string
	Access  Access
	// Permission is required when Access is AccessPermission.
	Permission authz.Permission
	// Hidden marks routes that must return 404 rather than 403 when the
	// principal is not authorized, so their existence is not disclosed
	// (requirements §7.3).
	Hidden bool
	// SessionOnly refuses API tokens, requiring an interactive login.
	//
	// It guards credential management: changing a password, enrolling or
	// replacing the second factor, minting further tokens, managing sessions.
	// A token lives in a script or a config file, and if one could do those
	// things a single leak would be a permanent account takeover instead of a
	// scoped, revocable grant.
	SessionOnly bool
	// Browser marks a route a person navigates to rather than one a script
	// calls, which changes only how a DENIAL is rendered: such a route
	// redirects to the login or enrollment page instead of returning a JSON
	// 404, because §2 promises that an unauthenticated visitor is shown a login
	// screen.
	//
	// That is a disclosure: an anonymous caller learns the route exists. It is
	// therefore opt-in per route and never inferred from a header, so the set
	// of routes whose existence is admitted is a short, reviewable list rather
	// than whatever the browser happened to send. register() refuses to combine
	// it with Hidden, with a non-GET method, or with an anonymous route.
	Browser bool
	// Stub marks a route whose access class is enforced but whose handler is
	// still notImplemented.
	//
	// Recorded rather than remembered. PROGRESS.md carried "42 of 81 routes
	// still return 501" for three phases after it stopped being true, which is
	// the problem with a number a person has to maintain. register() works this
	// out from the handler it was actually given, the generated API surface
	// prints it, and docs.TestTheProgressHeadlineNumbersAreTrue fails the build
	// when the prose disagrees.
	Stub bool
}

// ID returns the canonical route identifier.
func (r Route) ID() string { return RouteID(r.Method, r.Pattern) }

// Router wraps http.ServeMux and records what it registered.
//
// Middleware is split in two because ordering is a security property here.
// Global middleware (panic recovery, request ID, client-IP resolution,
// security headers) wraps the mux and therefore runs before routing. Per-route
// middleware (rate limiting, CSRF, authentication and the allowlist gate) runs
// after routing, because it must act on the identity of the matched route
// rather than on a URL string it re-parses for itself.
type Router struct {
	mux      *http.ServeMux
	routes   []Route
	globalMW []Middleware
	routeMW  []Middleware
}

// Middleware wraps a handler.
type Middleware func(http.Handler) http.Handler

// NewRouter creates a router. Both slices are applied outermost-first.
func NewRouter(global, perRoute []Middleware) *Router {
	rt := &Router{
		mux:      http.NewServeMux(),
		globalMW: global,
		routeMW:  perRoute,
	}

	// An unmatched path must be BYTE-IDENTICAL to a registered route the caller
	// may not have. Without this it is not: the mux's built-in 404 is
	// net/http's, with a text/plain body and no Cache-Control, while a denial
	// goes through writeProblem. Either difference is a fingerprint — probe a
	// path, and the shape of the 404 tells you whether the route exists, which
	// is exactly what the hidden admin surface (§7.3) is supposed to withhold.
	//
	// A bare "/" is the mux's catch-all; "GET /{$}" is more specific and still
	// wins for the root itself.
	rt.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, http.StatusNotFound, "not found")
	})

	return rt
}

// chain applies middleware outermost-first.
func chain(mw []Middleware, h http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// register is the single registration path. Everything goes through it so no
// route can exist without being recorded.
func (rt *Router) register(route Route, h http.Handler) {
	if route.Access == AccessAnonymous && !IsAllowlisted(route.Method, route.Pattern) {
		// A developer cannot make a route anonymous simply by asking. It must
		// also appear in the literal allowlist, which makes every anonymous
		// route a reviewable line in a diff.
		panic(fmt.Sprintf(
			"api: route %s was registered as anonymous but is not in AnonymousAllowlist; "+
				"add it there deliberately or register it as authenticated", route.ID()))
	}
	if route.Access != AccessAnonymous && IsAllowlisted(route.Method, route.Pattern) {
		panic(fmt.Sprintf(
			"api: route %s is in AnonymousAllowlist but was registered as authenticated; "+
				"the allowlist and the registration disagree", route.ID()))
	}
	if route.Access == AccessPermission && route.Permission == "" {
		panic(fmt.Sprintf("api: route %s requires a permission but none was given", route.ID()))
	}
	if route.Browser {
		if route.Hidden {
			panic(fmt.Sprintf(
				"api: route %s is both Hidden and Browser; a redirect would confirm the "+
					"existence of a route whose whole purpose is to stay invisible", route.ID()))
		}
		if route.Method != http.MethodGet {
			panic(fmt.Sprintf(
				"api: route %s is Browser but not a GET; only a navigation can be redirected",
				route.ID()))
		}
		if route.Access == AccessAnonymous {
			panic(fmt.Sprintf(
				"api: route %s is anonymous, so it is never denied and Browser does nothing",
				route.ID()))
		}
	}

	for _, existing := range rt.routes {
		if existing.ID() == route.ID() {
			panic(fmt.Sprintf("api: route %s is registered twice", route.ID()))
		}
	}

	// Comparing function pointers: notImplemented is a top-level func, so every
	// reference to it has the same code pointer. This is the one thing a
	// reflect-based check is straightforwardly right about.
	route.Stub = reflect.ValueOf(h).Pointer() == reflect.ValueOf(http.HandlerFunc(notImplemented)).Pointer()

	rt.routes = append(rt.routes, route)
	// withRoute is outermost of the per-route chain: it publishes the matched
	// route into the context that the chain below it reads.
	rt.mux.Handle(route.ID(), withRoute(route, chain(rt.routeMW, h)))
}

// Anonymous registers a route reachable without a session.
func (rt *Router) Anonymous(method, pattern string, h http.HandlerFunc) {
	rt.register(Route{Method: method, Pattern: pattern, Access: AccessAnonymous}, h)
}

// Authenticated registers a route requiring an active, MFA-satisfied caller.
// Both sessions and API tokens qualify.
func (rt *Router) Authenticated(method, pattern string, h http.HandlerFunc) {
	rt.register(Route{Method: method, Pattern: pattern, Access: AccessAuthenticated}, h)
}

// SessionRoute registers a route that an API token may not use, whatever its
// scope. Credential management lives here.
func (rt *Router) SessionRoute(method, pattern string, h http.HandlerFunc) {
	rt.register(Route{
		Method: method, Pattern: pattern,
		Access: AccessAuthenticated, SessionOnly: true,
	}, h)
}

// Enrollment registers a route reachable by a session in the awaiting_mfa
// state. Only MFA enrollment and logout use this, and never an API token.
func (rt *Router) Enrollment(method, pattern string, h http.HandlerFunc) {
	rt.register(Route{
		Method: method, Pattern: pattern,
		Access: AccessEnrollment, SessionOnly: true,
	}, h)
}

// Page registers an HTML page an authenticated person navigates to. A denial
// redirects rather than returning JSON — see Route.Browser.
func (rt *Router) Page(pattern string, h http.HandlerFunc) {
	rt.register(Route{
		Method: http.MethodGet, Pattern: pattern,
		Access: AccessAuthenticated, SessionOnly: true, Browser: true,
	}, h)
}

// Permission registers a route requiring a specific permission.
func (rt *Router) Permission(method, pattern string, perm authz.Permission, h http.HandlerFunc) {
	rt.register(Route{
		Method: method, Pattern: pattern,
		Access: AccessPermission, Permission: perm,
	}, h)
}

// Admin registers a route that requires a permission AND returns 404 rather
// than 403 to an unauthorized caller, so its existence is not disclosed.
func (rt *Router) Admin(method, pattern string, perm authz.Permission, h http.HandlerFunc) {
	rt.register(Route{
		Method: method, Pattern: pattern,
		Access: AccessPermission, Permission: perm, Hidden: true,
	}, h)
}

// Routes returns every registered route, sorted. This is what makes the
// enumeration test possible.
func (rt *Router) Routes() []Route {
	out := make([]Route, len(rt.routes))
	copy(out, rt.routes)
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// ServeHTTP applies the global middleware and dispatches to the mux, which in
// turn applies the per-route chain.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	chain(rt.globalMW, rt.mux).ServeHTTP(w, r)
}

// Handler returns the fully wrapped handler.
func (rt *Router) Handler() http.Handler { return rt }

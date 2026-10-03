package api

import "fmt"

// AnonymousAllowlist is the complete set of routes reachable without a valid
// session. It is a literal list, not a prefix rule and not a pattern, because
// a pattern is how "/api/v1/*" accidentally becomes public.
//
// Requirements §7.1. Adding an entry here is a security decision and should be
// visible in a diff as one.
//
// Note what is deliberately absent: the application shell, its JS bundle,
// artwork, metadata, HLS manifests and segments, iCal and RSS feeds, and every
// /api/v1 route other than the auth endpoints below.
var AnonymousAllowlist = map[string]struct{}{
	"GET /login":                  {},
	"POST /api/v1/auth/login":     {},
	"POST /api/v1/auth/login/mfa": {},
	"GET /signup":                 {},
	"POST /api/v1/auth/signup":    {},
	// The signup's proof-of-work challenge (ADR-0051): sealed, nothing kept,
	// and 404 whenever signup is.
	"GET /api/v1/auth/signup/challenge": {},
	"GET /reset":                        {},
	"POST /api/v1/auth/reset/initiate":  {},
	"POST /api/v1/auth/reset/complete":  {},
	"GET /assets/auth/":                 {},
	"GET /healthz":                      {},

	// First run only. Both handlers check the account count on every request
	// and return 404 once any account exists, so the wizard closes itself the
	// instant the first administrator is created. There is no default account
	// and no bootstrap credential written to disk or logs.
	"GET /setup":         {},
	"POST /api/v1/setup": {},

	// The calendar and the feed (ADR-0041). Their clients send an address and
	// nothing else, so the token in the path is the credential, checked by the
	// handler; a wrong, revoked or suspended one is 404. Rate-limited.
	"GET /api/v1/feeds/{token}/calendar.ics": {},
	"GET /api/v1/feeds/{token}/rss":          {},
}

// RouteID is the canonical "METHOD /pattern" identifier used by the allowlist,
// the audit log and the enumeration test.
func RouteID(method, pattern string) string {
	return fmt.Sprintf("%s %s", method, pattern)
}

// IsAllowlisted reports whether a route may be reached anonymously.
func IsAllowlisted(method, pattern string) bool {
	_, ok := AnonymousAllowlist[RouteID(method, pattern)]
	return ok
}

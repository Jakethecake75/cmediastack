package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/logging"
)

type ctxKey int

const (
	ctxKeyRoute ctxKey = iota
	ctxKeyClientIP
	ctxKeyCSPNonce
)

// withRoute attaches the matched route to the request context so the
// authorization middleware can act on route identity rather than re-parsing
// the URL. Re-deriving a route from a URL string is how path-normalization
// bugs turn into authorization bypasses.
func withRoute(route Route, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), ctxKeyRoute, route)
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RouteFromContext returns the matched route.
func RouteFromContext(ctx context.Context) (Route, bool) {
	route, ok := ctx.Value(ctxKeyRoute).(Route)
	return route, ok
}

// ClientIP returns the resolved client address.
func ClientIP(ctx context.Context) string {
	ip, _ := ctx.Value(ctxKeyClientIP).(string)
	return ip
}

// CSPNonce returns the per-response nonce for inline script tags.
func CSPNonce(ctx context.Context) string {
	n, _ := ctx.Value(ctxKeyCSPNonce).(string)
	return n
}

// Authenticator resolves a request to a principal. Returning (nil, nil) means
// anonymous; returning an error means the credential was present but invalid.
//
// It takes the ResponseWriter because a session may rotate its secret during
// resolution, and the replacement cookie has to reach the client on the same
// response that used the old one.
type Authenticator interface {
	Authenticate(w http.ResponseWriter, r *http.Request) (*authz.Principal, error)
}

// AuditSink records authorization denials. The HTTP response tells the client
// nothing; this is where the truth goes.
type AuditSink interface {
	AuthzDenied(ctx context.Context, route string, d *authz.Denial, clientIP, userAgent string)
}

// ---------------------------------------------------------------------------
// 1. Panic recovery
// ---------------------------------------------------------------------------

// Recovery converts a panic into a 500 without leaking the stack to the client.
func Recovery(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if p := recover(); p != nil {
					logging.FromContext(r.Context()).Error("panic in handler",
						slog.Any("panic", p),
						slog.String("path", r.URL.Path))
					writeProblem(w, http.StatusInternalServerError, "internal error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ---------------------------------------------------------------------------
// 2. Request ID and log context
// ---------------------------------------------------------------------------

// RequestContext attaches a request ID and a logger.
func RequestContext(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := randomHex(8)
			ctx := logging.WithLogger(logging.WithRequestID(r.Context(), id), logger)
			w.Header().Set("X-Request-Id", id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ---------------------------------------------------------------------------
// 3. Trusted proxy resolution
// ---------------------------------------------------------------------------

// ClientIPResolver resolves the real client address.
//
// X-Forwarded-For is honoured ONLY when the immediate peer is in trustedCIDRs.
// If a reverse proxy is misconfigured to forward a client-supplied header, or
// if the app is exposed directly, every per-IP rate limit becomes spoofable by
// setting a header. Defaulting to "trust nothing" is the only safe default.
func ClientIPResolver(trustedCIDRs []string) Middleware {
	prefixes := parsePrefixes(trustedCIDRs)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer := hostOnly(r.RemoteAddr)
			resolved := peer

			if len(prefixes) > 0 && addrInAny(peer, prefixes) {
				if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
					// Right-most untrusted entry is the real client.
					parts := strings.Split(xff, ",")
					for i := len(parts) - 1; i >= 0; i-- {
						candidate := strings.TrimSpace(parts[i])
						if candidate == "" {
							continue
						}
						if !addrInAny(candidate, prefixes) {
							resolved = candidate
							break
						}
					}
				}
			}

			ctx := context.WithValue(r.Context(), ctxKeyClientIP, resolved)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func parsePrefixes(cidrs []string) []netip.Prefix {
	var out []netip.Prefix
	for _, c := range cidrs {
		if p, err := netip.ParsePrefix(c); err == nil {
			out = append(out, p)
			continue
		}
		if a, err := netip.ParseAddr(c); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return out
}

func addrInAny(s string, prefixes []netip.Prefix) bool {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return false
	}
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func hostOnly(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// ---------------------------------------------------------------------------
// 4. Security headers
// ---------------------------------------------------------------------------

// SecurityHeaders sets the response headers required by §8.
//
// The CSP is nonce-based with no unsafe-inline: script-src permits only tags
// carrying this response's nonce, so an injected <script> cannot execute even
// if output encoding fails somewhere.
// castScripts is where the Cast sender library, the framework it loads and
// Chrome's own sender for this version come from (ADR-0077).
const castScripts = "https://www.gstatic.com/cv/js/sender/ https://www.gstatic.com/cast/sdk/libs/ " +
	"https://www.gstatic.com/eureka/clank/"

func SecurityHeaders(hstsMaxAge time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nonce := randomHex(16)
			h := w.Header()

			h.Set("Content-Security-Policy", strings.Join([]string{
				"default-src 'self'",
				// Google's Cast sender, by its three paths and nothing else on
				// that host: the player loads it to cast (ADR-0077).
				"script-src 'self' 'nonce-" + nonce + "' " + castScripts,
				"style-src 'self' 'nonce-" + nonce + "'",
				"img-src 'self' data:",
				"media-src 'self' blob:",
				"connect-src 'self'",
				"font-src 'self'",
				"object-src 'none'",
				"base-uri 'none'",
				"form-action 'self'",
				"frame-ancestors 'none'",
			}, "; "))
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			h.Set("Cross-Origin-Resource-Policy", "same-origin")
			if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
				h.Set("Strict-Transport-Security",
					"max-age="+itoa(int(hstsMaxAge.Seconds()))+"; includeSubDomains; preload")
			}

			ctx := context.WithValue(r.Context(), ctxKeyCSPNonce, nonce)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ---------------------------------------------------------------------------
// 5. Rate limiting
// ---------------------------------------------------------------------------

// RateLimiter is a fixed-window counter keyed by class and client.
//
// A fixed window is chosen over a token bucket because the limits that matter
// here are "N per hour" style caps on expensive anonymous endpoints, where
// burst smoothing is not the point and auditability is.
type RateLimiter struct {
	mu      sync.Mutex
	windows map[string]*window
	now     func() time.Time
}

type window struct {
	count int
	reset time.Time
}

// NewRateLimiter builds a limiter. now is injectable for tests.
func NewRateLimiter(now func() time.Time) *RateLimiter {
	if now == nil {
		now = time.Now
	}
	return &RateLimiter{windows: map[string]*window{}, now: now}
}

// Allow records an attempt and reports whether it is within limit.
func (rl *RateLimiter) Allow(key string, limit int, period time.Duration) bool {
	if limit <= 0 {
		return false
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	w, ok := rl.windows[key]
	if !ok || now.After(w.reset) {
		rl.windows[key] = &window{count: 1, reset: now.Add(period)}
		return true
	}
	w.count++
	return w.count <= limit
}

// Reap drops expired windows. Called periodically so the map does not grow
// without bound under a distributed scan.
func (rl *RateLimiter) Reap() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.now()
	for k, w := range rl.windows {
		if now.After(w.reset) {
			delete(rl.windows, k)
		}
	}
}

// RateLimitRule describes the limit for a class of routes.
type RateLimitRule struct {
	Limit  int
	Period time.Duration
}

// RateLimit applies per-route-class limits keyed by client IP.
func RateLimit(rl *RateLimiter, rules map[string]RateLimitRule) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route, ok := RouteFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			rule, ok := rules[route.ID()]
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			key := route.ID() + "|" + ClientIP(r.Context())
			if !rl.Allow(key, rule.Limit, rule.Period) {
				w.Header().Set("Retry-After", itoa(int(rule.Period.Seconds())))
				writeProblem(w, http.StatusTooManyRequests, "too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------------------------------------------------------------------------
// 6 & 7. Authentication, the anonymous allowlist gate, and the MFA gate
// ---------------------------------------------------------------------------

// Authenticate resolves the request's principal and enforces the anonymous
// allowlist and the MFA enrollment gate.
//
// The ordering is the security property:
//
//   - A route not in the allowlist and a nil principal is a 404. Not a 401,
//     not a redirect with a helpful message: nothing about the application's
//     shape is disclosed to an unauthenticated caller.
//   - An awaiting_mfa principal reaches only the enrollment routes.
//   - Only then does the handler run, and only then does object-level
//     authorization inside the repository get a chance to matter.
func Authenticate(auth Authenticator, sink AuditSink) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route, matched := RouteFromContext(r.Context())
			if !matched {
				// An unmatched path never reached a registered route.
				writeProblem(w, http.StatusNotFound, "not found")
				return
			}

			principal, err := auth.Authenticate(w, r)
			if err != nil {
				// A present-but-invalid credential is treated as anonymous.
				// Distinguishing "expired" from "forged" tells an attacker
				// which of their guesses was structurally correct.
				principal = nil
			}
			ctx := authz.WithPrincipal(r.Context(), principal)
			r = r.WithContext(ctx)

			if route.Access == AccessAnonymous {
				next.ServeHTTP(w, r)
				return
			}

			if principal == nil {
				denyRoute(w, r, route, &authz.Denial{
					Reason: authz.ReasonAnonymous,
					Detail: "no session presented",
				}, sink)
				return
			}

			// Credential-management routes refuse tokens outright, before any
			// permission check: no scope makes a token acceptable here.
			if route.SessionOnly && principal.IsToken() {
				denyRoute(w, r, route, &authz.Denial{
					Actor:  principal.UserID,
					Reason: authz.ReasonTokenNotPermitted,
					Detail: "route " + route.ID() + " requires an interactive session",
				}, sink)
				return
			}

			// The enrollment gate. An approved account that has not enrolled an
			// authenticator can reach exactly one thing.
			if principal.State == authz.StateAwaitingMFA {
				if route.Access == AccessEnrollment {
					next.ServeHTTP(w, r)
					return
				}
				if route.Browser {
					redirectTo(w, EnrollPath)
					return
				}
				writeProblem(w, http.StatusConflict, "mfa_enrollment_required")
				return
			}

			if !principal.CanAct() {
				denyRoute(w, r, route, &authz.Denial{
					Actor:  principal.UserID,
					Reason: authz.ReasonNotActive,
					Detail: "account state is " + string(principal.State),
				}, sink)
				return
			}

			if route.Access == AccessPermission {
				if err := authz.RequirePermission(ctx, route.Permission); err != nil {
					d, _ := authz.AsDenial(err)
					denyRoute(w, r, route, d, sink)
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// Paths the browser is sent to when a navigation is denied. Both are anonymous
// routes, so pointing at them discloses nothing that /login does not already.
const (
	LoginPath  = "/login"
	EnrollPath = "/enroll"
)

// denyRoute renders a denial. Hidden routes and anonymous callers get 404 so
// that the administration surface is not merely forbidden but invisible
// (requirements §7.3); the real reason goes to the audit log.
func denyRoute(w http.ResponseWriter, r *http.Request, route Route, d *authz.Denial, sink AuditSink) {
	if sink != nil && d != nil {
		sink.AuthzDenied(r.Context(), route.ID(), d, ClientIP(r.Context()), r.UserAgent())
	}
	// A page navigation by somebody with no usable session is the case §2
	// describes: they get the login screen. The audit line above is written
	// first, so the friendlier response does not cost the record.
	//
	// ReasonNotActive redirects too, and that is the point rather than a
	// convenience: a suspended account must not be able to tell that its
	// credential was recognized. Answering 403 here while an unknown caller
	// gets a redirect would be exactly that oracle. A token is different — it
	// gets the plain 403, because a token never navigates.
	if route.Browser && (d == nil || d.Reason == authz.ReasonAnonymous || d.Reason == authz.ReasonNotActive) {
		redirectTo(w, LoginPath)
		return
	}
	if route.Hidden || d == nil || d.Reason == authz.ReasonAnonymous {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	writeProblem(w, http.StatusForbidden, "forbidden")
}

// redirectTo sends a browser to a fixed, in-tree path.
//
// The destination is always a constant from this file, never anything derived
// from the request: no "?next=" parameter, no Referer. An open redirect on a
// login flow is a phishing primitive, and the way to not have one is to have no
// caller-supplied destination at all. 303 rather than 302 so the method is
// reset to GET regardless of what was attempted.
func redirectTo(w http.ResponseWriter, path string) {
	w.Header().Set("Location", path)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// CSRF
// ---------------------------------------------------------------------------

// CSRFCookieName is the double-submit cookie.
const CSRFCookieName = "cms_csrf"

// CSRFHeaderName carries the matching value.
const CSRFHeaderName = "X-CSRF-Token"

// CSRF enforces double-submit on state-changing requests.
//
// SameSite=Lax on the session cookie is the first layer; this is the second,
// because SameSite is a browser behaviour and not every client is a browser
// that implements it the same way.
//
// Requests carrying an Authorization header are exempt, and that is not a hole:
// CSRF exists because cookies are AMBIENT — a browser attaches them to a
// cross-site request the user never intended to make. An Authorization header
// is not ambient. A cross-origin page cannot set one without a CORS preflight
// this server never approves, so there is nothing for a double-submit token to
// defend against. Requiring one anyway would make API tokens unusable for every
// POST while adding no security.
func CSRF() Middleware {
	safe := map[string]bool{http.MethodGet: true, http.MethodHead: true, http.MethodOptions: true}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if safe[r.Method] {
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := bearerToken(r); ok {
				next.ServeHTTP(w, r)
				return
			}
			cookie, err := r.Cookie(CSRFCookieName)
			if err != nil || cookie.Value == "" {
				writeProblem(w, http.StatusForbidden, "missing csrf token")
				return
			}
			header := r.Header.Get(CSRFHeaderName)
			if header == "" || !constantTimeEqual(header, cookie.Value) {
				writeProblem(w, http.StatusForbidden, "csrf token mismatch")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is unrecoverable; a predictable nonce or request
		// ID is worse than a crash.
		panic("api: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

// MetricsRecorder is the subset of the metrics registry this package needs.
type MetricsRecorder interface {
	ObserveHTTP(route, method string, status int, d time.Duration)
}

// statusRecorder captures the status code for the metrics middleware.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer, which
// streaming (Flush, Hijack) will need in Phase 4.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Metrics records request counts and durations.
//
// It is per-route middleware so the label is the route PATTERN, not the raw
// URL. Labelling by raw path would let any caller create unbounded time series
// by requesting /api/v1/media/1, /2, /3 — a cardinality bomb that is a denial
// of service against the monitoring system rather than the application.
func Metrics(rec MetricsRecorder) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route, ok := RouteFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			start := time.Now()
			next.ServeHTTP(sr, r)
			rec.ObserveHTTP(route.ID(), r.Method, sr.status, time.Since(start))
		})
	}
}

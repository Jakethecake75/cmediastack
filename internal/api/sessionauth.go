package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/platform/logging"
)

// SessionAuthenticator resolves the session cookie into a principal.
//
// It is the only place a request becomes an identity. Everything downstream —
// the allowlist gate, the MFA gate, route permissions, object-level scoping —
// acts on what this returns, and it returns nil for anything it cannot fully
// verify.
type SessionAuthenticator struct {
	store  *identity.Store
	cfg    identity.SessionConfig
	secure bool
}

// NewSessionAuthenticator builds the authenticator. secure marks the cookie
// Secure; it should be false only for plain-HTTP local development.
func NewSessionAuthenticator(store *identity.Store, cfg identity.SessionConfig, secure bool) *SessionAuthenticator {
	return &SessionAuthenticator{store: store, cfg: cfg, secure: secure}
}

// Authenticate implements Authenticator.
//
// A Bearer token is tried first and, if present, is the whole answer: a request
// that supplies one is not silently upgraded to the cookie's session if the
// token turns out to be bad. Falling through would let an attacker with a
// victim's browser cookie but a junk token get session privileges on a route
// they meant to use a narrow token for.
func (a *SessionAuthenticator) Authenticate(w http.ResponseWriter, r *http.Request) (*authz.Principal, error) {
	if raw, ok := bearerToken(r); ok {
		tok, user, err := a.store.ResolveAPIToken(r.Context(), raw)
		if err != nil {
			// Anonymous, which every protected route refuses; never fall back
			// to the cookie.
			return nil, nil //nolint:nilerr // a token that does not resolve is no credential at all
		}
		return a.store.BuildTokenPrincipal(r.Context(), tok, user)
	}

	cookie, err := r.Cookie(identity.SessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, nil //nolint:nilerr // no cookie is no credential: anonymous
	}

	sess, user, rotated, err := a.store.ResolveSession(r.Context(), cookie.Value, a.cfg)
	if err != nil {
		// Token reuse is the one failure worth shouting about: it means a
		// superseded session secret was presented, which does not happen by
		// accident. The session has already been revoked by ResolveSession.
		if errors.Is(err, identity.ErrSessionReuse) {
			logging.FromContext(r.Context()).Warn("session token reuse detected; session revoked",
				"client_ip", ClientIP(r.Context()))
		}
		a.clearCookie(w)
		return nil, nil
	}

	if rotated != "" {
		a.SetSessionCookie(w, rotated)
	}

	return a.store.BuildPrincipal(r.Context(), sess, user)
}

// bearerToken extracts an Authorization: Bearer credential.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}

// SetSessionCookie writes the session cookie.
//
// HttpOnly keeps it away from JavaScript, so an XSS that gets past the CSP
// still cannot read it. SameSite=Lax is the first CSRF layer; the double-submit
// token is the second. Path=/ because the whole application needs it.
//
// Secure comes from the configuration (a.secure) rather than being a literal:
// it is true for every deployment but plain HTTP to http://localhost, where a
// Secure cookie would make the development login impossible. That is why the
// SAST rule below is suppressed, not because the flag is optional.
func (a *SessionAuthenticator) SetSessionCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- HttpOnly and SameSite are set; Secure is a.secure, true except on http://localhost
		Name:     identity.SessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		// No Expires or MaxAge: this is a session cookie, and the server's
		// idle and absolute timeouts are the real lifetime. A cookie that
		// outlives its server-side session is just a stale credential.
	})
}

func (a *SessionAuthenticator) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- clears the cookie SetSessionCookie set, with the same attributes
		Name:     identity.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

// ClearSessionCookie removes the session cookie, used on logout.
func (a *SessionAuthenticator) ClearSessionCookie(w http.ResponseWriter) { a.clearCookie(w) }

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/identity"
)

// ADR-0080: staying signed in.

func sessionCookie(t *testing.T, h http.Header) *http.Cookie {
	t.Helper()
	for _, c := range (&http.Response{Header: h}).Cookies() {
		if c.Name == identity.SessionCookieName {
			return c
		}
	}
	return nil
}

// The cookie used to be a browser-session cookie, dropped whenever the
// browser or the television's app was closed. It now lasts as long as the
// session can, and a rotated one as long as is left.
func TestTheSessionCookieLastsAsLongAsTheSession(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin() // the rig's sessions last 30 days

	c := r.client()
	c.visitPage("/login")
	res := c.post("/api/v1/auth/login", map[string]any{
		"username": "jacob", "password": "correct-horse-battery",
	})
	ck := sessionCookie(t, res.Header)
	if ck == nil {
		t.Fatalf("login set no session cookie: %d %s", res.Code, res.Raw)
	}
	if want := int((30 * 24 * time.Hour).Seconds()); ck.MaxAge != want {
		t.Errorf("login's cookie Max-Age = %d, want %d", ck.MaxAge, want)
	}

	r.clk.advance(16 * time.Minute)
	res = admin.get("/api/v1/me")
	ck = sessionCookie(t, res.Header)
	if ck == nil {
		t.Fatalf("no rotation after 16 minutes: %d %s", res.Code, res.Raw)
	}
	if want := int((30*24*time.Hour - 16*time.Minute).Seconds()); ck.MaxAge != want {
		t.Errorf("a rotated cookie's Max-Age = %d, want %d", ck.MaxAge, want)
	}
}

// getLost sends a GET with the client's cookies and one Sec-Fetch-Dest, and
// does NOT keep what comes back: a cancelled request whose answer never
// reached the browser.
func (c *client) getLost(path, dest string) int {
	c.rig.t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, nil)
	req.RemoteAddr = c.ip + ":40000"
	req.Header.Set("Sec-Fetch-Dest", dest)
	for k, v := range c.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	rec := httptest.NewRecorder()
	c.rig.rt.ServeHTTP(rec, req)
	return rec.Code
}

// A browser cancels film and picture requests whenever the viewer seeks or
// scrolls. A secret rotated in one of those answers was lost, and the old
// one, presented after the grace, revoked the session as stolen. They do
// not rotate.
func TestAFilmOrPictureRequestNeverRotatesTheSession(t *testing.T) {
	for _, dest := range []string{"video", "audio", "track", "image"} {
		t.Run(dest, func(t *testing.T) {
			r := newRig(t)
			admin := r.bootstrapAdmin()

			r.clk.advance(16 * time.Minute)
			if code := admin.getLost("/api/v1/me", dest); code != http.StatusOK {
				t.Fatalf("the %s request failed: %d", dest, code)
			}
			r.clk.advance(2 * time.Minute)
			if res := admin.get("/api/v1/me"); res.Code != http.StatusOK {
				t.Errorf("signed out after a lost %s answer: %d", dest, res.Code)
			}
		})
	}
}

// The app's own requests still rotate: the change is for what a browser
// cancels, not a way out of rotation.
func TestTheAppsOwnRequestsStillRotate(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	old := admin.cookies[identity.SessionCookieName]

	r.clk.advance(16 * time.Minute)
	if code := admin.getLost("/api/v1/me", "empty"); code != http.StatusOK {
		t.Fatalf("the request failed: %d", code)
	}
	// Its answer was lost, so the old secret, past the grace, is a replay.
	r.clk.advance(2 * time.Minute)
	admin.cookies[identity.SessionCookieName] = old
	if res := admin.get("/api/v1/me"); res.Code != http.StatusNotFound {
		t.Errorf("a fetch did not rotate the session: %d", res.Code)
	}
}

// A code entered more than 15 minutes after the password crosses a
// rotation; the new secret has to reach the browser (the middleware sends
// it), or the session would be revoked as stolen at the next request after
// the grace.
func TestACodeEnteredLateKeepsTheSession(t *testing.T) {
	r := newRig(t)
	r.bootstrapAdmin()

	c := r.client()
	c.visitPage("/login")
	if res := c.post("/api/v1/auth/login", map[string]any{
		"username": "jacob", "password": "correct-horse-battery",
	}); res.Code != http.StatusOK {
		t.Fatalf("login: %d %s", res.Code, res.Raw)
	}

	r.clk.advance(16 * time.Minute)
	if res := c.post("/api/v1/auth/login/mfa", map[string]any{"code": r.mustCode(r.adminSecret)}); res.Code != http.StatusOK {
		t.Fatalf("mfa: %d %s", res.Code, res.Raw)
	}

	r.clk.advance(2 * time.Minute)
	if res := c.get("/api/v1/me"); res.Code != http.StatusOK {
		t.Errorf("signed out after a late code: %d %s", res.Code, res.Raw)
	}
}

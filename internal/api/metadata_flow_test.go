package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/metadata"
)

// The metadata provider's administration surface.
//
// The property worth most here is a negative one: no endpoint returns the
// stored credential, in any form, to anybody.

// fakeMetadata records what it was asked and answers as told.
type fakeMetadata struct {
	// tokens records EVERY token handed to SetToken, including the ones that
	// were rejected. A recorder that only kept accepted values could not tell
	// "the bad key was refused" from "SetToken was never reached".
	tokens  []string
	stored  string
	health  metadata.Health
	err     error
	status  metadata.Status
	checks  int
	matches []metadata.Match
}

func (f *fakeMetadata) Status(context.Context) (metadata.Status, error) {
	return f.status, nil
}

func (f *fakeMetadata) SetToken(_ context.Context, token string) (metadata.Health, error) {
	f.tokens = append(f.tokens, token)
	if f.err != nil {
		return f.health, f.err
	}
	f.stored = token
	f.status = metadata.Status{Configured: token != "", Provider: "tmdb", Health: f.health}
	return f.health, nil
}

func (f *fakeMetadata) Check(context.Context) (metadata.Health, error) {
	f.checks++
	return f.health, f.err
}

func (f *fakeMetadata) Search(context.Context, metadata.Query) ([]metadata.Match, error) {
	return f.matches, f.err
}

func (f *fakeMetadata) SearchToRequest(context.Context, metadata.Query) ([]metadata.Match, error) {
	return f.matches, f.err
}

func (f *fakeMetadata) Details(context.Context, metadata.Kind, int64) (metadata.Details, error) {
	return metadata.Details{}, f.err
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// withMetadata rebuilds the router with a metadata service attached.
func (r *rig) withMetadata(f *fakeMetadata) {
	r.t.Helper()
	auth := NewSessionAuthenticator(r.store, r.svc.Policy().Session, false)
	rt := NewRouter(
		[]Middleware{Recovery(quietLogger()), RequestContext(quietLogger()),
			ClientIPResolver(nil), SecurityHeaders(time.Hour)},
		[]Middleware{CSRF(), Authenticate(auth, r.audit)},
	)
	RegisterRoutes(rt, New(Deps{
		Identity: r.svc, Auth: auth, Egress: r.egress, Indexers: r.indexers,
		Profiles: r.profiles, Roots: r.roots, Media: r.media,
		Scanner: r.scanner, Deleter: r.scanner, Tickets: r.tickets,
		Requests: r.requests, Metadata: f, Audit: r.audit,
		TrashRetention: 7 * 24 * time.Hour,
	}))
	r.rt = rt
}

// ---------------------------------------------------------------------------
// The credential is never readable
// ---------------------------------------------------------------------------

// A key that can be read back over HTTP is one screenshot from disclosure, and
// nobody needs to read it: what an operator wants to know is whether one is set
// and whether it works.
func TestTheMetadataCredentialIsNeverReturned(t *testing.T) {
	r := newRig(t)
	f := &fakeMetadata{health: metadata.Health{OK: true, Detail: "the key works"}}
	r.withMetadata(f)
	admin := r.bootstrapAdmin()

	const secret = "eyJhbGciOiJIUzI1NiJ9.a-real-looking-token.signature"
	if res := admin.do(http.MethodPut, "/api/v1/admin/metadata/token",
		map[string]any{"token": secret}); res.Code != http.StatusOK {
		t.Fatalf("set token: %d %s", res.Code, res.Raw)
	}
	if f.stored != secret {
		t.Fatalf("precondition: the service did not receive the token")
	}

	// Every endpoint that could plausibly carry it.
	for _, probe := range []struct {
		what string
		res  response
	}{
		{"GET /api/v1/admin/metadata", admin.get("/api/v1/admin/metadata")},
		{"POST .../check", admin.post("/api/v1/admin/metadata/check", nil)},
		{"the PUT response itself", admin.do(http.MethodPut,
			"/api/v1/admin/metadata/token", map[string]any{"token": secret})},
	} {
		if strings.Contains(probe.res.Raw, secret) {
			t.Errorf("%s returned the credential: %s", probe.what, probe.res.Raw)
		}
		// Not even a fragment of it. A masked key still discloses its shape and
		// its first characters, which is how people confirm a guess.
		if strings.Contains(probe.res.Raw, secret[:12]) {
			t.Errorf("%s returned part of the credential: %s", probe.what, probe.res.Raw)
		}
	}
}

// ---------------------------------------------------------------------------
// A key that does not work is not stored
// ---------------------------------------------------------------------------

// Save-then-test leaves an instance configured with something broken and an
// operator who has to remember to read a health line. The answer must say so
// explicitly, or they go looking for a way to remove a key that was never
// saved.
func TestARejectedKeyIsNotStoredAndTheAnswerSaysSo(t *testing.T) {
	r := newRig(t)
	f := &fakeMetadata{
		err:    metadata.ErrUnauthorized,
		health: metadata.Health{OK: false, Detail: "TMDB wants a v4 Read Access Token"},
	}
	r.withMetadata(f)
	admin := r.bootstrapAdmin()

	res := admin.do(http.MethodPut, "/api/v1/admin/metadata/token",
		map[string]any{"token": "a-v3-key"})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("a rejected key returned %d, want 400: %s", res.Code, res.Raw)
	}
	if !strings.Contains(res.Raw, "Nothing was stored") {
		t.Errorf("the answer does not say the key was not stored: %s", res.Raw)
	}
	// The provider's own explanation reaches the operator.
	if !strings.Contains(res.Raw, "v4 Read Access Token") {
		t.Errorf("the diagnosis was discarded: %s", res.Raw)
	}
	// It reached the service — so this passes because the key was REFUSED, not
	// because the request never arrived.
	if len(f.tokens) != 1 || f.tokens[0] != "a-v3-key" {
		t.Errorf("SetToken saw %v", f.tokens)
	}
	if f.stored != "" {
		t.Errorf("a rejected key was stored: %q", f.stored)
	}
}

// A provider that is unreachable is a different job for whoever is reading than
// a key that is wrong, and collapsing them sends somebody to check the wrong
// thing.
func TestProviderFailuresAreDistinguished(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{metadata.ErrUnauthorized, http.StatusBadRequest},
		{metadata.ErrRateLimited, http.StatusServiceUnavailable},
		{metadata.ErrUnavailable, http.StatusBadGateway},
	} {
		r := newRig(t)
		r.withMetadata(&fakeMetadata{err: c.err})
		admin := r.bootstrapAdmin()

		res := admin.do(http.MethodPut, "/api/v1/admin/metadata/token",
			map[string]any{"token": "x"})
		if res.Code != c.want {
			t.Errorf("%v returned %d, want %d: %s", c.err, res.Code, c.want, res.Raw)
		}
	}
}

// "Is it working?" answered with "no, because X" is a SUCCESSFUL report, not a
// failed request. A 500 here would send an operator to look at this software
// when the problem is somewhere else.
func TestAFailingProviderIsReportedNotErrored(t *testing.T) {
	r := newRig(t)
	f := &fakeMetadata{
		err:    metadata.ErrUnauthorized,
		health: metadata.Health{OK: false, Detail: "the provider rejected the key"},
	}
	r.withMetadata(f)
	admin := r.bootstrapAdmin()

	res := admin.post("/api/v1/admin/metadata/check", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("check returned %d, want 200 with a health report: %s", res.Code, res.Raw)
	}
	health, _ := res.Body["health"].(map[string]any)
	if health["ok"] != false {
		t.Errorf("health.ok = %v, want false", health["ok"])
	}
	if !strings.Contains(res.Raw, "rejected the key") {
		t.Errorf("the detail did not reach the operator: %s", res.Raw)
	}
}

// ---------------------------------------------------------------------------
// Who may touch it
// ---------------------------------------------------------------------------

func TestOnlyAnAdministratorConfiguresTheProvider(t *testing.T) {
	r := newRig(t)
	f := &fakeMetadata{health: metadata.Health{OK: true}}
	r.withMetadata(f)
	admin := r.bootstrapAdmin()

	code, _ := r.issueInvite(admin, authz.RoleManager, true)
	manager := r.redeemAndEnroll(code, "morgan", "manager-passphrase-1")

	// Admin-gated routes answer 404, so the surface is invisible rather than
	// merely forbidden.
	for _, probe := range []response{
		manager.get("/api/v1/admin/metadata"),
		manager.do(http.MethodPut, "/api/v1/admin/metadata/token", map[string]any{"token": "x"}),
		manager.post("/api/v1/admin/metadata/check", nil),
	} {
		if probe.Code != http.StatusNotFound {
			t.Errorf("a manager reached the metadata surface: %d %s", probe.Code, probe.Raw)
		}
	}
	if len(f.tokens) != 0 {
		t.Errorf("a manager's token reached the service: %v", f.tokens)
	}

	// But a Manager MAY search the provider: that is library editing, which is
	// their job, and it is a different permission for that reason.
	if res := manager.get("/api/v1/metadata/search?kind=movie&title=arrival"); res.Code == http.StatusForbidden {
		t.Errorf("a manager cannot search metadata: %d %s", res.Code, res.Raw)
	}
}

func TestAMetadataSearchNeedsATitle(t *testing.T) {
	r := newRig(t)
	r.withMetadata(&fakeMetadata{})
	admin := r.bootstrapAdmin()

	if res := admin.get("/api/v1/metadata/search?kind=movie"); res.Code != http.StatusBadRequest {
		t.Errorf("an empty search returned %d, want 400", res.Code)
	}
}

// "ok: false" with an empty detail tells an operator that something is wrong
// and nothing about what — and here nothing IS wrong: an instance with no
// metadata provider is a supported configuration. Found by reading the
// binary's JSON, not by a test.
func TestAnUnconfiguredProviderExplainsItself(t *testing.T) {
	r := newRig(t)
	r.withMetadata(&fakeMetadata{})
	admin := r.bootstrapAdmin()

	res := admin.get("/api/v1/admin/metadata")
	if res.Code != http.StatusOK {
		t.Fatalf("status: %d %s", res.Code, res.Raw)
	}
	if res.Body["configured"] != false {
		t.Fatalf("precondition: reported as configured")
	}
	health, _ := res.Body["health"].(map[string]any)
	detail, _ := health["detail"].(string)
	if strings.TrimSpace(detail) == "" {
		t.Error("an unconfigured provider reports ok:false with no explanation, " +
			"which reads as a fault rather than as a choice not yet made")
	}
	if !strings.Contains(detail, "works without one") {
		t.Errorf("the detail does not say the instance is fine without one: %q", detail)
	}
}

// A key loaded at startup has not been checked. It is neither working nor
// broken, and the status says which it is rather than "ok: false" with nothing
// to add — which the admin screen showed as a broken key the first time it
// displayed one.
func TestAKeyNotYetCheckedSaysSoRatherThanBroken(t *testing.T) {
	r := newRig(t)
	f := &fakeMetadata{status: metadata.Status{Configured: true, Provider: "tmdb"}}
	r.withMetadata(f)
	admin := r.bootstrapAdmin()

	res := admin.get("/api/v1/admin/metadata")
	h, _ := res.Body["health"].(map[string]any)
	if res.Code != http.StatusOK || res.Body["checked"] != false ||
		!strings.Contains(h["detail"].(string), "not checked since the server started") {
		t.Fatalf("unchecked: %d %s", res.Code, res.Raw)
	}

	f.status.Health = metadata.Health{OK: true, Detail: "the key works", CheckedAt: time.Now()}
	res = admin.get("/api/v1/admin/metadata")
	h, _ = res.Body["health"].(map[string]any)
	if res.Body["checked"] != true || h["detail"] != "the key works" || h["ok"] != true {
		t.Fatalf("checked: %s", res.Raw)
	}
}

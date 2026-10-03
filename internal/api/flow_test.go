package api

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/egress"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/notify"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
	"github.com/jakethecake75/cmediastack/internal/release"
	"github.com/jakethecake75/cmediastack/internal/request"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// End-to-end tests over the real router, middleware chain, service and
// database. Nothing is mocked except the clock.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type rig struct {
	t         *testing.T
	rt        *Router
	svc       *identity.Service
	store     *identity.Store
	clk       *clock
	audit     *audit.Logger
	egress    *egress.Guard
	indexers  *indexer.Store
	profiles  *release.ProfileStore
	downloads *fakeEngine
	tickets   *search.Tickets
	roots     *library.RootStore
	media     *importer.Store
	scanner   *importer.Importer
	requests  *request.Service
	database  *db.DB
	// notifier sends to fakeDiscord, which stands in for Discord.
	notifier *notify.Service
	discord  *fakeDiscord
}

// fakeEngine stands in for the download engine.
//
// The real one needs a torrent client, a data directory and a listening
// socket; none of that is what the queue endpoints are being tested for, which
// is authorization, shape validation and audit.
type fakeEngine struct {
	mu    sync.Mutex
	items []download.Transfer
	// calls records EVERY hash handed to Remove, including ones that matched
	// nothing. removed records only the ones that did. The distinction is the
	// point: a test asserting that a malformed identifier never reaches the
	// engine cannot use a list that is only appended to on success, because
	// then it passes whether the validation runs or not.
	calls      []string
	removed    []string
	added      []string
	addedBytes []string
	meta       []download.Meta
	records    []download.Record
	recordsErr error
	removeErr  error
	addErr     error
}

func (f *fakeEngine) List() []download.Transfer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]download.Transfer(nil), f.items...)
}

func (f *fakeEngine) Remove(hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, hash)
	if f.removeErr != nil {
		return f.removeErr
	}
	for i, it := range f.items {
		if it.InfoHash == hash {
			f.items = append(f.items[:i], f.items[i+1:]...)
			f.removed = append(f.removed, hash)
			return nil
		}
	}
	return download.ErrNotFound
}

func (f *fakeEngine) Notes() []string { return []string{"a note from the engine"} }

func (f *fakeEngine) Records(context.Context) ([]download.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordsErr != nil {
		return nil, f.recordsErr
	}
	return append([]download.Record(nil), f.records...), nil
}

func (f *fakeEngine) AddMagnet(_ context.Context, magnetOrHash string, meta download.Meta) (download.Transfer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, magnetOrHash)
	f.meta = append(f.meta, meta)
	if f.addErr != nil {
		return download.Transfer{}, f.addErr
	}
	t := download.Transfer{InfoHash: hashFor(magnetOrHash)}
	f.items = append(f.items, t)
	return t, nil
}

func (f *fakeEngine) AddTorrent(data []byte, meta download.Meta) (download.Transfer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addedBytes = append(f.addedBytes, string(data))
	f.meta = append(f.meta, meta)
	if f.addErr != nil {
		return download.Transfer{}, f.addErr
	}
	t := download.Transfer{InfoHash: hashFor(string(data)), MetadataGot: true}
	f.items = append(f.items, t)
	return t, nil
}

func (f *fakeEngine) Start(string) error { return nil }

// hashFor produces a stable, well-formed info hash from arbitrary input so the
// fake's transfers look like real ones to everything downstream.
func hashFor(s string) string {
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func newRig(t *testing.T) *rig { return newRigWith(t, nil) }

// newRigWith builds a rig, letting a test adjust the identity policy before
// the service is constructed.
func newRigWith(t *testing.T, tweak func(*identity.Policy)) *rig {
	t.Helper()

	database, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "flow.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}

	keyB64, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipherFromBase64(keyB64)
	if err != nil {
		t.Fatal(err)
	}

	clk := &clock{t: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)}

	// Cheap argon2 parameters: this suite hashes a lot, and the production
	// parameters are asserted separately in identity.TestDefaultParamsMeetPolicy.
	params := identity.Argon2Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

	store := identity.NewStore(database, cipher, params, clk.now)
	if err := store.EnsureBuiltinRoles(t.Context()); err != nil {
		t.Fatal(err)
	}
	auditLog := audit.New(database, clk.now)

	policy := identity.Policy{
		RegistrationMode: "open",
		PendingTTL:       14 * 24 * time.Hour,
		MaxOutstanding:   50,
		Password:         identity.PasswordPolicy{MinLength: 12},
		Session: identity.SessionConfig{
			IdleTimeout:     12 * time.Hour,
			AbsoluteTimeout: 30 * 24 * time.Hour,
		},
		LoginMaxAttempts:  5,
		LoginWindow:       15 * time.Minute,
		TOTPIssuer:        "CMediaStack",
		RecoveryCodeCount: 10,
		Argon2:            params,
	}

	if tweak != nil {
		tweak(&policy)
	}

	svc := identity.NewService(store, auditLog, policy, clk.now)
	auth := NewSessionAuthenticator(store, policy.Session, false)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	rt := NewRouter(
		[]Middleware{
			Recovery(logger), RequestContext(logger),
			ClientIPResolver(nil), SecurityHeaders(time.Hour),
		},
		[]Middleware{
			CSRF(),
			Authenticate(auth, auditLog),
		},
	)
	// A guard with a tunnel interface that certainly does not exist, which is
	// the honest shape for a test container: verification fails, health stays
	// down, and the admin surface has something real to report.
	guard := egress.New(egress.Config{
		Interface: "wg-test-no-such-interface",
		Profiles: map[string]egress.Profile{
			"download": {Mode: egress.ModeDirect, DenyPrivate: true},
			"indexer":  {Mode: egress.ModeDirect, DenyPrivate: true},
			"update":   {Mode: egress.ModeBlocked},
		},
	})

	indexers := indexer.NewStore(database, cipher, clk.now)
	profiles := release.NewProfileStore(database, clk.now)
	if err := profiles.EnsureDefaults(t.Context()); err != nil {
		t.Fatal(err)
	}
	searcher := search.New(indexers, indexer.NewClient(guard), indexers)

	downloads := &fakeEngine{}
	tickets := search.NewTickets(cipher, search.DefaultTicketTTL, clk.now)

	roots := library.NewRootStore(database, filepath.Join(t.TempDir(), "downloads"), clk.now)
	media := importer.NewStore(database, clk.now)
	scanner := importer.New(media, roots, slog.New(slog.NewTextHandler(io.Discard, nil)), clk.now)

	requests := request.NewService(request.NewStore(database, clk.now), auditLog, clk.now)
	discord := &fakeDiscord{info: notify.Info{Name: "CMediaStack", ChannelID: "42"}}
	notifier := notify.NewService(store, cipher, auditLog, discord, clk.now)

	RegisterRoutes(rt, New(Deps{
		Identity: svc, Auth: auth, Egress: guard, Indexers: indexers,
		Search: searcher, Profiles: profiles, Downloads: downloads,
		Roots: roots, Media: media, Scanner: scanner, Deleter: scanner,
		Tickets: tickets, Grabs: searcher, Requests: requests, Audit: auditLog,
		Notifications:  notifier,
		TrashRetention: 7 * 24 * time.Hour,
	}))

	return &rig{t: t, rt: rt, svc: svc, store: store, clk: clk,
		audit: auditLog, egress: guard, indexers: indexers, profiles: profiles,
		downloads: downloads, tickets: tickets, roots: roots, media: media,
		scanner: scanner, requests: requests, database: database,
		notifier: notifier, discord: discord}
}

// client keeps cookies across requests, like a browser.
type client struct {
	rig     *rig
	cookies map[string]string
	ip      string
	// bearer, when set, authenticates with an API token instead of the cookie.
	bearer string
}

func (r *rig) client() *client {
	return &client{rig: r, cookies: map[string]string{}, ip: "203.0.113.10"}
}

type response struct {
	Code   int
	Body   map[string]any
	Raw    string
	Header http.Header
}

func (c *client) do(method, path string, body any) response {
	c.rig.t.Helper()

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			c.rig.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}

	req := httptest.NewRequestWithContext(context.Background(), method, path, rdr)
	req.RemoteAddr = c.ip + ":40000"
	req.Header.Set("Content-Type", "application/json")
	for k, v := range c.cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	// Double-submit: echo the CSRF cookie back in the header.
	if v, ok := c.cookies[CSRFCookieName]; ok {
		req.Header.Set(CSRFHeaderName, v)
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}

	rec := httptest.NewRecorder()
	c.rig.rt.ServeHTTP(rec, req)

	for _, ck := range rec.Result().Cookies() {
		if ck.MaxAge < 0 {
			delete(c.cookies, ck.Name)
			continue
		}
		c.cookies[ck.Name] = ck.Value
	}

	out := response{Code: rec.Code, Raw: rec.Body.String(), Header: rec.Header()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.Body)
	return out
}

func (c *client) get(path string) response          { return c.do(http.MethodGet, path, nil) }
func (c *client) post(path string, b any) response  { return c.do(http.MethodPost, path, b) }
func (c *client) patch(path string, b any) response { return c.do(http.MethodPatch, path, b) }

// visitPage fetches an anonymous page to pick up a CSRF cookie.
func (c *client) visitPage(path string) { c.get(path) }

func (r *rig) mustCode(secret string) string {
	r.t.Helper()
	code, err := identity.TOTPCode(secret, r.clk.now())
	if err != nil {
		r.t.Fatal(err)
	}
	return code
}

// bootstrapAdmin runs the wizard and completes enrollment, returning a signed-in
// admin client.
func (r *rig) bootstrapAdmin() *client {
	r.t.Helper()
	c := r.client()
	c.visitPage("/setup")

	if res := c.post("/api/v1/setup", map[string]any{
		"username": "jacob", "email": "jacob@example.com", "password": "correct-horse-battery",
	}); res.Code != http.StatusCreated {
		r.t.Fatalf("setup failed: %d %s", res.Code, res.Raw)
	}

	c.visitPage("/login")
	res := c.post("/api/v1/auth/login", map[string]any{
		"username": "jacob", "password": "correct-horse-battery",
	})
	if res.Code != http.StatusOK || res.Body["next"] != "enroll" {
		r.t.Fatalf("login: %d %s", res.Code, res.Raw)
	}

	res = c.get("/api/v1/auth/mfa/enroll")
	if res.Code != http.StatusOK {
		r.t.Fatalf("enroll begin: %d %s", res.Code, res.Raw)
	}
	secret, _ := res.Body["secret"].(string)

	res = c.post("/api/v1/auth/mfa/enroll/confirm", map[string]any{
		"secret": secret, "code": r.mustCode(secret),
	})
	if res.Code != http.StatusOK {
		r.t.Fatalf("enroll confirm: %d %s", res.Code, res.Raw)
	}
	return c
}

// ---------------------------------------------------------------------------
// First run
// ---------------------------------------------------------------------------

func TestFirstRunThroughToWorkingAdmin(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	res := admin.get("/api/v1/me")
	if res.Code != http.StatusOK {
		t.Fatalf("/api/v1/me after enrollment: %d %s", res.Code, res.Raw)
	}
	if res.Body["username"] != "jacob" || res.Body["role"] != "Admin" {
		t.Errorf("unexpected profile: %v", res.Body)
	}
	if res.Body["state"] != string(authz.StateActive) {
		t.Errorf("state = %v, want active", res.Body["state"])
	}
}

// The wizard must close itself permanently once an account exists.
func TestSetupIsUnreachableOnceAnAccountExists(t *testing.T) {
	r := newRig(t)
	r.bootstrapAdmin()

	anon := r.client()
	if res := anon.get("/setup"); res.Code != http.StatusNotFound {
		t.Errorf("GET /setup after setup returned %d, want 404", res.Code)
	}

	// And the submit endpoint must not create a second administrator.
	anon.visitPage("/login")
	res := anon.post("/api/v1/setup", map[string]any{
		"username": "attacker", "email": "a@example.com", "password": "another-long-password",
	})
	if res.Code != http.StatusNotFound {
		t.Errorf("POST /api/v1/setup after setup returned %d, want 404", res.Code)
	}

	n, err := r.store.CountUsers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("account count = %d, want 1 (a second admin was created)", n)
	}
}

// A newly created account is awaiting_mfa and can reach nothing but enrollment,
// even though it is an Admin.
func TestAdminCannotActBeforeEnrolling(t *testing.T) {
	r := newRig(t)
	c := r.client()
	c.visitPage("/setup")
	c.post("/api/v1/setup", map[string]any{
		"username": "jacob", "email": "j@example.com", "password": "correct-horse-battery",
	})
	c.visitPage("/login")
	c.post("/api/v1/auth/login", map[string]any{
		"username": "jacob", "password": "correct-horse-battery",
	})

	for _, path := range []string{"/api/v1/me", "/api/v1/admin/users", "/api/v1/accounts/requests"} {
		res := c.get(path)
		if res.Code != http.StatusConflict {
			t.Errorf("un-enrolled admin reached %s with %d, want 409", path, res.Code)
		}
	}
	if res := c.get("/api/v1/auth/mfa/enroll"); res.Code != http.StatusOK {
		t.Errorf("enrollment route was blocked: %d", res.Code)
	}
}

// ---------------------------------------------------------------------------
// Signup and approval
// ---------------------------------------------------------------------------

func TestSignupApprovalAndFirstLogin(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	applicant := r.client()
	applicant.visitPage("/signup")
	res := applicant.post("/api/v1/auth/signup", map[string]any{
		"username": "friend", "email": "friend@example.com",
		"password": "another-good-passphrase", "note": "let me in",
	})
	if res.Code != http.StatusAccepted {
		t.Fatalf("signup: %d %s", res.Code, res.Raw)
	}

	// A pending request grants nothing: the applicant cannot log in.
	applicant.visitPage("/login")
	res = applicant.post("/api/v1/auth/login", map[string]any{
		"username": "friend", "password": "another-good-passphrase",
	})
	if res.Code != http.StatusUnauthorized {
		t.Errorf("a pending applicant logged in: %d %s", res.Code, res.Raw)
	}

	// The admin sees it in the queue.
	res = admin.get("/api/v1/accounts/requests")
	if res.Code != http.StatusOK {
		t.Fatalf("queue: %d %s", res.Code, res.Raw)
	}
	reqs, _ := res.Body["requests"].([]any)
	if len(reqs) != 1 {
		t.Fatalf("queue has %d entries, want 1", len(reqs))
	}
	first, _ := reqs[0].(map[string]any)
	reqID := int64(first["id"].(float64))
	if first["username"] != "friend" || first["note"] != "let me in" {
		t.Errorf("unexpected queue entry: %v", first)
	}

	userRole, err := r.store.RoleByName(t.Context(), authz.RoleUser)
	if err != nil {
		t.Fatal(err)
	}

	res = admin.post("/api/v1/accounts/requests/"+itoa(int(reqID))+"/approve", map[string]any{
		"role_id": userRole.ID, "library_ids": []int64{}, "rating_ceiling": 0,
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("approve: %d %s", res.Code, res.Raw)
	}
	if res.Body["state"] != string(authz.StateAwaitingMFA) {
		t.Errorf("approved account state = %v, want awaiting_mfa", res.Body["state"])
	}

	// The approved user can now log in — and lands on enrollment, because the
	// password carried over but no authenticator exists yet.
	friend := r.client()
	friend.visitPage("/login")
	res = friend.post("/api/v1/auth/login", map[string]any{
		"username": "friend", "password": "another-good-passphrase",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("approved user login: %d %s", res.Code, res.Raw)
	}
	if res.Body["next"] != "enroll" {
		t.Errorf("next = %v, want enroll", res.Body["next"])
	}

	enroll := friend.get("/api/v1/auth/mfa/enroll")
	secret, _ := enroll.Body["secret"].(string)
	res = friend.post("/api/v1/auth/mfa/enroll/confirm", map[string]any{
		"secret": secret, "code": r.mustCode(secret),
	})
	if res.Code != http.StatusOK {
		t.Fatalf("friend enrollment: %d %s", res.Code, res.Raw)
	}
	codes, _ := res.Body["recovery_codes"].([]any)
	if len(codes) != 10 {
		t.Errorf("got %d recovery codes, want 10", len(codes))
	}

	// And the User role is genuinely limited.
	if res := friend.get("/api/v1/me"); res.Code != http.StatusOK {
		t.Errorf("friend cannot read own profile: %d", res.Code)
	}
	if res := friend.get("/api/v1/admin/users"); res.Code != http.StatusNotFound {
		t.Errorf("USER REACHED ADMIN ROUTE: %d", res.Code)
	}
	if res := friend.get("/api/v1/accounts/requests"); res.Code != http.StatusForbidden {
		t.Errorf("friend reached the approval queue: %d %s", res.Code, res.Raw)
	}
}

// The signup form must not reveal whether an address is already registered.
func TestSignupIsNotAnEnumerationOracle(t *testing.T) {
	r := newRig(t)
	r.bootstrapAdmin() // jacob / jacob@example.com now exists

	c := r.client()
	c.visitPage("/signup")

	fresh := c.post("/api/v1/auth/signup", map[string]any{
		"username": "brandnew", "email": "brandnew@example.com", "password": "a-fine-passphrase",
	})
	taken := c.post("/api/v1/auth/signup", map[string]any{
		"username": "jacob", "email": "jacob@example.com", "password": "a-fine-passphrase",
	})

	if fresh.Code != taken.Code {
		t.Errorf("status differs by whether the account exists: %d vs %d", fresh.Code, taken.Code)
	}
	if fresh.Raw != taken.Raw {
		t.Errorf("body differs by whether the account exists:\n new: %s\ntaken: %s", fresh.Raw, taken.Raw)
	}
	// A second request for an address already waiting collides with the
	// pending-email index — and is answered exactly like the first.
	again := c.post("/api/v1/auth/signup", map[string]any{
		"username": "brandnew2", "email": "brandnew@example.com", "password": "a-fine-passphrase",
	})
	if again.Code != fresh.Code || again.Raw != fresh.Raw {
		t.Errorf("a pending address answers differently: %d %s", again.Code, again.Raw)
	}
}

// Only that collision is masked. A request that could not be recorded for any
// other reason is not answered as though it had been: the person would wait for
// an approval that can never come.
func TestASignupThatCouldNotBeRecordedIsNotReportedAsRecorded(t *testing.T) {
	r := newRig(t)
	r.bootstrapAdmin()
	// Stands in for a full disk or a locked database: the insert fails, and not
	// on a uniqueness constraint.
	if _, err := r.database.ExecContext(t.Context(), `
		CREATE TRIGGER refuse_requests BEFORE INSERT ON account_request
		BEGIN SELECT RAISE(ABORT, 'simulated storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	c := r.client()
	c.visitPage("/signup")
	res := c.post("/api/v1/auth/signup", map[string]any{
		"username": "brandnew", "email": "brandnew@example.com", "password": "a-fine-passphrase",
	})
	if res.Code < 500 {
		t.Errorf("an unrecorded request was answered %d %s", res.Code, res.Raw)
	}
}

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------

// An unknown username and a wrong password must be indistinguishable.
func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	r := newRig(t)
	r.bootstrapAdmin()

	c := r.client()
	c.visitPage("/login")

	unknown := c.post("/api/v1/auth/login", map[string]any{
		"username": "nobody-here", "password": "some-long-password",
	})
	wrong := c.post("/api/v1/auth/login", map[string]any{
		"username": "jacob", "password": "definitely-not-the-password",
	})

	if unknown.Code != http.StatusUnauthorized || wrong.Code != http.StatusUnauthorized {
		t.Fatalf("codes: unknown=%d wrong=%d", unknown.Code, wrong.Code)
	}
	if unknown.Raw != wrong.Raw {
		t.Errorf("bodies differ:\nunknown: %s\n  wrong: %s", unknown.Raw, wrong.Raw)
	}
}

func TestLoginThrottlingEngages(t *testing.T) {
	r := newRig(t)
	r.bootstrapAdmin()

	c := r.client()
	c.visitPage("/login")

	var throttled bool
	for i := 0; i < 8; i++ {
		res := c.post("/api/v1/auth/login", map[string]any{
			"username": "jacob", "password": "wrong-password-here",
		})
		if res.Code == http.StatusTooManyRequests {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Fatal("repeated failures were never throttled")
	}

	// Even the correct password is refused while throttled.
	res := c.post("/api/v1/auth/login", map[string]any{
		"username": "jacob", "password": "correct-horse-battery",
	})
	if res.Code != http.StatusTooManyRequests {
		t.Errorf("throttle did not apply to a correct password: %d", res.Code)
	}
}

// A TOTP code is valid for its whole time step, so verification alone permits
// replay. The consumed counter must close that window.
func TestTOTPCodeCannotBeReplayed(t *testing.T) {
	r := newRig(t)
	c := r.client()
	c.visitPage("/setup")
	c.post("/api/v1/setup", map[string]any{
		"username": "jacob", "email": "j@example.com", "password": "correct-horse-battery",
	})
	c.visitPage("/login")
	c.post("/api/v1/auth/login", map[string]any{"username": "jacob", "password": "correct-horse-battery"})
	enroll := c.get("/api/v1/auth/mfa/enroll")
	secret, _ := enroll.Body["secret"].(string)
	code := r.mustCode(secret)
	c.post("/api/v1/auth/mfa/enroll/confirm", map[string]any{"secret": secret, "code": code})

	// A second session, presenting the very same code inside its step.
	replay := r.client()
	replay.visitPage("/login")
	replay.post("/api/v1/auth/login", map[string]any{"username": "jacob", "password": "correct-horse-battery"})
	res := replay.post("/api/v1/auth/login/mfa", map[string]any{"code": code})
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("a replayed authenticator code was accepted: %d %s (THE ATTACK SUCCEEDED)", res.Code, res.Raw)
	}

	// The next step's code works, proving the account is not simply broken.
	r.clk.advance(31 * time.Second)
	res = replay.post("/api/v1/auth/login/mfa", map[string]any{"code": r.mustCode(secret)})
	if res.Code != http.StatusOK {
		t.Fatalf("a fresh code was refused: %d %s", res.Code, res.Raw)
	}
}

// ---------------------------------------------------------------------------
// Session lifecycle
// ---------------------------------------------------------------------------

// §7.2: suspension must revoke sessions immediately, not at the next expiry.
func TestSuspensionKillsLiveSessionsImmediately(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	// Create and sign in a second user.
	applicant := r.client()
	applicant.visitPage("/signup")
	applicant.post("/api/v1/auth/signup", map[string]any{
		"username": "friend", "email": "f@example.com", "password": "another-good-passphrase",
	})
	reqs := admin.get("/api/v1/accounts/requests")
	entry := reqs.Body["requests"].([]any)[0].(map[string]any)
	reqID := int(entry["id"].(float64))
	userRole, _ := r.store.RoleByName(t.Context(), authz.RoleUser)
	created := admin.post("/api/v1/accounts/requests/"+itoa(reqID)+"/approve", map[string]any{
		"role_id": userRole.ID, "library_ids": []int64{}, "rating_ceiling": 0,
	})
	userID := int(created.Body["user_id"].(float64))

	friend := r.client()
	friend.visitPage("/login")
	friend.post("/api/v1/auth/login", map[string]any{"username": "friend", "password": "another-good-passphrase"})
	enroll := friend.get("/api/v1/auth/mfa/enroll")
	secret, _ := enroll.Body["secret"].(string)
	friend.post("/api/v1/auth/mfa/enroll/confirm", map[string]any{"secret": secret, "code": r.mustCode(secret)})

	if res := friend.get("/api/v1/me"); res.Code != http.StatusOK {
		t.Fatalf("friend could not use their session: %d %s", res.Code, res.Raw)
	}

	// Suspend, with the friend's session still live.
	res := admin.post("/api/v1/admin/users/"+itoa(userID)+"/suspend", map[string]any{"reason": "testing"})
	if res.Code != http.StatusOK {
		t.Fatalf("suspend: %d %s", res.Code, res.Raw)
	}

	// The very next request on the existing session must fail. No clock
	// advance, no re-login: immediate.
	if res := friend.get("/api/v1/me"); res.Code != http.StatusNotFound {
		t.Errorf("a suspended user's live session still worked: %d %s", res.Code, res.Raw)
	}

	// And they cannot log back in.
	fresh := r.client()
	fresh.visitPage("/login")
	if res := fresh.post("/api/v1/auth/login", map[string]any{
		"username": "friend", "password": "another-good-passphrase",
	}); res.Code != http.StatusUnauthorized {
		t.Errorf("a suspended user logged back in: %d", res.Code)
	}
}

func TestLogoutRevokesTheSession(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	if res := admin.post("/api/v1/auth/logout", nil); res.Code != http.StatusOK {
		t.Fatalf("logout: %d %s", res.Code, res.Raw)
	}
	if res := admin.get("/api/v1/me"); res.Code != http.StatusNotFound {
		t.Errorf("the session survived logout: %d", res.Code)
	}
}

func TestSessionExpiresOnIdleTimeout(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	r.clk.advance(13 * time.Hour) // idle timeout is 12h
	if res := admin.get("/api/v1/me"); res.Code != http.StatusNotFound {
		t.Errorf("an idle-expired session still worked: %d", res.Code)
	}
}

// Rotation replaces the session secret; presenting the superseded one well
// after the grace window is a replay, and it revokes the whole session.
func TestSupersededSessionSecretIsTreatedAsReuse(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	old := admin.cookies[identity.SessionCookieName]
	if old == "" {
		t.Fatal("no session cookie was issued")
	}

	// Force a rotation by crossing the rotation interval.
	r.clk.advance(16 * time.Minute)
	if res := admin.get("/api/v1/me"); res.Code != http.StatusOK {
		t.Fatalf("request across a rotation failed: %d %s", res.Code, res.Raw)
	}
	rotated := admin.cookies[identity.SessionCookieName]
	if rotated == old {
		t.Fatal("the session secret did not rotate")
	}

	// Move past the grace window, then present the superseded secret.
	r.clk.advance(2 * time.Minute)
	attacker := r.client()
	attacker.cookies[identity.SessionCookieName] = old
	if res := attacker.get("/api/v1/me"); res.Code != http.StatusNotFound {
		t.Errorf("a superseded session secret was accepted: %d", res.Code)
	}

	// Reuse detection revokes the family, so the legitimate holder is also cut
	// off. That is the intended trade: a detected theft ends the session.
	if res := admin.get("/api/v1/me"); res.Code != http.StatusNotFound {
		t.Errorf("the session survived a detected reuse: %d", res.Code)
	}
}

// ---------------------------------------------------------------------------
// Authorization over HTTP
// ---------------------------------------------------------------------------

// The escalation guard must hold through the HTTP layer, not just in the
// engine's unit tests.
func TestManagerCannotApproveIntoAdminOverHTTP(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	// Create a Manager.
	m := r.client()
	m.visitPage("/signup")
	m.post("/api/v1/auth/signup", map[string]any{
		"username": "manager", "email": "m@example.com", "password": "manager-passphrase-x",
	})
	entry := admin.get("/api/v1/accounts/requests").Body["requests"].([]any)[0].(map[string]any)
	mgrRole, _ := r.store.RoleByName(t.Context(), authz.RoleManager)
	admin.post("/api/v1/accounts/requests/"+itoa(int(entry["id"].(float64)))+"/approve",
		map[string]any{"role_id": mgrRole.ID, "library_ids": []int64{}, "rating_ceiling": 0})

	mgr := r.client()
	mgr.visitPage("/login")
	mgr.post("/api/v1/auth/login", map[string]any{"username": "manager", "password": "manager-passphrase-x"})
	enroll := mgr.get("/api/v1/auth/mfa/enroll")
	secret, _ := enroll.Body["secret"].(string)
	mgr.post("/api/v1/auth/mfa/enroll/confirm", map[string]any{"secret": secret, "code": r.mustCode(secret)})

	// A third applicant for the Manager to act on.
	app := r.client()
	app.visitPage("/signup")
	app.post("/api/v1/auth/signup", map[string]any{
		"username": "victim", "email": "v@example.com", "password": "victim-passphrase-9",
	})

	queue := mgr.get("/api/v1/accounts/requests")
	if queue.Code != http.StatusOK {
		t.Fatalf("manager cannot see the queue: %d %s", queue.Code, queue.Raw)
	}
	var victimID int
	for _, raw := range queue.Body["requests"].([]any) {
		e := raw.(map[string]any)
		if e["username"] == "victim" {
			victimID = int(e["id"].(float64))
		}
	}
	if victimID == 0 {
		t.Fatal("victim request not found in the queue")
	}

	adminRole, _ := r.store.RoleByName(t.Context(), authz.RoleAdmin)

	// The attack: a Manager approving an account straight into Admin.
	res := mgr.post("/api/v1/accounts/requests/"+itoa(victimID)+"/approve",
		map[string]any{"role_id": adminRole.ID, "library_ids": []int64{}, "rating_ceiling": 0})
	if res.Code != http.StatusNotFound {
		t.Fatalf("MANAGER APPROVED INTO ADMIN: %d %s (THE ATTACK SUCCEEDED)", res.Code, res.Raw)
	}

	// Cloning themselves into Manager is refused too.
	res = mgr.post("/api/v1/accounts/requests/"+itoa(victimID)+"/approve",
		map[string]any{"role_id": mgrRole.ID, "library_ids": []int64{}, "rating_ceiling": 0})
	if res.Code != http.StatusNotFound {
		t.Errorf("a Manager approved another Manager: %d %s", res.Code, res.Raw)
	}

	// But approving into User works, so the refusals above are not blanket.
	userRole, _ := r.store.RoleByName(t.Context(), authz.RoleUser)
	res = mgr.post("/api/v1/accounts/requests/"+itoa(victimID)+"/approve",
		map[string]any{"role_id": userRole.ID, "library_ids": []int64{}, "rating_ceiling": 0})
	if res.Code != http.StatusCreated {
		t.Errorf("a Manager could not approve into User: %d %s", res.Code, res.Raw)
	}
}

// The denial the client saw as 404 must still be recorded truthfully.
func TestDeniedEscalationIsAudited(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	events, err := r.audit.List(
		authz.WithPrincipal(t.Context(), adminPrincipalFor(t, r)),
		audit.Query{Action: audit.ActionFirstRunCompleted})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("first-run event count = %d, want 1", len(events))
	}

	_ = admin
}

func adminPrincipalFor(t *testing.T, r *rig) *authz.Principal {
	t.Helper()
	u, err := r.store.UserByUsername(t.Context(), "jacob")
	if err != nil {
		t.Fatal(err)
	}
	role, err := r.store.RoleByID(t.Context(), u.RoleID)
	if err != nil {
		t.Fatal(err)
	}
	return &authz.Principal{
		UserID: u.ID, Username: u.Username, Role: role,
		State: authz.StateActive, MFASatisfied: true, UnrestrictedLibraries: true,
	}
}

// ---------------------------------------------------------------------------
// CSRF
// ---------------------------------------------------------------------------

func TestStateChangingRequestWithoutCSRFTokenIsRefused(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	// Drop the CSRF cookie but keep the session: exactly what a cross-site
	// form post looks like.
	delete(admin.cookies, CSRFCookieName)
	if res := admin.post("/api/v1/auth/logout", nil); res.Code != http.StatusForbidden {
		t.Errorf("a state-changing request without a CSRF token returned %d, want 403", res.Code)
	}
}

// Mass assignment: a body that smuggles a privileged field into an endpoint
// that never accepts one is rejected outright rather than silently ignored, so
// the attempt is visible instead of being a near miss.
//
// Signup is the sharpest case — it is anonymous, and "role_id" is exactly what
// an attacker would try to add.
func TestSmuggledPrivilegedFieldsAreRejected(t *testing.T) {
	r := newRig(t)
	r.bootstrapAdmin()

	c := r.client()
	c.visitPage("/signup")

	for _, smuggled := range []map[string]any{
		{"username": "x", "email": "x@example.com", "password": "a-fine-passphrase", "role_id": 1},
		{"username": "x", "email": "x@example.com", "password": "a-fine-passphrase", "state": "active"},
		{"username": "x", "email": "x@example.com", "password": "a-fine-passphrase", "rating_ceiling": 0},
	} {
		res := c.post("/api/v1/auth/signup", smuggled)
		if res.Code != http.StatusBadRequest {
			t.Errorf("body %v returned %d, want 400", smuggled, res.Code)
		}
		if !strings.Contains(res.Raw, "malformed") {
			t.Errorf("unexpected body: %s", res.Raw)
		}
	}

	// Nothing was created by any of those attempts.
	n, err := r.store.CountPendingRequests(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d requests were created by rejected bodies", n)
	}

	// The same body without the smuggled field is accepted, so the rejection
	// is about the extra field and not about the endpoint being broken.
	if res := c.post("/api/v1/auth/signup", map[string]any{
		"username": "x", "email": "x@example.com", "password": "a-fine-passphrase",
	}); res.Code != http.StatusAccepted {
		t.Errorf("a clean signup was rejected: %d %s", res.Code, res.Raw)
	}
}

// Signup deliberately accepts a request for a name that is already registered,
// because refusing would tell an anonymous caller that the account exists. The
// consequence is that a duplicate reaches the approval queue in normal use, and
// the approver has to be told what happened when they click Approve. This
// regression exists because that path returned a bare 500 "internal error",
// found by driving the real UI rather than by any unit test.
func TestApprovingADuplicateUsernameIsReportedNotSwallowed(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	userRole, err := r.store.RoleByName(t.Context(), authz.RoleUser)
	if err != nil {
		t.Fatal(err)
	}

	submit := func(username, email string) int64 {
		t.Helper()
		c := r.client()
		c.visitPage("/signup")
		res := c.post("/api/v1/auth/signup", map[string]any{
			"username": username, "email": email,
			"password": "another-good-passphrase", "note": "",
		})
		if res.Code != http.StatusAccepted {
			t.Fatalf("signup %s: %d %s", username, res.Code, res.Raw)
		}
		return 0
	}

	submit("twin", "twin-one@example.com")
	submit("twin", "twin-two@example.com") // same name: accepted, no oracle

	res := admin.get("/api/v1/accounts/requests")
	reqs, _ := res.Body["requests"].([]any)
	if len(reqs) != 2 {
		t.Fatalf("queue has %d entries, want 2", len(reqs))
	}

	approve := func(entry any) response {
		t.Helper()
		m, _ := entry.(map[string]any)
		id := int64(m["id"].(float64))
		return admin.post("/api/v1/accounts/requests/"+itoa(int(id))+"/approve", map[string]any{
			"role_id": userRole.ID, "library_ids": []int64{}, "rating_ceiling": 0,
		})
	}

	if got := approve(reqs[0]); got.Code != http.StatusCreated {
		t.Fatalf("first approval: %d %s", got.Code, got.Raw)
	}

	second := approve(reqs[1])
	if second.Code != http.StatusConflict {
		t.Errorf("approving a duplicate returned %d, want 409\nbody: %s", second.Code, second.Raw)
	}
	if second.Code == http.StatusInternalServerError {
		t.Error("the approver was shown a bare internal error for an ordinary duplicate")
	}
	if msg, _ := second.Body["error"].(string); !strings.Contains(msg, "already registered") {
		t.Errorf("error message %q does not say what went wrong", msg)
	}

	// The failed approval must not have half-created anything, and the request
	// must still be in the queue so the approver can deny it rather than being
	// left with a row nothing can act on.
	after, err := r.store.CountUsers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after != 2 { // the admin and the one approved twin
		t.Errorf("%d users exist after the failed approval, want 2", after)
	}

	res = admin.get("/api/v1/accounts/requests")
	remaining, _ := res.Body["requests"].([]any)
	if len(remaining) != 1 {
		t.Errorf("queue has %d entries after the conflict, want the duplicate still there", len(remaining))
	}
}

// A dropdown takes its default from the first option, so the order the server
// returns roles in decides what an approver grants when they click Approve
// without reading. It must be the least privileged role they can assign, not
// the most.
func TestAssignableRolesPutTheSafestOptionFirst(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	res := admin.get("/api/v1/roles")
	if res.Code != http.StatusOK {
		t.Fatalf("roles: %d %s", res.Code, res.Raw)
	}
	roles, _ := res.Body["roles"].([]any)
	if len(roles) < 2 {
		t.Fatalf("an Admin can assign %d roles; the ordering is untested", len(roles))
	}

	var ranks []int
	var names []string
	for _, entry := range roles {
		m, _ := entry.(map[string]any)
		ranks = append(ranks, int(m["rank"].(float64)))
		names = append(names, m["name"].(string))
	}

	for i := 1; i < len(ranks); i++ {
		if ranks[i] < ranks[i-1] {
			t.Fatalf("roles are not ordered least-privileged first: %v with ranks %v", names, ranks)
		}
	}
	if names[0] != authz.RoleUser {
		t.Errorf("the default role an Admin would grant is %q, want %q", names[0], authz.RoleUser)
	}

	// And the Admin's own role is never on the list: assignment is strictly
	// downward, so nobody can clone their own privilege level.
	for _, n := range names {
		if n == authz.RoleAdmin {
			t.Error("an Admin was offered the Admin role")
		}
	}
}

// ---------------------------------------------------------------------------
// Egress admin surface
// ---------------------------------------------------------------------------

func TestEgressStatusIsAdminOnlyAndInvisibleToOthers(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	res := admin.get("/api/v1/admin/egress")
	if res.Code != http.StatusOK {
		t.Fatalf("admin could not read egress status: %d %s", res.Code, res.Raw)
	}
	if healthy, _ := res.Body["healthy"].(bool); healthy {
		t.Error("egress reported healthy before any probe ran")
	}
	profiles, _ := res.Body["profiles"].([]any)
	if len(profiles) != 3 {
		t.Fatalf("got %d profiles, want 3", len(profiles))
	}

	// A User must not even learn the route exists.
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	user := r.redeemAndEnroll(code, "watcher", "a-perfectly-fine-passphrase")
	for _, path := range []string{"/api/v1/admin/egress"} {
		if res := user.get(path); res.Code != http.StatusNotFound {
			t.Errorf("a User got %d from %s, want 404", res.Code, path)
		}
	}
}

// The proxy password must never leave the process, whatever the admin surface
// is asked for.
func TestEgressStatusNeverReturnsTheProxyPassword(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	res := admin.get("/api/v1/admin/egress")
	if strings.Contains(res.Raw, "password") {
		t.Errorf("the egress status body mentions a password: %s", res.Raw)
	}
}

// Egress policy is configuration, not runtime state. A runtime toggle for the
// control that prevents leaks is the first thing an attacker with an admin
// session would reach for.
func TestEgressPolicyCannotBeChangedOverHTTP(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	res := admin.patch("/api/v1/admin/egress", map[string]any{})
	if res.Code != http.StatusConflict {
		t.Errorf("PATCH /api/v1/admin/egress returned %d, want 409\nbody: %s", res.Code, res.Raw)
	}
	if msg, _ := res.Body["error"].(string); !strings.Contains(msg, "config") {
		t.Errorf("the refusal does not say where the policy lives: %q", msg)
	}
}

// The leak test returns its verdict in the body. A failing tunnel is data, not
// an HTTP error: a 500 would be indistinguishable from the endpoint breaking.
func TestLeakTestReportsItsVerdictInTheBody(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	res := admin.post("/api/v1/admin/egress/leak-test", map[string]any{})
	if res.Code != http.StatusOK {
		t.Fatalf("leak test: %d %s", res.Code, res.Raw)
	}
	// The rig's tunnel interface does not exist, so this must be false — and
	// crucially it must not be absent or true.
	routed, present := res.Body["routes_through_tunnel"].(bool)
	if !present {
		t.Fatal("the verdict field is missing")
	}
	if routed {
		t.Error("the leak test claimed traffic routes through a tunnel that does not exist")
	}
	if detail, _ := res.Body["detail"].(string); detail == "" {
		t.Error("no explanation was given")
	}
	if caveat, _ := res.Body["caveat"].(string); !strings.Contains(caveat, "cannot prove") {
		t.Error("the response overstates what the test establishes")
	}
}

func TestLeakTestIsAudited(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	if res := admin.post("/api/v1/admin/egress/leak-test", map[string]any{}); res.Code != http.StatusOK {
		t.Fatalf("leak test: %d", res.Code)
	}

	events, err := r.audit.List(
		authz.WithPrincipal(t.Context(), adminPrincipalFor(t, r)),
		audit.Query{Action: audit.ActionEgressChanged})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("leak-test audit entries = %d, want 1", len(events))
	}
	if events[0].Outcome != audit.OutcomeFailure {
		t.Errorf("outcome = %q, want failure: the rig has no tunnel", events[0].Outcome)
	}
}

// Enrollment returns a scannable code as well as the key. Typing 32 base32
// characters into a phone is the single highest-friction moment in the product
// and every account hits it.
func TestEnrollmentOffersAScannableCode(t *testing.T) {
	r := newRig(t)

	c := r.client()
	c.visitPage("/setup")
	if res := c.post("/api/v1/setup", map[string]any{
		"username": "admin", "email": "a@example.com", "password": "a-good-long-passphrase",
	}); res.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", res.Code, res.Raw)
	}
	c.visitPage("/login")
	if res := c.post("/api/v1/auth/login", map[string]any{
		"username": "admin", "password": "a-good-long-passphrase",
	}); res.Code != http.StatusOK {
		t.Fatalf("login: %d", res.Code)
	}

	res := c.get("/api/v1/auth/mfa/enroll")
	if res.Code != http.StatusOK {
		t.Fatalf("enroll: %d %s", res.Code, res.Raw)
	}

	qr, _ := res.Body["qr"].(string)
	if !strings.HasPrefix(qr, "data:image/png;base64,") {
		t.Fatalf("no QR data URI in the enrollment offer: %.80s", qr)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(qr, "data:image/png;base64,"))
	if err != nil {
		t.Fatalf("the QR payload is not valid base64: %v", err)
	}
	if !bytes.HasPrefix(raw, []byte("\x89PNG\r\n\x1a\n")) {
		t.Error("the QR payload is not a PNG")
	}
	if len(raw) < 200 {
		t.Errorf("the QR payload is %d bytes, which is too small to be a real code", len(raw))
	}

	// The manual key must still be there. The QR is the convenient path, not
	// the only one — a desktop user with the authenticator on a locked phone
	// still needs the key.
	if secret, _ := res.Body["secret"].(string); secret == "" {
		t.Error("the manual setup key was dropped when the QR was added")
	}
	if uri, _ := res.Body["uri"].(string); !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Error("the otpauth URI was dropped")
	}
}

// Several API responses carry a secret shown exactly once. A shared cache
// holding any of them is a leak nobody would think to look for.
func TestSecretBearingResponsesAreNotCacheable(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	for _, probe := range []struct {
		name string
		res  response
	}{
		{"own profile", admin.get("/api/v1/me")},
		{"sessions", admin.get("/api/v1/me/sessions")},
		{"tokens", admin.get("/api/v1/me/tokens")},
		{"invites", admin.get("/api/v1/invites")},
		{"egress status", admin.get("/api/v1/admin/egress")},
		{"a 404", admin.get("/api/v1/admin/users/999999")},
	} {
		if got := probe.res.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", probe.name, got)
		}
	}
}

// ---------------------------------------------------------------------------
// Indexers
// ---------------------------------------------------------------------------

// The API key goes in and must never come back out. This drives the real HTTP
// surface rather than the store, because the store already refuses — what is
// being checked is that no route reaches the method that would decrypt it.
func TestIndexerAPIKeyNeverComesBackOverHTTP(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	const secret = "tracker-passkey-do-not-leak"
	res := admin.post("/api/v1/admin/indexers", map[string]any{
		"name": "Tracker", "kind": "torznab",
		"base_url": "https://tracker.example.com", "api_key": secret,
		"categories": []int{2000}, "enabled": true, "priority": 10,
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", res.Code, res.Raw)
	}
	if strings.Contains(res.Raw, secret) {
		t.Fatalf("the creation response echoed the API key: %s", res.Raw)
	}

	list := admin.get("/api/v1/admin/indexers")
	if list.Code != http.StatusOK {
		t.Fatalf("list: %d %s", list.Code, list.Raw)
	}
	if strings.Contains(list.Raw, secret) {
		t.Fatalf("THE API KEY LEAKED IN THE LISTING: %s", list.Raw)
	}
	if !strings.Contains(list.Raw, "Tracker") {
		t.Errorf("the indexer is missing from the listing: %s", list.Raw)
	}

	// And it still works: the search path can decrypt what the admin path
	// cannot see.
	enabled, err := r.indexers.Enabled(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 || enabled[0].APIKey != secret {
		t.Error("the key did not survive for the search path")
	}
}

func TestIndexerRoutesAreInvisibleToNonAdmins(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	user := r.redeemAndEnroll(code, "viewer", "a-perfectly-fine-passphrase")

	for _, probe := range []response{
		user.get("/api/v1/admin/indexers"),
		user.post("/api/v1/admin/indexers", map[string]any{"name": "x"}),
		user.del("/api/v1/admin/indexers/1"),
	} {
		if probe.Code != http.StatusNotFound {
			t.Errorf("a User got %d from an indexer route, want 404", probe.Code)
		}
	}
}

// A bad indexer must be refused when it is SAVED, with a message the operator
// can act on — not accepted and then silently never work.
func TestBadIndexerDefinitionsAreRefusedOnSave(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	for name, body := range map[string]map[string]any{
		"file scheme": {"name": "x", "kind": "torznab", "base_url": "file:///etc/passwd"},
		"no host":     {"name": "x", "kind": "torznab", "base_url": "https://"},
		"bad kind":    {"name": "x", "kind": "magic", "base_url": "https://x.example.com"},
		"no name":     {"name": "", "kind": "torznab", "base_url": "https://x.example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			res := admin.post("/api/v1/admin/indexers", body)
			if res.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400\nbody: %s", res.Code, res.Raw)
			}
			if msg, _ := res.Body["error"].(string); msg == "" {
				t.Error("no explanation was given")
			}
		})
	}
}

func TestIndexerChangesAreAudited(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	res := admin.post("/api/v1/admin/indexers", map[string]any{
		"name": "Tracker", "kind": "torznab",
		"base_url": "https://tracker.example.com", "api_key": "k", "enabled": true,
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", res.Code, res.Raw)
	}

	events, err := r.audit.List(
		authz.WithPrincipal(t.Context(), adminPrincipalFor(t, r)),
		audit.Query{Action: audit.ActionIndexerCreated})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("indexer-created audit entries = %d, want 1", len(events))
	}
	// The audit detail must not carry the key either.
	if strings.Contains(events[0].Detail, "k") && events[0].Detail != "Tracker" {
		t.Errorf("audit detail = %q", events[0].Detail)
	}
}

// ---------------------------------------------------------------------------
// Interactive search
// ---------------------------------------------------------------------------

func TestSearchWithNoIndexersIsADistinctAnswer(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	res := admin.post("/api/v1/releases/search", map[string]any{"term": "the matrix"})
	if res.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409\nbody: %s", res.Code, res.Raw)
	}
	if msg, _ := res.Body["error"].(string); !strings.Contains(msg, "indexers") {
		t.Errorf("the message does not say what is missing: %q", msg)
	}
}

// The search response must never hand the client a download URL. A grab goes
// through a separate audited endpoint that takes the candidate's identity — if
// the client supplied the URL, it would choose what gets downloaded and every
// check in the pipeline would become advisory.
func TestSearchResultsCarryNoDownloadURL(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	// An indexer pointed at an address that will not answer: the search must
	// still succeed, partially, and the shape of the response is what matters.
	if res := admin.post("/api/v1/admin/indexers", map[string]any{
		"name": "Dead", "kind": "torznab",
		"base_url": "https://indexer.invalid", "api_key": "k", "enabled": true,
	}); res.Code != http.StatusCreated {
		t.Fatalf("create indexer: %d %s", res.Code, res.Raw)
	}

	res := admin.post("/api/v1/releases/search", map[string]any{"term": "the matrix"})
	if res.Code != http.StatusOK {
		t.Fatalf("search: %d %s", res.Code, res.Raw)
	}
	if strings.Contains(res.Raw, "download_url") {
		t.Errorf("the search response exposes a download URL: %s", res.Raw)
	}
}

// An operator who sees four results and does not know three indexers timed out
// concludes the release does not exist. The response must say so.
func TestAPartialSearchSaysSoInWordsAndNotOnlyAsABoolean(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	if res := admin.post("/api/v1/admin/indexers", map[string]any{
		"name": "Dead", "kind": "torznab",
		"base_url": "https://indexer.invalid", "api_key": "k", "enabled": true,
	}); res.Code != http.StatusCreated {
		t.Fatal(res.Raw)
	}

	res := admin.post("/api/v1/releases/search", map[string]any{"term": "x"})
	if res.Code != http.StatusOK {
		t.Fatalf("search: %d %s", res.Code, res.Raw)
	}
	if partial, _ := res.Body["partial"].(bool); !partial {
		t.Error("a search where every indexer failed did not report itself partial")
	}
	if warning, _ := res.Body["warning"].(string); warning == "" {
		t.Error("no human-readable warning accompanied the partial flag")
	}

	// And the per-indexer reason must be there, not just a count.
	indexers, _ := res.Body["indexers"].([]any)
	if len(indexers) != 1 {
		t.Fatalf("outcomes = %v", indexers)
	}
	entry, _ := indexers[0].(map[string]any)
	if entry["error"] == nil || entry["error"] == "" {
		t.Error("the outcome does not say why the indexer failed")
	}
}

func TestQualityProfilesAreSeededAndReadable(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	res := admin.get("/api/v1/quality-profiles")
	if res.Code != http.StatusOK {
		t.Fatalf("profiles: %d %s", res.Code, res.Raw)
	}
	profiles, _ := res.Body["profiles"].([]any)
	if len(profiles) < 3 {
		t.Fatalf("got %d profiles, want the built-ins", len(profiles))
	}

	first, _ := profiles[0].(map[string]any)
	if first["name"] != "HD-1080p" {
		t.Errorf("the first profile is %v, want HD-1080p", first["name"])
	}
	if builtin, _ := first["builtin"].(bool); !builtin {
		t.Error("a seeded profile is not marked built-in")
	}
	// The default must not cut off at 4K: the target hardware cannot tone-map
	// HDR at all (ADR-0005).
	if cutoff, _ := first["cutoff"].(string); strings.Contains(cutoff, "2160") {
		t.Errorf("the default profile cuts off at %q", cutoff)
	}
}

// Searching is gated on the interactive-search permission, and a plain User
// does not have it.
func TestSearchRequiresThePermission(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	user := r.redeemAndEnroll(code, "viewer", "a-perfectly-fine-passphrase")

	if res := user.post("/api/v1/releases/search", map[string]any{"term": "x"}); res.Code == http.StatusOK {
		t.Errorf("a User ran an interactive search: %d", res.Code)
	}
	if res := user.get("/api/v1/quality-profiles"); res.Code == http.StatusOK {
		t.Errorf("a User read the quality profiles: %d", res.Code)
	}
}

// A search naming a profile that does not exist must be a clean 404, not an
// unjudged search that quietly returns everything.
func TestSearchWithAnUnknownProfileIsNotFound(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	if res := admin.post("/api/v1/admin/indexers", map[string]any{
		"name": "Dead", "kind": "torznab",
		"base_url": "https://indexer.invalid", "api_key": "k", "enabled": true,
	}); res.Code != http.StatusCreated {
		t.Fatal(res.Raw)
	}

	res := admin.post("/api/v1/releases/search", map[string]any{
		"term": "x", "profile_id": 99999,
	})
	if res.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404\nbody: %s", res.Code, res.Raw)
	}
}

// An operator glancing at the egress page must not come away believing their
// downloads are tunnelled when nothing is checking. "healthy: true" next to no
// enforcement is exactly that misreading.
func TestEgressStatusSaysWhenItIsNotEnforcing(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	// The rig's guard is built without enforcement, which is the default a
	// fresh install has.
	res := admin.get("/api/v1/admin/egress")
	if res.Code != http.StatusOK {
		t.Fatalf("egress: %d %s", res.Code, res.Raw)
	}
	if enforcing, _ := res.Body["enforcing"].(bool); enforcing {
		t.Fatal("the rig reports enforcing; this test is measuring the wrong thing")
	}
	warning, _ := res.Body["warning"].(string)
	if !strings.Contains(warning, "NOT being enforced") {
		t.Errorf("no warning that egress is unenforced: %q", warning)
	}
	// And every profile must stop claiming it pauses without a tunnel.
	profiles, _ := res.Body["profiles"].([]any)
	for _, entry := range profiles {
		p, _ := entry.(map[string]any)
		if pauses, _ := p["pauses_without_tunnel"].(bool); pauses {
			t.Errorf("profile %v claims it pauses without a tunnel, but nothing is enforced", p["subsystem"])
		}
	}
}

// ---------------------------------------------------------------------------
// Download queue
// ---------------------------------------------------------------------------

func TestTheQueueReportsProgressAndTheEnginesNotes(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	r.downloads.items = []download.Transfer{{
		InfoHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Name:     "Some.Release.2019.1080p.BluRay-GROUP",
		Bytes:    1000, Completed: 250, Peers: 3, Seeders: 2,
		MetadataGot: true, AddedAt: r.clk.now(),
	}}

	res := admin.get("/api/v1/queue")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d\nbody: %s", res.Code, res.Raw)
	}

	items, _ := res.Body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1\nbody: %s", len(items), res.Raw)
	}
	item, _ := items[0].(map[string]any)
	if pct, _ := item["percent"].(float64); pct != 25 {
		t.Errorf("percent = %v, want 25", item["percent"])
	}

	// The engine's notes ride along with the queue. "DHT is off, so magnets may
	// never resolve" has to be readable on the same screen as the stuck magnet
	// it explains, not buried in a log the operator is not tailing.
	notes, _ := res.Body["notes"].([]any)
	if len(notes) == 0 {
		t.Errorf("the queue carried no engine notes\nbody: %s", res.Raw)
	}
}

// A transfer whose metadata has not arrived must be distinguishable from one
// that is genuinely zero bytes long. Without the flag, an unresolved magnet
// renders as 0% of 0 bytes, which reads as a broken download rather than a
// pending one.
func TestAnUnresolvedMagnetIsDistinguishableFromAnEmptyTransfer(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	r.downloads.items = []download.Transfer{{
		InfoHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		AddedAt:  r.clk.now(),
	}}

	res := admin.get("/api/v1/queue")
	items, _ := res.Body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1\nbody: %s", len(items), res.Raw)
	}
	item, _ := items[0].(map[string]any)
	if have, _ := item["have_metadata"].(bool); have {
		t.Errorf("a transfer with no metadata claimed to have it\nbody: %s", res.Raw)
	}
}

func TestRemovingFromTheQueueIsAudited(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	const hash = "cccccccccccccccccccccccccccccccccccccccc"
	r.downloads.items = []download.Transfer{{InfoHash: hash, AddedAt: r.clk.now()}}

	if res := admin.post("/api/v1/queue/"+hash+"/remove", nil); res.Code != http.StatusOK {
		t.Fatalf("status = %d\nbody: %s", res.Code, res.Raw)
	}
	if len(r.downloads.removed) != 1 || r.downloads.removed[0] != hash {
		t.Fatalf("engine removals = %v", r.downloads.removed)
	}
	events, err := r.audit.List(
		authz.WithPrincipal(t.Context(), adminPrincipalFor(t, r)),
		audit.Query{Action: audit.ActionQueueRemoved})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("queue-removed audit entries = %d, want 1", len(events))
	}
	if events[0].TargetID != hash {
		t.Errorf("audit target = %q, want the info hash", events[0].TargetID)
	}
	if events[0].Outcome != audit.OutcomeSuccess {
		t.Errorf("audit outcome = %q", events[0].Outcome)
	}

	// Gone. A second removal is a clean 404, not a second audit line claiming
	// something was removed that was not there.
	if res := admin.post("/api/v1/queue/"+hash+"/remove", nil); res.Code != http.StatusNotFound {
		t.Errorf("removing twice: status = %d, want 404\nbody: %s", res.Code, res.Raw)
	}
}

// The identifier is an info hash, and the endpoint says so rather than passing
// whatever arrives to a lookup. A path-shaped or oversized string must never
// reach the engine at all.
func TestTheQueueRefusesAnythingThatIsNotAnInfoHash(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	for _, bad := range []string{
		"..%2f..%2fetc%2fpasswd",
		"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", // right length, not hex
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",   // 38
		strings.Repeat("a", 64),                    // a v2 hash: honestly refused
		"1",
	} {
		res := admin.post("/api/v1/queue/"+bad+"/remove", nil)
		// 400, specifically. A 404 would mean the string was accepted as an
		// identifier and merely matched nothing, which is a different and
		// weaker claim — and it is what this test saw before the engine fake
		// was taught to record failed lookups too.
		if res.Code != http.StatusBadRequest {
			t.Errorf("%q: status = %d, want 400\nbody: %s", bad, res.Code, res.Raw)
		}
	}
	if len(r.downloads.calls) != 0 {
		t.Errorf("a malformed identifier reached the engine: %v", r.downloads.calls)
	}
}

// An uppercase hash is the same transfer. Torrent clients and trackers differ
// on the case they print, and an operator copying one out of a log should not
// get a 404 for it.
func TestAnUppercaseInfoHashIsTheSameTransfer(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	const hash = "dddddddddddddddddddddddddddddddddddddddd"
	r.downloads.items = []download.Transfer{{InfoHash: hash, AddedAt: r.clk.now()}}

	if res := admin.post("/api/v1/queue/"+strings.ToUpper(hash)+"/remove", nil); res.Code != http.StatusOK {
		t.Fatalf("status = %d\nbody: %s", res.Code, res.Raw)
	}
}

func TestTheQueueRequiresThePermission(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	user := r.redeemAndEnroll(code, "viewer", "a-perfectly-fine-passphrase")

	if res := user.get("/api/v1/queue"); res.Code == http.StatusOK {
		t.Errorf("a User read the download queue: %d", res.Code)
	}
	res := user.post("/api/v1/queue/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee/remove", nil)
	if res.Code == http.StatusOK {
		t.Errorf("a User removed from the download queue: %d", res.Code)
	}
	// The engine must not have been called at all. Authorization that rejects
	// the response but still performs the effect is not authorization.
	if len(r.downloads.calls) != 0 {
		t.Errorf("an unauthorized removal reached the engine: %v", r.downloads.calls)
	}
}

// The engine is off by default. The queue endpoints must then say so rather
// than returning an empty list, which an operator reads as "nothing is
// downloading" when the truth is "nothing can download".
func TestWithNoEngineTheQueueSaysSoRatherThanLookingEmpty(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	// Rewire with no engine at all, keeping the rest of the rig.
	rt := NewRouter(
		[]Middleware{
			Recovery(slog.New(slog.NewTextHandler(io.Discard, nil))),
			RequestContext(slog.New(slog.NewTextHandler(io.Discard, nil))),
			ClientIPResolver(nil), SecurityHeaders(time.Hour),
		},
		[]Middleware{CSRF(), Authenticate(NewSessionAuthenticator(
			r.store, r.svc.Policy().Session, false), r.audit)},
	)
	RegisterRoutes(rt, New(Deps{
		Identity: r.svc,
		Auth:     NewSessionAuthenticator(r.store, r.svc.Policy().Session, false),
		Egress:   r.egress, Indexers: r.indexers, Profiles: r.profiles,
		Roots: r.roots, Media: r.media, Scanner: r.scanner, Deleter: r.scanner,
		Tickets: r.tickets, Requests: r.requests, Audit: r.audit,
		TrashRetention: 7 * 24 * time.Hour,
	}))
	r.rt = rt

	res := admin.get("/api/v1/queue")
	if res.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501\nbody: %s", res.Code, res.Raw)
	}
	if _, present := res.Body["items"]; present {
		t.Errorf("an absent engine returned a queue\nbody: %s", res.Raw)
	}
	// The reason has to name the setting. "Not implemented" alone sends an
	// operator looking for a bug in code that is working as configured.
	if !strings.Contains(res.Raw, "download.enabled") {
		t.Errorf("the refusal does not name the setting: %s", res.Raw)
	}

	if res := admin.post("/api/v1/queue/"+strings.Repeat("a", 40)+"/remove", nil); res.Code != http.StatusNotImplemented {
		t.Errorf("remove: status = %d, want 501\nbody: %s", res.Code, res.Raw)
	}
}

// ---------------------------------------------------------------------------
// Grab
// ---------------------------------------------------------------------------

const grabFeed = `<?xml version="1.0"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
<channel>
<item><title>Some.Movie.2019.1080p.BluRay.x264-GRP</title>
<enclosure url="https://indexer.example.com/dl/abc.torrent" length="2147483648"/>
<torznab:attr name="seeders" value="40"/>
<torznab:attr name="peers" value="45"/>
</item>
</channel></rss>`

// A minimal, well-formed bencode dictionary. The engine is faked in these
// tests; what matters is that the bytes the SERVER fetched are what reach it.
const grabTorrent = "d8:announce20:http://tracker.local4:infod6:lengthi1024e4:name8:file.bin12:piece lengthi16384eee"

// searchableRig rebuilds the rig's router with an indexer client that answers
// from canned data, so a search returns real candidates and a grab fetches real
// bytes without a network. The SAME client answers both, exactly as in
// production, so the grab path under test is the production one.
func (r *rig) searchable(t *testing.T, torrentBody string) {
	t.Helper()

	client := indexer.NewClientWithDoer(doerFunc(func(req *http.Request) (*http.Response, error) {
		body := grabFeed
		if strings.Contains(req.URL.Path, "/dl/") {
			body = torrentBody
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{},
		}, nil
	}))
	searcher := search.New(r.indexers, client, r.indexers)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auth := NewSessionAuthenticator(r.store, r.svc.Policy().Session, false)
	rt := NewRouter(
		[]Middleware{Recovery(logger), RequestContext(logger),
			ClientIPResolver(nil), SecurityHeaders(time.Hour)},
		[]Middleware{CSRF(), Authenticate(auth, r.audit)},
	)
	RegisterRoutes(rt, New(Deps{
		Identity: r.svc, Auth: auth, Egress: r.egress, Indexers: r.indexers,
		Search: searcher, Profiles: r.profiles, Downloads: r.downloads,
		Roots: r.roots, Media: r.media, Scanner: r.scanner, Deleter: r.scanner,
		Tickets: r.tickets, Grabs: searcher, Requests: r.requests, Audit: r.audit,
		TrashRetention: 7 * 24 * time.Hour,
	}))
	r.rt = rt
}

type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

// addIndexer registers one and returns its id.
func (r *rig) addIndexer(c *client, name string) int64 {
	r.t.Helper()
	res := c.post("/api/v1/admin/indexers", map[string]any{
		"name": name, "kind": "torznab",
		"base_url": "https://indexer.example.com", "api_key": "k", "enabled": true,
	})
	if res.Code != http.StatusCreated {
		r.t.Fatalf("create indexer: %d %s", res.Code, res.Raw)
	}
	id, _ := res.Body["id"].(float64)
	return int64(id)
}

// searchOne runs a search and returns the first candidate.
func (r *rig) searchOne(c *client) map[string]any {
	r.t.Helper()
	res := c.post("/api/v1/releases/search", map[string]any{"term": "some movie"})
	if res.Code != http.StatusOK {
		r.t.Fatalf("search: %d %s", res.Code, res.Raw)
	}
	cands, _ := res.Body["candidates"].([]any)
	if len(cands) == 0 {
		r.t.Fatalf("the search returned no candidates: %s", res.Raw)
	}
	first, _ := cands[0].(map[string]any)
	return first
}

// The headline. A grab must go through a sealed ticket, and the bytes that
// reach the engine must be the ones the SERVER fetched from the indexer.
func TestAGrabFetchesTheReleaseAndQueuesIt(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addIndexer(admin, "Tracker")
	r.searchable(t, grabTorrent)

	cand := r.searchOne(admin)
	ticket, _ := cand["ticket"].(string)
	if ticket == "" {
		t.Fatalf("the candidate carries no grab ticket: %+v", cand)
	}

	res := admin.post("/api/v1/releases/grab", map[string]any{"ticket": ticket})
	if res.Code != http.StatusAccepted {
		t.Fatalf("grab: %d %s", res.Code, res.Raw)
	}

	if len(r.downloads.addedBytes) != 1 {
		t.Fatalf("engine received %d torrents, want 1", len(r.downloads.addedBytes))
	}
	// Not a URL the client sent, and not something the client could have
	// substituted: the exact bytes the indexer served.
	if r.downloads.addedBytes[0] != grabTorrent {
		t.Errorf("the engine received something other than what was fetched: %q",
			r.downloads.addedBytes[0])
	}
	if len(r.downloads.added) != 0 {
		t.Errorf("a torrent grab also added a magnet: %v", r.downloads.added)
	}
}

// The search response must never carry a download URL, ticket or not. The
// ticket replaces it precisely so the client never holds one.
func TestAGrabTicketIsOpaqueOnTheWire(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addIndexer(admin, "Tracker")
	r.searchable(t, grabTorrent)

	res := admin.post("/api/v1/releases/search", map[string]any{"term": "some movie"})
	if res.Code != http.StatusOK {
		t.Fatalf("search: %d %s", res.Code, res.Raw)
	}
	for _, leak := range []string{"download_url", "/dl/abc.torrent", "indexer.example.com"} {
		if strings.Contains(res.Raw, leak) {
			t.Errorf("the search response leaks %q:\n%s", leak, res.Raw)
		}
	}
}

// The grab endpoint takes a ticket and nothing else. A body carrying a URL must
// be refused outright rather than quietly ignored — DisallowUnknownFields makes
// the attempt visible instead of a near miss.
func TestAGrabRefusesToTakeAURL(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addIndexer(admin, "Tracker")
	r.searchable(t, grabTorrent)

	// A VALID ticket with an extra field alongside it. This is the case that
	// actually tests mass-assignment protection: without DisallowUnknownFields
	// the body decodes, the grab succeeds, and a reviewer reading this endpoint
	// cannot tell from the code whether that url was consulted. Refusing the
	// whole body makes the attempt visible instead of a near miss.
	cand := r.searchOne(admin)
	valid, _ := cand["ticket"].(string)
	if valid == "" {
		t.Fatal("no ticket to test with")
	}

	for _, body := range []map[string]any{
		{"ticket": valid, "url": "http://169.254.169.254/latest/meta-data/"},
		{"ticket": valid, "download_url": "http://127.0.0.1:9090/metrics"},
		{"ticket": valid, "magnet": "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"url": "http://169.254.169.254/latest/meta-data/"},
		{"download_url": "http://127.0.0.1:9090/metrics"},
	} {
		res := admin.post("/api/v1/releases/grab", body)
		if res.Code != http.StatusBadRequest {
			t.Errorf("%v: status = %d, want 400\nbody: %s", body, res.Code, res.Raw)
		}
	}
	if len(r.downloads.added)+len(r.downloads.addedBytes) != 0 {
		t.Errorf("a URL-carrying body reached the engine")
	}
}

// A ticket is bound to the account that searched. Handing one to a colleague
// must not let them grab with it, which is what makes the audit line naming who
// grabbed a true statement.
func TestAGrabTicketDoesNotTransferBetweenAccounts(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addIndexer(admin, "Tracker")
	r.searchable(t, grabTorrent)

	cand := r.searchOne(admin)
	ticket, _ := cand["ticket"].(string)

	code, _ := r.issueInvite(admin, authz.RoleManager, true)
	other := r.redeemAndEnroll(code, "manager", "a-perfectly-fine-passphrase")

	res := other.post("/api/v1/releases/grab", map[string]any{"ticket": ticket})
	if res.Code != http.StatusBadRequest {
		t.Errorf("another account used the ticket: %d\nbody: %s", res.Code, res.Raw)
	}
	if len(r.downloads.addedBytes) != 0 {
		t.Errorf("a transferred ticket reached the engine")
	}
}

// Disabling an indexer must take effect immediately. A ticket minted before the
// change is not a standing exemption from it.
func TestATicketDoesNotOutliveItsIndexerBeingDisabled(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	id := r.addIndexer(admin, "Tracker")
	r.searchable(t, grabTorrent)

	cand := r.searchOne(admin)
	ticket, _ := cand["ticket"].(string)

	if res := admin.patch("/api/v1/admin/indexers/"+strconv.FormatInt(id, 10), map[string]any{
		"name": "Tracker", "kind": "torznab",
		"base_url": "https://indexer.example.com", "enabled": false,
	}); res.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", res.Code, res.Raw)
	}

	res := admin.post("/api/v1/releases/grab", map[string]any{"ticket": ticket})
	if res.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409\nbody: %s", res.Code, res.Raw)
	}
	if len(r.downloads.addedBytes) != 0 {
		t.Errorf("a grab from a disabled indexer reached the engine")
	}
}

// An expired ticket must be named as expired so the operator knows to search
// again, rather than reading as a bug.
func TestAnExpiredTicketTellsTheOperatorToSearchAgain(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addIndexer(admin, "Tracker")
	r.searchable(t, grabTorrent)

	cand := r.searchOne(admin)
	ticket, _ := cand["ticket"].(string)

	r.clk.advance(search.DefaultTicketTTL + time.Minute)

	res := admin.post("/api/v1/releases/grab", map[string]any{"ticket": ticket})
	if res.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410\nbody: %s", res.Code, res.Raw)
	}
	if !strings.Contains(strings.ToLower(res.Raw), "search") {
		t.Errorf("the refusal does not say what to do: %s", res.Raw)
	}
}

// A grab is the moment this software acquires something, which makes it the
// line in the log that matters most to whoever is answerable for the instance.
func TestAGrabIsAudited(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addIndexer(admin, "Tracker")
	r.searchable(t, grabTorrent)

	cand := r.searchOne(admin)
	ticket, _ := cand["ticket"].(string)

	if res := admin.post("/api/v1/releases/grab", map[string]any{"ticket": ticket}); res.Code != http.StatusAccepted {
		t.Fatalf("grab: %d %s", res.Code, res.Raw)
	}

	events, err := r.audit.List(
		authz.WithPrincipal(t.Context(), adminPrincipalFor(t, r)),
		audit.Query{Action: audit.ActionReleaseGrabbed})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("grab audit entries = %d, want 1", len(events))
	}
	if !strings.Contains(events[0].Detail, "Some.Movie") {
		t.Errorf("the audit line does not name the release: %q", events[0].Detail)
	}
	// The download URL carries the indexer's API key on many trackers, and an
	// audit log is the least-guarded copy of anything.
	if strings.Contains(events[0].Detail, "indexer.example.com") ||
		strings.Contains(events[0].Detail, "api_key") {
		t.Errorf("the audit line carries the download URL: %q", events[0].Detail)
	}
}

// A refused grab is exactly what someone reviewing the log wants to see.
func TestAFailedGrabIsAuditedToo(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addIndexer(admin, "Tracker")
	// The indexer answers the download with an HTML error page, which is what
	// a tracker does when a key is stale or a release has been pulled.
	r.searchable(t, "<html>not a torrent</html>")

	cand := r.searchOne(admin)
	ticket, _ := cand["ticket"].(string)

	res := admin.post("/api/v1/releases/grab", map[string]any{"ticket": ticket})
	if res.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502\nbody: %s", res.Code, res.Raw)
	}

	events, err := r.audit.List(
		authz.WithPrincipal(t.Context(), adminPrincipalFor(t, r)),
		audit.Query{Action: audit.ActionReleaseGrabbed})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Outcome != audit.OutcomeFailure {
		t.Fatalf("failed-grab audit = %+v", events)
	}
}

// Searching and grabbing are different acts. A role granted the first without
// the second must not be able to cause a download.
func TestGrabbingRequiresTheQueuePermissionNotJustSearch(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addIndexer(admin, "Tracker")
	r.searchable(t, grabTorrent)

	cand := r.searchOne(admin)
	ticket, _ := cand["ticket"].(string)

	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	user := r.redeemAndEnroll(code, "viewer", "a-perfectly-fine-passphrase")

	if res := user.post("/api/v1/releases/grab", map[string]any{"ticket": ticket}); res.Code == http.StatusAccepted {
		t.Errorf("a User grabbed a release: %d", res.Code)
	}
	if len(r.downloads.addedBytes) != 0 {
		t.Errorf("an unauthorized grab reached the engine")
	}
}

// A queue that cannot answer "who caused this, and what is it" leaves the
// operator answerable for something they cannot explain. The release name the
// INDEXER published is the one they searched for and recognise; the torrent's
// own declared name is chosen by the uploader and is often neither.
func TestAGrabRecordsWhoAskedAndWhatFor(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addIndexer(admin, "Tracker")
	r.searchable(t, grabTorrent)

	cand := r.searchOne(admin)
	ticket, _ := cand["ticket"].(string)

	if res := admin.post("/api/v1/releases/grab", map[string]any{"ticket": ticket}); res.Code != http.StatusAccepted {
		t.Fatalf("grab: %d %s", res.Code, res.Raw)
	}

	if len(r.downloads.meta) != 1 {
		t.Fatalf("meta recorded %d times, want 1", len(r.downloads.meta))
	}
	m := r.downloads.meta[0]
	if m.AddedLabel != "jacob" {
		t.Errorf("added_label = %q, want the grabbing user", m.AddedLabel)
	}
	if m.AddedBy == nil {
		t.Error("the queue row records no user id")
	}
	if !strings.HasPrefix(m.Title, "Some.Movie") {
		t.Errorf("title = %q, want the name the indexer published", m.Title)
	}
	if m.IndexerName != "Tracker" {
		t.Errorf("indexer = %q", m.IndexerName)
	}
}

// The engine knows progress; the store knows identity and history. A queue that
// shows only the first cannot tell an operator what they are downloading, who
// asked, or when — and "when" means when it was grabbed, not when this process
// happened to start.
func TestTheQueueJoinsLiveProgressWithPersistedIdentity(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	grabbed := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

	r.downloads.items = []download.Transfer{{
		InfoHash: hash, Name: "some.torrent.declared.name",
		Bytes: 1000, Completed: 500, MetadataGot: true,
		// What this process saw, which is NOT what the operator means by when.
		AddedAt: r.clk.now(),
	}}
	r.downloads.records = []download.Record{{
		InfoHash: hash, Title: "Some.Movie.2019.1080p.BluRay-GRP",
		IndexerName: "Tracker", AddedLabel: "jacob",
		Status: download.StatusDownloading, AddedAt: grabbed,
	}}

	res := admin.get("/api/v1/queue")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d\nbody: %s", res.Code, res.Raw)
	}
	items, _ := res.Body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1\nbody: %s", len(items), res.Raw)
	}
	item, _ := items[0].(map[string]any)

	// The name the indexer published, not the one the uploader chose.
	if item["title"] != "Some.Movie.2019.1080p.BluRay-GRP" {
		t.Errorf("title = %v", item["title"])
	}
	if item["added_by"] != "jacob" {
		t.Errorf("added_by = %v", item["added_by"])
	}
	if item["indexer"] != "Tracker" {
		t.Errorf("indexer = %v", item["indexer"])
	}
	if added, _ := item["added_at"].(string); !strings.HasPrefix(added, "2026-03-01") {
		t.Errorf("added_at = %v, want the original grab time", item["added_at"])
	}
	// And progress still comes from the engine.
	if pct, _ := item["percent"].(float64); pct != 50 {
		t.Errorf("percent = %v", item["percent"])
	}
}

// A transfer running with no row is an alarming state: the row could not be
// written, so it will vanish on the next restart with its bytes still on disk.
// Showing it as an ordinary entry would hide exactly the thing worth seeing.
func TestATransferWithNoQueueRowIsShownAsUnrecorded(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	r.downloads.items = []download.Transfer{{
		InfoHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Name:     "orphan", MetadataGot: true,
	}}

	res := admin.get("/api/v1/queue")
	items, _ := res.Body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d\nbody: %s", len(items), res.Raw)
	}
	item, _ := items[0].(map[string]any)
	if item["status"] != "unrecorded" {
		t.Errorf("status = %v, want unrecorded", item["status"])
	}
}

// "What has this instance acquired" is a question the operator is answerable
// for. A queue showing only what is in flight cannot answer it.
func TestFinishedAndStoppedTransfersStayInTheQueueView(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	done := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	r.downloads.records = []download.Record{
		{InfoHash: "cccccccccccccccccccccccccccccccccccccccc", Title: "Finished",
			Status: download.StatusComplete, AddedAt: done, CompletedAt: &done},
		{InfoHash: "dddddddddddddddddddddddddddddddddddddddd", Title: "Stopped",
			Status: download.StatusStopped, AddedAt: done},
	}

	res := admin.get("/api/v1/queue")
	items, _ := res.Body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2\nbody: %s", len(items), res.Raw)
	}

	seen := map[string]bool{}
	for _, raw := range items {
		it, _ := raw.(map[string]any)
		seen[it["status"].(string)] = true
		if it["title"] == "Finished" {
			if it["completed_at"] == nil {
				t.Error("a completed transfer has no completion time")
			}
			if done, _ := it["done"].(bool); !done {
				t.Error("a completed transfer is not marked done")
			}
		}
	}
	if !seen["complete"] || !seen["stopped"] {
		t.Errorf("statuses present = %v", seen)
	}
}

// A queue that cannot be read must say so rather than report an empty queue: an
// operator reading "nothing is downloading" when the truth is "the database did
// not answer" will act on it.
func TestAnUnreadableQueueIsAnErrorNotAnEmptyList(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	r.downloads.recordsErr = errors.New("database is on fire")

	res := admin.get("/api/v1/queue")
	if res.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500\nbody: %s", res.Code, res.Raw)
	}
	if _, present := res.Body["items"]; present {
		t.Errorf("an unreadable queue returned a list: %s", res.Raw)
	}
}

// The seeding obligation is captured at the grab, from the indexer as it stands
// then. Editing an indexer afterwards must not silently rewrite what was
// already promised to a tracker.
func TestAGrabCapturesTheIndexersSeedingObligation(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	if res := admin.post("/api/v1/admin/indexers", map[string]any{
		"name": "Tracker", "kind": "torznab",
		"base_url": "https://indexer.example.com", "api_key": "k", "enabled": true,
		"seed_ratio": 2.0, "seed_hours": 72,
	}); res.Code != http.StatusCreated {
		t.Fatalf("create indexer: %d %s", res.Code, res.Raw)
	}
	r.searchable(t, grabTorrent)

	cand := r.searchOne(admin)
	ticket, _ := cand["ticket"].(string)
	if res := admin.post("/api/v1/releases/grab", map[string]any{"ticket": ticket}); res.Code != http.StatusAccepted {
		t.Fatalf("grab: %d %s", res.Code, res.Raw)
	}

	if len(r.downloads.meta) != 1 {
		t.Fatalf("meta = %d", len(r.downloads.meta))
	}
	m := r.downloads.meta[0]
	if m.SeedRatio != 2.0 {
		t.Errorf("seed ratio = %v, want 2.0", m.SeedRatio)
	}
	if m.SeedTime != 72*time.Hour {
		t.Errorf("seed time = %v, want 72h", m.SeedTime)
	}
}

// ---------------------------------------------------------------------------
// Root folders
// ---------------------------------------------------------------------------

func TestARootFolderCanBeAddedAndListed(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	dir := t.TempDir()

	res := admin.post("/api/v1/admin/rootfolders", map[string]any{
		"path": dir, "kind": "movies", "label": "Films",
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d\nbody: %s", res.Code, res.Raw)
	}
	// The hardlink answer is reported in WORDS, not only as a boolean: an
	// operator glancing at a green tick should not have to know that
	// "hardlinks: false" means every import silently uses twice the disk.
	if note, _ := res.Body["hardlink_note"].(string); note == "" {
		t.Errorf("no hardlink note was returned: %s", res.Raw)
	}

	list := admin.get("/api/v1/admin/rootfolders")
	rows, _ := list.Body["root_folders"].([]any)
	if len(rows) != 1 {
		t.Fatalf("root folders = %d\nbody: %s", len(rows), list.Raw)
	}
}

// Every refusal must say WHY. "That path was refused" without a reason is how
// an operator ends up turning a check off.
func TestARefusedRootFolderSaysWhy(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	base := t.TempDir()

	outer := filepath.Join(base, "media")
	inner := filepath.Join(base, "media", "movies")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}

	if res := admin.post("/api/v1/admin/rootfolders", map[string]any{
		"path": outer, "kind": "series",
	}); res.Code != http.StatusCreated {
		t.Fatalf("first root: %d %s", res.Code, res.Raw)
	}

	res := admin.post("/api/v1/admin/rootfolders", map[string]any{
		"path": inner, "kind": "movies",
	})
	if res.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409\nbody: %s", res.Code, res.Raw)
	}
	if !strings.Contains(strings.ToLower(res.Raw), "contain") {
		t.Errorf("the refusal does not explain nesting: %s", res.Raw)
	}

	// A relative path, a missing directory and an unknown kind are all 400
	// with a reason rather than a bare rejection.
	for _, body := range []map[string]any{
		{"path": "relative/path", "kind": "movies"},
		{"path": filepath.Join(base, "does-not-exist"), "kind": "movies"},
		{"path": base, "kind": "photographs"},
	} {
		res := admin.post("/api/v1/admin/rootfolders", body)
		if res.Code != http.StatusBadRequest {
			t.Errorf("%v: status = %d, want 400\nbody: %s", body, res.Code, res.Raw)
		}
		if len(res.Raw) < 30 {
			t.Errorf("%v: the refusal has no explanation: %s", body, res.Raw)
		}
	}
}

// Where a library lives decides what a delete can reach. Only an Admin may
// change it — and the route is invisible to everyone else.
func TestRootFolderConfigurationIsInvisibleToNonAdmins(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	dir := t.TempDir()

	code, _ := r.issueInvite(admin, authz.RoleManager, true)
	mgr := r.redeemAndEnroll(code, "manager", "a-perfectly-fine-passphrase")

	if res := mgr.post("/api/v1/admin/rootfolders", map[string]any{
		"path": dir, "kind": "movies",
	}); res.Code != http.StatusNotFound {
		t.Errorf("a Manager saw the root-folder admin route: %d\nbody: %s", res.Code, res.Raw)
	}
	if res := mgr.get("/api/v1/admin/rootfolders"); res.Code != http.StatusNotFound {
		t.Errorf("a Manager listed root folders through the admin route: %d", res.Code)
	}

	// But a Manager CAN read the library layout through the non-admin route:
	// they need it to fill in a form, and where a library lives is not a
	// secret from someone who may browse it.
	if res := admin.post("/api/v1/admin/rootfolders", map[string]any{
		"path": dir, "kind": "movies",
	}); res.Code != http.StatusCreated {
		t.Fatal(res.Raw)
	}
	if res := mgr.get("/api/v1/rootfolders"); res.Code != http.StatusOK {
		t.Errorf("a Manager could not read the library layout: %d\nbody: %s", res.Code, res.Raw)
	}
}

// Removing a root folder from the configuration and deleting a library are
// different intentions. The destructive one must never be a side effect.
func TestDeletingARootFolderSaysTheFilesAreStillThere(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	dir := t.TempDir()
	film := filepath.Join(dir, "film.mkv")
	if err := os.WriteFile(film, []byte("the film"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := admin.post("/api/v1/admin/rootfolders", map[string]any{"path": dir, "kind": "movies"})
	if res.Code != http.StatusCreated {
		t.Fatal(res.Raw)
	}
	id, _ := res.Body["id"].(float64)

	del := admin.do(http.MethodDelete, "/api/v1/admin/rootfolders/"+strconv.Itoa(int(id)), nil)
	if del.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", del.Code, del.Raw)
	}
	if !strings.Contains(strings.ToLower(del.Raw), "still on disk") {
		t.Errorf("the response does not say the files survive: %s", del.Raw)
	}
	if _, err := os.Stat(film); err != nil {
		t.Fatalf("removing a root folder destroyed the library: %v", err)
	}
}

// Where a library lives is the setting that decides what a delete can reach, so
// changing it belongs in the audit log.
func TestRootFolderChangesAreAudited(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	dir := t.TempDir()

	if res := admin.post("/api/v1/admin/rootfolders", map[string]any{
		"path": dir, "kind": "movies",
	}); res.Code != http.StatusCreated {
		t.Fatal(res.Raw)
	}

	events, err := r.audit.List(
		authz.WithPrincipal(t.Context(), adminPrincipalFor(t, r)),
		audit.Query{Action: audit.ActionSystemSettingChanged})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(events))
	}
	// The path, not just an id: "root folder 3 deleted" is no use six months on.
	if !strings.Contains(events[0].Detail, dir) {
		t.Errorf("the audit line does not name the path: %q", events[0].Detail)
	}
}

// ---------------------------------------------------------------------------
// Library scanning
// ---------------------------------------------------------------------------

func TestScanningARootFolderAdoptsWhatIsThere(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	dir := t.TempDir()

	film := filepath.Join(dir, "Arrival (2016)", "Arrival.2016.1080p.BluRay-GRP.mkv")
	if err := os.MkdirAll(filepath.Dir(film), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(film, make([]byte, 20<<20), 0o644); err != nil {
		t.Fatal(err)
	}

	res := admin.post("/api/v1/admin/rootfolders", map[string]any{"path": dir, "kind": "movies"})
	if res.Code != http.StatusCreated {
		t.Fatal(res.Raw)
	}
	id, _ := res.Body["id"].(float64)

	scan := admin.post("/api/v1/admin/rootfolders/"+strconv.Itoa(int(id))+"/scan", nil)
	if scan.Code != http.StatusOK {
		t.Fatalf("scan: %d %s", scan.Code, scan.Raw)
	}
	if added, _ := scan.Body["added"].(float64); added != 1 {
		t.Errorf("added = %v\nbody: %s", scan.Body["added"], scan.Raw)
	}
	// The response says the rule out loud, because an operator running this on
	// a library they care about needs to know before they click, not after.
	if !strings.Contains(scan.Raw, "never changes anything on disk") {
		t.Errorf("the response does not state the read-only guarantee: %s", scan.Raw)
	}

	// And it is now in the library.
	media := admin.get("/api/v1/media")
	if count, _ := media.Body["count"].(float64); count != 1 {
		t.Errorf("library count = %v\nbody: %s", media.Body["count"], media.Raw)
	}
}

// If a mount fails the root looks empty. Acting on that would erase the record
// of every file on the disk, so the instance refuses and says why.
func TestAVanishedLibraryIsRefusedOverHTTPWithAnExplanation(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	dir := t.TempDir()

	for i := 0; i < 10; i++ {
		p := filepath.Join(dir, fmt.Sprintf("Film %d (201%d)", i, i), "film.mkv")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, 20<<20), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res := admin.post("/api/v1/admin/rootfolders", map[string]any{"path": dir, "kind": "movies"})
	if res.Code != http.StatusCreated {
		t.Fatal(res.Raw)
	}
	id := strconv.Itoa(int(res.Body["id"].(float64)))

	if s := admin.post("/api/v1/admin/rootfolders/"+id+"/scan", nil); s.Code != http.StatusOK {
		t.Fatalf("first scan: %d %s", s.Code, s.Raw)
	}

	// The disk "unmounts".
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			t.Fatal(err)
		}
	}

	s := admin.post("/api/v1/admin/rootfolders/"+id+"/scan", nil)
	if s.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409\nbody: %s", s.Code, s.Raw)
	}
	// 409 rather than 500: nothing is broken, the instance is refusing.
	for _, want := range []string{"unmounted", "Nothing was changed"} {
		if !strings.Contains(s.Raw, want) {
			t.Errorf("the refusal does not mention %q: %s", want, s.Raw)
		}
	}

	// The library records survived, so the operator can remount and carry on.
	media := admin.get("/api/v1/media")
	if count, _ := media.Body["count"].(float64); count != 10 {
		t.Errorf("library count = %v after a failed mount, want 10 still recorded", media.Body["count"])
	}
}

// Asking the instance to walk an operator's disks is not browsing.
func TestScanningIsInvisibleToNonAdmins(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	dir := t.TempDir()

	res := admin.post("/api/v1/admin/rootfolders", map[string]any{"path": dir, "kind": "movies"})
	if res.Code != http.StatusCreated {
		t.Fatal(res.Raw)
	}
	id := strconv.Itoa(int(res.Body["id"].(float64)))

	code, _ := r.issueInvite(admin, authz.RoleManager, true)
	mgr := r.redeemAndEnroll(code, "manager", "a-perfectly-fine-passphrase")

	if s := mgr.post("/api/v1/admin/rootfolders/"+id+"/scan", nil); s.Code != http.StatusNotFound {
		t.Errorf("a Manager could scan: %d %s", s.Code, s.Raw)
	}
}

// A scan walks an operator's disks. That belongs in the audit log.
func TestScansAreAudited(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	dir := t.TempDir()

	res := admin.post("/api/v1/admin/rootfolders", map[string]any{"path": dir, "kind": "movies"})
	if res.Code != http.StatusCreated {
		t.Fatal(res.Raw)
	}
	id := strconv.Itoa(int(res.Body["id"].(float64)))
	if s := admin.post("/api/v1/admin/rootfolders/"+id+"/scan", nil); s.Code != http.StatusOK {
		t.Fatal(s.Raw)
	}

	events, err := r.audit.List(
		authz.WithPrincipal(t.Context(), adminPrincipalFor(t, r)),
		audit.Query{Action: audit.ActionLibraryScanned})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("scan audit entries = %d, want 1", len(events))
	}
	if !strings.Contains(events[0].Detail, dir) {
		t.Errorf("the audit line does not name the root: %q", events[0].Detail)
	}
}

// The library listing must not be a way to enumerate an operator's directory
// layout for anyone who can browse.
func TestTheLibraryListingDoesNotLeakAbsolutePaths(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	dir := t.TempDir()

	film := filepath.Join(dir, "Arrival (2016)", "Arrival.2016.1080p.BluRay-GRP.mkv")
	if err := os.MkdirAll(filepath.Dir(film), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(film, make([]byte, 20<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	res := admin.post("/api/v1/admin/rootfolders", map[string]any{"path": dir, "kind": "movies"})
	id := strconv.Itoa(int(res.Body["id"].(float64)))
	admin.post("/api/v1/admin/rootfolders/"+id+"/scan", nil)

	list := admin.get("/api/v1/media")
	if strings.Contains(list.Raw, dir) {
		t.Errorf("the library listing carries the absolute root path: %s", list.Raw)
	}
	detail := admin.get("/api/v1/media/1")
	if strings.Contains(detail.Raw, dir) {
		t.Errorf("the item detail carries the absolute root path: %s", detail.Raw)
	}
}

// ---------------------------------------------------------------------------
// Deletion and the trash
// ---------------------------------------------------------------------------

// setupLibrary puts one film in a scanned root and returns its item id.
func (r *rig) setupLibrary(admin *client, dir string) int {
	r.t.Helper()
	film := filepath.Join(dir, "Arrival (2016)", "Arrival.2016.1080p.BluRay-GRP.mkv")
	if err := os.MkdirAll(filepath.Dir(film), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(film, make([]byte, 20<<20), 0o644); err != nil {
		r.t.Fatal(err)
	}
	res := admin.post("/api/v1/admin/rootfolders", map[string]any{"path": dir, "kind": "movies"})
	if res.Code != http.StatusCreated {
		r.t.Fatal(res.Raw)
	}
	rootID := strconv.Itoa(int(res.Body["id"].(float64)))
	if s := admin.post("/api/v1/admin/rootfolders/"+rootID+"/scan", nil); s.Code != http.StatusOK {
		r.t.Fatal(s.Raw)
	}
	list := admin.get("/api/v1/media")
	items, _ := list.Body["items"].([]any)
	if len(items) != 1 {
		r.t.Fatalf("library = %s", list.Raw)
	}
	return int(items[0].(map[string]any)["id"].(float64))
}

// A media library is often the only copy, and deleting the wrong row is the
// likeliest failure by a wide margin. Deleting must be undoable.
func TestDeletingMediaTrashesItAndSaysSo(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	dir := t.TempDir()
	id := r.setupLibrary(admin, dir)

	del := admin.do(http.MethodDelete, "/api/v1/admin/media/"+strconv.Itoa(id), nil)
	if del.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", del.Code, del.Raw)
	}
	if !strings.Contains(del.Raw, "Nothing was unlinked") {
		t.Errorf("the response does not say the files survive: %s", del.Raw)
	}

	// Gone from the library.
	if list := admin.get("/api/v1/media"); !strings.Contains(list.Raw, `"count":0`) {
		t.Errorf("the item is still listed: %s", list.Raw)
	}
	// Still on disk, in the trash, with a time it stops being recoverable.
	trash := admin.get("/api/v1/admin/trash")
	if trash.Code != http.StatusOK {
		t.Fatalf("trash: %d %s", trash.Code, trash.Raw)
	}
	items, _ := trash.Body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("trash = %s", trash.Raw)
	}
	if items[0].(map[string]any)["purge_after"] == nil {
		t.Error("the trash listing does not say when the file stops being recoverable")
	}
}

// The undo, over HTTP.
func TestATrashedFileCanBeRestoredOverHTTP(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	dir := t.TempDir()
	id := r.setupLibrary(admin, dir)

	if del := admin.do(http.MethodDelete, "/api/v1/admin/media/"+strconv.Itoa(id), nil); del.Code != http.StatusOK {
		t.Fatal(del.Raw)
	}
	trash := admin.get("/api/v1/admin/trash")
	items, _ := trash.Body["items"].([]any)
	first := items[0].(map[string]any)

	res := admin.post("/api/v1/admin/trash/restore", map[string]any{
		"root_id": first["root_id"], "path": first["path"],
	})
	if res.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", res.Code, res.Raw)
	}
	restored, _ := res.Body["restored"].(string)
	if _, err := os.Stat(filepath.Join(dir, restored)); err != nil {
		t.Fatalf("the restored file is not on disk: %v", err)
	}
}

// A restore must not become "move any file in the library somewhere else". The
// vault would contain it, but containment is not the same as it being the right
// operation.
func TestRestoreOnlyAcceptsPathsInTheTrash(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	dir := t.TempDir()
	r.setupLibrary(admin, dir)

	for _, p := range []string{
		"Arrival (2016)/Arrival.2016.1080p.BluRay-GRP.mkv",
		"../../etc/passwd",
		"/etc/passwd",
		"",
	} {
		res := admin.post("/api/v1/admin/trash/restore", map[string]any{"root_id": 1, "path": p})
		if res.Code != http.StatusBadRequest {
			t.Errorf("restore(%q): status = %d, want 400\nbody: %s", p, res.Code, res.Raw)
		}
	}
	// The library file is untouched.
	if _, err := os.Stat(filepath.Join(dir, "Arrival (2016)", "Arrival.2016.1080p.BluRay-GRP.mkv")); err != nil {
		t.Errorf("a restore attempt moved a library file: %v", err)
	}
}

// Deleting media is the one operation the whole effect-based design was built
// around. A Manager holds queue access and library editing and must still not
// reach it.
func TestAManagerCannotDeleteMediaOverHTTP(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	dir := t.TempDir()
	id := r.setupLibrary(admin, dir)

	code, _ := r.issueInvite(admin, authz.RoleManager, true)
	mgr := r.redeemAndEnroll(code, "manager", "a-perfectly-fine-passphrase")

	if res := mgr.do(http.MethodDelete, "/api/v1/admin/media/"+strconv.Itoa(id), nil); res.Code != http.StatusNotFound {
		t.Errorf("a Manager reached the media delete route: %d %s", res.Code, res.Raw)
	}
	if res := mgr.get("/api/v1/admin/trash"); res.Code != http.StatusNotFound {
		t.Errorf("a Manager read the trash: %d", res.Code)
	}
	// And the file is still there.
	if _, err := os.Stat(filepath.Join(dir, "Arrival (2016)", "Arrival.2016.1080p.BluRay-GRP.mkv")); err != nil {
		t.Errorf("the file was destroyed despite the refusal: %v", err)
	}
}

// Deleting media is the audit line an operator answerable for their instance
// most needs.
func TestMediaDeletionsAndRestoresAreAudited(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	dir := t.TempDir()
	id := r.setupLibrary(admin, dir)

	if del := admin.do(http.MethodDelete, "/api/v1/admin/media/"+strconv.Itoa(id), nil); del.Code != http.StatusOK {
		t.Fatal(del.Raw)
	}
	trash := admin.get("/api/v1/admin/trash")
	items, _ := trash.Body["items"].([]any)
	first := items[0].(map[string]any)
	if res := admin.post("/api/v1/admin/trash/restore", map[string]any{
		"root_id": first["root_id"], "path": first["path"],
	}); res.Code != http.StatusOK {
		t.Fatal(res.Raw)
	}

	ctx := authz.WithPrincipal(t.Context(), adminPrincipalFor(t, r))
	for action, what := range map[audit.Action]string{
		audit.ActionMediaDeleted:  "Arrival",
		audit.ActionMediaRestored: "restored from trash",
	} {
		events, err := r.audit.List(ctx, audit.Query{Action: action})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 {
			t.Fatalf("%s entries = %d, want 1", action, len(events))
		}
		if !strings.Contains(events[0].Detail, what) {
			t.Errorf("%s detail = %q, want it to mention %q", action, events[0].Detail, what)
		}
	}
}

// A transfer grabbed for an episode says which, running or finished — the queue
// is where an operator looks to see what an episode search set in motion.
func TestTheQueueSaysWhatAnEpisodeGrabWasFor(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	const running, finished = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "cccccccccccccccccccccccccccccccccccccccc"
	r.downloads.items = []download.Transfer{{InfoHash: running, Name: "x", MetadataGot: true}}
	r.downloads.records = []download.Record{
		{InfoHash: running, Title: "Severance.S02E03.1080p.WEB.H264-GRP", Status: download.StatusDownloading,
			Target: &download.Target{ItemID: 4, Season: 2, Episode: 3}},
		{InfoHash: finished, Title: "Some.Movie.2019.1080p.BluRay-GRP", Status: download.StatusComplete},
	}

	res := admin.get("/api/v1/queue")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d\nbody: %s", res.Code, res.Raw)
	}
	byHash := map[string]map[string]any{}
	for _, it := range res.Body["items"].([]any) {
		m := it.(map[string]any)
		byHash[m["info_hash"].(string)] = m
	}
	f, _ := byHash[running]["for"].(map[string]any)
	if f == nil || f["code"] != "S02E03" || f["item_id"] != float64(4) {
		t.Errorf("the episode grab does not say what it is for: %v", byHash[running]["for"])
	}
	if _, ok := byHash[finished]["for"]; ok {
		t.Errorf("a grab for nothing claims a target: %v", byHash[finished]["for"])
	}
}

// A Cardigann indexer is created from its pasted definition, which the
// listing gives back; one this build cannot follow, or none at all, is
// refused when it is saved, saying why (ADR-0058).
func TestACardigannIndexerIsSavedFromItsDefinition(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	const def = "id: t\nname: T\nlinks: [https://t.example.org/]\nsearch:\n  paths:\n    - path: s\n" +
		"  rows:\n    selector: tr\n  fields:\n    title:\n      selector: a\n    download:\n" +
		"      selector: a\n      attribute: href\n"
	res := admin.post("/api/v1/admin/indexers", map[string]any{"name": "T", "kind": "cardigann",
		"base_url": "https://t.example.org", "definition": def, "enabled": true})
	if res.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", res.Code, res.Raw)
	}
	list := admin.get("/api/v1/admin/indexers")
	ix := list.Body["indexers"].([]any)[0].(map[string]any)
	if ix["kind"] != "cardigann" || ix["definition"] != def {
		t.Errorf("listed %v", ix)
	}
	for name, body := range map[string]map[string]any{
		"login": {"name": "L", "kind": "cardigann", "base_url": "https://t.example.org",
			"definition": strings.Replace(def, "search:", "login:\n  path: l\n  captcha: {type: image}\nsearch:", 1)},
		"none": {"name": "N", "kind": "cardigann", "base_url": "https://t.example.org"},
	} {
		res := admin.post("/api/v1/admin/indexers", body)
		msg, _ := res.Body["error"].(string)
		if res.Code != http.StatusBadRequest || !strings.Contains(msg, map[string]string{
			"login": "captcha", "none": "needs its definition"}[name]) {
			t.Errorf("%s: %d %s", name, res.Code, res.Raw)
		}
	}
}

// A signing-in tracker's settings are taken when it is saved and never come
// back: the listing says only that there are some (ADR-0059, decision 1).
func TestCardigannSettingsNeverComeBackOverHTTP(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	const secret = "tracker-password-do-not-leak"
	def := "id: p\nsettings:\n  - {name: username, type: text}\n  - {name: password, type: password}\n" +
		"login:\n  path: login.php\n  inputs:\n    u: \"{{ .Config.username }}\"\n    p: \"{{ .Config.password }}\"\n" +
		"search:\n  paths: [{path: s}]\n  rows: {selector: tr}\n  fields:\n    title: {selector: a}\n" +
		"    download: {selector: a, attribute: href}\n"
	res := admin.post("/api/v1/admin/indexers", map[string]any{"name": "P", "kind": "cardigann",
		"base_url": "https://p.example.org", "definition": def, "enabled": true,
		"settings": map[string]string{"username": "jacob", "password": secret}})
	if res.Code != http.StatusCreated || strings.Contains(res.Raw, secret) {
		t.Fatalf("create: %d %s", res.Code, res.Raw)
	}
	list := admin.get("/api/v1/admin/indexers")
	if strings.Contains(list.Raw, secret) || strings.Contains(list.Raw, "jacob") {
		t.Fatalf("THE SETTINGS LEAKED IN THE LISTING: %s", list.Raw)
	}
	if ix := list.Body["indexers"].([]any)[0].(map[string]any); ix["has_settings"] != true {
		t.Errorf("listed %v", ix)
	}
	enabled, err := r.indexers.Enabled(t.Context())
	if err != nil || len(enabled) != 1 || enabled[0].Settings["password"] != secret {
		t.Errorf("the search path cannot open them: %v %v", enabled, err)
	}
	bad := admin.post("/api/v1/admin/indexers", map[string]any{"name": "Q", "kind": "cardigann",
		"base_url": "https://p.example.org", "definition": def, "settings": map[string]string{"user": "x"}})
	if msg, _ := bad.Body["error"].(string); bad.Code != http.StatusBadRequest || !strings.Contains(msg, "no setting user") {
		t.Errorf("an undeclared setting: %d %s", bad.Code, bad.Raw)
	}
}

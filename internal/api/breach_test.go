package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

type breachList struct {
	mu    sync.Mutex
	known map[string]bool
	err   error
	asked int
}

func (b *breachList) Breached(_ context.Context, pw string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asked++
	return b.known[pw], b.err
}

// ADR-0051, decision 1: every place a person sets a password refuses one in
// the breach corpus, and says why.
func TestABreachedPasswordIsRefusedWherePeopleSetThem(t *testing.T) {
	r := newRig(t)
	const leaked = "leaked-passphrase-123"
	list := &breachList{known: map[string]bool{leaked: true}}
	r.svc.SetBreachChecker(list, slog.New(slog.NewTextHandler(io.Discard, nil)))
	breached := func(where string, res response) {
		t.Helper()
		if res.Code != http.StatusBadRequest || !strings.Contains(res.Raw, "known data breach") {
			t.Errorf("%s: %d %s", where, res.Code, res.Raw)
		}
	}

	setup := r.client()
	setup.visitPage("/setup")
	breached("setup", setup.post("/api/v1/setup", map[string]any{
		"username": "jacob", "email": "jacob@example.com", "password": leaked}))
	admin := r.bootstrapAdmin()

	anon := r.client()
	anon.visitPage("/signup")
	breached("signup", anon.post("/api/v1/auth/signup", map[string]any{
		"username": "newcomer", "email": "new@example.com", "password": leaked}))

	breached("change", admin.post("/api/v1/me/password", map[string]any{
		"current_password": "correct-horse-battery", "new_password": leaked}))

	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	r.redeemAndEnroll(code, "friend", "invited-passphrase-1")
	user, _ := r.store.UserByUsername(t.Context(), "friend")
	link := admin.post("/api/v1/admin/users/"+strconv.FormatInt(user.ID, 10)+"/reset-link", nil)
	token, _ := link.Body["token"].(string)
	reset := r.client()
	reset.visitPage("/reset")
	breached("reset", reset.post("/api/v1/auth/reset/complete", map[string]any{"token": token, "password": leaked}))
	if res := reset.post("/api/v1/auth/reset/complete", map[string]any{"token": token,
		"password": "an-unknown-passphrase-9"}); res.Code != http.StatusOK {
		t.Errorf("the refused reset spent its token: %d %s", res.Code, res.Raw)
	}

	// HIBP unreachable: accepted.
	list.mu.Lock()
	list.err = errors.New("no route to host")
	list.mu.Unlock()
	if res := admin.post("/api/v1/me/password", map[string]any{
		"current_password": "correct-horse-battery", "new_password": leaked}); res.Code != http.StatusOK {
		t.Errorf("a check that could not be made refused the change: %d %s", res.Code, res.Raw)
	}
}

// ADR-0051, decision 2: a signup pays a proof-of-work fetched from an
// anonymous route, once.
func TestASignupPaysItsProofOfWork(t *testing.T) {
	r := newRig(t)
	key, _ := secrets.GenerateKey()
	cipher, _ := secrets.NewCipherFromBase64(key)
	r.svc.SetProofOfWork(identity.NewProofOfWork(cipher, 8, nil))

	anon := r.client()
	anon.visitPage("/signup")
	ch := anon.get("/api/v1/auth/signup/challenge")
	token, _ := ch.Body["challenge"].(string)
	if ch.Code != http.StatusOK || token == "" || ch.Body["bits"] != float64(8) {
		t.Fatalf("challenge: %d %s", ch.Code, ch.Raw)
	}
	body := func(user, counter string) map[string]any {
		return map[string]any{"username": user, "email": user + "@example.com",
			"password": "a-perfectly-fine-passphrase", "pow_challenge": token, "pow_counter": counter}
	}
	if res := anon.post("/api/v1/auth/signup", map[string]any{"username": "a", "email": "a@example.com",
		"password": "a-perfectly-fine-passphrase"}); res.Code != http.StatusBadRequest || !strings.Contains(res.Raw, "challenge") {
		t.Errorf("a signup with no proof: %d %s", res.Code, res.Raw)
	}
	counter := ""
	for c := 0; ; c++ {
		if s := strconv.Itoa(c); identity.Solved(token, s, 8) {
			counter = s
			break
		}
	}
	if res := anon.post("/api/v1/auth/signup", body("first", counter)); res.Code != http.StatusAccepted {
		t.Fatalf("a signup with its proof: %d %s", res.Code, res.Raw)
	}
	if res := anon.post("/api/v1/auth/signup", body("second", counter)); res.Code != http.StatusBadRequest {
		t.Errorf("a proof spent twice: %d %s", res.Code, res.Raw)
	}
}

package identity

import (
	"context"
	"crypto/sha1" // #nosec G505 -- recomputing the range protocol's index in a test
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

// ADR-0051, decision 1: the range API is asked k-anonymously — five characters
// out, padding asked for, the match made here, a padding entry nobody's.
func TestPwnedPasswordsIsAskedKAnonymously(t *testing.T) {
	breached := "correct horse battery staple"
	sum := sha1.Sum([]byte(breached)) // #nosec G401 -- see the import
	full := strings.ToUpper(hex.EncodeToString(sum[:]))
	padding := "padded entry"
	psum := sha1.Sum([]byte(padding)) // #nosec G401 -- see the import
	pfull := strings.ToUpper(hex.EncodeToString(psum[:]))

	var mu sync.Mutex
	var paths, pads, agents []string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.RequestURI())
		pads = append(pads, r.Header.Get("Add-Padding"))
		agents = append(agents, r.Header.Get("User-Agent"))
		code := status
		mu.Unlock()
		w.WriteHeader(code)
		// What the service answers for a prefix: suffixes and counts, padding
		// entries with a count of zero.
		_, _ = w.Write([]byte("0018A45C4D1DEF81644B54AB7F969B88D65:3\r\n" +
			full[5:] + ":1201\r\n" + pfull[5:] + ":0\r\n"))
	}))
	defer srv.Close()
	pp := NewPwnedPasswords(srv.Client(), srv.URL, "test")

	if got, err := pp.Breached(t.Context(), breached); err != nil || !got {
		t.Errorf("a breached password: %v %v", got, err)
	}
	if got, err := pp.Breached(t.Context(), padding); err != nil || got {
		t.Errorf("a padding entry was taken for a breach: %v %v", got, err)
	}
	if got, err := pp.Breached(t.Context(), "nothing like it"); err != nil || got {
		t.Errorf("an unbreached password: %v %v", got, err)
	}
	mu.Lock()
	status = http.StatusServiceUnavailable
	mu.Unlock()
	if _, err := pp.Breached(t.Context(), breached); err == nil {
		t.Error("a 503 was read as an answer")
	}

	mu.Lock()
	defer mu.Unlock()
	if paths[0] != "/range/"+full[:5] {
		t.Errorf("asked %q, want only the five-character prefix %s", paths[0], full[:5])
	}
	for i, p := range paths {
		if strings.Contains(p, full[5:]) || strings.Contains(p, pfull[5:]) {
			t.Errorf("request %d carried more than the prefix: %s", i, p)
		}
		if pads[i] != "true" || !strings.HasPrefix(agents[i], "CMediaStack/test") {
			t.Errorf("request %d: Add-Padding %q, User-Agent %q", i, pads[i], agents[i])
		}
	}
}

type fakeBreach struct {
	breached map[string]bool
	err      error
	asked    []string
}

func (f *fakeBreach) Breached(_ context.Context, pw string) (bool, error) {
	f.asked = append(f.asked, pw)
	return f.breached[pw], f.err
}

// ADR-0051, decision 1: the policy first, then the check; a check that cannot
// be made accepts; off is off.
func TestTheBreachCheckIsTheLastWord(t *testing.T) {
	f := newAdminFixture(t)
	f.svc.policy.Password = PasswordPolicy{MinLength: 12}
	check := &fakeBreach{breached: map[string]bool{"Password1234!": true}}

	if err := f.svc.acceptablePassword(t.Context(), "Password1234!"); err != nil {
		t.Errorf("with the check off: %v", err)
	}
	f.svc.SetBreachChecker(check, nil)
	if err := f.svc.acceptablePassword(t.Context(), "Password1234!"); !errors.Is(err, ErrPasswordBreached) {
		t.Errorf("a breached password: %v", err)
	}
	if err := f.svc.acceptablePassword(t.Context(), "short"); !errors.Is(err, ErrPasswordTooShort) || len(check.asked) != 1 {
		t.Errorf("a short password: %v, and HIBP asked %d time(s) — the policy decides first", err, len(check.asked))
	}
	if err := f.svc.acceptablePassword(t.Context(), "a fine long passphrase"); err != nil {
		t.Errorf("an unbreached password: %v", err)
	}
	check.err = errors.New("no route to host")
	if err := f.svc.acceptablePassword(t.Context(), "Password1234!"); err != nil {
		t.Errorf("a check that could not be made refused the password: %v", err)
	}
}

func solve(t *testing.T, token string, bits int) string {
	t.Helper()
	for c := 0; c < 1<<24; c++ {
		if s := strconv.Itoa(c); Solved(token, s, bits) {
			return s
		}
	}
	t.Fatal("no solution")
	return ""
}

// ADR-0051, decision 2.
func TestASignupChallengeIsSealedSolvedAndSpent(t *testing.T) {
	key, _ := secrets.GenerateKey()
	cipher, _ := secrets.NewCipherFromBase64(key)
	clock := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	p := NewProofOfWork(cipher, 8, func() time.Time { return clock })

	token, err := p.Challenge()
	if err != nil {
		t.Fatal(err)
	}
	counter := solve(t, token, 8)
	// A different first character, always: a forgery that happened to be the
	// token would spend it.
	forged := "A" + token[1:]
	if token[0] == 'A' {
		forged = "B" + token[1:]
	}
	wrong := "0"
	for Solved(token, wrong, 8) {
		wrong += "0"
	}
	for name, tc := range map[string][2]string{
		"no challenge":     {"", counter},
		"no counter":       {token, ""},
		"not a number":     {token, "12a"},
		"unsolved":         {token, wrong},
		"forged":           {forged, counter},
		"another spelling": {token + "A", counter},
	} {
		if err := p.Verify(tc[0], tc[1]); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if err := p.Verify(token, counter); err != nil {
		t.Fatalf("a solved challenge: %v", err)
	}
	if err := p.Verify(token, counter); !errors.Is(err, ErrProofInvalid) {
		t.Errorf("a challenge spent twice: %v", err)
	}

	// Expired.
	late, _ := p.Challenge()
	lateCounter := solve(t, late, 8)
	clock = clock.Add(ProofTTL)
	if err := p.Verify(late, lateCounter); !errors.Is(err, ErrProofInvalid) {
		t.Errorf("an expired challenge: %v", err)
	}
	// Issued at 8 bits, checked after the price rose to 12: the higher price.
	hard := NewProofOfWork(cipher, 12, func() time.Time { return clock })
	early, _ := p.Challenge()
	cheap := ""
	for c := 0; ; c++ {
		s := strconv.Itoa(c)
		if Solved(early, s, 8) && !Solved(early, s, 12) {
			cheap = s
			break
		}
	}
	if err := hard.Verify(early, cheap); !errors.Is(err, ErrProofInvalid) {
		t.Errorf("a challenge solved at the old price: %v", err)
	}
	// Off: anything.
	if err := NewProofOfWork(cipher, 0, nil).Verify("", ""); err != nil {
		t.Errorf("with the proof off: %v", err)
	}
}

// ADR-0051, decision 2: a closed registration answers as before — the proof
// is asked for only where a signup could be made.
func TestAClosedRegistrationSaysNothingOfTheProof(t *testing.T) {
	f := newAdminFixture(t) // its policy names no registration mode: closed
	key, _ := secrets.GenerateKey()
	cipher, _ := secrets.NewCipherFromBase64(key)
	f.svc.SetProofOfWork(NewProofOfWork(cipher, 8, nil))
	if _, err := f.svc.Signup(t.Context(), SignupInput{Username: "x", Email: "x@example.com",
		Password: "a fine long passphrase"}); !errors.Is(err, ErrRegistrationClosed) {
		t.Errorf("a closed signup without a proof: %v", err)
	}
	if _, _, err := f.svc.SignupChallenge(); !errors.Is(err, ErrRegistrationClosed) {
		t.Errorf("a closed challenge: %v", err)
	}
	f.svc.policy.RegistrationMode = "open"
	if tok, bits, err := f.svc.SignupChallenge(); err != nil || tok == "" || bits != 8 {
		t.Errorf("an open challenge: %q %d %v", tok, bits, err)
	}
	if _, err := f.svc.Signup(t.Context(), SignupInput{Username: "x", Email: "x@example.com",
		Password: "a fine long passphrase"}); !errors.Is(err, ErrProofRequired) {
		t.Errorf("an open signup without a proof: %v", err)
	}
	f.svc.SetProofOfWork(nil)
	if tok, bits, err := f.svc.SignupChallenge(); err != nil || tok != "" || bits != 0 {
		t.Errorf("with the proof off: %q %d %v", tok, bits, err)
	}
}

// Solved counts zero bits exactly: for many counters, at every difficulty up to
// 16, it agrees with a count made bit by bit.
func TestSolvedCountsLeadingZeroBitsExactly(t *testing.T) {
	for c := 0; c < 4000; c++ {
		s := strconv.Itoa(c)
		sum := sha256.Sum256([]byte("tok:" + s))
		zeros := 0
	count:
		for _, b := range sum {
			for bit := 7; bit >= 0; bit-- {
				if b>>uint(bit)&1 == 1 {
					break count
				}
				zeros++
			}
		}
		for need := 0; need <= 16; need++ {
			if got := Solved("tok", s, need); got != (zeros >= need) {
				t.Fatalf("counter %s, %d zero bits: Solved(%d) = %v", s, zeros, need, got)
			}
		}
	}
}

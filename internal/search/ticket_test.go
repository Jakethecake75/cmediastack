package search

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
	"github.com/jakethecake75/cmediastack/internal/release"
)

func testCipher(t *testing.T) *secrets.Cipher {
	t.Helper()
	k, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	c, err := secrets.NewCipherFromBase64(k)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testCandidate() Candidate {
	return Candidate{
		Result: indexer.Result{
			IndexerID: 7, Title: "Some.Movie.2019.1080p.BluRay-GRP",
			DownloadURL: "https://indexer.example.com/dl/abc.torrent",
			Size:        1 << 30,
		},
		Quality: release.Quality{Name: "Bluray-1080p"},
	}
}

func TestATicketRoundTrips(t *testing.T) {
	tk := NewTickets(testCipher(t), time.Hour, nil)

	token, err := tk.Seal(testCandidate(), 42)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tk.Open(token, 42)
	if err != nil {
		t.Fatal(err)
	}
	if got.DownloadURL != testCandidate().DownloadURL {
		t.Errorf("url = %q", got.DownloadURL)
	}
	if got.IndexerID != 7 {
		t.Errorf("indexer = %d", got.IndexerID)
	}
}

// The whole point of sealing. If the URL were readable, the client could read
// it, change it, and hand it back — which is the design this replaces.
func TestATicketDoesNotRevealTheDownloadURL(t *testing.T) {
	tk := NewTickets(testCipher(t), time.Hour, nil)

	token, err := tk.Seal(testCandidate(), 42)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"indexer.example.com", "abc.torrent", "https"} {
		if strings.Contains(token, secret) {
			t.Errorf("the token leaks %q: %s", secret, token)
		}
	}
}

// A ticket is not a bearer capability that travels between accounts. Binding
// it to the holder is what makes the audit line naming who grabbed true.
func TestATicketCannotBeUsedByAnotherAccount(t *testing.T) {
	tk := NewTickets(testCipher(t), time.Hour, nil)

	token, err := tk.Seal(testCandidate(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tk.Open(token, 43); !errors.Is(err, ErrTicketInvalid) {
		t.Errorf("another account opened the ticket: err = %v", err)
	}
}

func TestAnExpiredTicketIsNamedAsExpired(t *testing.T) {
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	tk := NewTickets(testCipher(t), 30*time.Minute, clock)

	token, err := tk.Seal(testCandidate(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tk.Open(token, 42); err != nil {
		t.Fatalf("a fresh ticket was refused: %v", err)
	}

	now = now.Add(31 * time.Minute)
	// Named, not folded into "invalid": the operator needs to know to search
	// again, and saying so reveals nothing they do not already know.
	if _, err := tk.Open(token, 42); !errors.Is(err, ErrTicketExpired) {
		t.Errorf("err = %v, want ErrTicketExpired", err)
	}
}

// Any tampering must fail closed. AES-GCM is what makes this true; the test is
// here so that a future change away from an AEAD is caught immediately.
func TestATamperedTicketIsRefused(t *testing.T) {
	tk := NewTickets(testCipher(t), time.Hour, nil)

	token, err := tk.Seal(testCandidate(), 42)
	if err != nil {
		t.Fatal(err)
	}

	for i := range token {
		b := []byte(token)
		// Flip to a different character in the same alphabet.
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		if _, err := tk.Open(string(b), 42); err == nil {
			t.Fatalf("a ticket with byte %d changed was accepted", i)
		}
	}
}

func TestGarbageTicketsAreRefusedWithoutPanicking(t *testing.T) {
	tk := NewTickets(testCipher(t), time.Hour, nil)

	for _, bad := range []string{
		"", "!!!!", "AAAA", strings.Repeat("A", MaxTicketBytes+1),
		"../../etc/passwd", "null", "e30",
	} {
		if _, err := tk.Open(bad, 42); err == nil {
			t.Errorf("Open(%q) succeeded", bad)
		}
	}
}

// A sealer with no cipher must refuse, not emit something readable. "Failed
// open" here would mean plaintext URLs on the wire and a forgeable grab.
func TestASealerWithNoCipherRefusesRatherThanEmittingPlaintext(t *testing.T) {
	tk := NewTickets(nil, time.Hour, nil)

	if _, err := tk.Seal(testCandidate(), 42); !errors.Is(err, ErrNoTickets) {
		t.Errorf("Seal err = %v, want ErrNoTickets", err)
	}
	if _, err := tk.Open("anything", 42); !errors.Is(err, ErrNoTickets) {
		t.Errorf("Open err = %v, want ErrNoTickets", err)
	}
}

// Two seals of the same candidate must differ: AES-GCM uses a fresh nonce, so
// a token is not a stable identifier an observer can correlate across searches.
func TestTwoSealsOfTheSameCandidateDiffer(t *testing.T) {
	tk := NewTickets(testCipher(t), time.Hour, nil)

	a, err := tk.Seal(testCandidate(), 42)
	if err != nil {
		t.Fatal(err)
	}
	b, err := tk.Seal(testCandidate(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two seals produced an identical token, so the nonce is not fresh")
	}
}

// A ticket sealed under one instance's key must not open under another's.
func TestATicketFromAnotherInstanceIsRefused(t *testing.T) {
	a := NewTickets(testCipher(t), time.Hour, nil)
	b := NewTickets(testCipher(t), time.Hour, nil)

	token, err := a.Seal(testCandidate(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Open(token, 42); !errors.Is(err, ErrTicketInvalid) {
		t.Errorf("err = %v, want ErrTicketInvalid", err)
	}
}

// A ticket must have exactly one spelling.
//
// base64 is malleable at the end: when the sealed length is not a multiple of
// three, the final character carries bits nothing reads, and up to 15 other
// characters decode to identical bytes. Non-strict decoding accepts them all,
// so one ticket would have sixteen valid tokens.
//
// Not a forgery — the ciphertext is identical, and the AEAD is behaving
// correctly. The problem is that anything keying on the token STRING rather
// than on what it decodes to (single-use tracking, rate limiting, correlating
// an audit line to a grab) would treat those as different tickets.
//
// This is also what made TestATamperedTicketIsRefused fail intermittently,
// which is the worse half: a flaky test about tampering is one people learn to
// re-run.
func TestATicketHasExactlyOneSpelling(t *testing.T) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

	// A FIXED clock, and that is not tidiness. A ticket carries an expiry, and
	// its JSON representation changes length as trailing zeros come and go —
	// so with a real clock the sealed length shifts BETWEEN the attempts below,
	// and scanning three padding lengths can miss the residue it is looking
	// for. It did: this test failed on a later sweep with "could not produce a
	// ticket whose length has encoding slack", which is the same intermittency
	// it was written to remove, one level up.
	fixed := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	tk := NewTickets(testCipher(t), time.Hour, func() time.Time { return fixed })

	// The slack exists only when the sealed length is not a multiple of three.
	// AES-GCM is a stream mode, so one more character of plaintext is one more
	// byte of ciphertext: with the clock held still, three consecutive lengths
	// must cover all three residues.
	token, pad := "", 0
	for ; pad < 3; pad++ {
		c := testCandidate()
		c.Title += strings.Repeat("x", pad)
		got, err := tk.Seal(c, 42)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := base64.RawURLEncoding.Strict().DecodeString(got)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw)%3 == 1 {
			// 15 other final characters decode identically at this length: the
			// worst case, and the one worth testing.
			token = got
			break
		}
	}
	if token == "" {
		t.Fatal("could not produce a ticket whose length has encoding slack, " +
			"which should be impossible across three consecutive lengths with " +
			"the clock held still")
	}
	if _, err := tk.Open(token, 42); err != nil {
		t.Fatalf("precondition: the ticket does not open: %v", err)
	}

	// Only the final character can carry slack bits, so that is where to look.
	b := []byte(token)
	last := len(b) - 1
	original := b[last]

	var accepted []byte
	for i := 0; i < len(alphabet); i++ {
		if alphabet[i] == original {
			continue
		}
		b[last] = alphabet[i]
		if _, err := tk.Open(string(b), 42); err == nil {
			accepted = append(accepted, alphabet[i])
		}
	}
	if len(accepted) > 0 {
		t.Errorf("%d alternative spellings of one ticket were accepted (%q instead "+
			"of %q); anything keying on the token string would see them as "+
			"different tickets", len(accepted), accepted, original)
	}

	// And the real one still works, so this did not pass by breaking the ticket.
	b[last] = original
	if _, err := tk.Open(string(b), 42); err != nil {
		t.Errorf("the canonical ticket stopped opening: %v", err)
	}
}

package search

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

// Errors a caller distinguishes.
var (
	ErrTicketInvalid = errors.New("search: the grab ticket is not valid")
	ErrTicketExpired = errors.New("search: the grab ticket has expired")
	ErrNoTickets     = errors.New("search: no ticket sealer is wired")
)

// DefaultTicketTTL bounds how long a search result stays grabbable.
//
// Long enough that an operator can read a page of results and decide; short
// enough that a ticket copied out of a browser's network tab is not a
// standing capability. An expired ticket is a clean, named refusal, so the
// failure mode is "search again", not a mystery.
const DefaultTicketTTL = 30 * time.Minute

// Ticket is a candidate's identity on the wire.
//
// It exists because the grab endpoint must NOT take a download URL from the
// client. If it did, anyone who could reach it could make the server fetch a
// URL of their choosing through the egress-guarded HTTP client — the indexer
// checks, the profile, the quality ladder and the audit trail would all
// describe a release that was never the one downloaded.
//
// Sealing solves a narrow problem precisely: it proves the client did not
// CHANGE what the indexer offered. It does not make the URL safe — the indexer
// is still a third party — so the fetch path validates it again, and revalidates
// every redirect hop.
type Ticket struct {
	IndexerID   int64     `json:"i"`
	DownloadURL string    `json:"u"`
	InfoHash    string    `json:"h,omitempty"`
	Title       string    `json:"t"`
	Size        int64     `json:"s,omitempty"`
	Quality     string    `json:"q,omitempty"`
	IssuedFor   int64     `json:"f"`
	ExpiresAt   time.Time `json:"e"`
	// Target is the episode or film a targeted search matched this release
	// to. Sealed like everything else here, so the client cannot attach a
	// release to anything the server did not match it to (ADR-0023,
	// ADR-0026). Nil for a release found by the general search.
	Target *Target `json:"g,omitempty"`
}

// Tickets seals and opens grab tickets.
type Tickets struct {
	cipher *secrets.Cipher
	ttl    time.Duration
	now    func() time.Time
}

// NewTickets builds a sealer. A nil cipher yields a sealer that refuses
// everything rather than one that emits unsealed tickets.
func NewTickets(c *secrets.Cipher, ttl time.Duration, now func() time.Time) *Tickets {
	if ttl <= 0 {
		ttl = DefaultTicketTTL
	}
	if now == nil {
		now = time.Now
	}
	return &Tickets{cipher: c, ttl: ttl, now: now}
}

// ticketContext is the AEAD associated data.
//
// Binding the user id into the AAD means a ticket issued to one account cannot
// be opened under another: passing a colleague a grab link does not let them
// grab as themselves, and the audit line naming who grabbed is therefore true.
// The literal prefix keeps a ticket from being interchangeable with any other
// sealed value in the database, which all share this cipher.
func ticketContext(userID int64) string {
	return fmt.Sprintf("release-grab:v1:%d", userID)
}

// Seal returns an opaque token identifying one candidate for one user.
func (t *Tickets) Seal(c Candidate, userID int64) (string, error) {
	if t == nil || t.cipher == nil {
		return "", ErrNoTickets
	}
	tk := Ticket{
		IndexerID:   c.IndexerID,
		DownloadURL: c.DownloadURL,
		InfoHash:    c.InfoHash,
		Title:       c.Title,
		Size:        c.Size,
		Quality:     c.Quality.Name,
		IssuedFor:   userID,
		ExpiresAt:   t.now().Add(t.ttl).UTC(),
	}
	if c.Target != nil {
		if !c.Target.Valid() {
			return "", fmt.Errorf("%w: %+v is neither an episode nor a film", ErrTicketInvalid, *c.Target)
		}
		target := *c.Target
		tk.Target = &target
	}
	raw, err := json.Marshal(tk)
	if err != nil {
		return "", fmt.Errorf("search: sealing a ticket: %w", err)
	}
	sealed, err := t.cipher.Encrypt(raw, ticketContext(userID))
	if err != nil {
		return "", fmt.Errorf("search: sealing a ticket: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// MaxTicketBytes caps a token before any decoding work is done. A real one is
// a few hundred bytes; the cap stops a large body from becoming decode work.
const MaxTicketBytes = 8 << 10

// Open recovers a ticket, or refuses.
//
// Every failure below is a refusal to act. In particular an expired ticket is
// named as expired rather than folded into "invalid": the operator needs to
// know to search again, and telling them so reveals nothing — they already
// know they made the ticket.
func (t *Tickets) Open(token string, userID int64) (Ticket, error) {
	if t == nil || t.cipher == nil {
		return Ticket{}, ErrNoTickets
	}
	if token == "" || len(token) > MaxTicketBytes {
		return Ticket{}, ErrTicketInvalid
	}
	// Strict, so a ticket has exactly ONE spelling.
	//
	// base64 is malleable at the end: when the sealed length is not a multiple
	// of three, the final character carries bits that nothing reads, and 15
	// different characters decode to identical bytes. Non-strict decoding
	// accepts all of them, so one ticket has sixteen valid tokens.
	//
	// That is not a forgery — the ciphertext is identical and the AEAD is doing
	// its job — but it is a footgun with a fuse on it. Anything that ever keys
	// on the token STRING rather than on what it decodes to (single-use
	// tracking, rate limiting, correlating an audit line to a grab) would treat
	// those sixteen as different tickets. Making the encoding canonical now
	// costs one method call and removes the whole class.
	//
	// Found because it made TestATamperedTicketIsRefused fail intermittently:
	// that test flips each byte to 'A', and 'A' is sometimes one of the fifteen.
	sealed, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return Ticket{}, ErrTicketInvalid
	}
	// A ticket sealed for another user fails HERE, in the AEAD, not in a
	// comparison afterwards that someone could later forget to write.
	raw, err := t.cipher.Decrypt(sealed, ticketContext(userID))
	if err != nil {
		return Ticket{}, ErrTicketInvalid
	}

	var tk Ticket
	if err := json.Unmarshal(raw, &tk); err != nil {
		return Ticket{}, ErrTicketInvalid
	}
	// Belt and braces: the AAD already made this true. Checking it again costs
	// one comparison and means a future change to the AAD cannot quietly turn
	// tickets transferable.
	if tk.IssuedFor != userID {
		return Ticket{}, ErrTicketInvalid
	}
	if tk.DownloadURL == "" {
		return Ticket{}, ErrTicketInvalid
	}
	if tk.Target != nil && !tk.Target.Valid() {
		return Ticket{}, ErrTicketInvalid
	}
	if t.now().After(tk.ExpiresAt) {
		return Ticket{}, ErrTicketExpired
	}
	return tk, nil
}

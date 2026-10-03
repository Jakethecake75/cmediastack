package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/bits"
	"strconv"
	"sync"
	"time"

	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

// Signup proof-of-work (ADR-0051, decision 2): a challenge sealed with the
// master key, solved by the client, checked before any Argon2id time is spent.

// Errors of the proof.
var (
	// ErrProofRequired means a signup carried no solved challenge.
	ErrProofRequired = errors.New("identity: the signup did not carry a solved challenge; fetch one and solve it")
	// ErrProofInvalid means the challenge is forged, expired, used, or unsolved.
	ErrProofInvalid = errors.New("identity: the signup's challenge is not valid; fetch a new one")
)

// ProofTTL is how long a challenge may be solved and used.
const ProofTTL = 10 * time.Minute

// maxChallengeBytes bounds a challenge token before anything is decoded.
const maxChallengeBytes = 512

const proofContext = "cms:signup-pow:v1"

type challenge struct {
	Nonce   string    `json:"n"`
	Bits    int       `json:"b"`
	Expires time.Time `json:"e"`
}

// ProofOfWork issues and checks signup challenges.
type ProofOfWork struct {
	cipher *secrets.Cipher
	bits   int
	now    func() time.Time

	mu   sync.Mutex
	used map[string]time.Time // nonce → its expiry
}

// NewProofOfWork builds one. Zero bits turns the proof off.
func NewProofOfWork(cipher *secrets.Cipher, bits int, now func() time.Time) *ProofOfWork {
	if now == nil {
		now = time.Now
	}
	return &ProofOfWork{cipher: cipher, bits: bits, now: now, used: map[string]time.Time{}}
}

// Bits is how many leading zero bits a solution needs; zero is off.
func (p *ProofOfWork) Bits() int {
	if p == nil || p.cipher == nil {
		return 0
	}
	return p.bits
}

// Challenge issues a sealed challenge.
func (p *ProofOfWork) Challenge() (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	raw, err := json.Marshal(challenge{Nonce: hex.EncodeToString(nonce), Bits: p.bits,
		Expires: p.now().Add(ProofTTL).UTC()})
	if err != nil {
		return "", err
	}
	sealed, err := p.cipher.Encrypt(raw, proofContext)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Solved reports whether SHA-256(token + ":" + counter) begins with bits zero
// bits — the one hash the server computes per signup.
func Solved(token, counter string, need int) bool {
	sum := sha256.Sum256([]byte(token + ":" + counter))
	zeros := 0
	for _, b := range sum {
		if b == 0 {
			zeros += 8
			continue
		}
		zeros += bits.LeadingZeros8(b)
		break
	}
	return zeros >= need
}

// Verify checks a solved challenge and spends it. With the proof off it
// accepts anything.
func (p *ProofOfWork) Verify(token, counter string) error {
	need := p.Bits()
	if need == 0 {
		return nil
	}
	if token == "" || counter == "" {
		return ErrProofRequired
	}
	if len(token) > maxChallengeBytes || len(counter) > 20 {
		return ErrProofInvalid
	}
	if _, err := strconv.ParseUint(counter, 10, 64); err != nil {
		return ErrProofInvalid
	}
	sealed, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return ErrProofInvalid
	}
	raw, err := p.cipher.Decrypt(sealed, proofContext)
	if err != nil {
		return ErrProofInvalid
	}
	var c challenge
	if err := json.Unmarshal(raw, &c); err != nil || c.Nonce == "" {
		return ErrProofInvalid
	}
	now := p.now()
	// The difficulty it was issued at, or today's, whichever is harder: a
	// challenge from before the operator raised it does not get the old price.
	if c.Bits > need {
		need = c.Bits
	}
	if !now.Before(c.Expires) || !Solved(token, counter, need) {
		return ErrProofInvalid
	}
	// Spent by its nonce, not its spelling: one challenge, one signup.
	p.mu.Lock()
	defer p.mu.Unlock()
	for n, exp := range p.used {
		if !now.Before(exp) {
			delete(p.used, n)
		}
	}
	if _, seen := p.used[c.Nonce]; seen {
		return ErrProofInvalid
	}
	p.used[c.Nonce] = c.Expires
	return nil
}

// SetProofOfWork requires signups to carry a solved challenge. Nil, or zero
// bits, leaves it off.
func (svc *Service) SetProofOfWork(p *ProofOfWork) { svc.pow = p }

// SignupChallenge issues a challenge for the signup page, and how many bits it
// needs; an empty challenge means none is needed. A closed registration
// answers as signup does (ADR-0051, decision 2).
func (svc *Service) SignupChallenge() (string, int, error) {
	switch svc.policy.RegistrationMode {
	case "open", "invite":
	default:
		return "", 0, ErrRegistrationClosed
	}
	need := svc.pow.Bits()
	if need == 0 {
		return "", 0, nil
	}
	token, err := svc.pow.Challenge()
	return token, need, err
}

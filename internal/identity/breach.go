package identity

import (
	"bufio"
	"context"
	"crypto/sha1" // #nosec G505 -- the Pwned Passwords range protocol is defined over SHA-1; it is an index, not a protection
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// The breached-password check (ADR-0051, decision 1).

// ErrPasswordBreached means the password appears in a known breach corpus.
var ErrPasswordBreached = errors.New("identity: that password has appeared in a known data breach; choose another")

// BreachChecker reports whether a password is known to have been breached.
type BreachChecker interface {
	Breached(ctx context.Context, password string) (bool, error)
}

// DefaultPwnedPasswordsBase is Have I Been Pwned's Pwned Passwords service.
const DefaultPwnedPasswordsBase = "https://api.pwnedpasswords.com" // #nosec G101 -- a public service's address, not a credential

// BreachTimeout bounds one range request.
const BreachTimeout = 5 * time.Second

// PwnedPasswords asks the Pwned Passwords range API, k-anonymously: only the
// first five hex characters of the password's SHA-1 leave the host, the
// answer is padded so its size says nothing, and the match is made here.
type PwnedPasswords struct {
	client    *http.Client
	base      string
	userAgent string
}

// NewPwnedPasswords builds a client. version goes into the User-Agent, which
// the service asks for.
func NewPwnedPasswords(client *http.Client, base, version string) *PwnedPasswords {
	if client == nil {
		client = &http.Client{Timeout: BreachTimeout}
	}
	if base == "" {
		base = DefaultPwnedPasswordsBase
	}
	return &PwnedPasswords{client: client, base: strings.TrimRight(base, "/"),
		userAgent: "CMediaStack/" + version + " ( https://github.com/jakethecake75/cmediastack )"}
}

// Breached reports whether the password is in the corpus.
func (p *PwnedPasswords) Breached(ctx context.Context, password string) (bool, error) {
	sum := sha1.Sum([]byte(password)) // #nosec G401 -- the range protocol's index, not a password hash
	digest := strings.ToUpper(hex.EncodeToString(sum[:]))
	prefix, suffix := digest[:5], digest[5:]
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+"/range/"+prefix, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("User-Agent", p.userAgent)
	req.Header.Set("Add-Padding", "true")
	resp, err := p.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("identity: asking Pwned Passwords: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("identity: Pwned Passwords answered %d", resp.StatusCode)
	}
	lines := bufio.NewScanner(io.LimitReader(resp.Body, 4<<20))
	for lines.Scan() {
		hash, count, ok := strings.Cut(strings.TrimSpace(lines.Text()), ":")
		// A padding entry has a count of zero, and is nobody's password.
		if ok && strings.EqualFold(hash, suffix) && strings.TrimSpace(count) != "0" {
			return true, nil
		}
	}
	if err := lines.Err(); err != nil {
		return false, fmt.Errorf("identity: reading Pwned Passwords: %w", err)
	}
	return false, nil
}

// SetBreachChecker turns the check on (ADR-0051). Nil leaves it off.
func (svc *Service) SetBreachChecker(c BreachChecker, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	svc.breach, svc.log = c, log
}

// acceptablePassword applies the policy and, when it is on, the breach check.
// A check that cannot be made accepts the password and says so in the log: an
// outage at a third party must not stop people setting passwords, and the
// policy's own rules have already applied (ADR-0051, decision 1).
func (svc *Service) acceptablePassword(ctx context.Context, password string) error {
	if err := svc.policy.Password.Validate(password); err != nil {
		return err
	}
	if svc.breach == nil {
		return nil
	}
	breached, err := svc.breach.Breached(ctx, password)
	if err != nil {
		svc.log.Warn("a password could not be checked against known breaches, and was accepted",
			slog.String("error", err.Error()))
		return nil
	}
	if breached {
		return ErrPasswordBreached
	}
	return nil
}

package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- HMAC-SHA1 is RFC 6238's default and what authenticator apps implement; HMAC does not rest on SHA-1's collision resistance
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// TOTP is implemented here rather than pulled in as a dependency.
//
// This is a deliberate exception to "do not reinvent solved problems": RFC 4226
// truncation over an HMAC is roughly thirty lines, it sits on the most
// security-critical path in the application, and it is verifiable against the
// RFC's own published test vectors (see totp_test.go). Removing a third-party
// dependency from the authentication path is worth more here than the lines
// saved, given the compromised-dependency persona in the threat model.
//
// Compatible with Google Authenticator, Authy, Aegis, 1Password and any other
// RFC 6238 implementation: SHA-1, 6 digits, 30-second step.

const (
	totpDigits      = 6
	totpPeriod      = 30 * time.Second
	totpSecretBytes = 20 // 160 bits, per RFC 4226 §4 R6
)

// TOTPSkew is how many steps either side of the current one are accepted.
// One step covers clock drift and a code entered as it rolls over; more than
// that widens the window an intercepted code stays valid for.
const TOTPSkew = 1

var (
	ErrInvalidTOTP   = errors.New("identity: invalid authenticator code")
	ErrInvalidSecret = errors.New("identity: malformed authenticator secret")
)

// GenerateTOTPSecret returns a new base32 secret suitable for an authenticator
// app. Base32 without padding is what QR-code provisioning URIs expect.
func GenerateTOTPSecret() (string, error) {
	buf := make([]byte, totpSecretBytes)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return "", fmt.Errorf("identity: read totp secret: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

// hotp computes the RFC 4226 code for a counter.
func hotp(secret []byte, counter uint64, digits int) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)

	mac := hmac.New(sha1.New, secret)
	mac.Write(buf[:])
	sum := mac.Sum(nil)

	// Dynamic truncation, RFC 4226 §5.3.
	offset := sum[len(sum)-1] & 0x0f
	code := (uint32(sum[offset])&0x7f)<<24 |
		(uint32(sum[offset+1])&0xff)<<16 |
		(uint32(sum[offset+2])&0xff)<<8 |
		(uint32(sum[offset+3]) & 0xff)

	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, code%mod)
}

func decodeSecret(secret string) ([]byte, error) {
	s := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
	if err != nil {
		return nil, ErrInvalidSecret
	}
	if len(key) == 0 {
		return nil, ErrInvalidSecret
	}
	return key, nil
}

// TOTPCode returns the code for a base32 secret at a point in time. Exported
// for the enrollment flow, which shows the user a code to confirm their app is
// configured, and for tests.
func TOTPCode(secret string, at time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	counter, ok := timeStep(at)
	if !ok {
		return "", errNoTimeStep
	}
	return hotp(key, counter, totpDigits), nil
}

// errNoTimeStep refuses a time before 1970, which has no TOTP time step.
var errNoTimeStep = errors.New("identity: a time before 1970 has no TOTP time step")

// timeStep is the RFC 6238 time step t falls in. A time before 1970 has none —
// the clock is wrong — and converting its negative Unix time would wrap into a
// step in the far future rather than fail.
func timeStep(t time.Time) (uint64, bool) {
	s := t.UTC().Unix()
	if s < 0 {
		return 0, false
	}
	return uint64(s) / uint64(totpPeriod.Seconds()), true
}

// VerifyTOTP checks a submitted code against the secret, allowing TOTPSkew
// steps of drift. Comparison is constant-time.
//
// Replay protection is NOT this function's job: a code stays valid for its
// whole step, so the caller must record the consumed counter per user and
// refuse a repeat. See ConsumedCounter.
func VerifyTOTP(secret, code string, at time.Time) error {
	key, err := decodeSecret(secret)
	if err != nil {
		return err
	}
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return ErrInvalidTOTP
	}

	current, ok := timeStep(at)
	if !ok {
		return ErrInvalidTOTP
	}
	for delta := -TOTPSkew; delta <= TOTPSkew; delta++ {
		counter := current
		if delta < 0 {
			d := uint64(-delta)
			if d > counter {
				continue
			}
			counter -= d
		} else {
			counter += uint64(delta)
		}
		want := hotp(key, counter, totpDigits)
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return nil
		}
	}
	return ErrInvalidTOTP
}

// ConsumedCounter returns the time step a code belongs to, so the caller can
// persist it and reject a replay of the same code within its validity window.
//
// A time before 1970 has no step and reads as 0; VerifyTOTP has already refused
// every code at such a time, so nothing is consumed at it.
func ConsumedCounter(at time.Time) uint64 {
	step, _ := timeStep(at)
	return step
}

// ProvisioningURI builds the otpauth:// URI an authenticator app scans.
//
// The account label is the username, never the email: the QR code and its URI
// are frequently screenshotted, pasted into support threads and stored in
// password managers, and there is no reason to put an email address in one.
func ProvisioningURI(issuer, username, secret string) string {
	label := url.PathEscape(issuer + ":" + username)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", totpDigits))
	q.Set("period", fmt.Sprintf("%d", int(totpPeriod.Seconds())))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// ---------------------------------------------------------------------------
// Recovery codes
// ---------------------------------------------------------------------------

// recoveryAlphabet excludes characters that are ambiguous when transcribed
// from a screen or a printout: 0/O, 1/I/l, and vowels that can form words.
const recoveryAlphabet = "23456789BCDFGHJKMNPQRTVWXY"

// GenerateRecoveryCode returns a single-use code in the form XXXXX-XXXXX.
//
// With mandatory MFA these are the only route back into a locked-out account,
// so they are generated with crypto/rand and stored Argon2id-hashed exactly
// like passwords.
func GenerateRecoveryCode() (string, error) {
	const groupLen = 5
	out := make([]byte, 0, groupLen*2+1)
	for g := 0; g < 2; g++ {
		if g > 0 {
			out = append(out, '-')
		}
		for i := 0; i < groupLen; i++ {
			c, err := randomIndex(len(recoveryAlphabet))
			if err != nil {
				return "", err
			}
			out = append(out, recoveryAlphabet[c])
		}
	}
	return string(out), nil
}

// GenerateRecoveryCodes returns n distinct codes.
func GenerateRecoveryCodes(n int) ([]string, error) {
	seen := make(map[string]struct{}, n)
	out := make([]string, 0, n)
	for len(out) < n {
		c, err := GenerateRecoveryCode()
		if err != nil {
			return nil, err
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out, nil
}

// randomIndex returns a uniform value in [0, n) without modulo bias.
func randomIndex(n int) (int, error) {
	if n <= 0 || n > 256 {
		return 0, errors.New("identity: bad alphabet size")
	}
	max := 256 - (256 % n)
	var b [1]byte
	for {
		if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
			return 0, err
		}
		if int(b[0]) < max {
			return int(b[0]) % n, nil
		}
	}
}

// NormalizeRecoveryCode makes user input comparable: uppercase, no spaces, and
// a dash in the canonical position.
func NormalizeRecoveryCode(in string) string {
	s := strings.ToUpper(in)
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(recoveryAlphabet, r) {
			return r
		}
		return -1
	}, s)
	if len(s) == 10 {
		return s[:5] + "-" + s[5:]
	}
	return s
}

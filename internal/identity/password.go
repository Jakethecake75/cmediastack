// Package identity owns accounts: their credentials, their second factor, and
// the lifecycle from account request through approval to an active user.
package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
)

// Password errors. VerifyPassword deliberately returns the same error for a
// bad password and a malformed hash: the caller must not be able to tell them
// apart, and neither must a timing observer.
var (
	ErrPasswordMismatch  = errors.New("identity: credential mismatch")
	ErrPasswordTooShort  = errors.New("identity: password is too short")
	ErrPasswordTooLong   = errors.New("identity: password exceeds the maximum length")
	ErrPasswordTooSimple = errors.New("identity: password does not contain enough variety")
)

// MaxPasswordLength bounds Argon2id input. Without a cap, a multi-megabyte
// password is a free CPU-exhaustion primitive on an anonymous endpoint.
const MaxPasswordLength = 1024

// Argon2Params are the tuned Argon2id parameters, stored alongside each hash
// so that raising them later does not invalidate existing credentials.
type Argon2Params struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultArgon2Params follows OWASP's second recommended argon2id
// configuration: 19 MiB, t=2, p=1. Documented in SECURITY.md.
func DefaultArgon2Params() Argon2Params {
	return Argon2Params{
		Memory:      19456,
		Iterations:  2,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// HashPassword returns a PHC-format argon2id string:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
//
// Encoding the parameters into the hash is what makes them upgradable: a
// verify against an old hash uses the old parameters, and NeedsRehash reports
// when a credential should be re-derived on next successful login.
func HashPassword(password string, p Argon2Params) (string, error) {
	if len(password) > MaxPasswordLength {
		return "", ErrPasswordTooLong
	}
	salt := make([]byte, p.SaltLength)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("identity: read salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword checks a password against a PHC-format hash in constant time.
func VerifyPassword(password, encoded string) error {
	if len(password) > MaxPasswordLength {
		return ErrPasswordMismatch
	}
	p, salt, want, err := decodeHash(encoded)
	if err != nil {
		// A malformed stored hash must be indistinguishable from a wrong
		// password, or the error becomes an account-state oracle.
		return ErrPasswordMismatch
	}

	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrPasswordMismatch
	}
	return nil
}

// NeedsRehash reports whether a stored hash was derived with weaker parameters
// than the current policy. Callers rehash on the next successful login.
func NeedsRehash(encoded string, current Argon2Params) bool {
	p, _, _, err := decodeHash(encoded)
	if err != nil {
		return true
	}
	return p.Memory < current.Memory ||
		p.Iterations < current.Iterations ||
		p.KeyLength < current.KeyLength
}

func decodeHash(encoded string) (p Argon2Params, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=...,t=...,p=...", salt, key]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, nil, nil, errors.New("identity: not an argon2id hash")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return p, nil, nil, errors.New("identity: malformed version")
	}
	if version != argon2.Version {
		return p, nil, nil, errors.New("identity: unsupported argon2 version")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return p, nil, nil, errors.New("identity: malformed parameters")
	}

	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return p, nil, nil, errors.New("identity: malformed salt")
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return p, nil, nil, errors.New("identity: malformed key")
	}

	saltLen, keyLen := len(salt), len(key)
	if saltLen > maxHashPart || keyLen > maxHashPart {
		return p, nil, nil, errors.New("identity: a salt or key longer than any hash this software writes")
	}
	p.SaltLength = uint32(saltLen)
	p.KeyLength = uint32(keyLen)
	return p, salt, key, nil
}

// maxHashPart bounds a stored hash's salt and key. The ones this software
// writes are 16 and 32 bytes; a longer one is not a hash it wrote, and the
// key's length is how much the next derivation would be asked to produce.
const maxHashPart = 1024

// DummyHash is a valid argon2id hash of a random value, used to spend the same
// CPU time when an account does not exist.
//
// Without it, "unknown username" returns in microseconds while "wrong
// password" takes ~50ms, and the login endpoint becomes a user-enumeration
// oracle regardless of how carefully the error message is worded.
var DummyHash = func() string {
	buf := make([]byte, 32)
	_, _ = io.ReadFull(rand.Reader, buf)
	h, err := HashPassword(base64.RawStdEncoding.EncodeToString(buf), DefaultArgon2Params())
	if err != nil {
		panic("identity: cannot build dummy hash: " + err.Error())
	}
	return h
}()

// SpendVerificationTime performs a throwaway verification so that a login
// attempt against a nonexistent account costs the same as a real one.
func SpendVerificationTime(password string) {
	_ = VerifyPassword(password, DummyHash)
	runtime.KeepAlive(password)
}

// PasswordPolicy is the minimum acceptable credential.
//
// Length is the requirement that matters; composition rules mostly push people
// toward predictable substitutions. The variety check here is a low floor
// intended to reject "aaaaaaaaaaaa", not to enforce a character-class matrix.
type PasswordPolicy struct {
	MinLength int
}

// Validate applies the policy.
func (pp PasswordPolicy) Validate(password string) error {
	if len([]rune(password)) < pp.MinLength {
		return fmt.Errorf("%w: minimum is %d characters", ErrPasswordTooShort, pp.MinLength)
	}
	if len(password) > MaxPasswordLength {
		return ErrPasswordTooLong
	}
	distinct := map[rune]struct{}{}
	var classes int
	var hasLower, hasUpper, hasDigit, hasOther bool
	for _, r := range password {
		distinct[r] = struct{}{}
		switch {
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsDigit(r):
			hasDigit = true
		default:
			hasOther = true
		}
	}
	for _, b := range []bool{hasLower, hasUpper, hasDigit, hasOther} {
		if b {
			classes++
		}
	}
	if len(distinct) < 5 && classes < 2 {
		return ErrPasswordTooSimple
	}
	return nil
}

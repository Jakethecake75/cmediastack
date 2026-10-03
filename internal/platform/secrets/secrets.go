// Package secrets provides authenticated encryption for credentials at rest.
//
// Requirements §8: "Secrets (indexer API keys, proxy credentials, provider
// passwords, OIDC client secrets) encrypted at rest with AES-256-GCM using a
// key from an env-supplied master key or an external KMS/Vault; never written
// to logs, never returned by the API."
//
// The master key never touches the database or the config file. It arrives in
// an environment variable, is validated at boot by the config lint, and lives
// only in this package's memory.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

// BackupInfo is the HKDF info string the backup passphrase is derived with
// (ADR-0029). It is part of every backup ever written: change it and no
// existing backup decrypts. TestTheBackupPassphraseNeverChanges pins the
// derivation to a known answer so that cannot happen by accident.
const BackupInfo = "cmediastack backup v1"

// Errors returned by this package. They are deliberately uninformative about
// why decryption failed: distinguishing "wrong key" from "corrupt ciphertext"
// from "tampered tag" is an oracle.
var (
	ErrInvalidKey  = errors.New("secrets: master key must be exactly 32 bytes")
	ErrDecryptFail = errors.New("secrets: decryption failed")
	ErrEmptyCipher = errors.New("secrets: empty ciphertext")
)

// Cipher encrypts and decrypts stored credentials.
//
// It does not keep the master key. What it needs of it — the AES key schedule,
// and the backup passphrase — is derived when it is built, and the key itself
// is left with the caller.
type Cipher struct {
	aead cipher.AEAD
	// backupPassphrase is what backups are encrypted to. See BackupPassphrase.
	backupPassphrase string
}

// NewCipher builds a Cipher from a 32-byte key.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	pass, err := backupPassphrase(key)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead, backupPassphrase: pass}, nil
}

// backupPassphrase derives the backup passphrase from the master key:
// unpadded base64url of HKDF-SHA256(key, no salt, BackupInfo), 32 bytes.
//
// A separate key rather than the master key itself: a backup's passphrase can
// then be handed to the standard age tool without handing over the key that
// seals every stored credential. And derived rather than stored, so there is
// still one secret for the operator to keep, not two.
func backupPassphrase(key []byte) (string, error) {
	derived, err := hkdf.Key(sha256.New, key, nil, BackupInfo, 32)
	if err != nil {
		return "", fmt.Errorf("secrets: deriving the backup passphrase: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(derived), nil
}

// BackupPassphrase is the passphrase backups are encrypted to (ADR-0029).
//
// Anyone holding it can read every backup this instance has taken, and forge
// one that restores cleanly — which is why it is derived from the master key
// rather than written anywhere, and why nothing prints it.
func (c *Cipher) BackupPassphrase() string {
	return c.backupPassphrase
}

// NewCipherFromBase64 builds a Cipher from a base64-encoded 32-byte key, which
// is how the master key arrives from the environment.
func NewCipherFromBase64(encoded string) (*Cipher, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("secrets: master key is not valid base64: %w", err)
	}
	return NewCipher(key)
}

// GenerateKey returns a new random 32-byte key, base64 encoded. Used by the
// first-run wizard's setup instructions and by tests.
func GenerateKey() (string, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", fmt.Errorf("secrets: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// Encrypt seals plaintext, binding it to context via GCM's additional
// authenticated data.
//
// The context should identify what the secret is for — "indexer:7:api_key",
// "egress:download:password". Binding it means a ciphertext lifted from one
// row cannot be replayed into another: an attacker with write access to the
// database cannot move the proxy password into the OIDC client-secret field
// and read it back through a different code path.
//
// Output layout: nonce || ciphertext || tag.
func (c *Cipher) Encrypt(plaintext []byte, context string) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, plaintext, []byte(context))
	return sealed, nil
}

// EncryptString is Encrypt for string plaintext.
func (c *Cipher) EncryptString(plaintext, context string) ([]byte, error) {
	return c.Encrypt([]byte(plaintext), context)
}

// Decrypt opens a ciphertext produced by Encrypt with the same context.
func (c *Cipher) Decrypt(sealed []byte, context string) ([]byte, error) {
	if len(sealed) == 0 {
		return nil, ErrEmptyCipher
	}
	ns := c.aead.NonceSize()
	if len(sealed) < ns+c.aead.Overhead() {
		return nil, ErrDecryptFail
	}
	nonce, body := sealed[:ns], sealed[ns:]
	out, err := c.aead.Open(nil, nonce, body, []byte(context))
	if err != nil {
		return nil, ErrDecryptFail
	}
	return out, nil
}

// DecryptString is Decrypt returning a string.
func (c *Cipher) DecryptString(sealed []byte, context string) (string, error) {
	out, err := c.Decrypt(sealed, context)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Mask renders a secret for display. It never reveals more than the last four
// characters, and reveals nothing at all for short values.
//
// This is what the API returns in place of a stored credential: write-only
// fields, masked on read (requirements §8).
func Mask(secret string) string {
	const dots = "••••••••"
	if len(secret) <= 8 {
		return dots
	}
	return dots + secret[len(secret)-4:]
}

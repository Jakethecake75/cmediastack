package secrets

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func testCipher(t *testing.T) *Cipher {
	t.Helper()
	enc, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewCipherFromBase64(enc)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRoundTrip(t *testing.T) {
	c := testCipher(t)
	const secret = "nord-socks5-password-9f8e7d"

	sealed, err := c.EncryptString(secret, "egress:download:password")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(secret)) {
		t.Fatal("plaintext is present in the ciphertext")
	}

	got, err := c.DecryptString(sealed, "egress:download:password")
	if err != nil {
		t.Fatal(err)
	}
	if got != secret {
		t.Fatalf("round trip = %q, want %q", got, secret)
	}
}

func TestNonceIsUniquePerEncryption(t *testing.T) {
	c := testCipher(t)
	seen := map[string]bool{}
	for i := 0; i < 256; i++ {
		sealed, err := c.EncryptString("same-plaintext-every-time", "ctx")
		if err != nil {
			t.Fatal(err)
		}
		k := string(sealed)
		if seen[k] {
			t.Fatal("identical ciphertext produced twice: nonce reuse")
		}
		seen[k] = true
	}
}

// Negative test: a ciphertext lifted from one field must not decrypt in
// another. This is what stops an attacker with database write access from
// moving the proxy password into a field whose value is echoed back.
func TestContextBindingPreventsCiphertextRelocation(t *testing.T) {
	c := testCipher(t)

	sealed, err := c.EncryptString("proxy-password", "egress:download:password")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.DecryptString(sealed, "indexer:7:api_key"); err == nil {
		t.Fatal("ciphertext decrypted under a different context (THE ATTACK SUCCEEDED)")
	}
	if _, err := c.DecryptString(sealed, ""); err == nil {
		t.Fatal("ciphertext decrypted under an empty context")
	}
}

// Negative test: tampering with any byte must fail the GCM tag.
func TestTamperingIsDetected(t *testing.T) {
	c := testCipher(t)
	sealed, err := c.EncryptString("indexer-api-key-value", "indexer:1:api_key")
	if err != nil {
		t.Fatal(err)
	}

	for i := range sealed {
		bad := bytes.Clone(sealed)
		bad[i] ^= 0x01
		if _, err := c.DecryptString(bad, "indexer:1:api_key"); err == nil {
			t.Fatalf("tampered byte %d decrypted successfully", i)
		}
	}
}

// Negative test: a different key must not open the ciphertext.
func TestWrongKeyFails(t *testing.T) {
	a := testCipher(t)
	b := testCipher(t)

	sealed, err := a.EncryptString("secret", "ctx")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.DecryptString(sealed, "ctx"); err == nil {
		t.Fatal("a foreign key opened the ciphertext")
	}
}

func TestRejectsWrongKeyLength(t *testing.T) {
	for _, n := range []int{0, 1, 16, 24, 31, 33, 64} {
		if _, err := NewCipher(make([]byte, n)); err == nil {
			t.Errorf("accepted a %d-byte key", n)
		}
	}
	if _, err := NewCipher(make([]byte, 32)); err != nil {
		t.Errorf("rejected a valid 32-byte key: %v", err)
	}
}

func TestRejectsMalformedBase64Key(t *testing.T) {
	if _, err := NewCipherFromBase64("not!valid!base64!"); err == nil {
		t.Fatal("accepted malformed base64")
	}
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if _, err := NewCipherFromBase64(short); err == nil {
		t.Fatal("accepted a 16-byte key")
	}
}

func TestDecryptRejectsTruncatedInput(t *testing.T) {
	c := testCipher(t)
	if _, err := c.Decrypt(nil, "ctx"); err == nil {
		t.Error("accepted nil ciphertext")
	}
	if _, err := c.Decrypt([]byte{1, 2, 3}, "ctx"); err == nil {
		t.Error("accepted a truncated ciphertext")
	}
}

// Errors must not distinguish failure modes: doing so is a decryption oracle.
func TestDecryptErrorsAreIndistinguishable(t *testing.T) {
	a := testCipher(t)
	b := testCipher(t)
	sealed, _ := a.EncryptString("secret", "ctx")

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 0xFF

	_, wrongKey := b.DecryptString(sealed, "ctx")
	_, wrongCtx := a.DecryptString(sealed, "other")
	_, tamperErr := a.DecryptString(tampered, "ctx")

	if wrongKey.Error() != wrongCtx.Error() || wrongCtx.Error() != tamperErr.Error() {
		t.Errorf("error messages differ by failure mode: %q / %q / %q",
			wrongKey, wrongCtx, tamperErr)
	}
}

func TestMaskNeverRevealsMoreThanLastFour(t *testing.T) {
	cases := map[string]string{
		"":                     "••••••••",
		"short":                "••••••••",
		"exactly8":             "••••••••",
		"abcdefghi":            "••••••••fghi",
		"nord-api-key-1234567": "••••••••4567",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}

	// The masked form must never contain the leading part of the secret.
	secret := "SUPERSECRETVALUE9999"
	masked := Mask(secret)
	if strings.Contains(masked, "SUPERSECRET") {
		t.Errorf("mask leaked the secret prefix: %q", masked)
	}
}

// The backup passphrase is part of every backup ever written: a change to the
// derivation — the info string, the hash, the encoding, the length — would
// leave every existing backup undecryptable, and nothing else would notice
// until a restore was needed. So it is pinned to an answer computed outside Go:
// the same derivation run through Python's hmac module and through
// `openssl kdf ... HKDF`, which agree with each other. The runbook's recovery
// recipe is the openssl one.
func TestTheBackupPassphraseNeverChanges(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	c, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	const want = "_VBom-CwBWIfwjyWCqtmQNbzTNZRGe_dXI1W9OBD3Ws"
	if got := c.BackupPassphrase(); got != want {
		t.Fatalf("backup passphrase = %q, want %q: the derivation changed, and every "+
			"existing backup would stop decrypting", got, want)
	}
	if BackupInfo != "cmediastack backup v1" {
		t.Fatalf("BackupInfo = %q", BackupInfo)
	}
}

// Derived from the key, so two instances never share one, and it is not the key
// itself in another encoding.
func TestTheBackupPassphraseIsTheKeysAndNotTheKey(t *testing.T) {
	a, b := testCipher(t), testCipher(t)
	if a.BackupPassphrase() == b.BackupPassphrase() {
		t.Fatal("two keys gave the same backup passphrase")
	}
	key := make([]byte, 32)
	c, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	p := c.BackupPassphrase()
	for _, enc := range []string{
		base64.StdEncoding.EncodeToString(key),
		base64.RawURLEncoding.EncodeToString(key),
	} {
		if p == enc {
			t.Fatal("the backup passphrase is the master key")
		}
	}
	if len(p) != 43 || strings.ContainsAny(p, "+/=") {
		t.Fatalf("passphrase %q is not 32 bytes of unpadded base64url", p)
	}
}

package identity

import (
	"encoding/base32"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Argon2id
// ---------------------------------------------------------------------------

// testParams keep the test suite fast. Production parameters are asserted
// separately in TestDefaultParamsMeetPolicy.
func testParams() Argon2Params {
	return Argon2Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
}

func TestPasswordRoundTrip(t *testing.T) {
	const pw = "correct-horse-battery-staple"
	h, err := HashPassword(pw, testParams())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h, pw) {
		t.Fatal("the hash contains the plaintext")
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("unexpected hash format: %s", h)
	}
	if err := VerifyPassword(pw, h); err != nil {
		t.Fatalf("correct password rejected: %v", err)
	}
}

func TestWrongPasswordIsRejected(t *testing.T) {
	h, err := HashPassword("the-real-password", testParams())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"", "the-real-passwor", "the-real-password ", "The-Real-Password", "wrong",
	} {
		if err := VerifyPassword(bad, h); err == nil {
			t.Errorf("password %q was accepted (THE ATTACK SUCCEEDED)", bad)
		}
	}
}

func TestSaltIsUniquePerHash(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		h, err := HashPassword("same-password", testParams())
		if err != nil {
			t.Fatal(err)
		}
		if seen[h] {
			t.Fatal("identical hash produced twice: the salt is not random")
		}
		seen[h] = true
	}
}

// A malformed stored hash must be indistinguishable from a wrong password, or
// the error becomes an oracle for account state.
func TestMalformedHashIsIndistinguishableFromWrongPassword(t *testing.T) {
	good, _ := HashPassword("pw", testParams())
	wrong := VerifyPassword("nope", good)

	for _, bad := range []string{
		"", "not-a-hash", "$argon2id$", "$argon2i$v=19$m=1024,t=1,p=1$AAAA$BBBB",
		"$argon2id$v=99$m=1024,t=1,p=1$AAAA$BBBB",
		"$argon2id$v=19$m=bad,t=1,p=1$AAAA$BBBB",
		"$argon2id$v=19$m=1024,t=1,p=1$!!!$BBBB",
	} {
		err := VerifyPassword("anything", bad)
		if err == nil {
			t.Errorf("malformed hash %q verified successfully", bad)
			continue
		}
		if err.Error() != wrong.Error() {
			t.Errorf("malformed hash %q gave a distinguishable error: %v vs %v", bad, err, wrong)
		}
	}
}

func TestOversizedPasswordIsRejected(t *testing.T) {
	huge := strings.Repeat("a", MaxPasswordLength+1)
	if _, err := HashPassword(huge, testParams()); !errors.Is(err, ErrPasswordTooLong) {
		t.Errorf("HashPassword accepted an oversized password: %v", err)
	}
	h, _ := HashPassword("normal", testParams())
	if err := VerifyPassword(huge, h); !errors.Is(err, ErrPasswordMismatch) {
		t.Errorf("VerifyPassword should reject oversized input cheaply, got %v", err)
	}
}

// A stored hash whose salt or key is longer than any this software writes is
// not one of its hashes: refused as malformed — so it is rehashed — rather than
// read as parameters the next derivation would be asked to meet.
func TestAHashWithAnOverlongSaltOrKeyIsMalformed(t *testing.T) {
	b64 := func(n int) string { return base64.RawStdEncoding.EncodeToString(make([]byte, n)) }
	for name, h := range map[string]string{
		"key":  "$argon2id$v=19$m=1024,t=1,p=1$" + b64(16) + "$" + b64(maxHashPart+1),
		"salt": "$argon2id$v=19$m=1024,t=1,p=1$" + b64(maxHashPart+1) + "$" + b64(32),
	} {
		if _, _, _, err := decodeHash(h); err == nil {
			t.Errorf("an overlong %s decoded", name)
		}
		if !NeedsRehash(h, testParams()) {
			t.Errorf("an overlong %s is not rehashed", name)
		}
	}
	ok := "$argon2id$v=19$m=1024,t=1,p=1$" + b64(maxHashPart) + "$" + b64(maxHashPart)
	if _, _, _, err := decodeHash(ok); err != nil {
		t.Errorf("a salt and key at the bound were refused: %v", err)
	}
}

func TestNeedsRehashDetectsWeakerParameters(t *testing.T) {
	weak := Argon2Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	h, err := HashPassword("pw", weak)
	if err != nil {
		t.Fatal(err)
	}
	if !NeedsRehash(h, DefaultArgon2Params()) {
		t.Error("a hash weaker than current policy should need rehashing")
	}
	if NeedsRehash(h, weak) {
		t.Error("a hash at current policy should not need rehashing")
	}
	if !NeedsRehash("garbage", DefaultArgon2Params()) {
		t.Error("an unparseable hash should need rehashing")
	}
}

func TestDefaultParamsMeetPolicy(t *testing.T) {
	p := DefaultArgon2Params()
	if p.Memory < 19456 {
		t.Errorf("default memory %d KiB is below the documented 19456", p.Memory)
	}
	if p.Iterations < 2 {
		t.Errorf("default iterations %d is below 2", p.Iterations)
	}
	if p.SaltLength < 16 || p.KeyLength < 32 {
		t.Errorf("default salt/key lengths are too short: %d/%d", p.SaltLength, p.KeyLength)
	}
}

func TestDummyHashIsUsableAndDistinct(t *testing.T) {
	if !strings.HasPrefix(DummyHash, "$argon2id$") {
		t.Fatalf("DummyHash is not an argon2id hash: %q", DummyHash)
	}
	// It must never verify against a guessable password.
	for _, guess := range []string{"", "password", "admin", "dummy"} {
		if err := VerifyPassword(guess, DummyHash); err == nil {
			t.Errorf("DummyHash verified against %q", guess)
		}
	}
	SpendVerificationTime("anything") // must not panic
}

func TestPasswordPolicy(t *testing.T) {
	pp := PasswordPolicy{MinLength: 12}

	if err := pp.Validate("short"); err == nil {
		t.Error("a short password was accepted")
	}
	if err := pp.Validate(strings.Repeat("a", 12)); err == nil {
		t.Error("a single-character-repeat password was accepted")
	}
	if err := pp.Validate(strings.Repeat("a", MaxPasswordLength+1)); err == nil {
		t.Error("an oversized password was accepted")
	}
	for _, ok := range []string{
		"correct-horse-battery-staple",
		"Tr0ub4dor&3xxxxx",
		"a long passphrase with spaces",
	} {
		if err := pp.Validate(ok); err != nil {
			t.Errorf("valid password %q rejected: %v", ok, err)
		}
	}
}

// ---------------------------------------------------------------------------
// TOTP — verified against RFC 6238's published test vectors
// ---------------------------------------------------------------------------

// RFC 6238 Appendix B uses the ASCII seed "12345678901234567890" for SHA-1.
func rfc6238Secret() string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).
		EncodeToString([]byte("12345678901234567890"))
}

// The RFC publishes 8-digit values; CMediaStack uses 6 digits, which is the
// same code truncated to its last six. Both are asserted here so a change to
// the truncation logic cannot pass silently.
func TestTOTPMatchesRFC6238Vectors(t *testing.T) {
	secret := rfc6238Secret()
	vectors := []struct {
		unix    int64
		eight   string
		wantSix string
	}{
		{59, "94287082", "287082"},
		{1111111109, "07081804", "081804"},
		{1111111111, "14050471", "050471"},
		{1234567890, "89005924", "005924"},
		{2000000000, "69279037", "279037"},
		{20000000000, "65353130", "353130"},
	}

	for _, v := range vectors {
		got, err := TOTPCode(secret, time.Unix(v.unix, 0).UTC())
		if err != nil {
			t.Fatalf("TOTPCode at %d: %v", v.unix, err)
		}
		if got != v.wantSix {
			t.Errorf("T=%d: code = %s, want %s (RFC 8-digit value %s)", v.unix, got, v.wantSix, v.eight)
		}
	}
}

func TestVerifyTOTPAcceptsCurrentCode(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code, err := TOTPCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyTOTP(secret, code, now); err != nil {
		t.Fatalf("current code rejected: %v", err)
	}
	// Whitespace from a copy-paste must not break it.
	if err := VerifyTOTP(secret, "  "+code+" ", now); err != nil {
		t.Errorf("padded code rejected: %v", err)
	}
}

func TestVerifyTOTPHonoursSkewWindowAndNoMore(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	base := time.Unix(1_700_000_000, 0).UTC()

	// One step either side is accepted.
	for _, delta := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		code, _ := TOTPCode(secret, base.Add(delta))
		if err := VerifyTOTP(secret, code, base); err != nil {
			t.Errorf("code from %v drift was rejected", delta)
		}
	}

	// Two steps away must not be.
	for _, delta := range []time.Duration{-90 * time.Second, 90 * time.Second, 10 * time.Minute} {
		code, _ := TOTPCode(secret, base.Add(delta))
		if err := VerifyTOTP(secret, code, base); err == nil {
			t.Errorf("code from %v drift was accepted (window is too wide)", delta)
		}
	}
}

func TestVerifyTOTPRejectsMalformedInput(t *testing.T) {
	secret, _ := GenerateTOTPSecret()
	now := time.Now()

	for _, bad := range []string{"", "12345", "1234567", "abcdef", "00000a"} {
		if err := VerifyTOTP(secret, bad, now); err == nil {
			t.Errorf("malformed code %q was accepted", bad)
		}
	}
	if err := VerifyTOTP("not-base32!", "123456", now); err == nil {
		t.Error("a malformed secret was accepted")
	}
	if err := VerifyTOTP("", "123456", now); err == nil {
		t.Error("an empty secret was accepted")
	}
}

func TestForeignSecretIsRejected(t *testing.T) {
	a, _ := GenerateTOTPSecret()
	b, _ := GenerateTOTPSecret()
	now := time.Now()

	code, _ := TOTPCode(a, now)
	if err := VerifyTOTP(b, code, now); err == nil {
		t.Fatal("a code from a different secret was accepted (THE ATTACK SUCCEEDED)")
	}
}

func TestGeneratedSecretsAreDistinctAndDecodable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		s, err := GenerateTOTPSecret()
		if err != nil {
			t.Fatal(err)
		}
		if seen[s] {
			t.Fatal("duplicate TOTP secret generated")
		}
		seen[s] = true

		raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
		if err != nil {
			t.Fatalf("secret is not valid base32: %v", err)
		}
		if len(raw) != totpSecretBytes {
			t.Fatalf("secret is %d bytes, want %d", len(raw), totpSecretBytes)
		}
	}
}

// A clock before 1970 has no time step. Converting its negative Unix time
// would wrap into a step in the far future; instead nothing verifies, no code
// is produced, and nothing is consumed.
func TestATimeBefore1970HasNoTOTPStep(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	before := time.Unix(-1, 0)
	if code, err := TOTPCode(secret, before); err == nil {
		t.Errorf("a code for a time before 1970: %q", code)
	}
	wrapped := hotp(mustDecode(t, secret), uint64(1<<64-1)/30, totpDigits)
	if err := VerifyTOTP(secret, wrapped, before); !errors.Is(err, ErrInvalidTOTP) {
		t.Errorf("the code for the wrapped step verified before 1970: %v", err)
	}
	if got := ConsumedCounter(before); got != 0 {
		t.Errorf("ConsumedCounter before 1970 = %d, want 0", got)
	}
	if code, err := TOTPCode(secret, time.Unix(0, 0)); err != nil || len(code) != totpDigits {
		t.Errorf("1970 itself has a step: %q, %v", code, err)
	}
}

func mustDecode(t *testing.T, secret string) []byte {
	t.Helper()
	key, err := decodeSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestConsumedCounterDistinguishesSteps(t *testing.T) {
	// Align to a step boundary so "within one step" means what it says:
	// 1_700_000_000 is 20s into its own step, which would make a +10s offset
	// cross into the next one.
	base := time.Unix(1_700_000_010, 0).UTC()
	if base.Unix()%int64(totpPeriod.Seconds()) != 0 {
		t.Fatalf("test base %d is not on a step boundary", base.Unix())
	}

	if ConsumedCounter(base) != ConsumedCounter(base.Add(29*time.Second)) {
		t.Error("times within one step should share a counter")
	}
	if ConsumedCounter(base) == ConsumedCounter(base.Add(30*time.Second)) {
		t.Error("times a step apart should have different counters")
	}
	// Codes are stable across a step and change at its edge.
	secret, _ := GenerateTOTPSecret()
	a, _ := TOTPCode(secret, base)
	b, _ := TOTPCode(secret, base.Add(29*time.Second))
	c, _ := TOTPCode(secret, base.Add(30*time.Second))
	if a != b {
		t.Error("code changed within a step")
	}
	if a == c {
		t.Error("code did not change at the step boundary")
	}
}

func TestProvisioningURIShape(t *testing.T) {
	uri := ProvisioningURI("CMediaStack", "jacob", "JBSWY3DPEHPK3PXP")

	for _, want := range []string{
		"otpauth://totp/", "secret=JBSWY3DPEHPK3PXP",
		"issuer=CMediaStack", "algorithm=SHA1", "digits=6", "period=30",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("provisioning URI missing %q: %s", want, uri)
		}
	}
	// The label must not carry an email address.
	if strings.Contains(uri, "@") {
		t.Errorf("provisioning URI contains an @, suggesting an email leaked into the label: %s", uri)
	}
}

// ---------------------------------------------------------------------------
// Recovery codes
// ---------------------------------------------------------------------------

func TestRecoveryCodesAreDistinctAndUnambiguous(t *testing.T) {
	codes, err := GenerateRecoveryCodes(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 10 {
		t.Fatalf("got %d codes, want 10", len(codes))
	}

	seen := map[string]bool{}
	for _, c := range codes {
		if seen[c] {
			t.Fatalf("duplicate recovery code %q", c)
		}
		seen[c] = true

		if len(c) != 11 || c[5] != '-' {
			t.Errorf("code %q is not in XXXXX-XXXXX form", c)
		}
		for _, r := range strings.ReplaceAll(c, "-", "") {
			if !strings.ContainsRune(recoveryAlphabet, r) {
				t.Errorf("code %q contains ambiguous character %q", c, r)
			}
		}
	}
}

func TestNormalizeRecoveryCode(t *testing.T) {
	want := "23456-789BC"
	for _, in := range []string{
		"23456-789BC", "23456789BC", "23456 789 bc", "  23456-789bc  ", "2-3-4-5-6-7-8-9-B-C",
	} {
		if got := NormalizeRecoveryCode(in); got != want {
			t.Errorf("NormalizeRecoveryCode(%q) = %q, want %q", in, got, want)
		}
	}
}

// Recovery codes are stored hashed, exactly like passwords.
func TestRecoveryCodeHashesVerify(t *testing.T) {
	code, err := GenerateRecoveryCode()
	if err != nil {
		t.Fatal(err)
	}
	h, err := HashPassword(code, testParams())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h, code) {
		t.Fatal("the recovery code appears in its own hash")
	}
	if err := VerifyPassword(code, h); err != nil {
		t.Fatalf("recovery code did not verify: %v", err)
	}
	if err := VerifyPassword("23456-789BD", h); err == nil {
		t.Fatal("a wrong recovery code verified")
	}
}

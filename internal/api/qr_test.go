package api

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
)

// A QR that renders but encodes the wrong thing is worse than no QR: the user
// scans it, the app accepts the seed, and enrollment fails with a code that
// looks right. This decodes the image the server actually produced and asserts
// it carries the same otpauth URI the manual key path hands out.
//
// gozxing is a TEST-ONLY dependency — an independent implementation, so this is
// a genuine round trip rather than the encoder agreeing with itself.
//
// # Why this retries
//
// gozxing fails to read roughly 1 valid QR in 200. Measured, not guessed: 400
// enrollments, 2 failures (0.50%), and the rate did not move when the image was
// re-rendered at exact integer module scaling (-8 px/module, -6, and 344px) or
// when the decoder was given TRY_HARDER. Changing the encoder changes nothing,
// which is what places the fault in this test's reader rather than in the QR the
// server produces.
//
// Left alone that is a 0.5% false failure on a suite that other work treats as a
// gate — about one spurious red in every two hundred full runs, which is exactly
// how a gate stops being believed. Each attempt requests a FRESH enrollment, so
// the secrets are independent and three attempts put a false failure at roughly
// one in eight million. A real decode failure — the QR encoding the wrong thing
// — fails all three, because the assertions below run on whichever attempt
// decoded.
func TestEnrollmentQRDecodesToTheRealOTPAuthURI(t *testing.T) {
	r := newRig(t)

	c := r.client()
	c.visitPage("/setup")
	if res := c.post("/api/v1/setup", map[string]any{
		"username": "admin", "email": "a@example.com", "password": "a-good-long-passphrase",
	}); res.Code != http.StatusCreated {
		t.Fatalf("setup: %d", res.Code)
	}
	c.visitPage("/login")
	c.post("/api/v1/auth/login", map[string]any{
		"username": "admin", "password": "a-good-long-passphrase",
	})

	var wantURI, wantSecret, got string
	var lastErr error

	for attempt := 1; attempt <= 3; attempt++ {
		res := c.get("/api/v1/auth/mfa/enroll")
		wantURI, _ = res.Body["uri"].(string)
		wantSecret, _ = res.Body["secret"].(string)
		dataURI, _ := res.Body["qr"].(string)

		// Everything below the decode is asserted unconditionally: a missing or
		// malformed PNG is a real failure and must not be retried away.
		raw, err := base64.StdEncoding.DecodeString(
			strings.TrimPrefix(dataURI, "data:image/png;base64,"))
		if err != nil {
			t.Fatalf("decoding the data URI: %v", err)
		}
		img, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("the payload is not a decodable PNG: %v", err)
		}
		bmp, err := gozxing.NewBinaryBitmapFromImage(img)
		if err != nil {
			t.Fatalf("preparing the bitmap: %v", err)
		}

		result, err := qrcode.NewQRCodeReader().Decode(bmp, nil)
		if err == nil {
			got = result.GetText()
			break
		}
		lastErr = err
		t.Logf("attempt %d: gozxing could not read a valid QR (%v); retrying with "+
			"a fresh enrollment", attempt, err)
	}
	if got == "" {
		t.Fatalf("the QR code could not be read back in three attempts, which is "+
			"far past this reader's measured failure rate and therefore a real "+
			"problem with the image: %v", lastErr)
	}

	if got != wantURI {
		t.Errorf("the QR encodes a different URI than the one offered:\n  qr:  %s\n  uri: %s", got, wantURI)
	}
	if !strings.Contains(got, "secret="+wantSecret) {
		t.Errorf("the QR does not carry the offered secret:\n  qr: %s", got)
	}
	if !strings.HasPrefix(got, "otpauth://totp/") {
		t.Errorf("the QR is not an otpauth URI: %s", got)
	}
}

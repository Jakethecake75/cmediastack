package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/notify"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/config"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
	"github.com/jakethecake75/cmediastack/internal/subtitles"
)

type keyRig struct {
	database         *db.DB
	path             string
	oldKey, newKey   string
	oldCiph, newCiph *secrets.Cipher
	userID, idxID    int64
	cgID             int64
}

func newKey(t *testing.T) (string, *secrets.Cipher) {
	t.Helper()
	k, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	c, err := secrets.NewCipherFromBase64(k)
	if err != nil {
		t.Fatal(err)
	}
	return k, c
}

// newKeyRig is a database holding one of every stored secret, sealed under
// the old key by the stores that seal them.
func newKeyRig(t *testing.T) *keyRig {
	t.Helper()
	r := &keyRig{path: filepath.Join(t.TempDir(), "keys.db")}
	r.oldKey, r.oldCiph = newKey(t)
	r.newKey, r.newCiph = newKey(t)
	database, err := db.Open(db.Options{Path: r.path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.database = database
	ctx := authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 1, Username: "jacob", State: authz.StateActive,
		MFASatisfied: true, Role: authz.Role{Name: "Admin", Rank: 100, Permissions: authz.NewPermissionSet(authz.AllPermissions...)}})

	ids := identity.NewStore(database, r.oldCiph, identity.Argon2Params{Memory: 64, Iterations: 1, Parallelism: 1,
		SaltLength: 16, KeyLength: 32}, time.Now)
	if err := ids.EnsureBuiltinRoles(ctx); err != nil {
		t.Fatal(err)
	}
	role, _ := ids.RoleByName(ctx, authz.RoleAdmin)
	if r.userID, err = ids.CreateUser(ctx, identity.NewUser{Username: "jacob", Email: "j@example.com",
		PasswordHash: "$argon2id$stub", RoleID: role.ID}); err != nil {
		t.Fatal(err)
	}
	if err := ids.SetTOTPSecret(ctx, r.userID, "JBSWY3DPEHPK3PXP"); err != nil {
		t.Fatal(err)
	}
	if r.idxID, err = indexer.NewStore(database, r.oldCiph, time.Now).Create(ctx, indexer.Definition{
		Name: "Tracker", Kind: "torznab", BaseURL: "https://tracker.example", APIKey: "an-api-key", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if r.cgID, err = indexer.NewStore(database, r.oldCiph, time.Now).Create(ctx, indexer.Definition{
		Name: "Private", Kind: indexer.KindCardigann, BaseURL: "https://private.example", Enabled: true,
		Cardigann: `id: p
settings:
  - {name: username, type: text}
  - {name: password, type: password}
search:
  paths: [{path: s}]
  rows: {selector: tr}
  fields:
    title: {selector: a}
    download: {selector: a, attribute: href}
`,
		Settings: map[string]string{"username": "u", "password": "pw"}}); err != nil {
		t.Fatal(err)
	}
	for key, plain := range map[string][2]string{
		metadata.SealedSetting:   {metadata.SealedContext, "a-tmdb-token"},
		notify.SealedSetting:     {notify.SealedContext, "https://discord.com/api/webhooks/1/x"},
		subtitles.SealedKey:      {subtitles.SealedKeyContext, "an-opensubtitles-key"},
		subtitles.SealedPassword: {subtitles.SealedPasswordContext, "an-opensubtitles-password"},
	} {
		sealed, err := r.oldCiph.EncryptString(plain[1], plain[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(t.Context(), `INSERT INTO setting (key, value, updated_at) VALUES (?, ?, 'x')`,
			key, base64.StdEncoding.EncodeToString(sealed)); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

// opens reports whether every stored secret opens under the cipher.
func (r *keyRig) opens(t *testing.T, c *secrets.Cipher) map[string]string {
	t.Helper()
	got := map[string]string{}
	ids := identity.NewStore(r.database, c, identity.Argon2Params{}, time.Now)
	if u, err := ids.UserByUsername(t.Context(), "jacob"); err == nil {
		if s, err := ids.TOTPSecret(u); err == nil {
			got["totp"] = s
		}
	}
	var key []byte
	if err := r.database.QueryRowContext(t.Context(), `SELECT api_key_enc FROM indexer WHERE id = ?`, r.idxID).Scan(&key); err == nil {
		if s, err := c.DecryptString(key, indexer.KeyContext(r.idxID)); err == nil {
			got["indexer"] = s
		}
	}
	var settings []byte
	if err := r.database.QueryRowContext(t.Context(), `SELECT settings_enc FROM indexer WHERE id = ?`, r.cgID).Scan(&settings); err == nil {
		if raw, err := c.Decrypt(settings, indexer.SettingsContext(r.cgID)); err == nil {
			got["cardigann"] = string(raw)
		}
	}
	for name, s := range map[string][2]string{"tmdb": {metadata.SealedSetting, metadata.SealedContext},
		"discord": {notify.SealedSetting, notify.SealedContext}, "os-key": {subtitles.SealedKey, subtitles.SealedKeyContext},
		"os-password": {subtitles.SealedPassword, subtitles.SealedPasswordContext}} {
		var v string
		if err := r.database.QueryRowContext(t.Context(), `SELECT value FROM setting WHERE key = ?`, s[0]).Scan(&v); err == nil {
			raw, _ := base64.StdEncoding.DecodeString(v)
			if p, err := c.DecryptString(raw, s[1]); err == nil {
				got[name] = p
			}
		}
	}
	return got
}

// ADR-0054, decision 2: every stored secret re-sealed, under its own context.
func TestTheMasterKeyIsRotated(t *testing.T) {
	r := newKeyRig(t)
	if got := r.opens(t, r.oldCiph); len(got) != len(rotatedKinds) {
		t.Fatalf("the rig sealed %v", got)
	}
	counts, err := rotate(t.Context(), r.database, r.oldCiph, r.newCiph)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range rotatedKinds {
		if counts[k] != 1 {
			t.Errorf("%s: %d re-sealed", k, counts[k])
		}
	}
	want := map[string]string{"totp": "JBSWY3DPEHPK3PXP", "indexer": "an-api-key", "tmdb": "a-tmdb-token",
		"discord": "https://discord.com/api/webhooks/1/x", "os-key": "an-opensubtitles-key",
		"os-password": "an-opensubtitles-password", "cardigann": `{"password":"pw","username":"u"}`}
	got := r.opens(t, r.newCiph)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s under the new key: %q, want %q", k, got[k], v)
		}
	}
	if old := r.opens(t, r.oldCiph); len(old) != 0 {
		t.Errorf("still opening under the old key: %v", old)
	}
}

// ADR-0054, decision 2: one value that will not open under the old key, and
// nothing changes.
func TestARotationThatCannotOpenEverythingChangesNothing(t *testing.T) {
	r := newKeyRig(t)
	_, stranger := newKey(t)
	sealed, _ := stranger.EncryptString("another-key", indexer.KeyContext(r.idxID))
	if _, err := r.database.ExecContext(t.Context(), `UPDATE indexer SET api_key_enc = ? WHERE id = ?`, sealed, r.idxID); err != nil {
		t.Fatal(err)
	}
	_, err := rotate(t.Context(), r.database, r.oldCiph, r.newCiph)
	if err == nil || !strings.Contains(err.Error(), "indexer API key") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("rotate: %v", err)
	}
	got := r.opens(t, r.oldCiph)
	if got["totp"] == "" || got["tmdb"] == "" || got["discord"] == "" {
		t.Errorf("a failed rotation changed what had opened: %v", got)
	}
	if n := r.opens(t, r.newCiph); len(n) != 0 {
		t.Errorf("a failed rotation left values under the new key: %v", n)
	}
}

// ADR-0054, decision 1: the command's guards, its audit line, and what it says.
func TestTheRotateCommand(t *testing.T) {
	r := newKeyRig(t)
	cfg := config.Default()
	cfg.Database.Path = r.path
	cfg.Secrets.MasterKeyEnv = "CMS_TEST_KEY"
	t.Setenv("CMS_TEST_KEY", r.oldKey)
	stopped := func() bool { return false }

	t.Setenv("CMS_TEST_KEY_NEW", r.oldKey)
	if err := runRotateKey(cfg, stopped, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "is the current key") {
		t.Errorf("the same key: %v", err)
	}
	t.Setenv("CMS_TEST_KEY_NEW", "not-a-key")
	if err := runRotateKey(cfg, stopped, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "CMS_TEST_KEY_NEW") {
		t.Errorf("a bad new key: %v", err)
	}
	t.Setenv("CMS_TEST_KEY_NEW", r.newKey)
	if err := runRotateKey(cfg, func() bool { return true }, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "running") {
		t.Errorf("with the server running: %v", err)
	}
	if got := r.opens(t, r.oldCiph); len(got) != len(rotatedKinds) {
		t.Fatalf("a refused rotation changed something: %v", got)
	}

	var out bytes.Buffer
	if err := runRotateKey(cfg, stopped, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "7 sealed value(s)") || !strings.Contains(out.String(), "KEEP THE OLD KEY") {
		t.Errorf("it said:\n%s", out.String())
	}
	if got := r.opens(t, r.newCiph); len(got) != len(rotatedKinds) {
		t.Errorf("after the command: %v", got)
	}
	var detail, actor string
	if err := r.database.QueryRowContext(t.Context(), `SELECT detail, actor_label FROM audit_event WHERE action = ?`,
		audit.ActionMasterKeyRotated).Scan(&detail, &actor); err != nil || actor != "host" ||
		strings.Contains(detail, r.oldKey) || strings.Contains(detail, r.newKey) || !strings.Contains(detail, "re-sealed") {
		t.Errorf("audit %q by %q: %v", detail, actor, err)
	}
}

// ADR-0054, decision 3: every call that seals with the cipher is a value the
// rotation re-seals, or one of the two that expire instead.
func TestEverySealedValueIsRotated(t *testing.T) {
	// Each file that seals stored values, and how many kinds it seals.
	rotated := map[string]int{
		"identity/store.go": 1, "indexer/store.go": 2, "metadata/service.go": 1, "notify/service.go": 1,
		"subtitles/service.go": 2,
	}
	expiring := map[string]bool{"search/ticket.go": true, "identity/pow.go": true}
	kinds := 0
	for _, n := range rotated {
		kinds += n
	}
	if len(rotatedKinds) != kinds {
		t.Errorf("%d kinds rotated, %d sealed by the files below", len(rotatedKinds), kinds)
	}
	seal := regexp.MustCompile(`cipher\.Encrypt(String)?\(`)
	root := filepath.Join("..", "..", "internal")
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "platform/secrets/") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if seal.Match(src) {
			seen[rel] = true
			if rotated[rel] == 0 && !expiring[rel] {
				t.Errorf("%s seals a value the key rotation does not re-seal: add it to rotate.go, or to "+
					"this test's expiring list with the reason it need not be", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for f := range rotated {
		if !seen[f] {
			t.Errorf("%s no longer seals anything; take it out of the rotation", f)
		}
	}

}

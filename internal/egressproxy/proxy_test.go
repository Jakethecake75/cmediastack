package egressproxy

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/platform/config"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

type memSettings map[string]string

func (m memSettings) Setting(_ context.Context, key string) (string, error) { return m[key], nil }
func (m memSettings) SetSetting(_ context.Context, key, value string) error {
	m[key] = value
	return nil
}

func testCipher(t *testing.T) (*secrets.Cipher, string) {
	t.Helper()
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	c, err := secrets.NewCipherFromBase64(key)
	if err != nil {
		t.Fatal(err)
	}
	return c, key
}

// base is the configuration this deployment boots with: every profile
// direct, no tunnel, nothing enforced — and a getenv that holds a master key,
// which the lint asks for.
func base(t *testing.T) (config.Config, func(string) string) {
	t.Helper()
	cfg := config.Default()
	cfg.Egress.RequireNamespaceGuard = false
	_, key := testCipher(t)
	return cfg, func(name string) string {
		if name == cfg.Secrets.MasterKeyEnv {
			return key
		}
		return ""
	}
}

var nord = Setting{Address: "amsterdam.nl.socks.nordhold.net:1080", Username: "svc-user",
	Password: "svc-pass", Profiles: []string{"download", "metadata"}}

func TestAProxyIsStoredSealedAndLoaded(t *testing.T) {
	c, _ := testCipher(t)
	m := memSettings{}
	s := NewStore(m, c)
	if err := s.Save(t.Context(), nord); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Load(t.Context())
	if err != nil || !ok {
		t.Fatalf("load: %v %v", ok, err)
	}
	if got.Address != nord.Address || got.Username != nord.Username || got.Password != nord.Password ||
		strings.Join(got.Profiles, ",") != "download,metadata" {
		t.Errorf("loaded %+v, want %+v", got, nord)
	}
	if strings.Contains(m[PlainSetting], "svc-pass") {
		t.Errorf("the plain setting holds the password: %s", m[PlainSetting])
	}
	sealed, err := base64.StdEncoding.DecodeString(m[SealedSetting])
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := c.DecryptString(sealed, SealedContext); err != nil || plain != "svc-pass" {
		t.Errorf("the sealed password does not open under %q: %q %v", SealedContext, plain, err)
	}
	if _, err := c.DecryptString(sealed, "other"); err == nil {
		t.Error("the sealed password opens under another context")
	}
}

func TestClearingTheProxyRemovesItsPassword(t *testing.T) {
	c, _ := testCipher(t)
	m := memSettings{}
	s := NewStore(m, c)
	if err := s.Save(t.Context(), nord); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(t.Context(), Setting{}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Load(t.Context()); ok || err != nil {
		t.Errorf("after clearing: set=%v err=%v", ok, err)
	}
	if m[PlainSetting] != "" || m[SealedSetting] != "" {
		t.Errorf("clearing left %q / %q", m[PlainSetting], m[SealedSetting])
	}
}

func TestTickedDirectProfilesBecomeSocks5(t *testing.T) {
	cfg, getenv := base(t)
	out, st := Apply(cfg, nord, getenv)
	for _, name := range []string{"download", "metadata"} {
		p := out.Egress.Profiles[name]
		if p.Mode != config.EgressSOCKS5 || p.Address != nord.Address || p.Username != nord.Username ||
			p.Password != nord.Password || !p.RemoteDNS {
			t.Errorf("%s = %+v, want socks5 through the proxy with remote DNS", name, p)
		}
	}
	if out.Egress.Profiles["indexer"].Mode != config.EgressDirect {
		t.Errorf("an unticked profile changed: %+v", out.Egress.Profiles["indexer"])
	}
	if cfg.Egress.Profiles["download"].Mode != config.EgressDirect {
		t.Error("Apply changed the configuration it was given")
	}
	if st.Problem != "" || st.InForce.Address != nord.Address || st.InForce.Password != "" {
		t.Errorf("status %+v", st)
	}
}

func TestAProfileTheFileSetKeepsTheFilesSetting(t *testing.T) {
	cfg, getenv := base(t)
	cfg.Egress.Profiles["download"] = config.EgressProfile{Mode: config.EgressBlocked}
	out, st := Apply(cfg, nord, getenv)
	if out.Egress.Profiles["download"].Mode != config.EgressBlocked {
		t.Errorf("download = %+v, want the file's blocked", out.Egress.Profiles["download"])
	}
	if strings.Join(st.FileSet, ",") != "download" {
		t.Errorf("FileSet = %v, want [download]", st.FileSet)
	}
}

func TestATickedProfileIsNeverLeftDirect(t *testing.T) {
	cfg, getenv := base(t)
	for mask := 1; mask < 1<<len(Offered); mask++ {
		s := nord
		s.Profiles = nil
		for i, name := range Offered {
			if mask&(1<<i) != 0 {
				s.Profiles = append(s.Profiles, name)
			}
		}
		out, _ := Apply(cfg, s, getenv)
		for _, name := range s.Profiles {
			if out.Egress.Profiles[name].Mode == config.EgressDirect {
				t.Errorf("ticked %v: %s left direct", s.Profiles, name)
			}
		}
	}
}

func TestAnOverlayTheLintRefusesBlocksInsteadOfBricking(t *testing.T) {
	cfg, getenv := base(t)
	s := nord
	s.Profiles = []string{"indexer"} // proxied searches, direct metadata: the lint refuses
	out, st := Apply(cfg, s, getenv)
	if out.Egress.Profiles["indexer"].Mode != config.EgressBlocked {
		t.Errorf("indexer = %+v, want blocked", out.Egress.Profiles["indexer"])
	}
	if !strings.Contains(st.Problem, "metadata") {
		t.Errorf("Problem = %q, want the lint's words", st.Problem)
	}
	if err := config.Lint(out, getenv); err != nil {
		t.Errorf("the fallback itself fails the lint: %v", err)
	}
}

func TestValidateRefusesWhatTheLintRefuses(t *testing.T) {
	cfg, getenv := base(t)
	for _, tc := range []struct {
		name string
		s    Setting
		want string
	}{
		{"indexer alone", Setting{Address: "p:1080", Profiles: []string{"indexer"}}, "metadata"},
		{"no port", Setting{Address: "proxy.example", Profiles: []string{"download"}}, "host:port"},
		{"not offered", Setting{Address: "p:1080", Profiles: []string{"notification"}}, "notification"},
		{"no profile", Setting{Address: "p:1080"}, "profile"},
	} {
		if err := Validate(cfg, tc.s, getenv); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error naming %q", tc.name, err, tc.want)
		}
	}
	if err := Validate(cfg, Setting{Address: "p:1080", Username: "u", Password: "pw",
		Profiles: []string{"download"}}, getenv); err != nil {
		t.Errorf("download alone: %v", err)
	}
	if err := Validate(cfg, Setting{}, getenv); err != nil {
		t.Errorf("clearing: %v", err)
	}
}

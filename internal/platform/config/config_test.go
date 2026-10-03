package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validEnv returns a getenv func that satisfies the secrets requirement.
func validEnv(extra map[string]string) func(string) string {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	env := map[string]string{"CMS_MASTER_KEY": key}
	for k, v := range extra {
		env[k] = v
	}
	return func(k string) string { return env[k] }
}

func mustLint(t *testing.T, cfg Config, getenv func(string) string) {
	t.Helper()
	if err := Lint(cfg, getenv); err != nil {
		t.Fatalf("expected config to lint clean, got: %v", err)
	}
}

func lintProblems(t *testing.T, cfg Config, getenv func(string) string) string {
	t.Helper()
	err := Lint(cfg, getenv)
	if err == nil {
		t.Fatal("expected lint to fail, it passed")
	}
	le, ok := AsLintError(err)
	if !ok {
		t.Fatalf("expected *LintError, got %T", err)
	}
	return strings.Join(le.Problems, "\n")
}

func TestDefaultConfigLintsClean(t *testing.T) {
	mustLint(t, Default(), validEnv(nil))
}

// The named example from requirements §9: refuse to boot when anonymity is on
// but nothing enforces it.
func TestLintRejectsAnonymityWithoutEnforcement(t *testing.T) {
	cfg := Default()
	cfg.Egress.AnonymityEnabled = true
	cfg.Egress.RequireNamespaceGuard = false
	cfg.Egress.Profiles["download"] = EgressProfile{Mode: EgressDirect}

	got := lintProblems(t, cfg, validEnv(nil))
	if !strings.Contains(got, "no enforcement") {
		t.Errorf("expected an enforcement complaint, got:\n%s", got)
	}
}

func TestLintRejectsSocks5WithoutRemoteDNS(t *testing.T) {
	cfg := Default()
	cfg.Egress.AnonymityEnabled = true
	cfg.Egress.RequireNamespaceGuard = false
	cfg.Egress.Profiles["download"] = EgressProfile{
		Mode:      EgressSOCKS5,
		Address:   "proxy.example:1080",
		RemoteDNS: false,
	}

	got := lintProblems(t, cfg, validEnv(nil))
	if !strings.Contains(got, "remote_dns") {
		t.Errorf("expected a remote_dns complaint, got:\n%s", got)
	}
}

func TestLintAcceptsNamespaceGuardWithoutProxy(t *testing.T) {
	// ADR-0001: the namespace guard alone is a sufficient primary control.
	cfg := Default()
	cfg.Egress.AnonymityEnabled = true
	cfg.Egress.RequireNamespaceGuard = true
	cfg.Egress.Profiles["download"] = EgressProfile{Mode: EgressBlocked}
	mustLint(t, cfg, validEnv(nil))
}

func TestLintRejectsMissingOrShortMasterKey(t *testing.T) {
	cfg := Default()

	got := lintProblems(t, cfg, func(string) string { return "" })
	if !strings.Contains(got, "CMS_MASTER_KEY") {
		t.Errorf("expected a master key complaint, got:\n%s", got)
	}

	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	got = lintProblems(t, cfg, validEnv(map[string]string{"CMS_MASTER_KEY": short}))
	if !strings.Contains(got, "want exactly 32") {
		t.Errorf("expected a key length complaint, got:\n%s", got)
	}
}

func TestLintRejectsDisabledMFA(t *testing.T) {
	cfg := Default()
	cfg.Auth.MFARequired = false
	got := lintProblems(t, cfg, validEnv(nil))
	if !strings.Contains(got, "mfa_required") {
		t.Errorf("expected an MFA complaint, got:\n%s", got)
	}
}

func TestLintRejectsWeakArgon2Params(t *testing.T) {
	cfg := Default()
	cfg.Auth.Argon2Memory = 1024
	got := lintProblems(t, cfg, validEnv(nil))
	if !strings.Contains(got, "argon2_memory_kib") {
		t.Errorf("expected an argon2 complaint, got:\n%s", got)
	}
}

func TestLintRejectsUncappedOpenRegistration(t *testing.T) {
	cfg := Default()
	cfg.Registration.Mode = RegistrationOpen
	cfg.Registration.MaxOutstanding = 0
	cfg.Registration.PerIPPerHour = 0

	got := lintProblems(t, cfg, validEnv(nil))
	if !strings.Contains(got, "max_outstanding") {
		t.Errorf("expected a max_outstanding complaint, got:\n%s", got)
	}
	if !strings.Contains(got, "per_ip_per_hour") {
		t.Errorf("expected a per_ip_per_hour complaint, got:\n%s", got)
	}
}

func TestLintRejectsPubliclyBoundManagementListener(t *testing.T) {
	cfg := Default()
	cfg.Server.ManagementAddr = "0.0.0.0:9090"
	got := lintProblems(t, cfg, validEnv(nil))
	if !strings.Contains(got, "management_addr") {
		t.Errorf("expected a management_addr complaint, got:\n%s", got)
	}
}

func TestLintRejectsManagementOnPublicListener(t *testing.T) {
	cfg := Default()
	cfg.Server.ManagementAddr = cfg.Server.Addr
	got := lintProblems(t, cfg, validEnv(nil))
	if !strings.Contains(got, "equals server.addr") {
		t.Errorf("expected a shared-listener complaint, got:\n%s", got)
	}
}

func TestLintRejectsHalfConfiguredTLS(t *testing.T) {
	cfg := Default()
	cfg.Server.TLSCertFile = "/certs/tls.crt"
	got := lintProblems(t, cfg, validEnv(nil))
	if !strings.Contains(got, "must be set together") {
		t.Errorf("expected a TLS pairing complaint, got:\n%s", got)
	}
}

func TestApplyEnvOverrides(t *testing.T) {
	cfg := Default()
	getenv := validEnv(map[string]string{
		"CMS_SERVER_ADDR":       "127.0.0.1:9999",
		"CMS_REGISTRATION_MODE": "invite",
		"CMS_TRUSTED_PROXIES":   "10.0.0.0/8, 172.16.0.1",
		"CMS_ANONYMITY_ENABLED": "true",
	})
	applyEnv(&cfg, getenv)

	if cfg.Server.Addr != "127.0.0.1:9999" {
		t.Errorf("server addr override failed: %q", cfg.Server.Addr)
	}
	if cfg.Registration.Mode != RegistrationInvite {
		t.Errorf("registration mode override failed: %q", cfg.Registration.Mode)
	}
	if len(cfg.Server.TrustedProxies) != 2 || cfg.Server.TrustedProxies[1] != "172.16.0.1" {
		t.Errorf("trusted proxies override failed: %v", cfg.Server.TrustedProxies)
	}
	if !cfg.Egress.AnonymityEnabled {
		t.Error("anonymity override failed")
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	t.Setenv("CMS_MASTER_KEY", key)

	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("Load with a missing file should succeed, got: %v", err)
	}
	if cfg.Server.Addr != Default().Server.Addr {
		t.Errorf("expected defaults, got %q", cfg.Server.Addr)
	}
}

func TestLoadParsesFileAndLints(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	t.Setenv("CMS_MASTER_KEY", key)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "" +
		"server:\n" +
		"  addr: 127.0.0.1:8181\n" +
		"registration:\n" +
		"  mode: invite\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Addr != "127.0.0.1:8181" {
		t.Errorf("addr = %q", cfg.Server.Addr)
	}
	if cfg.Registration.Mode != RegistrationInvite {
		t.Errorf("mode = %q", cfg.Registration.Mode)
	}
	// Unspecified fields must retain their defaults.
	if cfg.Auth.Argon2Memory != Default().Auth.Argon2Memory {
		t.Errorf("argon2 memory should have defaulted, got %d", cfg.Auth.Argon2Memory)
	}
}

// The lint must not refuse the configuration the documentation recommends.
//
// "direct" under ADR-0001 means direct inside a network namespace whose only
// route out is the tunnel — it is the intended deployment, and
// config.example.yaml says so in a comment. An earlier version of this rule
// rejected it outright, so an operator following the example could not boot and
// the only clue was a lint message asserting the configuration was never
// intended. Found by starting the binary with anonymity on.
func TestTheRecommendedEgressConfigurationPassesTheLint(t *testing.T) {
	cfg := Default()
	cfg.Egress.AnonymityEnabled = true
	cfg.Egress.RequireNamespaceGuard = true
	cfg.Egress.Profiles["download"] = EgressProfile{Mode: EgressDirect}

	if err := Lint(cfg, validEnv(nil)); err != nil && strings.Contains(err.Error(), "download") {
		t.Errorf("the lint refuses the ADR-0001 deployment: %v", err)
	}
}

// The case that IS wrong: anonymity on, direct, and nothing enforcing it.
func TestDirectWithNoEnforcementIsStillRefused(t *testing.T) {
	cfg := Default()
	cfg.Egress.AnonymityEnabled = true
	cfg.Egress.RequireNamespaceGuard = false
	cfg.Egress.Profiles["download"] = EgressProfile{Mode: EgressDirect}

	err := Lint(cfg, validEnv(nil))
	if err == nil {
		t.Fatal("anonymity with no enforcement at all was accepted")
	}
	if !strings.Contains(err.Error(), "namespace guard") {
		t.Errorf("the message does not say how to fix it: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The half-tunnelled instance
// ---------------------------------------------------------------------------

// Tunnelling the traffic that LOOKS sensitive while leaving the traffic that
// discloses the same thing on the host's own route.
//
// An indexer search says "somebody here wants X". A metadata lookup says "this
// instance HOLDS X", and an artwork fetch says it again from a second host.
// Proxying the first and not the others does not protect the operator — it
// makes them believe they are protected, which is worse than knowing they are
// not, because it is the belief that stops them looking.
func TestAHalfTunnelledInstanceIsRefused(t *testing.T) {
	cfg := Default()
	cfg.Egress.Profiles["indexer"] = EgressProfile{
		Mode: EgressSOCKS5, Address: "127.0.0.1:1080", RemoteDNS: true,
	}
	// metadata and subtitle are left on the default: direct.

	got := lintProblems(t, cfg, validEnv(nil))
	for _, want := range []string{"metadata", "subtitle"} {
		if !strings.Contains(got, "egress.profiles."+want) {
			t.Errorf("the lint did not complain about %s:\n%s", want, got)
		}
	}
	// The message has to say WHY, or an operator silences it by making the
	// mistake symmetrical in the wrong direction.
	if !strings.Contains(got, "discloses what this instance holds") {
		t.Errorf("the message does not explain the leak:\n%s", got)
	}
}

func TestAConsistentlyConfiguredInstanceIsNotNagged(t *testing.T) {
	proxied := EgressProfile{Mode: EgressSOCKS5, Address: "127.0.0.1:1080", RemoteDNS: true}

	t.Run("everything proxied", func(t *testing.T) {
		cfg := Default()
		for _, n := range []string{"indexer", "metadata", "subtitle"} {
			cfg.Egress.Profiles[n] = proxied
		}
		mustLint(t, cfg, validEnv(nil))
	})

	t.Run("everything direct is a decision, not a mistake", func(t *testing.T) {
		// The default. If an operator has chosen direct throughout, this lint
		// has nothing to say: there is no false reassurance to puncture.
		mustLint(t, Default(), validEnv(nil))
	})

	t.Run("blocked counts as consistent", func(t *testing.T) {
		cfg := Default()
		cfg.Egress.Profiles["indexer"] = proxied
		cfg.Egress.Profiles["metadata"] = EgressProfile{Mode: EgressBlocked}
		cfg.Egress.Profiles["subtitle"] = EgressProfile{Mode: EgressBlocked}
		mustLint(t, cfg, validEnv(nil))
	})
}

// A namespace guard protects the DOWNLOAD process's network namespace. The
// application process — which is what fetches metadata and artwork — does not
// run in it, so counting a guarded "direct" as proxied would be precisely the
// false reassurance this check exists to prevent.
func TestTheNamespaceGuardDoesNotExcuseDirectMetadata(t *testing.T) {
	cfg := Default()
	cfg.Egress.RequireNamespaceGuard = true
	cfg.Egress.Profiles["indexer"] = EgressProfile{
		Mode: EgressSOCKS5, Address: "127.0.0.1:1080", RemoteDNS: true,
	}
	got := lintProblems(t, cfg, validEnv(nil))
	if !strings.Contains(got, "egress.profiles.metadata") {
		t.Errorf("a namespace guard silenced the metadata warning:\n%s", got)
	}
}

package config

import (
	"strings"
	"testing"
)

// ADR-0052, decision 2: the source offer's address is an absolute http(s) one,
// the project's by default.
func TestTheSourceAddressIsAnAbsoluteWebAddress(t *testing.T) {
	if Default().Server.SourceURL != DefaultSourceURL {
		t.Errorf("default %q", Default().Server.SourceURL)
	}
	if err := Lint(Default(), validEnv(nil)); err != nil && strings.Contains(err.Error(), "source_url") {
		t.Errorf("the default was refused: %v", err)
	}
	for _, bad := range []string{"", "javascript:alert(1)", "/source", "ftp://example.com/src",
		"https://", "github.com/x/y"} {
		cfg := Default()
		cfg.Server.SourceURL = bad
		if got := lintProblems(t, cfg, validEnv(nil)); !strings.Contains(got, "server.source_url") {
			t.Errorf("%q was accepted: %s", bad, got)
		}
	}
	cfg := Default()
	cfg.Server.SourceURL = "http://git.lan/me/cmediastack-fork"
	if err := Lint(cfg, validEnv(nil)); err != nil && strings.Contains(err.Error(), "source_url") {
		t.Errorf("an operator's own address was refused: %v", err)
	}
}

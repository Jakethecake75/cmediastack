package config

import (
	"strings"
	"testing"
)

// Upgrades are off by default, and only mean something with automatic
// acquisition on (ADR-0036).
func TestUpgradesNeedAutomaticAcquisition(t *testing.T) {
	cfg := Default()
	if cfg.Acquisition.Upgrades {
		t.Fatal("upgrades are on by default")
	}
	cfg.Acquisition.Upgrades = true
	if got := lintProblems(t, cfg, validEnv(nil)); !strings.Contains(got, "acquisition.upgrades is true but acquisition.automatic is false") {
		t.Errorf("problems = %q", got)
	}
	cfg.Acquisition.Automatic = true
	cfg.Download.Enabled = true
	mustLint(t, cfg, validEnv(nil))
}

package config

import (
	"strings"
	"testing"
	"time"
)

// A day without progress is a stall by default; off at zero; never under an
// hour, because a swarm that is slow to find is not a dead one (ADR-0034).
func TestTheStallThresholdIsChecked(t *testing.T) {
	cfg := Default()
	if cfg.Download.StallAfter != 24*time.Hour {
		t.Fatalf("stall_after defaults to %s, want 24h", cfg.Download.StallAfter)
	}
	mustLint(t, cfg, validEnv(nil))

	cfg.Download.StallAfter = 0
	mustLint(t, cfg, validEnv(nil))

	for _, d := range []time.Duration{30 * time.Minute, -time.Hour} {
		cfg.Download.StallAfter = d
		if got := lintProblems(t, cfg, validEnv(nil)); !strings.Contains(got, "download.stall_after is "+d.String()) {
			t.Errorf("%s: problems = %q, want one about download.stall_after", d, got)
		}
	}
}

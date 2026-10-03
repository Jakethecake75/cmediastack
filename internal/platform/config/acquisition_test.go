package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Off by default (ADR-0030, decision 1), with a budget that is sane if it is
// ever turned on.
func TestAutomaticAcquisitionIsOffByDefault(t *testing.T) {
	a := Default().Acquisition
	if a.Automatic {
		t.Fatal("automatic acquisition is on by default")
	}
	if a.RSSInterval != 15*time.Minute || a.SearchInterval != 15*time.Minute ||
		a.SearchesPerRun != 3 || a.MaxGrabsPerRun != 5 {
		t.Fatalf("defaults = %+v", a)
	}
	// And the defaults pass the lint once it is on, with the engine on.
	cfg := Default()
	cfg.Acquisition.Automatic = true
	cfg.Download.Enabled = true
	mustLint(t, cfg, validEnv(nil))
}

// What the lint refuses, and says why.
func TestAutomaticAcquisitionSettingsThatCannotWorkAreRefused(t *testing.T) {
	cases := map[string]struct {
		change func(*Config)
		want   string
	}{
		"with the engine off": {func(c *Config) { c.Download.Enabled = false },
			"download.enabled is false"},
		"asking for recent releases too often": {func(c *Config) { c.Acquisition.RSSInterval = 5 * time.Minute },
			"rss_interval is 5m0s"},
		"searching too often": {func(c *Config) { c.Acquisition.SearchInterval = time.Minute },
			"search_interval is 1m0s"},
		"no searches": {func(c *Config) { c.Acquisition.SearchesPerRun = 0 },
			"searches_per_run is 0"},
		"a flood of searches": {func(c *Config) { c.Acquisition.SearchesPerRun = 51 },
			"searches_per_run is 51"},
		"no grabs": {func(c *Config) { c.Acquisition.MaxGrabsPerRun = 0 },
			"max_grabs_per_run is 0"},
		"a flood of grabs": {func(c *Config) { c.Acquisition.MaxGrabsPerRun = 101 },
			"max_grabs_per_run is 101"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			cfg.Acquisition.Automatic = true
			cfg.Download.Enabled = true
			tc.change(&cfg)
			if got := lintProblems(t, cfg, validEnv(nil)); !strings.Contains(got, tc.want) {
				t.Fatalf("problems = %q, want one mentioning %q", got, tc.want)
			}
		})
	}

	// Off, the same settings are nobody's business: nothing reads them.
	cfg := Default()
	cfg.Acquisition.RSSInterval = time.Minute
	cfg.Acquisition.SearchesPerRun = 0
	mustLint(t, cfg, validEnv(nil))
}

// The file and the environment both turn it on; the budget is the file's.
func TestAutomaticAcquisitionLoadsFromTheFileAndTheEnvironment(t *testing.T) {
	t.Setenv("CMS_MASTER_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	p := filepath.Join(t.TempDir(), "c.yaml")
	body := "download:\n  enabled: true\n" +
		"acquisition:\n  automatic: true\n  rss_interval: 20m\n  search_interval: 30m\n" +
		"  searches_per_run: 4\n  max_grabs_per_run: 2\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.Acquisition
	if !a.Automatic || a.RSSInterval != 20*time.Minute || a.SearchInterval != 30*time.Minute ||
		a.SearchesPerRun != 4 || a.MaxGrabsPerRun != 2 {
		t.Fatalf("loaded %+v", a)
	}

	env := Default()
	applyEnv(&env, func(k string) string {
		if k == "CMS_ACQUISITION_AUTOMATIC" {
			return "true"
		}
		return ""
	})
	if !env.Acquisition.Automatic {
		t.Fatal("CMS_ACQUISITION_AUTOMATIC=true did not turn it on")
	}
}

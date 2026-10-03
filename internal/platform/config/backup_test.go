package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupsAreOnByDefault(t *testing.T) {
	b := Default().Backup
	if b.Interval != 24*time.Hour || b.Keep != 7*24*time.Hour || b.KeepMin != 3 {
		t.Fatalf("backup defaults = %+v", b)
	}
	if got := Default().BackupDir(); got != "/config/backups" {
		t.Fatalf("default backup dir = %q, want /config/backups beside the database", got)
	}
}

func TestTheBackupDirectoryResolvesAgainstTheDatabase(t *testing.T) {
	cfg := Default()
	cfg.Database.Path = "/srv/cms/data/cms.db"
	for dir, want := range map[string]string{
		"":                "/srv/cms/data/backups",
		"   ":             "/srv/cms/data/backups",
		"bk":              "/srv/cms/data/bk",
		"../bk":           "/srv/cms/bk",
		"/mnt/nas/cms/":   "/mnt/nas/cms",
		"/mnt/nas/./cms/": "/mnt/nas/cms",
	} {
		cfg.Backup.Dir = dir
		if got := cfg.BackupDir(); got != want {
			t.Errorf("backup.dir %q resolved to %q, want %q", dir, got, want)
		}
	}
}

func TestTheBackupDirectoryCanComeFromTheEnvironment(t *testing.T) {
	cfg := Default()
	applyEnv(&cfg, func(k string) string {
		if k == "CMS_BACKUP_DIR" {
			return "/mnt/nas/cms-backups"
		}
		return ""
	})
	if got := cfg.BackupDir(); got != "/mnt/nas/cms-backups" {
		t.Fatalf("backup dir = %q", got)
	}
}

func TestBackupSettingsTheScheduleCannotHonourAreRefused(t *testing.T) {
	cases := map[string]struct {
		change func(*BackupConfig)
		want   string
	}{
		"a negative interval":       {func(b *BackupConfig) { b.Interval = -time.Hour }, "backup.interval"},
		"an interval under an hour": {func(b *BackupConfig) { b.Interval = 15 * time.Minute }, "checked hourly"},
		"keeping nothing":           {func(b *BackupConfig) { b.Keep = 0 }, "backup.keep must be"},
		"no floor under pruning":    {func(b *BackupConfig) { b.KeepMin = 0 }, "could delete the last backup"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			tc.change(&cfg.Backup)
			if got := lintProblems(t, cfg, validEnv(nil)); !strings.Contains(got, tc.want) {
				t.Fatalf("problems = %q, want one mentioning %q", got, tc.want)
			}
		})
	}
}

func TestScheduledBackupsCanBeTurnedOff(t *testing.T) {
	cfg := Default()
	cfg.Backup.Interval = 0
	mustLint(t, cfg, validEnv(nil))
	cfg.Backup.Interval = time.Hour
	mustLint(t, cfg, validEnv(nil))
}

// "0s", not "0". A bare number is not a duration to the YAML decoder, and the
// file does not load at all — found by writing the documented configuration
// into a file and starting the binary on it. The example file and the lint
// messages say 0s; this keeps them honest.
func TestTurningScheduledBackupsOffIsWrittenZeroS(t *testing.T) {
	t.Setenv("CMS_MASTER_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "c.yaml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg, err := Load(write("backup:\n  interval: 0s\n"))
	if err != nil || cfg.Backup.Interval != 0 {
		t.Fatalf("0s: %v, %v", cfg.Backup.Interval, err)
	}
	if _, err := Load(write("backup:\n  interval: 0\n")); err == nil ||
		!strings.Contains(err.Error(), "time.Duration") {
		t.Fatalf("a bare 0: %v", err)
	}
}

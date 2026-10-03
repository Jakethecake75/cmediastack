package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/backup"
	"github.com/jakethecake75/cmediastack/internal/platform/config"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

// backupRig is an instance's configuration, its database, and one backup of it
// taken the way the server takes one.
func backupRig(t *testing.T) (config.Config, backup.Taken) {
	t.Helper()
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CMS_TEST_MASTER_KEY", key)
	cfg := config.Default()
	cfg.Secrets.MasterKeyEnv = "CMS_TEST_MASTER_KEY"
	cfg.Database.Path = filepath.Join(t.TempDir(), "cms.db")

	database, err := db.Open(db.Options{Path: cfg.Database.Path})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipherFromBase64(key)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := backup.New(database, cfg.Database.Path, cipher.BackupPassphrase(), backup.Policy{
		Dir: cfg.BackupDir(), CreateDir: true, Interval: 24 * time.Hour, Keep: 7 * 24 * time.Hour, KeepMin: 3,
	}, audit.New(database, time.Now), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	admin := authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 1, Username: "jacob",
		State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 1, Name: "Admin", Rank: 100, Permissions: authz.NewPermissionSet(authz.PermSystemSettings)}})
	tk, err := svc.Take(admin, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return cfg, tk
}

func TestVerifyBackupReportsAGoodBackup(t *testing.T) {
	cfg, tk := backupRig(t)
	var out strings.Builder
	if err := runVerifyBackup(cfg, tk.Path, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		tk.Path + ": good.", tk.SHA256, "checksum for checksum", "Nothing was changed.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}
}

func TestVerifyBackupWithAnotherKeyNamesTheSetting(t *testing.T) {
	cfg, tk := backupRig(t)
	other, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CMS_TEST_MASTER_KEY", other)
	err = runVerifyBackup(cfg, tk.Path, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "not taken with this master key") ||
		!strings.Contains(err.Error(), "CMS_TEST_MASTER_KEY") {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreBackupSaysHowToPutItInPlace(t *testing.T) {
	cfg, tk := backupRig(t)
	to := filepath.Join(t.TempDir(), "restored.db")
	var out strings.Builder
	if err := runRestoreBackup(cfg, tk.Path, to, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(to); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"decrypted, checked, and fenced", "Stop the server",
		cfg.Database.Path + "-wal", "Move " + to + " to " + cfg.Database.Path,
		"0 session(s) ended",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, out.String())
		}
	}
	// The live database is where it was, untouched.
	if _, err := os.Stat(cfg.Database.Path); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreBackupRefusesToGuessWhereToWrite(t *testing.T) {
	cfg, tk := backupRig(t)
	err := runRestoreBackup(cfg, tk.Path, "", &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "-restore-to") {
		t.Fatalf("err = %v", err)
	}
	// Nor over the live database.
	err = runRestoreBackup(cfg, tk.Path, cfg.Database.Path, &strings.Builder{})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}
}

// By the time the report is written the database HAS been restored. A console
// that cannot take the report must not turn that into a silent success.
func TestARestoreWhoseReportCannotBeWrittenSaysSo(t *testing.T) {
	cfg, tk := backupRig(t)
	to := filepath.Join(t.TempDir(), "restored.db")
	err := runRestoreBackup(cfg, tk.Path, to, brokenConsole{})
	if err == nil || !strings.Contains(err.Error(), "was restored to "+to) {
		t.Fatalf("err = %v", err)
	}
	err = runVerifyBackup(cfg, tk.Path, brokenConsole{})
	if err == nil || !strings.Contains(err.Error(), "the backup is good, but") {
		t.Fatalf("err = %v", err)
	}
}

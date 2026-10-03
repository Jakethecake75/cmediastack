package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/config"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

type brokenConsole struct{}

func (brokenConsole) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

// By the time the summary is written the account HAS been recovered and
// audited. A console that cannot take the summary must not turn that into a
// silent success: the command fails, and says the recovery happened.
func TestARecoveryWhoseSummaryCannotBeWrittenSaysSo(t *testing.T) {
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CMS_TEST_MASTER_KEY", key)
	cfg := config.Default()
	cfg.Secrets.MasterKeyEnv = "CMS_TEST_MASTER_KEY"
	cfg.Database.Path = filepath.Join(t.TempDir(), "cms.db")
	// Cheap, so the test does not spend its time hashing.
	cfg.Auth.Argon2Memory, cfg.Auth.Argon2Iterations, cfg.Auth.Argon2Parallelism = 1024, 1, 1

	database, err := db.Open(db.Options{Path: cfg.Database.Path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipherFromBase64(key)
	if err != nil {
		t.Fatal(err)
	}
	params := identity.Argon2Params{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
	store := identity.NewStore(database, cipher, params, time.Now)
	if err := store.EnsureBuiltinRoles(t.Context()); err != nil {
		t.Fatal(err)
	}
	svc := identity.NewService(store, audit.New(database, time.Now), identity.Policy{
		Password: identity.PasswordPolicy{MinLength: 12}, Argon2: params}, time.Now)
	if _, err := svc.CreateFirstAdmin(t.Context(), "jacob", "jacob@example.com",
		"correct-horse-battery-staple", "127.0.0.1", "test"); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()

	err = runRecover(cfg, "jacob", false, strings.NewReader(""), brokenConsole{})
	if err == nil || !strings.Contains(err.Error(), "recovered and audited, but this summary could not be written") {
		t.Fatalf("err = %v", err)
	}
}

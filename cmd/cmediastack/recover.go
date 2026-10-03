package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/config"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

// The break-glass console. See internal/identity/recovery.go for why this lives
// on the host rather than behind a route.
//
// This is the ONLY place identity.Service.Recover is called, and
// identity.TestOnlyTheCommandLineCanRecoverAnAccount fails the build if that
// stops being true.

// runRecover resets an account's authenticator, and optionally its password,
// from the host.
//
// It takes the same configuration and the same master key as the server, so it
// cannot be pointed at a database it could not otherwise read, and it applies
// migrations first: a recovery on a stale schema would fail in ways that look
// like corruption.
func runRecover(cfg config.Config, username string, alsoPassword bool, in io.Reader, out io.Writer) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("-recover needs a username")
	}

	cipher, err := secrets.NewCipherFromBase64(os.Getenv(cfg.Secrets.MasterKeyEnv))
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}

	// A password is read from stdin, never from a flag. An argument lands in
	// the shell history, in `ps` output for every user on the box, and in any
	// process accounting the host keeps — which would make the recovery console
	// a way to LEAK a credential rather than replace one.
	var newPassword string
	if alsoPassword {
		if _, err := fmt.Fprintf(out, "New password for %q (input is read from stdin, not echoed by a terminal\n"+
			"that has echo off; pipe it in if you would rather not type it): ", username); err != nil {
			return fmt.Errorf("writing the prompt, before anything was changed: %w", err)
		}
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("reading the new password: %w", err)
		}
		newPassword = strings.TrimRight(line, "\r\n")
		if newPassword == "" {
			return fmt.Errorf("no password was supplied; run without -recover-password " +
				"to reset only the authenticator")
		}
	}

	database, err := db.Open(db.Options{
		Path:        cfg.Database.Path,
		BusyTimeout: cfg.Database.BusyTimeout,
	})
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()

	ctx := context.Background()
	if _, err := database.Migrate(ctx); err != nil {
		return err
	}

	store := identity.NewStore(database, cipher, identity.Argon2Params{
		Memory:      cfg.Auth.Argon2Memory,
		Iterations:  cfg.Auth.Argon2Iterations,
		Parallelism: cfg.Auth.Argon2Parallelism,
		SaltLength:  cfg.Auth.Argon2SaltLength,
		KeyLength:   cfg.Auth.Argon2KeyLength,
	}, time.Now)

	svc := identity.NewService(store, audit.New(database, time.Now), identity.Policy{
		Password: identity.PasswordPolicy{MinLength: cfg.Auth.MinPasswordLength},
		Argon2: identity.Argon2Params{
			Memory:      cfg.Auth.Argon2Memory,
			Iterations:  cfg.Auth.Argon2Iterations,
			Parallelism: cfg.Auth.Argon2Parallelism,
			SaltLength:  cfg.Auth.Argon2SaltLength,
			KeyLength:   cfg.Auth.Argon2KeyLength,
		},
	}, time.Now)

	res, err := svc.Recover(ctx, username, newPassword)
	if err != nil {
		return err
	}

	// Composed, then written once. By now the account HAS been recovered and
	// audited; if the summary cannot reach the console, the command still
	// fails, so the operator is not left believing nothing happened.
	var b strings.Builder
	fmt.Fprintf(&b, "\nRecovered %q (%s).\n", res.Username, res.Role)
	fmt.Fprintf(&b, "  was:              %s, %s\n", res.PreviousState,
		enrolledWord(res.WasEnrolled))
	b.WriteString("  authenticator:    cleared, along with its recovery codes\n")
	if res.PasswordChanged {
		b.WriteString("  password:         replaced\n")
	} else {
		b.WriteString("  password:         unchanged\n")
	}
	fmt.Fprintf(&b, "  sessions revoked: %d\n", res.SessionsRevoked)
	fmt.Fprintf(&b, "  tokens revoked:   %d\n", res.TokensRevoked)
	fmt.Fprintf(&b, "\nThe account is now awaiting_mfa. Sign in with the password and you\n"+
		"will be taken through authenticator enrollment, which issues a fresh set\n"+
		"of recovery codes. Nothing here bypasses the second factor.\n\n"+
		"This was written to the audit log as %q.\n", audit.ActionAccountRecovered)
	if _, err := io.WriteString(out, b.String()); err != nil {
		return fmt.Errorf("the account was recovered and audited, but this summary could not be written: %w", err)
	}
	return nil
}

func enrolledWord(enrolled bool) string {
	if enrolled {
		return "had an authenticator"
	}
	return "had no authenticator"
}

package identity

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

// The last-administrator guard, tested in isolation.
//
// api.TestTheLastAdministratorCannotBeRemoved proves the HTTP paths out of "this
// instance has a working administrator" are closed. These tests prove the guard
// ITSELF is correct, which is a different claim and one that is not currently
// observable from outside the package: authz.CanModifyUser refuses every route
// to the guard before it can fire, because Admin is the top rank and nobody may
// act on a peer.
//
// That is why these are here rather than folded into the end-to-end suite. When
// role editing lands and an administrator can lower the Admin role's rank, the
// guard becomes the only thing standing between an operator and an instance
// they cannot administer. A guard whose logic was never tested until the day it
// became load-bearing is a guard nobody should trust.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

type adminFixture struct {
	t     *testing.T
	svc   *Service
	store *Store
	roles map[string]authz.Role
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()

	database, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "admin.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}

	keyB64, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipherFromBase64(keyB64)
	if err != nil {
		t.Fatal(err)
	}

	now := func() time.Time { return time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC) }
	store := NewStore(database, cipher, testParams(), now)
	if err := store.EnsureBuiltinRoles(t.Context()); err != nil {
		t.Fatal(err)
	}

	f := &adminFixture{
		t: t, store: store, roles: map[string]authz.Role{},
		svc: NewService(store, audit.New(database, now), Policy{Argon2: testParams()}, now),
	}
	for _, name := range []string{authz.RoleAdmin, authz.RoleManager, authz.RoleUser} {
		role, err := store.RoleByName(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		f.roles[name] = role
	}
	return f
}

// account creates a user in the given role and state. enrolled controls whether
// it has an authenticator, which is the half of "can this account sign in?" that
// the state does not say.
func (f *adminFixture) account(username, roleName string, state authz.UserState, enrolled bool) *User {
	f.t.Helper()
	id, err := f.store.CreateUser(f.t.Context(), NewUser{
		Username: username, Email: username + "@example.com",
		PasswordHash: "$argon2id$stub", RoleID: f.roles[roleName].ID,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if enrolled {
		// Also flips the account to active: this is the only transition out of
		// awaiting_mfa.
		if err := f.store.SetTOTPSecret(f.t.Context(), id, "JBSWY3DPEHPK3PXP"); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := f.store.SetUserState(f.t.Context(), id, state); err != nil {
		f.t.Fatal(err)
	}
	u, err := f.store.UserByID(f.t.Context(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return u
}

// asRank returns a context carrying a principal in a role at the given rank,
// holding every permission.
//
// The rank is a parameter because the scenario the guard exists for does not
// exist yet: it needs an actor who OUTRANKS an administrator, which today is
// nobody. Rather than pretend otherwise, these tests construct that future
// directly and check what the guard does in it.
func asRank(rank int) context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID:   9001,
		Username: "future-superuser",
		State:    authz.StateActive,
		Role: authz.Role{
			ID: 9001, Name: "Superuser", Rank: rank,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...),
		},
		UnrestrictedLibraries: true,
	})
}

// ---------------------------------------------------------------------------
// What counts as an administrator
// ---------------------------------------------------------------------------

// CountActiveAdmins is the guard's only input, so its definition of
// "administrator" is the whole protection. An account that cannot sign in
// cannot rescue an instance, and counting it is precisely how somebody ends up
// locked out with a database row insisting they are fine.
func TestOnlyAnAdministratorWhoCanSignInCounts(t *testing.T) {
	f := newAdminFixture(t)

	cases := []struct {
		what     string
		make     func()
		expected int
	}{
		{"nobody at all", func() {}, 0},
		{"an active, enrolled administrator", func() {
			f.account("real", authz.RoleAdmin, authz.StateActive, true)
		}, 1},
		{"plus a suspended administrator", func() {
			f.account("suspended", authz.RoleAdmin, authz.StateSuspended, true)
		}, 1},
		{"plus an administrator who never enrolled", func() {
			f.account("halfway", authz.RoleAdmin, authz.StateAwaitingMFA, false)
		}, 1},
		{"plus an active, enrolled Manager", func() {
			f.account("morgan", authz.RoleManager, authz.StateActive, true)
		}, 1},
		{"plus a second real administrator", func() {
			f.account("second", authz.RoleAdmin, authz.StateActive, true)
		}, 2},
	}
	for _, tc := range cases {
		tc.make()
		got, err := f.store.CountActiveAdmins(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.expected {
			t.Errorf("with %s: counted %d administrators, want %d",
				tc.what, got, tc.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// The guard
// ---------------------------------------------------------------------------

// TestTheLastAdminGuardBites is the test api.TestTheLastAdministratorCannotBeRemoved
// cites: the guard refuses, on its own, without help from the rank rules.
func TestTheLastAdminGuardBites(t *testing.T) {
	f := newAdminFixture(t)
	sole := f.account("jacob", authz.RoleAdmin, authz.StateActive, true)

	// A rank above Admin, which is the world role editing will create.
	ctx := asRank(authz.RankAdmin + 1)

	err := f.svc.guardLastAdmin(ctx, sole, f.roles[authz.RoleAdmin])
	if !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("removing the only administrator returned %v, want ErrLastAdministrator", err)
	}
	// The message names the account, because an operator reading a 409 needs to
	// know which one is holding things up.
	if got := err.Error(); !strings.Contains(got, "jacob") {
		t.Errorf("the refusal does not say who: %q", got)
	}
}

func TestTheGuardAllowsWhatIsSafe(t *testing.T) {
	f := newAdminFixture(t)
	ctx := asRank(authz.RankAdmin + 1)

	t.Run("a second administrator makes the first removable", func(t *testing.T) {
		g := newAdminFixture(t)
		first := g.account("jacob", authz.RoleAdmin, authz.StateActive, true)
		g.account("backup", authz.RoleAdmin, authz.StateActive, true)

		if err := g.svc.guardLastAdmin(asRank(authz.RankAdmin+1), first,
			g.roles[authz.RoleAdmin]); err != nil {
			t.Errorf("refused with a second administrator present: %v", err)
		}
	})

	t.Run("a non-administrator is never guarded", func(t *testing.T) {
		u := f.account("sam", authz.RoleUser, authz.StateActive, true)
		if err := f.svc.guardLastAdmin(ctx, u, f.roles[authz.RoleUser]); err != nil {
			t.Errorf("guarded an ordinary account: %v", err)
		}
	})

	// The two cases below matter more than they look. An account that cannot
	// sign in is not holding the instance up, so refusing to touch it would
	// make the guard a trap: an operator who suspended their only
	// administrator by other means could then not even tidy up the row.
	t.Run("a suspended administrator is not protected", func(t *testing.T) {
		g := newAdminFixture(t)
		u := g.account("frozen", authz.RoleAdmin, authz.StateSuspended, true)
		if err := g.svc.guardLastAdmin(asRank(authz.RankAdmin+1), u,
			g.roles[authz.RoleAdmin]); err != nil {
			t.Errorf("guarded a suspended administrator: %v", err)
		}
	})

	t.Run("an un-enrolled administrator is not protected", func(t *testing.T) {
		g := newAdminFixture(t)
		u := g.account("halfway", authz.RoleAdmin, authz.StateAwaitingMFA, false)
		if err := g.svc.guardLastAdmin(asRank(authz.RankAdmin+1), u,
			g.roles[authz.RoleAdmin]); err != nil {
			t.Errorf("guarded an administrator who never enrolled: %v", err)
		}
	})
}

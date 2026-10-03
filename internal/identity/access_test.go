package identity

import (
	"errors"
	"slices"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

func (f *adminFixture) root(path, kind string) int64 {
	f.t.Helper()
	res, err := f.store.db.ExecContext(f.t.Context(), `
		INSERT INTO root_folder (path, kind, created_at, updated_at)
		VALUES (?, ?, '2026-09-29T00:00:00Z', '2026-09-29T00:00:00Z')`, path, kind)
	if err != nil {
		f.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (f *adminFixture) principalOf(id int64) *authz.Principal {
	f.t.Helper()
	u, err := f.store.UserByID(f.t.Context(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	p, err := f.store.BuildPrincipal(f.t.Context(), &Session{ID: "s", MFASatisfied: true}, u)
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

// ADR-0037, decisions 1 and 2: a grant names root folders; every account that
// existed before keeps the whole library; a restricted account sees exactly its
// roots, and a forgotten root takes its grant with it.
func TestAGrantNamesRootFolders(t *testing.T) {
	f := newAdminFixture(t)
	ctx := t.Context()
	kids := f.root("/media/kids", "movies")
	films := f.root("/media/films", "movies")

	// An account as it was before migration 0022: no column written, so the
	// default decides — every library.
	res, err := f.store.db.ExecContext(ctx, `
		INSERT INTO app_user (username, email, password_hash, state, role_id, created_at, updated_at)
		VALUES ('old', 'old@example.com', 'x', 'active', ?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		f.roles[authz.RoleUser].ID)
	if err != nil {
		t.Fatal(err)
	}
	oldID, _ := res.LastInsertId()
	if p := f.principalOf(oldID); !p.UnrestrictedLibraries || !authz.ScopeFor(p).AllLibraries {
		t.Errorf("an existing account lost the library: %+v", p)
	}

	id, err := f.store.CreateUser(ctx, NewUser{Username: "kid", Email: "kid@example.com",
		PasswordHash: "x", RoleID: f.roles[authz.RoleUser].ID, LibraryIDs: []int64{kids},
		RatingCeiling: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetUserState(ctx, id, authz.StateActive); err != nil {
		t.Fatal(err)
	}
	p := f.principalOf(id)
	scope := authz.ScopeFor(p)
	if p.UnrestrictedLibraries || !scope.AllowsLibrary(kids) || scope.AllowsLibrary(films) ||
		scope.RatingCeiling != 2 {
		t.Errorf("a restricted account's scope is %+v", scope)
	}
	if g, err := f.store.Access(ctx, id); err != nil || g.AllLibraries ||
		!slices.Equal(g.RootFolderIDs, []int64{kids}) || g.RatingCeiling != 2 {
		t.Errorf("access read back as %+v, %v", g, err)
	}

	if _, err := f.store.CreateUser(ctx, NewUser{Username: "ghost", Email: "ghost@example.com",
		PasswordHash: "x", RoleID: f.roles[authz.RoleUser].ID, LibraryIDs: []int64{999}}); !errors.Is(err, ErrNoSuchRootFolder) {
		t.Errorf("a grant of a root that does not exist: %v", err)
	}

	// Forgetting the root forgets the grant; the account then sees nothing.
	if _, err := f.store.db.ExecContext(ctx, `DELETE FROM root_folder WHERE id = ?`, kids); err != nil {
		t.Fatal(err)
	}
	if s := authz.ScopeFor(f.principalOf(id)); !s.MatchesNothing() {
		t.Errorf("an account whose only root was forgotten still sees %+v", s)
	}

	// What an approver sends, read.
	yes, no := true, false
	if g, err := NormaliseGrant(nil, nil, 0); err != nil || !g.AllLibraries {
		t.Errorf("no choice at all is every library: %+v %v", g, err)
	}
	if g, err := NormaliseGrant(&yes, []int64{films}, 0); err != nil || !g.AllLibraries || g.RootFolderIDs != nil {
		t.Errorf("all_libraries true: %+v %v", g, err)
	}
	if g, err := NormaliseGrant(nil, []int64{films, films}, 3); err != nil || g.AllLibraries ||
		!slices.Equal(g.RootFolderIDs, []int64{films}) {
		t.Errorf("a list: %+v %v", g, err)
	}
	if _, err := NormaliseGrant(&no, nil, 0); !errors.Is(err, ErrNoLibraries) {
		t.Errorf("restricted to nothing: %v", err)
	}
	if _, err := NormaliseGrant(nil, nil, authz.MaxRatingRank+1); err == nil {
		t.Error("a ceiling above the highest rank was accepted")
	}
}

package identity

import (
	"errors"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// ADR-0039: an edit lasts past the seed that runs at every start; Admin does
// not; putting the defaults back hands the role to the seed again.
func TestAnEditedRoleSurvivesTheSeed(t *testing.T) {
	f := newAdminFixture(t)
	ctx := authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 9001, Username: "future-superuser",
		State: authz.StateActive, MFASatisfied: true, UnrestrictedLibraries: true,
		Role: authz.Role{ID: 9001, Name: "Superuser", Rank: authz.RankAdmin + 1,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)}})
	user := f.roles[authz.RoleUser]

	want := []authz.Permission{authz.PermLogin, authz.PermBrowse, authz.PermStream,
		authz.PermSubmitRequest, authz.PermDownloadOriginal}
	if _, _, err := f.svc.EditRole(ctx, user.ID, want, false, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.store.EnsureBuiltinRoles(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, _ := f.store.RoleByID(t.Context(), user.ID)
	if !got.Permissions.Has(authz.PermDownloadOriginal) || len(got.Permissions) != len(want) {
		t.Errorf("after the seed the edited role holds %v", got.Permissions.Slice())
	}

	// Admin is the seed's whatever happens to its rows.
	admin := f.roles[authz.RoleAdmin]
	if _, err := f.store.db.ExecContext(t.Context(),
		`DELETE FROM role_permission WHERE role_id = ? AND permission = 'admin.users'`, admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(t.Context(),
		`UPDATE role SET permissions_chosen_at = '2026-01-01T00:00:00Z' WHERE id = ?`, admin.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.EnsureBuiltinRoles(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.store.RoleByID(t.Context(), admin.ID); !got.Permissions.Has(authz.PermManageUsers) {
		t.Error("the seed left Admin without admin.users")
	}

	if _, _, err := f.svc.EditRole(ctx, user.ID, nil, true, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.db.ExecContext(t.Context(),
		`INSERT INTO role_permission (role_id, permission) VALUES (?, 'library.edit')`, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.EnsureBuiltinRoles(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.store.RoleByID(t.Context(), user.ID); got.Permissions.Has(authz.PermEditLibraryItems) ||
		got.Permissions.Has(authz.PermDownloadOriginal) {
		t.Errorf("a role back on its defaults was not reseeded: %v", got.Permissions.Slice())
	}

	if _, _, err := f.svc.EditRole(ctx, admin.ID, want, false, "", ""); !errors.Is(err, ErrRoleNotEditable) {
		t.Errorf("Admin edited: %v", err)
	}

	// Only a role below the actor's: a Manager-ranked actor holding
	// admin.users may not edit Manager.
	peer := authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 9002, Username: "manager",
		State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 9002, Name: "Manager", Rank: authz.RankManager,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)}})
	manager := f.roles[authz.RoleManager]
	if _, _, err := f.svc.EditRole(peer, manager.ID, []authz.Permission{authz.PermLogin}, false, "", ""); !authz.IsDenied(err) {
		t.Errorf("a peer edited its own rank's role: %v", err)
	}
	if _, _, err := f.svc.EditRole(peer, user.ID, []authz.Permission{authz.PermLogin, authz.PermBrowse}, false, "", ""); err != nil {
		t.Errorf("a Manager could not edit the role below it: %v", err)
	}
}

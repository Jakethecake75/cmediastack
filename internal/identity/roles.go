package identity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// Editing a role's permissions (ADR-0039, decision 2).

var (
	// ErrRoleNotEditable refuses an edit of the Admin role.
	ErrRoleNotEditable = errors.New("identity: the Admin role holds every permission and is not editable")
	// ErrPermissionReserved refuses granting an Admin-only permission below
	// Admin.
	ErrPermissionReserved = errors.New("identity: admin.system, admin.users and admin.network stay the Admin role's")
	// ErrLoginRequired refuses removing auth.login from a role.
	ErrLoginRequired = errors.New("identity: a role must keep auth.login; suspend an account to stop it signing in")
	// ErrUnknownPermission refuses a permission that does not exist.
	ErrUnknownPermission = errors.New("identity: no such permission")
)

// ReservedForAdmin are the permissions no role below Admin may hold: each is
// instance-wide trust, and holding one would make a second administrator in all
// but name.
var ReservedForAdmin = []authz.Permission{
	authz.PermSystemSettings, authz.PermManageUsers, authz.PermManageNetwork,
}

// RoleSummary is a role as the roles screen shows it.
type RoleSummary struct {
	authz.Role
	// Defaults is whether its permissions are the built-in ones.
	Defaults bool
	// Holders is how many accounts hold it.
	Holders int
}

// builtinDefaults returns a built-in role's seeded permissions, by name.
func builtinDefaults(name string) (authz.PermissionSet, bool) {
	for _, r := range authz.BuiltinRoles() {
		if r.Name == name {
			return r.Permissions, true
		}
	}
	return nil, false
}

// chosen reports whether a person chose this role's permissions.
func roleChosen(ctx context.Context, q db.Execer, roleID int64) (bool, error) {
	var at sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT permissions_chosen_at FROM role WHERE id = ?`, roleID).
		Scan(&at); err != nil {
		return false, err
	}
	return at.Valid, nil
}

// RoleSummaries lists every role with its holders.
func (svc *Service) RoleSummaries(ctx context.Context) ([]RoleSummary, error) {
	if err := authz.RequirePermission(ctx, authz.PermManageUsers); err != nil {
		return nil, err
	}
	roles, err := svc.store.ListRoles(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RoleSummary, 0, len(roles))
	for _, r := range roles {
		sum := RoleSummary{Role: r}
		chosen, err := roleChosen(ctx, svc.store.db, r.ID)
		if err != nil {
			return nil, err
		}
		sum.Defaults = !chosen
		if err := svc.store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM app_user WHERE role_id = ?`, r.ID).Scan(&sum.Holders); err != nil {
			return nil, err
		}
		out = append(out, sum)
	}
	return out, nil
}

// EditRole replaces a role's permissions, or with defaults puts back its
// built-in ones. It returns the role before and after.
func (svc *Service) EditRole(ctx context.Context, roleID int64, perms []authz.Permission,
	defaults bool, sourceIP, userAgent string) (authz.Role, authz.Role, error) {

	actor := authz.FromContext(ctx)
	if err := authz.RequirePermission(ctx, authz.PermManageUsers); err != nil {
		return authz.Role{}, authz.Role{}, err
	}
	before, err := svc.store.RoleByID(ctx, roleID)
	if err != nil {
		return authz.Role{}, authz.Role{}, err
	}
	if before.Name == authz.RoleAdmin || before.Rank >= authz.RankAdmin {
		return authz.Role{}, authz.Role{}, ErrRoleNotEditable
	}
	if before.Rank >= actor.Role.Rank {
		d := &authz.Denial{Actor: actor.UserID, Reason: authz.ReasonEscalation,
			Detail: fmt.Sprintf("role %q is not below the actor's", before.Name)}
		svc.audit.AuthzDenied(ctx, "role.edit", d, sourceIP, userAgent)
		return authz.Role{}, authz.Role{}, d
	}

	want := authz.PermissionSet{}
	if defaults {
		set, ok := builtinDefaults(before.Name)
		if !ok {
			return authz.Role{}, authz.Role{}, fmt.Errorf("identity: role %q has no built-in defaults", before.Name)
		}
		want = set.Clone()
	} else {
		for _, p := range perms {
			if !slices.Contains(authz.AllPermissions, p) {
				return authz.Role{}, authz.Role{}, fmt.Errorf("%w: %q", ErrUnknownPermission, p)
			}
			want[p] = struct{}{}
		}
		for _, p := range ReservedForAdmin {
			if want.Has(p) {
				return authz.Role{}, authz.Role{}, fmt.Errorf("%w (%s)", ErrPermissionReserved, p)
			}
		}
		if !want.Has(authz.PermLogin) {
			return authz.Role{}, authz.Role{}, ErrLoginRequired
		}
	}
	if err := authz.CanGrantPermissions(ctx, want); err != nil {
		if d, ok := authz.AsDenial(err); ok {
			svc.audit.AuthzDenied(ctx, "role.edit", d, sourceIP, userAgent)
		}
		return authz.Role{}, authz.Role{}, err
	}

	err = svc.store.db.InTx(ctx, func(tx db.Execer) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM role_permission WHERE role_id = ?`, roleID); err != nil {
			return err
		}
		for _, p := range want.Slice() {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO role_permission (role_id, permission) VALUES (?, ?)`, roleID, string(p)); err != nil {
				return err
			}
		}
		var chosenAt any
		if !defaults {
			chosenAt = ts(svc.now())
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE role SET permissions_chosen_at = ?, updated_at = ? WHERE id = ?`,
			chosenAt, ts(svc.now()), roleID)
		return err
	})
	if err != nil {
		return authz.Role{}, authz.Role{}, fmt.Errorf("identity: editing role %q: %w", before.Name, err)
	}
	after, err := svc.store.RoleByID(ctx, roleID)
	if err != nil {
		return authz.Role{}, authz.Role{}, err
	}

	added, removed := permissionDiff(before.Permissions, after.Permissions)
	var parts []string
	if defaults {
		parts = append(parts, "put back to the built-in permissions")
	}
	if len(added) > 0 {
		parts = append(parts, "added "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "removed "+strings.Join(removed, ", "))
	}
	if len(parts) == 0 {
		parts = append(parts, "unchanged")
	}
	detail := before.Name + ": " + strings.Join(parts, "; ")
	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &actor.UserID, ActorLabel: actor.Username,
		Action: audit.ActionRolePermissionsChanged, TargetKind: "role",
		TargetID: fmt.Sprintf("%d", roleID), SourceIP: sourceIP, UserAgent: userAgent,
		Before: permissionStrings(before.Permissions), After: permissionStrings(after.Permissions),
		Detail: detail,
	})
	return before, after, nil
}

func permissionStrings(set authz.PermissionSet) []string {
	out := make([]string, 0, len(set))
	for _, p := range set.Slice() {
		out = append(out, string(p))
	}
	slices.Sort(out)
	return out
}

func permissionDiff(before, after authz.PermissionSet) (added, removed []string) {
	for _, p := range after.Slice() {
		if !before.Has(p) {
			added = append(added, string(p))
		}
	}
	for _, p := range before.Slice() {
		if !after.Has(p) {
			removed = append(removed, string(p))
		}
	}
	slices.Sort(added)
	slices.Sort(removed)
	return added, removed
}

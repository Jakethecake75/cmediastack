package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// Administering other people's accounts.
//
// # What already protects this, and what does not
//
// authz.CanModifyUser carries most of the weight and is used rather than
// re-implemented here: it refuses an account modifying itself, and it refuses
// acting on a peer or a superior. That second rule is why suspension is not an
// escalation route — without it a Manager could suspend an Admin, and
// escalation by denial works as well as escalation by permission.
//
// Those rules have a consequence worth stating, because it is the reason a
// guard below looks redundant: **today, an instance cannot be locked out of its
// own administration through these endpoints.** Admin is the top rank, nobody
// may act on an equal, and nobody may act on themselves — so the last
// administrator cannot be suspended or demoted by anybody, including
// themselves. api.TestTheLastAdministratorCannotBeRemoved asserts that, and
// asserts it against the paths rather than the intention.
//
// The guard is here anyway, and the reason is specific rather than defensive
// habit: role EDITING is not built (PATCH /api/v1/admin/roles/{id} is still
// unimplemented). The moment it is, an administrator can remove
// admin.system from the Admin role, or lower its rank, and every protection in
// the paragraph above evaporates at once. The guard costs one query and will be
// load-bearing then.
//
// # Why it is called from two places, not one
//
// It was called from ChangeUserRole alone until a mutation test disabled the
// two authz rank rules — simulating exactly the world role editing creates —
// and watched a Manager suspend the only administrator: 200 OK, zero
// administrators left, instance unadministrable. Demotion was refused; both
// suspension paths were not. A guard that covers one route to an outcome and
// not the other is not a guard, so Service.SuspendUser consults it too.
//
// That mutation is the check that matters for anything added here later: with
// authz.CanModifyUser's self and rank rules disabled, the end-to-end lockout
// test must still pass. TestTheLastAdminGuardBites covers the guard's own logic
// in isolation, because today nothing else can reach it.

// Errors the administration path distinguishes.
var (
	ErrLastAdministrator = errors.New("identity: that would leave the instance with no administrator who can sign in")
)

// ListUsers returns every account for the administration screen.
//
// Deliberately unfiltered by state: an administrator on this screen most often
// wants the suspended ones, because reactivating somebody is the commonest
// reason to be here.
func (svc *Service) ListUsers(ctx context.Context) ([]*User, error) {
	if err := authz.RequirePermission(ctx, authz.PermManageUsers); err != nil {
		return nil, err
	}
	return svc.store.ListUsers(ctx, 500)
}

// denied records a refused authorization, if that is what the error is.
//
// §8 asks for authz denials in the audit log, and this is the category that
// most needs to be there: somebody probing whether they can promote themselves
// looks exactly like somebody who mis-clicked, and the only way to tell is to
// have both written down. The alternative — logging refusals at the route
// layer only — misses every denial decided inside a service, which is all of
// the interesting ones, because reaching the route already required the
// permission.
func (svc *Service) denied(ctx context.Context, route string, err error, sourceIP, userAgent string) {
	if d, ok := authz.AsDenial(err); ok {
		svc.audit.AuthzDenied(ctx, route, d, sourceIP, userAgent)
	}
}

// modifiable loads a user and proves the actor may act on them.
//
// sourceIP and userAgent are carried as parameters rather than read from the
// context: the resolved client address lives in a context key private to the
// HTTP layer (api.ClientIP), and importing that here would be a cycle. Passing
// them explicitly also means a caller with no request behind it — a scheduled
// task — cannot accidentally record a blank address as though it were one.
func (svc *Service) modifiable(ctx context.Context, userID int64, route, sourceIP, userAgent string) (*User, authz.Role, *authz.Principal, error) {
	actor := authz.FromContext(ctx)
	target, err := svc.store.UserByID(ctx, userID)
	if err != nil {
		return nil, authz.Role{}, nil, err
	}
	role, err := svc.store.RoleByID(ctx, target.RoleID)
	if err != nil {
		return nil, authz.Role{}, nil, err
	}
	if err := authz.CanModifyUser(ctx, authz.TargetUser{UserID: target.ID, Role: role}); err != nil {
		svc.denied(ctx, route, err, sourceIP, userAgent)
		return nil, authz.Role{}, nil, err
	}
	return target, role, actor, nil
}

// guardLastAdmin refuses a change that would leave nobody able to administer.
//
// Asked BEFORE the change, of the account the change is about: "is this one of
// the administrators who can actually sign in, and is it the only one?" Asking
// afterwards would mean undoing a change in a state where the account that
// could undo it no longer exists.
//
// "Can actually sign in" is the part that matters. An account that is suspended
// or never enrolled an authenticator cannot rescue anything, and counting it is
// how somebody ends up locked out of their own server with a row in the
// database insisting otherwise.
func (svc *Service) guardLastAdmin(ctx context.Context, target *User, role authz.Role) error {
	if !role.Permissions.Has(authz.PermSystemSettings) {
		return nil // not an administrator; changing them locks nobody out
	}
	if target.State != authz.StateActive || !target.Enrolled() {
		return nil // already unable to sign in, so it is not holding anything up
	}

	n, err := svc.store.CountActiveAdmins(ctx)
	if err != nil {
		return err
	}
	if n <= 1 {
		return fmt.Errorf("%w: %q is the only administrator who can sign in",
			ErrLastAdministrator, target.Username)
	}
	return nil
}

// ReactivateUser lets a suspended account act again.
//
// It does NOT reset or bypass the second factor. An account that never enrolled
// returns to awaiting_mfa, which is exactly where it was: reactivation restores
// the access somebody had, it does not grant access they never established.
func (svc *Service) ReactivateUser(ctx context.Context, userID int64, sourceIP, userAgent string) error {
	if err := authz.RequirePermission(ctx, authz.PermSuspendUser); err != nil {
		svc.denied(ctx, "user.reactivate", err, sourceIP, userAgent)
		return err
	}
	target, _, actor, err := svc.modifiable(ctx, userID, "user.reactivate", sourceIP, userAgent)
	if err != nil {
		return err
	}

	next := authz.StateActive
	if !target.Enrolled() {
		next = authz.StateAwaitingMFA
	}
	if err := svc.store.SetUserState(ctx, userID, next); err != nil {
		return err
	}

	return svc.audit.Write(ctx, audit.Event{
		ActorUserID: &actor.UserID,
		ActorLabel:  actor.Username,
		Action:      audit.ActionUserReactivated,
		TargetKind:  "user",
		TargetID:    fmt.Sprintf("%d", userID),
		SourceIP:    sourceIP,
		UserAgent:   userAgent,
		Detail:      target.Username,
		Before:      map[string]any{"state": string(target.State)},
		After:       map[string]any{"state": string(next)},
	})
}

// ChangeUserRole moves an account to a different role.
//
// Both ends are checked, and they are different checks. The account's CURRENT
// role must be below the actor's (authz.CanModifyUser) — otherwise demoting a
// superior would neutralise them. The TARGET role must be one the actor could
// assign (authz.CanAssignRole: strictly downward, and never granting a
// permission the actor does not themselves hold) — otherwise promoting somebody
// else is a way to acquire authority by proxy.
func (svc *Service) ChangeUserRole(ctx context.Context, userID, roleID int64, sourceIP, userAgent string) error {
	if err := authz.RequirePermission(ctx, authz.PermManageUsers); err != nil {
		svc.denied(ctx, "user.role.change", err, sourceIP, userAgent)
		return err
	}
	target, currentRole, actor, err := svc.modifiable(ctx, userID, "user.role.change", sourceIP, userAgent)
	if err != nil {
		return err
	}

	role, err := svc.store.RoleByID(ctx, roleID)
	if err != nil {
		return err
	}
	// A separate denial from the one above, and worth its own record: this is
	// the actor reaching UPWARD rather than sideways. "Tried to grant a role
	// they do not hold" is the single most interesting line in an audit log.
	if err := authz.CanAssignRole(ctx, role); err != nil {
		svc.denied(ctx, "user.role.assign", err, sourceIP, userAgent)
		return err
	}
	if role.ID == target.RoleID {
		return nil // nothing changed, and nothing worth an audit line
	}

	// Only when the change REMOVES administration. Moving an administrator
	// between two administrating roles locks nobody out.
	if !role.Permissions.Has(authz.PermSystemSettings) {
		if err := svc.guardLastAdmin(ctx, target, currentRole); err != nil {
			return err
		}
	}

	if err := svc.store.SetUserRole(ctx, userID, roleID); err != nil {
		return err
	}

	// A role change alters what every existing session of that account may do.
	// Sessions are server-side records re-read on each request (ADR-9), so the
	// new role applies immediately without revoking anything — which is the
	// point of not using self-contained tokens.
	return svc.audit.Write(ctx, audit.Event{
		ActorUserID: &actor.UserID,
		ActorLabel:  actor.Username,
		Action:      audit.ActionRoleChanged,
		TargetKind:  "user",
		TargetID:    fmt.Sprintf("%d", userID),
		SourceIP:    sourceIP,
		UserAgent:   userAgent,
		Detail:      target.Username,
		Before:      map[string]any{"role": target.RoleName, "rank": target.RoleRank},
		After:       map[string]any{"role": role.Name, "rank": role.Rank},
	})
}

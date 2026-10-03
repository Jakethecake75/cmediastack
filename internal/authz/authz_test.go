package authz

import (
	"context"
	"testing"
)

// This file holds the negative tests for Phase 1 acceptance criteria (c) and
// (d): a low-privilege user cannot reach any admin capability by any path, and
// a Manager cannot approve an account into Admin or escalate their own
// permissions.
//
// Every test here asserts that an attack FAILS.

func roleByName(name string) Role {
	for _, r := range BuiltinRoles() {
		if r.Name == name {
			return r
		}
	}
	panic("unknown role " + name)
}

// principal builds an active, MFA-satisfied principal in the named role.
func principal(id int64, roleName string, libraries ...int64) *Principal {
	r := roleByName(roleName)
	return &Principal{
		UserID:                id,
		Username:              roleName + "-user",
		Role:                  r,
		State:                 StateActive,
		SessionID:             "sess-test",
		LibraryIDs:            libraries,
		UnrestrictedLibraries: r.Permissions.Has(PermSystemSettings),
		MFASatisfied:          true,
	}
}

func ctxFor(p *Principal) context.Context {
	return WithPrincipal(context.Background(), p)
}

func assertDenied(t *testing.T, err error, wantReason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected denial with reason %q, got nil (THE ATTACK SUCCEEDED)", wantReason)
	}
	d, ok := AsDenial(err)
	if !ok {
		t.Fatalf("expected *Denial, got %T: %v", err, err)
	}
	if d.Reason != wantReason {
		t.Errorf("denial reason = %q, want %q (detail: %s)", d.Reason, wantReason, d.Detail)
	}
}

func assertAllowed(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("expected the operation to be allowed, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Anonymous
// ---------------------------------------------------------------------------

func TestAnonymousIsDeniedEverything(t *testing.T) {
	ctx := context.Background() // no principal at all

	for _, perm := range AllPermissions {
		if err := RequirePermission(ctx, perm); err == nil {
			t.Fatalf("anonymous was granted %q", perm)
		}
	}
	for _, eff := range AllEffects() {
		if err := RequireEffect(ctx, eff, "target"); err == nil {
			t.Fatalf("anonymous was granted effect %q", eff)
		}
	}
	assertDenied(t, RequireLibraryAccess(ctx, 1), ReasonAnonymous)
	assertDenied(t, CanAssignRole(ctx, roleByName(RoleUser)), ReasonAnonymous)
}

func TestAnonymousScopeMatchesNothing(t *testing.T) {
	s := ScopeFromContext(context.Background())
	if !s.MatchesNothing() {
		t.Fatal("anonymous scope must match no rows")
	}
	if s.AllLibraries {
		t.Fatal("anonymous scope must not be unrestricted")
	}
	if s.AllowsLibrary(1) {
		t.Fatal("anonymous scope allowed a library")
	}
}

// A nil principal must not be coercible into a working one by any exported
// accessor. This guards the "no default/anonymous principal" invariant.
func TestNilPrincipalHasNothing(t *testing.T) {
	var p *Principal
	if p.CanAct() {
		t.Fatal("nil principal reported CanAct")
	}
	if p.IsAdmin() {
		t.Fatal("nil principal reported IsAdmin")
	}
	for _, perm := range AllPermissions {
		if p.Has(perm) {
			t.Fatalf("nil principal held %q", perm)
		}
	}
}

// ---------------------------------------------------------------------------
// Account lifecycle states
// ---------------------------------------------------------------------------

func TestNonActiveStatesCannotAct(t *testing.T) {
	cases := []struct {
		state      UserState
		mfa        bool
		wantReason string
	}{
		{StateAwaitingMFA, true, ReasonMFARequired},
		{StateSuspended, true, ReasonNotActive},
		{StateDisabled, true, ReasonNotActive},
		{StateActive, false, ReasonMFARequired}, // active but session lacks a second factor
	}

	for _, tc := range cases {
		p := principal(7, RoleUser, 1)
		p.State = tc.state
		p.MFASatisfied = tc.mfa
		ctx := ctxFor(p)

		assertDenied(t, RequirePermission(ctx, PermBrowse), tc.wantReason)
		assertDenied(t, RequireLibraryAccess(ctx, 1), tc.wantReason)

		if ScopeFor(p).MatchesNothing() != true {
			t.Errorf("state %q with mfa=%v produced a scope that matches rows", tc.state, tc.mfa)
		}
	}
}

// A suspended Admin is not an Admin.
func TestSuspendedAdminHoldsNothing(t *testing.T) {
	p := principal(1, RoleAdmin)
	p.State = StateSuspended
	if p.IsAdmin() {
		t.Fatal("suspended admin still reported IsAdmin")
	}
	assertDenied(t, RequireEffect(ctxFor(p), EffectDestroyMediaBytes, "/media/x.mkv"), ReasonNotActive)
}

// ---------------------------------------------------------------------------
// Acceptance criterion (c): low-privilege user cannot reach admin capability
// ---------------------------------------------------------------------------

func TestUserCannotReachAnyAdminPermission(t *testing.T) {
	ctx := ctxFor(principal(20, RoleUser, 1))

	adminOnly := []Permission{
		PermDeleteMediaFiles, PermManageRootFolders, PermManageIndexers,
		PermManageNetwork, PermManageUsers, PermViewAuditLog, PermSystemSettings,
		PermApproveAccounts, PermIssueInvites, PermSetGrantsAtApproval,
		PermSuspendUser, PermViewOtherActivity, PermApproveRequests,
		PermInteractiveSearch, PermManageQueue, PermEditLibraryItems,
	}
	for _, perm := range adminOnly {
		assertDenied(t, RequirePermission(ctx, perm), ReasonMissingPerm)
	}
}

func TestUserCannotReachAnyEffect(t *testing.T) {
	ctx := ctxFor(principal(20, RoleUser, 1))
	for _, eff := range AllEffects() {
		assertDenied(t, RequireEffect(ctx, eff, "target"), ReasonMissingPerm)
	}
}

func TestUserCannotReadUngrantedLibrary(t *testing.T) {
	ctx := ctxFor(principal(20, RoleUser, 1, 2))

	assertAllowed(t, RequireLibraryAccess(ctx, 1))
	assertAllowed(t, RequireLibraryAccess(ctx, 2))
	assertDenied(t, RequireLibraryAccess(ctx, 3), ReasonOutOfScope)
	assertDenied(t, RequireLibraryAccess(ctx, 99), ReasonOutOfScope)
}

func TestRatingCeilingIsEnforcedInScope(t *testing.T) {
	p := principal(21, RoleUser, 1)
	p.RatingCeiling = 3 // e.g. PG-13
	s := ScopeFor(p)

	if !s.AllowsRating(2) || !s.AllowsRating(3) {
		t.Error("content at or below the ceiling should be visible")
	}
	if s.AllowsRating(4) || s.AllowsRating(5) {
		t.Error("content above the ceiling must not be visible")
	}
}

// ---------------------------------------------------------------------------
// Acceptance criterion (d): Manager cannot escalate
// ---------------------------------------------------------------------------

func TestManagerCannotAssignAdminRole(t *testing.T) {
	ctx := ctxFor(principal(10, RoleManager))
	assertDenied(t, CanAssignRole(ctx, roleByName(RoleAdmin)), ReasonEscalation)
}

func TestManagerCannotAssignManagerRole(t *testing.T) {
	// "at or above" — a Manager may not clone themselves.
	ctx := ctxFor(principal(10, RoleManager))
	assertDenied(t, CanAssignRole(ctx, roleByName(RoleManager)), ReasonEscalation)
}

func TestManagerCanAssignUserRole(t *testing.T) {
	ctx := ctxFor(principal(10, RoleManager))
	assertAllowed(t, CanAssignRole(ctx, roleByName(RoleUser)))
}

func TestManagerCannotGrantUnheldPermissions(t *testing.T) {
	ctx := ctxFor(principal(10, RoleManager))

	// A custom role that looks harmless but carries one permission the
	// Manager does not hold.
	sneaky := Role{
		Name: "Helper", Rank: RankUser,
		Permissions: NewPermissionSet(PermLogin, PermBrowse, PermManageIndexers),
	}
	assertDenied(t, CanAssignRole(ctx, sneaky), ReasonEscalation)

	assertDenied(t, CanGrantPermissions(ctx, NewPermissionSet(PermViewAuditLog)), ReasonEscalation)
	assertDenied(t, CanGrantPermissions(ctx, NewPermissionSet(PermBrowse, PermDeleteMediaFiles)), ReasonEscalation)

	// Granting a strict subset of what the Manager holds is fine.
	assertAllowed(t, CanGrantPermissions(ctx, NewPermissionSet(PermBrowse, PermStream)))
}

func TestManagerCannotModifyAdminOrPeer(t *testing.T) {
	ctx := ctxFor(principal(10, RoleManager))

	assertDenied(t, CanModifyUser(ctx, TargetUser{UserID: 1, Role: roleByName(RoleAdmin)}), ReasonEscalation)
	assertDenied(t, CanModifyUser(ctx, TargetUser{UserID: 11, Role: roleByName(RoleManager)}), ReasonEscalation)
	assertAllowed(t, CanModifyUser(ctx, TargetUser{UserID: 20, Role: roleByName(RoleUser)}))
}

func TestNobodyCanModifyTheirOwnRole(t *testing.T) {
	// Including an Admin: self-demotion is how instances lock themselves out.
	for _, roleName := range []string{RoleAdmin, RoleManager} {
		p := principal(42, roleName)
		err := CanModifyUser(ctxFor(p), TargetUser{UserID: 42, Role: p.Role})
		assertDenied(t, err, ReasonSelfRoleChange)
	}
}

func TestUserCannotAssignAnyRole(t *testing.T) {
	ctx := ctxFor(principal(20, RoleUser, 1))
	for _, r := range BuiltinRoles() {
		assertDenied(t, CanAssignRole(ctx, r), ReasonMissingPerm)
	}
}

// ---------------------------------------------------------------------------
// Effect-based permissions: the privilege-laundering fix
// ---------------------------------------------------------------------------

// A Manager holds PermManageQueue, so they may operate the download queue. The
// queue offers "remove torrent and delete data". That reaches the same unlink
// as media deletion, which is Admin-only. RequireEffect is what stops it.
func TestManagerWithQueueAccessCannotDestroyMediaBytes(t *testing.T) {
	ctx := ctxFor(principal(10, RoleManager))

	assertAllowed(t, RequirePermission(ctx, PermManageQueue))
	assertDenied(t, RequireEffect(ctx, EffectDestroyMediaBytes, "/media/movies/x.mkv"), ReasonMissingPerm)
}

// Same laundering path via library edits and root-folder repointing.
func TestManagerWithEditAccessCannotMutateLibraryPaths(t *testing.T) {
	ctx := ctxFor(principal(10, RoleManager))

	assertAllowed(t, RequirePermission(ctx, PermEditLibraryItems))
	assertDenied(t, RequireEffect(ctx, EffectMutateLibraryPaths, "root:/media/movies"), ReasonMissingPerm)
	assertDenied(t, RequireEffect(ctx, EffectEgressConfig, "profile:download"), ReasonMissingPerm)
	assertDenied(t, RequireEffect(ctx, EffectReadSecret, "indexer:3"), ReasonMissingPerm)
}

func TestAdminCanPerformEveryEffect(t *testing.T) {
	ctx := ctxFor(principal(1, RoleAdmin))
	for _, eff := range AllEffects() {
		assertAllowed(t, RequireEffect(ctx, eff, "target"))
	}
}

// An effect with no permission mapping must fail closed, not open. This is the
// test that protects future contributors: adding an Effect constant without
// adding it to effectRequires denies rather than allows.
func TestUnmappedEffectFailsClosed(t *testing.T) {
	ctx := ctxFor(principal(1, RoleAdmin))
	assertDenied(t, RequireEffect(ctx, Effect("effect_that_does_not_exist"), "x"), ReasonUnknownEffect)
}

func TestEveryDeclaredEffectIsMapped(t *testing.T) {
	declared := []Effect{
		EffectDestroyMediaBytes, EffectMutateLibraryPaths,
		EffectEgressConfig, EffectGrantAccess, EffectReadSecret,
	}
	for _, eff := range declared {
		if _, ok := PermissionForEffect(eff); !ok {
			t.Errorf("effect %q has no permission mapping", eff)
		}
	}
	if len(declared) != len(AllEffects()) {
		t.Errorf("AllEffects has %d entries, this test knows about %d — update the test when adding an effect",
			len(AllEffects()), len(declared))
	}
}

// ---------------------------------------------------------------------------
// Role definitions
// ---------------------------------------------------------------------------

func TestBuiltinRolePermissionsMatchTheMatrix(t *testing.T) {
	admin := roleByName(RoleAdmin)
	manager := roleByName(RoleManager)
	user := roleByName(RoleUser)

	if len(admin.Permissions) != len(AllPermissions) {
		t.Errorf("Admin should hold every permission: has %d of %d",
			len(admin.Permissions), len(AllPermissions))
	}

	// Requirements §7: these are Admin-only rows in the matrix.
	adminOnly := []Permission{
		PermDeleteMediaFiles, PermManageIndexers, PermManageNetwork,
		PermManageUsers, PermViewAuditLog, PermSystemSettings, PermManageRootFolders,
	}
	for _, perm := range adminOnly {
		if manager.Permissions.Has(perm) {
			t.Errorf("Manager must not hold admin-only permission %q", perm)
		}
		if user.Permissions.Has(perm) {
			t.Errorf("User must not hold admin-only permission %q", perm)
		}
	}

	// Manager must be a strict subset of Admin, and User of Manager.
	if !manager.Permissions.IsSubsetOf(admin.Permissions) {
		t.Error("Manager holds a permission Admin does not")
	}
	if !user.Permissions.IsSubsetOf(manager.Permissions) {
		t.Error("User holds a permission Manager does not")
	}

	if admin.Rank <= manager.Rank || manager.Rank <= user.Rank {
		t.Error("role ranks must be strictly ordered Admin > Manager > User")
	}
}

func TestAdminScopeSeesEveryLibrary(t *testing.T) {
	s := ScopeFor(principal(1, RoleAdmin))
	if !s.AllLibraries {
		t.Fatal("admin scope should be unrestricted")
	}
	if s.MatchesNothing() {
		t.Fatal("admin scope matched nothing")
	}
	if !s.AllowsLibrary(12345) {
		t.Fatal("admin scope refused a library")
	}
}

func TestPermissionSetSubsetSemantics(t *testing.T) {
	a := NewPermissionSet(PermBrowse, PermStream)
	b := NewPermissionSet(PermBrowse, PermStream, PermSubmitRequest)

	if !a.IsSubsetOf(b) {
		t.Error("a should be a subset of b")
	}
	if b.IsSubsetOf(a) {
		t.Error("b must not be a subset of a")
	}
	if !NewPermissionSet().IsSubsetOf(a) {
		t.Error("the empty set is a subset of everything")
	}

	// Clone must not alias.
	c := a.Clone()
	c[PermManageUsers] = struct{}{}
	if a.Has(PermManageUsers) {
		t.Error("Clone aliased the original set")
	}
}

// ADR-0037, decision 5: approving an account must not be a way for a
// restricted approver to create a less restricted one.
func TestAGrantIsNoWiderThanTheGrantors(t *testing.T) {
	restricted := principal(2, RoleManager, 1, 2)
	restricted.RatingCeiling = 3
	ctx := ctxFor(restricted)

	assertAllowed(t, CanGrant(ctx, Grant{RootFolderIDs: []int64{1}, RatingCeiling: 3}))
	assertAllowed(t, CanGrant(ctx, Grant{RootFolderIDs: []int64{1, 2}, RatingCeiling: 1}))
	assertDenied(t, CanGrant(ctx, Grant{AllLibraries: true, RatingCeiling: 3}), ReasonEscalation)
	assertDenied(t, CanGrant(ctx, Grant{RootFolderIDs: []int64{1, 3}, RatingCeiling: 3}), ReasonEscalation)
	assertDenied(t, CanGrant(ctx, Grant{RootFolderIDs: []int64{1}, RatingCeiling: 4}), ReasonEscalation)
	assertDenied(t, CanGrant(ctx, Grant{RootFolderIDs: []int64{1}, RatingCeiling: 0}), ReasonEscalation)

	// A Manager granted every library with no ceiling may grant anything.
	open := principal(3, RoleManager)
	open.UnrestrictedLibraries = true
	assertAllowed(t, CanGrant(ctxFor(open), Grant{AllLibraries: true}))
	assertAllowed(t, CanGrant(ctxFor(open), Grant{RootFolderIDs: []int64{9}, RatingCeiling: 5}))

	if err := CanGrant(ctxFor(open), Grant{AllLibraries: true, RatingCeiling: 6}); err == nil {
		t.Error("a ceiling above the highest rank was accepted")
	}
	if err := CanGrant(context.Background(), Grant{AllLibraries: true}); err == nil {
		t.Error("anonymous may grant")
	}
}

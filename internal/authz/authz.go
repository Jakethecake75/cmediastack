package authz

import (
	"context"
	"errors"
	"fmt"
	"slices"
)

// Denial is an authorization failure. It carries enough detail for the audit
// log while the HTTP layer renders something that leaks nothing.
//
// Requirements §7.3: a non-admin calling an admin route must get the same
// response as a nonexistent route. That is a presentation decision. The real
// reason is recorded here so an operator can still tell an attack from a bug —
// 404 to the client, 403 in the audit log.
type Denial struct {
	// Actor is the user ID, or 0 for anonymous.
	Actor int64
	// Reason is a stable machine-readable code.
	Reason string
	// Detail is human-readable, for the audit log only. Never rendered to a
	// client.
	Detail string
}

func (d *Denial) Error() string {
	return fmt.Sprintf("authorization denied (%s): %s", d.Reason, d.Detail)
}

// Denial reason codes.
const (
	ReasonAnonymous      = "anonymous"
	ReasonNotActive      = "not_active"
	ReasonMFARequired    = "mfa_required"
	ReasonMissingPerm    = "missing_permission"
	ReasonEscalation     = "escalation_attempt"
	ReasonOutOfScope     = "out_of_scope"
	ReasonSelfRoleChange = "self_role_change"
	ReasonUnknownEffect  = "unknown_effect"
	// ReasonTokenNotPermitted is an API token reaching a route that requires an
	// interactive session.
	ReasonTokenNotPermitted = "token_not_permitted"
)

// IsDenied reports whether err is an authorization denial.
func IsDenied(err error) bool {
	var d *Denial
	return errors.As(err, &d)
}

// AsDenial extracts the *Denial from err, if present.
func AsDenial(err error) (*Denial, bool) {
	var d *Denial
	ok := errors.As(err, &d)
	return d, ok
}

func deny(p *Principal, reason, detail string) *Denial {
	var actor int64
	if p != nil {
		actor = p.UserID
	}
	return &Denial{Actor: actor, Reason: reason, Detail: detail}
}

// checkActable is the gate every other check passes through first.
func checkActable(p *Principal) *Denial {
	if p == nil {
		return deny(nil, ReasonAnonymous, "no principal on the request context")
	}
	switch p.State {
	case StateActive:
		// continue
	case StateAwaitingMFA:
		return deny(p, ReasonMFARequired, "account has not enrolled an authenticator")
	default:
		return deny(p, ReasonNotActive, fmt.Sprintf("account state is %q", p.State))
	}
	if !p.MFASatisfied {
		return deny(p, ReasonMFARequired, "session did not present a second factor")
	}
	return nil
}

// RequirePermission returns nil if the principal holds perm, otherwise a
// *Denial.
func RequirePermission(ctx context.Context, perm Permission) error {
	p := FromContext(ctx)
	if d := checkActable(p); d != nil {
		return d
	}
	if !p.Role.Permissions.Has(perm) {
		return deny(p, ReasonMissingPerm, fmt.Sprintf("role %q lacks %q", p.Role.Name, perm))
	}
	return nil
}

// RequireEffect authorizes an effect at the point it occurs.
//
// This is the function that closes the privilege-laundering hole: the queue,
// the importer, the trash purge and the media-delete endpoint all reach the
// same unlink, and the unlink calls this. target is a description of what is
// being acted on, recorded in the denial for the audit log.
func RequireEffect(ctx context.Context, eff Effect, target string) error {
	p := FromContext(ctx)
	if d := checkActable(p); d != nil {
		return d
	}
	perm, ok := effectRequires[eff]
	if !ok {
		// An unmapped effect is a programming error, and the safe response to
		// a programming error in an authorization path is denial.
		return deny(p, ReasonUnknownEffect, fmt.Sprintf("effect %q has no permission mapping", eff))
	}
	if !p.Role.Permissions.Has(perm) {
		return deny(p, ReasonMissingPerm,
			fmt.Sprintf("effect %q on %q requires %q, role %q lacks it", eff, target, perm, p.Role.Name))
	}
	return nil
}

// PermissionForEffect exposes the effect→permission mapping for the role
// editor UI and for tests that assert every effect is mapped.
func PermissionForEffect(eff Effect) (Permission, bool) {
	p, ok := effectRequires[eff]
	return p, ok
}

// AllEffects returns every declared effect.
func AllEffects() []Effect {
	out := make([]Effect, 0, len(effectRequires))
	for e := range effectRequires {
		out = append(out, e)
	}
	return out
}

// ---------------------------------------------------------------------------
// Escalation guards
// ---------------------------------------------------------------------------

// CanAssignRole reports whether the actor may put someone into targetRole.
//
// Requirements §7: "a Manager can never approve an account into a role at or
// above Manager, never grant a permission they do not themselves hold, and
// never modify their own role or another Admin."
func CanAssignRole(ctx context.Context, targetRole Role) error {
	p := FromContext(ctx)
	if d := checkActable(p); d != nil {
		return d
	}
	if !p.Role.Permissions.Has(PermSetGrantsAtApproval) {
		return deny(p, ReasonMissingPerm,
			fmt.Sprintf("role %q lacks %q", p.Role.Name, PermSetGrantsAtApproval))
	}
	if targetRole.Rank >= p.Role.Rank {
		return deny(p, ReasonEscalation,
			fmt.Sprintf("role %q (rank %d) may not assign role %q (rank %d): assignment must be strictly downward",
				p.Role.Name, p.Role.Rank, targetRole.Name, targetRole.Rank))
	}
	if !targetRole.Permissions.IsSubsetOf(p.Role.Permissions) {
		return deny(p, ReasonEscalation,
			fmt.Sprintf("role %q would grant permissions the actor does not hold", targetRole.Name))
	}
	return nil
}

// CanGrantPermissions reports whether the actor may grant this explicit set.
// Used for per-user permission overrides, where a role rank comparison is not
// enough on its own.
func CanGrantPermissions(ctx context.Context, perms PermissionSet) error {
	p := FromContext(ctx)
	if d := checkActable(p); d != nil {
		return d
	}
	if !p.Role.Permissions.Has(PermSetGrantsAtApproval) {
		return deny(p, ReasonMissingPerm,
			fmt.Sprintf("role %q lacks %q", p.Role.Name, PermSetGrantsAtApproval))
	}
	if !perms.IsSubsetOf(p.Role.Permissions) {
		var missing []Permission
		for perm := range perms {
			if !p.Role.Permissions.Has(perm) {
				missing = append(missing, perm)
			}
		}
		slices.Sort(missing)
		return deny(p, ReasonEscalation,
			fmt.Sprintf("actor does not hold %v and therefore may not grant them", missing))
	}
	return nil
}

// TargetUser is the minimum information needed to authorize an action against
// another account, without loading a full principal.
type TargetUser struct {
	UserID int64
	Role   Role
}

// CanModifyUser reports whether the actor may change target's role, state or
// grants.
//
// Self role modification is denied for everyone, including administrators: an
// admin demoting themselves is the classic way to lock an instance out of its
// own administration surface, and the recovery path is a database edit.
func CanModifyUser(ctx context.Context, target TargetUser) error {
	p := FromContext(ctx)
	if d := checkActable(p); d != nil {
		return d
	}
	if !p.Role.Permissions.Has(PermManageUsers) && !p.Role.Permissions.Has(PermSuspendUser) {
		return deny(p, ReasonMissingPerm,
			fmt.Sprintf("role %q may not modify users", p.Role.Name))
	}
	if target.UserID == p.UserID {
		return deny(p, ReasonSelfRoleChange,
			"an account may not modify its own role or grants")
	}
	// You may not act on a peer or a superior. Admins outrank Managers; two
	// Admins cannot demote each other.
	if target.Role.Rank >= p.Role.Rank {
		return deny(p, ReasonEscalation,
			fmt.Sprintf("role %q (rank %d) may not modify a user holding role %q (rank %d)",
				p.Role.Name, p.Role.Rank, target.Role.Name, target.Role.Rank))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Object-level scoping
// ---------------------------------------------------------------------------

// Scope is the set of restrictions applied to every data-layer read. The
// repository layer takes a Scope; it does not take a Principal, so that no
// query can accidentally read a role instead of a grant.
type Scope struct {
	// AllLibraries short-circuits library filtering.
	AllLibraries bool
	// LibraryIDs are the readable libraries when AllLibraries is false: root
	// folder ids, because a library is a root folder (ADR-0037).
	LibraryIDs []int64
	// RatingCeiling caps visible content rank. Zero means uncapped.
	RatingCeiling int
}

// ScopeFor derives the data-layer scope for a principal. An anonymous or
// non-actable principal gets a scope that matches nothing — not an error, so
// that a caller which forgets to check still returns an empty result rather
// than the whole library.
//
// Defence in depth: the caller SHOULD have checked already. If it did not,
// this fails closed.
func ScopeFor(p *Principal) Scope {
	if !p.CanAct() {
		return Scope{AllLibraries: false, LibraryIDs: nil, RatingCeiling: 1}
	}
	if p.UnrestrictedLibraries || p.Role.Permissions.Has(PermSystemSettings) {
		return Scope{AllLibraries: true, RatingCeiling: p.RatingCeiling}
	}
	return Scope{
		AllLibraries:  false,
		LibraryIDs:    slices.Clone(p.LibraryIDs),
		RatingCeiling: p.RatingCeiling,
	}
}

// ScopeFromContext derives the scope for the request's principal.
func ScopeFromContext(ctx context.Context) Scope {
	return ScopeFor(FromContext(ctx))
}

// MatchesNothing reports whether a scope can return any row at all. Used by
// repositories to short-circuit, and by tests to assert that an anonymous
// scope is inert.
func (s Scope) MatchesNothing() bool {
	return !s.AllLibraries && len(s.LibraryIDs) == 0
}

// AllowsLibrary reports whether the scope permits reading a library.
func (s Scope) AllowsLibrary(libraryID int64) bool {
	if s.AllLibraries {
		return true
	}
	return slices.Contains(s.LibraryIDs, libraryID)
}

// AllowsRating reports whether the scope permits content of the given rating
// rank. Higher rank means more restrictive content.
func (s Scope) AllowsRating(rank int) bool {
	if s.RatingCeiling <= 0 {
		return true
	}
	return rank <= s.RatingCeiling
}

// RequireLibraryAccess is the object-level check for a single record. Every
// record fetch that resolves to a library calls this.
func RequireLibraryAccess(ctx context.Context, libraryID int64) error {
	p := FromContext(ctx)
	if d := checkActable(p); d != nil {
		return d
	}
	if !ScopeFor(p).AllowsLibrary(libraryID) {
		return deny(p, ReasonOutOfScope,
			fmt.Sprintf("library %d is not granted to user %d", libraryID, p.UserID))
	}
	return nil
}

// Grant is what an account may see: every library, or the root folders listed,
// and a rating ceiling (ADR-0037). The shape an approver chooses and an
// administrator changes.
type Grant struct {
	AllLibraries bool
	// RootFolderIDs are the libraries when AllLibraries is false.
	RootFolderIDs []int64
	// RatingCeiling is 0 for none, or the highest rank visible.
	RatingCeiling int
}

// MaxRatingRank is the most restrictive rank a title can carry (NC-17).
const MaxRatingRank = 5

// CanGrant reports whether the actor may give an account this much of the
// library (ADR-0037, decision 5). A grant can be no wider than the grantor's own
// scope: a restricted approver grants only roots they can see, and a ceiling at
// or below their own. Without this, approving an account would be a way for a
// restricted approver to create an unrestricted one.
func CanGrant(ctx context.Context, g Grant) error {
	p := FromContext(ctx)
	if d := checkActable(p); d != nil {
		return d
	}
	if g.RatingCeiling < 0 || g.RatingCeiling > MaxRatingRank {
		return fmt.Errorf("a rating ceiling is 0 for none, or 1 to %d", MaxRatingRank)
	}
	own := ScopeFor(p)
	if g.AllLibraries && !own.AllLibraries {
		return deny(p, ReasonEscalation,
			"a grantor restricted to some libraries may not grant all of them")
	}
	if !g.AllLibraries {
		for _, id := range g.RootFolderIDs {
			if !own.AllowsLibrary(id) {
				return deny(p, ReasonEscalation,
					fmt.Sprintf("root folder %d is not granted to the grantor", id))
			}
		}
	}
	if own.RatingCeiling > 0 && (g.RatingCeiling == 0 || g.RatingCeiling > own.RatingCeiling) {
		return deny(p, ReasonEscalation,
			fmt.Sprintf("a grantor with a rating ceiling of %d may not grant a higher one",
				own.RatingCeiling))
	}
	return nil
}

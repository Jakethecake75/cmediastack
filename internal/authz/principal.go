package authz

import "context"

// UserState is the account lifecycle state. Only StateActive can act.
type UserState string

const (
	// StateAwaitingMFA is an approved account that has not yet enrolled an
	// authenticator. It holds a session, but that session's only capability is
	// completing enrollment.
	StateAwaitingMFA UserState = "awaiting_mfa"
	StateActive      UserState = "active"
	StateSuspended   UserState = "suspended"
	StateDisabled    UserState = "disabled"
)

// Role rank determines who may grant what. Higher outranks lower.
const (
	RankUser    = 10
	RankManager = 50
	RankAdmin   = 100
)

// Role is a named permission bundle with a rank.
type Role struct {
	ID          int64
	Name        string
	Rank        int
	Builtin     bool
	Permissions PermissionSet
}

// Built-in role names.
const (
	RoleAdmin   = "Admin"
	RoleManager = "Manager"
	RoleUser    = "User"
)

// BuiltinRoles returns the three roles seeded at first run, matching the
// permission matrix in requirements §7. Configurable per-role permissions are
// stored in the database; these are the defaults.
//
// Note what Manager deliberately does NOT get: PermDeleteMediaFiles,
// PermManageRootFolders, PermManageIndexers, PermManageNetwork,
// PermManageUsers, PermViewAuditLog, PermSystemSettings.
func BuiltinRoles() []Role {
	return []Role{
		{
			Name: RoleAdmin, Rank: RankAdmin, Builtin: true,
			Permissions: NewPermissionSet(AllPermissions...),
		},
		{
			Name: RoleManager, Rank: RankManager, Builtin: true,
			Permissions: NewPermissionSet(
				PermLogin, PermStream, PermBrowse, PermDownloadOriginal,
				PermSubmitRequest, PermAutoApproveOwn, PermApproveRequests,
				PermApproveAccounts, PermIssueInvites, PermSetGrantsAtApproval,
				PermSuspendUser, PermViewOtherActivity,
				PermInteractiveSearch, PermManageQueue, PermEditLibraryItems,
			),
		},
		{
			Name: RoleUser, Rank: RankUser, Builtin: true,
			Permissions: NewPermissionSet(
				PermLogin, PermStream, PermBrowse, PermSubmitRequest,
			),
		},
	}
}

// CredentialKind distinguishes an interactive session from a non-interactive
// API token.
//
// It exists so that credential-management routes can require a session. A token
// is a long-lived bearer credential that lives in a script or a config file; it
// must not be able to change the password, enroll or replace the second factor,
// mint further tokens, or manage sessions. Otherwise one leaked token is a
// permanent account takeover rather than a scoped, revocable grant.
type CredentialKind string

const (
	// CredentialSession is an interactive login. The zero value, so any
	// principal built without thinking about this is treated as a session,
	// which is the more permissive case — hence BuildTokenPrincipal sets the
	// token kind explicitly and a test asserts it.
	CredentialSession CredentialKind = ""
	CredentialToken   CredentialKind = "token"
)

// Principal is an authenticated actor. A nil *Principal is anonymous.
//
// There is deliberately no Anonymous() constructor and no zero-value default:
// code that needs a principal must obtain one from a verified session, and
// code that receives nil must handle denial explicitly.
type Principal struct {
	UserID    int64
	Username  string
	Role      Role
	State     UserState
	SessionID string

	// LibraryIDs are the libraries this principal may see — root folder ids
	// (ADR-0037). Ignored when UnrestrictedLibraries is true.
	LibraryIDs []int64
	// UnrestrictedLibraries is set for an account granted every library, for
	// principals whose role holds PermSystemSettings, and for background work.
	UnrestrictedLibraries bool
	// RatingCeiling caps visible content by rating rank. Zero means no cap.
	RatingCeiling int
	// MFASatisfied records whether this session presented a second factor.
	MFASatisfied bool
	// Credential says how the caller authenticated.
	Credential CredentialKind
}

// IsToken reports whether the principal authenticated with an API token.
func (p *Principal) IsToken() bool {
	return p != nil && p.Credential == CredentialToken
}

// CanAct reports whether the principal is in a state permitted to do anything
// beyond MFA enrollment. Suspended and disabled accounts, and accounts that
// have not enrolled an authenticator, cannot act.
func (p *Principal) CanAct() bool {
	return p != nil && p.State == StateActive && p.MFASatisfied
}

// Has reports whether the principal holds a permission. A principal that
// cannot act holds nothing, regardless of its role.
func (p *Principal) Has(perm Permission) bool {
	if !p.CanAct() {
		return false
	}
	return p.Role.Permissions.Has(perm)
}

// IsAdmin reports whether the principal holds the system-settings permission,
// which is the definition of administrator used throughout the app.
func (p *Principal) IsAdmin() bool {
	return p.Has(PermSystemSettings)
}

type ctxKey int

const ctxKeyPrincipal ctxKey = iota

// WithPrincipal attaches a verified principal to ctx. Only the authentication
// middleware may call this.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKeyPrincipal, p)
}

// FromContext returns the principal attached to ctx, or nil for anonymous.
// The nil return is the anonymous case and every caller must handle it.
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKeyPrincipal).(*Principal)
	return p
}

// Package authz is the single authorization authority for CMediaStack.
//
// Two rules govern everything here:
//
//  1. Deny by default. A nil principal is anonymous and is denied everything.
//     There is no code path in which a missing session resolves to a default
//     principal with any capability at all.
//
//  2. Permissions are defined over EFFECTS, not over routes. "Delete media
//     files: Admin only" is meaningless if a Manager can reach the same effect
//     by removing a torrent with its data, repointing a root folder, or
//     renaming a file into a void. RequireEffect is called at the point the
//     effect occurs — inside the filesystem layer — not in an HTTP handler.
package authz

// Permission is a capability a role may hold.
type Permission string

const (
	PermLogin               Permission = "auth.login"
	PermStream              Permission = "media.stream"
	PermBrowse              Permission = "media.browse"
	PermDownloadOriginal    Permission = "media.download_original"
	PermSubmitRequest       Permission = "request.submit"
	PermAutoApproveOwn      Permission = "request.auto_approve_own"
	PermApproveRequests     Permission = "request.approve"
	PermRequestPremium      Permission = "request.premium"
	PermApproveAccounts     Permission = "account.approve"
	PermIssueInvites        Permission = "account.invite"
	PermSetGrantsAtApproval Permission = "account.set_grants"
	PermSuspendUser         Permission = "account.suspend"
	PermViewOtherActivity   Permission = "account.view_activity"
	PermInteractiveSearch   Permission = "acquisition.search"
	PermManageQueue         Permission = "acquisition.queue"
	PermEditLibraryItems    Permission = "library.edit"
	PermDeleteMediaFiles    Permission = "library.delete"
	PermManageRootFolders   Permission = "library.root_folders"
	PermManageIndexers      Permission = "admin.indexers"
	PermManageNetwork       Permission = "admin.network"
	PermManageUsers         Permission = "admin.users"
	PermViewAuditLog        Permission = "admin.audit"
	PermSystemSettings      Permission = "admin.system"
)

// AllPermissions is the complete set, used by the role editor and by tests
// that assert no permission is unreachable or undocumented.
var AllPermissions = []Permission{
	PermLogin, PermStream, PermBrowse, PermDownloadOriginal,
	PermSubmitRequest, PermAutoApproveOwn, PermApproveRequests, PermRequestPremium,
	PermApproveAccounts, PermIssueInvites, PermSetGrantsAtApproval, PermSuspendUser,
	PermViewOtherActivity, PermInteractiveSearch, PermManageQueue,
	PermEditLibraryItems, PermDeleteMediaFiles, PermManageRootFolders,
	PermManageIndexers, PermManageNetwork, PermManageUsers, PermViewAuditLog,
	PermSystemSettings,
}

// Effect is a consequence an operation has on the world, independent of which
// route or feature produced it.
type Effect string

const (
	// EffectDestroyMediaBytes covers ANY operation that unlinks or truncates a
	// file under a root folder: media delete, torrent remove-with-data,
	// import cleanup, trash purge, failed-import rollback.
	EffectDestroyMediaBytes Effect = "destroy_media_bytes"
	// EffectMutateLibraryPaths covers root-folder repointing and bulk rename
	// application — operations that can orphan an entire library without
	// deleting a single byte directly.
	EffectMutateLibraryPaths Effect = "mutate_library_paths"
	// EffectEgressConfig covers proxy, egress-profile and indexer URL changes:
	// anything that can redirect traffic outside the tunnel.
	EffectEgressConfig Effect = "egress_config"
	// EffectGrantAccess covers role assignment, library grants and invite
	// issuance.
	EffectGrantAccess Effect = "grant_access"
	// EffectReadSecret covers decrypting a stored credential for use.
	EffectReadSecret Effect = "read_secret"
)

// effectRequires maps each effect to the permission that authorizes it. This
// map is the reason a Manager with queue access cannot delete media: the queue
// handler may run, but the unlink calls RequireEffect and is refused.
var effectRequires = map[Effect]Permission{
	EffectDestroyMediaBytes:  PermDeleteMediaFiles,
	EffectMutateLibraryPaths: PermManageRootFolders,
	EffectEgressConfig:       PermManageNetwork,
	EffectGrantAccess:        PermSetGrantsAtApproval,
	EffectReadSecret:         PermManageIndexers,
}

// PermissionSet is a set of permissions.
type PermissionSet map[Permission]struct{}

// NewPermissionSet builds a set from a slice.
func NewPermissionSet(perms ...Permission) PermissionSet {
	s := make(PermissionSet, len(perms))
	for _, p := range perms {
		s[p] = struct{}{}
	}
	return s
}

// Has reports whether the set contains p.
func (s PermissionSet) Has(p Permission) bool {
	_, ok := s[p]
	return ok
}

// Slice returns the set's members. Order is not guaranteed.
func (s PermissionSet) Slice() []Permission {
	out := make([]Permission, 0, len(s))
	for p := range s {
		out = append(out, p)
	}
	return out
}

// IsSubsetOf reports whether every permission in s is also in other. This is
// the check that stops a Manager granting a permission they do not hold.
func (s PermissionSet) IsSubsetOf(other PermissionSet) bool {
	for p := range s {
		if !other.Has(p) {
			return false
		}
	}
	return true
}

// Clone returns an independent copy.
func (s PermissionSet) Clone() PermissionSet {
	out := make(PermissionSet, len(s))
	for p := range s {
		out[p] = struct{}{}
	}
	return out
}

package authz

import "context"

// SystemTask is the name of background work acting without a person behind it.
type SystemTask string

const (
	// TaskImport is the importer moving completed downloads into the library.
	TaskImport SystemTask = "system:import"
	// TaskLibraryScan is a scan reconciling the database with what is on disk.
	TaskLibraryScan SystemTask = "system:library-scan"
	// TaskTrashPurge is the task that finally unlinks superseded and deleted
	// files once their retention window has passed.
	TaskTrashPurge SystemTask = "system:trash-purge"
	// TaskIdentify is the pass that asks a metadata provider what each library
	// item is. It PROPOSES; it does not relabel anything.
	TaskIdentify SystemTask = "system:identify"
	// TaskPlaybackPurge drops playback positions older than the configured
	// retention. Playback history is a record of what somebody watched and
	// when, which the threat model lists as an asset in its own right.
	TaskPlaybackPurge SystemTask = "system:playback-purge"
	// TaskEpisodeRefresh asks a metadata provider which episodes each series
	// has, so that "missing" means something (ADR-0022).
	TaskEpisodeRefresh SystemTask = "system:episode-refresh"
	// TaskAcquire fetches what is wanted without a person: the indexers'
	// recent releases and searches for wanted items, and a grab of what
	// matches (ADR-0030).
	TaskAcquire SystemTask = "system:acquire"
	// TaskNotify reads the audit log and sends what the operator chose to
	// their Discord webhook (ADR-0032).
	TaskNotify SystemTask = "system:notify"
	// TaskRatings asks the metadata provider how each identified title is
	// rated, which decides who may see it (ADR-0037).
	TaskRatings SystemTask = "system:ratings"
	// TaskMusicRefresh asks MusicBrainz for followed artists' albums and the
	// track lists of monitored albums (ADR-0044).
	TaskMusicRefresh SystemTask = "system:music-refresh"
	// TaskSubtitles is the sweep that fetches wanted subtitles (ADR-0056).
	TaskSubtitles SystemTask = "system:subtitles"
)

// systemGrants is what each background task may do — PER TASK, not shared.
//
// # Why this is a map and not a list
//
// A scheduled task has no person behind it, so there is nobody whose authority
// it is borrowing and nobody to hold responsible for what it does. That makes
// this the one place in the codebase where authority appears from nowhere, and
// therefore the thing an attacker who could influence a background task would
// reach for.
//
// It started as a single shared set, and the trash purge is what proved that
// wrong. The purge must unlink files; the importer must not. A shared set with
// PermDeleteMediaFiles in it would hand destroy authority to the importer as a
// side effect of building a cleanup job — and the importer's whole
// reversible-upgrade design rests on it NOT having that.
//
// So each task gets exactly what it needs and nothing else:
//
//   - Import: browse, and path mutation for moving a superseded file to trash.
//     NOT destroy: an upgrade moves the file it replaces, which is what makes
//     it reversible (§2).
//   - Library scan: browse ONLY. A scan changes nothing on disk — not even a
//     rename — so it has no business holding an effect that could.
//   - Trash purge: browse and destroy. NOT path mutation: it unlinks, it does
//     not move things around the library.
//
// Nothing here touches users, roles, indexers, egress or system settings. No
// background task has business changing who can do what.
var systemGrants = map[SystemTask][]Permission{
	TaskImport:      {PermBrowse, PermManageRootFolders},
	TaskLibraryScan: {PermBrowse},
	TaskTrashPurge:  {PermBrowse, PermDeleteMediaFiles},
	// Browse ONLY, and that is the whole grant. The pass reads library items and
	// writes to a table of proposals; it changes no media, no path and no title.
	//
	// Notably absent: PermEditLibraryItems. A background task that could rewrite
	// what an operator browses to is the thing ADR-0019 exists to prevent, and
	// withholding the permission is stronger than intending not to use it.
	TaskIdentify: {PermBrowse},
	// Browse ONLY, and it is enough: the task deletes rows from a table of
	// positions, which is not a media effect. Notably absent is
	// PermDeleteMediaFiles — a retention task that could unlink a film is a
	// retention task one typo away from being a library-deletion task.
	TaskPlaybackPurge: {PermBrowse},
	// Browse ONLY. The task records what a provider says a series contains,
	// which is a fact recorded ABOUT an item rather than a change to it — the
	// same reasoning that puts AttachIdentity behind browse.
	//
	// Notably absent: PermEditLibraryItems. Both monitoring setters require it,
	// so a background refresh cannot turn back on a season somebody stopped
	// following, however it is written. That is a stronger guarantee than the
	// ON CONFLICT clause that happens not to touch the flag today.
	TaskEpisodeRefresh: {PermBrowse},
	// Browse, search and the queue: what a person needs to find a wanted item
	// and grab it, and nothing past that. It reads the wanted list, asks the
	// indexers, and adds to the queue (ADR-0030).
	//
	// Notably absent: PermEditLibraryItems, so it cannot add a title, change
	// what is monitored, or relabel anything — what is wanted stays a person's
	// decision, and this task only acts on it. PermDeleteMediaFiles and
	// PermManageRootFolders: it never touches a file; the import, a separate
	// task with its own grant, files what it downloads.
	TaskAcquire: {PermBrowse, PermInteractiveSearch, PermManageQueue},
	// Reading the audit log, and nothing else: the notifier reads it from where
	// it last stopped and sends what the operator chose (ADR-0032).
	//
	// Notably absent: PermSystemSettings. The webhook and the categories are
	// read inside the notifier without it; what goes out, and where, is
	// changed only by a person holding both it and this permission.
	TaskNotify: {PermViewAuditLog},
	// Browse ONLY: a rating is a fact recorded about a title, like its
	// provider id (ADR-0019, ADR-0037). Notably absent: PermEditLibraryItems,
	// so a person's rating is never the task's to overwrite, and the store
	// refuses to.
	TaskRatings: {PermBrowse},
	// Browse ONLY, as the episode refresh: the catalogue is a fact about a
	// title, and PermEditLibraryItems would let it change what is monitored.
	TaskMusicRefresh: {PermBrowse},
	// Browse ONLY (ADR-0056, decision 1): a sidecar beside a video changes no
	// title, no monitoring and no existing file. Notably absent:
	// PermEditLibraryItems, which no background task holds; the person's
	// fetch keeps its own check, and the sweep is a method no route reaches.
	TaskSubtitles: {PermBrowse},
}

// SystemPrincipal returns a context for background work.
//
// It cannot be reached from an HTTP request:
// TestOnlySchedulingCodeCanMintASystemPrincipal reads the source of every
// package and fails the build if this is called outside the short allowlist
// that registers scheduled tasks. That
// structural check is what makes the grants above safe — a runtime check cannot
// prove that a handler does not call this.
//
// An UNKNOWN task gets nothing at all, rather than a default set. A typo in a
// task name must produce work that cannot act, not work that quietly inherits
// somebody else's authority.
//
// The task name is carried so an audit line can say which piece of background
// work acted, rather than "system" for everything.
func SystemPrincipal(ctx context.Context, task SystemTask) context.Context {
	perms := systemGrants[task]
	return WithPrincipal(ctx, &Principal{
		UserID:   0,
		Username: string(task),
		Role: Role{
			Name:        string(task),
			Rank:        RankUser,
			Permissions: NewPermissionSet(perms...),
		},
		State:                 StateActive,
		MFASatisfied:          true,
		UnrestrictedLibraries: true,
		Credential:            CredentialSession,
	})
}

// IsSystem reports whether a principal is background work rather than a person.
//
// Used by the audit log so a line can say so plainly, and available to any
// handler that wants to refuse being driven by one.
func IsSystem(p *Principal) bool {
	if p == nil {
		return false
	}
	if _, known := systemGrants[SystemTask(p.Username)]; known {
		return p.UserID == 0
	}
	return false
}

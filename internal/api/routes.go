package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/logging"
	"github.com/jakethecake75/cmediastack/internal/web"
)

// RegisterRoutes declares the complete Phase 1 HTTP surface.
//
// Routes whose business logic belongs to a later phase are registered here
// deliberately, wired to notImplemented. That is not a stub standing in for
// missing security: the access class, the allowlist decision and the
// permission requirement are all real and are enforced by the middleware
// today. Registering them now means the enumeration test covers the whole
// surface from Phase 1 rather than growing to cover it later, and a route
// cannot be added in Phase 3 without a deliberate access decision.
//
// PROGRESS.md tracks which handlers are implemented.
func RegisterRoutes(rt *Router, h *Handlers) {
	// --- Anonymous surface. Every entry here also appears in
	// --- AnonymousAllowlist; register() enforces that they agree.
	rt.Anonymous(http.MethodGet, "/login", h.or(h.loginPage))
	rt.Anonymous(http.MethodPost, "/api/v1/auth/login", h.or(h.Login))
	rt.Anonymous(http.MethodPost, "/api/v1/auth/login/mfa", h.or(h.LoginMFA))
	rt.Anonymous(http.MethodGet, "/signup", h.page("signup"))
	rt.Anonymous(http.MethodPost, "/api/v1/auth/signup", h.or(h.Signup))
	rt.Anonymous(http.MethodGet, "/api/v1/auth/signup/challenge", h.or(h.SignupChallenge))
	rt.Anonymous(http.MethodGet, "/reset", h.page("reset"))
	rt.Anonymous(http.MethodGet, "/setup", h.or(h.SetupPage))
	rt.Anonymous(http.MethodPost, "/api/v1/setup", h.or(h.Setup))
	rt.Anonymous(http.MethodPost, "/api/v1/auth/reset/initiate", h.or(h.ResetInitiate))
	rt.Anonymous(http.MethodPost, "/api/v1/auth/reset/complete", h.or(h.ResetComplete))
	rt.Anonymous(http.MethodGet, "/assets/auth/", web.Assets(web.BundleAuth, "/assets/auth/"))
	rt.Anonymous(http.MethodGet, "/healthz", healthz)

	// --- MFA enrollment. Reachable by an approved account that has not yet
	// --- enrolled an authenticator, and by nothing else. The page is in the
	// --- ANONYMOUS asset bundle even though the route requires a session: it
	// --- has to be, because it is reached before the account can act, and
	// --- putting it in the app bundle would mean shipping the authenticated
	// --- client to a principal that is not yet allowed to use it.
	rt.Enrollment(http.MethodGet, "/enroll", h.or(h.page("enroll")))
	rt.Enrollment(http.MethodGet, "/api/v1/auth/mfa/enroll", h.or(h.EnrollBegin))
	rt.Enrollment(http.MethodPost, "/api/v1/auth/mfa/enroll", h.or(h.EnrollBegin))
	rt.Enrollment(http.MethodPost, "/api/v1/auth/mfa/enroll/confirm", h.or(h.EnrollConfirm))
	rt.Enrollment(http.MethodPost, "/api/v1/auth/logout", h.or(h.Logout))

	// --- Application shell. Deliberately NOT anonymous: an unauthenticated
	// --- caller must not be able to enumerate the API client from the bundle.
	//
	// The root is the ONLY route in the whole surface that redirects rather
	// than 404s when it is denied, because §2 promises that a visitor with no
	// session is shown a login screen and the root is where a visitor arrives.
	// The disclosure is nil: every origin has a root, and /login is anonymous
	// already. Every other page — /app/, /enroll — stays invisible, so nothing
	// is learned by probing them.
	//
	// "/{$}" and not "/": in net/http's mux a bare "/" is a catch-all that
	// would match every otherwise-unrouted path, which would turn the shell
	// into a wildcard and quietly undo TestUnmatchedPathIs404ForAnonymous.
	rt.Page("/{$}", h.or(h.page("app")))
	rt.SessionRoute(http.MethodGet, "/app/", h.or(h.page("app")))
	rt.Authenticated(http.MethodGet, "/assets/app/", web.Assets(web.BundleApp, "/assets/app/"))

	// --- Self service. The credential-management routes below are registered
	// --- with SessionRoute, which refuses API tokens whatever their scope: a
	// --- token must not be able to change the password, replace the second
	// --- factor, mint further tokens or manage sessions.
	rt.Authenticated(http.MethodGet, "/api/v1/me", h.or(h.Me))
	// One's own email, with the current password (ADR-0039). Session-only: a
	// token must not redirect where a reset would be delivered.
	rt.SessionRoute(http.MethodPatch, "/api/v1/me", h.or(h.UpdateMe))
	rt.SessionRoute(http.MethodGet, "/api/v1/me/sessions", h.or(h.MySessions))
	rt.SessionRoute(http.MethodDelete, "/api/v1/me/sessions/{id}", h.or(h.RevokeMySession))
	rt.SessionRoute(http.MethodPost, "/api/v1/me/password", h.or(h.ChangePassword))
	rt.SessionRoute(http.MethodGet, "/api/v1/me/tokens", h.or(h.ListAPITokens))
	rt.SessionRoute(http.MethodPost, "/api/v1/me/tokens", h.or(h.IssueAPIToken))
	rt.SessionRoute(http.MethodDelete, "/api/v1/me/tokens/{id}", h.or(h.RevokeAPIToken))
	rt.SessionRoute(http.MethodPost, "/api/v1/me/mfa/recovery-codes", h.or(h.RegenerateRecoveryCodes))

	// --- Library and playback
	// Searching the library by title (ADR-0038): what the caller may see, and
	// never the provider.
	rt.Permission(http.MethodGet, "/api/v1/search", authz.PermBrowse, h.or(h.SearchLibrary))
	rt.Permission(http.MethodGet, "/api/v1/media", authz.PermBrowse, h.or(h.ListMedia))
	// Adding a series or a film before any of it is on disk (ADR-0025,
	// ADR-0026). Editing the library, like the monitoring switches and the
	// provider search the same screen uses. It writes rows only: no file, no
	// folder, no download.
	rt.Permission(http.MethodPost, "/api/v1/media", authz.PermEditLibraryItems, h.or(h.AddMedia))
	rt.Permission(http.MethodGet, "/api/v1/media/{id}", authz.PermBrowse, h.or(h.GetMedia))
	// What a series contains and what it is missing. An episode row exists
	// because a PROVIDER said the episode exists, never because a file does —
	// a season assembled from files on disk is complete by construction and
	// would never report a gap (ADR-0022).
	rt.Permission(http.MethodGet, "/api/v1/media/{id}/children", authz.PermBrowse, h.or(h.SeriesChildren))
	rt.Permission(http.MethodGet, "/api/v1/wanted", authz.PermBrowse, h.or(h.Wanted))
	// Music (ADR-0044): an artist's albums, an album's tracks — fetched from
	// MusicBrainz the first time it is opened — and whether it is wanted.
	// Searching MusicBrainz is editing the library, as searching TMDB is.
	rt.Permission(http.MethodGet, "/api/v1/media/{id}/albums", authz.PermBrowse, h.or(h.ArtistAlbums))
	rt.Permission(http.MethodGet, "/api/v1/albums/{id}", authz.PermBrowse, h.or(h.GetAlbum))
	rt.Permission(http.MethodPut, "/api/v1/albums/{id}/monitored", authz.PermEditLibraryItems, h.or(h.SetAlbumMonitored))
	rt.Permission(http.MethodPost, "/api/v1/albums/{id}/search", authz.PermInteractiveSearch, h.or(h.SearchForAlbum))
	rt.Permission(http.MethodGet, "/api/v1/books/search", authz.PermEditLibraryItems, h.or(h.SearchBooks))
	rt.Permission(http.MethodGet, "/api/v1/music/artists", authz.PermEditLibraryItems, h.or(h.SearchArtists))
	// Changing what is followed is editing the library, not browsing it.
	rt.Permission(http.MethodPut, "/api/v1/media/{id}/seasons/{season}/monitored", authz.PermEditLibraryItems, h.or(h.SetSeasonMonitored))
	// Whether a series takes on its new seasons (ADR-0061).
	rt.Permission(http.MethodPut, "/api/v1/media/{id}/new-seasons", authz.PermEditLibraryItems, h.or(h.SetFollowNewSeasons))
	// Whether a series files in season folders (ADR-0063).
	rt.Permission(http.MethodPut, "/api/v1/media/{id}/season-folders", authz.PermEditLibraryItems, h.or(h.SetSeasonFolders))
	// Whether a series is searched by date (ADR-0064).
	rt.Permission(http.MethodPut, "/api/v1/media/{id}/daily", authz.PermEditLibraryItems, h.or(h.SetDaily))
	rt.Permission(http.MethodPut, "/api/v1/episodes/{id}/monitored", authz.PermEditLibraryItems, h.or(h.SetEpisodeMonitored))
	// A film is monitored as a whole (ADR-0030): off keeps it in the library and
	// off the Wanted list, out of automatic acquisition's reach.
	rt.Permission(http.MethodPut, "/api/v1/media/{id}/monitored", authz.PermEditLibraryItems, h.or(h.SetFilmMonitored))
	// A title's own quality profile (ADR-0035): what it is fetched as, which is
	// curating the library, as monitoring is.
	rt.Permission(http.MethodPut, "/api/v1/media/{id}/quality-profile", authz.PermEditLibraryItems, h.or(h.SetTitleProfile))
	// A title's rating, by hand (ADR-0037): it decides who may see the title,
	// which is curating the library, and it is audited.
	rt.Permission(http.MethodPut, "/api/v1/media/{id}/rating", authz.PermEditLibraryItems, h.or(h.SetTitleRating))
	// Searching for one episode is an interactive search like any other, and
	// grabbing what it finds is the ordinary grab route: the episode travels
	// sealed in the ticket, not in a second request (ADR-0023).
	rt.Permission(http.MethodPost, "/api/v1/episodes/{id}/search", authz.PermInteractiveSearch, h.or(h.SearchForEpisode))
	rt.Permission(http.MethodPost, "/api/v1/media/{id}/seasons/{season}/search", authz.PermInteractiveSearch, h.or(h.SearchForSeason))
	// The same for one film (ADR-0026): every release judged against the film,
	// and only one that IS the film grabbable, with the film sealed into it.
	rt.Permission(http.MethodPost, "/api/v1/media/{id}/search", authz.PermInteractiveSearch, h.or(h.SearchForFilm))
	// Spending a third party's rate limit on a whole series is administrative,
	// like the other provider-facing triggers.
	rt.Admin(http.MethodPost, "/api/v1/admin/media/{id}/refresh-episodes", authz.PermSystemSettings, h.or(h.RefreshEpisodes))
	// A title's poster, keyed by the title so its scope decides (ADR-0038).
	rt.Permission(http.MethodGet, "/api/v1/media/{id}/artwork", authz.PermBrowse, h.or(h.TitleArtwork))
	// What is popular (ADR-0043): the provider's lists, fetched once in six
	// hours for everybody, so browsing spends nothing.
	rt.Permission(http.MethodGet, "/api/v1/discover/{section}", authz.PermBrowse, h.or(h.Discover))
	// The play-session routes (HLS) were removed in 4ad (ADR-0038): the
	// session is the signed-in one, the bytes are /files/{id}/stream and
	// /convert, and progress is /files/{id}/position.
	//
	// A title's file as stored, downloaded: a copy that leaves the instance,
	// so its own permission and an audit line (ADR-0038).
	rt.Permission(http.MethodGet, "/api/v1/media/{id}/original", authz.PermDownloadOriginal, h.or(h.DownloadOriginal))

	// --- Feeds (ADR-0041). A calendar app and a feed reader send an address
	// --- and nothing else, so the route is anonymous and the token in the path
	// --- is the credential: a feed token, which reads these two routes as its
	// --- account, with media.browse and nothing more. A wrong one is 404.
	rt.Anonymous(http.MethodGet, "/api/v1/feeds/{token}/calendar.ics", h.or(h.CalendarFeed))
	rt.Anonymous(http.MethodGet, "/api/v1/feeds/{token}/rss", h.or(h.RSSFeed))
	rt.SessionRoute(http.MethodGet, "/api/v1/me/feeds", h.or(h.MyFeeds))
	rt.SessionRoute(http.MethodPost, "/api/v1/me/feeds", h.or(h.MintFeeds))
	rt.SessionRoute(http.MethodDelete, "/api/v1/me/feeds", h.or(h.RevokeFeeds))

	// --- Requests
	// Gated on SUBMIT, not on approve: an ordinary user must be able to list
	// requests, because their own are in there. What differs by role is the
	// SCOPE of that listing, and scope is not something a route can express —
	// it is computed from the principal in request.Service.List.
	rt.Permission(http.MethodPost, "/api/v1/requests", authz.PermSubmitRequest, h.or(h.SubmitRequest))
	rt.Permission(http.MethodGet, "/api/v1/requests", authz.PermSubmitRequest, h.or(h.ListRequests))
	rt.Permission(http.MethodPost, "/api/v1/requests/{id}/approve", authz.PermApproveRequests, h.or(h.ApproveRequest))
	rt.Permission(http.MethodPost, "/api/v1/requests/{id}/deny", authz.PermApproveRequests, h.or(h.DenyRequest))
	// Which library item satisfies an approved request (ADR-0028). Saying which
	// film a request meant is part of deciding it, so it is the approver's —
	// and it adds nothing: the item is added under library.edit, as any is.
	rt.Permission(http.MethodPost, "/api/v1/requests/{id}/item", authz.PermApproveRequests, h.or(h.LinkRequest))
	// A problem with a title (ADR-0042): reported by whoever may ask for
	// titles, about one they can see; resolved by whoever can fix it.
	rt.Permission(http.MethodPost, "/api/v1/issues", authz.PermSubmitRequest, h.or(h.ReportIssue))
	rt.Permission(http.MethodGet, "/api/v1/issues", authz.PermSubmitRequest, h.or(h.ListIssues))
	rt.Permission(http.MethodPost, "/api/v1/issues/{id}/resolve", authz.PermEditLibraryItems, h.or(h.ResolveIssue))

	// --- Manager surface
	// The role list is scoped to what the caller may actually assign, so it is
	// gated on the approval permission rather than on admin.users: an approver
	// who cannot see the roles cannot fill in the approval form.
	rt.Permission(http.MethodGet, "/api/v1/roles", authz.PermApproveAccounts, h.or(h.AssignableRoles))
	rt.Permission(http.MethodGet, "/api/v1/accounts/requests", authz.PermApproveAccounts, h.or(h.PendingAccounts))
	rt.Permission(http.MethodGet, "/api/v1/accounts/requests/{id}", authz.PermApproveAccounts, h.or(h.GetAccountRequest))
	rt.Permission(http.MethodPost, "/api/v1/accounts/requests/{id}/approve", authz.PermApproveAccounts, h.or(h.ApproveAccount))
	rt.Permission(http.MethodPost, "/api/v1/accounts/requests/{id}/deny", authz.PermApproveAccounts, h.or(h.DenyAccount))
	rt.Permission(http.MethodGet, "/api/v1/invites", authz.PermIssueInvites, h.or(h.ListInvites))
	rt.Permission(http.MethodPost, "/api/v1/invites", authz.PermIssueInvites, h.or(h.IssueInvite))
	rt.Permission(http.MethodDelete, "/api/v1/invites/{id}", authz.PermIssueInvites, h.or(h.RevokeInvite))
	rt.Permission(http.MethodGet, "/api/v1/queue", authz.PermManageQueue, h.or(h.Queue))
	rt.Permission(http.MethodPost, "/api/v1/queue/{id}/remove", authz.PermManageQueue, h.or(h.QueueRemove))
	// What happened to a completed download. Reachable from the queue, on the
	// permission that manages it, because that is where an operator is standing
	// when they ask why nothing appeared in the library.
	rt.Permission(http.MethodGet, "/api/v1/queue/{id}/history", authz.PermManageQueue, h.or(h.ImportHistory))
	rt.Permission(http.MethodPost, "/api/v1/releases/search", authz.PermInteractiveSearch, h.or(h.SearchReleases))
	// Grabbing is gated on the QUEUE permission, not on search. Looking at what
	// exists and causing the instance to acquire it are different acts with
	// different consequences, and a role that may do the first without the
	// second is a policy an operator should be able to express.
	rt.Permission(http.MethodPost, "/api/v1/releases/grab", authz.PermManageQueue, h.or(h.GrabRelease))
	// Profiles are readable by anyone who can run an interactive search: the
	// search form needs them to fill in its dropdown, and a profile is policy
	// rather than a secret. Editing one is admin.system, enforced in the store.
	rt.Permission(http.MethodGet, "/api/v1/quality-profiles", authz.PermInteractiveSearch, h.or(h.ListQualityProfiles))
	// Which profile judges a search that names none (ADR-0027). Policy for the
	// whole instance, so it is an administrator's, like editing a profile.
	rt.Admin(http.MethodPut, "/api/v1/admin/quality-profiles/default", authz.PermSystemSettings, h.or(h.SetDefaultProfile))

	// --- Admin surface. Hidden: an unauthorized caller gets 404, not 403, so
	// --- the administration surface is invisible rather than merely forbidden.
	rt.Admin(http.MethodGet, "/api/v1/admin/users", authz.PermManageUsers, h.or(h.ListUsers))
	// An account created by an administrator, who never learns its password:
	// the answer is a one-time link that sets one (ADR-0039).
	rt.Admin(http.MethodPost, "/api/v1/admin/users", authz.PermManageUsers, h.or(h.CreateUser))
	rt.Admin(http.MethodPatch, "/api/v1/admin/users/{id}", authz.PermManageUsers, h.or(h.UpdateUser))
	// Which libraries an account sees, and its rating ceiling (ADR-0037).
	// Separate from the PATCH above, which changes one thing at a time so its
	// audit line is unambiguous; this is one grant, written together.
	rt.Admin(http.MethodPut, "/api/v1/admin/users/{id}/access", authz.PermManageUsers, h.or(h.SetUserAccess))
	rt.Admin(http.MethodPost, "/api/v1/admin/users/{id}/suspend", authz.PermSuspendUser, h.or(h.SuspendUser))
	// Delivery path for password resets while there is no mail transport.
	rt.Admin(http.MethodPost, "/api/v1/admin/users/{id}/reset-link", authz.PermManageUsers, h.or(h.MintResetLink))
	// The roles and what they may do; a role below Admin can be edited, never
	// to hold admin.system, admin.users or admin.network (ADR-0039).
	rt.Admin(http.MethodGet, "/api/v1/admin/roles", authz.PermManageUsers, h.or(h.ListRoles))
	rt.Admin(http.MethodPatch, "/api/v1/admin/roles/{id}", authz.PermManageUsers, h.or(h.EditRole))
	// The metadata provider. Admin-gated on system settings rather than on
	// indexers: this is one instance-wide credential, and the traffic it
	// authorises discloses the whole library rather than one search.
	rt.Admin(http.MethodGet, "/api/v1/admin/metadata", authz.PermSystemSettings, h.or(h.MetadataStatus))
	rt.Admin(http.MethodPut, "/api/v1/admin/metadata/token", authz.PermSystemSettings, h.or(h.SetMetadataToken))
	rt.Admin(http.MethodPost, "/api/v1/admin/metadata/check", authz.PermSystemSettings, h.or(h.CheckMetadata))
	// OpenSubtitles (ADR-0055): its key, account and languages; a file's
	// subtitle fetched; the languages, for whoever may fetch.
	rt.Admin(http.MethodGet, "/api/v1/admin/subtitles", authz.PermSystemSettings, h.or(h.SubtitleStatus))
	rt.Admin(http.MethodPut, "/api/v1/admin/subtitles", authz.PermSystemSettings, h.or(h.ConfigureSubtitles))
	rt.Permission(http.MethodGet, "/api/v1/subtitles/languages", authz.PermEditLibraryItems, h.or(h.SubtitleLanguages))
	rt.Permission(http.MethodPost, "/api/v1/files/{id}/subtitles/fetch", authz.PermEditLibraryItems, h.or(h.FetchSubtitle))
	// Notifications (ADR-0032): one Discord webhook, never read back. Changing
	// the webhook or the categories also needs admin.audit, checked by the
	// notifier: choosing what leaves the audit log is reading it.
	rt.Admin(http.MethodGet, "/api/v1/admin/notifications", authz.PermSystemSettings, h.or(h.NotificationStatus))
	rt.Admin(http.MethodPut, "/api/v1/admin/notifications/webhook", authz.PermSystemSettings, h.or(h.SetNotificationWebhook))
	rt.Admin(http.MethodPut, "/api/v1/admin/notifications/categories", authz.PermSystemSettings, h.or(h.SetNotificationCategories))
	rt.Admin(http.MethodPost, "/api/v1/admin/notifications/test", authz.PermSystemSettings, h.or(h.SendTestNotification))
	// Searching a provider is gated on EDITING library items, not on browsing:
	// it is the lookup an operator runs when correcting what something is, and
	// it spends a request against a third party every time it is called.
	rt.Permission(http.MethodGet, "/api/v1/metadata/search", authz.PermEditLibraryItems, h.or(h.SearchMetadata))

	// --- Identification. Gated on editing library items, because confirming
	// --- one is exactly that: it may rename what an operator browses to.
	rt.Permission(http.MethodGet, "/api/v1/identify/pending", authz.PermEditLibraryItems, h.or(h.PendingIdentifications))
	rt.Permission(http.MethodGet, "/api/v1/identify/{id}", authz.PermEditLibraryItems, h.or(h.GetIdentification))
	rt.Permission(http.MethodPost, "/api/v1/identify/{id}/confirm", authz.PermEditLibraryItems, h.or(h.ConfirmIdentification))
	rt.Permission(http.MethodPost, "/api/v1/identify/{id}/reject", authz.PermEditLibraryItems, h.or(h.RejectIdentification))
	rt.Permission(http.MethodPost, "/api/v1/identify/{id}/reopen", authz.PermEditLibraryItems, h.or(h.ReopenIdentification))
	// Running a pass spends requests against a third party and is a whole-library
	// operation, so it sits with the other administrative triggers.
	rt.Admin(http.MethodPost, "/api/v1/admin/identify/run", authz.PermSystemSettings, h.or(h.RunIdentification))

	// --- Playback. Watching something is browsing, and the bytes are
	// --- authorized by the session like every other route: the player is
	// --- same-origin, so a <video> request carries the cookie (ADR-0020).
	// --- There is no signed URL and no token in a query string, so there is
	// --- one authorization model rather than two and revoking a session
	// --- revokes playback with it.
	rt.Permission(http.MethodGet, "/api/v1/files/{id}/playback", authz.PermBrowse, h.or(h.PlaybackInfo))
	rt.Permission(http.MethodGet, "/api/v1/files/{id}/stream", authz.PermBrowse, h.or(h.StreamFile))
	// Where somebody got to. PUT because it is idempotent and the player sends
	// it on a throttle and again as the page unloads, so duplicates are normal.
	rt.Permission(http.MethodPut, "/api/v1/files/{id}/position", authz.PermBrowse, h.or(h.SavePosition))
	rt.Permission(http.MethodDelete, "/api/v1/files/{id}/position", authz.PermBrowse, h.or(h.ForgetPosition))
	// Converting a file so a browser can play it: the video is copied and only
	// the audio is rebuilt (ADR-0020). Browse, like the stream it replaces —
	// it is the same file, made openable. The admission limit is what bounds
	// the cost, not the permission.
	rt.Permission(http.MethodGet, "/api/v1/files/{id}/convert", authz.PermBrowse, h.or(h.ConvertFile))
	// Subtitles the library already holds: tracks inside the container and
	// sidecars beside the file. Fetching FROM a provider is a separate thing
	// and is not built. The {sid} is an opaque handle this server issued and
	// resolves against its own listing; it is never a filename (ADR-0020).
	rt.Permission(http.MethodGet, "/api/v1/files/{id}/subtitles", authz.PermBrowse, h.or(h.ListSubtitles))
	rt.Permission(http.MethodGet, "/api/v1/files/{id}/subtitles/{sid}", authz.PermBrowse, h.or(h.ServeSubtitle))

	// --- Migrating from another application. Administrative, because it reads
	// --- a file from the host and writes identities across the whole library
	// --- (ADR-0021). The source is named by FILENAME, resolved inside the
	// --- migration directory through os.Root; there is no upload, and no path
	// --- from a request reaches the filesystem.
	rt.Admin(http.MethodGet, "/api/v1/admin/migrate/sources", authz.PermSystemSettings, h.or(h.MigrationSources))
	rt.Admin(http.MethodPost, "/api/v1/admin/migrate/radarr", authz.PermSystemSettings, h.or(h.MigrateFromRadarr))

	// Artwork is served from this instance rather than linked to the provider:
	// a page of provider URLs would make the operator's own browser announce
	// their library to a third party, once per row (ADR-0018). Browse, because
	// a poster is part of looking at a library.
	rt.Permission(http.MethodGet, "/api/v1/artwork/poster/{provider}/{id}", authz.PermBrowse, h.or(h.Poster))
	rt.Admin(http.MethodGet, "/api/v1/admin/indexers", authz.PermManageIndexers, h.or(h.ListIndexers))
	rt.Admin(http.MethodPost, "/api/v1/admin/indexers", authz.PermManageIndexers, h.or(h.CreateIndexer))
	rt.Admin(http.MethodPatch, "/api/v1/admin/indexers/{id}", authz.PermManageIndexers, h.or(h.UpdateIndexer))
	rt.Admin(http.MethodDelete, "/api/v1/admin/indexers/{id}", authz.PermManageIndexers, h.or(h.DeleteIndexer))
	rt.Admin(http.MethodGet, "/api/v1/admin/egress", authz.PermManageNetwork, h.or(h.EgressStatus))
	rt.Admin(http.MethodPatch, "/api/v1/admin/egress", authz.PermManageNetwork, h.or(h.EgressUpdate))
	rt.Admin(http.MethodPost, "/api/v1/admin/egress/leak-test", authz.PermManageNetwork, h.or(h.EgressLeakTest))
	// The SOCKS5 proxy, set from the web with the password and a fresh code
	// (ADR-0065). Everything else about egress stays in the file.
	// Session-only as well as hidden: it asks for the password and a code, and
	// an API token is not the person who knows them.
	rt.register(Route{Method: http.MethodPut, Pattern: "/api/v1/admin/egress/proxy",
		Access: AccessPermission, Permission: authz.PermManageNetwork, Hidden: true, SessionOnly: true},
		h.or(h.SetEgressProxy))
	// Reading where the library lives is browsing; changing it is not. The
	// listing is gated on PermBrowse inside the store, so it is registered
	// here at the permission that lets a Manager fill in a form — the store
	// still refuses anything beyond reading.
	rt.Permission(http.MethodGet, "/api/v1/rootfolders", authz.PermBrowse, h.or(h.ListRootFolders))
	rt.Admin(http.MethodGet, "/api/v1/admin/rootfolders", authz.PermManageRootFolders, h.or(h.ListRootFolders))
	rt.Admin(http.MethodPost, "/api/v1/admin/rootfolders", authz.PermManageRootFolders, h.or(h.CreateRootFolder))
	rt.Admin(http.MethodDelete, "/api/v1/admin/rootfolders/{id}", authz.PermManageRootFolders, h.or(h.DeleteRootFolder))
	rt.Admin(http.MethodPost, "/api/v1/admin/rootfolders/{id}/refresh", authz.PermManageRootFolders, h.or(h.RefreshRootFolder))
	// Reading the library is browsing; asking the instance to walk an
	// operator's disks is not, so a scan is gated on the permission that
	// configures where those disks are.
	rt.Admin(http.MethodPost, "/api/v1/admin/rootfolders/{id}/scan", authz.PermManageRootFolders, h.or(h.ScanRootFolder))
	rt.Admin(http.MethodDelete, "/api/v1/admin/media/{id}", authz.PermDeleteMediaFiles, h.or(h.DeleteMedia))
	// The trash is the undo. Reading it needs only the permission that could
	// have created it; restoring puts a file back where it belongs.
	rt.Admin(http.MethodGet, "/api/v1/admin/trash", authz.PermDeleteMediaFiles, h.or(h.ListTrash))
	rt.Admin(http.MethodPost, "/api/v1/admin/trash/restore", authz.PermDeleteMediaFiles, h.or(h.RestoreFromTrash))
	// One file to the trash, and one trashed file unlinked now (ADR-0053).
	rt.Admin(http.MethodDelete, "/api/v1/admin/files/{id}", authz.PermDeleteMediaFiles, h.or(h.DeleteFile))
	rt.Admin(http.MethodPost, "/api/v1/admin/trash/purge", authz.PermDeleteMediaFiles, h.or(h.PurgeTrash))
	// Reading the audit log (ADR-0031): a page at a time, and counts. There is
	// no route that changes or removes a row.
	rt.Admin(http.MethodGet, "/api/v1/admin/audit", authz.PermViewAuditLog, h.or(h.ListAudit))
	rt.Admin(http.MethodGet, "/api/v1/admin/audit/summary", authz.PermViewAuditLog, h.or(h.AuditSummary))
	// The settings are read here and changed in the file (ADR-0040): the PATCH
	// is a permanent, explained refusal, as the egress policy's is.
	rt.Admin(http.MethodGet, "/api/v1/admin/system/settings", authz.PermSystemSettings, h.or(h.SystemSettings))
	rt.Admin(http.MethodPatch, "/api/v1/admin/system/settings", authz.PermSystemSettings, h.ChangeSystemSettings)
	rt.Admin(http.MethodGet, "/api/v1/admin/system/tasks", authz.PermSystemSettings, h.or(h.ListTasks))
	rt.Admin(http.MethodPost, "/api/v1/admin/system/tasks/{name}/run", authz.PermSystemSettings, h.or(h.TriggerTask))
	// Taken and listed here; never downloaded and never restored here
	// (ADR-0029). A download route would let a stolen administrator session
	// carry off the whole database in one request, and a restore route would
	// let it roll the instance back — suspended accounts live again, old
	// passwords valid again, the audit log rewound.
	rt.Admin(http.MethodPost, "/api/v1/admin/system/backup", authz.PermSystemSettings, h.or(h.TakeBackup))
	rt.Admin(http.MethodPost, "/api/v1/admin/system/restart", authz.PermSystemSettings, h.or(h.Restart))
	rt.Admin(http.MethodGet, "/api/v1/admin/system/backups", authz.PermSystemSettings, h.or(h.ListBackups))
	// The process's recent log records, redacted, and every health check at
	// once (ADR-0040). /healthz stays on the management listener and says ok.
	rt.Admin(http.MethodGet, "/api/v1/admin/system/logs", authz.PermSystemSettings, h.or(h.SystemLogs))
	rt.Admin(http.MethodGet, "/health/detail", authz.PermSystemSettings, h.or(h.HealthDetail))
}

// ---------------------------------------------------------------------------
// handlers
// ---------------------------------------------------------------------------

// notImplemented is an honest placeholder: the route exists, its access class
// is enforced, and its business logic is not written yet.
func notImplemented(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, http.StatusNotImplemented, "not implemented in this phase")
}

// healthz is the liveness probe. It reports nothing: no version, no counts, no
// user information (requirements §7.1). It is also bound to the management
// listener, so it is not reachable from the public one.
func healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// page renders one of the HTML shells and issues the CSRF token the page will
// need in order to submit anything.
//
// The shell carries no user data — see the web package's doc comment. It gets
// the password policy so the form's hint cannot drift from the rule the server
// enforces, and nothing else.
func (h *Handlers) page(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := web.PageData{}
		if h != nil {
			issueCSRFCookie(w, h.auth.secure)
			data.MinPasswordLength = h.svc.Policy().Password.MinLength
			data.SourceURL, data.Version = h.sourceURL, h.version
		}
		if err := web.Render(w, name, data); err != nil {
			logging.FromContext(r.Context()).Error("rendering page",
				slog.String("page", name), slog.Any("error", err))
			writeProblem(w, http.StatusInternalServerError, "internal error")
		}
	}
}

// loginPage is the shell at /login, with one addition: somebody who already has
// a working session is sent to the application instead of being shown a form
// that would only tell them they are already signed in.
func (h *Handlers) loginPage(w http.ResponseWriter, r *http.Request) {
	if p := authz.FromContext(r.Context()); p != nil && p.CanAct() {
		redirectTo(w, "/")
		return
	}
	h.page("login")(w, r)
}

// problem is the single error shape returned by the API. It carries no detail
// that would help an attacker distinguish failure modes.
type problem struct {
	Error string `json:"error"`
}

func writeProblem(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Error: msg})
}

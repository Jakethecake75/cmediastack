package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/egressproxy"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/logging"
	qrcode "github.com/skip2/go-qrcode"
)

// maxRequestBody bounds every JSON body. Without a cap, an anonymous endpoint
// that parses JSON is a memory-exhaustion primitive.
const maxRequestBody = 64 << 10 // 64 KiB

// Handlers holds the dependencies the real handlers need.
//
// A nil *Handlers is valid: RegisterRoutes then wires every route to
// notImplemented while keeping its access class enforced. That is what lets
// the route-enumeration test cover the whole surface before the business logic
// behind it exists.
type Handlers struct {
	proxy     *egressproxy.Controller
	restart   func()
	svc       *identity.Service
	auth      *SessionAuthenticator
	tasks     TaskScheduler
	egress    EgressController
	indexers  IndexerService
	search    SearchService
	profiles  ProfileSource
	downloads DownloadEngine
	roots     RootFolderService
	media     MediaService
	scanner   LibraryScanner
	deleter   MediaDeleter
	// trashRetention is how long a deleted file stays recoverable.
	trashRetention time.Duration
	tickets        TicketSealer
	grabs          GrabService
	requests       RequestService
	metadata       MetadataService
	identify       IdentifyService
	playback       PlaybackService
	migrate        MigrateService
	episodes       EpisodeService
	refresher      EpisodeRefresher
	altTitles      AlternativeTitleSource
	filmTitles     FilmTitleSource
	searchSoon     func(string) bool
	adder          LibraryAdder
	artwork        ArtworkReader
	backups        BackupService
	acquisition    AcquisitionService
	notifications  NotificationService
	audit          *audit.Logger
	health         *HealthParts
	logs           *logging.Ring
	settings       func() (map[string]any, error)
	calendar       CalendarSource
	arrivals       ArrivalSource
	baseURL        string
	issues         IssueService
	discover       DiscoverSource
	discoverCache  *discoverCache
	music          MusicService
	books          BookService
	// sourceURL and version are every page's offer of the source (ADR-0052).
	sourceURL string
	version   string
	subtitles SubtitleService
}

// Deps is everything the handlers need.
//
// A struct rather than seventeen positional parameters, and the reason is not
// tidiness. Several of these are interfaces that one concrete type satisfies —
// *importer.Importer is passed as BOTH the scanner and the deleter — so two
// adjacent arguments could be swapped and the code would still compile and
// still run, with the wrong dependency answering. Named fields make that
// mistake impossible to write rather than merely unlikely, and a new dependency
// stops being a change to every call site.
type Deps struct {
	Identity  *identity.Service
	Auth      *SessionAuthenticator
	Tasks     TaskScheduler
	Egress    EgressController
	Indexers  IndexerService
	Search    SearchService
	Profiles  ProfileSource
	Downloads DownloadEngine
	Roots     RootFolderService
	Media     MediaService
	Scanner   LibraryScanner
	Deleter   MediaDeleter
	Tickets   TicketSealer
	Grabs     GrabService
	Requests  RequestService
	Metadata  MetadataService
	Identify  IdentifyService
	// Playback serves media bytes and reports what is in a file. Nil means the
	// instance plays nothing, which is a supported configuration.
	Playback PlaybackService
	// Migrate reads another application's database. Nil means the instance
	// offers no migration, and the routes answer 501.
	Migrate MigrateService
	// Episodes answers what a series contains and what it is missing.
	Episodes EpisodeService
	// Refresher asks the provider for a series' episode list.
	Refresher EpisodeRefresher
	// AlternativeTitles names what else a series is called, so an episode
	// search recognises the scene's spelling of it. Nil means only a series'
	// own title is recognised, and the search says so.
	AlternativeTitles AlternativeTitleSource
	// FilmTitles names every title a film goes by — its original title and its
	// alternatives — so a film search recognises the names releases use
	// (ADR-0026). Nil means only the film's own title is recognised, and the
	// search says so.
	FilmTitles FilmTitleSource
	// SearchSoon starts automatic acquisition's pass for what was just added
	// — "search", "albums" or "books" — and reports whether it did (ADR-0071).
	// Nil, as when automatic acquisition is off, starts nothing.
	SearchSoon func(task string) bool
	// Adder puts a title in the library before any of it is on disk
	// (ADR-0025, ADR-0026). Nil means nothing can be added, and the route
	// answers 501.
	Adder LibraryAdder
	// Artwork reads the poster cache. Nil means the instance serves no
	// pictures, which is a supported configuration.
	Artwork ArtworkReader
	// Backups takes and lists the database's encrypted backups (ADR-0029).
	// Nil means the routes answer 501.
	Backups BackupService
	// Acquisition reports what automatic acquisition last did about each
	// wanted item (ADR-0030). Nil means it is off, and the Wanted screen says
	// so.
	Acquisition AcquisitionService
	// Notifications is the Discord webhook and what is sent to it
	// (ADR-0032). Nil means the routes answer 501.
	Notifications NotificationService
	Audit         *audit.Logger
	// Health is what the detailed health report needs beyond the other
	// dependencies (ADR-0040). Nil means the route answers 501.
	Health *HealthParts
	// Logs is the process's recent, redacted log records (ADR-0040).
	Logs *logging.Ring
	// Settings answers the effective configuration (ADR-0040).
	Settings func() (map[string]any, error)
	// Calendar and Arrivals feed the calendar and the feed (ADR-0041);
	// BaseURL makes their addresses absolute.
	Calendar CalendarSource
	Arrivals ArrivalSource
	BaseURL  string
	// Issues are problems people report with titles (ADR-0042).
	Issues IssueService
	// Discover lists what is popular (ADR-0043).
	Discover DiscoverSource
	// Music is artists, albums and tracks (ADR-0044).
	Music MusicService
	// Books are added from Open Library (ADR-0048).
	Books BookService
	// SourceURL and Version are the footer's offer of this build's source
	// (ADR-0052). An empty SourceURL renders no footer.
	SourceURL string
	Version   string
	// Subtitles fetches a file's subtitle from OpenSubtitles (ADR-0055).
	Subtitles SubtitleService
	// Proxy is the SOCKS5 proxy set from the web, and Restart stops the
	// process gracefully so its supervisor starts it again (ADR-0065).
	Proxy   *egressproxy.Controller
	Restart func()

	// TrashRetention is how long a deleted file stays recoverable. Zero is
	// replaced with the safe default rather than honoured; see New.
	TrashRetention time.Duration
}

// New builds the handler set.
//
// Every dependency except Identity and Auth may be nil, in which case the
// routes that need it report that plainly rather than pretending to work. That
// is deliberate: the download engine in particular is off by default, and a
// queue endpoint that silently returned an empty list when no engine is running
// would read as "nothing is downloading" rather than "nothing can download".
func New(d Deps) *Handlers {
	if d.TrashRetention <= 0 {
		// A zero retention would make every deletion immediate, which is the
		// design the trash exists to avoid. The configuration lint already
		// refuses it; this is the second line of defence.
		d.TrashRetention = 7 * 24 * time.Hour
	}
	return &Handlers{
		proxy: d.Proxy, restart: d.Restart,
		svc: d.Identity, auth: d.Auth, tasks: d.Tasks, egress: d.Egress,
		indexers: d.Indexers, search: d.Search, profiles: d.Profiles,
		downloads: d.Downloads, roots: d.Roots, media: d.Media,
		scanner: d.Scanner, deleter: d.Deleter, trashRetention: d.TrashRetention,
		tickets: d.Tickets, grabs: d.Grabs, requests: d.Requests,
		metadata: d.Metadata, identify: d.Identify, artwork: d.Artwork,
		playback:      d.Playback,
		migrate:       d.Migrate,
		episodes:      d.Episodes,
		refresher:     d.Refresher,
		altTitles:     d.AlternativeTitles,
		filmTitles:    d.FilmTitles,
		searchSoon:    d.SearchSoon,
		adder:         d.Adder,
		backups:       d.Backups,
		acquisition:   d.Acquisition,
		notifications: d.Notifications,
		audit:         d.Audit,
		health:        d.Health,
		logs:          d.Logs,
		settings:      d.Settings,
		calendar:      d.Calendar,
		arrivals:      d.Arrivals,
		baseURL:       d.BaseURL,
		issues:        d.Issues,
		discover:      d.Discover,
		discoverCache: &discoverCache{},
		music:         d.Music,
		books:         d.Books,
		sourceURL:     d.SourceURL,
		version:       d.Version,
		subtitles:     d.Subtitles,
	}
}

// or returns f when handlers are wired, and notImplemented when they are not.
// It is safe on a nil receiver.
func (h *Handlers) or(f http.HandlerFunc) http.HandlerFunc {
	if h == nil {
		return notImplemented
	}
	return f
}

// decodeJSON reads a size-capped, strictly-typed body.
//
// DisallowUnknownFields is mass-assignment protection: a body carrying
// "role_id" into a profile update is rejected outright rather than silently
// ignored, so the attempt is visible instead of being a near miss.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeProblem(w, http.StatusBadRequest, "malformed request")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// No API response is cacheable. Several of them carry a secret shown
	// exactly once — a TOTP secret, recovery codes, an invite code, a token —
	// and the rest carry somebody's account details. A shared cache holding any
	// of that is a leak nobody would think to look for.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---------------------------------------------------------------------------
// First-run setup
// ---------------------------------------------------------------------------

// SetupPage serves the wizard, or 404 once an account exists.
//
// The check runs per request rather than at boot, so the wizard closes the
// instant the first administrator is created. There is no default account and
// no bootstrap credential written to disk or logs.
func (h *Handlers) SetupPage(w http.ResponseWriter, r *http.Request) {
	needed, err := h.svc.SetupNeeded(r.Context())
	if err != nil || !needed {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	h.page("setup")(w, r)
}

type setupRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Setup creates the first administrator.
func (h *Handlers) Setup(w http.ResponseWriter, r *http.Request) {
	var in setupRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	id, err := h.svc.CreateFirstAdmin(r.Context(), in.Username, in.Email, in.Password,
		ClientIP(r.Context()), r.UserAgent())
	switch {
	case errors.Is(err, identity.ErrSetupComplete):
		// Indistinguishable from a route that does not exist.
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, identity.ErrPasswordTooShort),
		errors.Is(err, identity.ErrPasswordTooSimple),
		errors.Is(err, identity.ErrPasswordBreached),
		errors.Is(err, identity.ErrPasswordTooLong):
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeProblem(w, http.StatusBadRequest, "could not create the administrator")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"user_id": id,
		"next":    "log in, then enroll an authenticator",
	})
}

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login verifies a password and issues a session that cannot yet act.
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var in loginRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	res, err := h.svc.Login(r.Context(), in.Username, in.Password,
		ClientIP(r.Context()), r.UserAgent())
	switch {
	case errors.Is(err, identity.ErrThrottled):
		w.Header().Set("Retry-After", "900")
		writeProblem(w, http.StatusTooManyRequests, "too many attempts")
		return
	case err != nil:
		// One message for every failure mode: unknown account, wrong password,
		// suspended, disabled. Anything else is an enumeration oracle.
		writeProblem(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	h.auth.SetSessionCookie(w, res.Cookie)
	issueCSRFCookie(w, h.auth.secure)

	next := "mfa"
	if res.NeedsEnrollment {
		next = "enroll"
	}
	writeJSON(w, http.StatusOK, map[string]any{"next": next})
}

type mfaRequest struct {
	Code string `json:"code"`
}

// LoginMFA completes a sign-in with an authenticator or recovery code.
//
// It is an anonymous route in the router's terms because the principal it
// operates on is not yet able to act. The session cookie issued by Login is
// what identifies the caller.
func (h *Handlers) LoginMFA(w http.ResponseWriter, r *http.Request) {
	var in mfaRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	cookie, err := r.Cookie(identity.SessionCookieName)
	if err != nil || cookie.Value == "" {
		writeProblem(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	store := h.svc.Store()
	sess, user, _, err := store.ResolveSession(r.Context(), cookie.Value, h.svc.Policy().Session)
	if err != nil {
		writeProblem(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	err = h.svc.VerifyMFA(r.Context(), sess, user, in.Code, ClientIP(r.Context()), r.UserAgent())
	switch {
	case errors.Is(err, identity.ErrThrottled):
		w.Header().Set("Retry-After", "900")
		writeProblem(w, http.StatusTooManyRequests, "too many attempts")
		return
	case err != nil:
		writeProblem(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"next": "app"})
}

// Logout revokes the current session.
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	p := authz.FromContext(r.Context())
	if p == nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	_ = h.svc.Logout(r.Context(), p.SessionID, p.UserID, p.Username)
	h.auth.ClearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// Signup
// ---------------------------------------------------------------------------

type signupRequest struct {
	Username   string `json:"username"`
	Email      string `json:"email"`
	Password   string `json:"password"`
	Note       string `json:"note"`
	InviteCode string `json:"invite_code"`
	// The solved proof-of-work challenge (ADR-0051).
	PowChallenge string `json:"pow_challenge"`
	PowCounter   string `json:"pow_counter"`
}

// SignupChallenge hands the signup page a proof-of-work challenge, or says
// none is needed. A closed registration answers as signup does.
func (h *Handlers) SignupChallenge(w http.ResponseWriter, r *http.Request) {
	token, bits, err := h.svc.SignupChallenge()
	switch {
	case errors.Is(err, identity.ErrRegistrationClosed):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"challenge": token, "bits": bits})
}

// Signup records an account request.
//
// The response is identical whether or not the address is already registered,
// and whether or not the request was stored. The signup form must not be a
// user-enumeration oracle (§7.2).
func (h *Handlers) Signup(w http.ResponseWriter, r *http.Request) {
	var in signupRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	res, err := h.svc.Signup(r.Context(), identity.SignupInput{
		Username:   in.Username,
		Email:      in.Email,
		Password:   in.Password,
		Note:       in.Note,
		InviteCode: in.InviteCode,
		SourceIP:   ClientIP(r.Context()),
		UserAgent:  r.UserAgent(),

		ProofChallenge: in.PowChallenge,
		ProofCounter:   in.PowCounter,
	})
	switch {
	case errors.Is(err, identity.ErrProofRequired), errors.Is(err, identity.ErrProofInvalid):
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, identity.ErrRegistrationClosed):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, identity.ErrInviteInvalid):
		// Safe to report: the applicant supplied the code, so this tells them
		// nothing they did not already know.
		writeProblem(w, http.StatusBadRequest, "invalid or expired invite code")
		return
	case errors.Is(err, identity.ErrQueueFull):
		w.Header().Set("Retry-After", "3600")
		writeProblem(w, http.StatusTooManyRequests, "the request queue is full; try again later")
		return
	case errors.Is(err, identity.ErrPasswordTooShort),
		errors.Is(err, identity.ErrPasswordTooSimple),
		errors.Is(err, identity.ErrPasswordBreached),
		errors.Is(err, identity.ErrPasswordTooLong):
		// The applicant supplied this and can see it, so it is safe to report.
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}

	if res.Approved {
		// An auto-approving invite created the account outright.
		writeJSON(w, http.StatusCreated, map[string]any{
			"status":  "approved",
			"message": "your account is ready; log in and enroll an authenticator",
		})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":  "submitted",
		"message": "your request has been submitted for review",
	})
}

// ---------------------------------------------------------------------------
// MFA enrollment
// ---------------------------------------------------------------------------

// EnrollBegin returns a candidate secret and provisioning URI.
func (h *Handlers) EnrollBegin(w http.ResponseWriter, r *http.Request) {
	p := authz.FromContext(r.Context())
	if p == nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	user, err := h.svc.Store().UserByID(r.Context(), p.UserID)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}

	offer, err := h.svc.BeginEnrollment(user)
	if errors.Is(err, identity.ErrAlreadyEnrolled) {
		writeProblem(w, http.StatusConflict, "already enrolled")
		return
	}
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}

	// The QR is generated here rather than at a URL of its own. A second
	// endpoint serving the secret as an image would be another route to get the
	// authenticator seed, with its own access class to get wrong; a data URI in
	// this response reaches exactly the caller who was already handed the
	// secret in plain text on the line above.
	//
	// Medium recovery: the code is read off a screen at arm's length, not off a
	// scuffed parcel. Higher recovery would make it physically larger for no
	// gain here.
	qr, err := qrcode.Encode(offer.URI, qrcode.Medium, 320)
	if err != nil {
		// Not fatal. Manual key entry is a complete path on its own, and
		// failing enrollment because a picture could not be drawn would be a
		// worse outcome than showing the key alone.
		logging.FromContext(r.Context()).Warn("could not render the enrollment QR code",
			slog.Any("error", err))
		qr = nil
	}

	// The secret is returned to the client and echoed back on confirm. It is
	// not stored until a correct code proves the authenticator actually has
	// it — with mandatory MFA, storing it earlier could lock the user out of
	// their own account permanently.
	body := map[string]any{
		"secret": offer.Secret,
		"uri":    offer.URI,
	}
	if qr != nil {
		body["qr"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(qr)
	}
	writeJSON(w, http.StatusOK, body)
}

type enrollConfirmRequest struct {
	Secret string `json:"secret"`
	Code   string `json:"code"`
}

// EnrollConfirm verifies the code, activates the account and returns the
// recovery codes once.
func (h *Handlers) EnrollConfirm(w http.ResponseWriter, r *http.Request) {
	p := authz.FromContext(r.Context())
	if p == nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	var in enrollConfirmRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	user, err := h.svc.Store().UserByID(r.Context(), p.UserID)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}

	codes, err := h.svc.CompleteEnrollment(r.Context(), user, in.Secret, in.Code,
		ClientIP(r.Context()), r.UserAgent())
	switch {
	case errors.Is(err, identity.ErrAlreadyEnrolled):
		writeProblem(w, http.StatusConflict, "already enrolled")
		return
	case err != nil:
		writeProblem(w, http.StatusUnauthorized, "invalid code")
		return
	}

	// The account is now active, but this session was issued before the second
	// factor existed. Promoting it here avoids forcing an immediate re-login
	// while still meaning the session has demonstrably satisfied MFA.
	_ = h.svc.Store().MarkSessionMFASatisfied(r.Context(), p.SessionID)

	writeJSON(w, http.StatusOK, map[string]any{
		"recovery_codes": codes,
		"message":        "store these now; they are not shown again",
	})
}

// ---------------------------------------------------------------------------
// Self
// ---------------------------------------------------------------------------

// Me returns the caller's own profile.
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	p := authz.FromContext(r.Context())
	if p == nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":             p.UserID,
		"username":       p.Username,
		"role":           p.Role.Name,
		"state":          string(p.State),
		"permissions":    permissionStrings(p.Role.Permissions.Slice()),
		"library_ids":    p.LibraryIDs,
		"all_libraries":  p.UnrestrictedLibraries,
		"rating_ceiling": p.RatingCeiling,
	})
}

func permissionStrings(in []authz.Permission) []string {
	out := make([]string, len(in))
	for i, p := range in {
		out[i] = string(p)
	}
	return out
}

// ---------------------------------------------------------------------------
// Account approval
// ---------------------------------------------------------------------------

// AssignableRoles lists the roles the caller may grant at approval time.
//
// The list is produced by running the real assignment guard once per role, not
// by reimplementing its rule. A UI built from this cannot offer an option the
// server would refuse, and — the failure that actually goes unnoticed — cannot
// quietly stop offering one the server would have allowed.
func (h *Handlers) AssignableRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.svc.AssignableRoles(r.Context())
	if err != nil {
		writeAuthzAware(w, err)
		return
	}

	out := make([]map[string]any, 0, len(roles))
	for _, role := range roles {
		out = append(out, map[string]any{
			"id":          role.ID,
			"name":        role.Name,
			"rank":        role.Rank,
			"builtin":     role.Builtin,
			"permissions": permissionStrings(role.Permissions.Slice()),
		})
	}
	// The rating ceilings the same form chooses from (ADR-0037), with what
	// each admits, so the form's labels cannot drift from the ranks.
	writeJSON(w, http.StatusOK, map[string]any{"roles": out, "count": len(out),
		"rating_ceilings": ratingCeilingsJSON()})
}

// PendingAccounts lists the approval queue. This is the in-app dashboard.
func (h *Handlers) PendingAccounts(w http.ResponseWriter, r *http.Request) {
	reqs, err := h.svc.Store().PendingRequests(r.Context(), 100)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}

	out := make([]map[string]any, 0, len(reqs))
	for _, req := range reqs {
		out = append(out, map[string]any{
			"id":         req.ID,
			"username":   req.Username,
			"email":      req.Email,
			"note":       req.Note,
			"source_ip":  req.SourceIP,
			"user_agent": req.UserAgent,
			"created_at": req.CreatedAt,
			"expires_at": req.ExpiresAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out, "count": len(out)})
}

type approveRequest struct {
	RoleID int64 `json:"role_id"`
	// AllLibraries absent with no library_ids is every library; library_ids
	// are root folders (ADR-0037).
	AllLibraries  *bool   `json:"all_libraries"`
	LibraryIDs    []int64 `json:"library_ids"`
	RatingCeiling int     `json:"rating_ceiling"`
}

// ApproveAccount promotes a request into a user.
func (h *Handlers) ApproveAccount(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	var in approveRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	grant, err := identity.NormaliseGrant(in.AllLibraries, in.LibraryIDs, in.RatingCeiling)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "identity: "))
		return
	}
	userID, err := h.svc.ApproveRequest(r.Context(), identity.ApprovalInput{
		RequestID:     id,
		RoleID:        in.RoleID,
		AllLibraries:  grant.AllLibraries,
		LibraryIDs:    grant.RootFolderIDs,
		RatingCeiling: grant.RatingCeiling,
		SourceIP:      ClientIP(r.Context()),
		UserAgent:     r.UserAgent(),
	})
	if errors.Is(err, identity.ErrNoSuchRootFolder) {
		writeProblem(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "identity: "))
		return
	}
	if errors.Is(err, identity.ErrAccountExists) {
		// Reported plainly. The approver can see the applicant's name and the
		// user list already, and "internal error" would leave them clicking a
		// button that silently never works.
		writeProblem(w, http.StatusConflict, "that username or email is already registered")
		return
	}
	if err != nil {
		writeAuthzAware(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"user_id": userID,
		"state":   string(authz.StateAwaitingMFA),
		"message": "the account must enroll an authenticator before it can be used",
	})
}

type denyRequest struct {
	Reason string `json:"reason"`
}

// DenyAccount refuses a request.
func (h *Handlers) DenyAccount(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	var in denyRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := h.svc.DenyRequest(r.Context(), id, in.Reason,
		ClientIP(r.Context()), r.UserAgent()); err != nil {
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// SuspendUser freezes an account and kills its sessions.
func (h *Handlers) SuspendUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	var in denyRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	err = h.svc.SuspendUser(r.Context(), id, in.Reason,
		ClientIP(r.Context()), r.UserAgent())
	switch {
	case errors.Is(err, identity.ErrLastAdministrator):
		// 409 rather than the generic failure writeAuthzAware would give: the
		// actor has the authority and the instance is refusing because the
		// outcome would be unrecoverable. An operator needs to tell those apart,
		// and "internal error" would send them looking for a bug.
		writeProblem(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// writeAuthzAware renders an authorization denial as 404 and everything else
// as a generic failure, so the response never distinguishes "you may not" from
// "it does not exist".
func writeAuthzAware(w http.ResponseWriter, err error) {
	// A title out of the caller's scope reads as absent (ADR-0037), so the
	// stores' not-found errors are answered as the route's own would be.
	if authz.IsDenied(err) || errors.Is(err, identity.ErrNotFound) ||
		errors.Is(err, library.ErrNotFound) || errors.Is(err, importer.ErrItemNotFound) ||
		errors.Is(err, importer.ErrFileNotFound) {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	writeProblem(w, http.StatusInternalServerError, "internal error")
}

// issueCSRFCookie sets the double-submit token on the pages that will submit a
// state-changing request.
func issueCSRFCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{ // #nosec G124 -- not HttpOnly by design (below); Secure as for the session cookie
		Name:  CSRFCookieName,
		Value: randomHex(16),
		Path:  "/",
		// Deliberately NOT HttpOnly: the page's script has to read it to put
		// it in the request header. That is the double-submit pattern, and it
		// is safe because the value is meaningless without the session cookie,
		// which IS HttpOnly.
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// startSearch starts automatic acquisition's pass for what was just added, and
// reports whether it did (ADR-0071).
func (h *Handlers) startSearch(task string) bool {
	return h.searchSoon != nil && h.searchSoon(task)
}

// searchingNote is what an added title's answer says about getting it.
func searchingNote(searching bool, what string) string {
	if searching {
		return "Searching the indexers for it now: the best release the default quality profile " +
			"accepts is grabbed, and Downloads shows it. Nothing was created on disk yet; the " +
			"folder appears when the " + what + " is imported."
	}
	return ""
}

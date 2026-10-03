// Command cmediastack is the CMediaStack server.
//
// One binary, one database, one configuration. The --role flag selects which
// part of the system this process runs (ADR-0007); the default is the main
// application.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"log/slog"

	"github.com/jakethecake75/cmediastack/internal/acquire"
	"github.com/jakethecake75/cmediastack/internal/api"
	"github.com/jakethecake75/cmediastack/internal/artwork"
	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/books"
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/egress"
	"github.com/jakethecake75/cmediastack/internal/egressproxy"
	"github.com/jakethecake75/cmediastack/internal/follow"
	"github.com/jakethecake75/cmediastack/internal/identify"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/issue"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/migrate"
	"github.com/jakethecake75/cmediastack/internal/music"
	"github.com/jakethecake75/cmediastack/internal/notify"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/backup"
	"github.com/jakethecake75/cmediastack/internal/platform/config"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/platform/logging"
	"github.com/jakethecake75/cmediastack/internal/platform/metrics"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
	"github.com/jakethecake75/cmediastack/internal/platform/tasks"
	"github.com/jakethecake75/cmediastack/internal/playback"
	"github.com/jakethecake75/cmediastack/internal/release"
	"github.com/jakethecake75/cmediastack/internal/request"
	"github.com/jakethecake75/cmediastack/internal/search"
	"github.com/jakethecake75/cmediastack/internal/subtitles"
	"github.com/jakethecake75/cmediastack/internal/tv"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	err := run()
	switch exitCode(err) {
	case 0, 3:
	case 78:
		// Configuration problems get the full list, because fixing them one
		// boot at a time is miserable.
		le, _ := config.AsLintError(err)
		fmt.Fprintln(os.Stderr, le.Error())
	default:
		fmt.Fprintf(os.Stderr, "cmediastack: %v\n", err)
	}
	os.Exit(exitCode(err))
}

// errRestart ends runApp when the web asked for a restart (ADR-0065).
var errRestart = errors.New("restart requested")

// exitCode is the process's exit status for run's result: 3 for a restart,
// which systemd's Restart=always and Docker's unless-stopped both start
// again; 78 (EX_CONFIG) for a configuration the lint refused.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, errRestart) {
		return 3
	}
	if _, ok := config.AsLintError(err); ok {
		return 78
	}
	return 1
}

// restartable wraps the signal context: trigger ends it the way a signal
// does, and requested says whether it was a restart rather than a signal.
func restartable(parent context.Context) (ctx context.Context, trigger func(), requested func() bool) {
	ctx, cancel := context.WithCancel(parent)
	var asked atomic.Bool
	return ctx, func() { asked.Store(true); cancel() }, asked.Load
}

func run() error {
	var (
		configPath  = flag.String("config", "/config/config.yaml", "path to the configuration file")
		role        = flag.String("role", "app", "process role: app | downloader")
		showVersion = flag.Bool("version", false, "print the version and exit")
		checkOnly   = flag.Bool("check", false, "validate configuration and exit")
		healthCheck = flag.Bool("healthcheck", false,
			"probe the running instance's readiness endpoint and exit; for a container healthcheck")

		// Break-glass recovery. See internal/identity/recovery.go: an instance
		// has exactly one administrator, so a lost authenticator with lost
		// recovery codes is otherwise unrecoverable. Deliberately on the host
		// rather than behind a route — using it needs a shell, the data
		// directory and the master key, which is stronger authentication than
		// anything this application can offer, and it adds no reachable surface.
		recoverUser = flag.String("recover", "",
			"reset this account's authenticator from the host console and exit")
		recoverPassword = flag.Bool("recover-password", false,
			"with -recover, also read a new password from stdin (never from a flag)")

		// Backups (ADR-0029). On the host for the same reasons as -recover:
		// they need the master key, and a restore reachable over HTTP would let
		// a stolen administrator session roll the instance back.
		verifyBackup = flag.String("verify-backup", "",
			"decrypt and check this backup file with the master key, report what it holds, and exit")
		restoreBackup = flag.String("restore-backup", "",
			"decrypt and check this backup file into -restore-to, and exit; the server is not touched")
		restoreTo = flag.String("restore-to", "",
			"with -restore-backup, the new database file to write; it must not exist")

		// Rotating the master key (ADR-0054). On the host for the same
		// reasons: it needs both keys, from the environment, never a flag.
		rotateKey = flag.Bool("rotate-key", false,
			"re-seal every stored secret from the master key to the one named with _NEW, and exit; "+
				"the server must be stopped")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(Version)
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *checkOnly {
		// Which file, or that there was none.
		//
		// "configuration is valid" on a path that does not exist is a green
		// light about nothing: a missing file is legitimately defaults plus
		// environment (see config.Load), but an operator who asked whether the
		// configuration AT THIS PATH is valid has been answered a different
		// question. The compose healthcheck named a file this deployment never
		// creates and passed unconditionally for exactly this reason.
		if _, statErr := os.Stat(*configPath); errors.Is(statErr, fs.ErrNotExist) {
			fmt.Printf("configuration is valid — note: %s does not exist, so this "+
				"validated the defaults plus the environment and nothing else\n",
				*configPath)
			return nil
		}
		fmt.Printf("configuration is valid: %s\n", *configPath)
		return nil
	}

	if *healthCheck {
		return probeReadiness(cfg)
	}

	// Before the logger and before any role: this starts no listener, joins no
	// namespace and serves nothing. It opens the database, changes one account
	// and exits.
	if *recoverUser != "" {
		return runRecover(cfg, *recoverUser, *recoverPassword, os.Stdin, os.Stdout)
	}
	if *recoverPassword {
		return fmt.Errorf("-recover-password does nothing without -recover <username>")
	}
	if *verifyBackup != "" {
		return runVerifyBackup(cfg, *verifyBackup, os.Stdout)
	}
	if *restoreBackup != "" {
		return runRestoreBackup(cfg, *restoreBackup, *restoreTo, os.Stdout)
	}
	if *restoreTo != "" {
		return fmt.Errorf("-restore-to does nothing without -restore-backup FILE")
	}
	if *rotateKey {
		return runRotateKey(cfg, func() bool { _, err := readyz(cfg); return err == nil }, os.Stdout)
	}

	// The recent records the administrator's logs screen reads (ADR-0040),
	// kept behind the redaction handler like the output.
	logRing := logging.NewRing(logging.RingSize)
	logger := logging.New(os.Stdout, logging.Options{
		Level:  cfg.Logging.Level,
		Format: cfg.Logging.Format,
		Module: cfg.Logging.Modules,
		Ring:   logRing,
	})
	slog.SetDefault(logger)

	switch *role {
	case "app":
		return runApp(cfg, logger, logRing)
	case "downloader":
		// ADR-0001: the downloader refuses to start unless it can prove its
		// outbound traffic leaves through the tunnel.
		return runDownloader(cfg, logger)
	default:
		return fmt.Errorf("unknown role %q (want app or downloader)", *role)
	}
}

func runApp(cfg config.Config, logger *slog.Logger, logRing *logging.Ring) error {
	started := time.Now()
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, restart, restartRequested := restartable(sigCtx)

	// The master key is validated by the config lint before we get here, so a
	// failure at this point is a genuine surprise.
	cipher, err := secrets.NewCipherFromBase64(os.Getenv(cfg.Secrets.MasterKeyEnv))
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}

	database, err := db.Open(db.Options{
		Path:        cfg.Database.Path,
		BusyTimeout: cfg.Database.BusyTimeout,
	})
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()

	applied, err := database.Migrate(ctx)
	if err != nil {
		return err
	}
	if len(applied) > 0 {
		logger.Info("applied migrations", slog.Any("versions", applied))
	}

	auditLog := audit.New(database, time.Now)

	// --- backups (ADR-0029) --------------------------------------------------
	// Encrypted to a passphrase derived from the master key, so the key stays
	// the one secret to keep. Only the default directory is created: a named
	// one that is missing is more often an unmounted disk.
	backups, err := backup.New(database, cfg.Database.Path, cipher.BackupPassphrase(), backup.Policy{
		Dir:       cfg.BackupDir(),
		CreateDir: strings.TrimSpace(cfg.Backup.Dir) == "",
		Interval:  cfg.Backup.Interval,
		Keep:      cfg.Backup.Keep,
		KeepMin:   cfg.Backup.KeepMin,
	}, auditLog, time.Now)
	if err != nil {
		return err
	}
	// Durations as text: the JSON handler would print nanoseconds, and this line
	// is read by a person checking where their backups go.
	logger.Info("backups",
		slog.String("dir", cfg.BackupDir()),
		slog.String("interval", cfg.Backup.Interval.String()),
		slog.String("keep", cfg.Backup.Keep.String()),
		slog.Int("keep_min", cfg.Backup.KeepMin))

	// --- identity ---------------------------------------------------------
	argon := identity.Argon2Params{
		Memory:      cfg.Auth.Argon2Memory,
		Iterations:  cfg.Auth.Argon2Iterations,
		Parallelism: cfg.Auth.Argon2Parallelism,
		SaltLength:  cfg.Auth.Argon2SaltLength,
		KeyLength:   cfg.Auth.Argon2KeyLength,
	}
	store := identity.NewStore(database, cipher, argon, time.Now)

	// Seeded on every boot, not just the first: a permission added in a
	// release has to reach the Admin role, or the feature it guards is
	// unreachable by anyone. Custom roles are never touched.
	if err := store.EnsureBuiltinRoles(ctx); err != nil {
		return fmt.Errorf("seed roles: %w", err)
	}

	sessionCfg := identity.SessionConfig{
		IdleTimeout:     cfg.Auth.SessionIdleTimeout,
		AbsoluteTimeout: cfg.Auth.SessionAbsoluteTimeout,
	}
	svc := identity.NewService(store, auditLog, identity.Policy{
		RegistrationMode:  string(cfg.Registration.Mode),
		PendingTTL:        cfg.Registration.PendingTTL,
		MaxOutstanding:    cfg.Registration.MaxOutstanding,
		Password:          identity.PasswordPolicy{MinLength: cfg.Auth.MinPasswordLength},
		Session:           sessionCfg,
		LoginMaxAttempts:  cfg.Auth.LoginMaxAttempts,
		LoginWindow:       cfg.Auth.LoginWindow,
		TOTPIssuer:        cfg.Auth.TOTPIssuer,
		RecoveryCodeCount: cfg.Auth.RecoveryCodeCount,
		Argon2:            argon,
	}, time.Now)

	// The session cookie is marked Secure unless the app is serving plain HTTP
	// to a loopback address, which is the only case where it would break a
	// local development login.
	secureCookies := cfg.Server.TLSCertFile != "" || !strings.HasPrefix(cfg.Server.BaseURL, "http://localhost")
	auth := api.NewSessionAuthenticator(store, sessionCfg, secureCookies)

	if needed, err := svc.SetupNeeded(ctx); err == nil && needed {
		logger.Warn("no accounts exist: the first-run wizard is open at /setup, " +
			"and it closes permanently once the first administrator is created")
	}

	// Indexers. The store keeps List (no API keys) and Enabled (keys decrypted)
	// apart on purpose, and only the first is reachable from the admin surface.
	indexerStore := indexer.NewStore(database, cipher, time.Now)

	// Quality profiles. Seeded once, then left alone: re-seeding on every boot
	// would quietly undo an operator's edits and resurrect profiles they
	// deleted.
	profileStore := release.NewProfileStore(database, time.Now)
	if err := profileStore.EnsureDefaults(ctx); err != nil {
		return fmt.Errorf("seed quality profiles: %w", err)
	}

	// --- a SOCKS5 proxy set from the web (ADR-0065) ---------------------------
	// Laid over the file's direct profiles before the guard is built. When the
	// result fails the lint, those profiles are blocked rather than direct, and
	// the app still starts so the screen that fixes it is reachable.
	proxyStore := egressproxy.NewStore(store, cipher)
	fileCfg := cfg
	var proxyStatus egressproxy.Status
	if storedProxy, _, err := proxyStore.Load(ctx); err != nil {
		cfg, proxyStatus = egressproxy.Unreadable(cfg, err)
	} else {
		cfg, proxyStatus = egressproxy.Apply(cfg, storedProxy, os.Getenv)
	}
	if proxyStatus.Problem != "" {
		logger.Error("the SOCKS5 proxy set from the web is not in force; the traffic it would carry is "+
			"blocked until it is saved again", slog.String("problem", proxyStatus.Problem))
	}
	proxyCtl := egressproxy.NewController(proxyStore, fileCfg, os.Getenv, proxyStatus)

	// --- egress ------------------------------------------------------------
	// Built before the scheduler, because the health task needs it. It starts
	// UNHEALTHY and stays that way until a probe succeeds, so nothing on the
	// acquisition path can dial in the meantime.
	reg := metrics.New()
	guard := egress.New(egress.Config{
		Profiles:  egressProfiles(cfg),
		Exempt:    cfg.Egress.ExemptFromKillSwitch,
		Interface: cfg.Egress.TunnelInterface,
		// The kill switch is only enforced when the operator asked for it.
		// Otherwise a fresh install with no WireGuard would pause every
		// subsystem and report the reason in a health gauge nobody has looked
		// at. See egress.Config.Enforce.
		Enforce:  cfg.Egress.AnonymityEnabled,
		Observer: egressObserver{reg: reg, audit: auditLog, log: logger},
	})

	// --- download engine ----------------------------------------------------
	//
	// What the engine may do is DERIVED from the egress policy rather than
	// configured beside it, so the two cannot disagree. See download.ConfigFor:
	// under a socks5 profile DHT and uTP are forced off, because a proxy
	// carries TCP and nothing else, and leaving UDP on would announce the
	// operator's address to the swarm while they believed otherwise.
	// Declared as the interface, not as *download.Engine. A nil *download.Engine
	// placed in an interface is NOT a nil interface — the interface carries a
	// type — so the handlers' "is an engine wired?" check would pass and the
	// first method call would dereference nil. Keeping the concrete pointer
	// inside the branch that builds it means the disabled case can only ever be
	// a genuinely nil interface.
	var engine api.DownloadEngine
	var downloads *download.Manager
	if cfg.Download.Enabled {
		// jailed is false in the app role: this process has not verified a
		// namespace, and claiming one it has not checked is the failure this
		// argument exists to prevent.
		dlCfg, notes := download.ConfigFor(
			cfg.Download.DataDir, cfg.Download.ListenPort,
			egressProfiles(cfg)[download.ProfileName],
			cfg.Egress.AnonymityEnabled, false)
		dlCfg.MaxActive = cfg.Download.MaxActive
		dlCfg.Seed = cfg.Download.Seed

		eng, err := download.New(dlCfg, guard, notes)
		if err != nil {
			return fmt.Errorf("start the download engine: %w", err)
		}
		defer func() { _ = eng.Close() }()

		// Logged at startup, one line each. An operator running in degraded
		// mode should find out from the log, not from wondering why nothing
		// has peers.
		for _, note := range eng.Notes() {
			logger.Warn("download engine", slog.String("note", note))
		}
		logger.Info("download engine started",
			slog.Int("listen_port", eng.ListenPort()),
			slog.Bool("dht", dlCfg.EnableDHT),
			slog.Bool("utp", dlCfg.EnableUTP),
			slog.Bool("accepts_incoming", dlCfg.AcceptIncoming))

		// The Manager, not the Engine, is what everything else holds: it is the
		// only thing that writes a queue row, so "we started a transfer" and
		// "we recorded that we started a transfer" cannot come apart.
		downloads = download.NewManager(eng, download.NewStore(database, time.Now), logger)

		// Bytes from a previous run are still on disk under the data directory,
		// named by info hash. Without this they are orphaned: nothing left alive
		// knows what they were or that anyone asked for them, and the only
		// symptom is a disk that slowly fills.
		restored, err := downloads.Restore(ctx)
		if err != nil {
			// Not fatal. A queue that will not load is bad; refusing to serve
			// the library, the users and the admin surface because of it is
			// worse.
			logger.Error("the download queue could not be restored",
				slog.String("error", err.Error()))
		} else if restored > 0 {
			logger.Info("resumed downloads from the previous run",
				slog.Int("count", restored))
		}

		engine = downloads
	} else {
		logger.Info("the download engine is disabled (download.enabled is false)")
	}

	// The search service composes the three: indexers supply candidates,
	// internal/release parses and ranks them, and a profile judges.
	searchSvc := search.New(indexerStore, indexer.NewClient(guard), indexerStore)

	// Library root folders. The download directory is passed in so that adding
	// a root can answer "will a hardlink work from here" by trying it, rather
	// than leaving the operator to discover during an import that every file is
	// being copied at twice the disk cost.
	rootStore := library.NewRootStore(database, cfg.Download.DataDir, time.Now)

	// The library and the importer. The importer holds no destroy authority —
	// see the scheduled task below and authz.systemPermissions — so an upgrade
	// MOVES the file it replaces into the root's trash folder rather than
	// unlinking it, which is what §2 means by reversible.
	mediaStore := importer.NewStore(database, time.Now)
	imp := importer.New(mediaStore, rootStore, logger, time.Now)

	// Grab tickets are sealed under the instance master key, so they do not
	// survive a key rotation — which is correct: a rotation should invalidate
	// every outstanding capability, and a stale ticket costs one re-search.
	// The two the configuration promised (ADR-0051): a password a person sets
	// is checked against known breaches, through the metadata profile, and a
	// signup pays a proof-of-work before any Argon2id time is spent.
	if cfg.Auth.BreachCheckEnabled {
		svc.SetBreachChecker(identity.NewPwnedPasswords(
			guard.HTTPClient("metadata", identity.BreachTimeout, nil), "", Version), logger)
	}
	svc.SetProofOfWork(identity.NewProofOfWork(cipher, cfg.Registration.ProofOfWorkBits, time.Now))

	// OpenSubtitles (ADR-0055), through the subtitle profile, off until a key
	// is entered.
	subtitleSvc := subtitles.NewService(store, cipher,
		subtitles.NewClient(guard.HTTPClient("subtitle", 30*time.Second, nil), "", Version),
		mediaStore, rootStore, auditLog, logger)

	tickets := search.NewTickets(cipher, search.DefaultTicketTTL, time.Now)
	requestSvc := request.NewService(request.NewStore(database, time.Now), auditLog, time.Now)

	// The metadata provider's traffic goes through the egress guard on its own
	// profile, so an operator who tunnels indexer traffic and leaves this
	// direct is refused by the configuration lint rather than left believing
	// their library is private. A lookup says "this instance HOLDS X" as
	// plainly as a search says "somebody here WANTS X".
	metadataSvc := metadata.NewService(store, cipher, auditLog, func() *http.Client {
		return guard.HTTPClient("metadata", 20*time.Second, nil)
	}, time.Now)
	artCache, err := library.OpenCache(filepath.Join(filepath.Dir(cfg.Database.Path), "artwork"))
	if err != nil {
		// Not fatal: an instance with no artwork cache identifies perfectly
		// well and simply has no pictures. Refusing to start over a directory
		// that holds only re-downloadable files would be the wrong trade.
		logger.Warn("no artwork cache; posters will not be stored",
			slog.String("error", err.Error()))
	}
	var artStore *artwork.Store
	if artCache != nil {
		artStore = artwork.New(artCache,
			guard.HTTPClient("metadata", 30*time.Second, nil),
			[]string{"image.tmdb.org"})
	}

	// The migration directory sits beside the application's own database. Not
	// fatal when it cannot be opened: an instance with nowhere to put a Radarr
	// export works perfectly well and simply offers no migration (ADR-0021).
	var migrateRunner *migrate.Runner
	if sources, err := migrate.OpenSources(
		filepath.Join(filepath.Dir(cfg.Database.Path), "migrate")); err != nil {
		logger.Warn("no migration directory; migrating from another application is unavailable",
			slog.String("error", err.Error()))
	} else {
		defer func() { _ = sources.Close() }()
		migrateRunner = migrate.NewRunner(sources,
			migrate.NewService(mediaStore, auditLog, logger))
	}

	identifySvc := identify.NewService(
		identify.NewStore(database, time.Now),
		func() metadata.Provider { return metadataSvc.Provider() },
		mediaStore, artStore, auditLog, logger, time.Now)

	// --- episodes ----------------------------------------------------------
	//
	// What a series contains, from the provider — never from the files on
	// disk, which would make every season complete by construction (ADR-0022).
	episodeStore := library.NewEpisodeStore(database, time.Now)
	episodeProvider := func() tv.EpisodeProvider {
		// Returned as an untyped nil when nothing is configured. A nil
		// metadata.Provider placed in this interface would be a NON-nil
		// interface holding nothing, and the refresher's "is there a
		// provider?" check would pass and then dereference it.
		p := metadataSvc.Provider()
		if p == nil {
			return nil
		}
		return p
	}
	episodeRefresher := tv.NewRefresher(episodeStore, mediaStore, episodeProvider, logger, time.Now)

	// Adding a series or a film before any of it is on disk (ADR-0025,
	// ADR-0026): the item, its identification and every episode, in one
	// transaction or not at all.
	adder := follow.NewService(database, mediaStore, identifySvc.Store(), episodeStore,
		rootStore, episodeProvider, auditLog, logger)

	// --- music (ADR-0044) --------------------------------------------------
	//
	// MusicBrainz needs no key; its traffic goes through the egress guard on
	// the metadata profile, as TMDB's does, one request a second.
	musicSvc := music.NewService(database, music.NewStore(database, time.Now), rootStore,
		music.NewMusicBrainz(guard.HTTPClient("metadata", 20*time.Second, nil), "", Version), auditLog)
	// A music root is scanned by the music library, any other by the video
	// importer (ADR-0045).
	musicLib := music.NewLibrary(musicSvc, rootStore, logger, time.Now)

	// --- books (ADR-0048) --------------------------------------------------
	//
	// Open Library needs no key either; the same profile, the same courtesy.
	booksSvc := books.NewService(database, rootStore,
		books.NewOpenLibrary(guard.HTTPClient("metadata", books.Timeout, nil), "", Version), auditLog, time.Now)
	booksLib := books.NewLibrary(database, rootStore, logger, time.Now)
	scanner := music.ScanDispatch{Video: imp, Music: musicLib, Books: booksLib, Roots: rootStore}

	// --- playback ----------------------------------------------------------
	//
	// The sandbox is built once and asked whether the kernel would allow it,
	// by trying. ADR-0020 permits parsing without it, loudly, because refusing
	// would make the library unplayable on a host where every other media
	// server works — and a host that cannot create a user namespace is a
	// configuration, not a fault.
	mediaSandbox := playback.NewSandbox(logger, true)
	playbackPositions := playback.NewPositions(database, time.Now)
	// The ceiling on simultaneous conversions. ADR-0005's premise is that this
	// hardware has no headroom, and an unbounded number of ffmpeg processes on
	// four cores takes the working sessions down with the new one. Zero means
	// unset, and NewRemuxer turns that into a small number rather than into
	// "as many as arrive".
	playbackSvc := playback.NewService(
		mediaStore, rootStore,
		playback.NewStore(database, time.Now),
		playback.NewProber(mediaSandbox),
		playbackPositions,
		mediaSandbox, logger, cfg.Media.MaxConcurrentTranscodes)

	if err := metadataSvc.Load(ctx); err != nil {
		// Not fatal. An instance with no working metadata provider is a
		// supported configuration and everything else still works; starting up
		// and saying so beats refusing to start over an optional credential.
		logger.Warn("the stored metadata credential could not be loaded",
			slog.String("error", err.Error()))
	}
	// So a finished download closes the request that asked for it. "It
	// downloaded and then nothing happened" is the complaint this category of
	// software earns, and a request with no ending is that complaint.
	imp.SetRequestCloser(requestSvc)
	// So a file arriving in the library is in the audit log, as every other
	// change to the library is — and reaches the notifier from there
	// (ADR-0032).
	imp.SetAuditor(auditLog)

	// Notifications (ADR-0032): one Discord webhook, reached through the
	// notification profile — the one exempt from the kill switch, so that "the
	// tunnel is down" can still be said. A redirect is followed only within
	// Discord: the link's path is the credential.
	notifier := notify.NewService(store, cipher, auditLog,
		notify.NewDiscord(guard.HTTPClient("notification", 15*time.Second, notify.ValidateRedirect)), time.Now)

	// --- metrics and scheduled tasks --------------------------------------
	scheduler := tasks.New(logger, time.Now)
	scheduler.Observe(func(name string, o tasks.Outcome) {
		reg.ObserveTask(name, o.Succeeded(), o.Duration, time.Now())
		// Notes a task that starts failing or recovers; it does not block.
		notifier.TaskFinished(name, o)
	})

	// Playback history says what somebody watched and when, which
	// docs/THREAT-MODEL.md lists as an asset in its own right. Keeping it
	// forever is a choice nobody made; the retention window is configuration
	// and this is what honours it.
	scheduler.Register(tasks.Task{
		Name:        "playback.retention",
		Description: "Drop playback positions older than the configured retention window.",
		Interval:    6 * time.Hour,
		Run: func(ctx context.Context) (string, error) {
			sysCtx := authz.SystemPrincipal(ctx, authz.TaskPlaybackPurge)
			n, err := playbackPositions.Purge(sysCtx, cfg.Media.PlaybackHistoryRetention)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("dropped %d playback position(s) older than %s",
				n, cfg.Media.PlaybackHistoryRetention), nil
		},
	})

	// Keeps episode lists current. A series whose answer can move at any moment
	// — an episode still to air, one that aired recently, an announced season,
	// or never asked about — is asked every run; every other series once a
	// week, because a finished series can be renewed (tv.Policy, ADR-0022).
	//
	// 200 a run is a capacity figure, not a guess. A thousand series of which a
	// hundred are running need ≈330 series asked a day (900 weekly, 100 twice
	// daily), and two runs of 200 give room for 400; oldest-asked-first means
	// nothing starves while demand stays under that. At one request for a
	// finished series and two for a running one, it is ≈530 requests a day.
	scheduler.Register(tasks.Task{
		Name:        "episodes.refresh",
		Description: "Ask the metadata provider which episodes each series has, so the wanted list knows what is missing and a renewed series is noticed.",
		Interval:    12 * time.Hour,
		Run: func(ctx context.Context) (string, error) {
			if !metadataSvc.Configured() {
				// Said, not failed: an instance with no provider is a supported
				// configuration, and a red task on the dashboard over it would be
				// an alarm about a choice rather than a fault.
				return "no metadata provider is configured, so no episode list can be refreshed", nil
			}
			sysCtx := authz.SystemPrincipal(ctx, authz.TaskEpisodeRefresh)
			results, err := episodeRefresher.RefreshAll(sysCtx, 200)
			var episodes, unread int
			for _, r := range results {
				episodes += r.Episodes
				unread += r.SeasonsFailed
			}
			summary := fmt.Sprintf("refreshed %d series (%d episodes read)", len(results), episodes)
			if unread > 0 {
				summary += fmt.Sprintf("; %d season(s) could not be read and will be asked about again", unread)
			}
			if err != nil {
				// A failed run shows its error and not its summary, so the
				// summary travels in the error: "rate-limited" on its own would
				// hide the forty series refreshed before it.
				return summary, fmt.Errorf("%s, then: %w", summary, err)
			}
			return summary, nil
		},
	})

	// Expired account requests hold an email, a source IP and a password hash,
	// so purging them is a privacy obligation rather than housekeeping.
	scheduler.Register(tasks.Task{
		Name:        "identity.maintenance",
		Description: "Purge expired account requests, dead sessions, spent invites, used reset tokens and old throttle records.",
		Interval:    time.Hour,
		Run: func(ctx context.Context) (string, error) {
			reqs, sess, attempts, err := svc.RunMaintenance(ctx)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("purged %d requests, %d sessions, %d throttle records",
				reqs, sess, attempts), nil
		},
	})

	// Gauges are point-in-time, so something has to sample them.
	// A finished download is noticed by a poll rather than by a callback inside
	// the torrent library. A poll reading the engine's own view has no ordering
	// hazards and cannot wedge the library's event loop if the database is
	// slow; the cost is that completion is recorded within a tick rather than
	// instantly, which nothing downstream depends on.
	if downloads != nil {
		scheduler.Register(tasks.Task{
			Name:        "download.completion",
			Description: "Mark finished transfers complete in the download queue.",
			Interval:    30 * time.Second,
			Run: func(ctx context.Context) (string, error) {
				n, err := downloads.SyncCompletion(ctx)
				if err != nil {
					return "", err
				}
				if n == 0 {
					return "", nil
				}
				return fmt.Sprintf("marked %d download(s) complete", n), nil
			},
		})

		// Stalled downloads (ADR-0034): progress recorded, and what has not
		// moved for download.stall_after given up if automatic acquisition
		// grabbed it, reported if a person did. Every five minutes: a stall is
		// a matter of hours, and each run reads the engine and writes only
		// what changed.
		if stallAfter := cfg.Download.StallAfter; stallAfter > 0 {
			scheduler.Register(tasks.Task{
				Name:        "download.stalls",
				Description: "Record download progress; give up automatic downloads that stopped moving.",
				Interval:    5 * time.Minute,
				Run: func(ctx context.Context) (string, error) {
					stalls, err := downloads.SyncStalls(ctx, stallAfter, acquire.Automatic)
					if err != nil {
						return "", err
					}
					return reportStalls(ctx, auditLog, stalls, time.Now()), nil
				},
			})
		}

	}

	// The identification pass. Hourly rather than often: it spends requests
	// against a third party, and an unidentified item is not urgent — it is a
	// poster missing from a library that otherwise works.
	//
	// Its principal is authz.TaskIdentify, whose grant is browse and nothing
	// else, so it can propose and cannot relabel (ADR-0019).
	scheduler.Register(tasks.Task{
		Name: "library.identify",
		Description: "Ask the metadata provider what each unidentified library item is. " +
			"Proposes; only an exact title and year match is attached automatically, " +
			"and nothing is ever renamed without a person.",
		Interval: time.Hour,
		Run: func(ctx context.Context) (string, error) {
			if !metadataSvc.Configured() {
				return "no metadata provider is configured", nil
			}
			res, err := identifySvc.RunPass(
				authz.SystemPrincipal(ctx, authz.TaskIdentify), 200)
			summary := fmt.Sprintf(
				"%d considered, %d attached automatically, %d waiting for review, "+
					"%d with no match, %d skipped (already decided by a person), %d failed",
				res.Considered, res.Accepted, res.Proposed,
				res.NothingFound, res.Skipped, res.Failed)
			return summary, err
		},
	})

	if downloads != nil {
		// Importing completed downloads into the library.
		//
		// This is ONE of the places background authority is minted, and the
		// grant is deliberately visible here rather than buried in the
		// importer: authz.SystemPrincipal carries browse and path-mutation and
		// NOTHING else — in particular no authority to unlink a media file. A
		// structural test (authz.TestOnlySchedulingCodeCanMintASystemPrincipal)
		// fails the build if any other file calls it.
		scheduler.Register(tasks.Task{
			Name:        "library.import",
			Description: "Move completed downloads into the library, hardlinked so seeding continues.",
			Interval:    time.Minute,
			Run: func(ctx context.Context) (string, error) {
				sysCtx := authz.SystemPrincipal(ctx, authz.TaskImport)
				return runImports(sysCtx, downloads, mediaStore, imp, musicLib, booksLib, logger)
			},
		})
	}

	// Seeding is distribution: it is the part of this software that sends
	// content to strangers. The obligation is tracked against durable totals,
	// so a restart does not forgive what was already uploaded — see
	// Manager.SyncSeeding for why that matters and what happens when an
	// indexer records no requirement at all.
	if downloads != nil {
		const seedTick = time.Minute
		scheduler.Register(tasks.Task{
			Name:        "download.seeding",
			Description: "Advance seeding obligations and stop transfers that have met their ratio or time.",
			Interval:    seedTick,
			Run: func(ctx context.Context) (string, error) {
				n, err := downloads.SyncSeeding(ctx, seedTick, cfg.Download.Seed)
				if err != nil {
					return "", err
				}
				if n == 0 {
					return "", nil
				}
				return fmt.Sprintf("stopped seeding %d transfer(s)", n), nil
			},
		})
	}

	// Automatic acquisition (ADR-0030): what is wanted, fetched without a
	// person — the indexers' recent releases, and a budgeted search of the
	// Wanted list, grabbed only when the default profile accepts a release
	// that matches exactly one wanted item. Off unless the operator turned it
	// on, and never without the download engine: the configuration lint
	// refuses the one without the other.
	//
	// The passes run as system:acquire — browse, search, queue, and nothing
	// else (authz.TaskAcquire) — minted here, beside the tasks that receive it.
	// Neither runs at start: a process restarting in a loop must not become a
	// loop of indexer requests.
	var acquisition api.AcquisitionService
	if cfg.Acquisition.Automatic && downloads != nil {
		acquirer, err := acquire.New(acquire.Deps{
			Store:    acquire.NewStore(database, time.Now),
			Finder:   searchSvc,
			Queue:    downloads,
			Titles:   metadataSvc,
			Profiles: profileStore,
			// ADR-0033: a pack is queued only once its file list is seen to
			// hold every wanted episode, read the way the import will read it.
			PackCheck: checkPack,
			// Decision 6: with egress enforced and the tunnel not verified,
			// no indexer is asked. The requests would be refused anyway, and
			// each would be recorded as an indexer failure.
			Gate: func() (bool, string) {
				if !guard.Enforcing() {
					return true, ""
				}
				if healthy, detail, _ := guard.Healthy(); !healthy {
					return false, "egress is enforced and the tunnel is not verified (" + detail + ")"
				}
				return true, ""
			},
			Audit: auditLog,
			Log:   logger,
			Now:   time.Now,
		}, acquire.Config{
			RecentInterval: cfg.Acquisition.RSSInterval,
			SearchInterval: cfg.Acquisition.SearchInterval,
			SearchesPerRun: cfg.Acquisition.SearchesPerRun,
			MaxGrabsPerRun: cfg.Acquisition.MaxGrabsPerRun,
			Upgrades:       cfg.Acquisition.Upgrades,
		})
		if err != nil {
			return fmt.Errorf("start automatic acquisition: %w", err)
		}
		acquisition = acquirer
		scheduler.Register(tasks.Task{
			Name: "acquire.recent",
			Description: "Ask each indexer once for its recent releases and grab what is wanted: " +
				"matched to exactly one wanted episode or film, accepted by the default quality " +
				"profile, seeded, and never grabbed before.",
			Interval: cfg.Acquisition.RSSInterval,
			Run: func(ctx context.Context) (string, error) {
				return acquirer.RunRecent(authz.SystemPrincipal(ctx, authz.TaskAcquire))
			},
		})
		scheduler.Register(tasks.Task{
			Name: "acquire.search",
			Description: fmt.Sprintf("Search for up to %d wanted item(s) that are due — never searched "+
				"first, then the longest waiting — and grab what the default quality profile accepts. "+
				"An item whose search finds nothing waits 6 hours, doubling to a week.",
				cfg.Acquisition.SearchesPerRun),
			Interval: cfg.Acquisition.SearchInterval,
			Run: func(ctx context.Context) (string, error) {
				return acquirer.RunSearch(authz.SystemPrincipal(ctx, authz.TaskAcquire))
			},
		})
		scheduler.Register(tasks.Task{
			Name: "acquire.albums",
			Description: fmt.Sprintf("Search for up to %d wanted album(s) whose track list is known "+
				"and grab the best release that is the album: a named format, seeded, never grabbed "+
				"before. An album is fetched automatically once.", cfg.Acquisition.SearchesPerRun),
			Interval: cfg.Acquisition.SearchInterval,
			Run: func(ctx context.Context) (string, error) {
				return acquirer.RunAlbums(authz.SystemPrincipal(ctx, authz.TaskAcquire))
			},
		})
		scheduler.Register(tasks.Task{
			Name: "acquire.books",
			Description: fmt.Sprintf("Search for up to %d wanted book(s) whose author is known and grab "+
				"the best release that is the book: a named format, EPUB first, seeded, never grabbed before.",
				cfg.Acquisition.SearchesPerRun),
			Interval: cfg.Acquisition.SearchInterval,
			Run: func(ctx context.Context) (string, error) {
				return acquirer.RunBooks(authz.SystemPrincipal(ctx, authz.TaskAcquire))
			},
		})
		logger.Info("automatic acquisition is on",
			slog.String("recent_releases_every", cfg.Acquisition.RSSInterval.String()),
			slog.String("search_every", cfg.Acquisition.SearchInterval.String()),
			slog.Int("searches_per_run", cfg.Acquisition.SearchesPerRun),
			slog.Int("max_grabs_per_run", cfg.Acquisition.MaxGrabsPerRun),
			// Said at start: replacing files is not something to find out about
			// from the trash (ADR-0036).
			slog.Bool("upgrades", cfg.Acquisition.Upgrades))
	}

	// A periodic reconciliation between the database and the disks.
	//
	// Hourly rather than often: a scan walks every file in every root, and an
	// operator who adds media by hand does not need it noticed within seconds.
	// It changes nothing on disk, and it refuses outright if most of a root's
	// files have vanished — that is far more often an unmounted disk than a
	// deletion, and acting on it would erase the record of a whole library.
	scheduler.Register(tasks.Task{
		Name:        "library.scan",
		Description: "Reconcile the library database with what is on disk. Changes nothing on disk.",
		Interval:    time.Hour,
		Run: func(ctx context.Context) (string, error) {
			sysCtx := authz.SystemPrincipal(ctx, authz.TaskLibraryScan)
			return runScans(sysCtx, rootStore, scanner, requestSvc, logger)
		},
	})

	// The destructive half of the reversible design, and the only thing in this
	// software that unlinks a media file without a person naming it. It runs
	// under a principal holding destroy authority AND NOTHING ELSE — not even
	// path mutation — which is why the grants are per task rather than shared.
	scheduler.Register(tasks.Task{
		Name:        "library.trash_purge",
		Description: "Unlink trashed files once their retention window has passed.",
		Interval:    6 * time.Hour,
		Run: func(ctx context.Context) (string, error) {
			sysCtx := authz.SystemPrincipal(ctx, authz.TaskTrashPurge)
			res, err := imp.PurgeTrash(sysCtx, cfg.Media.TrashRetention)
			if err != nil {
				return "", err
			}
			if res.Purged == 0 {
				return "", nil
			}
			return fmt.Sprintf("purged %d file(s), freed %.2f GiB",
				res.Purged, float64(res.Freed)/(1<<30)), nil
		},
	})

	// The audit log's ceiling on denials (ADR-0031, decision 4): what was over
	// it in an hour that has ended is written as one line, within five minutes,
	// so a scanner that stops does not leave its count unwritten.
	scheduler.Register(tasks.Task{
		Name: "audit.denials",
		Description: fmt.Sprintf("Write the count of authorization denials over the audit log's ceiling in the hour "+
			"just ended: at most %d an hour are written one by one from one address, %d from every address "+
			"together and %d from one account; the rest are counted and written as one line.",
			audit.AnonymousPerSourcePerHour, audit.AnonymousPerHour, audit.PersonPerHour),
		Interval: 5 * time.Minute,
		Run: func(ctx context.Context) (string, error) {
			n, err := auditLog.FlushDenials(ctx, false)
			if err != nil {
				return "", err
			}
			if n == 0 {
				return "", nil
			}
			return "wrote the count of denials over the ceiling in the hour just ended", nil
		},
	})

	// Notifications: what the operator chose from the audit log, and tasks
	// that start failing or recover, to their Discord webhook. Nothing when
	// no webhook is set.
	scheduler.Register(tasks.Task{
		Name: notify.TaskName,
		Description: "Send what the operator chose from the audit log — and scheduled tasks that start failing " +
			"or recover — to the Discord webhook, at most three messages a minute. Nothing when no webhook is set.",
		Interval: notify.Interval,
		Run: func(ctx context.Context) (string, error) {
			return notifier.Run(authz.SystemPrincipal(ctx, authz.TaskNotify))
		},
	})

	// Music (ADR-0044): followed artists' albums weekly, and the track lists of
	// monitored albums that have none. One MusicBrainz request a second, so a
	// run of 20 artists and 50 albums takes about a minute.
	scheduler.Register(tasks.Task{
		Name: "music.refresh",
		Description: "Ask MusicBrainz for followed artists' albums (weekly for each) and the track lists " +
			"of monitored albums that have none, so the Wanted list knows what is missing.",
		Interval: 12 * time.Hour,
		Run: func(ctx context.Context) (string, error) {
			pass, err := musicSvc.Refresh(authz.SystemPrincipal(ctx, authz.TaskMusicRefresh), 20, 50)
			if err != nil {
				return pass.Summary(), fmt.Errorf("%s, then: %w", pass.Summary(), err)
			}
			return pass.Summary(), nil
		},
	})

	// Wanted subtitles, fetched from OpenSubtitles without a person (ADR-0056):
	// a few a pass, with browse alone; nothing is asked until a key and a
	// language are set.
	subtitleSweep := subtitles.NewSweeper(subtitleSvc, database, time.Now)
	scheduler.Register(tasks.Task{
		Name: "subtitles.fetch",
		Description: fmt.Sprintf("Fetch up to %d wanted subtitle(s) from OpenSubtitles: a film's or an "+
			"episode's file with no subtitle in a wanted language. One that found nothing waits a day, "+
			"doubling to a month.", subtitles.SearchesPerPass),
		Interval: 6 * time.Hour,
		Run: func(ctx context.Context) (string, error) {
			return subtitleSweep.Run(authz.SystemPrincipal(ctx, authz.TaskSubtitles))
		},
	})

	// Ratings (ADR-0037): how each identified title is rated, which decides
	// whether an account with a ceiling may see it. Fifty titles an hour, each
	// asked once, and an unrated one again after a month.
	scheduler.Register(tasks.Task{
		Name: "metadata.ratings",
		Description: "Ask the metadata provider how each identified title is rated (US certification), " +
			"which decides whether an account with a rating ceiling can see it. A rating a person set is never replaced.",
		Interval: time.Hour,
		Run: func(ctx context.Context) (string, error) {
			if !metadataSvc.Configured() {
				return "no metadata provider is configured, so no title can be rated", nil
			}
			pass, err := importer.RateTitles(authz.SystemPrincipal(ctx, authz.TaskRatings),
				mediaStore, metadataSvc, 50)
			if err != nil {
				return pass.Summary(), fmt.Errorf("%s, then: %w", pass.Summary(), err)
			}
			return pass.Summary(), nil
		},
	})

	scheduler.Register(tasks.Task{
		Name:        "metrics.sample",
		Description: "Sample gauge metrics: live sessions, live tokens, pending account requests.",
		Interval:    time.Minute,
		Run: func(ctx context.Context) (string, error) {
			return "", sampleGauges(ctx, database, reg)
		},
	})

	// The tunnel dropping does not restart the process: the interface goes away,
	// the route changes, and the only thing that notices is a check that looks
	// again. Until this runs for the first time, egress is closed.
	scheduler.Register(tasks.Task{
		Name:        "egress.health",
		Description: "Verify outbound download traffic still leaves through the tunnel. Failing this closes the kill switch.",
		Interval:    cfg.Egress.ProbeInterval,
		Run: func(ctx context.Context) (string, error) {
			if !cfg.Egress.AnonymityEnabled {
				// Nothing to verify and nothing to gate. Reporting a healthy
				// tunnel would be a lie; reporting a failure would light up a
				// dashboard over a policy the operator switched off on purpose.
				const detail = "egress enforcement is off (anonymity_enabled is false); " +
					"no tunnel is expected"
				guard.SetHealthy(true, detail)
				return detail, nil
			}
			healthy, detail := egress.Prober{
				Interface: cfg.Egress.TunnelInterface,
				Target:    cfg.Egress.ProbeTarget,
				Dial:      guard.ProbeDialer("download"),
			}.Probe(ctx)
			guard.SetHealthy(healthy, detail)
			if !healthy {
				// Returned as an error so the task shows as failing on the
				// dashboard, rather than as a green tick next to a closed gate.
				return "", errors.New(detail)
			}
			return detail, nil
		},
	})

	// Backups (ADR-0029). The check runs hourly and once at startup, and takes
	// a backup when the newest on disk is older than backup.interval: the disk
	// remembers when the last one was, where the ticker would forget at every
	// restart.
	scheduler.Register(tasks.Task{
		Name: "database.backup",
		Description: "Take an encrypted, checked backup of the database when the newest is older than " +
			"backup.interval, and delete backups older than backup.keep — never the newest backup.keep_min.",
		Interval:   time.Hour,
		RunAtStart: true,
		Run:        backups.RunScheduled,
	})

	scheduler.Register(tasks.Task{
		Name:        "database.integrity",
		Description: "Run SQLite's own integrity check. Manual only: it is not free on a large database.",
		Run: func(ctx context.Context) (string, error) {
			if err := database.IntegrityCheck(ctx); err != nil {
				return "", err
			}
			return "integrity check passed", nil
		},
	})

	scheduler.Start(ctx)
	defer scheduler.Stop()

	// Probe once before the listeners open. The guard starts closed, so without
	// this the first interval's worth of time after every restart is spent with
	// egress paused for no reason, and the admin page reports "no probe has run
	// yet" to an operator who just restarted to fix something.
	//
	// Synchronous on purpose: with no probe target this is a route lookup and
	// costs microseconds, and with one it is a single bounded dial. Serving a
	// request before knowing this answer is the thing worth avoiding.
	if _, err := scheduler.Trigger(ctx, "egress.health"); err != nil {
		logger.Warn("the initial egress probe could not run",
			slog.String("error", err.Error()))
	}

	// --- public listener --------------------------------------------------
	router := api.NewRouter(
		[]api.Middleware{
			api.Recovery(logger),
			api.RequestContext(logger),
			api.ClientIPResolver(cfg.Server.TrustedProxies),
			api.SecurityHeaders(365 * 24 * time.Hour),
		},
		[]api.Middleware{
			api.Metrics(reg),
			api.RateLimit(api.NewRateLimiter(time.Now), rateLimitRules(cfg)),
			api.CSRF(),
			api.Authenticate(auth, metricsAuditSink{inner: auditLog, reg: reg}),
		},
	)
	api.RegisterRoutes(router, api.New(api.Deps{
		Identity: svc, Auth: auth, Tasks: scheduler, Egress: guard,
		Indexers: indexerStore, Search: searchSvc, Profiles: profileStore,
		Downloads: engine, Roots: rootStore, Media: mediaStore,
		Scanner: scanner, Deleter: imp, Tickets: tickets, Grabs: searchSvc,
		Requests: requestSvc, Metadata: metadataSvc,
		Identify: identifySvc, Artwork: artCache, Audit: auditLog,
		Playback: playbackSvc,
		Logs:     logRing,
		Issues:   issue.NewService(database, auditLog, time.Now),
		Discover: metadataSvc,
		Music:    musicSvc,
		Books:    booksSvc,
		// Every page offers this build's source (ADR-0052).
		SourceURL: cfg.Server.SourceURL,
		Version:   Version,
		Subtitles: subtitleSvc,
		Proxy:     proxyCtl,
		Restart:   restart,
		Calendar:  episodeStore,
		Arrivals:  mediaStore,
		BaseURL:   cfg.Server.BaseURL,
		Settings:  cfg.Document,
		Health: &api.HealthParts{
			Version:          Version,
			Started:          started,
			Schema:           database.SchemaVersion,
			SandboxAvailable: mediaSandbox.Available(),
			Space:            library.Space,
		},
		Migrate:   migrateRunner,
		Episodes:  episodeStore,
		Refresher: episodeRefresher,
		// The scene's names for a series, for episode search (ADR-0023), and
		// every name a film goes by, for film search (ADR-0026).
		AlternativeTitles: metadataSvc,
		FilmTitles:        metadataSvc,
		Adder:             adder,
		Backups:           backups,
		// What automatic acquisition last did about each wanted item, for the
		// Wanted screen (ADR-0030). Nil when it is off.
		Acquisition: acquisition,
		// The Discord webhook and what is sent to it (ADR-0032).
		Notifications:  notifier,
		TrashRetention: cfg.Media.TrashRetention,
	}))

	appSrv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           router,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
	}

	// --- management listener ----------------------------------------------
	// Health and metrics live here, on a separately bound socket, so they are
	// not reachable from the public interface (requirements §7.1).
	mgmtMux := http.NewServeMux()
	mgmtMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mgmtMux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := database.PingContext(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("database unavailable\n"))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	// §7.1: metrics live on the management listener, never the public one.
	// Metric names and label values leak operational shape.
	mgmtMux.Handle("GET /metrics", reg.Handler())

	mgmtSrv := &http.Server{
		Addr:              cfg.Server.ManagementAddr,
		Handler:           mgmtMux,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
	}

	errCh := make(chan error, 2)

	go func() {
		logger.Info("management listener started", slog.String("addr", cfg.Server.ManagementAddr))
		if err := mgmtSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("management listener: %w", err)
		}
	}()

	go func() {
		logger.Info("application listener started",
			slog.String("addr", cfg.Server.Addr),
			slog.String("version", Version),
			slog.Int("routes", len(router.Routes())),
			slog.Int("anonymous_routes", len(api.AnonymousAllowlist)),
			slog.String("registration_mode", string(cfg.Registration.Mode)),
			slog.Bool("mfa_required", cfg.Auth.MFARequired),
		)
		var err error
		if cfg.Server.TLSCertFile != "" {
			err = appSrv.ListenAndServeTLS(cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile)
		} else {
			err = appSrv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("application listener: %w", err)
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownGrace)
	defer cancel()

	_ = mgmtSrv.Shutdown(shutdownCtx)
	shutdownErr := appSrv.Shutdown(shutdownCtx)

	// The denials over the ceiling this hour, counted in memory, written now
	// rather than lost with the process (ADR-0031, decision 4). After the
	// listeners have stopped, so nothing is counted after it — and even when
	// they did not stop in time, with a deadline of its own, since theirs may
	// be what ran out.
	flushCtx, cancelFlush := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelFlush()
	if n, err := auditLog.FlushDenials(flushCtx, true); err != nil {
		logger.Error("the count of denials over the ceiling could not be written",
			slog.String("error", err.Error()))
	} else if n > 0 {
		logger.Info("wrote the count of denials over the ceiling before stopping")
	}

	if shutdownErr != nil {
		return fmt.Errorf("shutdown: %w", shutdownErr)
	}
	if restartRequested() {
		logger.Info("stopped cleanly; restarting as asked from the web")
		return errRestart
	}
	logger.Info("stopped cleanly")
	return nil
}

// rateLimitRules are the per-route caps. The anonymous endpoints are the ones
// that matter: signup performs Argon2id hashing and login performs a
// deliberate constant-time verification, so both are expensive by design.
func rateLimitRules(cfg config.Config) map[string]api.RateLimitRule {
	return map[string]api.RateLimitRule{
		"POST /api/v1/auth/login":                {Limit: 10, Period: 15 * time.Minute},
		"POST /api/v1/auth/login/mfa":            {Limit: 10, Period: 15 * time.Minute},
		"POST /api/v1/auth/signup":               {Limit: cfg.Registration.PerIPPerHour, Period: time.Hour},
		"GET /api/v1/auth/signup/challenge":      {Limit: 30, Period: time.Hour},
		"POST /api/v1/auth/reset/initiate":       {Limit: 5, Period: time.Hour},
		"POST /api/v1/auth/reset/complete":       {Limit: 10, Period: time.Hour},
		"GET /api/v1/search":                     {Limit: 120, Period: time.Minute},
		"GET /api/v1/media/{id}/original":        {Limit: 30, Period: time.Hour},
		"GET /api/v1/feeds/{token}/calendar.ics": {Limit: 120, Period: time.Hour},
		"POST /api/v1/issues":                    {Limit: 30, Period: time.Hour},
		"GET /api/v1/feeds/{token}/rss":          {Limit: 120, Period: time.Hour},
		"POST /api/v1/requests":                  {Limit: 60, Period: time.Hour},
	}
}

// metricsAuditSink records an authorization denial in both the audit log and
// the metrics, so a burst of denials is visible on a dashboard as well as in
// the log an operator has to remember to read.
type metricsAuditSink struct {
	inner *audit.Logger
	reg   *metrics.Registry
}

func (s metricsAuditSink) AuthzDenied(ctx context.Context, route string, d *authz.Denial, clientIP, userAgent string) {
	if d != nil {
		s.reg.AuthzDenials.WithLabelValues(d.Reason).Inc()
	}
	s.inner.AuthzDenied(ctx, route, d, clientIP, userAgent)
}

// sampleGauges reads the point-in-time values that no event feeds.
func sampleGauges(ctx context.Context, database *db.DB, reg *metrics.Registry) error {
	var sessions, tokens, pending int64

	if err := database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session WHERE revoked_at IS NULL`).Scan(&sessions); err != nil {
		return err
	}
	if err := database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM api_token WHERE revoked_at IS NULL`).Scan(&tokens); err != nil {
		return err
	}
	if err := database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM account_request WHERE state = 'pending'`).Scan(&pending); err != nil {
		return err
	}

	reg.SessionsLive.Set(float64(sessions))
	reg.TokensLive.Set(float64(tokens))
	reg.PendingAccountRequests.Set(float64(pending))
	return nil
}

// ---------------------------------------------------------------------------
// Egress wiring
// ---------------------------------------------------------------------------

// egressProfiles translates the configuration into the guard's policy.
//
// DenyPrivate is set on every profile that dials an address derived from
// something the operator did not write: an indexer's tracker URL, a magnet
// link, an artwork URL from a metadata provider, a subtitle mirror. Those are
// the SSRF paths. It is deliberately NOT set on profiles the operator controls
// end to end, because an operator pointing a notification webhook at a box on
// their own LAN is doing something reasonable.
func egressProfiles(cfg config.Config) map[string]egress.Profile {
	hostileInput := map[string]bool{
		"download": true, "indexer": true, "metadata": true, "subtitle": true,
	}

	out := make(map[string]egress.Profile, len(cfg.Egress.Profiles))
	for name, p := range cfg.Egress.Profiles {
		password := p.Password // a proxy saved from the web (ADR-0065)
		if password == "" {
			password = os.Getenv(p.PasswordEnv)
		}
		out[name] = egress.Profile{
			Mode:      egress.Mode(p.Mode),
			Address:   p.Address,
			Username:  p.Username,
			Password:  password,
			RemoteDNS: p.RemoteDNS,
			// A profile that is not in the map above defaults to false here,
			// but a profile that is not in the CONFIG does not reach this loop
			// at all and the guard blocks it outright. Forgetting a subsystem
			// costs it its network, not its containment.
			DenyPrivate: hostileInput[name],
		}
	}
	return out
}

// egressObserver records kill-switch transitions everywhere they matter.
//
// The metric is for the dashboard, the audit entry is for the question "when
// did this instance last egress without the tunnel", and the log line is for
// the operator watching right now. A kill-switch event that only updated a
// gauge would be invisible to anyone not already looking at Grafana.
type egressObserver struct {
	reg   *metrics.Registry
	audit *audit.Logger
	log   *slog.Logger
}

func (o egressObserver) EgressHealthChanged(healthy bool, detail string) {
	if healthy {
		o.reg.ProxyHealthy.Set(1)
		o.log.Info("egress tunnel verified", slog.String("detail", detail))
		return
	}
	o.reg.ProxyHealthy.Set(0)
	o.log.Error("EGRESS TUNNEL IS DOWN — transfers are paused", slog.String("detail", detail))
}

func (o egressObserver) EgressKillSwitchEngaged(detail string) {
	o.reg.KillSwitchEvents.Inc()
	_ = o.audit.Write(context.Background(), audit.Event{
		ActorLabel: "system",
		Action:     audit.ActionKillSwitch,
		Outcome:    audit.OutcomeFailure,
		TargetKind: "egress",
		TargetID:   "download",
		Detail:     detail,
	})
}

// runDownloader starts the download engine, and refuses to if it cannot prove
// it is behind the tunnel.
//
// Refusing to start is the whole point. A downloader that comes up unprotected
// has already broken the promise in §2, and it breaks it silently — the first
// evidence would be an infringement notice. Exiting non-zero is loud, and it is
// the failure mode an operator can actually respond to.
func runDownloader(cfg config.Config, logger *slog.Logger) error {
	verified := false
	if !cfg.Egress.RequireNamespaceGuard {
		// Turning the guard off is permitted, because an operator may have a
		// perimeter of their own. It is not permitted to be quiet.
		logger.Warn("egress.require_namespace_guard is false: " +
			"this process will not verify that downloads leave through a tunnel")
	} else {
		v, err := egress.Verify(cfg.Egress.TunnelInterface)
		if err != nil {
			logger.Error("refusing to start the download engine",
				slog.String("reason", v.Detail),
				slog.String("expected_interface", cfg.Egress.TunnelInterface),
				slog.Any("default_routes", v.DefaultRoutes))
			return fmt.Errorf("egress verification failed: %w", err)
		}
		logger.Info("egress verified",
			slog.String("interface", v.Interface),
			slog.String("source_ip", v.SourceIP),
			slog.String("detail", v.Detail))
		verified = true
	}

	// Verification happens BEFORE the engine is constructed, which is the order
	// the requirement asks for: the jail exists before the thing it contains,
	// so there is no window in which an unprotected downloader runs.
	if !verified {
		logger.Warn("starting the download engine WITHOUT egress verification: " +
			"nothing has checked that downloads leave through a tunnel")
	}

	guard := egress.New(egress.Config{
		Profiles:  egressProfiles(cfg),
		Exempt:    cfg.Egress.ExemptFromKillSwitch,
		Interface: cfg.Egress.TunnelInterface,
		Enforce:   cfg.Egress.AnonymityEnabled,
	})
	// The startup check above is what this process trusts. Marking the guard
	// healthy here reflects that decision rather than guessing: the alternative
	// is an engine that cannot dial because nothing has probed yet.
	guard.SetHealthy(true, "verified at startup")

	// verified is the namespace check from the top of this function — the one
	// that refused to start if it failed. It is the honest answer to "is a
	// namespace carrying the guarantee".
	dlCfg, notes := download.ConfigFor(
		cfg.Download.DataDir, cfg.Download.ListenPort,
		egressProfiles(cfg)[download.ProfileName],
		cfg.Egress.AnonymityEnabled, verified)
	dlCfg.MaxActive = cfg.Download.MaxActive
	dlCfg.Seed = cfg.Download.Seed

	engine, err := download.New(dlCfg, guard, notes)
	if err != nil {
		return fmt.Errorf("start the download engine: %w", err)
	}
	defer func() { _ = engine.Close() }()

	for _, note := range engine.Notes() {
		logger.Warn("download engine", slog.String("note", note))
	}
	logger.Info("download engine started",
		slog.Int("listen_port", engine.ListenPort()),
		slog.Bool("dht", dlCfg.EnableDHT),
		slog.Bool("utp", dlCfg.EnableUTP))

	// The split-process deployment needs an IPC seam so the app role can reach
	// this engine, and that seam does not exist yet. Saying so beats appearing
	// to be a working deployment: today this role is useful for verifying the
	// jail and for running the engine where the whole app is already inside the
	// namespace.
	logger.Warn("the downloader role runs the engine but exposes no interface to the app role; " +
		"for a single-container deployment set download.enabled and use --role app")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := watchEgress(ctx, cfg, logger, egress.Prober{
		Interface: cfg.Egress.TunnelInterface,
		Target:    cfg.Egress.ProbeTarget,
		Dial:      guard.ProbeDialer(download.ProfileName),
	}.Probe); err != nil {
		return err
	}
	logger.Info("shutting down the download engine")
	return nil
}

// watchEgress re-verifies the tunnel for as long as the process runs, and EXITS
// when it can no longer be verified.
//
// # Why exiting is the right answer
//
// Verifying once at startup is not enough, and the gap is not theoretical. If
// the tunnel container restarts — gluetun reconnecting, or being updated — the
// namespace this process joined is destroyed and a new one is created for the
// new container. This process stays in the OLD one. Observed: after restarting
// the tunnel container, the downloader was still "Up", and its namespace had no
// default route, no eth0, and a dead wg0. It was alive, inert, and silent.
//
// That is fail-closed, which is the important half: nothing fell back to direct
// egress, and a probe from inside that namespace could not resolve DNS or reach
// the gateway. But `restart: unless-stopped` never fires on a process that does
// not exit, so the container stays a zombie until a person notices that nothing
// has downloaded for a week.
//
// So the process ends itself. In a container that is not giving up, it is
// handing the problem to the thing that can actually fix it: the restart policy
// recreates the container, which joins the CURRENT namespace and verifies again
// — or refuses to start, which is the same guarantee as the first boot. The
// alternative, pausing transfers in-process, leaves something that needs a
// human to recover and looks healthy while it waits.
//
// # What this check can and cannot see
//
// It is the same probe the app role schedules: routing verification, plus an
// end-to-end connection when egress.probe_target is configured. Routing
// verification alone has a blind spot, and the orphaned namespace above is
// exactly it — after the tunnel container restarted, the stranded namespace
// still had `default dev wg0` and wg0 still UP, so the route check PASSED while
// nothing could leave at all (no eth0, and a probe from inside could not reach
// anything). A route says which interface the kernel would choose; it does not
// say that anything arrives.
//
// probe_target closes that, and is empty by default because dialing a third
// party every minute is a privacy cost this project does not impose on an
// operator who did not ask for it. Both halves are stated in the config file so
// the choice is informed rather than inherited.
func watchEgress(ctx context.Context, cfg config.Config, logger *slog.Logger,
	probe func(context.Context) (bool, string)) error {
	if !cfg.Egress.RequireNamespaceGuard {
		// The operator opted out at startup and was warned then. Re-checking
		// something nobody asked to be enforced would be noise.
		<-ctx.Done()
		return nil
	}

	interval := cfg.Egress.ProbeInterval
	if interval <= 0 {
		// Unset must not mean "never". The check costs a UDP socket that sends
		// nothing, so a floor is cheap and an unchecked tunnel is not.
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			healthy, detail := probe(ctx)
			if healthy {
				continue
			}
			if ctx.Err() != nil {
				// Shutting down. A probe cancelled by SIGTERM is not a tunnel
				// failure, and exiting non-zero for it would make every clean
				// stop look like one.
				return nil
			}
			logger.Error("the tunnel can no longer be verified; stopping so the "+
				"restart policy can rejoin the current namespace",
				slog.String("reason", detail),
				slog.String("expected_interface", cfg.Egress.TunnelInterface))
			return fmt.Errorf("%w: %s", egress.ErrNotJailed, detail)
		}
	}
}

// reportStalls writes each newly stalled download to the audit log, once, and
// says what was found (ADR-0034).
func reportStalls(ctx context.Context, log *audit.Logger, stalls []download.Stall, now time.Time) string {
	if len(stalls) == 0 {
		return ""
	}
	var given, reported []string
	for _, st := range stalls {
		quiet := now.Sub(st.Since).Round(time.Hour)
		what := "left running for the person who grabbed it to remove"
		if st.GivenUp {
			what = "given up; what it was for is wanted again"
			given = append(given, st.Record.Title)
		} else {
			reported = append(reported, st.Record.Title)
		}
		if err := log.Write(ctx, audit.Event{
			ActorLabel: download.StallLabel,
			Action:     audit.ActionDownloadStalled,
			Outcome:    audit.OutcomeSuccess,
			TargetKind: "release",
			TargetID:   st.Record.InfoHash,
			Detail: fmt.Sprintf("%s | no progress for %s, since %s | %s", st.Record.Title, quiet,
				st.Since.UTC().Format(time.RFC3339), what),
		}); err != nil {
			slog.Default().Error("a stalled download could not be audited",
				slog.String("info_hash", st.Record.InfoHash), slog.String("error", err.Error()))
		}
	}
	var parts []string
	if len(given) > 0 {
		parts = append(parts, fmt.Sprintf("gave up %d stalled automatic download(s): %s",
			len(given), strings.Join(given, "; ")))
	}
	if len(reported) > 0 {
		parts = append(parts, fmt.Sprintf("%d download(s) a person grabbed have stopped moving: %s",
			len(reported), strings.Join(reported, "; ")))
	}
	return strings.Join(parts, ". ")
}

// checkPack reads a pack's .torrent and says whether its files hold every
// wanted episode of the season (ADR-0033, decision 5). Only names are read.
func checkPack(torrent []byte, season int, want map[int]bool) (bool, string) {
	files, err := download.TorrentFiles(torrent)
	if err != nil {
		return false, err.Error()
	}
	cands := make([]importer.Candidate, 0, len(files))
	for _, f := range files {
		cands = append(cands, importer.Candidate{Path: f.Path, Bytes: f.Bytes})
	}
	return importer.CheckPack(cands, season, want)
}

// runImports takes every finished, not-yet-imported download into the library.
//
// Driven by a poll rather than a callback for the same reason completion is
// (ADR-0014): a poll reading the engine's own view has no ordering hazards and
// cannot wedge the torrent library's event loop if the database is slow.
//
// One failing download never stops the others. A tracker that served a
// malformed file, a root folder that filled up, a release whose name cannot be
// parsed — each of those is a reason to skip ONE import and record why, not to
// stop importing everything behind it.
// completedDownloads is what the import pass needs of the download manager.
type completedDownloads interface {
	Records(ctx context.Context) ([]download.Record, error)
	FilesOf(hash string) ([]download.TransferFile, error)
	FilesOnDisk(hash string) ([]download.TransferFile, error)
	DataPathFor(hash string) (string, error)
}

// albumImporter files a downloaded album track by track (ADR-0046).
type albumImporter interface {
	ImportDownload(ctx context.Context, dir string, files []string, albumID int64,
		infoHash string) (music.ImportResult, error)
}

// bookImporter files a downloaded book (ADR-0049).
type bookImporter interface {
	ImportDownload(ctx context.Context, dir string, files []string, itemID int64,
		infoHash string) (books.ImportResult, error)
}

func runImports(ctx context.Context, downloads completedDownloads,
	store *importer.Store, imp *importer.Importer, albums albumImporter, shelf bookImporter,
	logger *slog.Logger) (string, error) {

	records, err := downloads.Records(ctx)
	if err != nil {
		return "", err
	}

	imported, skipped := 0, 0
	for _, rec := range records {
		if rec.Status != download.StatusComplete {
			continue
		}
		// Imported ones never again, skipped ones hourly rather than every
		// pass, failed ones every pass (importer.Store.ShouldAttempt). A
		// season pack is judged file by file (ADR-0033).
		shouldAttempt := store.ShouldAttempt
		if rec.Target != nil && rec.Target.Pack {
			shouldAttempt = store.ShouldAttemptPack
		}
		attempt, err := shouldAttempt(ctx, rec.InfoHash)
		if err != nil {
			logger.Error("could not check whether a download should be imported",
				slog.String("info_hash", rec.InfoHash), slog.String("error", err.Error()))
			continue
		}
		if !attempt {
			continue
		}

		files, err := downloads.FilesOf(rec.InfoHash)
		if err != nil {
			// Complete, and no longer in the engine: seeding ended — at once,
			// when seeding is off — or this process restarted since it
			// finished. What it downloaded is still on disk, and that is what
			// an import reads. This used to say "nothing to import from, and
			// nothing alarming" and move on, which left such a download
			// complete and never imported, silently (found verifying
			// ADR-0026). A removed transfer does not reach here: its row is
			// stopped, not complete.
			files, err = downloads.FilesOnDisk(rec.InfoHash)
			if err != nil {
				// Recorded, so the queue's history says why, and retried
				// hourly rather than every pass (importer.SkipRetryInterval).
				skipped++
				if rerr := store.RecordOutcome(ctx, importer.Record{
					InfoHash: rec.InfoHash, Outcome: importer.OutcomeSkipped,
					Detail: "the download finished, but its files are not where it left them: " + err.Error(),
				}); rerr != nil {
					logger.Error("could not record why a download was not imported",
						slog.String("info_hash", rec.InfoHash), slog.String("error", rerr.Error()))
				}
				logger.Info("a completed download was not imported",
					slog.String("title", rec.Title), slog.String("reason", err.Error()))
				continue
			}
		}
		dir, err := downloads.DataPathFor(rec.InfoHash)
		if err != nil {
			logger.Error("a completed download has no usable data path",
				slog.String("info_hash", rec.InfoHash), slog.String("error", err.Error()))
			continue
		}

		if rec.Target != nil && rec.Target.Album > 0 {
			// An album goes to the music library, never to the video
			// importer to guess at (ADR-0046, decision 6).
			if importAlbum(ctx, albums, store, rec, dir, files, logger) {
				imported++
			} else {
				skipped++
			}
			continue
		}
		if rec.Target != nil && rec.Target.Book {
			// A book goes to the books library (ADR-0049, decision 5).
			if importBook(ctx, shelf, store, rec, dir, files, logger) {
				imported++
			} else {
				skipped++
			}
			continue
		}

		candidates := make([]importer.Candidate, 0, len(files))
		for _, f := range files {
			candidates = append(candidates, importer.Candidate{Path: f.Path, Bytes: f.Bytes})
		}

		res, err := imp.Import(ctx, importSource(rec, dir, candidates))
		switch {
		case err != nil:
			logger.Error("an import failed",
				slog.String("title", rec.Title),
				slog.String("info_hash", rec.InfoHash),
				slog.String("error", err.Error()))
		case res.Outcome == importer.OutcomeImported:
			imported++
			logger.Info("imported",
				slog.String("title", rec.Title),
				slog.String("detail", res.Detail),
				slog.Bool("hardlinked", res.Hardlinked))
		default:
			skipped++
			// At INFO, not DEBUG: "it downloaded and then nothing happened" is
			// the complaint this software earns, and the reason belongs
			// somewhere an operator will actually see it.
			logger.Info("a completed download was not imported",
				slog.String("title", rec.Title),
				slog.String("reason", res.Detail))
		}
	}

	switch {
	case imported == 0 && skipped == 0:
		return "", nil
	default:
		return fmt.Sprintf("imported %d, skipped %d", imported, skipped), nil
	}
}

// importAlbum files a download grabbed for an album and records what came of
// it against the download, as every import is recorded: imported when a track
// was placed or replaced, skipped with the reason otherwise, failed on an
// error. It reports whether anything was imported.
func importAlbum(ctx context.Context, albums albumImporter, store *importer.Store,
	rec download.Record, dir string, files []download.TransferFile, logger *slog.Logger) bool {
	out := importer.Record{InfoHash: rec.InfoHash, Outcome: importer.OutcomeSkipped}
	if albums == nil {
		out.Detail = "this download was grabbed for an album, and music is not wired"
	} else {
		paths := make([]string, 0, len(files))
		for _, f := range files {
			paths = append(paths, f.Path)
		}
		res, err := albums.ImportDownload(ctx, dir, paths, rec.Target.Album, rec.InfoHash)
		switch {
		case err != nil:
			out.Outcome, out.Detail = importer.OutcomeFailed, err.Error()
		case len(res.Placed)+len(res.Replaced) > 0:
			out.Outcome, out.Detail = importer.OutcomeImported, res.Summary()
		default:
			out.Detail = "no track of the album was filed: " + res.Summary()
		}
	}
	if err := store.RecordOutcome(ctx, out); err != nil {
		logger.Error("an album import outcome could not be recorded",
			slog.String("info_hash", rec.InfoHash), slog.String("error", err.Error()))
	}
	logger.Info("an album download was imported", slog.String("title", rec.Title),
		slog.String("outcome", out.Outcome), slog.String("detail", out.Detail))
	return out.Outcome == importer.OutcomeImported
}

// importBook files a download grabbed for a book and records what came of it
// against the download, as importAlbum does for an album.
func importBook(ctx context.Context, shelf bookImporter, store *importer.Store,
	rec download.Record, dir string, files []download.TransferFile, logger *slog.Logger) bool {
	out := importer.Record{InfoHash: rec.InfoHash, Outcome: importer.OutcomeSkipped}
	if shelf == nil {
		out.Detail = "this download was grabbed for a book, and books are not wired"
	} else {
		paths := make([]string, 0, len(files))
		for _, f := range files {
			paths = append(paths, f.Path)
		}
		res, err := shelf.ImportDownload(ctx, dir, paths, rec.Target.ItemID, rec.InfoHash)
		switch {
		case err != nil:
			out.Outcome, out.Detail = importer.OutcomeFailed, err.Error()
		case res.Placed != "":
			out.Outcome, out.Detail = importer.OutcomeImported, res.Summary()
		default:
			out.Detail = res.Summary()
		}
	}
	if err := store.RecordOutcome(ctx, out); err != nil {
		logger.Error("a book import outcome could not be recorded",
			slog.String("info_hash", rec.InfoHash), slog.String("error", err.Error()))
	}
	logger.Info("a book download was imported", slog.String("title", rec.Title),
		slog.String("outcome", out.Outcome), slog.String("detail", out.Detail))
	return out.Outcome == importer.OutcomeImported
}

// importSource describes a completed download to the importer — including what
// it was grabbed FOR, when it came from an episode search (ADR-0023). Dropping
// the target here would compile, pass every importer test, and quietly return
// every episode grab to guessing its series from the release name, which is
// why it is a function with a test of its own.
func importSource(rec download.Record, dir string, files []importer.Candidate) importer.Source {
	src := importer.Source{
		InfoHash:     rec.InfoHash,
		Dir:          dir,
		ReleaseTitle: rec.Title,
		Files:        files,
	}
	if rec.Target != nil {
		src.Target = &importer.Target{
			ItemID: rec.Target.ItemID, Season: rec.Target.Season, Episode: rec.Target.Episode,
			Film: rec.Target.Film, Pack: rec.Target.Pack, LastSeason: rec.Target.LastSeason,
		}
	}
	return src
}

// runScans reconciles every root folder with what is on disk.
//
// One root failing never stops the others: a disk that is not mounted is one
// root's problem, and leaving the rest of a library unreconciled because of it
// helps nobody.
// onDiskFulfiller closes the approved requests whose linked item now has a
// file (ADR-0028): a file a scan recorded, where no import happened to close
// them.
type onDiskFulfiller interface {
	FulfilOnDisk(ctx context.Context) (int64, error)
}

// rootScanner scans one root folder: the video importer, or the music library
// for a music root (ADR-0045).
type rootScanner interface {
	Scan(ctx context.Context, rootID int64) (importer.ScanResult, error)
}

func runScans(ctx context.Context, roots *library.RootStore,
	imp rootScanner, requests onDiskFulfiller, logger *slog.Logger) (string, error) {

	list, err := roots.List(ctx)
	if err != nil {
		return "", err
	}

	added, updated, problems := 0, 0, 0
	for _, rf := range list {
		res, err := imp.Scan(ctx, rf.ID)
		switch {
		case errors.Is(err, importer.ErrLibraryVanished):
			problems++
			// ERROR, not WARN. Most of a library disappearing at once is
			// either a disk that did not mount or something very wrong, and
			// both deserve waking somebody up.
			logger.Error("a root folder's files have mostly vanished; nothing was changed",
				slog.String("root", rf.Path), slog.String("detail", err.Error()))
		case err != nil:
			problems++
			logger.Error("a library scan failed",
				slog.String("root", rf.Path), slog.String("error", err.Error()))
		default:
			added += res.Added
			updated += res.Updated
			if res.Added > 0 || len(res.Missing) > 0 || res.Truncated {
				logger.Info("library scan",
					slog.String("root", rf.Path), slog.String("summary", res.Summary()))
			}
		}
	}

	// After the scans, whatever happened to them: a root that failed does not
	// make another root's file any less there. A failure here is logged and
	// does not fail the scan, whose records are written either way.
	var fulfilled int64
	if requests != nil {
		n, ferr := requests.FulfilOnDisk(ctx)
		if ferr != nil {
			logger.Error("requests whose item a scan found on disk could not be closed",
				slog.String("error", ferr.Error()))
		}
		fulfilled = n
	}

	if added == 0 && problems == 0 && fulfilled == 0 {
		return "", nil
	}
	out := fmt.Sprintf("%d added, %d already known, %d root folder(s) with problems",
		added, updated, problems)
	if fulfilled > 0 {
		out += fmt.Sprintf("; %d request(s) fulfilled by what is now on disk", fulfilled)
	}
	return out, nil
}

// probeReadiness asks the running instance whether it is ready, for use as a
// container healthcheck.
//
// # Why this exists rather than reusing --check
//
// --check validates configuration. As a healthcheck it answers the wrong
// question: a process whose listener has died, whose database has gone away, or
// which is wedged entirely, still has valid configuration. Combined with
// `restart: unless-stopped` that produces a container which is never restarted
// because it is never reported unhealthy.
//
// The image is distroless — no shell, no curl — so the binary probes itself.
// That is the standard shape for a distroless healthcheck and is why this is a
// flag rather than a script.
//
// It talks to the MANAGEMENT listener, which is the one bound to loopback and
// the one /readyz lives on. /readyz pings the database, so a passing probe
// means the process is alive AND its storage is reachable, which is the useful
// definition of ready for this application.
func probeReadiness(cfg config.Config) error {
	body, err := readyz(cfg)
	if err != nil {
		return err
	}
	fmt.Printf("ready: %s\n", body)
	return nil
}

// readyz asks the management listener whether the server is ready, and what
// it said.
func readyz(cfg config.Config) (string, error) {
	addr := cfg.Server.ManagementAddr
	if addr == "" {
		return "", fmt.Errorf("healthcheck: no management listener is configured, " +
			"so there is nothing to probe")
	}

	// A wildcard bind is not an address to dial. ":9090", "0.0.0.0:9090" and
	// "[::]:9090" all mean "every interface", and the one this process can
	// always reach is loopback.
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("healthcheck: management address %q: %w", addr, err)
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}

	// Short, because a healthcheck that hangs is a healthcheck that reports
	// nothing until Docker's own timeout kills it.
	client := &http.Client{Timeout: 3 * time.Second}
	url := "http://" + net.JoinHostPort(host, port) + "/readyz"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("healthcheck: %s: %w", url, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("healthcheck: %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("healthcheck: %s answered %d: %s",
			url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return strings.TrimSpace(string(body)), nil
}

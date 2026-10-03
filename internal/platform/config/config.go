// Package config loads, validates and security-lints CMediaStack configuration.
//
// Configuration comes from a YAML file, overlaid by environment variables
// prefixed CMS_. Every field has a safe default; the security lint in lint.go
// refuses to boot on insecure combinations (requirements doc §9).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RegistrationMode controls whether the signup routes are registered at all.
type RegistrationMode string

const (
	// RegistrationOpen allows anyone to submit an account request. The request
	// still requires admin approval before a User exists.
	RegistrationOpen RegistrationMode = "open"
	// RegistrationInvite requires a valid single-use invite code to submit.
	RegistrationInvite RegistrationMode = "invite"
	// RegistrationClosed removes the signup routes from the router entirely.
	// GET /signup 404s from the router, not from a handler.
	RegistrationClosed RegistrationMode = "closed"
)

// EgressMode is the transport policy for one subsystem's outbound traffic.
type EgressMode string

const (
	EgressDirect    EgressMode = "direct"
	EgressSOCKS5    EgressMode = "socks5"
	EgressHTTPProxy EgressMode = "http-proxy"
	EgressBlocked   EgressMode = "blocked"
)

// Config is the whole application configuration.
type Config struct {
	Server       ServerConfig       `yaml:"server"`
	Database     DatabaseConfig     `yaml:"database"`
	Auth         AuthConfig         `yaml:"auth"`
	Registration RegistrationConfig `yaml:"registration"`
	Secrets      SecretsConfig      `yaml:"secrets"`
	Logging      LoggingConfig      `yaml:"logging"`
	Egress       EgressConfig       `yaml:"egress"`
	Media        MediaConfig        `yaml:"media"`
	Download     DownloadConfig     `yaml:"download"`
	Acquisition  AcquisitionConfig  `yaml:"acquisition"`
	Backup       BackupConfig       `yaml:"backup"`
}

// Document is the configuration as the file spells it, for the settings screen
// (ADR-0040). It holds no secret: the master key is named by the environment
// variable that holds it, not held.
func (c Config) Document() (map[string]any, error) {
	raw, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// DefaultSourceURL is this project's repository: the source of an unmodified
// build (ADR-0052).
const DefaultSourceURL = "https://github.com/jakethecake75/cmediastack"

type ServerConfig struct {
	// Addr is the public application listener.
	Addr string `yaml:"addr"`
	// ManagementAddr serves /healthz, /readyz and /metrics. Bind it to
	// loopback or a management interface: requirements §7.1 forbids exposing
	// health or metrics to the internet.
	ManagementAddr string `yaml:"management_addr"`
	// TrustedProxies lists CIDRs permitted to set X-Forwarded-For. Empty means
	// forwarded headers are ignored entirely and RemoteAddr is authoritative.
	// Trusting a forwarded header from the internet makes every per-IP rate
	// limit spoofable (threat model §5.4).
	TrustedProxies []string `yaml:"trusted_proxies"`
	// BaseURL is used to build absolute links in notifications and emails.
	BaseURL string `yaml:"base_url"`
	// SourceURL is where every page says this build's source can be had, as
	// the AGPL's section 13 asks (ADR-0052). The default is right only for an
	// unmodified build: whoever runs a modified one owes their own source.
	SourceURL string `yaml:"source_url"`
	// TLSCertFile and TLSKeyFile enable direct TLS termination. Leave empty
	// when a reverse proxy terminates TLS.
	TLSCertFile string `yaml:"tls_cert_file"`
	TLSKeyFile  string `yaml:"tls_key_file"`

	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
	ShutdownGrace     time.Duration `yaml:"shutdown_grace"`
}

type DatabaseConfig struct {
	// Path to the SQLite file. ADR-0004: SQLite only for v1.
	Path string `yaml:"path"`
	// BusyTimeout for the WAL single-writer model.
	BusyTimeout time.Duration `yaml:"busy_timeout"`
}

type AuthConfig struct {
	// Argon2id parameters. Defaults follow OWASP's second recommended option
	// (19 MiB, t=2, p=1) which is documented in SECURITY.md.
	Argon2Memory      uint32 `yaml:"argon2_memory_kib"`
	Argon2Iterations  uint32 `yaml:"argon2_iterations"`
	Argon2Parallelism uint8  `yaml:"argon2_parallelism"`
	Argon2SaltLength  uint32 `yaml:"argon2_salt_length"`
	Argon2KeyLength   uint32 `yaml:"argon2_key_length"`

	// MFARequired forces TOTP enrollment for every user before any other route
	// is reachable. Jacob's requirement: mandatory for everyone, no exceptions.
	MFARequired bool `yaml:"mfa_required"`
	// TOTPIssuer is the label shown in the authenticator app.
	TOTPIssuer string `yaml:"totp_issuer"`
	// RecoveryCodeCount generated at enrollment.
	RecoveryCodeCount int `yaml:"recovery_code_count"`

	SessionIdleTimeout     time.Duration `yaml:"session_idle_timeout"`
	SessionAbsoluteTimeout time.Duration `yaml:"session_absolute_timeout"`

	// MinPasswordLength is a floor, not a composition rule.
	MinPasswordLength int `yaml:"min_password_length"`
	// BreachCheckEnabled enables the HIBP k-anonymity range check, performed
	// through the metadata egress profile.
	BreachCheckEnabled bool `yaml:"breach_check_enabled"`

	// LoginMaxAttempts before exponential backoff engages, per key.
	LoginMaxAttempts int           `yaml:"login_max_attempts"`
	LoginWindow      time.Duration `yaml:"login_window"`
}

type RegistrationConfig struct {
	Mode RegistrationMode `yaml:"mode"`
	// PendingTTL expires unreviewed requests. They are purged, not archived:
	// a pending request holds an email, a source IP and a password hash, and
	// the threat model flags an unbounded queue of those as a PII pile.
	PendingTTL time.Duration `yaml:"pending_ttl"`
	// MaxOutstanding caps the pending queue. Signup performs Argon2id hashing,
	// so an uncapped anonymous queue is a CPU exhaustion primitive (threat T7).
	MaxOutstanding int `yaml:"max_outstanding"`
	// PerIPPerHour throttles submissions from one source.
	PerIPPerHour int `yaml:"per_ip_per_hour"`
	// ProofOfWorkBits requires the client to present a hashcash-style proof
	// before the server spends Argon2id time. 0 disables.
	ProofOfWorkBits int `yaml:"proof_of_work_bits"`
}

type SecretsConfig struct {
	// MasterKeyEnv names the environment variable holding a base64 32-byte
	// master key used for AES-256-GCM envelope encryption. The key is never
	// read from the config file and never written to disk by the app.
	MasterKeyEnv string `yaml:"master_key_env"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"` // json | text
	// Modules enables per-module debug logging, e.g. {"indexer": "debug"}.
	Modules map[string]string `yaml:"modules"`
}

type EgressConfig struct {
	// AnonymityEnabled turns on the download-engine anonymity posture. When
	// true the security lint requires either a configured namespace guard or
	// a configured SOCKS5 proxy; it refuses to boot with neither.
	AnonymityEnabled bool `yaml:"anonymity_enabled"`
	// RequireNamespaceGuard asserts at startup that the downloader is running
	// inside a network namespace whose egress is firewall-restricted
	// (ADR-0001). This is the primary control; the application-level proxy is
	// defence in depth.
	RequireNamespaceGuard bool `yaml:"require_namespace_guard"`
	// TunnelInterface is the interface the operator expects outbound download
	// traffic to leave by, normally wg0. The startup check asks the kernel
	// which interface it would actually use and refuses to start the downloader
	// if the answer differs. Empty falls back to a name-prefix heuristic, which
	// is weaker and says so in the log.
	TunnelInterface string `yaml:"tunnel_interface"`
	// ProbeTarget is an optional host:port the health probe connects to through
	// the guarded dialer, proving the path works end to end rather than merely
	// looking right. Empty means routing verification alone decides health,
	// which reaches nothing and is the private default.
	ProbeTarget string `yaml:"probe_target"`
	// ProbeInterval is how often the tunnel is re-checked. A tunnel that drops
	// does not restart the process, so nothing notices until this runs.
	ProbeInterval time.Duration `yaml:"probe_interval"`
	// ExemptFromKillSwitch names profiles that keep working when the tunnel is
	// down. Deny by default: the list starts with notification only, because a
	// kill switch that also silences the alert telling you it fired is a kill
	// switch you find out about from your library being empty.
	ExemptFromKillSwitch []string `yaml:"exempt_from_kill_switch"`
	// Profiles maps a subsystem name to its transport policy. Nothing inherits
	// implicitly; a subsystem with no profile is treated as blocked.
	Profiles map[string]EgressProfile `yaml:"profiles"`
}

type EgressProfile struct {
	Mode EgressMode `yaml:"mode"`
	// Address is host:port for socks5 and http-proxy modes.
	Address string `yaml:"address"`
	// Username and Password authenticate to the proxy. Password is read from
	// the environment variable named by PasswordEnv, never from the file.
	Username    string `yaml:"username"`
	PasswordEnv string `yaml:"password_env"`
	// Password is set only by a proxy saved from the web (ADR-0065), laid over
	// the file at boot. It is never read from the file.
	Password string `yaml:"-"`
	// RemoteDNS selects socks5h semantics: hostnames are resolved by the
	// proxy, never by the host resolver.
	RemoteDNS bool `yaml:"remote_dns"`
}

// DownloadConfig is the acquisition engine's own settings.
type DownloadConfig struct {
	// Enabled starts the BitTorrent engine in this process.
	//
	// False is the honest default for a first run: an operator who has not yet
	// configured egress should not have a torrent client running, and turning
	// it on should be a decision they made rather than one they inherited.
	Enabled bool `yaml:"enabled"`
	// DataDir holds in-progress and completed torrent data. Files live under
	// DataDir/<infohash>/, NEVER under a directory named from the torrent —
	// a torrent's declared name is attacker-controlled and "../.." is a legal
	// bencode string.
	DataDir string `yaml:"data_dir"`
	// ListenPort is the peer port. Zero picks one.
	//
	// NordVPN forwards no ports, so nothing reaches this from outside and the
	// instance is a passive peer regardless (ADR-0001, accepted residual).
	ListenPort int `yaml:"listen_port"`
	// MaxActive bounds concurrent transfers.
	MaxActive int `yaml:"max_active"`
	// Seed keeps torrents seeding after they complete. Private trackers
	// require it; leaving it off is how an account gets banned.
	Seed bool `yaml:"seed"`
	// StallAfter is how long a download may make no progress before it is
	// stalled: one automatic acquisition grabbed is given up, a person's is
	// reported (ADR-0034). Zero turns it off; under an hour is refused.
	StallAfter time.Duration `yaml:"stall_after"`
}

// AcquisitionConfig is automatic acquisition: fetching what is wanted without a
// person (ADR-0030).
type AcquisitionConfig struct {
	// Automatic turns it on. Off by default, as the download engine is: an
	// instance whose indexers, egress and quality profile have not been set up
	// should not inherit a machine that fetches on its own.
	Automatic bool `yaml:"automatic"`
	// RSSInterval is how often each enabled indexer is asked for its recent
	// releases — one request per indexer per interval, however large the
	// library. Ten minutes at the least.
	RSSInterval time.Duration `yaml:"rss_interval"`
	// SearchInterval is how often wanted items are searched for, and
	// SearchesPerRun how many each time. One search asks every enabled
	// indexer, so these two are the request budget indexers see.
	SearchInterval time.Duration `yaml:"search_interval"`
	SearchesPerRun int           `yaml:"searches_per_run"`
	// MaxGrabsPerRun caps what one pass may add to the queue, so a flood of
	// mislabelled releases cannot queue the whole wanted list at once.
	MaxGrabsPerRun int `yaml:"max_grabs_per_run"`
	// Upgrades lets it replace a file below its title's profile's cutoff
	// with a better release (ADR-0036). Off by default: fetching what is
	// missing is not agreeing to replacing what is there.
	Upgrades bool `yaml:"upgrades"`
}

// BackupConfig is when the database is backed up and how long backups are kept
// (ADR-0029).
type BackupConfig struct {
	// Dir holds the encrypted backups. A relative path is resolved against the
	// database's own directory; empty means "backups" there, which is
	// /config/backups in the container.
	//
	// Beside the database is a default, not a recommendation: backups on the
	// database's disk die with it. A directory on another disk — the NAS the
	// media live on is a good one — is better. Only files named as backups are
	// ever deleted from it.
	Dir string `yaml:"dir"`
	// Interval is how old the newest backup may get before another is taken.
	// It is checked hourly and once at startup, against the backups on disk,
	// so a restart does not reset it. Zero turns scheduled backups off; a
	// backup can still be taken from the admin screen.
	Interval time.Duration `yaml:"interval"`
	// Keep is how long a backup is kept.
	Keep time.Duration `yaml:"keep"`
	// KeepMin is how many of the newest backups are kept however old they are,
	// so a schedule that has stopped working does not delete the last good
	// ones on its way out.
	KeepMin int `yaml:"keep_min"`
}

// BackupDir is where backups go: backup.dir, resolved against the database's
// directory when it is relative.
func (c Config) BackupDir() string {
	dir := strings.TrimSpace(c.Backup.Dir)
	if dir == "" {
		dir = "backups"
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Join(filepath.Dir(c.Database.Path), dir)
}

type MediaConfig struct {
	// TrashRetention is how long soft-deleted files remain recoverable.
	TrashRetention time.Duration `yaml:"trash_retention"`
	// PlaybackHistoryRetention bounds per-user playback history (§8 PII).
	PlaybackHistoryRetention time.Duration `yaml:"playback_history_retention"`
	// MaxConcurrentTranscodes is a hard admission-control ceiling. 0 means
	// derive it from the runtime hardware probe (ADR-0005).
	MaxConcurrentTranscodes int `yaml:"max_concurrent_transcodes"`
}

// Default returns a configuration with every field set to a safe value.
// It is deliberately usable as-is for a first run on loopback.
func Default() Config {
	return Config{
		Server: ServerConfig{
			Addr:              "0.0.0.0:8080",
			ManagementAddr:    "127.0.0.1:9090",
			TrustedProxies:    nil,
			BaseURL:           "http://localhost:8080",
			SourceURL:         DefaultSourceURL,
			ReadHeaderTimeout: 10 * time.Second,
			ShutdownGrace:     20 * time.Second,
		},
		Database: DatabaseConfig{
			Path:        "/config/cmediastack.db",
			BusyTimeout: 5 * time.Second,
		},
		Auth: AuthConfig{
			Argon2Memory:           19456, // 19 MiB
			Argon2Iterations:       2,
			Argon2Parallelism:      1,
			Argon2SaltLength:       16,
			Argon2KeyLength:        32,
			MFARequired:            true,
			TOTPIssuer:             "CMediaStack",
			RecoveryCodeCount:      10,
			SessionIdleTimeout:     12 * time.Hour,
			SessionAbsoluteTimeout: 30 * 24 * time.Hour,
			MinPasswordLength:      12,
			BreachCheckEnabled:     true,
			LoginMaxAttempts:       5,
			LoginWindow:            15 * time.Minute,
		},
		Registration: RegistrationConfig{
			Mode:            RegistrationOpen,
			PendingTTL:      14 * 24 * time.Hour,
			MaxOutstanding:  50,
			PerIPPerHour:    3,
			ProofOfWorkBits: 18,
		},
		Secrets: SecretsConfig{
			MasterKeyEnv: "CMS_MASTER_KEY",
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Egress: EgressConfig{
			AnonymityEnabled:      false,
			RequireNamespaceGuard: true,
			TunnelInterface:       "wg0",
			ProbeInterval:         60 * time.Second,
			ExemptFromKillSwitch:  []string{"notification"},
			Profiles: map[string]EgressProfile{
				"download":     {Mode: EgressDirect},
				"indexer":      {Mode: EgressDirect},
				"metadata":     {Mode: EgressDirect},
				"subtitle":     {Mode: EgressDirect},
				"notification": {Mode: EgressDirect},
				"update":       {Mode: EgressBlocked},
			},
		},
		Download: DownloadConfig{
			Enabled:    false,
			DataDir:    "/downloads",
			ListenPort: 0,
			MaxActive:  5,
			Seed:       true,
			StallAfter: 24 * time.Hour,
		},
		Media: MediaConfig{
			TrashRetention:           7 * 24 * time.Hour,
			PlaybackHistoryRetention: 180 * 24 * time.Hour,
			MaxConcurrentTranscodes:  0,
		},
		Acquisition: AcquisitionConfig{
			Automatic:      false,
			RSSInterval:    15 * time.Minute,
			SearchInterval: 15 * time.Minute,
			SearchesPerRun: 3,
			MaxGrabsPerRun: 5,
		},
		// On by default. An instance about to start its library from nothing is
		// exactly when a backup regime should begin, not after the first loss.
		Backup: BackupConfig{
			Dir:      "",
			Interval: 24 * time.Hour,
			Keep:     7 * 24 * time.Hour,
			KeepMin:  3,
		},
	}
}

// Load reads configuration from path (if it exists), applies CMS_*
// environment overrides, then runs the security lint. A lint failure is a
// boot failure: the process must not start in an insecure configuration.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		raw, err := os.ReadFile(path) // #nosec G304 -- the operator's own --config path
		switch {
		case err == nil:
			if err := yaml.Unmarshal(raw, &cfg); err != nil {
				return Config{}, fmt.Errorf("parse config %s: %w", path, err)
			}
		case os.IsNotExist(err):
			// A missing file is not an error: defaults plus environment are a
			// complete configuration. First-run deployments rely on this.
		default:
			return Config{}, fmt.Errorf("read config %s: %w", path, err)
		}
	}

	applyEnv(&cfg, os.Getenv)

	if err := Lint(cfg, os.Getenv); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// applyEnv overlays CMS_* environment variables onto cfg. Only the fields an
// operator realistically needs to override in a container are supported;
// everything else belongs in the file, where it can be reviewed.
func applyEnv(cfg *Config, getenv func(string) string) {
	str := func(key string, dst *string) {
		if v := getenv(key); v != "" {
			*dst = v
		}
	}
	boolean := func(key string, dst *bool) {
		if v := getenv(key); v != "" {
			if b, err := strconv.ParseBool(v); err == nil {
				*dst = b
			}
		}
	}
	integer := func(key string, dst *int) {
		if v := getenv(key); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}

	str("CMS_SERVER_ADDR", &cfg.Server.Addr)
	str("CMS_MANAGEMENT_ADDR", &cfg.Server.ManagementAddr)
	str("CMS_BASE_URL", &cfg.Server.BaseURL)
	str("CMS_SOURCE_URL", &cfg.Server.SourceURL)
	str("CMS_TLS_CERT_FILE", &cfg.Server.TLSCertFile)
	str("CMS_TLS_KEY_FILE", &cfg.Server.TLSKeyFile)
	str("CMS_DATABASE_PATH", &cfg.Database.Path)
	str("CMS_LOG_LEVEL", &cfg.Logging.Level)
	str("CMS_LOG_FORMAT", &cfg.Logging.Format)
	boolean("CMS_ANONYMITY_ENABLED", &cfg.Egress.AnonymityEnabled)
	boolean("CMS_REQUIRE_NAMESPACE_GUARD", &cfg.Egress.RequireNamespaceGuard)
	boolean("CMS_DOWNLOAD_ENABLED", &cfg.Download.Enabled)
	boolean("CMS_ACQUISITION_AUTOMATIC", &cfg.Acquisition.Automatic)
	boolean("CMS_ACQUISITION_UPGRADES", &cfg.Acquisition.Upgrades)
	str("CMS_DOWNLOAD_DATA_DIR", &cfg.Download.DataDir)
	str("CMS_TUNNEL_INTERFACE", &cfg.Egress.TunnelInterface)
	str("CMS_BACKUP_DIR", &cfg.Backup.Dir)
	str("CMS_EGRESS_PROBE_TARGET", &cfg.Egress.ProbeTarget)
	boolean("CMS_MFA_REQUIRED", &cfg.Auth.MFARequired)
	integer("CMS_REGISTRATION_MAX_OUTSTANDING", &cfg.Registration.MaxOutstanding)

	if v := getenv("CMS_REGISTRATION_MODE"); v != "" {
		cfg.Registration.Mode = RegistrationMode(strings.ToLower(v))
	}
	if v := getenv("CMS_TRUSTED_PROXIES"); v != "" {
		cfg.Server.TrustedProxies = splitAndTrim(v)
	}
}

func splitAndTrim(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

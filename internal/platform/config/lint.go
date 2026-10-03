package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/egress"
)

// LintError collects every configuration problem found, so an operator fixes
// them in one pass rather than one boot at a time.
type LintError struct {
	Problems []string
}

func (e *LintError) Error() string {
	return "insecure or invalid configuration:\n  - " + strings.Join(e.Problems, "\n  - ")
}

// Lint refuses to boot on insecure combinations (requirements §9). Every rule
// here exists because the alternative is a silent security failure, not a
// stylistic preference.
//
// getenv is injected so the lint is testable without mutating process
// environment.
func Lint(cfg Config, getenv func(string) string) error {
	var p []string

	// --- Egress ------------------------------------------------------------
	// The named case from §9: anonymity on with nothing to enforce it.
	if cfg.Egress.AnonymityEnabled {
		dl, ok := cfg.Egress.Profiles["download"]
		hasProxy := ok && (dl.Mode == EgressSOCKS5 || dl.Mode == EgressHTTPProxy) && dl.Address != ""
		if !cfg.Egress.RequireNamespaceGuard && !hasProxy {
			p = append(p, "egress.anonymity_enabled is true but there is no enforcement: "+
				"set egress.require_namespace_guard (recommended, see ADR-0001) or configure "+
				"a socks5 address on the 'download' egress profile")
		}
		if ok && dl.Mode == EgressSOCKS5 && !dl.RemoteDNS {
			p = append(p, "egress.profiles.download uses socks5 without remote_dns: "+
				"hostnames would be resolved by the host resolver, leaking tracker and peer "+
				"lookups outside the tunnel (socks5h semantics are required)")
		}
		// "direct" with the namespace guard on is the RECOMMENDED deployment,
		// not a mistake: under ADR-0001 direct means direct inside a network
		// namespace whose only route out is the tunnel. This rule previously
		// refused that combination outright, which meant an operator following
		// the comments in config.example.yaml could not boot — the lint and the
		// documented configuration contradicted each other, and the lint won.
		//
		// What is genuinely wrong is "direct" with no namespace guard and no
		// proxy, and the check above already covers exactly that.
		if ok && dl.Mode == EgressDirect && !cfg.Egress.RequireNamespaceGuard {
			p = append(p, "egress.anonymity_enabled is true and egress.profiles.download "+
				"is 'direct' with no namespace guard: downloads would leave by the host's "+
				"own route. Set egress.require_namespace_guard (see ADR-0001) or use socks5")
		}
	}

	// A probe target the guarded dialer will always refuse.
	//
	// The dialer blocks private, loopback and link-local destinations — that is
	// the SSRF rule, and it applies to the probe like everything else. So a
	// probe_target of "192.168.1.1:443" or "10.0.0.1:53" — both entirely
	// natural choices for "somewhere I know I can reach" — can never succeed.
	//
	// The consequence is not subtle and is not survivable: the downloader now
	// exits when the probe fails, so a private target turns it into a container
	// that crash-loops forever, with a message about the tunnel that is about
	// the address instead. Better to refuse at boot and say which.
	//
	// Only literal addresses are checked. A hostname resolves at dial time and
	// the answer can change, so a boot-time lookup would be both slow and a
	// claim the lint cannot honestly make.
	//
	// egress.IsRestricted is called rather than reimplemented. A copy was
	// written first, and it was already wrong — it missed CGNAT and the
	// TEST-NET ranges the dialer also refuses, so the lint would have passed
	// targets the dialer then rejected, which is the exact disagreement this
	// check exists to prevent. internal/egress imports nothing from here, so
	// there is no cycle and no reason for a second copy.
	if target := strings.TrimSpace(cfg.Egress.ProbeTarget); target != "" {
		host, _, err := net.SplitHostPort(target)
		switch {
		case err != nil:
			p = append(p, fmt.Sprintf("egress.probe_target %q is not host:port", target))
		default:
			if ip := net.ParseIP(host); ip != nil && egress.IsRestricted(ip) {
				p = append(p, fmt.Sprintf(
					"egress.probe_target %q is a private, loopback or link-local address. "+
						"The guarded dialer refuses those, so this probe can never "+
						"succeed and the downloader would crash-loop reporting a "+
						"tunnel failure that is really this setting. Use a public "+
						"host:port, or leave it empty for routing verification alone",
					target))
			}
		}
	}

	// --- The half-tunnelled instance ---------------------------------------
	//
	// A subtler mistake than the ones above, and one nothing else would catch:
	// tunnelling the traffic that LOOKS sensitive while leaving the traffic that
	// discloses the same thing on the host's own route.
	//
	// An indexer search says "somebody here is looking for X". A metadata
	// lookup says "this instance holds X", and an artwork fetch says it again
	// from a second host. A configuration that proxies the first and not the
	// others has not protected the operator; it has made them believe they are
	// protected, which is worse than knowing they are not.
	//
	// Only ever a complaint when the indexer profile is ACTUALLY proxied — if an
	// operator has chosen direct throughout, that is a decision, not a mistake,
	// and this lint has nothing to say about it.
	if ix, ok := cfg.Egress.Profiles["indexer"]; ok && isProxied(ix) {
		for _, name := range []string{"metadata", "subtitle"} {
			other, present := cfg.Egress.Profiles[name]
			if !present || other.Mode == EgressBlocked {
				continue // nothing leaves by that route at all
			}
			if !isProxied(other) {
				p = append(p, "egress.profiles.indexer is proxied but egress.profiles."+
					name+" is '"+string(other.Mode)+"': a "+name+" lookup discloses what "+
					"this instance holds just as an indexer search discloses what it wants, "+
					"so tunnelling one and not the other leaves the library announced from "+
					"the host's own address. Set the same mode on both, or set "+name+
					" to 'blocked' if it should not be used at all")
			}
		}
	}

	for name, prof := range cfg.Egress.Profiles {
		switch prof.Mode {
		case EgressDirect, EgressBlocked:
			// nothing to validate
		case EgressSOCKS5, EgressHTTPProxy:
			if prof.Address == "" {
				p = append(p, fmt.Sprintf("egress.profiles.%s has mode %q but no address", name, prof.Mode))
				continue
			}
			if _, _, err := net.SplitHostPort(prof.Address); err != nil {
				p = append(p, fmt.Sprintf("egress.profiles.%s address %q is not host:port", name, prof.Address))
			}
			if prof.Username != "" && prof.PasswordEnv == "" && prof.Password == "" {
				p = append(p, fmt.Sprintf("egress.profiles.%s sets a username but no password_env; "+
					"proxy passwords are read from the environment, never from the config file", name))
			}
			if prof.PasswordEnv != "" && getenv(prof.PasswordEnv) == "" {
				p = append(p, fmt.Sprintf("egress.profiles.%s references environment variable %s, which is empty",
					name, prof.PasswordEnv))
			}
		default:
			p = append(p, fmt.Sprintf("egress.profiles.%s has unknown mode %q "+
				"(want direct, socks5, http-proxy or blocked)", name, prof.Mode))
		}
	}

	// --- Secrets -----------------------------------------------------------
	if cfg.Secrets.MasterKeyEnv == "" {
		p = append(p, "secrets.master_key_env is empty: there is nowhere to read the encryption key from")
	} else {
		raw := getenv(cfg.Secrets.MasterKeyEnv)
		if raw == "" {
			p = append(p, fmt.Sprintf("environment variable %s is not set; it must contain a "+
				"base64-encoded 32-byte master key (generate with: head -c32 /dev/urandom | base64)",
				cfg.Secrets.MasterKeyEnv))
		} else {
			key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
			if err != nil {
				p = append(p, fmt.Sprintf("environment variable %s is not valid base64", cfg.Secrets.MasterKeyEnv))
			} else if len(key) != 32 {
				p = append(p, fmt.Sprintf("environment variable %s decodes to %d bytes, want exactly 32",
					cfg.Secrets.MasterKeyEnv, len(key)))
			}
		}
	}

	// --- The source offer (ADR-0052) -----------------------------------------
	// A link on every page, including the ones a stranger sees: an absolute
	// http(s) address and nothing else — a javascript: URL there is a script.
	if u, err := url.Parse(cfg.Server.SourceURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		p = append(p, fmt.Sprintf("server.source_url %q is not an absolute http or https address; "+
			"every page links to it as where this build's source can be had (AGPL-3.0, section 13)",
			cfg.Server.SourceURL))
	}

	// --- Auth --------------------------------------------------------------
	if !cfg.Auth.MFARequired {
		// Not fatal on its own, but it is a deviation from the deployment this
		// was designed for, so it must be a deliberate act rather than a typo.
		p = append(p, "auth.mfa_required is false: this deployment was specified with mandatory "+
			"authenticator-app MFA for every user; set CMS_MFA_REQUIRED=true or remove the override")
	}
	if cfg.Auth.MinPasswordLength < 8 {
		p = append(p, fmt.Sprintf("auth.min_password_length is %d; refuse anything below 8",
			cfg.Auth.MinPasswordLength))
	}
	if cfg.Auth.Argon2Memory < 9216 {
		p = append(p, fmt.Sprintf("auth.argon2_memory_kib is %d KiB; OWASP's floor for argon2id "+
			"is 9216 KiB at t=4, and this build defaults to 19456 KiB at t=2", cfg.Auth.Argon2Memory))
	}
	if cfg.Auth.Argon2Iterations < 1 {
		p = append(p, "auth.argon2_iterations must be at least 1")
	}
	if cfg.Auth.Argon2Parallelism < 1 {
		p = append(p, "auth.argon2_parallelism must be at least 1")
	}
	if cfg.Auth.Argon2SaltLength < 16 {
		p = append(p, "auth.argon2_salt_length must be at least 16 bytes")
	}
	if cfg.Auth.Argon2KeyLength < 32 {
		p = append(p, "auth.argon2_key_length must be at least 32 bytes")
	}
	if cfg.Auth.RecoveryCodeCount < 5 {
		p = append(p, "auth.recovery_code_count must be at least 5: with mandatory MFA, "+
			"recovery codes are the only path back into a locked-out account")
	}
	if cfg.Auth.SessionAbsoluteTimeout <= cfg.Auth.SessionIdleTimeout {
		p = append(p, "auth.session_absolute_timeout must exceed auth.session_idle_timeout")
	}

	// --- Registration ------------------------------------------------------
	switch cfg.Registration.Mode {
	case RegistrationOpen, RegistrationInvite, RegistrationClosed:
	default:
		p = append(p, fmt.Sprintf("registration.mode %q is unknown (want open, invite or closed)",
			cfg.Registration.Mode))
	}
	if cfg.Registration.Mode == RegistrationOpen {
		// Open signup on a public listener is the highest-risk supported
		// configuration (threat model P1). The caps are what make it survivable.
		if cfg.Registration.MaxOutstanding <= 0 {
			p = append(p, "registration.max_outstanding must be > 0 when registration.mode is 'open': "+
				"signup performs argon2id hashing, so an uncapped anonymous queue is a CPU exhaustion primitive")
		}
		if cfg.Registration.PerIPPerHour <= 0 {
			p = append(p, "registration.per_ip_per_hour must be > 0 when registration.mode is 'open'")
		}
		if cfg.Registration.PendingTTL <= 0 {
			p = append(p, "registration.pending_ttl must be > 0: pending requests hold an email, "+
				"a source IP and a password hash, and must expire")
		}
	}

	// --- Server ------------------------------------------------------------
	if cfg.Server.Addr == "" {
		p = append(p, "server.addr is empty")
	}
	if cfg.Server.ManagementAddr == "" {
		p = append(p, "server.management_addr is empty: /healthz and /metrics must have a listener "+
			"separate from the public one")
	}
	if cfg.Server.ManagementAddr == cfg.Server.Addr {
		p = append(p, "server.management_addr equals server.addr: /metrics and /health/detail would "+
			"be reachable on the public listener")
	}
	if isPubliclyBound(cfg.Server.ManagementAddr) {
		p = append(p, fmt.Sprintf("server.management_addr %q is bound to all interfaces; bind it to "+
			"loopback or a management interface", cfg.Server.ManagementAddr))
	}
	for _, cidr := range cfg.Server.TrustedProxies {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			if _, err2 := netip.ParseAddr(cidr); err2 != nil {
				p = append(p, fmt.Sprintf("server.trusted_proxies entry %q is neither a CIDR nor an IP", cidr))
			}
		}
	}
	if (cfg.Server.TLSCertFile == "") != (cfg.Server.TLSKeyFile == "") {
		p = append(p, "server.tls_cert_file and server.tls_key_file must be set together")
	}

	// --- Logging -----------------------------------------------------------
	switch strings.ToLower(cfg.Logging.Level) {
	case "debug", "info", "warn", "error":
	default:
		p = append(p, fmt.Sprintf("logging.level %q is unknown (want debug, info, warn or error)", cfg.Logging.Level))
	}
	switch strings.ToLower(cfg.Logging.Format) {
	case "json", "text":
	default:
		p = append(p, fmt.Sprintf("logging.format %q is unknown (want json or text)", cfg.Logging.Format))
	}

	// --- Database ----------------------------------------------------------
	if cfg.Database.Path == "" {
		p = append(p, "database.path is empty")
	}

	// --- Stalled downloads (ADR-0034) ----------------------------------------
	if s := cfg.Download.StallAfter; s != 0 && s < time.Hour {
		p = append(p, fmt.Sprintf("download.stall_after is %s; it must be 0 (off) or at least 1h — "+
			"a swarm that is slow to find is not a dead one", s))
	}

	// --- Upgrades (ADR-0036) --------------------------------------------------
	if cfg.Acquisition.Upgrades && !cfg.Acquisition.Automatic {
		p = append(p, "acquisition.upgrades is true but acquisition.automatic is false: "+
			"upgrades are fetched by automatic acquisition. Turn it on, or upgrades off")
	}

	// --- Automatic acquisition (ADR-0030) --------------------------------------
	if a := cfg.Acquisition; a.Automatic {
		if !cfg.Download.Enabled {
			p = append(p, "acquisition.automatic is true but download.enabled is false: nothing "+
				"could be fetched. Turn the download engine on, or automatic acquisition off")
		}
		if a.RSSInterval < 10*time.Minute {
			// Indexers ban for request volume, and a recent-releases page a few
			// minutes old holds nothing a page fifteen minutes old does not.
			p = append(p, fmt.Sprintf("acquisition.rss_interval is %s; indexers ban accounts "+
				"for request volume, so it must be at least 10m", a.RSSInterval))
		}
		if a.SearchInterval < 5*time.Minute {
			p = append(p, fmt.Sprintf("acquisition.search_interval is %s; it must be at least 5m",
				a.SearchInterval))
		}
		if a.SearchesPerRun < 1 || a.SearchesPerRun > 50 {
			p = append(p, fmt.Sprintf("acquisition.searches_per_run is %d; it must be 1 to 50 — "+
				"each search asks every enabled indexer", a.SearchesPerRun))
		}
		if a.MaxGrabsPerRun < 1 || a.MaxGrabsPerRun > 100 {
			p = append(p, fmt.Sprintf("acquisition.max_grabs_per_run is %d; it must be 1 to 100",
				a.MaxGrabsPerRun))
		}
	}

	// --- Backups (ADR-0029) --------------------------------------------------
	switch iv := cfg.Backup.Interval; {
	case iv < 0:
		p = append(p, fmt.Sprintf("backup.interval is %s; use a positive duration, or 0s to turn "+
			"scheduled backups off", iv))
	case iv > 0 && iv < time.Hour:
		// The check runs hourly, so a shorter interval is a promise the
		// schedule cannot keep — and an operator who wrote "15m" believes their
		// losses are bounded by fifteen minutes when they are bounded by an hour.
		p = append(p, fmt.Sprintf("backup.interval is %s, but whether a backup is due is "+
			"checked hourly, so nothing shorter than 1h can be honoured; use at least 1h, "+
			"or 0s to turn scheduled backups off", iv))
	}
	if cfg.Backup.Keep <= 0 {
		p = append(p, "backup.keep must be a positive duration: it is how long a backup is kept")
	}
	if cfg.Backup.KeepMin < 1 {
		// Zero would let pruning delete the only backup there is.
		p = append(p, fmt.Sprintf("backup.keep_min is %d; it must be at least 1, or pruning "+
			"could delete the last backup there is", cfg.Backup.KeepMin))
	}

	if len(p) > 0 {
		return &LintError{Problems: p}
	}
	return nil
}

// isPubliclyBound reports whether addr binds to all interfaces.
func isPubliclyBound(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return host == "" || host == "0.0.0.0" || host == "::" || host == "[::]"
}

// AsLintError extracts a *LintError from err, if it is one.
func AsLintError(err error) (*LintError, bool) {
	var le *LintError
	ok := errors.As(err, &le)
	return le, ok
}

// isProxied reports whether a profile's traffic leaves by something other than
// the host's own route.
//
// A namespace-guarded "direct" is deliberately NOT counted here. Under ADR-0001
// the guard applies to the download process's network namespace, and the
// application process — which is what makes metadata and artwork requests —
// does not run in it. Treating direct as proxied because a guard exists
// elsewhere is exactly the false reassurance this check exists to prevent.
func isProxied(p EgressProfile) bool {
	return p.Mode == EgressSOCKS5 || p.Mode == EgressHTTPProxy
}

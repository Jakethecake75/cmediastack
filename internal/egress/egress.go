// Package egress is the only way outbound traffic leaves the download engine.
//
// # The requirement
//
// Requirements §2: the download engine must never egress outside its configured
// tunnel, and if the tunnel is unreachable, downloads pause — they never fall
// back to a direct connection. That is a statement about what CANNOT happen,
// and a check that runs before each dial does not establish it: the failure
// mode that matters is a code path that never consults the check at all.
//
// So the guarantee is built in two layers, and neither is this package alone.
//
// # Layer one: the kernel (ADR-0001)
//
// The downloader runs inside a network namespace whose only route out is a
// WireGuard interface, behind a default-deny firewall it holds no capability to
// modify. If this package were removed entirely, a direct connection would
// still be impossible, because there is no route for one. That is the control
// the promise actually rests on.
//
// Verify() proves the process is in that namespace before the download engine
// starts, and the downloader role refuses to run if it cannot. Refusing to
// start is the correct behaviour: a downloader that runs unprotected has
// already broken the promise, and it breaks it silently.
//
// # Layer two: this package
//
// Application-level proxying is defence in depth, demoted from its original
// place as the primary control when NordVPN turned out to offer no port
// forwarding and no reliable UDP relay (ADR-0001). It still earns its place:
//
//   - Guard.DialContext returns ErrEgressUnavailable when the tunnel is not
//     verified healthy, and returns no connection. There is no fallback branch
//     in this package — not a direct dial, not a retry without the proxy — and
//     TestNoFallbackWhenUnhealthy asserts it by exhausting every mode.
//   - A subsystem with no configured profile is BLOCKED, not direct. Nothing
//     inherits an escape route by being forgotten.
//   - Health starts DOWN and becomes healthy only after a probe succeeds. An
//     unset gauge and a healthy gauge look identical on a dashboard.
//   - TestNoPackageDialsDirectly reads the source of the subsystems that handle
//     hostile input and fails the build on net.Dial, http.Get and friends. The
//     structural property is the point; a runtime check cannot prove a path
//     does not exist.
package egress

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Errors callers are expected to distinguish.
var (
	// ErrEgressUnavailable means the tunnel is not verified healthy. It is
	// terminal for that attempt: the caller pauses, and never retries by
	// another route.
	ErrEgressUnavailable = errors.New("egress: tunnel is not healthy; transfers are paused")

	// ErrBlocked means the subsystem's profile forbids outbound traffic. A
	// subsystem with no profile at all gets this too.
	ErrBlocked = errors.New("egress: this subsystem is not permitted to make outbound connections")

	// ErrPrivateAddress means the destination resolved inside a range that
	// hostile input must not be able to reach.
	ErrPrivateAddress = errors.New("egress: destination is a private, loopback or link-local address")
)

// Mode is a profile's transport policy. It mirrors config.EgressMode, kept
// separate so this package can be tested without a config.
type Mode string

const (
	ModeDirect    Mode = "direct"
	ModeSOCKS5    Mode = "socks5"
	ModeHTTPProxy Mode = "http-proxy"
	ModeBlocked   Mode = "blocked"
)

// Profile is one subsystem's transport policy.
type Profile struct {
	Mode      Mode
	Address   string // host:port of the proxy, for socks5 and http-proxy
	Username  string
	Password  string
	RemoteDNS bool // socks5h semantics: the proxy resolves, we never do

	// DenyPrivate refuses destinations in private, loopback, link-local and
	// cloud-metadata ranges. Set for every profile that dials an address
	// derived from hostile input — an indexer's tracker URL, a magnet link, a
	// redirect — which is the SSRF control for §6.
	DenyPrivate bool
}

// Dialer is what every subsystem takes. Nothing in the download path may hold a
// *net.Dialer, an http.Client it built itself, or anything else that can reach
// the network without passing through here.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Observer receives kill-switch transitions so the metrics registry and the
// audit log can record them. It must not block.
type Observer interface {
	EgressHealthChanged(healthy bool, detail string)
	EgressKillSwitchEngaged(detail string)
}

// Guard holds the health state and hands out dialers.
//
// One Guard serves the whole process. Health is a property of the tunnel, not
// of a subsystem, so a tunnel failure pauses every profile that depends on it
// at once rather than each discovering it separately.
type Guard struct {
	mu       sync.RWMutex
	healthy  bool
	detail   string
	since    time.Time
	profiles map[string]Profile

	// tunnelled names the profiles whose traffic depends on the tunnel. A
	// profile dialing direct inside the namespace still depends on it; only a
	// profile explicitly exempted by the operator does not.
	tunnelled map[string]bool

	// enforce is false when the operator has not asked for tunnel enforcement
	// at all. See Config.Enforce.
	enforce bool

	iface    string
	observer Observer
	now      func() time.Time
	// base is the dialer used once policy has allowed a connection. It exists
	// so tests can substitute one; production passes nil and gets net.Dialer.
	base *net.Dialer
}

// Config builds a Guard.
type Config struct {
	Profiles map[string]Profile
	// Exempt names profiles that must keep working when the tunnel is down —
	// in practice none of the acquisition ones. Metadata and notification
	// traffic does not carry the operator's acquisition activity, so pausing
	// it when the tunnel drops would break the UI for no privacy gain.
	Exempt []string
	// Enforce turns the kill switch on. It mirrors egress.anonymity_enabled.
	//
	// When it is FALSE the operator has not asked for a tunnel, and pausing
	// every subsystem because one does not exist would be enforcing a policy
	// nobody chose — a fresh install with no WireGuard would simply not work,
	// and the reason would be a health gauge the operator has never looked at.
	// So the gate is open, and the admin surface says plainly that egress is
	// not being enforced rather than reporting a healthy tunnel that is not
	// there.
	//
	// Everything else still applies with Enforce false: blocked profiles are
	// still blocked, DenyPrivate still refuses private addresses, and socks5
	// still goes through the proxy. Only the tunnel-health gate is lifted.
	Enforce bool
	// Interface is the tunnel interface the operator expects, normally wg0.
	Interface string
	Observer  Observer
	Now       func() time.Time
	Timeout   time.Duration
}

// New builds a Guard. With enforcement on it starts UNHEALTHY: nothing dials
// until a probe has actually succeeded.
func New(cfg Config) *Guard {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	g := &Guard{
		healthy:   false,
		enforce:   cfg.Enforce,
		detail:    "no probe has run yet",
		since:     now(),
		profiles:  map[string]Profile{},
		tunnelled: map[string]bool{},
		iface:     cfg.Interface,
		observer:  cfg.Observer,
		now:       now,
		base:      &net.Dialer{Timeout: timeout},
	}
	for name, p := range cfg.Profiles {
		g.profiles[name] = p
		g.tunnelled[name] = true
	}
	for _, name := range cfg.Exempt {
		g.tunnelled[name] = false
	}
	if !cfg.Enforce {
		g.detail = "egress enforcement is off (anonymity_enabled is false); " +
			"no tunnel is expected and none is required"
	}
	return g
}

// Enforcing reports whether the kill switch is active.
func (g *Guard) Enforcing() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.enforce
}

// Healthy reports the current state and why.
func (g *Guard) Healthy() (bool, string, time.Time) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.healthy, g.detail, g.since
}

// SetHealthy records a probe result.
//
// A transition from healthy to unhealthy IS the kill switch: every in-flight
// transfer's next dial fails from that moment, without anything having to be
// told to stop.
func (g *Guard) SetHealthy(healthy bool, detail string) {
	g.mu.Lock()
	changed := g.healthy != healthy
	g.healthy = healthy
	g.detail = detail
	if changed {
		g.since = g.now()
	}
	obs := g.observer
	g.mu.Unlock()

	if !changed || obs == nil {
		return
	}
	obs.EgressHealthChanged(healthy, detail)
	if !healthy {
		obs.EgressKillSwitchEngaged(detail)
	}
}

// For returns the dialer for a named subsystem.
//
// An unknown name yields a dialer that refuses everything. That is the whole
// reason this returns a Dialer rather than (Dialer, error): a caller that
// ignores an error gets a working direct dialer, and a caller that ignores this
// gets one that cannot connect. Failure has to be the easy path.
func (g *Guard) For(subsystem string) Dialer {
	g.mu.RLock()
	profile, known := g.profiles[subsystem]
	needsTunnel := g.tunnelled[subsystem]
	g.mu.RUnlock()

	if !known {
		return refusingDialer{err: fmt.Errorf("%w: no profile named %q", ErrBlocked, subsystem)}
	}
	return &profileDialer{guard: g, name: subsystem, profile: profile, needsTunnel: needsTunnel}
}

// ProbeDialer returns a dialer that applies the profile's transport but skips
// the health gate.
//
// It exists because the gate would otherwise be a trap: once health drops,
// every dial fails, including the probe that would notice the tunnel came back,
// and the guard could never recover without a restart.
//
// The exemption is narrow and it is not a hole. The profile is applied in full —
// a socks5 probe really goes through the proxy, a blocked profile still refuses —
// so the probe tests the path payload traffic would take. What it skips is the
// conclusion drawn from the previous probe, which is the one thing it must not
// depend on. Only the health task may use this; TestOnlyTheProbeBypassesTheGate
// pins the number of callers.
func (g *Guard) ProbeDialer(subsystem string) Dialer {
	g.mu.RLock()
	profile, known := g.profiles[subsystem]
	g.mu.RUnlock()

	if !known {
		return refusingDialer{err: fmt.Errorf("%w: no profile named %q", ErrBlocked, subsystem)}
	}
	return &profileDialer{guard: g, name: subsystem, profile: profile, needsTunnel: false}
}

// Profiles returns a copy of the configured policy, for the admin surface.
func (g *Guard) Profiles() map[string]Profile {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make(map[string]Profile, len(g.profiles))
	for k, v := range g.profiles {
		// Credentials are deliberately not copied out. The admin surface
		// reports policy, never secrets.
		v.Password = ""
		out[k] = v
	}
	return out
}

// TunnelInterface is the interface the operator expects traffic to leave by.
func (g *Guard) TunnelInterface() string { return g.iface }

// Verify re-runs the routing check on demand, for the admin leak test. It is a
// method so callers reach it through the guard rather than importing the
// package function and passing whatever interface name they happen to hold.
func (g *Guard) Verify(wantInterface string) (Verification, error) {
	return Verify(wantInterface)
}

// DependsOnTunnel reports whether a profile pauses when the tunnel drops.
func (g *Guard) DependsOnTunnel(subsystem string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.enforce && g.tunnelled[subsystem]
}

// ---------------------------------------------------------------------------
// dialers
// ---------------------------------------------------------------------------

type refusingDialer struct{ err error }

func (r refusingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, r.err
}

type profileDialer struct {
	guard       *Guard
	name        string
	profile     Profile
	needsTunnel bool
	// allow is the one private destination this dialer may reach despite
	// DenyPrivate, as a DestinationKey. Empty for every dialer except the ones
	// HTTPClientAllowing builds for an indexer's own configured address
	// (ADR-0024).
	allow string
}

// DialContext applies policy, then connects.
//
// The order matters and is the security property: every refusal happens before
// any packet is sent, and every branch that refuses RETURNS. There is no path
// through this function that reaches a direct dial after a proxy failed.
func (d *profileDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if d.profile.Mode == ModeBlocked {
		return nil, fmt.Errorf("%w: profile %q is blocked", ErrBlocked, d.name)
	}

	if d.needsTunnel && d.guard.Enforcing() {
		if healthy, detail, _ := d.guard.Healthy(); !healthy {
			return nil, fmt.Errorf("%w (%s)", ErrEgressUnavailable, detail)
		}
	}

	switch d.profile.Mode {
	case ModeDirect:
		// "Direct" means direct INSIDE THE NAMESPACE. On a correctly deployed
		// downloader the only route out of here is the tunnel; this is not an
		// escape hatch, it is the normal case under ADR-0001.
		if d.profile.DenyPrivate {
			check := d.checkDestination
			if d.allow != "" && sameDestination(d.allow, address) {
				// The operator's own address for this indexer: its private
				// network is allowed, link-local and the rest still are not.
				check = d.checkOperatorDestination
			}
			if err := check(ctx, address); err != nil {
				return nil, err
			}
		}
		return d.guard.base.DialContext(ctx, network, address)

	case ModeSOCKS5:
		// Hostnames are handed to the proxy rather than resolved here: a local
		// lookup for a tracker hostname is itself a leak, and it happens
		// outside the tunnel. DenyPrivate cannot be applied without resolving,
		// so for socks5 the proxy is trusted to be outside our own network —
		// which it is, by definition.
		if !d.profile.RemoteDNS {
			// The config lint refuses this combination at boot. Refusing again
			// here means a programmatic caller cannot construct it either.
			return nil, fmt.Errorf(
				"egress: profile %q uses socks5 without remote_dns; "+
					"resolving locally would leak every lookup outside the tunnel", d.name)
		}
		return dialSOCKS5(ctx, d.guard.base, d.profile, network, address)

	case ModeHTTPProxy:
		return dialHTTPConnect(ctx, d.guard.base, d.profile, network, address)

	default:
		// An unrecognised mode is blocked, not direct.
		return nil, fmt.Errorf("%w: profile %q has unknown mode %q", ErrBlocked, d.name, d.profile.Mode)
	}
}

// checkDestination refuses addresses hostile input must not be able to reach.
//
// Resolution happens once here and the dial that follows resolves again, so
// this is not proof against a DNS rebind. It is not trying to be: the real
// containment for that is the namespace's firewall, which has no route to the
// host network at all. This catches the ordinary case — an indexer handing back
// http://127.0.0.1:8080/ or a metadata service redirected at 169.254.169.254 —
// where the cost of the check is a single lookup.
func (d *profileDialer) checkDestination(ctx context.Context, address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("egress: malformed address %q", address)
	}

	var resolver net.Resolver
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("egress: resolving %q: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("egress: %q resolved to nothing", host)
	}

	// ALL addresses must be acceptable, not merely one. A name that resolves to
	// both a public and a loopback address is a rebind attempt, and picking the
	// public one would be choosing not to notice.
	for _, ip := range ips {
		if IsRestricted(ip.IP) {
			return fmt.Errorf("%w: %s resolved to %s", ErrPrivateAddress, host, ip.IP)
		}
	}
	return nil
}

// checkOperatorDestination is checkDestination for the one address an operator
// configured themselves (ADR-0024): every address the name resolves to must be
// public or on the operator's own network. Link-local is refused here too —
// that is where cloud metadata endpoints live, and no indexer is legitimately
// there.
func (d *profileDialer) checkOperatorDestination(ctx context.Context, address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("egress: malformed address %q", address)
	}
	var resolver net.Resolver
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("egress: resolving %q: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("egress: %q resolved to nothing", host)
	}
	for _, ip := range ips {
		if IsRestricted(ip.IP) && !OnOperatorNetwork(ip.IP) {
			return fmt.Errorf("%w: %s resolved to %s, which is not an address an "+
				"indexer can be at even when you configure it", ErrPrivateAddress, host, ip.IP)
		}
	}
	return nil
}

// OnOperatorNetwork reports whether an address is on a network an operator
// plausibly runs their own services on: loopback, RFC 1918 and unique-local,
// and carrier-grade NAT — which is Tailscale's range.
//
// Deliberately NOT link-local (169.254.0.0/16, fe80::/10), where cloud metadata
// endpoints live, nor multicast, unspecified or reserved addresses. None of
// those is in the three ranges below, so the list is what is allowed rather
// than a list of exceptions to remember; TestWhatCountsAsTheOperatorsNetwork
// pins the ones that matter. (A first version also excluded them explicitly.
// A mutation test showed the exclusion could never change an answer.)
func OnOperatorNetwork(ip net.IP) bool {
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || cgnat.Contains(ip)
}

var cgnat = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("100.64.0.0/10")
	return n
}()

// DestinationKey is how an allowed destination is written: the host lower-cased
// without a trailing dot, joined to the port. Two spellings of the same address
// by case or trailing dot compare equal; two different addresses never do.
func DestinationKey(host, port string) string {
	return net.JoinHostPort(strings.TrimSuffix(strings.ToLower(host), "."), port)
}

// DestinationOf is the DestinationKey of a URL, with the port filled in from
// the scheme when the URL does not write one — which is what the HTTP transport
// dials, and so what a dialer is asked for.
func DestinationOf(u *url.URL) (string, error) {
	if u == nil || u.Hostname() == "" {
		return "", fmt.Errorf("egress: %v has no host", u)
	}
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", fmt.Errorf("egress: no default port for scheme %q", u.Scheme)
		}
	}
	return DestinationKey(u.Hostname(), port), nil
}

// sameDestination compares an allowed key with the address a dial was asked
// for, normalising the dial address the same way.
func sameDestination(allowed, address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	return DestinationKey(host, port) == allowed
}

// IsRestricted reports whether an address is one hostile input must not reach.
func IsRestricted(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	// Carrier-grade NAT, and the ranges reserved for documentation and
	// benchmarking, which have no business being a download destination.
	for _, cidr := range restrictedRanges {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

var restrictedRanges = func() []*net.IPNet {
	var out []*net.IPNet
	for _, s := range []string{
		"100.64.0.0/10",   // CGNAT (RFC 6598) — also Tailscale's range
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"240.0.0.0/4",     // reserved
		"fc00::/7",        // unique local
		"2001:db8::/32",   // documentation
	} {
		if _, n, err := net.ParseCIDR(s); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// ---------------------------------------------------------------------------
// Guarded HTTP
// ---------------------------------------------------------------------------

// HTTPClient builds an http.Client that can only reach the network through the
// guard.
//
// It lives here, and not in each subsystem, for a structural reason. A
// subsystem that builds its own &http.Transport{} and forgets DialContext gets
// a transport that works perfectly and silently bypasses every control in this
// package — no error, no log line, just traffic leaving by the wrong route.
// That is a one-line mistake in a struct literal, invisible in review.
//
// With the construction here, acquisition packages never name http.Transport at
// all, and TestNoPackageDialsDirectly can ban the identifier outright: there is
// no legitimate use of it left to carve an exception for.
//
// validateURL, if given, runs on every redirect target. A redirect is a URL the
// far end chose AFTER the request was made, so it has had no validation at all
// unless it is checked here.
func (g *Guard) HTTPClient(subsystem string, timeout time.Duration,
	validateURL func(*url.URL) error) *http.Client {
	return g.httpClient(g.For(subsystem), timeout, validateURL)
}

// HTTPClientAllowing is HTTPClient with ONE private destination allowed: the
// exact host and port given, as a DestinationKey.
//
// It exists for an indexer on the operator's own network (ADR-0024). The key
// must be the address the operator typed for that indexer — never anything a
// feed, a redirect or a download link supplied — and the only caller is
// internal/indexer, which TestOnlyTheIndexerClientMayAllowAPrivateDestination
// pins. Every other private destination stays refused, and even the allowed
// one may not resolve to link-local.
//
// The allowance is built into the dialer rather than passed in the request's
// context: a context value is copied by everything it passes through, and
// whether the transport dials with the request's context at all is an
// implementation detail of net/http, not a promise.
func (g *Guard) HTTPClientAllowing(subsystem string, timeout time.Duration,
	validateURL func(*url.URL) error, allowed string) *http.Client {

	dialer := g.For(subsystem)
	if pd, ok := dialer.(*profileDialer); ok && allowed != "" {
		pd.allow = allowed
	}
	return g.httpClient(dialer, timeout, validateURL)
}

func (g *Guard) httpClient(dialer Dialer, timeout time.Duration,
	validateURL func(*url.URL) error) *http.Client {

	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: timeout / 2,
			ExpectContinueTimeout: time.Second,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
			// TLS 1.2 floor. Compression is left on, because callers cap the
			// DECOMPRESSED stream and a bomb therefore buys nothing.
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("egress: too many redirects")
			}
			if validateURL != nil {
				return validateURL(req.URL)
			}
			return nil
		},
	}
}

package egress

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// healthyGuard builds an ENFORCING guard that has already passed a probe.
// Enforcement is explicit in every test below, because whether the kill switch
// applies at all is now a decision the operator makes (Config.Enforce) and a
// test that left it implicit would stop testing the gate the day the default
// changed.
func healthyGuard(t *testing.T, profiles map[string]Profile) *Guard {
	t.Helper()
	g := New(Config{Profiles: profiles, Enforce: true})
	g.SetHealthy(true, "test")
	return g
}

// ---------------------------------------------------------------------------
// The headline property: no fallback, ever
// ---------------------------------------------------------------------------

// §2: if the tunnel is unreachable, downloads pause — they never fall back to a
// direct connection.
//
// The test exhausts every mode rather than checking the one that seems most
// likely, because the failure being guarded against is a branch somebody adds
// later to one mode and not the others.
func TestNoFallbackWhenUnhealthy(t *testing.T) {
	// A listener that would accept anything, so a fallback would SUCCEED and be
	// visible as a returned connection rather than as an unrelated error.
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	reachable := ln.Addr().String()

	modes := map[string]Profile{
		"direct":     {Mode: ModeDirect},
		"socks5":     {Mode: ModeSOCKS5, Address: reachable, RemoteDNS: true},
		"http-proxy": {Mode: ModeHTTPProxy, Address: reachable},
		"blocked":    {Mode: ModeBlocked},
		"nonsense":   {Mode: Mode("something-new")},
	}

	g := New(Config{Profiles: modes, Enforce: true}) // starts unhealthy

	for name := range modes {
		t.Run(name, func(t *testing.T) {
			conn, err := g.For(name).DialContext(t.Context(), "tcp", reachable)
			if conn != nil {
				_ = conn.Close()
				t.Fatalf("FALLBACK: %s returned a live connection while the tunnel was down", name)
			}
			if err == nil {
				t.Fatalf("%s returned no connection and no error", name)
			}
			if !errors.Is(err, ErrEgressUnavailable) && !errors.Is(err, ErrBlocked) {
				t.Errorf("%s failed for the wrong reason: %v", name, err)
			}
		})
	}
}

func TestHealthStartsDown(t *testing.T) {
	g := New(Config{Profiles: map[string]Profile{"download": {Mode: ModeDirect}}, Enforce: true})
	healthy, detail, _ := g.Healthy()
	if healthy {
		t.Error("a new guard reported healthy before any probe ran")
	}
	if detail == "" {
		t.Error("no reason was given for the initial state")
	}
}

// A subsystem nobody configured must not get a working connection by default.
func TestUnknownProfileIsBlockedNotDirect(t *testing.T) {
	g := healthyGuard(t, map[string]Profile{"download": {Mode: ModeDirect}})

	conn, err := g.For("subtitle-fetcher-somebody-forgot").DialContext(
		t.Context(), "tcp", "203.0.113.9:80")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("an unconfigured subsystem got a connection")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Errorf("got %v, want ErrBlocked", err)
	}
}

type recordingObserver struct {
	mu       sync.Mutex
	changes  []bool
	switches int
}

func (r *recordingObserver) EgressHealthChanged(healthy bool, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, healthy)
}

func (r *recordingObserver) EgressKillSwitchEngaged(string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.switches++
}

func TestKillSwitchFiresOncePerTransition(t *testing.T) {
	obs := &recordingObserver{}
	g := New(Config{Profiles: map[string]Profile{"download": {Mode: ModeDirect}}, Observer: obs, Enforce: true})

	g.SetHealthy(true, "up")
	g.SetHealthy(true, "still up") // not a transition
	g.SetHealthy(false, "tunnel dropped")
	g.SetHealthy(false, "still down") // not a transition
	g.SetHealthy(true, "recovered")

	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.changes) != 3 {
		t.Errorf("observed %d transitions, want 3: %v", len(obs.changes), obs.changes)
	}
	if obs.switches != 1 {
		t.Errorf("the kill switch fired %d times, want 1", obs.switches)
	}
}

// The gate must close for traffic already in flight, without anything being
// told to stop.
func TestGateClosesUnderTraffic(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	g := healthyGuard(t, map[string]Profile{"download": {Mode: ModeDirect}})
	d := g.For("download")

	conn, err := d.DialContext(t.Context(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("a healthy guard refused: %v", err)
	}
	_ = conn.Close()

	g.SetHealthy(false, "tunnel dropped")

	// The SAME dialer, already handed out, must now refuse.
	conn, err = d.DialContext(t.Context(), "tcp", ln.Addr().String())
	if conn != nil {
		_ = conn.Close()
		t.Fatal("a dialer obtained before the drop still connected after it")
	}
	if !errors.Is(err, ErrEgressUnavailable) {
		t.Errorf("got %v, want ErrEgressUnavailable", err)
	}
}

// Exempt profiles keep working: metadata lookups do not reveal acquisition
// activity, and pausing them would break the UI for no privacy gain.
func TestExemptProfilesSurviveTheKillSwitch(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	g := New(Config{
		Profiles: map[string]Profile{
			"download": {Mode: ModeDirect},
			"metadata": {Mode: ModeDirect},
		},
		Exempt:  []string{"metadata"},
		Enforce: true,
	})
	// Deliberately never made healthy.

	if _, err := g.For("download").DialContext(t.Context(), "tcp", ln.Addr().String()); !errors.Is(err, ErrEgressUnavailable) {
		t.Errorf("download was not paused: %v", err)
	}
	conn, err := g.For("metadata").DialContext(t.Context(), "tcp", ln.Addr().String())
	if err != nil {
		t.Errorf("an exempt profile was paused: %v", err)
	} else {
		_ = conn.Close()
	}

	if !g.DependsOnTunnel("download") || g.DependsOnTunnel("metadata") {
		t.Error("DependsOnTunnel disagrees with the behaviour above")
	}
}

// ---------------------------------------------------------------------------
// SSRF
// ---------------------------------------------------------------------------

func TestRestrictedRangesAreRecognised(t *testing.T) {
	restricted := []string{
		"127.0.0.1", "10.1.2.3", "192.168.1.1", "172.16.0.1",
		"169.254.169.254", // cloud metadata, the one that matters
		"100.64.0.1",      // CGNAT
		"0.0.0.0", "224.0.0.1", "198.18.0.1", "203.0.113.5", "240.0.0.1",
		"::1", "fc00::1", "fe80::1",
	}
	for _, s := range restricted {
		if !IsRestricted(net.ParseIP(s)) {
			t.Errorf("%s was not treated as restricted", s)
		}
	}

	public := []string{"1.1.1.1", "8.8.8.8", "93.184.216.34", "2606:4700:4700::1111"}
	for _, s := range public {
		if IsRestricted(net.ParseIP(s)) {
			t.Errorf("%s was wrongly treated as restricted", s)
		}
	}

	if !IsRestricted(nil) {
		t.Error("a nil address must be restricted, not permitted")
	}
}

func TestDenyPrivateRefusesLoopback(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	g := healthyGuard(t, map[string]Profile{
		"indexer": {Mode: ModeDirect, DenyPrivate: true},
		"admin":   {Mode: ModeDirect},
	})

	conn, err := g.For("indexer").DialContext(t.Context(), "tcp", ln.Addr().String())
	if conn != nil {
		_ = conn.Close()
		t.Fatal("a DenyPrivate profile reached loopback")
	}
	if !errors.Is(err, ErrPrivateAddress) {
		t.Errorf("got %v, want ErrPrivateAddress", err)
	}

	// Without the flag it is permitted: the control is opt-in per profile, and
	// this asserts the test above is measuring the flag and not something else.
	if conn, err := g.For("admin").DialContext(t.Context(), "tcp", ln.Addr().String()); err != nil {
		t.Errorf("a profile without DenyPrivate was blocked anyway: %v", err)
	} else {
		_ = conn.Close()
	}
}

// ---------------------------------------------------------------------------
// The probe exemption
// ---------------------------------------------------------------------------

func TestProbeDialerBypassesTheGateButNotThePolicy(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	g := New(Config{Profiles: map[string]Profile{
		"download": {Mode: ModeDirect},
		"update":   {Mode: ModeBlocked},
	}, Enforce: true})
	// Unhealthy throughout.

	conn, err := g.ProbeDialer("download").DialContext(t.Context(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("the probe dialer was blocked by the gate it exists to reopen: %v", err)
	}
	_ = conn.Close()

	// It is an exemption from the health gate only. Policy still applies.
	if _, err := g.ProbeDialer("update").DialContext(t.Context(), "tcp", ln.Addr().String()); !errors.Is(err, ErrBlocked) {
		t.Errorf("the probe dialer ignored a blocked profile: %v", err)
	}
	if _, err := g.ProbeDialer("nonexistent").DialContext(t.Context(), "tcp", ln.Addr().String()); !errors.Is(err, ErrBlocked) {
		t.Errorf("the probe dialer served an unknown profile: %v", err)
	}
}

// The gate exemption is narrow by construction; this pins how narrow. If a
// second caller appears, that is a decision somebody should have to make on
// purpose rather than by autocomplete.
func TestOnlyTheProbeBypassesTheGate(t *testing.T) {
	root := ".."
	var callers []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, "ProbeDialer(") && !strings.Contains(line, "func (g *Guard)") {
				callers = append(callers, path+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(callers) > 1 {
		t.Errorf("ProbeDialer has %d callers, want at most the health probe:\n  %s",
			len(callers), strings.Join(callers, "\n  "))
	}
}

// ---------------------------------------------------------------------------
// Secrets
// ---------------------------------------------------------------------------

func TestProfilesNeverExposeTheProxyPassword(t *testing.T) {
	g := healthyGuard(t, map[string]Profile{
		"download": {Mode: ModeSOCKS5, Address: "proxy:1080",
			Username: "svc", Password: "hunter2-the-real-one", RemoteDNS: true},
	})

	for name, p := range g.Profiles() {
		if p.Password != "" {
			t.Errorf("profile %q handed out its password", name)
		}
		if p.Username == "" {
			t.Errorf("profile %q lost its username, which is not a secret", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Verification
// ---------------------------------------------------------------------------

// The check must never answer "jailed" when it cannot tell. This asks for an
// interface that certainly does not exist, which is the shape of a
// misconfiguration or a dropped tunnel.
func TestVerifyRefusesWhenTheInterfaceIsWrong(t *testing.T) {
	v, err := Verify("wg-definitely-not-a-real-interface")
	if err == nil {
		t.Fatal("verification passed against an interface that does not exist")
	}
	if !errors.Is(err, ErrNotJailed) {
		t.Errorf("got %v, want ErrNotJailed", err)
	}
	if v.Jailed {
		t.Error("Jailed was true on a failed verification")
	}
	if v.Detail == "" {
		t.Error("a failure gave no explanation for the operator")
	}
}

// The probe must report unhealthy for anything it cannot confirm. Reporting
// healthy on an inconclusive result would open the gate on a guess.
func TestProbeReportsUnhealthyWhenRoutingCannotBeConfirmed(t *testing.T) {
	p := Prober{Interface: "wg-definitely-not-a-real-interface"}
	healthy, detail := p.Probe(t.Context())
	if healthy {
		t.Error("the probe reported healthy without confirming the route")
	}
	if detail == "" {
		t.Error("no reason was recorded")
	}
}

func TestProbeReportsUnhealthyWithoutADialer(t *testing.T) {
	// Interface empty means routing verification alone, which may well pass in
	// a test container; the point is the Target branch with no dialer wired.
	p := Prober{Target: "example.invalid:443"}
	if healthy, detail := p.Probe(t.Context()); healthy {
		t.Errorf("reported healthy with no dialer: %s", detail)
	}
}

// ---------------------------------------------------------------------------
// The structural guarantee
// ---------------------------------------------------------------------------

// A runtime check cannot prove a code path does not exist. This reads the
// source of every package that will handle hostile input and fails on anything
// that can reach the network without going through a Dialer.
//
// The egress package itself is exempt: it is where the real dialing lives.
func TestNoPackageDialsDirectly(t *testing.T) {
	// Packages that are, or will be, on the acquisition path. Listed by name so
	// that adding one is a deliberate act.
	guarded := []string{
		"../indexer", "../download", "../metadata", "../subtitle",
		// Artwork fetches from a provider's image CDN, which discloses exactly
		// which titles this instance holds — the same leak as the metadata API
		// calls, over a different host. An instance that tunnels its indexer
		// and metadata traffic and then announces its whole library from the
		// operator's home IP over the image CDN has not been protected.
		"../artwork",
		// Notifications carry the audit log's lines to Discord (ADR-0032).
		"../notify",
	}
	// Named before they are built, so that they are guarded from their first
	// line. Any other missing directory is a misspelling, which would
	// otherwise pass by guarding nothing.
	notYetBuilt := map[string]bool{"../subtitle": true}

	banned := map[string]string{
		"net.Dial":              "use the Dialer from internal/egress",
		"http.Get":              "builds its own transport, bypassing the guard",
		"http.Post":             "builds its own transport, bypassing the guard",
		"http.Head":             "builds its own transport, bypassing the guard",
		"http.DefaultClient":    "bypasses the guard",
		"http.DefaultTransport": "bypasses the guard",
		// http.Transport is banned outright, with no exception to carve out,
		// because the dangerous version of it is the one that looks fine: a
		// literal that simply omits DialContext works perfectly and silently
		// leaves by the wrong route. Guard.HTTPClient is the only place a
		// transport is built, so there is no legitimate use left here.
		"http.Transport": "build the client with egress.Guard.HTTPClient instead",
		"net.Dialer":     "the guard owns the dialer",
	}

	for _, dir := range guarded {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !notYetBuilt[dir] {
				t.Errorf("%s is guarded but cannot be read: %v", dir, err)
			}
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			src := stripGoComments(string(body))
			for needle, why := range banned {
				if strings.Contains(src, needle) {
					t.Errorf("%s uses %s: %s", path, needle, why)
				}
			}
		}
	}
}

// stripGoComments removes block comments and whole-line // comments, so the
// scan above reads code rather than prose — the comments in these packages name
// the very calls they warn against. Trailing comments are deliberately left
// alone: deciding whether a // sits inside a string literal needs a parser, and
// getting it wrong would delete real code from the scan. A leftover comment can
// only cause a false failure, never a false pass.
func stripGoComments(src string) string {
	var out []string
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if inBlock {
			if idx := strings.Index(line, "*/"); idx >= 0 {
				inBlock = false
				out = append(out, line[idx+2:])
			}
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.HasPrefix(trimmed, "/*") && !strings.Contains(line, "*/") {
			inBlock = true
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestGuardIsSafeUnderConcurrentUse(t *testing.T) {
	g := healthyGuard(t, map[string]Profile{"download": {Mode: ModeBlocked}})

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 4 {
			case 0:
				g.SetHealthy(i%8 == 0, "flap")
			case 1:
				_, _, _ = g.Healthy()
			case 2:
				_ = g.Profiles()
			default:
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_, _ = g.For("download").DialContext(ctx, "tcp", "203.0.113.1:80")
			}
		}(i)
	}
	wg.Wait()
}

// A closed connection must never be handed back as usable.
var _ io.Closer = (net.Conn)(nil)

// ---------------------------------------------------------------------------
// Enforcement is the operator's choice
// ---------------------------------------------------------------------------

// With anonymity_enabled false the operator has not asked for a tunnel.
// Pausing every subsystem because one does not exist would enforce a policy
// nobody chose: a fresh install with no WireGuard would simply not work, and
// the reason would be a health gauge the operator has never looked at.
func TestWithEnforcementOffTheGateIsOpen(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	g := New(Config{
		Profiles: map[string]Profile{"download": {Mode: ModeDirect}},
		Enforce:  false,
	})
	// Never made healthy, and never will be: there is no tunnel to probe.

	conn, err := g.For("download").DialContext(t.Context(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("a non-enforcing guard paused a download: %v", err)
	}
	_ = conn.Close()

	if g.Enforcing() {
		t.Error("Enforcing() disagrees with the configuration")
	}
	if g.DependsOnTunnel("download") {
		t.Error("a profile reports depending on a tunnel that is not being enforced")
	}
	// And it says so, rather than reporting a healthy tunnel that is not there.
	if _, detail, _ := g.Healthy(); !strings.Contains(detail, "enforcement is off") {
		t.Errorf("the status does not explain that enforcement is off: %q", detail)
	}
}

// Turning enforcement off lifts the TUNNEL gate and nothing else. A blocked
// profile is still blocked and DenyPrivate still refuses.
func TestEnforcementOffDoesNotLiftEveryOtherControl(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	g := New(Config{
		Profiles: map[string]Profile{
			"update":  {Mode: ModeBlocked},
			"indexer": {Mode: ModeDirect, DenyPrivate: true},
		},
		Enforce: false,
	})

	if _, err := g.For("update").DialContext(t.Context(), "tcp", ln.Addr().String()); !errors.Is(err, ErrBlocked) {
		t.Errorf("a blocked profile dialled with enforcement off: %v", err)
	}
	if _, err := g.For("indexer").DialContext(t.Context(), "tcp", ln.Addr().String()); !errors.Is(err, ErrPrivateAddress) {
		t.Errorf("DenyPrivate was lifted along with enforcement: %v", err)
	}
	if _, err := g.For("nobody-configured-this").DialContext(t.Context(), "tcp", ln.Addr().String()); !errors.Is(err, ErrBlocked) {
		t.Errorf("an unconfigured profile dialled with enforcement off: %v", err)
	}
}

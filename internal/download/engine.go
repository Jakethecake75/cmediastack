// Package download runs the BitTorrent engine.
//
// # What this package is really for
//
// Wrapping anacrolix/torrent is the easy part. The job is making §2's promise
// true for a protocol that is actively hostile to it: BitTorrent opens
// connections to addresses supplied by strangers, over TCP and UDP, from
// several subsystems at once (peers, trackers, DHT, PEX, local discovery), and
// most of those paths do not go anywhere near an http.Client.
//
// # What can and cannot be constrained, honestly
//
// anacrolix/torrent can be constrained for **outbound peer connections**, and
// the mechanism is exact rather than approximate:
//
//   - ClientConfig.DialForPeerConns = false stops the library adding its own
//     listening sockets to the dialer set.
//   - Client.AddDialer then installs ours as the ONLY entry in cl.dialers.
//
// After that, every outbound peer connection goes through the egress guard.
//
// The library exports no way to count its dialers, so that is asserted
// BEHAVIOURALLY rather than structurally, which is the better test anyway:
// TestAClosedGateStopsPeerConnections runs a seeder and a leecher in the same
// process, on loopback, with the gate shut — and the transfer does not happen.
// "We added a guarded dialer" and "ours is the only path out" are different
// claims, and only the second one is the guarantee.
//
// What CANNOT be constrained that way:
//
//   - **DHT** binds its own UDP socket and speaks directly. A TCP dialer cannot
//     carry it.
//   - **uTP** is UDP for the same reason.
//   - **Incoming connections** arrive at a listening socket; there is no dial
//     to intercept.
//
// So the configuration is DERIVED FROM the egress mode rather than chosen
// beside it — see ConfigFor. Under a SOCKS5 profile, DHT and uTP are forced
// off and incoming connections are refused, because leaving them on would mean
// UDP leaving outside the tunnel while the operator believed otherwise. Under
// the namespace guard (ADR-0001) they stay on, because the kernel is carrying
// the guarantee and the namespace has no route that bypasses the tunnel.
//
// That coupling is the point. An operator cannot accidentally run DHT through a
// SOCKS5 setup, because the engine will not let the two be configured
// independently.
package download

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/storage"

	"github.com/jakethecake75/cmediastack/internal/egress"
)

// ProfileName is the egress profile download traffic uses.
const ProfileName = "download"

// Errors callers distinguish.
var (
	ErrEngineClosed  = errors.New("download: the engine is closed")
	ErrNotFound      = errors.New("download: no such transfer")
	ErrUnsafePeer    = errors.New("download: refused a peer address")
	ErrNoLeakFreeUTP = errors.New(
		"download: uTP and DHT cannot be carried by an application-level proxy; " +
			"they are disabled under a socks5 profile and available under the namespace guard")
)

// Config is what the engine needs. Most of it is derived rather than chosen —
// see ConfigFor.
type Config struct {
	// DataDir is where incomplete and complete files live.
	DataDir string
	// ListenPort is the peer port. Zero picks one.
	//
	// With NordVPN there is no port forwarding, so nothing reaches this from
	// outside and the instance is a passive peer whatever is set here
	// (ADR-0001, accepted residual). It is configurable anyway because another
	// provider may forward, and because a fixed port is easier to firewall.
	ListenPort int
	// EnableDHT and EnableUTP are DERIVED. ConfigFor sets them; setting them by
	// hand is how the guarantee gets broken quietly.
	EnableDHT bool
	EnableUTP bool
	// AcceptIncoming allows inbound peer connections.
	AcceptIncoming bool
	// MaxActive bounds concurrent transfers.
	MaxActive int
	// Seed keeps torrents seeding after completion.
	Seed bool
}

// ConfigFor derives an engine configuration from the egress policy.
//
// This exists so the two cannot disagree. The failure it prevents is not
// hypothetical and not loud: an operator configures a SOCKS5 proxy, believes
// their traffic is tunnelled, and DHT quietly announces their IP to the swarm
// over UDP for months. Nothing errors. Nothing logs. The only symptom is a
// letter.
// jailed is whether a NETWORK NAMESPACE carries the guarantee — the ADR-0001
// deployment, where every packet leaves through the tunnel whatever the
// application does. It is a separate fact from enforcing, which is only about
// application-level proxying, and conflating the two produced a warning that
// was flatly wrong in the RECOMMENDED configuration: a downloader that had just
// verified its namespace logged "nothing is tunnelled" on the next line.
//
// That is the inverse of the failure this function exists to prevent, and it is
// worse in one way: an alarm that fires when everything is correct is one an
// operator learns to ignore, which is how the real alarm gets missed.
func ConfigFor(dataDir string, listenPort int, profile egress.Profile,
	enforcing, jailed bool) (Config, []string) {
	cfg := Config{
		DataDir:    dataDir,
		ListenPort: listenPort,
		MaxActive:  5,
		Seed:       true,
	}
	var notes []string

	switch {
	// A blocked profile sends nothing. DHT and uTP have sockets of their own,
	// outside the guard, so they go too (ADR-0065: the fallback for a proxy
	// that is not in force is blocked, and must not leak UDP).
	case profile.Mode == egress.ModeBlocked:
		cfg.EnableDHT, cfg.EnableUTP, cfg.AcceptIncoming = false, false, false
		notes = append(notes, "the download profile is blocked: nothing is fetched, and DHT, uTP and "+
			"incoming connections are off")

	// Then, whatever else is true: a proxy carries TCP only, so UDP is off
	// under one with enforcement on or off (ADR-0065 found it off, the LXC
	// deployment's default, with DHT and uTP still on beside the proxy).
	case profile.Mode == egress.ModeSOCKS5 || profile.Mode == egress.ModeHTTPProxy:
		// An application-level proxy carries TCP and nothing else. Leaving DHT
		// or uTP on here would put UDP outside the tunnel while the operator
		// believed otherwise.
		cfg.EnableDHT, cfg.EnableUTP, cfg.AcceptIncoming = false, false, false
		notes = append(notes,
			"DHT is disabled: it is UDP, and a socks5 proxy cannot carry it without leaking",
			"uTP is disabled for the same reason; transfers are TCP-only",
			"incoming connections are refused: there is no dial to route through the proxy",
			"this is the degraded mode. The namespace guard (ADR-0001) keeps DHT and uTP")

	case !enforcing && jailed:
		// The namespace is doing the work. Application-level proxying is off
		// and does not need to be on: everything already leaves through the
		// tunnel, including the UDP a proxy could not have carried.
		cfg.EnableDHT, cfg.EnableUTP, cfg.AcceptIncoming = true, true, true
		notes = append(notes, "DHT and uTP are enabled: the network namespace "+
			"carries the egress guarantee (ADR-0001), so UDP is safe inside it. "+
			"Application-level proxying is off and is not needed here")

	case !enforcing:
		// Nothing is carrying the guarantee. This is the message that matters,
		// and it now fires only when it is true.
		cfg.EnableDHT, cfg.EnableUTP, cfg.AcceptIncoming = true, true, true
		notes = append(notes, "egress enforcement is off and no network namespace "+
			"was verified: DHT, uTP and incoming connections are enabled and "+
			"NOTHING IS TUNNELLED")

	default:
		// Direct inside the namespace: the kernel carries the guarantee, and
		// the namespace has no route that bypasses the tunnel.
		cfg.EnableDHT, cfg.EnableUTP = true, true
		cfg.AcceptIncoming = true
		notes = append(notes, "DHT and uTP are enabled: the network namespace "+
			"carries the egress guarantee, so UDP is safe inside it")
	}
	return cfg, notes
}

// Engine owns the torrent client.
type Engine struct {
	mu     sync.RWMutex
	client *torrent.Client
	closed bool

	cfg    Config
	guard  *egress.Guard
	notes  []string
	rateMu sync.Mutex
	rates  map[string]rateState

	// udpTrackers is false when the profile cannot carry UDP; proxiedUDP is
	// true when it is carried by the proxy, whose names aliases stands in for.
	udpTrackers bool
	proxiedUDP  bool
	aliases     *egress.Aliases
	// httpForms asks each udp:// tracker over HTTP at the same address when
	// the proxy refuses UDP (NordVPN's does).
	httpForms bool
	conns     *connCounter

	// addedAt records when THIS PROCESS added each transfer. The torrent
	// library does not track it and has no reason to, but a Transfer with a
	// zero time in the field is worse than no field at all: it renders as the
	// year 1 and reads as data. Note this is not the same fact as the queue
	// row's added_at, which is when the release was first grabbed and survives
	// restarts — the store is the authority on that one.
	addedAt map[string]time.Time
	now     func() time.Time
}

// noteAdded records an add time, and is the only writer of the map.
func (e *Engine) noteAdded(hash string) {
	if e.addedAt == nil {
		e.addedAt = make(map[string]time.Time)
	}
	if _, seen := e.addedAt[hash]; !seen {
		e.addedAt[hash] = e.now().UTC()
	}
}

// guardedDialer adapts the egress guard to anacrolix/torrent's Dialer.
//
// DialerNetwork reports "tcp", which is what the library uses to decide whether
// this dialer applies to a given peer address.
type guardedDialer struct {
	dial  egress.Dialer
	count *connCounter
}

func (g guardedDialer) DialerNetwork() string { return "tcp" }

func (g guardedDialer) Dial(ctx context.Context, addr string) (net.Conn, error) {
	g.count.attempted.Add(1)
	c, err := g.dial.DialContext(ctx, "tcp", addr)
	if err != nil {
		g.count.fail(err)
	}
	return c, err
}

// ConnStats is the engine's outgoing peer connections since it started
// (ADR-0066): the torrent library drops a failed one silently, so this is
// where "why does nothing connect" is answered.
type ConnStats struct {
	Attempted   int64     `json:"attempted"`
	Failed      int64     `json:"failed"`
	LastError   string    `json:"last_error,omitempty"`
	LastErrorAt time.Time `json:"last_error_at,omitzero"`
}

type connCounter struct {
	attempted, failed atomic.Int64
	mu                sync.Mutex
	lastErr           string
	lastAt            time.Time
}

func (c *connCounter) fail(err error) {
	c.failed.Add(1)
	c.mu.Lock()
	c.lastErr, c.lastAt = err.Error(), time.Now().UTC()
	c.mu.Unlock()
}

// Connections reports the outgoing peer connections attempted and failed.
func (e *Engine) Connections() ConnStats {
	e.conns.mu.Lock()
	defer e.conns.mu.Unlock()
	return ConnStats{Attempted: e.conns.attempted.Load(), Failed: e.conns.failed.Load(),
		LastError: e.conns.lastErr, LastErrorAt: e.conns.lastAt}
}

// New starts the engine.
//
// It refuses to start rather than starting unprotected. A download engine that
// comes up outside its tunnel has already broken §2, and it breaks it silently:
// the first evidence would be an infringement notice.
func New(cfg Config, guard *egress.Guard, notes []string) (*Engine, error) {
	if guard == nil {
		return nil, errors.New("download: refusing to start without an egress guard")
	}
	if cfg.DataDir == "" {
		return nil, errors.New("download: a data directory is required")
	}

	tc := torrent.NewDefaultClientConfig()
	tc.DataDir = cfg.DataDir

	// A host with no IPv6 stack is not an error, it is a Tuesday: minimal
	// containers, some VPS images, and a network namespace built without an
	// IPv6 address all look like this. The library treats a failed listen as
	// fatal, so without this the engine refuses to start on a perfectly good
	// machine — which collides directly with "should work on almost anything".
	//
	// Probing beats assuming: the answer depends on the namespace the process
	// is in, which is exactly what ADR-0001 moves around.
	if !ipv6Available() {
		tc.DisableIPv6 = true
	}
	tc.ListenPort = cfg.ListenPort
	tc.Seed = cfg.Seed
	tc.NoDHT = !cfg.EnableDHT
	tc.DisableUTP = !cfg.EnableUTP
	tc.AcceptPeerConnections = cfg.AcceptIncoming

	// THE line that matters. With this false, the library does not add its own
	// listening sockets to the dialer set, so the dialer added below is the
	// only one — every outbound peer connection goes through the guard.
	tc.DialForPeerConns = false

	// Tracker and metainfo HTTP also goes through the guard. This is a separate
	// path from peer connections and would otherwise use the default transport.
	tc.HTTPDialContext = guard.For(ProfileName).DialContext
	// Announces have a dialer of their own in the library: without this,
	// tracker traffic left by the host's own route whatever the profile said
	// (found live, ADR-0065).
	tc.TrackerDialContext = guard.For(ProfileName).DialContext

	// UDP trackers (ADR-0067). Direct: the library's own sockets. Under a
	// socks5 profile, through the proxy's UDP association when it agrees to
	// one — asked once, here — and otherwise dropped from each torrent as it
	// is added. A socket that cannot be opened later fails its sends rather
	// than being refused: the library panics on a refused socket.
	mode := guard.Profiles()[ProfileName].Mode
	aliases := egress.NewAliases()
	udpTrackers, proxiedUDP, httpForms := mode == egress.ModeDirect, false, false
	if mode == egress.ModeSOCKS5 {
		if pc, err := guard.ListenPacket(ProfileName, aliases); err != nil {
			notes = append(notes, "UDP trackers cannot be used: the proxy refused UDP ("+err.Error()+"). Each "+
				"is asked over HTTP at the same address instead, which the big open trackers answer")
			httpForms = true
		} else {
			_ = pc.Close()
			udpTrackers, proxiedUDP = true, true
			notes = append(notes, "UDP trackers go through the proxy, each tracker's name resolved by the proxy")
			tc.TrackerListenPacket = func(string, string) (net.PacketConn, error) {
				pc, err := guard.ListenPacket(ProfileName, aliases)
				if err != nil {
					// Not returned: the library panics on a refused socket. The
					// socket fails every send instead, which it handles.
					return deadPacketConn{err: err, done: make(chan struct{})}, nil //nolint:nilerr // see above
				}
				return pc, nil
			}
		}
	}

	// Announcing the client and version to every tracker and peer is a
	// fingerprint the operator gains nothing from.
	tc.HTTPUserAgent = "CMediaStack"
	tc.Bep20 = "-CM0001-"
	tc.ExtendedHandshakeClientVersion = "CMediaStack"

	tc.DefaultStorage = storage.NewFileByInfoHash(cfg.DataDir)

	client, err := torrent.NewClient(tc)
	if err != nil {
		return nil, fmt.Errorf("download: starting the torrent client: %w", err)
	}

	// Installed AFTER construction, which is the only place AddDialer exists.
	// Because DialForPeerConns is false, cl.dialers was empty until now.
	conns := &connCounter{}
	client.AddDialer(guardedDialer{dial: guard.For(ProfileName), count: conns})

	return &Engine{
		udpTrackers: udpTrackers,
		proxiedUDP:  proxiedUDP,
		httpForms:   httpForms,
		aliases:     aliases,
		conns:       conns,
		client:      client, cfg: cfg, guard: guard, notes: notes,
		addedAt: make(map[string]time.Time), now: time.Now,
	}, nil
}

// Notes returns the human-readable consequences of the derived configuration.
// The caller logs these at startup: an operator running in degraded mode should
// find out from a log line, not from wondering why nothing has peers.
func (e *Engine) Notes() []string {
	out := make([]string, len(e.notes))
	copy(out, e.notes)
	return out
}

// Close stops the engine.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	errs := e.client.Close()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// Transfer is one download's observable state.
type Transfer struct {
	InfoHash  string
	Name      string
	Bytes     int64
	Completed int64
	Peers     int
	Seeders   int
	Done      bool
	// Uploaded is payload bytes sent to peers in this process's lifetime. It
	// resets on restart, which is why seeding obligations are tracked against
	// the persisted row rather than against this number alone.
	Uploaded    int64
	AddedAt     time.Time
	MetadataGot bool
	// How it is doing (ADR-0068): peers connected, being connected to and
	// known but not yet tried; payload bytes received in this process's
	// lifetime; and bytes a second between the last two looks.
	Connected, Connecting, Waiting int
	Received, Rate                 int64
}

// sample is a transfer's received bytes at one look.
type sample struct {
	at    time.Time
	bytes int64
}

// rateBetween is bytes a second between two looks: 0 when the count went down
// (a restart) — and, with no earlier look, effectively 0, the zero time being
// centuries ago — and -1 when the looks are under a second apart, meaning
// "keep the rate you had".
func rateBetween(prev, now sample) int64 {
	dt := now.at.Sub(prev.at)
	if dt < time.Second {
		return -1
	}
	if now.bytes < prev.bytes {
		return 0
	}
	return int64(float64(now.bytes-prev.bytes) / dt.Seconds())
}

// Ratio is uploaded over downloaded, the number a tracker cares about. A
// torrent whose size is not yet known reports 0 rather than dividing by zero.
func (t Transfer) Ratio() float64 {
	if t.Bytes <= 0 {
		return 0
	}
	return float64(t.Uploaded) / float64(t.Bytes)
}

// Percent is completion, 0-100. A torrent whose metadata has not arrived yet
// reports 0 rather than dividing by zero.
func (t Transfer) Percent() float64 {
	if t.Bytes <= 0 {
		return 0
	}
	return float64(t.Completed) / float64(t.Bytes) * 100
}

// Add begins a transfer from a magnet link or an info hash.
//
// The URL is not fetched here: a .torrent file behind an HTTP URL is fetched
// through the guarded HTTP client by the caller, so that one code path handles
// every outbound request and the SSRF checks apply to it.
func (e *Engine) Add(ctx context.Context, magnetOrHash string) (Transfer, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return Transfer{}, ErrEngineClosed
	}

	spec, err := torrent.TorrentSpecFromMagnetUri(magnetOrHash)
	if err != nil {
		return Transfer{}, fmt.Errorf("download: adding magnet: %w", err)
	}
	t, _, err := e.client.AddTorrentSpec(e.withoutUDPTrackers(spec))
	if err != nil {
		return Transfer{}, fmt.Errorf("download: adding magnet: %w", err)
	}
	e.noteAdded(t.InfoHash().HexString())
	return e.transferOf(t), nil
}

// withoutUDPTrackers drops udp:// trackers when the profile cannot carry UDP,
// and when the proxy carries them, gives each tracker name its placeholder so
// the library sends without a lookup and the proxy resolves it (ADR-0067).
func (e *Engine) withoutUDPTrackers(spec *torrent.TorrentSpec) *torrent.TorrentSpec {
	if e.udpTrackers && !e.proxiedUDP {
		return spec
	}
	var tiers [][]string
	for _, tier := range spec.Trackers {
		var kept []string
		for _, u := range tier {
			if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(u)), "udp:") {
				kept = append(kept, u)
			} else if e.proxiedUDP {
				if aliased, ok := e.aliasTracker(u); ok {
					kept = append(kept, aliased)
				}
			} else if e.httpForms {
				if h, ok := httpForm(u); ok && !slices.Contains(kept, h) {
					kept = append(kept, h)
				}
			}
		}
		if len(kept) > 0 {
			tiers = append(tiers, kept)
		}
	}
	spec.Trackers = tiers
	return spec
}

// httpForm is a udp:// tracker's address asked over HTTP: same host, same
// port, the conventional /announce path.
func httpForm(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.Port() == "" {
		return "", false
	}
	return "http://" + u.Host + "/announce", true
}

// aliasTracker replaces a udp:// tracker's host name with its placeholder. An
// address that is already an IP literal is kept: it needs no lookup.
func (e *Engine) aliasTracker(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.Port() == "" {
		return "", false
	}
	if _, err := netip.ParseAddr(u.Hostname()); err == nil {
		return u.String(), true
	}
	u.Host = net.JoinHostPort(e.aliases.Alias(strings.ToLower(u.Hostname())).String(), u.Port())
	return u.String(), true
}

// deadPacketConn stands in for a UDP association that could not be opened:
// every send fails and reads wait for Close. The library panics on a refused
// socket, and a failed send is a failed announce, which it handles.
type deadPacketConn struct {
	err  error
	done chan struct{}
}

func (d deadPacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	<-d.done
	return 0, nil, net.ErrClosed
}
func (d deadPacketConn) WriteTo([]byte, net.Addr) (int, error) { return 0, d.err }
func (d deadPacketConn) Close() error {
	defer func() { _ = recover() }() // a second Close
	close(d.done)
	return nil
}
func (d deadPacketConn) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (d deadPacketConn) SetDeadline(time.Time) error      { return nil }
func (d deadPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (d deadPacketConn) SetWriteDeadline(time.Time) error { return nil }

// AddTorrentBytes begins a transfer from a .torrent file's contents.
func (e *Engine) AddTorrentBytes(data []byte) (Transfer, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return Transfer{}, ErrEngineClosed
	}

	mi, err := loadMetaInfo(data)
	if err != nil {
		return Transfer{}, err
	}
	spec, err := torrent.TorrentSpecFromMetaInfoErr(mi)
	if err != nil {
		return Transfer{}, fmt.Errorf("download: adding torrent: %w", err)
	}
	t, _, err := e.client.AddTorrentSpec(e.withoutUDPTrackers(spec))
	if err != nil {
		return Transfer{}, fmt.Errorf("download: adding torrent: %w", err)
	}
	e.noteAdded(t.InfoHash().HexString())
	return e.transferOf(t), nil
}

// Start begins downloading a transfer, now or, for a magnet whose metadata
// has not arrived, the moment it does. Until then the library wants no
// pieces, and once the metadata is in it stops dialling peers altogether: a
// magnet left at 0% with every peer waiting (found live, v0.2.0).
func (e *Engine) Start(hash string) error {
	t, err := e.lookup(hash)
	if err != nil {
		return err
	}
	go func() {
		select {
		case <-t.GotInfo():
			t.DownloadAll()
		case <-t.Closed():
		}
	}()
	return nil
}

// List returns every transfer.
func (e *Engine) List() []Transfer {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed || e.client == nil {
		return nil
	}

	torrents := e.client.Torrents()
	out := make([]Transfer, 0, len(torrents))
	for _, t := range torrents {
		out = append(out, e.transferOf(t))
	}
	return out
}

// Remove stops a transfer. The files are left on disk: deleting bytes is an
// effect with its own permission (EffectDestroyMediaBytes), and it does not
// belong to whoever can manage a queue.
func (e *Engine) Remove(hash string) error {
	t, err := e.lookup(hash)
	if err != nil {
		return err
	}
	e.mu.Lock()
	delete(e.addedAt, hash)
	e.mu.Unlock()
	t.Drop()
	return nil
}

func (e *Engine) lookup(hash string) (*torrent.Torrent, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return nil, ErrEngineClosed
	}
	for _, t := range e.client.Torrents() {
		if t.InfoHash().HexString() == hash {
			return t, nil
		}
	}
	return nil, ErrNotFound
}

// transferOf snapshots one torrent's state.
//
// The caller must already hold at least a read lock: it reads e.addedAt.
func (e *Engine) transferOf(t *torrent.Torrent) Transfer {
	tr := Transfer{
		InfoHash: t.InfoHash().HexString(),
		Name:     t.Name(),
	}
	tr.AddedAt = e.addedAt[tr.InfoHash]
	stats := t.Stats()
	tr.Peers = stats.TotalPeers
	tr.Seeders = stats.ConnectedSeeders
	// BytesWrittenData is payload actually sent to peers, not wire bytes:
	// handshakes and encryption overhead are not something a tracker credits,
	// so counting them would overstate the ratio and stop seeding early — on a
	// private tracker, that is how an account gets banned.
	tr.Uploaded = stats.BytesWrittenData.Int64()
	tr.Connected, tr.Connecting, tr.Waiting = stats.ActivePeers, stats.HalfOpenPeers, stats.PendingPeers
	tr.Received = stats.BytesReadUsefulData.Int64()
	tr.Rate = e.rate(tr.InfoHash, tr.Received)

	select {
	case <-t.GotInfo():
		tr.MetadataGot = true
		tr.Bytes = t.Length()
		tr.Completed = t.BytesCompleted()
		tr.Done = tr.Bytes > 0 && tr.Completed >= tr.Bytes
	default:
	}
	return tr
}

// rate keeps one sample per transfer and returns its rate since the last.
func (e *Engine) rate(hash string, received int64) int64 {
	e.rateMu.Lock()
	defer e.rateMu.Unlock()
	if e.rates == nil {
		e.rates = map[string]rateState{}
	}
	st := e.rates[hash]
	now := sample{e.now(), received}
	switch r := rateBetween(st.last, now); {
	case r >= 0:
		st.rate, st.last = r, now
	case st.last.at.IsZero():
		st.last = now
	}
	e.rates[hash] = st
	return st.rate
}

type rateState struct {
	last sample
	rate int64
}

// DataPathFor returns where a transfer's files live. It is built from the
// engine's own directory and the info hash — never from the torrent's declared
// name, which is attacker-controlled and is the classic path-traversal vector
// in a BitTorrent client.
//
// The hash is validated rather than trusted, and the error is not decorative.
// filepath.Join cleans its result, so Join(dataDir, "../../etc") escapes the
// data directory entirely; a future caller that reads an identifier off a
// request and hands it here would turn this into an arbitrary-path primitive.
// Refusing anything that is not 40 hex characters makes that impossible by
// construction instead of by remembering.
func (e *Engine) DataPathFor(hash string) (string, error) {
	if !IsInfoHash(hash) {
		return "", fmt.Errorf("download: %q is not an info hash", hash)
	}
	return filepath.Join(e.cfg.DataDir, hash), nil
}

// IsInfoHash reports whether s is exactly 40 lowercase hex characters, which
// is what InfoHash.HexString produces.
//
// Exported so the API layer validates against the same definition the engine
// does rather than its own copy. Two copies of a rule like this drift, and the
// half that drifts is the half nobody is looking at.
//
// v2 (SHA-256) hashes are 64 characters and are deliberately rejected: this
// engine is v1-only, so accepting one would turn an honest refusal into a
// confusing "no such transfer".
func IsInfoHash(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ipv6Available reports whether this host can open an IPv6 socket.
//
// It binds and closes a loopback listener rather than inspecting interfaces:
// an interface with an address is not the same as a working stack, and the
// bind is the operation that will actually be attempted.
//
// A loopback bind does not wait on anything, so no deadline is needed; the
// background context is only what the listener's API asks for.
func ipv6Available() bool {
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp6", "[::1]:0")
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// ListenPort reports the port the client is listening on, which is what a test
// (or a NAT-forwarding operator) needs to reach it.
func (e *Engine) ListenPort() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed || e.client == nil {
		return 0
	}
	return e.client.LocalPort()
}

// AddPeer points a transfer at a specific peer.
//
// It exists for two callers: a test that needs a deterministic connection
// rather than waiting on discovery, and the operator-facing "add peer" that
// private trackers sometimes require. The address still goes through the
// guarded dialer when the client tries it — this records a candidate, it does
// not open anything.
func (e *Engine) AddPeer(hash, host string, port int) error {
	t, err := e.lookup(hash)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: %q is not an IP address", ErrUnsafePeer, host)
	}
	if port <= 0 || port > 65535 {
		return fmt.Errorf("%w: port %d", ErrUnsafePeer, port)
	}
	t.AddPeers([]torrent.PeerInfo{{Addr: &net.TCPAddr{IP: ip, Port: port}}})
	return nil
}

// Verify re-hashes a transfer's data against the torrent.
//
// It is needed whenever files exist on disk that the client has not hashed
// itself: seeding something already downloaded, or recovering after a crash
// that lost the completion state. Without it the client believes it holds zero
// bytes and re-downloads everything it already has.
func (e *Engine) Verify(ctx context.Context, hash string) error {
	t, err := e.lookup(hash)
	if err != nil {
		return err
	}
	select {
	case <-t.GotInfo():
		// A piece that does not match is not an error — it is marked missing
		// and fetched again. An error is the check not happening at all: the
		// data could not be read.
		if err := t.VerifyDataContext(ctx); err != nil {
			return fmt.Errorf("download: verifying %s: %w", hash, err)
		}
		return nil
	default:
		return fmt.Errorf("download: cannot verify %s before its metadata arrives", hash)
	}
}

// TransferFile is one file inside a transfer.
type TransferFile struct {
	// Path is relative to the transfer's own data directory, forward slashes.
	Path  string
	Bytes int64
	// Completed is how much of it has arrived.
	Completed int64
}

// maxFilesOnDisk bounds FilesOnDisk. A release is a handful of files; a
// directory holding more than this is not one, and is refused rather than
// walked to the end. A variable only so a test can lower it.
var maxFilesOnDisk = 10000

// ErrTooManyFiles means a download's directory holds more files than any
// release does.
var ErrTooManyFiles = errors.New("download: the transfer's directory holds too many files to be a release")

// FilesOnDisk lists what a transfer left on disk, for one the engine no longer
// holds.
//
// A completed transfer leaves the engine when seeding ends — at once, when
// seeding is off — and is not re-added after a restart. Its files are still
// where the engine wrote them, and they are what the import needs; asking only
// the engine left a download that finished shortly before either event
// complete in the queue and never imported (found verifying ADR-0026).
//
// The directory is the one DataPathFor names, from the engine's own data
// directory and the validated hash — never from the torrent's declared name.
// It is walked through os.Root, so nothing it contains can lead the walk
// outside it, and only regular files are listed: a symbolic link is neither
// followed nor returned. Paths are relative to the directory, with forward
// slashes, as FilesOf returns them.
func (e *Engine) FilesOnDisk(hash string) ([]TransferFile, error) {
	dir, err := e.DataPathFor(hash)
	if err != nil {
		return nil, err
	}
	return filesUnder(dir)
}

func filesUnder(dir string) ([]TransferFile, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("download: nothing of this transfer is on disk: %w", err)
	}
	defer func() { _ = root.Close() }()

	var out []TransferFile
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if !d.Type().IsRegular() {
			// Directories are walked into; everything else — a symbolic
			// link, a device, a pipe — is not part of a release.
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if len(out) >= maxFilesOnDisk {
			return ErrTooManyFiles
		}
		out = append(out, TransferFile{Path: p, Bytes: info.Size(), Completed: info.Size()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("download: reading what the transfer left on disk: %w", err)
	}
	return out, nil
}

// FilesOf lists what a transfer contains.
//
// The paths come from the torrent's file list, which the UPLOADER wrote. They
// are returned as declared, not cleaned: cleaning here would hide a hostile
// entry from the caller that is supposed to refuse it, and the refusal belongs
// where the path becomes a filesystem operation (library.ContainedSource), not
// in a listing that is also used for display.
//
// Metadata is required, so a magnet that has not resolved returns nothing.
func (e *Engine) FilesOf(hash string) ([]TransferFile, error) {
	t, err := e.lookup(hash)
	if err != nil {
		return nil, err
	}
	select {
	case <-t.GotInfo():
	default:
		return nil, fmt.Errorf("download: metadata for %s has not arrived yet", hash)
	}

	files := t.Files()
	out := make([]TransferFile, 0, len(files))
	for _, f := range files {
		out = append(out, TransferFile{
			Path:      f.Path(),
			Bytes:     f.Length(),
			Completed: f.BytesCompleted(),
		})
	}
	return out, nil
}

package download

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/jakethecake75/cmediastack/internal/egress"
	"github.com/jakethecake75/cmediastack/internal/egress/socks5test"
)

// Trackers are egress like any peer (ADR-0001, ADR-0065). Found on the
// running binary with a proxy set from the web: the library announces through
// TrackerDialContext and TrackerListenPacket, not the HTTPDialContext the
// engine set, so announces left by the host's own route while the queue said
// the download was proxied.

// trackedTorrent is a torrent whose only tracker is announce.
func trackedTorrent(t *testing.T, announce string) ([]byte, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "payload.bin")
	payload := make([]byte, 64*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 16 * 1024}
	if err := info.BuildFromFilePath(path); err != nil {
		t.Fatal(err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi := metainfo.MetaInfo{InfoBytes: infoBytes, Announce: announce}
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mi.HashInfoBytes().HexString()
}

// announces counts the HTTP announces a tracker receives.
func announces(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		_, _ = w.Write([]byte("d8:intervali1800e5:peers0:e"))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func startTracked(t *testing.T, guard *egress.Guard, announce string) {
	t.Helper()
	e := newEngine(t, guard, Config{DataDir: t.TempDir()})
	data, hash := trackedTorrent(t, announce)
	if _, err := e.AddTorrentBytes(data); err != nil {
		t.Fatal(err)
	}
	_ = e.Start(hash)
}

func within(d time.Duration, done func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if done() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return done()
}

func TestTrackerAnnouncesGoThroughTheGuard(t *testing.T) {
	srv, closedHits := announces(t)
	startTracked(t, testGuard(t, false), srv.URL+"/announce") // the gate is shut
	if within(4*time.Second, func() bool { return closedHits.Load() > 0 }) {
		t.Fatalf("A TRACKER WAS REACHED with the gate shut: %d announces went around the guard", closedHits.Load())
	}

	// The control: with the gate open the same announce does arrive, so the
	// silence above is the guard's and not a torrent that never announced.
	srv2, openHits := announces(t)
	startTracked(t, testGuard(t, true), srv2.URL+"/announce")
	if !within(10*time.Second, func() bool { return openHits.Load() > 0 }) {
		t.Fatal("with the gate open the tracker was never announced to, so the test above proves nothing")
	}
}

func TestUDPTrackersAreRefusedUnderAProxy(t *testing.T) {
	pc, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	var packets atomic.Int32
	go func() {
		buf := make([]byte, 2048)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
			packets.Add(1)
		}
	}()
	tracker := "udp://" + pc.LocalAddr().String() + "/announce"

	proxied := egress.New(egress.Config{Profiles: map[string]egress.Profile{
		ProfileName: {Mode: egress.ModeSOCKS5, Address: "127.0.0.1:1", RemoteDNS: true}}})
	proxied.SetHealthy(true, "test")
	startTracked(t, proxied, tracker)
	if within(4*time.Second, func() bool { return packets.Load() > 0 }) {
		t.Fatalf("A UDP TRACKER WAS REACHED under a socks5 profile: %d packets left outside the proxy", packets.Load())
	}

	// A magnet names its trackers itself, and takes another path in.
	magnetEngine := newEngine(t, proxied, Config{DataDir: t.TempDir()})
	_, hash := trackedTorrent(t, "")
	if _, err := magnetEngine.Add(t.Context(), "magnet:?xt=urn:btih:"+hash+"&tr="+tracker); err != nil {
		t.Fatal(err)
	}
	if within(4*time.Second, func() bool { return packets.Load() > 0 }) {
		t.Fatalf("A UDP TRACKER WAS REACHED from a magnet under a socks5 profile: %d packets", packets.Load())
	}

	startTracked(t, testGuard(t, true), tracker) // direct: the control
	if !within(10*time.Second, func() bool { return packets.Load() > 0 }) {
		t.Fatal("a direct engine never announced over UDP, so the test above proves nothing")
	}
}

func TestAProxyProfileForcesUDPOffWithoutEnforcement(t *testing.T) {
	for _, jailed := range []bool{false, true} {
		cfg, notes := ConfigFor("/data", 0, egress.Profile{Mode: egress.ModeSOCKS5, RemoteDNS: true}, false, jailed)
		if cfg.EnableDHT || cfg.EnableUTP || cfg.AcceptIncoming {
			t.Errorf("jailed=%v: UDP is on under a socks5 profile with enforcement off: %+v", jailed, cfg)
		}
		if strings.Contains(strings.ToLower(strings.Join(notes, " ")), "nothing is tunnelled") {
			t.Errorf("jailed=%v: a proxied engine was told nothing is tunnelled: %v", jailed, notes)
		}
	}
}

// A blocked download profile is the fallback for a proxy that is not in force
// (ADR-0065). DHT and uTP have sockets of their own, outside the guard, so
// blocked must turn them off too — enforcement on or off, jailed or not.
func TestABlockedProfileTurnsUDPOff(t *testing.T) {
	for _, enforcing := range []bool{false, true} {
		for _, jailed := range []bool{false, true} {
			cfg, notes := ConfigFor("/data", 0, egress.Profile{Mode: egress.ModeBlocked}, enforcing, jailed)
			if cfg.EnableDHT || cfg.EnableUTP || cfg.AcceptIncoming {
				t.Errorf("enforcing=%v jailed=%v: a blocked profile left UDP or incoming on: %+v", enforcing, jailed, cfg)
			}
			if !strings.Contains(strings.ToLower(strings.Join(notes, " ")), "blocked") {
				t.Errorf("enforcing=%v jailed=%v: the notes do not say the profile is blocked: %v", enforcing, jailed, notes)
			}
		}
	}
}

// ADR-0066, decision 3: the engine counts the peer connections it attempts
// and how many failed, with the last failure's words — the torrent library
// drops a failed connection silently, so this is the only place it is said.
func TestPeerConnectionAttemptsAndFailuresAreCounted(t *testing.T) {
	seedDir := t.TempDir()
	torrentFile, hash := makeTorrent(t, seedDir, "payload.bin", 64*1024)
	seeder := newEngine(t, testGuard(t, true), Config{DataDir: seedDir, Seed: true, AcceptIncoming: true})
	seed(t, seeder, torrentFile, hash)

	shut := newEngine(t, testGuard(t, false), Config{DataDir: t.TempDir()}) // the gate is shut
	if _, err := shut.AddTorrentBytes(torrentFile); err != nil {
		t.Fatal(err)
	}
	if err := shut.AddPeer(hash, "127.0.0.1", seeder.ListenPort()); err != nil {
		t.Fatal(err)
	}
	_ = shut.Start(hash)
	if !within(5*time.Second, func() bool { return shut.Connections().Failed > 0 }) {
		t.Fatalf("a refused connection was not counted: %+v", shut.Connections())
	}
	c := shut.Connections()
	if c.Attempted < c.Failed || !strings.Contains(c.LastError, "egress") || c.LastErrorAt.IsZero() {
		t.Errorf("connections %+v, want attempts ≥ failures and the guard's refusal as the last error", c)
	}

	open := newEngine(t, testGuard(t, true), Config{DataDir: t.TempDir()})
	if _, err := open.AddTorrentBytes(torrentFile); err != nil {
		t.Fatal(err)
	}
	if err := open.AddPeer(hash, "127.0.0.1", seeder.ListenPort()); err != nil {
		t.Fatal(err)
	}
	_ = open.Start(hash)
	if !within(5*time.Second, func() bool { return open.Connections().Attempted > 0 }) {
		t.Fatal("a connection that worked was not counted as attempted")
	}
	if c := open.Connections(); c.Failed != 0 || c.LastError != "" {
		t.Errorf("a connection that worked was counted as failed: %+v", c)
	}
}

// udpTracker is a BEP 15 tracker that answers with one peer, counting
// announces.
func udpTracker(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	pc, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	var announced atomic.Int32
	go func() {
		b := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(b)
			if err != nil {
				return
			}
			if n < 16 {
				continue
			}
			action, tid := binary.BigEndian.Uint32(b[8:12]), b[12:16]
			var out []byte
			switch action {
			case 0: // connect
				out = append(binary.BigEndian.AppendUint32(nil, 0), tid...)
				out = binary.BigEndian.AppendUint64(out, 0x1122334455667788)
			case 1: // announce
				announced.Add(1)
				out = append(binary.BigEndian.AppendUint32(nil, 1), tid...)
				out = binary.BigEndian.AppendUint32(out, 1800)
				out = binary.BigEndian.AppendUint32(out, 0)
				out = binary.BigEndian.AppendUint32(out, 1)
				out = append(out, 203, 0, 113, 7, 0x1A, 0xE1) // 203.0.113.7:6881
			default:
				continue
			}
			_, _ = pc.WriteTo(out, from)
		}
	}()
	return pc.LocalAddr().String(), &announced
}

// ADR-0067: under a proxy that relays UDP, a torrent's UDP trackers are
// announced to through it, by name — never resolved here, never sent direct.
func TestUDPTrackersGoThroughAProxyThatRelaysUDP(t *testing.T) {
	stub, announced := udpTracker(t)
	srv := socks5test.Start(t)
	srv.Names["tracker.example.invalid"] = stub
	guard := egress.New(egress.Config{Profiles: map[string]egress.Profile{
		ProfileName: {Mode: egress.ModeSOCKS5, Address: srv.Addr, Username: "u", Password: "p", RemoteDNS: true}}})
	guard.SetHealthy(true, "test")
	e := newEngine(t, guard, Config{DataDir: t.TempDir()})
	data, hash := trackedTorrent(t, "udp://tracker.example.invalid:1337/announce")
	if _, err := e.AddTorrentBytes(data); err != nil {
		t.Fatal(err)
	}
	_ = e.Start(hash)

	if !within(10*time.Second, func() bool { return announced.Load() > 0 }) {
		t.Fatalf("the UDP tracker was never announced to through the proxy; the proxy relayed %+v", srv.Sent())
	}
	for _, d := range srv.Sent() {
		if d.Domain != "tracker.example.invalid" {
			t.Errorf("a datagram reached the proxy addressed %+v, not by the tracker's name", d)
		}
	}
	// The peer is dialled as soon as it is learned — through the proxy, which
	// here relays only UDP — so a connection attempt is the proof it arrived.
	if !within(5*time.Second, func() bool { return e.Connections().Attempted > 0 }) {
		t.Errorf("the tracker's peer was never tried: %+v", e.Connections())
	}
	if !strings.Contains(strings.Join(e.Notes(), " "), "UDP trackers go through the proxy") {
		t.Errorf("the notes do not say UDP trackers are proxied: %v", e.Notes())
	}
}

// ADR-0067, decision 3: a proxy that refuses UDP leaves UDP trackers off, and
// says so — never direct, never a panic.
func TestAProxyThatRefusesUDPLeavesUDPTrackersOff(t *testing.T) {
	stub, announced := udpTracker(t)
	srv := socks5test.Start(t)
	srv.RefuseUDP = true
	srv.Names["tracker.example.invalid"] = stub
	guard := egress.New(egress.Config{Profiles: map[string]egress.Profile{
		ProfileName: {Mode: egress.ModeSOCKS5, Address: srv.Addr, Username: "u", Password: "p", RemoteDNS: true}}})
	guard.SetHealthy(true, "test")
	e := newEngine(t, guard, Config{DataDir: t.TempDir()})
	data, hash := trackedTorrent(t, "udp://tracker.example.invalid:1337/announce")
	if _, err := e.AddTorrentBytes(data); err != nil {
		t.Fatal(err)
	}
	_ = e.Start(hash)
	if within(3*time.Second, func() bool { return announced.Load() > 0 }) {
		t.Error("a UDP tracker was announced to although the proxy refused UDP")
	}
	if !strings.Contains(strings.Join(e.Notes(), " "), "refused UDP") {
		t.Errorf("the notes do not say the proxy refused UDP: %v", e.Notes())
	}
}

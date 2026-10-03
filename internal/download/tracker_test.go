package download

import (
	"bytes"
	"crypto/rand"
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

package download

import (
	"bytes"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/jakethecake75/cmediastack/internal/egress"
)

// testGuard builds an enforcing, healthy guard.
//
// DenyPrivate is OFF here, because these tests connect two in-process clients
// over loopback and the point being measured is the gate, not the SSRF filter —
// which has its own tests in internal/egress. Production sets it: a tracker
// returning 127.0.0.1:22 as a "peer" is a port-scan primitive.
func testGuard(t *testing.T, healthy bool) *egress.Guard {
	t.Helper()
	g := egress.New(egress.Config{
		Profiles: map[string]egress.Profile{ProfileName: {Mode: egress.ModeDirect}},
		Enforce:  true,
	})
	if healthy {
		g.SetHealthy(true, "test")
	}
	return g
}

// makeTorrent builds a .torrent over a file of random bytes, announcing
// nothing: these tests are about peer connections, and a tracker would be an
// outbound request to somewhere that does not exist.
//
// seedDir, when given, receives a copy of the payload at the path the engine's
// storage actually looks in — DataDir/<infohash>/<name>, because the engine uses
// storage.NewFileByInfoHash. Writing it anywhere else produces a "seeder" that
// holds no data, which looks exactly like a network failure and is how the first
// version of this test wasted a run.
func makeTorrent(t *testing.T, seedDir, name string, size int) ([]byte, string) {
	t.Helper()

	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	staging := t.TempDir()
	path := filepath.Join(staging, name)
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

	mi := metainfo.MetaInfo{InfoBytes: infoBytes}
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		t.Fatal(err)
	}
	hash := mi.HashInfoBytes().HexString()

	if seedDir != "" {
		dest := filepath.Join(seedDir, hash)
		if err := os.MkdirAll(dest, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, name), payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes(), hash
}

// seed adds a torrent whose data is already on disk and hashes it, so the
// client knows it holds the bytes rather than believing it holds none.
func seed(t *testing.T, e *Engine, torrentFile []byte, hash string) {
	t.Helper()
	if _, err := e.AddTorrentBytes(torrentFile); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	if err := e.Verify(t.Context(), hash); err != nil {
		t.Fatalf("verifying the seed data: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, tr := range e.List() {
			if tr.InfoHash == hash && tr.Done {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the seeder never reached 100%: it does not actually hold the data")
}

func newEngine(t *testing.T, guard *egress.Guard, cfg Config) *Engine {
	t.Helper()
	if cfg.DataDir == "" {
		cfg.DataDir = t.TempDir()
	}
	e, err := New(cfg, guard, nil)
	if err != nil {
		t.Fatalf("starting the engine: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// ---------------------------------------------------------------------------
// The guarantee
// ---------------------------------------------------------------------------

// The headline property, measured behaviourally because the library exports no
// way to count its dialers.
//
// A seeder and a leecher run in the same process, on loopback, with everything
// that could possibly work working — except the gate, which is shut. If ANY
// path out existed besides the guarded dialer, the leecher would find the
// seeder and the transfer would progress. It must not.
func TestAClosedGateStopsPeerConnections(t *testing.T) {
	seedDir := t.TempDir()
	torrentFile, hash := makeTorrent(t, seedDir, "payload.bin", 256*1024)

	// The seeder's gate is OPEN: it has to be able to run at all.
	seeder := newEngine(t, testGuard(t, true), Config{
		DataDir: seedDir, Seed: true, AcceptIncoming: true,
	})
	seed(t, seeder, torrentFile, hash)

	// The leecher's gate is SHUT.
	closedGuard := testGuard(t, false)
	leecher := newEngine(t, closedGuard, Config{DataDir: t.TempDir()})

	if _, err := leecher.AddTorrentBytes(torrentFile); err != nil {
		t.Fatalf("adding to the leecher: %v", err)
	}
	// Point it straight at the seeder, so nothing depends on discovery.
	if err := leecher.AddPeer(hash, "127.0.0.1", seeder.ListenPort()); err != nil {
		t.Fatalf("adding a peer: %v", err)
	}
	_ = leecher.Start(hash)

	// Generous: if a connection were going to happen, loopback would make it
	// happen in milliseconds.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		for _, tr := range leecher.List() {
			if tr.Completed > 0 {
				t.Fatalf("THE GATE LEAKED: %d bytes transferred with egress unhealthy", tr.Completed)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	// And the same setup with the gate OPEN must actually transfer, or the test
	// above proves nothing — it would pass just as well if the wiring were
	// broken and no transfer could ever happen.
	closedGuard.SetHealthy(true, "gate opened")
	if err := leecher.AddPeer(hash, "127.0.0.1", seeder.ListenPort()); err != nil {
		t.Fatalf("re-adding the peer: %v", err)
	}

	// A FAILURE timeout, not a wait: this returns the instant the transfer
	// completes, and the number only matters when something is wrong.
	//
	// It was 20 seconds and that made the test flaky — not here, but in
	// `go test ./... -race`, where the race detector's slowdown and two dozen
	// packages competing for four cores can push a complete torrent handshake
	// and transfer past 20s. It passed every time in isolation, which is the
	// worst way for a test to be wrong: it fails in CI and nowhere else.
	//
	// The leak window above stays at 4 seconds and is unaffected by the same
	// load, because it watches for ANY bytes rather than for completion, and a
	// loopback connection is milliseconds even on a busy machine.
	start := time.Now()
	deadline = time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		for _, tr := range leecher.List() {
			if tr.Done {
				// Logged so that a future flake is diagnosable from the output
				// rather than being a mystery about timing.
				t.Logf("the control transfer completed in %s", time.Since(start))
				return // the control case works; the test above is meaningful
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("with the gate OPEN the transfer never completed within %s, so the "+
		"closed-gate result above does not prove anything", time.Since(start))
}

// A guardless engine must refuse to start rather than start unprotected.
func TestTheEngineRefusesToStartWithoutAGuard(t *testing.T) {
	if _, err := New(Config{DataDir: t.TempDir()}, nil, nil); err == nil {
		t.Fatal("the engine started with no egress guard")
	}
}

// ---------------------------------------------------------------------------
// The derived configuration
// ---------------------------------------------------------------------------

// The failure this prevents is silent: an operator configures SOCKS5, believes
// their traffic is tunnelled, and DHT announces their address to the swarm over
// UDP for months. Nothing errors, nothing logs.
func TestASOCKS5ProfileForcesDHTAndUTPOff(t *testing.T) {
	cfg, notes := ConfigFor("/data", 0,
		egress.Profile{Mode: egress.ModeSOCKS5, RemoteDNS: true}, true, false)

	if cfg.EnableDHT {
		t.Error("DHT is on under a socks5 profile: it is UDP and would leave outside the tunnel")
	}
	if cfg.EnableUTP {
		t.Error("uTP is on under a socks5 profile: same problem")
	}
	if cfg.AcceptIncoming {
		t.Error("incoming connections are accepted under a socks5 profile")
	}
	if len(notes) == 0 {
		t.Fatal("the degradation was applied silently")
	}
	joined := strings.ToLower(strings.Join(notes, " "))
	for _, want := range []string{"dht", "utp", "incoming"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the notes do not mention %s: %v", want, notes)
		}
	}
}

// Under the namespace guard the kernel carries the guarantee, so UDP is safe.
func TestTheNamespaceGuardKeepsDHTAndUTP(t *testing.T) {
	cfg, notes := ConfigFor("/data", 0, egress.Profile{Mode: egress.ModeDirect}, true, true)

	if !cfg.EnableDHT || !cfg.EnableUTP {
		t.Error("DHT or uTP was disabled under the namespace guard, where they are safe")
	}
	if len(notes) == 0 {
		t.Error("no explanation was recorded")
	}
}

func TestWithNoEnforcementEverythingIsOnAndSaidPlainly(t *testing.T) {
	cfg, notes := ConfigFor("/data", 0, egress.Profile{Mode: egress.ModeDirect}, false, false)

	if !cfg.EnableDHT || !cfg.EnableUTP || !cfg.AcceptIncoming {
		t.Error("features were disabled when no enforcement was asked for")
	}
	if !strings.Contains(strings.ToLower(strings.Join(notes, " ")), "nothing is tunnelled") {
		t.Errorf("the notes do not say that nothing is tunnelled: %v", notes)
	}
}

// The warning about nothing being tunnelled must not fire when a namespace IS
// carrying the traffic — which is the ADR-0001 deployment and the recommended
// one.
//
// Found by running it. A downloader that had just logged "egress verified |
// interface=wg0" logged "nothing is tunnelled" on the very next line, because
// ConfigFor was told only about application-level proxying and treated its
// absence as "no tunnel at all". An alarm that fires when everything is correct
// is one an operator learns to ignore, which is how the real alarm gets missed.
func TestANamespacedDownloaderIsNotToldNothingIsTunnelled(t *testing.T) {
	cfg, notes := ConfigFor("/data", 0, egress.Profile{Mode: egress.ModeDirect},
		false /* no application-level proxy */, true /* namespace verified */)

	joined := strings.ToLower(strings.Join(notes, " "))
	if strings.Contains(joined, "nothing is tunnelled") {
		t.Errorf("a downloader inside a verified namespace was told nothing is "+
			"tunnelled:\n\t%v", notes)
	}
	if !strings.Contains(joined, "namespace") {
		t.Errorf("the notes do not say what IS carrying the guarantee: %v", notes)
	}
	// And the behaviour is unchanged: inside the namespace, UDP is safe.
	if !cfg.EnableDHT || !cfg.EnableUTP {
		t.Error("DHT or uTP was disabled inside a namespace, where they are safe")
	}
}

// A proxy profile still forces UDP off, jailed or not: a socks5 proxy carries
// TCP, and the namespace is not what the operator asked to rely on.
func TestAProxyProfileStillForcesUDPOffEvenWhenJailed(t *testing.T) {
	cfg, notes := ConfigFor("/data", 0, egress.Profile{Mode: egress.ModeSOCKS5}, true, true)

	if cfg.EnableDHT || cfg.EnableUTP || cfg.AcceptIncoming {
		t.Errorf("UDP survived a socks5 profile: %+v", cfg)
	}
	if !strings.Contains(strings.Join(notes, " "), "degraded") {
		t.Errorf("the degraded mode was not named: %v", notes)
	}
}

// ---------------------------------------------------------------------------
// Hostile torrent files
// ---------------------------------------------------------------------------

// A .torrent arrives from an indexer. Its declared name is a bencode string
// chosen by the uploader, and "../../etc/cron.d/x" is a legal one.
func TestAPathTraversalNameNeverReachesAPath(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(t, testGuard(t, true), Config{DataDir: dir})

	torrentFile, hash := makeTorrent(t, "", "payload.bin", 32*1024)
	if _, err := e.AddTorrentBytes(torrentFile); err != nil {
		t.Fatal(err)
	}

	// The path is built from the info hash, never from the declared name.
	path, err := e.DataPathFor(hash)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(dir)) {
		t.Errorf("the data path escaped the data directory: %q", path)
	}
	if strings.Contains(path, "..") {
		t.Errorf("the data path contains a traversal: %q", path)
	}
}

// filepath.Join cleans, so a traversal in the identifier does not merely make
// an odd-looking path — it leaves the data directory altogether. The identifier
// has a known shape, so anything else is refused before it is ever joined.
func TestADataPathCannotBeAskedForOutsideTheDataDirectory(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(t, testGuard(t, true), Config{DataDir: dir})

	for _, bad := range []string{
		"../../etc/passwd",
		"..",
		"/etc/passwd",
		"",
		strings.Repeat("a", 39),
		strings.Repeat("a", 41),
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", // uppercase: not what HexString emits
		"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
	} {
		path, err := e.DataPathFor(bad)
		if err == nil {
			t.Errorf("DataPathFor(%q) returned %q instead of refusing", bad, path)
		}
		if path != "" {
			t.Errorf("DataPathFor(%q) returned a path alongside its error: %q", bad, path)
		}
	}
}

func TestMalformedTorrentFilesAreRefused(t *testing.T) {
	e := newEngine(t, testGuard(t, true), Config{})

	for name, data := range map[string][]byte{
		"empty":       {},
		"not bencode": []byte("this is not a torrent"),
		"truncated":   []byte("d8:announce"),
		"binary":      {0x00, 0x01, 0xff, 0xfe},
		"oversized":   bytes.Repeat([]byte("a"), MaxTorrentFileBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := e.AddTorrentBytes(data); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func TestAddListAndRemove(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(t, testGuard(t, true), Config{DataDir: dir})

	torrentFile, hash := makeTorrent(t, "", "payload.bin", 64*1024)
	tr, err := e.AddTorrentBytes(torrentFile)
	if err != nil {
		t.Fatal(err)
	}
	if tr.InfoHash != hash {
		t.Errorf("hash = %q, want %q", tr.InfoHash, hash)
	}

	list := e.List()
	if len(list) != 1 {
		t.Fatalf("got %d transfers, want 1", len(list))
	}
	if !list[0].MetadataGot {
		t.Error("metadata from a .torrent file should be present immediately")
	}
	if list[0].Bytes != 64*1024 {
		t.Errorf("size = %d", list[0].Bytes)
	}

	if err := e.Remove(hash); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(e.List()) != 0 {
		t.Error("the transfer survived removal")
	}

	if err := e.Remove(hash); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing a gone transfer gave %v, want ErrNotFound", err)
	}
}

// Removing a transfer must not delete bytes. Destroying media is an effect with
// its own permission, and it does not belong to whoever can manage a queue.
func TestRemovingATransferLeavesTheFiles(t *testing.T) {
	seedDir := t.TempDir()
	torrentFile, hash := makeTorrent(t, seedDir, "payload.bin", 32*1024)

	e := newEngine(t, testGuard(t, true), Config{DataDir: seedDir, Seed: true})
	if _, err := e.AddTorrentBytes(torrentFile); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove(hash); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(seedDir, hash, "payload.bin")); err != nil {
		t.Errorf("removing the transfer deleted the file: %v", err)
	}
}

func TestOperationsOnAClosedEngineAreRefused(t *testing.T) {
	e := newEngine(t, testGuard(t, true), Config{})
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := e.AddTorrentBytes([]byte("x")); !errors.Is(err, ErrEngineClosed) {
		t.Errorf("add: %v, want ErrEngineClosed", err)
	}
	if err := e.Remove("abc"); !errors.Is(err, ErrEngineClosed) {
		t.Errorf("remove: %v, want ErrEngineClosed", err)
	}
	if list := e.List(); list != nil {
		t.Errorf("list returned %v on a closed engine", list)
	}
	// Closing twice must be harmless: shutdown paths get called more than once.
	if err := e.Close(); err != nil {
		t.Errorf("the second Close returned %v", err)
	}
}

func TestPercentDoesNotDivideByZero(t *testing.T) {
	if got := (Transfer{}).Percent(); got != 0 {
		t.Errorf("percent of an empty transfer = %v", got)
	}
	if got := (Transfer{Bytes: 200, Completed: 50}).Percent(); got != 25 {
		t.Errorf("percent = %v, want 25", got)
	}
}

// A host with no IPv6 stack is ordinary — minimal containers, some VPS images,
// a namespace built without an IPv6 address. The engine must start anyway, or
// "runs on almost anything" is not true. This container is one such host, so
// every other test in this file is already covering it; this states the
// requirement explicitly so a regression names itself.
func TestTheEngineStartsWithoutAnIPv6Stack(t *testing.T) {
	e, err := New(Config{DataDir: t.TempDir()}, testGuard(t, true), nil)
	if err != nil {
		t.Fatalf("the engine refused to start: %v", err)
	}
	defer func() { _ = e.Close() }()

	if e.ListenPort() == 0 {
		t.Error("no listening port was obtained")
	}
}

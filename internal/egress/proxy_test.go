package egress

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
)

// A SOCKS5 server implemented from RFC 1928, independently of the client under
// test. Testing the client against a mock that mirrors its own assumptions
// proves nothing; this one parses the wire format and records what it actually
// received.
type socksServer struct {
	ln net.Listener

	requireAuth bool
	wantUser    string
	wantPass    string

	// replyCode is what the server answers the CONNECT with. 0x00 succeeds.
	replyCode byte
	// methodOverride, when non-zero, is sent instead of a negotiated method.
	methodOverride byte

	mu sync.Mutex
	// Recorded from the last request, so tests can assert the client sent a
	// hostname rather than resolving it first.
	gotATYP byte
	gotAddr string
	gotPort int
	gotUser string
	gotPass string
}

func newSocksServer(t *testing.T, s *socksServer) *socksServer {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.ln = ln
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s
}

func (s *socksServer) addr() string { return s.ln.Addr().String() }

func (s *socksServer) snapshot() (byte, string, int, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gotATYP, s.gotAddr, s.gotPort, s.gotUser, s.gotPass
}

func (s *socksServer) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil || head[0] != 0x05 {
		return
	}
	methods := make([]byte, head[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}

	chosen := byte(0x00)
	if s.requireAuth {
		chosen = 0x02
	}
	if s.methodOverride != 0 {
		chosen = s.methodOverride
	}
	if _, err := conn.Write([]byte{0x05, chosen}); err != nil {
		return
	}
	if chosen == 0xFF {
		return
	}

	if chosen == 0x02 {
		ver := make([]byte, 2)
		if _, err := io.ReadFull(conn, ver); err != nil {
			return
		}
		user := make([]byte, ver[1])
		if _, err := io.ReadFull(conn, user); err != nil {
			return
		}
		plen := make([]byte, 1)
		if _, err := io.ReadFull(conn, plen); err != nil {
			return
		}
		pass := make([]byte, plen[0])
		if _, err := io.ReadFull(conn, pass); err != nil {
			return
		}

		s.mu.Lock()
		s.gotUser, s.gotPass = string(user), string(pass)
		s.mu.Unlock()

		status := byte(0x00)
		if string(user) != s.wantUser || string(pass) != s.wantPass {
			status = 0x01
		}
		if _, err := conn.Write([]byte{0x01, status}); err != nil || status != 0x00 {
			return
		}
	}

	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil {
		return
	}

	var addr string
	switch req[3] {
	case 0x01:
		b := make([]byte, 4)
		if _, err := io.ReadFull(conn, b); err != nil {
			return
		}
		addr = net.IP(b).String()
	case 0x03:
		n := make([]byte, 1)
		if _, err := io.ReadFull(conn, n); err != nil {
			return
		}
		b := make([]byte, n[0])
		if _, err := io.ReadFull(conn, b); err != nil {
			return
		}
		addr = string(b)
	case 0x04:
		b := make([]byte, 16)
		if _, err := io.ReadFull(conn, b); err != nil {
			return
		}
		addr = net.IP(b).String()
	default:
		return
	}

	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return
	}

	s.mu.Lock()
	s.gotATYP, s.gotAddr = req[3], addr
	s.gotPort = int(portBytes[0])<<8 | int(portBytes[1])
	s.mu.Unlock()

	// Reply with a bound address, as a real server does.
	reply := []byte{0x05, s.replyCode, 0x00, 0x01, 127, 0, 0, 1, 0x04, 0x38}
	if _, err := conn.Write(reply); err != nil || s.replyCode != 0x00 {
		return
	}

	// The tunnel is open: echo, so the test can prove the stream is clean and
	// the bound address was fully drained rather than left in the buffer.
	_, _ = io.Copy(conn, conn)
}

func socksGuard(t *testing.T, s *socksServer, p Profile) Dialer {
	t.Helper()
	p.Mode = ModeSOCKS5
	p.Address = s.addr()
	g := New(Config{Profiles: map[string]Profile{"download": p}})
	g.SetHealthy(true, "test")
	return g.For("download")
}

// ---------------------------------------------------------------------------

func TestSOCKS5ConnectsAndTheStreamIsClean(t *testing.T) {
	s := newSocksServer(t, &socksServer{})
	d := socksGuard(t, s, Profile{RemoteDNS: true})

	conn, err := d.DialContext(t.Context(), "tcp", "tracker.example.org:6969")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	// If the bound address were left unread, these bytes would arrive behind
	// ten bytes of reply and this comparison would fail.
	want := "BitTorrent protocol"
	if _, err := conn.Write([]byte(want)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("stream = %q, want %q — the bound address was not drained", got, want)
	}
}

// The property socks5h exists for: the client must NOT resolve the hostname.
// A local lookup for a tracker is itself a leak, and it happens outside the
// tunnel where it is visible to the operator's ISP.
func TestSOCKS5SendsTheHostnameRatherThanResolvingIt(t *testing.T) {
	s := newSocksServer(t, &socksServer{})
	d := socksGuard(t, s, Profile{RemoteDNS: true})

	conn, err := d.DialContext(t.Context(), "tcp", "private-tracker.example.org:6969")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()

	atyp, addr, port, _, _ := s.snapshot()
	if atyp != atypDomain {
		t.Errorf("address type = 0x%02x, want DOMAIN (0x03) — the client resolved locally", atyp)
	}
	if addr != "private-tracker.example.org" {
		t.Errorf("address = %q, want the hostname unchanged", addr)
	}
	if port != 6969 {
		t.Errorf("port = %d, want 6969", port)
	}
}

// An IP literal is sent as an IP, since there is nothing to resolve.
func TestSOCKS5SendsIPLiteralsAsAddresses(t *testing.T) {
	s := newSocksServer(t, &socksServer{})
	d := socksGuard(t, s, Profile{RemoteDNS: true})

	conn, err := d.DialContext(t.Context(), "tcp", "203.0.113.7:51413")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()

	atyp, addr, _, _, _ := s.snapshot()
	if atyp != atypIPv4 || addr != "203.0.113.7" {
		t.Errorf("got atyp 0x%02x addr %q, want IPv4 203.0.113.7", atyp, addr)
	}
}

func TestSOCKS5Authenticates(t *testing.T) {
	s := newSocksServer(t, &socksServer{
		requireAuth: true, wantUser: "nord-service-user", wantPass: "s3cret",
	})
	d := socksGuard(t, s, Profile{
		RemoteDNS: true, Username: "nord-service-user", Password: "s3cret",
	})

	conn, err := d.DialContext(t.Context(), "tcp", "tracker.example.org:6969")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()

	_, _, _, user, pass := s.snapshot()
	if user != "nord-service-user" || pass != "s3cret" {
		t.Errorf("server received %q/%q", user, pass)
	}
}

// Wrong credentials must fail closed, and the error must not echo the secret
// into the log.
func TestSOCKS5RejectedCredentialsFailWithoutLeakingThem(t *testing.T) {
	s := newSocksServer(t, &socksServer{
		requireAuth: true, wantUser: "right", wantPass: "right",
	})
	d := socksGuard(t, s, Profile{RemoteDNS: true, Username: "wrong", Password: "hunter2"})

	conn, err := d.DialContext(t.Context(), "tcp", "tracker.example.org:6969")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("a connection was returned despite rejected credentials")
	}
	if err == nil {
		t.Fatal("rejected credentials produced no error")
	}
	if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "wrong") {
		t.Errorf("the error leaked credentials: %v", err)
	}
}

func TestSOCKS5ProxyRefusalIsReportedSpecifically(t *testing.T) {
	for code, want := range map[byte]string{
		0x02: "not allowed by ruleset",
		0x03: "network unreachable",
		0x05: "connection refused",
	} {
		s := newSocksServer(t, &socksServer{replyCode: code})
		d := socksGuard(t, s, Profile{RemoteDNS: true})

		conn, err := d.DialContext(t.Context(), "tcp", "tracker.example.org:6969")
		if conn != nil {
			_ = conn.Close()
			t.Fatalf("reply 0x%02x still produced a connection", code)
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("reply 0x%02x gave %v, want it to mention %q", code, err, want)
		}
	}
}

func TestSOCKS5NoAcceptableMethodFailsClosed(t *testing.T) {
	s := newSocksServer(t, &socksServer{methodOverride: 0xFF})
	d := socksGuard(t, s, Profile{RemoteDNS: true})

	conn, err := d.DialContext(t.Context(), "tcp", "tracker.example.org:6969")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("a connection survived a rejected handshake")
	}
	if err == nil || !strings.Contains(err.Error(), "rejected every authentication method") {
		t.Errorf("got %v", err)
	}
}

// The config lint refuses socks5 without remote_dns at boot. Refusing again in
// the dialer means a programmatic caller cannot construct the leak either.
func TestSOCKS5WithoutRemoteDNSIsRefused(t *testing.T) {
	s := newSocksServer(t, &socksServer{})
	d := socksGuard(t, s, Profile{RemoteDNS: false})

	conn, err := d.DialContext(t.Context(), "tcp", "tracker.example.org:6969")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("socks5 without remote_dns connected anyway")
	}
	if err == nil || !strings.Contains(err.Error(), "remote_dns") {
		t.Errorf("got %v, want a refusal naming remote_dns", err)
	}
}

// UDP is not relayed. Saying so beats appearing to work and silently losing
// DHT and every udp:// tracker (ADR-0001).
func TestSOCKS5RefusesUDPRatherThanPretending(t *testing.T) {
	s := newSocksServer(t, &socksServer{})
	d := socksGuard(t, s, Profile{RemoteDNS: true})

	if _, err := d.DialContext(t.Context(), "udp", "tracker.example.org:6969"); err == nil {
		t.Fatal("a udp dial through socks5 appeared to succeed")
	}
}

func TestSOCKS5UnreachableProxyDoesNotFallBack(t *testing.T) {
	// A port nothing is listening on.
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	_ = ln.Close()

	g := New(Config{Profiles: map[string]Profile{
		"download": {Mode: ModeSOCKS5, Address: dead, RemoteDNS: true},
	}})
	g.SetHealthy(true, "routing looks fine")

	conn, err := g.For("download").DialContext(t.Context(), "tcp", "tracker.example.org:6969")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("an unreachable proxy produced a connection — something fell back")
	}
	if err == nil || !strings.Contains(err.Error(), "reaching socks5 proxy") {
		t.Errorf("got %v", err)
	}
}

// ---------------------------------------------------------------------------
// HTTP CONNECT
// ---------------------------------------------------------------------------

func httpProxy(t *testing.T, status string, extra string) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				buf := make([]byte, 1024)
				n, err := conn.Read(buf)
				if err != nil {
					return
				}
				if !strings.HasPrefix(string(buf[:n]), "CONNECT ") {
					return
				}
				if _, err := io.WriteString(conn, status+extra); err != nil {
					return
				}
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestHTTPConnectOpensATunnel(t *testing.T) {
	addr := httpProxy(t, "HTTP/1.1 200 Connection Established\r\n\r\n", "")

	g := New(Config{Profiles: map[string]Profile{
		"download": {Mode: ModeHTTPProxy, Address: addr},
	}})
	g.SetHealthy(true, "test")

	conn, err := g.For("download").DialContext(t.Context(), "tcp", "tracker.example.org:443")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 5)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("stream = %q", got)
	}
}

func TestHTTPConnectRefusalFailsClosed(t *testing.T) {
	addr := httpProxy(t, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n", "")

	g := New(Config{Profiles: map[string]Profile{
		"download": {Mode: ModeHTTPProxy, Address: addr},
	}})
	g.SetHealthy(true, "test")

	conn, err := g.For("download").DialContext(t.Context(), "tcp", "tracker.example.org:443")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("a 407 still produced a tunnel")
	}
	if err == nil || !strings.Contains(err.Error(), "407") {
		t.Errorf("got %v", err)
	}
}

// A proxy that sends payload alongside the 200 would have it stranded in a
// buffer this code discards. Losing the first bytes of a BitTorrent handshake
// is a bug somebody would chase for days, so it is refused instead.
func TestHTTPConnectRefusesDataSentBeforeTheTunnelOpens(t *testing.T) {
	addr := httpProxy(t, "HTTP/1.1 200 Connection Established\r\n\r\n", "surprise-payload")

	g := New(Config{Profiles: map[string]Profile{
		"download": {Mode: ModeHTTPProxy, Address: addr},
	}})
	g.SetHealthy(true, "test")

	conn, err := g.For("download").DialContext(t.Context(), "tcp", "tracker.example.org:443")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("early data was accepted, silently corrupting the stream")
	}
	if err == nil || !strings.Contains(err.Error(), "before the tunnel opened") {
		t.Errorf("got %v", err)
	}
}

// An unrecognised mode must be blocked rather than falling through to direct.
// The unhealthy case never reaches the mode switch, so this asserts the branch
// that TestNoFallbackWhenUnhealthy cannot.
func TestUnknownModeIsBlockedEvenWhenHealthy(t *testing.T) {
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	g := healthyGuard(t, map[string]Profile{"download": {Mode: Mode("tor-someday")}})

	conn, err := g.For("download").DialContext(t.Context(), "tcp", ln.Addr().String())
	if conn != nil {
		_ = conn.Close()
		t.Fatal("an unknown mode dialled anyway")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Errorf("got %v, want ErrBlocked", err)
	}
}

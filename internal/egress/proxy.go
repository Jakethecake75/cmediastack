package egress

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SOCKS5 (RFC 1928) and username/password authentication (RFC 1929), written
// in-tree for the same reason as the metrics exposition format: this build
// environment cannot reach sum.golang.org, so a new dependency's checksums
// could not be verified against the transparency log, and SECURITY.md promises
// pinned and verified dependencies. See ADR-0010.
//
// The protocol is small, fixed and fully specified, and proxy_test.go drives
// every branch against a real server implemented from the same RFC — including
// the ones that matter most, which are the refusals.

const (
	socks5Version = 0x05

	authNone         = 0x00
	authUserPass     = 0x02
	authNoAcceptable = 0xFF

	authUserPassVersion = 0x01
	authStatusSuccess   = 0x00

	cmdConnect = 0x01

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	replySucceeded = 0x00
)

// socksReplyText maps RFC 1928 §6 reply codes to something an operator can act
// on. "general failure" for all of them would make a misconfigured credential
// indistinguishable from an unreachable tracker.
var socksReplyText = map[byte]string{
	0x01: "general SOCKS server failure",
	0x02: "connection not allowed by ruleset",
	0x03: "network unreachable",
	0x04: "host unreachable",
	0x05: "connection refused",
	0x06: "TTL expired",
	0x07: "command not supported",
	0x08: "address type not supported",
}

// dialSOCKS5 opens a tunnelled connection through a SOCKS5 proxy.
//
// The destination host is sent as a DOMAIN address whenever it is not already
// an IP literal, so the proxy resolves it — socks5h semantics. Resolving here
// would put a DNS query for every tracker and every indexer onto the host's
// resolver, outside the tunnel, which is precisely the leak this mode exists to
// prevent. profileDialer refuses the profile outright when RemoteDNS is off.
func dialSOCKS5(ctx context.Context, base *net.Dialer, p Profile, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		// SOCKS5 UDP ASSOCIATE is not implemented, and NordVPN's endpoints do
		// not reliably relay UDP anyway (ADR-0001). Saying so beats appearing
		// to work and silently losing DHT and udp:// trackers.
		return nil, fmt.Errorf("egress: socks5 supports tcp only, not %q", network)
	}

	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("egress: malformed destination %q", address)
	}
	n, err := strconv.Atoi(portStr)
	if err != nil || n < 1 || n > math.MaxUint16 {
		return nil, fmt.Errorf("egress: bad destination port in %q", address)
	}
	port := uint16(n)

	conn, err := base.DialContext(ctx, "tcp", p.Address)
	if err != nil {
		return nil, fmt.Errorf("egress: reaching socks5 proxy %s: %w", p.Address, err)
	}

	// Every failure below closes the connection and returns. There is no branch
	// that falls through to a direct dial.
	if err := socks5Handshake(ctx, conn, p, host, port); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func socks5Handshake(ctx context.Context, conn net.Conn, p Profile, host string, port uint16) error {
	// The context bounds the whole handshake. Without this a proxy that accepts
	// the TCP connection and then says nothing would hang a download worker
	// indefinitely.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
		defer func() { _ = conn.SetDeadline(time.Time{}) }()
	}

	methods := []byte{authNone}
	if p.Username != "" {
		// Offer both, preferring authenticated. A proxy that would accept us
		// anonymously still gets told who we are if it asks.
		methods = []byte{authUserPass, authNone}
	}

	count, err := oneByte(len(methods))
	if err != nil {
		return err
	}
	greeting := append([]byte{socks5Version, count}, methods...)
	if _, err := conn.Write(greeting); err != nil {
		return fmt.Errorf("egress: socks5 greeting: %w", err)
	}

	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fmt.Errorf("egress: socks5 method reply: %w", err)
	}
	if resp[0] != socks5Version {
		return fmt.Errorf("egress: socks5 proxy answered version %d, want 5", resp[0])
	}

	switch resp[1] {
	case authNone:
		// Nothing to do.
	case authUserPass:
		if p.Username == "" {
			return errors.New("egress: socks5 proxy demands a username but none is configured")
		}
		if err := socks5Authenticate(conn, p); err != nil {
			return err
		}
	case authNoAcceptable:
		return errors.New("egress: socks5 proxy rejected every authentication method offered")
	default:
		return fmt.Errorf("egress: socks5 proxy chose unsupported method 0x%02x", resp[1])
	}

	req := []byte{socks5Version, cmdConnect, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			req = append(req, atypIPv4)
			req = append(req, v4...)
		} else {
			req = append(req, atypIPv6)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return fmt.Errorf("egress: hostname is %d bytes, socks5 allows 255", len(host))
		}
		n, err := oneByte(len(host))
		if err != nil {
			return err
		}
		req = append(req, atypDomain, n)
		req = append(req, host...)
	}
	req = binary.BigEndian.AppendUint16(req, port)

	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("egress: socks5 connect request: %w", err)
	}

	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return fmt.Errorf("egress: socks5 connect reply: %w", err)
	}
	if head[0] != socks5Version {
		return fmt.Errorf("egress: socks5 reply version %d, want 5", head[0])
	}
	if head[1] != replySucceeded {
		text, ok := socksReplyText[head[1]]
		if !ok {
			text = fmt.Sprintf("unknown reply code 0x%02x", head[1])
		}
		return fmt.Errorf("egress: socks5 proxy refused: %s", text)
	}

	// The bound address is of no use to a CONNECT client, but it has to be
	// drained or it becomes the first bytes of the caller's stream.
	switch head[3] {
	case atypIPv4:
		_, err := io.ReadFull(conn, make([]byte, 4+2))
		return wrapDrain(err)
	case atypIPv6:
		_, err := io.ReadFull(conn, make([]byte, 16+2))
		return wrapDrain(err)
	case atypDomain:
		n := make([]byte, 1)
		if _, err := io.ReadFull(conn, n); err != nil {
			return wrapDrain(err)
		}
		_, err := io.ReadFull(conn, make([]byte, int(n[0])+2))
		return wrapDrain(err)
	default:
		return fmt.Errorf("egress: socks5 reply used address type 0x%02x", head[3])
	}
}

// oneByte is n as the single byte the SOCKS5 wire format gives a length or a
// count. Every caller has already refused anything longer; this refuses it
// again rather than let a conversion wrap into a shorter length that would
// describe different bytes than follow it.
func oneByte(n int) (byte, error) {
	if n < 0 || n > math.MaxUint8 {
		return 0, fmt.Errorf("egress: %d does not fit the one byte socks5 allows", n)
	}
	return byte(n), nil
}

func wrapDrain(err error) error {
	if err != nil {
		return fmt.Errorf("egress: socks5 bound address: %w", err)
	}
	return nil
}

func socks5Authenticate(conn net.Conn, p Profile) error {
	if len(p.Username) > 255 || len(p.Password) > 255 {
		return errors.New("egress: socks5 credentials exceed 255 bytes")
	}

	user, err := oneByte(len(p.Username))
	if err != nil {
		return err
	}
	pass, err := oneByte(len(p.Password))
	if err != nil {
		return err
	}
	msg := []byte{authUserPassVersion, user}
	msg = append(msg, p.Username...)
	msg = append(msg, pass)
	msg = append(msg, p.Password...)

	if _, err := conn.Write(msg); err != nil {
		return fmt.Errorf("egress: socks5 authentication: %w", err)
	}

	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fmt.Errorf("egress: socks5 authentication reply: %w", err)
	}
	if resp[1] != authStatusSuccess {
		// Deliberately does not echo the username. This error reaches the logs,
		// and the proxy credential is a secret.
		return errors.New("egress: socks5 proxy rejected the configured credentials")
	}
	return nil
}

// ---------------------------------------------------------------------------
// HTTP CONNECT
// ---------------------------------------------------------------------------

// dialHTTPConnect tunnels through an HTTP proxy.
//
// It is supported because the config offers it, not because it is recommended:
// CONNECT to a plaintext proxy hands the destination hostname to anything on
// the path. Prefer socks5 with remote_dns, or the namespace guard.
func dialHTTPConnect(ctx context.Context, base *net.Dialer, p Profile, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("egress: http-proxy supports tcp only, not %q", network)
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return nil, fmt.Errorf("egress: malformed destination %q", address)
	}

	conn, err := base.DialContext(ctx, "tcp", p.Address)
	if err != nil {
		return nil, fmt.Errorf("egress: reaching http proxy %s: %w", p.Address, err)
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "CONNECT %s HTTP/1.1\r\n", address)
	fmt.Fprintf(&b, "Host: %s\r\n", address)
	if p.Username != "" {
		cred := base64.StdEncoding.EncodeToString([]byte(p.Username + ":" + p.Password))
		fmt.Fprintf(&b, "Proxy-Authorization: Basic %s\r\n", cred)
	}
	b.WriteString("Proxy-Connection: Keep-Alive\r\n\r\n")

	if _, err := io.WriteString(conn, b.String()); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("egress: http proxy CONNECT: %w", err)
	}

	// bufio over the raw conn would buffer past the header and swallow the
	// first bytes of the tunnelled stream, so the reader is bounded to the
	// response head and the connection is handed back unread beyond it.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("egress: http proxy reply: %w", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("egress: http proxy refused with %s", resp.Status)
	}
	if br.Buffered() > 0 {
		// A proxy that sent payload alongside the 200 would have it stranded in
		// a buffer this function is about to discard. Refusing is correct:
		// silently losing the first bytes of a torrent handshake is a bug that
		// would be diagnosed for days.
		_ = conn.Close()
		return nil, errors.New("egress: http proxy sent data before the tunnel opened")
	}
	return conn, nil
}

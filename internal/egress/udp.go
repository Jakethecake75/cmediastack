package egress

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"
)

// UDP through a SOCKS5 proxy: UDP ASSOCIATE, RFC 1928 §7 (ADR-0067).

const cmdUDPAssociate = 0x03

// aliasPrefix is where tracker names are given placeholder addresses. They
// never leave the process as destinations: the association sends the name.
var aliasPrefix = netip.MustParsePrefix("127.88.0.0/16")

// Aliases maps tracker hostnames to placeholder addresses in 127.88.0.0/16,
// so a library that resolves a host before every send is given an address it
// sends to without a lookup, and the association turns it back into the name
// for the proxy to resolve.
type Aliases struct {
	mu     sync.Mutex
	byName map[string]netip.Addr
	byAddr map[netip.Addr]string
}

// NewAliases returns an empty set.
func NewAliases() *Aliases {
	return &Aliases{byName: map[string]netip.Addr{}, byAddr: map[netip.Addr]string{}}
}

// Alias returns the placeholder for a name, the same one every time.
func (a *Aliases) Alias(name string) netip.Addr {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ip, ok := a.byName[name]; ok {
		return ip
	}
	n := len(a.byName) + 1 // .0.1 onwards; 65 534 names is more than any engine sees
	b := aliasPrefix.Addr().As4()
	b[2], b[3] = byte(n>>8), byte(n) // #nosec G115 -- n < 65536 by the bound above
	ip := netip.AddrFrom4(b)
	a.byName[name], a.byAddr[ip] = ip, name
	return ip
}

func (a *Aliases) name(ip netip.Addr) (string, bool) {
	if a == nil {
		return "", false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	n, ok := a.byAddr[ip.Unmap()]
	return n, ok
}

// ListenPacket opens a UDP socket for a subsystem. Under a socks5 profile it
// is an association through the proxy; under direct, an ordinary socket;
// blocked, or a subsystem with no profile, is refused. aliases may be nil.
func (g *Guard) ListenPacket(subsystem string, aliases *Aliases) (net.PacketConn, error) {
	p, ok := g.profiles[subsystem]
	if !ok {
		return nil, fmt.Errorf("%w: no egress profile for %q", ErrBlocked, subsystem)
	}
	// The kill switch holds for UDP as for TCP.
	if g.tunnelled[subsystem] && g.Enforcing() {
		if healthy, detail, _ := g.Healthy(); !healthy {
			return nil, fmt.Errorf("%w (%s)", ErrEgressUnavailable, detail)
		}
	}
	switch p.Mode {
	case ModeDirect:
		return (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", ":0")
	case ModeSOCKS5:
		return associate(g.base, p, aliases)
	default:
		return nil, fmt.Errorf("%w: profile %q does not carry UDP (%s)", ErrBlocked, subsystem, p.Mode)
	}
}

// associate opens a UDP association through a SOCKS5 proxy.
func associate(base *net.Dialer, p Profile, aliases *Aliases) (net.PacketConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	control, err := base.DialContext(ctx, "tcp", p.Address)
	if err != nil {
		return nil, fmt.Errorf("egress: reaching socks5 proxy %s: %w", p.Address, err)
	}
	// The client's own address is not known in advance: 0.0.0.0:0 says so.
	bound, err := socks5Handshake(ctx, control, p, cmdUDPAssociate, "0.0.0.0", 0)
	if err != nil {
		_ = control.Close()
		return nil, err
	}
	relay, err := net.ResolveUDPAddr("udp", bound)
	if err != nil || relay.Port == 0 {
		_ = control.Close()
		return nil, fmt.Errorf("egress: socks5 proxy bound an unusable relay %q", bound)
	}
	if relay.IP.IsUnspecified() {
		// "Send to me": the relay is on the proxy's own address.
		if ta, ok := control.RemoteAddr().(*net.TCPAddr); ok {
			relay.IP = ta.IP
		}
	}
	udp, err := net.ListenUDP("udp", nil)
	if err != nil {
		_ = control.Close()
		return nil, err
	}
	pc := &socks5PacketConn{control: control, udp: udp, relay: relay, aliases: aliases}
	go func() {
		// The association lives as long as the control connection (§7).
		_, _ = io.Copy(io.Discard, control)
		_ = pc.Close()
	}()
	return pc, nil
}

// socks5PacketConn sends datagrams to the proxy's relay with the SOCKS5 UDP
// header in front, and takes it off answers. It accepts datagrams only from
// the relay.
type socks5PacketConn struct {
	control net.Conn
	udp     *net.UDPConn
	relay   *net.UDPAddr
	aliases *Aliases
	once    sync.Once
}

func (c *socks5PacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	ua, ok := addr.(*net.UDPAddr)
	if !ok {
		return 0, fmt.Errorf("egress: socks5 UDP needs a UDP address, not %T", addr)
	}
	ip, ok := netip.AddrFromSlice(ua.IP)
	if !ok || ua.Port <= 0 || ua.Port > 0xFFFF {
		return 0, fmt.Errorf("egress: bad UDP destination %v", addr)
	}
	head := []byte{0, 0, 0} // RSV, RSV, FRAG
	if name, ok := c.aliases.name(ip); ok {
		n, err := oneByte(len(name))
		if err != nil {
			return 0, err
		}
		head = append(append(head, atypDomain, n), name...)
	} else if v4 := ip.Unmap(); v4.Is4() {
		a := v4.As4()
		head = append(append(head, atypIPv4), a[:]...)
	} else {
		a := ip.As16()
		head = append(append(head, atypIPv6), a[:]...)
	}
	head = binary.BigEndian.AppendUint16(head, uint16(ua.Port)) // #nosec G115 -- checked above
	if _, err := c.udp.WriteToUDP(append(head, b...), c.relay); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *socks5PacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	buf := make([]byte, 65536)
	for {
		n, from, err := c.udp.ReadFromUDP(buf)
		if err != nil {
			return 0, nil, err
		}
		if !from.IP.Equal(c.relay.IP) || from.Port != c.relay.Port || n < 4 || buf[2] != 0 {
			continue // not the relay's, or a fragment: dropped
		}
		src, rest, err := parseUDPHeader(buf[4:n], buf[3])
		if err != nil {
			continue
		}
		return copy(b, rest), src, nil
	}
}

// parseUDPHeader reads a datagram's source after the RSV/FRAG/ATYP bytes.
func parseUDPHeader(b []byte, atyp byte) (*net.UDPAddr, []byte, error) {
	var ip net.IP
	switch atyp {
	case atypIPv4:
		if len(b) < 4+2 {
			return nil, nil, errors.New("short")
		}
		ip, b = net.IP(b[:4]), b[4:]
	case atypIPv6:
		if len(b) < 16+2 {
			return nil, nil, errors.New("short")
		}
		ip, b = net.IP(b[:16]), b[16:]
	case atypDomain:
		if len(b) < 1 || len(b) < 1+int(b[0])+2 {
			return nil, nil, errors.New("short")
		}
		b = b[1+int(b[0]):] // a name as the source carries no address to give
	default:
		return nil, nil, errors.New("address type")
	}
	return &net.UDPAddr{IP: ip, Port: int(binary.BigEndian.Uint16(b[:2]))}, b[2:], nil
}

func (c *socks5PacketConn) Close() error {
	var err error
	c.once.Do(func() {
		err = errors.Join(c.udp.Close(), c.control.Close())
	})
	return err
}

func (c *socks5PacketConn) LocalAddr() net.Addr                { return c.udp.LocalAddr() }
func (c *socks5PacketConn) SetDeadline(t time.Time) error      { return c.udp.SetDeadline(t) }
func (c *socks5PacketConn) SetReadDeadline(t time.Time) error  { return c.udp.SetReadDeadline(t) }
func (c *socks5PacketConn) SetWriteDeadline(t time.Time) error { return c.udp.SetWriteDeadline(t) }

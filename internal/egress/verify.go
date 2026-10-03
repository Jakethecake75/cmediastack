package egress

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Verification answers one question: does traffic leaving this process
// actually go through the tunnel?
type Verification struct {
	// Jailed is true only when the check positively confirmed the tunnel
	// carries outbound traffic. "Could not determine" is false, never true.
	Jailed bool
	// Interface is the interface the kernel selected for a public destination.
	Interface string
	// SourceIP is the address it would use.
	SourceIP string
	// DefaultRoutes lists every default route found, for diagnostics. More than
	// one, via different interfaces, is worth an operator's attention.
	DefaultRoutes []string
	// Detail explains the verdict in one line, for the log and the admin page.
	Detail string
}

// ErrNotJailed means the process is demonstrably NOT behind the tunnel.
var ErrNotJailed = errors.New("egress: outbound traffic does not leave through the tunnel")

// Verify reports whether outbound traffic leaves through the named interface.
//
// # Why this check and not the obvious ones
//
// Comparing /proc/self/ns/net against /proc/1/ns/net looks like the natural
// test for "am I in a network namespace", and it is wrong for this deployment.
// Under ADR-0001 the downloader container joins the WireGuard container's
// namespace, so PID 1 of that container shares the namespace too and the two
// links are identical — the check would report "not jailed" for a correctly
// jailed process, and an operator who trusted it would disable the guard.
//
// Parsing the route table is closer, but it describes configuration rather than
// behaviour, and the kernel's choice depends on the destination.
//
// So the check is behavioural: ask the kernel which source address it would use
// to reach a public destination, and find out which interface owns it. A UDP
// "connection" performs route selection and binds a local address without
// sending a packet, so this costs nothing and reaches nothing — it works with
// the tunnel down, and it does not phone home.
//
// wantInterface is the interface the operator expects, normally wg0. An empty
// string accepts any interface that is not obviously the host's, which is
// weaker and says so in Detail.
func Verify(wantInterface string) (Verification, error) {
	v := Verification{DefaultRoutes: defaultRoutes()}

	// A link-local-free public address, never contacted. 192.0.2.1 is TEST-NET-1
	// (RFC 5737) — reserved for documentation, routed nowhere, and therefore a
	// destination that cannot accidentally succeed.
	//
	// No deadline: a UDP "connect" is a route lookup in the kernel and waits on
	// nothing. The background context is only what the dialer's API asks for.
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "udp", "192.0.2.1:9")
	if err != nil {
		v.Detail = "the kernel could not select a route to a public address: " + err.Error()
		return v, fmt.Errorf("%w: no route out at all", ErrNotJailed)
	}
	defer func() { _ = conn.Close() }()

	local, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || local.IP == nil {
		v.Detail = "the kernel selected no source address"
		return v, fmt.Errorf("%w: no source address", ErrNotJailed)
	}
	v.SourceIP = local.IP.String()

	iface, err := interfaceOwning(local.IP)
	if err != nil {
		v.Detail = "source address " + v.SourceIP + " belongs to no interface this process can see"
		return v, fmt.Errorf("%w: %w", ErrNotJailed, err)
	}
	v.Interface = iface

	switch {
	case wantInterface != "" && iface == wantInterface:
		v.Jailed = true
		v.Detail = fmt.Sprintf("outbound traffic leaves via %s (%s), as configured", iface, v.SourceIP)
	case wantInterface != "":
		v.Detail = fmt.Sprintf(
			"outbound traffic leaves via %s (%s), but the configured tunnel interface is %s",
			iface, v.SourceIP, wantInterface)
		return v, fmt.Errorf("%w: expected %s, got %s", ErrNotJailed, wantInterface, iface)
	case looksLikeTunnel(iface):
		v.Jailed = true
		v.Detail = fmt.Sprintf(
			"outbound traffic leaves via %s (%s), which looks like a tunnel — "+
				"set the expected interface name to make this check exact", iface, v.SourceIP)
	default:
		v.Detail = fmt.Sprintf(
			"outbound traffic leaves via %s (%s), which is not a tunnel interface", iface, v.SourceIP)
		return v, fmt.Errorf("%w: %s is not a tunnel", ErrNotJailed, iface)
	}

	// Two default routes via different interfaces means the verdict above holds
	// for this destination and may not hold for another. Reported, not fatal:
	// the firewall is what actually prevents the second route being used, and
	// this code cannot see the firewall.
	if len(v.DefaultRoutes) > 1 {
		v.Detail += fmt.Sprintf("; note %d default routes exist (%s)",
			len(v.DefaultRoutes), strings.Join(v.DefaultRoutes, ", "))
	}
	return v, nil
}

// interfaceOwning finds which interface carries an address.
func interfaceOwning(ip net.IP) (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var candidate net.IP
			switch a := addr.(type) {
			case *net.IPNet:
				candidate = a.IP
			case *net.IPAddr:
				candidate = a.IP
			}
			if candidate != nil && candidate.Equal(ip) {
				return iface.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no interface holds %s", ip)
}

// looksLikeTunnel is a weak heuristic, used only when the operator has not said
// which interface to expect. It is not a security check — naming the interface
// is — and Verify's Detail says so when this path is taken.
func looksLikeTunnel(name string) bool {
	for _, prefix := range []string{"wg", "tun", "tap", "ppp", "nordlynx", "proton", "utun"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// defaultRoutes reads /proc/net/route for diagnostics. It is Linux-only and
// best-effort: an empty result means "could not read", never "none exist", and
// nothing above treats it as evidence.
func defaultRoutes() []string {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var out []string
	scanner := bufio.NewScanner(f)
	scanner.Scan() // header
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || fields[1] != "00000000" {
			continue
		}
		gateway := "?"
		if raw, err := strconv.ParseUint(fields[2], 16, 32); err == nil {
			var b [4]byte
			binary.LittleEndian.PutUint32(b[:], uint32(raw))
			gateway = net.IP(b[:]).String()
		}
		out = append(out, fields[0]+" via "+gateway)
	}
	return out
}

// ---------------------------------------------------------------------------
// Health probing
// ---------------------------------------------------------------------------

// Prober decides whether the tunnel is currently usable.
//
// It re-verifies routing on every run rather than trusting the startup check.
// A tunnel that drops does not restart the process: the interface goes away,
// the route changes, and the only thing that notices is a check that looks
// again.
type Prober struct {
	// Interface is the expected tunnel interface.
	Interface string
	// Target is a host:port the probe connects to through the guarded dialer,
	// proving the path works end to end rather than merely looking right.
	// Empty means routing verification alone decides health.
	Target string
	// Dial is the guarded dialer for the download profile.
	Dial Dialer
}

// Probe returns healthy, and a one-line reason either way.
//
// A failure is never ambiguous in the unsafe direction: anything this function
// cannot confirm is reported as unhealthy, which pauses transfers. Pausing a
// working tunnel costs an operator some throughput; the opposite costs them the
// guarantee the whole subsystem exists to provide.
func (p Prober) Probe(ctx context.Context) (bool, string) {
	v, err := Verify(p.Interface)
	if err != nil {
		return false, v.Detail
	}
	if p.Target == "" {
		return true, v.Detail
	}
	if p.Dial == nil {
		return false, "no dialer is wired for the egress probe"
	}

	conn, err := p.Dial.DialContext(ctx, "tcp", p.Target)
	if err != nil {
		return false, fmt.Sprintf("routing is correct (%s) but %s is unreachable: %v",
			v.Interface, p.Target, err)
	}
	_ = conn.Close()
	return true, fmt.Sprintf("%s reachable via %s (%s)", p.Target, v.Interface, v.SourceIP)
}

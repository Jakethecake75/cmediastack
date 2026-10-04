package egress_test

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/egress"
	"github.com/jakethecake75/cmediastack/internal/egress/socks5test"
)

// UDP through the SOCKS5 proxy (ADR-0067).

func echoUDP(t *testing.T) *net.UDPAddr {
	t.Helper()
	pc, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		b := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(append([]byte("echo:"), b[:n]...), from)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr)
}

func proxiedGuard(proxy string) *egress.Guard {
	return egress.New(egress.Config{Profiles: map[string]egress.Profile{
		"download": {Mode: egress.ModeSOCKS5, Address: proxy, Username: "u", Password: "p", RemoteDNS: true},
		"blocked":  {Mode: egress.ModeBlocked},
	}})
}

func roundTrip(t *testing.T, pc net.PacketConn, to net.Addr, msg string) string {
	t.Helper()
	if _, err := pc.WriteTo([]byte(msg), to); err != nil {
		t.Fatal(err)
	}
	_ = pc.SetReadDeadline(time.Now().Add(3 * time.Second))
	b := make([]byte, 2048)
	n, _, err := pc.ReadFrom(b)
	if err != nil {
		t.Fatalf("no answer through the proxy: %v", err)
	}
	return string(b[:n])
}

func TestAUDPAssociationCarriesDatagramsBothWays(t *testing.T) {
	echo := echoUDP(t)
	srv := socks5test.Start(t)
	pc, err := proxiedGuard(srv.Addr).ListenPacket("download", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if got := roundTrip(t, pc, echo, "announce"); got != "echo:announce" {
		t.Errorf("answer %q, want echo:announce", got)
	}
	sent := srv.Sent()
	if len(sent) != 1 || sent[0].Addr != echo.String() {
		t.Errorf("the proxy relayed %+v, want one datagram to %s", sent, echo)
	}
}

func TestAnAliasReachesTheProxyAsTheTrackersName(t *testing.T) {
	echo := echoUDP(t)
	srv := socks5test.Start(t)
	srv.Names["tracker.example.invalid"] = echo.String()
	aliases := egress.NewAliases()
	ip := aliases.Alias("tracker.example.invalid")
	if !strings.HasPrefix(ip.String(), "127.88.") {
		t.Fatalf("alias %s is not a placeholder in 127.88.0.0/16", ip)
	}
	if again := aliases.Alias("tracker.example.invalid"); again != ip {
		t.Errorf("the same name got two aliases: %s and %s", ip, again)
	}
	pc, err := proxiedGuard(srv.Addr).ListenPacket("download", aliases)
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	to := &net.UDPAddr{IP: ip.AsSlice(), Port: 1337}
	if got := roundTrip(t, pc, to, "connect"); got != "echo:connect" {
		t.Errorf("answer %q, want echo:connect", got)
	}
	sent := srv.Sent()
	if len(sent) != 1 || sent[0].Domain != "tracker.example.invalid" || sent[0].Port != 1337 {
		t.Errorf("the proxy received %+v, want the name tracker.example.invalid:1337, unresolved", sent)
	}
}

func TestAProxyThatRefusesUDPIsAnError(t *testing.T) {
	srv := socks5test.Start(t)
	srv.RefuseUDP = true
	if _, err := proxiedGuard(srv.Addr).ListenPacket("download", nil); err == nil ||
		!strings.Contains(err.Error(), "command not supported") {
		t.Errorf("err = %v, want the proxy's refusal", err)
	}
}

func TestABlockedProfileOpensNoUDPSocket(t *testing.T) {
	if _, err := proxiedGuard("127.0.0.1:1").ListenPacket("blocked", nil); err == nil {
		t.Error("a blocked profile opened a UDP socket")
	}
	if _, err := proxiedGuard("127.0.0.1:1").ListenPacket("nonexistent", nil); err == nil {
		t.Error("a profile that does not exist opened a UDP socket")
	}
}

// With the kill switch engaged, no UDP socket is opened either.
func TestTheKillSwitchStopsUDPToo(t *testing.T) {
	srv := socks5test.Start(t)
	g := egress.New(egress.Config{Enforce: true, Profiles: map[string]egress.Profile{
		"download": {Mode: egress.ModeSOCKS5, Address: srv.Addr, Username: "u", Password: "p", RemoteDNS: true}}})
	if _, err := g.ListenPacket("download", nil); !errors.Is(err, egress.ErrEgressUnavailable) {
		t.Errorf("err = %v, want ErrEgressUnavailable while the tunnel is down", err)
	}
	g.SetHealthy(true, "up")
	pc, err := g.ListenPacket("download", nil)
	if err != nil {
		t.Fatalf("with the tunnel up: %v", err)
	}
	_ = pc.Close()
}

package config

import (
	"net"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/egress"
)

// withKey supplies the master key the lint requires, so these tests fail for
// the reason they are about rather than for a missing environment variable.
func withKey(string) string {
	return "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
}

// A probe target the dialer will always refuse is refused at boot instead.
//
// Found by configuring one. The guarded dialer blocks private, loopback and
// link-local destinations — the SSRF rule, which applies to the probe like
// everything else — so "192.168.1.1:443", an entirely natural choice for
// "somewhere I know I can reach", can never succeed. Since the downloader now
// EXITS when the probe fails, that setting turns it into a container that
// crash-loops forever while reporting a tunnel failure that is really this
// line of configuration.
func TestAPrivateProbeTargetIsRefusedAtBoot(t *testing.T) {
	for _, target := range []string{
		"192.168.1.1:443",    // the router
		"10.0.0.1:53",        // the local resolver
		"127.0.0.1:9999",     // loopback
		"172.17.0.2:9999",    // another container on the docker bridge
		"169.254.169.254:80", // link-local
		"100.64.0.1:443",     // CGNAT — the range a hand-written copy of the rule missed
	} {
		t.Run(target, func(t *testing.T) {
			cfg := Default()
			cfg.Egress.ProbeTarget = target

			err := Lint(cfg, withKey)
			if err == nil {
				t.Fatalf("probe_target %q was accepted; the dialer will refuse it "+
					"on every probe and the downloader will crash-loop", target)
			}
			if !strings.Contains(err.Error(), "probe_target") {
				t.Errorf("the complaint does not name the setting: %v", err)
			}
		})
	}
}

// A public target, and an empty one, are both fine. Without this the test above
// passes just as well against a lint that refuses everything.
func TestAPublicOrEmptyProbeTargetIsAccepted(t *testing.T) {
	for _, target := range []string{"", "1.1.1.1:443", "example.com:443", "[2606:4700::1111]:443"} {
		cfg := Default()
		cfg.Egress.ProbeTarget = target
		if err := Lint(cfg, withKey); err != nil {
			t.Errorf("probe_target %q was refused: %v", target, err)
		}
	}
}

// The lint and the dialer must agree about what is private.
//
// They are separate functions in separate packages and the first version of
// this check was a hand-written copy that had already drifted: it missed CGNAT
// and the TEST-NET ranges, so the lint would have accepted targets the dialer
// then refused — the exact disagreement the check exists to prevent. The copy
// is gone and the lint calls egress.IsRestricted; this makes that a fact rather
// than an intention.
func TestTheLintAgreesWithTheDialerAboutPrivateAddresses(t *testing.T) {
	for _, c := range []struct {
		ip         string
		restricted bool
	}{
		{"192.168.1.1", true}, {"10.0.0.1", true}, {"172.16.0.1", true},
		{"127.0.0.1", true}, {"169.254.1.1", true}, {"0.0.0.0", true},
		{"100.64.0.1", true},   // CGNAT
		{"192.0.2.1", true},    // TEST-NET-1
		{"198.51.100.1", true}, // TEST-NET-2
		{"203.0.113.1", true},  // TEST-NET-3
		{"::1", true}, {"fc00::1", true}, {"2001:db8::1", true},
		{"1.1.1.1", false}, {"8.8.8.8", false}, {"2606:4700::1111", false},
	} {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("%q is not an address", c.ip)
		}
		if got := egress.IsRestricted(ip); got != c.restricted {
			t.Errorf("egress.IsRestricted(%s) = %v, want %v", c.ip, got, c.restricted)
			continue
		}
		// And the lint reaches the same verdict through its own path.
		cfg := Default()
		cfg.Egress.ProbeTarget = net.JoinHostPort(c.ip, "443")
		err := Lint(cfg, withKey)
		refused := err != nil && strings.Contains(err.Error(), "probe_target")
		if refused != c.restricted {
			t.Errorf("the lint %s %s while the dialer says restricted=%v",
				map[bool]string{true: "refused", false: "accepted"}[refused],
				c.ip, c.restricted)
		}
	}
}

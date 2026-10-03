package egress

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// One private destination, allowed by name (ADR-0024).

// loopbackServer answers 200 on 127.0.0.1 and returns its port.
func loopbackServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return srv, port
}

func denyingGuard(t *testing.T) *Guard {
	return healthyGuard(t, map[string]Profile{"indexer": {Mode: ModeDirect, DenyPrivate: true}})
}

func get(c *http.Client, raw string) error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	return nil
}

// The operator's indexer on their own network is reachable — and nothing else
// private is, not even another port on the same machine.
func TestTheAllowedDestinationAndOnlyItIsReachable(t *testing.T) {
	_, port := loopbackServer(t)
	_, otherPort := loopbackServer(t)
	g := denyingGuard(t)

	c := g.HTTPClientAllowing("indexer", 5*time.Second, nil, DestinationKey("127.0.0.1", port))
	if err := get(c, "http://127.0.0.1:"+port+"/api"); err != nil {
		t.Fatalf("the allowed address was refused: %v", err)
	}
	if err := get(c, "http://127.0.0.1:"+otherPort+"/api"); !errors.Is(err, ErrPrivateAddress) {
		t.Errorf("another port on the same machine: err = %v, want ErrPrivateAddress", err)
	}

	// And a client built without the allowance reaches neither: the exemption
	// is this dialer's, not the profile's.
	plain := g.HTTPClient("indexer", 5*time.Second, nil)
	if err := get(plain, "http://127.0.0.1:"+port+"/api"); !errors.Is(err, ErrPrivateAddress) {
		t.Errorf("without the allowance: err = %v, want ErrPrivateAddress", err)
	}
}

// A hostname allowance is by name: the same machine under another spelling is
// not the address the operator wrote. Case and a trailing dot are the same name.
func TestAHostnameAllowanceIsByName(t *testing.T) {
	_, port := loopbackServer(t)
	g := denyingGuard(t)
	c := g.HTTPClientAllowing("indexer", 5*time.Second, nil, DestinationKey("localhost", port))

	for _, ok := range []string{"http://localhost:" + port, "http://LOCALHOST:" + port} {
		if err := get(c, ok+"/api"); err != nil {
			t.Errorf("%s was refused: %v", ok, err)
		}
	}
	if err := get(c, "http://127.0.0.1:"+port+"/api"); !errors.Is(err, ErrPrivateAddress) {
		t.Errorf("the same machine by address: err = %v; the allowance was for the NAME", err)
	}
}

// Link-local is refused even for the configured address: it is where cloud
// metadata endpoints live. Refused before any connection is attempted.
func TestLinkLocalIsRefusedEvenWhenItIsTheConfiguredAddress(t *testing.T) {
	g := denyingGuard(t)
	for _, host := range []string{"169.254.169.254", "fe80::1"} {
		dest := DestinationKey(host, "80")
		c := g.HTTPClientAllowing("indexer", 2*time.Second, nil, dest)
		err := get(c, "http://"+net.JoinHostPort(host, "80")+"/latest/meta-data/")
		if !errors.Is(err, ErrPrivateAddress) {
			t.Errorf("%s: err = %v, want ErrPrivateAddress", host, err)
		}
	}
}

// The networks an operator runs services on, and the ones they do not.
func TestWhatCountsAsTheOperatorsNetwork(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.16.5.4", "192.168.1.10",
		"fd00::10", "100.100.1.1"} {
		if !OnOperatorNetwork(net.ParseIP(s)) {
			t.Errorf("%s is somewhere an operator runs services", s)
		}
	}
	for _, s := range []string{"169.254.169.254", "fe80::1", "0.0.0.0", "::", "224.0.0.1",
		"ff02::1", "192.0.2.1", "8.8.8.8"} {
		if OnOperatorNetwork(net.ParseIP(s)) {
			t.Errorf("%s was counted as the operator's network", s)
		}
	}
}

// The key is what the transport dials: host folded, port filled in.
func TestADestinationIsWrittenTheWayTheTransportDialsIt(t *testing.T) {
	for raw, want := range map[string]string{
		"http://Prowlarr:9696/api":  "prowlarr:9696",
		"http://prowlarr./api":      "prowlarr:80",
		"https://jackett.lan/":      "jackett.lan:443",
		"http://[FD00::10]:9696/":   "[fd00::10]:9696",
		"http://192.168.1.10:9696/": "192.168.1.10:9696",
	} {
		u, _ := url.Parse(raw)
		got, err := DestinationOf(u)
		if err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"ftp://x.lan/", "http:///nohost"} {
		u, _ := url.Parse(raw)
		if got, err := DestinationOf(u); err == nil {
			t.Errorf("%s: %q accepted", raw, got)
		}
	}
}

// An allowance is created in one place. A second caller would be a second
// route to the operator's private network that nobody reviewed as one.
func TestOnlyTheIndexerClientMayAllowAPrivateDestination(t *testing.T) {
	root := filepath.Join("..", "..")
	var callers []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if name := info.Name(); name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, "HTTPClientAllowing(") && !strings.Contains(line, "func (g *Guard)") {
				callers = append(callers, filepath.ToSlash(path)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var outside []string
	for _, c := range callers {
		if !strings.Contains(c, "internal/indexer/") {
			outside = append(outside, c)
		}
	}
	if len(outside) > 0 {
		t.Errorf("HTTPClientAllowing is called outside internal/indexer:\n  %s", strings.Join(outside, "\n  "))
	}
	if len(callers) == 0 {
		t.Error("no caller found at all; this test is reading the wrong tree")
	}
}

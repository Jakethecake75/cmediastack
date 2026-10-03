package indexer

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/egress"
)

// An indexer on the operator's own network (ADR-0024), through the production
// client: a real egress guard with DenyPrivate on, real listeners on loopback —
// which is a private address like any other, and so exactly the case.

const lanTorrent = "d8:announce20:http://tracker.local4:infod6:lengthi1024e4:name8:file.bin12:piece lengthi16384eee"

// lanIndexer is a Torznab endpoint on loopback. Its feed offers one release
// whose link is on the indexer itself, as Prowlarr's and Jackett's are, and one
// whose link is on some OTHER private address.
func lanIndexer(t *testing.T, elsewhere string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api":
			w.Header().Set("Content-Type", "application/rss+xml")
			fmt.Fprintf(w, `<?xml version="1.0"?><rss version="2.0"><channel>
<item><title>Own.Link.2019.1080p.BluRay.x264-GRP</title>
<enclosure url="%s/dl/own.torrent" length="1024"/></item>
<item><title>Other.Link.2019.1080p.BluRay.x264-GRP</title>
<enclosure url="%s/dl/other.torrent" length="1024"/></item>
</channel></rss>`, srv.URL, elsewhere)
		case "/dl/own.torrent":
			_, _ = w.Write([]byte(lanTorrent))
		case "/dl/away":
			http.Redirect(w, r, elsewhere+"/dl/other.torrent", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// somewhereElse is another private service: a different port on the same host.
func somewhereElse(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(lanTorrent))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func productionClient(t *testing.T) *Client {
	t.Helper()
	g := egress.New(egress.Config{
		Profiles: map[string]egress.Profile{ProfileName: {Mode: egress.ModeDirect, DenyPrivate: true}},
	})
	g.SetHealthy(true, "test")
	return NewClient(g)
}

func lanDefinition(base string) Definition {
	return Definition{ID: 7, Name: "Prowlarr", Kind: KindTorznab, BaseURL: base, APIKey: "k", Enabled: true}
}

// The whole point: a Prowlarr on the operator's network is searched, and its
// own download links are fetched.
func TestAnIndexerOnTheOperatorsNetworkWorks(t *testing.T) {
	other := somewhereElse(t)
	idx := lanIndexer(t, other.URL)
	c := productionClient(t)
	d := lanDefinition(idx.URL)

	res, err := c.Search(t.Context(), d, emptyQuery())
	if err != nil {
		t.Fatalf("searching an indexer on the operator's network: %v", err)
	}
	var titles []string
	for _, r := range res {
		titles = append(titles, r.Title)
	}
	if len(res) != 1 || res[0].Title != "Own.Link.2019.1080p.BluRay.x264-GRP" {
		t.Fatalf("results = %v; the release linked on the indexer itself should be kept and the "+
			"one linked to another private address dropped", titles)
	}

	p, err := c.Download(t.Context(), d, res[0].DownloadURL)
	if err != nil {
		t.Fatalf("fetching the indexer's own download link: %v", err)
	}
	if string(p.Torrent) != lanTorrent {
		t.Errorf("torrent = %q", p.Torrent)
	}
}

// A feed's link to any OTHER private address is refused — the exemption is the
// operator's value, and a feed chose this one.
func TestALinkToAnotherPrivateAddressIsStillRefused(t *testing.T) {
	other := somewhereElse(t)
	idx := lanIndexer(t, other.URL)
	c := productionClient(t)

	_, err := c.Download(t.Context(), lanDefinition(idx.URL), other.URL+"/dl/other.torrent")
	if !errors.Is(err, ErrUnsafeURL) {
		t.Errorf("err = %v, want ErrUnsafeURL for another port on the same machine", err)
	}
}

// A redirect FROM the indexer's own address to another private one is refused
// at the redirect — the second of the two checks ADR-0013 keeps.
func TestARedirectAwayFromTheIndexerIsRefused(t *testing.T) {
	other := somewhereElse(t)
	idx := lanIndexer(t, other.URL)
	c := productionClient(t)

	_, err := c.Download(t.Context(), lanDefinition(idx.URL), idx.URL+"/dl/away")
	if err == nil {
		t.Fatal("a redirect from the indexer to another private address was followed")
	}
	// Refused by the redirect check, naming the address — not by some
	// incidental failure further along that would pass this test anyway.
	if !errors.Is(err, ErrUnsafeURL) || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("refused, but not by the redirect check: %v", err)
	}
	t.Logf("refused as: %v", err)
}

// One indexer's allowance is not another's. Two Prowlarrs on the LAN: a link in
// the first one's feed to the second one's address is a link to a private
// address the first indexer's operator-written value does not name.
func TestOneIndexersAllowanceIsNotAnothers(t *testing.T) {
	second := somewhereElse(t)
	first := lanIndexer(t, second.URL)
	c := productionClient(t)

	if _, err := c.Download(t.Context(), lanDefinition(first.URL), second.URL+"/dl/other.torrent"); err == nil {
		t.Error("the first indexer's feed reached the second indexer's address")
	}
	// ...while the second, asked about itself, is reachable.
	if _, err := c.Download(t.Context(), lanDefinition(second.URL), second.URL+"/dl/x.torrent"); err != nil {
		t.Errorf("the second indexer's own address was refused: %v", err)
	}
}

// Without the change in ADR-0024 none of this worked; the base rule alone still
// refuses loopback, which is what keeps every OTHER private address closed.
func TestTheBaseRuleStillRefusesPrivateAddresses(t *testing.T) {
	idx := lanIndexer(t, "http://127.0.0.1:1")
	c := productionClient(t)
	d := lanDefinition("https://indexer.example.com")
	if _, err := c.Download(t.Context(), d, idx.URL+"/dl/own.torrent"); !errors.Is(err, ErrUnsafeURL) {
		t.Errorf("a public indexer's feed reached loopback: err = %v", err)
	}
}

// Saving an indexer at a link-local address is refused on the form; the
// operator's own network is accepted.
func TestAnIndexerAddressIsCheckedWhenSaved(t *testing.T) {
	for _, bad := range []string{"http://169.254.169.254/", "http://[fe80::1]:9696", "http://224.0.0.1:9696", "http://0.0.0.0:9696"} {
		if err := lanDefinition(bad).Validate(); err == nil {
			t.Errorf("%s was accepted as an indexer's address", bad)
		}
	}
	for _, ok := range []string{"http://192.168.1.10:9696", "http://localhost:9117", "http://prowlarr:9696",
		"http://[fd00::10]:9696", "http://100.101.102.103:9696", "https://indexer.example.com"} {
		if err := lanDefinition(ok).Validate(); err != nil {
			t.Errorf("%s was refused: %v", ok, err)
		}
	}
}

// The pre-flight half of the rule refuses a link-local literal even when it is
// the indexer's own configured address — the dialer would refuse it too, and
// ADR-0013 keeps two checks precisely so that neither is the only one.
func TestTheIndexersOwnAddressIsStillNotLinkLocal(t *testing.T) {
	c := productionClient(t)
	for _, base := range []string{"http://169.254.169.254", "http://[fe80::1]:9696"} {
		d := lanDefinition(base)
		if err := c.validatorFor(d)(mustURL(t, base+"/api")); !errors.Is(err, ErrUnsafeURL) {
			t.Errorf("%s: err = %v, want ErrUnsafeURL", base, err)
		}
	}
	if err := c.validatorFor(lanDefinition("http://192.168.1.10:9696"))(mustURL(t, "http://192.168.1.10:9696/api")); err != nil {
		t.Errorf("the operator's own LAN address was refused: %v", err)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

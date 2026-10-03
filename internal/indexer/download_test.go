package indexer

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/egress"
)

// bencodeTorrent is the smallest thing that starts like a real one.
const bencodeTorrent = "d8:announce20:http://tracker.local4:infod6:lengthi1024e4:name8:file.bin12:piece lengthi16384eee"

func TestAMagnetInTheFeedIsNeverFetched(t *testing.T) {
	var called bool
	c := NewClientWithDoer(doerFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return respond(200, ""), nil
	}))

	magnet := "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&dn=x"
	p, err := c.Download(t.Context(), testIndexer(), magnet)
	if err != nil {
		t.Fatal(err)
	}
	if p.Magnet != magnet {
		t.Errorf("magnet = %q", p.Magnet)
	}
	if len(p.Torrent) != 0 {
		t.Errorf("a magnet produced torrent bytes")
	}
	// The point: no request left the instance at all.
	if called {
		t.Error("a magnet link caused an outbound request")
	}
}

func TestATorrentFileIsReturnedAsBytes(t *testing.T) {
	c := serving(200, bencodeTorrent, nil)

	p, err := c.Download(t.Context(), testIndexer(), "https://indexer.example.com/dl/1.torrent")
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Torrent) != bencodeTorrent {
		t.Errorf("torrent = %q", p.Torrent)
	}
	if p.Magnet != "" {
		t.Errorf("a torrent produced a magnet: %q", p.Magnet)
	}
}

// Several indexers answer a .torrent link with a redirect to a magnet URI. No
// HTTP client can follow that, and the URL validator refuses it by scheme — so
// without special handling a completely ordinary indexer reads as "refused an
// unsafe URL". This runs against a real server and a real http.Client, because
// the behaviour being relied on is net/http's, not ours: a CheckRedirect
// refusal returns the unfollowed 3xx AND an error.
func TestARedirectToAMagnetIsReadRatherThanRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	c := loopbackClient()

	p, err := c.Download(t.Context(), testIndexer(), srv.URL+"/dl/1.torrent")
	if err != nil {
		t.Fatalf("a redirect to a magnet was treated as a failure: %v", err)
	}
	if !strings.HasPrefix(p.Magnet, "magnet:") {
		t.Errorf("magnet = %q", p.Magnet)
	}
}

// The same machinery must NOT turn a redirect to somewhere dangerous into a
// success. A magnet is followed because it is inert; an http redirect to link
// local address space is the attack the validator exists for.
func TestARedirectToLinkLocalIsStillRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()

	c := loopbackClient()

	p, err := c.Download(t.Context(), testIndexer(), srv.URL+"/dl/1.torrent")
	if err == nil {
		t.Fatalf("a redirect to 169.254.169.254 succeeded: %+v", p)
	}
	if !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("error = %v, want ErrUnsafeURL", err)
	}
	// Named explicitly. Otherwise this passes when the test server's own
	// loopback origin is refused and the redirect is never followed at all,
	// which is exactly how it passed before the validator was fixed.
	if !strings.Contains(err.Error(), "169.254.169.254") {
		t.Errorf("the refusal does not name the redirect target: %v", err)
	}
}

func TestAnErrorPageWithHTTP200IsNotATorrent(t *testing.T) {
	c := serving(200, "<html><body>Invalid API key</body></html>", nil)

	_, err := c.Download(t.Context(), testIndexer(), "https://indexer.example.com/dl/1.torrent")
	if err == nil {
		t.Fatal("an HTML error page was accepted as a torrent")
	}
	if !errors.Is(err, ErrIndexerRefused) && !errors.Is(err, ErrMalformed) {
		t.Errorf("error = %v", err)
	}
}

func TestAnOversizedTorrentIsRefused(t *testing.T) {
	big := "d" + strings.Repeat("x", MaxTorrentBytes+64)
	c := serving(200, big, nil)

	_, err := c.Download(t.Context(), testIndexer(), "https://indexer.example.com/dl/1.torrent")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("error = %v, want ErrResponseTooLarge", err)
	}
}

func TestAnOversizedMagnetIsRefused(t *testing.T) {
	c := serving(200, "", nil)
	huge := "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&tr=" +
		strings.Repeat("a", MaxMagnetBytes)

	_, err := c.Download(t.Context(), testIndexer(), huge)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("error = %v, want ErrResponseTooLarge", err)
	}
}

func TestAMagnetWithNoInfoHashIsRefused(t *testing.T) {
	c := serving(200, "", nil)

	_, err := c.Download(t.Context(), testIndexer(), "magnet:?dn=a-display-name-only")
	if !errors.Is(err, ErrMalformed) {
		t.Errorf("error = %v, want ErrMalformed", err)
	}
}

// The download path must refuse the same shapes the search path does. A sealed
// ticket proves the URL was not changed by the client; it says nothing about
// whether the indexer chose a safe one.
func TestTheDownloadPathRefusesUnsafeURLs(t *testing.T) {
	c := serving(200, bencodeTorrent, nil)

	for _, bad := range []string{
		"file:///etc/passwd",
		"http://127.0.0.1:8080/x.torrent",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/x.torrent",
		"gopher://indexer.example.com/x",
		"http:///no-host",
	} {
		if _, err := c.Download(t.Context(), testIndexer(), bad); !errors.Is(err, ErrUnsafeURL) {
			t.Errorf("Download(%q) error = %v, want ErrUnsafeURL", bad, err)
		}
	}
}

func TestAnEmptyBodyIsNotATorrent(t *testing.T) {
	c := serving(200, "", nil)
	if _, err := c.Download(t.Context(), testIndexer(), "https://indexer.example.com/dl/1.torrent"); !errors.Is(err, ErrMalformed) {
		t.Errorf("error = %v, want ErrMalformed", err)
	}
}

func TestDownloadMapsIndexerStatusCodes(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, ErrIndexerAuth},
		{http.StatusForbidden, ErrIndexerAuth},
		{http.StatusTooManyRequests, ErrIndexerRefused},
		{http.StatusInternalServerError, ErrIndexerRefused},
		{http.StatusNotFound, ErrIndexerRefused},
	} {
		c := serving(tc.status, "nope", nil)
		_, err := c.Download(t.Context(), testIndexer(), "https://indexer.example.com/dl/1.torrent")
		if !errors.Is(err, tc.want) {
			t.Errorf("HTTP %d: error = %v, want %v", tc.status, err, tc.want)
		}
	}
}

// loopbackClient builds the same http.Client production uses — guarded dialer,
// validating CheckRedirect — but with a validator that permits loopback, which
// is where httptest listens. The redirect semantics under test belong to
// net/http, so a hand-rolled Doer would not exercise them.
func loopbackClient() *Client {
	g := egress.New(egress.Config{
		Profiles: map[string]egress.Profile{
			ProfileName: {Mode: egress.ModeDirect, DenyPrivate: false},
		},
	})
	g.SetHealthy(true, "test")
	c := NewClientWithDoer(g.HTTPClient(ProfileName, DefaultTimeout, testValidateURL))
	// The same validator on both halves, which is the invariant the Client
	// exists to hold.
	c.validate = testValidateURL
	return c
}

// testValidateURL mirrors validateRequestURL, exempting loopback so the test
// server is reachable at all. Everything else it refuses identically — link
// local above all, since that is precisely what the redirect tests are about.
// Without the exemption those tests pass by refusing the test server's own
// address, and never exercise a redirect.
func testValidateURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q", ErrUnsafeURL, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: no host", ErrUnsafeURL)
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	if ip != nil && egress.IsRestricted(ip) {
		return fmt.Errorf("%w: %s is not a routable public address", ErrUnsafeURL, ip)
	}
	return nil
}

// A magnet URI is a legitimate download link and many indexers publish nothing
// else. Judging one by the fetch path's http(s)-only rule discards every
// result the indexer returned — silently, as an empty search rather than an
// error, which is the worst way for this to fail.
func TestAMagnetOnlyIndexerStillReturnsResults(t *testing.T) {
	feed := `<?xml version="1.0"?><rss version="2.0"><channel>
<item><title>Some.Movie.2019.1080p.BluRay-GRP</title>
<enclosure url="magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&amp;dn=x" length="1024"/>
</item></channel></rss>`

	res, err := serving(200, feed, nil).Search(t.Context(), testIndexer(), emptyQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("results = %d, want 1: a magnet-only indexer returned nothing", len(res))
	}
	if !strings.HasPrefix(res[0].DownloadURL, "magnet:") {
		t.Errorf("download link = %q", res[0].DownloadURL)
	}
}

// A malformed magnet is still refused. Accepting magnets is not accepting
// anything that starts with the word.
func TestAMalformedMagnetIsStillDroppedFromAFeed(t *testing.T) {
	feed := `<?xml version="1.0"?><rss version="2.0"><channel>
<item><title>Some.Movie.2019.1080p.BluRay-GRP</title>
<enclosure url="magnet:?dn=no-info-hash-here" length="1024"/>
</item></channel></rss>`

	res, err := serving(200, feed, nil).Search(t.Context(), testIndexer(), emptyQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 0 {
		t.Errorf("a magnet with no info hash survived: %+v", res)
	}
}

// The pre-flight check and the redirect check must be one function. Two copies
// drift, and a pre-flight that refuses link-local while the redirect check
// permits it is worse than neither, because it reads as covered.
func TestTheClientHasExactlyOneURLValidator(t *testing.T) {
	for name, c := range map[string]*Client{
		"NewClient(nil)":     NewClient(nil),
		"NewClientWithDoer":  NewClientWithDoer(nil),
		"NewClient(a guard)": NewClient(egress.New(egress.Config{})),
	} {
		if c.validate == nil {
			t.Errorf("%s: the client has no URL validator, so Download would panic", name)
		}
	}
}

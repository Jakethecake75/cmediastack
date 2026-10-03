package indexer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// doerFunc answers requests without a network, so these tests exercise the
// parsing and the refusals rather than an indexer somebody has to be running.
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func respond(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{},
	}
}

// serving answers every request with one body and records the URL it was asked
// for, which is how the query-building tests see what was sent.
func serving(status int, body string, seen *url.URL) *Client {
	return NewClientWithDoer(doerFunc(func(r *http.Request) (*http.Response, error) {
		if seen != nil {
			*seen = *r.URL
		}
		return respond(status, body), nil
	}))
}

func testIndexer() Definition {
	return Definition{
		ID: 1, Name: "Test", Kind: KindTorznab,
		BaseURL: "https://indexer.example.com", APIKey: "super-secret-key",
		Enabled: true,
	}
}

func emptyQuery() Query { return Query{Season: -1} }

const goodFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
<channel>
  <item>
    <title>The.Matrix.1999.1080p.BluRay.x264-SWTYBLZ</title>
    <guid>abc123</guid>
    <comments>https://indexer.example.com/details/abc123</comments>
    <pubDate>Mon, 02 Jan 2023 15:04:05 -0700</pubDate>
    <size>8589934592</size>
    <enclosure url="https://indexer.example.com/dl/abc123.torrent" length="8589934592" type="application/x-bittorrent"/>
    <attr name="category" value="2040"/>
    <attr name="seeders" value="42"/>
    <attr name="peers" value="50"/>
    <attr name="infohash" value="0123456789abcdef0123456789ABCDEF01234567"/>
  </item>
  <item>
    <title>Breaking.Bad.S05E14.1080p.BluRay.x264-DEMAND</title>
    <guid>def456</guid>
    <pubDate>Tue, 03 Jan 2023 10:00:00 -0000</pubDate>
    <enclosure url="https://indexer.example.com/dl/def456.torrent" length="2147483648"/>
    <attr name="seeders" value="7"/>
  </item>
</channel>
</rss>`

func TestSearchParsesAFeed(t *testing.T) {
	c := serving(http.StatusOK, goodFeed, nil)

	results, err := c.Search(context.Background(), testIndexer(), emptyQuery())
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	first := results[0]
	if first.Title != "The.Matrix.1999.1080p.BluRay.x264-SWTYBLZ" {
		t.Errorf("title = %q", first.Title)
	}
	if first.Size != 8589934592 {
		t.Errorf("size = %d", first.Size)
	}
	if first.Seeders != 42 {
		t.Errorf("seeders = %d, want 42", first.Seeders)
	}
	// "peers" is seeders + leechers, so leechers is the difference.
	if first.Leechers != 8 {
		t.Errorf("leechers = %d, want 8 (peers 50 minus seeders 42)", first.Leechers)
	}
	if first.InfoHash != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("infohash = %q, want it normalised to lowercase", first.InfoHash)
	}
	if first.PublishedAt.IsZero() {
		t.Error("the publication date was not parsed")
	}
	if first.IndexerName != "Test" || first.IndexerID != 1 {
		t.Error("the result does not say which indexer it came from")
	}

	// The release name is parsed once, here, so everything downstream agrees.
	if first.Parsed.Title != "The Matrix" || first.Parsed.Year != 1999 {
		t.Errorf("parsed = %+v", first.Parsed)
	}
	if !results[1].Parsed.IsEpisode() {
		t.Error("the episode was not recognised as one")
	}
}

// ---------------------------------------------------------------------------
// XML attacks
// ---------------------------------------------------------------------------

// Go's encoding/xml is claimed to resolve no external entities. That is a claim
// about somebody else's code, which is exactly the sort of claim that should
// not be taken on trust — so it is tested against a real payload.
func TestXXEIsImpossible(t *testing.T) {
	payloads := map[string]string{
		"file read": `<?xml version="1.0"?>
<!DOCTYPE rss [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>
<rss version="2.0"><channel><item><title>&xxe;</title>
<enclosure url="https://example.com/a.torrent"/></item></channel></rss>`,

		"http fetch": `<?xml version="1.0"?>
<!DOCTYPE rss [<!ENTITY xxe SYSTEM "http://169.254.169.254/latest/meta-data/">]>
<rss version="2.0"><channel><item><title>&xxe;</title>
<enclosure url="https://example.com/a.torrent"/></item></channel></rss>`,

		"parameter entity": `<?xml version="1.0"?>
<!DOCTYPE rss [<!ENTITY % p SYSTEM "file:///etc/passwd"> %p;]>
<rss version="2.0"><channel><item><title>hi</title>
<enclosure url="https://example.com/a.torrent"/></item></channel></rss>`,
	}

	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			c := serving(http.StatusOK, payload, nil)
			results, err := c.Search(context.Background(), testIndexer(), emptyQuery())

			// Either outcome is acceptable — refusing the document, or parsing
			// it with the entity unresolved. What is NOT acceptable is the
			// entity's target appearing in the output.
			if err != nil {
				if !errors.Is(err, ErrMalformed) {
					t.Errorf("failed for an unexpected reason: %v", err)
				}
				return
			}
			for _, r := range results {
				for _, leak := range []string{"root:", "/bin/", "ami-", "meta-data"} {
					if strings.Contains(r.Title, leak) {
						t.Fatalf("AN ENTITY WAS RESOLVED: title contains %q: %q", leak, r.Title)
					}
				}
			}
		})
	}
}

// The billion-laughs expansion turns a few hundred bytes into gigabytes in
// parsers that expand internal entities. Go does not, and the parse must finish
// rather than consuming memory until something dies.
func TestBillionLaughsIsImpossible(t *testing.T) {
	payload := `<?xml version="1.0"?>
<!DOCTYPE rss [
<!ENTITY a "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa">
<!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;">
<!ENTITY c "&b;&b;&b;&b;&b;&b;&b;&b;&b;&b;">
<!ENTITY d "&c;&c;&c;&c;&c;&c;&c;&c;&c;&c;">
<!ENTITY e "&d;&d;&d;&d;&d;&d;&d;&d;&d;&d;">
<!ENTITY f "&e;&e;&e;&e;&e;&e;&e;&e;&e;&e;">
]>
<rss version="2.0"><channel><item><title>&f;</title>
<enclosure url="https://example.com/a.torrent"/></item></channel></rss>`

	c := serving(http.StatusOK, payload, nil)

	done := make(chan struct{})
	var results []Result
	var err error
	go func() {
		results, err = c.Search(context.Background(), testIndexer(), emptyQuery())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the parse did not finish: entities were expanded")
	}

	if err == nil {
		for _, r := range results {
			if len(r.Title) > 100_000 {
				t.Fatalf("an entity expanded to %d bytes", len(r.Title))
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Resource limits
// ---------------------------------------------------------------------------

func TestOversizedResponsesAreRefused(t *testing.T) {
	huge := strings.Repeat("A", MaxResponseBytes+1024)
	c := NewClientWithDoer(doerFunc(func(*http.Request) (*http.Response, error) {
		return respond(http.StatusOK, huge), nil
	}))

	_, err := c.Search(context.Background(), testIndexer(), emptyQuery())
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("got %v, want ErrResponseTooLarge", err)
	}
}

func TestResultCountIsCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel>`)
	for i := 0; i < MaxResults+500; i++ {
		fmt.Fprintf(&b, `<item><title>Film.%d.2020.1080p.BluRay.x264-G</title>`+
			`<enclosure url="https://example.com/%d.torrent"/></item>`, i, i)
	}
	b.WriteString(`</channel></rss>`)

	c := serving(http.StatusOK, b.String(), nil)
	results, err := c.Search(context.Background(), testIndexer(), emptyQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != MaxResults {
		t.Errorf("got %d results, want the cap of %d", len(results), MaxResults)
	}
}

func TestAbsurdCountsAreClamped(t *testing.T) {
	payload := `<?xml version="1.0"?><rss version="2.0"><channel><item>
<title>Film.2020.1080p.BluRay.x264-G</title>
<enclosure url="https://example.com/a.torrent"/>
<attr name="seeders" value="99999999999999999999"/>
<attr name="grabs" value="-5"/>
</item></channel></rss>`

	c := serving(http.StatusOK, payload, nil)
	results, err := c.Search(context.Background(), testIndexer(), emptyQuery())
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Seeders < 0 || results[0].Seeders > 10_000_000 {
		t.Errorf("seeders = %d, want it clamped", results[0].Seeders)
	}
	if results[0].Grabs < 0 {
		t.Errorf("grabs = %d, want no negative", results[0].Grabs)
	}
}

// ---------------------------------------------------------------------------
// Hostile URLs
// ---------------------------------------------------------------------------

// The download URL comes from the uploader. Pointing it at the cloud metadata
// service costs them nothing and would be fetched on a schedule once it reached
// the queue.
func TestResultsWithUnsafeDownloadURLsAreDropped(t *testing.T) {
	payload := `<?xml version="1.0"?><rss version="2.0"><channel>
<item><title>Evil.2020.1080p.BluRay.x264-G</title>
  <enclosure url="http://169.254.169.254/latest/meta-data/iam/"/></item>
<item><title>Evil2.2020.1080p.BluRay.x264-G</title>
  <enclosure url="file:///etc/passwd"/></item>
<item><title>Evil3.2020.1080p.BluRay.x264-G</title>
  <enclosure url="http://127.0.0.1:8080/admin"/></item>
<item><title>Evil4.2020.1080p.BluRay.x264-G</title>
  <enclosure url="gopher://evil.example.com/"/></item>
<item><title>Fine.2020.1080p.BluRay.x264-G</title>
  <enclosure url="https://indexer.example.com/dl/ok.torrent"/></item>
</channel></rss>`

	c := serving(http.StatusOK, payload, nil)
	results, err := c.Search(context.Background(), testIndexer(), emptyQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		for _, r := range results {
			t.Logf("kept: %s -> %s", r.Title, r.DownloadURL)
		}
		t.Fatalf("kept %d results, want only the safe one", len(results))
	}
	if !strings.HasPrefix(results[0].Title, "Fine.") {
		t.Errorf("kept the wrong one: %s", results[0].Title)
	}
}

func TestIndexerURLsAreValidatedWhenSaved(t *testing.T) {
	for name, d := range map[string]Definition{
		"no name":       {Kind: KindTorznab, BaseURL: "https://x.example.com"},
		"bad kind":      {Name: "x", Kind: "magic", BaseURL: "https://x.example.com"},
		"file scheme":   {Name: "x", Kind: KindTorznab, BaseURL: "file:///etc/passwd"},
		"gopher scheme": {Name: "x", Kind: KindTorznab, BaseURL: "gopher://x.example.com"},
		"no host":       {Name: "x", Kind: KindTorznab, BaseURL: "https://"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := d.Validate(); err == nil {
				t.Error("accepted")
			}
		})
	}

	good := testIndexer()
	if err := good.Validate(); err != nil {
		t.Errorf("a reasonable indexer was rejected: %v", err)
	}
}

// A redirect is a URL the far end chose AFTER the request was made, so it has
// had no validation unless it is checked at redirect time.
func TestRedirectTargetsAreValidated(t *testing.T) {
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1/admin",
		"file:///etc/passwd",
	} {
		u, err := url.Parse(target)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateRequestURL(u); err == nil {
			t.Errorf("%s was accepted as a redirect target", target)
		}
	}
	u, _ := url.Parse("https://indexer.example.com/api?t=search")
	if err := validateRequestURL(u); err != nil {
		t.Errorf("an ordinary URL was rejected: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The API key
// ---------------------------------------------------------------------------

// The protocol puts the API key in the query string, so it is in the indexer's
// access log whatever we do. What must not happen is it also being in OUR logs,
// which are the least-guarded copy of anything.
func TestErrorsNeverCarryTheAPIKey(t *testing.T) {
	const key = "this-is-the-secret-api-key"
	d := testIndexer()
	d.APIKey = key

	// A transport error: the *url.Error wraps the full URL, key and all.
	transportFail := NewClientWithDoer(doerFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: r.URL.String(), Err: errors.New("connection refused")}
	}))
	_, err := transportFail.Search(context.Background(), d, emptyQuery())
	if err == nil {
		t.Fatal("no error")
	}
	if strings.Contains(err.Error(), key) {
		t.Errorf("THE API KEY LEAKED INTO AN ERROR: %v", err)
	}

	// And the redaction keeps the rest of the URL, or the message is useless.
	if !strings.Contains(err.Error(), "indexer.example.com") {
		t.Errorf("the error does not say which indexer failed: %v", err)
	}
	if !strings.Contains(err.Error(), "REDACTED") {
		t.Errorf("the error does not show that something was redacted: %v", err)
	}
}

func TestRedactURLRemovesEverySecretParameter(t *testing.T) {
	u, err := url.Parse("https://x.example.com/api?t=search&apikey=AAA&passkey=BBB&token=CCC&rss_key=DDD&q=matrix")
	if err != nil {
		t.Fatal(err)
	}
	got := redactURL(u)
	for _, secret := range []string{"AAA", "BBB", "CCC", "DDD"} {
		if strings.Contains(got, secret) {
			t.Errorf("%q survived redaction: %s", secret, got)
		}
	}
	if !strings.Contains(got, "q=matrix") {
		t.Errorf("redaction removed a useful parameter: %s", got)
	}
}

// ---------------------------------------------------------------------------
// Query building
// ---------------------------------------------------------------------------

func TestQueryBuilding(t *testing.T) {
	var seen url.URL

	t.Run("free text", func(t *testing.T) {
		c := serving(http.StatusOK, goodFeed, &seen)
		q := emptyQuery()
		q.Term = "the matrix"
		if _, err := c.Search(context.Background(), testIndexer(), q); err != nil {
			t.Fatal(err)
		}
		if got := seen.Query().Get("t"); got != "search" {
			t.Errorf("t = %q, want search", got)
		}
		if got := seen.Query().Get("q"); got != "the matrix" {
			t.Errorf("q = %q", got)
		}
		if !strings.HasSuffix(seen.Path, "/api") {
			t.Errorf("path = %q, want it to end in /api", seen.Path)
		}
	})

	t.Run("television", func(t *testing.T) {
		c := serving(http.StatusOK, goodFeed, &seen)
		if _, err := c.Search(context.Background(), testIndexer(), Query{
			Term: "breaking bad", Season: 5, Episode: 14,
		}); err != nil {
			t.Fatal(err)
		}
		v := seen.Query()
		if v.Get("t") != "tvsearch" {
			t.Errorf("t = %q, want tvsearch", v.Get("t"))
		}
		if v.Get("season") != "5" || v.Get("ep") != "14" {
			t.Errorf("season/ep = %q/%q", v.Get("season"), v.Get("ep"))
		}
	})

	t.Run("movie by imdb id", func(t *testing.T) {
		c := serving(http.StatusOK, goodFeed, &seen)
		q := emptyQuery()
		q.IMDBID = "tt0133093"
		if _, err := c.Search(context.Background(), testIndexer(), q); err != nil {
			t.Fatal(err)
		}
		v := seen.Query()
		if v.Get("t") != "movie" {
			t.Errorf("t = %q, want movie", v.Get("t"))
		}
		// The tt prefix is stripped: the protocol wants the bare number.
		if v.Get("imdbid") != "0133093" {
			t.Errorf("imdbid = %q", v.Get("imdbid"))
		}
	})

	t.Run("a limit is always sent", func(t *testing.T) {
		c := serving(http.StatusOK, goodFeed, &seen)
		if _, err := c.Search(context.Background(), testIndexer(), emptyQuery()); err != nil {
			t.Fatal(err)
		}
		if seen.Query().Get("limit") == "" {
			t.Error("no limit was sent, so the indexer chooses how much to return")
		}
	})
}

// ---------------------------------------------------------------------------
// Indexer errors
// ---------------------------------------------------------------------------

func TestIndexerErrorDocumentsAreUnderstood(t *testing.T) {
	c := serving(http.StatusOK,
		`<?xml version="1.0"?><error code="100" description="Incorrect user credentials"/>`, nil)

	_, err := c.Search(context.Background(), testIndexer(), emptyQuery())
	if !errors.Is(err, ErrIndexerAuth) {
		t.Errorf("got %v, want ErrIndexerAuth", err)
	}
	if !strings.Contains(err.Error(), "Incorrect user credentials") {
		t.Errorf("the indexer's own explanation was dropped: %v", err)
	}
}

func TestHTTPStatusesAreClassified(t *testing.T) {
	for status, wantErr := range map[int]error{
		http.StatusUnauthorized:        ErrIndexerAuth,
		http.StatusForbidden:           ErrIndexerAuth,
		http.StatusTooManyRequests:     ErrIndexerRefused,
		http.StatusInternalServerError: ErrIndexerRefused,
		http.StatusNotFound:            ErrIndexerRefused,
	} {
		c := serving(status, "", nil)
		_, err := c.Search(context.Background(), testIndexer(), emptyQuery())
		if !errors.Is(err, wantErr) {
			t.Errorf("HTTP %d gave %v, want %v", status, err, wantErr)
		}
	}
}

func TestMalformedFeedsAreRejectedNotHalfRead(t *testing.T) {
	for name, body := range map[string]string{
		"truncated":   `<?xml version="1.0"?><rss><channel><item><title>x`,
		"not xml":     `this is not xml at all`,
		"empty":       ``,
		"binary":      "\x00\x01\x02\x03\xff\xfe",
		"nested junk": `<?xml version="1.0"?><rss><channel><item><title><b>x</b></title></item>`,
	} {
		t.Run(name, func(t *testing.T) {
			c := serving(http.StatusOK, body, nil)
			if _, err := c.Search(context.Background(), testIndexer(), emptyQuery()); err == nil {
				t.Error("a malformed feed was accepted")
			}
		})
	}
}

// One bad item in a thousand-item feed must cost that item, not the search.
func TestOneBadItemDoesNotLoseTheFeed(t *testing.T) {
	payload := `<?xml version="1.0"?><rss version="2.0"><channel>
<item><title></title><enclosure url="https://example.com/a.torrent"/></item>
<item><title>No.Download.URL.2020.1080p-G</title></item>
<item><title>Fine.2020.1080p.BluRay.x264-G</title>
  <enclosure url="https://example.com/fine.torrent"/></item>
</channel></rss>`

	c := serving(http.StatusOK, payload, nil)
	results, err := c.Search(context.Background(), testIndexer(), emptyQuery())
	if err != nil {
		t.Fatalf("the whole feed was lost to two bad items: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("got %d results, want the one usable item", len(results))
	}
}

// An unparseable date must stay zero. "Now" would make an old release look new
// and sort it to the top of every list.
func TestUnparseableDatesStayZeroRatherThanBecomingNow(t *testing.T) {
	payload := `<?xml version="1.0"?><rss version="2.0"><channel><item>
<title>Film.2020.1080p.BluRay.x264-G</title>
<pubDate>whenever, really</pubDate>
<enclosure url="https://example.com/a.torrent"/>
</item></channel></rss>`

	c := serving(http.StatusOK, payload, nil)
	results, _ := c.Search(context.Background(), testIndexer(), emptyQuery())
	if len(results) != 1 {
		t.Fatal("expected one result")
	}
	if !results[0].PublishedAt.IsZero() {
		t.Errorf("an unparseable date became %v", results[0].PublishedAt)
	}
}

func TestPubDateFormatsSeenInTheWild(t *testing.T) {
	for _, s := range []string{
		"Mon, 02 Jan 2023 15:04:05 -0700",
		"Mon, 2 Jan 2023 15:04:05 -0700",
		"2023-01-02T15:04:05Z",
		"2023-01-02 15:04:05",
	} {
		if parsePubDate(s).IsZero() {
			t.Errorf("%q was not parsed", s)
		}
	}
}

func TestInfoHashesAreSanitised(t *testing.T) {
	for in, want := range map[string]string{
		"0123456789abcdef0123456789abcdef01234567": "0123456789abcdef0123456789abcdef01234567",
		"0123456789ABCDEF0123456789ABCDEF01234567": "0123456789abcdef0123456789abcdef01234567",
		"../../../etc/passwd":                      "",
		"short":                                    "",
		"0123456789abcdef0123456789abcdef0123456g": "",
		"": "",
	} {
		if got := sanitiseInfoHash(in); got != want {
			t.Errorf("sanitiseInfoHash(%q) = %q, want %q", in, got, want)
		}
	}
}

// A client built without a guard must refuse to connect, not fall back to the
// default transport.
func TestAClientWithNoGuardRefusesToConnect(t *testing.T) {
	c := NewClient(nil)
	_, err := c.Search(context.Background(), testIndexer(), emptyQuery())
	if err == nil {
		t.Fatal("a guardless client made a request")
	}
	if !strings.Contains(err.Error(), "refusing to connect") {
		t.Errorf("got %v", err)
	}
}

// The title is bounded before it reaches a database column or a UI.
func TestOverlongTitlesAreTruncated(t *testing.T) {
	long := strings.Repeat("A", MaxTitleBytes*3)
	payload := fmt.Sprintf(`<?xml version="1.0"?><rss version="2.0"><channel><item>
<title>%s</title><enclosure url="https://example.com/a.torrent"/>
</item></channel></rss>`, long)

	c := serving(http.StatusOK, payload, nil)
	results, err := c.Search(context.Background(), testIndexer(), emptyQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(results[0].Title) > MaxTitleBytes {
		t.Errorf("title is %d bytes, want it capped at %d", len(results[0].Title), MaxTitleBytes)
	}
}

// A gzip bomb must be bounded by the DECOMPRESSED size, which is what the cap
// is applied to. This checks the cap sits on the read rather than on
// Content-Length, which an indexer controls.
func TestTheSizeCapAppliesToTheDecompressedStream(t *testing.T) {
	body := bytes.Repeat([]byte("A"), MaxResponseBytes+4096)
	c := NewClientWithDoer(doerFunc(func(*http.Request) (*http.Response, error) {
		resp := respond(http.StatusOK, "")
		resp.Body = io.NopCloser(bytes.NewReader(body))
		// A small Content-Length, as a compressed response would report.
		resp.ContentLength = 1024
		resp.Header.Set("Content-Length", "1024")
		return resp, nil
	}))

	if _, err := c.Search(context.Background(), testIndexer(), emptyQuery()); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("got %v, want ErrResponseTooLarge — the cap trusted Content-Length", err)
	}
}

// ADR-0064: a daily series' episode is asked for by date, as season=YYYY and
// ep=MM/DD, and a malformed date falls back to the numbers.
func TestADailyEpisodeIsAskedForByDate(t *testing.T) {
	var seen url.URL
	c := serving(200, `<rss><channel></channel></rss>`, &seen)
	if _, err := c.Search(t.Context(), testIndexer(), Query{Term: "The Daily Show", Season: 30, Episode: 120,
		AirDate: "2026-10-02"}); err != nil {
		t.Fatal(err)
	}
	q := seen.Query()
	if q.Get("t") != "tvsearch" || q.Get("season") != "2026" || q.Get("ep") != "10/02" {
		t.Errorf("asked %s", seen.RawQuery)
	}
	if _, err := c.Search(t.Context(), testIndexer(), Query{Term: "x", Season: 30, Episode: 120,
		AirDate: "2026-13-45"}); err != nil {
		t.Fatal(err)
	}
	if q := seen.Query(); q.Get("season") != "30" || q.Get("ep") != "120" {
		t.Errorf("a bad date: %s", seen.RawQuery)
	}

	var asked string
	cg := NewClientWithDoer(doerFunc(func(r *http.Request) (*http.Response, error) {
		asked = r.URL.Path + "?" + r.URL.RawQuery
		return respond(http.StatusOK, "<html></html>"), nil
	}))
	def := strings.Replace(publicDefinition, `    fl: "{{ if .Config.freeleech }}1{{ end }}"`,
		`    fl: "{{ .Query.Season }}|{{ .Query.Ep }}"`, 1)
	if _, err := cg.Search(t.Context(), cardigannIndexer(def), Query{Term: "Daily Show", Season: 30, Episode: 120,
		AirDate: "2026-10-02"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(asked, "/search/Daily-Show-2026.10.02/") || !strings.Contains(asked, "fl=2026%7C10%2F02") {
		t.Errorf("asked %s", asked)
	}
}

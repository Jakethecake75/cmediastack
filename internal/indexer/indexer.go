// Package indexer talks to Torznab and Newznab indexers.
//
// # Everything here is hostile input
//
// An indexer is a third party the operator chose to trust for *search*, which
// is not the same as trusting it with the process. Its response is XML written
// by somebody else, containing URLs written by somebody else again — the
// uploader — and every field in it reaches a database, a UI and eventually a
// download client.
//
// So the client is built on four refusals, each of which exists because of a
// specific way this goes wrong:
//
//  1. **It cannot reach the network except through the egress guard.** The
//     transport's DialContext is the guard's, so an indexer query leaves by the
//     tunnel or it does not leave. TestNoPackageDialsDirectly fails the build
//     if anyone adds an http.Get here.
//
//  2. **Responses are size-capped and time-bounded before parsing.** An indexer
//     that answers with an endless stream is otherwise a memory-exhaustion
//     primitive that the operator invited in themselves.
//
//  3. **XML is parsed with no entity expansion and no DTD.** Go's
//     encoding/xml resolves no external entities and expands no internal ones,
//     so XXE and billion-laughs are structurally impossible rather than
//     defended against — and TestXXEIsImpossible asserts that rather than
//     trusting the claim.
//
//  4. **Every URL in a response is re-checked.** A result's download link is
//     attacker-controlled; pointing it at 127.0.0.1 or 169.254.169.254 is free.
//     The guard's DenyPrivate covers the dial, and Validate rejects the scheme
//     and the shape before anything gets that far.
package indexer

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jakethecake75/cmediastack/internal/egress"
	"github.com/jakethecake75/cmediastack/internal/release"
)

// Limits on what an indexer is allowed to do to us.
const (
	// MaxResponseBytes caps a single response. Real Torznab pages run to a few
	// hundred kilobytes; 8 MiB is generous and still bounded.
	MaxResponseBytes = 8 << 20
	// MaxResults caps how many items are kept from one response, so a hostile
	// or broken indexer cannot turn one search into a million rows.
	MaxResults = 1000
	// DefaultTimeout bounds a whole request including the body read.
	DefaultTimeout = 30 * time.Second
	// MaxTitleBytes is the longest title kept. Anything past this is not a
	// release name.
	MaxTitleBytes = 1024
)

// Errors callers distinguish.
var (
	ErrIndexerRefused   = errors.New("indexer: the indexer refused the request")
	ErrIndexerAuth      = errors.New("indexer: the indexer rejected the API key")
	ErrResponseTooLarge = errors.New("indexer: response exceeded the size cap")
	ErrMalformed        = errors.New("indexer: the response was not a valid feed")
	ErrUnsafeURL        = errors.New("indexer: refused an unsafe URL")
)

// Kind selects the protocol dialect. They are near-identical; the differences
// are which attributes carry the useful numbers.
type Kind string

const (
	// KindTorznab is the torrent dialect: seeders, peers, infohash.
	KindTorznab Kind = "torznab"
	// KindNewznab is the usenet dialect: no swarm, but grabs and age.
	KindNewznab Kind = "newznab"
)

// Definition is a configured indexer.
//
// APIKey is present only in memory and only while a request is in flight. It is
// stored encrypted (AES-256-GCM, context-bound — see internal/platform/secrets)
// and is never returned by the admin API or written to a log.
type Definition struct {
	ID         int64
	Name       string
	Kind       Kind
	BaseURL    string
	APIKey     string
	Categories []int
	Enabled    bool
	// Priority orders indexers when several answer. Lower is preferred.
	Priority int
	// SeedRatio and SeedTime are the tracker's requirements, carried here so
	// the download engine can honour them per-indexer in a later increment.
	SeedRatio float64
	SeedTime  time.Duration
	// Cardigann is the pasted definition of a KindCardigann indexer
	// (ADR-0058). Empty on an update means the stored one is kept.
	Cardigann string
	// Settings are the operator's values for the definition's settings,
	// sealed when stored and present only on the search path (ADR-0059).
	// Empty on an update keeps the stored ones.
	Settings map[string]string
	// HasSettings says, on the admin path, whether any are stored.
	HasSettings bool
}

// Validate checks a definition an operator has typed.
//
// It runs when the indexer is SAVED, not when it is used, so the operator finds
// out about a mistake while they are looking at the form.
func (d Definition) Validate() error {
	if strings.TrimSpace(d.Name) == "" {
		return errors.New("indexer: a name is required")
	}
	switch d.Kind {
	case KindTorznab, KindNewznab:
	case KindCardigann:
		if d.Cardigann != "" {
			def, err := parseCardigann(d.Cardigann)
			if err != nil {
				return err
			}
			if err := checkSettings(def, d.Settings); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("indexer: unknown kind %q (want torznab, newznab or cardigann)", d.Kind)
	}
	u, err := url.Parse(strings.TrimSpace(d.BaseURL))
	if err != nil {
		return fmt.Errorf("indexer: unparseable URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		// file:// and gopher:// are not indexers. An operator who pastes one
		// has made a mistake; an attacker who persuades them to has not.
		return fmt.Errorf("indexer: URL scheme must be http or https, not %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("indexer: URL has no host")
	}
	// An indexer may be on the operator's own network — a Prowlarr or Jackett
	// on the LAN or the same host is the ordinary case (ADR-0024). It may not
	// be at an address no indexer is legitimately at: link-local is where cloud
	// metadata services live. Said here, while the operator is looking at the
	// form, rather than on the first search.
	if ip := net.ParseIP(u.Hostname()); ip != nil && egress.IsRestricted(ip) && !egress.OnOperatorNetwork(ip) {
		return fmt.Errorf("indexer: %s cannot be an indexer's address: it is link-local, "+
			"multicast or reserved, not somewhere a service on your network runs", ip)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Results
// ---------------------------------------------------------------------------

// Result is one release an indexer offered.
type Result struct {
	Title       string
	GUID        string
	DownloadURL string
	InfoURL     string
	InfoHash    string
	Size        int64
	PublishedAt time.Time
	Seeders     int
	Leechers    int
	Grabs       int
	Categories  []int

	IndexerID   int64
	IndexerName string

	// Parsed is the release name broken down. It is computed here so that
	// everything downstream works from the same parse rather than re-deriving
	// it, and so a result that cannot be understood is visible immediately.
	Parsed release.Parsed
}

// Query is a search request.
type Query struct {
	// Term is the free-text search. Empty means "everything", which is what an
	// RSS sync does.
	Term string
	// Categories restricts the search. Empty means the indexer's default.
	Categories []int
	// Season and Episode are set for a television search; -1 means unset.
	Season  int
	Episode int
	// AirDate, YYYY-MM-DD, asks for a daily series' episode by date instead
	// of by number (ADR-0064).
	AirDate string
	// IMDBID and TVDBID let an indexer match precisely rather than by string.
	IMDBID string
	TVDBID string
	// Limit caps results; zero means the indexer's default.
	Limit int
}

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// Doer is the subset of http.Client this package uses.
//
// An interface, so a test can substitute a transport without this package ever
// naming http.Transport — which it must not, because a transport built here
// that forgets DialContext would silently bypass every egress control. See
// egress.Guard.HTTPClient, which is where the construction lives instead.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client queries indexers.
//
// One Client serves every indexer: the transport, and therefore the egress
// policy, is a property of the process rather than of an indexer.
type Client struct {
	http Doer
	// userAgent is deliberately generic. Announcing the software and version to
	// every indexer is a fingerprint the operator gains nothing from.
	userAgent string
	now       func() time.Time
	// validate is the URL check, held here so the pre-flight check and the
	// redirect check are THE SAME FUNCTION. Two copies drift, and the half that
	// drifts is the half nobody is looking at: a pre-flight check that refuses
	// link-local while the redirect check permits it is worse than having
	// neither, because it reads as covered.
	//
	// This is the base rule. Each indexer's requests use validatorFor, which is
	// this rule with that indexer's own configured address allowed onto the
	// operator's network (ADR-0024) — and the same validatorFor result is what
	// that indexer's HTTP client checks redirects with.
	validate func(*url.URL) error

	// guard builds one HTTP client per indexer address, each allowed to reach
	// that one address on a private network and nothing else private. Nil for
	// a client built around a Doer, which is how tests answer without a network.
	guard  *egress.Guard
	mu     sync.Mutex
	byDest map[string]Doer
	// sessions are the signed-in Cardigann trackers' sessions, by indexer
	// (ADR-0059). Held in memory only.
	sessions map[int64]*cgSession
}

// NewClient builds a client bound to the egress guard.
//
// The guard is taken rather than a ready-made HTTP client so that this package
// cannot be handed an unguarded one by accident. The "indexer" profile is named
// here, in one place, rather than threaded through every call site.
func NewClient(guard *egress.Guard) *Client {
	if guard == nil {
		return &Client{http: refusingDoer{}, userAgent: userAgent,
			now: time.Now, validate: validateRequestURL}
	}
	return &Client{
		http:      guard.HTTPClient(ProfileName, DefaultTimeout, validateRequestURL),
		userAgent: userAgent,
		now:       time.Now,
		validate:  validateRequestURL,
		guard:     guard,
		byDest:    map[string]Doer{},
	}
}

// ownAddress is the indexer's configured address as an egress.DestinationKey,
// or "" when it has none worth allowing. It is taken from the DEFINITION — what
// the operator typed and saved — and from nowhere else.
func ownAddress(d Definition) string {
	u, err := url.Parse(strings.TrimSpace(d.BaseURL))
	if err != nil {
		return ""
	}
	dest, err := egress.DestinationOf(u)
	if err != nil {
		return ""
	}
	return dest
}

// validatorFor is the URL check for one indexer's requests: the base rule, with
// the indexer's own address allowed onto the operator's network (ADR-0024).
//
// Only an exact match of host AND port is exempt. A download link or a
// redirect naming any other private destination — another port on the same
// machine, the router, another host — is refused by the base rule exactly as
// before, because a feed chose it.
func (c *Client) validatorFor(d Definition) func(*url.URL) error {
	base := c.validate
	own := ownAddress(d)
	if own == "" {
		return base
	}
	return func(u *url.URL) error {
		if dest, err := egress.DestinationOf(u); err == nil && dest == own {
			return validateOwnAddress(u)
		}
		return base(u)
	}
}

// validateOwnAddress checks the one URL shape exempt from the private-address
// rule: the indexer's own configured address. The scheme is still http(s) and
// a literal address is still refused if it is link-local, multicast or
// otherwise not somewhere an operator runs services.
func validateOwnAddress(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q", ErrUnsafeURL, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: no host", ErrUnsafeURL)
	}
	if ip := net.ParseIP(host); ip != nil && egress.IsRestricted(ip) && !egress.OnOperatorNetwork(ip) {
		return fmt.Errorf("%w: %s is not an address an indexer can be at", ErrUnsafeURL, ip)
	}
	return nil
}

// doerFor returns the HTTP client for one indexer's requests: one per indexer
// address, whose dialer may reach that address on the operator's network and
// no other private destination. Built once and kept, so connections are
// pooled as they were when every indexer shared one client.
func (c *Client) doerFor(d Definition) Doer {
	own := ownAddress(d)
	if c.guard == nil || own == "" {
		return c.http
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if doer, ok := c.byDest[own]; ok {
		return doer
	}
	doer := c.guard.HTTPClientAllowing(ProfileName, DefaultTimeout, c.validatorFor(d), own)
	c.byDest[own] = doer
	return doer
}

// NewClientWithDoer is for tests, which need to answer requests without a
// network. It is deliberately not the ordinary constructor: production code
// takes a Guard and gets a guarded transport it cannot misconfigure.
func NewClientWithDoer(d Doer) *Client {
	if d == nil {
		d = refusingDoer{}
	}
	return &Client{http: d, userAgent: userAgent, now: time.Now, validate: validateRequestURL}
}

// ProfileName is the egress profile indexer traffic uses.
const ProfileName = "indexer"

const userAgent = "CMediaStack"

type refusingDoer struct{}

func (refusingDoer) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("indexer: no egress guard was wired; refusing to connect")
}

// validateRequestURL rejects a URL before it is dialled.
//
// The guard's DenyPrivate catches the address; this catches the shape, and it
// catches it earlier and with a better message. Both exist because they fail in
// different places: this cannot see a DNS answer, and the guard cannot see a
// scheme.
func validateRequestURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q", ErrUnsafeURL, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: no host", ErrUnsafeURL)
	}
	// A literal address can be checked here and now. A hostname cannot, and is
	// left to the guard, which resolves it at dial time.
	if ip := net.ParseIP(host); ip != nil && egress.IsRestricted(ip) {
		return fmt.Errorf("%w: %s is not a routable public address", ErrUnsafeURL, ip)
	}
	return nil
}

// Search queries one indexer.
func (c *Client) Search(ctx context.Context, d Definition, q Query) ([]Result, error) {
	if d.Kind == KindCardigann {
		if err := d.Validate(); err != nil {
			return nil, err
		}
		return c.searchCardigann(ctx, d, q)
	}
	endpoint, err := c.buildURL(d, q)
	if err != nil {
		return nil, err
	}

	body, err := c.fetch(ctx, c.doerFor(d), endpoint)
	if err != nil {
		return nil, err
	}

	feed, err := parseFeed(body)
	if err != nil {
		return nil, err
	}
	if feed.Error != nil {
		return nil, feed.Error.asError()
	}

	results := make([]Result, 0, len(feed.Channel.Items))
	for _, item := range feed.Channel.Items {
		r, ok := c.itemToResult(d, item)
		if !ok {
			continue
		}
		results = append(results, r)
		if len(results) >= MaxResults {
			break
		}
	}
	return results, nil
}

// buildURL assembles the query.
//
// The API key goes in the query string because the protocol requires it. That
// is worth naming: it means the key appears in the indexer's access log and in
// any proxy between here and there. Nothing in this process logs a full
// indexer URL for exactly that reason — see redactURL.
func (c *Client) buildURL(d Definition, q Query) (*url.URL, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}

	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(d.BaseURL), "/"))
	if err != nil {
		return nil, fmt.Errorf("indexer: %w", err)
	}
	if !strings.HasSuffix(base.Path, "/api") {
		base.Path = strings.TrimRight(base.Path, "/") + "/api"
	}

	v := url.Values{}
	v.Set("t", "search")
	if q.Season >= 0 || q.TVDBID != "" {
		v.Set("t", "tvsearch")
	}
	if q.IMDBID != "" {
		v.Set("t", "movie")
		v.Set("imdbid", strings.TrimPrefix(q.IMDBID, "tt"))
	}
	if q.TVDBID != "" {
		v.Set("tvdbid", q.TVDBID)
	}
	if q.Term != "" {
		v.Set("q", q.Term)
	}
	if y, md, ok := dailyDate(q.AirDate); ok {
		// The daily convention Sonarr, Prowlarr and Jackett share.
		v.Set("t", "tvsearch")
		v.Set("season", y)
		v.Set("ep", md)
	} else {
		if q.Season >= 0 {
			v.Set("season", strconv.Itoa(q.Season))
		}
		if q.Episode > 0 {
			v.Set("ep", strconv.Itoa(q.Episode))
		}
	}

	cats := q.Categories
	if len(cats) == 0 {
		cats = d.Categories
	}
	if len(cats) > 0 {
		parts := make([]string, len(cats))
		for i, c := range cats {
			parts[i] = strconv.Itoa(c)
		}
		v.Set("cat", strings.Join(parts, ","))
	}

	limit := q.Limit
	if limit <= 0 || limit > MaxResults {
		limit = MaxResults
	}
	v.Set("limit", strconv.Itoa(limit))
	v.Set("extended", "1")
	if d.APIKey != "" {
		v.Set("apikey", d.APIKey)
	}

	base.RawQuery = v.Encode()
	if err := c.validatorFor(d)(base); err != nil {
		return nil, err
	}
	return base, nil
}

// dailyDate splits a YYYY-MM-DD air date into the year and the MM/DD a daily
// search asks with.
func dailyDate(d string) (year, monthDay string, ok bool) {
	if _, err := time.Parse("2006-01-02", d); err != nil {
		return "", "", false
	}
	return d[:4], d[5:7] + "/" + d[8:], true
}

// fetch performs the request with every bound applied.
func (c *Client) fetch(ctx context.Context, doer Doer, u *url.URL) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("indexer: %w", err)
	}
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")
	body, _, err := c.doFinal(doer, req, u)
	return body, err
}

// doFinal sends a request with every bound applied — the generic user agent,
// the status answers, the size cap — and says where it ended up after any
// redirects.
func (c *Client) doFinal(doer Doer, req *http.Request, u *url.URL) ([]byte, *url.URL, error) {
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := doer.Do(req)
	if err != nil {
		// The error carries the full URL, and the full URL carries the API key.
		return nil, nil, fmt.Errorf("indexer: requesting %s: %w", redactURL(u), unwrapURLError(err))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, nil, fmt.Errorf("%w (HTTP %d)", ErrIndexerAuth, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, nil, fmt.Errorf("%w: rate limited", ErrIndexerRefused)
	case resp.StatusCode != http.StatusOK:
		return nil, nil, fmt.Errorf("%w: HTTP %d", ErrIndexerRefused, resp.StatusCode)
	}

	// One byte past the cap is read deliberately, so that hitting the limit is
	// distinguishable from a response that happens to be exactly that long.
	limited := io.LimitReader(resp.Body, MaxResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, fmt.Errorf("indexer: reading response: %w", err)
	}
	if len(body) > MaxResponseBytes {
		return nil, nil, fmt.Errorf("%w (%d bytes)", ErrResponseTooLarge, MaxResponseBytes)
	}
	final := u
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL
	}
	return body, final, nil
}

// redactURL renders a URL for a log or an error message with the API key
// removed. An indexer error that includes the query string would put the key in
// the log file, and log files are the least-guarded copy of anything.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	clone := *u
	q := clone.Query()
	for _, secret := range []string{"apikey", "api_key", "passkey", "token", "rss_key"} {
		if q.Has(secret) {
			q.Set(secret, "REDACTED")
		}
	}
	clone.RawQuery = q.Encode()
	clone.User = nil
	return clone.String()
}

// unwrapURLError strips the URL out of a *url.Error, because its Error() prints
// the full URL and that is where the API key lives.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// ---------------------------------------------------------------------------
// Feed parsing
// ---------------------------------------------------------------------------

type feed struct {
	XMLName xml.Name   `xml:"rss"`
	Channel feedChan   `xml:"channel"`
	Error   *feedError `xml:"-"`
}

type feedChan struct {
	Items []feedItem `xml:"item"`
}

type feedItem struct {
	Title       string     `xml:"title"`
	GUID        string     `xml:"guid"`
	Link        string     `xml:"link"`
	Comments    string     `xml:"comments"`
	PubDate     string     `xml:"pubDate"`
	Size        string     `xml:"size"`
	Enclosure   enclosure  `xml:"enclosure"`
	Attrs       []feedAttr `xml:"attr"`
	Description string     `xml:"description"`
}

type enclosure struct {
	URL    string `xml:"url,attr"`
	Length string `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type feedAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type feedError struct {
	Code        string `xml:"code,attr"`
	Description string `xml:"description,attr"`
}

func (e feedError) asError() error {
	// 100-109 are the credential range in the Newznab spec.
	if strings.HasPrefix(e.Code, "10") {
		return fmt.Errorf("%w: %s (code %s)", ErrIndexerAuth, e.Description, e.Code)
	}
	return fmt.Errorf("%w: %s (code %s)", ErrIndexerRefused, e.Description, e.Code)
}

// parseFeed decodes a Torznab/Newznab response.
//
// # Why this is safe from XXE without doing anything
//
// Go's encoding/xml does not process DTDs, does not resolve external entities,
// and does not expand internal ones: an entity outside the five predefined XML
// ones is an error unless Decoder.Entity is populated, which it is not here.
// So both XXE and the billion-laughs expansion are structurally impossible
// rather than defended against.
//
// That is a claim about a standard library's behaviour, which is exactly the
// sort of claim that should not be taken on trust — TestXXEIsImpossible and
// TestBillionLaughsIsImpossible assert it against real payloads.
//
// Strict is left ON. A malformed feed is rejected rather than half-read,
// because a half-read feed is a search that silently returns fewer results.
func parseFeed(body []byte) (*feed, error) {
	// An error document is a different root element, so it is tried first.
	if bodyLooksLikeError(body) {
		var fe feedError
		if err := xml.Unmarshal(body, &fe); err == nil {
			return &feed{Error: &fe}, nil
		}
	}

	var f feed
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	dec.Strict = true
	// Entity is left nil on purpose: with no entries, any entity beyond the
	// predefined five is an error rather than an expansion.
	dec.Entity = nil
	// CharsetReader is left nil, so a declared charset this build cannot decode
	// is an error rather than a silent mis-decode.

	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	return &f, nil
}

func bodyLooksLikeError(body []byte) bool {
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	return strings.Contains(strings.ToLower(string(head)), "<error")
}

// itemToResult converts one feed item, dropping anything unusable.
//
// Dropping rather than erroring is deliberate: one malformed item in a
// thousand-item feed should cost that item, not the search.
func (c *Client) itemToResult(d Definition, item feedItem) (Result, bool) {
	title := strings.TrimSpace(item.Title)
	if title == "" {
		return Result{}, false
	}
	if len(title) > MaxTitleBytes {
		title = title[:MaxTitleBytes]
	}

	download := strings.TrimSpace(item.Enclosure.URL)
	if download == "" {
		download = strings.TrimSpace(item.Link)
	}
	if download == "" {
		return Result{}, false
	}
	// The download link is chosen by the uploader, not the operator. Checking
	// it here means an unsafe one never reaches the queue, where it would be
	// retried on a schedule.
	//
	// A magnet URI is a legitimate download link and a great many indexers
	// publish nothing else. It is checked by its own rules rather than as a
	// URL: it has no host to resolve and nothing is ever fetched from it, so
	// the http(s)-only rule that protects the fetch path would here only
	// discard every result the indexer returned.
	if !acceptableDownloadLink(c.validatorFor(d), download) {
		return Result{}, false
	}

	r := Result{
		Title:       title,
		GUID:        strings.TrimSpace(item.GUID),
		DownloadURL: download,
		InfoURL:     strings.TrimSpace(item.Comments),
		IndexerID:   d.ID,
		IndexerName: d.Name,
		Parsed:      release.Parse(title),
	}

	r.Size = parseSize(item.Size, item.Enclosure.Length)
	r.PublishedAt = parsePubDate(item.PubDate)

	for _, a := range item.Attrs {
		switch strings.ToLower(a.Name) {
		case "seeders":
			r.Seeders = atoiClamped(a.Value)
		case "peers":
			// Torznab "peers" is seeders + leechers.
			if n := atoiClamped(a.Value); n > 0 {
				r.Leechers = n
			}
		case "leechers":
			r.Leechers = atoiClamped(a.Value)
		case "grabs":
			r.Grabs = atoiClamped(a.Value)
		case "infohash":
			r.InfoHash = sanitiseInfoHash(a.Value)
		case "size":
			if r.Size == 0 {
				r.Size = parseSize(a.Value, "")
			}
		case "category":
			if n := atoiClamped(a.Value); n > 0 && len(r.Categories) < 32 {
				r.Categories = append(r.Categories, n)
			}
		}
	}

	// "peers" includes seeders; leechers alone is the useful number.
	if r.Leechers > r.Seeders {
		r.Leechers -= r.Seeders
	}
	return r, true
}

func parseSize(primary, fallback string) int64 {
	for _, s := range []string{primary, fallback} {
		if n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// pubDateFormats are the shapes indexers actually emit. RFC 1123Z is the
// specified one; the others are what happens.
var pubDateFormats = []string{
	time.RFC1123Z,
	time.RFC1123,
	time.RFC3339,
	"Mon, 2 Jan 2006 15:04:05 -0700",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
}

func parsePubDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range pubDateFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	// An unparseable date is left zero rather than guessed at. "Now" would be
	// worse than nothing: it would make an old release look new and sort to the
	// top of every list.
	return time.Time{}
}

// atoiClamped parses a count, refusing negatives and absurd values. An indexer
// claiming 2^63 seeders is not reporting, it is probing.
func atoiClamped(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}
	const ceiling = 10_000_000
	if n > ceiling {
		return ceiling
	}
	return n
}

// sanitiseInfoHash keeps only a well-formed hex hash. An infohash goes on to be
// used as an identifier and, in some download clients, as part of a path.
func sanitiseInfoHash(s string) string {
	s = strings.TrimSpace(s)
	if len(s) != 40 && len(s) != 64 {
		return ""
	}
	for _, r := range s {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
		if !isHex {
			return ""
		}
	}
	return strings.ToLower(s)
}

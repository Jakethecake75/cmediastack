package indexer

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	// MaxTorrentBytes caps a fetched .torrent file.
	//
	// A real one is kilobytes; a large multi-file release with many pieces
	// might reach a few hundred. 2 MiB is generous and still bounded, and it is
	// below the engine's own cap so an oversized file is refused before it is
	// ever held in memory twice.
	MaxTorrentBytes = 2 << 20
	// MaxMagnetBytes caps a magnet URI. A magnet with a long tracker list runs
	// to a couple of kilobytes; anything past this is not a magnet link, and it
	// can arrive in a response header, where the transport's own limit is
	// measured in megabytes.
	MaxMagnetBytes = 4 << 10
)

// Payload is what a grab produced.
//
// Exactly one of Magnet and Torrent is set. Indexers differ: some publish a
// magnet URI directly in the feed, others publish an HTTP link to a .torrent,
// and a few publish a link that redirects to a magnet. The caller should not
// have to care which, so this carries whichever arrived.
type Payload struct {
	Magnet  string
	Torrent []byte
}

func isMagnet(s string) bool { return strings.HasPrefix(strings.ToLower(s), "magnet:") }

// checkMagnet bounds a magnet URI and requires the one field that makes it
// usable. An indexer that answers with "magnet:?dn=something" has given us a
// display name and no torrent, and saying so here beats a failure inside the
// engine with no indexer named in it.
func checkMagnet(s string) (string, error) {
	if len(s) > MaxMagnetBytes {
		return "", fmt.Errorf("%w: the magnet link is %d bytes", ErrResponseTooLarge, len(s))
	}
	if !strings.Contains(strings.ToLower(s), "xt=urn:bt") {
		return "", fmt.Errorf("%w: the magnet link carries no info hash", ErrMalformed)
	}
	return s, nil
}

// Download fetches what a candidate's download link points at.
//
// The URL comes from a sealed ticket, which guarantees the client did not
// CHANGE it. That is not the same as it being safe: the indexer chose it, and
// an indexer is a third party. So it is validated here exactly as a search URL
// is, and the guarded client re-validates every redirect hop — a 302 to
// 169.254.169.254 is the obvious attack, and it is the one a design that checks
// only the first URL walks straight into.
func (c *Client) Download(ctx context.Context, d Definition, rawURL string) (Payload, error) {
	if d.Kind == KindCardigann {
		// The real link may be on the details page (ADR-0060).
		link, err := c.cardigannLink(ctx, d, rawURL)
		if err != nil {
			return Payload{}, err
		}
		rawURL = link
	}
	if isMagnet(rawURL) {
		// Nothing to fetch. Handing the magnet back unfetched is both faster
		// and strictly safer: no request leaves the instance at all.
		m, err := checkMagnet(rawURL)
		return Payload{Magnet: m}, err
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return Payload{}, fmt.Errorf("%w: unparseable", ErrUnsafeURL)
	}
	// This indexer's rule, the one its HTTP client checks redirects with: the
	// base rule, with the indexer's own configured address allowed onto the
	// operator's network (ADR-0024). A link to any other private address is
	// refused here, before anything is dialled.
	if err := c.validatorFor(d)(u); err != nil {
		return Payload{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Payload{}, fmt.Errorf("indexer: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/x-bittorrent, application/octet-stream")

	doer := c.doerFor(d)
	if d.Kind == KindCardigann {
		// A private tracker's links need its signed-in session (ADR-0059).
		if doer, err = c.cardigannDoer(ctx, d); err != nil {
			return Payload{}, err
		}
	}
	resp, err := doer.Do(req)

	// A CheckRedirect refusal returns BOTH a response and an error: the
	// response is the 3xx that was not followed, with its Location intact.
	// Several indexers redirect a .torrent link straight to a magnet URI, which
	// no HTTP client can follow and which the URL validator refuses by scheme.
	// So the Location is read off that unfollowed response BEFORE the error is
	// treated as a failure, otherwise a completely ordinary indexer reads as
	// "refused an unsafe URL".
	//
	// This ordering is measured, not assumed: net/http returns (resp=302,
	// err=*url.Error) when CheckRedirect returns an error, and the response
	// body is already closed.
	if resp != nil {
		loc := resp.Header.Get("Location")
		if resp.StatusCode >= 300 && resp.StatusCode <= 399 && isMagnet(loc) {
			drain(resp)
			m, cerr := checkMagnet(loc)
			return Payload{Magnet: m}, cerr
		}
	}
	if err != nil {
		if resp != nil {
			drain(resp)
		}
		return Payload{}, fmt.Errorf("indexer: fetching %s: %w", redactURL(u), unwrapURLError(err))
	}
	defer drain(resp)

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Payload{}, fmt.Errorf("%w (HTTP %d)", ErrIndexerAuth, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		return Payload{}, fmt.Errorf("%w: rate limited", ErrIndexerRefused)
	case resp.StatusCode != http.StatusOK:
		return Payload{}, fmt.Errorf("%w: HTTP %d", ErrIndexerRefused, resp.StatusCode)
	}

	limited := io.LimitReader(resp.Body, MaxTorrentBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return Payload{}, fmt.Errorf("indexer: reading the torrent: %w", err)
	}
	if len(body) > MaxTorrentBytes {
		return Payload{}, fmt.Errorf("%w (%d bytes)", ErrResponseTooLarge, MaxTorrentBytes)
	}
	if len(body) == 0 {
		return Payload{}, fmt.Errorf("%w: the indexer returned an empty body", ErrMalformed)
	}

	// A .torrent is bencode, and bencode for a dictionary starts with 'd'.
	// Checking one byte turns "the indexer served an HTML error page with HTTP
	// 200", which is extremely common, into a clear message rather than a
	// bencode parse error forty frames down.
	if body[0] != 'd' {
		if bodyLooksLikeError(body) {
			return Payload{}, fmt.Errorf("%w: the indexer returned an error page, not a torrent", ErrIndexerRefused)
		}
		return Payload{}, fmt.Errorf("%w: the response is not a torrent file", ErrMalformed)
	}
	return Payload{Torrent: body}, nil
}

// drain reads and closes a response body so the connection can be reused.
// Closing twice is harmless, which matters because net/http has already closed
// the body on the CheckRedirect path above.
func drain(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

// acceptableDownloadLink reports whether a feed item's download link is one
// this software is willing to keep.
//
// Two shapes are allowed and they are checked differently. An http(s) URL will
// be FETCHED, so it gets the full SSRF check through the client's own
// validator. A magnet URI is never fetched — it is handed to the torrent
// engine as an identifier — so it is checked for being a well-formed magnet
// and nothing more. Judging a magnet by the fetch rules rejects it for having
// the wrong scheme, which silently empties the results of every magnet-only
// indexer.
func acceptableDownloadLink(validate func(*url.URL) error, link string) bool {
	if isMagnet(link) {
		_, err := checkMagnet(link)
		return err == nil
	}
	u, err := url.Parse(link)
	if err != nil {
		return false
	}
	return validate(u) == nil
}

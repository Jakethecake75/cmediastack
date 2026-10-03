// Package artwork fetches remote images and caches them on disk.
//
// # What this actually is
//
// A path from the public internet to the operator's filesystem, whose bytes and
// whose FILENAMES are chosen by a third party. That is the same shape as the
// import pipeline, which is where this project's one real vulnerability was
// found (ADR-0016): the importer joined a torrent's declared file path to the
// download directory, and `filepath.Join` cleans, so "../../../../etc/shadow"
// became "/etc/shadow".
//
// A metadata provider's poster field is exactly that kind of string. TMDB
// returns "/f89U3ADr1oiB1s9GkdPOEpXUk5H.jpg"; nothing but that provider's own
// correctness stops it returning "/../../../../etc/cron.d/x". So:
//
//  1. A provider's string NEVER reaches a filesystem path. The destination is
//     built from fields this software chose — a kind, a numeric id, a size — and
//     the remote string is used only to build the URL.
//  2. Even so, the write goes through library.Cache, which is os.Root: the
//     kernel resolves every component with RESOLVE_BENEATH at the moment of use.
//     Rule 1 is the design; rule 2 is what holds when rule 1 is wrong.
//
// # And the bytes themselves
//
// These images are served back to a browser from this application's own origin.
// A file that is really HTML, cached as ".jpg" and served to a logged-in
// operator, is stored cross-site scripting against the app itself. The content
// type is therefore decided by SNIFFING THE BYTES rather than by trusting the
// Content-Type header, the extension, or the provider — and anything that is
// not a recognised image is refused before it is written, not after.
package artwork

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/library"
)

// Errors this package distinguishes.
var (
	// ErrNotAnImage means the bytes fetched were not a format this software
	// will serve back to a browser.
	ErrNotAnImage = errors.New("artwork: the response was not a recognised image")
	// ErrTooLarge means the response exceeded MaxBytes.
	ErrTooLarge = errors.New("artwork: the image is larger than this software will cache")
	// ErrRefusedURL means the image URL was not one this software will fetch.
	ErrRefusedURL = errors.New("artwork: refusing to fetch that URL")
	// ErrUnavailable means the provider's host answered, unhelpfully.
	ErrUnavailable = errors.New("artwork: the image could not be fetched")
)

// MaxBytes caps a single image.
//
// A poster is tens of kilobytes; a backdrop a few hundred. Eight megabytes is
// far above anything legitimate and far below anything that matters to a disk,
// which is the right place for a limit whose job is to stop an unbounded read
// rather than to be tuned.
const MaxBytes = 8 << 20

// Kind distinguishes the images this software stores. Not free text: it becomes
// a directory name.
type Kind string

const (
	KindPoster   Kind = "poster"
	KindBackdrop Kind = "backdrop"
	KindStill    Kind = "still"
)

func (k Kind) valid() bool {
	switch k {
	case KindPoster, KindBackdrop, KindStill:
		return true
	}
	return false
}

// Ref names an image to fetch and where it belongs.
//
// Note what is and is not used to build the destination: Kind, Provider, ID and
// Size are this software's own values and form the path. RemotePath is the
// provider's string and forms only the URL.
type Ref struct {
	Kind Kind
	// Provider is the short name of the service the image came from, so two
	// providers' images for the same title cannot collide.
	Provider string
	// ID is the provider's numeric identifier for the title.
	ID int64
	// Size is the provider's size token ("w200"), validated as such.
	Size string
	// RemotePath is the provider's own path for the file. It goes into the URL
	// and nowhere near the filesystem.
	RemotePath string
}

// Fetcher fetches an image URL. Satisfied by an *http.Client built from the
// egress guard, which is how these requests inherit the tunnel policy.
type Fetcher interface {
	Do(req *http.Request) (*http.Response, error)
}

// Store caches artwork on disk.
type Store struct {
	cache  *library.Cache
	client Fetcher
	// hosts is the set of image hosts this store will fetch from. Empty means
	// fetch nothing, which is the safe direction for a misconfiguration.
	hosts map[string]bool
}

// New builds a store.
//
// hosts is an allowlist. A provider supplies a base URL along with its image
// paths, and following a provider-supplied host would make this a
// server-side request forgery primitive pointed at whatever that provider (or
// anyone who could answer as it) named.
func New(cache *library.Cache, client Fetcher, hosts []string) *Store {
	set := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			set[h] = true
		}
	}
	return &Store{cache: cache, client: client, hosts: set}
}

// sizeToken matches a provider size like "w200", "h632" or "original".
//
// Validated rather than passed through because it becomes part of a filename.
// Belt and braces over the containment, and cheap.
func validSize(s string) bool {
	if s == "original" {
		return true
	}
	if len(s) < 2 || (s[0] != 'w' && s[0] != 'h') {
		return false
	}
	n, err := strconv.Atoi(s[1:])
	return err == nil && n > 0 && n <= 10000
}

// Path is where a ref is cached, relative to the cache directory.
//
// Built entirely from validated, software-chosen values. The extension is
// decided AFTER the bytes are sniffed, so this returns the stem.
func (r Ref) stem() (string, error) {
	if !r.Kind.valid() {
		return "", fmt.Errorf("artwork: %q is not a kind of image this software stores", r.Kind)
	}
	if r.ID <= 0 {
		return "", fmt.Errorf("artwork: %d is not an identifier", r.ID)
	}
	if !validSize(r.Size) {
		return "", fmt.Errorf("artwork: %q is not a size token", r.Size)
	}
	prov := strings.ToLower(strings.TrimSpace(r.Provider))
	for _, c := range prov {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return "", fmt.Errorf("artwork: %q is not a provider name", r.Provider)
		}
	}
	if prov == "" {
		return "", fmt.Errorf("artwork: no provider named")
	}
	// kind/provider/id-size — every segment from a checked value.
	return path.Join(string(r.Kind), prov,
		strconv.FormatInt(r.ID, 10)+"-"+r.Size), nil
}

// Cached returns the cached path for a ref, and whether it is present.
//
// Checked by trying each extension this software stores rather than by
// recording the type: a cache is allowed to be inspected and repaired with
// ordinary tools, and a sidecar index would make that a way to corrupt it.
func (s *Store) Cached(r Ref) (string, bool) {
	stem, err := r.stem()
	if err != nil {
		return "", false
	}
	for _, ext := range []string{".jpg", ".png", ".webp"} {
		if s.cache.Exists(stem + ext) {
			return stem + ext, true
		}
	}
	return "", false
}

// Fetch downloads an image if it is not already cached, and returns its path
// relative to the cache directory.
func (s *Store) Fetch(ctx context.Context, base string, r Ref) (string, error) {
	if rel, ok := s.Cached(r); ok {
		return rel, nil
	}
	stem, err := r.stem()
	if err != nil {
		return "", err
	}

	u, err := s.resolve(base, r.Size, r.RemotePath)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "image/jpeg,image/png,image/webp")

	res, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
		_ = res.Body.Close()
	}()

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: the image host answered %d", ErrUnavailable, res.StatusCode)
	}

	// Read one byte past the cap, so "exactly at the limit" and "over it" are
	// distinguishable. Reading exactly MaxBytes cannot tell them apart and
	// would silently truncate a file at the boundary.
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if len(body) > MaxBytes {
		return "", fmt.Errorf("%w: over %d bytes", ErrTooLarge, MaxBytes)
	}

	// The bytes decide, not the header and not the extension. See the package
	// comment: this file is served back to a browser from this app's origin.
	ext, ok := imageExtension(body)
	if !ok {
		return "", fmt.Errorf("%w: %d bytes beginning %q", ErrNotAnImage,
			len(body), preview(body))
	}

	rel := stem + ext
	if err := s.cache.Replace(rel, func(w io.Writer) error {
		_, werr := w.Write(body)
		return werr
	}); err != nil {
		return "", err
	}
	return rel, nil
}

// remoteFile is the only shape a provider's image path may take: one filename,
// with a picture's extension, and nothing else.
//
// Not a regex over a whole path — a single segment. This is stricter than it
// needs to be for TMDB, whose paths really are exactly this, and the strictness
// is the point: a provider that can supply only a FILENAME cannot supply a
// traversal, an absolute URL, a different host, or a query string. The
// containment underneath (library.Cache) is what holds if this is ever wrong,
// but this is what makes it hard to be wrong.
var remoteFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,120}\.(?i:jpe?g|png|webp)$`)

// resolve builds the image URL and refuses anything unexpected about it.
//
// # Why the size is part of the URL and not only the filename
//
// TMDB's image URL is base + size + "/" + file, e.g.
// https://image.tmdb.org/t/p/ + w200 + /f89U3ADr1oiB1s9GkdPOEpXUk5H.jpg.
//
// The first version of this used url.ResolveReference to combine the base with
// the provider's path, which is the idiomatic thing and is wrong here: a
// provider path beginning with "/" is ROOT-relative, so resolving it against
// https://image.tmdb.org/t/p/w200/ produces https://image.tmdb.org/f89U….jpg —
// silently discarding the whole prefix. Every fixture agreed with the mistake,
// because a fixture server answers whatever path it is asked for. The real CDN
// answered 404, which is how this was found.
func (s *Store) resolve(base, size, remotePath string) (*url.URL, error) {
	if !validSize(size) {
		return nil, fmt.Errorf("%w: %q is not a size token", ErrRefusedURL, size)
	}

	file := strings.TrimPrefix(strings.TrimSpace(remotePath), "/")
	if !remoteFile.MatchString(file) {
		return nil, fmt.Errorf("%w: %q is not a plain image filename",
			ErrRefusedURL, remotePath)
	}

	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return nil, fmt.Errorf("%w: unparseable base %q", ErrRefusedURL, base)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("%w: %s is not https", ErrRefusedURL, u.Scheme)
	}
	if !s.hosts[strings.ToLower(u.Hostname())] {
		return nil, fmt.Errorf("%w: %q is not an allowed image host",
			ErrRefusedURL, u.Hostname())
	}
	if u.User != nil {
		// Credentials in an image URL are either a mistake or an attempt to
		// make this software authenticate somewhere on somebody's behalf.
		return nil, fmt.Errorf("%w: the base URL carries credentials", ErrRefusedURL)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: the base URL carries a query or fragment", ErrRefusedURL)
	}

	// Assembled from the checked parts rather than resolved, so there is no
	// reference-resolution rule left to be surprised by.
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + size + "/" + file
	return u, nil
}

// ValidateURL is the redirect check for the HTTP client.
//
// A redirect is where an allowlist is usually defeated: the first request goes
// to the permitted host and the 302 goes anywhere. The client re-checks every
// hop through this.
func (s *Store) ValidateURL(u *url.URL) error {
	if u.Scheme != "https" || !s.hosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("%w: redirect to %q", ErrRefusedURL, u.Redacted())
	}
	return nil
}

// imageExtension identifies an image by its magic bytes.
//
// Only the three formats every browser renders and this software is willing to
// serve. Notably absent: SVG, which is XML, executes script, and would be a
// stored-XSS delivery mechanism dressed as a picture.
func imageExtension(b []byte) (string, bool) {
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return ".jpg", true
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return ".png", true
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return ".webp", true
	}
	return "", false
}

// ContentType maps a cached file back to the header it must be served with.
//
// Served explicitly, never sniffed by the browser: the response also carries
// X-Content-Type-Options: nosniff (api.SecurityHeaders), and the two together
// are what stop a file that slipped through from being interpreted as anything
// but a picture.
func ContentType(rel string) string {
	switch path.Ext(rel) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

// preview renders the first few bytes for an error message, printably.
func preview(b []byte) string {
	const n = 16
	if len(b) > n {
		b = b[:n]
	}
	var sb strings.Builder
	for _, c := range b {
		if c >= 0x20 && c < 0x7f {
			sb.WriteByte(c)
		} else {
			sb.WriteByte('.')
		}
	}
	return sb.String()
}

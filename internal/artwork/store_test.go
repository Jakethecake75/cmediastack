package artwork

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/library"
)

// Real magic bytes, taken from a real file rather than from memory: the first
// twelve bytes of a genuine TMDB poster are ff d8 ff e0 00 10 4a 46 49 46 00 01
// (JPEG/JFIF), checked against image.tmdb.org while this was written.
var (
	jpegBytes = append([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01},
		bytes.Repeat([]byte{0x42}, 64)...)
	pngBytes  = append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0x42}, 64)...)
	webpBytes = append([]byte("RIFF\x00\x00\x00\x00WEBP"), bytes.Repeat([]byte{0x42}, 64)...)
)

type rig struct {
	t     *testing.T
	dir   string
	cache *library.Cache
	store *Store
	srv   *httptest.Server
	// served is what the fake host returns.
	served     []byte
	servedType string
	status     int
	// redirectTo, when set, makes the host 302 there instead.
	redirectTo string
	hits       int
}

func newRig(t *testing.T, hosts ...string) *rig {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "art")
	cache, err := library.OpenCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })

	r := &rig{t: t, dir: dir, cache: cache, served: jpegBytes,
		servedType: "image/jpeg", status: 200}

	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits++
		if r.redirectTo != "" {
			http.Redirect(w, req, r.redirectTo, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", r.servedType)
		w.WriteHeader(r.status)
		_, _ = w.Write(r.served)
	}))
	t.Cleanup(r.srv.Close)

	host := mustHost(t, r.srv.URL)
	if len(hosts) == 0 {
		hosts = []string{host}
	}
	// The fake server is http, and resolve() requires https. Rather than weaken
	// the rule for tests — which would mean the rule is untested — the client
	// rewrites the scheme, so production code still sees and enforces https.
	r.store = New(cache, &schemeRewriter{to: r.srv.URL}, hosts)
	return r
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname()
}

// schemeRewriter sends an https request to a local http test server, leaving
// every check in the code under test operating on the real https URL.
type schemeRewriter struct{ to string }

func (s *schemeRewriter) Do(req *http.Request) (*http.Response, error) {
	u, _ := url.Parse(s.to)
	req.URL.Scheme, req.URL.Host = u.Scheme, u.Host
	return http.DefaultClient.Do(req)
}

func (r *rig) base() string { return "https://" + mustHost(r.t, r.srv.URL) + "/t/p" }

func (r *rig) ref() Ref {
	return Ref{Kind: KindPoster, Provider: "tmdb", ID: 603, Size: "w200",
		RemotePath: "/f89U3ADr1oiB1s9GkdPOEpXUk5H.jpg"}
}

// ---------------------------------------------------------------------------
// The path never comes from the provider
// ---------------------------------------------------------------------------

// The defining property. A provider's string is used to build a URL and must
// never reach a filesystem path — the same class of defect as the importer's,
// which was a real vulnerability (ADR-0016).
func TestAProvidersPathNeverReachesTheFilesystem(t *testing.T) {
	hostile := []string{
		"/../../../../etc/cron.d/x.jpg",
		"/....//....//escape.jpg",
		"/%2e%2e%2f%2e%2e%2fescape.jpg",
		"/a/../../../../../../tmp/escape.jpg",
	}
	for _, p := range hostile {
		r := newRig(t)
		ref := r.ref()
		ref.RemotePath = p

		rel, err := r.store.Fetch(context.Background(), r.base(), ref)
		if err != nil {
			// Refusing is a fine outcome. What must never happen is a write
			// outside the cache, checked below either way.
			t.Logf("%q refused: %v", p, err)
		} else if !strings.HasPrefix(rel, "poster/tmdb/603-w200") {
			t.Errorf("%q produced destination %q — the provider's string reached the path",
				p, rel)
		}

		// The assertion that matters: nothing outside the cache directory.
		for _, escape := range []string{"/tmp/escape.jpg", "/etc/cron.d/x.jpg"} {
			if _, serr := os.Stat(escape); serr == nil {
				t.Fatalf("%q wrote outside the cache, to %s", p, escape)
			}
		}
		if out := filepath.Join(filepath.Dir(r.dir), "escape.jpg"); fileExists(out) {
			t.Fatalf("%q wrote to %s, beside the cache", p, out)
		}
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestTheDestinationIsBuiltFromCheckedValues(t *testing.T) {
	r := newRig(t)
	rel, err := r.store.Fetch(context.Background(), r.base(), r.ref())
	if err != nil {
		t.Fatal(err)
	}
	if rel != "poster/tmdb/603-w200.jpg" {
		t.Errorf("path = %q, want poster/tmdb/603-w200.jpg", rel)
	}

	// And a ref whose own fields are wrong is refused before anything is
	// fetched — the fields form a path, so they are validated like one.
	bad := []Ref{
		{Kind: "../etc", Provider: "tmdb", ID: 1, Size: "w200"},
		{Kind: KindPoster, Provider: "../..", ID: 1, Size: "w200"},
		{Kind: KindPoster, Provider: "tmdb/x", ID: 1, Size: "w200"},
		{Kind: KindPoster, Provider: "tmdb", ID: 0, Size: "w200"},
		{Kind: KindPoster, Provider: "tmdb", ID: -1, Size: "w200"},
		{Kind: KindPoster, Provider: "tmdb", ID: 1, Size: "../.."},
		{Kind: KindPoster, Provider: "tmdb", ID: 1, Size: "w0"},
		{Kind: KindPoster, Provider: "tmdb", ID: 1, Size: ""},
	}
	before := r.hits
	for _, ref := range bad {
		ref.RemotePath = "/ok.jpg"
		if _, err := r.store.Fetch(context.Background(), r.base(), ref); err == nil {
			t.Errorf("%+v was accepted", ref)
		}
	}
	if r.hits != before {
		t.Errorf("a malformed ref caused %d request(s); it must be refused "+
			"before anything is fetched", r.hits-before)
	}
}

// ---------------------------------------------------------------------------
// The bytes decide what this is
// ---------------------------------------------------------------------------

// A file that is really HTML, cached as .jpg and served from this app's own
// origin to a logged-in operator, is stored cross-site scripting. The
// Content-Type header the remote host sent is not evidence.
func TestOnlyRealImagesAreCached(t *testing.T) {
	refused := []struct {
		why  string
		body []byte
	}{
		{"HTML claiming to be a JPEG", []byte("<html><script>alert(1)</script></html>")},
		{"an SVG, which is script in a picture's clothing",
			[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		{"empty", []byte{}},
		{"a GIF, which this software does not serve", []byte("GIF89a\x00\x00")},
		{"nearly a JPEG", []byte{0xFF, 0xD8, 0x00, 0x00}},
	}
	for _, c := range refused {
		r := newRig(t)
		r.served = c.body
		// The remote host insists, loudly, that this is a JPEG.
		r.servedType = "image/jpeg"

		_, err := r.store.Fetch(context.Background(), r.base(), r.ref())
		if !errors.Is(err, ErrNotAnImage) {
			t.Errorf("%s: err = %v, want ErrNotAnImage", c.why, err)
		}
		if _, ok := r.store.Cached(r.ref()); ok {
			t.Errorf("%s: it was cached anyway", c.why)
		}
	}

	// And the three that are real images are accepted, with the extension
	// decided by the bytes rather than by the request.
	for _, c := range []struct {
		body []byte
		ext  string
	}{{jpegBytes, ".jpg"}, {pngBytes, ".png"}, {webpBytes, ".webp"}} {
		r := newRig(t)
		r.served = c.body
		r.servedType = "application/octet-stream" // deliberately unhelpful
		rel, err := r.store.Fetch(context.Background(), r.base(), r.ref())
		if err != nil {
			t.Errorf("a real image was refused: %v", err)
			continue
		}
		if !strings.HasSuffix(rel, c.ext) {
			t.Errorf("extension = %q, want %q", rel, c.ext)
		}
		if got := ContentType(rel); got == "" {
			t.Errorf("no content type for %q", rel)
		}
	}
}

func TestAnOversizedImageIsRefusedRatherThanTruncated(t *testing.T) {
	r := newRig(t)
	r.served = append(jpegBytes, bytes.Repeat([]byte{0x42}, MaxBytes)...)

	_, err := r.store.Fetch(context.Background(), r.base(), r.ref())
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	// Truncating would leave a corrupt file that looks cached, so nothing is
	// written at all.
	if _, ok := r.store.Cached(r.ref()); ok {
		t.Error("an oversized image left a partial file behind")
	}
}

// ---------------------------------------------------------------------------
// Where it will and will not go
// ---------------------------------------------------------------------------

func TestOnlyAllowedHostsOverHTTPS(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct{ why, base, remote string }{
		{"a different host entirely", "https://evil.example.com/t/p/", "/x.jpg"},
		{"an absolute URL in the provider's path", "https://image.tmdb.org/t/p/",
			"https://evil.example.com/x.jpg"},
		{"plain http", "http://" + mustHost(t, r.srv.URL) + "/t/p/", "/x.jpg"},
		{"a file URL", "file:///etc/", "/passwd"},
		{"credentials in the URL", "https://user:pw@" + mustHost(t, r.srv.URL) + "/t/p/", "/x.jpg"},
	} {
		ref := r.ref()
		ref.RemotePath = c.remote
		if _, err := r.store.Fetch(context.Background(), c.base, ref); !errors.Is(err, ErrRefusedURL) {
			t.Errorf("%s: err = %v, want ErrRefusedURL", c.why, err)
		}
	}
	if r.hits != 0 {
		t.Errorf("a refused URL was still fetched %d time(s)", r.hits)
	}
}

// An allowlist is usually defeated at the redirect: the first request goes to
// the permitted host and the 302 goes anywhere.
func TestARedirectOffTheAllowlistIsRefused(t *testing.T) {
	r := newRig(t)
	if err := r.store.ValidateURL(mustURL(t, "https://evil.example.com/x.jpg")); !errors.Is(err, ErrRefusedURL) {
		t.Errorf("a redirect to another host was allowed: %v", err)
	}
	if err := r.store.ValidateURL(mustURL(t, "http://"+mustHost(t, r.srv.URL)+"/x.jpg")); !errors.Is(err, ErrRefusedURL) {
		t.Error("a redirect to plain http was allowed")
	}
	if err := r.store.ValidateURL(mustURL(t, "https://"+mustHost(t, r.srv.URL)+"/x.jpg")); err != nil {
		t.Errorf("a redirect within the allowlist was refused: %v", err)
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

// ---------------------------------------------------------------------------
// Caching
// ---------------------------------------------------------------------------

func TestACachedImageIsNotFetchedAgain(t *testing.T) {
	r := newRig(t)
	if _, err := r.store.Fetch(context.Background(), r.base(), r.ref()); err != nil {
		t.Fatal(err)
	}
	if r.hits != 1 {
		t.Fatalf("first fetch made %d requests", r.hits)
	}
	for i := 0; i < 5; i++ {
		if _, err := r.store.Fetch(context.Background(), r.base(), r.ref()); err != nil {
			t.Fatal(err)
		}
	}
	if r.hits != 1 {
		t.Errorf("%d requests for a cached image; a provider that rate-limits "+
			"would be hit once per page view", r.hits)
	}
}

func TestAFailedFetchLeavesNothingBehind(t *testing.T) {
	r := newRig(t)
	r.status = http.StatusNotFound
	r.served = []byte("no such image")

	if _, err := r.store.Fetch(context.Background(), r.base(), r.ref()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	// Including the .part file, or the next run would find a stale temporary
	// and a directory listing would be full of them.
	entries, _ := os.ReadDir(filepath.Join(r.dir, "poster", "tmdb"))
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("a failed fetch left %v behind", names)
	}
}

// ---------------------------------------------------------------------------
// The cache directory itself
// ---------------------------------------------------------------------------

// A Cache has no permission checks, so pointing one at a media folder would be
// a permission-free write path into somebody's library.
func TestACacheRefusesToOpenSomethingThatIsNotACache(t *testing.T) {
	media := filepath.Join(t.TempDir(), "movies")
	if err := os.MkdirAll(filepath.Join(media, "Arrival (2016)"), 0o755); err != nil {
		t.Fatal(err)
	}

	// It opens — and in doing so marks the directory, which is the point: the
	// marker is what a person sees. What must not happen is it opening an
	// existing library SILENTLY and indistinguishably from its own cache.
	c, err := library.OpenCache(media)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	if !c.Exists(library.CacheMarker) {
		t.Error("a cache was opened without leaving a marker, so nothing " +
			"distinguishes it from an operator's own directory")
	}
	if err := c.Remove(library.CacheMarker); err == nil {
		t.Error("the marker can be removed, so the directory can be laundered " +
			"back into looking like a library")
	}
}

func TestTheCacheContainsItsWrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	c, err := library.OpenCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	// Planted symlink, the attack that defeats a Join-and-prefix check.
	outside := filepath.Join(filepath.Dir(dir), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err = c.Replace("link/escaped.jpg", func(w io.Writer) error {
		_, werr := w.Write(jpegBytes)
		return werr
	})
	if err == nil && fileExists(filepath.Join(outside, "escaped.jpg")) {
		t.Fatal("a write escaped the cache through a symlink")
	}
	t.Logf("refused through the symlink: %v", err)
}

// ---------------------------------------------------------------------------
// Against the real thing
// ---------------------------------------------------------------------------

// TestAgainstTheRealImageCDN fetches an actual TMDB poster.
//
// Worth the network dependency, because this is the one part of the metadata
// work that CAN be verified against the live service: image.tmdb.org needs no
// API key, so the whole path — allowlist, https, redirect policy, size cap,
// magic-byte sniffing, contained write — runs against real bytes from a real
// CDN rather than against a fixture that agrees with my assumptions.
//
// It has already earned its keep twice.
//
//  1. The first version built the URL with url.ResolveReference, which is the
//     idiomatic thing and silently discarded the "/t/p/w200" prefix, because a
//     provider path beginning with "/" is root-relative. Every fixture agreed
//     with the mistake — a fake server answers whatever path it is asked for.
//     The CDN answered 404.
//  2. The URL ends in ".jpg" and the CDN returns a WEBP, because BunnyCDN
//     content-negotiates on the Accept header this code sends. So the format on
//     the wire genuinely does not match the URL, in production, today. Trusting
//     the extension — or the provider — would mislabel the file and serve it
//     back with the wrong Content-Type. The magic-byte sniffing is load-bearing
//     rather than defensive, and this is the evidence.
//
// Which is why the assertions below are about being a REAL, CORRECTLY
// IDENTIFIED image rather than about being a JPEG.
//
// Skipped under -short and skipped, loudly, when the network is unavailable: a
// test that fails because a CI runner has no egress teaches nothing.
func TestAgainstTheRealImageCDN(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	const host = "image.tmdb.org"

	dir := filepath.Join(t.TempDir(), "art")
	cache, err := library.OpenCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cache.Close() }()

	store := New(cache, http.DefaultClient, []string{host})
	ref := Ref{Kind: KindPoster, Provider: "tmdb", ID: 603, Size: "w200",
		RemotePath: "/f89U3ADr1oiB1s9GkdPOEpXUk5H.jpg"}

	// The real base, exactly as TMDB's /3/configuration reports it. The size
	// is appended by resolve(), which is the part the CDN corrected.
	rel, err := store.Fetch(context.Background(), "https://"+host+"/t/p", ref)
	if err != nil {
		if errors.Is(err, ErrUnavailable) {
			t.Skipf("the image CDN is unreachable from here: %v", err)
		}
		t.Fatalf("fetching a real poster: %v", err)
	}

	// The stem is ours; only the extension comes from the bytes.
	if !strings.HasPrefix(rel, "poster/tmdb/603-w200.") {
		t.Errorf("path = %q; the CDN's own filename should not appear in it", rel)
	}

	f, err := cache.Open(rel)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	got, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}

	// What was written is a real image, and the extension on disk matches what
	// the BYTES say — not what the URL said.
	ext, ok := imageExtension(got)
	if !ok {
		t.Fatalf("what was cached is not an image this software serves: %q", preview(got))
	}
	if !strings.HasSuffix(rel, ext) {
		t.Errorf("cached as %q but the bytes are %s", rel, ext)
	}
	if ContentType(rel) == "" {
		t.Errorf("no content type for %q", rel)
	}
	// And all of it: a silently truncated image is the failure mode a size cap
	// introduces.
	if len(got) < 1000 {
		t.Errorf("cached %d bytes, which is too few to be a poster", len(got))
	}
	t.Logf("the real CDN served %s (%d bytes) from a URL ending .jpg; "+
		"cached at %s", ext, len(got), rel)

	// A second call must not touch the network.
	if _, ok := store.Cached(ref); !ok {
		t.Error("the real image was fetched but not found in the cache afterwards")
	}
}

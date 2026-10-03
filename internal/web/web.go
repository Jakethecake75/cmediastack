// Package web owns the browser-facing surface: the HTML shells and the two
// static asset bundles.
//
// # No build step
//
// There is no Node, no npm and no bundler anywhere in this tree. The Phase 0
// architecture assumed a Vite build with separate entry points; that assumption
// is overruled here, and ADR-0011 records why. In short: a JavaScript build
// step contradicts the single-binary, CGO_ENABLED=0, offline-capable brief, and
// it would attach several hundred transitive npm packages to a project whose
// SECURITY.md promises pinned and verified dependencies. Everything under
// assets/ is hand-written, has no third-party code in it at all, and is
// compiled into the binary by embed.
//
// # Two bundles, not one
//
// assets/auth is reachable anonymously; assets/app is not. That split is a
// security boundary, not an optimization: the anonymous bundle must not contain
// the authenticated application's API client, or an unauthenticated visitor
// could enumerate the entire API surface by reading it. The two directories are
// self-contained — they share no file — so the boundary cannot be eroded by an
// import that looks harmless.
//
// # No inline script or style
//
// The CSP is nonce-based with no unsafe-inline. Rather than thread a nonce
// through every page, this package emits no inline <script> or <style> at all
// and no inline event-handler attributes, so every executable byte arrives from
// 'self'. A nonce that is never emitted cannot leak, and the pages stay
// cacheable. The consequence for anyone editing assets/: element.style.x = ...
// is blocked by the same policy, because CSP style-src-attr falls back to
// style-src. Toggle classes and the hidden attribute instead.
//
// # Pages carry no data
//
// A page template renders a shell. Every value a user sees comes from the JSON
// API afterwards, over the same handlers, the same middleware chain and the
// same authorization path a script would use. There is deliberately no second
// set of server-side form handlers: each one would be another place to forget a
// permission check, and the ones you forget are the ones nobody tests.
package web

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed templates/*.html assets/auth/* assets/app/*
var files embed.FS

// Bundle names. These are also the URL segments under /assets/.
const (
	BundleAuth = "auth"
	BundleApp  = "app"
)

// pages holds one fully-parsed template set per page. Each set gets its own
// copy of the layout so that two pages can both define "content" without
// colliding.
var pages = map[string]*template.Template{}

// pageBundle records which asset bundle each page loads. A page is in exactly
// one bundle; a page in the auth bundle must be reachable anonymously and a
// page in the app bundle must not be.
var pageBundle = map[string]string{
	"login":  BundleAuth,
	"signup": BundleAuth,
	"reset":  BundleAuth,
	"setup":  BundleAuth,
	"enroll": BundleAuth,
	"app":    BundleApp,
}

func init() {
	matches, err := fs.Glob(files, "templates/*.html")
	if err != nil {
		panic("web: globbing templates: " + err.Error())
	}
	for _, m := range matches {
		name := strings.TrimSuffix(path.Base(m), ".html")
		if name == "layout" {
			continue
		}
		bundle, ok := pageBundle[name]
		if !ok {
			// A template file with no bundle decision is a page nobody decided
			// the access class of. Fail at startup rather than serve it.
			panic("web: template " + m + " has no entry in pageBundle")
		}
		t, err := template.New(name).Funcs(template.FuncMap{
			"bundle": func() string { return bundle },
			"page":   func() string { return name },
		}).ParseFS(files, "templates/layout.html", m)
		if err != nil {
			panic("web: parsing " + m + ": " + err.Error())
		}
		if t.Lookup("content") == nil {
			panic("web: template " + m + ` does not define "content"`)
		}
		pages[name] = t
	}
	buildAssetIndex()
}

// PageData is everything a template can see.
//
// It is deliberately tiny. Pages carry no user data — see the package comment —
// so this holds only the handful of values the shell must know before its first
// API call, and every one of them is public information that appears in an
// error message anyway.
type PageData struct {
	// MinPasswordLength comes from the running policy rather than a constant
	// duplicated in JavaScript, so the hint on the form cannot drift from the
	// rule the server actually applies.
	MinPasswordLength int
	// SourceURL and Version are the footer's offer of this build's source,
	// on every page (ADR-0052).
	SourceURL string
	Version   string
}

// PageNames returns every page this package can render, for tests.
func PageNames() []string {
	out := make([]string, 0, len(pages))
	for name := range pages {
		out = append(out, name)
	}
	return out
}

// Render writes a page.
//
// It renders into a buffer first. A template that fails halfway through would
// otherwise leave a 200 response holding half a page, which is both a confusing
// failure and, if the failure is data-dependent, an information leak.
func Render(w http.ResponseWriter, name string, data any) error {
	t, ok := pages[name]
	if !ok {
		return fmt.Errorf("web: no page named %q", name)
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		return fmt.Errorf("web: rendering %q: %w", name, err)
	}

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// The shells contain no user data, but they do reveal which page you are
	// on, and a shared cache has no business holding an authenticated page.
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write(buf.Bytes())
	return err
}

// ---------------------------------------------------------------------------
// Static assets
// ---------------------------------------------------------------------------

type asset struct {
	body []byte
	etag string
	ctyp string
}

// assetIndex is built once at startup: the files never change while the process
// runs, so hashing them per request would be pure waste.
var assetIndex = map[string]asset{}

func buildAssetIndex() {
	err := fs.WalkDir(files, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		assetIndex[p] = asset{
			body: body,
			etag: `"` + base64.RawURLEncoding.EncodeToString(sum[:12]) + `"`,
			ctyp: contentType(p),
		}
		return nil
	})
	if err != nil {
		panic("web: indexing assets: " + err.Error())
	}
}

// contentType is an explicit allowlist rather than mime.TypeByExtension.
//
// Serving an embedded file under a type the platform happened to guess is how
// an .svg or .html dropped into the asset directory becomes a same-origin
// script host. Anything not named here is refused outright below.
func contentType(p string) string {
	switch path.Ext(p) {
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "text/javascript; charset=utf-8"
	case ".woff2":
		return "font/woff2"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	default:
		return ""
	}
}

// Assets serves one bundle. prefix is the URL prefix the route is registered
// under, for example "/assets/auth/".
//
// It resolves names against the embedded index only. There is no filesystem
// access, so path traversal has nothing to traverse to, and a name that is not
// in the index is a 404 rather than a directory listing.
func Assets(bundle, prefix string) http.HandlerFunc {
	root := "assets/" + bundle + "/"
	return func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, prefix)
		if rel == "" || strings.Contains(rel, "/") || strings.Contains(rel, "..") {
			// Flat namespace: one directory per bundle, no subdirectories. It
			// keeps the mapping from URL to embedded name trivially auditable.
			http.NotFound(w, r)
			return
		}

		a, ok := assetIndex[root+rel]
		if !ok || a.ctyp == "" {
			http.NotFound(w, r)
			return
		}

		h := w.Header()
		h.Set("Content-Type", a.ctyp)
		h.Set("ETag", a.etag)
		// The bundles change only when the binary does, but a stale bundle
		// against a new API is a confusing failure, so revalidate rather than
		// cache blind. The 304 path below makes that nearly free.
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Content-Type-Options", "nosniff")

		if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, a.etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(a.body)
	}
}

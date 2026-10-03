package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

func TestEveryPageRenders(t *testing.T) {
	names := PageNames()
	if len(names) < 6 {
		t.Fatalf("only %d pages found: %v", len(names), names)
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if err := Render(rec, name, PageData{MinPasswordLength: 12}); err != nil {
				t.Fatalf("render: %v", err)
			}
			body := rec.Body.String()

			if rec.Code != http.StatusOK {
				t.Errorf("status = %d", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
				t.Errorf("content type = %q", got)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Error("a page was served without no-store")
			}
			if !strings.HasPrefix(body, "<!doctype html>") {
				t.Error("missing doctype")
			}

			// The page must load its own bundle and only its own bundle. A page
			// that pulled in the other one would move code across the boundary
			// between the anonymous and authenticated surfaces.
			want := pageBundle[name]
			other := BundleApp
			if want == BundleApp {
				other = BundleAuth
			}
			if !strings.Contains(body, "/assets/"+want+"/"+want+".js") {
				t.Errorf("page does not load the %s bundle", want)
			}
			if strings.Contains(body, "/assets/"+other+"/") {
				t.Errorf("page loads the %s bundle as well as %s", other, want)
			}
		})
	}
}

func TestRenderRejectsUnknownPage(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := Render(rec, "not-a-page", PageData{}); err == nil {
		t.Fatal("an unknown page rendered without error")
	}
	if rec.Body.Len() != 0 {
		t.Errorf("a failed render still wrote %d bytes", rec.Body.Len())
	}
}

// The password hint has to come from the running policy. A number typed into
// the template would silently disagree with the server the day the policy
// changes, and the person filling in the form is the one who pays for it.
func TestPasswordHintComesFromThePolicy(t *testing.T) {
	for _, name := range []string{"setup", "signup", "reset"} {
		rec := httptest.NewRecorder()
		if err := Render(rec, name, PageData{MinPasswordLength: 17}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rec.Body.String(), "At least 17 characters") {
			t.Errorf("%s does not show the policy minimum", name)
		}
	}
}

// ADR-0052: every page offers this build's source, with its version.
func TestEveryPageOffersTheSource(t *testing.T) {
	for _, name := range PageNames() {
		rec := httptest.NewRecorder()
		if err := Render(rec, name, PageData{SourceURL: "https://example.com/src", Version: "v9.8.7"}); err != nil {
			t.Fatal(err)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `<a href="https://example.com/src"`) || !strings.Contains(body, "CMediaStack v9.8.7") ||
			!strings.Contains(body, "AGPL-3.0") {
			t.Errorf("%s does not offer the source", name)
		}
	}
	// A script address never reaches the page as one, whatever the lint missed.
	rec := httptest.NewRecorder()
	if err := Render(rec, "login", PageData{SourceURL: "javascript:alert(1)", Version: "v1"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rec.Body.String(), "javascript:") {
		t.Error("a javascript: address was rendered as a link")
	}
}

// ---------------------------------------------------------------------------
// Content Security Policy
// ---------------------------------------------------------------------------

var (
	inlineScript  = regexp.MustCompile(`(?i)<script(?:\s[^>]*)?>\s*[^<\s]`)
	inlineStyleEl = regexp.MustCompile(`(?i)<style[\s>]`)
	inlineHandler = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	styleAttr     = regexp.MustCompile(`(?i)\sstyle\s*=`)
)

// The CSP has no unsafe-inline. Anything inline is dead on arrival in a
// browser, and the failure is silent — the page simply does not work — so it is
// worth catching here rather than in a screenshot.
func TestTemplatesContainNothingInline(t *testing.T) {
	for _, name := range PageNames() {
		rec := httptest.NewRecorder()
		if err := Render(rec, name, PageData{MinPasswordLength: 12}); err != nil {
			t.Fatal(err)
		}
		body := rec.Body.String()

		for label, re := range map[string]*regexp.Regexp{
			"an inline <script> body":   inlineScript,
			"an inline <style> element": inlineStyleEl,
			"an inline on* handler":     inlineHandler,
			"an inline style attribute": styleAttr,
		} {
			if re.MatchString(body) {
				t.Errorf("%s contains %s, which the CSP blocks", name, label)
			}
		}
	}
}

// Same rule, from the other direction: CSP style-src-attr falls back to
// style-src, so assigning element.style.* at runtime is blocked too. innerHTML
// is banned separately — it is how database text becomes markup.
func TestScriptsAvoidBlockedAndUnsafeAPIs(t *testing.T) {
	banned := map[string]string{
		".innerHTML":         "assigns markup from data; use textContent",
		".outerHTML":         "assigns markup from data; use textContent",
		"eval(":              "not permitted under the CSP",
		"new Function":       "not permitted under the CSP",
		"document.write":     "not permitted under the CSP",
		".style.":            "inline styles are blocked by the CSP; toggle classes",
		"insertAdjacentHTML": "assigns markup from data; use textContent",
	}

	for name, a := range assetIndex {
		if !strings.HasSuffix(name, ".js") {
			continue
		}
		src := stripComments(string(a.body))
		for needle, why := range banned {
			if strings.Contains(src, needle) {
				t.Errorf("%s uses %s: %s", name, needle, why)
			}
		}
	}
}

// stripComments removes block comments and whole-line // comments so that the
// scan above reads code rather than prose — the comments in these files
// necessarily name the very APIs they are warning against.
//
// It is not a JavaScript parser, and it deliberately leaves trailing comments
// alone: recognising those means deciding whether a // is inside a string
// literal, and getting that wrong would silently delete real code from the
// scan. Leaving them in can only produce a false failure, never a false pass,
// which is the correct direction for a test that guards a security property.
func stripComments(src string) string {
	src = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(src, "")
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// ---------------------------------------------------------------------------
// The bundle boundary
// ---------------------------------------------------------------------------

// anonymousEndpoints are the only API paths the anonymous bundle is allowed to
// name. Every one of them is registered as anonymous or as enrollment-class in
// internal/api, and each is reachable before a principal can act.
var anonymousEndpoints = map[string]bool{
	"/api/v1/setup":                   true,
	"/api/v1/auth/login":              true,
	"/api/v1/auth/login/mfa":          true,
	"/api/v1/auth/signup":             true,
	"/api/v1/auth/reset/initiate":     true,
	"/api/v1/auth/reset/complete":     true,
	"/api/v1/auth/signup/challenge":   true, // ADR-0051
	"/api/v1/auth/logout":             true,
	"/api/v1/auth/mfa/enroll":         true,
	"/api/v1/auth/mfa/enroll/confirm": true,
}

var apiPath = regexp.MustCompile(`/api/v[0-9]+/[A-Za-z0-9/_.-]*`)

// This is the test that keeps the two bundles meaningfully separate.
//
// The anonymous bundle is served to anybody who can reach the login page. If it
// grew a route table, a permission list or an admin path, an unauthenticated
// visitor could read the shape of the entire application out of it — which is
// exactly what §7.1 says must not happen. Splitting the bundles achieves
// nothing on its own; this assertion is what makes the split real.
func TestAnonymousBundleNamesNoAuthenticatedEndpoint(t *testing.T) {
	var offenders []string

	for name, a := range assetIndex {
		if !strings.HasPrefix(name, "assets/"+BundleAuth+"/") {
			continue
		}
		for _, found := range apiPath.FindAllString(string(a.body), -1) {
			path := strings.TrimRight(found, "/")
			if !anonymousEndpoints[path] {
				offenders = append(offenders, name+": "+found)
			}
		}
	}

	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("the anonymous bundle names an authenticated endpoint: %s", o)
	}
}

// The reverse direction is not a security property but it is a correctness one:
// an authenticated-bundle path that nobody registered is a broken button.
func TestAppBundleReferencesLookLikeRoutes(t *testing.T) {
	for name, a := range assetIndex {
		if !strings.HasPrefix(name, "assets/"+BundleApp+"/") || !strings.HasSuffix(name, ".js") {
			continue
		}
		found := apiPath.FindAllString(string(a.body), -1)
		if len(found) < 5 {
			t.Errorf("%s references only %d API paths; is it wired up?", name, len(found))
		}
	}
}

// ---------------------------------------------------------------------------
// Asset serving
// ---------------------------------------------------------------------------

func TestAssetsServeWithTheRightType(t *testing.T) {
	h := Assets(BundleAuth, "/assets/auth/")

	for _, tc := range []struct{ file, want string }{
		{"auth.css", "text/css; charset=utf-8"},
		{"auth.js", "text/javascript; charset=utf-8"},
	} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/assets/auth/"+tc.file, nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", tc.file, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != tc.want {
			t.Errorf("%s: content type %q, want %q", tc.file, got, tc.want)
		}
		if rec.Header().Get("ETag") == "" {
			t.Errorf("%s: no ETag", tc.file)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: served without nosniff", tc.file)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s: empty", tc.file)
		}
	}
}

func TestAssetsHonourIfNoneMatch(t *testing.T) {
	h := Assets(BundleAuth, "/assets/auth/")

	first := httptest.NewRecorder()
	h(first, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/assets/auth/auth.css", nil))
	etag := first.Header().Get("ETag")

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/assets/auth/auth.css", nil)
	req.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	h(second, req)

	if second.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Error("a 304 carried a body")
	}
}

func TestAssetsRefuseAnythingButAFlatKnownName(t *testing.T) {
	h := Assets(BundleAuth, "/assets/auth/")

	for _, path := range []string{
		"/assets/auth/",                      // the bare directory
		"/assets/auth/nope.css",              // unknown name
		"/assets/auth/../app/app.js",         // traversal
		"/assets/auth/sub/dir.css",           // nested
		"/assets/auth/templates/layout.html", // outside the bundle
	} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s returned %d, want 404", path, rec.Code)
		}
	}
}

// A bundle must not be able to serve a file from the other bundle, whatever the
// URL says.
func TestAssetHandlerIsConfinedToItsBundle(t *testing.T) {
	rec := httptest.NewRecorder()
	Assets(BundleAuth, "/assets/auth/")(rec,
		httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/assets/auth/app.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("the auth bundle served an app-bundle name: %d", rec.Code)
	}
}

// Templates are embedded alongside the assets. If the index ever picked them
// up, the layout would be downloadable and, worse, servable as a document.
func TestTemplatesAreNotServable(t *testing.T) {
	for name := range assetIndex {
		if strings.Contains(name, "templates/") {
			t.Errorf("%s is in the asset index", name)
		}
	}
}

// Every view declared in the nav must exist in the shell, and every section in
// the shell must be reachable from the nav.
//
// A mismatch is silent in both directions and unpleasant in both: a nav entry
// with no section shows an empty page, and a section with no nav entry is dead
// markup that nobody notices is broken. Neither produces an error anywhere.
func TestEveryNavEntryHasASectionAndEverySectionHasANavEntry(t *testing.T) {
	js, ok := assetIndex["assets/app/app.js"]
	if !ok {
		t.Fatal("app/app.js is not in the bundle")
	}
	raw, err := files.ReadFile("templates/app.html")
	if err != nil {
		t.Fatal(err)
	}
	tmpl := string(raw)

	// The VIEWS array, which drives the nav.
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`\{\s*id:\s*'([a-z-]+)'`).FindAllStringSubmatch(string(js.body), -1) {
		declared[m[1]] = true
	}
	if len(declared) < 5 {
		t.Fatalf("only %d views were found in app.js; the VIEWS array may have moved", len(declared))
	}

	// The sections in the shell.
	present := map[string]bool{}
	for _, m := range regexp.MustCompile(`data-view="([a-z-]+)"`).FindAllStringSubmatch(tmpl, -1) {
		present[m[1]] = true
	}

	for id := range declared {
		if !present[id] {
			t.Errorf("the nav declares view %q but app.html has no section for it: "+
				"clicking that tab would show an empty page", id)
		}
	}
	for id := range present {
		if !declared[id] {
			t.Errorf("app.html has a section for %q that no nav entry reveals: "+
				"it is unreachable markup", id)
		}
	}
}

// Every view that loads data must have a loader, or its tab renders an empty
// section and nothing says why.
func TestEveryDataViewHasALoader(t *testing.T) {
	js := string(assetIndex["assets/app/app.js"].body)

	// Views that are pure forms need no loader; the rest do.
	formOnly := map[string]bool{"overview": true, "security": true}

	for _, m := range regexp.MustCompile(`\{\s*id:\s*'([a-z-]+)'`).FindAllStringSubmatch(js, -1) {
		id := m[1]
		if formOnly[id] {
			continue
		}
		if !strings.Contains(js, "loaders."+id+" =") {
			t.Errorf("view %q has no loaders.%s, so its tab would render whatever "+
				"was left in the section from last time", id, id)
		}
	}
}

// A release name, a filename and an indexer name are all written by somebody
// else. The rule that keeps them from becoming markup is that text only ever
// reaches the DOM through el() or textContent — asserted by the banned-API test
// above, and reinforced here: the media views must not introduce a template
// string that looks like markup.
func TestNoScriptBuildsMarkupFromStrings(t *testing.T) {
	for name, a := range assetIndex {
		if !strings.HasSuffix(name, ".js") {
			continue
		}
		src := stripComments(string(a.body))
		// A '<' immediately followed by a letter inside a quoted string is the
		// shape of hand-built markup.
		if m := regexp.MustCompile(`['"` + "`" + `]<[a-zA-Z]`).FindString(src); m != "" {
			t.Errorf("%s appears to build markup from a string (%q); "+
				"every node must come from document.createElement", name, m)
		}
	}
}

// A client-side validation message must not be dressed up as a failed request.
//
// The tempting shortcut is fail({status: 0}, 'pick a role first') — it reuses
// the error banner and looks harmless. It is not: failure() maps status 0 to
// "could not reach the server", so every one of those messages came out telling
// the operator their server was down when they had simply left a field blank.
// Three places had it, and a browser run found them, not review.
//
// refuse() is the correct helper. This fails the build on the shortcut.
func TestNoValidationMessageMasqueradesAsANetworkFailure(t *testing.T) {
	fake := regexp.MustCompile(`fail\(\s*\{\s*status\s*:\s*0`)
	for name, a := range assetIndex {
		if !strings.HasSuffix(name, ".js") {
			continue
		}
		if m := fake.FindString(stripComments(string(a.body))); m != "" {
			t.Errorf("%s reports a client-side validation problem as a failed "+
				"request (%q). failure() turns status 0 into \"could not reach "+
				"the server\", so the operator is told their server is down. "+
				"Use refuse(text) instead", name, m)
		}
	}
}

// The audit screen offers exactly the categories the log has: one missing is a
// kind of line nobody can filter for, and one extra is a filter the API
// refuses.
func TestTheAuditScreenOffersEveryCategory(t *testing.T) {
	raw, err := files.ReadFile("templates/app.html")
	if err != nil {
		t.Fatal(err)
	}
	sel := regexp.MustCompile(`(?s)<select id="audit-category"[^>]*>(.*?)</select>`).FindStringSubmatch(string(raw))
	if sel == nil {
		t.Fatal("app.html has no audit-category select")
	}
	var offered []string
	for _, m := range regexp.MustCompile(`<option value="([a-z]*)">`).FindAllStringSubmatch(sel[1], -1) {
		if m[1] != "" {
			offered = append(offered, m[1])
		}
	}
	want := append([]string(nil), audit.Categories...)
	sort.Strings(offered)
	sort.Strings(want)
	if strings.Join(offered, ",") != strings.Join(want, ",") {
		t.Fatalf("the screen offers %v, the log has %v", offered, want)
	}
}

// A function declared twice in one bundle is not an error in JavaScript: the
// later declaration silently replaces the earlier one everywhere, including
// before it in the file. That is how the access picker's root-folder loader
// was replaced by the Storage screen's, which returned nothing, so the picker
// offered no libraries — found in a browser, not by review (4ac).
func TestNoFunctionIsDeclaredTwice(t *testing.T) {
	decl := regexp.MustCompile(`(?m)^\s*function\s+([A-Za-z_$][\w$]*)\s*\(`)
	for name, a := range assetIndex {
		if !strings.HasSuffix(name, ".js") {
			continue
		}
		seen := map[string]int{}
		for _, m := range decl.FindAllStringSubmatch(stripComments(string(a.body)), -1) {
			seen[m[1]]++
		}
		for fn, n := range seen {
			if n > 1 {
				t.Errorf("%s declares function %s %d times; the last one silently wins", name, fn, n)
			}
		}
	}
}

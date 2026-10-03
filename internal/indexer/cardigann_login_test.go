package indexer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// privateTracker is a tracker that signs in with a form: a hidden token on
// the form, a value the definition reads off the page, a session cookie, a
// test page, and a search and a .torrent only for the signed in.
type privateTracker struct {
	mu       sync.Mutex
	logins   int
	sessions map[string]bool
	asked    []string
	posted   map[string]string
}

func (p *privateTracker) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.asked = append(p.asked, r.Method+" "+r.URL.RequestURI())
		c, _ := r.Cookie("sess")
		in := c != nil && p.sessions[c.Value]
		switch r.URL.Path {
		case "/login.php":
			_, _ = w.Write([]byte(`<html><body><span class="token" data-v="page-xyz"></span>
				<form id="login" action="takelogin.php" method="post">
				<input type="hidden" name="csrf" value="tok123"><input name="username"><input type="password" name="password">
				<input type="checkbox" name="remember"><input type="submit" name="go" value="Sign in"></form></body></html>`))
		case "/takelogin.php":
			_ = r.ParseForm()
			p.posted = map[string]string{}
			for k := range r.PostForm {
				p.posted[k] = r.PostForm.Get(k)
			}
			if r.PostForm.Get("username") != "jacob" || r.PostForm.Get("password") != "s3cret" ||
				r.PostForm.Get("csrf") != "tok123" {
				_, _ = w.Write([]byte(`<div class="error">Wrong username or password</div>`))
				return
			}
			p.logins++
			id := "s" + strings.Repeat("x", p.logins)
			p.sessions[id] = true
			http.SetCookie(w, &http.Cookie{Name: "sess", Value: id, Path: "/"})
			http.Redirect(w, r, "/index.php", http.StatusFound)
		case "/index.php":
			if !in {
				http.Redirect(w, r, "/login.php", http.StatusFound)
				return
			}
			_, _ = w.Write([]byte(`<a href="/logout.php">Log out</a>`))
		case "/browse.php":
			if !in {
				http.Redirect(w, r, "/login.php", http.StatusFound)
				return
			}
			_, _ = w.Write([]byte(`<table><tr><td><a class="n" href="/t/1">Heat.1995.1080p.BluRay.x264-GRP</a></td>
				<td><a class="dl" href="/dl/1.torrent">get</a></td></tr></table>`))
		case "/t/1":
			if !in {
				http.Redirect(w, r, "/login.php", http.StatusFound)
				return
			}
			_, _ = w.Write([]byte(`<div class="dl"><a class="real" href="/dl/1.torrent">Download</a></div>`))
		case "/dl/1.torrent":
			if !in {
				http.Error(w, "sign in", http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte("d4:infod4:name4:heatee"))
		default:
			http.NotFound(w, r)
		}
	})
}

const privateDefinition = `id: private
name: Private
links: [https://private.example/]
settings:
  - {name: username, type: text}
  - {name: password, type: password}
  - {name: info, type: info, label: "Use your own account"}
login:
  path: login.php
  method: form
  form: form#login
  inputs:
    username: "{{ .Config.username }}"
    password: "{{ .Config.password }}"
  selectorinputs:
    page: {selector: span.token, attribute: data-v}
  error:
    - selector: div.error
  test:
    path: index.php
    selector: a[href*="logout"]
search:
  paths:
    - path: browse.php
  inputs:
    q: "{{ .Keywords }}"
  rows:
    selector: tr
  fields:
    title: {selector: a.n}
    details: {selector: a.n, attribute: href}
    download: {selector: a.dl, attribute: href}
`

func privateRig(t *testing.T) (*privateTracker, *Client, Definition) {
	t.Helper()
	p := &privateTracker{sessions: map[string]bool{}}
	srv := httptest.NewServer(p.handler(t))
	t.Cleanup(srv.Close)
	d := Definition{ID: 9, Name: "Private", Kind: KindCardigann, BaseURL: srv.URL, Cardigann: privateDefinition,
		Settings: map[string]string{"username": "jacob", "password": "s3cret"}, Enabled: true}
	return p, NewClientWithDoer(srv.Client()), d
}

// TestATrackerIsSignedInToAsItsDefinitionSays pins ADR-0059, decisions 2 and
// 4: the form filled in and submitted, the session kept, the test page
// checked, a lapsed session signed in again, the grab through the session.
func TestATrackerIsSignedInToAsItsDefinitionSays(t *testing.T) {
	p, c, d := privateRig(t)
	got, err := c.Search(t.Context(), d, Query{Term: "Heat", Season: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Heat.1995.1080p.BluRay.x264-GRP" {
		t.Fatalf("results %+v", got)
	}
	p.mu.Lock()
	want := map[string]string{"csrf": "tok123", "username": "jacob", "password": "s3cret", "page": "page-xyz"}
	for k, v := range want {
		if p.posted[k] != v {
			t.Errorf("posted %s=%q, want %q (all: %v)", k, p.posted[k], v, p.posted)
		}
	}
	if _, ok := p.posted["remember"]; ok {
		t.Error("an unchecked box was sent")
	}
	if _, ok := p.posted["go"]; ok {
		t.Error("the submit button was sent")
	}
	if strings.Join(p.asked, ",") != "GET /login.php,POST /takelogin.php,GET /index.php,GET /index.php,GET /browse.php?q=Heat" {
		t.Errorf("asked %v", p.asked)
	}
	p.asked = nil
	p.mu.Unlock()

	// Signed in already: no second sign-in.
	if _, err := c.Search(t.Context(), d, Query{Term: "Heat", Season: -1}); err != nil {
		t.Fatal(err)
	}
	// The tracker forgets the session: the search lands on the login page,
	// and is asked again after signing in once more.
	p.mu.Lock()
	p.sessions = map[string]bool{}
	p.mu.Unlock()
	if got, err := c.Search(t.Context(), d, Query{Term: "Heat", Season: -1}); err != nil || len(got) != 1 {
		t.Fatalf("after the session lapsed: %v %v", got, err)
	}
	payload, err := c.Download(t.Context(), d, got[0].DownloadURL)
	if err != nil || !strings.HasPrefix(string(payload.Torrent), "d4:info") {
		t.Errorf("the grab: %q %v", payload.Torrent, err)
	}
	p.mu.Lock()
	if p.logins != 2 {
		t.Errorf("%d sign-ins, want 2", p.logins)
	}
	p.mu.Unlock()

	// New settings are a new session.
	d.Settings = map[string]string{"username": "jacob", "password": "wrong"}
	if _, err := c.Search(t.Context(), d, Query{Term: "Heat", Season: -1}); err == nil ||
		!strings.Contains(err.Error(), "Wrong username or password") {
		t.Errorf("a wrong password: %v", err)
	}
	// With no error selector to say so, the test page is what tells a
	// sign-in that did not work.
	d.Cardigann = strings.Replace(privateDefinition, "  error:\n    - selector: div.error\n", "", 1)
	if _, err := c.Search(t.Context(), d, Query{Term: "Heat", Season: -1}); err == nil ||
		!strings.Contains(err.Error(), "shows no sign of it") {
		t.Errorf("a silent failure: %v", err)
	}
}

// TestCredentialsGoOnlyToTheTrackersOwnAddress pins ADR-0059, decision 3.
func TestCredentialsGoOnlyToTheTrackersOwnAddress(t *testing.T) {
	_, c, d := privateRig(t)
	var leaked bool
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked = true }))
	defer evil.Close()
	for name, def := range map[string]string{
		"a login path elsewhere":    strings.Replace(privateDefinition, "  path: login.php", "  path: "+evil.URL+"/login.php", 1),
		"a submitpath elsewhere":    strings.Replace(privateDefinition, "  form: form#login", "  form: form#login\n  submitpath: "+evil.URL+"/collect", 1),
		"a post elsewhere":          strings.Replace(strings.Replace(privateDefinition, "method: form", "method: post", 1), "  path: login.php", "  path: "+evil.URL+"/x", 1),
		"a test page elsewhere":     strings.Replace(privateDefinition, "    path: index.php", "    path: "+evil.URL+"/index.php", 1),
		"another scheme, same host": strings.Replace(privateDefinition, "  path: login.php", "  path: "+strings.Replace(d.BaseURL, "http:", "https:", 1)+"/login.php", 1),
	} {
		dd := d
		dd.ID, dd.Cardigann = int64(len(name)), def
		if _, err := c.Search(t.Context(), dd, Query{Term: "Heat", Season: -1}); !errors.Is(err, ErrUnsafeURL) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if leaked {
		t.Error("the other address was asked")
	}
}

// TestACookieOrAPostSignsIn pins the other methods, and that a setting the
// definition does not declare is refused.
func TestACookieOrAPostSignsIn(t *testing.T) {
	p, c, d := privateRig(t)
	p.sessions["from-browser"] = true
	d.Cardigann = strings.Replace(strings.Replace(privateDefinition, "method: form", "method: cookie", 1),
		"  - {name: password, type: password}", "  - {name: password, type: password}\n  - {name: cookie, type: text}", 1)
	d.Settings = map[string]string{"cookie": "sess=from-browser; other=1"}
	if got, err := c.Search(t.Context(), d, Query{Term: "Heat", Season: -1}); err != nil || len(got) != 1 {
		t.Fatalf("by cookie: %v %v", got, err)
	}
	p.mu.Lock()
	if p.logins != 0 || p.posted != nil {
		t.Errorf("the cookie method signed in by form: %d", p.logins)
	}
	p.mu.Unlock()

	d.ID = 10
	d.Cardigann = strings.Replace(strings.Replace(privateDefinition, "method: form", "method: post", 1),
		"  path: login.php", "  path: takelogin.php", 1)
	d.Cardigann = strings.Replace(d.Cardigann, `    password: "{{ .Config.password }}"`,
		`    password: "{{ .Config.password }}"`+"\n    csrf: tok123", 1)
	d.Settings = map[string]string{"username": "jacob", "password": "s3cret"}
	if got, err := c.Search(context.Background(), d, Query{Term: "Heat", Season: -1}); err != nil || len(got) != 1 {
		t.Fatalf("by post: %v %v", got, err)
	}

	bad := d
	bad.Settings = map[string]string{"username": "jacob", "passwrod": "x", "info": "y"}
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "no setting info, passwrod") {
		t.Errorf("undeclared settings: %v", err)
	}
}

// Signing in through the production client, whose guard is wired: found on
// the running binary, where starting a session took the client's lock twice
// and hung every search of a signing-in tracker.
func TestASigningInTrackerIsSearchedThroughTheGuardedClient(t *testing.T) {
	p, _, d := privateRig(t)
	c := productionClient(t)
	done := make(chan error, 1)
	go func() {
		got, err := c.Search(t.Context(), d, Query{Term: "Heat", Season: -1})
		if err == nil && len(got) != 1 {
			err = errors.New("no result")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the search hung")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.logins != 1 {
		t.Errorf("%d sign-ins", p.logins)
	}
}

// TestADownloadLinkIsReadFromTheDetailsPage pins ADR-0060: the details page
// fetched when a release is grabbed, signed in, its selectors tried in turn,
// and what it links to checked as any download link.
func TestADownloadLinkIsReadFromTheDetailsPage(t *testing.T) {
	p, c, d := privateRig(t)
	d.Cardigann = strings.Replace(privateDefinition, "search:\n", "download:\n  selectors:\n"+
		"    - {selector: 'a[href^=\"magnet:\"]', attribute: href}\n"+
		"    - {selector: div.dl a.real, attribute: href, filters: [{name: append, args: \"?passkey=1\"}]}\nsearch:\n", 1)
	d.Cardigann = strings.Replace(d.Cardigann, "    download: {selector: a.dl, attribute: href}", "    download: {selector: a.n, attribute: href}", 1)
	got, err := c.Search(t.Context(), d, Query{Term: "Heat", Season: -1})
	if err != nil || len(got) != 1 || !strings.HasSuffix(got[0].DownloadURL, "/t/1") {
		t.Fatalf("search: %+v %v", got, err)
	}
	p.mu.Lock()
	p.asked = nil
	p.mu.Unlock()
	payload, err := c.Download(t.Context(), d, got[0].DownloadURL)
	if err != nil || !strings.HasPrefix(string(payload.Torrent), "d4:info") {
		t.Fatalf("the grab: %q %v", payload.Torrent, err)
	}
	p.mu.Lock()
	if strings.Join(p.asked, ",") != "GET /t/1,GET /dl/1.torrent?passkey=1" {
		t.Errorf("asked %v", p.asked)
	}
	p.mu.Unlock()

	for name, tc := range map[string]struct {
		page string
		want error
		says string
	}{
		"a magnet":                       {`<a href="magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567">m</a>`, nil, ""},
		"the metadata":                   {`<div class="dl"><a class="real" href="http://169.254.169.254/x">d</a></div>`, ErrUnsafeURL, "169.254.169.254"},
		"nothing found":                  {`<p>gone</p>`, ErrIndexerRefused, "no download link"},
		"a details link to the metadata": {"", ErrUnsafeURL, "169.254.169.254"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.page)) }))
		pub := Definition{ID: 99, Name: "Pub", Kind: KindCardigann, BaseURL: srv.URL, Cardigann: strings.Replace(
			publicDefinition, "search:\n", "download:\n  selectors:\n    - {selector: 'a[href^=\"magnet:\"]', attribute: href}\n"+
				"    - {selector: div.dl a.real, attribute: href}\nsearch:\n", 1)}
		link := srv.URL + "/t/7"
		if tc.page == "" {
			link = "http://169.254.169.254/t/7"
		}
		payload, err := NewClientWithDoer(srv.Client()).Download(t.Context(), pub, link)
		switch {
		case tc.want == nil && (err != nil || !strings.HasPrefix(payload.Magnet, "magnet:?xt=urn:btih:")):
			t.Errorf("%s: %+v %v", name, payload, err)
		case tc.want != nil && (!errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.says)):
			t.Errorf("%s: %v", name, err)
		}
		srv.Close()
	}
}

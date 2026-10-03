package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/release"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// One quality profile judges every search unless another is chosen (ADR-0027),
// through the real router, the real profile store seeded as a new instance is,
// the real search service reading a real feed, and real tickets.

const profileFeed = `<?xml version="1.0"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
<channel>
<item><title>Dune.2021.HDCAM.x264-CAMGRP</title>
<enclosure url="https://indexer.example.com/dl/cam.torrent" length="1500000000"/>
<torznab:attr name="seeders" value="900"/></item>
<item><title>Dune.2021.2160p.WEB-DL.DDP5.1.HDR.H.265-GRP</title>
<enclosure url="https://indexer.example.com/dl/uhd.torrent" length="20000000000"/>
<torznab:attr name="seeders" value="500"/></item>
<item><title>Dune.2021.1080p.BluRay.x264-GRP</title>
<enclosure url="https://indexer.example.com/dl/hd.torrent" length="12000000000"/>
<torznab:attr name="seeders" value="50"/></item>
</channel></rss>`

func (r *rig) profileSearchable(t *testing.T) {
	t.Helper()
	client := indexer.NewClientWithDoer(doerFunc(func(req *http.Request) (*http.Response, error) {
		body := profileFeed
		if strings.Contains(req.URL.Path, "/dl/") {
			body = grabTorrent
		}
		return &http.Response{StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	}))
	searcher := search.New(r.indexers, client, r.indexers)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auth := NewSessionAuthenticator(r.store, r.svc.Policy().Session, false)
	rt := NewRouter(
		[]Middleware{Recovery(logger), RequestContext(logger),
			ClientIPResolver(nil), SecurityHeaders(time.Hour)},
		[]Middleware{CSRF(), Authenticate(auth, r.audit)},
	)
	RegisterRoutes(rt, New(Deps{
		Identity: r.svc, Auth: auth, Egress: r.egress, Indexers: r.indexers,
		Search: searcher, Profiles: r.profiles, Downloads: r.downloads,
		Roots: r.roots, Media: r.media, Scanner: r.scanner, Deleter: r.scanner,
		Tickets: r.tickets, Grabs: searcher, Requests: r.requests, Audit: r.audit,
		TrashRetention: 7 * 24 * time.Hour,
	}))
	r.rt = rt
}

// rawJSON sends a body exactly as written, for the cases a map cannot spell.
func rawJSON(s string) json.RawMessage { return json.RawMessage(s) }

// grabbable names the candidates of a search answer that carry a ticket.
func grabbable(t *testing.T, res response) []string {
	t.Helper()
	if res.Code != http.StatusOK {
		t.Fatalf("search: %d %s", res.Code, res.Raw)
	}
	var out []string
	for _, c := range res.Body["candidates"].([]any) {
		m := c.(map[string]any)
		if _, ok := m["ticket"]; ok {
			out = append(out, m["title"].(string))
		}
	}
	return out
}

func profileID(t *testing.T, admin *client, name string) int64 {
	t.Helper()
	res := admin.get("/api/v1/quality-profiles")
	for _, p := range res.Body["profiles"].([]any) {
		m := p.(map[string]any)
		if m["name"] == name {
			return int64(m["id"].(float64))
		}
	}
	t.Fatalf("no profile %q: %s", name, res.Raw)
	return 0
}

// A search that names no profile is judged by the default; 0 is none; an id is
// that profile — the same rule the episode and film searches read.
func TestEverySearchIsJudgedByTheDefaultUnlessAnotherIsChosen(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addIndexer(admin, "Tracker")
	r.profileSearchable(t)

	res := admin.post("/api/v1/releases/search", map[string]any{"term": "dune 2021"})
	if got := grabbable(t, res); len(got) != 1 || got[0] != "Dune.2021.1080p.BluRay.x264-GRP" {
		t.Errorf("with no profile named, grabbable = %v; the default HD-1080p refuses the "+
			"camera recording and the 2160p release", got)
	}
	if res.Body["profile"] != release.DefaultProfileName || res.Body["profile_default"] != true {
		t.Errorf("the answer does not say it was judged by the default: %v %v",
			res.Body["profile"], res.Body["profile_default"])
	}

	res = admin.post("/api/v1/releases/search", map[string]any{"term": "dune 2021", "profile_id": 0})
	if got := grabbable(t, res); len(got) != 3 {
		t.Errorf("with profile 0, grabbable = %v; want all three — the person judges", got)
	}
	if res.Body["profile"] != "" || res.Body["profile_default"] != false {
		t.Errorf("unjudged, the answer says %v %v", res.Body["profile"], res.Body["profile_default"])
	}
	if _, ok := res.Body["profile_note"]; ok {
		t.Error("a note about a missing default when none was asked for")
	}

	uhd := profileID(t, admin, "Ultra-HD")
	res = admin.post("/api/v1/releases/search", map[string]any{"term": "dune 2021", "profile_id": uhd})
	got := grabbable(t, res)
	if len(got) != 2 || strings.Contains(strings.Join(got, "|"), "HDCAM") {
		t.Errorf("with Ultra-HD, grabbable = %v; want the 2160p and the 1080p BluRay", got)
	}
	if res.Body["profile"] != "Ultra-HD" || res.Body["profile_default"] != false {
		t.Errorf("chosen: %v %v", res.Body["profile"], res.Body["profile_default"])
	}

	for body, want := range map[string]int{`{"term":"x","profile_id":999}`: http.StatusNotFound,
		`{"term":"x","profile_id":-1}`: http.StatusBadRequest} {
		if res := admin.do(http.MethodPost, "/api/v1/releases/search", rawJSON(body)); res.Code != want {
			t.Errorf("%s: %d, want %d", body, res.Code, want)
		}
	}
}

// With no default, a search that names no profile is unjudged — and says so.
func TestWithNoDefaultASearchIsUnjudgedAndSaysSo(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addIndexer(admin, "Tracker")
	r.profileSearchable(t)

	if res := admin.do(http.MethodPut, "/api/v1/admin/quality-profiles/default",
		map[string]any{"profile_id": 0}); res.Code != http.StatusOK || res.Body["default"] != "none" {
		t.Fatalf("clearing the default: %d %s", res.Code, res.Raw)
	}
	res := admin.post("/api/v1/releases/search", map[string]any{"term": "dune 2021"})
	if got := grabbable(t, res); len(got) != 3 {
		t.Errorf("grabbable = %v, want all three", got)
	}
	if note, _ := res.Body["profile_note"].(string); !strings.Contains(note, "no default profile") {
		t.Errorf("profile_note = %q", note)
	}
}

// Choosing the default is an administrator's, it shows in the listing, and it
// is audited with what it was and what it became.
func TestChoosingTheDefaultIsAnAdministratorsAndIsAudited(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	hd720 := profileID(t, admin, "HD-720p")

	res := admin.do(http.MethodPut, "/api/v1/admin/quality-profiles/default", map[string]any{"profile_id": hd720})
	if res.Code != http.StatusOK || res.Body["default"] != "HD-720p" || res.Body["previous"] != release.DefaultProfileName {
		t.Fatalf("setting the default: %d %s", res.Code, res.Raw)
	}
	list := admin.get("/api/v1/quality-profiles")
	if list.Body["default_id"] != float64(hd720) {
		t.Errorf("default_id = %v, want %d", list.Body["default_id"], hd720)
	}
	defaults := 0
	for _, p := range list.Body["profiles"].([]any) {
		if p.(map[string]any)["default"] == true {
			defaults++
		}
	}
	if defaults != 1 {
		t.Errorf("%d profiles say they are the default", defaults)
	}

	events, err := r.audit.List(authz.WithPrincipal(t.Context(), adminPrincipalFor(t, r)),
		audit.Query{Action: audit.ActionSystemSettingChanged})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Detail != "the default quality profile is now HD-720p (was HD-1080p)" {
		t.Errorf("audit = %+v", events)
	}

	for body, want := range map[string]int{`{}`: http.StatusBadRequest,
		`{"profile_id":999}`: http.StatusNotFound} {
		if res := admin.do(http.MethodPut, "/api/v1/admin/quality-profiles/default", rawJSON(body)); res.Code != want {
			t.Errorf("%s: %d, want %d", body, res.Code, want)
		}
	}

	// A Manager is not told the route exists.
	code, _ := r.issueInvite(admin, authz.RoleManager, true)
	manager := r.redeemAndEnroll(code, "manager", "another-perfectly-fine-passphrase")
	if res := manager.do(http.MethodPut, "/api/v1/admin/quality-profiles/default",
		map[string]any{"profile_id": 0}); res.Code != http.StatusNotFound {
		t.Errorf("a Manager: %d %s, want 404", res.Code, res.Raw)
	}
	if list := admin.get("/api/v1/quality-profiles"); list.Body["default_id"] != float64(hd720) {
		t.Errorf("a refused change moved the default: %v", list.Body["default_id"])
	}
}

// ---------------------------------------------------------------------------
// The targeted searches read the same rule (unit level, with fakes)
// ---------------------------------------------------------------------------

type fakeProfiles struct {
	byID  map[int64]release.StoredProfile
	def   int64
	asked []string
}

func twoProfiles() *fakeProfiles {
	hd := release.StoredProfile{ID: 1, Default: true, Profile: release.Profile{Name: "HD-1080p",
		Allowed: []string{"Bluray-1080p"}, Cutoff: "Bluray-1080p"}}
	anyProfile := release.StoredProfile{ID: 4, Profile: release.Profile{Name: "Any",
		Allowed: []string{"CAM", "Bluray-1080p"}, Cutoff: "Bluray-1080p"}}
	return &fakeProfiles{byID: map[int64]release.StoredProfile{1: hd, 4: anyProfile}, def: 1}
}

func (f *fakeProfiles) Get(_ context.Context, id int64) (release.StoredProfile, error) {
	f.asked = append(f.asked, "get")
	sp, ok := f.byID[id]
	if !ok {
		return release.StoredProfile{}, release.ErrProfileNotFound
	}
	return sp, nil
}
func (f *fakeProfiles) List(context.Context) ([]release.StoredProfile, error) { return nil, nil }
func (f *fakeProfiles) Default(context.Context) (release.StoredProfile, bool, error) {
	f.asked = append(f.asked, "default")
	sp, ok := f.byID[f.def]
	return sp, ok, nil
}
func (f *fakeProfiles) SetDefault(context.Context, int64) (string, error) { return "", nil }

// The film search: absent is the default, 0 is none, an id is that profile.
func TestTheFilmSearchReadsTheProfileByTheSameRule(t *testing.T) {
	for _, tc := range []struct {
		body   string
		want   string // the profile's name the search was given, "" for none
		isDflt bool
		asked  string
	}{
		{`{}`, "HD-1080p", true, "default"},
		{`{"profile_id":0}`, "", false, ""},
		{`{"profile_id":4}`, "Any", false, "get"},
	} {
		s := &fakeEpisodeSearch{}
		profiles := twoProfiles()
		h := &Handlers{search: s, media: duneLibrary(), profiles: profiles}
		code, body := filmSearch(t, h, "7", tc.body)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d %v", tc.body, code, body)
		}
		got := ""
		if s.gotFilm.Profile != nil {
			got = s.gotFilm.Profile.Name
		}
		if got != tc.want || body["profile"] != tc.want || body["profile_default"] != tc.isDflt {
			t.Errorf("%s: searched with %q, answered %v/%v; want %q/%v",
				tc.body, got, body["profile"], body["profile_default"], tc.want, tc.isDflt)
		}
		if strings.Join(profiles.asked, ",") != tc.asked {
			t.Errorf("%s: the store was asked %v, want %q", tc.body, profiles.asked, tc.asked)
		}
	}
}

// The episode search too.
func TestTheEpisodeSearchReadsTheProfileByTheSameRule(t *testing.T) {
	s := &fakeEpisodeSearch{}
	h := &Handlers{search: s, episodes: &fakeEpisodes{subject: severanceSubject()}, profiles: twoProfiles()}
	if code, body := episodeSearch(t, h, "77", `{}`); code != http.StatusOK ||
		s.got.Profile == nil || s.got.Profile.Name != "HD-1080p" || body["profile_default"] != true {
		t.Errorf("absent: %d, searched with %+v, %v", code, s.got.Profile, body)
	}
	if code, _ := episodeSearch(t, h, "77", `{"profile_id":0}`); code != http.StatusOK || s.got.Profile != nil {
		t.Errorf("0: %d, searched with %+v", code, s.got.Profile)
	}
	if code, _ := episodeSearch(t, h, "77", `{"profile_id":2}`); code != http.StatusNotFound {
		t.Errorf("an unknown profile: %d, want 404", code)
	}
}

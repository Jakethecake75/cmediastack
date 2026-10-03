package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/follow"
	"github.com/jakethecake75/cmediastack/internal/identify"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/search"
	"github.com/jakethecake75/cmediastack/internal/tv"
)

// The whole of ADR-0026 through the real router: add a film, search for it with
// the real search service reading a real feed, grab with a real sealed ticket,
// and see the film reach the download queue. Only the network, the torrent
// engine and the metadata provider are canned.

const filmFeed = `<?xml version="1.0"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
<channel>
<item><title>Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1-GRP</title>
<enclosure url="https://indexer.example.com/dl/two.torrent" length="20000000000"/>
<torznab:attr name="seeders" value="900"/></item>
<item><title>Dune.Prophecy.S01E01.1080p.WEB.H264-GRP</title>
<enclosure url="https://indexer.example.com/dl/prophecy.torrent" length="3000000000"/>
<torznab:attr name="seeders" value="800"/></item>
<item><title>Dune.1984.1080p.BluRay.x264-OLD</title>
<enclosure url="https://indexer.example.com/dl/1984.torrent" length="9000000000"/>
<torznab:attr name="seeders" value="700"/></item>
<item><title>Dune.1080p.WEB.H264-NOYEAR</title>
<enclosure url="https://indexer.example.com/dl/noyear.torrent" length="4000000000"/>
<torznab:attr name="seeders" value="600"/></item>
<item><title>Dune.Part.One.2021.1080p.BluRay.x264-GRP</title>
<enclosure url="https://indexer.example.com/dl/one.torrent" length="12000000000"/>
<torznab:attr name="seeders" value="5"/></item>
</channel></rss>`

type staticFilmTitles []string

func (s staticFilmTitles) FilmTitles(context.Context, int64) ([]string, error) { return s, nil }

// filmReady rebuilds the router with the follow service, the search service on
// the canned feed, and the film's names; and gives the instance a films root.
func (r *rig) filmReady(t *testing.T) {
	t.Helper()
	ctx := authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 1, Name: "Admin", Rank: 100,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
	})
	dir := filepath.Join(t.TempDir(), "films")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.roots.Create(ctx, dir, library.KindMovies, "Films"); err != nil {
		t.Fatal(err)
	}

	var asked []string
	client := indexer.NewClientWithDoer(doerFunc(func(req *http.Request) (*http.Response, error) {
		body := filmFeed
		if strings.Contains(req.URL.Path, "/dl/") {
			body = grabTorrent
		} else {
			asked = append(asked, req.URL.Query().Get("t")+" "+req.URL.Query().Get("q"))
		}
		return &http.Response{StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	}))
	t.Cleanup(func() {
		for _, a := range asked {
			if a != "search dune 2021" {
				t.Errorf("an indexer was asked %q, want the general search for \"dune 2021\"", a)
			}
		}
	})
	searcher := search.New(r.indexers, client, r.indexers)
	episodes := library.NewEpisodeStore(r.database, r.clk.now)
	provider := &cannedSeries{}
	adder := follow.NewService(r.database, r.media, identify.NewStore(r.database, r.clk.now),
		episodes, r.roots, func() tv.EpisodeProvider { return provider }, r.audit,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

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
		Episodes: episodes, Adder: adder, TrashRetention: 7 * 24 * time.Hour,
		FilmTitles: staticFilmTitles{"Dune", "Dune", "Dune: Part One"},
	}))
	r.rt = rt
}

func TestAddingAFilmThroughTheAPIPutsItOnTheWantedList(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.filmReady(t)

	res := admin.post("/api/v1/media", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if res.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", res.Code, res.Raw)
	}
	item, _ := res.Body["item"].(map[string]any)
	if item["title"] != "Dune" || item["kind"] != "movie" || item["folder"] != "Dune (2021)" {
		t.Errorf("item = %v", item)
	}
	if _, ok := res.Body["monitor"]; ok {
		t.Errorf("a film's answer carries a monitoring choice: %s", res.Raw)
	}
	if note, _ := res.Body["note"].(string); !strings.Contains(note, "Wanted list") {
		t.Errorf("note = %q", note)
	}

	wanted := admin.get("/api/v1/wanted")
	films, _ := wanted.Body["films"].([]any)
	if len(films) != 1 || films[0].(map[string]any)["name"] != "Dune (2021)" {
		t.Errorf("wanted films = %v", wanted.Body["films"])
	}
}

// A film is kept without being wanted through the router (ADR-0030) — by
// someone who may edit the library, and by nobody else.
func TestAFilmIsUnmonitoredThroughTheAPI(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.filmReady(t)
	added := admin.post("/api/v1/media", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if added.Code != http.StatusCreated || added.Body["item"].(map[string]any)["monitored"] != true {
		t.Fatalf("add: %d %s", added.Code, added.Raw)
	}
	id := strconv.FormatInt(int64(added.Body["item"].(map[string]any)["id"].(float64)), 10)
	path := "/api/v1/media/" + id + "/monitored"

	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	user := r.redeemAndEnroll(code, "friend", "invited-passphrase-1")
	if res := user.do(http.MethodPut, path, map[string]any{"monitored": false}); res.Code == http.StatusOK {
		t.Fatalf("a user who may not edit the library unmonitored a film: %s", res.Raw)
	}

	res := admin.do(http.MethodPut, path, map[string]any{"monitored": false})
	if res.Code != http.StatusOK || res.Body["monitored"] != false {
		t.Fatalf("unmonitor: %d %s", res.Code, res.Raw)
	}
	wanted := admin.get("/api/v1/wanted")
	if films, _ := wanted.Body["films"].([]any); len(films) != 0 {
		t.Fatalf("an unmonitored film is wanted: %s", wanted.Raw)
	}
	if auto, _ := wanted.Body["automatic"].(map[string]any); auto["enabled"] != false {
		t.Fatalf("automatic = %v with no acquisition wired", auto)
	}
	if item := admin.get("/api/v1/media/" + id); item.Body["monitored"] != false {
		t.Fatalf("the film reads back %v", item.Body["monitored"])
	}
}

// The chain ADR-0026 rests on: of five releases the indexers offer for "Dune",
// only the one that IS Dune (2021) gets a ticket — though it has the fewest
// seeders and the scene calls it "Part One" — and the film reaches the queue
// from the sealed ticket.
func TestAFilmSearchGrabsIntoThatFilm(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addIndexer(admin, "Tracker")
	r.filmReady(t)

	added := admin.post("/api/v1/media", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if added.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", added.Code, added.Raw)
	}
	id := int64(added.Body["item"].(map[string]any)["id"].(float64))

	res := admin.post("/api/v1/media/"+strconv.FormatInt(id, 10)+"/search", map[string]any{})
	if res.Code != http.StatusOK {
		t.Fatalf("search: %d %s", res.Code, res.Raw)
	}
	cands, _ := res.Body["candidates"].([]any)
	if len(cands) != 5 {
		t.Fatalf("%d candidates, want all five back with verdicts\n%s", len(cands), res.Raw)
	}
	var ticket string
	reasons := map[string]string{}
	for _, c := range cands {
		m := c.(map[string]any)
		if tk, ok := m["ticket"].(string); ok {
			if ticket != "" {
				t.Errorf("a second ticket, for %v", m["title"])
			}
			ticket = tk
			if m["title"] != "Dune.Part.One.2021.1080p.BluRay.x264-GRP" {
				t.Errorf("a ticket was issued for %v", m["title"])
			}
			continue
		}
		reason, _ := m["rejection_reason"].(string)
		reasons[m["title"].(string)] = reason
	}
	for title, want := range map[string]string{
		"Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1-GRP": search.ReasonNotThisFilm,
		"Dune.Prophecy.S01E01.1080p.WEB.H264-GRP":    search.ReasonTelevision,
		"Dune.1984.1080p.BluRay.x264-OLD":            search.ReasonNotThisFilm,
		"Dune.1080p.WEB.H264-NOYEAR":                 search.ReasonNoYear,
	} {
		if reasons[title] != want {
			t.Errorf("%s refused as %q, want %q", title, reasons[title], want)
		}
	}
	if first := cands[0].(map[string]any); first["matches"] != true {
		t.Errorf("the match does not lead the list despite having the fewest seeders: %v", first["title"])
	}
	if ticket == "" {
		t.Fatal("no ticket for the film")
	}

	grab := admin.post("/api/v1/releases/grab", map[string]any{"ticket": ticket})
	if grab.Code != http.StatusAccepted {
		t.Fatalf("grab: %d %s", grab.Code, grab.Raw)
	}
	if f, _ := grab.Body["for"].(map[string]any); f == nil || f["label"] != "Dune (2021)" || f["kind"] != "film" {
		t.Errorf("the grab does not say it is for the film: %v", grab.Body["for"])
	}

	r.downloads.mu.Lock()
	defer r.downloads.mu.Unlock()
	if len(r.downloads.meta) != 1 {
		t.Fatalf("%d transfers added", len(r.downloads.meta))
	}
	if got := r.downloads.meta[0].Target; got == nil || *got != (download.Target{ItemID: id, Film: true}) {
		t.Errorf("the queue was handed %+v, want film %d — the import would file this "+
			"by guessing the film from the release name", got, id)
	}
}

// Somebody who may browse but not search cannot search for a film either; a
// series is not searched for as one.
func TestSearchingForAFilmNeedsThePermissionAndAFilm(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addIndexer(admin, "Tracker")
	r.filmReady(t)
	added := admin.post("/api/v1/media", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if added.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", added.Code, added.Raw)
	}
	id := strconv.FormatInt(int64(added.Body["item"].(map[string]any)["id"].(float64)), 10)

	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	viewer := r.redeemAndEnroll(code, "viewer", "a-perfectly-fine-passphrase")
	if res := viewer.post("/api/v1/media/"+id+"/search", map[string]any{}); res.Code == http.StatusOK {
		t.Fatalf("a User searched the indexers for a film: %s", res.Raw)
	}

	// A series, added the usual way, is not a film.
	tvDir := filepath.Join(t.TempDir(), "tv")
	if err := os.MkdirAll(tvDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if res := admin.post("/api/v1/admin/rootfolders", map[string]any{
		"path": tvDir, "kind": "series", "label": "Shows"}); res.Code != http.StatusCreated {
		t.Fatalf("root: %d %s", res.Code, res.Raw)
	}
	series := admin.post("/api/v1/media", map[string]any{"kind": "series", "tmdb_id": 95396, "monitor": "none"})
	if series.Code != http.StatusCreated {
		t.Fatalf("add series: %d %s", series.Code, series.Raw)
	}
	sid := strconv.FormatInt(int64(series.Body["item"].(map[string]any)["id"].(float64)), 10)
	if res := admin.post("/api/v1/media/"+sid+"/search", map[string]any{}); res.Code != http.StatusConflict ||
		!strings.Contains(res.Raw, "that is a series") {
		t.Errorf("a series searched for as a film: %d %s", res.Code, res.Raw)
	}
}

// The queue names what each grab was for: a film by its title and year, an
// episode by its series and code, and one whose item is gone says so.
func TestTheQueueNamesWhatEachGrabWasFor(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.filmReady(t)
	added := admin.post("/api/v1/media", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if added.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", added.Code, added.Raw)
	}
	id := int64(added.Body["item"].(map[string]any)["id"].(float64))

	ctx := authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 1, Name: "Admin", Rank: 100,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
	})
	roots, err := r.roots.List(ctx)
	if err != nil || len(roots) == 0 {
		t.Fatalf("roots: %v %v", roots, err)
	}
	severance, err := r.media.UpsertItem(ctx, importer.Item{Kind: importer.KindSeries,
		Title: "Severance", Year: 2022, RootFolderID: roots[0].ID, Folder: "Severance"})
	if err != nil {
		t.Fatal(err)
	}

	const film, gone, episode, followed = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccccccccccccccccccc",
		"dddddddddddddddddddddddddddddddddddddddd"
	r.downloads.records = []download.Record{
		{InfoHash: followed, Title: "Severance.S02E04.1080p.WEB.H264-GRP", Status: download.StatusComplete,
			Target: &download.Target{ItemID: severance.ID, Season: 2, Episode: 4}},
		{InfoHash: film, Title: "Dune.Part.One.2021.1080p.BluRay.x264-GRP", Status: download.StatusDownloading,
			Target: &download.Target{ItemID: id, Film: true}},
		{InfoHash: gone, Title: "Arrival.2016.1080p.BluRay.x264-GRP", Status: download.StatusComplete,
			Target: &download.Target{ItemID: 999, Film: true}},
		{InfoHash: episode, Title: "Severance.S02E03.1080p.WEB.H264-GRP", Status: download.StatusComplete,
			Target: &download.Target{ItemID: 998, Season: 2, Episode: 3}},
	}
	res := admin.get("/api/v1/queue")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d\nbody: %s", res.Code, res.Raw)
	}
	labels := map[string]map[string]any{}
	for _, it := range res.Body["items"].([]any) {
		m := it.(map[string]any)
		f, _ := m["for"].(map[string]any)
		labels[m["info_hash"].(string)] = f
	}
	if f := labels[film]; f["label"] != "Dune (2021)" || f["kind"] != "film" {
		t.Errorf("the film grab: %v", f)
	}
	if f := labels[gone]; f["label"] != "a film no longer in the library" {
		t.Errorf("a grab for a deleted film: %v", f)
	}
	if f := labels[followed]; f["label"] != "Severance (2022) S02E04" || f["season"] != float64(2) {
		t.Errorf("an episode of a followed series: %v", f)
	}
	if f := labels[episode]; f["code"] != "S02E03" || f["kind"] != "episode" ||
		f["label"] != "S02E03 of a series no longer in the library" {
		t.Errorf("an episode grab: %v", f)
	}
}

package api

import (
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
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// The whole chain through the real router, the real search service, the real
// indexer client parsing a real feed, real tickets and the real grab path — only
// the network and the torrent engine are canned. What it proves is the claim
// ADR-0023 rests on: the episode a release was matched to reaches the download
// queue from the SEALED ticket, and nothing else can put it there.

const episodeFeed = `<?xml version="1.0"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
<channel>
<item><title>Severance.S02.1080p.ATVP.WEB-DL.DDP5.1.H.264-FLUX</title>
<enclosure url="https://indexer.example.com/dl/pack.torrent" length="40000000000"/>
<torznab:attr name="seeders" value="900"/></item>
<item><title>Severance.Pay.S02E03.720p.HDTV.x264-GRP</title>
<enclosure url="https://indexer.example.com/dl/pay.torrent" length="700000000"/>
<torznab:attr name="seeders" value="300"/></item>
<item><title>Severance.S02E04.1080p.WEB.H264-GRP</title>
<enclosure url="https://indexer.example.com/dl/e04.torrent" length="3000000000"/>
<torznab:attr name="seeders" value="200"/></item>
<item><title>Severance.2022.S02E03.1080p.WEB.H264-SuccessfulCrab</title>
<enclosure url="https://indexer.example.com/dl/e03.torrent" length="3000000000"/>
<torznab:attr name="seeders" value="12"/></item>
</channel></rss>`

// episodeSearchable rebuilds the router with canned indexer answers and the
// episode store wired in.
func (r *rig) episodeSearchable(t *testing.T, episodes *library.EpisodeStore) {
	t.Helper()
	client := indexer.NewClientWithDoer(doerFunc(func(req *http.Request) (*http.Response, error) {
		body := episodeFeed
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
		Episodes: episodes, TrashRetention: 7 * 24 * time.Hour,
	}))
	r.rt = rt
}

// followSeverance puts a series in the library with a refreshed season 2, and
// returns the id of S02E03.
func (r *rig) followSeverance(t *testing.T) (*library.EpisodeStore, int64, int64) {
	t.Helper()
	ctx := authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 1, Name: "Admin", Rank: 100,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
	})
	dir := filepath.Join(t.TempDir(), "tv")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := r.roots.Create(ctx, dir, library.KindSeries, "Shows")
	if err != nil {
		t.Fatal(err)
	}
	item, err := r.media.UpsertItem(ctx, importer.Item{
		Kind: importer.KindSeries, Title: "Severance", Year: 2022,
		RootFolderID: root.ID, Folder: "Severance",
	})
	if err != nil {
		t.Fatal(err)
	}

	episodes := library.NewEpisodeStore(r.database, r.clk.now)
	season := library.SeasonInput{Number: 2, Name: "Season 2", EpisodeCount: 4}
	for n := 1; n <= 4; n++ {
		season.Episodes = append(season.Episodes, library.EpisodeInput{
			ProviderID: int64(5000 + n), Number: n, Title: "Episode " + strconv.Itoa(n),
			Aired: r.clk.now().AddDate(0, 0, -60+7*n),
		})
	}
	if err := episodes.Upsert(ctx, item.ID, []library.SeasonInput{season}); err != nil {
		t.Fatal(err)
	}
	_, byNumber, err := episodes.Seasons(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	return episodes, item.ID, byNumber[2][2].ID
}

func TestAnEpisodeSearchGrabsIntoThatEpisode(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addIndexer(admin, "Tracker")
	episodes, itemID, episodeID := r.followSeverance(t)
	r.episodeSearchable(t, episodes)

	res := admin.post("/api/v1/episodes/"+strconv.FormatInt(episodeID, 10)+"/search", map[string]any{})
	if res.Code != http.StatusOK {
		t.Fatalf("search: %d %s", res.Code, res.Raw)
	}
	cands, _ := res.Body["candidates"].([]any)
	if len(cands) != 4 {
		t.Fatalf("%d candidates, want all four back with verdicts\n%s", len(cands), res.Raw)
	}
	var ticket string
	var withTickets int
	for _, c := range cands {
		m := c.(map[string]any)
		if tk, ok := m["ticket"].(string); ok {
			withTickets++
			ticket = tk
			if m["title"] != "Severance.2022.S02E03.1080p.WEB.H264-SuccessfulCrab" {
				t.Errorf("a ticket was issued for %v", m["title"])
			}
		}
	}
	if withTickets != 1 {
		t.Fatalf("%d candidates carry a ticket; exactly the one that is S02E03 should\n%s", withTickets, res.Raw)
	}
	if first := cands[0].(map[string]any); first["matches"] != true {
		t.Errorf("the match does not lead the list despite having the fewest seeders: %v", first["title"])
	}

	grab := admin.post("/api/v1/releases/grab", map[string]any{"ticket": ticket})
	if grab.Code != http.StatusAccepted {
		t.Fatalf("grab: %d %s", grab.Code, grab.Raw)
	}
	if f, _ := grab.Body["for"].(map[string]any); f == nil || f["code"] != "S02E03" {
		t.Errorf("the grab does not say what it is for: %v", grab.Body["for"])
	}

	r.downloads.mu.Lock()
	defer r.downloads.mu.Unlock()
	if len(r.downloads.meta) != 1 {
		t.Fatalf("%d transfers added", len(r.downloads.meta))
	}
	got := r.downloads.meta[0].Target
	if got == nil || got.ItemID != itemID || got.Season != 2 || got.Episode != 3 {
		t.Errorf("the queue was handed %+v, want item %d S02E03 — the import would file "+
			"this by guessing the series from the release name", got, itemID)
	}
}

// Somebody who may browse but not search cannot search for an episode either.
func TestSearchingForAnEpisodeNeedsThePermissionToSearch(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addIndexer(admin, "Tracker")
	episodes, _, episodeID := r.followSeverance(t)
	r.episodeSearchable(t, episodes)

	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	viewer := r.redeemAndEnroll(code, "viewer", "a-perfectly-fine-passphrase")
	res := viewer.post("/api/v1/episodes/"+strconv.FormatInt(episodeID, 10)+"/search", map[string]any{})
	if res.Code == http.StatusOK {
		t.Fatalf("a User searched the indexers for an episode: %s", res.Raw)
	}
}

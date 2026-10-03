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
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/follow"
	"github.com/jakethecake75/cmediastack/internal/identify"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/tv"
)

// POST /api/v1/media — adding a series before any of it is on disk
// (ADR-0025), through the real router, the real follow service and the real
// stores. Only the metadata provider is canned.

// cannedSeries is a provider that knows one series, Severance, and one film,
// Dune (2021).
type cannedSeries struct {
	mu         sync.Mutex
	detailsErr error
	calls      int
}

func (c *cannedSeries) Details(_ context.Context, kind metadata.Kind, id int64) (metadata.Details, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.detailsErr != nil {
		return metadata.Details{}, c.detailsErr
	}
	if kind == metadata.KindMovie && id == 438631 {
		return metadata.Details{
			Match: metadata.Match{ProviderID: 438631, Kind: metadata.KindMovie, Title: "Dune",
				OriginalTitle: "Dune", Year: 2021, PosterPath: "dune.jpg"},
			IMDbID: "tt1160419",
		}, nil
	}
	if kind != metadata.KindSeries || id != 95396 {
		return metadata.Details{}, metadata.ErrNotFound
	}
	return metadata.Details{
		Match: metadata.Match{ProviderID: 95396, Kind: metadata.KindSeries, Title: "Severance",
			Year: 2022, PosterPath: "sev.jpg"},
		IMDbID: "tt11280740",
		Seasons: []metadata.Season{
			{Number: 1, Name: "Season 1", Episodes: 2},
			{Number: 2, Name: "Season 2", Episodes: 1},
		},
	}, nil
}

func (c *cannedSeries) Episodes(_ context.Context, _ int64, season int) ([]metadata.Episode, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	aired := time.Date(2022, 2, 17, 0, 0, 0, 0, time.UTC)
	if season == 1 {
		return []metadata.Episode{{ProviderID: 11, Number: 1, Aired: aired},
			{ProviderID: 12, Number: 2, Aired: aired.AddDate(0, 0, 7)}}, nil
	}
	// Season 2's one episode has not been scheduled.
	return []metadata.Episode{{ProviderID: 21, Number: 1}}, nil
}

// addable rebuilds the router with the follow service wired to a provider, and
// gives the instance a root folder for series.
func (r *rig) addable(t *testing.T, provider tv.EpisodeProvider, withRoot bool) *library.EpisodeStore {
	t.Helper()
	episodes := library.NewEpisodeStore(r.database, r.clk.now)
	adder := follow.NewService(r.database, r.media, identify.NewStore(r.database, r.clk.now),
		episodes, r.roots, func() tv.EpisodeProvider { return provider }, r.audit,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	if withRoot {
		ctx := authz.WithPrincipal(t.Context(), &authz.Principal{
			UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
			Role: authz.Role{ID: 1, Name: "Admin", Rank: 100,
				Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
		})
		dir := filepath.Join(t.TempDir(), "tv")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := r.roots.Create(ctx, dir, library.KindSeries, "Shows"); err != nil {
			t.Fatal(err)
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auth := NewSessionAuthenticator(r.store, r.svc.Policy().Session, false)
	rt := NewRouter(
		[]Middleware{Recovery(logger), RequestContext(logger),
			ClientIPResolver(nil), SecurityHeaders(time.Hour)},
		[]Middleware{CSRF(), Authenticate(auth, r.audit)},
	)
	RegisterRoutes(rt, New(Deps{
		Identity: r.svc, Auth: auth, Egress: r.egress, Indexers: r.indexers,
		Profiles: r.profiles, Downloads: r.downloads,
		Roots: r.roots, Media: r.media, Scanner: r.scanner, Deleter: r.scanner,
		Tickets: r.tickets, Requests: r.requests, Audit: r.audit,
		Episodes: episodes, Adder: adder, TrashRetention: 7 * 24 * time.Hour,
	}))
	r.rt = rt
	return episodes
}

func (r *rig) itemCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := r.database.QueryRow(`SELECT COUNT(*) FROM media_item`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (r *rig) seriesCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := r.database.QueryRow(`SELECT COUNT(*) FROM media_item WHERE kind = 'series'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAddingASeriesThroughTheAPI(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addable(t, &cannedSeries{}, true)

	res := admin.post("/api/v1/media", map[string]any{"kind": "series", "tmdb_id": 95396, "monitor": "all"})
	if res.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", res.Code, res.Raw)
	}
	item, _ := res.Body["item"].(map[string]any)
	if item["title"] != "Severance" || item["folder"] != "Severance (2022)" || item["tmdb_id"] != float64(95396) {
		t.Errorf("item = %v", item)
	}
	id := int64(item["id"].(float64))
	if loc := res.Header.Get("Location"); loc != "/api/v1/media/"+strconv.FormatInt(id, 10) {
		t.Errorf("Location = %q", loc)
	}
	// Both aired episodes of season 1 wanted; season 2's is announced, so not.
	if res.Body["wanted"] != float64(2) || res.Body["episodes"] != float64(3) || res.Body["monitor"] != "all" {
		t.Errorf("wanted %v of %v episodes, monitor %v", res.Body["wanted"], res.Body["episodes"], res.Body["monitor"])
	}
	if note, _ := res.Body["note"].(string); !strings.Contains(note, "Nothing was downloaded") {
		t.Errorf("the answer does not say nothing was downloaded: %q", note)
	}

	// The series and what it is missing are there to read, the same moment.
	children := admin.get("/api/v1/media/" + strconv.FormatInt(id, 10) + "/children")
	if children.Code != http.StatusOK || children.Body["known"] != float64(3) {
		t.Errorf("children: %d %s", children.Code, children.Raw)
	}
	wanted := admin.get("/api/v1/wanted")
	if n, _ := wanted.Body["count"].(float64); n != 2 {
		t.Errorf("the wanted list has %v entries, want 2: %s", wanted.Body["count"], wanted.Raw)
	}
}

// The request names an id; the provider names the series. A request that tries
// to name it anyway is refused rather than quietly ignored.
func TestARequestCannotNameWhatItIsAdding(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addable(t, &cannedSeries{}, true)

	res := admin.post("/api/v1/media", map[string]any{"kind": "series", "tmdb_id": 95396,
		"monitor": "all", "title": "Something Else Entirely"})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("a request carrying a title: %d %s", res.Code, res.Raw)
	}
	if n := r.seriesCount(t); n != 0 {
		t.Errorf("%d series added", n)
	}
}

func TestAddingTwiceAnswersWithTheFirst(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addable(t, &cannedSeries{}, true)

	first := admin.post("/api/v1/media", map[string]any{"kind": "series", "tmdb_id": 95396, "monitor": "all"})
	if first.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", first.Code, first.Raw)
	}
	again := admin.post("/api/v1/media", map[string]any{"kind": "series", "tmdb_id": 95396, "monitor": "none"})
	if again.Code != http.StatusConflict {
		t.Fatalf("second add: %d %s", again.Code, again.Raw)
	}
	was, _ := first.Body["item"].(map[string]any)
	is, _ := again.Body["item"].(map[string]any)
	if is == nil || is["id"] != was["id"] {
		t.Errorf("the conflict names %v, want the first add's item %v", is, was["id"])
	}
}

func TestAManagerMayAddAndAUserMayNot(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addable(t, &cannedSeries{}, true)

	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	viewer := r.redeemAndEnroll(code, "viewer", "a-perfectly-fine-passphrase")
	res := viewer.post("/api/v1/media", map[string]any{"kind": "series", "tmdb_id": 95396, "monitor": "all"})
	if res.Code == http.StatusCreated {
		t.Fatalf("a User added a series: %s", res.Raw)
	}
	if n := r.seriesCount(t); n != 0 {
		t.Fatalf("%d series added by a User", n)
	}

	code, _ = r.issueInvite(admin, authz.RoleManager, true)
	manager := r.redeemAndEnroll(code, "manager", "another-perfectly-fine-passphrase")
	res = manager.post("/api/v1/media", map[string]any{"kind": "series", "tmdb_id": 95396, "monitor": "future"})
	if res.Code != http.StatusCreated {
		t.Errorf("a Manager could not add a series: %d %s", res.Code, res.Raw)
	}
}

// Each refusal is a status that says whose problem it is, and a sentence.
func TestEveryRefusalSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider *cannedSeries
		noRoot   bool
		body     map[string]any
		want     int
		says     string
	}{
		{"a film with a monitoring choice", &cannedSeries{}, false,
			map[string]any{"kind": "movie", "tmdb_id": 438631, "monitor": "all"}, http.StatusBadRequest,
			"takes no monitoring choice"},
		{"a film with no root folder for films", &cannedSeries{}, false,
			map[string]any{"kind": "movie", "tmdb_id": 438631}, http.StatusConflict,
			"no root folder for films is configured"},
		{"not a kind", &cannedSeries{}, false,
			map[string]any{"kind": "album", "tmdb_id": 1}, http.StatusBadRequest, "kind must be"},
		{"no choice of what is wanted", &cannedSeries{}, false,
			map[string]any{"kind": "series", "tmdb_id": 95396}, http.StatusBadRequest, "all, future, latest or none"},
		{"a folder name that is not one", &cannedSeries{}, false,
			map[string]any{"kind": "series", "tmdb_id": 95396, "monitor": "all", "folder": "Star Trek: Discovery"},
			http.StatusBadRequest, "would have to be written as"},
		{"an id the provider does not have", &cannedSeries{}, false,
			map[string]any{"kind": "series", "tmdb_id": 42, "monitor": "all"}, http.StatusUnprocessableEntity, "no such series"},
		{"a rate limit", &cannedSeries{detailsErr: metadata.ErrRateLimited}, false,
			map[string]any{"kind": "series", "tmdb_id": 95396, "monitor": "all"}, http.StatusServiceUnavailable, "nothing was added"},
		{"a provider that is down", &cannedSeries{detailsErr: metadata.ErrUnavailable}, false,
			map[string]any{"kind": "series", "tmdb_id": 95396, "monitor": "all"}, http.StatusBadGateway, "nothing was added"},
		{"no root folder for series", &cannedSeries{}, true,
			map[string]any{"kind": "series", "tmdb_id": 95396, "monitor": "all"}, http.StatusConflict, "Storage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			admin := r.bootstrapAdmin()
			r.addable(t, tc.provider, !tc.noRoot)
			res := admin.post("/api/v1/media", tc.body)
			if res.Code != tc.want {
				t.Fatalf("%d %s; want %d", res.Code, res.Raw, tc.want)
			}
			if !strings.Contains(res.Raw, tc.says) {
				t.Errorf("the answer does not say %q: %s", tc.says, res.Raw)
			}
			if tc.want == http.StatusServiceUnavailable && res.Header.Get("Retry-After") == "" {
				t.Error("a rate limit without Retry-After")
			}
			if n := r.itemCount(t); n != 0 {
				t.Errorf("%d items added", n)
			}
		})
	}
}

// With no provider configured, nothing can be added, and the answer says what
// to configure.
func TestWithNoProviderTheAnswerSaysWhatIsMissing(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.addable(t, nil, true)
	res := admin.post("/api/v1/media", map[string]any{"kind": "series", "tmdb_id": 95396, "monitor": "all"})
	if res.Code != http.StatusConflict || !strings.Contains(res.Raw, "metadata provider") {
		t.Errorf("%d %s", res.Code, res.Raw)
	}
}

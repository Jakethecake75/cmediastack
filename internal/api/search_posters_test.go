package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identify"
	"github.com/jakethecake75/cmediastack/internal/metadata"
)

// withMetadataAndPosters is withMetadata with the identifier and a poster
// cache, the two halves of serving a search result's poster.
func (r *rig) withMetadataAndPosters(f *fakeMetadata, art *fakeArtwork) {
	r.t.Helper()
	auth := NewSessionAuthenticator(r.store, r.svc.Policy().Session, false)
	rt := NewRouter(
		[]Middleware{Recovery(quietLogger()), RequestContext(quietLogger()),
			ClientIPResolver(nil), SecurityHeaders(time.Hour)},
		[]Middleware{CSRF(), Authenticate(auth, r.audit)},
	)
	idSvc := identify.NewService(identify.NewStore(r.database, r.clk.now),
		func() metadata.Provider { return nil }, r.media, nil, r.audit, quietLogger(), r.clk.now)
	RegisterRoutes(rt, New(Deps{
		Identity: r.svc, Auth: auth, Egress: r.egress, Indexers: r.indexers,
		Profiles: r.profiles, Roots: r.roots, Media: r.media,
		Scanner: r.scanner, Deleter: r.scanner, Tickets: r.tickets,
		Requests: r.requests, Metadata: f, Audit: r.audit,
		Identify: idSvc, Artwork: art,
		TrashRetention: 7 * 24 * time.Hour,
	}))
	r.rt = rt
}

// A film search answers with a poster for each match that has one (ADR-0070),
// served to the account that searched, and to no other account that may not
// edit the library — a poster still tells nobody what the library holds.
func TestASearchOffersPostersToTheAccountThatSearched(t *testing.T) {
	r := newRig(t)
	f := &fakeMetadata{matches: []metadata.Match{
		{ProviderID: 438631, Kind: metadata.KindMovie, Title: "Dune", Year: 2021, PosterPath: "/dune.jpg"},
		{ProviderID: 841, Kind: metadata.KindMovie, Title: "Dune", Year: 1984},
	}}
	art := &fakeArtwork{dir: t.TempDir()}
	for _, id := range []int64{438631, 777} {
		p := filepath.Join(art.dir, "poster", "tmdb", strconv.FormatInt(id, 10)+"-w342.jpg")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("\xff\xd8\xff poster"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.withMetadataAndPosters(f, art)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(code, "sam", "user-passphrase-1")
	code, _ = r.issueInvite(admin, authz.RoleUser, true)
	kim := r.redeemAndEnroll(code, "kim", "user-passphrase-2")

	res := sam.get("/api/v1/requests/search?kind=movie&title=dune")
	matches, _ := res.Body["matches"].([]any)
	if res.Code != http.StatusOK || len(matches) != 2 {
		t.Fatalf("search: %d %s", res.Code, res.Raw)
	}
	first, _ := matches[0].(map[string]any)
	second, _ := matches[1].(map[string]any)
	if first["poster"] != "/api/v1/artwork/poster/tmdb/438631" {
		t.Errorf("poster = %v, want this instance's path for it", first["poster"])
	}
	if _, ok := second["poster"]; ok {
		t.Errorf("a match with no poster was given one: %v", second)
	}

	if got := sam.get("/api/v1/artwork/poster/tmdb/438631").Code; got != http.StatusOK {
		t.Errorf("the searcher's poster: %d, want 200", got)
	}
	if got := sam.get("/api/v1/artwork/poster/tmdb/777").Code; got != http.StatusNotFound {
		t.Errorf("a poster never offered to the searcher: %d, want 404", got)
	}
	if got := kim.get("/api/v1/artwork/poster/tmdb/438631").Code; got != http.StatusNotFound {
		t.Errorf("another account's search result: %d, want 404", got)
	}
	if got := admin.get("/api/v1/metadata/search?kind=movie&title=dune"); got.Code != http.StatusOK ||
		got.Body["matches"].([]any)[0].(map[string]any)["poster"] == nil {
		t.Errorf("the editor's search has no poster: %d %s", got.Code, got.Raw)
	}
}

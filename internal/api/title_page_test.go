package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/artwork"
	"github.com/jakethecake75/cmediastack/internal/metadata"
)

// fakeTitles answers for every title and records which it was asked about.
type fakeTitles struct{ asked []int64 }

func (f *fakeTitles) TitleDetails(_ context.Context, kind metadata.Kind, id int64) (metadata.Details, error) {
	f.asked = append(f.asked, id)
	return metadata.Details{
		Match:  metadata.Match{ProviderID: id, Kind: kind, Overview: "A bear comes to London."},
		Genres: []string{"Family"}, Runtime: 95, Score: 7.1, Tagline: "Please look after this bear.",
		BackdropPath: "back.jpg",
	}, nil
}
func (f *fakeTitles) ImageBase() string { return "https://image.example/t/p" }

// fakeImages caches a picture for whatever it is asked for, and records it.
type fakeImages struct {
	art     *fakeArtwork
	fetched []artwork.Ref
}

func (f *fakeImages) Fetch(_ context.Context, _ string, r artwork.Ref) (string, error) {
	f.fetched = append(f.fetched, r)
	rel := string(r.Kind) + "/" + r.Provider + "/" + strconv.FormatInt(r.ID, 10) + "-" + r.Size + ".jpg"
	p := filepath.Join(f.art.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	return rel, os.WriteFile(p, []byte("\xff\xd8\xff picture"), 0o600)
}

// ADR-0078: a title's page reads what the provider says about it — in the
// caller's scope, so a title out of it is absent and the provider is not asked.
func TestATitlesDetailsAreReadInItsScope(t *testing.T) {
	w := newScopeWorld(t)
	id := func(v int64) string { return strconv.FormatInt(v, 10) }

	res := w.kid.get("/api/v1/media/" + id(w.shown) + "/details")
	if res.Code != http.StatusOK {
		t.Fatalf("details: %d %s", res.Code, res.Raw)
	}
	if res.Body["overview"] != "A bear comes to London." || res.Body["score"] != 7.1 ||
		res.Body["runtime_minutes"] != float64(95) || res.Body["tagline"] != "Please look after this bear." {
		t.Errorf("details = %s", res.Raw)
	}
	if got, want := res.Body["backdrop"], "/api/v1/media/"+id(w.shown)+"/artwork?kind=backdrop"; got != want {
		t.Errorf("backdrop = %v, want %s", got, want)
	}
	if len(w.titles.asked) != 1 || w.titles.asked[0] != w.shownTMDB {
		t.Fatalf("the provider was asked about %v, want the one title", w.titles.asked)
	}

	if res := w.kid.get("/api/v1/media/" + id(w.hiddenByRoot) + "/details"); res.Code != http.StatusNotFound {
		t.Errorf("a title out of scope: %d %s", res.Code, res.Raw)
	}
	// Music, and a film nobody has identified: nothing to ask, and nothing said.
	if _, err := w.r.database.ExecContext(t.Context(),
		`UPDATE media_item SET tmdb_id = NULL WHERE id = ?`, w.unrated); err != nil {
		t.Fatal(err)
	}
	for _, v := range []int64{w.hiddenArtist, w.unrated} {
		res = w.admin.get("/api/v1/media/" + id(v) + "/details")
		if res.Code != http.StatusOK || len(res.Body) != 0 {
			t.Errorf("a title the provider did not identify: %d %s", res.Code, res.Raw)
		}
	}
	if len(w.titles.asked) != 1 {
		t.Errorf("the provider was asked about a hidden or unidentified title: %v", w.titles.asked)
	}
}

// The backdrop is the title's own artwork route with a kind: fetched into the
// cache by the provider's filename, and a series' kept apart from a film's.
func TestABackdropIsServedFromTheCache(t *testing.T) {
	w := newScopeWorld(t)
	id := func(v int64) string { return strconv.FormatInt(v, 10) }

	res := w.kid.get("/api/v1/media/" + id(w.shown) + "/artwork?kind=backdrop")
	if res.Code != http.StatusOK || res.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("backdrop: %d %q %s", res.Code, res.Header.Get("Content-Type"), res.Raw)
	}
	want := artwork.Ref{Kind: artwork.KindBackdrop, Provider: "tmdb", ID: w.shownTMDB, Size: "w1280", RemotePath: "back.jpg"}
	if len(w.images.fetched) != 1 || w.images.fetched[0] != want {
		t.Fatalf("fetched %+v, want %+v", w.images.fetched, want)
	}
	if res := w.kid.get("/api/v1/media/" + id(w.shownSeries) + "/artwork?kind=backdrop"); res.Code != http.StatusOK {
		t.Fatalf("a series' backdrop: %d", res.Code)
	}
	if got := w.images.fetched[1].Provider; got != "tmdbtv" {
		t.Errorf("a series' backdrop was cached as %q: a film with its id would show it", got)
	}
	if res := w.kid.get("/api/v1/media/" + id(w.hiddenByRoot) + "/artwork?kind=backdrop"); res.Code != http.StatusNotFound {
		t.Errorf("a hidden title's backdrop: %d", res.Code)
	}
	if len(w.images.fetched) != 2 {
		t.Errorf("a picture was fetched for a hidden title: %+v", w.images.fetched)
	}
	// Without the kind it is still the poster.
	if res := w.kid.get("/api/v1/media/" + id(w.shown) + "/artwork"); res.Code != http.StatusOK || len(w.images.fetched) != 2 {
		t.Errorf("the poster: %d, fetched %d", res.Code, len(w.images.fetched))
	}
}

// An episode's still is named in the series' children and served by the
// episode, in scope; an episode with none names none.
func TestAnEpisodesStillIsServedInScope(t *testing.T) {
	w := newScopeWorld(t)
	id := func(v int64) string { return strconv.FormatInt(v, 10) }

	res := w.kid.get("/api/v1/media/" + id(w.shownSeries) + "/children")
	if res.Code != http.StatusOK {
		t.Fatalf("children: %d %s", res.Code, res.Raw)
	}
	eps := res.Body["seasons"].([]any)[0].(map[string]any)["episodes"].([]any)
	first, second := eps[0].(map[string]any), eps[1].(map[string]any)
	epID := int64(first["id"].(float64))
	if first["still"] != "/api/v1/episodes/"+id(epID)+"/still" {
		t.Fatalf("still = %v", first["still"])
	}
	if _, ok := second["still"]; ok {
		t.Errorf("an episode with no still was given one: %v", second)
	}

	if res := w.kid.get("/api/v1/episodes/" + id(epID) + "/still"); res.Code != http.StatusOK ||
		res.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("still: %d %s", res.Code, res.Raw)
	}
	want := artwork.Ref{Kind: artwork.KindStill, Provider: "tmdb", ID: w.shownSeries*100 + 1, Size: "w300", RemotePath: "ep1.jpg"}
	if len(w.images.fetched) != 1 || w.images.fetched[0] != want {
		t.Fatalf("fetched %+v, want %+v", w.images.fetched, want)
	}
	for name, ep := range map[string]int64{"hidden": w.hiddenEpisode, "with no still": int64(second["id"].(float64))} {
		if res := w.kid.get("/api/v1/episodes/" + id(ep) + "/still"); res.Code != http.StatusNotFound {
			t.Errorf("an episode %s: %d", name, res.Code)
		}
	}
	if len(w.images.fetched) != 1 {
		t.Errorf("a still was fetched that should not have been: %+v", w.images.fetched)
	}
}

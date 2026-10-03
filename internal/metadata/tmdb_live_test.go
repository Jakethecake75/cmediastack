package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/artwork"
	"github.com/jakethecake75/cmediastack/internal/library"
)

// Live tests against the real TMDB API.
//
// # Why these exist
//
// ADR-0018 recorded one unverified thing: no SUCCESSFUL TMDB response had ever
// been parsed by this software, because the instance had never been given an
// API key. The field names in tmdb.go came from documentation rather than from a
// response this code had seen.
//
// These close that gap, and keep it closed. Set CMS_TMDB_TOKEN to a v4 Read
// Access Token and they run; without it they skip. That is deliberate: the test
// suite must stay runnable by anyone, offline, with no credential — but the
// question "do the struct tags match reality?" must be answerable by running
// something rather than by reading documentation again.
//
//	CMS_TMDB_TOKEN=eyJ... go test ./internal/metadata/ -run Live -v
//
// # What they assert, and what they deliberately do not
//
// They assert that the fields this software DEPENDS ON arrive populated. They
// do not assert exact values for anything TMDB may legitimately change —
// popularity, overview wording, even a poster path — because a test that fails
// when a third party edits a synopsis is a test people delete.
//
// The identifiers are stable and are pinned: 329865 is Arrival (2016) and 95396
// is Severance. TMDB ids do not change.

const (
	// Arrival (2016), a film with a known IMDb id and runtime.
	liveFilmID = 329865
	// Severance, a series — the case where imdb_id is NOT at the top level and
	// has to come from external_ids, and where runtime is a LIST.
	liveSeriesID = 95396
)

// liveClient builds a client from the environment, or skips.
func liveClient(t *testing.T) *TMDB {
	t.Helper()
	token := strings.TrimSpace(os.Getenv("CMS_TMDB_TOKEN"))
	if token == "" {
		t.Skip("set CMS_TMDB_TOKEN to a TMDB v4 Read Access Token to run the live tests")
	}
	// A plain client, not one from the egress guard. These tests check the
	// PROVIDER's shape, not this software's routing; the guard is asserted
	// structurally by egress.TestNoPackageDialsDirectly, which is also why a
	// non-test file in this package could not build a client like this.
	return NewTMDB(&http.Client{Timeout: 30 * time.Second}, DefaultTMDBBase, token)
}

func liveCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestLiveCheck proves the credential and reads /configuration.
func TestLiveCheck(t *testing.T) {
	h, err := liveClient(t).Check(liveCtx(t))
	if err != nil {
		t.Fatalf("Check against the live API: %v (%s)", err, h.Detail)
	}
	if !h.OK {
		t.Fatalf("Check reported not OK: %s", h.Detail)
	}
	// The two fields internal/artwork needs. If secure_base_url were absent the
	// code falls back audibly, and this is what would notice that happening.
	if h.ImageBase == "" {
		t.Error("the live /configuration carried no image base")
	}
	if !strings.HasPrefix(h.ImageBase, "https://") {
		t.Errorf("image base %q is not https", h.ImageBase)
	}
	if strings.HasSuffix(h.ImageBase, "/") {
		t.Errorf("image base %q keeps its trailing slash; artwork appends /size/file", h.ImageBase)
	}
	if len(h.Sizes) == 0 {
		t.Error("the live /configuration carried no poster sizes")
	}
	t.Logf("live image base %q, %d poster sizes: %v", h.ImageBase, len(h.Sizes), h.Sizes)
}

// TestLiveSearchFilm checks the film spellings: title, original_title,
// release_date.
func TestLiveSearchFilm(t *testing.T) {
	got, err := liveClient(t).Search(liveCtx(t),
		Query{Kind: KindMovie, Title: "Arrival", Year: 2016})
	if err != nil {
		t.Fatalf("live film search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the live API returned no results for a film that exists")
	}

	var found *Match
	for i := range got {
		if got[i].ProviderID == liveFilmID {
			found = &got[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("Arrival (id %d) was not in %d live results; first was %d %q",
			liveFilmID, len(got), got[0].ProviderID, got[0].Title)
	}

	// The fields this software depends on, populated from a real response.
	if found.Title == "" {
		t.Error(`Title is empty: the "title" tag does not match the live field`)
	}
	if found.Year != 2016 {
		t.Errorf(`Year = %d, want 2016: the "release_date" tag or yearOf is wrong`, found.Year)
	}
	if found.PosterPath == "" {
		t.Error(`PosterPath is empty: the "poster_path" tag does not match`)
	}
	// artwork refuses anything that is not a plain filename, so a leading slash
	// here would mean every poster fetch failed.
	if strings.ContainsAny(found.PosterPath, "/\\") {
		t.Errorf("PosterPath %q is not a plain filename; internal/artwork would refuse it",
			found.PosterPath)
	}
	if found.Overview == "" {
		t.Error(`Overview is empty: the "overview" tag does not match`)
	}
	t.Logf("live film: id=%d %q (%d) poster=%q", found.ProviderID, found.Title,
		found.Year, found.PosterPath)
}

// TestLiveSearchSeries checks the OTHER spellings: name, original_name,
// first_air_date — and the differently-named year parameter, which is the part
// that silently returns the wrong thing rather than failing.
func TestLiveSearchSeries(t *testing.T) {
	got, err := liveClient(t).Search(liveCtx(t),
		Query{Kind: KindSeries, Title: "Severance", Year: 2022})
	if err != nil {
		t.Fatalf("live series search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the live API returned no results for a series that exists")
	}

	var found *Match
	for i := range got {
		if got[i].ProviderID == liveSeriesID {
			found = &got[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("Severance (id %d) was not in %d live results; first was %d %q",
			liveSeriesID, len(got), got[0].ProviderID, got[0].Title)
	}

	if found.Title == "" {
		t.Error(`Title is empty for a series: the "name" fallback does not match the live field`)
	}
	if found.Year != 2022 {
		t.Errorf(`Year = %d, want 2022: the "first_air_date" tag or the `+
			`first_air_date_year parameter is wrong`, found.Year)
	}
	t.Logf("live series: id=%d %q (%d)", found.ProviderID, found.Title, found.Year)
}

// TestLiveDetailsFilm checks imdb_id, runtime and genres on a real film.
func TestLiveDetailsFilm(t *testing.T) {
	d, err := liveClient(t).Details(liveCtx(t), KindMovie, liveFilmID)
	if err != nil {
		t.Fatalf("live film details: %v", err)
	}
	if d.ProviderID != liveFilmID {
		t.Errorf("id = %d, want %d", d.ProviderID, liveFilmID)
	}
	// The identifier other tools agree on. A film carries it at the top level.
	if !strings.HasPrefix(d.IMDbID, "tt") {
		t.Errorf(`IMDbID = %q, want a tt… id: the "imdb_id" tag does not match`, d.IMDbID)
	}
	if d.Runtime <= 0 {
		t.Errorf(`Runtime = %d: the "runtime" tag does not match`, d.Runtime)
	}
	if len(d.Genres) == 0 {
		t.Error(`Genres is empty: the "genres" tag or its nested "name" does not match`)
	}
	if d.Released.IsZero() {
		t.Error("Released is zero: dateOf could not parse the live release_date")
	}
	if len(d.Seasons) != 0 {
		t.Errorf("a film reported %d seasons", len(d.Seasons))
	}
	t.Logf("live film details: %q (%d) imdb=%s runtime=%dm genres=%v released=%s",
		d.Title, d.Year, d.IMDbID, d.Runtime, d.Genres, d.Released.Format("2006-01-02"))
}

// TestLiveDetailsSeries is the one with the most ways to be wrong: a series has
// no top-level imdb_id, reports a LIST of runtimes, and carries the season list
// that ADR-0016 refused to invent.
func TestLiveDetailsSeries(t *testing.T) {
	d, err := liveClient(t).Details(liveCtx(t), KindSeries, liveSeriesID)
	if err != nil {
		t.Fatalf("live series details: %v", err)
	}
	if d.ProviderID != liveSeriesID {
		t.Errorf("id = %d, want %d", d.ProviderID, liveSeriesID)
	}
	// This is the append_to_response=external_ids round trip. If it did not
	// work, one request would have to become two.
	if !strings.HasPrefix(d.IMDbID, "tt") {
		t.Errorf(`IMDbID = %q, want a tt… id: append_to_response=external_ids `+
			`did not produce what this code reads`, d.IMDbID)
	}
	// NOT asserted as non-zero, and the live API is why. episode_run_time was
	// documented as a list of typical episode lengths and comes back EMPTY for
	// this series — apparently for most. Zero is the honest answer for a series
	// whose provider does not know, so demanding a number here would be
	// demanding an invented one.
	t.Logf("series runtime = %dm (0 is expected: episode_run_time is usually empty)", d.Runtime)

	if len(d.Seasons) == 0 {
		t.Fatal("no seasons: the season list is what makes an episode table possible")
	}
	for _, s := range d.Seasons {
		if s.Number < 0 {
			t.Errorf("season number %d", s.Number)
		}
		// An ANNOUNCED season legitimately has no episodes and no air date —
		// Severance season 3 is exactly that today. So the rule is conditional:
		// a season that has AIRED must have episodes. My first version of this
		// assertion was simply wrong, and the live API said so.
		if !s.Aired.IsZero() && s.Episodes <= 0 {
			t.Errorf("season %d aired %s but reports %d episodes",
				s.Number, s.Aired.Format("2006-01-02"), s.Episodes)
		}
		if s.Aired.IsZero() && s.Episodes > 0 {
			t.Errorf("season %d has %d episodes but no air date", s.Number, s.Episodes)
		}
	}
	t.Logf("live series details: %q imdb=%s runtime=%dm seasons=%d",
		d.Title, d.IMDbID, d.Runtime, len(d.Seasons))
	for _, s := range d.Seasons {
		t.Logf("  season %d %q — %d episodes, aired %s",
			s.Number, s.Name, s.Episodes, s.Aired.Format("2006-01-02"))
	}
}

// A title nobody has is an empty result, not an error. Worth checking against
// the live API because a provider returning 200 with an empty list and a
// provider returning 404 are different, and this code treats them differently.
func TestLiveSearchForSomethingThatDoesNotExist(t *testing.T) {
	got, err := liveClient(t).Search(liveCtx(t),
		Query{Kind: KindMovie, Title: "zzzqqxx no such film zzzqqxx"})
	if err != nil {
		t.Fatalf("a search with no matches returned an error: %v", err)
	}
	if len(got) != 0 {
		t.Logf("the live API found %d results for nonsense; not a failure", len(got))
	}
}

// TestLiveProviderPathReachesTheArtworkStore is the composition, end to end:
// a REAL poster path, from a real search, through internal/artwork's checks,
// onto disk.
//
// Worth running as one test rather than two, because the two halves were built
// against different evidence and the seam between them is where a mismatch
// would live. Two real bugs have already lived exactly there:
//
//   - the URL is base + size + "/" + file, and resolving the provider's path as
//     a reference silently dropped the size segment (the CDN answered 404);
//   - a provider path arrives as "/abc.jpg" and internal/artwork accepts a plain
//     FILENAME only, so the leading slash has to come off somewhere. This is
//     what proves it does.
func TestLiveProviderPathReachesTheArtworkStore(t *testing.T) {
	c := liveClient(t)
	ctx := liveCtx(t)

	h, err := c.Check(ctx)
	if err != nil {
		t.Fatalf("check: %v", err)
	}

	got, err := c.Search(ctx, Query{Kind: KindMovie, Title: "Arrival", Year: 2016})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var m *Match
	for i := range got {
		if got[i].ProviderID == liveFilmID && got[i].PosterPath != "" {
			m = &got[i]
			break
		}
	}
	if m == nil {
		t.Skip("the live search returned no poster path for the pinned film")
	}

	cache, err := library.OpenCache(filepath.Join(t.TempDir(), "art"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cache.Close() }()

	// The host allowlist comes from the provider's OWN reported base, parsed
	// rather than assumed — which is the only way an operator pointing at a
	// mirror would work, and the only way a provider redirecting somewhere
	// unexpected would be refused.
	base, err := url.Parse(h.ImageBase)
	if err != nil {
		t.Fatal(err)
	}
	store := artwork.New(cache, &http.Client{Timeout: 30 * time.Second},
		[]string{base.Hostname()})

	rel, err := store.Fetch(ctx, h.ImageBase, artwork.Ref{
		Kind:       artwork.KindPoster,
		Provider:   c.Name(),
		ID:         m.ProviderID,
		Size:       "w342",
		RemotePath: m.PosterPath,
	})
	if err != nil {
		t.Fatalf("fetching a real poster with a real provider path: %v", err)
	}

	// The destination is ours: kind/provider/id-size. The provider's filename
	// must not appear in it.
	want := "poster/tmdb/329865-w342."
	if !strings.HasPrefix(rel, want) {
		t.Errorf("cached at %q, want a path beginning %q", rel, want)
	}
	if strings.Contains(rel, strings.TrimSuffix(m.PosterPath, filepath.Ext(m.PosterPath))) {
		t.Errorf("the provider's filename appears in the destination: %q", rel)
	}

	fi, err := cache.Stat(rel)
	if err != nil {
		t.Fatalf("the poster is not on disk: %v", err)
	}
	if fi.Size() < 1000 {
		t.Errorf("cached %d bytes, too few for a poster", fi.Size())
	}
	t.Logf("real poster %q -> %s (%d bytes) via %s",
		m.PosterPath, rel, fi.Size(), h.ImageBase)
}

// TestLiveEpisodes reads real seasons and checks the one assumption the whole
// refresh design rests on: that the episode COUNT Details reports for a season
// equals the number of episodes the season endpoint returns.
//
// tv.Refresher decides whether to spend a request on a season by comparing
// those two numbers. If TMDB's summary count and its episode list disagreed —
// counting specials differently, say, or lagging behind — every refresh would
// either refetch that season forever or never notice it change, and nothing
// else in the suite would find out, because every other test uses a fake.
func TestLiveEpisodes(t *testing.T) {
	c := liveClient(t)
	ctx := liveCtx(t)

	d, err := c.Details(ctx, KindSeries, liveSeriesID)
	if err != nil {
		t.Fatalf("details: %v", err)
	}

	checked := 0
	for _, s := range d.Seasons {
		eps, err := c.Episodes(ctx, liveSeriesID, s.Number)
		if err != nil {
			t.Errorf("season %d: %v", s.Number, err)
			continue
		}
		checked++
		t.Logf("season %d %q: Details says %d, the season endpoint returned %d",
			s.Number, s.Name, s.Episodes, len(eps))

		if len(eps) != s.Episodes {
			t.Errorf("season %d: Details reports %d episodes and the season endpoint "+
				"returns %d. The refresher compares exactly these two numbers to decide "+
				"whether a season changed, so a disagreement means it refetches forever "+
				"or never notices", s.Number, s.Episodes, len(eps))
		}

		seen := map[int]bool{}
		for _, e := range eps {
			if e.Number < 0 {
				t.Errorf("S%02dE%02d: negative episode number", s.Number, e.Number)
			}
			if seen[e.Number] {
				t.Errorf("S%02dE%02d appears twice; the table's unique key would "+
					"collapse them", s.Number, e.Number)
			}
			seen[e.Number] = true
			if e.ProviderID <= 0 {
				t.Errorf("S%02dE%02d has no provider id", s.Number, e.Number)
			}
			// An episode of a season that has AIRED should carry a date. One
			// that does not is announced, which is legitimate for the current
			// season and suspicious for a finished one — logged, not failed,
			// because the provider is the authority and this test is not.
			if e.Aired.IsZero() && !s.Aired.IsZero() {
				t.Logf("  S%02dE%02d %q has no air date in a season that aired %s",
					s.Number, e.Number, e.Title, s.Aired.Format("2006-01-02"))
			}
		}
		if len(eps) > 0 {
			first, last := eps[0], eps[len(eps)-1]
			t.Logf("  E%02d %q aired %s … E%02d %q aired %s",
				first.Number, first.Title, first.Aired.Format("2006-01-02"),
				last.Number, last.Title, last.Aired.Format("2006-01-02"))
		}
	}
	if checked == 0 {
		t.Fatal("no season could be read, so nothing was checked")
	}
}

// A season number the series does not have. The refresher treats an error for
// one season as "leave it as it was and try again next time", so what matters
// is that it IS an error and not an empty list — an empty list would be read as
// "the provider lists no episodes" and delete them.
func TestLiveEpisodesForASeasonThatDoesNotExist(t *testing.T) {
	eps, err := liveClient(t).Episodes(liveCtx(t), liveSeriesID, 97)
	if err == nil {
		t.Fatalf("season 97 of a three-season show returned %d episodes and no "+
			"error. An empty list here would be read as 'this season has no "+
			"episodes' and would DELETE a real season's rows if a numbering "+
			"mistake ever asked for the wrong one", len(eps))
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound: the provider does not have that season, "+
			"which is not the same as answering in a shape this software cannot read", err)
	}
	t.Logf("a nonexistent season is an error, as it must be: %v", err)
}

// A series id the provider does not have is ErrNotFound. Adding a series by id
// (ADR-0025) turns this into "the provider has no series with that id" rather
// than a complaint about the provider.
func TestLiveDetailsForATitleThatDoesNotExist(t *testing.T) {
	_, err := liveClient(t).Details(liveCtx(t), KindSeries, 999999999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	t.Logf("as it should be: %v", err)
}

// The names the scene releases these shows under are among TMDB's alternative
// titles. ADR-0023 relies on exactly these three; if TMDB stops listing them,
// episode search starts refusing the right show, and this says so first.
func TestLiveAlternativeTitlesCarryTheSceneNames(t *testing.T) {
	c := liveClient(t)
	ctx := liveCtx(t)
	for _, tc := range []struct {
		id   int64
		want string
	}{
		{2316, "The Office (US)"},
		{2734, "Law and Order SVU"},
		{61889, "Daredevil"},
	} {
		got, err := c.AlternativeTitles(ctx, tc.id)
		if err != nil {
			t.Errorf("%d: %v", tc.id, err)
			continue
		}
		found := false
		for _, g := range got {
			if g == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%d: %q is not among %d alternative titles: %q", tc.id, tc.want, len(got), got)
		}
	}
}

// Every name ADR-0026's examples rely on is in the one answer FilmTitles reads:
// Star Wars' scene name among its alternatives, Amélie's and Spirited Away's
// original titles — which TMDB does not list as alternatives, so they must come
// from the details. If any of these stops holding, the film search starts
// refusing the right film, and this says so first.
func TestLiveFilmTitlesCarryTheNamesReleasesUse(t *testing.T) {
	c := liveClient(t)
	ctx := liveCtx(t)
	for _, tc := range []struct {
		id    int64
		first string
		want  []string
	}{
		{11, "Star Wars", []string{"Star Wars: Episode IV - A New Hope"}},
		{194, "Amélie", []string{"Le Fabuleux Destin d'Amélie Poulain", "Amelie"}},
		{129, "Spirited Away", []string{"千と千尋の神隠し"}},
		{438631, "Dune", []string{"Dune: Part One"}},
	} {
		got, err := c.FilmTitles(ctx, tc.id)
		if err != nil {
			t.Errorf("%d: %v", tc.id, err)
			continue
		}
		if len(got) == 0 || got[0] != tc.first {
			t.Errorf("%d: the title is not first: %q", tc.id, got)
		}
		for _, w := range tc.want {
			found := false
			for _, g := range got {
				if g == w {
					found = true
				}
			}
			if !found {
				t.Errorf("%d: %q is not among its %d names: %q", tc.id, w, len(got), got)
			}
		}
	}
	// And a film the provider does not have is "no such title".
	if _, err := c.FilmTitles(ctx, 999999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("a film that does not exist: err = %v, want ErrNotFound", err)
	}
}

package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Tests for the TMDB client.
//
// The fixtures below are built from TMDB's documented field names. They are NOT
// recordings of live responses — see the package comment — so a test that only
// fed this package its own assumptions back would prove nothing about the real
// service. What these tests are for is the behaviour AROUND the decoding, which
// is where the failure modes actually live: what happens when a field is
// missing, when the key is wrong, when the body is HTML, when a series and a
// film disagree about which field holds the title.
//
// The one thing that IS verified against the live service is the error
// envelope, and TestTheRealErrorEnvelopeIsUnderstood uses the exact bytes the
// API returned.

type fakeAPI struct {
	t *testing.T
	// paths records every path requested, so a test can assert that one
	// request was made rather than two.
	paths  []string
	auth   []string
	status int
	body   string
	srv    *httptest.Server
}

func newFake(t *testing.T, body string) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, status: 200, body: body}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.paths = append(f.paths, r.URL.Path+"?"+r.URL.RawQuery)
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) client(token string) *TMDB {
	return NewTMDB(f.srv.Client(), f.srv.URL, token)
}

// ---------------------------------------------------------------------------
// The credential
// ---------------------------------------------------------------------------

// The one response shape in this package taken from the live service rather
// than from documentation. These are the exact bytes api.themoviedb.org
// returned to an unauthenticated request while this was written.
const realUnauthorizedBody = `{"status_code":7,"status_message":"Invalid API key: You must be granted a valid key.","success":false}`

func TestTheRealErrorEnvelopeIsUnderstood(t *testing.T) {
	f := newFake(t, realUnauthorizedBody)
	f.status = http.StatusUnauthorized

	_, err := f.client("a-key-that-does-not-work").Check(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	// The provider's own words reach the operator. "Unauthorized" alone does
	// not tell somebody they pasted the wrong KIND of key.
	if !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("the provider's message was discarded: %v", err)
	}
}

func TestABadKeyIsExplainedInTermsAnOperatorCanAct(t *testing.T) {
	f := newFake(t, realUnauthorizedBody)
	f.status = http.StatusUnauthorized

	h, err := f.client("v3-style-key").Check(context.Background())
	if err == nil || h.OK {
		t.Fatal("a rejected key reported healthy")
	}
	// The commonest real mistake is pasting a v3 API key where a v4 read access
	// token is wanted. Saying so is the difference between a fix and a guess.
	if !strings.Contains(h.Detail, "v4 Read Access Token") {
		t.Errorf("the explanation does not name the likely cause: %q", h.Detail)
	}
}

func TestNoKeyIsNotAFailedRequest(t *testing.T) {
	f := newFake(t, `{}`)
	_, err := f.client("").Check(context.Background())
	if !errors.Is(err, ErrNoProvider) {
		t.Errorf("err = %v, want ErrNoProvider", err)
	}
	if len(f.paths) != 0 {
		t.Errorf("a request was made with no key: %v", f.paths)
	}
}

func TestTheKeyIsSentAsABearerCredential(t *testing.T) {
	f := newFake(t, `{"images":{"secure_base_url":"https://image.tmdb.org/t/p/"}}`)
	if _, err := f.client("the-token").Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.auth) != 1 || f.auth[0] != "Bearer the-token" {
		t.Errorf("Authorization = %v", f.auth)
	}
	// Never in the query string: a URL carrying a credential is logged by every
	// proxy between here and there, and ends up in this software's own logs.
	for _, p := range f.paths {
		if strings.Contains(p, "the-token") {
			t.Errorf("the key appears in the URL: %q", p)
		}
	}
}

// A 429 is recognised by STATUS. No rate-limit header was observed on the live
// service, so this code deliberately parses none.
func TestRateLimitingIsRecognisedByStatusAlone(t *testing.T) {
	f := newFake(t, `{"status_message":"slow down"}`)
	f.status = http.StatusTooManyRequests
	_, err := f.client("t").Search(context.Background(), Query{Kind: KindMovie, Title: "x"})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("err = %v, want ErrRateLimited", err)
	}
}

// ---------------------------------------------------------------------------
// When the shape is not what this expects
// ---------------------------------------------------------------------------

// The failure mode this package is most likely to have, because no successful
// response has ever been parsed from the real service. It must be reported as
// itself, with the body, rather than as a generic failure.
func TestAnUnexpectedShapeSaysWhatArrived(t *testing.T) {
	for _, body := range []string{
		`<!DOCTYPE html><html><body>503 Service Unavailable</body></html>`,
		`not json at all`,
		``,
	} {
		f := newFake(t, body)
		_, err := f.client("t").Search(context.Background(), Query{Kind: KindMovie, Title: "x"})
		if !errors.Is(err, ErrUnexpectedShape) {
			t.Errorf("body %q: err = %v, want ErrUnexpectedShape", snippet([]byte(body)), err)
			continue
		}
		if body != "" && !strings.Contains(err.Error(), "body began") {
			t.Errorf("the error does not quote what arrived: %v", err)
		}
	}
}

// A response that IS json but thin must produce a thin result, not an error. A
// provider adding a field must never break this software, and a provider
// omitting one must not either.
func TestAThinResponseIsAThinResultNotAFailure(t *testing.T) {
	f := newFake(t, `{"results":[{"id":603,"title":"The Matrix"}],"total_results":1}`)
	got, err := f.client("t").Search(context.Background(),
		Query{Kind: KindMovie, Title: "the matrix"})
	if err != nil {
		t.Fatalf("a minimal but valid response failed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results", len(got))
	}
	m := got[0]
	if m.ProviderID != 603 || m.Title != "The Matrix" {
		t.Errorf("unexpected match: %+v", m)
	}
	// Absent fields are zero, not errors.
	if m.Year != 0 || m.PosterPath != "" || m.Overview != "" {
		t.Errorf("absent fields were invented: %+v", m)
	}
}

// A result with no id cannot be attached to anything, so offering it as a
// choice would be offering a button that does nothing.
func TestResultsWithNoIdentifierAreDropped(t *testing.T) {
	f := newFake(t, `{"results":[{"title":"No Id"},{"id":0,"title":"Zero"},{"id":603,"title":"Real"}]}`)
	got, err := f.client("t").Search(context.Background(), Query{Kind: KindMovie, Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Real" {
		t.Errorf("got %+v, want only the result with an id", got)
	}
}

// ---------------------------------------------------------------------------
// Films and series disagree about field names
// ---------------------------------------------------------------------------

// TMDB calls a film's name "title" and a series' name "name", and dates
// "release_date" and "first_air_date". Getting this wrong produces a search
// that silently returns untitled results for one of the two kinds.
func TestSeriesAndFilmFieldNamesAreBothUnderstood(t *testing.T) {
	t.Run("film", func(t *testing.T) {
		f := newFake(t, `{"results":[{"id":1,"title":"Arrival","original_title":"Arrival","release_date":"2016-11-11"}]}`)
		got, _ := f.client("t").Search(context.Background(), Query{Kind: KindMovie, Title: "arrival"})
		if len(got) != 1 || got[0].Title != "Arrival" || got[0].Year != 2016 {
			t.Errorf("film: %+v", got)
		}
		if !strings.HasPrefix(f.paths[0], "/search/movie?") {
			t.Errorf("film search hit %q", f.paths[0])
		}
	})

	t.Run("series", func(t *testing.T) {
		f := newFake(t, `{"results":[{"id":2,"name":"Severance","original_name":"Severance","first_air_date":"2022-02-18"}]}`)
		got, _ := f.client("t").Search(context.Background(), Query{Kind: KindSeries, Title: "severance"})
		if len(got) != 1 || got[0].Title != "Severance" || got[0].Year != 2022 {
			t.Errorf("series: %+v", got)
		}
		if !strings.HasPrefix(f.paths[0], "/search/tv?") {
			t.Errorf("series search hit %q", f.paths[0])
		}
	})

	// And the year parameter differs too, which is the part that silently
	// returns the wrong film rather than failing.
	t.Run("the year parameter is named per kind", func(t *testing.T) {
		f := newFake(t, `{"results":[]}`)
		c := f.client("t")
		_, _ = c.Search(context.Background(), Query{Kind: KindMovie, Title: "dune", Year: 2021})
		_, _ = c.Search(context.Background(), Query{Kind: KindSeries, Title: "dune", Year: 2021})
		if !strings.Contains(f.paths[0], "year=2021") ||
			strings.Contains(f.paths[0], "first_air_date_year") {
			t.Errorf("film search used %q", f.paths[0])
		}
		if !strings.Contains(f.paths[1], "first_air_date_year=2021") {
			t.Errorf("series search used %q", f.paths[1])
		}
	})
}

// ---------------------------------------------------------------------------
// Details
// ---------------------------------------------------------------------------

func TestDetailsForASeriesReadsItsExternalIds(t *testing.T) {
	// A film carries imdb_id at the top level; a series does not, and it has to
	// be asked for. One request, not two.
	f := newFake(t, `{"id":95396,"name":"Severance","first_air_date":"2022-02-18",
		"episode_run_time":[50],"genres":[{"name":"Drama"}],
		"seasons":[{"season_number":0,"name":"Specials","episode_count":3,"air_date":"2022-03-01"},
		           {"season_number":1,"name":"Season 1","episode_count":9,"air_date":"2022-02-18"}],
		"external_ids":{"imdb_id":"tt11280740"}}`)

	d, err := f.client("t").Details(context.Background(), KindSeries, 95396)
	if err != nil {
		t.Fatal(err)
	}
	if d.IMDbID != "tt11280740" {
		t.Errorf("imdb id = %q", d.IMDbID)
	}
	if d.Runtime != 50 {
		t.Errorf("runtime = %d; a series reports a LIST of episode runtimes", d.Runtime)
	}
	if len(d.Seasons) != 2 {
		t.Fatalf("seasons = %+v", d.Seasons)
	}
	// Season 0 is Specials, and an operator's files are frequently in it.
	// Dropping it would make those files permanently unmatchable.
	if d.Seasons[0].Number != 0 || d.Seasons[0].Episodes != 3 {
		t.Errorf("specials were dropped or mangled: %+v", d.Seasons[0])
	}
	if len(f.paths) != 1 {
		t.Errorf("%d requests for one title; a backfill would take an afternoon", len(f.paths))
	}
	if !strings.Contains(f.paths[0], "append_to_response=external_ids") {
		t.Errorf("external ids were not requested: %q", f.paths[0])
	}
}

func TestDetailsRefusesAnIdentifierThatIsNotOne(t *testing.T) {
	f := newFake(t, `{}`)
	for _, id := range []int64{0, -1} {
		if _, err := f.client("t").Details(context.Background(), KindMovie, id); err == nil {
			t.Errorf("id %d was accepted", id)
		}
	}
	if len(f.paths) != 0 {
		t.Errorf("a malformed id caused a request: %v", f.paths)
	}
}

// The envelope the live API returns for a title that does not exist — for a
// series, a season and a film alike (checked while ADR-0025 was written).
const realNotFoundBody = `{"success":false,"status_code":34,"status_message":"The resource you requested could not be found."}`

// A 404 is "no such title", not a shape problem. It used to be reported as the
// provider answering nonsense, which sent an operator looking at a working
// provider for a fault in a number they had typed.
func TestANotFoundIsNoSuchTitleNotABadShape(t *testing.T) {
	f := newFake(t, realNotFoundBody)
	f.status = http.StatusNotFound

	_, err := f.client("t").Details(context.Background(), KindSeries, 999999999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if errors.Is(err, ErrUnexpectedShape) {
		t.Errorf("a 404 is still also called a shape problem: %v", err)
	}
	// The provider's own words, and which request it was about.
	if !strings.Contains(err.Error(), "could not be found") || !strings.Contains(err.Error(), "/tv/999999999") {
		t.Errorf("the error does not say what was not found: %v", err)
	}
	if _, err := f.client("t").Episodes(context.Background(), 95396, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("a season that does not exist: err = %v, want ErrNotFound", err)
	}
}

// A successful call whose body carries no id is the shape problem, not a
// success with a zero id — which would be attached to a library item and never
// match anything again.
func TestDetailsWithNoIdIsAShapeProblem(t *testing.T) {
	f := newFake(t, `{"title":"Something","overview":"but no id"}`)
	_, err := f.client("t").Details(context.Background(), KindMovie, 603)
	if !errors.Is(err, ErrUnexpectedShape) {
		t.Errorf("err = %v, want ErrUnexpectedShape", err)
	}
}

// ---------------------------------------------------------------------------
// Check
// ---------------------------------------------------------------------------

func TestCheckLearnsWhereArtworkLives(t *testing.T) {
	f := newFake(t, `{"images":{"secure_base_url":"https://image.tmdb.org/t/p/",
		"poster_sizes":["w92","w154","w185","w342","w500","w780","original"]}}`)

	c := f.client("t")
	h, err := c.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !h.OK {
		t.Fatalf("healthy check reported not OK: %s", h.Detail)
	}
	// Trailing slash removed, because internal/artwork appends "/size/file".
	if h.ImageBase != "https://image.tmdb.org/t/p" {
		t.Errorf("image base = %q", h.ImageBase)
	}
	if c.ImageBase() != h.ImageBase {
		t.Error("the client did not remember the image base")
	}
	if len(h.Sizes) != 7 {
		t.Errorf("sizes = %v", h.Sizes)
	}
}

// The key works and the field this software needs is absent: report it, keep
// working from the verified fallback, and do not pretend either that it failed
// or that everything was fine.
func TestCheckFallsBackAudiblyWhenTheImageBaseIsMissing(t *testing.T) {
	f := newFake(t, `{"images":{"poster_sizes":["w200"]}}`)
	h, err := f.client("t").Check(context.Background())
	if err != nil {
		t.Fatalf("a usable response was treated as a failure: %v", err)
	}
	if !h.OK {
		t.Error("the key works; the check should say so")
	}
	if h.ImageBase != FallbackImageBase {
		t.Errorf("image base = %q, want the fallback", h.ImageBase)
	}
	if !strings.Contains(h.Detail, "secure_base_url") {
		t.Errorf("the fallback happened silently: %q", h.Detail)
	}
}

// ---------------------------------------------------------------------------
// Alternative titles
// ---------------------------------------------------------------------------

// The television shape is "results", and blanks and duplicates are dropped.
//
// The body is trimmed from what the live API returned for The Office (2316)
// while ADR-0023 was written, with a blank and a duplicate added.
func TestAlternativeTitlesReadTheTelevisionShape(t *testing.T) {
	f := newFake(t, `{"id":2316,"results":[
		{"iso_3166_1":"US","title":"The Office (US)","type":""},
		{"iso_3166_1":"GB","title":"The Office (U.S.)","type":"title on Netflix"},
		{"iso_3166_1":"US","title":"  ","type":""},
		{"iso_3166_1":"CA","title":"The Office (US)","type":""}]}`)
	got, err := f.client("t").AlternativeTitles(context.Background(), 2316)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"The Office (US)", "The Office (U.S.)"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %q, want %q", got, want)
	}
	if len(f.paths) != 1 || !strings.HasPrefix(f.paths[0], "/tv/2316/alternative_titles?") {
		t.Errorf("asked %v, want /tv/2316/alternative_titles", f.paths)
	}
}

// The FILM shape answers with "titles", not "results". Decoding it as the
// television shape must yield nothing rather than something — this pins the
// reason the struct is television-only.
func TestTheFilmShapeIsNotMistakenForTheTelevisionOne(t *testing.T) {
	f := newFake(t, `{"id":603,"titles":[{"iso_3166_1":"US","title":"The Matrix 1","type":""}]}`)
	got, err := f.client("t").AlternativeTitles(context.Background(), 603)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a film-shaped body produced %q", got)
	}
}

// A film's names come from one request: its title, its ORIGINAL title — which
// TMDB does not list among the alternatives — and the alternatives, in that
// order, without blanks or repeats (ADR-0026).
//
// Trimmed from what the live API returned for Amélie (194) while ADR-0026 was
// written, with a blank and a repeat of the title added.
func TestFilmTitlesAreTheTitleTheOriginalAndTheAlternatives(t *testing.T) {
	f := newFake(t, `{"id":194,"title":"Amélie","original_title":"Le Fabuleux Destin d'Amélie Poulain",
		"release_date":"2001-04-25","alternative_titles":{"titles":[
		{"iso_3166_1":"US","title":"Amelie","type":""},
		{"iso_3166_1":"GB","title":"The Fabulous Destiny of Amelie Poulain","type":""},
		{"iso_3166_1":"FR","title":"Amélie","type":""},
		{"iso_3166_1":"UA","title":"  ","type":""}]}}`)
	got, err := f.client("t").FilmTitles(context.Background(), 194)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Amélie", "Le Fabuleux Destin d'Amélie Poulain", "Amelie",
		"The Fabulous Destiny of Amelie Poulain"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if len(f.paths) != 1 || f.paths[0] != "/movie/194?append_to_response=alternative_titles" {
		t.Errorf("asked %v; want the one request /movie/194?append_to_response=alternative_titles", f.paths)
	}
}

// A body shaped like the TELEVISION alternative titles yields the film's own
// names and none of the list — the same trap, the other way round, that
// TestTheFilmShapeIsNotMistakenForTheTelevisionOne pins.
func TestTheTelevisionShapeIsNotReadAsAFilms(t *testing.T) {
	f := newFake(t, `{"id":438631,"title":"Dune","original_title":"Dune",
		"alternative_titles":{"results":[{"iso_3166_1":"US","title":"Dune: Part One","type":""}]}}`)
	got, err := f.client("t").FilmTitles(context.Background(), 438631)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "Dune" {
		t.Errorf("got %q, want only [\"Dune\"]", got)
	}
}

// No id, or no title at all, is a shape problem rather than a film with no
// names — which would make every release "a different film".
func TestFilmTitlesWithNothingInThemAreAShapeProblem(t *testing.T) {
	for name, body := range map[string]string{
		"no id":    `{"title":"Dune"}`,
		"no title": `{"id":438631,"title":" ","original_title":"","alternative_titles":{"titles":[]}}`,
	} {
		f := newFake(t, body)
		if _, err := f.client("t").FilmTitles(context.Background(), 438631); !errors.Is(err, ErrUnexpectedShape) {
			t.Errorf("%s: err = %v, want ErrUnexpectedShape", name, err)
		}
	}
}

// A film the provider does not have is ErrNotFound, with its path.
func TestFilmTitlesOfAFilmThatDoesNotExist(t *testing.T) {
	f := newFake(t, realNotFoundBody)
	f.status = http.StatusNotFound
	_, err := f.client("t").FilmTitles(context.Background(), 999999999)
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "/movie/999999999") {
		t.Errorf("err = %v, want ErrNotFound naming /movie/999999999", err)
	}
	if _, err := f.client("t").FilmTitles(context.Background(), 0); err == nil {
		t.Error("id 0 was accepted")
	}
	if len(f.paths) != 1 {
		t.Errorf("a malformed id caused a request: %v", f.paths)
	}
}

// A malformed identifier causes no request.
func TestAlternativeTitlesRefuseANonIdentifier(t *testing.T) {
	f := newFake(t, `{}`)
	if _, err := f.client("t").AlternativeTitles(context.Background(), 0); err == nil {
		t.Error("id 0 was accepted")
	}
	if len(f.paths) != 0 {
		t.Errorf("a malformed id caused a request: %v", f.paths)
	}
}

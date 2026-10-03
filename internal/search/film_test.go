package search

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/release"
)

// ---------------------------------------------------------------------------
// Matching a film (ADR-0026)
// ---------------------------------------------------------------------------

// Dune (2021), with two of the names TMDB lists for it.
func dune2021() FilmWant {
	return FilmWant{ItemID: 7, Titles: []string{"Dune", "Dune: Part One"}, Year: 2021}
}

// Every release name here was read by the real parser, and the verdicts are
// the ones ADR-0026's table promises, in its order.
func TestEachReleaseIsJudgedAgainstTheFilm(t *testing.T) {
	w := dune2021()
	for _, tc := range []struct {
		name   string
		reason string // "" means a match
		detail string // a fragment the reason must contain
	}{
		{"Dune.2021.1080p.BluRay.x264-GRP", "", ""},
		{"Dune.2021.IMAX.1080p.WEB-DL.DDP5.1.Atmos.H.264-GRP", "", ""},
		{"Dune (2021) [1080p] [BluRay] [5.1] [YTS.MX]", "", ""},
		// An alternative title is one of its names.
		{"Dune.Part.One.2021.720p.WEB.H264-ALT", "", ""},
		// One year out is the same film released later somewhere else.
		{"Dune.2022.1080p.WEB.H264-GRP", "", ""},
		{"Dune.Prophecy.S01E01.1080p.WEB.H264-GRP", ReasonTelevision, "S01E01"},
		// Television is said before anything else, even with the right title.
		{"Dune.2000.S01E01.720p.HDTV.x264-GRP", ReasonTelevision, "television, not a film"},
		{"Dune.S01.1080p.WEB.H264-GRP", ReasonTelevision, "season 1"},
		{"Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1-GRP", ReasonNotThisFilm, "Dune Part Two (2024)"},
		{"Hans.Zimmer-Dune.OST-WEB-2021-ENRiCH", ReasonNotThisFilm, "different film"},
		{"Dune.1080p.WEB.H264-NOYEAR", ReasonNoYear, "another film called Dune"},
		{"Dune.1984.1080p.BluRay.x264-OLD", ReasonNotThisFilm, "Dune (1984), not the one from 2021"},
		{"Dune.2019.1080p.WEB.H264-GRP", ReasonNotThisFilm, "not the one from 2021"},
		{"1080p.WEB.H264", release.ReasonUnparsed, "which film"},
	} {
		rej := MatchFilm(release.Parse(tc.name), w)
		switch {
		case tc.reason == "" && rej != nil:
			t.Errorf("%s: refused (%s: %s), want a match", tc.name, rej.Reason, rej.Detail)
		case tc.reason != "" && rej == nil:
			t.Errorf("%s: matched, want %s", tc.name, tc.reason)
		case tc.reason != "" && (rej.Reason != tc.reason || !strings.Contains(rej.Detail, tc.detail)):
			t.Errorf("%s: %s %q, want %s containing %q", tc.name, rej.Reason, rej.Detail, tc.reason, tc.detail)
		}
	}
}

// The names a film is released under that are not its English title: the
// original title, and the scene's spelling among the alternatives. Each one is
// what makes that release match; without it, the same release is a different
// film.
func TestAFilmIsFoundUnderEveryNameItGoesBy(t *testing.T) {
	for _, tc := range []struct {
		release string
		titles  []string
		year    int
	}{
		{"Le.Fabuleux.Destin.d'Amelie.Poulain.2001.1080p.BluRay.x264-GRP",
			[]string{"Amélie", "Le Fabuleux Destin d'Amélie Poulain"}, 2001},
		// Accents folded: the release has none.
		{"Amelie.2001.1080p.BluRay.x264-GRP", []string{"Amélie"}, 2001},
		{"Star.Wars.Episode.IV.A.New.Hope.1977.1080p.BluRay.x264-GRP",
			[]string{"Star Wars", "Star Wars: Episode IV - A New Hope"}, 1977},
		{"Sen.to.Chihiro.no.Kamikakushi.2001.1080p.BluRay.x264-GRP",
			[]string{"Spirited Away", "千と千尋の神隠し", "Sen to Chihiro no Kamikakushi"}, 2001},
		// A title that is a number, and one that starts with one, are titles.
		{"1917.2019.1080p.BluRay.x264-GRP", []string{"1917"}, 2019},
		{"2001.A.Space.Odyssey.1968.1080p.BluRay.x264-GRP", []string{"2001: A Space Odyssey"}, 1968},
		{"Blade.Runner.2049.2017.2160p.UHD.BluRay.x265-GRP", []string{"Blade Runner 2049"}, 2017},
	} {
		w := FilmWant{ItemID: 1, Titles: tc.titles, Year: tc.year}
		if rej := MatchFilm(release.Parse(tc.release), w); rej != nil {
			t.Errorf("%s: refused: %s", tc.release, rej.Detail)
		}
		if len(tc.titles) > 1 {
			w.Titles = tc.titles[:1]
			if rej := MatchFilm(release.Parse(tc.release), w); rej == nil || rej.Reason != ReasonNotThisFilm {
				t.Errorf("%s: matched %q without the name it is released under: %+v",
					tc.release, tc.titles[0], rej)
			}
		}
	}
}

// A film whose year is not known matches nothing: every release of its title
// could be a remake.
func TestAFilmWithNoYearMatchesNothing(t *testing.T) {
	w := FilmWant{ItemID: 1, Titles: []string{"Dune"}}
	rej := MatchFilm(release.Parse("Dune.2021.1080p.BluRay.x264-GRP"), w)
	if rej == nil || rej.Reason != ReasonNoYear || !strings.Contains(rej.Detail, "year of Dune is not known") {
		t.Errorf("a film with no year matched, or said so wrongly: %+v", rej)
	}
}

// The default term is the folded title and the year.
func TestAFilmIsAskedForByItsFoldedTitleAndYear(t *testing.T) {
	for _, tc := range []struct {
		title string
		year  int
		want  string
	}{
		{"Dune", 2021, "dune 2021"},
		{"Amélie", 2001, "amelie 2001"},
		{"Star Wars: Episode IV - A New Hope", 1977, "star wars episode iv a new hope 1977"},
		{"Fast & Furious", 2009, "fast and furious 2009"},
		{"Dune", 0, "dune"},
		{"!!!", 2020, "!!! 2020"},
	} {
		if got := FilmTerm(tc.title, tc.year); got != tc.want {
			t.Errorf("FilmTerm(%q, %d) = %q, want %q", tc.title, tc.year, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// The search
// ---------------------------------------------------------------------------

// A film search asks a general search for the film's term, marks only the
// matches as the film, keeps every refusal with its reason, and puts the
// matches first however few seeders they have.
func TestAFilmSearchMarksTheMatchesAndSaysWhyTheRestAreNot(t *testing.T) {
	var seen indexer.Query
	var mu sync.Mutex
	svc := New(fakeSource{defs("A")}, searcherFunc(func(_ context.Context,
		_ indexer.Definition, q indexer.Query) ([]indexer.Result, error) {
		mu.Lock()
		seen = q
		mu.Unlock()
		return []indexer.Result{
			result("A", "Dune.Part.Two.2024.2160p.WEB-DL.DDP5.1-GRP", 900),
			result("A", "Dune.Prophecy.S01E01.1080p.WEB.H264-GRP", 800),
			result("A", "Dune.1984.1080p.BluRay.x264-OLD", 700),
			result("A", "Dune.1080p.WEB.H264-NOYEAR", 600),
			result("A", "Dune.2021.1080p.BluRay.x264-GRP", 5),
		}, nil
	}), nil)

	resp, err := svc.SearchFilm(context.Background(), FilmSearch{Want: dune2021()})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	// A general search, by title and year: not a TV search, and no IMDb id.
	if seen.Term != "dune 2021" || seen.Season != -1 || seen.Episode != 0 || seen.IMDBID != "" {
		t.Errorf("asked the indexer for %+v, want the general search \"dune 2021\"", seen)
	}
	mu.Unlock()

	if len(resp.Candidates) != 5 {
		t.Fatalf("%d candidates; refusals must come back, not vanish", len(resp.Candidates))
	}
	first := resp.Candidates[0]
	if first.Title != "Dune.2021.1080p.BluRay.x264-GRP" || !first.Accepted ||
		first.Target == nil || *first.Target != (Target{ItemID: 7, Film: true}) {
		t.Errorf("first candidate = %s accepted=%v target=%+v; the one match must lead, "+
			"however few seeders it has", first.Title, first.Accepted, first.Target)
	}
	reasons := map[string]bool{}
	for _, c := range resp.Candidates[1:] {
		if c.Accepted || c.Target != nil || c.Rejection == nil {
			t.Errorf("%s: accepted=%v target=%+v rejection=%+v; a candidate that is not the "+
				"film must be refused with a reason and carry no target", c.Title, c.Accepted, c.Target, c.Rejection)
			continue
		}
		reasons[c.Rejection.Reason] = true
	}
	for _, want := range []string{ReasonNotThisFilm, ReasonTelevision, ReasonNoYear} {
		if !reasons[want] {
			t.Errorf("no candidate was refused as %s: %v", want, reasons)
		}
	}
}

// Matching the film makes a release eligible, not acceptable: the profile still
// judges it — which is what keeps a camera recording of a film still in cinemas
// ungrabbable when a profile is chosen.
func TestAMatchingFilmTheProfileRefusesIsStillRefused(t *testing.T) {
	svc := New(fakeSource{defs("A")}, &fakeSearcher{byName: map[string][]indexer.Result{
		"A": {result("A", "Dune.2021.HDCAM.x264-CAMGRP", 50)},
	}}, nil)
	resp, err := svc.SearchFilm(context.Background(), FilmSearch{Want: dune2021(), Profile: hdProfile(t)})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 1 || resp.Candidates[0].Accepted {
		t.Fatalf("a CAM the profile forbids was accepted: %+v", resp.Candidates)
	}
	if resp.Candidates[0].Target == nil {
		t.Error("the CAM IS the film; refused by the profile, it should still say so")
	}
}

// Another term changes what is asked, not what can match.
func TestAnotherTermDoesNotWidenWhatMatchesAFilm(t *testing.T) {
	var seen string
	svc := New(fakeSource{defs("A")}, searcherFunc(func(_ context.Context,
		_ indexer.Definition, q indexer.Query) ([]indexer.Result, error) {
		seen = q.Term
		return []indexer.Result{result("A", "Dune.Part.Two.2024.1080p.WEB.H264-GRP", 1)}, nil
	}), nil)
	resp, err := svc.SearchFilm(context.Background(), FilmSearch{Want: dune2021(), Term: "Dune Part Two"})
	if err != nil {
		t.Fatal(err)
	}
	if seen != "Dune Part Two" {
		t.Errorf("the indexer was asked for %q", seen)
	}
	if resp.Candidates[0].Accepted || resp.Candidates[0].Target != nil {
		t.Error("searching under another name made another film grabbable for this one")
	}
}

// A request that does not name a film with a year is refused before any
// indexer is asked.
func TestAFilmSearchNeedsAFilmWithAYear(t *testing.T) {
	f := &fakeSearcher{}
	svc := New(fakeSource{defs("A")}, f, nil)
	for _, w := range []FilmWant{
		{Titles: []string{"Dune"}, Year: 2021},
		{ItemID: 7, Year: 2021},
		{ItemID: 7, Titles: []string{"Dune"}},
	} {
		if _, err := svc.SearchFilm(context.Background(), FilmSearch{Want: w}); err == nil {
			t.Errorf("%+v was searched for", w)
		}
	}
	if f.calls.Load() != 0 {
		t.Errorf("%d indexer calls for requests that name no film", f.calls.Load())
	}
}

// ---------------------------------------------------------------------------
// The sealed film
// ---------------------------------------------------------------------------

// The film a release was matched to survives the ticket, sealed.
func TestATicketCarriesAFilm(t *testing.T) {
	tk := NewTickets(testCipher(t), time.Hour, nil)
	c := testCandidate()
	c.Target = &Target{ItemID: 7, Film: true}
	token, err := tk.Seal(c, 42)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tk.Open(token, 42)
	if err != nil {
		t.Fatal(err)
	}
	if got.Target == nil || *got.Target != (Target{ItemID: 7, Film: true}) {
		t.Errorf("target = %+v, want the film", got.Target)
	}
	if got.Target.Code() != "" {
		t.Errorf("a film rendered a code: %q", got.Target.Code())
	}
}

// A target is one form or the other, never a mixture — and never nothing.
func TestATargetIsAnEpisodeOrAFilmAndNotBoth(t *testing.T) {
	for _, tc := range []struct {
		t     Target
		valid bool
	}{
		{Target{ItemID: 4, Season: 2, Episode: 3}, true},
		{Target{ItemID: 4, Season: 0, Episode: 1}, true},
		{Target{ItemID: 7, Film: true}, true},
		{Target{ItemID: 7, Season: 2, Episode: 3, Film: true}, false},
		{Target{ItemID: 7, Episode: 3, Film: true}, false},
		{Target{ItemID: 7, Season: 1, Film: true}, false},
		{Target{Film: true}, false},
		{Target{ItemID: 4, Season: 2}, false},
		{Target{}, false},
	} {
		if got := tc.t.Valid(); got != tc.valid {
			t.Errorf("%+v: Valid() = %v, want %v", tc.t, got, tc.valid)
		}
	}
	tk := NewTickets(testCipher(t), time.Hour, nil)
	c := testCandidate()
	c.Target = &Target{ItemID: 7, Season: 2, Episode: 3, Film: true}
	if _, err := tk.Seal(c, 42); err == nil {
		t.Error("a target that is both a film and an episode was sealed")
	}
}

// A mixed target that somehow got sealed — by a version of this code that did
// not check — is refused when opened, rather than handed to the import.
func TestAMixedTargetIsNotOpened(t *testing.T) {
	tk := NewTickets(testCipher(t), time.Hour, nil)
	raw, err := json.Marshal(Ticket{
		IndexerID: 7, DownloadURL: "https://indexer.example.com/dl/abc.torrent",
		Title: "Dune.2021.1080p.BluRay.x264-GRP", IssuedFor: 42,
		ExpiresAt: time.Now().Add(time.Hour).UTC(),
		Target:    &Target{ItemID: 7, Season: 2, Episode: 3, Film: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := tk.cipher.Encrypt(raw, ticketContext(42))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tk.Open(base64.RawURLEncoding.EncodeToString(sealed), 42); !errors.Is(err, ErrTicketInvalid) {
		t.Errorf("err = %v, want ErrTicketInvalid", err)
	}
}

// A ticket sealed before films existed carries no "fm" and opens as the
// episode it always was.
func TestAnEpisodeTicketFromBeforeFilmsStillOpens(t *testing.T) {
	tk := NewTickets(testCipher(t), time.Hour, nil)
	raw := []byte(`{"i":7,"u":"https://indexer.example.com/dl/abc.torrent","t":"Severance.S02E03",` +
		`"f":42,"e":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano) + `","g":{"m":4,"s":2,"e":3}}`)
	sealed, err := tk.cipher.Encrypt(raw, ticketContext(42))
	if err != nil {
		t.Fatal(err)
	}
	got, err := tk.Open(base64.RawURLEncoding.EncodeToString(sealed), 42)
	if err != nil {
		t.Fatal(err)
	}
	if got.Target == nil || *got.Target != (Target{ItemID: 4, Season: 2, Episode: 3}) {
		t.Errorf("target = %+v", got.Target)
	}
}

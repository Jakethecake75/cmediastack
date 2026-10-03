package search

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/release"
)

// ---------------------------------------------------------------------------
// Title folding
// ---------------------------------------------------------------------------

func TestTitlesFoldToTheFormTheSceneUses(t *testing.T) {
	same := [][2]string{
		// The three from ADR-0023, as TMDB's alternative titles spell them and
		// as the release parser reads the scene's names.
		{"The Office (US)", "The Office US"},
		{"Law and Order SVU", "Law and Order SVU"},
		{"Law & Order: SVU", "Law and Order SVU"},
		{"Marvel's Daredevil", "Marvels Daredevil"},
		{"Marvel’s Daredevil", "Marvels Daredevil"}, // a typographic apostrophe
		{"Pokémon", "Pokemon"},
		{"  Severance  ", "severance"},
		{"Star Trek: Discovery", "Star Trek Discovery"},
	}
	for _, p := range same {
		if a, b := NormalizeTitle(p[0]), NormalizeTitle(p[1]); a != b {
			t.Errorf("%q folds to %q and %q to %q; they should agree", p[0], a, p[1], b)
		}
	}

	different := [][2]string{
		{"Severance", "Severance Pay"},
		{"The Office", "The Office US"},
		{"Doctor Who", "Doctor Who Confidential"},
	}
	for _, p := range different {
		if NormalizeTitle(p[0]) == NormalizeTitle(p[1]) {
			t.Errorf("%q and %q fold to the same title; they are different shows", p[0], p[1])
		}
	}
}

// Folding is stable, and never leaves the spacing it is meant to remove.
func FuzzNormalizeTitle(f *testing.F) {
	for _, s := range []string{"The Office (US)", "Law & Order: SVU", "Pokémon", "a&&b", "&", "''", "  x  "} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n := NormalizeTitle(s)
		if again := NormalizeTitle(n); again != n {
			t.Fatalf("not stable: %q -> %q -> %q", s, n, again)
		}
		if strings.HasPrefix(n, " ") || strings.HasSuffix(n, " ") || strings.Contains(n, "  ") {
			t.Fatalf("%q -> %q has stray spaces", s, n)
		}
		if strings.ToLower(n) != n {
			t.Fatalf("%q -> %q is not folded to lower case", s, n)
		}
	})
}

// ---------------------------------------------------------------------------
// Matching
// ---------------------------------------------------------------------------

func severanceS02E03() EpisodeWant {
	return EpisodeWant{ItemID: 4, Titles: []string{"Severance"}, Year: 2022, Season: 2, Episode: 3}
}

// Every release name here was read by the real parser (release.Parse), and the
// verdicts are the ones ADR-0023's table promises.
func TestEachReleaseIsJudgedAgainstTheEpisode(t *testing.T) {
	w := severanceS02E03()
	for _, tc := range []struct {
		name   string
		reason string // "" means a match
		detail string // a fragment the reason must contain
	}{
		{"Severance.S02E03.1080p.WEB.H264-SuccessfulCrab", "", ""},
		{"Severance S02E03 Who Is Alive 1080p ATVP WEB-DL DDP5 1 Atmos H 264-FLUX", "", ""},
		{"Severance.2022.S02E03.1080p.WEB.H264-GRP", "", ""},
		{"Severance.S02E03-E04.1080p.WEB.H264-GRP", "", ""},
		{"Severance.S02E02E03.1080p.WEB.H264-GRP", "", ""},
		{"Severance.S02E04.1080p.WEB.H264-GRP", ReasonNotThisEpisode, "S02E04, not S02E03"},
		{"Severance.S01E03.1080p.WEB.H264-GRP", ReasonNotThisEpisode, "season 1, not season 2"},
		{"Severance.S02.1080p.ATVP.WEB-DL.DDP5.1.H.264-FLUX", ReasonSeasonPack, "whole-season pack"},
		{"Severance.Pay.S02E03.720p.HDTV.x264-GRP", ReasonNotThisSeries, `"Severance Pay"`},
		{"Severance.1998.S02E03.720p.HDTV.x264-GRP", ReasonNotThisSeries, "not the one from 2022"},
		{"The.Daily.Show.2024.03.15.1080p.WEB.h264-EDITH", ReasonNotThisSeries, "different series"},
		{"1080p.WEB.H264", release.ReasonUnparsed, "which series"},
	} {
		rej := MatchEpisode(release.Parse(tc.name), w)
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

// Alternative titles are what make a scene name match.
func TestAnAlternativeTitleMatchesTheScenesName(t *testing.T) {
	w := EpisodeWant{ItemID: 9, Titles: []string{"The Office", "The Office (US)"},
		Year: 2005, Season: 5, Episode: 3}
	if rej := MatchEpisode(release.Parse("The.Office.US.S05E03.720p.HDTV.x264-CTU"), w); rej != nil {
		t.Errorf("refused: %s", rej.Detail)
	}
	w.Titles = w.Titles[:1]
	if rej := MatchEpisode(release.Parse("The.Office.US.S05E03.720p.HDTV.x264-CTU"), w); rej == nil ||
		rej.Reason != ReasonNotThisSeries {
		t.Errorf("without the alternative title the scene name matched anyway: %+v", rej)
	}
}

// One year either way: the right Battlestar, and not the wrong Doctor Who.
func TestTheYearIsAllowedToBeOneOut(t *testing.T) {
	bsg := EpisodeWant{ItemID: 1, Titles: []string{"Battlestar Galactica"}, Year: 2004, Season: 1, Episode: 1}
	if rej := MatchEpisode(release.Parse("Battlestar.Galactica.2003.S01E01.720p.BluRay.x264-DON"), bsg); rej != nil {
		t.Errorf("Battlestar Galactica refused: %s", rej.Detail)
	}
	who := EpisodeWant{ItemID: 2, Titles: []string{"Doctor Who"}, Year: 2005, Season: 1, Episode: 1}
	if rej := MatchEpisode(release.Parse("Doctor.Who.1963.S01E01.DVDRip.x264-GRP"), who); rej == nil {
		t.Error("the 1963 Doctor Who was taken for the 2005 one")
	}
	if rej := MatchEpisode(release.Parse("Doctor.Who.2005.S01E01.1080p.BluRay.x264-GRP"), who); rej != nil {
		t.Errorf("the right Doctor Who refused: %s", rej.Detail)
	}
}

// Daily and absolute numbering are refused with their own reason, not
// misreported as the wrong episode.
func TestNumberingThisSoftwareCannotMatchSaysSo(t *testing.T) {
	daily := EpisodeWant{ItemID: 1, Titles: []string{"The Daily Show"}, Season: 29, Episode: 30}
	rej := MatchEpisode(release.Parse("The.Daily.Show.2024.03.15.1080p.WEB.h264-EDITH"), daily)
	if rej == nil || rej.Reason != ReasonNotSeasonNumbered || !strings.Contains(rej.Detail, "air date") {
		t.Errorf("daily: %+v", rej)
	}
}

// ---------------------------------------------------------------------------
// The search
// ---------------------------------------------------------------------------

// An episode search asks for the episode, marks only the matches, keeps the
// refusals with their reasons, and puts the matches first.
func TestAnEpisodeSearchMarksTheMatchesAndSaysWhyTheRestAreNot(t *testing.T) {
	var seen indexer.Query
	var mu sync.Mutex
	svc := New(fakeSource{defs("A")}, searcherFunc(func(_ context.Context,
		_ indexer.Definition, q indexer.Query) ([]indexer.Result, error) {
		mu.Lock()
		seen = q
		mu.Unlock()
		return []indexer.Result{
			result("A", "Severance.S02.1080p.ATVP.WEB-DL.DDP5.1.H.264-FLUX", 500),
			result("A", "Severance.Pay.S02E03.720p.HDTV.x264-GRP", 400),
			result("A", "Severance.S02E04.1080p.WEB.H264-GRP", 300),
			result("A", "Severance.S02E03.1080p.WEB.H264-SuccessfulCrab", 20),
		}, nil
	}), nil)

	resp, err := svc.SearchEpisode(context.Background(), EpisodeSearch{Want: severanceS02E03()})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	if seen.Term != "Severance" || seen.Season != 2 || seen.Episode != 3 {
		t.Errorf("asked the indexer for %+v, want Severance season 2 episode 3", seen)
	}
	mu.Unlock()

	if len(resp.Candidates) != 4 {
		t.Fatalf("%d candidates; refusals must come back, not vanish", len(resp.Candidates))
	}
	first := resp.Candidates[0]
	if !first.Accepted || first.Target == nil || *first.Target != (Target{ItemID: 4, Season: 2, Episode: 3}) {
		t.Errorf("first candidate = %s accepted=%v target=%+v; the one match must lead, "+
			"however few seeders it has", first.Title, first.Accepted, first.Target)
	}
	for _, c := range resp.Candidates[1:] {
		if c.Accepted || c.Target != nil || c.Rejection == nil {
			t.Errorf("%s: accepted=%v target=%+v rejection=%+v; a candidate that is not the "+
				"episode must be refused with a reason and carry no target", c.Title, c.Accepted, c.Target, c.Rejection)
		}
	}
}

// Matching the episode makes a release eligible, not acceptable: the profile
// still judges it.
func TestAMatchingReleaseTheProfileRefusesIsStillRefused(t *testing.T) {
	svc := New(fakeSource{defs("A")}, &fakeSearcher{byName: map[string][]indexer.Result{
		"A": {result("A", "Severance.S02E03.CAM.x264-GRP", 50)},
	}}, nil)
	resp, err := svc.SearchEpisode(context.Background(), EpisodeSearch{
		Want: severanceS02E03(), Profile: hdProfile(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 1 || resp.Candidates[0].Accepted {
		t.Fatalf("a CAM the profile forbids was accepted: %+v", resp.Candidates)
	}
}

// A different search term does not widen what is grabbable.
func TestAnotherTermDoesNotWidenWhatMatches(t *testing.T) {
	var seen string
	svc := New(fakeSource{defs("A")}, searcherFunc(func(_ context.Context,
		_ indexer.Definition, q indexer.Query) ([]indexer.Result, error) {
		seen = q.Term
		return []indexer.Result{result("A", "Severance.Pay.S02E03.720p.HDTV.x264-GRP", 1)}, nil
	}), nil)
	resp, err := svc.SearchEpisode(context.Background(), EpisodeSearch{
		Want: severanceS02E03(), Term: "Severance Pay",
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != "Severance Pay" {
		t.Errorf("the indexer was asked for %q", seen)
	}
	if resp.Candidates[0].Accepted || resp.Candidates[0].Target != nil {
		t.Error("searching under another name made another show grabbable for this one")
	}
}

// A request that does not name an episode is refused before any indexer is
// asked.
func TestAnEpisodeSearchNeedsAnEpisode(t *testing.T) {
	f := &fakeSearcher{}
	svc := New(fakeSource{defs("A")}, f, nil)
	for _, w := range []EpisodeWant{
		{Titles: []string{"Severance"}, Season: 2, Episode: 3},
		{ItemID: 4, Season: 2, Episode: 3},
		{ItemID: 4, Titles: []string{"Severance"}, Season: 2},
		{ItemID: 4, Titles: []string{"Severance"}, Season: -1, Episode: 3},
	} {
		if _, err := svc.SearchEpisode(context.Background(), EpisodeSearch{Want: w}); err == nil {
			t.Errorf("%+v was searched for", w)
		}
	}
	if f.calls.Load() != 0 {
		t.Errorf("%d indexer calls for requests that name no episode", f.calls.Load())
	}
}

// ---------------------------------------------------------------------------
// The sealed target
// ---------------------------------------------------------------------------

// The episode a release was matched to survives the ticket, sealed.
func TestATicketCarriesItsTarget(t *testing.T) {
	tk := NewTickets(testCipher(t), time.Hour, nil)
	c := testCandidate()
	c.Target = &Target{ItemID: 4, Season: 2, Episode: 3}

	token, err := tk.Seal(c, 42)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tk.Open(token, 42)
	if err != nil {
		t.Fatal(err)
	}
	if got.Target == nil || *got.Target != *c.Target {
		t.Errorf("target = %+v, want %+v", got.Target, c.Target)
	}

	plain, err := tk.Seal(testCandidate(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if opened, _ := tk.Open(plain, 42); opened.Target != nil {
		t.Errorf("a ticket from the general search came back with a target: %+v", opened.Target)
	}
}

// A target that names nothing is not sealed.
func TestAnInvalidTargetIsNotSealed(t *testing.T) {
	tk := NewTickets(testCipher(t), time.Hour, nil)
	c := testCandidate()
	c.Target = &Target{ItemID: 4, Season: 2}
	if _, err := tk.Seal(c, 42); err == nil {
		t.Error("a target with no episode was sealed")
	}
}

// TestADailyEpisodeIsKnownByItsAirDate pins ADR-0064, decisions 1 and 2: a
// release dated the day the episode aired is the episode, another day is not,
// a daily series is asked for by date, and a numbered release still matches.
func TestADailyEpisodeIsKnownByItsAirDate(t *testing.T) {
	w := EpisodeWant{ItemID: 9, Titles: []string{"The Daily Show"}, Year: 1996, Season: 30, Episode: 120, AirDate: "2026-10-02"}
	for _, tc := range []struct {
		name   string
		reason string
	}{
		{"The.Daily.Show.2026.10.02.Guest.Name.1080p.WEB.h264-GRP", ""},
		{"The.Daily.Show.S30E120.1080p.WEB.h264-GRP", ""},
		{"The.Daily.Show.2026.10.01.Guest.Name.1080p.WEB.h264-GRP", ReasonNotThisEpisode},
		{"The.Nightly.Show.2026.10.02.1080p.WEB.h264-GRP", ReasonNotThisSeries},
		{"The.Daily.Show.(1990).2026.10.02.1080p.WEB.h264-GRP", ReasonNotThisSeries},
	} {
		rej := MatchEpisode(release.Parse(tc.name), w)
		switch {
		case tc.reason == "" && rej != nil:
			t.Errorf("%s: refused %+v", tc.name, rej)
		case tc.reason != "" && (rej == nil || rej.Reason != tc.reason):
			t.Errorf("%s: %+v, want %s", tc.name, rej, tc.reason)
		}
	}
	undated := w
	undated.AirDate = ""
	if rej := MatchEpisode(release.Parse("The.Daily.Show.2026.10.02.1080p.WEB.h264-GRP"), undated); rej == nil ||
		rej.Reason != ReasonNotSeasonNumbered {
		t.Errorf("an episode with no air date took a dated release: %+v", rej)
	}

	var seen indexer.Query
	var mu sync.Mutex
	svc := New(fakeSource{defs("A")}, searcherFunc(func(_ context.Context, _ indexer.Definition,
		q indexer.Query) ([]indexer.Result, error) {
		mu.Lock()
		seen = q
		mu.Unlock()
		return []indexer.Result{result("A", "The.Daily.Show.2026.10.02.Guest.Name.1080p.WEB.h264-GRP", 9)}, nil
	}), nil)
	resp, err := svc.SearchEpisode(context.Background(), EpisodeSearch{Want: w, ByDate: true})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if seen.AirDate != "2026-10-02" {
		t.Errorf("asked %+v, want the air date", seen)
	}
	mu.Unlock()
	if len(resp.Candidates) != 1 || resp.Candidates[0].Target == nil ||
		*resp.Candidates[0].Target != (Target{ItemID: 9, Season: 30, Episode: 120}) {
		t.Errorf("candidates %+v", resp.Candidates)
	}
	if _, err := svc.SearchEpisode(context.Background(), EpisodeSearch{Want: w}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen.AirDate != "" {
		t.Errorf("a series not daily was asked by date: %+v", seen)
	}
}

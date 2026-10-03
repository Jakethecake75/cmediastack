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

func severanceS02() SeasonWant {
	return SeasonWant{ItemID: 4, Titles: []string{"Severance"}, Year: 2022, Season: 2}
}

// TestASeasonPackIsOnlyItsOwnSeason pins ADR-0033, decision 2: a pack of the
// wanted season, and nothing that merely resembles one.
func TestASeasonPackIsOnlyItsOwnSeason(t *testing.T) {
	w := severanceS02()
	for _, tc := range []struct {
		name   string
		reason string // "" means a match
		detail string
	}{
		{"Severance.S02.1080p.ATVP.WEB-DL.DDP5.1.H.264-FLUX", "", ""},
		{"Severance.S02.COMPLETE.1080p.WEB.H264-GRP", "", ""},
		{"Severance.Season.2.1080p.WEB.H264-GRP", "", ""},
		{"Severance.2022.S02.1080p.WEB.H264-GRP", "", ""},
		{"Severance.S03.1080p.WEB.H264-GRP", ReasonNotThisEpisode, "season 3, not season 2"},
		{"Severance.S01-S02.1080p.WEB.H264-GRP", ReasonSeveralSeasons, "several seasons"},
		{"Severance.S02.S03.1080p.WEB.H264-GRP", ReasonSeveralSeasons, "several seasons"},
		{"Severance.Complete.Series.1080p.WEB.H264-GRP", ReasonSeveralSeasons, "several seasons"},
		{"Severance.S02E01.1080p.WEB.H264-GRP", ReasonNotThisEpisode, "an episode (S02E01), not the whole season"},
		{"Severance.S02E01E02.1080p.WEB.H264-GRP", ReasonNotThisEpisode, "not the whole season"},
		{"Severance.Pay.S02.720p.HDTV.x264-GRP", ReasonNotThisSeries, `"Severance Pay"`},
		{"Severance.1998.S02.720p.HDTV.x264-GRP", ReasonNotThisSeries, "not the one from 2022"},
		{"Severance.2022.03.15.1080p.WEB.h264-GRP", ReasonNotSeasonNumbered, "air date"},
		{"1080p.WEB.H264", release.ReasonUnparsed, "which series"},
	} {
		rej := MatchSeasonPack(release.Parse(tc.name), w)
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

// TestASeasonSearchSealsWhatEachResultIs: a season's search asks once for the
// season, and each result is sealed to what it is — the season for a pack, the
// episode for an episode the provider lists — or refused (ADR-0033, decision
// 3).
func TestASeasonSearchSealsWhatEachResultIs(t *testing.T) {
	var seen indexer.Query
	var mu sync.Mutex
	svc := New(fakeSource{defs("A")}, searcherFunc(func(_ context.Context,
		_ indexer.Definition, q indexer.Query) ([]indexer.Result, error) {
		mu.Lock()
		seen = q
		mu.Unlock()
		return []indexer.Result{
			result("A", "Severance.S02.1080p.ATVP.WEB-DL.DDP5.1.H.264-FLUX", 50),
			result("A", "Severance.S02E03.1080p.WEB.H264-GRP", 40),
			result("A", "Severance.S02E09.1080p.WEB.H264-GRP", 30),
			result("A", "Severance.S01.1080p.WEB.H264-GRP", 20),
			result("A", "Severance.S01-S02.1080p.WEB.H264-GRP", 10),
		}, nil
	}), nil)

	resp, err := svc.SearchSeason(context.Background(), SeasonSearch{
		Want: severanceS02(), Episodes: []int{1, 2, 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if seen.Term != "Severance" || seen.Season != 2 || seen.Episode != 0 {
		t.Errorf("asked the indexer for %+v, want Severance season 2 and no episode", seen)
	}
	mu.Unlock()

	want := map[string]*Target{
		"Severance.S02.1080p.ATVP.WEB-DL.DDP5.1.H.264-FLUX": {ItemID: 4, Season: 2, Pack: true},
		"Severance.S02E03.1080p.WEB.H264-GRP":               {ItemID: 4, Season: 2, Episode: 3},
		"Severance.S02E09.1080p.WEB.H264-GRP":               nil,
		"Severance.S01.1080p.WEB.H264-GRP":                  nil,
		"Severance.S01-S02.1080p.WEB.H264-GRP":              nil,
	}
	if len(resp.Candidates) != len(want) {
		t.Fatalf("%d candidates, want %d: refusals come back too", len(resp.Candidates), len(want))
	}
	for _, c := range resp.Candidates {
		w, ok := want[c.Title]
		switch {
		case !ok:
			t.Errorf("unexpected candidate %s", c.Title)
		case w == nil && (c.Target != nil || c.Accepted || c.Rejection == nil):
			t.Errorf("%s: target=%+v accepted=%v rejection=%+v; want refused with a reason",
				c.Title, c.Target, c.Accepted, c.Rejection)
		case w != nil && (c.Target == nil || *c.Target != *w):
			t.Errorf("%s: target=%+v, want %+v", c.Title, c.Target, *w)
		}
	}
	if resp.Candidates[0].Target == nil || resp.Candidates[1].Target == nil {
		t.Error("the matches must lead the list, however few seeders they have")
	}
}

// A season target is valid only in its own shape, and is sealed as one.
func TestASeasonTargetHasOneShape(t *testing.T) {
	for _, tc := range []struct {
		t    Target
		ok   bool
		code string
	}{
		{Target{ItemID: 4, Season: 2, Pack: true}, true, "S02"},
		{Target{ItemID: 4, Season: 0, Pack: true}, true, "S00"},
		{Target{ItemID: 4, Season: 2, Episode: 3, Pack: true}, false, ""},
		{Target{ItemID: 4, Pack: true, Film: true}, false, ""},
		{Target{ItemID: 0, Season: 2, Pack: true}, false, ""},
		{Target{ItemID: 4, Season: -1, Pack: true}, false, ""},
	} {
		if got := tc.t.Valid(); got != tc.ok {
			t.Errorf("%+v valid=%v, want %v", tc.t, got, tc.ok)
		}
		if tc.ok && tc.t.Code() != tc.code {
			t.Errorf("%+v code=%q, want %q", tc.t, tc.t.Code(), tc.code)
		}
	}

	tk := NewTickets(testCipher(t), time.Hour, nil)
	c := testCandidate()
	c.Target = &Target{ItemID: 4, Season: 2, Pack: true}
	tok, err := tk.Seal(c, 42)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tk.Open(tok, 42)
	if err != nil {
		t.Fatal(err)
	}
	if got.Target == nil || *got.Target != *c.Target {
		t.Errorf("sealed %+v, opened %+v", *c.Target, got.Target)
	}
}

// TestAPersonsSeasonSearchTakesSeveralSeasons pins ADR-0057, decisions 1 and
// 2: a pack of several seasons, the searched one among them, sealed to its
// span — and only when the search says how far the series reaches.
func TestAPersonsSeasonSearchTakesSeveralSeasons(t *testing.T) {
	w := severanceS02()
	for _, tc := range []struct {
		name       string
		lastListed int
		want       *Target
		reason     string
	}{
		{"Severance.S01-S03.1080p.WEB.H264-GRP", 4, &Target{ItemID: 4, Season: 1, LastSeason: 3, Pack: true}, ""},
		{"Severance.Seasons.1-3.1080p.WEB.H264-GRP", 4, &Target{ItemID: 4, Season: 1, LastSeason: 3, Pack: true}, ""},
		{"Severance.The.Complete.Series.1080p.WEB.H264-GRP", 4, &Target{ItemID: 4, Season: 1, LastSeason: 4, Pack: true}, ""},
		{"Severance.2022.Complete.Series.1080p.WEB.H264-GRP", 2, &Target{ItemID: 4, Season: 1, LastSeason: 2, Pack: true}, ""},
		{"Severance.S00-S02.1080p.WEB.H264-GRP", 4, &Target{ItemID: 4, Season: 1, LastSeason: 2, Pack: true}, ""},
		{"Severance.Complete.Series.1080p.WEB.H264-GRP", 1, nil, ReasonNotThisEpisode},
		{"Severance.S03-S04.1080p.WEB.H264-GRP", 4, nil, ReasonNotThisEpisode},
		{"Severance.Pay.S01-S03.1080p.WEB.H264-GRP", 4, nil, ReasonNotThisSeries},
		{"Severance.S01-S03.1080p.WEB.H264-GRP", 0, nil, ReasonSeveralSeasons},
	} {
		got, rej := judgeForSeason(release.Parse(tc.name), w, map[int]bool{1: true}, tc.lastListed)
		switch {
		case tc.want != nil && (got == nil || *got != *tc.want):
			t.Errorf("%s: %+v (%+v), want %+v", tc.name, got, rej, *tc.want)
		case tc.want == nil && (got != nil || rej == nil || rej.Reason != tc.reason):
			t.Errorf("%s: %+v (%+v), want refused as %s", tc.name, got, rej, tc.reason)
		}
	}
	// The sealed span is a valid target with its own code; nothing else
	// carries a last season.
	tg := Target{ItemID: 4, Season: 1, LastSeason: 3, Pack: true}
	if !tg.Valid() || tg.Code() != "S01-S03" {
		t.Errorf("%+v: valid=%v code=%q", tg, tg.Valid(), tg.Code())
	}
	for _, bad := range []Target{
		{ItemID: 4, Season: 3, LastSeason: 3, Pack: true},
		{ItemID: 4, Season: 3, LastSeason: 2, Pack: true},
		{ItemID: 4, Season: 1, Episode: 2, LastSeason: 3},
		{ItemID: 4, Film: true, LastSeason: 3},
	} {
		if bad.Valid() {
			t.Errorf("%+v is valid", bad)
		}
	}
}

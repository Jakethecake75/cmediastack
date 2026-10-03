package search

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/indexer"
)

func dummy1994() AlbumWant {
	return AlbumWant{ItemID: 3, AlbumID: 30, Artists: []string{"Portishead"}, Title: "Dummy", Year: 1994}
}

// ADR-0046, decisions 2 and 3.
func TestAReleaseIsJudgedAgainstTheAlbum(t *testing.T) {
	for name, want := range map[string]string{
		"Portishead - Dummy (1994) [FLAC]":               "",
		"Portishead-Dummy-CD-FLAC-1994-GRP":              "",
		"Portishead.Dummy.1994.MP3":                      "",
		"Portishead - Dummy":                             "",
		"PORTISHEAD - DUMMY [2014 Remaster] (1994) 320":  "",
		"Portishead - Dummy (Deluxe Edition) [WEB FLAC]": "",
		"Portishead - Dummy Live (1994) [FLAC]":          ReasonNotThisAlbum,
		"Portishead - Third (2008) [FLAC]":               ReasonNotThisAlbum,
		"Portishead - Discography (1991-2008) [FLAC]":    ReasonNotThisAlbum,
		"Massive Attack - Dummy (1994) [FLAC]":           ReasonNotThisAlbum,
		"Portishead - Dummy (2008) [FLAC]":               ReasonNotThisAlbum,
		"Dummy (1994) [FLAC]":                            ReasonNotThisAlbum,
		"---":                                            "unparsed",
	} {
		rej := MatchAlbum(name, dummy1994())
		switch {
		case want == "" && rej != nil:
			t.Errorf("%s: refused as %s (%s)", name, rej.Reason, rej.Detail)
		case want != "" && (rej == nil || rej.Reason != want):
			t.Errorf("%s: %+v, want %s", name, rej, want)
		}
	}

	// A namesake needs a year, and the right one.
	weezer := AlbumWant{ItemID: 5, AlbumID: 50, Artists: []string{"Weezer"}, Title: "Weezer",
		Year: 2001, Namesake: true}
	for name, ok := range map[string]bool{
		"Weezer - Weezer (2001) [FLAC]": true,
		"Weezer - Weezer [FLAC]":        false,
		"Weezer - Weezer (1994) [FLAC]": false,
	} {
		if got := MatchAlbum(name, weezer) == nil; got != ok {
			t.Errorf("%s: matched=%v, want %v", name, got, ok)
		}
	}
	weezer.Year = 0
	if MatchAlbum("Weezer - Weezer (2001) [FLAC]", weezer) == nil {
		t.Error("a namesake whose own year is unknown was matched by a dated release")
	}
}

// ADR-0046, decision 4.
func TestAReleaseNameSaysItsAudioQuality(t *testing.T) {
	for name, want := range map[string]string{
		"Portishead - Dummy (1994) [FLAC 24-96]":  "FLAC 24-bit",
		"Portishead - Dummy [24bit Hi-Res FLAC]":  "FLAC 24-bit",
		"Portishead-Dummy-CD-FLAC-1994-GRP":       "FLAC",
		"Portishead - Dummy (ALAC)":               "FLAC",
		"Portishead - Dummy (1994) [MP3 320kbps]": "MP3-320",
		"Portishead - Dummy (1994) 320":           "MP3-320",
		"Portishead - Dummy (1994) [MP3 V0]":      "MP3-V0",
		"Portishead - Dummy (1994) [MP3]":         "MP3",
		"Portishead - Dummy (1994) [AAC]":         "AAC",
		"Portishead - Dummy (1994)":               "Unknown",
		"Portishead - Dummy (1994) [24bit]":       "Unknown",
	} {
		if got, _ := AudioQuality(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	ranks := []string{"x FLAC 24bit", "x FLAC", "x 320", "x V0", "x MP3", "x"}
	for i := 1; i < len(ranks); i++ {
		_, hi := AudioQuality(ranks[i-1])
		_, lo := AudioQuality(ranks[i])
		if hi <= lo {
			t.Errorf("%s (%d) does not outrank %s (%d)", ranks[i-1], hi, ranks[i], lo)
		}
	}
}

// ADR-0046, decisions 1, 4 and 5.
func TestAnAlbumSearchMarksTheMatchesBestFirst(t *testing.T) {
	var seen indexer.Query
	var mu sync.Mutex
	svc := New(fakeSource{defs("A")}, searcherFunc(func(_ context.Context,
		_ indexer.Definition, q indexer.Query) ([]indexer.Result, error) {
		mu.Lock()
		seen = q
		mu.Unlock()
		return []indexer.Result{
			result("A", "Portishead - Dummy (1994) [MP3 320]", 900),
			result("A", "Portishead - Dummy (1994)", 800),
			result("A", "Portishead - Dummy Live [FLAC]", 700),
			result("A", "Portishead - Dummy (1994) [FLAC]", 5),
		}, nil
	}), nil)
	resp, err := svc.SearchAlbum(context.Background(), AlbumSearch{Want: dummy1994()})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if seen.Term != "portishead dummy" || seen.Season != -1 || len(seen.Categories) != 1 || seen.Categories[0] != 3000 {
		t.Errorf("asked the indexer for %+v", seen)
	}
	mu.Unlock()
	if len(resp.Candidates) != 4 {
		t.Fatalf("%d candidates", len(resp.Candidates))
	}
	want := []string{"Portishead - Dummy (1994) [FLAC]", "Portishead - Dummy (1994) [MP3 320]"}
	for i, title := range want {
		c := resp.Candidates[i]
		if c.Title != title || !c.Accepted || c.Target == nil ||
			*c.Target != (Target{ItemID: 3, Album: 30}) {
			t.Errorf("candidate %d = %s accepted=%v target=%+v, want %s", i, c.Title, c.Accepted, c.Target, title)
		}
	}
	if resp.Candidates[0].Quality.Name != "FLAC" {
		t.Errorf("quality %q", resp.Candidates[0].Quality.Name)
	}
	reasons := map[string]bool{}
	for _, c := range resp.Candidates[2:] {
		if c.Accepted || c.Target != nil || c.Rejection == nil {
			t.Errorf("%s was not refused", c.Title)
			continue
		}
		reasons[c.Rejection.Reason] = true
	}
	if !reasons[ReasonUnknownFormat] || !reasons[ReasonNotThisAlbum] {
		t.Errorf("reasons %v", reasons)
	}

	// Another term changes what is asked, not what can match.
	resp, err = svc.SearchAlbum(context.Background(), AlbumSearch{Want: dummy1994(), Term: "portishead live"})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if seen.Term != "portishead live" {
		t.Errorf("term %q", seen.Term)
	}
	mu.Unlock()
	for _, c := range resp.Candidates {
		if c.Title == "Portishead - Dummy Live [FLAC]" && c.Accepted {
			t.Error("another term made another album grabbable")
		}
	}

	f := &fakeSearcher{}
	bare := New(fakeSource{defs("A")}, f, nil)
	for _, w := range []AlbumWant{
		{AlbumID: 30, Artists: []string{"Portishead"}, Title: "Dummy"},
		{ItemID: 3, Artists: []string{"Portishead"}, Title: "Dummy"},
		{ItemID: 3, AlbumID: 30, Title: "Dummy"},
		{ItemID: 3, AlbumID: 30, Artists: []string{"Portishead"}},
	} {
		if _, err := bare.SearchAlbum(context.Background(), AlbumSearch{Want: w}); err == nil {
			t.Errorf("%+v was searched for", w)
		}
	}
	if f.calls.Load() != 0 {
		t.Errorf("%d indexer calls for requests that name no album", f.calls.Load())
	}
}

// ADR-0046, decision 1: an album target is sealed, and is nothing else.
func TestATicketCarriesAnAlbum(t *testing.T) {
	for _, tc := range []struct {
		t     Target
		valid bool
	}{
		{Target{ItemID: 3, Album: 30}, true},
		{Target{ItemID: 3, Album: 30, Film: true}, false},
		{Target{ItemID: 3, Album: 30, Pack: true}, false},
		{Target{ItemID: 3, Album: 30, Season: 1}, false},
		{Target{ItemID: 3, Album: 30, Episode: 2}, false},
		{Target{ItemID: 3, Album: -1, Film: true}, false},
		{Target{Album: 30}, false},
	} {
		if got := tc.t.Valid(); got != tc.valid {
			t.Errorf("%+v: Valid() = %v, want %v", tc.t, got, tc.valid)
		}
	}
	tk := NewTickets(testCipher(t), time.Hour, nil)
	c := testCandidate()
	c.Target = &Target{ItemID: 3, Album: 30}
	token, err := tk.Seal(c, 42)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tk.Open(token, 42)
	if err != nil || got.Target == nil || *got.Target != (Target{ItemID: 3, Album: 30}) {
		t.Errorf("target = %+v %v, want the album", got.Target, err)
	}
	if got.Target.Code() != "" {
		t.Errorf("an album rendered a code: %q", got.Target.Code())
	}
}

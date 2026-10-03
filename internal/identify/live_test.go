package identify

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/metadata"
)

// Identification against the real provider.
//
// The fixture tests encode what this software believes about TMDB's results.
// These check that belief against what TMDB actually returns, which is a
// different claim — and the whole design here rests on one empirical fact:
// that a provider really does return several equally good answers to a question
// an operator would assume has one.
//
//	CMS_TMDB_TOKEN=eyJ... go test ./internal/identify/ -run Live -v
func liveProvider(t *testing.T) metadata.Provider {
	t.Helper()
	token := strings.TrimSpace(os.Getenv("CMS_TMDB_TOKEN"))
	if token == "" {
		t.Skip("set CMS_TMDB_TOKEN to run the live identification tests")
	}
	return metadata.NewTMDB(&http.Client{Timeout: 30 * time.Second},
		metadata.DefaultTMDBBase, token)
}

func liveDecide(t *testing.T, title string, year int, kind metadata.Kind) Result {
	t.Helper()
	p := liveProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	got, err := p.Search(ctx, metadata.Query{Kind: kind, Title: title, Year: year})
	if err != nil {
		t.Fatalf("live search for %q: %v", title, err)
	}
	res := Decide(Item{Title: title, Year: year, Kind: kind}, got)
	t.Logf("%q (%d): %s — %s", title, year, res.Verdict, res.Why)
	for i, s := range res.Ranked {
		if i > 4 {
			t.Logf("  … and %d more", len(res.Ranked)-i)
			break
		}
		t.Logf("  %.2f  %d %q (%d)  %s", s.Score, s.Match.ProviderID,
			s.Match.Title, s.Match.Year, strings.Join(s.Why, "; "))
	}
	return res
}

// TestLiveAmbiguityIsReal is the empirical claim the whole design rests on.
//
// If a provider only ever returned one good answer, a confidence threshold
// would be enough and the margin rule would be ceremony. It does not: two films
// called Arrival were released in 2016, and both are an exact title and exact
// year match for a library item parsed from "Arrival.2016.1080p.BluRay".
//
// If this ever stops being true — if TMDB deduplicates, or one of the two is
// removed — the test says so rather than silently becoming vacuous.
func TestLiveAmbiguityIsReal(t *testing.T) {
	res := liveDecide(t, "Arrival", 2016, metadata.KindMovie)

	exact := 0
	for _, s := range res.Ranked {
		if s.Signals.TitleExact && s.Signals.YearExact {
			exact++
		}
	}
	if exact < 2 {
		t.Fatalf("only %d candidate(s) match Arrival (2016) exactly. The margin "+
			"rule exists because a real provider returns several; if that has "+
			"changed, this test is the place to find out", exact)
	}
	if res.Verdict != VerdictPropose {
		t.Errorf("verdict = %q with %d exact candidates — a guess was made between them",
			res.Verdict, exact)
	}
	t.Logf("the live provider returned %d exact title+year matches for Arrival (2016)", exact)
}

// And the other half: something genuinely unambiguous IS accepted, so the rule
// is not merely refusing everything.
func TestLiveAnUnambiguousFilmIsAccepted(t *testing.T) {
	res := liveDecide(t, "The Matrix", 1999, metadata.KindMovie)
	if res.Verdict != VerdictAccept {
		t.Errorf("verdict = %q for The Matrix (1999): %s", res.Verdict, res.Why)
	}
	if b := res.Best(); b != nil && b.Match.ProviderID != 603 {
		t.Errorf("accepted id %d, want 603", b.Match.ProviderID)
	}
}

// A series, where the provider's field names differ and the year comes from
// first_air_date.
func TestLiveASeriesIsIdentified(t *testing.T) {
	res := liveDecide(t, "Severance", 2022, metadata.KindSeries)
	if res.Verdict == VerdictNone {
		t.Fatalf("nothing found for Severance (2022): %s", res.Why)
	}
	if b := res.Best(); b == nil || b.Match.ProviderID != 95396 {
		t.Errorf("best candidate is %v, want 95396", res.Best())
	}
}

// The commonest real failure: the release-name parser found a title and no
// year. Against a live provider this must produce candidates to CHOOSE from,
// never an automatic answer.
func TestLiveATitleWithNoYearIsAlwaysProposed(t *testing.T) {
	for _, title := range []string{"Dune", "The Thing", "Alien"} {
		res := liveDecide(t, title, 0, metadata.KindMovie)
		if res.Verdict == VerdictAccept {
			t.Errorf("%q with no year was accepted automatically as %q (%d)",
				title, res.Best().Match.Title, res.Best().Match.Year)
		}
		if len(res.Ranked) == 0 {
			t.Errorf("%q produced nothing to choose from", title)
		}
	}
}

// A title the parser mangled — which is what a real library is full of. It must
// not be accepted, and it should still surface the right answer to choose.
func TestLiveAMangledTitleIsProposedNotAccepted(t *testing.T) {
	// "the.matrix.1999.720p.brrip/matrix.mkv" is the real case PROGRESS.md
	// records the scanner producing: title "matrix", no year.
	res := liveDecide(t, "matrix", 0, metadata.KindMovie)
	if res.Verdict == VerdictAccept {
		t.Error("a title with no year was accepted")
	}
	found := false
	for _, s := range res.Ranked {
		if s.Match.ProviderID == 603 {
			found = true
		}
	}
	if !found {
		t.Errorf("The Matrix (603) was not among %d candidates for %q — the "+
			"right answer must at least be offered", len(res.Ranked), "matrix")
	}
}

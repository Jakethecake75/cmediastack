package request

import "testing"

// The normalisation rule, tested as a policy rather than as a function: each
// case below is a decision about when two people asking for something are
// asking for the SAME thing.
func TestMatchKeyCollapsesNoiseAndNothingElse(t *testing.T) {
	t.Run("noise collapses", func(t *testing.T) {
		groups := [][]struct {
			title string
			year  int
		}{
			{{"The Matrix", 1999}, {"the matrix", 1999}, {"  The   Matrix  ", 1999}},
			{{"Spider-Man", 2002}, {"Spider Man", 2002}, {"spider.man", 2002}},
			{{"Ocean's Eleven", 2001}, {"Oceans Eleven", 2001}},
			{{"WALL·E", 2008}, {"WALL-E", 2008}, {"Wall E", 2008}},
			{{"Fire & Blood", 2022}, {"Fire and Blood", 2022}},
			{{"The Matrix", 1999}, {"Matrix", 1999}},
		}
		for _, g := range groups {
			want := MatchKey(g[0].title, g[0].year)
			for _, other := range g[1:] {
				if got := MatchKey(other.title, other.year); got != want {
					t.Errorf("%q and %q should collide: %q vs %q",
						g[0].title, other.title, want, got)
				}
			}
		}
	})

	// Every case here is a title that a looser rule would wrongly merge. A false
	// merge silently attaches somebody to a request for a different film and
	// their own request is never made — the failure this rule is tuned against.
	t.Run("meaning does not collapse", func(t *testing.T) {
		pairs := [][2]struct {
			title string
			year  int
		}{
			{{"Dune", 2021}, {"Dune: Part Two", 2024}},
			{{"Dune", 1984}, {"Dune", 2021}},
			{{"Dune", 0}, {"Dune", 2021}},
			{{"Blade Runner", 1982}, {"Blade Runner 2049", 2017}},
			{{"The Thing", 1982}, {"The Thing", 2011}},
			{{"Alien", 1979}, {"Aliens", 1986}},
			{{"Halloween", 1978}, {"Halloween Ends", 2022}},
			// An article INSIDE a title is part of it.
			{{"All the President's Men", 1976}, {"All Presidents Men", 1976}},
		}
		for _, p := range pairs {
			if MatchKey(p[0].title, p[0].year) == MatchKey(p[1].title, p[1].year) {
				t.Errorf("%q (%d) and %q (%d) must NOT collide — a false merge "+
					"silently discards somebody's request",
					p[0].title, p[0].year, p[1].title, p[1].year)
			}
		}
	})

	// Accepted misses, recorded so they are decisions rather than surprises.
	// Each is a spelling nobody uses in practice, and by the rule above a missed
	// duplicate costs a human a moment while a false one silently discards
	// somebody's request.
	t.Run("accepted misses", func(t *testing.T) {
		for _, p := range [][2]string{
			{"WALL·E", "WALLE"},     // no separator at all
			{"Rocky II", "Rocky 2"}, // numerals are not resolved
			{"Se7en", "Seven"},      // leetspeak is not resolved
		} {
			if MatchKey(p[0], 2000) == MatchKey(p[1], 2000) {
				t.Logf("%q and %q now collide — that is an improvement, not a "+
					"failure; move this case up into the collapsing group", p[0], p[1])
			}
		}
	})

	// A title that normalises to nothing would collide with every other such
	// title: the worst possible false merge. The submit path refuses these
	// (TestARequestMustBeAskableFor), and this records why it has to.
	t.Run("punctuation-only titles all normalise to the same empty key", func(t *testing.T) {
		if MatchKey("!!!", 2009) != MatchKey("???", 2009) {
			t.Skip("they no longer collide")
		}
		if MatchKey("!!!", 0) != "" {
			t.Errorf("expected an empty key, got %q", MatchKey("!!!", 0))
		}
	})
}

func TestMatchKeyIsStable(t *testing.T) {
	// The stored key is compared against keys computed later, so drift between
	// two calls is a duplicate that silently stops being detected.
	for i := 0; i < 100; i++ {
		if MatchKey("The Matrix", 1999) != "matrix|1999" {
			t.Fatalf("unstable or unexpected key: %q", MatchKey("The Matrix", 1999))
		}
	}
}

package identify

import (
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/metadata"
)

func m(id int64, title string, year int, pop float64) metadata.Match {
	return metadata.Match{ProviderID: id, Kind: metadata.KindMovie,
		Title: title, Year: year, Popularity: pop}
}

func item(title string, year int) Item {
	return Item{Title: title, Year: year, Kind: metadata.KindMovie}
}

// ---------------------------------------------------------------------------
// The case this whole design exists for
// ---------------------------------------------------------------------------

// TestTwoFilmsCalledArrivalAreNeverGuessedBetween is not a contrived example.
// The live TMDB API returns BOTH of these for "Arrival" in 2016 — ids 329865
// and 472349 — and a library item parsed from "Arrival.2016.1080p.BluRay" is
// an exact title and exact year match for each.
//
// A confidence THRESHOLD accepts one of them, with even odds, forever, and
// relabels somebody's library half the time. Only a margin rule can refuse.
func TestTwoFilmsCalledArrivalAreNeverGuessedBetween(t *testing.T) {
	got := Decide(item("Arrival", 2016), []metadata.Match{
		m(329865, "Arrival", 2016, 41.2),
		m(472349, "Arrival", 2016, 1.4),
	})

	if got.Verdict != VerdictPropose {
		t.Fatalf("verdict = %q, want propose: two equally good candidates were "+
			"distinguished by something that is not evidence", got.Verdict)
	}
	// The reason has to name both, because a person is being asked to choose
	// and "ambiguous" is not a choice.
	for _, want := range []string{"329865", "472349"} {
		if !strings.Contains(got.Why, want) {
			t.Errorf("the explanation does not name candidate %s: %q", want, got.Why)
		}
	}

	// Popularity still ORDERS them — the likely one leads — it simply does not
	// decide. Those are different jobs and this is the line between them.
	if got.Best().Match.ProviderID != 329865 {
		t.Errorf("ranking put %d first; popularity should order equals",
			got.Best().Match.ProviderID)
	}
}

// And the same title with only ONE candidate is the case automatic acceptance
// exists for.
func TestAnUnambiguousExactMatchIsAccepted(t *testing.T) {
	got := Decide(item("Arrival", 2016), []metadata.Match{
		m(329865, "Arrival", 2016, 41.2),
		m(999, "Arrival of the Tall Ships", 1974, 0.1),
	})
	if got.Verdict != VerdictAccept {
		t.Fatalf("verdict = %q, want accept: %s", got.Verdict, got.Why)
	}
	if got.Best().Match.ProviderID != 329865 {
		t.Errorf("accepted %d", got.Best().Match.ProviderID)
	}
	if !strings.Contains(got.Why, "no other candidate comes close") {
		t.Errorf("the reason does not mention the margin: %q", got.Why)
	}
}

// ---------------------------------------------------------------------------
// What automatic acceptance refuses
// ---------------------------------------------------------------------------

func TestAcceptanceRequiresAnExactTitle(t *testing.T) {
	// One character apart, and different films. There is no edit-distance
	// threshold that separates this pair from a genuine typo, which is why no
	// distance is good enough to act on.
	got := Decide(item("Alien", 1986), []metadata.Match{
		m(679, "Aliens", 1986, 30.0),
	})
	if got.Verdict == VerdictAccept {
		t.Errorf("accepted %q for %q on a one-character difference",
			got.Best().Match.Title, "Alien")
	}
	if got.Verdict != VerdictPropose {
		t.Errorf("verdict = %q; a near match is still worth showing", got.Verdict)
	}
	if !strings.Contains(got.Why, "not an exact match") {
		t.Errorf("why = %q", got.Why)
	}
}

func TestAcceptanceRequiresAnExactYear(t *testing.T) {
	// A remake and its original are exactly the pair an adjacent-year rule
	// would confuse, and adjacent years are otherwise common and innocent.
	got := Decide(item("The Thing", 1982), []metadata.Match{
		m(1091, "The Thing", 1982, 20.0),
	})
	if got.Verdict != VerdictAccept {
		t.Fatalf("an exact year was not accepted: %s", got.Why)
	}

	got = Decide(item("The Thing", 2010), []metadata.Match{
		m(1091, "The Thing", 2011, 20.0),
	})
	if got.Verdict == VerdictAccept {
		t.Error("accepted a title whose year is one out; that is how a remake " +
			"becomes its original")
	}
	if !strings.Contains(got.Why, "do not match exactly") {
		t.Errorf("why = %q", got.Why)
	}
}

// An item with no year cannot be identified on its own, and that is the single
// commonest reason automatic identification will decline. It follows from the
// exact-year rule rather than being a separate one.
func TestAnItemWithNoYearIsNeverAcceptedAutomatically(t *testing.T) {
	got := Decide(item("Dune", 0), []metadata.Match{
		m(438631, "Dune", 2021, 50.0),
		m(841, "Dune", 1984, 12.0),
	})
	if got.Verdict != VerdictPropose {
		t.Fatalf("verdict = %q, want propose", got.Verdict)
	}
	if !strings.Contains(got.Why, "no year") {
		t.Errorf("the reason does not say why: %q", got.Why)
	}

	// Even with exactly one candidate. A single answer to an ambiguous question
	// is not thereby the right one — it may simply be the only one the provider
	// happened to return.
	got = Decide(item("Dune", 0), []metadata.Match{m(438631, "Dune", 2021, 50.0)})
	if got.Verdict == VerdictAccept {
		t.Error("accepted an identification for an item with no year")
	}
}

// ---------------------------------------------------------------------------
// Titles
// ---------------------------------------------------------------------------

// A release group names a file after the ORIGINAL title far more often than a
// catalogue does. Requiring the display title to match would miss most
// non-English films.
func TestTheProvidersOriginalTitleCountsEqually(t *testing.T) {
	got := Decide(item("Das Boot", 1981), []metadata.Match{
		{ProviderID: 387, Kind: metadata.KindMovie,
			Title: "The Boat", OriginalTitle: "Das Boot", Year: 1981, Popularity: 20},
	})
	if got.Verdict != VerdictAccept {
		t.Fatalf("verdict = %q, want accept: %s", got.Verdict, got.Why)
	}
	if !got.Best().Signals.TitleViaOriginal {
		t.Error("the match was not attributed to the original title")
	}
	// Said in words, because an operator seeing "The Boat" proposed for
	// "Das Boot" needs to know why it is not a mistake.
	if !strings.Contains(strings.Join(got.Best().Why, " "), "Das Boot") {
		t.Errorf("the reasoning does not mention the original title: %v", got.Best().Why)
	}
}

// The normalisation is release.NormaliseTitle, shared with the request surface
// so that a title which merges two requests also matches the library item those
// requests were about.
func TestSpellingIsNormalisedButMeaningIsNot(t *testing.T) {
	// Pure spelling: same characters, typed differently. Accepted.
	for _, title := range []string{"The Matrix", "the matrix", "The.Matrix", "THE MATRIX"} {
		got := Decide(item(title, 1999), []metadata.Match{m(603, "The Matrix", 1999, 60)})
		if got.Verdict != VerdictAccept {
			t.Errorf("%q was not accepted against The Matrix: %s", title, got.Why)
		}
	}

	// A missing leading article is NOT a spelling difference — it is a missing
	// word, and there exist distinct films that differ only by it ("Arrival"
	// and "The Arrival", both 2016, both real). So it is offered and not taken:
	// the cost is one confirmation on a title the parser mangled, which is a
	// title that deserves a human glance anyway.
	got := Decide(item("Matrix", 1999), []metadata.Match{m(603, "The Matrix", 1999, 60)})
	if got.Verdict != VerdictPropose {
		t.Errorf("%q against The Matrix = %q, want propose", "Matrix", got.Verdict)
	}
	if got.Best() == nil || got.Best().Match.ProviderID != 603 {
		t.Error("the right answer was not even offered for a mangled title")
	}
	if !got.Best().Signals.TitleArticleOnly {
		t.Error("the article-only agreement was not recorded as such")
	}

	// And meaning is not normalised away: a subtitle is part of the title.
	got = Decide(item("Dune", 2021), []metadata.Match{
		m(693134, "Dune: Part Two", 2024, 80),
	})
	if got.Verdict == VerdictAccept {
		t.Error("accepted Dune: Part Two for Dune")
	}
}

// A short title's single wrong letter matters far more than a long title's,
// and the score has to reflect that or "Alien" and "Aliens" rank as close as
// two spellings of a long name.
func TestDistanceIsRelativeToTitleLength(t *testing.T) {
	short := Score(item("Alien", 1979), m(1, "Aliens", 1979, 1))
	long := Score(item("The Lord of the Rings", 2001), m(2, "The Lord of the Ring", 2001, 1))

	if short.Score >= long.Score {
		t.Errorf("a one-letter difference in a 5-letter title scored %.2f, "+
			"no better than one in a 21-letter title at %.2f",
			short.Score, long.Score)
	}
}

// ---------------------------------------------------------------------------
// Nothing at all
// ---------------------------------------------------------------------------

func TestNothingResemblingTheItemIsNotShown(t *testing.T) {
	got := Decide(item("Arrival", 2016), []metadata.Match{
		m(1, "Completely Different Film", 1974, 90),
		m(2, "Another Unrelated Thing", 2003, 80),
	})
	if got.Verdict != VerdictNone {
		t.Errorf("verdict = %q, want none: %v", got.Verdict, got.Ranked)
	}
	if len(got.Ranked) != 0 {
		t.Errorf("showed %d candidates that resemble nothing", len(got.Ranked))
	}
}

func TestNoCandidatesAtAll(t *testing.T) {
	got := Decide(item("Arrival", 2016), nil)
	if got.Verdict != VerdictNone {
		t.Errorf("verdict = %q", got.Verdict)
	}
	if got.Best() != nil {
		t.Error("Best() returned something from an empty result")
	}
	if got.Why == "" {
		t.Error("no explanation for an empty result")
	}
}

// ---------------------------------------------------------------------------
// Every verdict is explained
// ---------------------------------------------------------------------------

// A person is being asked to confirm an identification. "0.82" does not help
// them answer "is this the same film?", so every outcome carries words.
func TestEveryVerdictExplainsItself(t *testing.T) {
	cases := []struct {
		what string
		item Item
		cand []metadata.Match
	}{
		{"accepted", item("Arrival", 2016), []metadata.Match{m(1, "Arrival", 2016, 10)}},
		{"ambiguous", item("Arrival", 2016), []metadata.Match{
			m(1, "Arrival", 2016, 10), m(2, "Arrival", 2016, 5)}},
		{"near miss", item("Alien", 1979), []metadata.Match{m(1, "Aliens", 1979, 10)}},
		{"wrong year", item("Dune", 2021), []metadata.Match{m(1, "Dune", 1984, 10)}},
		{"no year", item("Dune", 0), []metadata.Match{m(1, "Dune", 2021, 10)}},
		{"nothing", item("Arrival", 2016), nil},
	}
	for _, c := range cases {
		got := Decide(c.item, c.cand)
		if strings.TrimSpace(got.Why) == "" {
			t.Errorf("%s: no explanation", c.what)
		}
		if len(got.Why) < 20 {
			t.Errorf("%s: the explanation is too thin to act on: %q", c.what, got.Why)
		}
		for _, s := range got.Ranked {
			if len(s.Why) == 0 {
				t.Errorf("%s: candidate %d carries no reasoning", c.what, s.Match.ProviderID)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Edit distance
// ---------------------------------------------------------------------------

func TestEditDistance(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"alien", "aliens", 1},
		{"kitten", "sitting", 3},
		{"dune", "dune part two", 9},
		// Runes, not bytes: a multi-byte character is one edit, not three.
		{"café", "cafe", 1},
		{"日本語", "日本", 1},
	} {
		if got := editDistance(c.a, c.b); got != c.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// "The Arrival" (2016) is a different film from "Arrival" (2016), and the live
// provider returns both. Until leading articles were told apart from spelling,
// the wrong one scored a perfect match.
func TestALeadingArticleDoesNotMakeADifferentFilmIdentical(t *testing.T) {
	exact := Score(item("Arrival", 2016), m(329865, "Arrival", 2016, 41))
	article := Score(item("Arrival", 2016), m(401867, "The Arrival", 2016, 3))

	if !exact.Signals.TitleExact {
		t.Fatal("the identical title is not recorded as exact")
	}
	if article.Signals.TitleExact {
		t.Error(`"The Arrival" scores as an EXACT match for "Arrival"; they are ` +
			"different films and the provider returns both")
	}
	if !article.Signals.TitleArticleOnly {
		t.Error("the article-only agreement was not recorded")
	}
	if article.Score >= exact.Score {
		t.Errorf("the article-differing candidate scored %.3f, not below the exact %.3f",
			article.Score, exact.Score)
	}

	// And the margin rule still refuses, because two genuinely exact candidates
	// exist — this must not accidentally start accepting.
	got := Decide(item("Arrival", 2016), []metadata.Match{
		m(329865, "Arrival", 2016, 41),
		m(401867, "The Arrival", 2016, 3),
		m(472349, "Arrival", 2016, 1),
	})
	if got.Verdict != VerdictPropose {
		t.Errorf("verdict = %q with two exact candidates", got.Verdict)
	}
}

// The superscript in "Alien³" is not in unicode's DIGIT category, so dropping
// anything non-digit made it normalise to "alien" — colliding with a different
// film. Found against the live provider.
func TestASuperscriptIsPartOfTheTitle(t *testing.T) {
	got := Score(item("Alien", 1979), m(8077, "Alien³", 1992, 20))
	if got.Signals.TitleExact {
		t.Error(`"Alien³" scores as an exact match for "Alien"`)
	}

	// And the real Alien still does.
	if real := Score(item("Alien", 1979), m(348, "Alien", 1979, 40)); !real.Signals.TitleExact {
		t.Error("the real Alien no longer matches exactly")
	}
}

// Popularity orders candidates this software cannot separate, and never
// promotes a worse match over a better one.
//
// The live provider produced the case: a library item whose parser gave
// "matrix" with no year scores an obscure 1971 film called "Matrix" above "The
// Matrix", because "Matrix" is the more literal title match. Honest, and a
// useless thing to show somebody first.
func TestPopularityNudgesTheOrderAndNeverTheDecision(t *testing.T) {
	// A literal "Matrix" and an article-away "The Matrix", neither with a year
	// to separate them: the evidence barely differs and popularity decides the
	// order.
	// Within one title class, popularity decides — which is the ordering that
	// matters most often, because a provider usually returns several titles
	// that ARE spelled the same.
	ranked := Rank(item("Dune", 0), []metadata.Match{
		m(841, "Dune", 1984, 12.0),
		m(438631, "Dune", 2021, 90.0),
		m(627150, "Dune", 1989, 0.4),
	})
	if len(ranked) != 3 {
		t.Fatalf("%d candidates survived, want 3", len(ranked))
	}
	if ranked[0].Match.ProviderID != 438631 {
		t.Errorf("led with %d; among titles the evidence cannot separate, the "+
			"one people mean should lead", ranked[0].Match.ProviderID)
	}

	// Across a title class it does NOT, and cannot — see the comment on Rank.
	// The gap between an exact title and an article-only agreement is the same
	// size whether or not a year is present, so a nudge big enough to cross it
	// for a yearless item would also cross it for "Arrival" vs "The Arrival",
	// where both are real 2016 films. This asserts the safe half; the cost is
	// recorded as a limitation rather than tuned away.
	ranked = Rank(item("matrix", 0), []metadata.Match{
		m(411948, "Matrix", 1971, 0.6),
		m(603, "The Matrix", 1999, 95.0),
	})
	if ranked[0].Match.ProviderID != 411948 {
		t.Errorf("popularity crossed a title class: led with %d. A nudge large "+
			"enough to do that would also promote a popular \"The Arrival\" over "+
			"a quiet \"Arrival\"", ranked[0].Match.ProviderID)
	}
	if len(ranked) != 2 || ranked[1].Match.ProviderID != 603 {
		t.Error("the likely answer is not even second")
	}

	// A far better match must win however unpopular it is: the nudge is bounded
	// below the gap between an exact title and one that merely resembles it.
	ranked = Rank(item("Arrival", 2016), []metadata.Match{
		m(1, "Arrival", 2016, 0.1),     // exact title AND year
		m(2, "Arrowhead", 2016, 999.0), // barely resembles it, wildly popular
	})
	if ranked[0].Match.ProviderID != 1 {
		t.Errorf("popularity promoted %q over an exact match", ranked[0].Match.Title)
	}

	// The nudge never reaches Scored.Score, which is what Decide reads.
	only := Rank(item("Arrival", 2016), []metadata.Match{m(1, "Arrival", 2016, 9999)})
	if only[0].Score > 1.0 {
		t.Errorf("popularity leaked into the evidence score: %.3f", only[0].Score)
	}

	// And none of this reaches the decision. Two exact candidates, one far more
	// popular, is still a question for a person.
	got := Decide(item("Arrival", 2016), []metadata.Match{
		m(329865, "Arrival", 2016, 99.0),
		m(472349, "Arrival", 2016, 0.1),
	})
	if got.Verdict != VerdictPropose {
		t.Errorf("verdict = %q: popularity decided an identification", got.Verdict)
	}
}

// A year that DIFFERS is evidence, not ambiguity.
//
// The live provider returns a Dune (2021) and a Dune (2020) for a library item
// called "Dune (2021)". Under a numeric margin they were too close and the item
// was proposed — asking a person to adjudicate something the software already
// knew the answer to. A review queue full of questions like that is one nobody
// works through, which makes the conservative choice the unsafe one.
func TestACloseButDistinguishableCandidateDoesNotBlockAcceptance(t *testing.T) {
	got := Decide(item("Dune", 2021), []metadata.Match{
		m(438631, "Dune", 2021, 90),
		m(697620, "Dune", 2020, 3), // one year out: distinguishable
		m(841, "Dune", 1984, 12),   // clearly different
	})
	if got.Verdict != VerdictAccept {
		t.Fatalf("verdict = %q, want accept: %s", got.Verdict, got.Why)
	}
	if got.Best().Match.ProviderID != 438631 {
		t.Errorf("accepted %d", got.Best().Match.ProviderID)
	}

	// And the genuinely indistinguishable case still stops.
	got = Decide(item("Arrival", 2016), []metadata.Match{
		m(329865, "Arrival", 2016, 41),
		m(472349, "Arrival", 2016, 1),
	})
	if got.Verdict != VerdictPropose {
		t.Errorf("two candidates with identical evidence were distinguished")
	}
}

// The rule is about the EVIDENCE, not the number. Two candidates with the same
// signals are indistinguishable however far apart popularity puts them.
func TestIndistinguishableIsAboutSignalsNotScore(t *testing.T) {
	a := Score(item("Arrival", 2016), m(1, "Arrival", 2016, 0.1))
	b := Score(item("Arrival", 2016), m(2, "Arrival", 2016, 9999))
	if !indistinguishable(a.Signals, b.Signals) {
		t.Error("two exact title+year matches were treated as distinguishable")
	}

	c := Score(item("Dune", 2021), m(3, "Dune", 2020, 1))
	if indistinguishable(a.Signals, c.Signals) {
		t.Error("an exact year and an adjacent year were treated as the same evidence")
	}
}

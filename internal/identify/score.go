// Package identify decides which provider title a library item actually is.
//
// # The asymmetry this is built around
//
// A library item's title came from a release name — "the.matrix.1999.720p.brrip"
// — parsed by software that is good and not omniscient. Attaching a provider id
// to it fixes the title, the year, the artwork and every future search. Getting
// it wrong does the same thing with the wrong film.
//
// The two failure modes are not equal, and neither is loud:
//
//   - A MISSED identification leaves an item looking exactly as it does today:
//     a parsed title, no poster. Nothing is lost, and a person can fix it.
//   - A WRONG identification RELABELS somebody's library. The file is
//     untouched, but the thing they browse to is now called something else,
//     with somebody else's poster and somebody else's synopsis. Worse, it looks
//     deliberate — it looks like the software knows.
//
// So the default is that the machine PROPOSES and a person confirms. Automatic
// acceptance exists, and is deliberately hard to earn: see Decide.
//
// # Why popularity ranks but never convinces
//
// A provider orders results by popularity, and that is the right order to SHOW
// somebody — a search for "Dune" should lead with the film most people mean.
// It is not evidence of identity. Two films called Arrival were released in
// 2016; the more popular one is not thereby the one in the operator's library.
// Popularity therefore breaks ties in the ranking and contributes nothing to
// the score that decides whether to act without asking.
package identify

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/release"
)

// Item is what this software currently believes about a library entry.
type Item struct {
	Title string
	// Year is zero when the parser could not find one, which is common and is
	// the single biggest reason an item cannot be identified automatically.
	Year int
	Kind metadata.Kind
}

// Verdict is what to do about a set of candidates.
type Verdict string

const (
	// VerdictAccept means confident enough to apply without asking. Earned
	// rarely and on purpose — see Decide.
	VerdictAccept Verdict = "accept"
	// VerdictPropose means there is a best guess worth showing a person.
	VerdictPropose Verdict = "propose"
	// VerdictNone means nothing resembles this item closely enough to show.
	VerdictNone Verdict = "none"
	// VerdictChosen means no search was scored at all: a person picked the
	// title from the provider and the item was created from it (ADR-0025).
	// Never produced by Decide — only by Store.RecordChosen.
	VerdictChosen Verdict = "chosen"
)

// Signals are the individual reasons a candidate does or does not look right.
//
// Kept as a struct rather than folded into a number, because the number is for
// ordering and these are for explaining. An operator confirming an
// identification is answering "is this the same film?", and "0.82" does not
// help them answer it.
type Signals struct {
	// TitleExact is set when the normalised titles are identical, ARTICLES
	// INCLUDED. The strong form, and the only one automatic acceptance takes.
	TitleExact bool
	// TitleArticleOnly is set when the titles agree only after a leading
	// "the"/"a"/"an" is removed from one of them.
	//
	// A real and useful agreement — the scanner produces "matrix" from
	// "the.matrix.1999.720p.brrip" and that must find "The Matrix" — and a
	// weaker one, because an article is a word rather than a spelling. The live
	// provider gave the counterexample: a library item called "Arrival" (2016)
	// matched "The Arrival" (2016), a different film, perfectly, until these
	// two were told apart.
	TitleArticleOnly bool
	// TitleViaOriginal is set when the match was against the provider's
	// ORIGINAL title rather than its display title — which is common and worth
	// saying, because release groups use the original far more often than
	// catalogues do.
	TitleViaOriginal bool
	// TitleDistance is the edit distance between normalised titles, zero when
	// exact.
	TitleDistance int
	// YearExact, YearAdjacent: a release year and a provider's release date can
	// legitimately differ by one across a new year, a festival showing or a
	// territory.
	YearExact    bool
	YearAdjacent bool
	// YearUnknown is set when the ITEM has no year. Not a disagreement — an
	// absence, and a different thing.
	YearUnknown bool
	// YearConflict is set when both years are known and differ by more than a
	// year.
	YearConflict bool
}

// Scored is one candidate with its reasoning.
type Scored struct {
	Match   metadata.Match
	Score   float64
	Signals Signals
	// Why is the reasoning in words, for a person deciding.
	Why []string
}

// Result is the outcome for one item.
type Result struct {
	Verdict Verdict
	// Ranked is every candidate worth showing, best first.
	Ranked []Scored
	// Why explains the VERDICT — in particular why something that looks like a
	// good match was not accepted automatically.
	Why string
}

// Best returns the leading candidate, or nil.
func (r Result) Best() *Scored {
	if len(r.Ranked) == 0 {
		return nil
	}
	return &r.Ranked[0]
}

// MinScoreToShow is the floor below which a candidate is not worth a person's
// attention. Deliberately low: showing a bad guess costs a glance, and hiding
// the right answer costs an identification.
const MinScoreToShow = 0.30

// AcceptMargin is how far ahead of the runner-up a candidate must be for
// popularity to be barred from reordering the two. It is a DISPLAY constant;
// the acceptance rule is indistinguishable(), below.
const AcceptMargin = 0.15

// indistinguishable reports whether two candidates are supported by exactly the
// same evidence.
//
// This is the acceptance rule, and it replaced a numeric margin because the
// margin answered the wrong question. Against a library item called
// "Dune (2021)" the live provider returns Dune (2021) at 1.00 and a Dune (2020)
// at 0.90 — closer than the margin, so the margin refused. But those two are
// not indistinguishable: their YEARS DIFFER, and a year that differs is
// evidence, not noise. Refusing there asks a person to adjudicate something the
// software already knows the answer to, and a review queue full of questions
// like that is one nobody works through.
//
// Two films called Arrival WERE released in 2016 — the live provider returns
// both — and nothing in the evidence separates them. That is the case worth
// stopping for, and it is exactly what equal signals mean.
func indistinguishable(a, b Signals) bool { return a == b }

// Score judges one candidate against one item.
func Score(item Item, m metadata.Match) Scored {
	s := Scored{Match: m}

	want := release.NormaliseTitle(item.Title)
	got := release.NormaliseTitle(m.Title)
	orig := release.NormaliseTitle(m.OriginalTitle)

	// The provider's original title counts equally. A release group names a
	// file after the original far more often than a catalogue does, so
	// requiring the display title to match would miss most non-English films.
	s.Signals.TitleDistance = editDistance(want, got)
	s.Signals.TitleExact = want == got
	if orig != "" && orig != got {
		if d := editDistance(want, orig); d < s.Signals.TitleDistance {
			s.Signals.TitleDistance = d
			s.Signals.TitleViaOriginal = true
		}
		if want == orig {
			s.Signals.TitleExact = true
			s.Signals.TitleViaOriginal = true
		}
	}

	// The weaker agreement, checked only when the strong one failed.
	if !s.Signals.TitleExact {
		stripped := release.StripLeadingArticle(want)
		s.Signals.TitleArticleOnly = stripped == release.StripLeadingArticle(got) ||
			(orig != "" && stripped == release.StripLeadingArticle(orig))
	}

	switch {
	case item.Year == 0:
		s.Signals.YearUnknown = true
	case m.Year == 0:
		s.Signals.YearUnknown = true
	case item.Year == m.Year:
		s.Signals.YearExact = true
	case abs(item.Year-m.Year) == 1:
		s.Signals.YearAdjacent = true
	default:
		s.Signals.YearConflict = true
	}

	s.Score = combine(want, s.Signals)
	s.Why = explain(item, m, s.Signals)
	return s
}

// combine turns the signals into an ordering number.
//
// The weights are not tuned against a corpus, and saying so matters: they order
// candidates for a person to look at, and the only decision taken on the number
// alone is Decide's, which additionally requires exact signals rather than a
// high score. A number that merely ranks does not need to be calibrated; one
// that decides does, and this one is not trusted to.
func combine(want string, sig Signals) float64 {
	title := 0.0
	switch {
	case sig.TitleExact:
		title = 1.0
	case sig.TitleArticleOnly:
		// Below exact and well above a near miss. High enough that "matrix"
		// still leads with "The Matrix"; low enough that "The Arrival" never
		// ties with "Arrival".
		title = 0.85
	case len(want) > 0:
		// Relative to the title's length, so one wrong letter in "Alien" costs
		// far more than one wrong letter in "The Lord of the Rings" — which is
		// right: "Alien" and "Aliens" are different films, and a long title
		// with a typo is still that title.
		ratio := float64(sig.TitleDistance) / float64(len(want))
		title = 1.0 - ratio
		if title < 0 {
			title = 0
		}
	}

	year := 0.0
	switch {
	case sig.YearExact:
		year = 1.0
	case sig.YearAdjacent:
		year = 0.6
	case sig.YearUnknown:
		// Neutral, not negative. The item simply does not say, which is not
		// evidence against any candidate — but it is why Decide refuses to act
		// on its own without one.
		year = 0.5
	case sig.YearConflict:
		year = 0.0
	}

	// Title dominates. A year agreeing means little when the titles do not.
	return 0.75*title + 0.25*year
}

func explain(item Item, m metadata.Match, sig Signals) []string {
	var why []string
	switch {
	case sig.TitleExact && sig.TitleViaOriginal:
		why = append(why, fmt.Sprintf("the original title %q matches exactly", m.OriginalTitle))
	case sig.TitleExact:
		why = append(why, "the title matches exactly")
	case sig.TitleArticleOnly:
		why = append(why, fmt.Sprintf("the titles agree apart from a leading article "+
			"(%q vs %q), which is usually the same film and occasionally is not",
			item.Title, m.Title))
	case sig.TitleDistance <= 2:
		why = append(why, fmt.Sprintf("the title is close but not identical (%q vs %q)",
			item.Title, m.Title))
	default:
		why = append(why, fmt.Sprintf("the title differs (%q vs %q)", item.Title, m.Title))
	}

	switch {
	case sig.YearExact:
		why = append(why, fmt.Sprintf("the year matches (%d)", m.Year))
	case sig.YearAdjacent:
		why = append(why, fmt.Sprintf("the year is one out (%d here, %d there) — "+
			"normal across a new year or a festival showing", item.Year, m.Year))
	case sig.YearUnknown && item.Year == 0:
		why = append(why, "this item has no year, so the year cannot support or refute it")
	case sig.YearUnknown:
		why = append(why, "the provider gives no year for this title")
	case sig.YearConflict:
		why = append(why, fmt.Sprintf("the years disagree (%d here, %d there)",
			item.Year, m.Year))
	}
	return why
}

// PopularityNudge bounds how far popularity may move a candidate in the
// DISPLAY order. Small enough that it cannot cross a real difference in
// evidence; large enough to reorder candidates the evidence does not separate.
const PopularityNudge = 0.12

// popularityWeight maps an unbounded popularity onto [0, 1).
//
// x/(x+k) rather than a cap, so the difference between 0.4 and 95 is large and
// the difference between 500 and 900 is not — which matches what the number
// means. k is the popularity at which a title gets half the nudge; 20 puts a
// mainstream film near the top and leaves obscure ones near zero.
func popularityWeight(p float64) float64 {
	if p <= 0 {
		return 0
	}
	return p / (p + 20)
}

// Rank scores every candidate and orders them for a person to look at.
//
// # Where popularity is allowed to matter, and how much
//
// Only here, and only by PopularityNudge. The nudge is added to a SORT KEY
// computed in this function; it is never written into Scored.Score, which is
// what Decide reads. So the displayed order can prefer the title people usually
// mean, and the decision to act without asking cannot.
//
// That separation is the point. Two films called Arrival came out in 2016 and
// the more popular one is not thereby the one in the operator's library —
// popularity is not evidence of identity. But it is excellent evidence of what
// somebody is likely to be looking for, which is a different question and the
// one a list of candidates is answering.
//
// The live provider is why this exists at all. A library item whose parser
// produced "matrix" with no year scores an obscure 1971 film called "Matrix"
// above "The Matrix", because "Matrix" is the more literal title match. That is
// honest, and it is a useless thing to put in front of somebody.
//
// # The limit of what a nudge can do, worked out rather than tuned
//
// There are two gaps of interest, and they are the same size:
//
//   - The gap this would LIKE to cross: with no year on either side, an exact
//     title scores 0.875 and an article-only agreement 0.7625 — 0.1125 apart.
//     Crossing it puts "The Matrix" above an obscure 1971 "Matrix" for a
//     library item called "matrix", which is what somebody wants to see.
//   - The gap it MUST NOT cross: with the year matching on both sides, an exact
//     title scores 1.0 and an article-only agreement 0.8875 — also 0.1125.
//     Crossing it puts a popular "The Arrival" above a quiet "Arrival" for an
//     item called "Arrival (2016)", and both are real 2016 films.
//
// They are identical because the year contributes the same amount to both. So
// no bound on a popularity term can take the first without taking the second:
// the design cannot have both, and a constant tuned until one example looked
// right would simply have hidden that.
//
// 0.12 is therefore set just below 0.1125's effect — popularity reorders
// candidates whose evidence is closer than an article, and stops there. The
// consequence is stated in the ADR as a limitation rather than smoothed over:
// for a title the parser stripped an article from AND lost the year of, the
// literal match leads and the likely one is second.
//
// The real fix is upstream. That case arises because the scanner reads a
// FILENAME when the folder had the year in it (PROGRESS, increment 3c), and a
// year recovered there separates these candidates completely.
func Rank(item Item, candidates []metadata.Match) []Scored {
	out := make([]Scored, 0, len(candidates))
	for _, m := range candidates {
		s := Score(item, m)
		if s.Score >= MinScoreToShow {
			out = append(out, s)
		}
	}
	key := func(s Scored) float64 {
		return s.Score + PopularityNudge*popularityWeight(s.Match.Popularity)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ki, kj := key(out[i]), key(out[j])
		if ki != kj {
			return ki > kj
		}
		return out[i].Score > out[j].Score
	})
	return out
}

// Decide produces the verdict.
//
// # What automatic acceptance requires, and why each part
//
// All of these, together:
//
//  1. An EXACT normalised title match, ARTICLES INCLUDED. Not a close one:
//     "Alien" and "Aliens" are one character apart and are different films, and
//     no distance threshold separates that pair from a genuine typo. Not an
//     article-only agreement either — "Arrival" and "The Arrival" are both real
//     films from 2016, and the live provider returns both.
//
//  2. An EXACT year match. Not adjacent — adjacent is a real and common
//     disagreement, and it is exactly the disagreement that distinguishes a
//     remake from its original.
//
//  3. NOTHING ELSE supported by exactly the same evidence. This is the one that
//     catches the case a score threshold cannot: the live provider returns two
//     films called Arrival, both from 2016, and nothing distinguishes them.
//     Without this, one would be accepted with even odds, forever.
//
//     "Same evidence" rather than "close score", because those are different
//     questions. A Dune (2020) scores close to a Dune (2021) and is not
//     indistinguishable from it — the years differ, and a year that differs is
//     evidence. Asking a person to adjudicate that produces a review queue
//     nobody works through.
//
// An item with no year can never be accepted automatically, and that follows
// from (2) rather than being a separate rule: without a year, a title is a
// question, not an answer.
func Decide(item Item, candidates []metadata.Match) Result {
	ranked := Rank(item, candidates)
	res := Result{Ranked: ranked}

	if len(ranked) == 0 {
		res.Verdict = VerdictNone
		res.Why = fmt.Sprintf("nothing the provider returned resembles %q closely enough to show",
			item.Title)
		return res
	}

	best := ranked[0]
	res.Verdict = VerdictPropose

	switch {
	case !best.Signals.TitleExact:
		res.Why = "the best candidate's title is not an exact match, so this needs a person: " +
			strings.Join(best.Why, "; ")
		return res

	case item.Year == 0:
		res.Why = fmt.Sprintf("%q has no year, and a title without one cannot be "+
			"identified on its own — %d candidate(s) match the title",
			item.Title, countExactTitles(ranked))
		return res

	case !best.Signals.YearExact:
		res.Why = "the years do not match exactly: " + strings.Join(best.Why, "; ")
		return res
	}

	// Title and year both exact. The remaining question is whether anything
	// else is supported by exactly the same evidence.
	var rivals []Scored
	for _, s := range ranked[1:] {
		if indistinguishable(best.Signals, s.Signals) {
			rivals = append(rivals, s)
		}
	}
	if len(rivals) > 0 {
		res.Why = fmt.Sprintf("%d candidates match %q (%d) equally well — "+
			"%s and %s — so a person has to choose",
			len(rivals)+1, item.Title, item.Year,
			describe(best.Match), describe(rivals[0].Match))
		return res
	}

	res.Verdict = VerdictAccept
	res.Why = fmt.Sprintf("%s, and no other candidate comes close",
		strings.Join(best.Why, ", "))
	return res
}

func describe(m metadata.Match) string {
	if m.Year > 0 {
		return fmt.Sprintf("%q (%d, id %d)", m.Title, m.Year, m.ProviderID)
	}
	return fmt.Sprintf("%q (id %d)", m.Title, m.ProviderID)
}

func countExactTitles(ranked []Scored) int {
	n := 0
	for _, s := range ranked {
		if s.Signals.TitleExact {
			n++
		}
	}
	return n
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// editDistance is Levenshtein, two rows rather than a full matrix.
//
// Written out rather than taken as a dependency: it is twenty lines, the
// alternative is a module in the supply chain of a media server for one
// function, and §12's "do not reinvent solved problems" is about BitTorrent
// protocols and video transcoders, not about this.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	if len(ar) == 0 {
		return len(br)
	}
	if len(br) == 0 {
		return len(ar)
	}

	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

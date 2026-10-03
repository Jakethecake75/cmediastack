package release

import (
	"strings"
	"unicode"
)

// Comparing two titles that came from different places.
//
// A release name says "The.Matrix.1999.1080p", a person types "matrix", and a
// metadata provider says "The Matrix". Deciding whether those are the same
// title is a problem three parts of this software have, and it must be ONE
// answer: if the request surface and the identification surface normalise
// differently, a title that merges two requests will fail to match the library
// item those requests were about, and nobody will ever work out why.
//
// So the normalisation lives here, beside cleanTitle, and the POLICY built on
// top of it does not. internal/request uses it for an exact-collision rule;
// internal/identify uses it as one input to a score. Same words, different
// decisions.

// NormaliseTitle reduces a title to the form used for comparison.
//
// # Deliberately crude, and which way it errs
//
// Every rule here makes two titles more likely to be considered the same, and
// the two errors are not equal. Which one is worse depends on the caller — a
// false merge silently discards a request, a false identification relabels a
// library item — but both are worse than a miss, and a miss is always visible
// to a person. So this normalises SPELLING and refuses to normalise MEANING.
//
// It does NOT strip anything after a colon ("Dune: Part Two" is not "Dune"),
// does NOT resolve numerals ("Rocky II" is not "Rocky 2"), and does NOT touch
// the year, which callers keep separate because a title with a year and the
// same title without one are different questions.
//
// What it does:
//
//   - lowercases, and drops anything that is not a letter or a NUMBER;
//   - treats word separators — space, hyphen, underscore, dot, colon, slash,
//     plus, middle dot, en/em dash, bullet — as one space;
//   - substitutes "and" for "&", the one meaning-preserving change, because
//     "Fire & Blood" and "Fire and Blood" are the same title.
//
// It does NOT strip a leading article. That is StripLeadingArticle, kept
// separate because it is a weaker equivalence than the rest — see its comment.
//
// "Number" rather than "digit" is deliberate and was a real defect: unicode's
// digit category excludes superscripts, so a dropped "³" made "Alien³"
// normalise to "alien" and collide with "Alien" — two different films, scoring
// as an exact match. Found against the live provider.
//
// The middle dot is in the separator list because of WALL·E: that is the film's
// actual title, the hyphenated spelling is what most people type, and without
// it the two commonest spellings of one film did not collide.
func NormaliseTitle(title string) string {
	var b strings.Builder
	prevSpace := true
	space := func() {
		if !prevSpace {
			b.WriteByte(' ')
			prevSpace = true
		}
	}
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			b.WriteRune(r)
			prevSpace = false
		case r == '&':
			b.WriteString("and")
			prevSpace = false
		case isTitleSeparator(r):
			space()
		default:
			// Apostrophes, brackets and the rest simply vanish, so that
			// "Ocean's Eleven" and "Oceans Eleven" are the same title.
		}
	}

	return strings.TrimSpace(b.String())
}

// StripLeadingArticle removes a leading "the", "a" or "an" from a normalised
// title.
//
// # Why this is separate from NormaliseTitle
//
// It is a WEAKER equivalence than anything NormaliseTitle does, and conflating
// the two produced a real false match. Every other rule there is about
// spelling: "Spider-Man" and "Spider Man" are the same characters typed
// differently. An article is not a spelling difference — it is a word, and
// there exist distinct titles that differ only by it.
//
// The live provider supplied the example. A library item called "Arrival"
// (2016) scored a perfect exact match against "The Arrival" (2016), a different
// film, because both sides had been stripped. Meanwhile the stripping genuinely
// helps in the commoner direction: the scanner produces "matrix" from
// "the.matrix.1999.720p.brrip/matrix.mkv", and that must still find
// "The Matrix".
//
// So both comparisons exist, and the caller decides what each is worth.
// internal/identify treats an article-only agreement as good enough to PROPOSE
// and not good enough to ACCEPT. internal/request, whose question is whether
// two people asked for the same thing, treats it as a match — a person typing
// "Matrix" and a person typing "The Matrix" want the same film.
//
// Only leading, and only these three: an article in the middle of a title is
// part of the title.
func StripLeadingArticle(normalised string) string {
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(normalised, article) {
			return normalised[len(article):]
		}
	}
	return normalised
}

// isTitleSeparator reports whether a rune stands between words rather than
// inside one.
//
// Explicit rather than unicode.IsPunct, because the apostrophe is punctuation
// and must VANISH ("Ocean's" -> "oceans") while the hyphen must SEPARATE
// ("Spider-Man" -> "spider man"). A category test cannot tell those apart, and
// getting it backwards merges nothing and splits everything.
func isTitleSeparator(r rune) bool {
	switch r {
	case '-', '_', '.', ':', '/', '+',
		'·', // MIDDLE DOT, as in WALL·E
		'–', // EN DASH
		'—', // EM DASH
		'•': // BULLET
		return true
	}
	return unicode.IsSpace(r)
}

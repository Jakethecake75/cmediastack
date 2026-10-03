// Package request is the acquisition request surface: what people have asked
// this instance to fetch, and what came of it.
package request

import (
	"strconv"

	"github.com/jakethecake75/cmediastack/internal/release"
)

// MatchKey normalises a title and year into the form used for duplicate
// detection, and for nothing else.
//
// # Why this is deliberately crude
//
// With no metadata provider there is no id to compare, so "is this the same
// film?" is answered by comparing words. Every rule below makes the comparison
// looser, and every loosening trades a missed duplicate for a false one. The
// two errors are not equal:
//
//   - A MISSED duplicate produces two rows in a queue a human reads. They see
//     both, and merge or deny one. Cost: a moment of annoyance.
//   - A FALSE duplicate silently attaches somebody to a request for a
//     different film, and their actual request is never made. Cost: the
//     software quietly did not do what they asked, and nothing says so.
//
// So this stays conservative. It normalises spelling and punctuation, which are
// noise, and refuses to normalise meaning. It does NOT strip subtitles after a
// colon ("Dune: Part Two" is not "Dune"), does NOT resolve numerals ("Rocky II"
// is not "Rocky 2" here, and that is an accepted miss), and treats a title with
// a year as distinct from the same title without one — because "Dune" and
// "Dune (2021)" may well be different films, and guessing which is exactly the
// silent failure above.
func MatchKey(title string, year int) string {
	// Article-insensitive, deliberately: somebody typing "Matrix" and somebody
	// typing "The Matrix" are asking for the same film, and this surface's
	// question is exactly "did two people ask for the same thing?".
	// internal/identify makes the opposite call on the same pair of functions,
	// because its question is "is this library item that title?" — where
	// "Arrival" and "The Arrival" are different films.
	key := release.StripLeadingArticle(release.NormaliseTitle(title))

	// The year is part of the key, not an optional refinement. See the doc
	// comment: a request with no year is a DIFFERENT request from one with a
	// year, because nobody can tell which film the yearless one meant.
	if year > 0 {
		return key + "|" + strconv.Itoa(year)
	}
	return key
}

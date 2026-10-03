package release

import (
	"regexp"
	"strconv"
	"strings"
)

// Several seasons in one release (ADR-0033, decision 2).
//
// Parse reads the first season marker and stops there, which is right for
// every release naming one season and wrong for one naming several:
// "Show.S01-S03" parses as a pack of season 1. The import files one season per
// download, so a multi-season pack taken for season 1 would download all three
// and import one. This is asked separately rather than folded into Parsed
// because nothing else needs it, and a new field would ripple through every
// place a Parsed is compared.
var (
	// S01-S03, S01-03, S01 - S03.
	reSeasonSpan = regexp.MustCompile(`(?:^|[\s._\-\[\]()+])s(\d{1,3})[\s._]*-[\s._]*s?(\d{1,3})(?:$|[\s._\-\[\]()+])`)
	// Seasons 1-3, Season.1-3.
	reSeasonWordSpan = regexp.MustCompile(`(?:^|[\s._\-\[\]()+])seasons?[\s._]*(\d{1,3})[\s._]*-[\s._]*(\d{1,3})(?:$|[\s._\-\[\]()+])`)
	// A bare season marker, S02, standing alone (not S02E03).
	reBareSeason = regexp.MustCompile(`(?:^|[\s._\-\[\]()+])s(\d{1,3})(?:$|[\s._\-\[\]()+])`)
	// Complete Series, The.Complete.Series.
	reCompleteSeries = regexp.MustCompile(`(?:^|[\s._\-\[\]()+])complete[\s._]*series(?:$|[\s._\-\[\]()+])`)
)

// NamesSeveralSeasons reports whether a release name covers more than one
// season: a range (S01-S03, Seasons 1-3), two different season markers
// (S01.S02), or a complete series.
func NamesSeveralSeasons(name string) bool {
	_, _, several := SeasonSpan(name)
	return several
}

// SeasonSpan is the seasons a release naming several covers (ADR-0057): the
// lowest and highest it names, and several when it names more than one. A
// complete series naming no numbers spans 1 to 0 — to whatever its last
// season is, which the name does not say.
func SeasonSpan(name string) (first, last int, several bool) {
	s := strings.ToLower(name)
	seen := map[int]bool{}
	for _, re := range []*regexp.Regexp{reSeasonSpan, reSeasonWordSpan} {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			seen[number(m[1])], seen[number(m[2])] = true, true
		}
	}
	// Bare markers. The pattern consumes the separator after a match, which
	// would hide an adjacent second marker ("s01.s02"), so each search
	// restarts one byte past the previous match's start.
	for i := 0; i < len(s); {
		loc := reBareSeason.FindStringSubmatchIndex(s[i:])
		if loc == nil {
			break
		}
		seen[number(s[i+loc[2]:i+loc[3]])] = true
		i += loc[0] + 1
	}
	if len(seen) > 1 {
		first, last = -1, -1
		for n := range seen {
			if first < 0 || n < first {
				first = n
			}
			if n > last {
				last = n
			}
		}
		return first, last, true
	}
	if reCompleteSeries.MatchString(s) {
		return 1, 0, true
	}
	return 0, 0, false
}

func number(n string) int {
	v, _ := strconv.Atoi(n)
	return v
}

package request

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Part is one piece of a series a request names (ADR-0075): a whole season
// when Episode is zero, otherwise one episode of it.
type Part struct {
	Season  int `json:"season"`
	Episode int `json:"episode,omitempty"`
}

// Bounds on what a scope may name. A season past 100 or an episode past 2000 is
// a typo or an attack, and a scope is shown to other people.
const (
	maxScopeParts = 200
	maxSeason     = 100
	maxEpisode    = 2000
)

// FormatScope checks parts and writes them in their one canonical form: sorted,
// without repeats, and without an episode its whole season already covers. The
// form is part of the match key, so the same choice made twice must read the
// same.
func FormatScope(parts []Part) (string, error) {
	if len(parts) > maxScopeParts {
		return "", fmt.Errorf("%w: a request may name at most %d seasons and episodes", ErrInvalid, maxScopeParts)
	}
	whole := map[int]bool{}
	for _, p := range parts {
		if p.Season < 0 || p.Season > maxSeason || p.Episode < 0 || p.Episode > maxEpisode {
			return "", fmt.Errorf("%w: season %d episode %d is not a part of a series", ErrInvalid, p.Season, p.Episode)
		}
		if p.Episode == 0 {
			whole[p.Season] = true
		}
	}
	kept := make([]Part, 0, len(parts))
	for _, p := range parts {
		if p.Episode != 0 && whole[p.Season] {
			continue
		}
		kept = append(kept, p)
	}
	slices.SortFunc(kept, func(a, b Part) int {
		return cmp.Or(cmp.Compare(a.Season, b.Season), cmp.Compare(a.Episode, b.Episode))
	})
	kept = slices.Compact(kept)
	words := make([]string, len(kept))
	for i, p := range kept {
		words[i] = "S" + strconv.Itoa(p.Season)
		if p.Episode != 0 {
			words[i] += "E" + strconv.Itoa(p.Episode)
		}
	}
	return strings.Join(words, ","), nil
}

// ParseScope reads a canonical scope back. Empty is the whole title.
func ParseScope(s string) ([]Part, error) {
	if s == "" {
		return nil, nil
	}
	words := strings.Split(s, ",")
	out := make([]Part, 0, len(words))
	for _, w := range words {
		season, episode, _ := strings.Cut(strings.TrimPrefix(w, "S"), "E")
		var p Part
		var err error
		if p.Season, err = strconv.Atoi(season); err != nil || !strings.HasPrefix(w, "S") {
			return nil, fmt.Errorf("request: %q is not a season", w)
		}
		if episode != "" {
			if p.Episode, err = strconv.Atoi(episode); err != nil {
				return nil, fmt.Errorf("request: %q is not an episode", w)
			}
		}
		out = append(out, p)
	}
	return out, nil
}

package search

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/release"
)

// Searching for one season of one series (ADR-0033).
//
// A season's search asks the indexers once for the season and judges every
// result against it: a pack of exactly that season is sealed to the season,
// a single episode of it is sealed to that episode as the episode search would
// have, and everything else is refused with its reason.

// ReasonSeveralSeasons refuses a release covering more than one season. The
// import files one season per download.
const ReasonSeveralSeasons = "several_seasons"

// SeasonWant is one season a person or automatic acquisition is looking for.
type SeasonWant struct {
	ItemID int64
	// Titles are every name the series goes by, its own first (EpisodeWant).
	Titles []string
	// Year is the series' first-air year, zero when unknown.
	Year   int
	Season int
}

// MatchSeasonPack says whether a parsed release is a pack of the wanted
// season, and why not when it is not. A nil rejection is a match.
//
// A pack names the series, one season, and no episode. A release naming
// several seasons is refused even when the wanted one is among them, whatever
// series it is, and a
// release naming an episode is an episode, however many it spans.
func MatchSeasonPack(p release.Parsed, w SeasonWant) *release.Rejection {
	// Asked first: "Show.Complete.Series" parses with "Complete Series" in its
	// title, and would otherwise be refused as another show, which reads as
	// though the right one might be grabbable.
	if release.NamesSeveralSeasons(p.Raw) {
		return &release.Rejection{Reason: ReasonSeveralSeasons,
			Detail: "several seasons in one release; a download is filed as one season, " +
				"so this would download them all and import one"}
	}
	if rej := matchSeries(p, w.Titles, w.Year); rej != nil {
		return rej
	}
	if p.Season < 0 || len(p.AbsoluteEpisodes) > 0 || p.AirDate != "" {
		return notSeasonNumbered(p)
	}
	if p.Season != w.Season {
		return &release.Rejection{Reason: ReasonNotThisEpisode,
			Detail: fmt.Sprintf("season %d, not season %d", p.Season, w.Season)}
	}
	if len(p.Episodes) > 0 {
		first, last := p.Episodes[0], p.Episodes[len(p.Episodes)-1]
		return &release.Rejection{Reason: ReasonNotThisEpisode,
			Detail: fmt.Sprintf("an episode (%s), not the whole season", codeOf(p.Season, first, last))}
	}
	return nil
}

// SeasonSearch is a search for one season.
type SeasonSearch struct {
	Want SeasonWant
	// Episodes are the season's episode numbers the provider lists. A single
	// episode among the results is sealed to its episode only when it is one
	// of these.
	Episodes []int
	// Term replaces what the indexers are asked for (EpisodeSearch).
	Term string
	// LastSeason is the highest regular season the provider lists. Set only
	// by a person's search: a pack of several seasons including the one
	// searched for is matched, a complete series spanning to it (ADR-0057).
	// Zero — automatic acquisition — refuses every such pack.
	LastSeason int
	Profile    *release.Profile
	IndexerIDs []int64
}

// SearchSeason searches for one season and judges every candidate against it.
func (s *Service) SearchSeason(ctx context.Context, ss SeasonSearch) (Response, error) {
	w := ss.Want
	if w.ItemID <= 0 || w.Season < 0 || len(w.Titles) == 0 {
		return Response{}, fmt.Errorf("search: %+v does not name a season", w)
	}
	term := strings.TrimSpace(ss.Term)
	if term == "" {
		term = w.Titles[0]
	}
	listed := make(map[int]bool, len(ss.Episodes))
	for _, e := range ss.Episodes {
		listed[e] = true
	}

	resp, err := s.Search(ctx, Request{
		Term: term, Season: w.Season, Episode: 0,
		Profile: ss.Profile, IndexerIDs: ss.IndexerIDs,
	})
	if err != nil {
		return resp, err
	}

	for i := range resp.Candidates {
		c := &resp.Candidates[i]
		target, rej := judgeForSeason(c.Parsed, w, listed, ss.LastSeason)
		if rej != nil {
			c.Accepted, c.Rejection, c.Target = false, rej, nil
			continue
		}
		c.Target = target
	}
	sortCandidates(resp.Candidates, ss.Profile)
	return resp, nil
}

// judgeForSeason is what a season's search makes of one release: the season,
// one of its listed episodes, or a refusal. A release shaped like an episode is
// answered as an episode, so its refusal says which episode it is rather than
// that it is not a pack.
func judgeForSeason(p release.Parsed, w SeasonWant, listed map[int]bool, lastListed int) (*Target, *release.Rejection) {
	if len(p.Episodes) == 0 {
		if first, last, several := release.SeasonSpan(p.Raw); several && lastListed > 0 {
			return matchSeveralSeasons(p, w, first, last, lastListed)
		}
		if rej := MatchSeasonPack(p, w); rej != nil {
			return nil, rej
		}
		return &Target{ItemID: w.ItemID, Season: w.Season, Pack: true}, nil
	}
	first := p.Episodes[0]
	if rej := MatchEpisode(p, EpisodeWant{ItemID: w.ItemID, Titles: w.Titles, Year: w.Year,
		Season: w.Season, Episode: first}); rej != nil {
		return nil, rej
	}
	if !listed[first] {
		return nil, &release.Rejection{Reason: ReasonNotThisEpisode,
			Detail: fmt.Sprintf("%s is not an episode the provider lists for season %d",
				codeOf(w.Season, first, first), w.Season)}
	}
	return &Target{ItemID: w.ItemID, Season: w.Season, Episode: first}, nil
}

// reSeveralSeasonsWords is what a pack of several seasons may add to the
// series' title: "The Wire The Complete Series", "The Wire Seasons 1-3".
var reSeveralSeasonsWords = regexp.MustCompile(`(?i)(?:[\s._]+the)?[\s._]+(?:complete[\s._]+series|seasons?[\s._]*\d{1,3}[\s._]*-[\s._]*\d{1,3}).*$`)

// matchSeveralSeasons matches a pack of several seasons to the season a person
// searched for (ADR-0057): the series, and that season among the regular ones
// it spans. A complete series naming no numbers spans to the last the
// provider lists. A span that comes to one season is that season's pack.
func matchSeveralSeasons(p release.Parsed, w SeasonWant, first, last, lastListed int) (*Target, *release.Rejection) {
	p.Title = reSeveralSeasonsWords.ReplaceAllString(p.Title, "")
	if rej := matchSeries(p, w.Titles, w.Year); rej != nil {
		return nil, rej
	}
	if len(p.AbsoluteEpisodes) > 0 || p.AirDate != "" {
		return nil, notSeasonNumbered(p)
	}
	if last == 0 {
		last = lastListed
	}
	first = max(first, 1)
	if w.Season < first || w.Season > last {
		return nil, &release.Rejection{Reason: ReasonNotThisEpisode,
			Detail: fmt.Sprintf("seasons %d to %d, not season %d", first, last, w.Season)}
	}
	t := &Target{ItemID: w.ItemID, Season: first, LastSeason: last, Pack: true}
	if last == first {
		t.LastSeason = 0
	}
	return t, nil
}

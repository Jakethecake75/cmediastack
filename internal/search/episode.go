package search

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/jakethecake75/cmediastack/internal/release"
)

// Searching for one episode of one series (ADR-0023).
//
// The general search returns whatever the indexers offered and lets a person
// judge. This one knows what it is looking for, so it can say of every
// candidate whether it IS that episode — and only one that is gets a ticket,
// with the episode sealed into it, so the grab and the import after it carry
// the server's decision rather than the client's.

// Why a candidate is not the episode.
const (
	ReasonNotThisSeries     = "not_this_series"
	ReasonNotThisEpisode    = "not_this_episode"
	ReasonSeasonPack        = "season_pack"
	ReasonNotSeasonNumbered = "not_season_numbered"
)

// yearTolerance is how far apart a release's year and the series' may be.
const yearTolerance = 1

// EpisodeWant is one episode a person asked to search for.
type EpisodeWant struct {
	ItemID int64
	// Titles are every name the series goes by: its own title first, then the
	// provider's alternative titles. Release names follow scene convention —
	// The Office is released as "The.Office.US" — so the title alone would
	// refuse the right show.
	Titles []string
	// Year is the series' first-air year, zero when unknown.
	Year    int
	Season  int
	Episode int
	// AirDate is the day the episode aired, YYYY-MM-DD, empty when unknown. A
	// release naming that date and no season is this episode (ADR-0064).
	AirDate string
}

// NormalizeTitle folds a title to the form two spellings of it share.
//
// Case, accents, punctuation and "&" are folded; apostrophes are removed rather
// than turned into spaces, so "Marvel's Daredevil" and the scene's
// "Marvels.Daredevil" agree. Nothing is dropped or reordered beyond that: a
// comparison generous enough to call "Severance Pay" the same show as
// "Severance" is one that grabs the wrong one.
func NormalizeTitle(s string) string {
	// Accents off: "Pokémon" is released as "Pokemon". NFD splits a letter
	// from its combining mark, and the marks (category Mn) are removed.
	folded, _, err := transform.String(
		transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), s)
	if err == nil {
		s = folded
	}
	s = strings.ToLower(s)

	var b strings.Builder
	b.Grow(len(s) + 8)
	space := true // suppresses a leading space
	for _, r := range s {
		switch {
		case r == '&':
			if !space {
				b.WriteByte(' ')
			}
			b.WriteString("and ")
			space = true
		case r == '\'' || r == '’' || r == '‘' || r == '`':
			// Removed, not spaced: "marvel's" is "marvels".
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		default:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// MatchEpisode says whether a parsed release is the wanted episode, and why not
// when it is not. A nil rejection is a match.
//
// The checks run in the order an operator would ask them, and the first that
// fails is the answer: a release of a different show is "a different series"
// even when its episode number happens to be wrong as well.
func MatchEpisode(p release.Parsed, w EpisodeWant) *release.Rejection {
	if p.AirDate != "" && strings.HasPrefix(p.AirDate, strconv.Itoa(p.Year)+"-") {
		// The year a dated release names is its air date's, not the year the
		// series began (ADR-0064): The.Daily.Show.2026.10.02 is not a 2026 show.
		p.Year = 0
	}
	if rej := matchSeries(p, w.Titles, w.Year); rej != nil {
		return rej
	}
	if p.Season < 0 && p.AirDate != "" && w.AirDate != "" {
		if p.AirDate == w.AirDate {
			return nil
		}
		return &release.Rejection{Reason: ReasonNotThisEpisode,
			Detail: fmt.Sprintf("the episode of %s, not the one of %s", p.AirDate, w.AirDate)}
	}
	if p.Season < 0 {
		return notSeasonNumbered(p)
	}
	if p.Season != w.Season {
		return &release.Rejection{Reason: ReasonNotThisEpisode,
			Detail: fmt.Sprintf("season %d, not season %d", p.Season, w.Season)}
	}
	if p.FullSeason || len(p.Episodes) == 0 {
		return &release.Rejection{Reason: ReasonSeasonPack,
			Detail: "a whole-season pack, not this episode alone; the season's own " +
				"search is where a pack can be grabbed"}
	}
	first, last := p.Episodes[0], p.Episodes[len(p.Episodes)-1]
	if w.Episode < first || w.Episode > last {
		return &release.Rejection{Reason: ReasonNotThisEpisode,
			Detail: fmt.Sprintf("%s, not %s", codeOf(p.Season, first, last),
				codeOf(w.Season, w.Episode, w.Episode))}
	}
	return nil
}

// matchSeries says whether a release names the series — by any of its titles,
// and in a year within one of its own — and why not when it does not. The
// first question of every television match, episode or season.
func matchSeries(p release.Parsed, titles []string, year int) *release.Rejection {
	title := NormalizeTitle(p.Title)
	if title == "" {
		return &release.Rejection{Reason: release.ReasonUnparsed,
			Detail: "the release name does not say which series it is"}
	}

	known := false
	for _, t := range titles {
		if n := NormalizeTitle(t); n != "" && n == title {
			known = true
			break
		}
	}
	if !known {
		return &release.Rejection{Reason: ReasonNotThisSeries,
			Detail: fmt.Sprintf("a different series: %q", strings.TrimSpace(p.Title))}
	}
	// One year either way, not zero: Battlestar Galactica first aired in 2004
	// by the provider's count and is released as "Battlestar.Galactica.2003".
	// No check at all would take the 1963 Doctor Who for the 2005 one.
	if p.Year > 0 && year > 0 && (p.Year-year > yearTolerance || year-p.Year > yearTolerance) {
		return &release.Rejection{Reason: ReasonNotThisSeries,
			Detail: fmt.Sprintf("a different series: %s (%d), not the one from %d",
				strings.TrimSpace(p.Title), p.Year, year)}
	}
	return nil
}

// notSeasonNumbered is the refusal for a release with no season number, saying
// which of the numberings this software does not match it uses.
func notSeasonNumbered(p release.Parsed) *release.Rejection {
	detail := "names no season or episode"
	switch {
	case p.AirDate != "":
		detail = "named by air date (" + p.AirDate + "), which this software does not match"
	case len(p.AbsoluteEpisodes) > 0 || len(p.Episodes) > 0:
		detail = "numbered without a season (absolute numbering), which this software does not match"
	}
	return &release.Rejection{Reason: ReasonNotSeasonNumbered, Detail: detail}
}

func codeOf(season, first, last int) string {
	if first == last {
		return fmt.Sprintf("S%02dE%02d", season, first)
	}
	return fmt.Sprintf("S%02dE%02d-E%02d", season, first, last)
}

// EpisodeSearch is a search for one episode.
type EpisodeSearch struct {
	Want EpisodeWant
	// Term is what the indexers are asked for. Empty means the series' own
	// title. Whatever it is, the matching above decides what is grabbable, so
	// a term cannot widen what a ticket can be issued for.
	Term       string
	Profile    *release.Profile
	IndexerIDs []int64
	// ByDate asks the indexers for the episode by its air date: a daily
	// series (ADR-0064).
	ByDate bool
}

// SearchEpisode searches for one episode and judges every candidate against it.
//
// A candidate that is not the episode is refused with the reason, whatever the
// profile thought of it. A candidate that is carries its Target, and is still
// subject to the profile: matching the episode does not make a release
// acceptable, it makes it eligible.
func (s *Service) SearchEpisode(ctx context.Context, es EpisodeSearch) (Response, error) {
	w := es.Want
	if w.ItemID <= 0 || w.Season < 0 || w.Episode <= 0 || len(w.Titles) == 0 {
		return Response{}, fmt.Errorf("search: %+v does not name an episode", w)
	}
	term := strings.TrimSpace(es.Term)
	if term == "" {
		term = w.Titles[0]
	}

	req := Request{Term: term, Season: w.Season, Episode: w.Episode,
		Profile: es.Profile, IndexerIDs: es.IndexerIDs}
	if es.ByDate && w.AirDate != "" {
		// A daily series is released by date, and found by date (ADR-0064).
		req.AirDate = w.AirDate
	}
	resp, err := s.Search(ctx, req)
	if err != nil {
		return resp, err
	}

	for i := range resp.Candidates {
		c := &resp.Candidates[i]
		if rej := MatchEpisode(c.Parsed, w); rej != nil {
			// The episode refusal replaces the profile's verdict: "this is a
			// different show" is the answer, and "it was also the wrong
			// quality" is not worth reading after it.
			c.Accepted, c.Rejection, c.Target = false, rej, nil
			continue
		}
		c.Target = &Target{ItemID: w.ItemID, Season: w.Season, Episode: w.Episode}
	}
	sortCandidates(resp.Candidates, es.Profile)
	return resp, nil
}

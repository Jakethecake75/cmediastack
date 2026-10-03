package search

import (
	"context"
	"fmt"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/release"
)

// Searching for one film (ADR-0026).
//
// The same shape as the episode search: every candidate is judged against the
// film, and only one that IS the film carries a Target, which its ticket seals,
// so the grab and the import act on the server's decision about what the
// release is for.

// Why a candidate is not the film.
const (
	ReasonNotThisFilm = "not_this_film"
	ReasonTelevision  = "television"
	ReasonNoYear      = "no_year"
)

// FilmWant is one film a person asked to search for.
type FilmWant struct {
	ItemID int64
	// Titles are every name the film goes by: its title in the library first,
	// then the provider's title, original title and alternative titles. A
	// film not made in English is often released under its original title, and
	// the scene's spelling of an English one is often an alternative — Star
	// Wars (1977) is released as "Star.Wars.Episode.IV.A.New.Hope.1977".
	Titles []string
	// Year is the film's year. A film without one cannot be searched for: see
	// MatchFilm.
	Year int
}

// MatchFilm says whether a parsed release is the wanted film, and why not when
// it is not. A nil rejection is a match.
//
// The order is the order an operator would ask in, and the first check that
// fails is the answer (ADR-0026, decision 3).
//
// A year is REQUIRED, which the episode match does not ask. An episode has a
// season and a number that tell it from a namesake; a film has only its title
// and its year, and titles recur — TMDB lists four films titled "Dune", from
// 1984, 1989, 2020 and 2021. A release without a year cannot be told apart from
// the others, so it is refused rather than guessed at.
func MatchFilm(p release.Parsed, w FilmWant) *release.Rejection {
	title := NormalizeTitle(p.Title)
	if title == "" {
		return &release.Rejection{Reason: release.ReasonUnparsed,
			Detail: "the release name does not say which film it is"}
	}
	if p.IsEpisode() {
		return &release.Rejection{Reason: ReasonTelevision,
			Detail: "television, not a film: " + televisionOf(p)}
	}

	known := false
	for _, t := range w.Titles {
		if n := NormalizeTitle(t); n != "" && n == title {
			known = true
			break
		}
	}
	if !known {
		return &release.Rejection{Reason: ReasonNotThisFilm,
			Detail: "a different film: " + nameOf(p)}
	}

	name := "this title"
	if len(w.Titles) > 0 {
		name = strings.TrimSpace(w.Titles[0])
	}
	if w.Year <= 0 {
		// The API refuses to search for such a film at all; this is the
		// matcher refusing on its own account, so no caller can skip it.
		return &release.Rejection{Reason: ReasonNoYear,
			Detail: fmt.Sprintf("the year of %s is not known, so no release can be told "+
				"apart from another film of the same name", name)}
	}
	if p.Year <= 0 {
		return &release.Rejection{Reason: ReasonNoYear,
			Detail: fmt.Sprintf("names no year, so it cannot be told apart from another "+
				"film called %s", name)}
	}
	// One year either way: the provider's date is the film's earliest release
	// anywhere, and a release often carries a later country's year.
	if p.Year-w.Year > yearTolerance || w.Year-p.Year > yearTolerance {
		return &release.Rejection{Reason: ReasonNotThisFilm,
			Detail: fmt.Sprintf("a different film: %s, not the one from %d", nameOf(p), w.Year)}
	}
	return nil
}

// nameOf renders a release's title as the operator would say it: "Dune (1984)".
func nameOf(p release.Parsed) string {
	name := fmt.Sprintf("%q", strings.TrimSpace(p.Title))
	if p.Year > 0 {
		name = fmt.Sprintf("%s (%d)", strings.TrimSpace(p.Title), p.Year)
	}
	return name
}

// televisionOf says what sort of television a release is.
func televisionOf(p release.Parsed) string {
	switch {
	case p.Season >= 0 && len(p.Episodes) > 0:
		return codeOf(p.Season, p.Episodes[0], p.Episodes[len(p.Episodes)-1])
	case p.Season >= 0:
		return fmt.Sprintf("season %d", p.Season)
	case p.AirDate != "":
		return "an episode from " + p.AirDate
	default:
		return "an episode numbered without a season"
	}
}

// FilmTerm is what the indexers are asked for a film by default: its title,
// folded the way it is compared, and its year — "dune 2021", "amelie 2001"
// (ADR-0026, decision 4).
//
// Folded, because a release name has no accents or colons and an indexer's
// search may not fold them either. With the year, because a popular title
// returns its newer namesakes first and an indexer's page can end before the
// film appears. Neither changes what can MATCH: that is MatchFilm's, whatever
// was asked.
func FilmTerm(title string, year int) string {
	term := NormalizeTitle(title)
	if term == "" {
		term = strings.TrimSpace(title)
	}
	if year > 0 {
		term = fmt.Sprintf("%s %d", term, year)
	}
	return term
}

// FilmSearch is a search for one film.
type FilmSearch struct {
	Want FilmWant
	// Term is what the indexers are asked for. Empty means FilmTerm of the
	// film's title and year. Whatever it is, MatchFilm decides what is
	// grabbable, so a term cannot widen what a ticket can be issued for.
	Term       string
	Profile    *release.Profile
	IndexerIDs []int64
}

// SearchFilm searches for one film and judges every candidate against it.
//
// The indexers are asked a general search (t=search), not a film search by
// IMDb id: which indexers support that is declared in capabilities this
// software does not read yet (ADR-0026, decision 4).
func (s *Service) SearchFilm(ctx context.Context, fs FilmSearch) (Response, error) {
	w := fs.Want
	if w.ItemID <= 0 || len(w.Titles) == 0 || w.Year <= 0 {
		return Response{}, fmt.Errorf("search: %+v does not name a film with a year", w)
	}
	term := strings.TrimSpace(fs.Term)
	if term == "" {
		term = FilmTerm(w.Titles[0], w.Year)
	}

	resp, err := s.Search(ctx, Request{
		Term: term, Season: -1, Profile: fs.Profile, IndexerIDs: fs.IndexerIDs,
	})
	if err != nil {
		return resp, err
	}

	for i := range resp.Candidates {
		c := &resp.Candidates[i]
		if rej := MatchFilm(c.Parsed, w); rej != nil {
			// As for an episode: "this is a different film" replaces the
			// profile's verdict rather than sitting beside it.
			c.Accepted, c.Rejection, c.Target = false, rej, nil
			continue
		}
		c.Target = &Target{ItemID: w.ItemID, Film: true}
	}
	sortCandidates(resp.Candidates, fs.Profile)
	return resp, nil
}

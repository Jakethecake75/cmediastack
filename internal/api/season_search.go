package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// Searching for one season of a series (ADR-0033).

// SearchForSeason searches the indexers for one season.
//
// It never grabs. Every candidate comes back judged against the season: a
// pack of exactly that season carries a ticket sealed to the season, a single
// episode of it a ticket sealed to that episode, and everything else its
// reason. A person may grab any pack of the season, whatever they already
// have: the import never replaces a file with a worse one.
func (h *Handlers) SearchForSeason(w http.ResponseWriter, r *http.Request) {
	switch {
	case h.search == nil:
		writeProblem(w, http.StatusNotImplemented, "no search service is wired")
		return
	case h.episodes == nil:
		writeProblem(w, http.StatusNotImplemented, "episode tracking is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	season, err := strconv.Atoi(r.PathValue("season"))
	if err != nil || season < 0 {
		writeProblem(w, http.StatusNotFound, "no such season")
		return
	}
	var in targetedSearchRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	sub, err := h.episodes.ForSeasonSearch(r.Context(), id, season)
	switch {
	case errors.Is(err, library.ErrNoSuchSeason):
		writeProblem(w, http.StatusNotFound, "no such season")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	// Every name the series goes by, as for an episode's search.
	titles := []string{sub.SeriesTitle}
	var titlesNote string
	switch {
	case h.altTitles == nil || sub.TMDBID <= 0:
		titlesNote = "Only the series' own title is recognised: no metadata provider " +
			"is available to say what else it is called."
	default:
		alts, aerr := h.altTitles.AlternativeTitles(r.Context(), sub.TMDBID)
		if aerr != nil {
			titlesNote = "Only the series' own title is recognised: its other names " +
				"could not be read from the provider (" + aerr.Error() + ")."
		} else {
			titles = append(titles, alts...)
		}
	}

	judged, ok := h.resolveProfile(w, r, in.ProfileID, sub.QualityProfileID)
	if !ok {
		return
	}
	term := strings.TrimSpace(in.Term)
	if term == "" {
		term = sub.SeriesTitle
	}
	resp, err := h.search.SearchSeason(r.Context(), search.SeasonSearch{
		Want: search.SeasonWant{ItemID: sub.ItemID, Titles: titles, Year: sub.SeriesYear,
			Season: sub.Season},
		Episodes: sub.Episodes, Term: term, Profile: judged.profile, IndexerIDs: in.IndexerIDs,
		LastSeason: sub.LastSeason,
	})
	switch {
	case errors.Is(err, search.ErrNoIndexers):
		writeProblem(w, http.StatusConflict, "no indexers are enabled")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	p := authz.FromContext(r.Context())
	code := (search.Target{ItemID: sub.ItemID, Season: sub.Season, Pack: true}).Code()
	candidates := make([]map[string]any, 0, len(resp.Candidates))
	matches, packs := 0, 0
	for _, c := range resp.Candidates {
		entry := candidateJSON(c)
		entry["matches"] = c.Target != nil
		if c.Target != nil {
			matches++
			if c.Target.Pack {
				packs++
				entry["pack"] = true
				if c.Target.LastSeason > 0 {
					entry["seasons"] = c.Target.Code()
				}
			} else {
				entry["episode"] = c.Target.Code()
			}
		}
		// A ticket only for a candidate sealed to this season or one of its
		// episodes, and accepted.
		if c.Accepted && c.Target != nil && c.Target.Valid() && h.tickets != nil && p != nil {
			if tok, terr := h.tickets.Seal(c, p.UserID); terr == nil {
				entry["ticket"] = tok
			}
		}
		candidates = append(candidates, entry)
	}

	body := map[string]any{
		"season": map[string]any{
			"item_id": sub.ItemID, "series": sub.SeriesTitle, "season": sub.Season,
			"code": code, "episodes": len(sub.Episodes), "have": sub.Have,
		},
		"term":       term,
		"titles":     titles,
		"candidates": candidates,
		"count":      len(candidates),
		"matches":    matches,
		"packs":      packs,
		"accepted":   len(resp.Accepted()),
		"indexers":   outcomesJSON(resp.Outcomes),
		"queried":    resp.Queried,
		"failed":     resp.Failed,
		"partial":    resp.Partial(),
		"elapsed_ms": resp.Elapsed.Milliseconds(),
	}
	judged.describe(body)
	if titlesNote != "" {
		body["titles_note"] = titlesNote
	}
	if resp.Partial() {
		body["warning"] = "some indexers did not answer; these results are incomplete"
	}
	if sub.Have > 0 {
		body["have_note"] = strconv.Itoa(sub.Have) + " of the season's " + strconv.Itoa(len(sub.Episodes)) +
			" episodes are already in the library. A pack imports only what is missing or better."
	}
	if matches == 0 && len(candidates) > 0 {
		body["note"] = "The indexers answered, but nothing they offered is " + sub.SeriesTitle +
			" " + code + " or one of its episodes. Each result says why."
	}
	writeJSON(w, http.StatusOK, body)
}

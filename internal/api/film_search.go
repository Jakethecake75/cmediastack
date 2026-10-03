package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// Searching for one film (ADR-0026).

// FilmTitleSource names every title a film goes by.
type FilmTitleSource interface {
	FilmTitles(ctx context.Context, filmID int64) ([]string, error)
}

// SearchForFilm searches the indexers for one film in the library.
//
// It never grabs. It returns every candidate judged against the film, and only
// one that IS the film — and that the profile, if one was chosen, accepts —
// carries a ticket, which seals the film as well as the release. The grab, the
// queue and the import then act on the server's decision about what the
// release is for, whatever the release calls itself.
func (h *Handlers) SearchForFilm(w http.ResponseWriter, r *http.Request) {
	switch {
	case h.search == nil:
		writeProblem(w, http.StatusNotImplemented, "no search service is wired")
		return
	case h.media == nil:
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in targetedSearchRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	film, err := h.media.Film(r.Context(), id)
	switch {
	case errors.Is(err, importer.ErrItemNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, importer.ErrNotAFilm):
		// A book is searched for here too (ADR-0049).
		if it, gerr := h.media.GetItem(r.Context(), id); gerr == nil && it.Kind == importer.KindBook {
			h.searchForBook(w, r, it, in)
			return
		}
		writeProblem(w, http.StatusConflict, "that is a series: its episodes are searched "+
			"for one at a time, from the series or the Wanted screen")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	name := filmName(film)
	// Refused before any indexer is asked: with no year, nothing could match
	// (ADR-0026, decision 3), and a search that cannot succeed should not
	// spend anybody's rate limit.
	if film.Year <= 0 {
		writeProblem(w, http.StatusConflict, film.Title+" has no year in the library, so no "+
			"release can be told apart from another film of the same name. Identify it, or "+
			"use Search.")
		return
	}

	// Every name a release may use for the film. Not having them is said: a
	// search that quietly matches on fewer names looks exactly like one that
	// found nothing.
	titles := []string{film.Title}
	var titlesNote string
	switch {
	case h.filmTitles == nil:
		titlesNote = "Only the film's own title is recognised: no metadata provider is " +
			"available to say what else it is called."
	case film.TMDBID <= 0:
		titlesNote = "Only the film's own title is recognised: it has not been identified, " +
			"so the provider cannot be asked what else it is called."
	default:
		names, nerr := h.filmTitles.FilmTitles(r.Context(), film.TMDBID)
		if nerr != nil {
			titlesNote = "Only the film's own title is recognised: its other names could not " +
				"be read from the provider (" + nerr.Error() + ")."
		} else {
			titles = appendNewTitles(titles, names)
		}
	}

	judged, ok := h.resolveProfile(w, r, in.ProfileID, film.QualityProfileID)
	if !ok {
		return
	}

	term := strings.TrimSpace(in.Term)
	if term == "" {
		term = search.FilmTerm(film.Title, film.Year)
	}
	resp, err := h.search.SearchFilm(r.Context(), search.FilmSearch{
		Want:       search.FilmWant{ItemID: film.ID, Titles: titles, Year: film.Year},
		Term:       term,
		Profile:    judged.profile,
		IndexerIDs: in.IndexerIDs,
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
	candidates := make([]map[string]any, 0, len(resp.Candidates))
	matches := 0
	for _, c := range resp.Candidates {
		entry := candidateJSON(c)
		isFilm := c.Target != nil && c.Target.Film && c.Target.ItemID == film.ID
		entry["matches"] = isFilm
		if isFilm {
			matches++
		}
		// A ticket only for a candidate that is the film AND is accepted.
		// SearchFilm already refuses every non-match; the target is checked
		// again because a ticket without it would grab a release into
		// nothing in particular — the twin this search exists to prevent.
		if c.Accepted && isFilm && h.tickets != nil && p != nil {
			if tok, terr := h.tickets.Seal(c, p.UserID); terr == nil {
				entry["ticket"] = tok
			}
		}
		candidates = append(candidates, entry)
	}

	have := false
	if files, ferr := h.media.FilesFor(r.Context(), film.ID); ferr == nil {
		have = len(files) > 0
	}
	body := map[string]any{
		"film": map[string]any{
			"id": film.ID, "title": film.Title, "year": film.Year, "name": name, "have": have,
		},
		"term":       term,
		"titles":     titles,
		"candidates": candidates,
		"count":      len(candidates),
		"matches":    matches,
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
	switch {
	case len(candidates) == 0:
		// The year in the default term is what a release a year out misses
		// (ADR-0026, decision 4), so the way round it is said here.
		body["note"] = "Nothing was offered for “" + term + "”. A release dated a " +
			"year out, or named differently, may turn up under another term — the title " +
			"alone, or one of its other names. Whatever is asked, only " + name + " can be grabbed."
	case matches == 0:
		body["note"] = "The indexers answered, but nothing they offered is " + name +
			". Each result says why."
	}
	writeJSON(w, http.StatusOK, body)
}

// filmName is "Dune (2021)".
func filmName(it importer.Item) string {
	if it.Year > 0 {
		return it.Title + " (" + itoa(it.Year) + ")"
	}
	return it.Title
}

// appendNewTitles adds the names not already present, in order.
func appendNewTitles(titles, more []string) []string {
	seen := make(map[string]bool, len(titles)+len(more))
	for _, t := range titles {
		seen[t] = true
	}
	for _, t := range more {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			seen[t] = true
			titles = append(titles, t)
		}
	}
	return titles
}

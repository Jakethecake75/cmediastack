package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// Searching for one wanted episode (ADR-0023).

// AlternativeTitleSource names the other titles a series goes by.
type AlternativeTitleSource interface {
	AlternativeTitles(ctx context.Context, seriesID int64) ([]string, error)
}

// targetedSearchRequest is the body of an episode or film search.
type targetedSearchRequest struct {
	// Term replaces what the indexers are asked for — one of the title's other
	// names, typically. It does not change what can be grabbed: every
	// candidate is still matched against the episode or film.
	Term string `json:"term"`
	// ProfileID is read as on every search (ADR-0027): absent is the default
	// profile, 0 is none, anything else is that profile.
	ProfileID  *int64  `json:"profile_id"`
	IndexerIDs []int64 `json:"indexer_ids"`
}

// SearchForEpisode searches the indexers for one episode.
//
// It never grabs. It returns every candidate judged against the episode, and
// only one that IS the episode — and that the profile, if one was chosen,
// accepts — carries a ticket. That ticket seals the episode as well as the
// release, so the grab, the queue and the import all act on the server's
// decision about what the release is for.
func (h *Handlers) SearchForEpisode(w http.ResponseWriter, r *http.Request) {
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
	var in targetedSearchRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	sub, err := h.episodes.ForSearch(r.Context(), id)
	switch {
	case errors.Is(err, library.ErrNoSuchEpisode):
		writeProblem(w, http.StatusNotFound, "no such episode")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	ep := sub.Episode

	// The names a release may use for this series. The provider's own title
	// first; then its alternative titles, without which "The.Office.US" is
	// refused as a different show. Not having them is said, not hidden: a
	// search that quietly matches on fewer names looks exactly like one that
	// found nothing.
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

	want := search.EpisodeWant{
		ItemID: ep.ItemID, Titles: titles, Year: sub.SeriesYear,
		Season: ep.SeasonNumber, Episode: ep.Number,
	}
	if !ep.Aired.IsZero() {
		want.AirDate = ep.Aired.UTC().Format("2006-01-02")
	}
	term := strings.TrimSpace(in.Term)
	if term == "" {
		term = sub.SeriesTitle
	}
	resp, err := h.search.SearchEpisode(r.Context(), search.EpisodeSearch{
		Want: want, Term: term, Profile: judged.profile, IndexerIDs: in.IndexerIDs,
		ByDate: sub.Daily,
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
	code := (search.Target{ItemID: ep.ItemID, Season: ep.SeasonNumber, Episode: ep.Number}).Code()
	candidates := make([]map[string]any, 0, len(resp.Candidates))
	matches := 0
	for _, c := range resp.Candidates {
		entry := candidateJSON(c)
		entry["matches"] = c.Target != nil
		if c.Target != nil {
			matches++
		}
		// A ticket only for a candidate that is the episode AND is accepted.
		// SearchEpisode already refuses every non-match, so the Target check
		// is the second of two; it is kept because a ticket without a target
		// would grab an episode's release into nothing in particular.
		if c.Accepted && c.Target != nil && h.tickets != nil && p != nil {
			if tok, terr := h.tickets.Seal(c, p.UserID); terr == nil {
				entry["ticket"] = tok
			}
		}
		candidates = append(candidates, entry)
	}

	body := map[string]any{
		"episode": map[string]any{
			"id": ep.ID, "item_id": ep.ItemID, "series": sub.SeriesTitle,
			"season": ep.SeasonNumber, "number": ep.Number, "code": code,
			"title": ep.Title, "have": ep.Have(),
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
	if matches == 0 && len(candidates) > 0 {
		// The commonest reason a search "finds nothing" is that it found plenty
		// and none of it was this episode. Saying so points at the reasons on
		// each row instead of at the indexers.
		body["note"] = "The indexers answered, but nothing they offered is " + sub.SeriesTitle +
			" " + code + ". Each result says why."
	}
	writeJSON(w, http.StatusOK, body)
}

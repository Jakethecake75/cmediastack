package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/music"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// Searching for one album (ADR-0046).

// albumSearchRequest is a targeted search without a profile: an album is
// judged by the audio ladder, not a video quality profile.
type albumSearchRequest struct {
	Term       string  `json:"term"`
	IndexerIDs []int64 `json:"indexer_ids"`
}

// albumYear is the year of an album's first release, or zero.
func albumYear(a music.Album) int {
	if len(a.Released) < 4 {
		return 0
	}
	y, err := strconv.Atoi(a.Released[:4])
	if err != nil {
		return 0
	}
	return y
}

// albumName is "Portishead — Dummy (1994)".
func albumName(a music.Album) string {
	name := a.Title
	if y := albumYear(a); y > 0 {
		name += " (" + itoa(y) + ")"
	}
	if a.Artist != "" {
		name = a.Artist + " — " + name
	}
	return name
}

// SearchForAlbum searches the indexers for one album the caller may see.
//
// It never grabs. Only a candidate that is the album and whose format the name
// says carries a ticket, and the ticket seals the album with the release, so
// the grab and the import act on the server's decision about what it is for.
func (h *Handlers) SearchForAlbum(w http.ResponseWriter, r *http.Request) {
	switch {
	case h.search == nil:
		writeProblem(w, http.StatusNotImplemented, "no search service is wired")
		return
	case h.music == nil:
		writeProblem(w, http.StatusNotImplemented, "music is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in albumSearchRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	// Scoped: an album the caller may not see answers as a missing one.
	album, err := h.music.Album(r.Context(), id)
	if err != nil {
		musicProblem(w, err)
		return
	}
	if album.Artist == "" {
		writeProblem(w, http.StatusConflict, "the album's artist has no name, so no release can be matched to it")
		return
	}
	namesake := false
	if others, oerr := h.music.Store().Albums(r.Context(), album.ItemID); oerr == nil {
		folded := search.NormalizeTitle(album.Title)
		for _, o := range others {
			if o.ID != album.ID && search.NormalizeTitle(o.Title) == folded {
				namesake = true
			}
		}
	}
	want := search.AlbumWant{ItemID: album.ItemID, AlbumID: album.ID, Artists: []string{album.Artist},
		Title: album.Title, Year: albumYear(album), Namesake: namesake}
	term := strings.TrimSpace(in.Term)
	if term == "" {
		term = search.AlbumTerm(album.Artist, album.Title)
	}
	resp, err := h.search.SearchAlbum(r.Context(), search.AlbumSearch{
		Want: want, Term: term, IndexerIDs: in.IndexerIDs,
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
		isAlbum := c.Target != nil && c.Target.Album == album.ID && c.Target.ItemID == album.ItemID
		entry["matches"] = isAlbum
		if isAlbum {
			matches++
		}
		if c.Accepted && isAlbum && h.tickets != nil && p != nil {
			if tok, terr := h.tickets.Seal(c, p.UserID); terr == nil {
				entry["ticket"] = tok
			}
		}
		candidates = append(candidates, entry)
	}
	name := albumName(album)
	body := map[string]any{
		"album": map[string]any{"id": album.ID, "item_id": album.ItemID, "artist": album.Artist,
			"title": album.Title, "year": want.Year, "name": name, "have": album.Have, "known": album.Known},
		"term":       term,
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
	if resp.Partial() {
		body["warning"] = "some indexers did not answer; these results are incomplete"
	}
	switch {
	case len(candidates) == 0:
		body["note"] = "Nothing was offered for “" + term + "”. Whatever is asked, only " + name +
			" can be grabbed."
	case matches == 0:
		body["note"] = "The indexers answered, but nothing they offered is " + name + ". Each result says why."
	}
	writeJSON(w, http.StatusOK, body)
}

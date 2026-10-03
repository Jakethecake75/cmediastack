package api

import (
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// Three library routes registered in Phase 1 and built in 4ad (ADR-0038): a
// search of the library, a title's poster, and a title's original file.

// searchLimit is how many titles one search returns.
const searchLimit = 50

// SearchLibrary finds titles in the library by name (ADR-0038, decision 2).
//
// Every word of the query must appear in the title, both folded as the release
// matcher folds them — case, accents and punctuation — so "pokemon" finds
// "Pokémon". A title that starts with the query sorts first. It reads the
// library through ListItems, so it sees exactly what the caller may see
// (ADR-0037), and it never asks the provider.
func (h *Handlers) SearchLibrary(w http.ResponseWriter, r *http.Request) {
	if h.media == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if n := utf8.RuneCountInString(q); n < 2 || n > 100 {
		writeProblem(w, http.StatusBadRequest, "q is what to search for: 2 to 100 characters")
		return
	}
	want := search.NormalizeTitle(q)
	words := strings.Fields(want)
	if len(words) == 0 {
		writeProblem(w, http.StatusBadRequest, "q has no letters or digits to search for")
		return
	}

	items, err := h.media.ListItems(r.Context(), "")
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	type hit struct {
		it     importer.Item
		prefix bool
	}
	var hits []hit
	for _, it := range items {
		title := search.NormalizeTitle(it.Title)
		padded := " " + title + " "
		all := true
		for _, word := range words {
			if !strings.Contains(padded, " "+word) {
				all = false
				break
			}
		}
		if all {
			hits = append(hits, hit{it: it, prefix: strings.HasPrefix(title, want)})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].prefix != hits[j].prefix {
			return hits[i].prefix
		}
		return hits[i].it.SortTitle < hits[j].it.SortTitle
	})
	total := len(hits)
	if total > searchLimit {
		hits = hits[:searchLimit]
	}
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, itemJSON(h.it))
	}
	body := map[string]any{"query": q, "items": out, "count": len(out)}
	if total > len(out) {
		body["more"] = total - len(out)
	}
	writeJSON(w, http.StatusOK, body)
}

// TitleArtwork is a title's poster (ADR-0038, decision 3): the one cached for
// its provider id. Keyed by the title, so the title's scope decides it, and a
// title out of scope reads as one with no poster.
func (h *Handlers) TitleArtwork(w http.ResponseWriter, r *http.Request) {
	if h.media == nil || h.artwork == nil {
		writeProblem(w, http.StatusNotImplemented, "no artwork cache is wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	it, err := h.media.GetItem(r.Context(), id)
	if err != nil || it.TMDBID <= 0 {
		writeProblem(w, http.StatusNotFound, "no artwork has been cached for that title")
		return
	}
	h.servePoster(w, r, "tmdb", it.TMDBID)
}

// DownloadOriginal serves one of a title's files as a download (ADR-0038,
// decision 4): the bytes as stored, resumable, audited.
func (h *Handlers) DownloadOriginal(w http.ResponseWriter, r *http.Request) {
	if h.media == nil || h.playback == nil {
		writeProblem(w, http.StatusNotImplemented, "playback is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	it, err := h.media.GetItem(r.Context(), id)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	files, err := h.media.FilesFor(r.Context(), id)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	if len(files) == 0 {
		writeProblem(w, http.StatusNotFound, "this title has no file to download")
		return
	}
	file := files[0]
	if raw := r.URL.Query().Get("file"); raw != "" {
		fid, perr := strconv.ParseInt(raw, 10, 64)
		found := false
		for _, f := range files {
			if perr == nil && f.ID == fid {
				file, found = f, true
			}
		}
		if !found {
			writeProblem(w, http.StatusNotFound, "that is not one of this title's files")
			return
		}
	} else if len(files) > 1 {
		ids := make([]int64, 0, len(files))
		for _, f := range files {
			ids = append(ids, f.ID)
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "this title has several files; name one with ?file=",
			"files": ids,
		})
		return
	}

	// Written before the bytes: a download that fails half way was still
	// asked for, and the line is about the asking. Once per download, not per
	// resumed range: a request for a range that does not start at the
	// beginning is the same download continuing.
	rng := r.Header.Get("Range")
	resumed := rng != "" && !strings.HasPrefix(rng, "bytes=0-")
	if p := authz.FromContext(r.Context()); p != nil && h.audit != nil && !resumed {
		_ = h.audit.Write(r.Context(), audit.Event{
			ActorUserID: &p.UserID, ActorLabel: p.Username,
			Action:     audit.ActionMediaDownloaded,
			Outcome:    audit.OutcomeSuccess,
			TargetKind: "media_file", TargetID: fmt.Sprint(file.ID),
			SourceIP:  ClientIP(r.Context()),
			UserAgent: r.UserAgent(),
			Detail:    fmt.Sprintf("%s: %s", filmName(it), path.Base(file.RelPath)),
		})
	}
	if err := h.playback.ServeAttachment(w, r, file.ID); err != nil {
		if errors.Is(err, importer.ErrFileNotFound) {
			writeProblem(w, http.StatusNotFound, "no such file")
			return
		}
		writeAuthzAware(w, err)
	}
}

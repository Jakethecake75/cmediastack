package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/follow"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/music"
)

// Adding a title to the library before any of it is on disk: a series
// (ADR-0025) or a film (ADR-0026).

// LibraryAdder adds titles.
type LibraryAdder interface {
	Add(ctx context.Context, req follow.Request) (follow.Result, error)
}

// addMediaRequest is the body of POST /api/v1/media.
//
// No title and no year: the item is named by the provider's answer for the id,
// never by the request (ADR-0025, decision 1). decodeJSON refuses unknown
// fields, so a "title" sent anyway is a 400 rather than silently ignored.
type addMediaRequest struct {
	Kind         string `json:"kind"`
	TMDBID       int64  `json:"tmdb_id"`
	RootFolderID int64  `json:"root_folder_id"`
	Folder       string `json:"folder"`
	Monitor      string `json:"monitor"`
	// MusicBrainzID names an artist to follow (ADR-0044).
	MusicBrainzID string `json:"musicbrainz_id"`
	// OpenLibraryID names a book to add (ADR-0048).
	OpenLibraryID string `json:"openlibrary_id"`
}

// AddMedia puts a series in the library, with its episodes and the monitoring
// chosen, or a film — or nothing, and a sentence saying why.
func (h *Handlers) AddMedia(w http.ResponseWriter, r *http.Request) {
	var in addMediaRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if strings.EqualFold(strings.TrimSpace(in.Kind), music.KindArtist) {
		h.addArtist(w, r, in)
		return
	}
	if strings.EqualFold(strings.TrimSpace(in.Kind), importer.KindBook) {
		h.addBook(w, r, in)
		return
	}
	if h.adder == nil {
		writeProblem(w, http.StatusNotImplemented, "adding to the library is not wired")
		return
	}

	res, err := h.adder.Add(r.Context(), follow.Request{
		Kind: in.Kind, TMDBID: in.TMDBID, RootFolderID: in.RootFolderID,
		Folder: in.Folder, Monitor: in.Monitor,
	})
	// What the sentences below call it. Only for wording: the service decides
	// what the kind is, and refuses one it does not know.
	what, roots := "series", "series"
	if strings.EqualFold(strings.TrimSpace(in.Kind), importer.KindMovie) {
		what, roots = "film", "films"
	}
	var conflict *importer.ConflictError
	switch {
	case err == nil:
	case errors.As(err, &conflict):
		// The item it ran into, so the screen can offer to open it rather
		// than leave somebody to go and find it.
		// Which of the two conflicts, said as a word the screen can act on: an
		// approved request may be linked to the title already there (ADR-0028),
		// and never to whatever happens to occupy the folder it would have used.
		note, kind := "It is already in the library.", "already_in_library"
		if errors.Is(err, importer.ErrFolderTaken) {
			note, kind = "Another item already occupies that folder — often one a scan found "+
				"from files already there. Identify that item instead, or name another "+
				"folder. Nothing was changed.", "folder_taken"
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": err.Error(), "item": itemJSON(conflict.Existing), "note": note,
			"conflict": kind,
		})
		return
	case errors.Is(err, follow.ErrNotAddable),
		errors.Is(err, library.ErrNoSuchMonitoring),
		errors.Is(err, importer.ErrUnusableFolder),
		errors.Is(err, follow.ErrWrongRootFolder),
		errors.Is(err, follow.ErrChooseRootFolder):
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, follow.ErrNoRootFolder):
		writeProblem(w, http.StatusConflict,
			"no root folder for "+roots+" is configured; add one under Storage first")
		return
	case errors.Is(err, metadata.ErrNoProvider):
		writeProblem(w, http.StatusConflict,
			"no metadata provider is configured; a "+what+" is added from the provider's "+
				"answer, so one is needed — set it on the Metadata screen")
		return
	case errors.Is(err, metadata.ErrNotFound):
		writeProblem(w, http.StatusUnprocessableEntity,
			"the provider has no such "+what+", so nothing was added: "+err.Error())
		return
	case errors.Is(err, metadata.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		writeProblem(w, http.StatusServiceUnavailable,
			"the provider is rate-limiting this instance, so nothing was added; try again in a minute")
		return
	case isProviderFailure(err):
		writeProblem(w, http.StatusBadGateway,
			"the provider could not be read, so nothing was added: "+err.Error())
		return
	case errors.Is(err, importer.ErrUnnameable):
		writeProblem(w, http.StatusUnprocessableEntity,
			"no folder name can be made from the provider's title; name the folder: "+err.Error())
		return
	default:
		writeAuthzAware(w, err)
		return
	}

	w.Header().Set("Location", "/api/v1/media/"+strconv.FormatInt(res.Item.ID, 10))
	if res.Item.Kind == importer.KindMovie {
		writeJSON(w, http.StatusCreated, map[string]any{
			"item": itemJSON(res.Item),
			"root": res.Root.Path,
			"note": "Nothing was downloaded, and nothing was created on disk: the folder " +
				"appears when the film is imported into it. It is on the Wanted list until " +
				"then; search for it from there or from its page in the library.",
		})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"item":      itemJSON(res.Item),
		"root":      res.Root.Path,
		"monitor":   string(res.Monitor),
		"seasons":   res.Seasons,
		"episodes":  res.Episodes,
		"monitored": res.Monitored,
		"wanted":    res.Wanted,
		"note": "Nothing was downloaded, and nothing was created on disk: the folder " +
			"appears when the first episode is imported into it. Missing episodes are " +
			"searched for one at a time, from the series or the Wanted screen.",
	})
}

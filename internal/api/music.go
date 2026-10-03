package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/music"
)

// Music (ADR-0044): artists followed as titles, their albums and tracks.

// MusicService is what the music routes need.
type MusicService interface {
	SearchArtists(ctx context.Context, name string) ([]music.ArtistMatch, error)
	Add(ctx context.Context, req music.AddRequest) (music.AddResult, error)
	Album(ctx context.Context, albumID int64) (music.Album, error)
	Store() *music.Store
}

func albumJSON(a music.Album) map[string]any {
	out := map[string]any{"id": a.ID, "item_id": a.ItemID, "artist": a.Artist, "title": a.Title,
		"type": a.Type, "monitored": a.Monitored, "have": a.Have, "known": a.Known,
		"tracks_known": a.TracksKnown, "musicbrainz_id": a.MBID}
	if a.Released != "" {
		out["released"] = a.Released
		if y, err := strconv.Atoi(a.Released[:min(4, len(a.Released))]); err == nil {
			out["year"] = y
		}
	}
	if a.Tracks != nil {
		tracks := make([]map[string]any, 0, len(a.Tracks))
		for _, t := range a.Tracks {
			row := map[string]any{"id": t.ID, "disc": t.Disc, "number": t.Number, "title": t.Title}
			if t.LengthMS > 0 {
				row["length_ms"] = t.LengthMS
			}
			if t.FileID > 0 {
				row["file_id"] = t.FileID
			}
			tracks = append(tracks, row)
		}
		out["tracks"] = tracks
	}
	return out
}

func musicProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, music.ErrNoSuchAlbum):
		writeProblem(w, http.StatusNotFound, "not found")
	case errors.Is(err, music.ErrNoSuchMonitoring), errors.Is(err, music.ErrWrongRootFolder),
		errors.Is(err, music.ErrChooseRootFolder), errors.Is(err, importer.ErrUnusableFolder):
		writeProblem(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, music.ErrNoRootFolder):
		writeProblem(w, http.StatusConflict, "no root folder for music is configured; add one under Storage first")
	case errors.Is(err, music.ErrNotFound):
		writeProblem(w, http.StatusUnprocessableEntity, "MusicBrainz has no such artist, so nothing was added")
	case errors.Is(err, music.ErrUnavailable):
		writeProblem(w, http.StatusBadGateway, "MusicBrainz could not be asked, so nothing was done: "+err.Error())
	default:
		writeAuthzAware(w, err)
	}
}

// SearchArtists asks MusicBrainz for artists by name.
func (h *Handlers) SearchArtists(w http.ResponseWriter, r *http.Request) {
	if h.music == nil {
		writeProblem(w, http.StatusNotImplemented, "music is not wired")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 200 {
		writeProblem(w, http.StatusBadRequest, "q is the artist's name")
		return
	}
	found, err := h.music.SearchArtists(r.Context(), q)
	if err != nil {
		musicProblem(w, err)
		return
	}
	out := make([]map[string]any, 0, len(found))
	for _, a := range found {
		out = append(out, map[string]any{"musicbrainz_id": a.MBID, "name": a.Name,
			"disambiguation": a.Disambiguation, "country": a.Country, "type": a.Type, "begin": a.Begin})
	}
	writeJSON(w, http.StatusOK, map[string]any{"artists": out, "count": len(out)})
}

// addArtist is AddMedia for an artist.
func (h *Handlers) addArtist(w http.ResponseWriter, r *http.Request, in addMediaRequest) {
	if h.music == nil {
		writeProblem(w, http.StatusNotImplemented, "music is not wired")
		return
	}
	res, err := h.music.Add(r.Context(), music.AddRequest{MBID: in.MusicBrainzID,
		RootFolderID: in.RootFolderID, Folder: in.Folder, Monitor: in.Monitor,
		SourceIP: ClientIP(r.Context()), UserAgent: r.UserAgent()})
	var conflict *importer.ConflictError
	switch {
	case err == nil:
	case errors.As(err, &conflict):
		note, kind := "They are already in the library.", "already_in_library"
		if errors.Is(err, importer.ErrFolderTaken) {
			note, kind = "Another item already occupies that folder. Name another folder.", "folder_taken"
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(),
			"item": itemJSON(conflict.Existing), "note": note, "conflict": kind})
		return
	default:
		musicProblem(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/media/"+strconv.FormatInt(res.Item.ID, 10))
	writeJSON(w, http.StatusCreated, map[string]any{
		"item": itemJSON(res.Item), "albums": res.Albums, "wanted": res.Wanted, "monitor": in.Monitor,
		"note": "Nothing was downloaded, and nothing was created on disk. Each album's track list " +
			"is fetched from MusicBrainz when it is first opened.",
	})
}

// ArtistAlbums lists an artist's albums, with what the library holds of each.
func (h *Handlers) ArtistAlbums(w http.ResponseWriter, r *http.Request) {
	if h.music == nil {
		writeProblem(w, http.StatusNotImplemented, "music is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if h.media != nil {
		if _, err := h.media.GetItem(r.Context(), id); err != nil {
			writeAuthzAware(w, err)
			return
		}
	}
	albums, err := h.music.Store().Albums(r.Context(), id)
	if err != nil {
		musicProblem(w, err)
		return
	}
	out := make([]map[string]any, 0, len(albums))
	for _, a := range albums {
		out = append(out, albumJSON(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"albums": out, "count": len(out)})
}

// GetAlbum returns an album with its tracks.
func (h *Handlers) GetAlbum(w http.ResponseWriter, r *http.Request) {
	if h.music == nil {
		writeProblem(w, http.StatusNotImplemented, "music is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a, err := h.music.Album(r.Context(), id)
	if err != nil {
		musicProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, albumJSON(a))
}

// SetAlbumMonitored turns an album on or off.
func (h *Handlers) SetAlbumMonitored(w http.ResponseWriter, r *http.Request) {
	if h.music == nil {
		writeProblem(w, http.StatusNotImplemented, "music is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in monitorRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	a, err := h.music.Store().SetAlbumMonitored(r.Context(), id, in.Monitored)
	if err != nil {
		musicProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, albumJSON(a))
}

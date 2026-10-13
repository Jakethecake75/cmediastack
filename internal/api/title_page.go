package api

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/jakethecake75/cmediastack/internal/artwork"
	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/metadata"
)

// A title's page (ADR-0078): what the provider says about a title in the
// library, its backdrop, and an episode's still.

// TitleDetailsSource is what the provider says about an identified title.
// Satisfied by *metadata.Service, which keeps each answer for a day.
type TitleDetailsSource interface {
	TitleDetails(ctx context.Context, kind metadata.Kind, id int64) (metadata.Details, error)
	ImageBase() string
}

// ImageFetcher caches one of the provider's images and returns its path in
// the artwork cache. Satisfied by *artwork.Store, which decides what may be
// fetched: one filename, from an allowed host, that is a picture.
type ImageFetcher interface {
	Fetch(ctx context.Context, base string, r artwork.Ref) (string, error)
}

// MediaDetails is what a title's page says about it beyond the library's own
// row: the overview, genres, score, runtime and whether there is a backdrop.
//
// Read in the caller's scope first, so a title out of it is a 404 and nothing
// is asked of the provider. A title that is not identified, or a provider
// that cannot be reached, answers with nothing: the page is drawn without.
func (h *Handlers) MediaDetails(w http.ResponseWriter, r *http.Request) {
	if h.media == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	it, err := h.media.GetItem(r.Context(), id)
	switch {
	case errors.Is(err, importer.ErrItemNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	body := map[string]any{}
	d, err := h.titleDetails(r.Context(), it)
	if authz.IsDenied(err) {
		writeAuthzAware(w, err)
		return
	}
	if err == nil {
		if d.Overview != "" {
			body["overview"] = d.Overview
		}
		if d.Tagline != "" {
			body["tagline"] = d.Tagline
		}
		if len(d.Genres) > 0 {
			body["genres"] = d.Genres
		}
		if d.Runtime > 0 {
			body["runtime_minutes"] = d.Runtime
		}
		if d.Score > 0 {
			body["score"] = d.Score
		}
		if d.BackdropPath != "" && h.images != nil {
			body["backdrop"] = artworkPath(it.ID) + "?kind=backdrop"
		}
	}
	writeJSON(w, http.StatusOK, body)
}

// errNoDetails is a title the provider is not asked about.
var errNoDetails = errors.New("api: the title has no provider details")

// titleDetails asks the provider about a film or a series it identified.
func (h *Handlers) titleDetails(ctx context.Context, it importer.Item) (metadata.Details, error) {
	if h.titles == nil || it.TMDBID <= 0 ||
		(it.Kind != importer.KindMovie && it.Kind != importer.KindSeries) {
		return metadata.Details{}, errNoDetails
	}
	return h.titles.TitleDetails(ctx, metadata.Kind(it.Kind), it.TMDBID)
}

// serveBackdrop writes a title's backdrop. The caller has read the title in
// the caller's scope.
func (h *Handlers) serveBackdrop(w http.ResponseWriter, r *http.Request, it importer.Item) {
	d, err := h.titleDetails(r.Context(), it)
	if err != nil || d.BackdropPath == "" {
		writeProblem(w, http.StatusNotFound, "no artwork has been cached for that title")
		return
	}
	// A film and a series can carry the same provider id, so the series' are
	// kept apart: sharing a file would show one title the other's picture.
	provider := "tmdb"
	if it.Kind == importer.KindSeries {
		provider = "tmdbtv"
	}
	h.serveImage(w, r, artwork.Ref{Kind: artwork.KindBackdrop, Provider: provider,
		ID: it.TMDBID, Size: "w1280", RemotePath: d.BackdropPath})
}

// EpisodeStill is an episode's picture (ADR-0078), for a caller who may see
// its series. The filename is the one stored when the season was read.
func (h *Handlers) EpisodeStill(w http.ResponseWriter, r *http.Request) {
	if h.episodes == nil {
		writeProblem(w, http.StatusNotImplemented, "episode tracking is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	providerID, still, err := h.episodes.Still(r.Context(), id)
	if authz.IsDenied(err) {
		writeAuthzAware(w, err)
		return
	}
	if err != nil {
		writeProblem(w, http.StatusNotFound, "no artwork has been cached for that episode")
		return
	}
	h.serveImage(w, r, artwork.Ref{Kind: artwork.KindStill, Provider: "tmdb",
		ID: providerID, Size: "w300", RemotePath: still})
}

// serveImage writes one of the provider's images from the artwork cache,
// fetching it first when it is not there. A picture that cannot be had is a
// 404: the page shows none, which is all a missing picture costs.
func (h *Handlers) serveImage(w http.ResponseWriter, r *http.Request, ref artwork.Ref) {
	if h.images == nil || h.titles == nil || h.artwork == nil {
		writeProblem(w, http.StatusNotFound, "no artwork cache is wired")
		return
	}
	rel, err := h.images.Fetch(r.Context(), h.titles.ImageBase(), ref)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "no artwork has been cached for that title")
		return
	}
	f, err := h.artwork.Open(rel)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "no artwork has been cached for that title")
		return
	}
	defer func() { _ = f.Close() }()
	// As a poster is served: the type from the cached name, never sniffed, and
	// cached hard because a provider's image at a size does not change.
	w.Header().Set("Content-Type", artwork.ContentType(rel))
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	_, _ = io.Copy(w, f)
}

package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/artwork"
	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identify"
	"github.com/jakethecake75/cmediastack/internal/metadata"
)

// The identification review surface, and the artwork that goes with it.

// IdentifyService is what the API needs from the identifier.
type IdentifyService interface {
	Pending(ctx context.Context, limit int) ([]*identify.Identification, error)
	Get(ctx context.Context, itemID int64) (*identify.Identification, error)
	Confirm(ctx context.Context, itemID, providerID int64) (identify.Candidate, error)
	Reject(ctx context.Context, itemID int64, why string) error
	Reopen(ctx context.Context, itemID int64) error
	RunPass(ctx context.Context, limit int) (identify.PassResult, error)
	// CachePoster fetches a poster whose remote path this software already
	// recorded. The handler passes a provider and an id and nothing else: the
	// URL is reconstructed from stored data, never from the request.
	CachePoster(ctx context.Context, provider string, providerID int64) (string, error)
}

// ArtworkReader opens a cached image. Satisfied by *library.Cache.
type ArtworkReader interface {
	Exists(name string) bool
	Open(name string) (*os.File, error)
}

func identificationJSON(i *identify.Identification) map[string]any {
	out := map[string]any{
		"item_id":      i.ItemID,
		"state":        string(i.State),
		"parsed_title": i.ParsedTitle,
		// Said in words, because the number is for ordering and this is what a
		// person is actually answering: "is this the same film?"
		"why":        i.VerdictWhy,
		"verdict":    string(i.Verdict),
		"updated_at": i.UpdatedAt,
		// Whether a PERSON decided. An automatic decision may be revisited by a
		// later pass; a person's stands, and an operator looking at this screen
		// needs to know which they are looking at.
		"decided_by_a_person": i.ByAPerson(),
	}
	if i.ParsedYear > 0 {
		out["parsed_year"] = i.ParsedYear
	}
	if i.ProviderID > 0 {
		out["provider"] = i.Provider
		out["provider_id"] = i.ProviderID
	}
	if i.DecidedAt != nil {
		out["decided_at"] = i.DecidedAt
	}
	if i.SearchedAt != nil {
		out["searched_at"] = i.SearchedAt
	}

	cands := make([]map[string]any, 0, len(i.Candidates))
	for _, c := range i.Candidates {
		row := map[string]any{
			"provider_id": c.ProviderID,
			"title":       c.Title,
			"overview":    c.Overview,
			"score":       c.Score,
			"why":         c.Why,
		}
		if c.Year > 0 {
			row["year"] = c.Year
		}
		// Shown because a release name often used the original title, so it is
		// what lets somebody recognise the right row.
		if c.OriginalTitle != "" && c.OriginalTitle != c.Title {
			row["original_title"] = c.OriginalTitle
		}
		if c.PosterPath != "" {
			// The path this instance would serve it at, not the provider's —
			// the client never talks to the provider, and a provider URL here
			// would make every review screen leak the library to a third party
			// from the operator's own browser.
			row["poster"] = "/api/v1/artwork/poster/" + c.Provider + "/" +
				strconv.FormatInt(c.ProviderID, 10)
		}
		cands = append(cands, row)
	}
	out["candidates"] = cands
	return out
}

// PendingIdentifications lists what is waiting for a person.
func (h *Handlers) PendingIdentifications(w http.ResponseWriter, r *http.Request) {
	if h.identify == nil {
		writeProblem(w, http.StatusNotImplemented, "identification is not wired")
		return
	}
	items, err := h.identify.Pending(r.Context(), 100)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, i := range items {
		out = append(out, identificationJSON(i))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"count": len(out),
		"note": "Confirming attaches a provider id and, when the titles differ, " +
			"renames the library entry. Nothing on disk is touched.",
	})
}

// GetIdentification returns one item's identification.
func (h *Handlers) GetIdentification(w http.ResponseWriter, r *http.Request) {
	if h.identify == nil {
		writeProblem(w, http.StatusNotImplemented, "identification is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	got, err := h.identify.Get(r.Context(), id)
	if errors.Is(err, identify.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, "that item has not been identified")
		return
	}
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, identificationJSON(got))
}

type confirmRequest struct {
	ProviderID int64 `json:"provider_id"`
}

// ConfirmIdentification attaches a candidate on a person's authority.
func (h *Handlers) ConfirmIdentification(w http.ResponseWriter, r *http.Request) {
	if h.identify == nil {
		writeProblem(w, http.StatusNotImplemented, "identification is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in confirmRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	chosen, err := h.identify.Confirm(r.Context(), id, in.ProviderID)
	switch {
	case errors.Is(err, identify.ErrNoSuchCandidate):
		// 409, not 404: the item exists and so does the endpoint. What is wrong
		// is that the client sent an id nobody offered, which usually means the
		// screen it is looking at is stale.
		writeProblem(w, http.StatusConflict,
			"that was not one of the candidates offered for this item; reload and try again")
		return
	case errors.Is(err, identify.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "that item has not been identified")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"item_id":     id,
		"provider_id": chosen.ProviderID,
		"title":       chosen.Title,
		"note": "Confirmed. The library entry now carries this identification; " +
			"nothing on disk was renamed or moved.",
	})
}

type rejectRequest struct {
	Reason string `json:"reason"`
}

// RejectIdentification records that none of the candidates is right.
func (h *Handlers) RejectIdentification(w http.ResponseWriter, r *http.Request) {
	if h.identify == nil {
		writeProblem(w, http.StatusNotImplemented, "identification is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in rejectRequest
	if r.ContentLength > 0 && !decodeJSON(w, r, &in) {
		return
	}

	if err := h.identify.Reject(r.Context(), id, in.Reason); err != nil {
		if errors.Is(err, identify.ErrNotFound) {
			writeProblem(w, http.StatusNotFound, "that item has not been identified")
			return
		}
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"item_id": id,
		"note": "Recorded. The identification pass will leave this item alone " +
			"from now on; reopen it if you change your mind.",
	})
}

// ReopenIdentification clears a decision so the item can be identified again.
func (h *Handlers) ReopenIdentification(w http.ResponseWriter, r *http.Request) {
	if h.identify == nil {
		writeProblem(w, http.StatusNotImplemented, "identification is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.identify.Reopen(r.Context(), id); err != nil {
		if errors.Is(err, identify.ErrNotFound) {
			writeProblem(w, http.StatusNotFound, "that item has not been identified")
			return
		}
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"item_id": id,
		"note":    "Reopened. The next identification pass will look at it again.",
	})
}

// RunIdentification triggers a pass on demand.
func (h *Handlers) RunIdentification(w http.ResponseWriter, r *http.Request) {
	if h.identify == nil {
		writeProblem(w, http.StatusNotImplemented, "identification is not wired")
		return
	}
	res, err := h.identify.RunPass(r.Context(), 200)
	switch {
	case errors.Is(err, metadata.ErrNoProvider):
		writeProblem(w, http.StatusConflict,
			"no metadata provider is configured; set one on the Metadata screen")
		return
	case errors.Is(err, metadata.ErrRateLimited):
		writeProblem(w, http.StatusServiceUnavailable,
			"the provider is rate-limiting this instance; what was done so far is recorded")
		return
	case err != nil && !errors.Is(err, metadata.ErrUnauthorized):
		writeAuthzAware(w, err)
		return
	}

	body := map[string]any{
		"considered":    res.Considered,
		"accepted":      res.Accepted,
		"proposed":      res.Proposed,
		"nothing_found": res.NothingFound,
		"skipped":       res.Skipped,
		"failed":        res.Failed,
	}
	if err != nil {
		body["stopped_early"] = err.Error()
	}
	body["note"] = "Accepted items matched a title and year exactly and were " +
		"attached automatically; nothing was renamed. Proposed items are " +
		"waiting for you."
	writeJSON(w, http.StatusOK, body)
}

// ---------------------------------------------------------------------------
// Artwork
// ---------------------------------------------------------------------------

// Poster serves a cached image.
//
// # Why this instance serves them rather than linking the provider
//
// A review screen full of provider URLs would make the operator's own browser
// fetch from a third party, once per row — announcing the library to that party
// from the operator's address, and defeating the egress policy the server-side
// fetch exists to honour (ADR-0018). So the bytes come from here.
//
// Nothing is fetched on demand: this reads what the identification pass already
// cached. A missing poster is a 404, not a request to a CDN, because a page of
// broken images is a cosmetic problem and a page that triggers a hundred
// outbound requests is not.
func (h *Handlers) Poster(w http.ResponseWriter, r *http.Request) {
	if h.artwork == nil {
		writeProblem(w, http.StatusNotImplemented, "no artwork cache is wired")
		return
	}
	provider := strings.ToLower(strings.TrimSpace(r.PathValue("provider")))
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 || !isSimpleName(provider) {
		writeProblem(w, http.StatusBadRequest, "not an artwork reference")
		return
	}

	// Somebody who may not edit the library — who never uses the screens that
	// show search candidates — sees a poster only for a title they can see
	// (ADR-0037), so a poster is not a way to learn what else is held.
	if p := authz.FromContext(r.Context()); p == nil || !p.Has(authz.PermEditLibraryItems) {
		if h.media == nil || provider != "tmdb" || !h.posterVisible(r, id) {
			writeProblem(w, http.StatusNotFound, "no artwork has been cached for that title")
			return
		}
	}
	h.servePoster(w, r, provider, id)
}

// servePoster writes the cached poster for a provider's id, fetching it first
// when it is a candidate this instance recorded. The caller has decided the
// caller may see it.
func (h *Handlers) servePoster(w http.ResponseWriter, r *http.Request, provider string, id int64) {
	// The path is rebuilt from validated values, exactly as internal/artwork
	// built it — never from anything in the request. The cache is os.Root
	// underneath, so a mistake here is contained rather than fatal, but the
	// point is not to make one.
	size := "w342"
	rel, ok := h.findPoster(provider, id, size)
	if !ok {
		// A miss is not necessarily a no. The pass caches posters for what it
		// ACCEPTS, which leaves exactly the candidates on the review screen —
		// the one screen where a person is choosing by looking — without any.
		// So fetch it now, from the path stored alongside that candidate.
		//
		// Only for an id this software recorded: identify.ErrNotRecorded is the
		// answer for anything else, and no request leaves the process. That is
		// what stops this from being an open proxy wearing an artwork route.
		if h.identify == nil {
			writeProblem(w, http.StatusNotFound, "no artwork has been cached for that title")
			return
		}
		if _, err := h.identify.CachePoster(r.Context(), provider, id); err != nil {
			if errors.Is(err, identify.ErrNotRecorded) {
				writeProblem(w, http.StatusNotFound,
					"this instance has no record of that title, so there is no "+
						"poster it is willing to fetch")
				return
			}
			if authz.IsDenied(err) {
				writeAuthzAware(w, err)
				return
			}
			// No provider, or the provider could not be reached: the page
			// shows no picture, which is all a missing poster costs. Not a 500.
			writeProblem(w, http.StatusNotFound, "no artwork has been cached for that title")
			return
		}
		if rel, ok = h.findPoster(provider, id, size); !ok {
			writeProblem(w, http.StatusNotFound, "no artwork has been cached for that title")
			return
		}
	}

	f, err := h.artwork.Open(rel)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "no artwork has been cached for that title")
		return
	}
	defer func() { _ = f.Close() }()

	// Explicit, never sniffed — and the security headers add nosniff, which is
	// what stops a file that somehow got past internal/artwork's magic-byte
	// check from being interpreted as anything but a picture.
	w.Header().Set("Content-Type", artwork.ContentType(rel))
	// Cached hard: these are immutable. A poster for a given provider id at a
	// given size never changes; if the provider replaces the image, the way to
	// see it is to re-fetch into the cache, not to re-ask on every page view.
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	_, _ = io.Copy(w, f)
}

func (h *Handlers) findPoster(provider string, id int64, size string) (string, bool) {
	stem := "poster/" + provider + "/" + strconv.FormatInt(id, 10) + "-" + size
	for _, ext := range []string{".jpg", ".webp", ".png"} {
		if h.artwork.Exists(stem + ext) {
			return stem + ext, true
		}
	}
	return "", false
}

// isSimpleName reports whether a provider name is the shape this software
// stores: lowercase letters and digits, nothing else.
func isSimpleName(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// pathID parses an id from the route, answering the client on failure.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeProblem(w, http.StatusBadRequest, "not an id")
		return 0, false
	}
	return id, true
}

// posterVisible reports whether the caller may see a title carrying this
// provider id.
func (h *Handlers) posterVisible(r *http.Request, tmdbID int64) bool {
	ok, err := h.media.HoldsProviderID(r.Context(), tmdbID)
	return err == nil && ok
}

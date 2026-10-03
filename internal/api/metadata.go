package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/metadata"
)

// The metadata provider's administration surface.
//
// Note what is absent: any way to READ the stored credential back. Not masked,
// not partially, not to an administrator. The same rule the indexer surface
// follows — a key that can be read over HTTP is one screenshot from disclosure,
// and nobody actually needs to read it. What an operator needs to know is
// whether one is set and whether it works, and both are here.

// MetadataService is what the API needs from the metadata subsystem.
type MetadataService interface {
	Status(ctx context.Context) (metadata.Status, error)
	SetToken(ctx context.Context, token string) (metadata.Health, error)
	Check(ctx context.Context) (metadata.Health, error)
	Search(ctx context.Context, q metadata.Query) ([]metadata.Match, error)
	Details(ctx context.Context, kind metadata.Kind, id int64) (metadata.Details, error)
}

func healthJSON(h metadata.Health) map[string]any {
	out := map[string]any{
		"ok": h.OK,
		// Always in words. "ok: false" tells an operator that something is
		// wrong and nothing about what to do, which is the state this whole
		// surface exists to get them out of.
		"detail": h.Detail,
	}
	if !h.CheckedAt.IsZero() {
		out["checked_at"] = h.CheckedAt
	}
	if h.ImageBase != "" {
		out["image_base"] = h.ImageBase
	}
	if len(h.Sizes) > 0 {
		out["poster_sizes"] = h.Sizes
	}
	return out
}

// MetadataStatus reports whether a provider is configured and working.
func (h *Handlers) MetadataStatus(w http.ResponseWriter, r *http.Request) {
	if h.metadata == nil {
		writeProblem(w, http.StatusNotImplemented, "no metadata subsystem is wired")
		return
	}
	st, err := h.metadata.Status(r.Context())
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	// An unconfigured instance reported {"ok":false,"detail":""} — which tells
	// an operator that something is wrong and nothing about what, the exact
	// pattern healthJSON's own comment complains about. Nothing IS wrong here:
	// no provider is a supported configuration.
	if !st.Configured && st.Health.Detail == "" {
		st.Health.Detail = "No metadata provider is configured. Everything else " +
			"works without one; titles are identified from release names alone."
	}
	// A key loaded at startup has not been asked anything yet. Reported as
	// "ok: false" with nothing to say, it read as a broken key on the admin
	// screen — found there, the first time the screen showed it. Not checked
	// is its own state, and says so.
	checked := !st.Health.CheckedAt.IsZero()
	if st.Configured && !checked && st.Health.Detail == "" {
		st.Health.Detail = "Stored, and not checked since the server started. " +
			"Check it to find out whether it still works."
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": st.Configured,
		"checked":    checked,
		"provider":   st.Provider,
		"health":     healthJSON(st.Health),
		"note": "The API key is never returned by this endpoint, in any form. " +
			"Send a new one to replace it, or an empty one to remove it.",
	})
}

type metadataTokenRequest struct {
	Token string `json:"token"`
}

// SetMetadataToken stores a credential, after proving it works.
func (h *Handlers) SetMetadataToken(w http.ResponseWriter, r *http.Request) {
	if h.metadata == nil {
		writeProblem(w, http.StatusNotImplemented, "no metadata subsystem is wired")
		return
	}
	var in metadataTokenRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	health, err := h.metadata.SetToken(r.Context(), strings.TrimSpace(in.Token))
	switch {
	case errors.Is(err, metadata.ErrUnauthorized),
		errors.Is(err, metadata.ErrUnexpectedShape),
		errors.Is(err, metadata.ErrNotFound):
		// 400: the credential the operator sent is the problem, and it was NOT
		// stored. Saying so matters — an operator who thinks a bad key was
		// saved goes looking for a way to remove it.
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":  err.Error(),
			"health": healthJSON(health),
			"note":   "Nothing was stored. The key this instance had, if any, is unchanged.",
		})
		return
	case errors.Is(err, metadata.ErrRateLimited):
		writeProblem(w, http.StatusServiceUnavailable,
			"the provider is rate-limiting this instance; try again shortly")
		return
	case errors.Is(err, metadata.ErrUnavailable):
		writeProblem(w, http.StatusBadGateway,
			"the provider could not be reached, so the key could not be verified "+
				"and was not stored: "+err.Error())
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"health": healthJSON(health),
		"note": "Stored and verified. It was checked against the provider before " +
			"being saved, so a key that does not work never reaches the database.",
	})
}

// CheckMetadata re-tests the configured provider.
func (h *Handlers) CheckMetadata(w http.ResponseWriter, r *http.Request) {
	if h.metadata == nil {
		writeProblem(w, http.StatusNotImplemented, "no metadata subsystem is wired")
		return
	}
	health, err := h.metadata.Check(r.Context())
	if errors.Is(err, metadata.ErrNoProvider) {
		writeProblem(w, http.StatusConflict, "no metadata provider is configured")
		return
	}
	if err != nil && !isProviderFailure(err) {
		writeAuthzAware(w, err)
		return
	}
	// A provider that is down is a successful REPORT of a failure, not a failed
	// request: the operator asked "is it working?" and got an answer.
	writeJSON(w, http.StatusOK, map[string]any{"health": healthJSON(health)})
}

func isProviderFailure(err error) bool {
	return errors.Is(err, metadata.ErrUnauthorized) ||
		errors.Is(err, metadata.ErrRateLimited) ||
		errors.Is(err, metadata.ErrUnavailable) ||
		errors.Is(err, metadata.ErrUnexpectedShape) ||
		errors.Is(err, metadata.ErrNotFound)
}

// SearchMetadata looks a title up with the configured provider.
func (h *Handlers) SearchMetadata(w http.ResponseWriter, r *http.Request) {
	if h.metadata == nil {
		writeProblem(w, http.StatusNotImplemented, "no metadata subsystem is wired")
		return
	}
	q := metadata.Query{
		Kind:  metadata.Kind(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))),
		Title: strings.TrimSpace(r.URL.Query().Get("title")),
	}
	if q.Kind != metadata.KindSeries {
		q.Kind = metadata.KindMovie
	}
	if y, err := strconv.Atoi(r.URL.Query().Get("year")); err == nil {
		q.Year = y
	}
	if q.Title == "" {
		writeProblem(w, http.StatusBadRequest, "a title to search for is required")
		return
	}

	matches, err := h.metadata.Search(r.Context(), q)
	switch {
	case errors.Is(err, metadata.ErrNoProvider):
		writeProblem(w, http.StatusConflict,
			"no metadata provider is configured; set one on the Metadata screen")
		return
	case isProviderFailure(err):
		writeProblem(w, http.StatusBadGateway, err.Error())
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	out := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		row := map[string]any{
			"provider_id": m.ProviderID,
			"kind":        string(m.Kind),
			"title":       m.Title,
			"overview":    m.Overview,
		}
		if m.Year > 0 {
			row["year"] = m.Year
		}
		// Shown because a release name often used the original title, so it is
		// what lets somebody recognise the right row.
		if m.OriginalTitle != "" && m.OriginalTitle != m.Title {
			row["original_title"] = m.OriginalTitle
		}
		if m.PosterPath != "" {
			row["poster_path"] = m.PosterPath
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"matches": out, "count": len(out)})
}

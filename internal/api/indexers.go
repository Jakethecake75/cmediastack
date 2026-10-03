package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// IndexerService is the subset of the indexer subsystem the API needs.
//
// Note what is NOT here: nothing that returns a decrypted API key. The store
// keeps List (no keys) and Enabled (keys) apart, and this interface exposes
// only the first. "Can this endpoint return an API key?" is then answered by
// which method it can reach, rather than by remembering to strip a field.
type IndexerService interface {
	List(ctx context.Context) ([]indexer.Definition, error)
	HealthOf(ctx context.Context) (map[int64]indexer.Health, error)
	Create(ctx context.Context, d indexer.Definition) (int64, error)
	Update(ctx context.Context, d indexer.Definition) error
	Delete(ctx context.Context, id int64) error
}

type indexerRequest struct {
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	BaseURL    string  `json:"base_url"`
	APIKey     string  `json:"api_key"`
	Categories []int   `json:"categories"`
	Enabled    bool    `json:"enabled"`
	Priority   int     `json:"priority"`
	SeedRatio  float64 `json:"seed_ratio"`
	SeedHours  int     `json:"seed_hours"`
	// Definition is a Cardigann indexer's pasted YAML (ADR-0058); empty on an
	// update keeps the stored one.
	Definition string `json:"definition"`
	// Settings are values for the definition's settings — a username, a
	// password, a cookie (ADR-0059). Sealed, never returned; empty on an
	// update keeps the stored ones.
	Settings map[string]string `json:"settings"`
}

func (in indexerRequest) toDefinition(id int64) indexer.Definition {
	return indexer.Definition{
		ID: id, Name: in.Name, Kind: indexer.Kind(in.Kind), BaseURL: in.BaseURL,
		APIKey: in.APIKey, Categories: in.Categories, Enabled: in.Enabled,
		Priority: in.Priority, SeedRatio: in.SeedRatio,
		SeedTime:  time.Duration(in.SeedHours) * time.Hour,
		Cardigann: in.Definition, Settings: in.Settings,
	}
}

// ListIndexers returns the configured indexers and their health.
func (h *Handlers) ListIndexers(w http.ResponseWriter, r *http.Request) {
	if h.indexers == nil {
		writeProblem(w, http.StatusNotImplemented, "no indexer store is wired")
		return
	}

	defs, err := h.indexers.List(r.Context())
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	health, err := h.indexers.HealthOf(r.Context())
	if err != nil {
		writeAuthzAware(w, err)
		return
	}

	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		entry := map[string]any{
			"id": d.ID, "name": d.Name, "kind": string(d.Kind),
			"base_url": d.BaseURL, "categories": d.Categories,
			"enabled": d.Enabled, "priority": d.Priority,
			"seed_ratio": d.SeedRatio, "seed_hours": int(d.SeedTime.Hours()),
			// Deliberately absent: api_key. It is not stripped here — it was
			// never decrypted.
			"has_api_key": true,
		}
		if d.Kind == indexer.KindCardigann {
			// Not a secret: the operator pasted it from a public repository.
			entry["definition"] = d.Cardigann
			// Whether settings are stored, and never what they are.
			entry["has_settings"] = d.HasSettings
		}
		if hh, ok := health[d.ID]; ok {
			entry["consecutive_failures"] = hh.ConsecutiveFailures
			entry["last_error"] = hh.LastError
			if hh.LastCheckedAt != nil {
				entry["last_checked_at"] = *hh.LastCheckedAt
			}
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"indexers": out, "count": len(out)})
}

// CreateIndexer adds an indexer.
func (h *Handlers) CreateIndexer(w http.ResponseWriter, r *http.Request) {
	if h.indexers == nil {
		writeProblem(w, http.StatusNotImplemented, "no indexer store is wired")
		return
	}
	var in indexerRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	def := in.toDefinition(0)
	if err := def.Validate(); err != nil {
		// Safe to report in full: the administrator typed this and can see it.
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}
	if def.Kind == indexer.KindCardigann && def.Cardigann == "" {
		writeProblem(w, http.StatusBadRequest, "a Cardigann indexer needs its definition: paste it from "+
			"Jackett's or Prowlarr's repository")
		return
	}

	id, err := h.indexers.Create(r.Context(), def)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	h.auditIndexer(r, audit.ActionIndexerCreated, id, def.Name)

	body := map[string]any{"id": id}
	if def.APIKey != "" {
		body["message"] = "the API key is sealed under this indexer's id and cannot be read back; " +
			"re-enter it to change it"
	}
	writeJSON(w, http.StatusCreated, body)
}

// UpdateIndexer changes an indexer.
//
// An absent api_key means "leave the stored one alone", which is what the form
// submits when the operator did not retype a secret they cannot see.
func (h *Handlers) UpdateIndexer(w http.ResponseWriter, r *http.Request) {
	if h.indexers == nil {
		writeProblem(w, http.StatusNotImplemented, "no indexer store is wired")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	var in indexerRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	def := in.toDefinition(id)
	if err := def.Validate(); err != nil {
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}

	switch err := h.indexers.Update(r.Context(), def); {
	case errors.Is(err, indexer.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	h.auditIndexer(r, audit.ActionIndexerUpdated, id, def.Name)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// DeleteIndexer removes an indexer.
func (h *Handlers) DeleteIndexer(w http.ResponseWriter, r *http.Request) {
	if h.indexers == nil {
		writeProblem(w, http.StatusNotImplemented, "no indexer store is wired")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}

	switch err := h.indexers.Delete(r.Context(), id); {
	case errors.Is(err, indexer.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	h.auditIndexer(r, audit.ActionIndexerDeleted, id, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handlers) auditIndexer(r *http.Request, action audit.Action, id int64, name string) {
	if h.audit == nil {
		return
	}
	p := authz.FromContext(r.Context())
	if p == nil {
		return
	}
	_ = h.audit.Write(r.Context(), audit.Event{
		ActorUserID: &p.UserID,
		ActorLabel:  p.Username,
		Action:      action,
		TargetKind:  "indexer",
		TargetID:    strconv.FormatInt(id, 10),
		SourceIP:    ClientIP(r.Context()),
		UserAgent:   r.UserAgent(),
		Detail:      name,
	})
}

package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/request"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// Discover (ADR-0043): what is popular, from the provider, shared by every
// account for six hours, with a Request beside each.

// DiscoverSource lists what is popular.
type DiscoverSource interface {
	Discover(ctx context.Context, section string) ([]metadata.Match, error)
}

// discoverTTL is how long one list is kept: the lists are the same for
// everybody, so they are asked for at most four times a day each.
const discoverTTL = 6 * time.Hour

type discoverCache struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]discoverEntry
}

type discoverEntry struct {
	at    time.Time
	items []metadata.Match
}

func (c *discoverCache) get(ctx context.Context, src DiscoverSource, section string) ([]metadata.Match, time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	if e, ok := c.entries[section]; ok && now().Sub(e.at) < discoverTTL {
		return e.items, e.at, nil
	}
	items, err := src.Discover(ctx, section)
	if err != nil {
		return nil, time.Time{}, err
	}
	if c.entries == nil {
		c.entries = map[string]discoverEntry{}
	}
	c.entries[section] = discoverEntry{at: now(), items: items}
	return items, now(), nil
}

// Discover answers one list, marked for the caller: what is already in the
// library they can see, and what they have asked for.
func (h *Handlers) Discover(w http.ResponseWriter, r *http.Request) {
	if h.discover == nil {
		writeProblem(w, http.StatusNotImplemented, "no metadata provider is wired")
		return
	}
	section := r.PathValue("section")
	if !slices.Contains(metadata.DiscoverSections, section) {
		writeProblem(w, http.StatusNotFound, "the sections are "+strings.Join(metadata.DiscoverSections, ", "))
		return
	}
	ctx := r.Context()
	p := authz.FromContext(ctx)
	if p != nil && p.RatingCeiling > 0 {
		writeJSON(w, http.StatusOK, map[string]any{"section": section, "items": []any{}, "count": 0,
			"note": "Discover is not shown to an account with a rating ceiling: the provider's lists " +
				"carry no rating, so they could show what this account may not see."})
		return
	}
	items, fetched, err := h.discoverCache.get(ctx, h.discover, section)
	switch {
	case errors.Is(err, metadata.ErrNoProvider), errors.Is(err, metadata.ErrNoDiscover):
		writeProblem(w, http.StatusConflict, "no metadata provider is configured; set one on the Metadata screen")
		return
	case err != nil:
		writeProblem(w, http.StatusBadGateway, "the metadata provider could not be asked: "+err.Error())
		return
	}

	// What the caller has asked for and is still waiting on, by kind, folded
	// title and year — as a request is matched (ADR-0017).
	requested := map[string]bool{}
	if h.requests != nil {
		if open, err := h.requests.List(ctx, []request.State{request.StatePending, request.StateApproved}, 500); err == nil {
			for _, q := range open {
				requested[discoverKey(string(q.Kind), q.Title, q.Year)] = true
			}
		}
	}
	out := make([]map[string]any, 0, len(items))
	for _, m := range items {
		kind := string(m.Kind)
		row := map[string]any{"provider_id": m.ProviderID, "kind": kind, "title": m.Title,
			"overview": firstSentences(m.Overview, 280)}
		if m.Year > 0 {
			row["year"] = m.Year
		}
		if h.media != nil {
			if in, err := h.media.HasTitle(ctx, kind, m.ProviderID); err == nil && in {
				row["in_library"] = true
			}
		}
		if requested[discoverKey(kind, m.Title, m.Year)] {
			row["requested"] = true
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"section": section, "items": out, "count": len(out),
		"fetched_at": fetched})
}

func discoverKey(kind, title string, year int) string {
	return kind + "|" + search.NormalizeTitle(title) + "|" + itoa(year)
}

// firstSentences shortens an overview to about n characters, at a word.
func firstSentences(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) <= n {
		return s
	}
	cut := string([]rune(s)[:n])
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/metadata"
)

type fakeDiscover struct {
	calls atomic.Int32
	items []metadata.Match
	err   error
}

func (f *fakeDiscover) Discover(context.Context, string) ([]metadata.Match, error) {
	f.calls.Add(1)
	return f.items, f.err
}

// ADR-0043, decisions 1 and 2.
func TestDiscoverIsSharedAndMarked(t *testing.T) {
	w := newScopeWorld(t)
	w.discover.items = []metadata.Match{
		{ProviderID: w.shownTMDB, Kind: metadata.KindMovie, Title: "Paddington", Year: 2014},
		{ProviderID: w.hiddenTMDB, Kind: metadata.KindMovie, Title: "Heat", Year: 1995},
		{ProviderID: 77, Kind: metadata.KindMovie, Title: "Dune: Part Two", Year: 2024,
			Overview: strings.Repeat("A long overview. ", 40)},
		// Same provider id as Paddington, but a series: not in the library.
		{ProviderID: w.shownTMDB, Kind: metadata.KindSeries, Title: "Paddington Bear", Year: 1976},
	}
	viewer, _ := w.scopedAccount("viewer", authz.RoleUser, []int64{w.kids}, 0)
	if res := viewer.post("/api/v1/requests", map[string]any{"kind": "movie", "title": "Dune - Part Two",
		"year": 2024}); res.Code/100 != 2 {
		t.Fatalf("request: %d %s", res.Code, res.Raw)
	}

	res := viewer.get("/api/v1/discover/trending")
	if res.Code != http.StatusOK || res.Body["count"] != float64(4) {
		t.Fatalf("discover: %d %s", res.Code, res.Raw)
	}
	rows := res.Body["items"].([]any)
	mark := func(i int, key string) bool { v, _ := rows[i].(map[string]any)[key].(bool); return v }
	if !mark(0, "in_library") || mark(1, "in_library") || mark(3, "in_library") {
		t.Errorf("in-library marks wrong (the hidden title must not show): %s", res.Raw)
	}
	if !mark(2, "requested") || mark(0, "requested") {
		t.Errorf("requested marks wrong: %s", res.Raw)
	}
	if o := rows[2].(map[string]any)["overview"].(string); len([]rune(o)) > 281 || !strings.HasSuffix(o, "…") {
		t.Errorf("the overview is not shortened: %d", len(o))
	}

	// Everybody shares one fetch.
	w.admin.get("/api/v1/discover/trending")
	w.admin.get("/api/v1/discover/popular-films")
	if n := w.discover.calls.Load(); n != 2 {
		t.Errorf("%d fetches for two sections, want one each", n)
	}
	if res := viewer.get("/api/v1/discover/everything"); res.Code != http.StatusNotFound {
		t.Errorf("an unknown section: %d", res.Code)
	}

	// Kept six hours, then fetched again.
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	cache := &discoverCache{now: func() time.Time { return now }}
	src := &fakeDiscover{}
	for _, advance := range []time.Duration{0, 5*time.Hour + 59*time.Minute, 2 * time.Minute} {
		now = now.Add(advance)
		if _, _, err := cache.get(t.Context(), src, "trending"); err != nil {
			t.Fatal(err)
		}
	}
	if n := src.calls.Load(); n != 2 {
		t.Errorf("%d fetches across six hours and a minute, want 2", n)
	}
}

// ADR-0043, decision 3, and no provider.
func TestDiscoverIsNotForACappedAccount(t *testing.T) {
	w := newScopeWorld(t)
	w.discover.items = []metadata.Match{{ProviderID: 1, Kind: metadata.KindMovie, Title: "Saw"}}
	res := w.kid.get("/api/v1/discover/trending")
	if res.Code != http.StatusOK || res.Body["count"] != float64(0) || !strings.Contains(res.Raw, "rating ceiling") {
		t.Errorf("a capped account: %d %s", res.Code, res.Raw)
	}
	if n := w.discover.calls.Load(); n != 0 {
		t.Errorf("a capped account's visit asked the provider %d times", n)
	}
	w.discover.err = metadata.ErrNoProvider
	if res := w.admin.get("/api/v1/discover/popular-series"); res.Code != http.StatusConflict {
		t.Errorf("no provider: %d %s", res.Code, res.Raw)
	}
	w.discover.err = errors.New("connection refused")
	if res := w.admin.get("/api/v1/discover/upcoming-films"); res.Code != http.StatusBadGateway {
		t.Errorf("an unreachable provider: %d %s", res.Code, res.Raw)
	}
}

package api

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/metadata"
)

// ADR-0069: Home's Continue watching is the account's own places, under its
// library scope.
func TestContinueWatchingListsMyPlacesAndOnlyWhatICanSee(t *testing.T) {
	w := newScopeWorld(t)
	for _, f := range []int64{w.shownFile, w.hiddenFile} {
		res := w.admin.do(http.MethodPut, "/api/v1/files/"+strconv.FormatInt(f, 10)+"/position",
			map[string]any{"position_ms": 600000, "duration_ms": 6000000})
		if res.Code != http.StatusOK {
			t.Fatalf("saving a place: %d %s", res.Code, res.Raw)
		}
	}
	res := w.admin.get("/api/v1/me/continue")
	items, _ := res.Body["items"].([]any)
	if res.Code != http.StatusOK || len(items) != 2 {
		t.Fatalf("admin's continue watching: %d %s", res.Code, res.Raw)
	}
	first := items[0].(map[string]any)
	if first["file_id"] == nil || first["item_id"] == nil || first["title"] == "" || first["position_ms"] != float64(600000) ||
		first["duration_ms"] != float64(6000000) {
		t.Errorf("an entry lacks what it is: %v", first)
	}
	for _, it := range items { // both titles here are identified, so both have artwork
		if m := it.(map[string]any); m["poster"] == nil {
			t.Errorf("an identified title has no poster: %v", m)
		}
	}

	// The kid's own place in a file it may see is offered; the admin's are not.
	if res := w.kid.do(http.MethodPut, "/api/v1/files/"+strconv.FormatInt(w.shownFile, 10)+"/position",
		map[string]any{"position_ms": 60000, "duration_ms": 6000000}); res.Code != http.StatusOK {
		t.Fatalf("kid saving a place: %d %s", res.Code, res.Raw)
	}
	res = w.kid.get("/api/v1/me/continue")
	kidItems, _ := res.Body["items"].([]any)
	if len(kidItems) != 1 || kidItems[0].(map[string]any)["position_ms"] != float64(60000) {
		t.Errorf("kid's continue watching = %s, want only its own place", res.Raw)
	}
}

// ADR-0069: an account that may only request can search the provider to
// request — and still may not use the library editor's search.
func TestARequesterSearchesToRequest(t *testing.T) {
	r := newRig(t)
	f := &fakeMetadata{matches: []metadata.Match{{ProviderID: 438631, Kind: metadata.KindMovie, Title: "Dune", Year: 2021}}}
	r.withMetadata(f)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	user := r.redeemAndEnroll(code, "sam", "user-passphrase-1")

	res := user.get("/api/v1/requests/search?kind=movie&title=dune")
	matches, _ := res.Body["matches"].([]any)
	if res.Code != http.StatusOK || len(matches) != 1 {
		t.Fatalf("a requester's search: %d %s", res.Code, res.Raw)
	}
	if res := user.get("/api/v1/metadata/search?kind=movie&title=dune"); res.Code == http.StatusOK {
		t.Errorf("a requester used the library editor's search: %d", res.Code)
	}
}

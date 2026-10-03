package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/request"
)

// ADR-0028 through the real router: an approved request is added to the
// library and linked to what was added, and it is fulfilled when a file of that
// item arrives — whichever grab brought it.

// requestRow finds one request in a listing by its title.
func requestRow(t *testing.T, res response, title string) map[string]any {
	t.Helper()
	rows, _ := res.Body["requests"].([]any)
	for _, row := range rows {
		if m, _ := row.(map[string]any); m["title"] == title {
			return m
		}
	}
	t.Fatalf("no request %q in %s", title, res.Raw)
	return nil
}

func itemName(row map[string]any) any {
	it, _ := row["item"].(map[string]any)
	return it["name"]
}

// linkedRig is an instance that can add films, an administrator, a user, and
// the user's approved request for Dune (2021).
func linkedRig(t *testing.T) (*rig, *client, *client, string) {
	t.Helper()
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.filmReady(t)
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")
	res := r.submit(sam, "movie", "Dune", 2021)
	id := uid(int64(res.Body["id"].(float64)))
	res = admin.post("/api/v1/requests/"+id+"/approve", nil)
	if res.Code != http.StatusOK || !strings.Contains(res.Body["message"].(string), "add it to the library from this request") {
		t.Fatalf("approve: %d %s", res.Code, res.Raw)
	}
	return r, admin, sam, id
}

func TestAnApprovedRequestIsAddedToTheLibraryAndFulfilledWhenItsFilmArrives(t *testing.T) {
	r, admin, sam, id := linkedRig(t)

	res := admin.post("/api/v1/media", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if res.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", res.Code, res.Raw)
	}
	item := int64(res.Body["item"].(map[string]any)["id"].(float64))

	res = admin.post("/api/v1/requests/"+id+"/item", map[string]any{"media_item_id": item})
	if res.Code != http.StatusOK {
		t.Fatalf("link: %d %s", res.Code, res.Raw)
	}
	if res.Body["state"] != "approved" || res.Body["media_item_id"] != float64(item) ||
		itemName(res.Body) != "Dune (2021)" ||
		!strings.HasPrefix(res.Body["message"].(string), "Linked to Dune (2021). The request is fulfilled when a file of it arrives") {
		t.Errorf("link answer: %s", res.Raw)
	}

	// The requester sees what it is linked to, and that it is still waiting.
	row := requestRow(t, sam.get("/api/v1/requests"), "Dune")
	if row["state"] != "approved" || itemName(row) != "Dune (2021)" {
		t.Errorf("the requester sees %v", row)
	}
	if _, ok := row["fulfilled_at"]; ok {
		t.Error("a linked request with nothing on disk says when it was fulfilled")
	}

	// A file of the film arrives through a grab that named no request — the
	// one a film's own search makes — and the import's hook is told what it
	// became, exactly as the importer tells it.
	if n, err := r.requests.FulfilFromImport(t.Context(),
		"0123456789abcdef0123456789abcdef01234567", item); err != nil || n != 1 {
		t.Fatalf("the import fulfilled %d request(s): %v", n, err)
	}
	row = requestRow(t, sam.get("/api/v1/requests"), "Dune")
	if row["state"] != "fulfilled" || itemName(row) != "Dune (2021)" || row["fulfilled_at"] == nil {
		t.Errorf("after the import the requester sees %v", row)
	}
}

// The title a request asked for is already in the library: the refusal says so
// in a word the screen can act on, and the request can be linked to it.
func TestARequestCanBeLinkedToATitleAlreadyInTheLibrary(t *testing.T) {
	_, admin, _, id := linkedRig(t)
	first := admin.post("/api/v1/media", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if first.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", first.Code, first.Raw)
	}
	again := admin.post("/api/v1/media", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if again.Code != http.StatusConflict || again.Body["conflict"] != "already_in_library" {
		t.Fatalf("a second add: %d %s", again.Code, again.Raw)
	}
	existing := again.Body["item"].(map[string]any)["id"]
	if res := admin.post("/api/v1/requests/"+id+"/item", map[string]any{"media_item_id": existing}); res.Code != http.StatusOK {
		t.Errorf("linking to the title already there: %d %s", res.Code, res.Raw)
	}
}

// An item that merely occupies the folder the add wanted is a different
// conflict, and says so: the screen must never offer to link a request to it.
func TestAnAddRefusedForItsFolderSaysSoInAWord(t *testing.T) {
	r, admin, _, _ := linkedRig(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := r.database.ExecContext(t.Context(), `
		INSERT INTO media_item (kind, title, year, sort_title, root_folder_id, folder, added_at, updated_at)
		SELECT 'movie', 'Something a scan found', 2021, 'something', id, 'Dune (2021)', ?, ?
		FROM root_folder WHERE kind = 'movies'`, now, now); err != nil {
		t.Fatal(err)
	}
	res := admin.post("/api/v1/media", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if res.Code != http.StatusConflict || res.Body["conflict"] != "folder_taken" {
		t.Errorf("an add into an occupied folder: %d %s", res.Code, res.Raw)
	}
}

// Linking is deciding, and the requester may not.
func TestLinkingARequestIsTheApproversAlone(t *testing.T) {
	_, admin, sam, id := linkedRig(t)
	res := admin.post("/api/v1/media", map[string]any{"kind": "movie", "tmdb_id": 438631})
	item := res.Body["item"].(map[string]any)["id"]

	if res := sam.post("/api/v1/requests/"+id+"/item", map[string]any{"media_item_id": item}); res.Code != http.StatusForbidden {
		t.Errorf("the requester linked their own request: %d %s", res.Code, res.Raw)
	}
	if row := requestRow(t, sam.get("/api/v1/requests"), "Dune"); row["media_item_id"] != nil {
		t.Errorf("a refused link changed the request: %v", row)
	}
}

// Everything that cannot be right is refused, with an answer that says which.
func TestALinkThatCannotBeRightIsRefused(t *testing.T) {
	r, admin, sam, id := linkedRig(t)
	res := admin.post("/api/v1/media", map[string]any{"kind": "movie", "tmdb_id": 438631})
	film := int64(res.Body["item"].(map[string]any)["id"].(float64))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	sr, err := r.database.ExecContext(t.Context(), `
		INSERT INTO media_item (kind, title, year, sort_title, root_folder_id, folder, added_at, updated_at)
		SELECT 'series', 'Severance', 2022, 'severance', id, 'Severance (2022)', ?, ?
		FROM root_folder WHERE kind = 'movies'`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	series, _ := sr.LastInsertId()
	pending := uid(int64(r.submit(sam, "movie", "Arrival", 2016).Body["id"].(float64)))

	for _, c := range []struct {
		name, path string
		body       any
		code       int
		says       string
	}{
		{"a pending request", "/api/v1/requests/" + pending + "/item", map[string]any{"media_item_id": film},
			http.StatusConflict, "this one is pending"},
		{"a series for a film", "/api/v1/requests/" + id + "/item", map[string]any{"media_item_id": series},
			http.StatusBadRequest, "this is a film request, and item"},
		{"no such item", "/api/v1/requests/" + id + "/item", map[string]any{"media_item_id": 9999},
			http.StatusUnprocessableEntity, "there is no library item 9999"},
		{"no item at all", "/api/v1/requests/" + id + "/item", map[string]any{},
			http.StatusBadRequest, "media_item_id is required"},
		{"a field it does not take", "/api/v1/requests/" + id + "/item",
			map[string]any{"media_item_id": film, "state": "fulfilled"}, http.StatusBadRequest, ""},
		{"no such request", "/api/v1/requests/9999/item", map[string]any{"media_item_id": film},
			http.StatusNotFound, ""},
	} {
		res := admin.post(c.path, c.body)
		if res.Code != c.code || !strings.Contains(res.Raw, c.says) {
			t.Errorf("%s: %d %s, want %d saying %q", c.name, res.Code, res.Raw, c.code, c.says)
		}
	}
	if row := requestRow(t, sam.get("/api/v1/requests"), "Dune"); row["media_item_id"] != nil || row["state"] != "approved" {
		t.Errorf("a refused link changed the request: %v", row)
	}
}

// The name of the item is a read of the library: it is given to somebody who
// may browse, and to nobody else — the id alone says a link exists.
func TestARequestNamesItsItemOnlyToSomebodyWhoMayBrowse(t *testing.T) {
	r, admin, _, _ := linkedRig(t)
	res := admin.post("/api/v1/media", map[string]any{"kind": "movie", "tmdb_id": 438631})
	item := int64(res.Body["item"].(map[string]any)["id"].(float64))
	rq := &request.Request{ID: 1, Kind: request.KindMovie, Title: "Dune", State: request.StateApproved,
		MediaItemID: &item}
	h := &Handlers{media: r.media}

	for _, c := range []struct {
		name  string
		perms []authz.Permission
		named bool
	}{
		{"may browse", []authz.Permission{authz.PermSubmitRequest, authz.PermBrowse}, true},
		{"may only ask", []authz.Permission{authz.PermSubmitRequest}, false},
	} {
		actor := &authz.Principal{UserID: 2, Username: "sam", State: authz.StateActive,
			Role: authz.Role{Name: "custom", Permissions: authz.NewPermissionSet(c.perms...)}}
		body := requestJSON(rq, actor)
		withItem(t.Context(), body, rq, actor, h.itemNames())
		if _, named := body["item"]; named != c.named {
			t.Errorf("%s: item named = %v, want %v (%v)", c.name, named, c.named, body["item"])
		}
		if body["media_item_id"] != item {
			t.Errorf("%s: media_item_id = %v", c.name, body["media_item_id"])
		}
	}
}

// An approver who may not browse the library links by id, and is told the id
// back rather than the title: a link must not be a way to read the library
// one number at a time. (No built-in role approves without browsing; a custom
// one could.)
func TestALinkNamesTheItemOnlyToAnApproverWhoMayBrowse(t *testing.T) {
	r, admin, _, id := linkedRig(t)
	res := admin.post("/api/v1/media", map[string]any{"kind": "movie", "tmdb_id": 438631})
	dune := int64(res.Body["item"].(map[string]any)["id"].(float64))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	other, err := r.database.ExecContext(t.Context(), `
		INSERT INTO media_item (kind, title, year, sort_title, root_folder_id, folder, added_at, updated_at)
		SELECT 'movie', 'Dune', 1984, 'dune', id, 'Dune (1984)', ?, ?
		FROM root_folder WHERE kind = 'movies'`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	old, _ := other.LastInsertId()

	h := &Handlers{requests: r.requests, media: r.media}
	link := func(perms []authz.Permission, item int64) response {
		t.Helper()
		ctx := authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 1, Username: "jacob",
			State: authz.StateActive, MFASatisfied: true, UnrestrictedLibraries: true,
			Role: authz.Role{Name: "custom", Permissions: authz.NewPermissionSet(perms...)}})
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/requests/"+id+"/item",
			strings.NewReader(`{"media_item_id":`+uid(item)+`}`))
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		h.LinkRequest(rec, req)
		out := response{Code: rec.Code, Raw: rec.Body.String()}
		_ = json.Unmarshal(rec.Body.Bytes(), &out.Body)
		return out
	}
	blind := []authz.Permission{authz.PermApproveRequests}
	sighted := []authz.Permission{authz.PermApproveRequests, authz.PermBrowse}

	res = link(blind, old)
	if res.Code != http.StatusOK || !strings.HasPrefix(res.Body["message"].(string), "Linked to library item "+uid(old)+". ") {
		t.Errorf("without browse: %d %s", res.Code, res.Raw)
	}
	res = link(blind, dune)
	if msg := res.Body["message"].(string); strings.Contains(msg, "Dune (") ||
		!strings.HasSuffix(msg, " It was linked to library item "+uid(old)+" before.") {
		t.Errorf("a relink without browse says %q", msg)
	}
	if _, named := res.Body["item"]; named {
		t.Errorf("the answer named the item to an approver who may not browse: %s", res.Raw)
	}
	res = link(sighted, old)
	if msg := res.Body["message"].(string); !strings.HasPrefix(msg, "Linked to Dune (1984). ") ||
		!strings.HasSuffix(msg, " It was linked to Dune (2021) before.") {
		t.Errorf("with browse the relink says %q", msg)
	}
}

package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/request"
)

// ADR-0075 through the real router: approving a request for a title the
// provider named adds it, and approving a removal sends it to the trash.

func requestID(t *testing.T, res response) string {
	t.Helper()
	if res.Code != http.StatusCreated && res.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", res.Code, res.Raw)
	}
	return uid(int64(res.Body["id"].(float64)))
}

func TestApprovingARequestAddsItsFilmAndStartsTheSearch(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	var started []string
	r.filmReadyWith(t, func(task string) bool { started = append(started, task); return true })
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")

	id := requestID(t, sam.post("/api/v1/requests", map[string]any{
		"kind": "movie", "title": "Dune", "year": 2021, "tmdb_id": 438631}))
	res := admin.post("/api/v1/requests/"+id+"/approve", nil)
	if res.Code != http.StatusOK || !strings.HasPrefix(res.Body["message"].(string), "Approved and added to the library.") {
		t.Fatalf("approve: %d %s", res.Code, res.Raw)
	}
	if itemName(res.Body) != "Dune (2021)" || len(started) != 1 {
		t.Errorf("approve linked %v and started %v searches: %s", itemName(res.Body), started, res.Raw)
	}
	if row := requestRow(t, sam.get("/api/v1/requests"), "Dune"); row["media_item_id"] == nil || row["tmdb_id"] != float64(438631) {
		t.Errorf("the requester sees %v", row)
	}
}

func TestApprovingPartOfASeriesWantsOnlyThatPart(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	episodes := r.addable(t, &cannedSeries{}, true)
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")

	id := requestID(t, sam.post("/api/v1/requests", map[string]any{
		"kind": "series", "title": "Severance", "year": 2022, "tmdb_id": 95396,
		"scope": []map[string]int{{"season": 1, "episode": 2}}}))
	if res := admin.post("/api/v1/requests/"+id+"/approve", nil); res.Code != http.StatusOK ||
		!strings.HasPrefix(res.Body["message"].(string), "Approved and added") {
		t.Fatalf("approve: %d %s", res.Code, res.Raw)
	}
	item := r.onlyItem(t)
	_, eps, err := episodes.Seasons(r.adminCtx(t), item)
	if err != nil {
		t.Fatal(err)
	}
	for season, list := range eps {
		for _, e := range list {
			want := season == 1 && e.Number == 2
			if e.Monitored != want {
				t.Errorf("S%dE%d monitored = %v, want %v", season, e.Number, e.Monitored, want)
			}
		}
	}

	// Somebody else asking for all of it is a different request, and
	// approving it wants every regular season of the title already there.
	all := requestID(t, sam.post("/api/v1/requests", map[string]any{
		"kind": "series", "title": "Severance", "year": 2022, "tmdb_id": 95396}))
	if all == id {
		t.Fatal("the whole series joined the request for one episode")
	}
	if res := admin.post("/api/v1/requests/"+all+"/approve", nil); res.Code != http.StatusOK ||
		!strings.HasPrefix(res.Body["message"].(string), "Approved. It was already in the library.") {
		t.Fatalf("approve all: %d %s", res.Code, res.Raw)
	}
	_, eps, _ = episodes.Seasons(r.adminCtx(t), item)
	for season, list := range eps {
		for _, e := range list {
			if !e.Monitored {
				t.Errorf("S%dE%d is not wanted after the whole series was approved", season, e.Number)
			}
		}
	}
}

func TestARemovalIsAskedForAndOnlyADeleterApprovesIt(t *testing.T) {
	r, admin, sam, _ := linkedRig(t)
	if res := admin.post("/api/v1/media", map[string]any{"kind": "movie", "tmdb_id": 438631}); res.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", res.Code, res.Raw)
	}
	item := r.onlyItem(t)

	res := sam.post("/api/v1/requests", map[string]any{"action": "remove", "media_item_id": item, "title": "anything"})
	id := requestID(t, res)
	if res.Body["action"] != "remove" || res.Body["title"] != "Dune" || res.Body["state"] != "pending" {
		t.Fatalf("a removal request reads %s", res.Raw)
	}

	// A Manager may approve requests and may not delete files.
	code, _ := r.issueInvite(admin, authz.RoleManager, true)
	mo := r.redeemAndEnroll(code, "mo", "manager-passphrase-1")
	if res := mo.post("/api/v1/requests/"+id+"/approve", nil); res.Code != http.StatusForbidden {
		t.Fatalf("a manager approved a removal: %d %s", res.Code, res.Raw)
	}
	if row := requestRow(t, sam.get("/api/v1/requests"), "Dune"); row["action"] == "remove" && row["state"] != "pending" {
		t.Errorf("the refused approval changed the removal: %v", row)
	}

	res = admin.post("/api/v1/requests/"+id+"/approve", nil)
	if res.Code != http.StatusOK || res.Body["state"] != "fulfilled" ||
		!strings.HasPrefix(res.Body["message"].(string), "Removed: Dune is out of the library") {
		t.Fatalf("approve the removal: %d %s", res.Code, res.Raw)
	}
	if _, err := r.media.GetItem(r.adminCtx(t), item); err == nil {
		t.Error("the film is still in the library after its removal was approved")
	}
}

// A removal of part of a series takes only that part's files, and stops
// wanting it so nothing fetches it back.
func TestARemovalOfPartOfASeriesTakesThoseFilesAndStopsWantingThem(t *testing.T) {
	if !fileInParts(fileOf(1, 2, 3), []request.Part{{Season: 1, Episode: 3}}) ||
		!fileInParts(fileOf(1, 2, 0), []request.Part{{Season: 1}}) ||
		fileInParts(fileOf(2, 2, 0), []request.Part{{Season: 1}}) ||
		fileInParts(fileOf(1, 2, 0), []request.Part{{Season: 1, Episode: 3}}) ||
		fileInParts(importer.File{}, []request.Part{{Season: 1}}) {
		t.Error("fileInParts chose the wrong files")
	}

	r := newRig(t)
	admin := r.bootstrapAdmin()
	episodes := r.addable(t, &cannedSeries{}, true)
	if res := admin.post("/api/v1/media", map[string]any{"kind": "series", "tmdb_id": 95396, "monitor": "all"}); res.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", res.Code, res.Raw)
	}
	item := r.onlyItem(t)
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")
	id := requestID(t, sam.post("/api/v1/requests", map[string]any{"action": "remove", "media_item_id": item,
		"scope": []map[string]int{{"season": 1}}}))
	if res := admin.post("/api/v1/requests/"+id+"/approve", nil); res.Code != http.StatusOK || res.Body["state"] != "fulfilled" {
		t.Fatalf("approve: %d %s", res.Code, res.Raw)
	}
	_, eps, _ := episodes.Seasons(r.adminCtx(t), item)
	for season, list := range eps {
		for _, e := range list {
			if e.Monitored != (season != 1) {
				t.Errorf("S%dE%d monitored = %v after season 1 was removed", season, e.Number, e.Monitored)
			}
		}
	}
}

func fileOf(season, episode, last int) importer.File {
	return importer.File{Season: &season, Episode: &episode, EpisodeLast: &last}
}

// The Downloads page keeps every row where it was between reads (ADR-0075).
func TestTheQueueIsNewestFirstAndStable(t *testing.T) {
	at := func(m int) *time.Time { v := time.Date(2026, 10, 5, 0, m, 0, 0, time.UTC); return &v }
	items := []queueItem{{InfoHash: "b", AddedAt: at(1)}, {InfoHash: "u"}, {InfoHash: "c", AddedAt: at(2)}, {InfoHash: "a", AddedAt: at(1)}}
	sortQueue(items)
	var got string
	for _, it := range items {
		got += it.InfoHash
	}
	if got != "cabu" {
		t.Errorf("order = %s, want cabu", got)
	}
}

func (r *rig) onlyItem(t *testing.T) int64 {
	t.Helper()
	var id int64
	if err := r.database.QueryRowContext(t.Context(), `SELECT id FROM media_item`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (r *rig) adminCtx(t *testing.T) context.Context {
	return authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
		UnrestrictedLibraries: true,
		Role: authz.Role{ID: 1, Name: "Admin", Rank: 100,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
	})
}

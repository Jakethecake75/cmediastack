package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/books"
	"github.com/jakethecake75/cmediastack/internal/download"
)

type fakeBookCatalogue struct{}

var leftHand = books.Work{ID: "OL59800W", Title: "The Left Hand of Darkness",
	Authors: []string{"Ursula K. Le Guin"}, Year: 1969, Editions: 91}

func (fakeBookCatalogue) SearchBooks(context.Context, string) ([]books.Work, error) {
	return []books.Work{leftHand}, nil
}
func (fakeBookCatalogue) Work(_ context.Context, id string) (books.Work, error) {
	if id == leftHand.ID {
		return leftHand, nil
	}
	return books.Work{}, books.ErrNotFound
}

// ADR-0048: a book is searched for, added, wanted, unmonitored, and scoped,
// through the real router.
func TestABookIsAddedAndWanted(t *testing.T) {
	w := newScopeWorld(t)
	dir := filepath.Join(t.TempDir(), "books")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if res := w.admin.post("/api/v1/media", map[string]any{"kind": "book", "openlibrary_id": "OL59800W"}); res.Code != http.StatusConflict ||
		!strings.Contains(res.Raw, "no root folder for books") {
		t.Errorf("no books root: %d %s", res.Code, res.Raw)
	}
	root := w.admin.post("/api/v1/admin/rootfolders", map[string]any{"path": dir, "kind": "books", "label": "Books"})
	if root.Code != http.StatusCreated {
		t.Fatalf("root: %d %s", root.Code, root.Raw)
	}

	res := w.admin.get("/api/v1/books/search?q=left+hand")
	if res.Code != http.StatusOK || !strings.Contains(res.Raw, `"openlibrary_id":"OL59800W"`) ||
		!strings.Contains(res.Raw, `"year":1969`) {
		t.Fatalf("search: %d %s", res.Code, res.Raw)
	}
	if res := w.admin.get("/api/v1/books/search?q="); res.Code != http.StatusBadRequest {
		t.Errorf("an empty search: %d", res.Code)
	}
	for _, bad := range []map[string]any{
		{"kind": "book", "openlibrary_id": "OL59800W", "monitor": "all"},
		{"kind": "book", "openlibrary_id": "nope"},
		{"kind": "book", "openlibrary_id": "OL404W"},
	} {
		if res := w.admin.post("/api/v1/media", bad); res.Code/100 != 4 {
			t.Errorf("%v: %d %s", bad, res.Code, res.Raw)
		}
	}
	added := w.admin.post("/api/v1/media", map[string]any{"kind": "book", "openlibrary_id": "OL59800W"})
	if added.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", added.Code, added.Raw)
	}
	item := added.Body["item"].(map[string]any)
	if item["author"] != "Ursula K. Le Guin" || item["monitored"] != true || item["kind"] != "book" ||
		added.Body["name"] != "The Left Hand of Darkness (1969) — Ursula K. Le Guin" {
		t.Errorf("added %s", added.Raw)
	}
	if _, rated := item["rating"]; rated {
		t.Errorf("a book was given a rating, which no ceiling applies to: %v", item["rating"])
	}
	id := strconv.FormatInt(int64(item["id"].(float64)), 10)
	if res := w.admin.post("/api/v1/media", map[string]any{"kind": "book", "openlibrary_id": "OL59800W"}); res.Code != http.StatusConflict {
		t.Errorf("a second add: %d", res.Code)
	}

	wanted := w.admin.get("/api/v1/wanted")
	if wanted.Body["book_count"] != float64(1) || !strings.Contains(wanted.Raw, `"author":"Ursula K. Le Guin"`) {
		t.Errorf("wanted: %s", wanted.Raw)
	}
	// Out of the kid's scope: not on their list, and absent when asked for.
	if res := w.kid.get("/api/v1/wanted"); res.Body["book_count"] != float64(0) {
		t.Errorf("the kid's wanted books: %s", res.Raw)
	}
	if res := w.kid.get("/api/v1/media/" + id); res.Code != http.StatusNotFound {
		t.Errorf("the kid read a book out of scope: %d", res.Code)
	}
	viewer, _ := w.scopedAccount("reader", authz.RoleUser, []int64{int64(root.Body["id"].(float64))}, 0)
	if res := viewer.get("/api/v1/books/search?q=x"); res.Code != http.StatusForbidden {
		t.Errorf("a User asked Open Library: %d", res.Code)
	}
	if res := viewer.get("/api/v1/media/" + id); res.Code != http.StatusOK {
		t.Errorf("a reader with the Books library: %d", res.Code)
	}

	if res := w.admin.do(http.MethodPut, "/api/v1/media/"+id+"/monitored", map[string]any{"monitored": false}); res.Code != http.StatusOK {
		t.Fatalf("unmonitor: %d %s", res.Code, res.Raw)
	}
	if res := w.admin.get("/api/v1/wanted"); res.Body["book_count"] != float64(0) {
		t.Errorf("an unmonitored book is still wanted: %s", res.Raw)
	}
}

const bookFeed = `<?xml version="1.0"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
<channel>
<item><title>Ursula K. Le Guin - The Left Hand of Darkness [PDF]</title>
<enclosure url="https://indexer.example.com/dl/pdf.torrent" length="5000000"/>
<torznab:attr name="seeders" value="900"/></item>
<item><title>Ursula K. Le Guin - The Left Hand of Darkness (1969) [EPUB]</title>
<enclosure url="https://indexer.example.com/dl/epub.torrent" length="1000000"/>
<torznab:attr name="seeders" value="3"/></item>
<item><title>Harold Bloom - Ursula K. Le Guin's The Left Hand of Darkness [EPUB]</title>
<enclosure url="https://indexer.example.com/dl/bloom.torrent" length="1000000"/>
<torznab:attr name="seeders" value="50"/></item>
</channel></rss>`

// ADR-0049, decision 5: a book is searched for by the title's own route, the
// book alone carries tickets, the best format first, and the grab hands the
// queue the book.
func TestABookSearchGrabsIntoThatBook(t *testing.T) {
	w := newScopeWorld(t)
	dir := filepath.Join(t.TempDir(), "books")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if res := w.admin.post("/api/v1/admin/rootfolders", map[string]any{"path": dir, "kind": "books", "label": "Books"}); res.Code != http.StatusCreated {
		t.Fatalf("root: %d %s", res.Code, res.Raw)
	}
	w.r.addIndexer(w.admin, "Tracker")
	added := w.admin.post("/api/v1/media", map[string]any{"kind": "book", "openlibrary_id": "OL59800W"})
	if added.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", added.Code, added.Raw)
	}
	id := int64(added.Body["item"].(map[string]any)["id"].(float64))
	path := "/api/v1/media/" + strconv.FormatInt(id, 10) + "/search"

	res := w.admin.post(path, map[string]any{})
	if res.Code != http.StatusOK {
		t.Fatalf("search: %d %s", res.Code, res.Raw)
	}
	if len(w.asked) != 1 || w.asked[0] != "search guin the left hand of darkness 7000" {
		t.Errorf("asked %q", w.asked)
	}
	cands := res.Body["candidates"].([]any)
	if len(cands) != 3 || res.Body["matches"] != float64(2) {
		t.Fatalf("%d candidates: %s", len(cands), res.Raw)
	}
	first := cands[0].(map[string]any)
	if first["quality"] != "EPUB" || first["ticket"] == nil {
		t.Errorf("the EPUB does not lead: %v", first)
	}
	if last := cands[2].(map[string]any); last["ticket"] != nil || last["rejection_reason"] != "not_this_book" {
		t.Errorf("Bloom's study: %v", last)
	}
	grab := w.admin.post("/api/v1/releases/grab", map[string]any{"ticket": first["ticket"]})
	if grab.Code != http.StatusAccepted {
		t.Fatalf("grab: %d %s", grab.Code, grab.Raw)
	}
	if f, _ := grab.Body["for"].(map[string]any); f == nil || f["kind"] != "book" ||
		f["label"] != "The Left Hand of Darkness (1969)" {
		t.Errorf("the grab says it is for %v", grab.Body["for"])
	}
	w.r.downloads.mu.Lock()
	defer w.r.downloads.mu.Unlock()
	if got := w.r.downloads.meta[0].Target; got == nil || *got != (download.Target{ItemID: id, Book: true}) {
		t.Errorf("the queue was handed %+v", got)
	}
}

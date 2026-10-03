package acquire

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// addBook records a book, with an author or without one.
func (r *rig) addBook(title, author string, year int) int64 {
	r.t.Helper()
	r.folders++
	var a, y any
	if author != "" {
		a = author
	}
	if year > 0 {
		y = year
	}
	res, err := r.database.ExecContext(context.Background(), `
		INSERT INTO media_item (kind, title, year, sort_title, root_folder_id, folder, author, added_at, updated_at)
		VALUES ('book', ?, ?, ?, 2, ?, ?, ?, ?)`, title, y, strings.ToLower(title),
		fmt.Sprintf("%s %d", title, r.folders), a,
		r.clock.now().Format(time.RFC3339Nano), r.clock.now().Format(time.RFC3339Nano))
	if err != nil {
		r.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (r *rig) runBooks() string {
	r.t.Helper()
	s, err := r.svc.RunBooks(r.ctx)
	if err != nil {
		r.t.Fatalf("book pass: %v", err)
	}
	return s
}

// ADR-0050, decisions 1, 2 and 4.
func TestAWantedBookIsFetched(t *testing.T) {
	r := newRig(t, Config{})
	dune := r.addBook("Dune", "Frank Herbert", 1965)
	r.addBook("Anonymous Pamphlet", "", 0) // no author: never searched for
	held := r.addBook("Dune Messiah", "Frank Herbert", 1969)
	r.exec(`INSERT INTO media_file (item_id, root_folder_id, relative_path, imported_at) VALUES (?, 2, 'm.epub', 'x')`, held)
	off := r.addBook("Children of Dune", "Frank Herbert", 1976)
	r.exec(`UPDATE media_item SET monitored = 0 WHERE id = ?`, off)
	film := r.addFilm("Dune", 2021, 438631)
	r.client.byTerm["herbert dune"] = []indexer.Result{
		rel("Frank Herbert - Dune [PDF]", 1, 900),
		rel("Frank Herbert - Dune Messiah [EPUB]", 2, 800),
		rel("Frank Herbert - Dune (1965) [EPUB]", 3, 5),
		rel("Frank Herbert - Dune (1965)", 4, 50),
	}

	wanted, err := r.store.WantedBooks(r.ctx)
	if err != nil || len(wanted) != 1 || wanted[0].ItemID != dune || wanted[0].Author != "Frank Herbert" {
		t.Fatalf("wanted %+v %v", wanted, err)
	}
	summary := r.runBooks()
	added := r.queue.added()
	if len(added) != 1 || added[0].Target == nil || *added[0].Target != (download.Target{ItemID: dune, Book: true}) ||
		added[0].Title != "Frank Herbert - Dune (1965) [EPUB]" {
		t.Fatalf("grabbed %+v: %s", added, summary)
	}
	if !strings.Contains(summary, "grabbed Dune (1965) by Frank Herbert") {
		t.Errorf("summary %q", summary)
	}
	queries, _ := r.client.snapshot()
	if len(queries) != 1 || queries[0].Categories[0] != 7000 {
		t.Errorf("asked %+v", queries)
	}
	if st, ok := r.state(StateKey{Book: true, ID: dune}); !ok || st.Outcome != OutcomeGrabbed {
		t.Errorf("the book's state %+v %v", st, ok)
	}
	// A film's state on an item is still a film's.
	if err := r.store.Record(r.ctx, Want{Film: true, ItemID: film, Title: "Dune", Year: 2021},
		State{Outcome: OutcomeNothing, Detail: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.state(StateKey{Film: true, ID: film}); !ok {
		t.Error("a film's state was read as something else")
	}
	if _, ok := r.state(StateKey{Film: true, ID: dune}); ok {
		t.Error("a book's state was read as a film's")
	}

	// Downloading: not searched again, and in flight as the book — not as an
	// episode of its item.
	r.client.reset()
	if s := r.runBooks(); !strings.Contains(s, "1 downloading") || strings.Contains(s, "imported once") {
		t.Errorf("while downloading: %s", s)
	}
	inFlight, err := r.store.InFlight(r.ctx)
	if err != nil || !inFlight[Key{Book: true, ItemID: dune}] || inFlight[Key{ItemID: dune}] {
		t.Errorf("in flight %v %v", inFlight, err)
	}

	// Held: no longer wanted.
	r.exec(`INSERT INTO media_file (item_id, root_folder_id, relative_path, imported_at) VALUES (?, 2, 'd.epub', 'x')`, dune)
	if s := r.runBooks(); !strings.Contains(s, "no book is wanted") {
		t.Errorf("after the file arrived: %s", s)
	}
}

// ADR-0050, decision 3: a book whose download brought nothing is looked for
// again once its back-off is over, and the release that was tried is not.
func TestABookWhoseDownloadBroughtNothingIsLookedForAgain(t *testing.T) {
	r := newRig(t, Config{})
	dune := r.addBook("Dune", "Frank Herbert", 1965)
	r.client.byTerm["herbert dune"] = []indexer.Result{rel("Frank Herbert - Dune [EPUB]", 1, 5)}
	r.runBooks()
	if err := r.queueDB.SetStatus(context.Background(), hash(1), download.StatusComplete); err != nil {
		t.Fatal(err)
	}
	r.imported(1, "skipped")
	r.clock.advance(time.Hour)
	r.client.reset()
	r.client.byTerm["herbert dune"] = []indexer.Result{
		rel("Frank Herbert - Dune [EPUB]", 1, 5),
		rel("Dune by Frank Herbert (retail) epub", 2, 9),
	}
	s := r.runBooks()
	added := r.queue.added()
	if len(added) != 2 || added[1].Title != "Dune by Frank Herbert (retail) epub" {
		t.Errorf("grabbed %d: %s", len(added), s)
	}
	if st, _ := r.state(StateKey{Book: true, ID: dune}); st.Outcome != OutcomeGrabbed {
		t.Errorf("state %+v", st)
	}
}

// misjudgingBooks seals an accepted release to the book's item without saying
// it is a book — a shape no real search makes, and none a grab may act on.
type misjudgingBooks struct{ Finder }

func (m misjudgingBooks) SearchBook(ctx context.Context, bs search.BookSearch) (search.Response, error) {
	resp, err := m.Finder.SearchBook(ctx, bs)
	for i := range resp.Candidates {
		c := &resp.Candidates[i]
		c.Accepted, c.Rejection = true, nil
		c.Target = &search.Target{ItemID: bs.Want.ItemID}
	}
	return resp, err
}

// ADR-0050, decision 1: only a release sealed to the book as a book is grabbed
// for it, whatever the search says.
func TestOnlyTheBooksOwnReleaseIsGrabbed(t *testing.T) {
	r := newRig(t, Config{})
	r.svc.finder = misjudgingBooks{r.svc.finder}
	r.addBook("Dune", "Frank Herbert", 1965)
	r.client.byTerm["herbert dune"] = []indexer.Result{rel("Frank Herbert - Dune [EPUB]", 1, 5)}
	if s := r.runBooks(); len(r.queue.added()) != 0 {
		t.Errorf("grabbed %d: %s", len(r.queue.added()), s)
	}
}

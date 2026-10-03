package search

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/indexer"
)

func dune() BookWant { return BookWant{ItemID: 9, Title: "Dune", Author: "Frank Herbert"} }

// ADR-0049, decision 4.
func TestAReleaseIsJudgedAgainstTheBook(t *testing.T) {
	for name, ok := range map[string]bool{
		"Frank Herbert - Dune (1965) [EPUB]":                          true,
		"Dune by Frank Herbert epub":                                  true,
		"Herbert, Frank - Dune.azw3":                                  true,
		"Frank.Herbert.-.Dune.1965.RETAIL.EPUB.eBook-BitBook":         true,
		"Dune (Dune Chronicles, Book 1) - Frank Herbert [mobi]":       true,
		"DUNE - HERBERT, FRANK (pdf)":                                 true,
		"Frank Herbert - Dune Messiah (1969) [EPUB]":                  false,
		"Frank Herbert - Dune-Messiah [EPUB]":                         false,
		"Frank Herbert - Dune-Messiah":                                false,
		"Dune - Brian Herbert [EPUB]":                                 false,
		"Dune [EPUB]":                                                 false,
		"Frank Herbert - Children of Dune [EPUB]":                     false,
		"Frank Herbert - Dune, Dune Messiah, Children of Dune [EPUB]": false,
		"Kevin J. Anderson - Dune: House Atreides [EPUB]":             false,
		"---": false,
	} {
		if got := MatchBook(name, dune()) == nil; got != ok {
			t.Errorf("%s: matched=%v, want %v (%+v)", name, got, ok, MatchBook(name, dune()))
		}
	}
	// A word of the title that is also the author's is the title's.
	wilde := BookWant{ItemID: 1, Title: "Oscar Wilde: A Life", Author: "Oscar Wilde"}
	if rej := MatchBook("Oscar Wilde - Oscar Wilde: A Life [EPUB]", wilde); rej != nil {
		t.Errorf("a title with the author's name in it: %+v", rej)
	}
	if rej := MatchBook("Frank Herbert - Dune [EPUB]", BookWant{ItemID: 9, Title: "Dune"}); rej == nil ||
		!strings.Contains(rej.Detail, "author is not known") {
		t.Errorf("a book whose author is not known: %+v", rej)
	}
}

// ADR-0049, decision 1.
func TestAReleaseNameSaysItsBookFormat(t *testing.T) {
	for name, want := range map[string]string{
		"Dune [EPUB]": "EPUB", "Dune (azw3)": "AZW3", "Dune.KF8": "AZW3", "Dune mobi": "MOBI",
		"Dune.azw": "MOBI", "Dune - scan.pdf": "PDF", "Dune (1965)": "Unknown", "Dune epub pdf": "EPUB",
	} {
		if got, _ := BookQuality(name); got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}
	ranks := []string{"x epub", "x azw3", "x mobi", "x pdf", "x"}
	for i := 1; i < len(ranks); i++ {
		_, hi := BookQuality(ranks[i-1])
		_, lo := BookQuality(ranks[i])
		if hi <= lo {
			t.Errorf("%s does not outrank %s", ranks[i-1], ranks[i])
		}
	}
}

// ADR-0049, decision 5.
func TestABookSearchMarksTheMatchesBestFirst(t *testing.T) {
	var seen indexer.Query
	var mu sync.Mutex
	svc := New(fakeSource{defs("A")}, searcherFunc(func(_ context.Context,
		_ indexer.Definition, q indexer.Query) ([]indexer.Result, error) {
		mu.Lock()
		seen = q
		mu.Unlock()
		return []indexer.Result{
			result("A", "Frank Herbert - Dune [PDF]", 900),
			result("A", "Frank Herbert - Dune Messiah [EPUB]", 800),
			result("A", "Frank Herbert - Dune (1965)", 700),
			result("A", "Frank Herbert - Dune [EPUB]", 5),
		}, nil
	}), nil)
	resp, err := svc.SearchBook(context.Background(), BookSearch{Want: dune()})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if seen.Term != "herbert dune" || len(seen.Categories) != 1 || seen.Categories[0] != 7000 || seen.Season != -1 {
		t.Errorf("asked %+v", seen)
	}
	mu.Unlock()
	want := []string{"Frank Herbert - Dune [EPUB]", "Frank Herbert - Dune [PDF]"}
	for i, title := range want {
		c := resp.Candidates[i]
		if c.Title != title || !c.Accepted || c.Target == nil || *c.Target != (Target{ItemID: 9, Book: true}) {
			t.Errorf("candidate %d: %s %v %+v, want %s", i, c.Title, c.Accepted, c.Target, title)
		}
	}
	for _, c := range resp.Candidates[2:] {
		if c.Accepted || c.Target != nil || c.Rejection == nil {
			t.Errorf("%s was not refused", c.Title)
		}
	}
	f := &fakeSearcher{}
	bare := New(fakeSource{defs("A")}, f, nil)
	for _, w := range []BookWant{{Title: "Dune", Author: "x"}, {ItemID: 9, Author: "x"}, {ItemID: 9, Title: "Dune"}} {
		if _, err := bare.SearchBook(context.Background(), BookSearch{Want: w}); err == nil {
			t.Errorf("%+v was searched for", w)
		}
	}
	if f.calls.Load() != 0 {
		t.Errorf("%d indexer calls for requests that name no book", f.calls.Load())
	}
}

// ADR-0049, decision 5: a book target is its item and nothing else, sealed.
func TestATicketCarriesABook(t *testing.T) {
	for _, tc := range []struct {
		t     Target
		valid bool
	}{
		{Target{ItemID: 9, Book: true}, true},
		{Target{ItemID: 9, Book: true, Film: true}, false},
		{Target{ItemID: 9, Book: true, Album: 3}, false},
		{Target{ItemID: 9, Book: true, Season: 1}, false},
		{Target{ItemID: 9, Book: true, Episode: 1}, false},
		{Target{Book: true}, false},
	} {
		if got := tc.t.Valid(); got != tc.valid {
			t.Errorf("%+v: Valid() = %v", tc.t, got)
		}
	}
	tk := NewTickets(testCipher(t), time.Hour, nil)
	c := testCandidate()
	c.Target = &Target{ItemID: 9, Book: true}
	token, err := tk.Seal(c, 42)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tk.Open(token, 42)
	if err != nil || got.Target == nil || *got.Target != (Target{ItemID: 9, Book: true}) || got.Target.Code() != "" {
		t.Errorf("opened %+v %v", got.Target, err)
	}
}

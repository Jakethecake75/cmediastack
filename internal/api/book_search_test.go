package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/acquire"
	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// ADR-0049, decision 5: only a candidate that IS the book, and is accepted,
// carries a ticket — whatever the search service says.
func TestOnlyTheBookCarriesATicket(t *testing.T) {
	s := &fakeEpisodeSearch{resp: search.Response{Candidates: []search.Candidate{
		candidate("Frank Herbert - Dune [EPUB]", true, &search.Target{ItemID: 9, Book: true}, nil),
		candidate("Frank Herbert - Dune Messiah [EPUB]", true, &search.Target{ItemID: 10, Book: true}, nil),
		candidate("Frank Herbert - Dune (film)", true, &search.Target{ItemID: 9, Film: true}, nil),
		candidate("Frank Herbert - Dune", false, &search.Target{ItemID: 9, Book: true}, nil),
	}}}
	sealer := &recordingSealer{}
	h := &Handlers{search: s, tickets: sealer, media: duneLibrary()}
	book := importer.Item{ID: 9, Kind: importer.KindBook, Title: "Dune", Author: "Frank Herbert", Year: 1965}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/media/9/search", strings.NewReader(`{}`))
	req = req.WithContext(authz.WithPrincipal(req.Context(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
	}))
	w := httptest.NewRecorder()
	h.searchForBook(w, req, book, targetedSearchRequest{})
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %v", w.Code, body)
	}
	if len(sealer.sealed) != 1 || sealer.sealed[0].Title != "Frank Herbert - Dune [EPUB]" {
		t.Errorf("sealed %+v", sealer.sealed)
	}
	if s.gotBook.Want.Author != "Frank Herbert" || body["term"] != "herbert dune" {
		t.Errorf("searched for %+v as %v", s.gotBook.Want, body["term"])
	}
	// A book with no author is not searched for.
	book.Author = ""
	w = httptest.NewRecorder()
	h.searchForBook(w, req, book, targetedSearchRequest{})
	if w.Code != http.StatusConflict {
		t.Errorf("a book with no author: %d", w.Code)
	}
}

// ADR-0050, decisions 2 and 4: the Wanted screen says what was done about a
// book, and why one with no author is not looked for.
func TestTheWantedScreenSaysWhatWasDoneAboutABook(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	rep := &acquire.Report{
		States: map[acquire.StateKey]acquire.State{
			{Book: true, ID: 9}: {Outcome: acquire.OutcomeNothing, Detail: "nothing seeded",
				SearchedAt: now.Add(-time.Hour), NextAt: now.Add(5 * time.Hour)},
			{Film: true, ID: 12}: {Outcome: acquire.OutcomeGrabbed, Detail: "a film's"},
		},
		InFlight: map[acquire.Key]bool{{Book: true, ItemID: 10}: true, {ItemID: 12}: true},
	}
	for name, tc := range map[string]struct {
		book   importer.Item
		status string
	}{
		"searched":    {importer.Item{ID: 9, Author: "Frank Herbert"}, acquire.OutcomeNothing},
		"downloading": {importer.Item{ID: 10, Author: "Frank Herbert"}, "downloading"},
		"no author":   {importer.Item{ID: 11}, "not_searchable"},
		"not yet":     {importer.Item{ID: 12, Author: "Frank Herbert"}, "not_searched"},
	} {
		if got := bookAutomaticJSON(tc.book, rep, now); got["status"] != tc.status {
			t.Errorf("%s: %v, want %s", name, got["status"], tc.status)
		}
	}
}

package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// Searching for one book (ADR-0049): the title's search route, for a title
// that is a book.

// searchForBook searches the indexers for a book the caller may see. Only a
// candidate that is the book and names its format carries a ticket, sealed to
// the book.
func (h *Handlers) searchForBook(w http.ResponseWriter, r *http.Request, book importer.Item, in targetedSearchRequest) {
	if strings.TrimSpace(book.Author) == "" {
		writeProblem(w, http.StatusConflict, book.Title+" has no author in the library, so no release "+
			"can be told apart from another book of the same name")
		return
	}
	term := strings.TrimSpace(in.Term)
	if term == "" {
		term = search.BookTerm(book.Title, book.Author)
	}
	resp, err := h.search.SearchBook(r.Context(), search.BookSearch{
		Want: search.BookWant{ItemID: book.ID, Title: book.Title, Author: book.Author},
		Term: term, IndexerIDs: in.IndexerIDs,
	})
	switch {
	case errors.Is(err, search.ErrNoIndexers):
		writeProblem(w, http.StatusConflict, "no indexers are enabled")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	p := authz.FromContext(r.Context())
	candidates := make([]map[string]any, 0, len(resp.Candidates))
	matches := 0
	for _, c := range resp.Candidates {
		entry := candidateJSON(c)
		isBook := c.Target != nil && c.Target.Book && c.Target.ItemID == book.ID
		entry["matches"] = isBook
		if isBook {
			matches++
		}
		if c.Accepted && isBook && h.tickets != nil && p != nil {
			if tok, terr := h.tickets.Seal(c, p.UserID); terr == nil {
				entry["ticket"] = tok
			}
		}
		candidates = append(candidates, entry)
	}
	have := false
	if files, ferr := h.media.FilesFor(r.Context(), book.ID); ferr == nil {
		have = len(files) > 0
	}
	name := bookName(book)
	body := map[string]any{
		"book": map[string]any{"id": book.ID, "title": book.Title, "author": book.Author,
			"year": book.Year, "name": name, "have": have},
		"term":       term,
		"candidates": candidates,
		"count":      len(candidates),
		"matches":    matches,
		"accepted":   len(resp.Accepted()),
		"indexers":   outcomesJSON(resp.Outcomes),
		"queried":    resp.Queried,
		"failed":     resp.Failed,
		"partial":    resp.Partial(),
		"elapsed_ms": resp.Elapsed.Milliseconds(),
	}
	if resp.Partial() {
		body["warning"] = "some indexers did not answer; these results are incomplete"
	}
	switch {
	case len(candidates) == 0:
		body["note"] = "Nothing was offered for “" + term + "”. Whatever is asked, only " + name + " can be grabbed."
	case matches == 0:
		body["note"] = "The indexers answered, but nothing they offered is " + name + ". Each result says why."
	}
	writeJSON(w, http.StatusOK, body)
}

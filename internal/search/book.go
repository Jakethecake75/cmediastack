package search

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/release"
)

// Searching for one book (ADR-0049).
//
// As for an album, a release name is not parsed. Its brackets are set aside,
// it is folded, the words of the author's name, "by", years and tags are taken
// out, and what is left must be the title, exactly.

// ReasonNotThisBook is why a candidate is not the book.
const ReasonNotThisBook = "not_this_book"

// BookCategories is what a book search asks the indexers for: Torznab's Books.
var BookCategories = []int{7000}

// BookWant is one book a person asked to search for.
type BookWant struct {
	ItemID int64
	Title  string
	Author string
}

// bookTags are the words that may sit beside a book's title in a release name.
var bookTags = map[string]bool{
	"epub": true, "azw3": true, "azw": true, "mobi": true, "pdf": true, "kf8": true,
	"retail": true, "ebook": true, "e": true, "book": true, "kindle": true, "ebooks": true,
	"by": true,
}

var (
	bracketed = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)|\{[^}]*\}`)
	bookYear  = regexp.MustCompile(`^(1[5-9]|20)\d\d$`)
	// sceneGroup is the "-GROUP" a scene release ends with.
	sceneGroup = regexp.MustCompile(`(\S)-[A-Za-z0-9]+$`)
)

// words is a name folded and split.
func words(s string) []string { return strings.Fields(NormalizeTitle(s)) }

// surname is the last word of an author's folded name.
func surname(author string) string {
	w := words(author)
	if len(w) == 0 {
		return ""
	}
	return w[len(w)-1]
}

// MatchBook says whether a release name is the wanted book, and why not when
// it is not (ADR-0049, decision 4). A nil rejection is a match.
//
// The title must appear as one run of the name's words, and every word outside
// that run must be a word of the author's name, "by", a year or a tag — with
// the author's surname among them.
func MatchBook(name string, w BookWant) *release.Rejection {
	title := words(w.Title)
	last := surname(w.Author)
	if !strings.ContainsAny(name, " \t") {
		// A scene name — words joined by dots — ends in its group. A name with
		// spaces is a person's, and a hyphen in it is part of a word.
		name = sceneGroup.ReplaceAllString(name, "$1")
	}
	all := words(bracketed.ReplaceAllString(name, " "))
	if len(all) == 0 || len(title) == 0 {
		return &release.Rejection{Reason: release.ReasonUnparsed,
			Detail: "the release name does not say which book it is"}
	}
	if last == "" {
		return &release.Rejection{Reason: ReasonNotThisBook,
			Detail: "the book's author is not known, so no release can be told from another book of the same name"}
	}
	author := map[string]bool{}
	for _, a := range words(w.Author) {
		author[a] = true
	}
	named := false
	for i := 0; i+len(title) <= len(all); i++ {
		if !slices.Equal(all[i:i+len(title)], title) {
			continue
		}
		outside := append(slices.Clone(all[:i]), all[i+len(title):]...)
		fits, surnamed := true, false
		for _, x := range outside {
			if x == last {
				surnamed = true
			}
			if !author[x] && !bookTags[x] && !bookYear.MatchString(x) {
				fits = false
			}
		}
		named = named || surnamed
		if fits && surnamed {
			return nil
		}
	}
	if !named && !slices.Contains(all, last) {
		return &release.Rejection{Reason: ReasonNotThisBook,
			Detail: "it does not name the author, " + strings.TrimSpace(w.Author)}
	}
	return &release.Rejection{Reason: ReasonNotThisBook,
		Detail: fmt.Sprintf("a different book: %q is not %s by %s", strings.Join(all, " "),
			strings.TrimSpace(w.Title), strings.TrimSpace(w.Author))}
}

// BookQuality is the format a release name says, and its rank on the ladder
// (ADR-0049, decision 1). Zero is Unknown.
func BookQuality(name string) (string, int) {
	has := map[string]bool{}
	for _, w := range words(name) {
		has[w] = true
	}
	switch {
	case has["epub"]:
		return "EPUB", 4
	case has["azw3"] || has["kf8"]:
		return "AZW3", 3
	case has["mobi"] || has["azw"]:
		return "MOBI", 2
	case has["pdf"]:
		return "PDF", 1
	}
	return release.QualityUnknown.Name, 0
}

// BookTerm is what the indexers are asked for a book: the author's surname and
// the title, folded.
func BookTerm(title, author string) string {
	return strings.TrimSpace(surname(author) + " " + NormalizeTitle(title))
}

// BookSearch is a search for one book.
type BookSearch struct {
	Want       BookWant
	Term       string
	IndexerIDs []int64
}

// SearchBook searches for one book and judges every candidate against it. No
// video quality profile applies: the format ladder does.
func (s *Service) SearchBook(ctx context.Context, bs BookSearch) (Response, error) {
	w := bs.Want
	if w.ItemID <= 0 || strings.TrimSpace(w.Title) == "" || strings.TrimSpace(w.Author) == "" {
		return Response{}, fmt.Errorf("search: %+v does not name a book and its author", w)
	}
	term := strings.TrimSpace(bs.Term)
	if term == "" {
		term = BookTerm(w.Title, w.Author)
	}
	resp, err := s.Search(ctx, Request{
		Term: term, Season: -1, Categories: BookCategories, IndexerIDs: bs.IndexerIDs,
	})
	if err != nil {
		return resp, err
	}
	for i := range resp.Candidates {
		c := &resp.Candidates[i]
		name, rank := BookQuality(c.Title)
		c.Quality, c.Score, c.Target = release.Quality{Name: name}, rank, nil
		if rej := MatchBook(c.Title, w); rej != nil {
			c.Accepted, c.Rejection = false, rej
			continue
		}
		if rank == 0 {
			c.Accepted, c.Rejection = false, &release.Rejection{Reason: ReasonUnknownFormat,
				Detail: "the name does not say whether it is EPUB, AZW3, MOBI or PDF"}
			continue
		}
		c.Accepted, c.Rejection = true, nil
		c.Target = &Target{ItemID: w.ItemID, Book: true}
	}
	sort.SliceStable(resp.Candidates, func(i, j int) bool {
		a, b := resp.Candidates[i], resp.Candidates[j]
		if a.Accepted != b.Accepted {
			return a.Accepted
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.Seeders > b.Seeders
	})
	return resp, nil
}

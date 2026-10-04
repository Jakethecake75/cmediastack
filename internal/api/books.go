package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/books"
	"github.com/jakethecake75/cmediastack/internal/importer"
)

// Books (ADR-0048): a book added from Open Library, and wanted until a file
// holds it.

// BookService is what the book routes need.
type BookService interface {
	Search(ctx context.Context, q string) ([]books.Work, error)
	Add(ctx context.Context, req books.AddRequest) (importer.Item, error)
	Wanted(ctx context.Context, limit int) ([]importer.Item, error)
}

// bookName is "The Left Hand of Darkness (1969) — Ursula K. Le Guin".
func bookName(it importer.Item) string {
	name := filmName(it)
	if it.Author != "" {
		name += " — " + it.Author
	}
	return name
}

func bookProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, books.ErrNotAWork):
		writeProblem(w, http.StatusBadRequest, "openlibrary_id must be an Open Library work id, like OL59800W")
	case errors.Is(err, books.ErrWrongRootFolder), errors.Is(err, books.ErrChooseRootFolder),
		errors.Is(err, importer.ErrUnusableFolder):
		writeProblem(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, books.ErrNoRootFolder):
		writeProblem(w, http.StatusConflict, "no root folder for books is configured; add one under Storage first")
	case errors.Is(err, books.ErrNotFound):
		writeProblem(w, http.StatusUnprocessableEntity, "Open Library has no such book, so nothing was added")
	case errors.Is(err, books.ErrUnavailable):
		writeProblem(w, http.StatusBadGateway, "Open Library could not be asked, so nothing was done: "+err.Error())
	case errors.Is(err, importer.ErrUnnameable):
		writeProblem(w, http.StatusUnprocessableEntity,
			"no folder name can be made from the book's title; name the folder: "+err.Error())
	default:
		writeAuthzAware(w, err)
	}
}

// SearchBooks asks Open Library for books by title or author.
func (h *Handlers) SearchBooks(w http.ResponseWriter, r *http.Request) {
	if h.books == nil {
		writeProblem(w, http.StatusNotImplemented, "books are not wired")
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 200 {
		writeProblem(w, http.StatusBadRequest, "q is the book's title, its author, or both")
		return
	}
	found, err := h.books.Search(r.Context(), q)
	if err != nil {
		bookProblem(w, err)
		return
	}
	out := make([]map[string]any, 0, len(found))
	for _, b := range found {
		row := map[string]any{"openlibrary_id": b.ID, "title": b.Title, "authors": b.Authors,
			"editions": b.Editions}
		if b.Year > 0 {
			row["year"] = b.Year
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"books": out, "count": len(out)})
}

// addBook adds a book, monitored, or nothing and a sentence saying why.
func (h *Handlers) addBook(w http.ResponseWriter, r *http.Request, in addMediaRequest) {
	if h.books == nil {
		writeProblem(w, http.StatusNotImplemented, "books are not wired")
		return
	}
	if strings.TrimSpace(in.Monitor) != "" || in.TMDBID != 0 || in.MusicBrainzID != "" {
		// Refused rather than ignored: a field a book does not have.
		writeProblem(w, http.StatusBadRequest, "a book is added by openlibrary_id alone, monitored; "+
			"monitor, tmdb_id and musicbrainz_id are not a book's")
		return
	}
	item, err := h.books.Add(r.Context(), books.AddRequest{WorkID: in.OpenLibraryID,
		RootFolderID: in.RootFolderID, Folder: in.Folder,
		SourceIP: ClientIP(r.Context()), UserAgent: r.UserAgent()})
	var conflict *importer.ConflictError
	switch {
	case err == nil:
	case errors.As(err, &conflict):
		note, kind := "It is already in the library.", "already_in_library"
		if errors.Is(err, importer.ErrFolderTaken) {
			note, kind = "Another item already occupies that folder. Name another folder.", "folder_taken"
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(),
			"item": itemJSON(conflict.Existing), "note": note, "conflict": kind})
		return
	default:
		bookProblem(w, err)
		return
	}
	w.Header().Set("Location", "/api/v1/media/"+strconv.FormatInt(item.ID, 10))
	note := searchingNote(h.startSearch("books"), "book")
	if note == "" {
		note = "It is on the Wanted list until a file holds it. Nothing was downloaded, and " +
			"nothing was created on disk."
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"item": itemJSON(item), "name": bookName(item),
		"note": note,
	})
}

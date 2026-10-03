package books

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// KindBook is a book's media_item kind.
const KindBook = importer.KindBook

// Catalogue is what the service asks Open Library — an interface so tests can
// answer without the network.
type Catalogue interface {
	SearchBooks(ctx context.Context, q string) ([]Work, error)
	Work(ctx context.Context, id string) (Work, error)
}

// RootLister is the slice of the root-folder store adding needs.
type RootLister interface {
	List(ctx context.Context) ([]library.RootFolder, error)
}

// Errors of adding.
var (
	// ErrNoRootFolder means no books root folder is configured, or the caller
	// may see none.
	ErrNoRootFolder = errors.New("books: no root folder holds books")
	// ErrChooseRootFolder means several could, and one must be named.
	ErrChooseRootFolder = errors.New("books: more than one root folder holds books; choose one")
	// ErrWrongRootFolder means the named one does not exist or holds something else.
	ErrWrongRootFolder = errors.New("books: that root folder does not hold books")
)

// Service adds books and lists the wanted ones.
type Service struct {
	db        *db.DB
	roots     RootLister
	catalogue Catalogue
	audit     *audit.Logger
	now       func() time.Time
}

// NewService builds one.
func NewService(database *db.DB, roots RootLister, catalogue Catalogue, auditLog *audit.Logger,
	now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{db: database, roots: roots, catalogue: catalogue, audit: auditLog, now: now}
}

// Search asks Open Library. It spends a request, so it is library.edit, as
// searching any provider is.
func (svc *Service) Search(ctx context.Context, q string) ([]Work, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return nil, err
	}
	return svc.catalogue.SearchBooks(ctx, q)
}

// AddRequest is a book to add.
type AddRequest struct {
	WorkID       string
	RootFolderID int64
	Folder       string
	SourceIP     string
	UserAgent    string
}

// FolderFor is a book's folder: "<Author> - <Title> (<Year>)", one component
// (ADR-0048, decision 3).
func FolderFor(w Work) (string, error) {
	name := w.Title
	if a := strings.TrimSpace(w.Author()); a != "" {
		name = a + " - " + w.Title
	}
	return importer.FolderFor(name, w.Year)
}

// Add puts a book in the library, monitored, or nothing: the book is read from
// Open Library, its root chosen, and the row written only if neither the book
// nor its folder is there already. Nothing is written to disk.
func (svc *Service) Add(ctx context.Context, req AddRequest) (importer.Item, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return importer.Item{}, err
	}
	id := strings.TrimSpace(req.WorkID)
	if !ValidWorkID(id) {
		return importer.Item{}, ErrNotAWork
	}
	root, err := svc.chooseRoot(ctx, req.RootFolderID)
	if err != nil {
		return importer.Item{}, err
	}
	work, err := svc.catalogue.Work(ctx, id)
	if err != nil {
		return importer.Item{}, err
	}
	folder := strings.TrimSpace(req.Folder)
	if folder == "" {
		if folder, err = FolderFor(work); err != nil {
			return importer.Item{}, err
		}
	}
	if err := importer.CheckFolderName(folder); err != nil {
		return importer.Item{}, err
	}

	now := svc.now().UTC().Format(time.RFC3339Nano)
	var year any
	if work.Year > 0 {
		year = work.Year
	}
	res, err := svc.db.ExecContext(ctx, `
		INSERT INTO media_item (kind, title, year, sort_title, root_folder_id, folder,
		                        openlibrary_id, author, monitored, added_at, updated_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM media_item WHERE kind = ? AND openlibrary_id = ?)
		  AND NOT EXISTS (SELECT 1 FROM media_item WHERE root_folder_id = ? AND folder = ?)`,
		KindBook, work.Title, year, importer.SortTitle(work.Title), root.ID, folder,
		work.ID, nullString(work.Author()), now, now,
		KindBook, work.ID, root.ID, folder)
	if err != nil {
		return importer.Item{}, fmt.Errorf("books: adding the book: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Unscoped: which condition refused the add, whoever can see the title.
		var existing int64
		if err := svc.db.QueryRowContext(ctx, `SELECT id FROM media_item WHERE kind = ? AND openlibrary_id = ?`,
			KindBook, work.ID).Scan(&existing); err == nil {
			return importer.Item{}, &importer.ConflictError{Err: importer.ErrAlreadyInLibrary,
				Existing: importer.Item{ID: existing, Kind: KindBook, Title: work.Title}}
		}
		return importer.Item{}, &importer.ConflictError{Err: importer.ErrFolderTaken,
			Existing: importer.Item{RootFolderID: root.ID, Folder: folder}}
	}
	itemID, err := res.LastInsertId()
	if err != nil {
		return importer.Item{}, err
	}
	item := importer.Item{ID: itemID, Kind: KindBook, Title: work.Title, Year: work.Year,
		RootFolderID: root.ID, Folder: folder, Monitored: true, Author: work.Author(),
		OpenLibraryID: work.ID}
	if p := authz.FromContext(ctx); p != nil && svc.audit != nil {
		_ = svc.audit.Write(ctx, audit.Event{
			ActorUserID: &p.UserID, ActorLabel: p.Username, Action: audit.ActionMediaAdded,
			TargetKind: "media_item", TargetID: fmt.Sprint(itemID), SourceIP: req.SourceIP, UserAgent: req.UserAgent,
			Detail: fmt.Sprintf("%s (book, %s) added into %s/%s", work.Title, work.ID, root.Path, folder),
		})
	}
	return item, nil
}

func (svc *Service) chooseRoot(ctx context.Context, id int64) (library.RootFolder, error) {
	all, err := svc.roots.List(ctx)
	if err != nil {
		return library.RootFolder{}, err
	}
	var fitting []library.RootFolder
	for _, r := range all {
		if id != 0 && r.ID == id {
			if r.Kind != library.KindBooks {
				return library.RootFolder{}, fmt.Errorf("%w: %s holds %s", ErrWrongRootFolder, r.Path, r.Kind)
			}
			return r, nil
		}
		if r.Kind == library.KindBooks {
			fitting = append(fitting, r)
		}
	}
	switch {
	case id != 0:
		return library.RootFolder{}, fmt.Errorf("%w: there is no root folder %d", ErrWrongRootFolder, id)
	case len(fitting) == 0:
		return library.RootFolder{}, ErrNoRootFolder
	case len(fitting) > 1:
		return library.RootFolder{}, ErrChooseRootFolder
	}
	return fitting[0], nil
}

// Wanted lists the books the caller may see that are monitored and held by no
// file, the most recently added first.
func (svc *Service) Wanted(ctx context.Context, limit int) ([]importer.Item, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	visible, args := library.Visible(ctx, "i")
	rows, err := svc.db.QueryContext(ctx, `
		SELECT i.id, i.title, COALESCE(i.year, 0), i.root_folder_id, i.folder,
		       COALESCE(i.author, ''), COALESCE(i.openlibrary_id, ''), i.added_at
		FROM media_item i
		WHERE `+visible+` AND i.kind = 'book' AND i.monitored = 1
		  AND NOT EXISTS (SELECT 1 FROM media_file f WHERE f.item_id = i.id)
		ORDER BY i.added_at DESC, i.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("books: reading the wanted books: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]importer.Item, 0)
	for rows.Next() {
		it := importer.Item{Kind: KindBook, Monitored: true}
		var added string
		if err := rows.Scan(&it.ID, &it.Title, &it.Year, &it.RootFolderID, &it.Folder,
			&it.Author, &it.OpenLibraryID, &added); err != nil {
			return nil, fmt.Errorf("books: reading the wanted books: %w", err)
		}
		it.AddedAt, _ = time.Parse(time.RFC3339Nano, added)
		out = append(out, it)
	}
	return out, rows.Err()
}

func nullString(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

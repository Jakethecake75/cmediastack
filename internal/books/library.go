package books

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// The scan and the import of book files (ADR-0049).

// Roots opens the library's locations.
type Roots interface {
	List(ctx context.Context) ([]library.RootFolder, error)
	OpenVault(ctx context.Context, id int64) (*library.Vault, error)
}

// Library scans books roots and imports downloaded books.
type Library struct {
	db    *db.DB
	roots Roots
	log   *slog.Logger
	now   func() time.Time
}

// NewLibrary builds one.
func NewLibrary(database *db.DB, roots Roots, log *slog.Logger, now func() time.Time) *Library {
	if log == nil {
		log = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &Library{db: database, roots: roots, log: log, now: now}
}

// Reasons a book file is left alone.
const (
	ReasonNotInBookFolder = "not inside a book's folder"
	ReasonNoBook          = "no book in the library owns this folder; add it from Open Library"
	ReasonLesserFormat    = "another file of this book is the one recorded"
)

type bookRow struct {
	id     int64
	title  string
	author string
	folder string
}

// booksIn is every book in a root, by folder.
func (l *Library) booksIn(ctx context.Context, rootID int64) (map[string]bookRow, error) {
	// Unscoped: the scan reconciles the whole root.
	rows, err := l.db.QueryContext(ctx, `
		SELECT id, title, COALESCE(author, ''), folder FROM media_item
		WHERE kind = ? AND root_folder_id = ?`, KindBook, rootID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bookRow{}
	for rows.Next() {
		var b bookRow
		if err := rows.Scan(&b.id, &b.title, &b.author, &b.folder); err != nil {
			return nil, err
		}
		out[b.folder] = b
	}
	return out, rows.Err()
}

type knownFile struct {
	id     int64
	itemID int64
}

// knownBooks is what the database holds of a root's books: path to file.
func (l *Library) knownBooks(ctx context.Context, rootID int64) (map[string]knownFile, error) {
	// Unscoped: the scan reconciles the whole root.
	rows, err := l.db.QueryContext(ctx, `
		SELECT f.id, f.item_id, f.relative_path FROM media_file f
		WHERE f.root_folder_id = ? AND f.item_id IN (SELECT id FROM media_item WHERE kind = ?)`,
		rootID, KindBook)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]knownFile{}
	for rows.Next() {
		var k knownFile
		var p string
		if err := rows.Scan(&k.id, &k.itemID, &p); err != nil {
			return nil, err
		}
		out[p] = k
	}
	return out, rows.Err()
}

// record writes a book's one file: the row it has is moved to the new path,
// or a row is made for it.
func (l *Library) record(ctx context.Context, itemID, rootID int64, rel string, size int64,
	infoHash string, hardlinked bool) error {
	now := l.now().UTC().Format(time.RFC3339Nano)
	quality, _ := Format(rel)
	var hash any
	if infoHash != "" {
		hash = infoHash
	}
	hl := 0
	if hardlinked {
		hl = 1
	}
	return l.db.InTx(ctx, func(tx db.Execer) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE media_file SET root_folder_id = ?, relative_path = ?, size_bytes = ?, quality = ?,
			       info_hash = ?, hardlinked = ?, imported_at = ?
			WHERE item_id = ?`, rootID, rel, size, quality, hash, hl, now, itemID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO media_file (item_id, root_folder_id, relative_path, size_bytes, quality, revision,
			                        release_title, info_hash, hardlinked, imported_at)
			VALUES (?, ?, ?, ?, ?, 0, '', ?, ?, ?)`, itemID, rootID, rel, size, quality, hash, hl, now)
		return err
	})
}

type found struct {
	path string
	size int64
}

// Scan walks a books root and records each book's file (ADR-0049, decision 3).
// It changes nothing on disk.
func (l *Library) Scan(ctx context.Context, rootID int64) (importer.ScanResult, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return importer.ScanResult{}, err
	}
	started := l.now()
	roots, err := l.roots.List(ctx)
	if err != nil {
		return importer.ScanResult{}, err
	}
	var root library.RootFolder
	for _, r := range roots {
		if r.ID == rootID {
			root = r
		}
	}
	if root.ID == 0 {
		return importer.ScanResult{}, library.ErrRootNotFound
	}
	vault, err := l.roots.OpenVault(ctx, rootID)
	if err != nil {
		return importer.ScanResult{}, err
	}
	defer func() { _ = vault.Close() }()

	res := importer.ScanResult{RootID: rootID, Root: root.Path}
	shelf, err := l.booksIn(ctx, rootID)
	if err != nil {
		return res, err
	}
	known, err := l.knownBooks(ctx, rootID)
	if err != nil {
		return res, err
	}
	skip := func(p string, size int64, why string) {
		res.Skipped = append(res.Skipped, importer.Rejection{Path: p, Bytes: size, Reason: why})
	}
	seen := map[string]bool{}
	byBook := map[int64][]found{}

	walkErr := fs.WalkDir(vault.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			l.log.Warn("a directory could not be read during a books scan",
				slog.String("path", p), slog.String("error", err.Error()))
			return nil //nolint:nilerr // the rest of the root is still worth scanning
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 || !IsBook(p) {
			return nil
		}
		if res.Scanned >= importer.MaxScanFiles {
			res.Truncated = true
			return fs.SkipAll
		}
		res.Scanned++
		var size int64
		if info, ierr := d.Info(); ierr == nil {
			size = info.Size()
		}
		comps := strings.Split(p, "/")
		if len(comps) < 2 {
			skip(p, size, ReasonNotInBookFolder)
			return nil
		}
		book, ok := shelf[comps[0]]
		if !ok {
			skip(p, size, ReasonNoBook)
			return nil
		}
		seen[p] = true
		byBook[book.id] = append(byBook[book.id], found{path: p, size: size})
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return res, fmt.Errorf("books: scanning %s: %w", root.Path, walkErr)
	}

	ids := make([]int64, 0, len(byBook))
	for id := range byBook {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		files := byBook[id]
		var recorded string
		for _, f := range files {
			if k, ok := known[f.path]; ok && k.itemID == id {
				recorded = f.path
			}
		}
		pick := recorded
		if pick == "" {
			paths := make([]string, 0, len(files))
			for _, f := range files {
				paths = append(paths, f.path)
			}
			pick, _ = best(paths)
		}
		for _, f := range files {
			switch {
			case f.path != pick:
				skip(f.path, f.size, ReasonLesserFormat)
			case recorded != "":
				res.Updated++
			default:
				if err := l.record(ctx, id, rootID, f.path, f.size, "", false); err != nil {
					l.log.Warn("a book file found by the scan could not be recorded",
						slog.String("path", f.path), slog.String("error", err.Error()))
					skip(f.path, f.size, err.Error())
					continue
				}
				res.Added++
			}
		}
	}
	for p := range known {
		if !seen[p] {
			res.Missing = append(res.Missing, p)
		}
	}
	sort.Strings(res.Missing)
	res.Elapsed = l.now().Sub(started)
	if len(known) > 0 && float64(len(res.Missing))/float64(len(known)) > importer.MissingRatioGuard {
		return res, fmt.Errorf("%w: %d of %d recorded book files are not on disk in %s. Nothing has been "+
			"changed; check the mount", importer.ErrLibraryVanished, len(res.Missing), len(known), root.Path)
	}
	return res, nil
}

// ImportResult is what importing a downloaded book did.
type ImportResult struct {
	// Placed is the file filed into the library; Replaced the one it moved to
	// the trash; Kept the one already there that was better or as good.
	Placed, Replaced, Kept string
	Skipped                []importer.Rejection
}

// Summary says it in a line.
func (r ImportResult) Summary() string {
	switch {
	case r.Placed != "" && r.Replaced != "":
		return "filed " + r.Placed + ", replacing " + r.Replaced + " (in the trash)"
	case r.Placed != "":
		return "filed " + r.Placed
	case r.Kept != "":
		return "kept " + r.Kept + ": the download's format is not better"
	}
	return "no book file (EPUB, AZW3, MOBI or PDF) in the download"
}

// ImportDownload files a completed download grabbed for a book (ADR-0049,
// decision 5): its best book file, hard-linked or copied across filesystems,
// replacing the book's file only when its format is better.
func (l *Library) ImportDownload(ctx context.Context, dir string, files []string, itemID int64,
	infoHash string) (ImportResult, error) {
	var res ImportResult
	// Unscoped: the import files for the whole library.
	var rootID int64
	var title, author, folder, kind string
	if err := l.db.QueryRowContext(ctx, `
		SELECT kind, root_folder_id, title, COALESCE(author, ''), folder FROM media_item WHERE id = ?`,
		itemID).Scan(&kind, &rootID, &title, &author, &folder); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return res, fmt.Errorf("%w: item %d", importer.ErrTargetGone, itemID)
		}
		return res, err
	}
	if kind != KindBook {
		return res, fmt.Errorf("%w: %s is not a book", importer.ErrNotTheTarget, title)
	}
	pick, ok := best(files)
	for _, f := range files {
		if f != pick {
			res.Skipped = append(res.Skipped, importer.Rejection{Path: f, Reason: "not the book's best file"})
		}
	}
	if !ok {
		return res, nil
	}
	newQuality, _ := Format(pick)
	// Unscoped: the book's own file, for the whole library.
	var oldPath, oldQuality string
	err := l.db.QueryRowContext(ctx, `SELECT relative_path, quality FROM media_file WHERE item_id = ? LIMIT 1`,
		itemID).Scan(&oldPath, &oldQuality)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return res, err
	case !Better(newQuality, oldQuality):
		res.Kept = oldPath
		return res, nil
	}

	dst, err := FilePath(folder, title, author, path.Ext(pick))
	if err != nil {
		return res, err
	}
	vault, err := l.roots.OpenVault(ctx, rootID)
	if err != nil {
		return res, err
	}
	defer func() { _ = vault.Close() }()
	src, err := library.OpenSource(dir)
	if err != nil {
		return res, err
	}
	defer func() { _ = src.Close() }()

	if oldPath != "" {
		if _, err := vault.Supersede(ctx, oldPath, l.now()); err != nil {
			return res, fmt.Errorf("books: moving %s to the trash: %w", oldPath, err)
		}
		res.Replaced = oldPath
	}
	// One copy, which the download seeds (ADR-0076).
	copies, err := library.Place(src, pick, vault, dst)
	if err != nil {
		return res, err
	}
	hardlinked := copies == 1
	var size int64
	if info, serr := vault.Stat(dst); serr == nil {
		size = info.Size()
	}
	if err := l.record(ctx, itemID, rootID, dst, size, infoHash, hardlinked); err != nil {
		return res, err
	}
	res.Placed = dst
	return res, nil
}

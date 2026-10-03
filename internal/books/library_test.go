package books

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
)

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ADR-0049, decisions 1 and 2.
func TestBookFilesAndTheirFormats(t *testing.T) {
	for name, want := range map[string]string{
		"x.epub": "EPUB", "X.EPUB": "EPUB", "x.azw3": "AZW3", "x.mobi": "MOBI", "x.pdf": "PDF",
		"x.m4b": "", "x.mp3": "", "x.txt": "", "x.jpg": "",
	} {
		if got, _ := Format(name); got != want || IsBook(name) != (want != "") {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	if !Better("EPUB", "PDF") || !Better("AZW3", "MOBI") || Better("PDF", "EPUB") || Better("EPUB", "EPUB") {
		t.Error("the ladder is EPUB > AZW3 > MOBI > PDF, and only a better format replaces")
	}
	if p, err := FilePath("Frank Herbert - Dune (1965)", "Dune", "Frank Herbert", ".EPUB"); err != nil ||
		p != "Frank Herbert - Dune (1965)/Dune - Frank Herbert.epub" {
		t.Errorf("path %q %v", p, err)
	}
	if p, _ := FilePath("F", "AC/DC: A Life?", "", ".pdf"); strings.Count(p, "/") != 1 {
		t.Errorf("an unsafe title filed as %q", p)
	}
	if _, err := FilePath("../x", "Dune", "", ".epub"); err == nil {
		t.Error("a folder with a separator was accepted")
	}
	if b, ok := best([]string{"d/x.pdf", "d/x.mobi", "d/x.epub", "d/notes.txt"}); !ok || b != "d/x.epub" {
		t.Errorf("best %q", b)
	}
	if _, ok := best([]string{"d/cover.jpg"}); ok {
		t.Error("a download with no book file had a best one")
	}
}

type libRig struct {
	*rig
	lib  *Library
	dir  string
	root int64
	dune importer.Item
}

func newLibRig(t *testing.T) *libRig {
	t.Helper()
	r := newRig(t)
	r.cat.works["OL893415W"] = Work{ID: "OL893415W", Title: "Dune", Authors: []string{"Frank Herbert"}, Year: 1965}
	dir := mkdir(t, filepath.Join(t.TempDir(), "books"))
	rf, err := r.roots.Create(r.ctx, dir, library.KindBooks, "Books")
	if err != nil {
		t.Fatal(err)
	}
	dune, err := r.svc.Add(r.ctx, AddRequest{WorkID: "OL893415W"})
	if err != nil {
		t.Fatal(err)
	}
	return &libRig{rig: r, lib: NewLibrary(r.db, r.roots, nil, time.Now), dir: dir, root: rf.ID, dune: dune}
}

func (r *libRig) held(t *testing.T) (string, string) {
	t.Helper()
	var p, q string
	if err := r.db.QueryRowContext(r.ctx, `SELECT relative_path, quality FROM media_file WHERE item_id = ?`,
		r.dune.ID).Scan(&p, &q); err != nil {
		t.Fatalf("the book's file: %v", err)
	}
	return p, q
}

func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err == nil {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// ADR-0049, decision 3.
func TestTheScanFindsABooksFileInItsFolder(t *testing.T) {
	r := newLibRig(t)
	folder := filepath.Join(r.dir, r.dune.Folder)
	// The lesser format sorts first, so the best is chosen, not the first.
	write(t, filepath.Join(folder, "A Dune.pdf"), "pdf")
	write(t, filepath.Join(folder, "Dune.epub"), "epub")
	write(t, filepath.Join(folder, "cover.jpg"), "jpg")
	write(t, filepath.Join(r.dir, "Somebody - Something", "x.epub"), "x")
	write(t, filepath.Join(r.dir, "loose.epub"), "y")

	scan := authz.SystemPrincipal(t.Context(), authz.TaskLibraryScan)
	before := snapshot(t, r.dir)
	res, err := r.lib.Scan(scan, r.root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned != 4 || res.Added != 1 {
		t.Errorf("scan %+v", res)
	}
	reasons := map[string]string{}
	for _, s := range res.Skipped {
		reasons[s.Path] = s.Reason
	}
	for p, want := range map[string]string{
		r.dune.Folder + "/A Dune.pdf": ReasonLesserFormat,
		"Somebody - Something/x.epub": ReasonNoBook,
		"loose.epub":                  ReasonNotInBookFolder,
	} {
		if reasons[p] != want {
			t.Errorf("%s: %q, want %q", p, reasons[p], want)
		}
	}
	if p, q := r.held(t); p != r.dune.Folder+"/Dune.epub" || q != "EPUB" {
		t.Errorf("held %s %s", p, q)
	}
	if after := snapshot(t, r.dir); after != before {
		t.Errorf("the scan changed the disk")
	}

	// Again: the recorded file stays the book's.
	if res, err := r.lib.Scan(scan, r.root); err != nil || res.Updated != 1 || res.Added != 0 {
		t.Errorf("a second scan: %+v %v", res, err)
	}
	if err := os.Remove(filepath.Join(folder, "Dune.epub")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.lib.Scan(scan, r.root); !errors.Is(err, importer.ErrLibraryVanished) {
		t.Errorf("the only book file vanished and the scan believed it: %v", err)
	}
}

// ADR-0049, decision 5.
func TestADownloadedBookIsImported(t *testing.T) {
	r := newLibRig(t)
	imp := authz.SystemPrincipal(t.Context(), authz.TaskImport)
	dl := t.TempDir()
	write(t, filepath.Join(dl, "Frank Herbert - Dune [MOBI]", "Dune.mobi"), "mobi")
	write(t, filepath.Join(dl, "Frank Herbert - Dune [MOBI]", "Dune.pdf"), "pdf")
	write(t, filepath.Join(dl, "Frank Herbert - Dune [MOBI]", "cover.jpg"), "jpg")
	files := []string{"Frank Herbert - Dune [MOBI]/Dune.mobi", "Frank Herbert - Dune [MOBI]/Dune.pdf",
		"Frank Herbert - Dune [MOBI]/cover.jpg"}
	res, err := r.lib.ImportDownload(imp, dl, files, r.dune.ID, strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	want := r.dune.Folder + "/Dune - Frank Herbert.mobi"
	if res.Placed != want || len(res.Skipped) != 2 {
		t.Fatalf("import: %s %+v", res.Summary(), res)
	}
	if b, err := os.ReadFile(filepath.Join(r.dir, filepath.FromSlash(want))); err != nil || string(b) != "mobi" {
		t.Errorf("not placed: %v", err)
	}

	// A PDF does not replace it; an EPUB does, and the MOBI goes to the trash.
	dl2 := t.TempDir()
	write(t, filepath.Join(dl2, "Dune.pdf"), "pdf2")
	if res, err := r.lib.ImportDownload(imp, dl2, []string{"Dune.pdf"}, r.dune.ID, strings.Repeat("b", 40)); err != nil ||
		res.Kept != want || res.Placed != "" {
		t.Errorf("a worse format: %s %v", res.Summary(), err)
	}
	write(t, filepath.Join(dl2, "Dune.epub"), "epub")
	res, err = r.lib.ImportDownload(imp, dl2, []string{"Dune.epub"}, r.dune.ID, strings.Repeat("c", 40))
	if err != nil || res.Replaced != want || res.Placed != r.dune.Folder+"/Dune - Frank Herbert.epub" {
		t.Fatalf("a better format: %s %v", res.Summary(), err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, filepath.FromSlash(want))); !os.IsNotExist(err) {
		t.Error("the replaced MOBI is still in place")
	}
	var trashed []string
	_ = filepath.Walk(filepath.Join(r.dir, ".cmediastack-trash"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			trashed = append(trashed, filepath.Base(p))
		}
		return nil
	})
	if len(trashed) != 1 || !strings.HasSuffix(trashed[0], ".mobi") {
		t.Errorf("the trash holds %q", trashed)
	}
	var rows int
	if err := r.db.QueryRowContext(r.ctx, `SELECT COUNT(*) FROM media_file WHERE item_id = ?`, r.dune.ID).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("the book has %d file rows, want its one", rows)
	}
	if _, q := r.held(t); q != "EPUB" {
		t.Errorf("held %s", q)
	}

	// No book file; not a book; gone.
	if res, err := r.lib.ImportDownload(imp, dl, []string{"Frank Herbert - Dune [MOBI]/cover.jpg"}, r.dune.ID, ""); err != nil ||
		res.Placed != "" || !strings.Contains(res.Summary(), "no book file") {
		t.Errorf("no book file: %s %v", res.Summary(), err)
	}
	r.exec(t, `INSERT INTO media_item (id, kind, title, sort_title, root_folder_id, folder, added_at, updated_at)
		VALUES (77, 'movie', 'Dune', 'dune', ?, 'Dune (2021)', 'x', 'x')`, r.root)
	if _, err := r.lib.ImportDownload(imp, dl, files, 77, ""); !errors.Is(err, importer.ErrNotTheTarget) {
		t.Errorf("a film as a book: %v", err)
	}
	if _, err := r.lib.ImportDownload(imp, dl, files, 999, ""); !errors.Is(err, importer.ErrTargetGone) {
		t.Errorf("a book that is gone: %v", err)
	}
}

func (r *rig) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := r.db.ExecContext(r.ctx, q, args...); err != nil {
		t.Fatal(err)
	}
}

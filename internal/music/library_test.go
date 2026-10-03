package music

import (
	"context"
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

type libRig struct {
	*rig
	lib    *Library
	roots  *library.RootStore
	dir    string
	artist int64
	okc    int64
}

func write(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// newLibRig follows Radiohead in a real music root folder on disk.
func newLibRig(t *testing.T) *libRig {
	t.Helper()
	r := newRig(t)
	base := t.TempDir()
	downloads := filepath.Join(base, "downloads")
	dir := filepath.Join(base, "music")
	for _, d := range []string{downloads, dir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	roots := library.NewRootStore(r.db, downloads, time.Now)
	rf, err := roots.Create(r.ctx, dir, library.KindMusic, "Music on disk")
	if err != nil {
		t.Fatal(err)
	}
	r.svc = NewService(r.db, r.svc.store, roots, r.cat, nil)
	res, err := r.svc.Add(r.ctx, AddRequest{MBID: radiohead, RootFolderID: rf.ID, Monitor: MonitorAll})
	if err != nil {
		t.Fatal(err)
	}
	lr := &libRig{rig: r, lib: NewLibrary(r.svc, roots, nil, time.Now), roots: roots, dir: dir, artist: res.Item.ID}
	albums, _ := r.svc.store.Albums(r.ctx, res.Item.ID)
	for _, a := range albums {
		if a.MBID == "okc" {
			lr.okc = a.ID
		}
	}
	return lr
}

// ADR-0045, decision 4.
func TestTheScanFindsFollowedArtists(t *testing.T) {
	r := newLibRig(t)
	write(t, filepath.Join(r.dir, "Radiohead", "OK Computer (1997) [FLAC]", "01 - Airbag.flac"), "a")
	write(t, filepath.Join(r.dir, "Radiohead", "OK Computer (1997) [FLAC]", "02 Paranoid Android.flac"), "b")
	write(t, filepath.Join(r.dir, "Radiohead", "OK Computer (1997) [FLAC]", "cover.jpg"), "c")
	write(t, filepath.Join(r.dir, "Radiohead", "OK Computer (1997) [FLAC]", "99 - Bonus.flac"), "d")
	write(t, filepath.Join(r.dir, "Radiohead", "Kid A", "01.flac"), "e")
	write(t, filepath.Join(r.dir, "Blur", "Parklife", "01.flac"), "f")
	write(t, filepath.Join(r.dir, "Radiohead", "loose.flac"), "g")

	scan := authz.SystemPrincipal(t.Context(), authz.TaskLibraryScan)
	before := snapshot(t, r.dir)
	res, err := r.lib.Scan(scan, mustRoot(t, r))
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 || res.Scanned != 6 {
		t.Errorf("scan: %d added of %d scanned", res.Added, res.Scanned)
	}
	reasons := map[string]string{}
	for _, s := range res.Skipped {
		reasons[s.Path] = s.Reason
	}
	for p, want := range map[string]string{
		"Radiohead/OK Computer (1997) [FLAC]/99 - Bonus.flac": ReasonNoTrack,
		"Radiohead/Kid A/01.flac":                             ReasonNoAlbum,
		"Blur/Parklife/01.flac":                               ReasonNoArtist,
		"Radiohead/loose.flac":                                ReasonNotInAlbum,
	} {
		if reasons[p] != want {
			t.Errorf("%s: %q, want %q", p, reasons[p], want)
		}
	}
	a, _ := r.svc.store.Album(r.ctx, r.okc)
	if a.Have != 2 || a.Known != 2 {
		t.Errorf("OK Computer holds %d of %d", a.Have, a.Known)
	}
	if after := snapshot(t, r.dir); after != before {
		t.Errorf("the scan changed the disk:\n%s\n%s", before, after)
	}

	// A music root is scanned by the music library, whatever else is wired.
	d := ScanDispatch{Video: refusingScanner{}, Music: r.lib, Roots: r.roots}
	if res, err := d.Scan(scan, mustRoot(t, r)); err != nil || res.Updated != 2 {
		t.Errorf("the dispatch: %+v %v", res, err)
	}

	// A books root goes to the books library (ADR-0049, decision 3).
	booksDir := filepath.Join(t.TempDir(), "books")
	if err := os.MkdirAll(booksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shelf, err := r.roots.Create(r.ctx, booksDir, library.KindBooks, "Books")
	if err != nil {
		t.Fatal(err)
	}
	d.Books = namedScanner("the books library")
	if res, err := d.Scan(scan, shelf.ID); err != nil || res.Root != "the books library" {
		t.Errorf("a books root was scanned by %q, %v", res.Root, err)
	}

	// Again: nothing new; the same two known.
	res, err = r.lib.Scan(scan, mustRoot(t, r))
	if err != nil || res.Added != 0 || res.Updated != 2 {
		t.Errorf("a second scan: %+v %v", res, err)
	}
	// One gone is reported missing; both gone, the scan refuses to believe it.
	if err := os.Remove(filepath.Join(r.dir, "Radiohead", "OK Computer (1997) [FLAC]", "01 - Airbag.flac")); err != nil {
		t.Fatal(err)
	}
	if res, err = r.lib.Scan(scan, mustRoot(t, r)); err == nil {
		t.Errorf("half the music vanished and the scan believed it: %+v", res)
	}
}

func mustRoot(t *testing.T, r *libRig) int64 {
	t.Helper()
	list, err := r.roots.List(r.ctx)
	if err != nil || len(list) == 0 {
		t.Fatalf("roots: %v", err)
	}
	for _, rf := range list {
		if rf.Kind == library.KindMusic && rf.Label == "Music on disk" {
			return rf.ID
		}
	}
	t.Fatal("no music root")
	return 0
}

func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// ADR-0045, decision 5.
func TestADownloadedAlbumIsImported(t *testing.T) {
	r := newLibRig(t)
	dl := t.TempDir()
	write(t, filepath.Join(dl, "Radiohead - OK Computer (1997) [MP3]", "01 - Airbag.mp3"), "mp3-1")
	write(t, filepath.Join(dl, "Radiohead - OK Computer (1997) [MP3]", "02 - Paranoid Android.mp3"), "mp3-2")
	write(t, filepath.Join(dl, "Radiohead - OK Computer (1997) [MP3]", "folder.jpg"), "jpg")
	write(t, filepath.Join(dl, "Radiohead - OK Computer (1997) [MP3]", "Interview.mp3"), "talk")
	src, err := library.OpenSource(dl)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	files := []string{
		"Radiohead - OK Computer (1997) [MP3]/01 - Airbag.mp3",
		"Radiohead - OK Computer (1997) [MP3]/02 - Paranoid Android.mp3",
		"Radiohead - OK Computer (1997) [MP3]/folder.jpg",
		"Radiohead - OK Computer (1997) [MP3]/Interview.mp3",
	}
	imp := authz.SystemPrincipal(t.Context(), authz.TaskImport)
	res, err := r.lib.ImportAlbum(imp, src, files, r.okc, strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Placed) != 2 || len(res.Skipped) != 1 || res.Skipped[0].Path != files[3] {
		t.Fatalf("import: %s %+v", res.Summary(), res)
	}
	placed := filepath.Join(r.dir, "Radiohead", "OK Computer (1997)", "02 - Paranoid Android.mp3")
	if b, err := os.ReadFile(placed); err != nil || string(b) != "mp3-2" {
		t.Errorf("the file was not placed: %v", err)
	}
	a, _ := r.svc.store.Album(r.ctx, r.okc)
	if a.Have != 2 {
		t.Errorf("the album holds %d tracks", a.Have)
	}

	// A lossy second copy keeps what is there; a lossless one replaces it,
	// and the replaced file is in the trash, not gone.
	dl2 := t.TempDir()
	write(t, filepath.Join(dl2, "OKC", "01 - Airbag.mp3"), "mp3-again")
	write(t, filepath.Join(dl2, "OKC", "02 - Paranoid Android.flac"), "flac-2")
	// Through ImportDownload, as a grab's download arrives (ADR-0046).
	res, err = r.lib.ImportDownload(imp, dl2, []string{"OKC/01 - Airbag.mp3", "OKC/02 - Paranoid Android.flac"},
		r.okc, strings.Repeat("b", 40))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Kept) != 1 || len(res.Replaced) != 1 || len(res.Placed) != 1 {
		t.Errorf("the second import: %s", res.Summary())
	}
	if _, err := os.Stat(placed); !os.IsNotExist(err) {
		t.Error("the replaced lossy file is still in place")
	}
	var trashed []string
	_ = filepath.Walk(filepath.Join(r.dir, ".cmediastack-trash"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			trashed = append(trashed, filepath.Base(p))
		}
		return nil
	})
	if len(trashed) != 1 || !strings.Contains(trashed[0], "Paranoid Android") {
		t.Errorf("the trash holds %q", trashed)
	}
	flac := filepath.Join(r.dir, "Radiohead", "OK Computer (1997)", "02 - Paranoid Android.flac")
	if b, err := os.ReadFile(flac); err != nil || string(b) != "flac-2" {
		t.Errorf("the lossless file was not placed: %v", err)
	}
}

// namedScanner answers every scan with its own name as the root.
type namedScanner string

func (n namedScanner) Scan(context.Context, int64) (importer.ScanResult, error) {
	return importer.ScanResult{Root: string(n)}, nil
}

type refusingScanner struct{}

func (refusingScanner) Scan(context.Context, int64) (importer.ScanResult, error) {
	return importer.ScanResult{}, errors.New("the video importer was asked to scan music")
}

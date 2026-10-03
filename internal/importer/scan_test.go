package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/library"
)

// put writes a file into an existing library, as an operator's collection would
// already have it — under their layout, not ours.
func (r *rig) put(root, rel string, size int64) {
	r.t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// snapshot fingerprints an entire tree: every path, its size, and its content
// hash. Two snapshots differing means something on disk changed.
func snapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if d.IsDir() {
			out = append(out, "d "+rel)
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(b)
		out = append(out, fmt.Sprintf("f %s %d %s", rel, len(b), hex.EncodeToString(sum[:8])))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// The rule a scan must never break.
//
// Some software in this category "organises on scan", and that is how people
// lose libraries: a parser misreads a title, a thousand directories are
// renamed, and there is no undo. The layout an operator chose is the layout
// that stays.
func TestAScanNeverChangesAnythingOnDisk(t *testing.T) {
	r := newRig(t)

	// A messy, realistic existing library: our layout, somebody else's layout,
	// odd names, extras, artwork, a nested season folder.
	r.put(r.movies, "Blade Runner 2049 (2017)/Blade Runner 2049 (2017) [Bluray-1080p].mkv", 20*mib)
	r.put(r.movies, "blade runner 2049 2017 1080p/brunner.2049.mkv", 20*mib)
	r.put(r.movies, "Arrival.2016.2160p.UHD.BluRay-GRP/Arrival.2016.2160p.mkv", 20*mib)
	r.put(r.movies, "Arrival.2016.2160p.UHD.BluRay-GRP/poster.jpg", 40*kib)
	r.put(r.movies, "Arrival.2016.2160p.UHD.BluRay-GRP/movie.nfo", 2*kib)
	r.put(r.movies, "Arrival.2016.2160p.UHD.BluRay-GRP/Extras/deleted-scene.mkv", 20*mib)
	r.put(r.movies, "loose.film.2001.1080p.WEB-DL.mkv", 20*mib)

	before := snapshot(t, r.movies)

	res, err := r.imp.Scan(r.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added == 0 {
		t.Fatal("the scan recorded nothing, so this proves little")
	}

	after := snapshot(t, r.movies)
	if len(before) != len(after) {
		t.Fatalf("the scan changed the tree: %d entries before, %d after", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Errorf("the scan changed the tree:\n  before: %s\n  after:  %s", before[i], after[i])
		}
	}
}

// An operator adopting this software already HAS a library. A scan that cannot
// see it makes a working instance look empty beside forty terabytes of media.
func TestAScanAdoptsAnExistingLibraryWhereItStands(t *testing.T) {
	r := newRig(t)
	r.put(r.movies, "Arrival.2016.2160p.UHD.BluRay-GRP/Arrival.2016.2160p.mkv", 20*mib)
	r.put(r.movies, "Blade Runner 2049 (2017)/Blade Runner 2049 (2017) [Bluray-1080p].mkv", 20*mib)

	res, err := r.imp.Scan(r.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 {
		t.Fatalf("added = %d, want 2: %+v", res.Added, res.Skipped)
	}

	items, err := r.store.ListItems(r.ctx, KindMovie)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d: %+v", len(items), items)
	}

	// The recorded path is the operator's, not one we would have chosen.
	files, err := r.store.FilesFor(r.ctx, items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %d", len(files))
	}
	if !strings.HasPrefix(files[0].RelPath, items[0].Folder+"/") {
		t.Errorf("the recorded path %q is not under the item's own folder %q",
			files[0].RelPath, items[0].Folder)
	}
}

// THE guard. If a mount fails, the root looks empty — and a scan that dutifully
// marked everything missing would erase the record of every file on that disk
// in one pass, with nothing on disk changed to explain it.
func TestAVanishedLibraryIsRefusedRatherThanRecorded(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 10; i++ {
		r.put(r.movies, fmt.Sprintf("Film %d (201%d)/film.mkv", i, i), 20*mib)
	}
	if res, err := r.imp.Scan(r.ctx, 1); err != nil || res.Added != 10 {
		t.Fatalf("first scan: %v %+v", err, res)
	}

	// The disk "unmounts": everything disappears at once.
	entries, _ := os.ReadDir(r.movies)
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(r.movies, e.Name())); err != nil {
			t.Fatal(err)
		}
	}

	res, err := r.imp.Scan(r.ctx, 1)
	if !errors.Is(err, ErrLibraryVanished) {
		t.Fatalf("err = %v, want ErrLibraryVanished", err)
	}
	// The message has to point at the real cause, or an operator will act on
	// the wrong one.
	if !strings.Contains(err.Error(), "unmounted") {
		t.Errorf("the refusal does not name the likely cause: %v", err)
	}
	// And nothing was forgotten: the records are still there to recover.
	items, _ := r.store.ListItems(r.ctx, KindMovie)
	if len(items) != 10 {
		t.Errorf("items = %d, want the 10 still recorded", len(items))
	}
	_ = res
}

// One or two files genuinely deleted is ordinary. Only a wholesale
// disappearance is suspicious, and the guard must not fire on the ordinary case.
func TestAFewDeletedFilesAreReportedNotRefused(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 10; i++ {
		r.put(r.movies, fmt.Sprintf("Film %d (201%d)/film.mkv", i, i), 20*mib)
	}
	if _, err := r.imp.Scan(r.ctx, 1); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(filepath.Join(r.movies, "Film 3 (2013)")); err != nil {
		t.Fatal(err)
	}

	res, err := r.imp.Scan(r.ctx, 1)
	if err != nil {
		t.Fatalf("a single deletion was refused: %v", err)
	}
	if len(res.Missing) != 1 {
		t.Errorf("missing = %v, want 1", res.Missing)
	}
	if !strings.Contains(res.Summary(), "no longer on disk") {
		t.Errorf("the summary does not mention it: %q", res.Summary())
	}
}

// A symlink inside a library pointing out of it must not be indexed, or the
// library ends up holding paths the kernel will refuse to open — or worse,
// somebody's /etc.
func TestAScanDoesNotFollowSymlinksOutOfTheRoot(t *testing.T) {
	r := newRig(t)
	outside := filepath.Join(filepath.Dir(r.movies), "private")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.mkv"), make([]byte, 20*mib), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(r.movies, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(r.movies, "etc")); err != nil {
		t.Fatal(err)
	}
	r.put(r.movies, "Real Film (2019)/film.mkv", 20*mib)

	res, err := r.imp.Scan(r.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Fatalf("added = %d, want only the real film", res.Added)
	}

	items, _ := r.store.ListItems(r.ctx, KindMovie)
	for _, it := range items {
		files, _ := r.store.FilesFor(r.ctx, it.ID)
		for _, f := range files {
			if strings.Contains(f.RelPath, "escape") || strings.Contains(f.RelPath, "etc") {
				t.Errorf("the scan indexed something through a symlink: %q", f.RelPath)
			}
		}
	}
}

// The trash holds files an operator's own upgrades superseded. Descending would
// re-import their upgrade history as new media.
func TestAScanSkipsTheTrashAndHousekeepingFolders(t *testing.T) {
	r := newRig(t)
	r.put(r.movies, "Real Film (2019)/film.mkv", 20*mib)
	r.put(r.movies, library.TrashDir+"/20260101-000000.000-Old Film (2019).mkv", 20*mib)
	r.put(r.movies, "@eaDir/thumb.mkv", 20*mib)
	r.put(r.movies, ".Trash-1000/deleted.mkv", 20*mib)
	r.put(r.movies, "lost+found/orphan.mkv", 20*mib)

	res, err := r.imp.Scan(r.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Errorf("added = %d, want 1 — housekeeping folders were indexed", res.Added)
	}
	if res.Scanned != 1 {
		t.Errorf("scanned = %d, want 1", res.Scanned)
	}
}

// Samples and extras are as common in an existing library as in a download, and
// they are as wrong to index.
func TestAScanSkipsSamplesAndTinyFiles(t *testing.T) {
	r := newRig(t)
	r.put(r.movies, "Film (2019)/Film (2019).mkv", 20*mib)
	r.put(r.movies, "Film (2019)/Sample/sample.mkv", 20*mib)
	r.put(r.movies, "Film (2019)/Film (2019)-trailer.mkv", 20*mib)
	r.put(r.movies, "Film (2019)/tiny.mkv", 1*kib)

	res, err := r.imp.Scan(r.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Fatalf("added = %d, want 1: %+v", res.Added, res.Skipped)
	}
	reasons := map[string]int{}
	for _, s := range res.Skipped {
		reasons[s.Reason]++
	}
	if reasons[ReasonSampleByName] != 2 {
		t.Errorf("sample rejections = %d, want 2: %+v", reasons[ReasonSampleByName], res.Skipped)
	}
	if reasons[ReasonTooSmall] != 1 {
		t.Errorf("too-small rejections = %d: %+v", reasons[ReasonTooSmall], res.Skipped)
	}
}

// An existing library is full of .nfo and .jpg. Reporting every one would bury
// the single genuinely odd file in a result thousands of lines long.
func TestArtworkAndMetadataFilesAreNotReportedAsProblems(t *testing.T) {
	r := newRig(t)
	r.put(r.movies, "Film (2019)/Film (2019).mkv", 20*mib)
	for _, quiet := range []string{"poster.jpg", "fanart.png", "movie.nfo", "film.srt", "x.sfv"} {
		r.put(r.movies, "Film (2019)/"+quiet, 4*kib)
	}
	r.put(r.movies, "Film (2019)/suspicious.exe", 4*kib)

	res, err := r.imp.Scan(r.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("skipped = %+v, want only the executable", res.Skipped)
	}
	if !strings.HasSuffix(res.Skipped[0].Path, "suspicious.exe") {
		t.Errorf("the one reported file is %q", res.Skipped[0].Path)
	}
}

// Episodes in a series root get their season and episode read from the
// filename, which is what an existing library has.
func TestAScanReadsSeasonsAndEpisodesFromAnExistingLayout(t *testing.T) {
	r := newRig(t)
	r.put(r.series, "The Expanse (2015)/Season 02/The.Expanse.S02E05.1080p.WEB-DL-GRP.mkv", 20*mib)
	r.put(r.series, "The Expanse (2015)/Season 02/The.Expanse.S02E06.1080p.WEB-DL-GRP.mkv", 20*mib)

	res, err := r.imp.Scan(r.ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 2 {
		t.Fatalf("added = %d: %+v", res.Added, res.Skipped)
	}

	items, err := r.store.ListItems(r.ctx, KindSeries)
	if err != nil {
		t.Fatal(err)
	}
	// Both episodes belong to ONE series, because the show's own folder is the
	// item — not the season folder beneath it.
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 series: %+v", len(items), items)
	}
	files, _ := r.store.FilesFor(r.ctx, items[0].ID)
	if len(files) != 2 {
		t.Fatalf("files = %d", len(files))
	}
	for _, f := range files {
		if f.Season == nil || *f.Season != 2 {
			t.Errorf("season = %v for %q", f.Season, f.RelPath)
		}
		if f.Episode == nil {
			t.Errorf("no episode for %q", f.RelPath)
		}
	}
}

// Scanning twice must not duplicate anything, or a scheduled scan grows the
// library without bound.
func TestScanningTwiceChangesNothing(t *testing.T) {
	r := newRig(t)
	r.put(r.movies, "Film (2019)/Film (2019).mkv", 20*mib)

	first, err := r.imp.Scan(r.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.imp.Scan(r.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Added != 1 || second.Added != 0 || second.Updated != 1 {
		t.Errorf("first %+v second %+v", first, second)
	}

	items, _ := r.store.ListItems(r.ctx, KindMovie)
	if len(items) != 1 {
		t.Errorf("items = %d after two scans", len(items))
	}
}

// A file whose name says nothing is reported rather than filed under a guess.
func TestAnUnreadableNameIsReportedNotGuessed(t *testing.T) {
	r := newRig(t)
	r.put(r.movies, "1080p.mkv", 20*mib)

	res, err := r.imp.Scan(r.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 0 {
		t.Errorf("a nameless file was filed under a guess")
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("skipped = %+v", res.Skipped)
	}
}

// A thin filename inside an informative folder is extremely common in an
// existing library. The folder says what the item IS; the filename says which
// file it is. Using one for both loses information the library already has.
//
// Both cases below came from scanning a real library against the binary.
func TestTheFolderNamesTheItemAndTheFilenameNamesTheFile(t *testing.T) {
	r := newRig(t)
	// A thin filename under a folder that carries the title and year.
	r.put(r.movies, "the.matrix.1999.720p.brrip/matrix.mkv", 20*mib)
	// A film whose folder is bare and whose filename carries everything.
	r.put(r.movies, "unsorted/Arrival.2016.2160p.UHD.BluRay-GRP.mkv", 20*mib)

	if _, err := r.imp.Scan(r.ctx, 1); err != nil {
		t.Fatal(err)
	}
	items, err := r.store.ListItems(r.ctx, KindMovie)
	if err != nil {
		t.Fatal(err)
	}

	byFolder := map[string]Item{}
	for _, it := range items {
		byFolder[it.Folder] = it
	}

	m := byFolder["the.matrix.1999.720p.brrip"]
	if !strings.EqualFold(m.Title, "The Matrix") {
		t.Errorf("title = %q, want it read from the folder", m.Title)
	}
	if m.Year != 1999 {
		t.Errorf("year = %d, want 1999 from the folder", m.Year)
	}

	// And when the folder says nothing useful, the filename still carries it.
	a := byFolder["unsorted"]
	if a.Year != 2016 && a.Title == "" {
		t.Errorf("a bare folder lost the filename's information: %+v", a)
	}
}

// A series' year lives on its folder, never on an episode filename.
func TestASeriesKeepsItsYearFromTheShowFolder(t *testing.T) {
	r := newRig(t)
	r.put(r.series, "The Expanse (2015)/Season 02/The.Expanse.S02E05.1080p.WEB-DL-GRP.mkv", 20*mib)

	if _, err := r.imp.Scan(r.ctx, 2); err != nil {
		t.Fatal(err)
	}
	items, err := r.store.ListItems(r.ctx, KindSeries)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	if items[0].Year != 2015 {
		t.Errorf("year = %d, want 2015 from the show's folder", items[0].Year)
	}
	if !strings.EqualFold(items[0].Title, "The Expanse") {
		t.Errorf("title = %q", items[0].Title)
	}

	// The FILE still carries what only the filename knows.
	files, _ := r.store.FilesFor(r.ctx, items[0].ID)
	if len(files) != 1 || files[0].Season == nil || *files[0].Season != 2 {
		t.Errorf("the file lost its season: %+v", files)
	}
	if files[0].Quality == "" || files[0].Quality == "Unknown" {
		t.Errorf("the file lost its quality: %q", files[0].Quality)
	}
}

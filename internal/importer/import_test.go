package importer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// ---------------------------------------------------------------------------
// harness — real files, real hardlinks, real roots
// ---------------------------------------------------------------------------

type rig struct {
	t         *testing.T
	store     *Store
	roots     *library.RootStore
	imp       *Importer
	downloads string
	movies    string
	series    string
	ctx       context.Context
}

func adminCtx() context.Context {
	var admin authz.Role
	for _, r := range authz.BuiltinRoles() {
		if r.Name == authz.RoleAdmin {
			admin = r
		}
	}
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 1, Username: "jacob", Role: admin,
		State: authz.StateActive, MFASatisfied: true,
	})
}

func newRig(t *testing.T) *rig {
	t.Helper()
	base := t.TempDir()
	downloads := filepath.Join(base, "downloads")
	movies := filepath.Join(base, "media", "movies")
	series := filepath.Join(base, "media", "series")
	for _, d := range []string{downloads, movies, series} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	database, err := db.Open(db.Options{Path: filepath.Join(base, "lib.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx := adminCtx()
	roots := library.NewRootStore(database, downloads, nil)
	if _, err := roots.Create(ctx, movies, library.KindMovies, "Films"); err != nil {
		t.Fatal(err)
	}
	if _, err := roots.Create(ctx, series, library.KindSeries, "Shows"); err != nil {
		t.Fatal(err)
	}

	store := NewStore(database, nil)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &rig{
		t: t, store: store, roots: roots, imp: New(store, roots, quiet, nil),
		downloads: downloads, movies: movies, series: series, ctx: ctx,
	}
}

// download writes a real directory of real files and returns a Source.
func (r *rig) download(hash, releaseTitle string, files map[string]int64) Source {
	r.t.Helper()
	dir := filepath.Join(r.downloads, hash)
	var cands []Candidate
	for rel, size := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(full, make([]byte, size), 0o644); err != nil {
			r.t.Fatal(err)
		}
		cands = append(cands, Candidate{Path: rel, Bytes: size})
	}
	return Source{InfoHash: hash, Dir: dir, ReleaseTitle: releaseTitle, Files: cands}
}

func hash(n byte) string { return strings.Repeat(string('a'+rune(n%6)), 40) }

// ---------------------------------------------------------------------------

// The whole chain: a completed download becomes a library entry, hardlinked so
// seeding continues, named where any media server would look for it.
func TestAFilmIsImportedHardlinkedAndNamed(t *testing.T) {
	r := newRig(t)
	src := r.download(hash(0), "Blade Runner 2049 2017 1080p BluRay x264-GROUP", map[string]int64{
		"Blade.Runner.2049.2017.1080p.BluRay.x264-GROUP.mkv": 20 * mib,
		"Sample/sample.mkv":  1 * mib,
		"readme.nfo":         512,
		"Subs/2_English.srt": 4 * kib,
	})

	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeImported {
		t.Fatalf("outcome = %q: %s", res.Outcome, res.Detail)
	}

	want := filepath.Join(r.movies,
		"Blade Runner 2049 (2017)",
		"Blade Runner 2049 (2017) [Bluray-1080p].mkv")
	fi, err := os.Stat(want)
	if err != nil {
		t.Fatalf("the film is not where it should be: %v", err)
	}

	// Hardlinked: the same inode, so the torrent client is still serving it and
	// the library cost no extra disk.
	srcInfo, err := os.Stat(filepath.Join(src.Dir, "Blade.Runner.2049.2017.1080p.BluRay.x264-GROUP.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(srcInfo, fi) {
		t.Error("the imported file is not the same inode as the download")
	}
	if !res.Hardlinked {
		t.Error("the result does not report a hardlink")
	}

	// The subtitle came too, named to match so a player finds it.
	sub := filepath.Join(r.movies, "Blade Runner 2049 (2017)",
		"Blade Runner 2049 (2017) [Bluray-1080p].en.srt")
	if _, err := os.Stat(sub); err != nil {
		t.Errorf("the subtitle is not beside the film: %v", err)
	}

	// And the sample and the .nfo did not.
	entries, _ := os.ReadDir(filepath.Join(r.movies, "Blade Runner 2049 (2017)"))
	if len(entries) != 2 {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the folder holds %v, want only the film and its subtitle", names)
	}
}

func TestAnEpisodeLandsInItsSeasonFolder(t *testing.T) {
	r := newRig(t)
	src := r.download(hash(1), "The.Expanse.S02E05.1080p.WEB-DL.DD5.1.H264-GRP", map[string]int64{
		"The.Expanse.S02E05.1080p.WEB-DL.DD5.1.H264-GRP.mkv": 20 * mib,
	})

	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeImported {
		t.Fatalf("outcome = %q: %s", res.Outcome, res.Detail)
	}

	matches, _ := filepath.Glob(filepath.Join(r.series, "The Expanse*", "Season 02", "*S02E05*"))
	if len(matches) != 1 {
		t.Fatalf("episode files found: %v", matches)
	}
	if res.File.Season == nil || *res.File.Season != 2 {
		t.Errorf("season = %v", res.File.Season)
	}
}

// A download with nothing importable is a SKIP with a reason, never a silent
// nothing. "It downloaded and then nothing happened" is the complaint this
// whole package is shaped around.
func TestAnUnimportableDownloadIsRecordedWithAReason(t *testing.T) {
	r := newRig(t)
	src := r.download(hash(2), "Some.Release-GRP", map[string]int64{
		"keygen.exe": 40 * kib,
		"readme.txt": 1 * kib,
	})

	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeSkipped {
		t.Fatalf("outcome = %q", res.Outcome)
	}

	records, err := r.store.RecordsFor(r.ctx, src.InfoHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Detail == "" {
		t.Error("the record carries no reason")
	}
	if !strings.Contains(records[0].Detail, "keygen.exe") {
		t.Errorf("the reason does not name what was examined: %q", records[0].Detail)
	}
}

// Nothing that is not a video container reaches the library, whatever it is
// called or how large.
func TestAnExecutableNeverReachesTheLibrary(t *testing.T) {
	r := newRig(t)
	src := r.download(hash(3), "Movie.2019.1080p.BluRay-GRP", map[string]int64{
		"Movie.2019.1080p.BluRay-GRP.exe": 30 * mib,
		"Movie.2019.1080p.BluRay-GRP.mkv": 20 * mib,
	})

	if _, err := r.imp.Import(r.ctx, src); err != nil {
		t.Fatal(err)
	}

	var found []string
	_ = filepath.Walk(r.movies, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			found = append(found, filepath.Base(p))
		}
		return nil
	})
	for _, f := range found {
		if strings.HasSuffix(f, ".exe") {
			t.Errorf("an executable reached the library: %q", f)
		}
	}
	if len(found) != 1 {
		t.Errorf("library holds %v, want just the film", found)
	}
}

// A better release replaces a worse one; a worse one does not replace a better.
func TestUpgradesReplaceAndDowngradesDoNot(t *testing.T) {
	r := newRig(t)

	// 720p first.
	first := r.download(hash(0), "Film.2019.720p.WEB-DL.x264-GRP", map[string]int64{
		"Film.2019.720p.WEB-DL.x264-GRP.mkv": 20 * mib,
	})
	if res, err := r.imp.Import(r.ctx, first); err != nil || res.Outcome != OutcomeImported {
		t.Fatalf("first import: %v %+v", err, res)
	}

	// A 1080p Bluray is better: it imports.
	better := r.download(hash(1), "Film.2019.1080p.BluRay.x264-GRP", map[string]int64{
		"Film.2019.1080p.BluRay.x264-GRP.mkv": 30 * mib,
	})
	res, err := r.imp.Import(r.ctx, better)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeImported {
		t.Fatalf("an upgrade was skipped: %s", res.Detail)
	}

	// An SDTV rip is worse: it is skipped, and the reason says so.
	worse := r.download(hash(2), "Film.2019.SDTV.x264-GRP", map[string]int64{
		"Film.2019.SDTV.x264-GRP.mkv": 10 * mib,
	})
	res, err = r.imp.Import(r.ctx, worse)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeSkipped {
		t.Fatalf("a downgrade was imported: %+v", res)
	}
	if !strings.Contains(res.Detail, "better quality") {
		t.Errorf("the skip reason is unclear: %q", res.Detail)
	}
}

// A PROPER of the same quality IS an upgrade: it is the group saying the
// earlier release was defective.
func TestAProperOfTheSameQualityReplacesTheOriginal(t *testing.T) {
	r := newRig(t)

	first := r.download(hash(0), "Film.2019.1080p.BluRay.x264-GRP", map[string]int64{
		"Film.2019.1080p.BluRay.x264-GRP.mkv": 20 * mib,
	})
	if _, err := r.imp.Import(r.ctx, first); err != nil {
		t.Fatal(err)
	}

	proper := r.download(hash(1), "Film.2019.1080p.BluRay.PROPER.x264-GRP", map[string]int64{
		"Film.2019.1080p.BluRay.PROPER.x264-GRP.mkv": 20 * mib,
	})
	res, err := r.imp.Import(r.ctx, proper)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeImported {
		t.Fatalf("a PROPER was skipped: %s", res.Detail)
	}

	// The same quality without a PROPER is not.
	same := r.download(hash(2), "Film.2019.1080p.BluRay.x264-OTHER", map[string]int64{
		"Film.2019.1080p.BluRay.x264-OTHER.mkv": 20 * mib,
	})
	res, err = r.imp.Import(r.ctx, same)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeSkipped {
		t.Errorf("a same-quality non-PROPER replaced the original: %+v", res)
	}
}

// Libraries lose files to manual tidying, failed disks and restored backups.
// Refusing to re-import into that gap is how a library stays broken.
func TestARecordedFileMissingFromDiskIsReImported(t *testing.T) {
	r := newRig(t)
	src := r.download(hash(0), "Film.2019.1080p.BluRay.x264-GRP", map[string]int64{
		"Film.2019.1080p.BluRay.x264-GRP.mkv": 20 * mib,
	})
	res, err := r.imp.Import(r.ctx, src)
	if err != nil || res.Outcome != OutcomeImported {
		t.Fatalf("first import: %v %+v", err, res)
	}

	// The operator deletes it by hand.
	if err := os.Remove(filepath.Join(r.movies, res.File.RelPath)); err != nil {
		t.Fatal(err)
	}

	again := r.download(hash(1), "Film.2019.1080p.BluRay.x264-GRP", map[string]int64{
		"Film.2019.1080p.BluRay.x264-GRP.mkv": 20 * mib,
	})
	res2, err := r.imp.Import(r.ctx, again)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Outcome != OutcomeImported {
		t.Fatalf("a gap in the library was not refilled: %s", res2.Detail)
	}
}

// Importing does not consume the download: the torrent client is still seeding
// it, and on a private tracker breaking that is how an account is lost.
func TestImportingNeverConsumesTheDownload(t *testing.T) {
	r := newRig(t)
	src := r.download(hash(0), "Film.2019.1080p.BluRay.x264-GRP", map[string]int64{
		"Film.2019.1080p.BluRay.x264-GRP.mkv": 20 * mib,
		"Sample/sample.mkv":                   1 * mib,
	})

	if _, err := r.imp.Import(r.ctx, src); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"Film.2019.1080p.BluRay.x264-GRP.mkv", "Sample/sample.mkv"} {
		if _, err := os.Stat(filepath.Join(src.Dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("the download lost %q: %v", rel, err)
		}
	}
}

// A hostile torrent cannot place anything outside the library, whatever it
// declares its files to be called.
func TestAHostileDownloadCannotEscapeTheLibrary(t *testing.T) {
	r := newRig(t)
	outside := filepath.Join(filepath.Dir(r.movies), "..", "escaped.mkv")

	// The file on disk is ordinary; the DECLARED title is the attack.
	src := r.download(hash(0), "../../../../escaped 2019 1080p BluRay-GRP", map[string]int64{
		"film.mkv": 20 * mib,
	})
	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Logf("import refused: %v", err)
	}
	if _, serr := os.Stat(outside); serr == nil {
		t.Fatal("a file was placed outside the library")
	}
	if res.Outcome == OutcomeImported {
		// Importing is fine as long as it landed INSIDE.
		full := filepath.Join(r.movies, res.File.RelPath)
		if !strings.HasPrefix(filepath.Clean(full), filepath.Clean(r.movies)) {
			t.Errorf("the file landed outside the root: %q", full)
		}
	}
}

// Nothing is configured for this kind of media: an honest failure naming what
// is missing, not a silent nothing.
func TestNoRootFolderIsAClearFailure(t *testing.T) {
	base := t.TempDir()
	database, err := db.Open(db.Options{Path: filepath.Join(base, "l.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}

	roots := library.NewRootStore(database, filepath.Join(base, "dl"), nil)
	store := NewStore(database, nil)
	imp := New(store, roots, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	src := Source{
		InfoHash: hash(0), Dir: base,
		ReleaseTitle: "Film.2019.1080p.BluRay-GRP",
		Files:        []Candidate{{"film.mkv", 20 * mib}},
	}
	res, err := imp.Import(adminCtx(), src)
	if !errors.Is(err, ErrNoRootFolder) {
		t.Fatalf("err = %v, want ErrNoRootFolder", err)
	}
	if res.Outcome != OutcomeFailed {
		t.Errorf("outcome = %q", res.Outcome)
	}

	// And it was recorded, so an operator can find out why nothing happened.
	records, _ := store.RecordsFor(adminCtx(), src.InfoHash)
	if len(records) != 1 || !strings.Contains(records[0].Detail, "root folder") {
		t.Errorf("records = %+v", records)
	}
}

// The completion poll fires every thirty seconds. Without this the same file
// would be re-imported forever.
func TestASecondImportOfTheSameDownloadIsNotRepeated(t *testing.T) {
	r := newRig(t)
	src := r.download(hash(0), "Film.2019.1080p.BluRay.x264-GRP", map[string]int64{
		"Film.2019.1080p.BluRay.x264-GRP.mkv": 20 * mib,
	})
	if _, err := r.imp.Import(r.ctx, src); err != nil {
		t.Fatal(err)
	}

	done, err := r.store.AlreadyImported(r.ctx, src.InfoHash)
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Error("a completed import is not recorded as done")
	}
}

// Two releases whose titles differ only in punctuation are one item pointing at
// one directory, not two items each believing they own it.
func TestTwoReleasesOfOneFilmShareOneItem(t *testing.T) {
	r := newRig(t)

	for i, title := range []string{
		"Film.2019.720p.WEB-DL-GRP",
		"Film 2019 1080p BluRay x264-OTHER",
	} {
		src := r.download(hash(byte(i)), title, map[string]int64{
			"file.mkv": int64(20+i) * mib,
		})
		if _, err := r.imp.Import(r.ctx, src); err != nil {
			t.Fatal(err)
		}
	}

	items, err := r.store.ListItems(r.ctx, KindMovie)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1: %+v", len(items), items)
	}
}

// The vulnerability a structural test caught, kept as a regression.
//
// A torrent's file list is written by the uploader. Joining an entry to the
// download directory unchecked does not produce a path under it — filepath.Join
// CLEANS, so "../../../../etc/shadow" becomes /etc/shadow — and hardlinking
// that into a media library puts it somewhere this software serves over HTTP.
func TestADownloadCannotNameAFileOutsideItself(t *testing.T) {
	r := newRig(t)

	// A real secret outside the download, standing in for /etc/shadow.
	secret := filepath.Join(filepath.Dir(r.downloads), "secret.mkv")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(r.downloads, hash(0))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink planted inside the download, which is the version a prefix
	// check does not catch.
	if err := os.Symlink(filepath.Dir(r.downloads), filepath.Join(dir, "up")); err != nil {
		t.Fatal(err)
	}

	for _, declared := range []string{
		"../secret.mkv",
		"../../secret.mkv",
		"up/secret.mkv",
		"/etc/hostname",
	} {
		src := Source{
			InfoHash: hash(0), Dir: dir,
			ReleaseTitle: "Film.2019.1080p.BluRay-GRP",
			Files:        []Candidate{{Path: declared, Bytes: 20 * mib}},
		}
		res, err := r.imp.Import(r.ctx, src)
		if res.Outcome == OutcomeImported {
			t.Errorf("a download naming %q was imported", declared)
		}
		if err == nil && res.Outcome == OutcomeFailed {
			continue
		}
		_ = err
	}

	// Nothing from outside reached the library.
	var found []string
	_ = filepath.Walk(r.movies, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			if strings.Contains(string(b), "SECRET") {
				found = append(found, p)
			}
		}
		return nil
	})
	if len(found) > 0 {
		t.Fatalf("a file from outside the download reached the library: %v", found)
	}
}

// §2 asks for destructive operations to be reversible. An upgrade does not
// delete what it replaces — it moves it to trash inside the same root, so an
// operator who disagrees gets their file back.
func TestAnUpgradeIsReversible(t *testing.T) {
	r := newRig(t)

	first := r.download(hash(0), "Film.2019.720p.WEB-DL.x264-GRP", map[string]int64{
		"Film.2019.720p.WEB-DL.x264-GRP.mkv": 20 * mib,
	})
	res1, err := r.imp.Import(r.ctx, first)
	if err != nil || res1.Outcome != OutcomeImported {
		t.Fatalf("first: %v %+v", err, res1)
	}
	oldPath := res1.File.RelPath

	better := r.download(hash(1), "Film.2019.1080p.BluRay.x264-GRP", map[string]int64{
		"Film.2019.1080p.BluRay.x264-GRP.mkv": 30 * mib,
	})
	res2, err := r.imp.Import(r.ctx, better)
	if err != nil || res2.Outcome != OutcomeImported {
		t.Fatalf("upgrade: %v %+v", err, res2)
	}

	// The old file is gone from its place...
	if _, err := os.Stat(filepath.Join(r.movies, oldPath)); err == nil {
		t.Error("the superseded file is still in the library alongside its replacement")
	}
	// ...but its BYTES survive in trash, which is what makes this reversible.
	trash := filepath.Join(r.movies, library.TrashDir)
	entries, err := os.ReadDir(trash)
	if err != nil {
		t.Fatalf("no trash folder was created: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("trash holds %d entries, want 1", len(entries))
	}
	if !strings.Contains(res2.Detail, "moved to") {
		t.Errorf("the result does not say where the old file went: %q", res2.Detail)
	}

	// And the library records exactly one file for the item, not two.
	files, err := r.store.FilesFor(r.ctx, res2.Item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Errorf("the item holds %d file records, want 1: %+v", len(files), files)
	}
	if files[0].Quality != "Bluray-1080p" {
		t.Errorf("the surviving record is %q", files[0].Quality)
	}
}

// The importer runs as background work, which holds no authority to destroy
// media bytes. That is what makes the trash design load-bearing rather than a
// nicety: with destroy authority it could simply unlink, and it cannot.
func TestTheImporterWorksUnderASystemPrincipalThatCannotDelete(t *testing.T) {
	r := newRig(t)
	sysCtx := authz.SystemPrincipal(context.Background(), authz.TaskImport)

	// It cannot destroy bytes.
	if err := authz.RequireEffect(sysCtx, authz.EffectDestroyMediaBytes, "/x"); err == nil {
		t.Fatal("background work can destroy media bytes")
	}

	first := r.download(hash(0), "Film.2019.720p.WEB-DL-GRP", map[string]int64{
		"Film.2019.720p.WEB-DL-GRP.mkv": 20 * mib,
	})
	if res, err := r.imp.Import(sysCtx, first); err != nil || res.Outcome != OutcomeImported {
		t.Fatalf("the importer could not run as background work: %v %+v", err, res)
	}

	// Including the upgrade path, which is the one that touches an existing file.
	better := r.download(hash(1), "Film.2019.1080p.BluRay-GRP", map[string]int64{
		"Film.2019.1080p.BluRay-GRP.mkv": 30 * mib,
	})
	res, err := r.imp.Import(sysCtx, better)
	if err != nil || res.Outcome != OutcomeImported {
		t.Fatalf("an upgrade failed under background authority: %v %+v", err, res)
	}
}

// A same-quality PROPER lands on the identical filename. Without superseding
// first the link fails with "file exists" and the upgrade silently never
// happens — which is how this was found.
func TestASameQualityProperReplacesInPlace(t *testing.T) {
	r := newRig(t)

	first := r.download(hash(0), "Film.2019.1080p.BluRay.x264-GRP", map[string]int64{
		"Film.2019.1080p.BluRay.x264-GRP.mkv": 20 * mib,
	})
	res1, err := r.imp.Import(r.ctx, first)
	if err != nil || res1.Outcome != OutcomeImported {
		t.Fatalf("first: %v %+v", err, res1)
	}

	proper := r.download(hash(1), "Film.2019.1080p.BluRay.PROPER.x264-GRP", map[string]int64{
		"Film.2019.1080p.BluRay.PROPER.x264-GRP.mkv": 21 * mib,
	})
	res2, err := r.imp.Import(r.ctx, proper)
	if err != nil || res2.Outcome != OutcomeImported {
		t.Fatalf("the PROPER did not replace: %v %+v", err, res2)
	}
	// Same path, and the file there is now the PROPER's bytes.
	if res2.File.RelPath != res1.File.RelPath {
		t.Errorf("paths differ: %q vs %q", res1.File.RelPath, res2.File.RelPath)
	}
	fi, err := os.Stat(filepath.Join(r.movies, res2.File.RelPath))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != 21*mib {
		t.Errorf("the file in place is %d bytes, want the PROPER's %d", fi.Size(), 21*mib)
	}
}

// The trash folder is inside the root, so a library scan must know to skip it —
// otherwise superseded files get re-imported as if they were new.
func TestTheTrashFolderIsInsideTheRootAndNamedRecognisably(t *testing.T) {
	if !strings.HasPrefix(library.TrashDir, ".") {
		t.Errorf("the trash folder %q is not hidden", library.TrashDir)
	}
	if strings.ContainsAny(library.TrashDir, `/\`) {
		t.Errorf("the trash folder %q is more than one component", library.TrashDir)
	}
}

// ---------------------------------------------------------------------------
// Closing the request that asked for it
// ---------------------------------------------------------------------------

// fakeCloser records what the importer handed over, including the calls that
// closed nothing.
//
// Recording EVERY call rather than only the successful ones is the shape a
// previous test in this project got wrong: a recorder appended to only on
// success passes whether or not the code under test ever runs.
type fakeCloser struct {
	calls  []string
	items  []int64
	closed int64
	err    error
}

func (f *fakeCloser) FulfilFromImport(_ context.Context, infoHash string, itemID int64) (int64, error) {
	f.calls = append(f.calls, infoHash)
	f.items = append(f.items, itemID)
	return f.closed, f.err
}

func TestAnImportClosesTheRequestThatAskedForIt(t *testing.T) {
	r := newRig(t)
	closer := &fakeCloser{closed: 1}
	r.imp.SetRequestCloser(closer)

	src := r.download(hash(0), "Film.2019.1080p.BluRay.x264-GRP", map[string]int64{
		"Film.2019.1080p.BluRay.x264-GRP.mkv": 20 * mib,
	})
	res, err := r.imp.Import(r.ctx, src)
	if err != nil || res.Outcome != OutcomeImported {
		t.Fatalf("import: %v %s %s", err, res.Outcome, res.Detail)
	}

	if len(closer.calls) != 1 || closer.calls[0] != src.InfoHash {
		t.Fatalf("handed over %v, want exactly [%s]", closer.calls, src.InfoHash)
	}
	// The LIBRARY ITEM, not the file: a request is for a film, and a film may
	// acquire more files later.
	if closer.items[0] != res.Item.ID {
		t.Errorf("handed over item %d, want %d", closer.items[0], res.Item.ID)
	}
}

// A skip or a failure means the thing somebody asked for is NOT in the library.
// Telling them it arrived is worse than telling them nothing: they go looking
// for it, do not find it, and stop trusting the answer.
func TestAnImportThatDidNotImportClosesNothing(t *testing.T) {
	closer := &fakeCloser{closed: 1}

	t.Run("nothing importable in the download", func(t *testing.T) {
		r := newRig(t)
		r.imp.SetRequestCloser(closer)
		// An allowlist of container extensions means a download of only these
		// is skipped rather than imported.
		src := r.download(hash(1), "Film.2019.1080p.BluRay-GRP", map[string]int64{
			"readme.nfo":              1024,
			"RARBG_DO_NOT_MIRROR.exe": 1024,
		})
		res, _ := r.imp.Import(r.ctx, src)
		if res.Outcome == OutcomeImported {
			t.Fatalf("precondition: it imported %q", res.Selected)
		}
		if len(closer.calls) != 0 {
			t.Errorf("a %s import closed a request: %v", res.Outcome, closer.calls)
		}
	})
}

// The requester not being notified must never become "the import failed". The
// file is on disk either way, and failing here would invite an operator to
// grab the same release a second time.
func TestAFailureToCloseARequestDoesNotFailTheImport(t *testing.T) {
	r := newRig(t)
	r.imp.SetRequestCloser(&fakeCloser{err: errors.New("the requests table is on fire")})

	src := r.download(hash(0), "Film.2019.1080p.BluRay.x264-GRP", map[string]int64{
		"Film.2019.1080p.BluRay.x264-GRP.mkv": 20 * mib,
	})
	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatalf("a request-bookkeeping failure failed the whole import: %v", err)
	}
	if res.Outcome != OutcomeImported {
		t.Errorf("outcome = %s, want imported", res.Outcome)
	}
	if _, serr := os.Stat(filepath.Join(r.movies, res.File.RelPath)); serr != nil {
		t.Errorf("the file is not in the library: %v", serr)
	}
}

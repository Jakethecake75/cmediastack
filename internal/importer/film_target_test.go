package importer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/release"
)

// A download grabbed FOR a film is filed under that film, and checked against
// it (ADR-0026).

// starWars is the film ADR-0026 opens with, added the way the Add screen adds
// it: the provider's title, in the folder named after it.
func (r *rig) starWars(rootID int64) Item {
	r.t.Helper()
	it, err := r.add(Item{Kind: KindMovie, Title: "Star Wars", Year: 1977, RootFolderID: rootID,
		Folder: "Star Wars (1977)", TMDBID: 11, IMDbID: "tt0076759"})
	if err != nil {
		r.t.Fatal(err)
	}
	return it
}

func (r *rig) filmItems() []Item {
	r.t.Helper()
	items, err := r.store.queryItems(r.ctx, everything, `WHERE kind = 'movie' ORDER BY id`)
	if err != nil {
		r.t.Fatal(err)
	}
	return items
}

const starWarsRelease = "Star.Wars.Episode.IV.A.New.Hope.1977.1080p.BluRay.x264-GRP"

// The case that kept films out of ADR-0025. The scene's name for Star Wars
// would, without a target, build "Star Wars Episode IV A New Hope (1977)" — a
// second Star Wars. With a target it lands in the film that was added, named
// as the film is named.
func TestAGrabForAFilmLandsInThatFilm(t *testing.T) {
	r := newRig(t)
	film := r.starWars(movieRootID(t, r))

	src := r.download(hash(1), starWarsRelease, map[string]int64{starWarsRelease + ".mkv": 20 * mib})
	src.Target = &Target{ItemID: film.ID, Film: true}
	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeImported {
		t.Fatalf("outcome = %q: %s", res.Outcome, res.Detail)
	}
	if res.Item.ID != film.ID {
		t.Errorf("filed under item %d, want the added film %d", res.Item.ID, film.ID)
	}
	if got := r.filmItems(); len(got) != 1 {
		t.Errorf("%d films in the library after the import, want 1: %+v", len(got), got)
	}
	want := filepath.Join(r.movies, "Star Wars (1977)", "Star Wars (1977) [Bluray-1080p].mkv")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("the film is not at %s: %v", want, err)
	}
	if res.File.Season != nil || res.File.Episode != nil {
		t.Errorf("a film was recorded with an episode: %v %v", res.File.Season, res.File.Episode)
	}
	if !res.Hardlinked {
		t.Error("the film was copied, not hardlinked, on one filesystem")
	}
}

// The control for the test above: the same download without a target makes the
// twin. If this stops being true, the test above no longer tests the target.
func TestWithoutATargetTheFilmIsWorkedOutFromTheName(t *testing.T) {
	r := newRig(t)
	r.starWars(movieRootID(t, r))

	src := r.download(hash(2), starWarsRelease, map[string]int64{starWarsRelease + ".mkv": 20 * mib})
	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeImported {
		t.Fatalf("outcome = %q: %s", res.Outcome, res.Detail)
	}
	got := r.filmItems()
	if len(got) != 2 || got[1].Folder != "Star Wars Episode IV A New Hope (1977)" {
		t.Errorf("films = %+v; without a target the release name makes a second Star Wars, "+
			"which is the behaviour a film target exists to avoid", got)
	}
}

// The film's own root, not the films root with the most free space.
func TestAGrabForAFilmLandsOnThatFilmsRoot(t *testing.T) {
	r := newRig(t)
	other := filepath.Join(filepath.Dir(r.movies), "films-two")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := r.roots.Create(r.ctx, other, library.KindMovies, "More films")
	if err != nil {
		t.Fatal(err)
	}
	film := r.starWars(second.ID)

	src := r.download(hash(3), starWarsRelease, map[string]int64{starWarsRelease + ".mkv": 20 * mib})
	src.Target = &Target{ItemID: film.ID, Film: true}
	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeImported || res.File.RootFolderID != second.ID {
		t.Fatalf("outcome %q on root %d, want imported onto the film's own root %d: %s",
			res.Outcome, res.File.RootFolderID, second.ID, res.Detail)
	}
	if _, err := os.Stat(filepath.Join(other, "Star Wars (1977)", "Star Wars (1977) [Bluray-1080p].mkv")); err != nil {
		t.Errorf("the film is not in its folder on its own root: %v", err)
	}
}

// Each refusal attaches nothing, places nothing, and says why.
func TestAFilmTargetThatDoesNotFitIsRefused(t *testing.T) {
	r := newRig(t)
	film := r.starWars(movieRootID(t, r))
	series := r.followed("Severance", r.seriesRoot().ID)

	for _, tc := range []struct {
		name    string
		release string
		target  Target
		says    []string
	}{
		{"television grabbed for a film", "Dune.Prophecy.S01E01.1080p.WEB.H264-GRP",
			Target{ItemID: film.ID, Film: true}, []string{"Star Wars", "television", "S01E01"}},
		// Refused before the target is consulted, by the layout every import
		// plans first — and still refused, which is what matters.
		{"a daily show grabbed for a film", "The.Daily.Show.2024.03.15.1080p.WEB.h264-EDITH",
			Target{ItemID: film.ID, Film: true}, []string{"names an episode but no season"}},
		{"a film target on a series", starWarsRelease,
			Target{ItemID: series.ID, Film: true}, []string{"Severance is not a film"}},
		{"an episode target on a film", "Severance.S02E03.1080p.WEB.H264-GRP",
			Target{ItemID: film.ID, Season: 2, Episode: 3}, []string{"Star Wars is not a series"}},
		{"a film deleted while it downloaded", starWarsRelease,
			Target{ItemID: 999, Film: true}, []string{"no longer in the library", "not re-created"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(r.filmItems()) + len(r.seriesItems())
			n := byte(len(tc.name))
			src := r.download(hash(n), tc.release, map[string]int64{tc.release + ".mkv": 20 * mib})
			target := tc.target
			src.Target = &target
			res, err := r.imp.Import(r.ctx, src)
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != OutcomeSkipped {
				t.Fatalf("outcome = %q (%s), want skipped", res.Outcome, res.Detail)
			}
			for _, s := range tc.says {
				if !strings.Contains(res.Detail, s) {
					t.Errorf("the reason %q does not say %q", res.Detail, s)
				}
			}
			if after := len(r.filmItems()) + len(r.seriesItems()); after != before {
				t.Errorf("%d items before, %d after: a refused download created one", before, after)
			}
		})
	}
	var placed []string
	_ = filepath.Walk(r.movies, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			placed = append(placed, p)
		}
		return nil
	})
	if len(placed) != 0 {
		t.Errorf("refused downloads reached the films root: %v", placed)
	}
}

// Whether a film on disk is replaced is the import's usual rule, target or not:
// a better quality replaces it, the same or worse is skipped.
func TestAFilmOnDiskIsReplacedOnlyByABetterRelease(t *testing.T) {
	r := newRig(t)
	film := r.starWars(movieRootID(t, r))
	grab := func(n byte, name string) Result {
		t.Helper()
		src := r.download(hash(n), name, map[string]int64{name + ".mkv": 20 * mib})
		src.Target = &Target{ItemID: film.ID, Film: true}
		res, err := r.imp.Import(r.ctx, src)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	if res := grab(1, "Star.Wars.1977.720p.WEB.H264-GRP"); res.Outcome != OutcomeImported {
		t.Fatalf("first: %q %s", res.Outcome, res.Detail)
	}
	if res := grab(2, starWarsRelease); res.Outcome != OutcomeImported ||
		!strings.Contains(res.Detail, "replaced an earlier file") {
		t.Fatalf("an upgrade: %q %s", res.Outcome, res.Detail)
	}
	if res := grab(3, "Star.Wars.1977.720p.WEB.H264-OTHER"); res.Outcome != OutcomeSkipped ||
		!strings.Contains(res.Detail, "better quality") {
		t.Fatalf("a downgrade: %q %s", res.Outcome, res.Detail)
	}
	files, err := r.store.FilesFor(r.ctx, film.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Quality != "Bluray-1080p" {
		t.Errorf("files = %+v, want the one Bluray-1080p", files)
	}
}

// A film's folder is used as it is stored — here one a scan found, with a
// colon in it that SafeComponent would have rewritten into a second folder.
func TestTheFilmFolderIsUsedAsItIsStored(t *testing.T) {
	r := newRig(t)
	root := movieRootID(t, r)
	if err := os.MkdirAll(filepath.Join(r.movies, "Blade Runner: The Final Cut"), 0o755); err != nil {
		t.Fatal(err)
	}
	film, err := r.store.UpsertItem(r.ctx, Item{Kind: KindMovie, Title: "Blade Runner", Year: 1982,
		RootFolderID: root, Folder: "Blade Runner: The Final Cut"})
	if err != nil {
		t.Fatal(err)
	}
	name := "Blade.Runner.1982.The.Final.Cut.1080p.BluRay.x264-GRP"
	src := r.download(hash(4), name, map[string]int64{name + ".mkv": 20 * mib})
	src.Target = &Target{ItemID: film.ID, Film: true}
	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeImported {
		t.Fatalf("outcome = %q: %s", res.Outcome, res.Detail)
	}
	if res.File.RelPath != "Blade Runner: The Final Cut/Blade Runner (1982) [Bluray-1080p].mkv" {
		t.Errorf("placed at %q; the film's own folder was rewritten, or the file named "+
			"after the release", res.File.RelPath)
	}
}

// The file is named from the film's title, written the way filenames are
// written, whatever the release called it.
func TestAFilmIsNamedFromItsOwnTitle(t *testing.T) {
	for _, tc := range []struct {
		title, release, want string
		year                 int
	}{
		{"Star Wars: Episode IV - A New Hope", starWarsRelease,
			"F/Star Wars - Episode IV - A New Hope (1977) [Bluray-1080p].mkv", 1977},
		{"What If...?", "What.If.2013.720p.WEB.H264-GRP", "F/What If... (2013) [WEBDL-720p].mkv", 2013},
		{"Dune", "Dune.2021.2160p.UHD.BluRay.x265-GRP.mp4", "F/Dune (2021) [Bluray-2160p].mp4", 2021},
		{"Untitled", "Untitled.1080p.WEB.H264-GRP", "F/Untitled [WEBDL-1080p].mkv", 0},
	} {
		p := release.Parse(tc.release)
		if strings.HasSuffix(tc.release, ".mp4") {
			p.Container = "mp4"
		}
		l, err := PlanFilmIn("F", tc.title, tc.year, p, release.QualityOf(p).Name)
		if err != nil {
			t.Errorf("%s: %v", tc.title, err)
			continue
		}
		if l.RelPath != tc.want || l.IsTelevision || l.Season != -1 || l.Folder != "F" {
			t.Errorf("%s: %+v, want %s", tc.title, l, tc.want)
		}
	}
}

// PlanFilmIn refuses what it cannot place: a folder that is not one name, a
// film with no title, and television.
func TestPlanFilmInRefusesWhatItCannotPlace(t *testing.T) {
	film := release.Parse(starWarsRelease)
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b"} {
		if _, err := PlanFilmIn(bad, "Star Wars", 1977, film, "Bluray-1080p"); !errors.Is(err, ErrUnnameable) {
			t.Errorf("folder %q: err = %v, want ErrUnnameable", bad, err)
		}
	}
	if _, err := PlanFilmIn("F", "  ", 1977, film, "Bluray-1080p"); !errors.Is(err, ErrUnnameable) {
		t.Errorf("no title: err = %v", err)
	}
	tv := release.Parse("Severance.S02E03.1080p.WEB.H264-GRP")
	if _, err := PlanFilmIn("F", "Severance", 2022, tv, "WEBDL-1080p"); !errors.Is(err, ErrUnnameable) {
		t.Errorf("television: err = %v", err)
	}
}

// A film with no file is wanted; a film with one is not; a series is never on
// this list, whatever it holds (its episodes have their own). Alphabetical by
// the library's sort title.
func TestAFilmIsWantedUntilItHasAFile(t *testing.T) {
	r := newRig(t)
	root := movieRootID(t, r)
	for _, it := range []Item{
		{Kind: KindMovie, Title: "The Matrix", Year: 1999, RootFolderID: root, Folder: "The Matrix (1999)", TMDBID: 603},
		{Kind: KindMovie, Title: "Amélie", Year: 2001, RootFolderID: root, Folder: "Amélie (2001)", TMDBID: 194},
	} {
		if _, err := r.add(it); err != nil {
			t.Fatal(err)
		}
	}
	film := r.starWars(root)
	if _, err := r.add(severance(r.seriesRoot().ID)); err != nil {
		t.Fatal(err)
	}

	names := func() []string {
		t.Helper()
		got, err := r.store.WantedFilms(r.ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, it := range got {
			out = append(out, it.Title)
		}
		return out
	}
	if got := strings.Join(names(), "|"); got != "Amélie|The Matrix|Star Wars" {
		t.Errorf("wanted = %s, want Amélie|The Matrix|Star Wars (The Matrix files under M)", got)
	}

	src := r.download(hash(1), starWarsRelease, map[string]int64{starWarsRelease + ".mkv": 20 * mib})
	src.Target = &Target{ItemID: film.ID, Film: true}
	if res, err := r.imp.Import(r.ctx, src); err != nil || res.Outcome != OutcomeImported {
		t.Fatalf("import: %v %+v", err, res)
	}
	if got := strings.Join(names(), "|"); got != "Amélie|The Matrix" {
		t.Errorf("after Star Wars arrived, wanted = %s", got)
	}

	// The limit is honoured.
	if got, _ := r.store.WantedFilms(r.ctx, 1); len(got) != 1 || got[0].Title != "Amélie" {
		t.Errorf("limit 1 gave %+v", got)
	}
}

// Film answers for a film, and names what is wrong with anything else.
func TestAFilmIsAskedForAsAFilm(t *testing.T) {
	r := newRig(t)
	film := r.starWars(movieRootID(t, r))
	series := r.followed("Severance", r.seriesRoot().ID)

	got, err := r.store.Film(r.ctx, film.ID)
	if err != nil || got.ID != film.ID || got.TMDBID != 11 {
		t.Errorf("Film(%d) = %+v, %v", film.ID, got, err)
	}
	if _, err := r.store.Film(r.ctx, series.ID); !errors.Is(err, ErrNotAFilm) {
		t.Errorf("a series: err = %v, want ErrNotAFilm", err)
	}
	if _, err := r.store.Film(r.ctx, 999); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("no such item: err = %v, want ErrItemNotFound", err)
	}
}

// A film can be kept without being wanted (ADR-0030): unmonitored, it stays in
// the library and leaves the Wanted list, and monitored again it returns. A
// series has no such switch — its seasons and episodes do — and the switch
// needs what editing the library needs.
func TestAFilmCanBeKeptWithoutBeingWanted(t *testing.T) {
	r := newRig(t)
	root := movieRootID(t, r)
	film := r.starWars(root)
	series := r.followed("Severance", r.seriesRoot().ID)

	if !film.Monitored {
		t.Fatal("a film is added unmonitored")
	}
	wanted := func() int {
		t.Helper()
		got, err := r.store.WantedFilms(r.ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	if wanted() != 1 {
		t.Fatal("a new film is not wanted")
	}

	got, err := r.store.SetFilmMonitored(r.ctx, film.ID, false)
	if err != nil || got.Monitored || got.ID != film.ID {
		t.Fatalf("unmonitor: %+v, %v", got, err)
	}
	if wanted() != 0 {
		t.Fatal("an unmonitored film is still wanted")
	}
	if it, err := r.store.GetItem(r.ctx, film.ID); err != nil || it.Monitored {
		t.Fatalf("read back: %+v, %v", it, err)
	}
	if _, err := r.store.SetFilmMonitored(r.ctx, film.ID, true); err != nil || wanted() != 1 {
		t.Fatalf("monitor again: %v, %d wanted", err, wanted())
	}

	if _, err := r.store.SetFilmMonitored(r.ctx, series.ID, false); !errors.Is(err, ErrNotAFilm) {
		t.Errorf("a series: err = %v, want ErrNotAFilm", err)
	}
	if _, err := r.store.SetFilmMonitored(r.ctx, 999, false); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("no such item: err = %v, want ErrItemNotFound", err)
	}
	viewer := authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: 2, Username: "sam", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 2, Name: "User", Rank: 10,
			Permissions: authz.NewPermissionSet(authz.PermLogin, authz.PermBrowse)},
	})
	if _, err := r.store.SetFilmMonitored(viewer, film.ID, false); !authz.IsDenied(err) {
		t.Errorf("a viewer unmonitored a film: err = %v", err)
	}
	if wanted() != 1 {
		t.Fatal("a refused change changed something")
	}
}

// Both read the library, and need what browsing it needs.
func TestReadingWantedFilmsNeedsBrowse(t *testing.T) {
	r := newRig(t)
	film := r.starWars(movieRootID(t, r))
	nobody := authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: 2, Username: "sam", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 3, Name: "Nobody", Rank: 1,
			Permissions: authz.NewPermissionSet(authz.PermLogin)},
	})
	if _, err := r.store.WantedFilms(nobody, 0); !authz.IsDenied(err) {
		t.Errorf("WantedFilms: err = %v, want a denial", err)
	}
	if _, err := r.store.Film(nobody, film.ID); !authz.IsDenied(err) {
		t.Errorf("Film: err = %v, want a denial", err)
	}
}

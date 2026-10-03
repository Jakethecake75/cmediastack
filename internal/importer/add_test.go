package importer

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// Adding a title before any of it is on disk (ADR-0025): the names this
// software makes for it, and the item it records.

// A provider's title has characters a release name never does. These are real
// TMDB titles, checked against the live API.
func TestAProvidersTitleIsWrittenTheWayPeopleWriteFilenames(t *testing.T) {
	for _, tc := range []struct {
		title string
		year  int
		want  string
	}{
		{"Severance", 2022, "Severance (2022)"},
		{"Star Trek: Discovery", 2017, "Star Trek - Discovery (2017)"},
		{"Law & Order: Special Victims Unit", 1999, "Law & Order - Special Victims Unit (1999)"},
		{"What If...?", 2021, "What If... (2021)"},
		{"M*A*S*H", 1972, "MASH (1972)"},
		{"Face/Off", 1997, "Face-Off (1997)"},
		{"Re:Zero", 2016, "Re-Zero (2016)"},
		{"A Title : Spaced", 0, "A Title - Spaced"},
		{`Dr. "Who"`, 1963, "Dr. 'Who' (1963)"},
		// What SafeComponent still owns: nothing here gets past it.
		{"Pipe|Dream <1>", 2020, "Pipe_Dream _1_ (2020)"},
	} {
		got, err := FolderFor(tc.title, tc.year)
		if err != nil || got != tc.want {
			t.Errorf("FolderFor(%q, %d) = %q, %v; want %q", tc.title, tc.year, got, err, tc.want)
		}
	}
}

// A title that is a path is still one component, and never a way out.
func TestAFolderMadeFromATitleIsOneNameAndGoesNowhere(t *testing.T) {
	for _, title := range []string{"../../etc", "..", "/", `C:\Windows`, "a/b/c"} {
		got, err := FolderFor(title, 0)
		if err != nil {
			continue // refusing is a fine answer too
		}
		if strings.ContainsAny(got, `/\`) || got == "." || got == ".." {
			t.Errorf("FolderFor(%q) = %q", title, got)
		}
	}
	if _, err := FolderFor("???", 2020); !errors.Is(err, ErrUnnameable) {
		t.Errorf("a title of nothing but punctuation: err = %v, want ErrUnnameable", err)
	}
}

// The files an episode search imports into a series are named by the same rule
// as its folder, so the two agree; the TITLE recorded stays the provider's.
func TestAnEpisodeIsNamedTheWayItsSeriesFolderIs(t *testing.T) {
	l, err := PlanEpisodeIn("Star Trek - Discovery (2017)", "Star Trek: Discovery", 2017,
		parse(t, "Star.Trek.Discovery.S01E01.1080p.WEB.H264-GRP"), "WEBDL-1080p", true)
	if err != nil {
		t.Fatal(err)
	}
	want := "Star Trek - Discovery (2017)/Season 01/Star Trek - Discovery (2017) - S01E01 [WEBDL-1080p].mkv"
	if l.RelPath != want {
		t.Errorf("path = %q, want %q", l.RelPath, want)
	}
	// Without season folders, the same name in the series' own folder
	// (ADR-0063).
	flat, err := PlanEpisodeIn("Star Trek - Discovery (2017)", "Star Trek: Discovery", 2017,
		parse(t, "Star.Trek.Discovery.S01E01.1080p.WEB.H264-GRP"), "WEBDL-1080p", false)
	if err != nil || flat.RelPath != "Star Trek - Discovery (2017)/Star Trek - Discovery (2017) - S01E01 [WEBDL-1080p].mkv" ||
		flat.Season != 1 || flat.Episode != 1 {
		t.Errorf("flat: %+v %v", flat, err)
	}
	if l.Title != "Star Trek: Discovery" {
		t.Errorf("title = %q; the name on disk is a spelling, not a rename", l.Title)
	}
}

// A folder name somebody typed is used as typed or refused — never quietly
// changed into a different one.
func TestAFolderNameIsTakenAsTypedOrRefused(t *testing.T) {
	for _, ok := range []string{"Severance", "Severance (2022)", "Sévérance", ".hidden but fine"} {
		if err := CheckFolderName(ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "  ", "a/b", `a\b`, ".", "..", "Star Trek: Discovery",
		"Film.", " Severance", "Severance ", "CON", "What?", "nul\x00byte"} {
		if err := CheckFolderName(bad); !errors.Is(err, ErrUnusableFolder) {
			t.Errorf("%q: err = %v, want ErrUnusableFolder", bad, err)
		}
	}
}

// ---------------------------------------------------------------------------

func (r *rig) add(it Item) (Item, error) {
	r.t.Helper()
	var added Item
	err := r.store.db.InTx(r.ctx, func(tx db.Execer) error {
		var err error
		added, err = r.store.AddItem(r.ctx, tx, it)
		return err
	})
	return added, err
}

func severance(root int64) Item {
	return Item{Kind: KindSeries, Title: "Severance", Year: 2022, RootFolderID: root,
		Folder: "Severance (2022)", TMDBID: 95396, IMDbID: "tt11280740"}
}

func TestAnAddedItemIsRecordedWithTheIdentityItWasChosenBy(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	added, err := r.add(severance(root.ID))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.store.GetItem(r.ctx, added.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Severance" || got.Year != 2022 || got.TMDBID != 95396 ||
		got.IMDbID != "tt11280740" || got.Folder != "Severance (2022)" || got.SortTitle != "severance" {
		t.Errorf("recorded %+v", got)
	}
	files, _ := r.store.FilesFor(r.ctx, added.ID)
	if len(files) != 0 {
		t.Errorf("an added item has %d files", len(files))
	}
}

func TestTheSameTitleCannotBeAddedTwice(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	first, err := r.add(severance(root.ID))
	if err != nil {
		t.Fatal(err)
	}
	again := severance(root.ID)
	again.Folder = "Severance, again"
	_, err = r.add(again)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, ErrAlreadyInLibrary) {
		t.Fatalf("err = %v, want ErrAlreadyInLibrary", err)
	}
	if conflict.Existing.ID != first.ID {
		t.Errorf("the conflict names item %d, want %d", conflict.Existing.ID, first.ID)
	}
	items, _ := r.store.ListItems(r.ctx, KindSeries)
	if len(items) != 1 {
		t.Errorf("%d items", len(items))
	}
}

// TMDB numbers films and series separately: film 1399 is not series 1399.
func TestAFilmAndASeriesMayShareAProviderID(t *testing.T) {
	r := newRig(t)
	if _, err := r.add(Item{Kind: KindSeries, Title: "Game of Thrones", Year: 2011,
		RootFolderID: r.seriesRoot().ID, Folder: "Game of Thrones (2011)", TMDBID: 1399}); err != nil {
		t.Fatal(err)
	}
	roots, _ := r.roots.List(r.ctx)
	var movies library.RootFolder
	for _, rf := range roots {
		if rf.Kind == library.KindMovies {
			movies = rf
		}
	}
	if _, err := r.add(Item{Kind: KindMovie, Title: "Some Film", Year: 1999,
		RootFolderID: movies.ID, Folder: "Some Film (1999)", TMDBID: 1399}); err != nil {
		t.Errorf("a film with the same number as a series was refused: %v", err)
	}
}

// A folder a scan already found is not adopted: attaching an identity to
// somebody else's item, and renaming it, is a person's decision (ADR-0019).
func TestAnOccupiedFolderIsNotAdopted(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	scanned, err := r.store.UpsertItem(r.ctx, Item{Kind: KindSeries, Title: "severance",
		RootFolderID: root.ID, Folder: "Severance (2022)"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.add(severance(root.ID))
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, ErrFolderTaken) {
		t.Fatalf("err = %v, want ErrFolderTaken", err)
	}
	if conflict.Existing.ID != scanned.ID {
		t.Errorf("the conflict names item %d, want the scanned one, %d", conflict.Existing.ID, scanned.ID)
	}
	after, _ := r.store.GetItem(r.ctx, scanned.ID)
	if after.Title != "severance" || after.TMDBID != 0 {
		t.Errorf("the scanned item was changed: %+v", after)
	}
}

// The rule is one statement, so two adds of one title at once cannot both
// succeed — even into different folders, where no constraint would stop them.
func TestTwoAddsOfOneTitleAtOnceCannotBothSucceed(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	const racers = 8
	var wg sync.WaitGroup
	errs := make([]error, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			it := severance(root.ID)
			it.Folder = "Severance " + string(rune('A'+i))
			<-start
			errs[i] = r.store.db.InTx(r.ctx, func(tx db.Execer) error {
				_, err := r.store.AddItem(r.ctx, tx, it)
				return err
			})
		}(i)
	}
	close(start)
	wg.Wait()

	won := 0
	for i, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrAlreadyInLibrary):
		default:
			t.Errorf("racer %d failed for another reason: %v", i, err)
		}
	}
	if won != 1 {
		t.Errorf("%d adds succeeded, want exactly one", won)
	}
	items, _ := r.store.ListItems(r.ctx, KindSeries)
	if len(items) != 1 {
		t.Errorf("%d items recorded", len(items))
	}
}

// Two adds of one title, interleaved by hand rather than left to a scheduler:
// the first has inserted and not committed when the second begins. The second
// must wait, then be told the title is already there — not fail with "database
// is locked", which is what a check-first-insert-second version gives it (a
// read snapshot taken before the first commit cannot be upgraded to a write
// after it).
func TestAnAddThatLosesARaceIsToldSo(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()

	first, err := r.store.db.BeginTx(r.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.AddItem(r.ctx, first, severance(root.ID)); err != nil {
		t.Fatal(err)
	}

	lost := make(chan error, 1)
	go func() {
		second, err := r.store.db.BeginTx(r.ctx, nil)
		if err != nil {
			lost <- err
			return
		}
		defer func() { _ = second.Rollback() }()
		it := severance(root.ID)
		it.Folder = "Severance, a second time"
		_, err = r.store.AddItem(r.ctx, second, it)
		lost <- err
	}()

	// Long enough for the second add to have started and be waiting.
	time.Sleep(300 * time.Millisecond)
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-lost:
		if !errors.Is(err, ErrAlreadyInLibrary) {
			t.Fatalf("the second add got %v; want ErrAlreadyInLibrary", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the second add never finished")
	}
}

func TestAddingNeedsThePermissionToEditTheLibrary(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	viewer := authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: 2, Username: "sam", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 3, Name: "User", Rank: 10,
			Permissions: authz.NewPermissionSet(authz.PermLogin, authz.PermBrowse)},
	})
	err := r.store.db.InTx(viewer, func(tx db.Execer) error {
		_, err := r.store.AddItem(viewer, tx, severance(root.ID))
		return err
	})
	if !authz.IsDenied(err) {
		t.Fatalf("err = %v, want a denial", err)
	}
	// Nor can any background task: none holds library.edit.
	for _, task := range []authz.SystemTask{authz.TaskImport, authz.TaskLibraryScan,
		authz.TaskIdentify, authz.TaskEpisodeRefresh} {
		sys := authz.SystemPrincipal(t.Context(), task)
		err := r.store.db.InTx(sys, func(tx db.Execer) error {
			_, err := r.store.AddItem(sys, tx, severance(root.ID))
			return err
		})
		if !authz.IsDenied(err) {
			t.Errorf("%s: err = %v, want a denial", task, err)
		}
	}
}

func TestAnAddedItemNeedsWhatMakesItOne(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	for name, mutate := range map[string]func(*Item){
		"no provider id":   func(it *Item) { it.TMDBID = 0 },
		"no title":         func(it *Item) { it.Title = "  " },
		"no root":          func(it *Item) { it.RootFolderID = 0 },
		"not a kind":       func(it *Item) { it.Kind = "album" },
		"a path":           func(it *Item) { it.Folder = "Severance/../../etc" },
		"an unsafe name":   func(it *Item) { it.Folder = "Star Trek: Discovery" },
		"no folder at all": func(it *Item) { it.Folder = "" },
	} {
		it := severance(root.ID)
		mutate(&it)
		if _, err := r.add(it); err == nil {
			t.Errorf("%s: added", name)
		}
	}
	items, _ := r.store.ListItems(r.ctx, "")
	if len(items) != 0 {
		t.Errorf("%d items recorded", len(items))
	}
}

package importer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/release"
)

// A download grabbed FOR an episode is filed under that series, and checked
// against it (ADR-0023).

// seriesRoot returns the rig's TV root.
func (r *rig) seriesRoot() library.RootFolder {
	r.t.Helper()
	roots, err := r.roots.List(r.ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, root := range roots {
		if root.Kind == library.KindSeries {
			return root
		}
	}
	r.t.Fatal("the rig has no series root")
	return library.RootFolder{}
}

// followed is a series already in the library, in the folder a scan gave it:
// "Severance", with no year, because that is what was on disk.
func (r *rig) followed(folder string, rootID int64) Item {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Join(r.rootPath(rootID), folder, "Season 01"), 0o755); err != nil {
		r.t.Fatal(err)
	}
	item, err := r.store.UpsertItem(r.ctx, Item{
		Kind: KindSeries, Title: "Severance", Year: 2022, RootFolderID: rootID, Folder: folder,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return item
}

func (r *rig) rootPath(id int64) string {
	r.t.Helper()
	roots, err := r.roots.List(r.ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, root := range roots {
		if root.ID == id {
			return root.Path
		}
	}
	r.t.Fatalf("no root %d", id)
	return ""
}

func (r *rig) seriesItems() []Item {
	r.t.Helper()
	items, err := r.store.queryItems(r.ctx, everything, `WHERE kind = 'series' ORDER BY id`)
	if err != nil {
		r.t.Fatal(err)
	}
	return items
}

// The case that motivated ADR-0023. A release spelling the series with its year
// would, without a target, build the folder "Severance (2022)" and create a
// second Severance. With a target it lands in the series the operator follows.
func TestAGrabForAnEpisodeLandsInThatSeries(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	item := r.followed("Severance", root.ID)

	src := r.download(hash(1), "Severance.2022.S02E03.1080p.WEB-DL.DDP5.1.H.264-GRP", map[string]int64{
		"Severance.2022.S02E03.1080p.WEB-DL.DDP5.1.H.264-GRP.mkv": 20 * mib,
	})
	src.Target = &Target{ItemID: item.ID, Season: 2, Episode: 3}

	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeImported {
		t.Fatalf("outcome = %q: %s", res.Outcome, res.Detail)
	}
	if res.Item.ID != item.ID {
		t.Errorf("filed under item %d, want the followed series %d", res.Item.ID, item.ID)
	}
	if got := r.seriesItems(); len(got) != 1 {
		t.Errorf("%d series in the library after the import, want 1: %+v", len(got), got)
	}
	want := filepath.Join(root.Path, "Severance", "Season 02", "Severance (2022) - S02E03 [WEBDL-1080p].mkv")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("the episode is not at %s: %v", want, err)
	}
	if res.File.Season == nil || *res.File.Season != 2 || res.File.Episode == nil || *res.File.Episode != 3 {
		t.Errorf("recorded as season %v episode %v", res.File.Season, res.File.Episode)
	}
}

// The control for the test above: the same download WITHOUT a target does what
// the importer always did, and creates the second series. If this ever stops
// being true the test above is no longer testing the target.
func TestWithoutATargetTheSeriesIsWorkedOutFromTheName(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	r.followed("Severance", root.ID)

	src := r.download(hash(2), "Severance.2022.S02E03.1080p.WEB-DL.DDP5.1.H.264-GRP", map[string]int64{
		"Severance.2022.S02E03.1080p.WEB-DL.DDP5.1.H.264-GRP.mkv": 20 * mib,
	})
	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeImported {
		t.Fatalf("outcome = %q: %s", res.Outcome, res.Detail)
	}
	if got := r.seriesItems(); len(got) != 2 {
		t.Errorf("%d series; without a target the year in the name makes a second "+
			"one, which is the behaviour a target exists to avoid", len(got))
	}
}

// The series' own root, not the root with the most free space.
func TestAGrabForAnEpisodeLandsOnThatSeriesRoot(t *testing.T) {
	r := newRig(t)
	// A second TV root. Free space is unknown on both, so the importer's own
	// choice between them is whichever it lists first — which must not matter.
	other := filepath.Join(filepath.Dir(r.series), "series-two")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := r.roots.Create(r.ctx, other, library.KindSeries, "More shows")
	if err != nil {
		t.Fatal(err)
	}
	item := r.followed("Severance", second.ID)

	src := r.download(hash(3), "Severance.S02E03.1080p.WEB.H264-GRP", map[string]int64{
		"Severance.S02E03.1080p.WEB.H264-GRP.mkv": 20 * mib,
	})
	src.Target = &Target{ItemID: item.ID, Season: 2, Episode: 3}
	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeImported {
		t.Fatalf("outcome = %q: %s", res.Outcome, res.Detail)
	}
	if res.File.RootFolderID != second.ID {
		t.Errorf("placed on root %d, want the series' own root %d", res.File.RootFolderID, second.ID)
	}
	matches, _ := filepath.Glob(filepath.Join(other, "Severance", "Season 02", "*S02E03*"))
	if len(matches) != 1 {
		t.Errorf("the episode is not in the series' folder on its own root: %v", matches)
	}
}

// A file that is not the episode it was grabbed for is attached to nothing.
func TestAFileThatIsNotTheTargetIsRefused(t *testing.T) {
	r := newRig(t)
	item := r.followed("Severance", r.seriesRoot().ID)

	src := r.download(hash(4), "Severance.S02E04.1080p.WEB.H264-GRP", map[string]int64{
		"Severance.S02E04.1080p.WEB.H264-GRP.mkv": 20 * mib,
	})
	src.Target = &Target{ItemID: item.ID, Season: 2, Episode: 3}
	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeSkipped {
		t.Fatalf("outcome = %q; a file that is not the episode it was grabbed for "+
			"must not be attached to anything", res.Outcome)
	}
	if !strings.Contains(res.Detail, "S02E03") || !strings.Contains(res.Detail, "S02E04") {
		t.Errorf("the reason does not name both episodes: %q", res.Detail)
	}
	if matches, _ := filepath.Glob(filepath.Join(r.series, "*", "*", "*S02E0*")); len(matches) != 0 {
		t.Errorf("a refused file reached the library: %v", matches)
	}
	recs, err := r.store.RecordsFor(r.ctx, src.InfoHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Outcome != OutcomeSkipped || !strings.Contains(recs[0].Detail, "S02E04") {
		t.Errorf("the refusal was not recorded with its reason: %+v", recs)
	}
}

// A file holding a range covers every episode in it.
func TestAFileCoveringTheTargetInARangeIsAccepted(t *testing.T) {
	r := newRig(t)
	item := r.followed("Severance", r.seriesRoot().ID)

	src := r.download(hash(5), "Severance.S02E03E04.1080p.WEB.H264-GRP", map[string]int64{
		"Severance.S02E03E04.1080p.WEB.H264-GRP.mkv": 20 * mib,
	})
	src.Target = &Target{ItemID: item.ID, Season: 2, Episode: 4}
	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeImported {
		t.Fatalf("outcome = %q: %s", res.Outcome, res.Detail)
	}
	if res.File.EpisodeLast == nil || *res.File.EpisodeLast != 4 {
		t.Errorf("episode_last = %v, want 4", res.File.EpisodeLast)
	}
}

// A series deleted while its episode downloaded is not brought back.
func TestATargetThatWasDeletedIsNotRecreated(t *testing.T) {
	r := newRig(t)
	src := r.download(hash(0), "Severance.S02E03.1080p.WEB.H264-GRP", map[string]int64{
		"Severance.S02E03.1080p.WEB.H264-GRP.mkv": 20 * mib,
	})
	src.Target = &Target{ItemID: 999, Season: 2, Episode: 3}
	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeSkipped || !strings.Contains(res.Detail, "no longer in the library") {
		t.Fatalf("outcome = %q, %q; want a skip naming the deleted series", res.Outcome, res.Detail)
	}
	if got := r.seriesItems(); len(got) != 0 {
		t.Errorf("the deleted series was re-created from the release name: %+v", got)
	}
}

// An operator's folder name is used as it is, not "made safe" into a second
// folder. SafeComponent turns ":" into "_", which is right for a name this
// software invents and wrong for one the operator already has.
func TestTheSeriesFolderIsUsedAsItIsOnDisk(t *testing.T) {
	r := newRig(t)
	item := r.followed("Severance: Director's Cut", r.seriesRoot().ID)

	src := r.download(hash(1), "Severance.S02E03.1080p.WEB.H264-GRP", map[string]int64{
		"Severance.S02E03.1080p.WEB.H264-GRP.mkv": 20 * mib,
	})
	src.Target = &Target{ItemID: item.ID, Season: 2, Episode: 3}
	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeImported {
		t.Fatalf("outcome = %q: %s", res.Outcome, res.Detail)
	}
	if !strings.HasPrefix(res.File.RelPath, "Severance: Director's Cut/Season 02/") {
		t.Errorf("placed at %q; the series' own folder was rewritten", res.File.RelPath)
	}
}

// A folder that is not one directory name is refused before anything is
// written — the vault would refuse the escape anyway, and this says why.
func TestAFolderThatIsNotOneComponentIsRefused(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b"} {
		if _, err := PlanEpisodeIn(bad, "Severance", 2022,
			release.Parse("Severance.S02E03.1080p.WEB.H264-GRP"), "WEBDL-1080p", true); !errors.Is(err, ErrUnnameable) {
			t.Errorf("folder %q: err = %v, want ErrUnnameable", bad, err)
		}
	}
}

// ADR-0063: a series without season folders has a grabbed episode, and each
// file of a pack, filed flat in its own folder.
func TestASeriesWithoutSeasonFoldersIsFiledFlat(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	item := r.followed("Severance", root.ID)
	if _, err := r.store.db.ExecContext(r.ctx, `UPDATE media_item SET season_folders = 0 WHERE id = ?`, item.ID); err != nil {
		t.Fatal(err)
	}
	src := r.download(hash(1), "Severance.S02E03.1080p.WEB-DL.DDP5.1.H.264-GRP", map[string]int64{
		"Severance.S02E03.1080p.WEB-DL.DDP5.1.H.264-GRP.mkv": 20 * mib,
	})
	src.Target = &Target{ItemID: item.ID, Season: 2, Episode: 3}
	if res, err := r.imp.Import(r.ctx, src); err != nil || res.Outcome != OutcomeImported {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(root.Path, "Severance", "Severance (2022) - S02E03 [WEBDL-1080p].mkv")); err != nil {
		t.Errorf("not filed flat: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root.Path, "Severance", "Season 02")); err == nil {
		t.Error("a season folder was made")
	}

	r.listed(item.ID, 2, 1, 2)
	pack := r.pack(2, item.ID, map[string]int64{
		"Severance.S02E01.1080p.WEB-DL-GRP.mkv": 20 * mib,
		"Severance.S02E02.1080p.WEB-DL-GRP.mkv": 20 * mib,
	})
	if res, err := r.imp.Import(r.ctx, pack); err != nil || res.Outcome != OutcomeImported {
		t.Fatalf("%+v %v", res, err)
	}
	for _, e := range []string{"S02E01", "S02E02"} {
		if _, err := os.Stat(filepath.Join(root.Path, "Severance", "Severance (2022) - "+e+" [WEBDL-1080p].mkv")); err != nil {
			t.Errorf("%s not filed flat: %v", e, err)
		}
	}
}

// aired records a season of a series with each episode's air date.
func (r *rig) aired(itemID int64, season int, dates map[int]string) {
	r.t.Helper()
	res, err := r.store.db.ExecContext(r.ctx, `INSERT INTO season (item_id, number, episode_count, updated_at)
		VALUES (?, ?, ?, 'x')`, itemID, season, len(dates))
	if err != nil {
		r.t.Fatal(err)
	}
	seasonID, _ := res.LastInsertId()
	for n, day := range dates {
		if _, err := r.store.db.ExecContext(r.ctx, `INSERT INTO episode (item_id, season_id, season_number, number,
			aired_at, updated_at) VALUES (?, ?, ?, ?, ?, 'x')`, itemID, seasonID, season, n, day+"T00:00:00Z"); err != nil {
			r.t.Fatal(err)
		}
	}
}

// ADR-0064, decision 1, as files arrive: a dated file grabbed for an episode
// is it when that is the day it aired, and not otherwise; the scan files a
// dated file under the episode of its day, and under none when two aired then.
func TestADatedFileIsTheEpisodeOfItsDay(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	item := r.followed("Severance", root.ID)
	r.aired(item.ID, 3, map[int]string{1: "2026-10-01", 2: "2026-10-02", 4: "2026-10-05", 5: "2026-10-05"})
	r.aired(item.ID, 0, map[int]string{1: "2026-10-01"}) // a special the same day as S03E01: not counted

	src := r.download(hash(1), "Severance.2026.10.02.1080p.WEB-DL.H264-GRP", map[string]int64{
		"Severance.2026.10.02.1080p.WEB-DL.H264-GRP.mkv": 20 * mib,
	})
	src.Target = &Target{ItemID: item.ID, Season: 3, Episode: 2}
	res, err := r.imp.Import(r.ctx, src)
	if err != nil || res.Outcome != OutcomeImported {
		t.Fatalf("%+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(root.Path, "Severance", "Season 03", "Severance (2022) - S03E02 [WEBDL-1080p].mkv")); err != nil {
		t.Errorf("not filed as S03E02: %v", err)
	}

	other := r.download(hash(2), "Severance.2026.10.02.720p.HDTV.x264-GRP", map[string]int64{
		"Severance.2026.10.02.720p.HDTV.x264-GRP.mkv": 20 * mib,
	})
	other.Target = &Target{ItemID: item.ID, Season: 3, Episode: 1}
	if res, err := r.imp.Import(r.ctx, other); err != nil || res.Outcome != OutcomeSkipped ||
		!strings.Contains(res.Detail, "the file is S03E02") {
		t.Errorf("another day's file for S03E01: %+v %v", res, err)
	}

	r.put(root.Path, "Severance/Severance.2026.10.01.720p.HDTV.x264-GRP.mkv", 20*mib)
	r.put(root.Path, "Severance/Severance.2026.10.05.720p.HDTV.x264-GRP.mkv", 20*mib)
	if _, err := r.imp.Scan(r.ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	files, err := r.store.FilesFor(r.ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range files {
		code := "none"
		if f.Season != nil && f.Episode != nil {
			code = fmt.Sprintf("S%02dE%02d", *f.Season, *f.Episode)
		}
		got[filepath.Base(f.RelPath)] = code
	}
	if got["Severance.2026.10.01.720p.HDTV.x264-GRP.mkv"] != "S03E01" ||
		got["Severance.2026.10.05.720p.HDTV.x264-GRP.mkv"] != "none" {
		t.Errorf("scanned %v", got)
	}
}

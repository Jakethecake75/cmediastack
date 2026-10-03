package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A download grabbed for a whole season is imported file by file, each file as
// the episode its own name says (ADR-0033, decision 4).

// listed records the provider's episode list for one season of a series, as
// the episode refresh would have.
func (r *rig) listed(itemID int64, season int, numbers ...int) {
	r.t.Helper()
	now := "2026-01-01T00:00:00Z"
	res, err := r.store.db.ExecContext(r.ctx, `
		INSERT INTO season (item_id, number, episode_count, updated_at) VALUES (?, ?, ?, ?)`,
		itemID, season, len(numbers), now)
	if err != nil {
		r.t.Fatal(err)
	}
	seasonID, _ := res.LastInsertId()
	for _, n := range numbers {
		if _, err := r.store.db.ExecContext(r.ctx, `
			INSERT INTO episode (item_id, season_id, season_number, number, aired_at, updated_at)
			VALUES (?, ?, ?, ?, '2025-01-01T00:00:00Z', ?)`, itemID, seasonID, season, n, now); err != nil {
			r.t.Fatal(err)
		}
	}
}

// pack is a season pack of Severance season 2, grabbed for the followed series.
func (r *rig) pack(h byte, itemID int64, files map[string]int64) Source {
	r.t.Helper()
	src := r.download(hash(h), "Severance.S02.1080p.WEB-DL.DDP5.1.H.264-GRP", files)
	src.Target = &Target{ItemID: itemID, Season: 2, Pack: true}
	return src
}

func (r *rig) episodePath(root string, e int) string {
	return filepath.Join(root, "Severance", "Season 02",
		"Severance (2022) - S02E0"+string(rune('0'+e))+" [WEBDL-1080p].mkv")
}

func (r *rig) records(h string) map[string]Record {
	r.t.Helper()
	recs, err := r.store.RecordsFor(r.ctx, h)
	if err != nil {
		r.t.Fatal(err)
	}
	out := map[string]Record{}
	for _, rec := range recs {
		if _, seen := out[rec.SourcePath]; !seen { // newest first
			out[rec.SourcePath] = rec
		}
	}
	return out
}

func TestAPackImportsEachFileAsItsOwnEpisode(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	item := r.followed("Severance", root.ID)
	r.listed(item.ID, 2, 1, 2, 3)

	src := r.pack(1, item.ID, map[string]int64{
		"Severance.S02E01.1080p.WEB-DL-GRP.mkv":            20 * mib,
		"Season 2/Severance.S02E02.1080p.WEB-DL-GRP.mkv":   20 * mib,
		"Season 2/Episode 3 - Who Is Alive.mkv":            20 * mib,
		"Sample/Severance.S02E01.sample.mkv":               1 * mib,
		"Severance.S02.nfo":                                512,
		"Extras/Severance.S02.Behind.The.Scenes.1080p.mkv": 20 * mib,
	})
	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeImported {
		t.Fatalf("outcome %q: %s", res.Outcome, res.Detail)
	}
	for e := 1; e <= 3; e++ {
		if _, err := os.Stat(r.episodePath(root.Path, e)); err != nil {
			t.Errorf("episode %d is not in the library: %v", e, err)
		}
	}
	files, err := r.store.FilesFor(r.ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Errorf("%d files recorded, want 3: %+v", len(files), files)
	}
	if got := r.seriesItems(); len(got) != 1 {
		t.Errorf("%d series after the import, want 1", len(got))
	}

	recs := r.records(src.InfoHash)
	for _, p := range []string{"Severance.S02E01.1080p.WEB-DL-GRP.mkv",
		"Season 2/Severance.S02E02.1080p.WEB-DL-GRP.mkv", "Season 2/Episode 3 - Who Is Alive.mkv"} {
		if recs[p].Outcome != OutcomeImported {
			t.Errorf("%s: record %+v, want imported", p, recs[p])
		}
	}
	if rec, ok := recs["Extras/Severance.S02.Behind.The.Scenes.1080p.mkv"]; ok && rec.Outcome == OutcomeImported {
		t.Error("an extra was imported")
	}
}

func TestAPackFileMustNameAnEpisodeOfItsSeason(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	item := r.followed("Severance", root.ID)
	r.listed(item.ID, 2, 1, 2)

	src := r.pack(1, item.ID, map[string]int64{
		"Severance.S02E01.1080p.WEB-DL-GRP.mkv": 20 * mib,
		"Severance.S03E01.1080p.WEB-DL-GRP.mkv": 20 * mib,
		"03 - Woe's Hollow.mkv":                 20 * mib,
		"Severance.S02E14.1080p.WEB-DL-GRP.mkv": 20 * mib,
	})
	if _, err := r.imp.Import(r.ctx, src); err != nil {
		t.Fatal(err)
	}
	recs := r.records(src.InfoHash)
	for p, why := range map[string]string{
		"Severance.S03E01.1080p.WEB-DL-GRP.mkv": "season 3, but this pack was grabbed for season 2",
		"03 - Woe's Hollow.mkv":                 "does not say which episode it is",
		"Severance.S02E14.1080p.WEB-DL-GRP.mkv": "S02E14 is not an episode the provider lists",
	} {
		if rec := recs[p]; rec.Outcome != OutcomeSkipped || !strings.Contains(rec.Detail, why) {
			t.Errorf("%s: %s %q, want skipped saying %q", p, rec.Outcome, rec.Detail, why)
		}
	}
	if recs["Severance.S02E01.1080p.WEB-DL-GRP.mkv"].Outcome != OutcomeImported {
		t.Error("the one real episode of the season was not imported")
	}

	// A series the provider has listed nothing for: every file skipped, nothing
	// created.
	r2 := newRig(t)
	item2 := r2.followed("Severance", r2.seriesRoot().ID)
	src2 := r2.pack(2, item2.ID, map[string]int64{"Severance.S02E01.1080p.WEB-DL-GRP.mkv": 20 * mib})
	res2, err := r2.imp.Import(r2.ctx, src2)
	if err != nil || res2.Outcome != OutcomeSkipped {
		t.Errorf("a pack for a season with no listed episodes: %q %v; want skipped", res2.Outcome, err)
	}
	if files, _ := r2.store.FilesFor(r2.ctx, item2.ID); len(files) != 0 {
		t.Errorf("%d files recorded for a season nobody listed", len(files))
	}

	// The series deleted while its pack downloaded: not re-created.
	r3 := newRig(t)
	src3 := r3.pack(3, 999, map[string]int64{"Severance.S02E01.1080p.WEB-DL-GRP.mkv": 20 * mib})
	res3, err := r3.imp.Import(r3.ctx, src3)
	if err != nil || res3.Outcome != OutcomeSkipped || !strings.Contains(res3.Detail, ErrTargetGone.Error()) {
		t.Errorf("a pack for a deleted series: %q %q %v; want skipped as gone", res3.Outcome, res3.Detail, err)
	}
	if got := r3.seriesItems(); len(got) != 0 {
		t.Errorf("a deleted series was re-created: %+v", got)
	}
}

// PlanPack reads names only. A hostile file list — the uploader wrote it —
// cannot make it touch anything.
func TestPlanningAPackReadsNamesOnly(t *testing.T) {
	plan := PlanPack([]Candidate{
		{Path: "../../../../etc/Severance.S02E01.mkv", Bytes: 20 * mib},
		{Path: "Severance.S02E02.mkv", Bytes: 20 * mib},
	}, 2, map[int]bool{1: true, 2: true})
	if len(plan.Files) != 2 {
		t.Fatalf("planned %+v", plan)
	}
	if plan.Files[0].Episodes[0] != 1 || plan.Files[1].Episodes[0] != 2 {
		t.Errorf("episodes %v and %v, want 1 and 2", plan.Files[0].Episodes, plan.Files[1].Episodes)
	}
	// Without a provider's list, nothing is refused for not being listed.
	if p := PlanPack([]Candidate{{Path: "S02E40.mkv", Bytes: 20 * mib}}, 2, nil); len(p.Files) != 1 {
		t.Errorf("with no list, planned %+v", p)
	}
	// A double episode holds both.
	if p := PlanPack([]Candidate{{Path: "Show.S02E01E02.mkv", Bytes: 20 * mib}}, 2,
		map[int]bool{1: true, 2: true}); len(p.Files) != 1 || len(p.Files[0].Episodes) != 2 {
		t.Errorf("a double episode planned as %+v", p)
	}
}

func TestTwoFilesForOneEpisodeAreBothSkipped(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	item := r.followed("Severance", root.ID)
	r.listed(item.ID, 2, 1, 2)

	src := r.pack(1, item.ID, map[string]int64{
		"1080p/Severance.S02E01.1080p.mkv": 20 * mib,
		"720p/Severance.S02E01.720p.mkv":   10 * mib,
		"Severance.S02E02.1080p.mkv":       20 * mib,
	})
	if _, err := r.imp.Import(r.ctx, src); err != nil {
		t.Fatal(err)
	}
	recs := r.records(src.InfoHash)
	for _, p := range []string{"1080p/Severance.S02E01.1080p.mkv", "720p/Severance.S02E01.720p.mkv"} {
		rec := recs[p]
		if rec.Outcome != OutcomeSkipped || !strings.Contains(rec.Detail, "two files claim S02E01") {
			t.Errorf("%s: %s %q, want skipped as one of two claiming S02E01", p, rec.Outcome, rec.Detail)
		}
	}
	if recs["Severance.S02E02.1080p.mkv"].Outcome != OutcomeImported {
		t.Error("the rest of the pack stopped because of one ambiguous episode")
	}
}

func TestAPackNeverReplacesABetterFile(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	item := r.followed("Severance", root.ID)
	r.listed(item.ID, 2, 1, 2)

	better := r.download(hash(0), "Severance.S02E01.1080p.BluRay.x264-GRP", map[string]int64{
		"Severance.S02E01.1080p.BluRay.x264-GRP.mkv": 20 * mib,
	})
	better.Target = &Target{ItemID: item.ID, Season: 2, Episode: 1}
	if res, err := r.imp.Import(r.ctx, better); err != nil || res.Outcome != OutcomeImported {
		t.Fatalf("the Bluray episode: %q %v", res.Outcome, err)
	}

	src := r.download(hash(1), "Severance.S02.720p.WEB-DL.DDP5.1.H.264-GRP", map[string]int64{
		"Severance.S02E01.720p.mkv": 20 * mib,
		"Severance.S02E02.720p.mkv": 20 * mib,
	})
	src.Target = &Target{ItemID: item.ID, Season: 2, Pack: true}
	if _, err := r.imp.Import(r.ctx, src); err != nil {
		t.Fatal(err)
	}
	recs := r.records(src.InfoHash)
	if rec := recs["Severance.S02E01.720p.mkv"]; rec.Outcome != OutcomeSkipped ||
		!strings.Contains(rec.Detail, "better quality") {
		t.Errorf("E01: %s %q; the pack's 720p must not replace the Bluray", rec.Outcome, rec.Detail)
	}
	if recs["Severance.S02E02.720p.mkv"].Outcome != OutcomeImported {
		t.Error("E02, which was missing, did not arrive")
	}
	bluray := filepath.Join(root.Path, "Severance", "Season 02", "Severance (2022) - S02E01 [Bluray-1080p].mkv")
	if _, err := os.Stat(bluray); err != nil {
		t.Errorf("the better file is gone: %v", err)
	}
}

func TestAPacksSubtitlesGoWithTheirOwnEpisode(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	item := r.followed("Severance", root.ID)
	r.listed(item.ID, 2, 1, 2)

	src := r.pack(1, item.ID, map[string]int64{
		"Severance.S02E01.1080p.mkv":        20 * mib,
		"Severance.S02E01.1080p.en.srt":     4 * kib,
		"Severance.S02E02.1080p.mkv":        20 * mib,
		"Subs/Severance.S02E02.1080p/2.srt": 4 * kib,
		"English.srt":                       4 * kib,
	})
	res, err := r.imp.Import(r.ctx, src)
	if err != nil || res.Outcome != OutcomeImported {
		t.Fatalf("%q %v", res.Outcome, err)
	}
	dir := filepath.Join(root.Path, "Severance", "Season 02")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	subsFor := map[string]int{}
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".srt") {
			switch {
			case strings.Contains(n, "S02E01"):
				subsFor["E01"]++
			case strings.Contains(n, "S02E02"):
				subsFor["E02"]++
			}
		}
	}
	if subsFor["E01"] != 1 || subsFor["E02"] != 1 {
		t.Errorf("subtitles placed %v in %v; each episode must get only its own one", subsFor, entries)
	}
}

func TestAPackIsRetriedOnlyForWhatFailed(t *testing.T) {
	r := newRig(t)
	clock := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	r.store.now = func() time.Time { return clock }
	root := r.seriesRoot()
	item := r.followed("Severance", root.ID)
	r.listed(item.ID, 2, 1, 2)

	// E02's place in the library is taken by a directory, so placing it fails
	// — the kind of failure a person fixes, after which it must be retried.
	blocker := r.episodePath(root.Path, 2)
	if err := os.MkdirAll(blocker, 0o755); err != nil {
		t.Fatal(err)
	}
	src := r.pack(1, item.ID, map[string]int64{
		"Severance.S02E01.1080p.mkv": 20 * mib,
		"Severance.S02E02.1080p.mkv": 20 * mib,
	})
	if _, err := r.imp.Import(r.ctx, src); err == nil {
		t.Fatal("a pack with a file that could not be placed reported no error")
	}
	recs := r.records(src.InfoHash)
	if recs["Severance.S02E01.1080p.mkv"].Outcome != OutcomeImported ||
		recs["Severance.S02E02.1080p.mkv"].Outcome != OutcomeFailed {
		t.Fatalf("records %+v; want E01 imported and E02 failed", recs)
	}
	if again, err := r.store.ShouldAttemptPack(r.ctx, src.InfoHash); err != nil || !again {
		t.Fatalf("with a file failing: attempt=%v %v; a failure must be retried at once", again, err)
	}

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	if _, err := r.imp.Import(r.ctx, src); err != nil {
		t.Fatal(err)
	}
	all, err := r.store.RecordsFor(r.ctx, src.InfoHash)
	if err != nil {
		t.Fatal(err)
	}
	imported := map[string]int{}
	for _, rec := range all {
		if rec.Outcome == OutcomeImported {
			imported[rec.SourcePath]++
		}
	}
	if imported["Severance.S02E01.1080p.mkv"] != 1 || imported["Severance.S02E02.1080p.mkv"] != 1 {
		t.Errorf("imports per file %v; the retry must import E02 and leave E01 alone", imported)
	}
	// Left alone, not looked at again: looked at, it would be recorded as a
	// skip ("already in the library") over its own arrival.
	if rec := r.records(src.InfoHash)["Severance.S02E01.1080p.mkv"]; rec.Outcome != OutcomeImported {
		t.Errorf("E01's latest record is %s %q; the retry re-examined a file that had arrived",
			rec.Outcome, rec.Detail)
	}
	if again, err := r.store.ShouldAttemptPack(r.ctx, src.InfoHash); err != nil || again {
		t.Errorf("with every file imported: attempt=%v %v; want never again", again, err)
	}
}

// A pack in which nothing was failing but something was skipped is looked at
// again after the usual interval, not every minute.
func TestAPackWithSkipsWaitsTheSkipInterval(t *testing.T) {
	r := newRig(t)
	clock := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	r.store.now = func() time.Time { return clock }
	item := r.followed("Severance", r.seriesRoot().ID)
	r.listed(item.ID, 2, 1)
	src := r.pack(1, item.ID, map[string]int64{
		"Severance.S02E01.1080p.mkv": 20 * mib,
		"Severance.S02E02.1080p.mkv": 20 * mib, // not listed yet
	})
	if _, err := r.imp.Import(r.ctx, src); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	if again, _ := r.store.ShouldAttemptPack(r.ctx, src.InfoHash); again {
		t.Error("a pack with only a skip left was offered again a minute later")
	}
	clock = clock.Add(SkipRetryInterval)
	if again, _ := r.store.ShouldAttemptPack(r.ctx, src.InfoHash); !again {
		t.Error("a pack with a skip left was never looked at again; the provider may list it later")
	}
}

// Retrying a pack whose files were skipped moves each skip's time forward; it
// does not add a row per file per hour. The last row of a pack is usually
// another file's, so the check is per file.
func TestRetryingAPacksSkipsDoesNotGrowTheRecord(t *testing.T) {
	r := newRig(t)
	clock := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	r.store.now = func() time.Time { return clock }
	item := r.followed("Severance", r.seriesRoot().ID)
	r.listed(item.ID, 2, 1)
	src := r.pack(1, item.ID, map[string]int64{
		"Severance.S02E01.1080p.mkv": 20 * mib,
		"03 - Unnumbered.mkv":        20 * mib,
		"Severance.S02E07.1080p.mkv": 20 * mib,
	})
	for i := 0; i < 3; i++ {
		if _, err := r.imp.Import(r.ctx, src); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(2 * SkipRetryInterval)
	}
	recs, err := r.store.RecordsFor(r.ctx, src.InfoHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Errorf("%d records after three passes over a pack of three files, want 3: %+v", len(recs), recs)
	}
}

// TestAPackOfSeveralSeasonsFilesEachAsItsOwnSeason pins ADR-0057, decision 4:
// each file under the season and episode its own name says; a season outside
// the span, an unlisted episode and a bare episode number are left; one
// episode number in two seasons is two episodes, not a clash.
func TestAPackOfSeveralSeasonsFilesEachAsItsOwnSeason(t *testing.T) {
	r := newRig(t)
	root := r.seriesRoot()
	item := r.followed("Severance", root.ID)
	r.listed(item.ID, 1, 1, 2)
	r.listed(item.ID, 2, 1)
	r.listed(item.ID, 3, 1)

	src := r.download(hash(9), "Severance.S01-S02.1080p.WEB-DL.DDP5.1.H.264-GRP", map[string]int64{
		"S1/Severance.S01E01.1080p.WEB-DL-GRP.mkv": 20 * mib,
		"S1/Severance.S01E02.1080p.WEB-DL-GRP.mkv": 20 * mib,
		"S2/Severance.S02E01.1080p.WEB-DL-GRP.mkv": 20 * mib,
		"S2/Severance.S02E02.1080p.WEB-DL-GRP.mkv": 20 * mib,
		"S3/Severance.S03E01.1080p.WEB-DL-GRP.mkv": 20 * mib,
		"S2/Episode 3 - Who Is Alive.mkv":          20 * mib,
	})
	src.Target = &Target{ItemID: item.ID, Season: 1, LastSeason: 2, Pack: true}
	res, err := r.imp.Import(r.ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeImported || !strings.Contains(res.Detail, "seasons 1 to 2 of Severance: 3 imported, 3 skipped") {
		t.Fatalf("%s: %s", res.Outcome, res.Detail)
	}
	for _, rel := range []string{
		"Severance/Season 01/Severance (2022) - S01E01 [WEBDL-1080p].mkv",
		"Severance/Season 01/Severance (2022) - S01E02 [WEBDL-1080p].mkv",
		"Severance/Season 02/Severance (2022) - S02E01 [WEBDL-1080p].mkv",
	} {
		if _, err := os.Stat(filepath.Join(root.Path, rel)); err != nil {
			t.Errorf("%s is not in the library: %v", rel, err)
		}
	}
	recs := r.records(src.InfoHash)
	for p, why := range map[string]string{
		"S3/Severance.S03E01.1080p.WEB-DL-GRP.mkv": "grabbed for seasons 1 to 2",
		"S2/Severance.S02E02.1080p.WEB-DL-GRP.mkv": ReasonPackNotListed,
		"S2/Episode 3 - Who Is Alive.mkv":          ReasonPackNoEpisode,
	} {
		if recs[p].Outcome != OutcomeSkipped || !strings.Contains(recs[p].Detail, why) {
			t.Errorf("%s: %+v, want skipped as %q", p, recs[p], why)
		}
	}
}

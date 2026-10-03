package acquire

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/indexer"
)

// Upgrades to the cutoff (ADR-0036).

// haveFilmAs records a film's file at a quality, from a release.
func (r *rig) haveFilmAs(item int64, quality, release string) {
	r.t.Helper()
	r.exec(`INSERT INTO media_file (item_id, root_folder_id, relative_path, imported_at, quality, release_title)
	        VALUES (?, 2, ?, ?, ?, ?)`, item, fmt.Sprintf("%d/film.mkv", item),
		r.clock.now().Format(time.RFC3339Nano), quality, release)
}

// haveEpisodesAs records a file covering episodes first..last at a quality.
func (r *rig) haveEpisodesAs(item int64, season, first, last int, quality, release string) {
	r.t.Helper()
	r.exec(`INSERT INTO media_file (item_id, season, episode, episode_last, root_folder_id,
	                                relative_path, imported_at, quality, release_title)
	        VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?)`,
		item, season, first, last, fmt.Sprintf("%d/S%02dE%02d-E%02d.mkv", item, season, first, last),
		r.clock.now().Format(time.RFC3339Nano), quality, release)
}

// duneIn720p is a film on disk as a WEB-DL 720p, below HD-1080p's Bluray-1080p
// cutoff, with a Blu-ray 1080p and an HDTV 720p on offer.
func duneIn720p(r *rig) int64 {
	r.t.Helper()
	dune := r.addFilm("Dune", 2021, 438631)
	r.haveFilmAs(dune, "WEBDL-720p", "Dune.2021.720p.WEB-DL.H264-OLD")
	r.client.byTerm["dune 2021"] = []indexer.Result{
		rel("Dune.2021.1080p.BluRay.x264-GRP", 1, 20),
		rel("Dune.2021.720p.HDTV.x264-GRP", 2, 90),
	}
	return dune
}

func auditDetail(r *rig) string {
	r.t.Helper()
	var detail string
	if err := r.database.QueryRowContext(context.Background(),
		`SELECT detail FROM audit_event WHERE action = 'acquisition.grabbed' ORDER BY id DESC LIMIT 1`).Scan(&detail); err != nil {
		r.t.Fatal(err)
	}
	return detail
}

func TestAFileBelowItsCutoffIsUpgraded(t *testing.T) {
	r := newRig(t, Config{Upgrades: true})
	duneIn720p(r)
	summary := r.runSearch()
	added := r.queue.added()
	if len(added) != 1 || added[0].Title != "Dune.2021.1080p.BluRay.x264-GRP" {
		t.Fatalf("grabbed %+v (summary: %s); want the Blu-ray, which beats the 720p held", added, summary)
	}
	if !strings.Contains(summary, "an upgrade from WEBDL-720p") {
		t.Errorf("summary %q does not say it was an upgrade", summary)
	}
	if d := auditDetail(r); !strings.Contains(d, "Dune (2021) (an upgrade from WEBDL-720p)") {
		t.Errorf("audit %q", d)
	}
}

func TestUpgradesAreOffUnlessChosen(t *testing.T) {
	r := newRig(t, Config{})
	duneIn720p(r)
	r.client.feed = r.client.byTerm["dune 2021"]
	r.runSearch()
	r.runRecent()
	if got := r.queue.added(); len(got) != 0 {
		t.Errorf("grabbed %+v with upgrades off", got)
	}
	if queries, _ := r.client.snapshot(); len(queries) > 1 {
		t.Errorf("queries %+v; a film on disk is not searched for with upgrades off", queries)
	}
}

func TestAFileAtItsCutoffIsLeftAlone(t *testing.T) {
	r := newRig(t, Config{Upgrades: true})
	dune := r.addFilm("Dune", 2021, 438631)
	r.haveFilmAs(dune, "Bluray-1080p", "Dune.2021.1080p.BluRay.x264-HELD")
	r.client.byTerm["dune 2021"] = []indexer.Result{rel("Dune.2021.1080p.BluRay.x264-PROPER-GRP", 1, 90)}
	r.runSearch()
	if queries, _ := r.client.snapshot(); len(queries) != 0 || len(r.queue.added()) != 0 {
		t.Errorf("searched %+v, grabbed %+v; a file at its cutoff is enough", queries, r.queue.added())
	}
}

func TestAnUpgradeMustBeBetter(t *testing.T) {
	r := newRig(t, Config{Upgrades: true})
	dune := r.addFilm("Dune", 2021, 438631)
	r.haveFilmAs(dune, "WEBDL-1080p", "Dune.2021.1080p.WEB-DL.H264-HELD")
	r.client.byTerm["dune 2021"] = []indexer.Result{
		rel("Dune.2021.1080p.WEB-DL.H264-OTHER", 1, 90),
		rel("Dune.2021.720p.HDTV.x264-GRP", 2, 90),
	}
	r.runSearch()
	if got := r.queue.added(); len(got) != 0 {
		t.Fatalf("grabbed %+v; neither beats the WEB-DL 1080p held", got)
	}
	st, ok := r.state(StateKey{Film: true, ID: dune})
	if !ok || st.Outcome != OutcomeNothing || !st.NextAt.Equal(r.clock.now().Add(UpgradeInterval)) ||
		!strings.Contains(st.Detail, "not better than what is held") {
		t.Errorf("state %+v; want nothing, a week's wait, and why", st)
	}
}

func TestWhatIsWantedComesBeforeUpgrades(t *testing.T) {
	r := newRig(t, Config{Upgrades: true, SearchesPerRun: 1})
	duneIn720p(r)
	r.clock.advance(time.Minute)
	r.addFilm("Arrival", 2016, 329865)
	r.runSearch()
	queries, _ := r.client.snapshot()
	if len(queries) != 1 || !strings.EqualFold(queries[0].Term, "Arrival 2016") {
		t.Errorf("queries %+v; the one search of the pass is for what is missing", queries)
	}
}

func TestAnUpgradeIsLookedForWeekly(t *testing.T) {
	r := newRig(t, Config{Upgrades: true})
	dune := r.addFilm("Dune", 2021, 438631)
	r.haveFilmAs(dune, "WEBDL-720p", "Dune.2021.720p.WEB-DL.H264-OLD")
	r.runSearch() // finds nothing
	r.client.reset()
	r.clock.advance(UpgradeInterval - time.Hour)
	r.runSearch()
	if queries, _ := r.client.snapshot(); len(queries) != 0 {
		t.Errorf("searched again within the week: %+v", queries)
	}
	r.clock.advance(time.Hour)
	r.runSearch()
	if queries, _ := r.client.snapshot(); len(queries) != 1 {
		t.Errorf("queries %+v; a week on, it is looked for again", queries)
	}
}

func TestAnUpgradeFromTheRecentReleases(t *testing.T) {
	r := newRig(t, Config{Upgrades: true})
	duneIn720p(r)
	r.client.feed = r.client.byTerm["dune 2021"]
	summary := r.runRecent()
	if got := r.queue.added(); len(got) != 1 || got[0].Title != "Dune.2021.1080p.BluRay.x264-GRP" {
		t.Errorf("grabbed %+v (summary: %s)", got, summary)
	}
}

func TestADoubleEpisodeFileIsNotUpgraded(t *testing.T) {
	r := newRig(t, Config{Upgrades: true})
	sev := r.addSeries("Severance", 2022, 95396, 3)
	r.haveEpisodesAs(sev, 1, 1, 2, "WEBDL-720p", "Severance.S01E01E02.720p.WEB-DL-OLD")
	r.haveEpisodesAs(sev, 1, 3, 3, "WEBDL-720p", "Severance.S01E03.720p.WEB-DL-OLD")
	r.client.byTerm["severance"] = []indexer.Result{
		rel("Severance.S01E01.1080p.BluRay.x264-GRP", 1, 50),
		rel("Severance.S01E03.1080p.BluRay.x264-GRP", 3, 50),
	}
	r.client.feed = r.client.byTerm["severance"]
	r.runSearch()
	r.runRecent()
	got := fmt.Sprint(r.targets())
	if got != fmt.Sprintf("[%d S01E03]", sev) {
		t.Errorf("grabbed for %s; only E03, whose file is its own, is upgraded", got)
	}
}

// A person who unmonitors a film while its upgrade search runs has the last
// word, as for anything wanted (ADR-0030, decision 3).
func TestAPersonsChangeStopsAnUpgrade(t *testing.T) {
	r := newRig(t, Config{Upgrades: true})
	dune := duneIn720p(r)
	r.client.onSearch = func(indexer.Query) {
		r.exec(`UPDATE media_item SET monitored = 0 WHERE id = ?`, dune)
	}
	r.runSearch()
	if got := r.queue.added(); len(got) != 0 {
		t.Errorf("grabbed %+v for a film unmonitored while it was searched for", got)
	}
}

// A release of two episodes is not an upgrade of one when the other has a file
// of its own: it would be filed over both, and the other was not asked about.
func TestAReleaseOfTwoEpisodesIsNotAnUpgradeOfOne(t *testing.T) {
	r := newRig(t, Config{Upgrades: true})
	sev := r.addSeries("Severance", 2022, 95396, 4)
	r.haveEpisodesAs(sev, 1, 3, 3, "WEBDL-720p", "Severance.S01E03.720p.WEB-DL-OLD")
	r.haveEpisodesAs(sev, 1, 4, 4, "Bluray-1080p", "Severance.S01E04.1080p.BluRay-HELD")
	r.haveEpisodes(sev, 1, 1, 2)
	r.client.feed = []indexer.Result{rel("Severance.S01E03E04.1080p.BluRay.x264-GRP", 1, 50)}
	summary := r.runRecent()
	if got := r.queue.added(); len(got) != 0 {
		t.Errorf("grabbed %+v (summary: %s); a double episode is not an upgrade of E03", got, summary)
	}
}

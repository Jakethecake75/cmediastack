package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// A fixed clock, so "aired" and "announced" mean the same thing on every run.
var testNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func clock() func() time.Time { return func() time.Time { return testNow } }

type epRig struct {
	store  *EpisodeStore
	db     *sql.DB
	ctx    context.Context
	itemID int64
	// database is the same handle as db, for what needs a transaction.
	database *db.DB
}

func newEpRig(t *testing.T) *epRig {
	t.Helper()
	database, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "cms.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	raw := database.DB
	now := testNow.Format(episodeTimeLayout)
	if _, err := raw.ExecContext(t.Context(),
		`INSERT INTO root_folder (id, path, kind, label, created_at, updated_at)
		 VALUES (1, '/media/tv', 'series', 'TV', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	res, err := raw.ExecContext(t.Context(),
		`INSERT INTO media_item (kind, title, year, sort_title, root_folder_id, folder,
		                         tmdb_id, added_at, updated_at)
		 VALUES ('series', 'Severance', 2022, 'severance', 1, 'Severance', 95396, ?, ?)`,
		now, now)
	if err != nil {
		t.Fatal(err)
	}
	itemID, _ := res.LastInsertId()

	return &epRig{
		store:    NewEpisodeStore(database, clock()),
		db:       raw,
		ctx:      browseCtx(),
		itemID:   itemID,
		database: database,
	}
}

// putFile records a file covering one episode, or a range of them.
func (r *epRig) putFile(t *testing.T, season, first, last int) {
	t.Helper()
	var lastVal any
	if last > 0 {
		lastVal = last
	}
	_, err := r.db.ExecContext(t.Context(),
		`INSERT INTO media_file (item_id, season, episode, episode_last, root_folder_id,
		                         relative_path, size_bytes, quality, revision,
		                         release_title, imported_at)
		 VALUES (?,?,?,?,1,?,1,'HDTV-1080p',0,'x',?)`,
		r.itemID, season, first, lastVal,
		filepath.Join("Severance", "S01", "ep.mkv")+string(rune('a'+first)),
		testNow.Format(episodeTimeLayout))
	if err != nil {
		t.Fatal(err)
	}
}

func browseCtx() context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
		UnrestrictedLibraries: true,
		Role: authz.Role{ID: 1, Name: "Admin", Rank: 100,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
	})
}

// viewerCtx may browse but may not edit the library.
func viewerCtx() context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 2, Username: "sam", State: authz.StateActive, MFASatisfied: true,
		// Every library: what an account has unless somebody restricts it
		// (ADR-0037).
		UnrestrictedLibraries: true,
		Role: authz.Role{ID: 2, Name: "User", Rank: 10,
			Permissions: authz.NewPermissionSet(authz.PermLogin, authz.PermBrowse)},
	})
}

func aired(days int) time.Time { return testNow.AddDate(0, 0, days) }

// season1 is nine episodes, all aired well before the clock.
func season1(count int) SeasonInput {
	s := SeasonInput{Number: 1, Name: "Season 1", EpisodeCount: count, Aired: aired(-400)}
	for i := 1; i <= count; i++ {
		s.Episodes = append(s.Episodes, EpisodeInput{
			ProviderID: int64(1000 + i), Number: i,
			Title: "Episode " + string(rune('0'+i)), Aired: aired(-400 + i), Runtime: 45,
		})
	}
	return s
}

// The point of the whole increment: a season knows what it is missing.
func TestASeasonKnowsWhatItIsMissing(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(9)}); err != nil {
		t.Fatal(err)
	}
	// Three of nine on disk.
	r.putFile(t, 1, 1, 0)
	r.putFile(t, 1, 2, 0)
	r.putFile(t, 1, 5, 0)

	seasons, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 1 || seasons[0].EpisodeCount != 9 {
		t.Fatalf("seasons = %+v", seasons)
	}
	eps := byNumber[1]
	if len(eps) != 9 {
		t.Fatalf("%d episodes, want 9 — a season built from files would have had 3", len(eps))
	}

	var have, missing []int
	for _, e := range eps {
		if e.Have() {
			have = append(have, e.Number)
		} else {
			missing = append(missing, e.Number)
		}
	}
	if len(have) != 3 || len(missing) != 6 {
		t.Errorf("have %v, missing %v — want 3 and 6", have, missing)
	}
	for _, n := range []int{3, 4, 6, 7, 8, 9} {
		found := false
		for _, m := range missing {
			if m == n {
				found = true
			}
		}
		if !found {
			t.Errorf("episode %d is not reported missing; missing = %v", n, missing)
		}
	}
}

// One file, several episodes. The reason there is no episode_id on media_file.
func TestAFileCoveringARangeCountsForEveryEpisodeInIt(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(4)}); err != nil {
		t.Fatal(err)
	}
	// A double-length pilot released as one file: S01E01E02.
	r.putFile(t, 1, 1, 2)

	_, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range byNumber[1] {
		want := e.Number == 1 || e.Number == 2
		if e.Have() != want {
			t.Errorf("episode %d: have = %v, want %v. A file covering E01E02 holds "+
				"BOTH, which is why the join is a range and not a foreign key",
				e.Number, e.Have(), want)
		}
	}
}

// An episode with no air date is announced, not overdue.
//
// The condition the wanted list turns on: treating a date the provider does not
// have as "already aired" would put every unannounced episode of every running
// show on the list on day one.
func TestAnAnnouncedEpisodeIsNotWanted(t *testing.T) {
	r := newEpRig(t)
	s := SeasonInput{Number: 2, Name: "Season 2", EpisodeCount: 3, Aired: aired(-10)}
	s.Episodes = []EpisodeInput{
		{Number: 1, Title: "Aired", Aired: aired(-9)},
		{Number: 2, Title: "Airs tomorrow", Aired: aired(1)},
		{Number: 3, Title: "Announced, no date"}, // zero time
	}
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{s}); err != nil {
		t.Fatal(err)
	}

	wanted, err := r.store.Wanted(r.ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(wanted) != 1 {
		var got []string
		for _, w := range wanted {
			got = append(got, w.Title)
		}
		t.Fatalf("%d wanted, want only the one that has aired: %v", len(wanted), got)
	}
	if wanted[0].Number != 1 {
		t.Errorf("wanted E%d, want E1", wanted[0].Number)
	}

	// And the announced one is still recorded, with the fact said plainly.
	_, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	var announced int
	for _, e := range byNumber[2] {
		if e.Announced() {
			announced++
		}
	}
	if announced != 1 {
		t.Errorf("%d announced episodes, want 1 — an episode with no date is a "+
			"different fact from one that has not aired", announced)
	}
}

// Held episodes are not wanted.
func TestAnEpisodeOnDiskIsNotWanted(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3)}); err != nil {
		t.Fatal(err)
	}
	r.putFile(t, 1, 2, 0)

	wanted, err := r.store.Wanted(r.ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(wanted) != 2 {
		t.Fatalf("%d wanted, want 2 (E1 and E3)", len(wanted))
	}
	for _, w := range wanted {
		if w.Number == 2 {
			t.Error("an episode that is on disk was reported as wanted")
		}
		if w.SeriesTitle != "Severance" {
			t.Errorf("SeriesTitle = %q; an episode number with no series is "+
				"meaningless in a whole-library list", w.SeriesTitle)
		}
	}
}

// Specials are stored and not monitored.
func TestSpecialsAreKeptAndNotMonitored(t *testing.T) {
	r := newEpRig(t)
	specials := SeasonInput{Number: 0, Name: "Specials", EpisodeCount: 2, Aired: aired(-300)}
	specials.Episodes = []EpisodeInput{
		{Number: 1, Title: "A recap", Aired: aired(-299)},
		{Number: 2, Title: "A panel", Aired: aired(-298)},
	}
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{specials, season1(2)}); err != nil {
		t.Fatal(err)
	}

	seasons, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 2 {
		t.Fatalf("%d seasons, want specials kept alongside season 1", len(seasons))
	}
	for _, s := range seasons {
		wantMonitored := s.Number != 0
		if s.Monitored != wantMonitored {
			t.Errorf("season %d monitored = %v, want %v", s.Number, s.Monitored, wantMonitored)
		}
	}
	if len(byNumber[0]) != 2 {
		t.Errorf("specials' episodes were dropped; operators' files are often in them")
	}

	// And nothing in season 0 is wanted, while season 1 is.
	wanted, err := r.store.Wanted(r.ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range wanted {
		if w.SeasonNumber == 0 {
			t.Errorf("a special was wanted: S00E%02d %q", w.Number, w.Title)
		}
	}
	if len(wanted) != 2 {
		t.Errorf("%d wanted, want season 1's two episodes", len(wanted))
	}
}

// An episode the provider no longer lists is removed, and the FILE is not.
func TestAnEpisodeTheProviderDroppedIsRemovedAndItsFileIsNot(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(5)}); err != nil {
		t.Fatal(err)
	}
	r.putFile(t, 1, 5, 0)

	// The provider renumbers: season 1 is now three episodes.
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3)}); err != nil {
		t.Fatal(err)
	}

	_, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byNumber[1]) != 3 {
		t.Errorf("%d episodes after the provider dropped two; a stale row is a "+
			"phantom on the wanted list forever", len(byNumber[1]))
	}

	// The file is untouched. Deleting an episode deletes the belief that the
	// episode exists, not the bytes.
	var files int
	if err := r.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM media_file WHERE item_id = ?`,
		r.itemID).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Errorf("%d files; a refresh must never touch one", files)
	}
}

// A season the caller did not fetch is left alone, not emptied.
//
// The refresh deliberately skips seasons that cannot have changed, and if
// "not asked about" read as "has no episodes" every skipped season would be
// wiped on every run — which would then make it look changed, and refetch it
// forever.
func TestASeasonThatWasNotFetchedKeepsItsEpisodes(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(5)}); err != nil {
		t.Fatal(err)
	}
	// Same season, no Episodes slice: "I did not ask about this one".
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{
		{Number: 1, Name: "Season 1", EpisodeCount: 5, Aired: aired(-400)},
	}); err != nil {
		t.Fatal(err)
	}

	_, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byNumber[1]) != 5 {
		t.Errorf("%d episodes after a refresh that skipped this season; "+
			"nil episodes means 'not asked about', not 'has none'", len(byNumber[1]))
	}
}

// An empty slice, though, DOES mean the provider lists none.
func TestASeasonTheProviderReportsAsEmptyIsEmptied(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(5)}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{
		{Number: 1, Name: "Season 1", EpisodeCount: 0, Aired: aired(-400),
			Episodes: []EpisodeInput{}},
	}); err != nil {
		t.Fatal(err)
	}
	_, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byNumber[1]) != 0 {
		t.Errorf("%d episodes; an empty slice is the provider saying it lists none",
			len(byNumber[1]))
	}
}

// Re-running a refresh changes nothing and duplicates nothing.
func TestRefreshingTwiceIsIdempotent(t *testing.T) {
	r := newEpRig(t)
	for i := 0; i < 3; i++ {
		if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(4)}); err != nil {
			t.Fatal(err)
		}
	}
	_, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byNumber[1]) != 4 {
		t.Errorf("%d episodes after three identical refreshes", len(byNumber[1]))
	}
}

// Unmonitoring a season takes its episodes with it.
//
// A season row saying "not monitored" over episodes saying otherwise is a
// disagreement the wanted query resolves in favour of the episodes, which is
// the opposite of what the operator just asked for.
func TestUnmonitoringASeasonEmptiesItFromTheWantedList(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(4)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.store.Wanted(r.ctx, 100); len(got) != 4 {
		t.Fatalf("%d wanted before unmonitoring, want 4", len(got))
	}

	if err := r.store.SetSeasonMonitored(r.ctx, r.itemID, 1, false); err != nil {
		t.Fatal(err)
	}
	got, err := r.store.Wanted(r.ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("%d wanted after unmonitoring the season", len(got))
	}

	seasons, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if seasons[0].Monitored {
		t.Error("the season row still says monitored")
	}
	for _, e := range byNumber[1] {
		if e.Monitored {
			t.Errorf("E%02d still says monitored; the two rows disagree and the "+
				"wanted query would believe the episode", e.Number)
		}
	}
}

func TestUnmonitoringOneEpisodeLeavesTheRest(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3)}); err != nil {
		t.Fatal(err)
	}
	_, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetEpisodeMonitored(r.ctx, byNumber[1][1].ID, false); err != nil {
		t.Fatal(err)
	}
	wanted, err := r.store.Wanted(r.ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(wanted) != 2 {
		t.Fatalf("%d wanted, want 2", len(wanted))
	}
	for _, w := range wanted {
		if w.Number == 2 {
			t.Error("the unmonitored episode is still wanted")
		}
	}
}

// Changing what is monitored is editing the library, not browsing it.
func TestMonitoringNeedsThePermissionToEdit(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(2)}); err != nil {
		t.Fatal(err)
	}
	_, byNumber, _ := r.store.Seasons(r.ctx, r.itemID)

	if err := r.store.SetSeasonMonitored(viewerCtx(), r.itemID, 1, false); err == nil {
		t.Error("a principal with only browse unmonitored a season")
	}
	if err := r.store.SetEpisodeMonitored(viewerCtx(), byNumber[1][0].ID, false); err == nil {
		t.Error("a principal with only browse unmonitored an episode")
	}
	// And reading is browse, so the same principal can still look.
	if _, _, err := r.store.Seasons(viewerCtx(), r.itemID); err != nil {
		t.Errorf("a viewer could not read seasons: %v", err)
	}
}

// Deleting a series takes its seasons and episodes with it.
func TestDeletingASeriesRemovesItsEpisodes(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3)}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(t.Context(), `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(t.Context(), `DELETE FROM media_item WHERE id = ?`, r.itemID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"season", "episode"} {
		var n int
		if err := r.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%d rows left in %s after the series was deleted", n, table)
		}
	}
}

// A series nobody can ask about is not offered for refresh.
func TestOnlySeriesWorthAskingAboutAreRefreshed(t *testing.T) {
	r := newEpRig(t)
	now := testNow.Format(episodeTimeLayout)

	// A film, and an unidentified series: neither has an episode list to fetch.
	if _, err := r.db.ExecContext(t.Context(),
		`INSERT INTO media_item (kind, title, sort_title, root_folder_id, folder,
		                         tmdb_id, added_at, updated_at)
		 VALUES ('movie','Arrival','arrival',1,'Arrival',329865,?,?),
		        ('series','Unknown Show','unknown show',1,'Unknown',NULL,?,?)`,
		now, now, now, now); err != nil {
		t.Fatal(err)
	}

	ids, err := r.store.SeriesNeedingRefresh(r.ctx, testPolicy, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != r.itemID {
		t.Fatalf("SeriesNeedingRefresh = %v, want only the identified series (%d)",
			ids, r.itemID)
	}

	// A finished series that was asked about just now is not asked again.
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(4)}); err != nil {
		t.Fatal(err)
	}
	ids, err = r.store.SeriesNeedingRefresh(r.ctx, testPolicy, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("a finished series asked about moments ago is offered again: %v "+
			"— every run would spend a third party's rate limit on it", ids)
	}
}

// A running series IS offered, which is what stops the test above from passing
// against a function that simply never returns anything.
func TestARunningSeriesIsStillOfferedForRefresh(t *testing.T) {
	r := newEpRig(t)
	s := SeasonInput{Number: 1, Name: "Season 1", EpisodeCount: 2, Aired: aired(-10)}
	s.Episodes = []EpisodeInput{
		{Number: 1, Aired: aired(-9)},
		{Number: 2, Aired: aired(2)}, // still to air
	}
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{s}); err != nil {
		t.Fatal(err)
	}
	ids, err := r.store.SeriesNeedingRefresh(r.ctx, testPolicy, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Errorf("a series with an episode still to air was not offered: %v", ids)
	}
}

// testPolicy is the one the scheduled task uses.
var testPolicy = RefreshPolicy{Recent: 90 * 24 * time.Hour, Every: 7 * 24 * time.Hour}

// askedAbout backdates a series' last refresh.
func (r *epRig) askedAbout(t *testing.T, ago time.Duration) {
	t.Helper()
	if _, err := r.db.ExecContext(t.Context(), `UPDATE media_item SET episodes_refreshed_at = ? WHERE id = ?`,
		testNow.Add(-ago).Format(episodeTimeLayout), r.itemID); err != nil {
		t.Fatal(err)
	}
}

// A finished series is still asked about, just rarely — because a series that
// has ended can be RENEWED.
//
// The first version never asked again, reasoning that a finished series cannot
// change. Most shows spend a year or more between seasons with no recent
// episode and nothing still to air, so under that rule a show's next season
// would never have appeared at all.
func TestAFinishedSeriesIsStillAskedAboutEveryWeek(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(4)}); err != nil {
		t.Fatal(err)
	}

	r.askedAbout(t, 6*24*time.Hour)
	ids, err := r.store.SeriesNeedingRefresh(r.ctx, testPolicy, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("asked about six days ago and offered again: %v", ids)
	}

	r.askedAbout(t, 8*24*time.Hour)
	ids, err = r.store.SeriesNeedingRefresh(r.ctx, testPolicy, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != r.itemID {
		t.Errorf("a finished series last asked about eight days ago was not "+
			"offered (%v). It can have been renewed, and a series nobody asks "+
			"about again never finds out.", ids)
	}
}

// An announced season — listed, with nothing in it yet — keeps its series on
// the list, because it is precisely the season about to gain episodes.
//
// Severance on the live API: two finished seasons, and a third with no
// episodes and no date. Nothing in it can match "aired recently" or "still to
// air", because there is nothing in it.
func TestAnAnnouncedSeasonKeepsItsSeriesOnTheList(t *testing.T) {
	r := newEpRig(t)
	announced := SeasonInput{Number: 2, Name: "Season 2", Episodes: []EpisodeInput{}}
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(4), announced}); err != nil {
		t.Fatal(err)
	}
	ids, err := r.store.SeriesNeedingRefresh(r.ctx, testPolicy, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Errorf("a series with an announced, empty season was not offered: %v", ids)
	}
}

// A policy with a zero in it is refused rather than read as "every series,
// every run".
func TestARefreshPolicyWithAZeroInItIsRefused(t *testing.T) {
	r := newEpRig(t)
	for _, p := range []RefreshPolicy{{}, {Recent: time.Hour}, {Every: time.Hour}} {
		if _, err := r.store.SeriesNeedingRefresh(r.ctx, p, 50); !errors.Is(err, ErrNoRefreshPolicy) {
			t.Errorf("policy %+v: err = %v, want ErrNoRefreshPolicy", p, err)
		}
	}
}

// A season the provider stops listing is removed with its episodes — and the
// file that matched one of them is not.
func TestASeasonTheProviderNoLongerListsIsRemoved(t *testing.T) {
	r := newEpRig(t)
	two := SeasonInput{Number: 2, Name: "Season 2", EpisodeCount: 2, Aired: aired(-100),
		Episodes: []EpisodeInput{{Number: 1, Aired: aired(-100)}, {Number: 2, Aired: aired(-93)}}}
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3), two}); err != nil {
		t.Fatal(err)
	}
	r.putFile(t, 2, 1, 0)

	// The provider merged season 2 into season 1.
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(5)}); err != nil {
		t.Fatal(err)
	}

	seasons, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 1 || seasons[0].Number != 1 {
		t.Errorf("seasons = %+v; season 2 is gone from the provider and must go", seasons)
	}
	if len(byNumber[2]) != 0 {
		t.Errorf("season 2 left %d episode(s) behind", len(byNumber[2]))
	}
	wanted, err := r.store.Wanted(r.ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range wanted {
		if w.SeasonNumber == 2 {
			t.Errorf("S02E%02d is still wanted after the provider dropped season 2 — "+
				"a phantom no download can ever satisfy", w.Number)
		}
	}
	var files int
	if err := r.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM media_file WHERE item_id = ? AND season = 2`,
		r.itemID).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Errorf("the refresh touched a file: %d left, want 1", files)
	}
}

// An empty season list removes nothing: that is far likelier to be a provider
// answering badly than a series that stopped existing.
func TestAnEmptySeasonListRemovesNothing(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3)}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetSeasonMonitored(r.ctx, r.itemID, 1, false); err != nil {
		t.Fatal(err)
	}
	if err := r.store.Upsert(r.ctx, r.itemID, nil); err != nil {
		t.Fatal(err)
	}
	seasons, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 1 || len(byNumber[1]) != 3 {
		t.Fatalf("an empty answer erased the series: %d season(s), %d episode(s)",
			len(seasons), len(byNumber[1]))
	}
	if seasons[0].Monitored {
		t.Error("an empty answer reset the operator's choice to unmonitor season 1")
	}
}

// Reading a series' episodes is browsing.
func TestReadingEpisodesNeedsThePermissionToBrowse(t *testing.T) {
	r := newEpRig(t)
	nobody := authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 9, Username: "nobody", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 9, Name: "Pending", Rank: 0},
	})
	if _, _, err := r.store.Seasons(nobody, r.itemID); err == nil {
		t.Error("a principal with no permissions read a series' episodes")
	}
	if _, err := r.store.Wanted(nobody, 10); err == nil {
		t.Error("a principal with no permissions read the wanted list")
	}
	if err := r.store.Upsert(nobody, r.itemID, []SeasonInput{season1(1)}); err == nil {
		t.Error("a principal with no permissions wrote episodes")
	}
}

// Setting monitoring on something that is not there is an error, not a silent
// no-op that reports success.
func TestMonitoringSomethingThatDoesNotExistFails(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.SetSeasonMonitored(r.ctx, r.itemID, 7, false); err == nil {
		t.Error("unmonitoring a season that does not exist reported success")
	}
	if err := r.store.SetEpisodeMonitored(r.ctx, 4242, false); err == nil {
		t.Error("unmonitoring an episode that does not exist reported success")
	}
}

func TestErrNotASeriesIsDistinguishable(t *testing.T) {
	if !errors.Is(ErrNotASeries, ErrNotASeries) {
		t.Fatal("sanity")
	}
}

// A NEW episode inherits the season's monitored state.
//
// This is the reason seasons have a table at all (ADR-0022, decision 2): an
// episode that has not been announced yet has no row, so the operator's intent
// has to live somewhere that exists before the episode does. Unmonitor a
// season, let the provider add an episode to it, and the new episode must
// arrive unmonitored — otherwise a show somebody deliberately stopped following
// puts its next episode on the wanted list the day it is announced.
func TestANewEpisodeInheritsTheSeasonsMonitoredState(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3)}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetSeasonMonitored(r.ctx, r.itemID, 1, false); err != nil {
		t.Fatal(err)
	}

	// The provider adds a fourth episode, already aired.
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(4)}); err != nil {
		t.Fatal(err)
	}

	_, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range byNumber[1] {
		if e.Monitored {
			t.Errorf("E%02d is monitored in a season the operator unmonitored; "+
				"a new episode must inherit the season's state, which is the only "+
				"reason the season row exists", e.Number)
		}
	}
	if wanted, _ := r.store.Wanted(r.ctx, 100); len(wanted) != 0 {
		t.Errorf("%d wanted from a season nobody is following", len(wanted))
	}
}

// And a refresh never re-monitors what the operator turned off — including
// episodes that already existed.
func TestARefreshNeverOverridesTheOperatorsChoice(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3)}); err != nil {
		t.Fatal(err)
	}
	_, byNumber, _ := r.store.Seasons(r.ctx, r.itemID)
	if err := r.store.SetEpisodeMonitored(r.ctx, byNumber[1][1].ID, false); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3)}); err != nil {
			t.Fatal(err)
		}
	}
	_, byNumber, _ = r.store.Seasons(r.ctx, r.itemID)
	if byNumber[1][1].Monitored {
		t.Error("a refresh turned an episode the operator unmonitored back on")
	}
}

// Monitoring a season back on, then a new episode arriving, gives a monitored
// episode — the other half, so the test above cannot pass by never monitoring
// anything new.
func TestANewEpisodeInAMonitoredSeasonIsMonitored(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(2)}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3)}); err != nil {
		t.Fatal(err)
	}
	_, byNumber, _ := r.store.Seasons(r.ctx, r.itemID)
	if !byNumber[1][2].Monitored {
		t.Error("a new episode in a monitored season arrived unmonitored")
	}
}

// season2 is a season the provider adds later — a renewal.
func season2(count int) SeasonInput {
	s := SeasonInput{Number: 2, Name: "Season 2", EpisodeCount: count, Aired: aired(-20)}
	for i := 1; i <= count; i++ {
		s.Episodes = append(s.Episodes, EpisodeInput{
			ProviderID: int64(2000 + i), Number: i, Aired: aired(-20 + i),
		})
	}
	return s
}

// A renewed series the operator had stopped following does not come back
// monitored.
//
// There is no series-level switch; unmonitoring every season is how an
// operator says "I have stopped watching this". Before the weekly refresh a
// renewal was never noticed, so this could not happen. Once renewals ARE
// noticed, a new season defaulting to monitored would put a dropped show's next
// season straight onto the wanted list.
func TestARenewedSeriesTheOperatorDroppedStaysDropped(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3)}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetSeasonMonitored(r.ctx, r.itemID, 1, false); err != nil {
		t.Fatal(err)
	}

	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3), season2(2)}); err != nil {
		t.Fatal(err)
	}
	seasons, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 2 || seasons[1].Monitored {
		t.Fatalf("seasons = %+v; the new season of a series with every season "+
			"switched off must arrive switched off", seasons)
	}
	for _, e := range byNumber[2] {
		if e.Monitored {
			t.Errorf("S02E%02d arrived monitored in a series the operator dropped", e.Number)
		}
	}
	wanted, err := r.store.Wanted(r.ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(wanted) != 0 {
		t.Errorf("%d episode(s) of a dropped series are wanted after its renewal", len(wanted))
	}
}

// The other half: a renewed series the operator still follows gets its new
// season monitored, so the test above cannot pass by never monitoring a new
// season at all. Neither default-off specials nor switching off an OLD season
// is a decision to stop: only every regular season off is.
func TestARenewedSeriesTheOperatorFollowsIsFollowed(t *testing.T) {
	r := newEpRig(t)
	specials := SeasonInput{Number: 0, Name: "Specials", EpisodeCount: 1,
		Episodes: []EpisodeInput{{Number: 1, Aired: aired(-300)}}}
	if err := r.store.Upsert(r.ctx, r.itemID,
		[]SeasonInput{specials, season1(3), season2(2)}); err != nil {
		t.Fatal(err)
	}
	// "I don't want the first season" — which is not "I've stopped watching".
	if err := r.store.SetSeasonMonitored(r.ctx, r.itemID, 1, false); err != nil {
		t.Fatal(err)
	}

	season3 := SeasonInput{Number: 3, Name: "Season 3", EpisodeCount: 1, Aired: aired(-5),
		Episodes: []EpisodeInput{{ProviderID: 3001, Number: 1, Aired: aired(-5)}}}
	if err := r.store.Upsert(r.ctx, r.itemID,
		[]SeasonInput{specials, season1(3), season2(2), season3}); err != nil {
		t.Fatal(err)
	}
	seasons, _, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range seasons {
		if s.Number == 3 && !s.Monitored {
			t.Error("the new season of a series the operator follows arrived unmonitored")
		}
	}
	wanted, err := r.store.Wanted(r.ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(wanted) != 3 {
		t.Errorf("%d wanted, want season 2's two episodes and season 3's one", len(wanted))
	}
}

// Following only the specials is not following the series: the rule is about
// REGULAR seasons, and a Christmas-specials-only viewer's next regular season
// arrives off.
func TestFollowingOnlyTheSpecialsIsNotFollowingTheSeries(t *testing.T) {
	r := newEpRig(t)
	specials := SeasonInput{Number: 0, Name: "Specials", EpisodeCount: 1,
		Episodes: []EpisodeInput{{Number: 1, Aired: aired(-300)}}}
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{specials, season1(3)}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetSeasonMonitored(r.ctx, r.itemID, 0, true); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetSeasonMonitored(r.ctx, r.itemID, 1, false); err != nil {
		t.Fatal(err)
	}
	if err := r.store.Upsert(r.ctx, r.itemID,
		[]SeasonInput{specials, season1(3), season2(2)}); err != nil {
		t.Fatal(err)
	}
	seasons, _, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range seasons {
		if s.Number == 2 && s.Monitored {
			t.Error("a new regular season arrived monitored for an operator following only the specials")
		}
	}
}

// An episode search is told the episode and the series it belongs to.
func TestAnEpisodeIsFoundWithItsSeries(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3)}); err != nil {
		t.Fatal(err)
	}
	r.putFile(t, 1, 2, 0)
	_, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}

	sub, err := r.store.ForSearch(r.ctx, byNumber[1][1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if sub.SeriesTitle != "Severance" || sub.SeriesYear != 2022 || sub.TMDBID != 95396 {
		t.Errorf("series = %q (%d) tmdb %d", sub.SeriesTitle, sub.SeriesYear, sub.TMDBID)
	}
	if sub.Episode.SeasonNumber != 1 || sub.Episode.Number != 2 || !sub.Episode.Have() {
		t.Errorf("episode = S%02dE%02d have=%v", sub.Episode.SeasonNumber, sub.Episode.Number, sub.Episode.Have())
	}

	if _, err := r.store.ForSearch(r.ctx, 424242); !errors.Is(err, ErrNoSuchEpisode) {
		t.Errorf("a missing episode: err = %v, want ErrNoSuchEpisode", err)
	}
	if _, err := r.store.ForSearch(authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 9, State: authz.StateActive, MFASatisfied: true, Role: authz.Role{ID: 9, Rank: 0},
	}), byNumber[1][1].ID); err == nil {
		t.Error("a principal with no permissions read an episode")
	}
}

// A season's search is told the series and the season's listed episodes
// (ADR-0033).
func TestASeasonIsFoundWithItsSeriesAndEpisodes(t *testing.T) {
	r := newEpRig(t)
	// Season 4 and specials besides: a complete series reaches season 4,
	// never season 0's number (ADR-0057).
	s4, s0 := season1(1), season1(1)
	s4.Number, s4.Episodes[0].ProviderID = 4, 4001
	s0.Number, s0.Episodes[0].ProviderID = 0, 1
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3), s4, s0}); err != nil {
		t.Fatal(err)
	}
	r.putFile(t, 1, 2, 0)

	if sub, err := r.store.ForSeasonSearch(r.ctx, r.itemID, 0); err != nil || sub.LastSeason != 4 {
		t.Errorf("specials: last season %d (%v), want 4", sub.LastSeason, err)
	}
	sub, err := r.store.ForSeasonSearch(r.ctx, r.itemID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if sub.SeriesTitle != "Severance" || sub.SeriesYear != 2022 || sub.TMDBID != 95396 ||
		sub.ItemID != r.itemID || sub.Season != 1 {
		t.Errorf("subject = %+v", sub)
	}
	if fmt.Sprint(sub.Episodes) != "[1 2 3]" || sub.Have != 1 || sub.LastSeason != 4 {
		t.Errorf("episodes %v, have %d; want [1 2 3] and 1", sub.Episodes, sub.Have)
	}

	for _, season := range []int{2, -1} {
		if _, err := r.store.ForSeasonSearch(r.ctx, r.itemID, season); !errors.Is(err, ErrNoSuchSeason) {
			t.Errorf("season %d: err = %v, want ErrNoSuchSeason", season, err)
		}
	}
	if _, err := r.store.ForSeasonSearch(r.ctx, 424242, 1); !errors.Is(err, ErrNoSuchSeason) {
		t.Errorf("a missing series: err = %v, want ErrNoSuchSeason", err)
	}
	if _, err := r.store.ForSeasonSearch(authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 9, State: authz.StateActive, MFASatisfied: true, Role: authz.Role{ID: 9, Rank: 0},
	}), r.itemID, 1); err == nil {
		t.Error("a principal with no permissions read a season")
	}
}

// An episode's and a season's search are told the series' own quality profile
// (ADR-0035).
func TestASearchSubjectCarriesTheTitlesProfile(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(2)}); err != nil {
		t.Fatal(err)
	}
	res, err := r.db.ExecContext(r.ctx, `INSERT INTO quality_profile (name, allowed, cutoff, created_at, updated_at)
		VALUES ('Mine', '["WEBDL-1080p"]', 'WEBDL-1080p', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	mine, _ := res.LastInsertId()
	if _, err := r.db.ExecContext(r.ctx, `UPDATE media_item SET quality_profile_id = ? WHERE id = ?`,
		mine, r.itemID); err != nil {
		t.Fatal(err)
	}
	_, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if sub, err := r.store.ForSearch(r.ctx, byNumber[1][0].ID); err != nil || sub.QualityProfileID != mine {
		t.Errorf("episode subject profile %d (%v), want %d", sub.QualityProfileID, err, mine)
	}
	if sub, err := r.store.ForSeasonSearch(r.ctx, r.itemID, 1); err != nil || sub.QualityProfileID != mine {
		t.Errorf("season subject profile %d (%v), want %d", sub.QualityProfileID, err, mine)
	}
}

// ADR-0061: a series whose new seasons are not followed keeps the seasons it
// has as they are and takes the next one unmonitored; followed again, the one
// after arrives monitored. Only a visible series can be switched, and only by
// someone who may edit the library.
func TestASeriesNewSeasonsNeedNotBeFollowed(t *testing.T) {
	r := newEpRig(t)
	if set, err := r.store.SeriesSettings(r.ctx, r.itemID); err != nil || !set.FollowNewSeasons || !set.SeasonFolders {
		t.Fatalf("a new series: %+v %v; both on by default", set, err)
	}
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3)}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetSeriesFlag(r.ctx, r.itemID, FollowNewSeasons, false); err != nil {
		t.Fatal(err)
	}
	if set, _ := r.store.SeriesSettings(r.ctx, r.itemID); set.FollowNewSeasons || !set.SeasonFolders {
		t.Errorf("after switching one off: %+v", set)
	}
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3), season2(2)}); err != nil {
		t.Fatal(err)
	}
	seasons, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(seasons) != 2 || !seasons[0].Monitored || seasons[1].Monitored {
		t.Fatalf("seasons %+v: season 1 kept as it was, season 2 off", seasons)
	}
	for _, e := range byNumber[2] {
		if e.Monitored {
			t.Errorf("S02E%02d arrived monitored", e.Number)
		}
	}

	if err := r.store.SetSeriesFlag(r.ctx, r.itemID, FollowNewSeasons, true); err != nil {
		t.Fatal(err)
	}
	s3 := season2(1)
	s3.Number, s3.Name = 3, "Season 3"
	for i := range s3.Episodes {
		s3.Episodes[i].ProviderID += 100
	}
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(3), season2(2), s3}); err != nil {
		t.Fatal(err)
	}
	if seasons, _, _ := r.store.Seasons(r.ctx, r.itemID); len(seasons) != 3 || !seasons[2].Monitored || seasons[1].Monitored {
		t.Errorf("followed again: %+v", seasons)
	}

	if _, err := r.store.db.ExecContext(r.ctx, `INSERT INTO media_item (id, kind, title, sort_title, root_folder_id,
		folder, added_at, updated_at) VALUES (77, 'movie', 'Heat', 'heat', 1, 'Heat', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetSeriesFlag(r.ctx, 77, FollowNewSeasons, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("a film: %v", err)
	}
	if err := r.store.SetSeriesFlag(r.ctx, 424242, FollowNewSeasons, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("nothing: %v", err)
	}
	viewer := authz.WithPrincipal(context.Background(), &authz.Principal{UserID: 9, State: authz.StateActive,
		MFASatisfied: true, Role: authz.Role{ID: 9, Rank: 0, Permissions: authz.NewPermissionSet(authz.PermBrowse)}})
	if err := r.store.SetSeriesFlag(viewer, r.itemID, FollowNewSeasons, false); !authz.IsDenied(err) {
		t.Errorf("a viewer switched it: %v", err)
	}
	// Someone whose libraries do not include the series' root.
	elsewhere := scopedCtx([]int64{99}, 0)
	if _, err := r.store.SeriesSettings(elsewhere, r.itemID); !errors.Is(err, ErrNotFound) {
		t.Errorf("read from outside its library: %v", err)
	}
	if err := r.store.SetSeriesFlag(elsewhere, r.itemID, FollowNewSeasons, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("switched from outside its library: %v", err)
	}
}

// ADR-0063: season folders, the series' other switch, set on its own.
func TestASeriesSeasonFoldersAreSwitched(t *testing.T) {
	r := newEpRig(t)
	if err := r.store.SetSeriesFlag(r.ctx, r.itemID, SeasonFolders, false); err != nil {
		t.Fatal(err)
	}
	if set, err := r.store.SeriesSettings(r.ctx, r.itemID); err != nil || set.SeasonFolders || !set.FollowNewSeasons {
		t.Errorf("after switching season folders off: %+v %v", set, err)
	}
	if err := r.store.SetSeriesFlag(r.ctx, r.itemID, SeasonFolders, true); err != nil {
		t.Fatal(err)
	}
	if set, _ := r.store.SeriesSettings(r.ctx, r.itemID); !set.SeasonFolders {
		t.Errorf("back on: %+v", set)
	}
	if err := r.store.SetSeriesFlag(r.ctx, r.itemID, SeriesFlag(9), true); err == nil {
		t.Error("a switch that does not exist was set")
	}

	// Daily (ADR-0064), and the search subject says so.
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(1)}); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SetSeriesFlag(r.ctx, r.itemID, Daily, true); err != nil {
		t.Fatal(err)
	}
	if set, _ := r.store.SeriesSettings(r.ctx, r.itemID); !set.Daily || !set.SeasonFolders {
		t.Errorf("daily on: %+v", set)
	}
	seasons, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil || len(seasons) != 1 {
		t.Fatalf("%v %v", seasons, err)
	}
	if sub, err := r.store.ForSearch(r.ctx, byNumber[1][0].ID); err != nil || !sub.Daily {
		t.Errorf("the search subject: daily=%v %v", sub.Daily, err)
	}
}

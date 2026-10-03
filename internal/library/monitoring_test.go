package library

import (
	"errors"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// The monitoring choice made when a series is added (ADR-0025, decision 7).
//
// One series, four shapes of season, so every choice has something to decide
// about:
//
//	S00  two specials, both aired
//	S01  three episodes, aired more than a year ago
//	S02  the current season: two aired, one still to come
//	S03  announced: two episodes with no date

func addedSeries() []SeasonInput {
	specials := SeasonInput{Number: 0, Name: "Specials", EpisodeCount: 2, Episodes: []EpisodeInput{
		{ProviderID: 9001, Number: 1, Aired: aired(-300)},
		{ProviderID: 9002, Number: 2, Aired: aired(-100)},
	}}
	current := SeasonInput{Number: 2, Name: "Season 2", EpisodeCount: 3, Aired: aired(-20), Episodes: []EpisodeInput{
		{ProviderID: 2001, Number: 1, Aired: aired(-20)},
		{ProviderID: 2002, Number: 2, Aired: aired(-13)},
		{ProviderID: 2003, Number: 3, Aired: aired(7)},
	}}
	announced := SeasonInput{Number: 3, Name: "Season 3", EpisodeCount: 2, Episodes: []EpisodeInput{
		{ProviderID: 3001, Number: 1},
		{ProviderID: 3002, Number: 2},
	}}
	return []SeasonInput{specials, season1(3), current, announced}
}

// apply records seasons and applies a choice in one transaction, as an add does.
func (r *epRig) apply(t *testing.T, seasons []SeasonInput, m Monitoring) MonitoringResult {
	t.Helper()
	var res MonitoringResult
	err := r.database.InTx(r.ctx, func(tx db.Execer) error {
		if err := r.store.UpsertIn(r.ctx, tx, r.itemID, seasons); err != nil {
			return err
		}
		var err error
		res, err = r.store.ApplyMonitoring(r.ctx, tx, r.itemID, m)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// flags reads every season's flag and every episode's, as "S02" and "S02E03".
func (r *epRig) flags(t *testing.T) map[string]bool {
	t.Helper()
	seasons, eps, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, s := range seasons {
		out[code(s.Number, 0)] = s.Monitored
		for _, e := range eps[s.Number] {
			out[code(s.Number, e.Number)] = e.Monitored
		}
	}
	return out
}

func code(season, episode int) string {
	c := "S" + pad2(season)
	if episode > 0 {
		c += "E" + pad2(episode)
	}
	return c
}

func pad2(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// expect checks the flags named, all of them, and says every mismatch.
func expect(t *testing.T, got map[string]bool, on []string, off []string) {
	t.Helper()
	for _, c := range on {
		if v, ok := got[c]; !ok || !v {
			t.Errorf("%s should be monitored (present=%v)", c, ok)
		}
	}
	for _, c := range off {
		if v, ok := got[c]; !ok || v {
			t.Errorf("%s should NOT be monitored (present=%v)", c, ok)
		}
	}
}

func TestMonitoringAllWantsEveryEpisodeThatHasAired(t *testing.T) {
	r := newEpRig(t)
	res := r.apply(t, addedSeries(), MonitorAll)

	expect(t, r.flags(t),
		[]string{"S01", "S01E01", "S01E02", "S01E03", "S02", "S02E01", "S02E02", "S02E03", "S03", "S03E01", "S03E02"},
		[]string{"S00", "S00E01", "S00E02"})
	if res.Wanted != 5 || res.Monitored != 8 || res.Episodes != 10 || res.Seasons != 4 {
		t.Errorf("result = %+v; want 5 wanted (S01's three and S02's two aired), 8 monitored, "+
			"10 episodes, 4 seasons", res)
	}
	if res.Latest != 2 {
		t.Errorf("latest aired season = %d, want 2", res.Latest)
	}
}

// Future: nothing already aired is wanted — but the current season stays ON,
// with its aired episodes off, because a season switched off reads as
// "stopped following" and would make a renewal arrive off.
func TestMonitoringFutureWantsOnlyWhatIsStillToCome(t *testing.T) {
	r := newEpRig(t)
	res := r.apply(t, addedSeries(), MonitorFuture)

	expect(t, r.flags(t),
		[]string{"S02", "S02E03", "S03", "S03E01", "S03E02"},
		[]string{"S00", "S00E01", "S00E02", "S01", "S01E01", "S01E02", "S01E03", "S02E01", "S02E02"})
	if res.Wanted != 0 || res.Monitored != 3 {
		t.Errorf("result = %+v; want nothing wanted and three monitored (S02E03 and S03's two)", res)
	}
}

func TestMonitoringLatestFollowsTheCurrentSeasonAndAfter(t *testing.T) {
	r := newEpRig(t)
	res := r.apply(t, addedSeries(), MonitorLatest)

	expect(t, r.flags(t),
		[]string{"S02", "S02E01", "S02E02", "S02E03", "S03", "S03E01", "S03E02"},
		[]string{"S00", "S00E01", "S00E02", "S01", "S01E01", "S01E02", "S01E03"})
	if res.Wanted != 2 || res.Monitored != 5 {
		t.Errorf("result = %+v; want S02's two aired episodes wanted and five monitored", res)
	}
}

func TestMonitoringNoneWantsNothing(t *testing.T) {
	r := newEpRig(t)
	res := r.apply(t, addedSeries(), MonitorNone)

	expect(t, r.flags(t), nil,
		[]string{"S00", "S00E01", "S01", "S01E01", "S02", "S02E03", "S03", "S03E01", "S03E02"})
	if res.Wanted != 0 || res.Monitored != 0 {
		t.Errorf("result = %+v; want nothing monitored", res)
	}
}

// The property the choices were designed around: each one survives the series
// being renewed exactly as the person chose, through the ordinary refresh and
// without a series-level switch.
func TestEachChoiceMeetsARenewalAsChosen(t *testing.T) {
	for _, tc := range []struct {
		m      Monitoring
		follow bool
	}{
		{MonitorAll, true}, {MonitorFuture, true}, {MonitorLatest, true}, {MonitorNone, false},
	} {
		t.Run(string(tc.m), func(t *testing.T) {
			r := newEpRig(t)
			r.apply(t, addedSeries(), tc.m)
			before := r.flags(t)

			renewed := append(addedSeries(), SeasonInput{Number: 4, Name: "Season 4", EpisodeCount: 1,
				Episodes: []EpisodeInput{{ProviderID: 4001, Number: 1}}})
			if err := r.store.Upsert(r.ctx, r.itemID, renewed); err != nil {
				t.Fatal(err)
			}
			after := r.flags(t)

			if after["S04"] != tc.follow || after["S04E01"] != tc.follow {
				t.Errorf("season 4 arrived monitored=%v, want %v", after["S04"], tc.follow)
			}
			// And the refresh changed nothing the choice set.
			for c, v := range before {
				if after[c] != v {
					t.Errorf("%s changed from %v to %v on a refresh", c, v, after[c])
				}
			}
		})
	}
}

// The case "future" is shaped around: a FINISHED series — between seasons,
// everything aired, nothing announced. Nothing is wanted, and yet a renewal
// must arrive followed. Switching the last season off because nothing in it is
// wanted would read as "stopped following", and the new season would arrive
// off.
func TestFutureOnAFinishedSeriesStillFollowsARenewal(t *testing.T) {
	r := newEpRig(t)
	finished := func() []SeasonInput {
		last := SeasonInput{Number: 2, Name: "Season 2", EpisodeCount: 2, Episodes: []EpisodeInput{
			{ProviderID: 2001, Number: 1, Aired: aired(-200)},
			{ProviderID: 2002, Number: 2, Aired: aired(-193)},
		}}
		return []SeasonInput{season1(3), last}
	}
	res := r.apply(t, finished(), MonitorFuture)
	if res.Wanted != 0 || res.Monitored != 0 {
		t.Fatalf("result = %+v; a finished series under future wants nothing", res)
	}
	expect(t, r.flags(t), []string{"S02"}, []string{"S01", "S02E01", "S02E02"})

	renewed := append(finished(), SeasonInput{Number: 3, Name: "Season 3", EpisodeCount: 1,
		Episodes: []EpisodeInput{{ProviderID: 3001, Number: 1, Aired: aired(30)}}})
	if err := r.store.Upsert(r.ctx, r.itemID, renewed); err != nil {
		t.Fatal(err)
	}
	expect(t, r.flags(t), []string{"S03", "S03E01"}, nil)
}

// An air date is a date. One dated today is still to come under "future" —
// the provider's day is often already yesterday where the server is.
func TestAnEpisodeDatedTodayIsStillToCome(t *testing.T) {
	r := newEpRig(t)
	today := time.Date(testNow.Year(), testNow.Month(), testNow.Day(), 0, 0, 0, 0, time.UTC)
	seasons := []SeasonInput{{Number: 1, EpisodeCount: 2, Episodes: []EpisodeInput{
		{ProviderID: 1, Number: 1, Aired: today.AddDate(0, 0, -7)},
		{ProviderID: 2, Number: 2, Aired: today},
	}}}
	res := r.apply(t, seasons, MonitorFuture)

	expect(t, r.flags(t), []string{"S01", "S01E02"}, []string{"S01E01"})
	// Aired (midnight has passed) and monitored: wanted on the day it aired.
	if res.Wanted != 1 {
		t.Errorf("wanted = %d, want today's episode", res.Wanted)
	}
}

// A series nothing of which has aired has no "latest aired season". Every
// regular season starts on, whichever choice short of none was made.
func TestASeriesNothingOfWhichHasAiredIsFollowedWhole(t *testing.T) {
	for _, m := range []Monitoring{MonitorAll, MonitorFuture, MonitorLatest} {
		t.Run(string(m), func(t *testing.T) {
			r := newEpRig(t)
			seasons := []SeasonInput{
				{Number: 1, EpisodeCount: 1, Episodes: []EpisodeInput{{ProviderID: 1, Number: 1, Aired: aired(30)}}},
				{Number: 2, EpisodeCount: 1, Episodes: []EpisodeInput{{ProviderID: 2, Number: 1}}},
			}
			res := r.apply(t, seasons, m)
			expect(t, r.flags(t), []string{"S01", "S01E01", "S02", "S02E01"}, nil)
			if res.Latest != 0 || res.Wanted != 0 {
				t.Errorf("result = %+v; nothing has aired, so no latest season and nothing wanted", res)
			}
		})
	}
}

// The outcome is the choice's, whatever the flags said before it.
func TestApplyingAChoiceIgnoresWhatTheFlagsSaidBefore(t *testing.T) {
	r := newEpRig(t)
	r.apply(t, addedSeries(), MonitorNone)
	if _, err := r.db.ExecContext(r.ctx, `UPDATE season SET monitored = 1 WHERE item_id = ? AND number IN (0, 1)`, r.itemID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(r.ctx, `UPDATE episode SET monitored = 1 WHERE item_id = ? AND season_number = 0`, r.itemID); err != nil {
		t.Fatal(err)
	}
	r.apply(t, addedSeries(), MonitorLatest)
	expect(t, r.flags(t),
		[]string{"S02", "S02E01", "S03"},
		[]string{"S00", "S00E01", "S01", "S01E01"})
}

// A held episode is not wanted, whatever the choice: the count uses the wanted
// list's own rule.
func TestAHeldEpisodeIsNotCountedAsWanted(t *testing.T) {
	r := newEpRig(t)
	r.putFile(t, 1, 1, 2)
	res := r.apply(t, addedSeries(), MonitorAll)
	if res.Wanted != 3 {
		t.Errorf("wanted = %d; S01E01-E02 are held, so S01E03 and S02's two aired remain", res.Wanted)
	}
}

func TestThereIsNoDefaultChoice(t *testing.T) {
	for _, bad := range []string{"", " ", "some", "monitored", "future episodes"} {
		if _, err := ParseMonitoring(bad); !errors.Is(err, ErrNoSuchMonitoring) {
			t.Errorf("%q: err = %v, want ErrNoSuchMonitoring", bad, err)
		}
	}
	for in, want := range map[string]Monitoring{
		"all": MonitorAll, "Future": MonitorFuture, " latest ": MonitorLatest, "NONE": MonitorNone,
	} {
		if got, err := ParseMonitoring(in); err != nil || got != want {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
	// And the store refuses an unparsed one as well, rather than trusting the
	// caller to have parsed it.
	r := newEpRig(t)
	err := r.database.InTx(r.ctx, func(tx db.Execer) error {
		_, err := r.store.ApplyMonitoring(r.ctx, tx, r.itemID, Monitoring("everything"))
		return err
	})
	if !errors.Is(err, ErrNoSuchMonitoring) {
		t.Errorf("err = %v", err)
	}
}

// Choosing what is wanted is editing the library. Somebody who may only browse
// cannot, even through the store directly.
func TestChoosingWhatIsWantedNeedsThePermissionToEditTheLibrary(t *testing.T) {
	r := newEpRig(t)
	r.apply(t, addedSeries(), MonitorNone)
	viewer := viewerCtx()
	err := r.database.InTx(viewer, func(tx db.Execer) error {
		_, err := r.store.ApplyMonitoring(viewer, tx, r.itemID, MonitorAll)
		return err
	})
	if !authz.IsDenied(err) {
		t.Fatalf("err = %v, want a denial", err)
	}
	if got := r.flags(t); got["S01"] {
		t.Error("the flags changed anyway")
	}
}

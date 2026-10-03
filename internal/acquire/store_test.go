package acquire

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
)

// What a pass acts on and what the Wanted screen shows are the same list —
// written twice, because the screen's queries are capped and a pass must see
// everything. This holds the two to one answer over every condition either
// has: monitored or not, aired or not, announced, held by a single file or a
// range, a film with a file, an unmonitored film, a film with no year.
func TestTheWantedListIsTheWantedScreens(t *testing.T) {
	r := newRig(t, Config{})
	ctx := r.browse()

	// A series with a past season, a partly-aired one, and an announced
	// episode.
	sev := r.addItem("series", "Severance", 2022, 95396, start)
	past := start.AddDate(0, 0, -30)
	season := func(n int, aired ...time.Time) library.SeasonInput {
		in := library.SeasonInput{Number: n, Name: fmt.Sprint("Season ", n), EpisodeCount: len(aired)}
		for i, a := range aired {
			in.Episodes = append(in.Episodes, library.EpisodeInput{
				ProviderID: int64(n*100 + i + 1), Number: i + 1, Title: "x", Aired: a})
		}
		return in
	}
	if err := r.episodes.Upsert(ctx, sev, []library.SeasonInput{
		season(1, past, past, past, past, past, past, past, past, past),
		season(2, past, start.Add(-time.Hour), start.Add(time.Hour), time.Time{}),
	}); err != nil {
		t.Fatal(err)
	}
	r.haveEpisodes(sev, 1, 1, 3) // a range file: E01-E03
	r.haveEpisodes(sev, 1, 5, 5)
	if err := r.episodes.SetEpisodeMonitored(ctx, r.episodeID(sev, 1, 7), false); err != nil {
		t.Fatal(err)
	}

	// A series nobody follows any more.
	office := r.addSeries("The Office", 2005, 2316, 2)
	if err := r.episodes.SetSeasonMonitored(ctx, office, 1, false); err != nil {
		t.Fatal(err)
	}

	// Films: wanted, held, unmonitored, and one with no year.
	dune := r.addFilm("Dune", 2021, 438631)
	arrival := r.addFilm("Arrival", 2016, 329865)
	r.haveFilm(arrival)
	heat := r.addFilm("Heat", 1995, 949)
	media := importer.NewStore(r.database, r.clock.now)
	if _, err := media.SetFilmMonitored(ctx, heat, false); err != nil {
		t.Fatal(err)
	}
	nameless := r.addFilm("Nameless", 0, 0)

	// The screen's answer.
	screen := map[Key]bool{}
	eps, err := r.episodes.Wanted(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range eps {
		screen[Key{ItemID: e.ItemID, Season: e.SeasonNumber, Episode: e.Number}] = true
	}
	films, err := media.WantedFilms(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range films {
		screen[Key{Film: true, ItemID: f.ID}] = true
	}

	// This package's.
	wanted, err := r.store.Wanted(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	mine := map[Key]bool{}
	for _, w := range wanted {
		mine[w.Key()] = true
		still, err := r.store.StillWanted(r.ctx, w)
		if err != nil || !still {
			t.Errorf("%s is on the list but StillWanted says %v (%v)", w.Name(), still, err)
		}
	}

	if fmt.Sprint(sortedKeys(mine)) != fmt.Sprint(sortedKeys(screen)) {
		t.Fatalf("the wanted list and the Wanted screen disagree:\n  pass:   %v\n  screen: %v",
			sortedKeys(mine), sortedKeys(screen))
	}
	// And the answer is the one worked out by hand, so agreeing on nothing
	// would not pass.
	want := []Key{
		{ItemID: sev, Season: 1, Episode: 4}, {ItemID: sev, Season: 1, Episode: 6},
		{ItemID: sev, Season: 1, Episode: 8}, {ItemID: sev, Season: 1, Episode: 9},
		{ItemID: sev, Season: 2, Episode: 1}, {ItemID: sev, Season: 2, Episode: 2},
		{Film: true, ItemID: dune}, {Film: true, ItemID: nameless},
	}
	if fmt.Sprint(sortedKeys(mine)) != fmt.Sprint(sortedKeys(keySet(want))) {
		t.Fatalf("wanted = %v, want %v", sortedKeys(mine), sortedKeys(keySet(want)))
	}

	// StillWanted says no to each way of not being wanted.
	for name, w := range map[string]Want{
		"held by a range file": {ItemID: sev, EpisodeID: r.episodeID(sev, 1, 2), Season: 1, Episode: 2},
		"unmonitored episode":  {ItemID: sev, EpisodeID: r.episodeID(sev, 1, 7), Season: 1, Episode: 7},
		"not aired yet":        {ItemID: sev, EpisodeID: r.episodeID(sev, 2, 3), Season: 2, Episode: 3},
		"announced":            {ItemID: sev, EpisodeID: r.episodeID(sev, 2, 4), Season: 2, Episode: 4},
		"unmonitored season":   {ItemID: office, EpisodeID: r.episodeID(office, 1, 1), Season: 1, Episode: 1},
		"a film with a file":   {Film: true, ItemID: arrival},
		"an unmonitored film":  {Film: true, ItemID: heat},
		"a series, as a film":  {Film: true, ItemID: sev},
	} {
		still, err := r.store.StillWanted(r.ctx, w)
		if err != nil || still {
			t.Errorf("%s: StillWanted = %v, %v; want false", name, still, err)
		}
	}
}

func keySet(keys []Key) map[Key]bool {
	out := map[Key]bool{}
	for _, k := range keys {
		out[k] = true
	}
	return out
}

func sortedKeys(m map[Key]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, fmt.Sprintf("%v/%d/%02d/%02d", k.Film, k.ItemID, k.Season, k.Episode))
	}
	sort.Strings(out)
	return out
}

// In flight is queued, downloading, or finished and not imported — unless the
// import SKIPPED it, which is the release's fault, so the item is wanted again.
// A FAILED import is the instance's fault, retried, and stays in flight.
func TestWhatCountsAsInFlight(t *testing.T) {
	r := newRig(t, Config{})
	sev := r.addSeries("Severance", 2022, 95396, 6)
	dune := r.addFilm("Dune", 2021, 438631)
	ep := func(n int) *download.Target { return &download.Target{ItemID: sev, Season: 1, Episode: n} }

	r.queued(1, "Severance.S01E01.1080p.WEB-DL.H264-A", download.StatusQueued, ep(1))
	r.queued(2, "Severance.S01E02.1080p.WEB-DL.H264-A", download.StatusDownloading, ep(2))
	r.queued(3, "Severance.S01E03.1080p.WEB-DL.H264-A", download.StatusComplete, ep(3)) // not imported yet
	r.queued(4, "Severance.S01E04.1080p.WEB-DL.H264-A", download.StatusComplete, ep(4))
	r.imported(4, "failed") // the disk: still in flight
	r.queued(5, "Severance.S01E05.1080p.WEB-DL.H264-A", download.StatusComplete, ep(5))
	r.imported(5, "failed")
	r.clock.advance(time.Minute)
	// Then the release: wanted again.
	r.imported(5, "skipped")
	// Removed by a person.
	r.queued(6, "Severance.S01E06.1080p.WEB-DL.H264-A", download.StatusStopped, ep(6))
	r.queued(7, "Dune.2021.1080p.BluRay.x264-A", download.StatusDownloading,
		&download.Target{ItemID: dune, Film: true})
	r.queued(8, "Something.Else.2020.1080p.BluRay.x264-A", download.StatusDownloading, nil)

	got, err := r.store.InFlight(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := keySet([]Key{
		{ItemID: sev, Season: 1, Episode: 1}, {ItemID: sev, Season: 1, Episode: 2},
		{ItemID: sev, Season: 1, Episode: 3}, {ItemID: sev, Season: 1, Episode: 4},
		{Film: true, ItemID: dune},
	})
	if fmt.Sprint(sortedKeys(got)) != fmt.Sprint(sortedKeys(want)) {
		t.Fatalf("in flight = %v, want %v", sortedKeys(got), sortedKeys(want))
	}

	// Every one of them is in the queue, and so is never grabbed again —
	// including the removed one and the one the import skipped.
	for n := 1; n <= 8; n++ {
		if q, err := r.store.Queued(r.ctx, hash(n)); err != nil || !q {
			t.Errorf("release %d: Queued = %v, %v", n, q, err)
		}
	}
	if q, err := r.store.Queued(r.ctx, hash(99)); err != nil || q {
		t.Errorf("an unknown hash is queued? %v, %v", q, err)
	}
	// Compared as the queue stores it, whatever case the indexer used. A hash
	// with letters in it: an all-digit one reads the same in either case.
	r.queued(0xabcdef, "Something.Else.2021.1080p.BluRay.x264-B", download.StatusStopped, nil)
	if q, err := r.store.Queued(r.ctx, " "+strings.ToUpper(hash(0xabcdef))+" "); err != nil || !q {
		t.Errorf("an upper-case hash of a queued release: %v, %v", q, err)
	}
}

// A double episode grabbed for its first episode keeps its second from being
// grabbed separately; a name spanning thousands of episodes is not believed.
func TestADownloadCoversEveryEpisodeItsNameSpans(t *testing.T) {
	r := newRig(t, Config{})
	sev := r.addSeries("Severance", 2022, 95396, 9)
	r.queued(1, "Severance.S01E01E02.1080p.WEB-DL.H264-A", download.StatusDownloading,
		&download.Target{ItemID: sev, Season: 1, Episode: 1})
	r.queued(2, "Severance.S01E05-E99.1080p.WEB-DL.H264-A", download.StatusDownloading,
		&download.Target{ItemID: sev, Season: 1, Episode: 5})
	// Named for a different episode than it was grabbed for: only the target.
	r.queued(3, "Severance.S01E08.1080p.WEB-DL.H264-A", download.StatusDownloading,
		&download.Target{ItemID: sev, Season: 1, Episode: 7})

	got, err := r.store.InFlight(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := keySet([]Key{
		{ItemID: sev, Season: 1, Episode: 1}, {ItemID: sev, Season: 1, Episode: 2},
		{ItemID: sev, Season: 1, Episode: 5}, {ItemID: sev, Season: 1, Episode: 7},
	})
	if fmt.Sprint(sortedKeys(got)) != fmt.Sprint(sortedKeys(want)) {
		t.Fatalf("in flight = %v, want %v", sortedKeys(got), sortedKeys(want))
	}
}

// The store refuses a principal without the authority, and records only the
// three outcomes the table allows.
func TestTheStoreChecksWhoIsAsking(t *testing.T) {
	r := newRig(t, Config{})
	nobody := context.Background()
	if _, err := r.store.Wanted(nobody); !authz.IsDenied(err) {
		t.Errorf("Wanted with no principal: %v", err)
	}
	if _, err := r.store.States(nobody); !authz.IsDenied(err) {
		t.Errorf("States with no principal: %v", err)
	}
	if _, err := r.store.InFlight(nobody); !authz.IsDenied(err) {
		t.Errorf("InFlight with no principal: %v", err)
	}
	// The import task may browse; it may not record searches.
	importTask := authz.SystemPrincipal(context.Background(), authz.TaskImport)
	dune := r.addFilm("Dune", 2021, 438631)
	w := Want{Film: true, ItemID: dune, Title: "Dune", Year: 2021}
	if err := r.store.Record(importTask, w, State{Outcome: OutcomeNothing}); !authz.IsDenied(err) {
		t.Errorf("Record as the import task: %v", err)
	}
	if err := r.store.Record(r.ctx, w, State{Outcome: "maybe"}); err == nil {
		t.Error("an outcome the table does not allow was recorded")
	}
	if err := r.store.Record(r.ctx, Want{Film: true}, State{Outcome: OutcomeNothing}); err == nil {
		t.Error("a state with no item was recorded")
	}

	// Recorded twice, kept once, and read back as written.
	at := r.clock.now()
	for i := 1; i <= 2; i++ {
		if err := r.store.Record(r.ctx, w, State{SearchedAt: at, NextAt: at.Add(6 * time.Hour),
			Fruitless: i, Outcome: OutcomeNothing, Detail: fmt.Sprint("pass ", i)}); err != nil {
			t.Fatal(err)
		}
	}
	st, ok := r.state(StateKey{Film: true, ID: dune})
	if !ok || st.Fruitless != 2 || st.Detail != "pass 2" || !st.NextAt.Equal(at.Add(6*time.Hour)) ||
		!st.SearchedAt.Equal(at) {
		t.Fatalf("state = %+v, %v", st, ok)
	}
	var rows int
	if err := r.database.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM acquire_state`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("%d state rows (%v), want 1", rows, err)
	}
}

// A recorded explanation is bounded, and not cut through a character.
func TestADetailIsClippedWhole(t *testing.T) {
	long := ""
	for len(long) < maxDetail+50 {
		long += "Amélie "
	}
	got := clip(long, maxDetail)
	if len(got) > maxDetail {
		t.Fatalf("clipped to %d bytes, over %d", len(got), maxDetail)
	}
	for i, r := range got {
		if r == '�' {
			t.Fatalf("a character was cut at byte %d: %q", i, got[len(got)-10:])
		}
	}
	if clip("short", maxDetail) != "short" {
		t.Fatal("a short detail was changed")
	}
}

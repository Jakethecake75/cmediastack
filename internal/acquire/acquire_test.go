package acquire

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/indexer"
)

// wantSeverance is the common starting point: a series whose only wanted
// episode is S02E03.
func wantSeverance(r *rig) (item, episodeID int64) {
	r.t.Helper()
	item = r.addSeries("Severance", 2022, 95396, 9, 3)
	r.haveEpisodes(item, 1, 1, 9)
	r.haveEpisodes(item, 2, 1, 2)
	return item, r.episodeID(item, 2, 3)
}

const good = "Severance.S02E03.1080p.WEB-DL.H264-GRP"

// The whole point: an episode that is wanted arrives, found in the indexers'
// recent releases, with nobody pressing anything — and it is recorded exactly
// as a person's grab is, save for who.
func TestARecentReleaseOfAWantedEpisodeIsGrabbed(t *testing.T) {
	r := newRig(t, Config{})
	sev, epID := wantSeverance(r)
	r.client.feed = []indexer.Result{
		rel("Some.Other.Show.S01E01.1080p.WEB-DL.H264-GRP", 2, 50),
		rel(good, 1, 25),
		rel("Severance.S02E02.1080p.WEB-DL.H264-GRP", 3, 30), // already on disk
	}

	summary := r.runRecent()

	if got := r.targets(); fmt.Sprint(got) != fmt.Sprintf("[%d S02E03]", sev) {
		t.Fatalf("grabbed for %v, want only S02E03 (summary: %s)", got, summary)
	}
	meta := r.queue.added()[0]
	if meta.Title != good || meta.AddedLabel != Label || meta.AddedBy != nil ||
		meta.IndexerName != "Tracker" || meta.IndexerID != 1 {
		t.Fatalf("queued with %+v", meta)
	}
	if !strings.Contains(summary, "Grabbed Severance S02E03 ("+good+", from Tracker)") {
		t.Errorf("summary does not say what was grabbed: %s", summary)
	}

	// One request for the feed — an empty search, a page of it — and one
	// fetch, of the release that was grabbed.
	queries, fetched := r.client.snapshot()
	if len(queries) != 1 || queries[0].Term != "" || queries[0].Limit != RecentLimit ||
		queries[0].Season != -1 {
		t.Fatalf("queries = %+v", queries)
	}
	if len(fetched) != 1 || !strings.HasSuffix(fetched[0], hash(1)) {
		t.Fatalf("fetched %v", fetched)
	}

	// On the record, as a person's grab is: who (no person), what, for what,
	// how it was found.
	var actorID *int64
	var label, action, outcome, target, detail string
	if err := r.database.QueryRowContext(context.Background(), `
		SELECT actor_user_id, actor_label, action, outcome, target_id, detail
		FROM audit_event WHERE action = 'acquisition.grabbed'`).Scan(
		&actorID, &label, &action, &outcome, &target, &detail); err != nil {
		t.Fatal(err)
	}
	if actorID != nil || label != "system:acquire" || outcome != "success" || target != hash(1) ||
		!strings.Contains(detail, good) || !strings.Contains(detail, "for Severance S02E03") ||
		!strings.Contains(detail, "recent releases") || strings.Contains(detail, "https://") {
		t.Fatalf("audit: actor=%v %q %q %q %q", actorID, label, outcome, target, detail)
	}

	st, ok := r.state(StateKey{ID: epID})
	if !ok || st.Outcome != OutcomeGrabbed || !strings.Contains(st.Detail, good) {
		t.Fatalf("state = %+v, %v", st, ok)
	}

	// The next pass grabs nothing more: the item is in flight, and the
	// release is in the queue.
	r.client.reset()
	r.clock.advance(15 * time.Minute)
	if s := r.runRecent(); len(r.queue.added()) != 1 {
		t.Fatalf("a second pass grabbed again: %s", s)
	}
	if q, _ := r.client.snapshot(); len(q) != 0 {
		t.Fatalf("with everything wanted in flight, an indexer was still asked: %+v", q)
	}
}

// Nothing wanted, nothing asked: a pass that has nothing to look for spends
// nobody's rate limit.
func TestNothingIsAskedWhenNothingIsWanted(t *testing.T) {
	r := newRig(t, Config{})
	if s := r.runRecent(); !strings.Contains(s, "nothing is wanted") {
		t.Fatalf("summary = %q", s)
	}
	if s := r.runSearch(); !strings.Contains(s, "nothing is wanted") {
		t.Fatalf("summary = %q", s)
	}
	// A film with no year is wanted, but nothing could ever match it.
	r.addFilm("Nameless", 0, 0)
	if s := r.runRecent(); !strings.Contains(s, "no indexer was asked") ||
		!strings.Contains(s, "1 film(s) with no year to search by") {
		t.Fatalf("summary = %q", s)
	}
	if s := r.runSearch(); !strings.Contains(s, "nothing is due (1 wanted, 1 film(s) with no year") {
		t.Fatalf("summary = %q", s)
	}
	if q, _ := r.client.snapshot(); len(q) != 0 {
		t.Fatalf("an indexer was asked: %+v", q)
	}
}

// The queue is the blocklist. A release a person removed is never grabbed
// again, whether its hash was in the feed or only known once fetched — and a
// release found out that way is not fetched again next pass.
func TestTheQueueIsTheBlocklist(t *testing.T) {
	r := newRig(t, Config{})
	sev, _ := wantSeverance(r)
	target := &download.Target{ItemID: sev, Season: 2, Episode: 3}
	r.queued(1, good, download.StatusStopped, target) // "not that one"

	// Known by hash in the feed: not even fetched. Another release of the
	// same episode is taken instead.
	other := rel("Severance.S02E03.720p.HDTV.x264-OTHER", 4, 10)
	r.client.feed = []indexer.Result{rel(good, 1, 25), other}
	r.runRecent()
	if _, fetched := r.client.snapshot(); len(fetched) != 1 || !strings.HasSuffix(fetched[0], hash(4)) {
		t.Fatalf("fetched %v, want only the other release", fetched)
	}
	if got := r.targets(); len(got) != 1 || r.queue.added()[0].Title != other.Title {
		t.Fatalf("grabbed %v", got)
	}

	// Known only once fetched: the feed carried no hash.
	r2 := newRig(t, Config{})
	sev2, _ := wantSeverance(r2)
	r2.queued(5, good, download.StatusStopped, &download.Target{ItemID: sev2, Season: 2, Episode: 3})
	hidden := rel(good, 5, 25)
	hidden.InfoHash = ""
	r2.client.feed = []indexer.Result{hidden}
	s := r2.runRecent()
	if len(r2.queue.added()) != 0 || !strings.Contains(s, "grabbed before") {
		t.Fatalf("a removed release was grabbed again: %s", s)
	}
	r2.clock.advance(15 * time.Minute)
	r2.runRecent()
	if _, fetched := r2.client.snapshot(); len(fetched) != 1 {
		t.Fatalf("fetched %d times, want once: the second pass should remember", len(fetched))
	}
}

// One download per wanted item. A download in flight — including one whose
// import FAILED, which is the instance's problem — keeps the item from being
// grabbed or searched; one the import SKIPPED frees it.
func TestOneDownloadPerWantedItem(t *testing.T) {
	r := newRig(t, Config{})
	sev, _ := wantSeverance(r)
	target := &download.Target{ItemID: sev, Season: 2, Episode: 3}
	r.queued(7, "Severance.S02E03.720p.HDTV.x264-A", download.StatusDownloading, target)
	r.client.feed = []indexer.Result{rel(good, 1, 25)}
	r.client.byTerm["severance"] = []indexer.Result{rel(good, 1, 25)}

	check := func(when string) {
		t.Helper()
		r.runRecent()
		r.runSearch()
		if len(r.queue.added()) != 0 {
			t.Fatalf("%s: grabbed %v", when, r.targets())
		}
		if q, _ := r.client.snapshot(); len(q) != 0 {
			t.Fatalf("%s: an indexer was asked about an item in flight: %+v", when, q)
		}
	}
	check("downloading")
	if err := r.queueDB.SetStatus(context.Background(), hash(7), download.StatusComplete); err != nil {
		t.Fatal(err)
	}
	check("finished, not imported")
	r.imported(7, "failed")
	check("import failed")

	r.clock.advance(time.Minute)
	r.imported(7, "skipped")
	r.runRecent()
	if got := r.targets(); fmt.Sprint(got) != fmt.Sprintf("[%d S02E03]", sev) {
		t.Fatalf("after the import skipped the download, grabbed %v", got)
	}
}

// What a machine may not choose: a dead torrent, and anything the default
// profile refuses. The reason is kept where the operator will look.
func TestDeadAndRefusedReleasesAreNotGrabbed(t *testing.T) {
	r := newRig(t, Config{})
	_, epID := wantSeverance(r)
	offered := []indexer.Result{
		rel("Severance.S02E03.1080p.WEB-DL.H264-DEAD", 1, 0),
		rel("Severance.S02E03.480p.WEB-DL.x264-LOW", 2, 90),
	}
	r.client.feed = offered
	r.client.byTerm["severance"] = offered

	s := r.runRecent()
	if len(r.queue.added()) != 0 || !strings.Contains(s, "Not grabbed: Severance S02E03 — it has no seeders") {
		t.Fatalf("summary = %s", s)
	}
	s = r.runSearch()
	if len(r.queue.added()) != 0 {
		t.Fatalf("grabbed %v", r.targets())
	}
	st, _ := r.state(StateKey{ID: epID})
	if st.Outcome != OutcomeNothing || st.Fruitless != 1 ||
		!st.NextAt.Equal(r.clock.now().Add(6*time.Hour)) ||
		!strings.Contains(st.Detail, "2 of 2 result(s) were Severance S02E03, and none could be grabbed") ||
		!strings.Contains(st.Detail, "no seeders") || !strings.Contains(st.Detail, "HD-1080p") {
		t.Fatalf("state = %+v (summary %s)", st, s)
	}
}

// A release named in a language is a dub, or the original of a title not in
// English; a machine does not choose between them. One that carries the
// original as well — MULTi, dual audio — is not refused.
func TestAReleaseNamedInALanguageIsAPersonsChoice(t *testing.T) {
	r := newRig(t, Config{})
	sev, _ := wantSeverance(r)
	r.client.feed = []indexer.Result{
		rel("Severance.S02E03.German.1080p.WEB-DL.H264-GER", 1, 90),
		rel("Severance.S02E03.MULTi.1080p.WEB-DL.H264-MUL", 2, 10),
	}
	r.runRecent()
	if got := r.queue.added(); len(got) != 1 || got[0].Title != "Severance.S02E03.MULTi.1080p.WEB-DL.H264-MUL" {
		t.Fatalf("grabbed %v, want the MULTi release for %d", got, sev)
	}

	r2 := newRig(t, Config{})
	wantSeverance(r2)
	r2.client.feed = []indexer.Result{rel("Severance.S02E03.1080p.WEB-DL.TRUEFRENCH.H264-FR", 1, 90)}
	if s := r2.runRecent(); len(r2.queue.added()) != 0 || !strings.Contains(s, "it is named in French") {
		t.Fatalf("grabbed %v: %s", r2.targets(), s)
	}
}

// "The.Office.S01E02" is two different shows. A machine that cannot tell which
// grabs it for neither — whether both are wanted or only one is in the library
// with nothing wanted.
func TestANameThatFitsTwoTitlesIsGrabbedForNeither(t *testing.T) {
	r := newRig(t, Config{})
	us := r.addSeries("The Office", 2005, 2316, 3)
	uk := r.addSeries("The Office", 2001, 2996, 3)

	r.client.feed = []indexer.Result{rel("The.Office.S01E02.720p.HDTV.x264-GRP", 1, 10)}
	if s := r.runRecent(); len(r.queue.added()) != 0 || !strings.Contains(s, "its name fits both") {
		t.Fatalf("grabbed %v: %s", r.targets(), s)
	}

	// A year settles it.
	r.client.feed = []indexer.Result{rel("The.Office.2005.S01E02.720p.HDTV.x264-GRP", 2, 10)}
	r.runRecent()
	if got := r.targets(); fmt.Sprint(got) != fmt.Sprintf("[%d S01E02]", us) {
		t.Fatalf("grabbed %v, want the 2005 series", got)
	}

	// The US series is complete; only the UK one is wanted. The release still
	// fits both titles in the library.
	r.haveEpisodes(us, 1, 1, 3)
	r.client.feed = []indexer.Result{rel("The.Office.S01E03.720p.HDTV.x264-GRP", 3, 10)}
	s := r.runRecent()
	if len(r.queue.added()) != 1 || !strings.Contains(s, "also fits The Office (2005)") {
		t.Fatalf("grabbed %v: %s", r.targets(), s)
	}
	_ = uk
}

// Two wanted series with different titles can answer to the same scene name:
// the American "Kitchen Nightmares" and "Ramsay's Kitchen Nightmares", which
// TMDB also calls "Kitchen Nightmares". Neither title is the other's, so only
// the match against the whole wanted list sees that the name fits both.
func TestANameTwoWantedTitlesAnswerToIsGrabbedForNeither(t *testing.T) {
	r := newRig(t, Config{})
	r.addSeries("Kitchen Nightmares", 2007, 2296, 3)
	r.addSeries("Ramsay's Kitchen Nightmares", 2004, 1220, 3)
	r.titles.series[1220] = []string{"Kitchen Nightmares"}
	r.client.feed = []indexer.Result{rel("Kitchen.Nightmares.S01E02.720p.HDTV.x264-GRP", 1, 10)}

	s := r.runRecent()
	if len(r.queue.added()) != 0 || !strings.Contains(s, "its name fits both") {
		t.Fatalf("grabbed %v: %s", r.targets(), s)
	}
}

// The budget: a few items a pass, never-searched first and newest first, then
// whichever has waited longest; and a fruitless search waits longer each time.
func TestTheSearchPassIsBudgetedAndBacksOff(t *testing.T) {
	r := newRig(t, Config{SearchesPerRun: 2})
	sev := r.addSeries("Severance", 2022, 95396, 5)

	searchedFor := func() []int {
		t.Helper()
		q, _ := r.client.snapshot()
		r.client.reset()
		var eps []int
		for _, x := range q {
			if !strings.EqualFold(x.Term, "severance") || x.Season != 1 {
				t.Fatalf("query %+v", x)
			}
			eps = append(eps, x.Episode)
		}
		return eps
	}

	passes := [][]int{{5, 4}, {3, 2}, {1}, nil}
	for i, want := range passes {
		s := r.runSearch()
		if got := searchedFor(); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("pass %d searched for %v, want %v (%s)", i+1, got, want, s)
		}
		r.clock.advance(time.Minute)
	}

	// Six hours after the first searches, all five are due again, the longest
	// waiting first.
	r.clock.advance(6 * time.Hour)
	r.runSearch()
	if got := searchedFor(); fmt.Sprint(got) != "[5 4]" {
		t.Fatalf("after the back-off, searched for %v", got)
	}
	st, _ := r.state(StateKey{ID: r.episodeID(sev, 1, 5)})
	if st.Fruitless != 2 || !st.NextAt.Equal(r.clock.now().Add(12*time.Hour)) {
		t.Fatalf("second fruitless search: %+v", st)
	}
}

func TestBackoffDoublesToAWeek(t *testing.T) {
	want := map[int]time.Duration{
		0: 6 * time.Hour, 1: 6 * time.Hour, 2: 12 * time.Hour, 3: 24 * time.Hour,
		4: 48 * time.Hour, 5: 96 * time.Hour, 6: 7 * 24 * time.Hour, 50: 7 * 24 * time.Hour,
	}
	for n, d := range want {
		if got := backoff(n); got != d {
			t.Errorf("backoff(%d) = %v, want %v", n, got, d)
		}
	}
}

// A search that failed looked for nothing: it is retried in an hour, does not
// lengthen the wait, and a pass in which every search failed is red.
func TestAFailedSearchIsRetriedInAnHour(t *testing.T) {
	r := newRig(t, Config{})
	dune := r.addFilm("Dune", 2021, 438631)
	w := Want{Film: true, ItemID: dune, Title: "Dune", Year: 2021}
	before := r.clock.now().Add(-time.Hour)
	if err := r.store.Record(r.ctx, w, State{SearchedAt: before, NextAt: before,
		Fruitless: 2, Outcome: OutcomeNothing}); err != nil {
		t.Fatal(err)
	}
	r.client.fail = errors.New("503 from the tracker")

	if _, err := r.svc.RunSearch(r.ctx); err == nil || !strings.Contains(err.Error(), "no indexer answered") {
		t.Fatalf("every search failed, and the pass said %v", err)
	}
	st, _ := r.state(StateKey{Film: true, ID: dune})
	if st.Outcome != OutcomeFailed || st.Fruitless != 2 || !st.NextAt.Equal(r.clock.now().Add(time.Hour)) {
		t.Fatalf("state = %+v", st)
	}
	r.client.reset()
	r.clock.advance(59 * time.Minute)
	if s := r.runSearch(); !strings.Contains(s, "nothing is due") {
		t.Fatalf("retried early: %s", s)
	}
	r.clock.advance(2 * time.Minute)
	_, _ = r.svc.RunSearch(r.ctx)
	if q, _ := r.client.snapshot(); len(q) != 1 {
		t.Fatalf("not retried after the hour: %+v", q)
	}

	// The recent-release pass says so too.
	if _, err := r.svc.RunRecent(r.ctx); err == nil || !strings.Contains(err.Error(), "no indexer answered") {
		t.Fatalf("recent releases with every indexer down: %v", err)
	}
}

// A ceiling on what one pass fetches, whatever the feed holds.
func TestAPassGrabsNoMoreThanItsLimit(t *testing.T) {
	r := newRig(t, Config{MaxGrabsPerRun: 5})
	r.addSeries("Severance", 2022, 95396, 8)
	for e := 1; e <= 8; e++ {
		r.client.feed = append(r.client.feed,
			rel(fmt.Sprintf("Severance.S01E%02d.1080p.WEB-DL.H264-GRP", e), e, 10))
	}
	s := r.runRecent()
	if n := len(r.queue.added()); n != 5 || !strings.Contains(s, "The limit of 5 grab(s) a pass was reached") {
		t.Fatalf("%d grabs: %s", n, s)
	}
	r.clock.advance(15 * time.Minute)
	r.runRecent()
	if n := len(r.queue.added()); n != 8 {
		t.Fatalf("%d grabs after the second pass, want 8", n)
	}
}

// With no default profile nothing can be judged, so nothing is asked or
// fetched, and the task says why.
func TestNoDefaultProfileMeansNothingIsFetched(t *testing.T) {
	r := newRig(t, Config{})
	wantSeverance(r)
	if _, err := r.profiles.SetDefault(r.browse(), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.RunRecent(r.ctx); !errors.Is(err, ErrNoDefaultProfile) {
		t.Fatalf("RunRecent: %v", err)
	}
	if _, err := r.svc.RunSearch(r.ctx); !errors.Is(err, ErrNoDefaultProfile) {
		t.Fatalf("RunSearch: %v", err)
	}
	if q, _ := r.client.snapshot(); len(q) != 0 {
		t.Fatalf("asked %+v", q)
	}
}

// When the tunnel is down, no indexer is asked (ADR-0030, decision 6).
func TestAClosedGateAsksNoIndexer(t *testing.T) {
	r := newRig(t, Config{})
	wantSeverance(r)
	r.client.feed = []indexer.Result{rel(good, 1, 25)}
	r.closeGate("the tunnel is not verified")
	for _, run := range []func() string{r.runRecent, r.runSearch} {
		if s := run(); s != "stopped before asking any indexer: the tunnel is not verified" {
			t.Fatalf("summary = %q", s)
		}
	}
	if q, _ := r.client.snapshot(); len(q) != 0 || len(r.queue.added()) != 0 {
		t.Fatalf("asked %+v, grabbed %v", q, r.targets())
	}
}

// A pass acts on the task's authority and nobody else's.
func TestAPassNeedsTheTasksAuthority(t *testing.T) {
	r := newRig(t, Config{})
	wantSeverance(r)
	r.client.feed = []indexer.Result{rel(good, 1, 25)}
	for name, ctx := range map[string]context.Context{
		"nobody":          context.Background(),
		"the import task": authz.SystemPrincipal(context.Background(), authz.TaskImport),
		"a viewer": authz.WithPrincipal(context.Background(), &authz.Principal{
			UserID: 2, Username: "sam", State: authz.StateActive, MFASatisfied: true,
			Role: authz.Role{ID: 2, Name: "User", Rank: 10,
				Permissions: authz.NewPermissionSet(authz.PermLogin, authz.PermBrowse)}}),
	} {
		if _, err := r.svc.RunRecent(ctx); !authz.IsDenied(err) {
			t.Errorf("%s ran the recent-release pass: %v", name, err)
		}
		if _, err := r.svc.RunSearch(ctx); !authz.IsDenied(err) {
			t.Errorf("%s ran the search pass: %v", name, err)
		}
	}
	if len(r.queue.added()) != 0 {
		t.Fatalf("grabbed %v", r.targets())
	}
}

// A double episode is grabbed once, for its first episode, and holds both;
// one that would fetch an episode not wanted, or one already downloading, is
// refused.
func TestADoubleEpisodeIsGrabbedOnceAndHoldsBoth(t *testing.T) {
	r := newRig(t, Config{})
	sev := r.addSeries("Severance", 2022, 95396, 4)
	r.client.feed = []indexer.Result{
		rel("Severance.S01E01E02.1080p.WEB-DL.H264-GRP", 1, 10),
		rel("Severance.S01E02.1080p.WEB-DL.H264-OTHER", 2, 10),
	}
	s := r.runRecent()
	if got := r.targets(); fmt.Sprint(got) != fmt.Sprintf("[%d S01E01]", sev) {
		t.Fatalf("grabbed %v", got)
	}
	// E02 is held by what was just grabbed, so its own release is not a
	// candidate at all — not one refused because "a download started".
	if strings.Contains(s, "Not grabbed") {
		t.Fatalf("summary = %s", s)
	}
	r.clock.advance(15 * time.Minute)
	r.runRecent()
	if got := r.targets(); len(got) != 1 {
		t.Fatalf("E02 was grabbed separately: %v", got)
	}

	// The other way round: the single E02 first, so the double would fetch
	// an episode already downloading.
	r2 := newRig(t, Config{})
	r2.addSeries("Severance", 2022, 95396, 4)
	r2.client.feed = []indexer.Result{
		rel("Severance.S01E02.1080p.WEB-DL.H264-OTHER", 2, 10),
		rel("Severance.S01E01E02.1080p.WEB-DL.H264-GRP", 1, 10),
	}
	s = r2.runRecent()
	if len(r2.targets()) != 1 || !strings.Contains(s, "it also holds S01E02, which is already downloading") {
		t.Fatalf("grabbed %v: %s", r2.targets(), s)
	}

	// And one whose second episode is on disk already.
	r3 := newRig(t, Config{})
	sev3 := r3.addSeries("Severance", 2022, 95396, 4)
	r3.haveEpisodes(sev3, 1, 2, 2)
	r3.client.feed = []indexer.Result{rel("Severance.S01E01E02.1080p.WEB-DL.H264-GRP", 1, 10)}
	s = r3.runRecent()
	if len(r3.targets()) != 0 || !strings.Contains(s, "it also holds S01E02, which is not wanted") {
		t.Fatalf("grabbed %v: %s", r3.targets(), s)
	}
}

// A person who unmonitors an episode while a search for it is running wins:
// nothing is fetched, and there is nothing to back off from.
func TestAPersonsChangeDuringAPassWins(t *testing.T) {
	r := newRig(t, Config{})
	_, epID := wantSeverance(r)
	r.client.byTerm["severance"] = []indexer.Result{rel(good, 1, 25)}
	r.client.onSearch = func(indexer.Query) {
		if err := r.episodes.SetEpisodeMonitored(r.browse(), epID, false); err != nil {
			t.Error(err)
		}
	}
	r.runSearch()
	if len(r.queue.added()) != 0 {
		t.Fatalf("grabbed %v after it was unmonitored", r.targets())
	}
	st, _ := r.state(StateKey{ID: epID})
	if st.Outcome != OutcomeNothing || st.Fruitless != 0 || !strings.Contains(st.Detail, "no longer wanted") {
		t.Fatalf("state = %+v", st)
	}
	if _, fetched := r.client.snapshot(); len(fetched) != 0 {
		t.Fatalf("fetched %v", fetched)
	}
}

// Other names are what make "The.Office.US" the show in the library. They are
// read once a day, a failure once an hour, and a pass reads only so many.
func TestAlternativeTitlesAreUsedCachedAndBudgeted(t *testing.T) {
	r := newRig(t, Config{})
	office := r.addSeries("The Office", 2005, 2316, 1)
	r.titles.series[2316] = []string{"The Office (US)", "The Office US"}
	r.client.feed = []indexer.Result{rel("The.Office.US.S01E01.720p.HDTV.x264-GRP", 1, 10)}

	r.runRecent()
	if got := r.targets(); fmt.Sprint(got) != fmt.Sprintf("[%d S01E01]", office) || r.titles.count() != 1 {
		t.Fatalf("grabbed %v with %d lookups", got, r.titles.count())
	}

	// Cached: another wanted episode of the same series, no new lookup.
	r2 := newRig(t, Config{})
	r2.addSeries("The Office", 2005, 2316, 3)
	r2.titles.series[2316] = []string{"The Office US"}
	for i := 0; i < 3; i++ {
		r2.runRecent()
		r2.clock.advance(15 * time.Minute)
	}
	if n := r2.titles.count(); n != 1 {
		t.Fatalf("%d lookups in three passes, want 1", n)
	}
	r2.clock.advance(24 * time.Hour)
	r2.runRecent()
	if n := r2.titles.count(); n != 2 {
		t.Fatalf("%d lookups a day later, want 2", n)
	}

	// A failure keeps the names read before, and waits an hour.
	r2.titles.err = errors.New("provider down")
	r2.clock.advance(24 * time.Hour)
	r2.client.feed = []indexer.Result{rel("The.Office.US.S01E02.720p.HDTV.x264-GRP", 2, 10)}
	r2.runRecent()
	if n := r2.titles.count(); n != 3 || len(r2.queue.added()) != 1 {
		t.Fatalf("%d lookups, %d grabs: the names read before should still match", n, len(r2.queue.added()))
	}
	r2.clock.advance(30 * time.Minute)
	r2.runRecent()
	if n := r2.titles.count(); n != 3 {
		t.Fatalf("retried a failed lookup after 30 minutes (%d lookups)", n)
	}
	r2.clock.advance(31 * time.Minute)
	r2.runRecent()
	if n := r2.titles.count(); n != 4 {
		t.Fatalf("did not retry after an hour (%d lookups)", n)
	}

	// The budget: one lookup a pass, two series waiting.
	r3 := newRig(t, Config{})
	r3.svc.lookupBudget = 1
	r3.addSeries("Severance", 2022, 95396, 1)
	r3.addSeries("Andor", 2022, 83867, 1)
	s := r3.runRecent()
	if n := r3.titles.count(); n != 1 || !strings.Contains(s, "1 title(s) were matched by their own name only") {
		t.Fatalf("%d lookups: %s", n, s)
	}
}

// A wanted film is searched for with the film search — its title and year —
// and grabbed for the film. A film with no year is never searched; an
// unmonitored one is not wanted.
func TestAWantedFilmIsSearchedForAndGrabbed(t *testing.T) {
	r := newRig(t, Config{})
	dune := r.addFilm("Dune", 2021, 438631)
	r.titles.films[438631] = []string{"Dune", "Dune: Part One"}
	r.addFilm("Nameless", 0, 0)
	heat := r.addFilm("Heat", 1995, 949)
	if _, err := importer.NewStore(r.database, r.clock.now).SetFilmMonitored(r.browse(), heat, false); err != nil {
		t.Fatal(err)
	}
	r.client.byTerm["dune 2021"] = []indexer.Result{
		rel("Dune.1984.1080p.BluRay.x264-OLD", 1, 50),
		rel("Dune.Part.One.2021.1080p.BluRay.x264-GRP", 2, 40),
	}
	r.runSearch()
	if got := r.targets(); fmt.Sprint(got) != fmt.Sprintf("[film %d]", dune) ||
		r.queue.added()[0].Title != "Dune.Part.One.2021.1080p.BluRay.x264-GRP" {
		t.Fatalf("grabbed %v", r.queue.added())
	}
	q, _ := r.client.snapshot()
	if len(q) != 1 || q[0].Term != "dune 2021" {
		t.Fatalf("queries %+v", q)
	}
	st, _ := r.state(StateKey{Film: true, ID: dune})
	if st.Outcome != OutcomeGrabbed {
		t.Fatalf("state %+v", st)
	}
}

// A fetch that failed is not asked for again every pass: the feed shows the
// same release every fifteen minutes, and an indexer counts every request.
func TestAFailedFetchIsNotRepeatedEveryPass(t *testing.T) {
	r := newRig(t, Config{})
	wantSeverance(r)
	r.client.feed = []indexer.Result{rel(good, 1, 25)}
	r.client.fetchFail = indexer.ErrIndexerRefused
	s := r.runRecent()
	if !strings.Contains(s, "fetching "+good+" failed") {
		t.Fatalf("summary = %s", s)
	}
	var failures int
	if err := r.database.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM audit_event
		WHERE action = 'acquisition.grabbed' AND outcome = 'failure' AND actor_label = 'system:acquire'`).
		Scan(&failures); err != nil || failures != 1 {
		t.Fatalf("%d failure lines (%v), want 1", failures, err)
	}
	for i := 0; i < 3; i++ {
		r.clock.advance(15 * time.Minute)
		r.runRecent()
	}
	if _, fetched := r.client.snapshot(); len(fetched) != 1 {
		t.Fatalf("fetched %d times in four passes, want 1", len(fetched))
	}
	r.clock.advance(6 * time.Hour)
	r.client.fetchFail = nil
	r.runRecent()
	if len(r.queue.added()) != 1 {
		t.Fatal("not tried again after six hours")
	}
}

// The two passes are separate tasks and may start together. They must not
// both grab for the same episode — even when fetching a release is slow
// enough that both would be past their last check before either had queued
// anything.
func TestTheTwoPassesNeverGrabTwiceForOneItem(t *testing.T) {
	for i := 0; i < 3; i++ {
		r := newRig(t, Config{})
		wantSeverance(r)
		r.client.hold = 200 * time.Millisecond
		r.client.feed = []indexer.Result{rel(good, 1, 25)}
		r.client.byTerm["severance"] = []indexer.Result{rel("Severance.S02E03.720p.HDTV.x264-B", 2, 25)}
		var wg sync.WaitGroup
		for _, run := range []func(context.Context) (string, error){r.svc.RunRecent, r.svc.RunSearch} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := run(r.ctx); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if n := len(r.queue.added()); n != 1 {
			t.Fatalf("run %d: %d grabs for one episode", i, n)
		}
	}
}

// What the Wanted screen reads.
func TestTheReportSaysWhatHappened(t *testing.T) {
	r := newRig(t, Config{})
	sev, epID := wantSeverance(r)
	r.client.byTerm["severance"] = []indexer.Result{rel(good, 1, 25)}
	r.runSearch()
	rep, err := r.svc.Report(r.browse())
	if err != nil {
		t.Fatal(err)
	}
	if st := rep.States[StateKey{ID: epID}]; st.Outcome != OutcomeGrabbed {
		t.Fatalf("state %+v", st)
	}
	if !rep.InFlight[Key{ItemID: sev, Season: 2, Episode: 3}] {
		t.Fatalf("in flight %v", rep.InFlight)
	}
	if _, err := r.svc.Report(context.Background()); !authz.IsDenied(err) {
		t.Fatalf("an anonymous report: %v", err)
	}
}

func TestNewRefusesAServiceThatCannotWork(t *testing.T) {
	r := newRig(t, Config{})
	if _, err := New(Deps{Store: r.store}, Config{SearchesPerRun: 1, MaxGrabsPerRun: 1}); err == nil {
		t.Error("built without a finder, a queue or profiles")
	}
	if _, err := New(Deps{Store: r.store, Finder: r.svc.finder, Queue: r.queue, Profiles: r.profiles},
		Config{SearchesPerRun: 0, MaxGrabsPerRun: 1}); err == nil {
		t.Error("built with a budget of no searches")
	}
}

// ADR-0064: a daily series' wanted episode is asked for by its air date, and
// a release dated that day is grabbed for it — from the search and from the
// recent-release feed; a series not daily is asked by number, and a dated
// release of the day is still the episode.
func TestADailyEpisodeIsFetchedByItsAirDate(t *testing.T) {
	for _, daily := range []bool{true, false} {
		r := newRig(t, Config{})
		show := r.addSeries("The Daily Show", 1996, 2224, 1)
		if daily {
			r.exec(`UPDATE media_item SET daily = 1 WHERE id = ?`, show)
		}
		var aired string
		if err := r.database.QueryRowContext(context.Background(),
			`SELECT substr(aired_at, 1, 10) FROM episode WHERE item_id = ?`, show).Scan(&aired); err != nil {
			t.Fatal(err)
		}
		name := "The.Daily.Show." + strings.ReplaceAll(aired, "-", ".") + ".Guest.Name.1080p.WEB.h264-GRP"
		r.client.byTerm["the daily show"] = []indexer.Result{rel(name, 1, 50)}

		summary := r.runSearch()
		if got := r.targets(); len(got) != 1 || got[0] != fmt.Sprintf("%d S01E01", show) {
			t.Fatalf("daily=%v: grabbed %v: %s", daily, got, summary)
		}
		queries, _ := r.client.snapshot()
		want := ""
		if daily {
			want = aired
		}
		if len(queries) == 0 || queries[0].AirDate != want {
			t.Errorf("daily=%v: asked %+v", daily, queries)
		}
	}

	// From the feed: a dated release of a wanted episode's day.
	r := newRig(t, Config{})
	show := r.addSeries("The Daily Show", 1996, 2224, 1)
	var aired string
	if err := r.database.QueryRowContext(context.Background(),
		`SELECT substr(aired_at, 1, 10) FROM episode WHERE item_id = ?`, show).Scan(&aired); err != nil {
		t.Fatal(err)
	}
	r.client.feed = []indexer.Result{rel("The.Daily.Show."+strings.ReplaceAll(aired, "-", ".")+".1080p.WEB.h264-GRP", 2, 50)}
	if _, err := r.svc.RunRecent(r.ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.targets(); len(got) != 1 || got[0] != fmt.Sprintf("%d S01E01", show) {
		t.Errorf("from the feed: grabbed %v", got)
	}
}

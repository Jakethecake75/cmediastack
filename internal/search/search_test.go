package search

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/release"
)

// ---------------------------------------------------------------------------
// Doubles
// ---------------------------------------------------------------------------

type fakeSource struct{ defs []indexer.Definition }

func (f fakeSource) Enabled(context.Context) ([]indexer.Definition, error) {
	return f.defs, nil
}

type failingSource struct{ err error }

func (f failingSource) Enabled(context.Context) ([]indexer.Definition, error) {
	return nil, f.err
}

// fakeSearcher answers per indexer name, so a test can make one tracker fail,
// one be slow, and one work.
type fakeSearcher struct {
	mu       sync.Mutex
	byName   map[string][]indexer.Result
	errs     map[string]error
	delays   map[string]time.Duration
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	calls    atomic.Int32
}

func (f *fakeSearcher) Search(ctx context.Context, d indexer.Definition,
	_ indexer.Query) ([]indexer.Result, error) {

	f.calls.Add(1)
	n := f.inFlight.Add(1)
	for {
		old := f.maxSeen.Load()
		if n <= old || f.maxSeen.CompareAndSwap(old, n) {
			break
		}
	}
	defer f.inFlight.Add(-1)

	f.mu.Lock()
	delay := f.delays[d.Name]
	err := f.errs[d.Name]
	results := f.byName[d.Name]
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return results, nil
}

type recordingHealth struct {
	mu   sync.Mutex
	seen map[int64]error
}

func (r *recordingHealth) RecordResult(_ context.Context, id int64, err error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[int64]error{}
	}
	r.seen[id] = err
	return nil
}

func result(indexerName, title string, seeders int, opts ...func(*indexer.Result)) indexer.Result {
	r := indexer.Result{
		Title:       title,
		DownloadURL: "https://example.com/" + title + ".torrent",
		IndexerName: indexerName,
		Seeders:     seeders,
		Parsed:      release.Parse(title),
	}
	for _, o := range opts {
		o(&r)
	}
	return r
}

func withHash(h string) func(*indexer.Result) {
	return func(r *indexer.Result) { r.InfoHash = h }
}
func withSize(n int64) func(*indexer.Result) {
	return func(r *indexer.Result) { r.Size = n }
}

func defs(names ...string) []indexer.Definition {
	out := make([]indexer.Definition, len(names))
	for i, n := range names {
		out[i] = indexer.Definition{ID: int64(i + 1), Name: n, Enabled: true}
	}
	return out
}

func hdProfile(t *testing.T) *release.Profile {
	t.Helper()
	p := release.Profile{
		Name: "hd", Allowed: []string{
			"HDTV-720p", "WEBDL-720p", "Bluray-720p",
			"HDTV-1080p", "WEBRip-1080p", "WEBDL-1080p", "Bluray-1080p",
		},
		Cutoff:    "Bluray-1080p",
		Forbidden: []string{"/\\b(cam|hdcam|telesync)\\b/"},
	}
	if err := p.Compile(); err != nil {
		t.Fatal(err)
	}
	return &p
}

// ---------------------------------------------------------------------------
// The rule the package is built around
// ---------------------------------------------------------------------------

// One indexer failing must never fail the search — and the caller must be able
// to tell that it happened, or they will read four results as "the release does
// not exist".
func TestOneIndexerFailingDoesNotFailTheSearch(t *testing.T) {
	searcher := &fakeSearcher{
		byName: map[string][]indexer.Result{
			"Good":  {result("Good", "Film.2020.1080p.BluRay.x264-A", 10)},
			"Other": {result("Other", "Film.2020.1080p.WEB-DL.x264-B", 5)},
		},
		errs: map[string]error{"Broken": errors.New("connection refused")},
	}
	svc := New(fakeSource{defs("Good", "Broken", "Other")}, searcher, nil)

	resp, err := svc.Search(context.Background(), Request{Term: "film", Season: -1})
	if err != nil {
		t.Fatalf("the search failed because one indexer did: %v", err)
	}
	if len(resp.Candidates) != 2 {
		t.Errorf("got %d candidates, want 2 from the working indexers", len(resp.Candidates))
	}

	if !resp.Partial() {
		t.Error("the response does not report that it is partial")
	}
	if resp.Failed != 1 || resp.Queried != 3 {
		t.Errorf("queried %d, failed %d; want 3 and 1", resp.Queried, resp.Failed)
	}

	// The reason must be recoverable, not just the count.
	var found bool
	for _, o := range resp.Outcomes {
		if o.IndexerName == "Broken" {
			found = true
			if !strings.Contains(o.Err, "connection refused") {
				t.Errorf("the failure reason was lost: %q", o.Err)
			}
		}
	}
	if !found {
		t.Error("the failing indexer is not in the outcomes at all")
	}
}

func TestEveryIndexerFailingStillReturnsAnAnswer(t *testing.T) {
	searcher := &fakeSearcher{errs: map[string]error{
		"A": errors.New("down"), "B": errors.New("rate limited"),
	}}
	svc := New(fakeSource{defs("A", "B")}, searcher, nil)

	resp, err := svc.Search(context.Background(), Request{Term: "film", Season: -1})
	if err != nil {
		t.Fatalf("err = %v, want a partial response rather than an error", err)
	}
	if len(resp.Candidates) != 0 {
		t.Errorf("got %d candidates from two dead indexers", len(resp.Candidates))
	}
	if !resp.Partial() || resp.Failed != 2 {
		t.Errorf("failed = %d, partial = %v", resp.Failed, resp.Partial())
	}
	if _, ok := resp.Best(); ok {
		t.Error("Best() returned something when nothing was found")
	}
}

// A single slow tracker must not consume the whole budget and starve the
// others, because that looks exactly like "the release does not exist".
func TestASlowIndexerDoesNotStarveTheOthers(t *testing.T) {
	searcher := &fakeSearcher{
		byName: map[string][]indexer.Result{
			"Fast": {result("Fast", "Film.2020.1080p.BluRay.x264-A", 10)},
		},
		delays: map[string]time.Duration{"Slow": 30 * time.Second},
	}
	svc := New(fakeSource{defs("Fast", "Slow")}, searcher, nil)
	svc.timeout = 200 * time.Millisecond // the per-indexer deadline

	start := time.Now()
	resp, err := svc.Search(context.Background(), Request{Term: "film", Season: -1})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("the search took %v; the slow indexer was not bounded", elapsed)
	}
	if len(resp.Candidates) != 1 {
		t.Errorf("got %d candidates, want the fast indexer's one", len(resp.Candidates))
	}
	if !resp.Partial() {
		t.Error("the timed-out indexer is not reported")
	}
}

// Trackers rate-limit. Hitting twenty at once earns a ban on all of them
// simultaneously.
func TestFanOutIsBounded(t *testing.T) {
	var names []string
	for i := 0; i < MaxParallelIndexers*4; i++ {
		names = append(names, string(rune('A'+i)))
	}

	searcher := &fakeSearcher{delays: map[string]time.Duration{}}
	for _, n := range names {
		searcher.delays[n] = 20 * time.Millisecond
	}

	svc := New(fakeSource{defs(names...)}, searcher, nil)
	if _, err := svc.Search(context.Background(), Request{Term: "x", Season: -1}); err != nil {
		t.Fatal(err)
	}

	if got := searcher.maxSeen.Load(); got > MaxParallelIndexers {
		t.Errorf("%d indexers were queried at once, want at most %d", got, MaxParallelIndexers)
	}
	if int(searcher.calls.Load()) != len(names) {
		t.Errorf("%d indexers were queried, want all %d", searcher.calls.Load(), len(names))
	}
}

func TestIndexerHealthIsRecordedBothWays(t *testing.T) {
	health := &recordingHealth{}
	searcher := &fakeSearcher{
		byName: map[string][]indexer.Result{"Good": {result("Good", "Film.2020.1080p.BluRay-A", 1)}},
		errs:   map[string]error{"Bad": errors.New("nope")},
	}
	svc := New(fakeSource{defs("Good", "Bad")}, searcher, health)

	if _, err := svc.Search(context.Background(), Request{Term: "x", Season: -1}); err != nil {
		t.Fatal(err)
	}

	health.mu.Lock()
	defer health.mu.Unlock()
	if err, ok := health.seen[1]; !ok || err != nil {
		t.Errorf("the working indexer recorded %v", err)
	}
	if err, ok := health.seen[2]; !ok || err == nil {
		t.Error("the failing indexer's error was not recorded")
	}
}

// ---------------------------------------------------------------------------
// Deduplication
// ---------------------------------------------------------------------------

func TestTheSameTorrentFromTwoIndexersIsMergedByInfoHash(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	searcher := &fakeSearcher{byName: map[string][]indexer.Result{
		"A": {result("A", "Film.2020.1080p.BluRay.x264-G", 5, withHash(hash))},
		"B": {result("B", "Film.2020.1080p.BluRay.x264-G", 50, withHash(hash))},
	}}
	svc := New(fakeSource{defs("A", "B")}, searcher, nil)

	resp, err := svc.Search(context.Background(), Request{Term: "film", Season: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 1 {
		t.Fatalf("got %d candidates, want 1 after deduplication", len(resp.Candidates))
	}

	c := resp.Candidates[0]
	// The better swarm wins: same content, more seeders.
	if c.Seeders != 50 {
		t.Errorf("seeders = %d, want the healthier copy's 50", c.Seeders)
	}
	if len(c.SeenOn) != 2 {
		t.Errorf("seen on %v, want both indexers", c.SeenOn)
	}
}

// Two different encodes legitimately share a title. Collapsing on title alone
// would silently discard the better one.
func TestDifferentReleasesWithTheSameTitleAreNotMerged(t *testing.T) {
	searcher := &fakeSearcher{byName: map[string][]indexer.Result{
		"A": {
			result("A", "Film.2020.1080p.BluRay.x264-G", 5, withSize(8_000_000_000)),
			result("A", "Film.2020.1080p.BluRay.x264-G", 5, withSize(2_000_000_000)),
		},
	}}
	svc := New(fakeSource{defs("A")}, searcher, nil)

	resp, err := svc.Search(context.Background(), Request{Term: "film", Season: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 2 {
		t.Errorf("got %d candidates, want both encodes kept", len(resp.Candidates))
	}
}

// ---------------------------------------------------------------------------
// Judgement
// ---------------------------------------------------------------------------

// "Why did nothing get grabbed" is the question this software is worst at
// answering. The answer belongs in the response.
func TestRejectedCandidatesComeBackWithTheirReason(t *testing.T) {
	searcher := &fakeSearcher{byName: map[string][]indexer.Result{
		"A": {
			result("A", "Film.2020.CAM.x264-G", 100),
			result("A", "Film.2020.2160p.BluRay.REMUX.HDR-G", 20),
			result("A", "Film.2020.1080p.WEB-DL.x264-G", 10),
		},
	}}
	svc := New(fakeSource{defs("A")}, searcher, nil)

	resp, err := svc.Search(context.Background(), Request{
		Term: "film", Season: -1, Profile: hdProfile(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) != 3 {
		t.Fatalf("got %d candidates, want all three returned", len(resp.Candidates))
	}
	if len(resp.Accepted()) != 1 {
		t.Fatalf("accepted %d, want only the 1080p WEB-DL", len(resp.Accepted()))
	}

	for _, c := range resp.Candidates {
		if c.Accepted {
			continue
		}
		if c.Rejection == nil || c.Rejection.Detail == "" {
			t.Errorf("%s was rejected with no reason", c.Title)
		}
	}

	// The cam rip has the most seeders, so an implementation that sorted on
	// seeders would put it first. It must be last: rejected candidates sort
	// below everything acceptable.
	if resp.Candidates[0].Accepted != true {
		t.Error("a rejected candidate sorted above an accepted one")
	}
	best, ok := resp.Best()
	if !ok {
		t.Fatal("Best() found nothing")
	}
	if !strings.Contains(best.Title, "WEB-DL") {
		t.Errorf("best = %q", best.Title)
	}
}

// Seeders is a liveness signal, not a quality one. Sorting by it first is how a
// library fills up with whatever happens to be popular.
func TestSeedersBreakTiesRatherThanDrivingTheOrder(t *testing.T) {
	searcher := &fakeSearcher{byName: map[string][]indexer.Result{
		"A": {
			result("A", "Film.2020.720p.HDTV.x264-POPULAR", 5000),
			result("A", "Film.2020.1080p.BluRay.x264-QUIET", 3),
		},
	}}
	svc := New(fakeSource{defs("A")}, searcher, nil)

	resp, err := svc.Search(context.Background(), Request{
		Term: "film", Season: -1, Profile: hdProfile(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	best, _ := resp.Best()
	if !strings.Contains(best.Title, "QUIET") {
		t.Errorf("best = %q; the popular low-quality release won", best.Title)
	}
}

func TestEqualQualityIsBrokenBySeeders(t *testing.T) {
	searcher := &fakeSearcher{byName: map[string][]indexer.Result{
		"A": {
			result("A", "Film.2020.1080p.BluRay.x264-SLOW", 2, withSize(1)),
			result("A", "Film.2020.1080p.BluRay.x264-FAST", 900, withSize(2)),
		},
	}}
	svc := New(fakeSource{defs("A")}, searcher, nil)

	resp, err := svc.Search(context.Background(), Request{
		Term: "film", Season: -1, Profile: hdProfile(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	best, _ := resp.Best()
	if !strings.Contains(best.Title, "FAST") {
		t.Errorf("best = %q, want the better-seeded copy of equal quality", best.Title)
	}
}

// A PROPER of the same quality outranks the original.
func TestAProperOutranksTheOriginal(t *testing.T) {
	searcher := &fakeSearcher{byName: map[string][]indexer.Result{
		"A": {
			result("A", "Film.2020.1080p.WEB-DL.x264-ORIG", 500, withSize(1)),
			result("A", "Film.2020.PROPER.1080p.WEB-DL.x264-FIX", 5, withSize(2)),
		},
	}}
	svc := New(fakeSource{defs("A")}, searcher, nil)

	resp, err := svc.Search(context.Background(), Request{
		Term: "film", Season: -1, Profile: hdProfile(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	best, _ := resp.Best()
	if !strings.Contains(best.Title, "FIX") {
		t.Errorf("best = %q, want the PROPER", best.Title)
	}
}

// Without a profile the operator is the judge, so nothing is refused on their
// behalf — this is the interactive-search case.
func TestNoProfileRefusesNothing(t *testing.T) {
	searcher := &fakeSearcher{byName: map[string][]indexer.Result{
		"A": {
			result("A", "Film.2020.CAM.x264-G", 100),
			result("A", "Film.2020.1080p.BluRay.x264-G", 10),
		},
	}}
	svc := New(fakeSource{defs("A")}, searcher, nil)

	resp, err := svc.Search(context.Background(), Request{Term: "film", Season: -1})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Accepted()) != 2 {
		t.Errorf("accepted %d of 2 with no profile", len(resp.Accepted()))
	}
}

// ---------------------------------------------------------------------------
// Plumbing
// ---------------------------------------------------------------------------

func TestNoEnabledIndexersIsADistinctError(t *testing.T) {
	svc := New(fakeSource{nil}, &fakeSearcher{}, nil)
	if _, err := svc.Search(context.Background(), Request{Term: "x", Season: -1}); !errors.Is(err, ErrNoIndexers) {
		t.Errorf("got %v, want ErrNoIndexers", err)
	}
}

func TestALoadFailureIsAnErrorNotAnEmptyResult(t *testing.T) {
	svc := New(failingSource{errors.New("database is gone")}, &fakeSearcher{}, nil)
	_, err := svc.Search(context.Background(), Request{Term: "x", Season: -1})
	if err == nil {
		t.Fatal("a database failure produced an empty result rather than an error")
	}
	if errors.Is(err, ErrNoIndexers) {
		t.Error("a database failure was reported as 'no indexers', which an operator would act on wrongly")
	}
}

func TestSearchCanBeRestrictedToChosenIndexers(t *testing.T) {
	searcher := &fakeSearcher{byName: map[string][]indexer.Result{
		"A": {result("A", "Film.2020.1080p.BluRay-A", 1)},
		"B": {result("B", "Film.2020.1080p.BluRay-B", 1)},
		"C": {result("C", "Film.2020.1080p.BluRay-C", 1)},
	}}
	svc := New(fakeSource{defs("A", "B", "C")}, searcher, nil)

	resp, err := svc.Search(context.Background(), Request{
		Term: "film", Season: -1, IndexerIDs: []int64{2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Queried != 1 {
		t.Errorf("queried %d indexers, want 1", resp.Queried)
	}
	if len(resp.Candidates) != 1 || resp.Candidates[0].IndexerName != "B" {
		t.Errorf("candidates = %+v", resp.Candidates)
	}
}

func TestCancellationIsHonoured(t *testing.T) {
	searcher := &fakeSearcher{delays: map[string]time.Duration{
		"A": 10 * time.Second, "B": 10 * time.Second,
	}}
	svc := New(fakeSource{defs("A", "B")}, searcher, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	resp, err := svc.Search(ctx, Request{Term: "x", Season: -1})
	if err != nil {
		t.Fatalf("cancellation produced an error rather than a partial response: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("cancellation was not honoured promptly")
	}
	if !resp.Partial() {
		t.Error("a cancelled search did not report itself as partial")
	}
}

// Two searches overlap in normal operation: the RSS sync runs on a timer while
// an operator is using the UI. The first version of dedupe passed provenance
// through a package-level map, which is a data race under exactly that.
func TestConcurrentSearchesDoNotInterfere(t *testing.T) {
	searcher := &fakeSearcher{byName: map[string][]indexer.Result{
		"A": {
			result("A", "Film.2020.1080p.BluRay.x264-G", 10, withHash("aa")),
			result("A", "Other.2021.1080p.WEB-DL.x264-H", 20, withHash("bb")),
		},
		"B": {result("B", "Film.2020.1080p.BluRay.x264-G", 30, withHash("aa"))},
	}}
	svc := New(fakeSource{defs("A", "B")}, searcher, nil)

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := svc.Search(context.Background(), Request{
				Term: "film", Season: -1, Profile: hdProfile(t),
			})
			if err != nil {
				t.Errorf("search: %v", err)
				return
			}
			if len(resp.Candidates) != 2 {
				t.Errorf("got %d candidates, want 2", len(resp.Candidates))
			}
			for _, c := range resp.Candidates {
				if len(c.SeenOn) == 0 {
					t.Error("a candidate lost its provenance")
				}
			}
		}()
	}
	wg.Wait()
}

// A television request must reach the indexer as a television query.
func TestTelevisionRequestsCarryTheSeasonAndEpisode(t *testing.T) {
	var seen indexer.Query
	var mu sync.Mutex
	svc := New(fakeSource{defs("A")}, searcherFunc(func(_ context.Context,
		_ indexer.Definition, q indexer.Query) ([]indexer.Result, error) {
		mu.Lock()
		seen = q
		mu.Unlock()
		return nil, nil
	}), nil)

	if _, err := svc.Search(context.Background(), Request{
		Term: "breaking bad", Season: 5, Episode: 14,
	}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if seen.Season != 5 || seen.Episode != 14 {
		t.Errorf("query = %+v", seen)
	}
}

type searcherFunc func(context.Context, indexer.Definition, indexer.Query) ([]indexer.Result, error)

func (f searcherFunc) Search(ctx context.Context, d indexer.Definition,
	q indexer.Query) ([]indexer.Result, error) {
	return f(ctx, d, q)
}

// ---------------------------------------------------------------------------
// The downloader discovery
// ---------------------------------------------------------------------------

// The grab half is discovered with a runtime type assertion —
// `client.(Downloader)` in New — rather than taken as a parameter, so that a
// Searcher-only fake need not grow a Download method it will never call.
//
// That convenience has a failure mode, and it is a quiet one. If
// indexer.Client's Download signature ever drifts from the Downloader
// interface, the assertion simply stops matching: New leaves the field nil, the
// service returns ErrGrabUnavailable for every grab, nothing fails to compile,
// and every test in this package still passes because they all use fakes that
// DO satisfy it. The breakage is only visible in production, one grab at a
// time.
//
// This is the test the comment on Service.downloader has been citing. It did
// not exist until the citation checker went looking for it.
func TestTheRealClientCanGrab(t *testing.T) {
	// Compile-time: the production type satisfies the interface.
	var _ Downloader = (*indexer.Client)(nil)

	// Run-time: and New actually finds it, which is the half a compile-time
	// assertion does not cover — New could stop asking.
	svc := New(&fakeSource{}, indexer.NewClient(nil), nil)
	if svc.downloader == nil {
		t.Fatal("New did not discover the real client as a Downloader: " +
			"every grab would return ErrGrabUnavailable")
	}
}

// And the other half of that design: a service wired with a Searcher that is
// NOT a Downloader must refuse by name rather than appear to work.
func TestAServiceWithNoDownloaderRefusesToGrab(t *testing.T) {
	svc := New(&fakeSource{}, &fakeSearcher{}, nil)
	if svc.downloader != nil {
		t.Fatal("a Searcher-only fake was accepted as a Downloader")
	}
	// Refused before anything is resolved, so an empty ticket is enough: the
	// nil check is the first thing Grab does, deliberately.
	_, err := svc.Grab(context.Background(), Ticket{})
	if !errors.Is(err, ErrGrabUnavailable) {
		t.Errorf("grab without a downloader returned %v, want ErrGrabUnavailable", err)
	}
}

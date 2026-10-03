package tv

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/metadata"
)

// fakeEpisodeProvider answers from a table and records what it was asked.
type fakeEpisodeProvider struct {
	seasons []metadata.Season
	// episodes is keyed by season number.
	episodes map[int][]metadata.Episode
	// asked records every season fetched, which is how "it skipped the seasons
	// that cannot have changed" is checked rather than assumed.
	asked      []int
	detailsErr error
	episodeErr map[int]error
	// detailsCalls counts series asked about, which is how "it stopped at the
	// rate limit" is checked across series rather than assumed.
	detailsCalls int
}

func (f *fakeEpisodeProvider) Details(context.Context, metadata.Kind, int64) (metadata.Details, error) {
	f.detailsCalls++
	if f.detailsErr != nil {
		return metadata.Details{}, f.detailsErr
	}
	return metadata.Details{Seasons: f.seasons}, nil
}

func (f *fakeEpisodeProvider) Episodes(_ context.Context, _ int64, season int) ([]metadata.Episode, error) {
	f.asked = append(f.asked, season)
	if err, ok := f.episodeErr[season]; ok {
		return nil, err
	}
	return f.episodes[season], nil
}

// fakeSeries hands over one item.
type fakeSeries struct{ ref SeriesRef }

func (f fakeSeries) SeriesForRefresh(context.Context, int64) (SeriesRef, error) {
	return f.ref, nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newRefresher(r *epRig, p *fakeEpisodeProvider, kind string) *Refresher {
	return NewRefresher(r.store,
		fakeSeries{ref: SeriesRef{ID: r.itemID, Title: "Severance", Kind: kind, TMDBID: 95396}},
		func() EpisodeProvider { return p }, quietLogger(), clock())
}

func providerSeason(number, count, airedDays int) metadata.Season {
	return metadata.Season{Number: number, Name: "Season", Episodes: count, Aired: aired(airedDays)}
}

func providerEpisodes(count, firstAiredDays int) []metadata.Episode {
	out := make([]metadata.Episode, 0, count)
	for i := 1; i <= count; i++ {
		out = append(out, metadata.Episode{
			ProviderID: int64(9000 + i), Number: i,
			Title: "Ep", Aired: aired(firstAiredDays + i), Runtime: 45,
		})
	}
	return out
}

// A first refresh fetches everything and records it.
func TestAFirstRefreshFetchesEverySeason(t *testing.T) {
	r := newEpRig(t)
	p := &fakeEpisodeProvider{
		seasons: []metadata.Season{providerSeason(1, 3, -400), providerSeason(2, 2, -300)},
		episodes: map[int][]metadata.Episode{
			1: providerEpisodes(3, -400),
			2: providerEpisodes(2, -300),
		},
	}
	res, err := newRefresher(r, p, "series").Refresh(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Seasons != 2 || res.Episodes != 5 {
		t.Errorf("result = %+v, want 2 seasons and 5 episodes", res)
	}
	if len(p.asked) != 2 {
		t.Errorf("fetched %v, want both seasons on a first refresh", p.asked)
	}

	_, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byNumber[1]) != 3 || len(byNumber[2]) != 2 {
		t.Errorf("stored %d and %d episodes", len(byNumber[1]), len(byNumber[2]))
	}
}

// A season that cannot have changed costs no request.
//
// TMDB is one call per season. A twelve-season show refreshed nightly is twelve
// calls a night forever, for an answer that stopped moving years ago.
func TestAFinishedSeasonIsNotFetchedAgain(t *testing.T) {
	r := newEpRig(t)
	p := &fakeEpisodeProvider{
		seasons:  []metadata.Season{providerSeason(1, 3, -400)},
		episodes: map[int][]metadata.Episode{1: providerEpisodes(3, -400)},
	}
	ref := newRefresher(r, p, "series")

	if _, err := ref.Refresh(r.ctx, r.itemID); err != nil {
		t.Fatal(err)
	}
	p.asked = nil

	res, err := ref.Refresh(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.asked) != 0 {
		t.Errorf("fetched %v on a second refresh; nothing about that season can "+
			"have changed", p.asked)
	}
	if res.SeasonsSkipped != 1 {
		t.Errorf("SeasonsSkipped = %d, want 1 — the log must say what was NOT "+
			"done rather than implying it was", res.SeasonsSkipped)
	}
	// And the episodes are still there, not wiped by the skip.
	_, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byNumber[1]) != 3 {
		t.Errorf("%d episodes after a skipped refresh", len(byNumber[1]))
	}
}

// A season that gained an episode IS fetched again.
//
// Without this the test above passes just as well against a refresher that
// never fetches anything after the first run.
func TestASeasonThatGainedAnEpisodeIsFetchedAgain(t *testing.T) {
	r := newEpRig(t)
	p := &fakeEpisodeProvider{
		seasons:  []metadata.Season{providerSeason(1, 3, -400)},
		episodes: map[int][]metadata.Episode{1: providerEpisodes(3, -400)},
	}
	ref := newRefresher(r, p, "series")
	if _, err := ref.Refresh(r.ctx, r.itemID); err != nil {
		t.Fatal(err)
	}

	// The provider now says four.
	p.asked = nil
	p.seasons = []metadata.Season{providerSeason(1, 4, -400)}
	p.episodes[1] = providerEpisodes(4, -400)

	if _, err := ref.Refresh(r.ctx, r.itemID); err != nil {
		t.Fatal(err)
	}
	if len(p.asked) != 1 {
		t.Fatalf("fetched %v, want season 1 refetched after its count changed", p.asked)
	}
	_, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byNumber[1]) != 4 {
		t.Errorf("%d episodes, want the new one picked up", len(byNumber[1]))
	}
}

// A season with an episode still to air is fetched every time.
func TestARunningSeasonIsAlwaysFetched(t *testing.T) {
	r := newEpRig(t)
	eps := providerEpisodes(3, -10)
	eps[2].Aired = aired(5) // the last one has not aired
	p := &fakeEpisodeProvider{
		seasons:  []metadata.Season{providerSeason(1, 3, -10)},
		episodes: map[int][]metadata.Episode{1: eps},
	}
	ref := newRefresher(r, p, "series")
	if _, err := ref.Refresh(r.ctx, r.itemID); err != nil {
		t.Fatal(err)
	}
	p.asked = nil
	if _, err := ref.Refresh(r.ctx, r.itemID); err != nil {
		t.Fatal(err)
	}
	if len(p.asked) != 1 {
		t.Errorf("fetched %v; a season with an episode still to air can change", p.asked)
	}
}

// One season failing does not abandon the others, and does not wipe the one
// that failed.
func TestOneSeasonFailingDoesNotLoseTheRest(t *testing.T) {
	r := newEpRig(t)
	p := &fakeEpisodeProvider{
		seasons: []metadata.Season{providerSeason(1, 3, -400), providerSeason(2, 2, -300)},
		episodes: map[int][]metadata.Episode{
			1: providerEpisodes(3, -400),
			2: providerEpisodes(2, -300),
		},
	}
	ref := newRefresher(r, p, "series")
	if _, err := ref.Refresh(r.ctx, r.itemID); err != nil {
		t.Fatal(err)
	}

	// Season 1 now fails, and season 2 gained an episode.
	p.episodeErr = map[int]error{1: errors.New("502 from the provider")}
	p.seasons = []metadata.Season{providerSeason(1, 9, -400), providerSeason(2, 3, -300)}
	p.episodes[2] = providerEpisodes(3, -300)

	res, err := ref.Refresh(r.ctx, r.itemID)
	if err != nil {
		t.Fatalf("one season failing aborted the refresh: %v", err)
	}
	// One read and one FAILED — not "skipped", which means could not have
	// changed. This line used to assert one skipped, which is how the
	// conflation it now refuses got past review.
	if res.SeasonsRead != 1 || res.SeasonsFailed != 1 || res.SeasonsSkipped != 0 {
		t.Errorf("result = %+v, want one read and one failed", res)
	}

	_, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byNumber[1]) != 3 {
		t.Errorf("season 1 has %d episodes; a season whose fetch FAILED must be "+
			"left as it was, not emptied", len(byNumber[1]))
	}
	if len(byNumber[2]) != 3 {
		t.Errorf("season 2 has %d episodes, want the refreshed 3", len(byNumber[2]))
	}
}

// A season whose fetch failed is asked about again on the next refresh.
//
// The first version recorded the provider's new episode count for a season it
// had failed to read, so the next refresh saw the counts agree and never asked
// again: the episode the season had gained during a 502 was lost for good. Its
// comment promised the opposite.
func TestASeasonThatFailedIsAskedAboutAgain(t *testing.T) {
	r := newEpRig(t)
	p := &fakeEpisodeProvider{
		seasons:  []metadata.Season{providerSeason(1, 3, -400)},
		episodes: map[int][]metadata.Episode{1: providerEpisodes(3, -400)},
	}
	ref := newRefresher(r, p, "series")
	if _, err := ref.Refresh(r.ctx, r.itemID); err != nil {
		t.Fatal(err)
	}

	// The season gains an episode, and the first attempt to read it fails.
	p.seasons = []metadata.Season{providerSeason(1, 4, -400)}
	p.episodes[1] = providerEpisodes(4, -400)
	p.episodeErr = map[int]error{1: errors.New("502 from the provider")}
	if _, err := ref.Refresh(r.ctx, r.itemID); err != nil {
		t.Fatal(err)
	}

	// The provider recovers.
	p.episodeErr = nil
	p.asked = nil
	if _, err := ref.Refresh(r.ctx, r.itemID); err != nil {
		t.Fatal(err)
	}
	if len(p.asked) != 1 {
		t.Fatalf("fetched %v after a failure; the season that failed must be asked "+
			"about again", p.asked)
	}
	_, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byNumber[1]) != 4 {
		t.Errorf("season 1 has %d episodes, want the 4 the provider lists", len(byNumber[1]))
	}
}

// A failed season is never described as one that could not have changed.
func TestTheSummaryDoesNotCallAFailureUnchanged(t *testing.T) {
	got := RefreshResult{Title: "Severance", Seasons: 3, SeasonsRead: 1,
		SeasonsSkipped: 1, SeasonsFailed: 1, Episodes: 10}.Summary()
	if !strings.Contains(got, "skipped 1 that could not have changed") ||
		!strings.Contains(got, "1 could NOT be read") {
		t.Errorf("summary = %q; a failed season must be reported as failed, "+
			"separately from the ones skipped because they could not have changed", got)
	}
	if clean := (RefreshResult{Title: "X", Seasons: 1, SeasonsRead: 1}).Summary(); strings.Contains(clean, "NOT") {
		t.Errorf("a refresh with no failures reports one: %q", clean)
	}
}

// A rate limit ends the refresh: nothing more is asked, what was read is
// recorded, and the caller is told it stopped part-way.
//
// The first version logged the refusal and asked about the next season, and
// the next — each one into the same limit — then reported success.
func TestARateLimitStopsTheRefreshAndKeepsWhatWasRead(t *testing.T) {
	r := newEpRig(t)
	p := &fakeEpisodeProvider{
		seasons: []metadata.Season{
			providerSeason(1, 2, -400), providerSeason(2, 2, -300),
			providerSeason(3, 2, -200), providerSeason(4, 2, -100),
		},
		episodes: map[int][]metadata.Episode{
			1: providerEpisodes(2, -400), 2: providerEpisodes(2, -300),
			3: providerEpisodes(2, -200), 4: providerEpisodes(2, -100),
		},
		episodeErr: map[int]error{2: metadata.ErrRateLimited},
	}
	res, err := newRefresher(r, p, "series").Refresh(r.ctx, r.itemID)
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Fatalf("err = %v; a refresh the provider stopped must say so", err)
	}
	if len(p.asked) != 2 {
		t.Errorf("asked about seasons %v; after the provider said stop, nothing "+
			"more may be asked", p.asked)
	}
	if res.SeasonsRead != 1 || res.SeasonsFailed != 3 {
		t.Errorf("result = %+v, want one read and three unread", res)
	}

	seasons, byNumber, err := r.store.Seasons(r.ctx, r.itemID)
	if err != nil {
		t.Fatal(err)
	}
	if len(byNumber[1]) != 2 {
		t.Errorf("season 1 was read before the limit and has %d episodes recorded, want 2",
			len(byNumber[1]))
	}
	if len(seasons) != 4 {
		t.Errorf("%d seasons recorded; the unread ones must still be listed so "+
			"the next refresh asks about them", len(seasons))
	}
}

// RefreshAll stops at a rate limit met part-way through a series, rather than
// going on to the next series and meeting it again there.
func TestRefreshAllStopsAtALimitMetPartWayThroughASeries(t *testing.T) {
	r := newEpRig(t)
	if _, err := r.raw.Exec(
		`INSERT INTO media_item (kind, title, sort_title, root_folder_id, folder,
		                         tmdb_id, added_at, updated_at)
		 VALUES ('series','Andor','andor',1,'Andor',83867,?,?)`,
		testNow.Format(time.RFC3339Nano), testNow.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	p := &fakeEpisodeProvider{
		seasons:    []metadata.Season{providerSeason(1, 2, -400), providerSeason(2, 2, -300)},
		episodes:   map[int][]metadata.Episode{1: providerEpisodes(2, -400), 2: providerEpisodes(2, -300)},
		episodeErr: map[int]error{1: metadata.ErrRateLimited},
	}
	results, err := newRefresher(r, p, "series").RefreshAll(r.ctx, 50)
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Fatalf("err = %v, want the rate limit to stop the pass", err)
	}
	if p.detailsCalls != 1 {
		t.Errorf("the provider was asked about %d series; after it said stop, "+
			"no further series may be asked about", p.detailsCalls)
	}
	if len(results) != 1 {
		t.Errorf("%d results; the series stopped part-way was partly recorded and "+
			"the task log should say so", len(results))
	}
}

// A film has no episode list, and asking a series endpoint about a film's id
// returns somebody else's series.
func TestRefreshingAFilmIsRefused(t *testing.T) {
	r := newEpRig(t)
	p := &fakeEpisodeProvider{}
	_, err := newRefresher(r, p, "movie").Refresh(r.ctx, r.itemID)
	if !errors.Is(err, library.ErrNotASeries) {
		t.Errorf("err = %v, want ErrNotASeries", err)
	}
	if len(p.asked) != 0 {
		t.Error("a film's id was sent to a series endpoint")
	}
}

// An unidentified series has no id to ask about, and the refusal says so.
func TestRefreshingAnUnidentifiedSeriesSaysWhatIsMissing(t *testing.T) {
	r := newEpRig(t)
	p := &fakeEpisodeProvider{}
	ref := NewRefresher(r.store,
		fakeSeries{ref: SeriesRef{ID: r.itemID, Title: "Unknown", Kind: "series"}},
		func() EpisodeProvider { return p }, quietLogger(), clock())

	_, err := ref.Refresh(r.ctx, r.itemID)
	if !errors.Is(err, ErrNotIdentified) {
		t.Fatalf("err = %v, want ErrNotIdentified", err)
	}
	if !strings.Contains(err.Error(), "identified") {
		t.Errorf("the refusal does not say what to do first: %v", err)
	}
}

// With no provider configured the refresh says so rather than failing obscurely.
func TestRefreshingWithNoProviderSaysSo(t *testing.T) {
	r := newEpRig(t)
	ref := NewRefresher(r.store,
		fakeSeries{ref: SeriesRef{ID: r.itemID, Kind: "series", TMDBID: 1}},
		func() EpisodeProvider { return nil }, quietLogger(), clock())

	if _, err := ref.Refresh(r.ctx, r.itemID); !errors.Is(err, metadata.ErrNoProvider) {
		t.Errorf("err = %v, want ErrNoProvider", err)
	}
}

// RefreshAll stops on a rate limit rather than hammering.
func TestRefreshAllStopsWhenTheProviderAsksItTo(t *testing.T) {
	r := newEpRig(t)
	p := &fakeEpisodeProvider{detailsErr: metadata.ErrRateLimited}
	ref := newRefresher(r, p, "series")

	_, err := ref.RefreshAll(r.ctx, 50)
	if !errors.Is(err, metadata.ErrRateLimited) {
		t.Errorf("err = %v, want the rate limit to stop the pass", err)
	}
}

// A season row with no episodes is refetched: that is a refresh that failed
// part-way, and leaving it is how a series stays permanently half-recorded.
func TestASeasonRowWithNoEpisodesIsFetchedAgain(t *testing.T) {
	r := newEpRig(t)
	// A season row with a matching count and no episodes at all.
	if err := r.store.Upsert(r.ctx, r.itemID, []library.SeasonInput{
		{Number: 1, Name: "Season 1", EpisodeCount: 3, Aired: aired(-400)},
	}); err != nil {
		t.Fatal(err)
	}
	p := &fakeEpisodeProvider{
		seasons:  []metadata.Season{providerSeason(1, 3, -400)},
		episodes: map[int][]metadata.Episode{1: providerEpisodes(3, -400)},
	}
	if _, err := newRefresher(r, p, "series").Refresh(r.ctx, r.itemID); err != nil {
		t.Fatal(err)
	}
	if len(p.asked) != 1 {
		t.Errorf("fetched %v; a season whose episodes are missing must be asked "+
			"about again or it stays half-recorded forever", p.asked)
	}
}

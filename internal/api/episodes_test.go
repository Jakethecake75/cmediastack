package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/tv"
)

// fakeEpisodes answers from fixed data, so these tests are about the JSON
// contract and nothing else. The behaviour behind it is tested against a real
// database in internal/library.
type fakeEpisodes struct {
	seasons  []library.Season
	byNumber map[int][]library.Episode
	wanted   []library.WantedEpisode
	subject  *library.SearchSubject
	season   *library.SeasonSearchSubject
}

func (f *fakeEpisodes) Seasons(context.Context, int64) ([]library.Season, map[int][]library.Episode, error) {
	return f.seasons, f.byNumber, nil
}
func (f *fakeEpisodes) Wanted(context.Context, int) ([]library.WantedEpisode, error) {
	return f.wanted, nil
}
func (f *fakeEpisodes) SetSeasonMonitored(context.Context, int64, int64, bool) error { return nil }
func (f *fakeEpisodes) SetEpisodeMonitored(context.Context, int64, bool) error       { return nil }
func (f *fakeEpisodes) SetSeriesFlag(context.Context, int64, library.SeriesFlag, bool) error {
	return nil
}
func (f *fakeEpisodes) SeriesSettings(context.Context, int64) (library.SeriesSettings, error) {
	return library.SeriesSettings{}, library.ErrNotFound
}
func (f *fakeEpisodes) ForSearch(_ context.Context, id int64) (library.SearchSubject, error) {
	if f.subject == nil || f.subject.Episode.ID != id {
		return library.SearchSubject{}, library.ErrNoSuchEpisode
	}
	return *f.subject, nil
}

func (f *fakeEpisodes) ForSeasonSearch(_ context.Context, itemID int64, season int) (library.SeasonSearchSubject, error) {
	if f.season == nil || f.season.ItemID != itemID || f.season.Season != season {
		return library.SeasonSearchSubject{}, library.ErrNoSuchSeason
	}
	return *f.season, nil
}

func childrenOf(t *testing.T, f *fakeEpisodes) map[string]any {
	t.Helper()
	h := &Handlers{episodes: f}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/media/7/children", nil)
	req.SetPathValue("id", "7")
	w := httptest.NewRecorder()
	h.SeriesChildren(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// An announced episode is said to be announced, not given a null date.
//
// "Announced" and "has not aired yet" are different facts, and the difference is
// why the wanted list is not full of every unscheduled episode of every running
// show. A client handed a bare null would have to guess which one it meant.
func TestAnAnnouncedEpisodeIsSaidToBeAnnounced(t *testing.T) {
	aired := time.Date(2025, 1, 16, 0, 0, 0, 0, time.UTC)
	body := childrenOf(t, &fakeEpisodes{
		seasons: []library.Season{{Number: 3, EpisodeCount: 2, Monitored: true}},
		byNumber: map[int][]library.Episode{3: {
			{ID: 1, SeasonNumber: 3, Number: 1, Title: "Aired", Aired: aired, Monitored: true},
			{ID: 2, SeasonNumber: 3, Number: 2, Title: "Announced", Monitored: true},
		}},
	})

	eps := body["seasons"].([]any)[0].(map[string]any)["episodes"].([]any)
	first, second := eps[0].(map[string]any), eps[1].(map[string]any)

	if _, ok := first["aired_at"]; !ok || first["announced"] != nil {
		t.Errorf("an aired episode should carry aired_at and no announced flag: %v", first)
	}
	if second["announced"] != true {
		t.Errorf("an episode with no date must be marked announced: %v", second)
	}
	if _, ok := second["aired_at"]; ok {
		t.Errorf("an announced episode was given an aired_at: %v", second)
	}
}

// Both counts travel, because they answer different questions.
//
// "known" is how many episode rows this instance holds; "episode_count" is what
// the provider says exists. A season whose refresh failed part-way has fewer
// rows than the count, and reporting only one of them would give a completion
// figure silently measured against the wrong denominator.
func TestASeasonReportsWhatItKnowsAndWhatTheProviderSays(t *testing.T) {
	aired := time.Date(2022, 2, 17, 0, 0, 0, 0, time.UTC)
	body := childrenOf(t, &fakeEpisodes{
		seasons: []library.Season{{Number: 1, EpisodeCount: 9, Monitored: true}},
		byNumber: map[int][]library.Episode{1: {
			{ID: 1, SeasonNumber: 1, Number: 1, Aired: aired, HaveFileID: 40, HavePath: "S01E01.mkv"},
			{ID: 2, SeasonNumber: 1, Number: 2, Aired: aired},
		}},
	})
	s := body["seasons"].([]any)[0].(map[string]any)
	if s["have"] != float64(1) || s["known"] != float64(2) || s["episode_count"] != float64(9) {
		t.Errorf("season = have %v, known %v, episode_count %v; want 1, 2 and 9",
			s["have"], s["known"], s["episode_count"])
	}
	ep := s["episodes"].([]any)[0].(map[string]any)
	if ep["have"] != true || ep["file_id"] != float64(40) {
		t.Errorf("a held episode should say so and name its file: %v", ep)
	}
}

// An empty answer explains itself, because the three reasons for it need three
// different fixes.
func TestASeriesWithNoEpisodesSaysWhy(t *testing.T) {
	body := childrenOf(t, &fakeEpisodes{})
	note, _ := body["note"].(string)
	if !strings.Contains(note, "Identify") || !strings.Contains(note, "refresh") {
		t.Errorf("an empty series gave no guidance: %v", body)
	}
}

// The wanted list says which series each episode belongs to.
func TestWantedEpisodesNameTheirSeries(t *testing.T) {
	aired := time.Date(2025, 3, 20, 0, 0, 0, 0, time.UTC)
	h := &Handlers{episodes: &fakeEpisodes{wanted: []library.WantedEpisode{{
		Episode:     library.Episode{ID: 5, ItemID: 7, SeasonNumber: 2, Number: 10, Title: "Cold Harbor", Aired: aired, Monitored: true},
		SeriesTitle: "Severance",
	}}}}
	w := httptest.NewRecorder()
	h.Wanted(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/wanted", nil))

	var body struct {
		Wanted []map[string]any `json:"wanted"`
		Note   string           `json:"note"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Wanted) != 1 || body.Wanted[0]["series"] != "Severance" {
		t.Fatalf("wanted = %v", body.Wanted)
	}
	if !strings.Contains(body.Note, "announced") {
		t.Errorf("the note does not explain why announced episodes are absent: %q", body.Note)
	}
}

// A season number is validated before anything is changed.
func TestAMalformedSeasonNumberIsRefused(t *testing.T) {
	h := &Handlers{episodes: &fakeEpisodes{}}
	for _, bad := range []string{"-1", "two", ""} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/x", strings.NewReader(`{"monitored":false}`))
		req.SetPathValue("id", "7")
		req.SetPathValue("season", bad)
		w := httptest.NewRecorder()
		h.SetSeasonMonitored(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("season %q: status %d, want 400", bad, w.Code)
		}
	}
}

// fakeRefresher returns a fixed result and error.
type fakeRefresher struct {
	res tv.RefreshResult
	err error
}

func (f fakeRefresher) Refresh(context.Context, int64) (tv.RefreshResult, error) {
	return f.res, f.err
}

func refreshWith(t *testing.T, f fakeRefresher) *httptest.ResponseRecorder {
	t.Helper()
	h := &Handlers{refresher: f}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/admin/media/7/refresh-episodes", nil)
	req.SetPathValue("id", "7")
	w := httptest.NewRecorder()
	h.RefreshEpisodes(w, req)
	return w
}

// A refresh that could not read every season says so in a field, not only in
// prose: a client cannot tell a clean 200 from a partial one by reading English.
func TestARefreshThatMissedASeasonSaysSo(t *testing.T) {
	w := refreshWith(t, fakeRefresher{res: tv.RefreshResult{
		ItemID: 7, Title: "Severance", Seasons: 4, SeasonsRead: 2, SeasonsSkipped: 1, SeasonsFailed: 1,
	}})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["seasons_failed"] != float64(1) {
		t.Errorf("seasons_failed = %v, want 1: %v", body["seasons_failed"], body)
	}
}

// A provider's failure is the provider's, and a series the provider no longer
// has is the operator's to re-identify. Both were a 500 "internal error".
func TestAProviderFailureDuringARefreshIsSaidToBeTheProviders(t *testing.T) {
	down := refreshWith(t, fakeRefresher{err: fmt.Errorf("library: asking about Severance: %w: HTTP 502",
		metadata.ErrUnavailable)})
	if down.Code != http.StatusBadGateway || !strings.Contains(down.Body.String(), "provider") {
		t.Errorf("provider down: %d %s; want 502 naming the provider", down.Code, down.Body.String())
	}
	gone := refreshWith(t, fakeRefresher{err: fmt.Errorf("library: asking about Severance: %w",
		metadata.ErrNotFound)})
	if gone.Code != http.StatusConflict || !strings.Contains(gone.Body.String(), "identify it again") {
		t.Errorf("series gone from the provider: %d %s; want 409 saying what to do", gone.Code, gone.Body.String())
	}
}

// A rate limit met part-way says what was recorded before it; one met at once
// says nothing was. The first version promised "what was read so far is
// recorded" in both cases, and in the only case that reached it nothing had been.
func TestARateLimitSaysWhatWasRecordedBeforeIt(t *testing.T) {
	partial := refreshWith(t, fakeRefresher{
		res: tv.RefreshResult{ItemID: 7, Title: "Severance", Seasons: 4, SeasonsRead: 1, SeasonsFailed: 3},
		err: metadata.ErrRateLimited,
	})
	if partial.Code != http.StatusServiceUnavailable || partial.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d, Retry-After %q; want 503 with a Retry-After",
			partial.Code, partial.Header().Get("Retry-After"))
	}
	if !strings.Contains(partial.Body.String(), "fetched 1") {
		t.Errorf("a limit met part-way does not say what was recorded: %s", partial.Body.String())
	}

	none := refreshWith(t, fakeRefresher{err: metadata.ErrRateLimited})
	if none.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", none.Code)
	}
	if !strings.Contains(none.Body.String(), "nothing was read") {
		t.Errorf("a limit met on the first request implies something was recorded: %s",
			none.Body.String())
	}
}

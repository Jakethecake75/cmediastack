package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/release"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// A person's season search (ADR-0033, decision 3).

func severanceSeason() *library.SeasonSearchSubject {
	return &library.SeasonSearchSubject{
		ItemID: 4, Season: 2, SeriesTitle: "Severance", SeriesYear: 2022, TMDBID: 95396,
		Episodes: []int{1, 2, 3}, Have: 1,
	}
}

func seasonSearch(t *testing.T, h *Handlers, id, season string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"/api/v1/media/"+id+"/seasons/"+season+"/search", strings.NewReader(`{}`))
	req.SetPathValue("id", id)
	req.SetPathValue("season", season)
	req = req.WithContext(authz.WithPrincipal(req.Context(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
	}))
	w := httptest.NewRecorder()
	h.SearchForSeason(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestASeasonSearchTicketsOnlyTheSeason(t *testing.T) {
	pack := &search.Target{ItemID: 4, Season: 2, Pack: true}
	ep := &search.Target{ItemID: 4, Season: 2, Episode: 3}
	s := &fakeEpisodeSearch{resp: search.Response{Candidates: []search.Candidate{
		candidate("Severance.S02.1080p.WEB.H264-GRP", true, pack, nil),
		candidate("Severance.S02E03.1080p.WEB.H264-GRP", true, ep, nil),
		candidate("Severance.S01.1080p.WEB.H264-GRP", false, nil,
			&release.Rejection{Reason: search.ReasonNotThisEpisode, Detail: "season 1, not season 2"}),
		// Accepted with no target cannot come out of SearchSeason; if it did,
		// it must not be grabbable from here.
		candidate("Severance.S02.720p.HDTV.x264-GRP", true, nil, nil),
	}}}
	sealer := &recordingSealer{}
	h := &Handlers{search: s, episodes: &fakeEpisodes{season: severanceSeason()}, tickets: sealer}

	code, body := seasonSearch(t, h, "4", "2")
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	if s.gotSeason.Want.ItemID != 4 || s.gotSeason.Want.Season != 2 ||
		len(s.gotSeason.Episodes) != 3 || s.gotSeason.Want.Titles[0] != "Severance" {
		t.Errorf("searched for %+v", s.gotSeason)
	}
	var ticketed []string
	for _, c := range body["candidates"].([]any) {
		m := c.(map[string]any)
		if _, ok := m["ticket"]; ok {
			ticketed = append(ticketed, m["title"].(string))
		}
	}
	if strings.Join(ticketed, "|") != "Severance.S02.1080p.WEB.H264-GRP|Severance.S02E03.1080p.WEB.H264-GRP" {
		t.Errorf("tickets for %v; want the season's pack and its episode, nothing else", ticketed)
	}
	for _, c := range sealer.sealed {
		if c.Target == nil || !c.Target.Valid() {
			t.Errorf("sealed %s with target %+v", c.Title, c.Target)
		}
	}
	if season := body["season"].(map[string]any); season["code"] != "S02" || season["series"] != "Severance" {
		t.Errorf("season = %v", season)
	}

	// A season the series does not have is 404, and nothing is searched.
	s2 := &fakeEpisodeSearch{}
	h2 := &Handlers{search: s2, episodes: &fakeEpisodes{season: severanceSeason()}}
	if code, _ := seasonSearch(t, h2, "4", "7"); code != http.StatusNotFound {
		t.Errorf("status %d for a season that does not exist, want 404", code)
	}
	if code, _ := seasonSearch(t, h2, "4", "-1"); code != http.StatusNotFound {
		t.Errorf("status %d for season -1, want 404", code)
	}
	if s2.gotSeason.Want.ItemID != 0 {
		t.Error("a search ran for a season that does not exist")
	}
}

// A season's ticket reaches the queue as a season.
func TestAGrabHandsTheSealedSeasonToTheQueue(t *testing.T) {
	engine := &fakeEngine{}
	h := &Handlers{
		tickets: openingSealer{tk: search.Ticket{IndexerID: 1, Title: "Severance.S02.1080p.WEB.H264-GRP",
			DownloadURL: "https://example.com/x.torrent", Target: &search.Target{ItemID: 4, Season: 2, Pack: true}}},
		grabs: fixedGrab{}, downloads: engine,
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/releases/grab",
		strings.NewReader(`{"ticket":"t"}`))
	req = req.WithContext(authz.WithPrincipal(req.Context(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
	}))
	w := httptest.NewRecorder()
	h.GrabRelease(w, req)
	if w.Code != http.StatusAccepted || len(engine.meta) != 1 {
		t.Fatalf("status %d, %d transfers: %s", w.Code, len(engine.meta), w.Body.String())
	}
	got := engine.meta[0].Target
	if got == nil || !got.Pack || got.ItemID != 4 || got.Season != 2 || got.Episode != 0 {
		t.Errorf("the queue was handed %+v, want season 2 of item 4", got)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if f, _ := body["for"].(map[string]any); f == nil || f["kind"] != "season" || f["code"] != "S02" {
		t.Errorf("for = %v", body["for"])
	}
}

// A person's season search says how far the series reaches, a pack of several
// seasons is ticketed and named as one, and its grab reaches the queue with
// the whole span (ADR-0057).
func TestAPackOfSeveralSeasonsIsTicketedAndGrabbedWhole(t *testing.T) {
	span := &search.Target{ItemID: 4, Season: 1, LastSeason: 3, Pack: true}
	s := &fakeEpisodeSearch{resp: search.Response{Candidates: []search.Candidate{
		candidate("Severance.S01-S03.1080p.WEB.H264-GRP", true, span, nil),
	}}}
	sub := severanceSeason()
	sub.LastSeason = 3
	h := &Handlers{search: s, episodes: &fakeEpisodes{season: sub}, tickets: &recordingSealer{}}
	code, body := seasonSearch(t, h, "4", "2")
	if code != http.StatusOK || s.gotSeason.LastSeason != 3 {
		t.Fatalf("status %d, searched with last season %d", code, s.gotSeason.LastSeason)
	}
	c := body["candidates"].([]any)[0].(map[string]any)
	if c["ticket"] == nil || c["pack"] != true || c["seasons"] != "S01-S03" {
		t.Errorf("candidate %v", c)
	}

	engine := &fakeEngine{}
	g := &Handlers{
		tickets: openingSealer{tk: search.Ticket{IndexerID: 1, Title: "Severance.S01-S03.1080p.WEB.H264-GRP",
			DownloadURL: "https://example.com/x.torrent", Target: span}},
		grabs: fixedGrab{}, downloads: engine,
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/releases/grab",
		strings.NewReader(`{"ticket":"t"}`))
	req = req.WithContext(authz.WithPrincipal(req.Context(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
	}))
	w := httptest.NewRecorder()
	g.GrabRelease(w, req)
	if w.Code != http.StatusAccepted || len(engine.meta) != 1 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got := engine.meta[0].Target; got == nil || !got.Pack || got.Season != 1 || got.LastSeason != 3 {
		t.Errorf("the queue was handed %+v, want seasons 1 to 3", got)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if f, _ := out["for"].(map[string]any); f == nil || f["code"] != "S01-S03" ||
		!strings.Contains(f["label"].(string), "S01-S03") {
		t.Errorf("for = %v", out["for"])
	}
}

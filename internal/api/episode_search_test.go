package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/release"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// The JSON contract of an episode search. The matching itself is tested in
// internal/search; these are about what reaches the client, and above all
// which candidates carry a ticket.

type fakeEpisodeSearch struct {
	resp    search.Response
	err     error
	got     search.EpisodeSearch
	gotFilm search.FilmSearch
	films   int
	// gotSeason is the last season search (ADR-0033).
	gotSeason search.SeasonSearch
	// gotAlbum is the last album search (ADR-0046).
	gotAlbum search.AlbumSearch
	albums   int
	// gotBook is the last book search (ADR-0049).
	gotBook search.BookSearch
}

func (f *fakeEpisodeSearch) SearchBook(_ context.Context, bs search.BookSearch) (search.Response, error) {
	f.gotBook = bs
	return f.resp, f.err
}

func (f *fakeEpisodeSearch) SearchAlbum(_ context.Context, as search.AlbumSearch) (search.Response, error) {
	f.gotAlbum = as
	f.albums++
	return f.resp, f.err
}

func (f *fakeEpisodeSearch) Search(context.Context, search.Request) (search.Response, error) {
	return search.Response{}, errors.New("not this one")
}

func (f *fakeEpisodeSearch) SearchEpisode(_ context.Context, es search.EpisodeSearch) (search.Response, error) {
	f.got = es
	return f.resp, f.err
}

func (f *fakeEpisodeSearch) SearchSeason(_ context.Context, ss search.SeasonSearch) (search.Response, error) {
	f.gotSeason = ss
	return f.resp, f.err
}

func (f *fakeEpisodeSearch) SearchFilm(_ context.Context, fs search.FilmSearch) (search.Response, error) {
	f.gotFilm = fs
	f.films++
	return f.resp, f.err
}

type fakeAltTitles struct {
	titles []string
	err    error
}

func (f fakeAltTitles) AlternativeTitles(context.Context, int64) ([]string, error) {
	return f.titles, f.err
}

// recordingSealer seals a candidate as its title, and remembers what it sealed.
type recordingSealer struct{ sealed []search.Candidate }

func (s *recordingSealer) Seal(c search.Candidate, _ int64) (string, error) {
	s.sealed = append(s.sealed, c)
	return "ticket-for-" + c.Title, nil
}

func (s *recordingSealer) Open(string, int64) (search.Ticket, error) {
	return search.Ticket{}, errors.New("not used here")
}

func severanceSubject() *library.SearchSubject {
	return &library.SearchSubject{
		Episode: library.Episode{ID: 77, ItemID: 4, SeasonNumber: 2, Number: 3,
			Title: "Who Is Alive?", Monitored: true},
		SeriesTitle: "Severance", SeriesYear: 2022, TMDBID: 95396,
	}
}

func candidate(title string, accepted bool, target *search.Target, rej *release.Rejection) search.Candidate {
	return search.Candidate{
		Result:   indexer.Result{Title: title, IndexerName: "A", Parsed: release.Parse(title)},
		Accepted: accepted, Target: target, Rejection: rej,
	}
}

func episodeSearch(t *testing.T, h *Handlers, id, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/episodes/"+id+"/search", strings.NewReader(body))
	req.SetPathValue("id", id)
	req = req.WithContext(authz.WithPrincipal(req.Context(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
	}))
	w := httptest.NewRecorder()
	h.SearchForEpisode(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// Only a candidate that IS the episode, and is accepted, carries a ticket.
func TestOnlyTheEpisodeCarriesATicket(t *testing.T) {
	target := &search.Target{ItemID: 4, Season: 2, Episode: 3}
	s := &fakeEpisodeSearch{resp: search.Response{Candidates: []search.Candidate{
		candidate("Severance.S02E03.1080p.WEB.H264-GRP", true, target, nil),
		candidate("Severance.S02E04.1080p.WEB.H264-GRP", false, nil,
			&release.Rejection{Reason: search.ReasonNotThisEpisode, Detail: "S02E04, not S02E03"}),
		// Accepted with no target cannot come out of SearchEpisode. If it ever
		// did, it must not be grabbable from an episode search: its ticket
		// would carry no episode, and the import would file it by guesswork.
		candidate("Severance.S02E03.720p.HDTV.x264-GRP", true, nil, nil),
	}}}
	sealer := &recordingSealer{}
	h := &Handlers{search: s, episodes: &fakeEpisodes{subject: severanceSubject()}, tickets: sealer}

	code, body := episodeSearch(t, h, "77", `{}`)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	cands := body["candidates"].([]any)
	var withTickets []string
	for _, c := range cands {
		m := c.(map[string]any)
		if _, ok := m["ticket"]; ok {
			withTickets = append(withTickets, m["title"].(string))
		}
	}
	if len(withTickets) != 1 || withTickets[0] != "Severance.S02E03.1080p.WEB.H264-GRP" {
		t.Errorf("tickets issued for %v; only the matching, accepted release may have one", withTickets)
	}
	if len(sealer.sealed) != 1 || sealer.sealed[0].Target == nil {
		t.Errorf("sealed %d candidate(s); the one sealed must carry its target", len(sealer.sealed))
	}
	if body["matches"] != float64(1) {
		t.Errorf("matches = %v, want 1", body["matches"])
	}
	second := cands[1].(map[string]any)
	if second["matches"] != false || !strings.Contains(second["rejected_because"].(string), "S02E04") {
		t.Errorf("a refused candidate should say it does not match, and why: %v", second)
	}
}

// The series' alternative titles are what the search matches against, and the
// search is for the episode the path names.
func TestTheSearchIsForThatEpisodeUnderEveryNameTheSeriesHas(t *testing.T) {
	s := &fakeEpisodeSearch{}
	h := &Handlers{search: s, episodes: &fakeEpisodes{subject: severanceSubject()},
		altTitles: fakeAltTitles{titles: []string{"Separación"}}}

	code, body := episodeSearch(t, h, "77", `{}`)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	w := s.got.Want
	if w.ItemID != 4 || w.Season != 2 || w.Episode != 3 || w.Year != 2022 {
		t.Errorf("searched for %+v", w)
	}
	if len(w.Titles) != 2 || w.Titles[0] != "Severance" || w.Titles[1] != "Separación" {
		t.Errorf("titles = %q; the series' own title first, then its other names", w.Titles)
	}
	if s.got.Term != "Severance" {
		t.Errorf("term = %q; with none given it is the series' title", s.got.Term)
	}
	if _, ok := body["titles_note"]; ok {
		t.Errorf("a note about missing titles when none were missing: %v", body["titles_note"])
	}
}

// When the other names cannot be read, the search still runs on the one it has
// and says so, rather than quietly matching on less.
func TestMissingAlternativeTitlesAreSaid(t *testing.T) {
	for name, h := range map[string]*Handlers{
		"provider failed": {altTitles: fakeAltTitles{err: errors.New("the provider is unavailable")}},
		"no provider":     {},
	} {
		s := &fakeEpisodeSearch{}
		h.search, h.episodes = s, &fakeEpisodes{subject: severanceSubject()}
		code, body := episodeSearch(t, h, "77", `{}`)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", name, code)
		}
		if note, _ := body["titles_note"].(string); !strings.Contains(note, "Only the series' own title") {
			t.Errorf("%s: titles_note = %q", name, note)
		}
		if len(s.got.Want.Titles) != 1 {
			t.Errorf("%s: titles = %q", name, s.got.Want.Titles)
		}
	}
}

// A term given is passed on; it changes what is asked, not what is matched.
func TestAGivenTermIsWhatTheIndexersAreAsked(t *testing.T) {
	s := &fakeEpisodeSearch{}
	h := &Handlers{search: s, episodes: &fakeEpisodes{subject: severanceSubject()}}
	if code, _ := episodeSearch(t, h, "77", `{"term":"  Separacion  "}`); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if s.got.Term != "Separacion" {
		t.Errorf("term = %q", s.got.Term)
	}
	if len(s.got.Want.Titles) == 0 || s.got.Want.Titles[0] != "Severance" {
		t.Errorf("the term leaked into the titles matched against: %q", s.got.Want.Titles)
	}
}

// Results that are all somebody else's episode explain themselves.
func TestASearchThatFoundOnlyOtherEpisodesSaysSo(t *testing.T) {
	s := &fakeEpisodeSearch{resp: search.Response{Candidates: []search.Candidate{
		candidate("Severance.S02E04.1080p.WEB.H264-GRP", false, nil,
			&release.Rejection{Reason: search.ReasonNotThisEpisode, Detail: "S02E04, not S02E03"}),
	}}}
	h := &Handlers{search: s, episodes: &fakeEpisodes{subject: severanceSubject()}}
	_, body := episodeSearch(t, h, "77", `{}`)
	if note, _ := body["note"].(string); !strings.Contains(note, "Severance S02E03") {
		t.Errorf("note = %q", note)
	}
}

// An episode that does not exist is a 404, and nothing is searched.
func TestSearchingForAnEpisodeThatDoesNotExistIs404(t *testing.T) {
	s := &fakeEpisodeSearch{}
	h := &Handlers{search: s, episodes: &fakeEpisodes{subject: severanceSubject()}}
	if code, _ := episodeSearch(t, h, "78", `{}`); code != http.StatusNotFound {
		t.Errorf("status %d, want 404", code)
	}
	if s.got.Want.ItemID != 0 {
		t.Error("a search ran for an episode that does not exist")
	}
}

// openingSealer opens every token as one fixed ticket.
type openingSealer struct{ tk search.Ticket }

func (s openingSealer) Seal(search.Candidate, int64) (string, error) { return "t", nil }
func (s openingSealer) Open(string, int64) (search.Ticket, error)    { return s.tk, nil }

type fixedGrab struct{}

func (fixedGrab) Grab(_ context.Context, tk search.Ticket) (search.Grabbed, error) {
	return search.Grabbed{Payload: indexer.Payload{Torrent: []byte("d4:infod6:lengthi1eee")},
		IndexerID: tk.IndexerID, IndexerName: "A", Title: tk.Title}, nil
}

// The grab takes the episode from the SEALED ticket and hands it to the queue,
// where the import will find it. A grab from the general search hands nothing.
func TestAGrabHandsTheSealedEpisodeToTheQueue(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target *search.Target
	}{
		{"episode search", &search.Target{ItemID: 4, Season: 2, Episode: 3}},
		{"general search", nil},
	} {
		engine := &fakeEngine{}
		h := &Handlers{
			tickets: openingSealer{tk: search.Ticket{IndexerID: 1, Title: "Severance.S02E03.1080p.WEB.H264-GRP",
				DownloadURL: "https://example.com/x.torrent", Target: tc.target}},
			grabs: fixedGrab{}, downloads: engine,
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/releases/grab", strings.NewReader(`{"ticket":"t"}`))
		req = req.WithContext(authz.WithPrincipal(req.Context(), &authz.Principal{
			UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
		}))
		w := httptest.NewRecorder()
		h.GrabRelease(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("%s: status %d: %s", tc.name, w.Code, w.Body.String())
		}
		if len(engine.meta) != 1 {
			t.Fatalf("%s: %d transfers added", tc.name, len(engine.meta))
		}
		got := engine.meta[0].Target
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		switch {
		case tc.target == nil && got != nil:
			t.Errorf("%s: a target appeared from nowhere: %+v", tc.name, got)
		case tc.target == nil && body["for"] != nil:
			t.Errorf("%s: the response claims a target: %v", tc.name, body["for"])
		case tc.target != nil && (got == nil || got.ItemID != 4 || got.Season != 2 || got.Episode != 3):
			t.Errorf("%s: the queue was handed %+v, want item 4 S02E03", tc.name, got)
		case tc.target != nil && body["for"] == nil:
			t.Errorf("%s: the response does not say what the grab is for", tc.name)
		}
	}
}

// No indexers is a 409 with a sentence, as for the general search.
func TestAnEpisodeSearchWithNoIndexersSaysSo(t *testing.T) {
	s := &fakeEpisodeSearch{err: search.ErrNoIndexers}
	h := &Handlers{search: s, episodes: &fakeEpisodes{subject: severanceSubject()}}
	if code, body := episodeSearch(t, h, "77", `{}`); code != http.StatusConflict ||
		!strings.Contains(body["error"].(string), "no indexers") {
		t.Errorf("status %d, body %v", code, body)
	}
}

// ADR-0064: a person's episode search knows the episode's air date, and asks
// by it when the series is daily.
func TestAnEpisodeSearchKnowsItsAirDate(t *testing.T) {
	for _, daily := range []bool{false, true} {
		sub := severanceSubject()
		sub.Episode.Aired = time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
		sub.Daily = daily
		s := &fakeEpisodeSearch{}
		h := &Handlers{search: s, episodes: &fakeEpisodes{subject: sub}, tickets: &recordingSealer{}}
		if code, body := episodeSearch(t, h, "77", `{}`); code != http.StatusOK {
			t.Fatalf("status %d: %v", code, body)
		}
		if s.got.Want.AirDate != "2026-10-02" || s.got.ByDate != daily {
			t.Errorf("daily=%v: searched %+v", daily, s.got)
		}
	}
}

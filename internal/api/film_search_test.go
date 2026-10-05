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
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/playback"
	"github.com/jakethecake75/cmediastack/internal/release"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// The JSON contract of a film search (ADR-0026). The matching itself is tested
// in internal/search; these are about what reaches the client, and above all
// which candidates carry a ticket.

// fakeFilms is a library of one film, Dune (2021), and one series.
type fakeFilms struct {
	items map[int64]importer.Item
	files map[int64][]importer.File
}

func duneLibrary() *fakeFilms {
	return &fakeFilms{items: map[int64]importer.Item{
		7: {ID: 7, Kind: importer.KindMovie, Title: "Dune", Year: 2021, TMDBID: 438631,
			Folder: "Dune (2021)", AddedAt: time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC),
			Monitored: true},
		4: {ID: 4, Kind: importer.KindSeries, Title: "Severance", Year: 2022, TMDBID: 95396},
		9: {ID: 9, Kind: importer.KindMovie, Title: "Untitled", TMDBID: 1, Monitored: true},
	}, files: map[int64][]importer.File{}}
}

func (f *fakeFilms) ListItems(context.Context, string) ([]importer.Item, error) { return nil, nil }
func (f *fakeFilms) GetItem(_ context.Context, id int64) (importer.Item, error) {
	it, ok := f.items[id]
	if !ok {
		return importer.Item{}, importer.ErrItemNotFound
	}
	return it, nil
}
func (f *fakeFilms) SetRating(context.Context, int64, string) (importer.Item, importer.Item, error) {
	return importer.Item{}, importer.Item{}, nil
}
func (f *fakeFilms) HoldsProviderID(context.Context, int64) (bool, error)  { return true, nil }
func (f *fakeFilms) HasTitle(context.Context, string, int64) (bool, error) { return false, nil }
func (f *fakeFilms) ItemForQueue(ctx context.Context, id int64) (importer.Item, error) {
	return f.GetItem(ctx, id)
}
func (f *fakeFilms) FilesFor(_ context.Context, id int64) ([]importer.File, error) {
	return f.files[id], nil
}
func (f *fakeFilms) RecordsFor(context.Context, string) ([]importer.Record, error) { return nil, nil }
func (f *fakeFilms) FileForPlayback(context.Context, int64) (playback.FileRef, error) {
	return playback.FileRef{}, importer.ErrFileNotFound
}
func (f *fakeFilms) Film(ctx context.Context, id int64) (importer.Item, error) {
	it, err := f.GetItem(ctx, id)
	if err != nil {
		return importer.Item{}, err
	}
	if it.Kind != importer.KindMovie {
		return importer.Item{}, importer.ErrNotAFilm
	}
	return it, nil
}
func (f *fakeFilms) WantedFilms(context.Context, int) ([]importer.Item, error) {
	var out []importer.Item
	for _, it := range f.items {
		if it.Kind == importer.KindMovie && it.Monitored && len(f.files[it.ID]) == 0 {
			out = append(out, it)
		}
	}
	return out, nil
}
func (f *fakeFilms) SetQualityProfile(_ context.Context, id, profileID int64) (importer.Item, error) {
	it, ok := f.items[id]
	if !ok {
		return importer.Item{}, importer.ErrItemNotFound
	}
	it.QualityProfileID = profileID
	f.items[id] = it
	return it, nil
}

func (f *fakeFilms) SetFilmMonitored(ctx context.Context, id int64, on bool) (importer.Item, error) {
	it, err := f.Film(ctx, id)
	if err != nil {
		return importer.Item{}, err
	}
	it.Monitored = on
	f.items[id] = it
	return it, nil
}

type fakeFilmTitles struct {
	titles []string
	err    error
	asked  []int64
}

func (f *fakeFilmTitles) FilmTitles(_ context.Context, id int64) ([]string, error) {
	f.asked = append(f.asked, id)
	return f.titles, f.err
}

func filmSearch(t *testing.T, h *Handlers, id, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/media/"+id+"/search", strings.NewReader(body))
	req.SetPathValue("id", id)
	req = req.WithContext(authz.WithPrincipal(req.Context(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
	}))
	w := httptest.NewRecorder()
	h.SearchForFilm(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// Only a candidate that IS the film — its film, in the film form — and is
// accepted carries a ticket.
func TestOnlyTheFilmCarriesATicket(t *testing.T) {
	film := &search.Target{ItemID: 7, Film: true}
	s := &fakeEpisodeSearch{resp: search.Response{Candidates: []search.Candidate{
		candidate("Dune.2021.1080p.BluRay.x264-GRP", true, film, nil),
		candidate("Dune.1984.1080p.BluRay.x264-OLD", false, nil,
			&release.Rejection{Reason: search.ReasonNotThisFilm, Detail: "a different film: Dune (1984)"}),
		// None of these can come out of SearchFilm. Each, if it ever did, must
		// not be grabbable from a film search: no target files it by
		// guesswork, an episode target is not this film, and another film's
		// target is somebody else's.
		candidate("Dune.2021.720p.WEB.H264-GRP", true, nil, nil),
		candidate("Dune.2021.2160p.WEB.H264-GRP", true, &search.Target{ItemID: 7, Season: 1, Episode: 1}, nil),
		candidate("Dune.2021.576p.WEB.H264-GRP", true, &search.Target{ItemID: 8, Film: true}, nil),
		// Matching, and refused by the profile: no ticket either.
		candidate("Dune.2021.HDCAM.x264-CAMGRP", false, film,
			&release.Rejection{Reason: release.ReasonForbiddenTerm, Detail: "forbidden: cam"}),
	}}}
	sealer := &recordingSealer{}
	h := &Handlers{search: s, media: duneLibrary(), tickets: sealer}

	code, body := filmSearch(t, h, "7", `{}`)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	var withTickets []string
	for _, c := range body["candidates"].([]any) {
		m := c.(map[string]any)
		if _, ok := m["ticket"]; ok {
			withTickets = append(withTickets, m["title"].(string))
		}
	}
	if len(withTickets) != 1 || withTickets[0] != "Dune.2021.1080p.BluRay.x264-GRP" {
		t.Errorf("tickets issued for %v; only the matching, accepted release may have one", withTickets)
	}
	if len(sealer.sealed) != 1 || sealer.sealed[0].Target == nil || !sealer.sealed[0].Target.Film {
		t.Errorf("sealed %+v; the one sealed must carry its film", sealer.sealed)
	}
	if body["matches"] != float64(2) {
		t.Errorf("matches = %v, want 2: the 1080p and the CAM are both the film", body["matches"])
	}
	f, _ := body["film"].(map[string]any)
	if f["name"] != "Dune (2021)" || f["have"] != false {
		t.Errorf("film = %v", f)
	}
}

// The search is for the film the path names, under every name it has, asked
// for by its folded title and year.
func TestTheSearchIsForThatFilmUnderEveryNameItHas(t *testing.T) {
	s := &fakeEpisodeSearch{}
	names := &fakeFilmTitles{titles: []string{"Dune", "Dune", "Dune: Part One", " "}}
	h := &Handlers{search: s, media: duneLibrary(), filmTitles: names}

	code, body := filmSearch(t, h, "7", `{}`)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, body)
	}
	w := s.gotFilm.Want
	if w.ItemID != 7 || w.Year != 2021 {
		t.Errorf("searched for %+v", w)
	}
	if strings.Join(w.Titles, "|") != "Dune|Dune: Part One" {
		t.Errorf("titles = %q; the film's own title first, then its other names, once each", w.Titles)
	}
	if len(names.asked) != 1 || names.asked[0] != 438631 {
		t.Errorf("the provider was asked about %v, want the film's own id", names.asked)
	}
	if s.gotFilm.Term != "dune 2021" || body["term"] != "dune 2021" {
		t.Errorf("term = %q / %v; with none given it is the folded title and year", s.gotFilm.Term, body["term"])
	}
	if _, ok := body["titles_note"]; ok {
		t.Errorf("a note about missing titles when none were missing: %v", body["titles_note"])
	}
}

// When the other names cannot be had, the search runs on the one it has and
// says why.
func TestMissingFilmTitlesAreSaid(t *testing.T) {
	notIdentified := duneLibrary()
	it := notIdentified.items[7]
	it.TMDBID = 0
	notIdentified.items[7] = it
	for name, tc := range map[string]struct {
		h    *Handlers
		says string
	}{
		"provider failed": {&Handlers{media: duneLibrary(),
			filmTitles: &fakeFilmTitles{err: errors.New("the provider is unavailable")}}, "could not be read"},
		"no provider":    {&Handlers{media: duneLibrary()}, "no metadata provider"},
		"not identified": {&Handlers{media: notIdentified, filmTitles: &fakeFilmTitles{}}, "has not been identified"},
	} {
		s := &fakeEpisodeSearch{}
		tc.h.search = s
		code, body := filmSearch(t, tc.h, "7", `{}`)
		if code != http.StatusOK {
			t.Fatalf("%s: status %d", name, code)
		}
		note, _ := body["titles_note"].(string)
		if !strings.Contains(note, "Only the film's own title") || !strings.Contains(note, tc.says) {
			t.Errorf("%s: titles_note = %q", name, note)
		}
		if len(s.gotFilm.Want.Titles) != 1 {
			t.Errorf("%s: titles = %q", name, s.gotFilm.Want.Titles)
		}
	}
}

// A term given is what the indexers are asked, and nothing else changes.
func TestAGivenTermIsWhatTheIndexersAreAskedForAFilm(t *testing.T) {
	s := &fakeEpisodeSearch{}
	h := &Handlers{search: s, media: duneLibrary()}
	if code, _ := filmSearch(t, h, "7", `{"term":"  Dune Part One  "}`); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if s.gotFilm.Term != "Dune Part One" || s.gotFilm.Want.Titles[0] != "Dune" || s.gotFilm.Want.Year != 2021 {
		t.Errorf("searched %+v", s.gotFilm)
	}
}

// What cannot be searched for as a film is refused before any indexer is
// asked, with the reason.
func TestWhatIsNotAFilmWithAYearIsNotSearchedFor(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want int
		says string
	}{
		{"4", http.StatusConflict, "that is a series"},
		{"9", http.StatusConflict, "has no year"},
		{"99", http.StatusNotFound, "not found"},
	} {
		s := &fakeEpisodeSearch{}
		h := &Handlers{search: s, media: duneLibrary()}
		code, body := filmSearch(t, h, tc.id, `{}`)
		if code != tc.want || !strings.Contains(body["error"].(string), tc.says) {
			t.Errorf("item %s: %d %v, want %d saying %q", tc.id, code, body, tc.want, tc.says)
		}
		if s.films != 0 {
			t.Errorf("item %s: the indexers were asked anyway", tc.id)
		}
	}
}

// An empty answer, and an answer of other films, each say what to do next.
func TestAFilmSearchThatFoundNothingSaysWhatToTryNext(t *testing.T) {
	s := &fakeEpisodeSearch{}
	h := &Handlers{search: s, media: duneLibrary()}
	_, body := filmSearch(t, h, "7", `{}`)
	if note, _ := body["note"].(string); !strings.Contains(note, "dune 2021") || !strings.Contains(note, "a year out") {
		t.Errorf("nothing offered: note = %q", note)
	}

	s.resp = search.Response{Candidates: []search.Candidate{
		candidate("Dune.1984.1080p.BluRay.x264-OLD", false, nil,
			&release.Rejection{Reason: search.ReasonNotThisFilm, Detail: "a different film: Dune (1984)"}),
	}}
	_, body = filmSearch(t, h, "7", `{}`)
	if note, _ := body["note"].(string); !strings.Contains(note, "nothing they offered is Dune (2021)") {
		t.Errorf("only other films: note = %q", note)
	}
}

// No indexers is a 409 with a sentence, as for every search.
func TestAFilmSearchWithNoIndexersSaysSo(t *testing.T) {
	h := &Handlers{search: &fakeEpisodeSearch{err: search.ErrNoIndexers}, media: duneLibrary()}
	if code, body := filmSearch(t, h, "7", `{}`); code != http.StatusConflict ||
		!strings.Contains(body["error"].(string), "no indexers") {
		t.Errorf("status %d, body %v", code, body)
	}
}

// The grab takes the film from the SEALED ticket and hands it to the queue,
// and its answer names the film.
func TestAGrabHandsTheSealedFilmToTheQueue(t *testing.T) {
	engine := &fakeEngine{}
	h := &Handlers{
		tickets: openingSealer{tk: search.Ticket{IndexerID: 1, Title: "Dune.Part.One.2021.1080p.BluRay.x264-GRP",
			DownloadURL: "https://example.com/x.torrent", Target: &search.Target{ItemID: 7, Film: true}}},
		grabs: fixedGrab{}, downloads: engine, media: duneLibrary(),
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/releases/grab", strings.NewReader(`{"ticket":"t"}`))
	req = req.WithContext(authz.WithPrincipal(req.Context(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
	}))
	w := httptest.NewRecorder()
	h.GrabRelease(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(engine.meta) != 1 || engine.meta[0].Target == nil ||
		*engine.meta[0].Target != (download.Target{ItemID: 7, Film: true}) {
		t.Fatalf("the queue was handed %+v, want film 7", engine.meta)
	}
	var body struct {
		For map[string]any `json:"for"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.For["kind"] != "film" || body.For["label"] != "Dune (2021)" || body.For["item_id"] != float64(7) {
		t.Errorf("for = %v; the grab should say it is for Dune (2021)", body.For)
	}
	if _, ok := body.For["code"]; ok {
		t.Errorf("a film was given an episode code: %v", body.For)
	}
	if _, ok := body.For["season"]; ok {
		t.Errorf("a film was given a season: %v", body.For)
	}
}

// Wanted lists films beside episodes, each named.
func TestWantedListsFilmsBesideEpisodes(t *testing.T) {
	h := &Handlers{episodes: &fakeEpisodes{}, media: duneLibrary()}
	w := httptest.NewRecorder()
	h.Wanted(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/wanted", nil))
	var body struct {
		Films     []map[string]any `json:"films"`
		FilmCount int              `json:"film_count"`
		Wanted    []any            `json:"wanted"`
		Note      string           `json:"note"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.FilmCount != 2 || len(body.Films) != 2 {
		t.Fatalf("films = %v", body.Films)
	}
	names := map[string]bool{}
	for _, f := range body.Films {
		names[f["name"].(string)] = true
	}
	if !names["Dune (2021)"] || !names["Untitled"] {
		t.Errorf("films named %v", names)
	}
	if body.Wanted == nil {
		t.Error("the episodes list is missing rather than empty")
	}
	if !strings.Contains(body.Note, "released or not") {
		t.Errorf("the note does not say a film is wanted whether or not it is out: %q", body.Note)
	}
}

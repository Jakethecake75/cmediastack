package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/acquire"
	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/music"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// oneAlbum is a music service holding one album. Its store refuses every read:
// the principal these tests use may not browse, so no namesake is looked up.
type oneAlbum struct{ album music.Album }

func (oneAlbum) SearchArtists(context.Context, string) ([]music.ArtistMatch, error) { return nil, nil }
func (oneAlbum) Add(context.Context, music.AddRequest) (music.AddResult, error) {
	return music.AddResult{}, nil
}
func (o oneAlbum) Album(_ context.Context, id int64) (music.Album, error) {
	if id != o.album.ID {
		return music.Album{}, music.ErrNoSuchAlbum
	}
	return o.album, nil
}
func (oneAlbum) Store() *music.Store { return music.NewStore(nil, nil) }

// ADR-0046, decision 7: only a candidate that IS the album, and is accepted,
// carries a ticket — whatever the search service says.
func TestOnlyTheAlbumCarriesATicket(t *testing.T) {
	dummy := music.Album{ID: 30, ItemID: 3, Artist: "Portishead", Title: "Dummy", Released: "1994-08-22"}
	s := &fakeEpisodeSearch{resp: search.Response{Candidates: []search.Candidate{
		candidate("Portishead - Dummy (1994) [FLAC]", true, &search.Target{ItemID: 3, Album: 30}, nil),
		// None of these can come out of SearchAlbum. Each, if it ever did,
		// must still not be grabbable for this album.
		candidate("Portishead - Third (2008) [FLAC]", true, &search.Target{ItemID: 3, Album: 31}, nil),
		candidate("Massive Attack - Dummy [FLAC]", true, &search.Target{ItemID: 4, Album: 30}, nil),
		candidate("Portishead - Dummy [MP3]", true, nil, nil),
		candidate("Portishead - Dummy (1994)", false, &search.Target{ItemID: 3, Album: 30}, nil),
	}}}
	sealer := &recordingSealer{}
	h := &Handlers{search: s, music: oneAlbum{dummy}, tickets: sealer}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/albums/30/search", strings.NewReader(`{}`))
	req.SetPathValue("id", "30")
	req = req.WithContext(authz.WithPrincipal(req.Context(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
	}))
	w := httptest.NewRecorder()
	h.SearchForAlbum(w, req)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %v", w.Code, body)
	}
	if len(sealer.sealed) != 1 || sealer.sealed[0].Title != "Portishead - Dummy (1994) [FLAC]" {
		t.Errorf("sealed %d: %+v", len(sealer.sealed), sealer.sealed)
	}
	if body["matches"] != float64(2) || body["term"] != "portishead dummy" {
		t.Errorf("matches %v term %v", body["matches"], body["term"])
	}
	want := s.gotAlbum.Want
	if want.ItemID != 3 || want.AlbumID != 30 || want.Year != 1994 || want.Title != "Dummy" ||
		len(want.Artists) != 1 || want.Artists[0] != "Portishead" {
		t.Errorf("searched for %+v", want)
	}

	// An album that is not there, or not the caller's to see, is not searched for.
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/albums/31/search", strings.NewReader(`{}`))
	req.SetPathValue("id", "31")
	w = httptest.NewRecorder()
	h.SearchForAlbum(w, req)
	if w.Code != http.StatusNotFound || s.albums != 1 {
		t.Errorf("a missing album: %d, %d searches", w.Code, s.albums)
	}
}

// ADR-0047, decision 5: the Wanted screen says what automatic acquisition did
// about an album, and why it will not look for one.
func TestTheWantedScreenSaysWhatWasDoneAboutAnAlbum(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	listed := music.Album{ID: 30, TracksKnown: true, Known: 11}
	rep := &acquire.Report{
		States: map[acquire.StateKey]acquire.State{
			{Album: true, ID: 30}: {Outcome: acquire.OutcomeNothing, Detail: "none had seeders",
				SearchedAt: now.Add(-time.Hour), NextAt: now.Add(5 * time.Hour)},
		},
		InFlight:       map[acquire.Key]bool{{Album: 31}: true},
		AlbumsImported: map[int64]bool{32: true},
	}
	for name, tc := range map[string]struct {
		album  music.Album
		status string
		next   bool
	}{
		"searched":    {listed, acquire.OutcomeNothing, true},
		"downloading": {music.Album{ID: 31, TracksKnown: true, Known: 3}, "downloading", false},
		"imported":    {music.Album{ID: 32, TracksKnown: true, Known: 3}, "imported_once", false},
		"no list":     {music.Album{ID: 33}, "not_searchable", false},
		"not yet":     {music.Album{ID: 34, TracksKnown: true, Known: 3}, "not_searched", false},
	} {
		got := albumAutomaticJSON(tc.album, rep, now)
		if got["status"] != tc.status {
			t.Errorf("%s: status %v, want %s", name, got["status"], tc.status)
		}
		if _, has := got["next_search_at"]; has != tc.next {
			t.Errorf("%s: next search %v", name, got["next_search_at"])
		}
		if tc.status != acquire.OutcomeNothing && got["note"] == nil {
			t.Errorf("%s: no note", name)
		}
	}
}

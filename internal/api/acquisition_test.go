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

	"github.com/jakethecake75/cmediastack/internal/acquire"
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/library"
)

// The Wanted screen's view of automatic acquisition (ADR-0030). What the passes
// do is tested against a real database in internal/acquire; these are about
// what reaches the client.

type fakeAcquisition struct {
	rep acquire.Report
	err error
}

func (f *fakeAcquisition) Report(context.Context) (acquire.Report, error) { return f.rep, f.err }
func (f *fakeAcquisition) Settings() acquire.Config {
	return acquire.Config{RecentInterval: 15 * time.Minute, SearchInterval: 15 * time.Minute,
		SearchesPerRun: 3, MaxGrabsPerRun: 5}
}

func wantedEpisodes() *fakeEpisodes {
	aired := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	ep := func(id int64, n int) library.WantedEpisode {
		return library.WantedEpisode{SeriesTitle: "Severance", Episode: library.Episode{
			ID: id, ItemID: 4, SeasonNumber: 2, Number: n, Aired: aired, Monitored: true}}
	}
	return &fakeEpisodes{wanted: []library.WantedEpisode{ep(11, 3), ep(12, 4), ep(13, 5)}}
}

func getWanted(t *testing.T, h *Handlers) map[string]any {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/wanted", nil)
	w := httptest.NewRecorder()
	h.Wanted(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// Beside every wanted item, what automatic acquisition last did about it and
// what it will do next — the answer to "why has this not arrived?".
func TestTheWantedScreenSaysWhatAutomaticAcquisitionIsDoing(t *testing.T) {
	now := time.Now()
	later := now.Add(6 * time.Hour)
	earlier := now.Add(-time.Hour)
	h := &Handlers{episodes: wantedEpisodes(), media: duneLibrary(), acquisition: &fakeAcquisition{
		rep: acquire.Report{
			States: map[acquire.StateKey]acquire.State{
				{ID: 11}: {SearchedAt: earlier, NextAt: earlier, Outcome: acquire.OutcomeGrabbed,
					Detail: "grabbed Severance.S02E03.1080p.WEB-DL.H264-GRP, from Tracker"},
				{ID: 12}: {SearchedAt: earlier, NextAt: later, Fruitless: 1, Outcome: acquire.OutcomeNothing,
					Detail: "the indexers found nothing"},
				{Film: true, ID: 7}: {SearchedAt: earlier.Add(-time.Hour), NextAt: earlier,
					Fruitless: 2, Outcome: acquire.OutcomeNothing, Detail: "12 result(s), none of them Dune (2021)"},
			},
			InFlight: map[acquire.Key]bool{{ItemID: 4, Season: 2, Episode: 3}: true},
		},
	}}
	body := getWanted(t, h)

	auto := body["automatic"].(map[string]any)
	if auto["enabled"] != true || auto["searches_per_run"] != float64(3) ||
		auto["max_grabs_per_run"] != float64(5) || auto["recent_every_seconds"] != float64(900) ||
		auto["search_every_seconds"] != float64(900) {
		t.Fatalf("automatic = %v", auto)
	}

	rows := body["wanted"].([]any)
	status := func(row any) map[string]any { return row.(map[string]any)["automatic"].(map[string]any) }

	if a := status(rows[0]); a["status"] != "downloading" || a["outcome"] != "grabbed" ||
		a["next_search_at"] != nil {
		t.Errorf("S02E03 = %v", a)
	}
	if a := status(rows[1]); a["status"] != "nothing" || a["fruitless"] != float64(1) ||
		a["next_search_at"] != later.UTC().Format(time.RFC3339) || a["detail"] != "the indexers found nothing" {
		t.Errorf("S02E04 = %v", a)
	}
	if a := status(rows[2]); a["status"] != "not_searched" || a["searched_at"] != nil {
		t.Errorf("S02E05 = %v", a)
	}

	films := map[string]map[string]any{}
	for _, f := range body["films"].([]any) {
		films[f.(map[string]any)["name"].(string)] = status(f)
	}
	// Due again already: no next search time is shown, because it is next.
	if a := films["Dune (2021)"]; a["status"] != "nothing" || a["next_search_at"] != nil ||
		!strings.Contains(a["detail"].(string), "none of them Dune") {
		t.Errorf("Dune = %v", a)
	}
	if a := films["Untitled"]; a["status"] != "not_searchable" || !strings.Contains(a["note"].(string), "no year") {
		t.Errorf("a film with no year = %v", a)
	}
}

// Off says off, and the rows carry nothing that could be read as a plan.
func TestTheWantedScreenSaysWhenAutomaticAcquisitionIsOff(t *testing.T) {
	body := getWanted(t, &Handlers{episodes: wantedEpisodes(), media: duneLibrary()})
	auto := body["automatic"].(map[string]any)
	if auto["enabled"] != false || !strings.Contains(auto["note"].(string), "acquisition.automatic") {
		t.Fatalf("automatic = %v", auto)
	}
	for _, row := range body["wanted"].([]any) {
		if _, ok := row.(map[string]any)["automatic"]; ok {
			t.Fatalf("a row says what automatic acquisition did while it is off: %v", row)
		}
	}
}

// A report that cannot be read does not cost the operator the list of what is
// missing; it is said instead.
func TestAnUnreadableReportLeavesTheListAndSaysSo(t *testing.T) {
	body := getWanted(t, &Handlers{episodes: wantedEpisodes(), media: duneLibrary(),
		acquisition: &fakeAcquisition{err: errors.New("database is locked")}})
	auto := body["automatic"].(map[string]any)
	if !strings.Contains(auto["error"].(string), "database is locked") {
		t.Fatalf("automatic = %v", auto)
	}
	if n := len(body["wanted"].([]any)); n != 3 {
		t.Fatalf("%d rows, want 3", n)
	}
}

func putMonitored(t *testing.T, h *Handlers, id, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/v1/media/"+id+"/monitored",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h.SetFilmMonitored(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// A film can be kept without being wanted, and then it is off the Wanted list —
// which, with automatic acquisition on, is what keeps it from being fetched.
func TestAFilmCanBeKeptWithoutBeingWanted(t *testing.T) {
	lib := duneLibrary()
	h := &Handlers{episodes: &fakeEpisodes{}, media: lib}

	code, out := putMonitored(t, h, "7", `{"monitored":false}`)
	if code != http.StatusOK || out["monitored"] != false || out["name"] != "Dune (2021)" ||
		!strings.Contains(out["note"].(string), "off the Wanted list") {
		t.Fatalf("%d %v", code, out)
	}
	for _, f := range getWanted(t, h)["films"].([]any) {
		if f.(map[string]any)["item_id"] == float64(7) {
			t.Fatal("an unmonitored film is still on the Wanted list")
		}
	}
	if code, _ := putMonitored(t, h, "7", `{"monitored":true}`); code != http.StatusOK || !lib.items[7].Monitored {
		t.Fatalf("monitoring it again: %d", code)
	}

	for _, c := range []struct {
		id, body string
		want     int
	}{
		{"4", `{"monitored":false}`, http.StatusConflict}, // a series
		{"99", `{"monitored":false}`, http.StatusNotFound},
		{"7", `{"monitored":"no"}`, http.StatusBadRequest},
		{"7", `{"monitored":false,"title":"x"}`, http.StatusBadRequest},
	} {
		if code, out := putMonitored(t, h, c.id, c.body); code != c.want {
			t.Errorf("PUT %s %s = %d (%v), want %d", c.id, c.body, code, out, c.want)
		}
	}
}

// The queue says which downloads no person asked for, and removing one says
// what that does and does not mean.
func TestTheQueueSaysWhichDownloadsAreAutomatic(t *testing.T) {
	person := int64(1)
	eng := &fakeEngine{records: []download.Record{
		{InfoHash: hashFor("a"), Title: "Severance.S02E03.1080p.WEB-DL.H264-GRP", AddedBy: nil,
			AddedLabel: acquire.Label, Status: download.StatusDownloading},
		{InfoHash: hashFor("b"), Title: "Dune.2021.1080p.BluRay.x264-GRP", AddedBy: &person,
			AddedLabel: "jacob", Status: download.StatusDownloading},
		// A label alone is not enough: a person called "system:acquire" is a
		// person.
		{InfoHash: hashFor("c"), Title: "Heat.1995.1080p.BluRay.x264-GRP", AddedBy: &person,
			AddedLabel: acquire.Label, Status: download.StatusComplete},
	}, items: []download.Transfer{{InfoHash: hashFor("a")}}}
	h := &Handlers{downloads: eng}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/queue", nil)
	w := httptest.NewRecorder()
	h.Queue(w, req)
	var body struct {
		Items []struct {
			InfoHash  string `json:"info_hash"`
			Automatic bool   `json:"automatic"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK {
		t.Fatalf("%d %v %s", w.Code, err, w.Body.String())
	}
	got := map[string]bool{}
	for _, it := range body.Items {
		got[it.InfoHash] = it.Automatic
	}
	if !got[hashFor("a")] || got[hashFor("b")] || got[hashFor("c")] || len(got) != 3 {
		t.Fatalf("automatic = %v", got)
	}

	remove := func(h *Handlers) string {
		eng.items = []download.Transfer{{InfoHash: hashFor("a")}}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/queue/"+hashFor("a"), nil)
		req.SetPathValue("id", hashFor("a"))
		w := httptest.NewRecorder()
		h.QueueRemove(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("remove: %d %s", w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out["note"].(string)
	}
	if note := remove(h); strings.Contains(note, "Automatic acquisition") {
		t.Errorf("with automatic acquisition off, the note mentions it: %s", note)
	}
	h.acquisition = &fakeAcquisition{}
	if note := remove(h); !strings.Contains(note, "never grab this release again") ||
		!strings.Contains(note, "unmonitor") {
		t.Errorf("with automatic acquisition on, the note = %s", note)
	}
}

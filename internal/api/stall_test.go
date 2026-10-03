package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/acquire"
	"github.com/jakethecake75/cmediastack/internal/download"
)

// The queue says a download stopped moving, since when, and whether it was
// given up (ADR-0034).
func TestTheQueueSaysADownloadStalled(t *testing.T) {
	person := int64(1)
	moved := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	found := moved.Add(25 * time.Hour)
	eng := &fakeEngine{records: []download.Record{
		{InfoHash: hashFor("a"), Title: "Machine.S01.1080p-GRP", AddedLabel: acquire.Label,
			Status: download.StatusStopped, AddedAt: moved.Add(-time.Hour), ProgressedAt: moved, StalledAt: found},
		{InfoHash: hashFor("b"), Title: "Person.S01E01.1080p-GRP", AddedBy: &person, AddedLabel: "jacob",
			Status: download.StatusDownloading, AddedAt: moved, StalledAt: found},
		{InfoHash: hashFor("c"), Title: "Moving.S01E01.1080p-GRP", AddedLabel: "jacob",
			Status: download.StatusDownloading, AddedAt: moved},
	}, items: []download.Transfer{{InfoHash: hashFor("b")}, {InfoHash: hashFor("c")}}}
	h := &Handlers{downloads: eng}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/queue", nil)
	w := httptest.NewRecorder()
	h.Queue(w, req)
	var body struct {
		Items []struct {
			InfoHash string `json:"info_hash"`
			Stalled  *struct {
				Since   time.Time `json:"since"`
				FoundAt time.Time `json:"found_at"`
				GivenUp bool      `json:"given_up"`
			} `json:"stalled"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK {
		t.Fatalf("%d %v %s", w.Code, err, w.Body.String())
	}
	got := map[string]int{}
	for i, it := range body.Items {
		got[it.InfoHash] = i
	}
	a, b, c := body.Items[got[hashFor("a")]], body.Items[got[hashFor("b")]], body.Items[got[hashFor("c")]]
	if a.Stalled == nil || !a.Stalled.GivenUp || !a.Stalled.Since.Equal(moved) || !a.Stalled.FoundAt.Equal(found) {
		t.Errorf("the machine's: %+v; want given up, still since %v", a.Stalled, moved)
	}
	if b.Stalled == nil || b.Stalled.GivenUp || !b.Stalled.Since.Equal(moved) {
		t.Errorf("the person's: %+v; want reported, not given up, since it was added", b.Stalled)
	}
	if c.Stalled != nil {
		t.Errorf("a moving download says it stalled: %+v", c.Stalled)
	}
}

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/acquire"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/music"
)

// Automatic acquisition, as the screens see it (ADR-0030).
//
// Nothing here starts a search or a grab: the passes are scheduled tasks
// running as system:acquire, and a person's own Search and Grab are the routes
// they always were. What this surface adds is the answer to "what is it doing
// about this?" beside each wanted item, and the switch that says a film is not
// wanted.

// AcquisitionService reports what automatic acquisition last did about each
// wanted item. *acquire.Service is one.
type AcquisitionService interface {
	Report(ctx context.Context) (acquire.Report, error)
	Settings() acquire.Config
}

// automaticSettingsJSON is the top of the Wanted screen: on or off, and the
// budget.
func (h *Handlers) automaticSettingsJSON() map[string]any {
	if h.acquisition == nil {
		return map[string]any{
			"enabled": false,
			"note": "Automatic acquisition is off: nothing here is searched for or downloaded " +
				"unless a person does it. It is turned on in the configuration " +
				"(acquisition.automatic), with the download engine running.",
		}
	}
	cfg := h.acquisition.Settings()
	return map[string]any{
		"enabled":              true,
		"recent_every_seconds": int64(cfg.RecentInterval / time.Second),
		"search_every_seconds": int64(cfg.SearchInterval / time.Second),
		"searches_per_run":     cfg.SearchesPerRun,
		"max_grabs_per_run":    cfg.MaxGrabsPerRun,
		"note": "Automatic acquisition is on. Every item here is looked for in the indexers' " +
			"recent releases, and searched for in turn, a few at a time; what the default " +
			"quality profile accepts is downloaded. Unmonitor anything you do not want fetched.",
	}
}

// automaticJSON says, for one wanted item, what automatic acquisition last did
// and what it will do next.
func automaticJSON(st acquire.State, known, downloading, searchable bool, now time.Time) map[string]any {
	out := map[string]any{}
	switch {
	case downloading:
		out["status"] = "downloading"
		out["note"] = "A download for it is under way; nothing more is fetched until it is " +
			"imported or removed from the queue."
	case !searchable:
		out["status"] = "not_searchable"
		out["note"] = "It has no year in the library, so no release can be told apart from " +
			"another film of the same name. Identify it and it will be searched for."
	case !known:
		out["status"] = "not_searched"
		out["note"] = "Not searched for yet; it is watched for in the recent releases meanwhile."
	default:
		out["status"] = st.Outcome
	}
	if known {
		out["searched_at"] = st.SearchedAt.UTC().Format(time.RFC3339)
		out["outcome"] = st.Outcome
		out["detail"] = st.Detail
		out["fruitless"] = st.Fruitless
		if !downloading && searchable && st.NextAt.After(now) {
			out["next_search_at"] = st.NextAt.UTC().Format(time.RFC3339)
		}
	}
	return out
}

// albumAutomaticJSON is what automatic acquisition last did about an album
// (ADR-0047), in the shape of an episode's or a film's.
func albumAutomaticJSON(a music.Album, rep *acquire.Report, now time.Time) map[string]any {
	st, known := rep.States[acquire.StateKey{Album: true, ID: a.ID}]
	downloading := rep.InFlight[acquire.Key{Album: a.ID}]
	imported := rep.AlbumsImported[a.ID]
	listed := a.TracksKnown && a.Known > 0
	out := automaticJSON(st, known, downloading, listed && !imported, now)
	switch {
	case downloading:
	case imported:
		out["status"] = "imported_once"
		out["note"] = "A download for it was imported and some of its tracks are still missing. " +
			"An album is fetched automatically once; search for it to choose another release."
	case !listed:
		out["status"] = "not_searchable"
		out["note"] = "Its track list has not been read from MusicBrainz yet, so what arrived " +
			"could not be filed. It is searched for once the list is read."
	case !known:
		out["note"] = "Not searched for yet."
	}
	return out
}

// bookAutomaticJSON is what automatic acquisition last did about a book
// (ADR-0050).
func bookAutomaticJSON(b importer.Item, rep *acquire.Report, now time.Time) map[string]any {
	st, known := rep.States[acquire.StateKey{Book: true, ID: b.ID}]
	downloading := rep.InFlight[acquire.Key{Book: true, ItemID: b.ID}]
	authored := strings.TrimSpace(b.Author) != ""
	out := automaticJSON(st, known, downloading, authored, now)
	switch {
	case downloading:
	case !authored:
		out["status"] = "not_searchable"
		out["note"] = "Its author is not known, so no release can be told apart from another " +
			"book of the same name."
	case !known:
		out["note"] = "Not searched for yet."
	}
	return out
}

// SetFilmMonitored says whether a film is wanted while it has no file
// (ADR-0030, decision 2). A film is added monitored; unmonitoring keeps it in
// the library and off the Wanted list, and so out of automatic acquisition's
// reach. A series is monitored season by season and episode by episode.
func (h *Handlers) SetFilmMonitored(w http.ResponseWriter, r *http.Request) {
	if h.media == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req monitorRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	item, err := h.media.SetFilmMonitored(r.Context(), id, req.Monitored)
	switch {
	case errors.Is(err, importer.ErrItemNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, importer.ErrNotAFilm):
		writeProblem(w, http.StatusConflict, "that is a series: its seasons and episodes are "+
			"monitored one by one")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	note := "It is on the Wanted list while it has no file."
	if !req.Monitored {
		note = "It stays in the library and is off the Wanted list: nothing will search for it " +
			"or download it until it is monitored again."
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": item.ID, "name": filmName(item), "monitored": item.Monitored, "note": note,
	})
}

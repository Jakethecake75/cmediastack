package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jakethecake75/cmediastack/internal/acquire"
	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/tv"
)

// What a series contains, and what it is missing.
//
// The distinction this surface exists to express: an episode row is something a
// PROVIDER said exists, so "missing" means something. A list built from the
// files on disk would be complete by construction and would never report a gap
// (ADR-0022).

// EpisodeService is what the API needs to answer questions about a series.
type EpisodeService interface {
	// Seasons returns a series' seasons and their episodes, keyed by season
	// number, including which episodes this instance holds.
	Seasons(ctx context.Context, itemID int64) ([]library.Season, map[int][]library.Episode, error)
	// Wanted returns monitored episodes that have aired and are not held.
	Wanted(ctx context.Context, limit int) ([]library.WantedEpisode, error)
	// SetSeasonMonitored turns a season on or off, and its episodes with it.
	SetSeasonMonitored(ctx context.Context, itemID, seasonNumber int64, on bool) error
	// SetEpisodeMonitored turns one episode on or off.
	SetEpisodeMonitored(ctx context.Context, episodeID int64, on bool) error
	// SetSeriesFlag and SeriesSettings are a series' own switches: whether it
	// follows new seasons (ADR-0061) and files in season folders (ADR-0063).
	SetSeriesFlag(ctx context.Context, itemID int64, flag library.SeriesFlag, on bool) error
	SeriesSettings(ctx context.Context, itemID int64) (library.SeriesSettings, error)
	// ForSearch returns one episode and the series it belongs to.
	ForSearch(ctx context.Context, episodeID int64) (library.SearchSubject, error)
	// ForSeasonSearch returns one season of a series and its listed episodes.
	ForSeasonSearch(ctx context.Context, itemID int64, season int) (library.SeasonSearchSubject, error)
}

// EpisodeRefresher asks the provider what a series contains.
type EpisodeRefresher interface {
	Refresh(ctx context.Context, itemID int64) (tv.RefreshResult, error)
}

func episodeJSON(e library.Episode) map[string]any {
	row := map[string]any{
		"id":        e.ID,
		"season":    e.SeasonNumber,
		"number":    e.Number,
		"title":     e.Title,
		"monitored": e.Monitored,
		"have":      e.Have(),
	}
	if e.Overview != "" {
		row["overview"] = e.Overview
	}
	if e.RuntimeMinutes > 0 {
		row["runtime_minutes"] = e.RuntimeMinutes
	}
	switch {
	case e.Announced():
		// Said as its own state rather than as a null date. "Announced" and
		// "has not aired yet" are different facts, and the difference is
		// exactly why the wanted list is not full of unscheduled episodes.
		row["announced"] = true
	default:
		row["aired_at"] = e.Aired.UTC().Format(time.RFC3339)
	}
	if e.Have() {
		row["file_id"] = e.HaveFileID
		row["path"] = e.HavePath
	}
	return row
}

// SeriesChildren reports a series' seasons and episodes.
func (h *Handlers) SeriesChildren(w http.ResponseWriter, r *http.Request) {
	if h.episodes == nil {
		writeProblem(w, http.StatusNotImplemented, "episode tracking is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	seasons, byNumber, err := h.episodes.Seasons(r.Context(), id)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}

	rows := make([]map[string]any, 0, len(seasons))
	var totalHave, totalEpisodes int
	for _, s := range seasons {
		eps := byNumber[s.Number]
		episodes := make([]map[string]any, 0, len(eps))
		have := 0
		for _, e := range eps {
			if e.Have() {
				have++
			}
			episodes = append(episodes, episodeJSON(e))
		}
		totalHave += have
		totalEpisodes += len(eps)

		row := map[string]any{
			"number":    s.Number,
			"name":      s.Name,
			"monitored": s.Monitored,
			"episodes":  episodes,
			// Both numbers, because they are different questions: "how many
			// episodes do I have of the ones I know about" and "how many does
			// the provider say exist". A season whose refresh failed part-way
			// has fewer rows than the count, and hiding that would report a
			// completion figure that is quietly measured against the wrong
			// denominator.
			"have":          have,
			"known":         len(eps),
			"episode_count": s.EpisodeCount,
		}
		if !s.Aired.IsZero() {
			row["aired_at"] = s.Aired.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}

	body := map[string]any{
		"seasons": rows,
		"have":    totalHave,
		"known":   totalEpisodes,
	}
	if set, serr := h.episodes.SeriesSettings(r.Context(), id); serr == nil {
		body["follow_new_seasons"] = set.FollowNewSeasons
		body["season_folders"] = set.SeasonFolders
		body["daily"] = set.Daily
	}
	if totalEpisodes == 0 {
		// An empty answer that explains itself. Without this an operator
		// looking at a series with no episodes cannot tell "this is not a
		// series", "it has not been identified" and "nobody has refreshed it
		// yet" apart — and the fix is different for each.
		body["note"] = "Nothing is known about this series' episodes yet. " +
			"Identify it, then refresh its episode list."
	}
	writeJSON(w, http.StatusOK, body)
}

// Wanted lists what this instance should have and does not: every film in the
// library with no file (ADR-0026), and every monitored episode that has aired
// and is not on disk (ADR-0022).
func (h *Handlers) Wanted(w http.ResponseWriter, r *http.Request) {
	if h.episodes == nil {
		writeProblem(w, http.StatusNotImplemented, "episode tracking is not wired")
		return
	}
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}

	wanted, err := h.episodes.Wanted(r.Context(), limit)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}

	// What automatic acquisition last did about each item (ADR-0030), when it
	// is on. A report that cannot be read leaves the list as it is and says
	// so, rather than failing the screen that shows what is missing.
	var report *acquire.Report
	automatic := h.automaticSettingsJSON()
	if h.acquisition != nil {
		rep, rerr := h.acquisition.Report(r.Context())
		if rerr != nil {
			if authz.IsDenied(rerr) {
				writeAuthzAware(w, rerr)
				return
			}
			automatic["error"] = "what automatic acquisition has done could not be read: " + rerr.Error()
		} else {
			report = &rep
		}
	}
	now := time.Now()

	rows := make([]map[string]any, 0, len(wanted))
	for _, want := range wanted {
		row := episodeJSON(want.Episode)
		row["item_id"] = want.ItemID
		row["series"] = want.SeriesTitle
		if report != nil {
			st, known := report.States[acquire.StateKey{ID: want.ID}]
			flying := report.InFlight[acquire.Key{ItemID: want.ItemID,
				Season: want.SeasonNumber, Episode: want.Number}]
			row["automatic"] = automaticJSON(st, known, flying, true, now)
		}
		rows = append(rows, row)
	}

	films := make([]map[string]any, 0)
	if h.media != nil {
		items, err := h.media.WantedFilms(r.Context(), limit)
		if err != nil {
			writeAuthzAware(w, err)
			return
		}
		for _, it := range items {
			row := map[string]any{"item_id": it.ID, "title": it.Title, "name": filmName(it),
				"added_at": it.AddedAt.UTC().Format(time.RFC3339)}
			if it.Year > 0 {
				row["year"] = it.Year
			}
			if report != nil {
				st, known := report.States[acquire.StateKey{Film: true, ID: it.ID}]
				flying := report.InFlight[acquire.Key{Film: true, ItemID: it.ID}]
				row["automatic"] = automaticJSON(st, known, flying, it.Year > 0, now)
			}
			films = append(films, row)
		}
	}

	albums := make([]map[string]any, 0)
	if h.music != nil {
		list, err := h.music.Store().WantedAlbums(r.Context(), limit)
		if err != nil {
			writeAuthzAware(w, err)
			return
		}
		for _, a := range list {
			row := albumJSON(a)
			if report != nil {
				row["automatic"] = albumAutomaticJSON(a, report, now)
			}
			albums = append(albums, row)
		}
	}

	books := make([]map[string]any, 0)
	if h.books != nil {
		list, err := h.books.Wanted(r.Context(), limit)
		if err != nil {
			writeAuthzAware(w, err)
			return
		}
		for _, b := range list {
			row := map[string]any{"item_id": b.ID, "title": b.Title, "author": b.Author,
				"year": b.Year, "name": bookName(b), "added_at": b.AddedAt.UTC().Format(time.RFC3339)}
			if report != nil {
				row["automatic"] = bookAutomaticJSON(b, report, now)
			}
			books = append(books, row)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"wanted":      rows,
		"count":       len(rows),
		"films":       films,
		"film_count":  len(films),
		"albums":      albums,
		"album_count": len(albums),
		"books":       books,
		"book_count":  len(books),
		"automatic":   automatic,
		"note": "Monitored films in the library with no file, and monitored episodes that " +
			"have aired and are not on disk. A film is wanted from the moment it is added, " +
			"released or not, until it is unmonitored. Episodes the provider lists with no " +
			"air date are announced, not overdue, and are not here.",
	})
}

type monitorRequest struct {
	Monitored bool `json:"monitored"`
}

// SetSeasonMonitored turns a season on or off.
func (h *Handlers) SetSeasonMonitored(w http.ResponseWriter, r *http.Request) {
	if h.episodes == nil {
		writeProblem(w, http.StatusNotImplemented, "episode tracking is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	season, err := strconv.ParseInt(r.PathValue("season"), 10, 64)
	if err != nil || season < 0 {
		writeProblem(w, http.StatusBadRequest, "that is not a season number")
		return
	}
	var req monitorRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if err := h.episodes.SetSeasonMonitored(r.Context(), id, season, req.Monitored); err != nil {
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"season":    season,
		"monitored": req.Monitored,
		"note": "Every episode in the season was set to match. A season and its " +
			"episodes disagreeing would be resolved in the episodes' favour by " +
			"the wanted list, which is the opposite of what you just asked for.",
	})
}

// SetFollowNewSeasons says whether a series takes on its new seasons
// (ADR-0061).
func (h *Handlers) SetFollowNewSeasons(w http.ResponseWriter, r *http.Request) {
	h.setSeriesFlag(w, r, library.FollowNewSeasons, "follow", "follow_new_seasons",
		"A season the provider lists for the first time will start monitored.",
		"A season the provider lists for the first time will start unmonitored. "+
			"The seasons already here keep what they are set to.")
}

// SetSeasonFolders says whether a series files its episodes in season
// folders (ADR-0063).
func (h *Handlers) SetSeasonFolders(w http.ResponseWriter, r *http.Request) {
	h.setSeriesFlag(w, r, library.SeasonFolders, "season_folders", "season_folders",
		"Episodes imported from now on are filed in their season's folder.",
		"Episodes imported from now on are filed in the series' own folder. No file already "+
			"here is moved.")
}

// SetDaily says whether a series is released, and searched for, by date
// (ADR-0064).
func (h *Handlers) SetDaily(w http.ResponseWriter, r *http.Request) {
	h.setSeriesFlag(w, r, library.Daily, "daily", "daily",
		"Its episodes are searched for by the day they aired.",
		"Its episodes are searched for by season and number. A release named by date is "+
			"still taken when it is the day an episode aired.")
}

// setSeriesFlag sets one of a series' switches from {field: true|false}.
func (h *Handlers) setSeriesFlag(w http.ResponseWriter, r *http.Request, flag library.SeriesFlag,
	field, answer, onNote, offNote string) {
	if h.episodes == nil {
		writeProblem(w, http.StatusNotImplemented, "episode tracking is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req map[string]*bool
	if !decodeJSON(w, r, &req) {
		return
	}
	on := req[field]
	if on == nil {
		writeProblem(w, http.StatusBadRequest, field+" is true or false")
		return
	}
	if err := h.episodes.SetSeriesFlag(r.Context(), id, flag, *on); err != nil {
		writeAuthzAware(w, err)
		return
	}
	note := onNote
	if !*on {
		note = offNote
	}
	writeJSON(w, http.StatusOK, map[string]any{answer: *on, "note": note})
}

// SetEpisodeMonitored turns one episode on or off.
func (h *Handlers) SetEpisodeMonitored(w http.ResponseWriter, r *http.Request) {
	if h.episodes == nil {
		writeProblem(w, http.StatusNotImplemented, "episode tracking is not wired")
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
	if err := h.episodes.SetEpisodeMonitored(r.Context(), id, req.Monitored); err != nil {
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "monitored": req.Monitored})
}

// RefreshEpisodes asks the provider what a series contains.
func (h *Handlers) RefreshEpisodes(w http.ResponseWriter, r *http.Request) {
	if h.refresher == nil {
		writeProblem(w, http.StatusNotImplemented, "episode tracking is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	res, err := h.refresher.Refresh(r.Context(), id)
	switch {
	case err == nil:
	case errors.Is(err, metadata.ErrNoProvider):
		writeProblem(w, http.StatusConflict,
			"no metadata provider is configured; set one on the Metadata screen")
		return
	case errors.Is(err, metadata.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		// Which of the two it was, rather than a promise that covers both. A
		// limit met on the first request recorded nothing; one met part-way
		// recorded the seasons read before it, and says which.
		detail := "the provider is rate-limiting this instance, so nothing was read"
		if res.ItemID != 0 {
			detail = "the provider started rate-limiting part-way; what was read " +
				"before it did is recorded — " + res.Summary()
		}
		writeProblem(w, http.StatusServiceUnavailable, detail)
		return
	case errors.Is(err, library.ErrNotASeries):
		// 409 rather than 400: the request is well formed and the item is real.
		// What is wrong is that a film has no episodes.
		writeProblem(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, tv.ErrNotIdentified):
		writeProblem(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, metadata.ErrNotFound):
		// The item's identification names something the provider no longer
		// has — an id TMDB merged or removed. The operator's to fix, by
		// identifying the series again; not a fault in this instance.
		writeProblem(w, http.StatusConflict, "the provider has no series with this "+
			"item's id, so nothing was read; identify it again: "+err.Error())
		return
	case isProviderFailure(err):
		// Somebody else's failure, said as one. These were a 500 "internal
		// error" before, which sent an operator looking at this instance for a
		// fault in a third party's service.
		writeProblem(w, http.StatusBadGateway, "the provider could not be read: "+err.Error())
		return
	default:
		writeAuthzAware(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"item_id":         res.ItemID,
		"title":           res.Title,
		"seasons":         res.Seasons,
		"episodes":        res.Episodes,
		"seasons_read":    res.SeasonsRead,
		"seasons_skipped": res.SeasonsSkipped,
		// A 200 with seasons it could not read. Not a failure of the request —
		// what could be read was — but not something to report as a clean
		// success either, which is why it is a field a client can test.
		"seasons_failed": res.SeasonsFailed,
		"summary":        res.Summary(),
	})
}

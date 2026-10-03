package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jakethecake75/cmediastack/internal/migrate"
)

// Migrating from another application.
//
// One route, because the plan and the application of it are one call: a plan is
// built from the library as it is now, and handing item ids back to a second
// request would be acting on a stale description of something that may have
// changed in between (ADR-0021).

// MigrateService is what the API needs to run a migration.
type MigrateService interface {
	// Sources lists the files an operator has placed in the migration
	// directory, and says where that directory is.
	Sources() (dir string, files []string, err error)
	// RunRadarr opens the named source and migrates from it.
	RunRadarr(ctx context.Context, name string, opt migrate.Options) (migrate.Plan, error)
}

// MigrationSources reports what can be migrated from.
//
// The directory path is in the response because the first question an operator
// has is "where do I put the file", and an empty list with no answer to that is
// an unhelpful thing to return.
func (h *Handlers) MigrationSources(w http.ResponseWriter, r *http.Request) {
	if h.migrate == nil {
		writeProblem(w, http.StatusNotImplemented, "migration is not wired")
		return
	}
	dir, files, err := h.migrate.Sources()
	switch {
	case errors.Is(err, migrate.ErrNotConfigured):
		writeProblem(w, http.StatusNotImplemented,
			"this instance has no migration directory")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	if files == nil {
		files = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"directory": dir,
		"files":     files,
	})
}

type migrateRequest struct {
	// Source is a FILENAME in the migration directory. Never a path: the
	// service refuses anything with a separator in it and resolves the rest
	// through os.Root.
	Source string `json:"source"`
	// Apply inverts the default. Absent means a dry run, which is the whole
	// point of the default — an operation that touches hundreds of rows and
	// cannot be previewed is one nobody should run.
	Apply bool `json:"apply"`
	// AdoptTitles takes Radarr's labels as well as its ids. Needs media.edit.
	AdoptTitles bool `json:"adopt_titles"`
	// Overwrite replaces identifiers this library already holds.
	Overwrite bool `json:"overwrite"`
}

// MigrateFromRadarr reads a Radarr database and attaches the identities it
// holds to the matching library items.
func (h *Handlers) MigrateFromRadarr(w http.ResponseWriter, r *http.Request) {
	if h.migrate == nil {
		writeProblem(w, http.StatusNotImplemented, "migration is not wired")
		return
	}
	var req migrateRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	plan, err := h.migrate.RunRadarr(r.Context(), req.Source, migrate.Options{
		DryRun:      !req.Apply,
		AdoptTitles: req.AdoptTitles,
		Overwrite:   req.Overwrite,
	})
	switch {
	case err == nil:
	case errors.Is(err, migrate.ErrNotConfigured):
		// 501, like every other unwired capability: nothing is wrong with the
		// request and nothing the operator does to it will help.
		writeProblem(w, http.StatusNotImplemented,
			"this instance has no migration directory")
		return
	case errors.Is(err, migrate.ErrBadSourceName), errors.Is(err, migrate.ErrNoSuchSource):
		writeProblem(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, migrate.ErrNotRadarr), errors.Is(err, migrate.ErrTooOld):
		// 422: the request named a real file and the file is not usable. The
		// message carries the version found and what to do about it, which is
		// the only actionable part.
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
		return
	case errors.Is(err, migrate.ErrNoLibrary):
		// 409: nothing is wrong with the request, the instance is not ready
		// for it. The operator has almost certainly skipped the scan.
		writeProblem(w, http.StatusConflict, err.Error())
		return
	default:
		writeAuthzAware(w, err)
		return
	}

	writeJSON(w, http.StatusOK, migrationJSON(plan))
}

func migrationJSON(p migrate.Plan) map[string]any {
	counts := p.Counts()
	matches := make([]map[string]any, 0, len(p.Matches))
	for _, m := range p.Matches {
		row := map[string]any{
			"item_id": m.ItemID,
			"folder":  m.Folder,
			"action":  string(m.Action),
			"tmdb_id": m.TMDBID,
			"title":   m.Title,
			"year":    m.Year,
		}
		if m.IMDbID != "" {
			row["imdb_id"] = m.IMDbID
		}
		if m.Action == migrate.ActionConflict {
			// Both ids, because the operator deciding whether to overwrite
			// needs to see what they would be replacing.
			row["current_tmdb_id"] = m.CurrentTMDB
		}
		if m.CaseInsensitive {
			row["case_insensitive"] = true
		}
		if m.TitleChanges() {
			row["current_title"] = m.CurrentTitle
		}
		matches = append(matches, row)
	}

	unmatched := make([]map[string]any, 0, len(p.Unmatched))
	for _, mv := range p.Unmatched {
		unmatched = append(unmatched, map[string]any{
			"folder": mv.Folder, "title": mv.Title,
			"year": mv.Year, "tmdb_id": mv.TmdbID,
		})
	}

	unknown := p.Unknown
	if unknown == nil {
		unknown = []string{}
	}
	ambiguous := p.Ambiguous
	if ambiguous == nil {
		ambiguous = []string{}
	}

	return map[string]any{
		"applied":          p.Applied,
		"adopted_titles":   p.AdoptedTitles,
		"source_version":   p.SourceVersion,
		"movies_read":      p.MoviesRead,
		"items_in_library": p.ItemsInLibrary,
		"summary":          p.Summary(),
		"counts": map[string]int{
			"attach":          counts[migrate.ActionAttach],
			"already_correct": counts[migrate.ActionAlreadyCorrect],
			"conflict":        counts[migrate.ActionConflict],
		},
		"matches": matches,
		// The three lists that make up the operator's worklist.
		"unmatched": unmatched,
		"unknown":   unknown,
		"ambiguous": ambiguous,
	}
}

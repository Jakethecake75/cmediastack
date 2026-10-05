package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/playback"
)

// MediaService is the subset of the library the API reads.
//
// Read-only, deliberately. Nothing here can move, rename or delete a file:
// those are effects, they are checked in the filesystem layer, and an interface
// that could reach them would make this handler the place to look for
// authorization rather than the vault.
type MediaService interface {
	ListItems(ctx context.Context, kind string) ([]importer.Item, error)
	GetItem(ctx context.Context, id int64) (importer.Item, error)
	// ItemForQueue reads an item a download is for, whoever may see it
	// (ADR-0037): the queue names what it is downloading.
	ItemForQueue(ctx context.Context, id int64) (importer.Item, error)
	FilesFor(ctx context.Context, itemID int64) ([]importer.File, error)
	// FileForPlayback reads one file the caller may see (ADR-0077 asks it
	// before minting a cast link).
	FileForPlayback(ctx context.Context, fileID int64) (playback.FileRef, error)
	RecordsFor(ctx context.Context, infoHash string) ([]importer.Record, error)
	// Film returns one film to search for, or ErrNotAFilm (ADR-0026).
	Film(ctx context.Context, id int64) (importer.Item, error)
	// WantedFilms returns the monitored films with no file (ADR-0026,
	// ADR-0030).
	WantedFilms(ctx context.Context, limit int) ([]importer.Item, error)
	// SetFilmMonitored says whether a film is wanted while it has no file
	// (ADR-0030). It is the one write here, and it moves nothing: it decides
	// whether the film is on the Wanted list. Gated on editing the library.
	SetFilmMonitored(ctx context.Context, id int64, on bool) (importer.Item, error)
	// SetQualityProfile names the profile a title is judged by, 0 for the
	// default (ADR-0035). Gated on editing the library, as monitoring is.
	SetQualityProfile(ctx context.Context, id, profileID int64) (importer.Item, error)
	// SetRating is a person rating a title, or with "" giving it back to the
	// provider (ADR-0037). It returns the item before and after.
	SetRating(ctx context.Context, id int64, certification string) (importer.Item, importer.Item, error)
	// HoldsProviderID reports whether a title the caller may see carries the
	// provider id — whether its poster is theirs to see (ADR-0037).
	HoldsProviderID(ctx context.Context, tmdbID int64) (bool, error)
	// HasTitle reports whether a title of this kind with this provider id is
	// one the caller may see (ADR-0043).
	HasTitle(ctx context.Context, kind string, tmdbID int64) (bool, error)
}

// LibraryScanner reconciles the database with what is on disk.
//
// Separate from MediaService because it is a different authority: reading the
// library is browsing, asking the instance to walk an operator's disks is not.
type LibraryScanner interface {
	Scan(ctx context.Context, rootID int64) (importer.ScanResult, error)
}

func itemJSON(it importer.Item) map[string]any {
	out := map[string]any{
		"id":       it.ID,
		"kind":     it.Kind,
		"title":    it.Title,
		"folder":   it.Folder,
		"root_id":  it.RootFolderID,
		"added_at": it.AddedAt,
	}
	if it.Year > 0 {
		out["year"] = it.Year
	}
	// A film is monitored or not (ADR-0030); a series is monitored season by
	// season, and the column means nothing on one, so it is not said.
	if it.Kind == importer.KindMovie || it.Kind == importer.KindBook {
		out["monitored"] = it.Monitored
	}
	// A book's author and work (ADR-0048).
	if it.Author != "" {
		out["author"] = it.Author
	}
	if it.OpenLibraryID != "" {
		out["openlibrary_id"] = it.OpenLibraryID
	}
	// Its own quality profile, when it names one; absent is the default
	// (ADR-0035).
	if it.QualityProfileID > 0 {
		out["quality_profile_id"] = it.QualityProfileID
	}
	// Its rating, which decides who may see it (ADR-0037) — a film's or a
	// series'. Music and books carry none and no ceiling hides them (ADR-0044),
	// so they are not said to be "unrated: hidden".
	if it.Kind == importer.KindMovie || it.Kind == importer.KindSeries {
		out["rating"] = ratingJSON(it)
	}
	// Identification, when there has been any. Absent rather than zero: a
	// caller should be able to tell "not identified" from "identified as 0",
	// and a page that shows a badge for identified items should not have to
	// know that 0 is the sentinel.
	if it.TMDBID > 0 {
		out["tmdb_id"] = it.TMDBID
		// The URL rather than the ingredients. A page that has to assemble
		// this from a provider name and an id is a page that has to be
		// changed when the route changes, and — worse — one that could be
		// tempted to assemble a provider URL instead, which is the thing
		// ADR-0018 serves artwork locally to prevent.
		out["poster"] = artworkPath(it.ID)
	}
	if it.IMDbID != "" {
		out["imdb_id"] = it.IMDbID
	}
	return out
}

// artworkPath is the one place that knows the shape of a title's artwork
// route (ADR-0038).
func artworkPath(itemID int64) string {
	return fmt.Sprintf("/api/v1/media/%d/artwork", itemID)
}

func fileJSON(f importer.File) map[string]any {
	out := map[string]any{
		"id":       f.ID,
		"path":     f.RelPath,
		"bytes":    f.SizeBytes,
		"gib":      float64(f.SizeBytes) / (1 << 30),
		"quality":  f.Quality,
		"revision": f.Revision,
		"group":    f.ReleaseGroup,
		// Whether the bytes are shared with a still-seeding download. An
		// operator deleting a file needs to know the same inode is being served
		// to strangers, and that unlinking here does not stop that.
		"hardlinked":  f.Hardlinked,
		"imported_at": f.ImportedAt,
	}
	if f.Season != nil {
		out["season"] = *f.Season
	}
	if f.Episode != nil {
		out["episode"] = *f.Episode
		if f.EpisodeLast != nil && *f.EpisodeLast != *f.Episode {
			out["episode_last"] = *f.EpisodeLast
		}
	}
	// The release title is provenance an operator needs when something is wrong
	// with a file. It is not a secret: it is a public release name.
	if f.ReleaseTitle != "" {
		out["release"] = f.ReleaseTitle
	}
	return out
}

// ListMedia returns the library.
func (h *Handlers) ListMedia(w http.ResponseWriter, r *http.Request) {
	if h.media == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}

	// Validated rather than passed through: an unknown kind should be a named
	// refusal, not an empty list that reads as "you have no films".
	kind := r.URL.Query().Get("kind")
	switch kind {
	case "", importer.KindMovie, importer.KindSeries, "artist", "book":
	default:
		writeProblem(w, http.StatusBadRequest,
			"kind must be 'movie', 'series', 'artist', 'book', or absent for everything")
		return
	}

	items, err := h.media.ListItems(r.Context(), kind)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, itemJSON(it))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "count": len(out)})
}

// GetMedia returns one library item with its files.
func (h *Handlers) GetMedia(w http.ResponseWriter, r *http.Request) {
	if h.media == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "not an id")
		return
	}

	// The permission check lives in ListItems, which is the method that reads
	// the library. Get is reached only after it, so it is checked here too
	// rather than trusting the route's own gate.
	if _, err := h.media.ListItems(r.Context(), ""); err != nil {
		writeAuthzAware(w, err)
		return
	}

	item, err := h.media.GetItem(r.Context(), id)
	switch {
	case errors.Is(err, importer.ErrItemNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	files, err := h.media.FilesFor(r.Context(), id)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	out := make([]map[string]any, 0, len(files))
	for _, f := range files {
		out = append(out, fileJSON(f))
	}

	body := itemJSON(item)
	body["files"] = out
	writeJSON(w, http.StatusOK, body)
}

// ScanRootFolder walks a root folder and records what it finds.
//
// Synchronous, deliberately. A scan of a large library takes a while, and an
// operator who asked for one wants to know what it found — an endpoint that
// returned 202 and a job id would need a job system, and a scan is not
// dangerous enough to need one: it changes nothing on disk.
//
// The request carries no paths and no options. What gets scanned is the root
// folder the operator already configured, by id, so this endpoint cannot be
// asked to walk somewhere it was never pointed at.
func (h *Handlers) ScanRootFolder(w http.ResponseWriter, r *http.Request) {
	if h.scanner == nil {
		writeProblem(w, http.StatusNotImplemented, "no library scanner is wired")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "not an id")
		return
	}

	res, err := h.scanner.Scan(r.Context(), id)
	switch {
	case errors.Is(err, library.ErrRootNotFound):
		writeProblem(w, http.StatusNotFound, "no such root folder")
		return
	case errors.Is(err, importer.ErrLibraryVanished):
		// 409, not 500: nothing is broken. The instance is refusing to act on
		// a library that appears to have vanished, which is almost always an
		// unmounted disk. The whole message goes back, because the operator
		// needs to read it rather than a status code.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   err.Error(),
			"scanned": res.Scanned,
			"missing": len(res.Missing),
			"note": "Nothing was changed. Check that the disk holding this root " +
				"folder is mounted, then scan again.",
		})
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	body := map[string]any{
		"root":       res.Root,
		"scanned":    res.Scanned,
		"added":      res.Added,
		"updated":    res.Updated,
		"missing":    res.Missing,
		"elapsed_ms": res.Elapsed.Milliseconds(),
		"summary":    res.Summary(),
		"note": "A scan never changes anything on disk. Files stay where they " +
			"are, under the names they have.",
	}
	// Bounded: a library with ten thousand oddities must not produce a response
	// nobody can read. The count is always honest even when the list is cut.
	const maxSkipped = 200
	skipped := make([]map[string]any, 0, len(res.Skipped))
	for i, s := range res.Skipped {
		if i >= maxSkipped {
			body["skipped_truncated"] = len(res.Skipped) - maxSkipped
			break
		}
		skipped = append(skipped, map[string]any{
			"path": s.Path, "bytes": s.Bytes, "reason": s.Reason,
		})
	}
	body["skipped"] = skipped
	body["skipped_count"] = len(res.Skipped)
	if res.Truncated {
		body["warning"] = fmt.Sprintf("The scan stopped at the %d-file cap, so this "+
			"is a partial result. Check that this root folder points where you think.",
			importer.MaxScanFiles)
	}

	h.auditScan(r, id, res)
	writeJSON(w, http.StatusOK, body)
}

// auditScan records that somebody asked the instance to walk a disk.
func (h *Handlers) auditScan(r *http.Request, rootID int64, res importer.ScanResult) {
	p := authz.FromContext(r.Context())
	if h.audit == nil || p == nil {
		return
	}
	_ = h.audit.Write(r.Context(), audit.Event{
		ActorUserID: &p.UserID,
		ActorLabel:  p.Username,
		Action:      audit.ActionLibraryScanned,
		Outcome:     audit.OutcomeSuccess,
		TargetKind:  "root_folder",
		TargetID:    strconv.FormatInt(rootID, 10),
		SourceIP:    ClientIP(r.Context()),
		UserAgent:   r.UserAgent(),
		Detail:      res.Root + ": " + res.Summary(),
	})
}

// ImportHistory says what happened to a completed download.
//
// This is the answer to "it downloaded and then nothing happened", which is the
// complaint this category of software earns. It is reachable from the queue, on
// the same permission that manages it, because that is where an operator is
// standing when they ask.
func (h *Handlers) ImportHistory(w http.ResponseWriter, r *http.Request) {
	if h.media == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	hash := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	if !download.IsInfoHash(hash) {
		writeProblem(w, http.StatusBadRequest, "not an info hash")
		return
	}

	records, err := h.media.RecordsFor(r.Context(), hash)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	out := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		out = append(out, map[string]any{
			"outcome":     rec.Outcome,
			"detail":      rec.Detail,
			"source":      rec.SourcePath,
			"occurred_at": rec.OccurredAt,
		})
	}
	body := map[string]any{"info_hash": hash, "history": out, "count": len(out)}
	if len(out) == 0 {
		body["note"] = "Nothing has tried to import this yet. A download is imported " +
			"once it finishes and the library.import task next runs."
	}
	writeJSON(w, http.StatusOK, body)
}

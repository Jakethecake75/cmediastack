package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/follow"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/request"
)

// What an approval does (ADR-0075): a request for a title the provider named
// is added to the library and searched for at once; a removal sends its files
// to the trash.

// addForRequest adds an approved request's title to the library, monitors what
// it asked for, links the request to it and starts the search. It returns the
// sentence the approver is shown; a failure is said there, and the request
// stays approved for somebody to add by hand.
func (h *Handlers) addForRequest(ctx context.Context, rq *request.Request, rootID int64) string {
	if rq.TMDBID <= 0 {
		return "Approved. It was asked for in words, so add it to the library from this request: " +
			"it is then searched for, and fulfilled when a file of it arrives."
	}
	if h.adder == nil {
		return "Approved, but adding to the library is not wired here."
	}
	monitor := ""
	if rq.Kind == request.KindSeries {
		monitor = "all"
		if rq.Scope != "" {
			monitor = "none"
		}
	}
	res, err := h.adder.Add(ctx, follow.Request{
		Kind: string(rq.Kind), TMDBID: rq.TMDBID, RootFolderID: rootID, Monitor: monitor,
	})
	item, existed := res.Item, false
	var conflict *importer.ConflictError
	switch {
	case err == nil:
	case errors.As(err, &conflict) && errors.Is(err, importer.ErrAlreadyInLibrary):
		item, existed = conflict.Existing, true
	case errors.Is(err, follow.ErrChooseRootFolder):
		return "Approved, but it was not added: more than one root folder could hold it. " +
			"Choose one beside Approve, or add it from this request."
	default:
		return "Approved, but it could not be added to the library: " + err.Error()
	}

	said := "Approved and added to the library."
	if existed {
		said = "Approved. It was already in the library."
	}
	if rq.Kind == request.KindSeries && (rq.Scope != "" || existed) {
		parts, _ := request.ParseScope(rq.Scope)
		if err := h.monitorParts(ctx, item.ID, parts, true); err != nil {
			said += " What was asked for could not be marked wanted: " + err.Error() + "."
		}
	}
	if _, err := h.requests.Link(ctx, rq.ID, item.ID); err != nil {
		said += " The request could not be linked to it: " + err.Error() + "."
	}
	if h.startSearch("search") {
		said += " Searching the indexers for it now; Downloads shows what is grabbed."
	}
	return said
}

// monitorParts turns the named seasons and episodes of a series on or off.
// No parts means every regular season: the whole series.
func (h *Handlers) monitorParts(ctx context.Context, itemID int64, parts []request.Part, on bool) error {
	if h.episodes == nil {
		return errors.New("episode tracking is not wired")
	}
	seasons, episodes, err := h.episodes.Seasons(ctx, itemID)
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		for _, s := range seasons {
			if s.Number > 0 {
				parts = append(parts, request.Part{Season: s.Number})
			}
		}
	}
	var missing []string
	for _, p := range parts {
		if p.Episode == 0 {
			if err := h.episodes.SetSeasonMonitored(ctx, itemID, int64(p.Season), on); err != nil {
				missing = append(missing, "season "+strconv.Itoa(p.Season))
			}
			continue
		}
		found := false
		for _, e := range episodes[p.Season] {
			if e.Number == p.Episode {
				found = true
				if err := h.episodes.SetEpisodeMonitored(ctx, e.ID, on); err != nil {
					return err
				}
			}
		}
		if !found {
			missing = append(missing, fmt.Sprintf("S%dE%d", p.Season, p.Episode))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the provider does not list %s", strings.Join(missing, ", "))
	}
	return nil
}

// removeForRequest carries out an approved removal: the whole title, or the
// files of the seasons and episodes named, to the trash. Those seasons and
// episodes are no longer wanted, so nothing fetches them again.
func (h *Handlers) removeForRequest(r *http.Request, rq *request.Request) (string, error) {
	ctx := r.Context()
	if h.deleter == nil || h.media == nil {
		return "", errors.New("no library is wired")
	}
	if rq.MediaItemID == nil {
		return "It is no longer in the library; there was nothing to remove.",
			h.requests.CompleteRemoval(ctx, rq.ID, rq.Title+": already gone")
	}
	id := *rq.MediaItemID
	parts, err := request.ParseScope(rq.Scope)
	if err != nil {
		return "", err
	}

	if len(parts) == 0 {
		res, err := h.deleter.DeleteItem(ctx, id)
		switch {
		case errors.Is(err, importer.ErrItemNotFound):
			return "It is no longer in the library; there was nothing to remove.",
				h.requests.CompleteRemoval(ctx, rq.ID, rq.Title+": already gone")
		case err != nil:
			return "", err
		}
		h.auditDelete(r, res)
		stopped := h.stopDownloadsFor(r, id)
		detail := fmt.Sprintf("%s: %d file(s) to the trash, %d download(s) stopped", rq.Title, len(res.Trashed), stopped)
		return "Removed: " + rq.Title + " is out of the library and its files are in the trash, " +
			"recoverable until they are purged.", h.requests.CompleteRemoval(ctx, rq.ID, detail)
	}

	files, err := h.media.FilesFor(ctx, id)
	if err != nil {
		return "", err
	}
	trashed := 0
	for _, f := range files {
		if !fileInParts(f, parts) {
			continue
		}
		if _, err := h.deleter.DeleteFile(ctx, f.ID); err != nil && !errors.Is(err, importer.ErrItemNotFound) {
			return "", err
		}
		h.auditMedia(r, audit.ActionMediaFileDeleted, "media_file", f.ID,
			rq.Title+": "+f.RelPath+" moved to trash for a removal request")
		trashed++
	}
	// Not wanted any more, or acquisition would fetch them straight back.
	// Best effort: the files are gone either way.
	_ = h.monitorParts(ctx, id, parts, false)
	detail := fmt.Sprintf("%s %s: %d file(s) to the trash", rq.Title, rq.Scope, trashed)
	return fmt.Sprintf("Removed: %d file(s) of %s are in the trash, recoverable until they are purged, "+
		"and no longer wanted.", trashed, rq.Title), h.requests.CompleteRemoval(ctx, rq.ID, detail)
}

// fileInParts reports whether a file holds an episode the parts name.
func fileInParts(f importer.File, parts []request.Part) bool {
	if f.Season == nil || f.Episode == nil {
		return false
	}
	last := *f.Episode
	if f.EpisodeLast != nil && *f.EpisodeLast > last {
		last = *f.EpisodeLast
	}
	for _, p := range parts {
		if p.Season != *f.Season {
			continue
		}
		if p.Episode == 0 || (p.Episode >= *f.Episode && p.Episode <= last) {
			return true
		}
	}
	return false
}

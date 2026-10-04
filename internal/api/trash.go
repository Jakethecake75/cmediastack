package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// MediaDeleter is the destructive half of the library.
//
// A separate interface from MediaService so that "can this handler delete
// media?" is answered by which dependency it holds, rather than by reading the
// method it happened to call. The read-only handlers do not have this.
type MediaDeleter interface {
	DeleteItem(ctx context.Context, itemID int64) (importer.DeleteResult, error)
	ListTrash(ctx context.Context, retention time.Duration) ([]importer.TrashItem, error)
	Restore(ctx context.Context, rootID int64, trashPath string) (string, error)
	// DeleteFile and PurgeOne are ADR-0053's: one file to the trash, and one
	// trashed file unlinked now.
	DeleteFile(ctx context.Context, fileID int64) (importer.FileDeleteResult, error)
	PurgeOne(ctx context.Context, rootID int64, trashPath string) (int64, error)
}

// DeleteFile moves one file to its root's trash; the title stays, and what
// the file held is wanted again (ADR-0053, decision 1). A file the caller may
// not see answers as a missing one.
func (h *Handlers) DeleteFile(w http.ResponseWriter, r *http.Request) {
	if h.deleter == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	res, err := h.deleter.DeleteFile(r.Context(), id)
	switch {
	case errors.Is(err, importer.ErrItemNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	detail := res.Item.Title + ": " + res.File.RelPath + " moved to trash, nothing unlinked"
	body := map[string]any{"deleted": res.File.RelPath, "item_id": res.Item.ID,
		"note": "The file was moved to its root folder's trash and is recoverable until it is " +
			"purged. The title stays; what the file held is missing again."}
	if res.Trashed == "" {
		detail = res.Item.Title + ": " + res.File.RelPath + " was already gone from disk; its record was removed"
		body["warning"] = "The file was already missing from disk. Its record has been removed; there was nothing to trash."
	}
	h.auditMedia(r, audit.ActionMediaFileDeleted, "media_file", id, detail)
	writeJSON(w, http.StatusOK, body)
}

// PurgeTrash unlinks one trashed file now, whatever its retention (ADR-0053,
// decision 2). It is the one act here that cannot be undone, so it names one
// file, from the trash, and nothing else.
func (h *Handlers) PurgeTrash(w http.ResponseWriter, r *http.Request) {
	if h.deleter == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	var in restoreRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if !strings.HasPrefix(in.Path, library.TrashDir+"/") {
		writeProblem(w, http.StatusBadRequest, "that is not a path in the trash")
		return
	}
	freed, err := h.deleter.PurgeOne(r.Context(), in.RootID, in.Path)
	switch {
	case errors.Is(err, importer.ErrNotInTrash):
		writeProblem(w, http.StatusNotFound, "no such file in the trash")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	h.auditMedia(r, audit.ActionMediaPurged, "root_folder", in.RootID,
		in.Path+" unlinked now, "+strconv.FormatInt(freed, 10)+" bytes freed")
	writeJSON(w, http.StatusOK, map[string]any{"purged": in.Path, "bytes": freed,
		"note": "The file is gone for good."})
}

// auditMedia writes one line for a person's change to the library's files.
func (h *Handlers) auditMedia(r *http.Request, action audit.Action, kind string, id int64, detail string) {
	p := authz.FromContext(r.Context())
	if h.audit == nil || p == nil {
		return
	}
	_ = h.audit.Write(r.Context(), audit.Event{
		ActorUserID: &p.UserID, ActorLabel: p.Username, Action: action, Outcome: audit.OutcomeSuccess,
		TargetKind: kind, TargetID: strconv.FormatInt(id, 10),
		SourceIP: ClientIP(r.Context()), UserAgent: r.UserAgent(), Detail: detail,
	})
}

// DeleteMedia removes an item from the library.
//
// It does not unlink anything. The files move to the root's trash folder and
// stay there for the retention window, so an operator who deleted the wrong row
// — the likeliest failure by a wide margin, and a media library is often the
// only copy — has a real undo rather than a dialog box they clicked through.
//
// There is deliberately no "and purge now" parameter. A checkbox that destroys
// data on a mis-click is how reversibility gets lost; emptying the trash is a
// separate, named act.
func (h *Handlers) DeleteMedia(w http.ResponseWriter, r *http.Request) {
	if h.deleter == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "not an id")
		return
	}

	res, err := h.deleter.DeleteItem(r.Context(), id)
	switch {
	case errors.Is(err, importer.ErrItemNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	h.auditDelete(r, res)

	body := map[string]any{
		"deleted":           res.Item.Title,
		"trashed":           len(res.Trashed),
		"downloads_stopped": h.stopDownloadsFor(r, id),
		"note": "The files were moved to this root folder's trash and are still " +
			"recoverable until they are purged. Nothing was unlinked.",
	}
	if len(res.Missing) > 0 {
		// Not an error, but the operator should know their library and their
		// disk had drifted.
		body["already_gone"] = res.Missing
		body["warning"] = "Some recorded files were already missing from disk. " +
			"Their records have been removed; there was nothing to trash."
	}
	writeJSON(w, http.StatusOK, body)
}

// ListTrash reports what is recoverable, and until when.
func (h *Handlers) ListTrash(w http.ResponseWriter, r *http.Request) {
	if h.deleter == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	items, err := h.deleter.ListTrash(r.Context(), h.trashRetention)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}

	out := make([]map[string]any, 0, len(items))
	var total int64
	for _, it := range items {
		total += it.Bytes
		out = append(out, map[string]any{
			"root_id":     it.RootID,
			"path":        it.Path,
			"name":        it.Name,
			"bytes":       it.Bytes,
			"trashed_at":  it.TrashedAt,
			"purge_after": it.PurgeAfter,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"count": len(out),
		"bytes": total,
		"gib":   float64(total) / (1 << 30),
		// Seconds as well as the duration string: "168h0m0s" makes an operator
		// do arithmetic to learn it means a week, and this is a number that is
		// read at a glance or not at all.
		"retention":         h.trashRetention.String(),
		"retention_seconds": int64(h.trashRetention.Seconds()),
		"note": "These files are recoverable until the time shown. After that a " +
			"scheduled purge unlinks them.",
	})
}

type restoreRequest struct {
	RootID int64  `json:"root_id"`
	Path   string `json:"path"`
}

// RestoreFromTrash brings a deleted file back into the library.
func (h *Handlers) RestoreFromTrash(w http.ResponseWriter, r *http.Request) {
	if h.deleter == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	var in restoreRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	// The path must name something inside the trash folder. Checked here so a
	// restore cannot be turned into "move any file in the library somewhere
	// else" by a caller who supplies a different path — the vault would contain
	// it, but containment is not the same as it being the right operation.
	if !strings.HasPrefix(in.Path, library.TrashDir+"/") {
		writeProblem(w, http.StatusBadRequest, "that is not a path in the trash")
		return
	}

	dest, err := h.deleter.Restore(r.Context(), in.RootID, in.Path)
	switch {
	case errors.Is(err, importer.ErrNothingToRestore):
		writeProblem(w, http.StatusNotFound, "no such file in the trash")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	h.auditRestore(r, in.Path, dest)
	writeJSON(w, http.StatusOK, map[string]any{
		"restored": dest,
		"note": "The file is back in the library. It will appear once the next " +
			"scan records it, or you can scan its root folder now.",
	})
}

// stopDownloadsFor stops every queued transfer aimed at a deleted title
// (ADR-0066): a stopped transfer is never imported, so nothing it grabbed can
// be filed against whatever title comes next. Its downloaded files are deleted
// with it (ADR-0070).
func (h *Handlers) stopDownloadsFor(r *http.Request, itemID int64) int {
	if h.downloads == nil {
		return 0
	}
	records, err := h.downloads.Records(r.Context())
	if err != nil {
		return 0
	}
	stopped := 0
	for _, rec := range records {
		if rec.Target == nil || rec.Target.ItemID != itemID {
			continue
		}
		if err := h.downloads.Remove(rec.InfoHash); err == nil {
			stopped++
		}
	}
	return stopped
}

func (h *Handlers) auditDelete(r *http.Request, res importer.DeleteResult) {
	p := authz.FromContext(r.Context())
	if h.audit == nil || p == nil {
		return
	}
	_ = h.audit.Write(r.Context(), audit.Event{
		ActorUserID: &p.UserID,
		ActorLabel:  p.Username,
		Action:      audit.ActionMediaDeleted,
		Outcome:     audit.OutcomeSuccess,
		TargetKind:  "media_item",
		TargetID:    strconv.FormatInt(res.Item.ID, 10),
		SourceIP:    ClientIP(r.Context()),
		UserAgent:   r.UserAgent(),
		Detail: res.Item.Title + ": " + strconv.Itoa(len(res.Trashed)) +
			" file(s) moved to trash, nothing unlinked",
	})
}

func (h *Handlers) auditRestore(r *http.Request, from, to string) {
	p := authz.FromContext(r.Context())
	if h.audit == nil || p == nil {
		return
	}
	_ = h.audit.Write(r.Context(), audit.Event{
		ActorUserID: &p.UserID,
		ActorLabel:  p.Username,
		Action:      audit.ActionMediaRestored,
		Outcome:     audit.OutcomeSuccess,
		TargetKind:  "media_file",
		TargetID:    to,
		SourceIP:    ClientIP(r.Context()),
		UserAgent:   r.UserAgent(),
		Detail:      "restored from trash: " + from + " -> " + to,
	})
}

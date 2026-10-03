package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// RootFolderService is the subset of the library the API needs.
//
// Note what is NOT here: anything that opens a Vault. The admin surface
// configures WHERE the library lives; it does not touch files inside it. An
// endpoint that could obtain a Vault could write through it, and the whole
// point of holding a Vault being the capability is that it is not handed out
// casually.
type RootFolderService interface {
	List(ctx context.Context) ([]library.RootFolder, error)
	Get(ctx context.Context, id int64) (library.RootFolder, error)
	Create(ctx context.Context, path, kind, label string) (library.RootFolder, error)
	Delete(ctx context.Context, id int64) error
	Refresh(ctx context.Context, id int64) (library.RootFolder, error)
}

type rootFolderRequest struct {
	Path  string `json:"path"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

func rootFolderJSON(rf library.RootFolder) map[string]any {
	out := map[string]any{
		"id":    rf.ID,
		"path":  rf.Path,
		"kind":  rf.Kind,
		"label": rf.Label,
		// Reported in words as well as a boolean. An operator glancing at a
		// green tick should not have to know that "hardlinks: false" means
		// every import will quietly use twice the disk they budgeted for.
		"hardlinks":     rf.HardlinksOK,
		"hardlink_note": rf.HardlinkNote,
		"free_bytes":    rf.FreeBytes,
		"free_gib":      float64(rf.FreeBytes) / (1 << 30),
		"created_at":    rf.CreatedAt,
	}
	if rf.CheckedAt != nil {
		out["checked_at"] = rf.CheckedAt
	}
	return out
}

// ListRootFolders returns the configured library locations.
func (h *Handlers) ListRootFolders(w http.ResponseWriter, r *http.Request) {
	if h.roots == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	rows, err := h.roots.List(r.Context())
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, rf := range rows {
		out = append(out, rootFolderJSON(rf))
	}
	writeJSON(w, http.StatusOK, map[string]any{"root_folders": out, "count": len(out)})
}

// CreateRootFolder adds a library location after proving it is usable.
//
// Every check runs now, at configuration time, rather than during an import at
// three in the morning — see library.RootStore.Create for what each one
// prevents. The reasons come back in the response, because "that path was
// refused" without a reason is how an operator ends up disabling a check.
func (h *Handlers) CreateRootFolder(w http.ResponseWriter, r *http.Request) {
	if h.roots == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	var in rootFolderRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	rf, err := h.roots.Create(r.Context(), in.Path, in.Kind, in.Label)
	switch {
	case errors.Is(err, library.ErrRootExists):
		writeProblem(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, library.ErrRootNested):
		writeProblem(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, library.ErrRootInvalid):
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	h.auditRoot(r, audit.ActionSystemSettingChanged, rf, "root folder added")

	body := rootFolderJSON(rf)
	if !rf.HardlinksOK {
		// Said at the moment of the decision, where it can still be changed.
		body["warning"] = "Hardlinks are not possible from the download directory to this " +
			"root folder, so every import will COPY the file and use twice the disk space. " +
			"Putting the library and the downloads on the same filesystem avoids that."
	}
	writeJSON(w, http.StatusCreated, body)
}

// DeleteRootFolder forgets a library location. The files are untouched.
func (h *Handlers) DeleteRootFolder(w http.ResponseWriter, r *http.Request) {
	if h.roots == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "not an id")
		return
	}

	// Read it first so the audit line can name the path that was removed. A
	// line saying "root folder 3 deleted" is no use six months later.
	rf, gerr := h.roots.Get(r.Context(), id)

	err = h.roots.Delete(r.Context(), id)
	switch {
	case errors.Is(err, library.ErrRootNotFound):
		writeProblem(w, http.StatusNotFound, "no such root folder")
		return
	case errors.Is(err, library.ErrRootInUse):
		writeProblem(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	if gerr == nil {
		h.auditRoot(r, audit.ActionSystemSettingChanged, rf, "root folder removed from the configuration")
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"removed": id,
		"note": "The configuration entry was removed. Every file in that folder is " +
			"still on disk: removing a root folder and deleting a library are " +
			"different intentions, and this is the first one.",
	})
}

// RefreshRootFolder re-checks free space and hardlink viability.
//
// Worth having as an explicit action because both answers change without this
// software being involved: a disk fills, a mount moves, an operator repartitions.
func (h *Handlers) RefreshRootFolder(w http.ResponseWriter, r *http.Request) {
	if h.roots == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "not an id")
		return
	}
	rf, err := h.roots.Refresh(r.Context(), id)
	switch {
	case errors.Is(err, library.ErrRootNotFound):
		writeProblem(w, http.StatusNotFound, "no such root folder")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rootFolderJSON(rf))
}

// auditRoot records a change to where the library lives.
//
// Where a library lives is the setting that decides what a delete can reach, so
// changing it belongs in the audit log next to the permission changes rather
// than among the preferences.
func (h *Handlers) auditRoot(r *http.Request, action audit.Action, rf library.RootFolder, detail string) {
	p := authz.FromContext(r.Context())
	if h.audit == nil || p == nil {
		return
	}
	_ = h.audit.Write(r.Context(), audit.Event{
		ActorUserID: &p.UserID,
		ActorLabel:  p.Username,
		Action:      action,
		Outcome:     audit.OutcomeSuccess,
		TargetKind:  "root_folder",
		TargetID:    strconv.FormatInt(rf.ID, 10),
		SourceIP:    ClientIP(r.Context()),
		UserAgent:   r.UserAgent(),
		Detail:      detail + ": " + rf.Path,
	})
}

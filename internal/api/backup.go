package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/backup"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// BackupService is what the backup routes need (ADR-0029).
type BackupService interface {
	Take(ctx context.Context, sourceIP, userAgent string) (backup.Taken, error)
	Status(ctx context.Context) (backup.Status, error)
}

// restoreNote is said wherever backups are listed, because the question it
// answers — "where is the restore button?" — is the first one a listing raises.
const restoreNote = "A backup is restored on the host, never over HTTP: " +
	"cmediastack -restore-backup FILE -restore-to PATH writes a new database file for you " +
	"to put in place. cmediastack -verify-backup FILE checks any copy, wherever it has been."

// TakeBackup takes a backup now.
//
// The answer names the file and its hash and nothing more. No route serves a
// backup — this one included — so a stolen administrator session can make one
// and cannot carry it away.
func (h *Handlers) TakeBackup(w http.ResponseWriter, r *http.Request) {
	if h.backups == nil {
		writeProblem(w, http.StatusNotImplemented, "backups are not wired")
		return
	}
	t, err := h.backups.Take(r.Context(), ClientIP(r.Context()), r.UserAgent())
	switch {
	case authz.IsDenied(err):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, backup.ErrBusy):
		writeProblem(w, http.StatusConflict, "a backup is already being taken")
		return
	case err != nil:
		// The administrator needs the reason — a full disk, a backup directory
		// that is not mounted, a snapshot that failed its integrity check —
		// and it names nothing but paths they configured.
		writeProblem(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"backup":         backupJSON(t.Backup),
		"sha256":         t.SHA256,
		"schema_version": t.Contents.Schema.Version,
		"holds":          censusJSON(t.Contents.Census),
		"message": "Backed up: checked, encrypted, and confirmed by decrypting it again. " +
			"Copy the backup directory off this host — a backup on the database's own " +
			"disk does not survive that disk.",
	})
}

// ListBackups shows the backups on disk and the policy that keeps them.
func (h *Handlers) ListBackups(w http.ResponseWriter, r *http.Request) {
	if h.backups == nil {
		writeProblem(w, http.StatusNotImplemented, "backups are not wired")
		return
	}
	st, err := h.backups.Status(r.Context())
	switch {
	case authz.IsDenied(err):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, err.Error())
		return
	}
	list := make([]map[string]any, 0, len(st.Backups))
	for _, b := range st.Backups {
		list = append(list, backupJSON(b))
	}
	out := map[string]any{
		"dir":              st.Dir,
		"interval_seconds": int64(st.Interval.Seconds()),
		"keep_seconds":     int64(st.Keep.Seconds()),
		"keep_min":         st.KeepMin,
		"backups":          list,
		"count":            len(list),
		"restore":          restoreNote,
	}
	if st.NextDue != nil {
		out["next_due"] = *st.NextDue
	}
	if st.SameFilesystem != nil {
		out["same_filesystem_as_database"] = *st.SameFilesystem
	}
	writeJSON(w, http.StatusOK, out)
}

func backupJSON(b backup.Backup) map[string]any {
	return map[string]any{"name": b.Name, "taken_at": b.TakenAt, "bytes": b.Size}
}

func censusJSON(c db.Census) map[string]any {
	out := map[string]any{
		"accounts": c.Accounts, "library_items": c.Items, "files": c.Files,
		"requests": c.Requests, "audit_records": c.AuditEvents,
	}
	if !c.LastActivity.IsZero() {
		out["last_activity"] = c.LastActivity
	}
	return out
}

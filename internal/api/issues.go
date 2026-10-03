package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/issue"
)

// Problems people report with titles (ADR-0042).

// IssueService is what the issue routes need.
type IssueService interface {
	Report(ctx context.Context, in issue.Report) (issue.Issue, bool, error)
	List(ctx context.Context, includeResolved bool, limit int) ([]issue.Issue, error)
	Resolve(ctx context.Context, id int64, resolution, sourceIP, userAgent string) (issue.Issue, error)
}

func issueJSON(is issue.Issue) map[string]any {
	name := filmName(importer.Item{Title: is.ItemTitle, Year: is.ItemYear})
	out := map[string]any{
		"id": is.ID, "item_id": is.ItemID, "title": name, "kind": is.Kind,
		"note": is.Note, "reported_by": is.ReportedBy, "reported_at": is.ReportedAt,
		"state": is.State,
	}
	if is.Episode > 0 {
		out["season"], out["episode"] = is.Season, is.Episode
	}
	if is.ResolvedAt != nil {
		out["resolved_at"] = *is.ResolvedAt
		out["resolved_by"] = is.ResolvedBy
		out["resolution"] = is.Resolution
	}
	return out
}

func issueProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, issue.ErrInvalid):
		writeProblem(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "issue: invalid: "))
	case errors.Is(err, issue.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
	case errors.Is(err, issue.ErrResolved):
		writeProblem(w, http.StatusConflict, "that issue is already resolved")
	default:
		writeAuthzAware(w, err)
	}
}

type reportRequest struct {
	MediaItemID int64  `json:"media_item_id"`
	Kind        string `json:"kind"`
	Season      int    `json:"season"`
	Episode     int    `json:"episode"`
	Note        string `json:"note"`
}

// ReportIssue records a problem with a title, or points at the open one that
// already says it.
func (h *Handlers) ReportIssue(w http.ResponseWriter, r *http.Request) {
	if h.issues == nil {
		writeProblem(w, http.StatusNotImplemented, "issues are not wired")
		return
	}
	var in reportRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	is, created, err := h.issues.Report(r.Context(), issue.Report{
		ItemID: in.MediaItemID, Kind: in.Kind, Season: in.Season, Episode: in.Episode,
		Note: in.Note, SourceIP: ClientIP(r.Context()), UserAgent: r.UserAgent(),
	})
	if err != nil {
		issueProblem(w, err)
		return
	}
	body := issueJSON(is)
	if !created {
		body["message"] = "Somebody has already reported this, and it is open. Nothing more was added."
		writeJSON(w, http.StatusOK, body)
		return
	}
	body["message"] = "Reported. Whoever looks after the library will see it, and the answer will be here."
	writeJSON(w, http.StatusCreated, body)
}

// ListIssues lists open issues, or every one with ?all=true.
func (h *Handlers) ListIssues(w http.ResponseWriter, r *http.Request) {
	if h.issues == nil {
		writeProblem(w, http.StatusNotImplemented, "issues are not wired")
		return
	}
	list, err := h.issues.List(r.Context(), r.URL.Query().Get("all") == "true", 200)
	if err != nil {
		issueProblem(w, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, is := range list {
		out = append(out, issueJSON(is))
	}
	writeJSON(w, http.StatusOK, map[string]any{"issues": out, "count": len(out)})
}

type resolveRequest struct {
	Resolution string `json:"resolution"`
}

// ResolveIssue closes an issue with what was done.
func (h *Handlers) ResolveIssue(w http.ResponseWriter, r *http.Request) {
	if h.issues == nil {
		writeProblem(w, http.StatusNotImplemented, "issues are not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in resolveRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	is, err := h.issues.Resolve(r.Context(), id, in.Resolution, ClientIP(r.Context()), r.UserAgent())
	if err != nil {
		issueProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, issueJSON(is))
}

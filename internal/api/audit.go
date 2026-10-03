package api

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// Reading the audit log (ADR-0031).
//
// Administrators only — both routes are hidden — and the data layer checks the
// permission again. Nothing here writes: reading is not audited (decision 2),
// and there is no route that changes or removes a row.

// auditParams are the only query parameters ListAudit answers. Anything else
// is refused, so a misspelt filter is an error rather than an unfiltered page
// that looks filtered.
var auditParams = map[string]bool{
	"before": true, "limit": true, "category": true, "action": true, "outcome": true,
	"actor": true, "target": true, "q": true, "since": true, "until": true,
}

// reAuditAction is the shape every action has: lower-case words and dots.
var reAuditAction = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)

// Bounds on what a filter may hold. Generous for anything real; they stop a
// query string from carrying a megabyte into a LIKE.
const (
	maxAuditActor  = 64
	maxAuditTarget = 128
	maxAuditAction = 64
)

// ListAudit reads one page of the audit log, newest first.
func (h *Handlers) ListAudit(w http.ResponseWriter, r *http.Request) {
	if h.audit == nil {
		writeProblem(w, http.StatusNotImplemented, "no audit log is wired")
		return
	}
	f, problem := auditFilter(r.URL.Query())
	if problem != "" {
		writeProblem(w, http.StatusBadRequest, problem)
		return
	}

	page, err := h.audit.Page(r.Context(), f)
	switch {
	case errors.Is(err, audit.ErrBadFilter):
		writeProblem(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "audit: "))
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	events := make([]map[string]any, 0, len(page.Records))
	for _, rec := range page.Records {
		events = append(events, auditJSON(rec))
	}
	body := map[string]any{
		"events": events,
		"count":  len(events),
		"note": "Newest first. Every value is as it was recorded — addresses, user agents " +
			"and details included — and much of it was written by whoever made the request.",
	}
	if page.Next > 0 {
		body["next_before"] = page.Next
	}
	writeJSON(w, http.StatusOK, body)
}

// auditFilter reads a filter from a query string, or says what is wrong with
// it.
func auditFilter(v url.Values) (audit.Filter, string) {
	var f audit.Filter
	for k, vals := range v {
		if !auditParams[k] {
			return f, "the audit log has no filter called " + strconv.Quote(k)
		}
		if len(vals) > 1 {
			return f, "the filter " + strconv.Quote(k) + " is given more than once"
		}
	}
	if s := v.Get("before"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n <= 0 {
			return f, "before is the next_before of a previous page"
		}
		f.Before = n
	}
	if s := v.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > audit.MaxPageSize {
			return f, "limit is from 1 to " + strconv.Itoa(audit.MaxPageSize)
		}
		f.Limit = n
	}
	f.Category = v.Get("category")
	if s := v.Get("action"); s != "" {
		if len(s) > maxAuditAction || !reAuditAction.MatchString(s) {
			return f, "that is not an action's name"
		}
		f.Action = audit.Action(s)
	}
	f.Outcome = audit.Outcome(v.Get("outcome"))
	if f.Actor = strings.TrimSpace(v.Get("actor")); len(f.Actor) > maxAuditActor {
		return f, "actor is longer than " + strconv.Itoa(maxAuditActor) + " characters"
	}
	if f.Target = strings.TrimSpace(v.Get("target")); len(f.Target) > maxAuditTarget {
		return f, "target is longer than " + strconv.Itoa(maxAuditTarget) + " characters"
	}
	f.Text = strings.TrimSpace(v.Get("q"))
	for name, dst := range map[string]*time.Time{"since": &f.Since, "until": &f.Until} {
		s := v.Get(name)
		if s == "" {
			continue
		}
		t, ok := auditTime(s, name == "until")
		if !ok {
			return f, name + " is a time (2026-09-27T14:00:00Z) or a date (2026-09-27)"
		}
		*dst = t
	}
	return f, ""
}

// auditTime reads an RFC 3339 time or a date. A date given as the end of a
// range means the end of that day.
func auditTime(s string, end bool) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, false
	}
	if end {
		d = d.Add(24 * time.Hour)
	}
	return d, true
}

// auditJSON is one event as the screen reads it.
func auditJSON(rec audit.Record) map[string]any {
	out := map[string]any{
		"id":          rec.ID,
		"occurred_at": rec.OccurredAt.UTC().Format(time.RFC3339Nano),
		"actor":       rec.ActorLabel,
		"action":      string(rec.Action),
		"category":    audit.CategoryOf(rec.Action),
		"outcome":     string(rec.Outcome),
	}
	if rec.ActorUserID != nil {
		out["actor_user_id"] = *rec.ActorUserID
	}
	for k, v := range map[string]string{
		"target_kind": rec.TargetKind, "target_id": rec.TargetID, "source_ip": rec.SourceIP,
		"user_agent": rec.UserAgent, "detail": rec.Detail,
	} {
		if v != "" {
			out[k] = v
		}
	}
	// The values as they were recorded: JSON, sent as JSON.
	for k, v := range map[string]any{"before": rec.Before, "after": rec.After} {
		if v != nil {
			out[k] = v
		}
	}
	return out
}

// AuditSummary counts the log by action and outcome over the last days, and by
// category, so a burst is visible before anybody scrolls.
func (h *Handlers) AuditSummary(w http.ResponseWriter, r *http.Request) {
	if h.audit == nil {
		writeProblem(w, http.StatusNotImplemented, "no audit log is wired")
		return
	}
	for k := range r.URL.Query() {
		if k != "days" {
			writeProblem(w, http.StatusBadRequest, "the summary has no parameter called "+strconv.Quote(k))
			return
		}
	}
	days := 7
	if s := r.URL.Query().Get("days"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 90 {
			writeProblem(w, http.StatusBadRequest, "days is from 1 to 90")
			return
		}
		days = n
	}
	counts, err := h.audit.Summary(r.Context(), time.Duration(days)*24*time.Hour)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}

	byAction := make([]map[string]any, 0, len(counts.By))
	byCategory := map[string]map[string]int{}
	total := 0
	for _, c := range counts.By {
		byAction = append(byAction, map[string]any{
			"action": string(c.Action), "outcome": string(c.Outcome), "count": c.N,
		})
		cat := audit.CategoryOf(c.Action)
		if byCategory[cat] == nil {
			byCategory[cat] = map[string]int{}
		}
		byCategory[cat][string(c.Outcome)] += c.N
		total += c.N
	}
	type category struct {
		name     string
		n        int
		outcomes map[string]int
	}
	sorted := make([]category, 0, len(byCategory))
	for name, outcomes := range byCategory {
		c := category{name: name, outcomes: outcomes}
		for _, v := range outcomes {
			c.n += v
		}
		sorted = append(sorted, c)
	}
	// Most first; a tie by name, so the order is the same every time.
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].n != sorted[j].n {
			return sorted[i].n > sorted[j].n
		}
		return sorted[i].name < sorted[j].name
	})
	categories := make([]map[string]any, 0, len(sorted))
	for _, c := range sorted {
		categories = append(categories, map[string]any{"category": c.name, "count": c.n, "outcomes": c.outcomes})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"days":       days,
		"since":      counts.Since.Format(time.RFC3339),
		"total":      total,
		"actions":    byAction,
		"categories": categories,
		"ceiling": map[string]any{
			"anonymous_per_address_per_hour": audit.AnonymousPerSourcePerHour,
			"anonymous_per_hour":             audit.AnonymousPerHour,
			"per_account_per_hour":           audit.PersonPerHour,
			"note": "Denials over these are counted, not written one by one: one " +
				"authz.denied.suppressed line an hour says how many and from where.",
		},
	})
}

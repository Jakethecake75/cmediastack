package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// Reading the log back (ADR-0031). Every read here requires
// authz.PermViewAuditLog at the data layer, as List always has, so no handler
// can reach the rows without it.

// Categories are the first word of every action: what the screen filters by.
var Categories = []string{
	"auth", "authz", "account", "user", "indexer", "egress",
	"request", "acquisition", "media", "system",
}

// CategoryOf is an action's first word.
func CategoryOf(a Action) string {
	s := string(a)
	if i := strings.IndexByte(s, '.'); i > 0 {
		return s[:i]
	}
	return s
}

// Page bounds.
const (
	DefaultPageSize = 100
	MaxPageSize     = 200
	// MaxTextFilter bounds a free-text filter: it is matched against every
	// row the other filters leave.
	MaxTextFilter = 100
)

// ErrBadFilter means a filter names something the log cannot hold.
var ErrBadFilter = errors.New("audit: not a filter the log can answer")

// Filter selects what a page shows. Zero values select everything.
type Filter struct {
	// Before continues from a previous page: only events older than the one
	// with this id. Zero starts from the newest.
	Before int64
	Limit  int

	Category string
	Action   Action
	Outcome  Outcome
	Actor    string
	Target   string
	// Text is matched, case-insensitively, anywhere in the detail, the
	// target, the actor, the address and the user agent.
	Text  string
	Since time.Time
	Until time.Time
}

// Record is one event as read back.
type Record struct {
	ID int64
	Event
}

// Page is one page of the log, newest first.
type Page struct {
	Records []Record
	// Next continues after this page: pass it as Filter.Before. Zero when
	// there is nothing older.
	Next int64
}

// Page reads one page of the log, newest first.
//
// Pagination is by id, not by offset: the log grows while it is read, and an
// offset would show a row twice or skip one.
func (l *Logger) Page(ctx context.Context, f Filter) (Page, error) {
	if err := authz.RequirePermission(ctx, authz.PermViewAuditLog); err != nil {
		return Page{}, err
	}
	if err := f.check(); err != nil {
		return Page{}, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}

	q := recordColumns + ` WHERE 1=1`
	var args []any
	if f.Before > 0 {
		q += ` AND id < ?`
		args = append(args, f.Before)
	}
	if f.Category != "" {
		q += ` AND action LIKE ? ESCAPE '\'`
		args = append(args, likeEscape(f.Category)+".%")
	}
	if f.Action != "" {
		q += ` AND action = ?`
		args = append(args, string(f.Action))
	}
	if f.Outcome != "" {
		q += ` AND outcome = ?`
		args = append(args, string(f.Outcome))
	}
	if f.Actor != "" {
		q += ` AND actor_label = ?`
		args = append(args, f.Actor)
	}
	if f.Target != "" {
		q += ` AND target_id = ?`
		args = append(args, f.Target)
	}
	if f.Text != "" {
		pattern := "%" + likeEscape(f.Text) + "%"
		q += ` AND (COALESCE(detail,'') LIKE ? ESCAPE '\'
		        OR COALESCE(target_id,'') LIKE ? ESCAPE '\'
		        OR actor_label LIKE ? ESCAPE '\'
		        OR COALESCE(source_ip,'') LIKE ? ESCAPE '\'
		        OR COALESCE(user_agent,'') LIKE ? ESCAPE '\')`
		args = append(args, pattern, pattern, pattern, pattern, pattern)
	}
	if !f.Since.IsZero() {
		q += ` AND occurred_at >= ?`
		args = append(args, f.Since.UTC().Format(time.RFC3339Nano))
	}
	if !f.Until.IsZero() {
		q += ` AND occurred_at < ?`
		args = append(args, f.Until.UTC().Format(time.RFC3339Nano))
	}
	// One more than a page, to know whether there is another.
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)

	records, err := l.records(ctx, q, args...)
	if err != nil {
		return Page{}, err
	}
	out := Page{Records: records}
	if len(out.Records) > limit {
		out.Records = out.Records[:limit]
		out.Next = out.Records[limit-1].ID
	}
	return out, nil
}

// recordColumns selects a Record's columns, in the order records scans them.
const recordColumns = `SELECT id, occurred_at, actor_user_id, actor_label, action, outcome,
	       COALESCE(target_kind,''), COALESCE(target_id,''), COALESCE(source_ip,''),
	       COALESCE(user_agent,''), COALESCE(detail,''),
	       COALESCE(before_json,''), COALESCE(after_json,'')
	FROM audit_event`

// records runs a query over recordColumns.
func (l *Logger) records(ctx context.Context, q string, args ...any) ([]Record, error) {
	rows, err := l.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: reading the log: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Record
	for rows.Next() {
		var r Record
		var ts, action, outcome, before, after string
		if err := rows.Scan(&r.ID, &ts, &r.ActorUserID, &r.ActorLabel, &action, &outcome,
			&r.TargetKind, &r.TargetID, &r.SourceIP, &r.UserAgent, &r.Detail,
			&before, &after); err != nil {
			return nil, fmt.Errorf("audit: reading the log: %w", err)
		}
		r.OccurredAt, _ = time.Parse(time.RFC3339Nano, ts)
		r.Action, r.Outcome = Action(action), Outcome(outcome)
		r.Before, r.After = rawJSON(before), rawJSON(after)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: reading the log: %w", err)
	}
	return out, nil
}

// MaxFollowing bounds one read by After.
const MaxFollowing = 1000

// After reads the events that followed one, oldest first, at most limit of
// them: how a reader that remembers where it stopped carries on — the
// notifier (ADR-0032). Zero starts from the beginning.
func (l *Logger) After(ctx context.Context, id int64, limit int) ([]Record, error) {
	if err := authz.RequirePermission(ctx, authz.PermViewAuditLog); err != nil {
		return nil, err
	}
	if id < 0 {
		return nil, fmt.Errorf("%w: a cursor is an event's id", ErrBadFilter)
	}
	if limit <= 0 || limit > MaxFollowing {
		limit = MaxFollowing
	}
	return l.records(ctx, recordColumns+` WHERE id > ? ORDER BY id LIMIT ?`, id, limit)
}

// LastID is the newest event's id, or zero when the log is empty: where a
// reader that should not see the history starts.
func (l *Logger) LastID(ctx context.Context) (int64, error) {
	if err := authz.RequirePermission(ctx, authz.PermViewAuditLog); err != nil {
		return 0, err
	}
	var id int64
	if err := l.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM audit_event`).Scan(&id); err != nil {
		return 0, fmt.Errorf("audit: reading the log: %w", err)
	}
	return id, nil
}

// check refuses a filter that could only be a mistake.
func (f Filter) check() error {
	switch {
	case f.Before < 0:
		return fmt.Errorf("%w: a cursor is an event's id", ErrBadFilter)
	case f.Category != "" && !isCategory(f.Category):
		return fmt.Errorf("%w: %q is not a category", ErrBadFilter, f.Category)
	case f.Outcome != "" && f.Outcome != OutcomeSuccess && f.Outcome != OutcomeDenied &&
		f.Outcome != OutcomeFailure:
		return fmt.Errorf("%w: %q is not an outcome", ErrBadFilter, f.Outcome)
	case len(f.Text) > MaxTextFilter:
		return fmt.Errorf("%w: the text to find is longer than %d characters", ErrBadFilter, MaxTextFilter)
	case !f.Since.IsZero() && !f.Until.IsZero() && !f.Until.After(f.Since):
		return fmt.Errorf("%w: the range ends before it starts", ErrBadFilter)
	}
	return nil
}

func isCategory(s string) bool {
	for _, c := range Categories {
		if c == s {
			return true
		}
	}
	return false
}

// likeEscape makes s match itself in a LIKE with ESCAPE '\'.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// rawJSON hands back a stored value as the JSON it was written as, or nil.
func rawJSON(s string) any {
	if s == "" {
		return nil
	}
	if !json.Valid([]byte(s)) {
		// Written by Write, which only stores what json.Marshal produced; a
		// row edited by hand is shown as the text it holds.
		return s
	}
	return json.RawMessage(s)
}

// Count is how many events of one action ended one way.
type Count struct {
	Action  Action
	Outcome Outcome
	N       int
}

// Counts is the log counted over a window that ends now.
type Counts struct {
	Since time.Time
	// By is by action and outcome, most first.
	By []Count
}

// Summary counts the events of the last while by action and outcome. The
// window ends at the logger's own clock, the one every event was stamped by.
func (l *Logger) Summary(ctx context.Context, within time.Duration) (Counts, error) {
	if err := authz.RequirePermission(ctx, authz.PermViewAuditLog); err != nil {
		return Counts{}, err
	}
	out := Counts{Since: l.now().UTC().Add(-within)}
	rows, err := l.db.QueryContext(ctx, `
		SELECT action, outcome, COUNT(*) FROM audit_event
		WHERE occurred_at >= ?
		GROUP BY action, outcome
		ORDER BY COUNT(*) DESC, action, outcome`, out.Since.Format(time.RFC3339Nano))
	if err != nil {
		return Counts{}, fmt.Errorf("audit: counting the log: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var c Count
		var action, outcome string
		if err := rows.Scan(&action, &outcome, &c.N); err != nil {
			return Counts{}, fmt.Errorf("audit: counting the log: %w", err)
		}
		c.Action, c.Outcome = Action(action), Outcome(outcome)
		out.By = append(out.By, c)
	}
	if err := rows.Err(); err != nil {
		return Counts{}, fmt.Errorf("audit: counting the log: %w", err)
	}
	return out, nil
}

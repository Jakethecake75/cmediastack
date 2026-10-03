// Package issue records problems people report with titles in the library
// (ADR-0042): the sound is out of sync, the subtitles are missing, it is the
// wrong film. A person who may ask for titles reports one; a person who may
// edit the library resolves it.
package issue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// Kinds of problem.
var Kinds = []string{"video", "audio", "subtitles", "wrong_title", "other"}

// Errors callers distinguish.
var (
	// ErrNotFound is an issue, or a title, that does not exist or that the
	// caller may not see — alike (ADR-0037).
	ErrNotFound = errors.New("issue: not found")
	// ErrInvalid is a report that is not one.
	ErrInvalid = errors.New("issue: invalid")
	// ErrResolved is an issue already resolved.
	ErrResolved = errors.New("issue: already resolved")
)

// Issue is one reported problem.
type Issue struct {
	ID         int64
	ItemID     int64
	ItemTitle  string
	ItemYear   int
	Kind       string
	Season     int
	Episode    int
	Note       string
	ReportedBy string
	ReporterID int64
	ReportedAt time.Time
	State      string
	ResolvedBy string
	ResolvedAt *time.Time
	Resolution string
}

// Report is what somebody says is wrong.
type Report struct {
	ItemID    int64
	Kind      string
	Season    int
	Episode   int
	Note      string
	SourceIP  string
	UserAgent string
}

// Service stores issues and writes their audit lines.
type Service struct {
	db    *db.DB
	audit *audit.Logger
	now   func() time.Time
}

// NewService builds one.
func NewService(database *db.DB, auditLog *audit.Logger, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{db: database, audit: auditLog, now: now}
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// visibleTitle reads a title the caller may see, or ErrNotFound.
func (s *Service) visibleTitle(ctx context.Context, itemID int64) (string, int, error) {
	visible, vargs := library.Visible(ctx, "")
	var title string
	var year sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT title, year FROM media_item WHERE id = ? AND `+visible,
		append([]any{itemID}, vargs...)...).Scan(&title, &year)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, ErrNotFound
	}
	if err != nil {
		return "", 0, err
	}
	return title, int(year.Int64), nil
}

func label(title string, year int) string {
	if year > 0 {
		return fmt.Sprintf("%s (%d)", title, year)
	}
	return title
}

// Report records a problem, or answers with the open issue that already says
// it; created reports which.
func (s *Service) Report(ctx context.Context, in Report) (Issue, bool, error) {
	if err := authz.RequirePermission(ctx, authz.PermSubmitRequest); err != nil {
		return Issue{}, false, err
	}
	actor := authz.FromContext(ctx)
	in.Kind = strings.TrimSpace(in.Kind)
	in.Note = strings.TrimSpace(in.Note)
	switch {
	case !contains(Kinds, in.Kind):
		return Issue{}, false, fmt.Errorf("%w: kind is one of %s", ErrInvalid, strings.Join(Kinds, ", "))
	case utf8.RuneCountInString(in.Note) > 500:
		return Issue{}, false, fmt.Errorf("%w: a note is at most 500 characters", ErrInvalid)
	case in.Season < 0 || in.Episode < 0 || (in.Episode > 0 && in.Season == 0):
		return Issue{}, false, fmt.Errorf("%w: an episode needs its season", ErrInvalid)
	}
	title, year, err := s.visibleTitle(ctx, in.ItemID)
	if err != nil {
		return Issue{}, false, err
	}

	var id int64
	created := false
	err = s.db.InTx(ctx, func(tx db.Execer) error {
		// The open issue that already says it, if there is one.
		switch err := tx.QueryRowContext(ctx, `
			SELECT id FROM media_issue
			WHERE item_id = ? AND kind = ? AND season = ? AND episode = ? AND state = 'open'`,
			in.ItemID, in.Kind, in.Season, in.Episode).Scan(&id); {
		case err == nil:
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO media_issue (item_id, kind, season, episode, note, reported_by, reported_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			in.ItemID, in.Kind, in.Season, in.Episode, in.Note, actor.UserID, ts(s.now()))
		if err != nil {
			return err
		}
		created = true
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return Issue{}, false, fmt.Errorf("issue: recording a report: %w", err)
	}
	if created && s.audit != nil {
		what := label(title, year)
		if in.Episode > 0 {
			what += fmt.Sprintf(" S%02dE%02d", in.Season, in.Episode)
		}
		detail := what + ": " + strings.ReplaceAll(in.Kind, "_", " ")
		if in.Note != "" {
			detail += " — " + in.Note
		}
		_ = s.audit.Write(ctx, audit.Event{
			ActorUserID: &actor.UserID, ActorLabel: actor.Username,
			Action: audit.ActionIssueReported, TargetKind: "media_item",
			TargetID: fmt.Sprintf("%d", in.ItemID), SourceIP: in.SourceIP, UserAgent: in.UserAgent,
			Detail: detail,
		})
	}
	got, err := s.get(ctx, id)
	return got, created, err
}

const selectIssue = `
	SELECT m.id, m.item_id, i.title, COALESCE(i.year, 0), m.kind, m.season, m.episode, m.note,
	       COALESCE(r.username, ''), COALESCE(m.reported_by, 0), m.reported_at, m.state,
	       COALESCE(v.username, ''), m.resolved_at, m.resolution
	FROM media_issue m
	JOIN media_item i ON i.id = m.item_id
	LEFT JOIN app_user r ON r.id = m.reported_by
	LEFT JOIN app_user v ON v.id = m.resolved_by`

func scan(row interface{ Scan(...any) error }) (Issue, error) {
	var is Issue
	var reported string
	var resolved sql.NullString
	if err := row.Scan(&is.ID, &is.ItemID, &is.ItemTitle, &is.ItemYear, &is.Kind, &is.Season,
		&is.Episode, &is.Note, &is.ReportedBy, &is.ReporterID, &reported, &is.State,
		&is.ResolvedBy, &resolved, &is.Resolution); err != nil {
		return Issue{}, err
	}
	is.ReportedAt, _ = time.Parse(time.RFC3339Nano, reported)
	if resolved.Valid {
		t, _ := time.Parse(time.RFC3339Nano, resolved.String)
		is.ResolvedAt = &t
	}
	return is, nil
}

func (s *Service) get(ctx context.Context, id int64) (Issue, error) {
	is, err := scan(s.db.QueryRowContext(ctx, selectIssue+` WHERE m.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, ErrNotFound
	}
	return is, err
}

// List returns issues newest first: every one on a title the caller may see,
// to a person who may edit the library; their own to anyone else.
func (s *Service) List(ctx context.Context, includeResolved bool, limit int) ([]Issue, error) {
	if err := authz.RequirePermission(ctx, authz.PermSubmitRequest); err != nil {
		if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
			return nil, err
		}
	}
	p := authz.FromContext(ctx)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	visible, vargs := library.Visible(ctx, "i")
	q := selectIssue + ` WHERE ` + visible
	args := vargs
	if !p.Has(authz.PermEditLibraryItems) {
		q += ` AND m.reported_by = ?`
		args = append(args, p.UserID)
	}
	if !includeResolved {
		q += ` AND m.state = 'open'`
	}
	q += ` ORDER BY m.reported_at DESC, m.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("issue: listing: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]Issue, 0)
	for rows.Next() {
		is, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, is)
	}
	return out, rows.Err()
}

// Resolve closes an issue with what was done about it.
func (s *Service) Resolve(ctx context.Context, id int64, resolution, sourceIP, userAgent string) (Issue, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return Issue{}, err
	}
	actor := authz.FromContext(ctx)
	resolution = strings.TrimSpace(resolution)
	if resolution == "" || utf8.RuneCountInString(resolution) > 500 {
		return Issue{}, fmt.Errorf("%w: say what was done, in at most 500 characters", ErrInvalid)
	}
	is, err := s.get(ctx, id)
	if err != nil {
		return Issue{}, err
	}
	if _, _, err := s.visibleTitle(ctx, is.ItemID); err != nil {
		return Issue{}, err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE media_issue SET state = 'resolved', resolved_by = ?, resolved_at = ?, resolution = ?
		WHERE id = ? AND state = 'open'`, actor.UserID, ts(s.now()), resolution, id)
	if err != nil {
		return Issue{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Issue{}, ErrResolved
	}
	if s.audit != nil {
		_ = s.audit.Write(ctx, audit.Event{
			ActorUserID: &actor.UserID, ActorLabel: actor.Username,
			Action: audit.ActionIssueResolved, TargetKind: "media_item",
			TargetID: fmt.Sprintf("%d", is.ItemID), SourceIP: sourceIP, UserAgent: userAgent,
			Detail: label(is.ItemTitle, is.ItemYear) + ": " + strings.ReplaceAll(is.Kind, "_", " ") +
				" resolved — " + resolution,
		})
	}
	return s.get(ctx, id)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Package audit is the append-only audit log.
//
// Requirements §8: "Append-only audit log: authn events (success and failure),
// authz denials, permission changes, user CRUD, indexer/proxy config changes,
// grabs, deletions, and admin impersonation. Include actor, source IP, user
// agent, timestamp, before/after."
//
// Append-only is enforced structurally: this package exposes no method that
// issues UPDATE or DELETE against audit_event, and a test asserts that no such
// method exists. There is no "correct a mistaken entry" path by design.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// Action identifies what happened. Values are stable strings; they end up in
// exported logs and in Loki queries, so they do not change casually.
type Action string

const (
	ActionLoginSucceeded   Action = "auth.login.succeeded"
	ActionLoginFailed      Action = "auth.login.failed"
	ActionMFASucceeded     Action = "auth.mfa.succeeded"
	ActionMFAFailed        Action = "auth.mfa.failed"
	ActionMFAEnrolled      Action = "auth.mfa.enrolled"
	ActionRecoveryCodeUsed Action = "auth.recovery_code.used"
	ActionLogout           Action = "auth.logout"
	ActionSessionRevoked   Action = "auth.session.revoked"
	ActionPasswordChanged  Action = "auth.password.changed"

	ActionPasswordResetRequested   Action = "auth.password_reset.requested"
	ActionPasswordResetMinted      Action = "auth.password_reset.minted"
	ActionRecoveryCodesRegenerated Action = "auth.recovery_codes.regenerated"
	ActionAPITokenIssued           Action = "auth.api_token.issued"  // #nosec G101 -- an action's name, not a credential
	ActionAPITokenRevoked          Action = "auth.api_token.revoked" // #nosec G101 -- an action's name, not a credential
	// A calendar and feed address issued or revoked (ADR-0041).
	ActionFeedTokenIssued  Action = "auth.feed_token.issued"  // #nosec G101 -- an action's name, not a credential
	ActionFeedTokenRevoked Action = "auth.feed_token.revoked" // #nosec G101 -- an action's name, not a credential

	ActionAuthzDenied Action = "authz.denied"
	// ActionAuthzDeniedSuppressed counts the denials an hour that were over the
	// ceiling and not written one by one (ADR-0031, decision 4).
	ActionAuthzDeniedSuppressed Action = "authz.denied.suppressed"

	ActionAccountRequested Action = "account.requested"
	ActionAccountApproved  Action = "account.approved"
	ActionAccountDenied    Action = "account.denied"
	ActionAccountExpired   Action = "account.expired"
	ActionInviteIssued     Action = "account.invite.issued"
	ActionInviteRedeemed   Action = "account.invite.redeemed"
	ActionInviteRevoked    Action = "account.invite.revoked"

	ActionUserCreated     Action = "user.created"
	ActionUserUpdated     Action = "user.updated"
	ActionUserSuspended   Action = "user.suspended"
	ActionUserReactivated Action = "user.reactivated"
	ActionUserDeleted     Action = "user.deleted"
	ActionRoleChanged     Action = "user.role.changed"
	// ActionAccountRecovered is break-glass recovery from the host console.
	// The most security-sensitive record this log carries: it is the only one
	// whose actor is not an account, because the actor is whoever had a shell.
	ActionAccountRecovered Action = "user.recovered"
	ActionGrantsChanged    Action = "user.grants.changed"
	// ActionRolePermissionsChanged is a role's permissions edited or put back
	// to the defaults (ADR-0039): it changes what every holder may do.
	ActionRolePermissionsChanged Action = "user.role_permissions.changed"

	ActionIndexerCreated Action = "indexer.created"
	ActionIndexerUpdated Action = "indexer.updated"
	ActionIndexerDeleted Action = "indexer.deleted"
	ActionEgressChanged  Action = "egress.changed"
	ActionKillSwitch     Action = "egress.kill_switch"

	ActionRequestSubmitted Action = "request.submitted"
	ActionRequestApproved  Action = "request.approved"
	ActionRequestDenied    Action = "request.denied"
	ActionRequestGrabbed   Action = "request.grabbed"
	ActionRequestFulfilled Action = "request.fulfilled"
	// ActionRequestLinked records which library item an approver said
	// satisfies a request (ADR-0028).
	ActionRequestLinked Action = "request.linked"

	ActionReleaseGrabbed Action = "acquisition.grabbed"
	ActionQueueRemoved   Action = "acquisition.queue.removed"
	// ActionDownloadStalled is a download found making no progress: given up
	// when automatic acquisition grabbed it, reported when a person did
	// (ADR-0034).
	ActionDownloadStalled Action = "acquisition.stalled"
	ActionLibraryScanned  Action = "media.library.scanned"
	ActionMediaAdded      Action = "media.added"
	ActionMediaDeleted    Action = "media.deleted"
	ActionMediaRestored   Action = "media.restored"
	// ActionMediaFileDeleted is one file moved to the trash, its title kept;
	// ActionMediaPurged one trashed file unlinked now (ADR-0053).
	ActionMediaFileDeleted Action = "media.file_deleted"
	ActionMediaPurged      Action = "media.purged"
	// ActionMediaSubtitleFetched is a subtitle fetched and written beside a
	// file (ADR-0055).
	ActionMediaSubtitleFetched Action = "media.subtitle_fetched"
	ActionMediaIdentified      Action = "media.identified"
	// ActionMediaProfileChanged is a title given its own quality profile, or
	// put back on the default (ADR-0035).
	ActionMediaProfileChanged Action = "media.quality_profile.changed"
	// ActionMediaRatingChanged is a person setting a title's rating, or giving
	// it back to the provider (ADR-0037). A rating decides who can see a title.
	ActionMediaRatingChanged Action = "media.rating.changed"
	// ActionMediaDownloaded is a title's original file downloaded (ADR-0038):
	// a copy that has left the instance.
	ActionMediaDownloaded Action = "media.downloaded"
	// A problem with a title reported, and resolved (ADR-0042).
	ActionIssueReported Action = "media.issue.reported"
	ActionIssueResolved Action = "media.issue.resolved"
	ActionTrashPurged   Action = "media.trash.purged"
	// ActionMediaImported is a file arriving in the library (ADR-0032): the
	// one change to the library that was not recorded.
	ActionMediaImported Action = "media.imported"

	ActionSystemSettingChanged Action = "system.setting.changed"
	// ActionBackupCreated records a backup taken, or one that failed its checks
	// and was not kept (ADR-0029). The detail carries the file's SHA-256, so a
	// copy elsewhere can be checked against it without the key.
	ActionBackupCreated Action = "system.backup.created"
	// ActionBackupPruned records a backup deleted for being older than
	// backup.keep.
	ActionBackupPruned Action = "system.backup.pruned"
	// ActionBackupRestored is written into a database restored from a backup,
	// as the first thing in it after the backup: which file, and what the
	// restore ended. Its history otherwise stops at the backup and resumes
	// without a word about the gap.
	ActionBackupRestored Action = "system.backup.restored"
	// ActionMasterKeyRotated is every sealed value re-sealed under a new master
	// key, from the host (ADR-0054). The detail counts them; no key is written.
	ActionMasterKeyRotated  Action = "system.master_key.rotated"
	ActionFirstRunCompleted Action = "system.first_run.completed"
)

// Outcome of an audited action.
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeDenied  Outcome = "denied"
	OutcomeFailure Outcome = "failure"
)

// Event is one audit record.
type Event struct {
	OccurredAt time.Time
	// ActorUserID is nil for anonymous and system actors.
	ActorUserID *int64
	// ActorLabel is a human-readable actor: a username, "anonymous", or
	// "system:scheduler". Never an email address (§8 PII minimisation).
	ActorLabel string
	Action     Action
	Outcome    Outcome
	TargetKind string
	TargetID   string
	SourceIP   string
	UserAgent  string
	Detail     string
	Before     any
	After      any
}

// Logger writes audit events. It deliberately has no Update or Delete method.
type Logger struct {
	db  *db.DB
	now func() time.Time

	// The ceiling on authz.denied rows (ADR-0031, decision 4): this hour's
	// counts, and what was not written.
	mu      sync.Mutex
	denials denialWindow
}

// New creates a Logger. now is injectable for tests.
func New(database *db.DB, now func() time.Time) *Logger {
	if now == nil {
		now = time.Now
	}
	return &Logger{db: database, now: now}
}

// Write appends an event.
//
// A failure to write an audit record is returned rather than swallowed: the
// caller decides whether the audited operation should proceed without a
// record. For security-relevant operations the answer is no.
func (l *Logger) Write(ctx context.Context, e Event) error {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = l.now()
	}
	if e.ActorLabel == "" {
		e.ActorLabel = "anonymous"
	}
	if e.Outcome == "" {
		e.Outcome = OutcomeSuccess
	}

	before, err := marshalOrEmpty(e.Before)
	if err != nil {
		return fmt.Errorf("audit: marshal before: %w", err)
	}
	after, err := marshalOrEmpty(e.After)
	if err != nil {
		return fmt.Errorf("audit: marshal after: %w", err)
	}

	_, err = l.db.ExecContext(ctx, `
		INSERT INTO audit_event
		    (occurred_at, actor_user_id, actor_label, action, outcome,
		     target_kind, target_id, source_ip, user_agent, detail, before_json, after_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.OccurredAt.UTC().Format(time.RFC3339Nano),
		e.ActorUserID, e.ActorLabel, string(e.Action), string(e.Outcome),
		nullIfEmpty(e.TargetKind), nullIfEmpty(e.TargetID),
		nullIfEmpty(e.SourceIP), nullIfEmpty(e.UserAgent), nullIfEmpty(e.Detail),
		before, after,
	)
	if err != nil {
		return fmt.Errorf("audit: write %s: %w", e.Action, err)
	}
	return nil
}

// AuthzDenied implements the api.AuditSink interface, recording the real
// authorization failure behind a response that tells the client nothing.
//
// Up to a ceiling (ADR-0031, decision 4): an anonymous client could otherwise
// add a row with every request it makes. What is over the ceiling is counted,
// and written as one line when the hour is over.
func (l *Logger) AuthzDenied(ctx context.Context, route string, d *authz.Denial, clientIP, userAgent string) {
	var actor *int64
	label := "anonymous"
	if d != nil && d.Actor != 0 {
		id := d.Actor
		actor = &id
		label = fmt.Sprintf("user:%d", id)
	}
	detail := ""
	if d != nil {
		detail = d.Reason + ": " + d.Detail
	}

	write, finished := l.admitDenial(label, clientIP, actor != nil)
	for _, e := range finished {
		_ = l.Write(ctx, e)
	}
	if !write {
		return
	}

	// A failure here must not break the request, but it must be visible.
	_ = l.Write(ctx, Event{
		ActorUserID: actor,
		ActorLabel:  label,
		Action:      ActionAuthzDenied,
		Outcome:     OutcomeDenied,
		TargetKind:  "route",
		TargetID:    route,
		SourceIP:    clientIP,
		UserAgent:   userAgent,
		Detail:      detail,
	})
}

// Query filters an audit-log read.
type Query struct {
	Action  Action
	ActorID *int64
	Since   time.Time
	Until   time.Time
	Limit   int
}

// List reads audit events, newest first. Reading requires
// authz.PermViewAuditLog, checked here rather than only at the route, because
// this is the data layer.
func (l *Logger) List(ctx context.Context, q Query) ([]Event, error) {
	if err := authz.RequirePermission(ctx, authz.PermViewAuditLog); err != nil {
		return nil, err
	}
	if q.Limit <= 0 || q.Limit > 1000 {
		q.Limit = 200
	}

	sql := `SELECT occurred_at, actor_user_id, actor_label, action, outcome,
	               COALESCE(target_kind,''), COALESCE(target_id,''),
	               COALESCE(source_ip,''), COALESCE(user_agent,''), COALESCE(detail,'')
	        FROM audit_event WHERE 1=1`
	var args []any

	if q.Action != "" {
		sql += " AND action = ?"
		args = append(args, string(q.Action))
	}
	if q.ActorID != nil {
		sql += " AND actor_user_id = ?"
		args = append(args, *q.ActorID)
	}
	if !q.Since.IsZero() {
		sql += " AND occurred_at >= ?"
		args = append(args, q.Since.UTC().Format(time.RFC3339Nano))
	}
	if !q.Until.IsZero() {
		sql += " AND occurred_at <= ?"
		args = append(args, q.Until.UTC().Format(time.RFC3339Nano))
	}
	sql += " ORDER BY occurred_at DESC, id DESC LIMIT ?"
	args = append(args, q.Limit)

	rows, err := l.db.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Event
	for rows.Next() {
		var e Event
		var ts string
		var action, outcome string
		if err := rows.Scan(&ts, &e.ActorUserID, &e.ActorLabel, &action, &outcome,
			&e.TargetKind, &e.TargetID, &e.SourceIP, &e.UserAgent, &e.Detail); err != nil {
			return nil, err
		}
		e.OccurredAt, _ = time.Parse(time.RFC3339Nano, ts)
		e.Action = Action(action)
		e.Outcome = Outcome(outcome)
		out = append(out, e)
	}
	return out, rows.Err()
}

func marshalOrEmpty(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

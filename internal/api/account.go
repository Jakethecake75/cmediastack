package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/platform/tasks"
)

// ---------------------------------------------------------------------------
// Invites
// ---------------------------------------------------------------------------

type issueInviteRequest struct {
	RoleID int64 `json:"role_id"`
	// AllLibraries absent with no library_ids is every library; library_ids
	// are root folders (ADR-0037).
	AllLibraries  *bool   `json:"all_libraries"`
	LibraryIDs    []int64 `json:"library_ids"`
	RatingCeiling int     `json:"rating_ceiling"`
	AutoApprove   bool    `json:"auto_approve"`
	TTLHours      int     `json:"ttl_hours"`
	Note          string  `json:"note"`
}

// IssueInvite creates a single-use invite and returns its code exactly once.
func (h *Handlers) IssueInvite(w http.ResponseWriter, r *http.Request) {
	var in issueInviteRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	grant, err := identity.NormaliseGrant(in.AllLibraries, in.LibraryIDs, in.RatingCeiling)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "identity: "))
		return
	}
	code, inv, err := h.svc.IssueInvite(r.Context(), identity.IssueInviteInput{
		RoleID:        in.RoleID,
		AllLibraries:  grant.AllLibraries,
		LibraryIDs:    grant.RootFolderIDs,
		RatingCeiling: grant.RatingCeiling,
		AutoApprove:   in.AutoApprove,
		TTL:           time.Duration(in.TTLHours) * time.Hour,
		Note:          in.Note,
		SourceIP:      ClientIP(r.Context()),
		UserAgent:     r.UserAgent(),
	})
	if errors.Is(err, identity.ErrNoSuchRootFolder) {
		writeProblem(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "identity: "))
		return
	}
	if err != nil {
		writeAuthzAware(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":           inv.ID,
		"code":         code,
		"role":         inv.RoleName,
		"auto_approve": inv.AutoApprove,
		"expires_at":   inv.ExpiresAt,
		"message":      "this code is shown once; only its hash is stored",
	})
}

// ListInvites shows outstanding invites. Codes are absent because only their
// hashes exist.
func (h *Handlers) ListInvites(w http.ResponseWriter, r *http.Request) {
	invites, err := h.svc.Store().ListInvites(r.Context())
	if err != nil {
		writeAuthzAware(w, err)
		return
	}

	// The service's clock, not the wall clock: the same one redemption checks
	// expiry against, so what an operator is shown and what is enforced cannot
	// disagree.
	now := h.svc.Now()
	out := make([]map[string]any, 0, len(invites))
	for _, inv := range invites {
		state := "active"
		switch {
		case inv.RedeemedAt != nil:
			state = "redeemed"
		case inv.RevokedAt != nil:
			state = "revoked"
		case now.After(inv.ExpiresAt):
			state = "expired"
		}
		out = append(out, map[string]any{
			"id": inv.ID, "role": inv.RoleName, "state": state,
			"auto_approve": inv.AutoApprove, "note": inv.Note,
			"all_libraries": inv.AllLibraries,
			"library_ids":   inv.LibraryIDs, "rating_ceiling": inv.RatingCeiling,
			"created_at": inv.CreatedAt, "expires_at": inv.ExpiresAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": out, "count": len(out)})
}

// RevokeInvite cancels an unredeemed invite.
func (h *Handlers) RevokeInvite(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	if err := h.svc.RevokeInvite(r.Context(), id,
		ClientIP(r.Context()), r.UserAgent()); err != nil {
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

// MySessions lists the caller's live sessions.
//
// It reads by the principal's own user ID, never by an ID from the request, so
// there is no parameter here for an attacker to change.
func (h *Handlers) MySessions(w http.ResponseWriter, r *http.Request) {
	p := authz.FromContext(r.Context())
	if p == nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}

	sessions, err := h.svc.Store().UserSessions(r.Context(), p.UserID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}

	out := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, map[string]any{
			"id":            s.ID,
			"current":       s.ID == p.SessionID,
			"mfa_satisfied": s.MFASatisfied,
			"source_ip":     s.SourceIP,
			"user_agent":    s.UserAgent,
			"created_at":    s.CreatedAt,
			"last_seen_at":  s.LastSeenAt,
			"expires_at":    s.AbsExpires,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out, "count": len(out)})
}

// RevokeMySession ends one of the caller's own sessions.
//
// Ownership is enforced in the UPDATE's WHERE clause, not by a preceding
// lookup, so there is no window in which a session belonging to somebody else
// could be revoked. A foreign or unknown session ID gets the same 404.
func (h *Handlers) RevokeMySession(w http.ResponseWriter, r *http.Request) {
	p := authz.FromContext(r.Context())
	if p == nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}

	target := r.PathValue("id")
	if err := h.svc.Store().RevokeOwnedSession(r.Context(), p.UserID, target, "revoked_by_user"); err != nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}

	// Revoking the session you are using is a logout.
	if target == p.SessionID {
		h.auth.ClearSessionCookie(w)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "was_current": target == p.SessionID})
}

// ---------------------------------------------------------------------------
// Password reset
// ---------------------------------------------------------------------------

type resetInitiateRequest struct {
	Username string `json:"username"`
}

// ResetInitiate starts a password reset.
//
// The response is identical for a known and an unknown account, and does not
// reveal that delivery is unimplemented — saying so would confirm the account
// exists, which is exactly what this endpoint must not do.
func (h *Handlers) ResetInitiate(w http.ResponseWriter, r *http.Request) {
	var in resetInitiateRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	err := h.svc.InitiateReset(r.Context(), in.Username, ClientIP(r.Context()), r.UserAgent())
	if errors.Is(err, identity.ErrThrottled) {
		w.Header().Set("Retry-After", "3600")
		writeProblem(w, http.StatusTooManyRequests, "too many attempts")
		return
	}
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":  "submitted",
		"message": "if that account exists, a reset has been started",
	})
}

type resetCompleteRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// ResetComplete consumes a reset token and sets a new password.
//
// It does not sign the user in. They log in normally afterwards and still have
// to present their second factor: a reset recovers a forgotten password, it is
// not a way around the authenticator.
func (h *Handlers) ResetComplete(w http.ResponseWriter, r *http.Request) {
	var in resetCompleteRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	err := h.svc.CompleteReset(r.Context(), in.Token, in.Password,
		ClientIP(r.Context()), r.UserAgent())
	switch {
	case errors.Is(err, identity.ErrResetInvalid):
		writeProblem(w, http.StatusBadRequest, "invalid or expired reset token")
		return
	case errors.Is(err, identity.ErrPasswordTooShort),
		errors.Is(err, identity.ErrPasswordTooSimple),
		errors.Is(err, identity.ErrPasswordBreached),
		errors.Is(err, identity.ErrPasswordTooLong):
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "password changed; all sessions were revoked. Log in again, then present your authenticator.",
	})
}

// MintResetLink lets an administrator create a reset token for a user.
//
// This is the working delivery path while there is no mail transport. It is an
// administrative action against another account, so the escalation guard
// applies: a Manager cannot mint a reset for an Admin.
func (h *Handlers) MintResetLink(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}

	token, expires, err := h.svc.MintResetToken(r.Context(), id,
		ClientIP(r.Context()), r.UserAgent())
	if err != nil {
		writeAuthzAware(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      token,
		"expires_at": expires,
		"message":    "give this to the user over a channel you trust; it is single-use and shown once",
	})
}

// ---------------------------------------------------------------------------
// Self-service credentials
// ---------------------------------------------------------------------------

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword updates the caller's own password.
func (h *Handlers) ChangePassword(w http.ResponseWriter, r *http.Request) {
	var in changePasswordRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	err := h.svc.ChangePassword(r.Context(), in.CurrentPassword, in.NewPassword,
		ClientIP(r.Context()), r.UserAgent())
	switch {
	case errors.Is(err, identity.ErrLoginFailed):
		writeProblem(w, http.StatusUnauthorized, "invalid credentials")
		return
	case errors.Is(err, identity.ErrPasswordTooShort),
		errors.Is(err, identity.ErrPasswordTooSimple),
		errors.Is(err, identity.ErrPasswordBreached),
		errors.Is(err, identity.ErrPasswordTooLong):
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"message": "password changed; other sessions were revoked",
	})
}

type regenerateCodesRequest struct {
	CurrentPassword string `json:"current_password"`
}

// RegenerateRecoveryCodes issues a fresh set and invalidates the old ones.
func (h *Handlers) RegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	var in regenerateCodesRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	codes, err := h.svc.RegenerateRecoveryCodes(r.Context(), in.CurrentPassword,
		ClientIP(r.Context()), r.UserAgent())
	if err != nil {
		writeProblem(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"recovery_codes": codes,
		"message":        "store these now; the previous set no longer works",
	})
}

// ---------------------------------------------------------------------------
// API tokens
// ---------------------------------------------------------------------------

type issueTokenRequest struct {
	Name        string   `json:"name"`
	Permissions []string `json:"permissions"`
	TTLDays     int      `json:"ttl_days"`
}

// IssueAPIToken mints a scoped token for the calling user.
//
// This route is session-only: a token cannot mint another token, so a leaked
// token cannot widen its own scope or outlive its revocation by spawning
// successors.
func (h *Handlers) IssueAPIToken(w http.ResponseWriter, r *http.Request) {
	var in issueTokenRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	perms := make([]authz.Permission, 0, len(in.Permissions))
	for _, p := range in.Permissions {
		perms = append(perms, authz.Permission(p))
	}

	raw, tok, err := h.svc.IssueAPIToken(r.Context(), identity.IssueTokenInput{
		Name:        in.Name,
		Permissions: perms,
		TTL:         time.Duration(in.TTLDays) * 24 * time.Hour,
		SourceIP:    ClientIP(r.Context()),
		UserAgent:   r.UserAgent(),
	})
	if errors.Is(err, identity.ErrTokenScopeTooWide) {
		// Safe to report: the caller asked for it and can see their own
		// permissions, so this reveals nothing new.
		writeProblem(w, http.StatusForbidden, "requested scope exceeds your own permissions")
		return
	}
	if err != nil {
		writeAuthzAware(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":          tok.ID,
		"token":       raw,
		"name":        tok.Name,
		"permissions": in.Permissions,
		"expires_at":  tok.ExpiresAt,
		"message":     "this token is shown once; only its hash is stored",
	})
}

// ListAPITokens shows the caller's tokens. The values are absent because only
// their hashes exist.
func (h *Handlers) ListAPITokens(w http.ResponseWriter, r *http.Request) {
	p := authz.FromContext(r.Context())
	if p == nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}

	tokens, err := h.svc.Store().ListAPITokens(r.Context(), p.UserID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}

	out := make([]map[string]any, 0, len(tokens))
	for _, t := range tokens {
		perms := make([]string, len(t.Permissions))
		for i, perm := range t.Permissions {
			perms[i] = string(perm)
		}
		out = append(out, map[string]any{
			"id": t.ID, "name": t.Name, "permissions": perms,
			"created_at": t.CreatedAt, "expires_at": t.ExpiresAt,
			"last_used_at": t.LastUsedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out, "count": len(out)})
}

// RevokeAPIToken revokes one of the caller's own tokens.
func (h *Handlers) RevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	if err := h.svc.RevokeAPIToken(r.Context(), id,
		ClientIP(r.Context()), r.UserAgent()); err != nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// Scheduled tasks
// ---------------------------------------------------------------------------

// TaskScheduler is the subset of the scheduler this package needs.
type TaskScheduler interface {
	Snapshot() []tasks.Status
	Trigger(ctx context.Context, name string) (tasks.Outcome, error)
}

// ListTasks shows every scheduled task with its next run, last run and failure
// history (requirements §9).
func (h *Handlers) ListTasks(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		writeProblem(w, http.StatusNotImplemented, "no scheduler is wired")
		return
	}

	out := make([]map[string]any, 0)
	for _, st := range h.tasks.Snapshot() {
		history := make([]map[string]any, 0, len(st.History))
		for _, o := range st.History {
			history = append(history, map[string]any{
				"started_at": o.StartedAt, "duration_ms": o.Duration.Milliseconds(),
				"ok": o.Succeeded(), "summary": o.Summary, "error": o.Err, "manual": o.Manual,
			})
		}
		entry := map[string]any{
			"name": st.Name, "description": st.Description,
			"interval_seconds": int64(st.Interval.Seconds()),
			"running":          st.Running,
			"runs":             st.Runs, "failures": st.Failures,
			"history": history,
		}
		if st.LastRun != nil {
			entry["last_run"] = *st.LastRun
		}
		if st.NextRun != nil {
			entry["next_run"] = *st.NextRun
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": out, "count": len(out)})
}

// TriggerTask runs a task now.
func (h *Handlers) TriggerTask(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		writeProblem(w, http.StatusNotImplemented, "no scheduler is wired")
		return
	}

	name := r.PathValue("name")
	out, err := h.tasks.Trigger(r.Context(), name)
	switch {
	case errors.Is(err, tasks.ErrUnknownTask):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, tasks.ErrAlreadyRunning):
		// Refused rather than queued: an operator who clicks twice should be
		// told the first run is still going, not silently given a second one.
		writeProblem(w, http.StatusConflict, "that task is already running")
		return
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok": out.Succeeded(), "summary": out.Summary,
		"error": out.Err, "duration_ms": out.Duration.Milliseconds(),
	})
}

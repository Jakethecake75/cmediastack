package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identity"
)

// Administering accounts.
//
// Every handler here is registered with rt.Admin, so an account without the
// permission sees 404 rather than 403 — the existence of an administration
// surface is not disclosed to somebody who may not use it.

// ListUsers returns every account.
func (h *Handlers) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.svc.ListUsers(r.Context())
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	actor := authz.FromContext(r.Context())

	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		entry := map[string]any{
			"id":       u.ID,
			"username": u.Username,
			"state":    string(u.State),
			"role":     u.RoleName,
			"role_id":  u.RoleID,
			"rank":     u.RoleRank,
			// Whether this account can actually sign in, which is not the same
			// as its state: an account that never enrolled an authenticator is
			// "awaiting_mfa" and cannot act, and an administrator reading this
			// screen needs that distinction to make sense of it.
			"enrolled":   u.Enrolled(),
			"created_at": u.CreatedAt,
			// Deliberately absent: email. §8 asks for PII minimisation, and an
			// account-management screen needs a username to act on, not an
			// address. Nothing here needs to mail anybody.
		}
		// Absent until the account completes a full sign-in — password AND
		// second factor. Enrolling an authenticator does not set it, which
		// looks like an omission and is not: an account that enrolled once and
		// never came back should read "never", not "active today", or the one
		// column an operator uses to find dormant accounts reports the opposite
		// of the truth. Verified against the binary: enrollment leaves it null,
		// POST /api/v1/auth/login/mfa sets it.
		if u.LastLoginAt != nil {
			entry["last_login_at"] = *u.LastLoginAt
		}
		// Said per row rather than left to the client to work out, because the
		// rule is not obvious: you may not act on yourself, or on a peer, or on
		// a superior.
		entry["manageable"] = actor != nil && u.ID != actor.UserID && u.RoleRank < actor.Role.Rank
		// What the account may see of the library (ADR-0037).
		entry["all_libraries"] = u.AllLibraries
		entry["rating_ceiling"] = u.RatingCeiling
		ids := []int64{}
		if !u.AllLibraries {
			if got, gerr := h.svc.Store().LibraryGrants(r.Context(), u.ID); gerr == nil && got != nil {
				ids = got
			}
		}
		entry["library_ids"] = ids
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out, "count": len(out)})
}

type userPatch struct {
	// State is "suspended" or "active". A role change and a state change are
	// separate requests: doing both at once makes the audit line ambiguous
	// about which one the operator actually intended.
	State  string `json:"state"`
	RoleID int64  `json:"role_id"`
	Reason string `json:"reason"`
}

// UpdateUser suspends, reactivates or re-roles an account.
func (h *Handlers) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "not an id")
		return
	}
	var in userPatch
	if !decodeJSON(w, r, &in) {
		return
	}
	if (in.State == "") == (in.RoleID == 0) {
		writeProblem(w, http.StatusBadRequest,
			"send exactly one of state or role_id: doing both at once makes the "+
				"audit line ambiguous about what was intended")
		return
	}

	ip, ua := ClientIP(r.Context()), r.UserAgent()
	switch {
	case in.RoleID != 0:
		err = h.svc.ChangeUserRole(r.Context(), id, in.RoleID, ip, ua)
	case in.State == string(authz.StateSuspended):
		err = h.svc.SuspendUser(r.Context(), id, in.Reason, ip, ua)
	case in.State == string(authz.StateActive):
		err = h.svc.ReactivateUser(r.Context(), id, ip, ua)
	default:
		writeProblem(w, http.StatusBadRequest,
			"state must be 'suspended' or 'active'")
		return
	}

	switch {
	case errors.Is(err, identity.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, identity.ErrLastAdministrator):
		// 409 rather than 403: the actor has the authority, and the instance is
		// refusing because the outcome would be unrecoverable. Those are
		// different things and an operator should be able to tell them apart.
		writeProblem(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	// The service writes the audit line, because it is the layer that knows
	// what changed from what. Re-writing one here would produce two records of
	// one action.
	writeJSON(w, http.StatusOK, map[string]any{
		"updated": id,
		"note": "Sessions are server-side records re-read on every request, so this " +
			"takes effect on the account's next request rather than when its " +
			"session would have expired.",
	})
}

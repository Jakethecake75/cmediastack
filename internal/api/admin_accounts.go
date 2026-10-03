package api

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identity"
)

// Account administration (ADR-0039): a pending request read alone, the roles
// and their permissions, an account an administrator creates, and a person's
// own email.

func identityMessage(err error) string {
	return strings.TrimPrefix(err.Error(), "identity: ")
}

// GetAccountRequest returns one pending account request.
func (h *Handlers) GetAccountRequest(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	req, err := h.svc.PendingRequest(r.Context(), id)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": req.ID, "username": req.Username, "email": req.Email, "note": req.Note,
		"source_ip": req.SourceIP, "user_agent": req.UserAgent,
		"created_at": req.CreatedAt, "expires_at": req.ExpiresAt,
		"invited": req.InviteID != nil,
	})
}

func roleJSON(s identity.RoleSummary) map[string]any {
	return map[string]any{
		"id": s.ID, "name": s.Name, "rank": s.Rank, "builtin": s.Builtin,
		"permissions": permissionStrings(s.Permissions.Slice()),
		"defaults":    s.Defaults,
		"holders":     s.Holders,
		"editable":    s.Name != authz.RoleAdmin,
	}
}

// ListRoles lists every role, what it may do, and who holds it.
func (h *Handlers) ListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.svc.RoleSummaries(r.Context())
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	out := make([]map[string]any, 0, len(roles))
	for _, s := range roles {
		out = append(out, roleJSON(s))
	}
	all := make([]string, 0, len(authz.AllPermissions))
	for _, p := range authz.AllPermissions {
		all = append(all, string(p))
	}
	reserved := make([]string, 0, len(identity.ReservedForAdmin))
	for _, p := range identity.ReservedForAdmin {
		reserved = append(reserved, string(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": out, "count": len(out),
		"permissions": all, "reserved_for_admin": reserved})
}

type roleEditRequest struct {
	Permissions []string `json:"permissions"`
	Defaults    bool     `json:"defaults"`
}

// EditRole replaces a role's permissions, or puts back its built-in ones.
func (h *Handlers) EditRole(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in roleEditRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if !in.Defaults && in.Permissions == nil {
		writeProblem(w, http.StatusBadRequest, "send permissions, or defaults: true")
		return
	}
	perms := make([]authz.Permission, 0, len(in.Permissions))
	for _, p := range in.Permissions {
		perms = append(perms, authz.Permission(p))
	}
	_, after, err := h.svc.EditRole(r.Context(), id, perms, in.Defaults, ClientIP(r.Context()), r.UserAgent())
	switch {
	case errors.Is(err, identity.ErrRoleNotEditable):
		writeProblem(w, http.StatusConflict, identityMessage(err))
		return
	case errors.Is(err, identity.ErrPermissionReserved), errors.Is(err, identity.ErrLoginRequired),
		errors.Is(err, identity.ErrUnknownPermission):
		writeProblem(w, http.StatusBadRequest, identityMessage(err))
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": after.ID, "name": after.Name,
		"permissions": permissionStrings(after.Permissions.Slice()),
		"defaults":    in.Defaults,
		"note": "Every account holding this role, and every API token it issued, is judged " +
			"by these from its next request.",
	})
}

type createUserRequest struct {
	Username      string  `json:"username"`
	Email         string  `json:"email"`
	RoleID        int64   `json:"role_id"`
	AllLibraries  *bool   `json:"all_libraries"`
	LibraryIDs    []int64 `json:"library_ids"`
	RatingCeiling int     `json:"rating_ceiling"`
}

// CreateUser creates an account on an administrator's authority. The answer
// carries a one-time link that sets its password; nobody else ever knows it.
func (h *Handlers) CreateUser(w http.ResponseWriter, r *http.Request) {
	var in createUserRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	grant, err := identity.NormaliseGrant(in.AllLibraries, in.LibraryIDs, in.RatingCeiling)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, identityMessage(err))
		return
	}
	id, token, expires, err := h.svc.CreateAccount(r.Context(), identity.NewAccount{
		Username: in.Username, Email: in.Email, RoleID: in.RoleID, Grant: grant,
		SourceIP: ClientIP(r.Context()), UserAgent: r.UserAgent(),
	})
	switch {
	case errors.Is(err, identity.ErrInvalidUsername), errors.Is(err, identity.ErrInvalidEmail),
		errors.Is(err, identity.ErrNoSuchRootFolder):
		writeProblem(w, http.StatusBadRequest, identityMessage(err))
		return
	case errors.Is(err, identity.ErrAccountExists):
		writeProblem(w, http.StatusConflict, "that username or email is already registered")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"user_id":    id,
		"state":      string(authz.StateAwaitingMFA),
		"link":       "/reset?token=" + url.QueryEscape(token),
		"expires_at": expires,
		"message": "Give this link to them over a channel you trust. It sets their password once; " +
			"then they sign in and enroll an authenticator. It is shown only now.",
	})
}

type updateMeRequest struct {
	Email           *string `json:"email"`
	CurrentPassword string  `json:"current_password"`
}

// UpdateMe changes the caller's own email, with their current password.
func (h *Handlers) UpdateMe(w http.ResponseWriter, r *http.Request) {
	var in updateMeRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Email == nil {
		writeProblem(w, http.StatusBadRequest,
			"email is the one thing a profile changes; the username names the account in the audit log")
		return
	}
	err := h.svc.ChangeEmail(r.Context(), in.CurrentPassword, *in.Email, ClientIP(r.Context()), r.UserAgent())
	switch {
	case errors.Is(err, identity.ErrInvalidEmail):
		writeProblem(w, http.StatusBadRequest, identityMessage(err))
		return
	case errors.Is(err, identity.ErrLoginFailed):
		writeProblem(w, http.StatusForbidden, "the current password is not right")
		return
	case errors.Is(err, identity.ErrAccountExists):
		writeProblem(w, http.StatusConflict, "that email is already registered")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"email": strings.ToLower(strings.TrimSpace(*in.Email))})
}

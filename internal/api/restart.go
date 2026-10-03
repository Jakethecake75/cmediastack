package api

import (
	"errors"
	"net/http"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/egressproxy"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// A SOCKS5 proxy set from the web, and the restart that applies it (ADR-0065).

type egressProxyRequest struct {
	Address  string   `json:"address"`
	Username string   `json:"username"`
	Password string   `json:"password"`
	Profiles []string `json:"profiles"`
	// The administrator's own password and a current authenticator code: a
	// session alone cannot redirect the instance's traffic.
	CurrentPassword string `json:"current_password"`
	Code            string `json:"code"`
}

// SetEgressProxy stores the proxy. It applies on restart.
func (h *Handlers) SetEgressProxy(w http.ResponseWriter, r *http.Request) {
	if h.proxy == nil {
		writeProblem(w, http.StatusNotImplemented, "no proxy store is wired")
		return
	}
	var in egressProxyRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	ip, ua := ClientIP(r.Context()), r.UserAgent()
	switch err := h.svc.Reauthenticate(r.Context(), in.CurrentPassword, in.Code, ip, ua); {
	case errors.Is(err, identity.ErrThrottled):
		writeProblem(w, http.StatusTooManyRequests, "too many attempts; wait and try again")
		return
	case errors.Is(err, identity.ErrLoginFailed):
		writeProblem(w, http.StatusForbidden, "your password and a current authenticator code are needed to change the proxy")
		return
	case err != nil:
		writeProblem(w, http.StatusInternalServerError, "could not check your password")
		return
	}

	before, err := h.proxy.Status(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, err.Error())
		return
	}
	next := egressproxy.Setting{Address: in.Address, Username: in.Username, Password: in.Password, Profiles: in.Profiles}
	if err := h.proxy.Save(r.Context(), next); err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	if p := authz.FromContext(r.Context()); h.audit != nil && p != nil {
		_ = h.audit.Write(r.Context(), audit.Event{
			ActorUserID: &p.UserID, ActorLabel: p.Username,
			Action: audit.ActionEgressProxyChanged, TargetKind: "egress", TargetID: "proxy",
			SourceIP: ip, UserAgent: ua,
			Detail: "from " + orNone(before.Stored.Address) + " to " + orNone(next.Address),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"applies_on_restart": true})
}

func orNone(address string) string {
	if address == "" {
		return "(none)"
	}
	return address
}

// Restart stops the process gracefully; its supervisor starts it again.
func (h *Handlers) Restart(w http.ResponseWriter, r *http.Request) {
	if h.restart == nil {
		writeProblem(w, http.StatusNotImplemented, "this process cannot restart itself")
		return
	}
	if p := authz.FromContext(r.Context()); h.audit != nil && p != nil {
		_ = h.audit.Write(r.Context(), audit.Event{
			ActorUserID: &p.UserID, ActorLabel: p.Username, Action: audit.ActionSystemRestarted,
			SourceIP: ClientIP(r.Context()), UserAgent: r.UserAgent(),
		})
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	h.restart()
}

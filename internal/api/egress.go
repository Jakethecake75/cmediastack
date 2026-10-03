package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/egress"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// EgressController is the subset of the egress guard this package needs.
//
// An interface rather than the concrete type so the handlers can be tested
// without a real tunnel, and so nothing here can reach a dialer: the admin
// surface reports on egress policy, it does not make connections.
type EgressController interface {
	Healthy() (bool, string, time.Time)
	Enforcing() bool
	Profiles() map[string]egress.Profile
	DependsOnTunnel(subsystem string) bool
	Verify(wantInterface string) (egress.Verification, error)
	TunnelInterface() string
}

// EgressStatus reports whether the tunnel is up and what the policy is.
func (h *Handlers) EgressStatus(w http.ResponseWriter, r *http.Request) {
	if h.egress == nil {
		writeProblem(w, http.StatusNotImplemented, "no egress guard is wired")
		return
	}

	healthy, detail, since := h.egress.Healthy()

	names := make([]string, 0)
	for name := range h.egress.Profiles() {
		names = append(names, name)
	}
	sort.Strings(names)

	profiles := make([]map[string]any, 0, len(names))
	all := h.egress.Profiles()
	for _, name := range names {
		p := all[name]
		profiles = append(profiles, map[string]any{
			"subsystem": name,
			"mode":      string(p.Mode),
			// Address and username identify the proxy, which an administrator
			// managing it needs. The password is never in Profiles() at all —
			// the guard strips it before it gets here.
			"address":               p.Address,
			"username":              p.Username,
			"remote_dns":            p.RemoteDNS,
			"blocks_private_ips":    p.DenyPrivate,
			"pauses_without_tunnel": h.egress.DependsOnTunnel(name),
		})
	}

	body := map[string]any{
		"healthy":          healthy,
		"detail":           detail,
		"since":            since,
		"enforcing":        h.egress.Enforcing(),
		"tunnel_interface": h.egress.TunnelInterface(),
		"profiles":         profiles,
		"note": "Egress policy is set in the configuration file and cannot be changed at runtime. " +
			"See PATCH /api/v1/admin/egress.",
	}
	if !h.egress.Enforcing() {
		// Said in words, not only as a boolean beside a green "healthy". An
		// operator glancing at this page must not come away believing their
		// downloads are tunnelled when nothing is checking.
		body["warning"] = "Egress is NOT being enforced: anonymity_enabled is false in the " +
			"configuration, so no tunnel is required and downloads are not paused if one is absent."
	}
	writeJSON(w, http.StatusOK, body)
}

// EgressUpdate refuses, deliberately and permanently.
//
// A runtime switch for the control that stops the download engine leaking is
// the single most valuable thing an attacker who reaches an admin session could
// flip, and it would leave no artefact beyond an audit line. Keeping the policy
// in the config file means changing it requires filesystem access and a
// restart, it is reviewable in a diff, and the security lint gets to refuse an
// unsafe combination before the process starts.
//
// This is 409 rather than 501: the endpoint is not unfinished, it is closed.
func (h *Handlers) EgressUpdate(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, http.StatusConflict,
		"egress policy is configuration, not runtime state: edit egress.* in the config file "+
			"and restart. A runtime toggle for the control that prevents leaks would be the "+
			"first thing an attacker with an admin session would reach for.")
}

// EgressLeakTest re-verifies routing on demand and records the result.
//
// It contacts nothing outside the instance. The check asks the kernel which
// interface it would use for a public destination and which address it would
// use as the source; no packet is sent, so running this does not phone home and
// does not depend on the tunnel being up to produce an answer.
//
// It does NOT prove the absence of a leak — nothing running inside the jail
// can, since a leak by definition takes a path this process cannot observe.
// What it proves is that routing is what the operator configured. The firewall
// in ADR-0001 is what makes the stronger claim, and it is outside this binary.
func (h *Handlers) EgressLeakTest(w http.ResponseWriter, r *http.Request) {
	if h.egress == nil {
		writeProblem(w, http.StatusNotImplemented, "no egress guard is wired")
		return
	}
	p := authz.FromContext(r.Context())

	want := h.egress.TunnelInterface()
	v, err := h.egress.Verify(want)

	outcome := audit.OutcomeSuccess
	if err != nil {
		outcome = audit.OutcomeFailure
	}
	if h.audit != nil && p != nil {
		_ = h.audit.Write(r.Context(), audit.Event{
			ActorUserID: &p.UserID,
			ActorLabel:  p.Username,
			Action:      audit.ActionEgressChanged,
			Outcome:     outcome,
			TargetKind:  "egress",
			TargetID:    "leak-test",
			SourceIP:    ClientIP(r.Context()),
			UserAgent:   r.UserAgent(),
			Detail:      v.Detail,
		})
	}

	// 200 either way: the test ran and produced an answer. A failing result is
	// data, not an HTTP error — an operator running a leak test wants the
	// verdict in the body, and a 500 would be indistinguishable from the
	// endpoint itself being broken.
	writeJSON(w, http.StatusOK, map[string]any{
		"routes_through_tunnel": v.Jailed,
		"expected_interface":    want,
		"actual_interface":      v.Interface,
		"source_ip":             v.SourceIP,
		"default_routes":        v.DefaultRoutes,
		"detail":                v.Detail,
		"caveat": "This proves routing matches the configuration. It cannot prove the absence " +
			"of a leak: a leak takes a path this process cannot observe. The namespace firewall " +
			"is what makes that guarantee, and it lives outside this binary.",
	})
}

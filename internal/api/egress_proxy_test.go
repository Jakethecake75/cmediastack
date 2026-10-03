package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// A SOCKS5 proxy set from the web, and a restart to apply it (ADR-0065).

func proxyBody(r *rig, password, code string) map[string]any {
	return map[string]any{
		"address": "amsterdam.nl.socks.nordhold.net:1080", "username": "svc-user", "password": "svc-pass",
		"profiles": []string{"download"}, "current_password": password, "code": code,
	}
}

// freshCode moves past the step the enrollment used and returns a code for
// the new one.
func (r *rig) freshCode() string {
	r.t.Helper()
	r.clk.advance(31 * time.Second)
	return r.mustCode(r.adminSecret)
}

func TestAProxyIsSavedOnlyWithThePasswordAndACode(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	for name, body := range map[string]map[string]any{
		"no password": proxyBody(r, "", r.freshCode()),
		"wrong code":  proxyBody(r, "correct-horse-battery", "000000"),
		"no code":     proxyBody(r, "correct-horse-battery", ""),
	} {
		if res := admin.do(http.MethodPut, "/api/v1/admin/egress/proxy", body); res.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", name, res.Code, res.Raw)
		}
	}
	if st, _ := r.proxy.Status(t.Context()); st.Stored.Address != "" {
		t.Fatalf("a refused change was stored: %+v", st.Stored)
	}

	res := admin.do(http.MethodPut, "/api/v1/admin/egress/proxy", proxyBody(r, "correct-horse-battery", r.freshCode()))
	if res.Code != http.StatusOK || res.Body["applies_on_restart"] != true {
		t.Fatalf("the right password and code: %d %s", res.Code, res.Raw)
	}
	if st, _ := r.proxy.Status(t.Context()); st.Stored.Address != "amsterdam.nl.socks.nordhold.net:1080" {
		t.Errorf("not stored: %+v", st.Stored)
	}
}

func TestAProxyTheLintRefusesIsRefusedWithItsWords(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	body := proxyBody(r, "correct-horse-battery", r.freshCode())
	body["profiles"] = []string{"indexer"}
	res := admin.do(http.MethodPut, "/api/v1/admin/egress/proxy", body)
	if res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Raw, "metadata") {
		t.Errorf("%d %s, want 422 naming metadata", res.Code, res.Raw)
	}
}

func TestAProxyChangeIsAuditedWithoutItsPassword(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	if res := admin.do(http.MethodPut, "/api/v1/admin/egress/proxy",
		proxyBody(r, "correct-horse-battery", r.freshCode())); res.Code != http.StatusOK {
		t.Fatalf("%d %s", res.Code, res.Raw)
	}
	var n int
	var detail string
	if err := r.database.QueryRowContext(t.Context(), `SELECT COUNT(*), MAX(detail) FROM audit_event WHERE action = ?`,
		audit.ActionEgressProxyChanged).Scan(&n, &detail); err != nil {
		t.Fatal(err)
	}
	if n != 1 || !strings.Contains(detail, "(none)") || !strings.Contains(detail, "nordhold.net:1080") {
		t.Errorf("%d lines, detail %q: want one naming the old and new address", n, detail)
	}
	var leaks int
	if err := r.database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_event WHERE detail LIKE '%svc-pass%'
		OR detail LIKE '%correct-horse%'`).Scan(&leaks); err != nil {
		t.Fatal(err)
	}
	if leaks != 0 {
		t.Errorf("%d audit lines carry a password", leaks)
	}
}

func TestTheEgressStatusShowsTheProxyAndNeverItsPassword(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	if res := admin.do(http.MethodPut, "/api/v1/admin/egress/proxy",
		proxyBody(r, "correct-horse-battery", r.freshCode())); res.Code != http.StatusOK {
		t.Fatalf("%d %s", res.Code, res.Raw)
	}
	res := admin.get("/api/v1/admin/egress")
	proxy, _ := res.Body["proxy"].(map[string]any)
	stored, _ := proxy["stored"].(map[string]any)
	if stored["address"] != "amsterdam.nl.socks.nordhold.net:1080" || proxy["differs"] != true {
		t.Errorf("proxy status %v", proxy)
	}
	if strings.Contains(res.Raw, "svc-pass") {
		t.Errorf("the status carries the password: %s", res.Raw)
	}
}

func TestRestartIsAuditedAndCallsRestart(t *testing.T) {
	r := newRig(t)
	if res := r.client().post("/api/v1/admin/system/restart", nil); res.Code == http.StatusAccepted || *r.restarts != 0 {
		t.Errorf("anonymous: %d, %d restarts", res.Code, *r.restarts)
	}
	admin := r.bootstrapAdmin()
	if res := admin.post("/api/v1/admin/system/restart", nil); res.Code != http.StatusAccepted {
		t.Fatalf("%d %s, want 202", res.Code, res.Raw)
	}
	if *r.restarts != 1 {
		t.Errorf("%d restarts, want 1", *r.restarts)
	}
	var n int
	if err := r.database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_event WHERE action = ?`,
		audit.ActionSystemRestarted).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d restart lines: %v", n, err)
	}
}

func TestTheProxyRouteIsHiddenAndRefusesAPITokens(t *testing.T) {
	r := newRig(t)
	for _, route := range r.rt.Routes() {
		if route.Pattern == "/api/v1/admin/egress/proxy" {
			if !route.Hidden || !route.SessionOnly {
				t.Errorf("%+v: want hidden and session-only", route)
			}
			return
		}
	}
	t.Error("the proxy route is not registered")
}

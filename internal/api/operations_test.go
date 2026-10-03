package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/egress"
	"github.com/jakethecake75/cmediastack/internal/platform/backup"
	"github.com/jakethecake75/cmediastack/internal/platform/config"
	"github.com/jakethecake75/cmediastack/internal/platform/logging"
	"github.com/jakethecake75/cmediastack/internal/platform/tasks"
)

type opsTasks struct{ statuses []tasks.Status }

func (f opsTasks) Snapshot() []tasks.Status { return f.statuses }
func (f opsTasks) Trigger(context.Context, string) (tasks.Outcome, error) {
	return tasks.Outcome{}, nil
}

type opsBackups struct {
	st  backup.Status
	err error
}

func (f opsBackups) Take(context.Context, string, string) (backup.Taken, error) {
	return backup.Taken{}, nil
}
func (f opsBackups) Status(context.Context) (backup.Status, error) { return f.st, f.err }

type opsEgress struct {
	enforcing, healthy bool
}

func (f opsEgress) Healthy() (bool, string, time.Time) {
	return f.healthy, "wg0 route", time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
}
func (f opsEgress) Enforcing() bool                     { return f.enforcing }
func (f opsEgress) Profiles() map[string]egress.Profile { return nil }
func (f opsEgress) DependsOnTunnel(string) bool         { return false }
func (f opsEgress) TunnelInterface() string             { return "wg0" }
func (f opsEgress) Verify(string) (egress.Verification, error) {
	return egress.Verification{}, nil
}

func serveJSON(t *testing.T, h http.HandlerFunc, target string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// ADR-0040, decision 1.
func TestHealthDetailSaysWhatIsWrong(t *testing.T) {
	w := newScopeWorld(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	parts := &HealthParts{
		Version: "1.2.3", Started: now.Add(-time.Hour), Now: func() time.Time { return now },
		Schema:           func(context.Context) (int, error) { return 23, nil },
		SandboxAvailable: true,
		Space: func(path string) (int64, int64, error) {
			if strings.Contains(path, "films") {
				return 2 << 30, 100 << 30, nil // 2 %, 2 GiB: low
			}
			return 50 << 30, 100 << 30, nil
		},
	}
	fresh := opsBackups{st: backup.Status{Interval: 24 * time.Hour,
		Backups: []backup.Backup{{TakenAt: now.Add(-3 * time.Hour)}}}}
	h := &Handlers{health: parts, roots: w.r.roots, egress: opsEgress{},
		tasks:   opsTasks{statuses: []tasks.Status{{Name: "database.backup", History: []tasks.Outcome{{}}}}},
		backups: fresh}

	states := func(body map[string]any) map[string]string {
		out := map[string]string{}
		for _, c := range body["checks"].([]any) {
			m := c.(map[string]any)
			out[m["name"].(string)] = m["state"].(string)
		}
		return out
	}
	// Signed in as the administrator, so the root folders can be listed.
	req := func(h *Handlers) (int, map[string]any) {
		rec := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(authz.WithPrincipal(t.Context(), adminPrincipalCtx(t)), http.MethodGet, "/health/detail", nil)
		h.HealthDetail(rec, r)
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body
	}

	code, body := req(h)
	got := states(body)
	if code != http.StatusOK || body["version"] != "1.2.3" || body["schema_version"] != float64(23) ||
		body["uptime_seconds"] != float64(3600) {
		t.Fatalf("report: %d %v", code, body)
	}
	if got["database"] != "ok" || got["egress"] != "ok" || got["tasks"] != "ok" || got["backups"] != "ok" ||
		got["media parser sandbox"] != "ok" || got["storage: Kids"] != "ok" || got["storage: Films"] != "warn" {
		t.Errorf("checks %v", got)
	}
	if body["state"] != "warn" {
		t.Errorf("overall %v, want warn: the worst check", body["state"])
	}

	// Everything that can go wrong.
	parts.Schema = func(context.Context) (int, error) { return 0, errors.New("disk I/O error") }
	parts.SandboxAvailable = false
	parts.Space = func(string) (int64, int64, error) { return 0, 0, errors.New("no such file or directory") }
	h.egress = opsEgress{enforcing: true}
	h.tasks = opsTasks{statuses: []tasks.Status{{Name: "library.import", History: []tasks.Outcome{{}, {Err: "boom"}}}}}
	h.backups = opsBackups{st: backup.Status{Interval: 24 * time.Hour,
		Backups: []backup.Backup{{TakenAt: now.Add(-72 * time.Hour)}}}}
	_, body = req(h)
	got = states(body)
	if got["database"] != "fail" || got["egress"] != "fail" || got["tasks"] != "warn" || got["backups"] != "warn" ||
		got["media parser sandbox"] != "warn" || got["storage: Kids"] != "fail" || body["state"] != "fail" {
		t.Errorf("a broken instance: %v, %v", got, body["state"])
	}
	if !strings.Contains(string(mustJSON(t, body)), "library.import") {
		t.Errorf("the failing task is not named: %v", body)
	}
	h.backups = opsBackups{}
	if _, body := req(h); states(body)["backups"] != "warn" {
		t.Errorf("no backup at all: %v", states(body))
	}

	// Hidden from everybody but the administrator.
	if res := w.kid.get("/health/detail"); res.Code != http.StatusNotFound {
		t.Errorf("a Manager reached it: %d", res.Code)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ADR-0040, decision 2.
func TestRecentLogsAreReadable(t *testing.T) {
	ring := logging.NewRing(50)
	log := logging.New(io.Discard, logging.Options{Level: "debug", Ring: ring})
	log.Info("scan finished", slog.String("root", "Kids"))
	log.Warn("disk nearly full", slog.String("root", "Films"))
	log.Info("signed in", slog.String("password", "hunter2"))
	h := &Handlers{logs: ring}

	code, body := serveJSON(t, h.SystemLogs, "/api/v1/admin/system/logs")
	recs := body["records"].([]any)
	if code != http.StatusOK || len(recs) != 3 || recs[0].(map[string]any)["message"] != "signed in" {
		t.Fatalf("logs: %d %v", code, body)
	}
	if strings.Contains(string(mustJSON(t, body)), "hunter2") {
		t.Error("a secret reached the logs route")
	}
	if _, body := serveJSON(t, h.SystemLogs, "/api/v1/admin/system/logs?level=warn"); body["count"] != float64(1) {
		t.Errorf("warn and above: %v", body)
	}
	if _, body := serveJSON(t, h.SystemLogs, "/api/v1/admin/system/logs?q=kids"); body["count"] != float64(1) {
		t.Errorf("text: %v", body)
	}
	if _, body := serveJSON(t, h.SystemLogs, "/api/v1/admin/system/logs?limit=2"); body["count"] != float64(2) {
		t.Errorf("limit: %v", body)
	}
	for _, q := range []string{"?level=loud", "?limit=0", "?limit=501"} {
		if code, _ := serveJSON(t, h.SystemLogs, "/api/v1/admin/system/logs"+q); code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, code)
		}
	}
}

// ADR-0040, decision 3.
func TestSettingsAreReadNotWritten(t *testing.T) {
	cfg := config.Default()
	cfg.Acquisition.Automatic = true
	h := &Handlers{settings: cfg.Document}
	code, body := serveJSON(t, h.SystemSettings, "/api/v1/admin/system/settings")
	settings, _ := body["settings"].(map[string]any)
	acq, _ := settings["acquisition"].(map[string]any)
	if code != http.StatusOK || acq["automatic"] != true {
		t.Fatalf("settings: %d %v", code, body)
	}
	if secrets, _ := settings["secrets"].(map[string]any); secrets["master_key_env"] != "CMS_MASTER_KEY" {
		t.Errorf("the master key is named by its variable: %v", settings["secrets"])
	}

	r := newRig(t)
	admin := r.bootstrapAdmin()
	if res := admin.do(http.MethodPatch, "/api/v1/admin/system/settings",
		map[string]any{"acquisition": map[string]any{"automatic": true}}); res.Code != http.StatusConflict ||
		!strings.Contains(res.Raw, "configuration file") {
		t.Errorf("a settings change: %d %s", res.Code, res.Raw)
	}
}

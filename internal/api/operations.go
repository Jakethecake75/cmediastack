package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/platform/logging"
)

// The operations routes (ADR-0040): every health check in one answer, the
// process's recent log records, and the settings, read and never written.

// HealthParts is what the health report needs that the other handlers do not
// already hold.
type HealthParts struct {
	Version string
	Started time.Time
	// Schema reads the database, answering its schema version.
	Schema func(ctx context.Context) (int, error)
	// SandboxAvailable is whether the media parser runs jailed (ADR-0020).
	SandboxAvailable bool
	// Space measures a root folder's filesystem, free and total bytes.
	Space func(path string) (free, total int64, err error)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// HealthCheck is one line of the report.
type HealthCheck struct {
	Name   string `json:"name"`
	State  string `json:"state"` // ok | warn | fail
	Detail string `json:"detail"`
}

// Health states, worst last.
const (
	healthOK   = "ok"
	healthWarn = "warn"
	healthFail = "fail"
)

func worse(a, b string) string {
	rank := map[string]int{healthOK: 0, healthWarn: 1, healthFail: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// lowSpace is where a root folder's free space becomes a warning: under 5 %
// of the filesystem, or under 10 GiB, whichever is smaller — a 20 TB array at
// 4 % still has room for a season, a 100 GB disk at 10 GiB does not have much.
func lowSpace(free, total int64) bool {
	const floor = 10 << 30
	if total <= 0 {
		return false
	}
	return free*100 < total*5 && free < floor
}

// HealthDetail answers every check at once.
func (h *Handlers) HealthDetail(w http.ResponseWriter, r *http.Request) {
	p := h.health
	if p == nil {
		writeProblem(w, http.StatusNotImplemented, "the health report is not wired")
		return
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	ctx := r.Context()
	var checks []HealthCheck
	add := func(name, state, detail string) {
		checks = append(checks, HealthCheck{Name: name, State: state, Detail: detail})
	}
	body := map[string]any{"version": p.Version, "uptime_seconds": int64(now().Sub(p.Started).Seconds())}

	if p.Schema != nil {
		if v, err := p.Schema(ctx); err != nil {
			add("database", healthFail, "the database could not be read: "+err.Error())
		} else {
			body["schema_version"] = v
			add("database", healthOK, fmt.Sprintf("readable, schema version %d", v))
		}
	}

	if h.egress != nil {
		healthy, detail, since := h.egress.Healthy()
		switch {
		case !h.egress.Enforcing():
			add("egress", healthOK, "enforcement is off: "+detail)
		case healthy:
			add("egress", healthOK, "the tunnel is verified: "+detail)
		default:
			add("egress", healthFail, fmt.Sprintf("enforced, and the tunnel is not verified since %s: %s",
				since.UTC().Format(time.RFC3339), detail))
		}
	}

	if h.tasks != nil {
		var failing []string
		for _, st := range h.tasks.Snapshot() {
			if n := len(st.History); n > 0 && !st.History[n-1].Succeeded() {
				failing = append(failing, st.Name)
			}
		}
		sort.Strings(failing)
		if len(failing) > 0 {
			add("tasks", healthWarn, "last run failed: "+strings.Join(failing, ", "))
		} else {
			add("tasks", healthOK, "every task's last run succeeded")
		}
	}

	if h.backups != nil {
		st, err := h.backups.Status(ctx)
		switch {
		case err != nil:
			add("backups", healthWarn, "the backups could not be read: "+err.Error())
		case len(st.Backups) == 0:
			add("backups", healthWarn, "there is no backup yet")
		case st.Interval > 0 && now().Sub(st.Backups[0].TakenAt) > 2*st.Interval:
			add("backups", healthWarn, fmt.Sprintf("the newest backup is %s old",
				now().Sub(st.Backups[0].TakenAt).Round(time.Hour)))
		default:
			add("backups", healthOK, fmt.Sprintf("the newest backup was taken %s",
				st.Backups[0].TakenAt.UTC().Format(time.RFC3339)))
		}
	}

	if h.roots != nil && p.Space != nil {
		roots, err := h.roots.List(ctx)
		if err != nil {
			add("storage", healthFail, "the root folders could not be read: "+err.Error())
		}
		for _, rf := range roots {
			name := "storage: " + rf.Label
			if rf.Label == "" {
				name = "storage: " + rf.Path
			}
			free, total, err := p.Space(rf.Path)
			switch {
			case err != nil:
				add(name, healthFail, "cannot be read — is the disk mounted? "+err.Error())
			case lowSpace(free, total):
				add(name, healthWarn, fmt.Sprintf("%.1f GiB free of %.1f GiB",
					float64(free)/(1<<30), float64(total)/(1<<30)))
			default:
				add(name, healthOK, fmt.Sprintf("%.1f GiB free of %.1f GiB",
					float64(free)/(1<<30), float64(total)/(1<<30)))
			}
		}
	}

	if p.SandboxAvailable {
		add("media parser sandbox", healthOK, "files are parsed inside the jail")
	} else {
		add("media parser sandbox", healthWarn,
			"not available: files are parsed without the jail (ADR-0020); check the seccomp profile")
	}

	if h.metadata != nil {
		if st, err := h.metadata.Status(ctx); err == nil && st.Configured {
			add("metadata", healthOK, "a provider is configured: "+st.Provider)
		} else {
			add("metadata", healthWarn, "no metadata provider is configured")
		}
	}

	overall := healthOK
	for _, c := range checks {
		overall = worse(overall, c.State)
	}
	body["state"] = overall
	body["checks"] = checks
	writeJSON(w, http.StatusOK, body)
}

// SystemLogs answers the process's recent log records, redacted.
func (h *Handlers) SystemLogs(w http.ResponseWriter, r *http.Request) {
	if h.logs == nil {
		writeProblem(w, http.StatusNotImplemented, "no log ring is wired")
		return
	}
	q := r.URL.Query()
	level := slog.LevelInfo
	switch strings.ToLower(q.Get("level")) {
	case "", "info":
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		writeProblem(w, http.StatusBadRequest, "level is debug, info, warn or error")
		return
	}
	limit := 200
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			writeProblem(w, http.StatusBadRequest, "limit is 1 to 500")
			return
		}
		limit = n
	}
	text := strings.TrimSpace(q.Get("q"))
	if len(text) > 200 {
		writeProblem(w, http.StatusBadRequest, "q is at most 200 characters")
		return
	}
	records := h.logs.Records(level, text, limit)
	out := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		attrs := make(map[string]string, len(rec.Attrs))
		for _, kv := range rec.Attrs {
			attrs[kv.Key] = kv.Value
		}
		out = append(out, map[string]any{"time": rec.Time, "level": strings.ToLower(rec.Level.String()),
			"message": rec.Message, "attrs": attrs})
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": out, "count": len(out),
		"kept": logging.RingSize,
		"note": "The most recent records since the process started, redacted as the log output is. " +
			"The host's log is the history."})
}

// SystemSettings answers the effective configuration. It holds no secret: the
// master key is named by the environment variable that holds it, and stored
// credentials are sealed in the database.
func (h *Handlers) SystemSettings(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		writeProblem(w, http.StatusNotImplemented, "the settings are not wired")
		return
	}
	doc, err := h.settings()
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": doc,
		"note": "Changed in the configuration file, then restart. The default quality profile, " +
			"the metadata key and notifications are runtime settings with screens of their own."})
}

// ChangeSystemSettings refuses, permanently, and says where a setting lives.
func (h *Handlers) ChangeSystemSettings(w http.ResponseWriter, r *http.Request) {
	writeProblem(w, http.StatusConflict,
		"settings are the configuration file, reviewed where they are written: edit it and restart. "+
			"A runtime switch for registration, automatic acquisition or egress would be the first "+
			"thing a stolen administrator session reached for (ADR-0040).")
}

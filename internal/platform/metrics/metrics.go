package metrics

import (
	"bytes"
	"net/http"
	"strconv"
	"time"
)

// Registry holds every metric the application exposes.
//
// Requirements §9: "Prometheus /metrics: queue depth, grab success rate, import
// failures, transcode sessions, proxy health, indexer latency/error rate, auth
// failures."
//
// Metrics for later phases are declared here and read zero until their
// subsystem exists. That is deliberate: a dashboard built today keeps working
// when Phase 2 lands, rather than breaking on a metric that suddenly appears.
// Each one says which phase feeds it.
//
// /metrics is served on the MANAGEMENT listener only. Metric names and label
// values leak operational shape — route names, indexer names, user counts — and
// none of that belongs on a public interface.
type Registry struct {
	reg *registry

	// --- HTTP -------------------------------------------------------------
	HTTPRequests *CounterVec // route, method, status
	HTTPDuration *HistogramVec

	// --- Authentication and authorization ---------------------------------
	AuthAttempts  *CounterVec // outcome: success|failure|throttled
	MFAAttempts   *CounterVec // outcome, method: totp|recovery
	AuthzDenials  *CounterVec // reason
	SessionsLive  *Gauge
	TokensLive    *Gauge
	SessionReuses *Counter

	// --- Accounts ---------------------------------------------------------
	PendingAccountRequests *Gauge
	AccountDecisions       *CounterVec // decision: approved|denied

	// --- Scheduled tasks --------------------------------------------------
	TaskRuns     *CounterVec // task, outcome
	TaskDuration *HistogramVec
	TaskLastRun  *GaugeVec

	// --- Declared for later phases ----------------------------------------
	QueueDepth       *Gauge
	GrabsTotal       *CounterVec
	ImportsTotal     *CounterVec
	TranscodeActive  *Gauge
	IndexerRequests  *CounterVec
	IndexerLatency   *HistogramVec
	ProxyHealthy     *Gauge
	KillSwitchEvents *Counter
}

// defaultBuckets are the latency buckets used for HTTP timing.
var defaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// New builds the registry.
func New() *Registry {
	r := newRegistry()
	m := &Registry{reg: r}

	m.HTTPRequests = r.counterVec("cms_http_requests_total",
		"HTTP requests by route, method and status.", "route", "method", "status")
	m.HTTPDuration = r.histogramVec("cms_http_request_duration_seconds",
		"HTTP request duration in seconds.", defaultBuckets, "route", "method")

	m.AuthAttempts = r.counterVec("cms_auth_attempts_total",
		"Password authentication attempts by outcome.", "outcome")
	m.MFAAttempts = r.counterVec("cms_mfa_attempts_total",
		"Second-factor attempts by outcome and method.", "outcome", "method")
	m.AuthzDenials = r.counterVec("cms_authz_denials_total",
		"Authorization denials by reason.", "reason")
	m.SessionsLive = r.gauge("cms_sessions_live",
		"Sessions that are neither revoked nor expired.")
	m.TokensLive = r.gauge("cms_api_tokens_live",
		"API tokens that are neither revoked nor expired.")
	m.SessionReuses = r.counter("cms_session_token_reuse_total",
		"Superseded session secrets presented after the rotation grace window. Any value above zero warrants investigation.")

	m.PendingAccountRequests = r.gauge("cms_account_requests_pending",
		"Account requests awaiting a decision.")
	m.AccountDecisions = r.counterVec("cms_account_decisions_total",
		"Account request decisions.", "decision")

	m.TaskRuns = r.counterVec("cms_task_runs_total",
		"Scheduled task runs by outcome.", "task", "outcome")
	m.TaskDuration = r.histogramVec("cms_task_duration_seconds",
		"Scheduled task duration in seconds.",
		[]float64{0.01, 0.1, 0.5, 1, 5, 15, 60, 300}, "task")
	m.TaskLastRun = r.gaugeVec("cms_task_last_run_timestamp_seconds",
		"Unix time of a task's last completion.", "task")

	m.QueueDepth = r.gauge("cms_download_queue_depth",
		"Download tasks not yet complete. Fed from Phase 2.")
	m.GrabsTotal = r.counterVec("cms_grabs_total",
		"Release grabs by outcome. Fed from Phase 2.", "outcome")
	m.ImportsTotal = r.counterVec("cms_imports_total",
		"Import jobs by outcome. Fed from Phase 3.", "outcome")
	m.TranscodeActive = r.gauge("cms_transcode_sessions_active",
		"Active transcode sessions. Fed from Phase 4.")
	m.IndexerRequests = r.counterVec("cms_indexer_requests_total",
		"Indexer requests by indexer and outcome. Fed from Phase 2.", "indexer", "outcome")
	m.IndexerLatency = r.histogramVec("cms_indexer_latency_seconds",
		"Indexer response latency in seconds. Fed from Phase 2.",
		[]float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30}, "indexer")
	m.ProxyHealthy = r.gauge("cms_egress_proxy_healthy",
		"1 when the download egress tunnel is verified healthy, 0 otherwise. Fed from Phase 2.")
	m.KillSwitchEvents = r.counter("cms_egress_kill_switch_total",
		"Times the egress kill switch paused transfers. Fed from Phase 2.")

	// Start with the tunnel reported DOWN rather than absent. An unset gauge
	// and a healthy gauge look identical on a dashboard, and this is the one
	// signal that must fail visibly rather than silently.
	m.ProxyHealthy.Set(0)

	return m
}

// Handler serves the text exposition format. Mount it on the management
// listener only.
func (m *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Buffered so a slow or disconnecting scraper cannot hold locks inside
		// the collectors while the application is trying to record into them.
		var buf bytes.Buffer
		m.reg.writeAll(&buf)

		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	})
}

// Render returns the exposition output, for tests and diagnostics.
func (m *Registry) Render() string {
	var buf bytes.Buffer
	m.reg.writeAll(&buf)
	return buf.String()
}

// ObserveHTTP records one request.
func (m *Registry) ObserveHTTP(route, method string, status int, d time.Duration) {
	m.HTTPRequests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	m.HTTPDuration.WithLabelValues(route, method).Observe(d.Seconds())
}

// ObserveTask records one scheduled-task run.
func (m *Registry) ObserveTask(task string, ok bool, d time.Duration, finished time.Time) {
	outcome := "success"
	if !ok {
		outcome = "failure"
	}
	m.TaskRuns.WithLabelValues(task, outcome).Inc()
	m.TaskDuration.WithLabelValues(task).Observe(d.Seconds())
	m.TaskLastRun.WithLabelValues(task).Set(float64(finished.Unix()))
}

// ---------------------------------------------------------------------------
// registry constructors
// ---------------------------------------------------------------------------

func (r *registry) counter(name, help string) *Counter {
	c := &Counter{nm: name, hlp: help}
	r.register(c)
	return c
}

func (r *registry) counterVec(name, help string, labels ...string) *CounterVec {
	v := &CounterVec{children: map[string]*Counter{}, nm: name, hlp: help, lbls: labels}
	r.register(v)
	return v
}

func (r *registry) gauge(name, help string) *Gauge {
	g := &Gauge{nm: name, hlp: help}
	r.register(g)
	return g
}

func (r *registry) gaugeVec(name, help string, labels ...string) *GaugeVec {
	v := &GaugeVec{children: map[string]*Gauge{}, nm: name, hlp: help, lbls: labels}
	r.register(v)
	return v
}

func (r *registry) histogramVec(name, help string, buckets []float64, labels ...string) *HistogramVec {
	v := &HistogramVec{
		children: map[string]*Histogram{},
		nm:       name, hlp: help, lbls: labels, buckets: buckets,
	}
	r.register(v)
	return v
}

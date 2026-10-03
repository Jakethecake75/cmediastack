package metrics

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests pin the exposition output. If the upstream Prometheus client is
// ever swapped in for this package's internals, these are what must keep
// passing.

func TestCounterAndGauge(t *testing.T) {
	m := New()

	m.AuthAttempts.WithLabelValues("failure").Inc()
	m.AuthAttempts.WithLabelValues("failure").Inc()
	m.AuthAttempts.WithLabelValues("success").Inc()
	m.SessionsLive.Set(3)
	m.PendingAccountRequests.Set(0)

	out := m.Render()

	for _, want := range []string{
		`cms_auth_attempts_total{outcome="failure"} 2`,
		`cms_auth_attempts_total{outcome="success"} 1`,
		`cms_sessions_live 3`,
		`cms_account_requests_pending 0`,
		"# TYPE cms_auth_attempts_total counter",
		"# TYPE cms_sessions_live gauge",
		"# HELP cms_sessions_live ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestCounterIgnoresNegativeDelta(t *testing.T) {
	m := New()
	m.SessionReuses.Add(5)
	m.SessionReuses.Add(-3) // must not decrease a counter
	if got := m.SessionReuses.Value(); got != 5 {
		t.Errorf("counter = %v, want 5 (a negative delta must be ignored)", got)
	}
}

func TestGaugeGoesUpAndDown(t *testing.T) {
	m := New()
	m.TranscodeActive.Inc()
	m.TranscodeActive.Inc()
	m.TranscodeActive.Dec()
	if got := m.TranscodeActive.Value(); got != 1 {
		t.Errorf("gauge = %v, want 1", got)
	}
}

func TestHistogramBucketsAreCumulative(t *testing.T) {
	m := New()
	h := m.HTTPDuration.WithLabelValues("GET /x", "GET")

	// Buckets include 0.01, 0.05, 0.1 ...
	h.Observe(0.001) // <= 0.005
	h.Observe(0.03)  // <= 0.05
	h.Observe(0.2)   // <= 0.25
	h.Observe(30)    // above every bound: +Inf only

	out := m.Render()

	// Cumulative: le="0.05" must include the 0.001 observation too.
	for _, want := range []string{
		`cms_http_request_duration_seconds_bucket{route="GET /x",method="GET",le="0.005"} 1`,
		`cms_http_request_duration_seconds_bucket{route="GET /x",method="GET",le="0.05"} 2`,
		`cms_http_request_duration_seconds_bucket{route="GET /x",method="GET",le="0.25"} 3`,
		`cms_http_request_duration_seconds_bucket{route="GET /x",method="GET",le="+Inf"} 4`,
		`cms_http_request_duration_seconds_count{route="GET /x",method="GET"} 4`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "cms_http_request_duration_seconds_sum") {
		t.Error("histogram has no _sum series")
	}
}

// A label value containing a quote or a newline must not be able to produce an
// unparseable line — or worse, inject a fabricated metric.
func TestLabelValuesAreEscaped(t *testing.T) {
	m := New()
	m.IndexerRequests.WithLabelValues(`bad"name`, "ok").Inc()
	m.IndexerRequests.WithLabelValues("line\nbreak", "ok").Inc()
	m.IndexerRequests.WithLabelValues(`back\slash`, "ok").Inc()

	out := m.Render()

	if strings.Contains(out, "line\nbreak\"") {
		t.Error("a raw newline reached the output")
	}
	for _, want := range []string{
		`indexer="bad\"name"`,
		`indexer="line\nbreak"`,
		`indexer="back\\slash"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing escaped label %q in:\n%s", want, out)
		}
	}

	// Every non-comment line must still parse as name{labels} value.
	lineRE := regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*(\{.*\})? -?[0-9eE+.\-]+|NaN|[+-]Inf$`)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !lineRE.MatchString(line) {
			t.Errorf("unparseable exposition line: %q", line)
		}
	}
}

// A wrong number of label values is a programming error, but it must not panic
// in a request path.
func TestWrongLabelCountDoesNotPanic(t *testing.T) {
	m := New()
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("a wrong label count panicked: %v", p)
		}
	}()

	m.AuthAttempts.WithLabelValues("a", "b", "c").Inc() // wants 1
	m.SessionsLive.Set(1)

	// The bogus series must not appear in the output.
	if strings.Contains(m.Render(), `outcome="a"`) {
		t.Error("a mislabelled series reached the output")
	}
}

func TestDuplicateMetricNamePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registering a duplicate metric name was permitted")
		}
	}()
	r := newRegistry()
	r.gauge("dup", "first")
	r.gauge("dup", "second")
}

func TestProxyHealthStartsDown(t *testing.T) {
	m := New()
	// An unset gauge and a healthy gauge look the same on a dashboard, so the
	// kill-switch signal must start at 0.
	if got := m.ProxyHealthy.Value(); got != 0 {
		t.Errorf("cms_egress_proxy_healthy = %v at startup, want 0", got)
	}
	if !strings.Contains(m.Render(), "cms_egress_proxy_healthy 0") {
		t.Error("the proxy health gauge is not exposed at startup")
	}
}

func TestHandlerServesTextFormat(t *testing.T) {
	m := New()
	m.ObserveHTTP("GET /api/v1/me", "GET", 200, 12*time.Millisecond)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `cms_http_requests_total{route="GET /api/v1/me",method="GET",status="200"} 1`) {
		t.Errorf("request counter missing:\n%s", body)
	}
}

func TestObserveTaskRecordsOutcomeAndTimestamp(t *testing.T) {
	m := New()
	finished := time.Unix(1_700_000_000, 0)

	m.ObserveTask("purge", true, 250*time.Millisecond, finished)
	m.ObserveTask("purge", false, time.Second, finished)

	out := m.Render()
	for _, want := range []string{
		`cms_task_runs_total{task="purge",outcome="success"} 1`,
		`cms_task_runs_total{task="purge",outcome="failure"} 1`,
		`cms_task_last_run_timestamp_seconds{task="purge"} 1.7e+09`,
		`cms_task_duration_seconds_count{task="purge"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// The whole point of a metrics package is that it is written to from every
// goroutine in the process.
func TestConcurrentUse(t *testing.T) {
	m := New()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.AuthAttempts.WithLabelValues("failure").Inc()
				m.SessionsLive.Inc()
				m.HTTPDuration.WithLabelValues("GET /x", "GET").Observe(0.01)
				_ = m.Render()
			}
		}()
	}
	wg.Wait()

	if got := m.AuthAttempts.WithLabelValues("failure").Value(); got != 5000 {
		t.Errorf("counter = %v, want 5000 (lost increments under concurrency)", got)
	}
}

func TestOutputIsStablyOrdered(t *testing.T) {
	m := New()
	m.AuthzDenials.WithLabelValues("missing_permission").Inc()
	m.AuthzDenials.WithLabelValues("anonymous").Inc()

	first := m.Render()
	for i := 0; i < 5; i++ {
		if m.Render() != first {
			t.Fatal("exposition output is not stably ordered between scrapes")
		}
	}

	// Sorted by label key, so "anonymous" precedes "missing_permission".
	anon := strings.Index(first, `reason="anonymous"`)
	miss := strings.Index(first, `reason="missing_permission"`)
	if anon < 0 || miss < 0 || anon > miss {
		t.Errorf("series are not sorted: anonymous at %d, missing at %d", anon, miss)
	}
}

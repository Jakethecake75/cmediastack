// Package metrics implements Prometheus instrumentation and the text
// exposition format.
//
// Requirements §9 asks for a Prometheus /metrics endpoint. This package
// provides one WITHOUT taking prometheus/client_golang as a dependency, which
// is a deliberate exception to §12's "wrap mature libraries" rule, on the same
// reasoning that put TOTP in-tree:
//
//  1. What Phase 1 needs is counters, gauges and fixed-bucket histograms
//     serialised as a stable, simple text format — not a hard problem, and one
//     whose correctness is fully verifiable by test.
//  2. The client library pulls a substantial transitive tree (protobuf,
//     procfs, common) into an application whose threat model names a
//     compromised dependency as a persona.
//  3. Decisively: this build environment cannot reach sum.golang.org, so that
//     dependency's checksums could not be verified against the transparency
//     log. Committing a go.sum nobody could verify, in a project whose
//     SECURITY.md promises pinned and verified dependencies, would make that
//     promise false.
//
// The format implemented is the Prometheus text exposition format, version
// 0.0.4, which is stable and which every scraper accepts.
//
// If you would rather have the upstream client, swapping it in is a contained
// change: only this package's internals move, and metrics_test.go documents the
// output that must not change.
package metrics

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// metricType is the exposed TYPE.
type metricType string

const (
	typeCounter   metricType = "counter"
	typeGauge     metricType = "gauge"
	typeHistogram metricType = "histogram"
)

// collector is anything that can write itself out.
type collector interface {
	name() string
	help() string
	kind() metricType
	write(w *bytes.Buffer)
}

// ---------------------------------------------------------------------------
// label handling
// ---------------------------------------------------------------------------

// escapeLabelValue applies the escaping the text format requires. Without it a
// label value containing a quote produces a line no scraper can parse, and one
// containing a newline can inject a fabricated metric.
func escapeLabelValue(v string) string {
	if !strings.ContainsAny(v, `\"`+"\n") {
		return v
	}
	var b strings.Builder
	for _, r := range v {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// formatLabels renders {a="1",b="2"} for parallel name/value slices.
func formatLabels(names, values []string, extra ...[2]string) string {
	if len(names) == 0 && len(extra) == 0 {
		return ""
	}
	parts := make([]string, 0, len(names)+len(extra))
	for i, n := range names {
		v := ""
		if i < len(values) {
			v = values[i]
		}
		parts = append(parts, n+`="`+escapeLabelValue(v)+`"`)
	}
	for _, e := range extra {
		parts = append(parts, e[0]+`="`+escapeLabelValue(e[1])+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// formatValue renders a float the way the text format expects.
func formatValue(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// labelKey joins label values into a map key. The separator is a byte that
// cannot appear in a label value's meaningful content, so two different label
// sets cannot collide into one series.
func labelKey(values []string) string { return strings.Join(values, "\x00") }

// ---------------------------------------------------------------------------
// Counter
// ---------------------------------------------------------------------------

// Counter is a monotonically increasing value.
type Counter struct {
	mu    sync.Mutex
	n     float64
	nm    string
	hlp   string
	lbls  []string
	lvals []string
}

// Inc adds one.
func (c *Counter) Inc() { c.Add(1) }

// Add increases the counter. A negative delta is ignored rather than panicking:
// a metrics bug must not take down the thing being measured.
func (c *Counter) Add(delta float64) {
	if delta < 0 {
		return
	}
	c.mu.Lock()
	c.n += delta
	c.mu.Unlock()
}

// Value returns the current count.
func (c *Counter) Value() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (c *Counter) name() string     { return c.nm }
func (c *Counter) help() string     { return c.hlp }
func (c *Counter) kind() metricType { return typeCounter }

func (c *Counter) write(w *bytes.Buffer) {
	fmt.Fprintf(w, "%s%s %s\n", c.nm, formatLabels(c.lbls, c.lvals), formatValue(c.Value()))
}

// ---------------------------------------------------------------------------
// Gauge
// ---------------------------------------------------------------------------

// Gauge is a value that can go up and down.
type Gauge struct {
	mu    sync.Mutex
	v     float64
	nm    string
	hlp   string
	lbls  []string
	lvals []string
}

// Set replaces the value.
func (g *Gauge) Set(v float64) {
	g.mu.Lock()
	g.v = v
	g.mu.Unlock()
}

// Add changes the value by delta.
func (g *Gauge) Add(delta float64) {
	g.mu.Lock()
	g.v += delta
	g.mu.Unlock()
}

// Inc adds one. Dec subtracts one.
func (g *Gauge) Inc() { g.Add(1) }

// Dec subtracts one.
func (g *Gauge) Dec() { g.Add(-1) }

// Value returns the current value.
func (g *Gauge) Value() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.v
}

func (g *Gauge) name() string     { return g.nm }
func (g *Gauge) help() string     { return g.hlp }
func (g *Gauge) kind() metricType { return typeGauge }

func (g *Gauge) write(w *bytes.Buffer) {
	fmt.Fprintf(w, "%s%s %s\n", g.nm, formatLabels(g.lbls, g.lvals), formatValue(g.Value()))
}

// ---------------------------------------------------------------------------
// Histogram
// ---------------------------------------------------------------------------

// Histogram counts observations into fixed buckets.
type Histogram struct {
	mu      sync.Mutex
	buckets []float64 // upper bounds, ascending
	counts  []uint64  // cumulative-per-bucket, non-cumulative internally
	sum     float64
	total   uint64

	nm    string
	hlp   string
	lbls  []string
	lvals []string
}

// Observe records one value.
func (h *Histogram) Observe(v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sum += v
	h.total++
	for i, bound := range h.buckets {
		if v <= bound {
			h.counts[i]++
			return
		}
	}
	// Above every bound: counted only in +Inf, which is h.total.
}

func (h *Histogram) name() string     { return h.nm }
func (h *Histogram) help() string     { return h.hlp }
func (h *Histogram) kind() metricType { return typeHistogram }

func (h *Histogram) write(w *bytes.Buffer) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Buckets are exposed cumulatively: le="x" is everything at or below x.
	var cumulative uint64
	for i, bound := range h.buckets {
		cumulative += h.counts[i]
		fmt.Fprintf(w, "%s_bucket%s %d\n", h.nm,
			formatLabels(h.lbls, h.lvals, [2]string{"le", formatValue(bound)}), cumulative)
	}
	fmt.Fprintf(w, "%s_bucket%s %d\n", h.nm,
		formatLabels(h.lbls, h.lvals, [2]string{"le", "+Inf"}), h.total)
	fmt.Fprintf(w, "%s_sum%s %s\n", h.nm, formatLabels(h.lbls, h.lvals), formatValue(h.sum))
	fmt.Fprintf(w, "%s_count%s %d\n", h.nm, formatLabels(h.lbls, h.lvals), h.total)
}

// ---------------------------------------------------------------------------
// Vectors
// ---------------------------------------------------------------------------

// CounterVec is a family of counters distinguished by label values.
type CounterVec struct {
	mu       sync.Mutex
	children map[string]*Counter
	nm       string
	hlp      string
	lbls     []string
}

// WithLabelValues returns the counter for these label values, creating it on
// first use.
//
// A wrong number of values would otherwise produce a silently mislabelled
// series, so the call is ignored and a throwaway counter returned instead of
// panicking: a metrics mistake must never break the request path.
func (v *CounterVec) WithLabelValues(values ...string) *Counter {
	if len(values) != len(v.lbls) {
		return &Counter{nm: v.nm, hlp: v.hlp}
	}
	key := labelKey(values)

	v.mu.Lock()
	defer v.mu.Unlock()
	if c, ok := v.children[key]; ok {
		return c
	}
	c := &Counter{nm: v.nm, hlp: v.hlp, lbls: v.lbls, lvals: append([]string(nil), values...)}
	v.children[key] = c
	return c
}

func (v *CounterVec) name() string     { return v.nm }
func (v *CounterVec) help() string     { return v.hlp }
func (v *CounterVec) kind() metricType { return typeCounter }

func (v *CounterVec) write(w *bytes.Buffer) {
	v.mu.Lock()
	keys := make([]string, 0, len(v.children))
	for k := range v.children {
		keys = append(keys, k)
	}
	children := v.children
	v.mu.Unlock()

	sort.Strings(keys)
	for _, k := range keys {
		children[k].write(w)
	}
}

// GaugeVec is a family of gauges.
type GaugeVec struct {
	mu       sync.Mutex
	children map[string]*Gauge
	nm       string
	hlp      string
	lbls     []string
}

// WithLabelValues returns the gauge for these label values.
func (v *GaugeVec) WithLabelValues(values ...string) *Gauge {
	if len(values) != len(v.lbls) {
		return &Gauge{nm: v.nm, hlp: v.hlp}
	}
	key := labelKey(values)

	v.mu.Lock()
	defer v.mu.Unlock()
	if g, ok := v.children[key]; ok {
		return g
	}
	g := &Gauge{nm: v.nm, hlp: v.hlp, lbls: v.lbls, lvals: append([]string(nil), values...)}
	v.children[key] = g
	return g
}

func (v *GaugeVec) name() string     { return v.nm }
func (v *GaugeVec) help() string     { return v.hlp }
func (v *GaugeVec) kind() metricType { return typeGauge }

func (v *GaugeVec) write(w *bytes.Buffer) {
	v.mu.Lock()
	keys := make([]string, 0, len(v.children))
	for k := range v.children {
		keys = append(keys, k)
	}
	children := v.children
	v.mu.Unlock()

	sort.Strings(keys)
	for _, k := range keys {
		children[k].write(w)
	}
}

// HistogramVec is a family of histograms.
type HistogramVec struct {
	mu       sync.Mutex
	children map[string]*Histogram
	nm       string
	hlp      string
	lbls     []string
	buckets  []float64
}

// WithLabelValues returns the histogram for these label values.
func (v *HistogramVec) WithLabelValues(values ...string) *Histogram {
	if len(values) != len(v.lbls) {
		return newHistogram(v.nm, v.hlp, v.buckets, nil, nil)
	}
	key := labelKey(values)

	v.mu.Lock()
	defer v.mu.Unlock()
	if h, ok := v.children[key]; ok {
		return h
	}
	h := newHistogram(v.nm, v.hlp, v.buckets, v.lbls, append([]string(nil), values...))
	v.children[key] = h
	return h
}

func (v *HistogramVec) name() string     { return v.nm }
func (v *HistogramVec) help() string     { return v.hlp }
func (v *HistogramVec) kind() metricType { return typeHistogram }

func (v *HistogramVec) write(w *bytes.Buffer) {
	v.mu.Lock()
	keys := make([]string, 0, len(v.children))
	for k := range v.children {
		keys = append(keys, k)
	}
	children := v.children
	v.mu.Unlock()

	sort.Strings(keys)
	for _, k := range keys {
		children[k].write(w)
	}
}

func newHistogram(name, help string, buckets []float64, lbls, lvals []string) *Histogram {
	sorted := append([]float64(nil), buckets...)
	sort.Float64s(sorted)
	return &Histogram{
		buckets: sorted,
		counts:  make([]uint64, len(sorted)),
		nm:      name, hlp: help, lbls: lbls, lvals: lvals,
	}
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

// registry holds collectors and serialises them.
type registry struct {
	mu         sync.Mutex
	collectors []collector
	names      map[string]bool
}

func newRegistry() *registry {
	return &registry{names: map[string]bool{}}
}

// register adds a collector, panicking on a duplicate name — a duplicate is a
// programming error that would produce an unparseable scrape.
func (r *registry) register(c collector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.names[c.name()] {
		panic("metrics: duplicate metric name " + c.name())
	}
	r.names[c.name()] = true
	r.collectors = append(r.collectors, c)
}

// writeAll serialises every collector in the text exposition format.
//
// Into a buffer, and typed as one: writing to memory cannot fail, so there is
// no error to lose, and the handler writes the finished buffer out once — a
// slow scraper never holds a collector's lock.
//
// Deliberately not named WriteTo: that name belongs to io.WriterTo, whose
// contract is (int64, error), and go vet is right to object to a method that
// borrows the name without the contract.
func (r *registry) writeAll(w *bytes.Buffer) {
	r.mu.Lock()
	cs := append([]collector(nil), r.collectors...)
	r.mu.Unlock()

	sort.Slice(cs, func(i, j int) bool { return cs[i].name() < cs[j].name() })

	for _, c := range cs {
		fmt.Fprintf(w, "# HELP %s %s\n", c.name(), strings.ReplaceAll(c.help(), "\n", " "))
		fmt.Fprintf(w, "# TYPE %s %s\n", c.name(), c.kind())
		c.write(w)
	}
}

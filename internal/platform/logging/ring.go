package logging

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// The process's recent log records, kept in memory for the administrator's
// logs screen (ADR-0040). The ring sits behind the redaction handler, so it
// holds exactly what the log output holds and nothing a mask removed.

// RingSize is how many records the ring keeps.
const RingSize = 1000

// Record is one log record as the logs screen shows it.
type Record struct {
	Time    time.Time
	Level   slog.Level
	Message string
	Attrs   []KV
}

// KV is one attribute, flattened: a group's attributes carry its name as a
// prefix, "group.key".
type KV struct {
	Key, Value string
}

// Ring is a bounded buffer of records, oldest overwritten first.
type Ring struct {
	mu   sync.Mutex
	buf  []Record
	next int
	full bool
}

// NewRing makes a ring that keeps n records.
func NewRing(n int) *Ring {
	if n <= 0 {
		n = RingSize
	}
	return &Ring{buf: make([]Record, n)}
}

func (r *Ring) add(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = rec
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// Records returns the records at or above a level whose message or attributes
// contain text (case-folded), newest first, at most limit.
func (r *Ring) Records(floor slog.Level, text string, limit int) []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := r.next
	if r.full {
		n = len(r.buf)
	}
	text = strings.ToLower(text)
	out := make([]Record, 0, min(limit, n))
	for i := 0; i < n && len(out) < limit; i++ {
		rec := r.buf[(r.next-1-i+len(r.buf))%len(r.buf)]
		if rec.Level < floor || (text != "" && !rec.matches(text)) {
			continue
		}
		out = append(out, rec)
	}
	return out
}

func (rec Record) matches(folded string) bool {
	if strings.Contains(strings.ToLower(rec.Message), folded) {
		return true
	}
	for _, kv := range rec.Attrs {
		if strings.Contains(strings.ToLower(kv.Key+"="+kv.Value), folded) {
			return true
		}
	}
	return false
}

// ringHandler records into a Ring.
type ringHandler struct {
	ring   *Ring
	level  slog.Leveler
	attrs  []KV
	prefix string
}

func (h *ringHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *ringHandler) Handle(_ context.Context, r slog.Record) error {
	rec := Record{Time: r.Time, Level: r.Level, Message: r.Message,
		Attrs: append([]KV(nil), h.attrs...)}
	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs = flatten(rec.Attrs, h.prefix, a)
		return true
	})
	h.ring.add(rec)
	return nil
}

func (h *ringHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append([]KV(nil), h.attrs...)
	for _, a := range attrs {
		next.attrs = flatten(next.attrs, h.prefix, a)
	}
	return &next
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	next := *h
	next.prefix = h.prefix + name + "."
	return &next
}

func flatten(out []KV, prefix string, a slog.Attr) []KV {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		for _, ga := range v.Group() {
			out = flatten(out, prefix+a.Key+".", ga)
		}
		return out
	}
	return append(out, KV{Key: prefix + a.Key, Value: v.String()})
}

// fanout hands a record to every handler that wants it.
type fanout []slog.Handler

func (f fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f fanout) Handle(ctx context.Context, r slog.Record) error {
	var first error
	for _, h := range f {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r.Clone()); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

func (f fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (f fanout) WithGroup(name string) slog.Handler {
	out := make(fanout, len(f))
	for i, h := range f {
		out[i] = h.WithGroup(name)
	}
	return out
}

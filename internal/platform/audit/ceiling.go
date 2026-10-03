package audit

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The ceiling on authz.denied rows (ADR-0031, decision 4).
//
// Any anonymous client can make the application write one: a request for a
// protected route without a session is answered 404 and recorded, with its
// address and user agent. That record is the point — probing of the
// administration routes shows there — but without a ceiling a scanner writes a
// row per request, fills the disk, rides along in every backup, and is all the
// audit screen shows. So denials are written one by one up to a ceiling per
// hour, counted beyond it, and the count written as one line once the hour is
// over.
const (
	// AnonymousPerSourcePerHour is how many denials an hour are written for
	// one address with no usable session.
	AnonymousPerSourcePerHour = 20
	// AnonymousPerHour is how many are written an hour for every address
	// together.
	AnonymousPerHour = 120
	// PersonPerHour is how many are written an hour for one signed-in account.
	// A person's denials are never cut by the anonymous ceiling, so a flood
	// cannot be used to hide an account probing the administration routes.
	PersonPerHour = 60

	// maxTrackedSources bounds the memory the counting takes. Sources beyond
	// it are counted together as "other".
	maxTrackedSources = 4096
	// summaryTopSources is how many sources the summary line names.
	summaryTopSources = 10

	// systemLabel is who writes the summary line.
	systemLabel = "system:audit"
)

// denialWindow is one hour's counting.
type denialWindow struct {
	// start is the hour; since, when the counting in it began — the hour
	// itself, or later when the process started during it.
	start, since time.Time
	// addresses counts every anonymous denial this hour, per address; people,
	// every denial of a signed-in account, per account. They are counted apart
	// so that a flood from many addresses cannot use up the tracking that
	// holds an account to its own ceiling.
	addresses map[string]int
	people    map[string]int
	// anonymous counts the denials written this hour under the ceiling for
	// every address together.
	anonymous int
	// suppressed counts what was not written, per source; other, from sources
	// beyond maxTrackedSources.
	suppressed map[string]int
	other      int
}

func (w *denialWindow) reset(start, since time.Time) {
	*w = denialWindow{start: start, since: since, addresses: map[string]int{}, people: map[string]int{},
		suppressed: map[string]int{}}
}

func (w *denialWindow) total() int {
	n := w.other
	for _, c := range w.suppressed {
		n += c
	}
	return n
}

// admitDenial says whether a denial is written, and hands back the summary of
// an hour that has just ended, for the caller to write.
func (l *Logger) admitDenial(label, clientIP string, person bool) (bool, []Event) {
	l.mu.Lock()
	defer l.mu.Unlock()

	finished := l.rollLocked(l.now(), false)
	w := &l.denials

	counts, source := w.addresses, clientIP
	if person {
		counts, source = w.people, label
	}
	if source == "" {
		source = "an unknown address"
	}

	n, tracked := counts[source]
	if !tracked && len(counts) < maxTrackedSources {
		tracked = true
	}
	if tracked {
		counts[source] = n + 1
	}

	var write bool
	switch {
	case person && tracked:
		// Never cut by the anonymous ceiling.
		write = n < PersonPerHour
	default:
		// An address; or anything past the tracking bound, which cannot be
		// told apart and so is held to the ceiling for every address together.
		write = n < AnonymousPerSourcePerHour && w.anonymous < AnonymousPerHour
		if write {
			w.anonymous++
		}
	}
	if !write {
		if tracked {
			w.suppressed[source]++
		} else {
			w.other++
		}
	}
	return write, finished
}

// rollLocked starts a new hour when the clock has moved into one, returning
// the summary of the hour that ended, if anything in it was not written. With
// stopping, the hour in progress is summarised too — for a server that is
// stopping — and the counting starts again.
func (l *Logger) rollLocked(now time.Time, stopping bool) []Event {
	now = now.UTC()
	hour := now.Truncate(time.Hour)
	w := &l.denials
	if w.start.IsZero() {
		// The first count since the process started: it began now, not at
		// the top of the hour.
		w.reset(hour, now)
		return nil
	}
	ended := hour.After(w.start)
	if !ended && !stopping {
		return nil
	}

	var out []Event
	if total := w.total(); total > 0 {
		out = append(out, suppressedEvent(w, now, !ended, total))
	}
	if ended {
		w.reset(hour, hour)
	} else {
		w.reset(hour, now)
	}
	return out
}

// suppressedEvent is the line that says what was not written.
func suppressedEvent(w *denialWindow, now time.Time, partial bool, total int) Event {
	type count struct {
		source string
		n      int
	}
	counts := make([]count, 0, len(w.suppressed))
	for s, n := range w.suppressed {
		counts = append(counts, count{s, n})
	}
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].n != counts[j].n {
			return counts[i].n > counts[j].n
		}
		return counts[i].source < counts[j].source
	})

	var parts []string
	rest, restN := 0, w.other
	for i, c := range counts {
		if i < summaryTopSources {
			parts = append(parts, fmt.Sprintf("%s ×%d", c.source, c.n))
			continue
		}
		rest++
		restN += c.n
	}
	switch {
	case w.other > 0:
		// Sources past the tracking bound were not told apart: how many
		// there were is not known.
		parts = append(parts, fmt.Sprintf("other sources ×%d", restN))
	case rest > 0:
		parts = append(parts, fmt.Sprintf("%d other source(s) ×%d", rest, restN))
	}

	// The span the counting covered: from when it began in the hour — the top
	// of it, or when the process started — to the end of the hour, or to the
	// server stopping.
	span := fmt.Sprintf("from %s to %s UTC", w.since.Format("2006-01-02 15:04"),
		w.start.Add(time.Hour).Format("15:04"))
	if partial {
		span = fmt.Sprintf("from %s UTC until the server stopped at %s", w.since.Format("2006-01-02 15:04"),
			now.Format("15:04"))
	}
	return Event{
		ActorLabel: systemLabel,
		Action:     ActionAuthzDeniedSuppressed,
		Outcome:    OutcomeDenied,
		TargetKind: "hour",
		TargetID:   w.start.Format(time.RFC3339),
		Detail: fmt.Sprintf("%d denial(s) %s were over the ceiling and not written one by one "+
			"(at most %d an hour from one address, %d an hour from every address together, "+
			"and %d an hour from one account): %s",
			total, span, AnonymousPerSourcePerHour, AnonymousPerHour, PersonPerHour,
			strings.Join(parts, ", ")),
	}
}

// FlushDenials writes the count of denials that were over the ceiling in an
// hour that has ended — or, when stopping, in the hour so far. It reports how
// many lines it wrote: none or one.
//
// The scheduled task audit.denials calls it every few minutes, so a scanner
// that stops does not leave its count unwritten until the next denial.
func (l *Logger) FlushDenials(ctx context.Context, stopping bool) (int, error) {
	l.mu.Lock()
	finished := l.rollLocked(l.now(), stopping)
	l.mu.Unlock()

	for i, e := range finished {
		if err := l.Write(ctx, e); err != nil {
			return i, err
		}
	}
	return len(finished), nil
}

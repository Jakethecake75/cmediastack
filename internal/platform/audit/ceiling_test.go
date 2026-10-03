package audit

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// A logger on a clock the test moves.
type clockedLogger struct {
	*Logger
	mu sync.Mutex
	t  time.Time
}

func newClockedLogger(t *testing.T, start time.Time) *clockedLogger {
	t.Helper()
	d, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "audit.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := &clockedLogger{t: start}
	c.Logger = New(d, func() time.Time {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.t
	})
	return c
}

func (c *clockedLogger) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// anonymous is a request for a protected route with no session.
func (c *clockedLogger) anonymous(ip string) {
	c.AuthzDenied(context.Background(), "GET /api/v1/admin/users",
		&authz.Denial{Reason: authz.ReasonAnonymous, Detail: "no session presented"}, ip, "scanner/1.0")
}

// person is a signed-in account refused a route.
func (c *clockedLogger) person(id int64) {
	c.AuthzDenied(context.Background(), "GET /api/v1/admin/users",
		&authz.Denial{Actor: id, Reason: authz.ReasonMissingPerm, Detail: `role "User" lacks "admin.users"`},
		"192.0.2.7", "firefox")
}

func (c *clockedLogger) count(t *testing.T, action Action, where string, args ...any) int {
	t.Helper()
	var n int
	q := `SELECT COUNT(*) FROM audit_event WHERE action = ?`
	if where != "" {
		q += " AND " + where
	}
	if err := c.db.QueryRowContext(context.Background(), q, append([]any{string(action)}, args...)...).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (c *clockedLogger) suppressed(t *testing.T) []Event {
	t.Helper()
	got, err := c.List(adminCtx(), Query{Action: ActionAuthzDeniedSuppressed})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

var hour0 = time.Date(2026, 9, 27, 14, 5, 0, 0, time.UTC)

// One address can add at most twenty rows an hour; the rest are counted, and
// the count is written as one line when the hour is over.
func TestOneAddressAddsAtMostItsCeilingAnHour(t *testing.T) {
	c := newClockedLogger(t, hour0)
	for i := 0; i < 25; i++ {
		c.anonymous("203.0.113.9")
	}
	if n := c.count(t, ActionAuthzDenied, ""); n != AnonymousPerSourcePerHour {
		t.Fatalf("%d denials written, want %d", n, AnonymousPerSourcePerHour)
	}
	if n, err := c.FlushDenials(context.Background(), false); err != nil || n != 0 {
		t.Fatalf("flushed %d (%v) with the hour still going", n, err)
	}

	c.advance(time.Hour)
	if n, err := c.FlushDenials(context.Background(), false); err != nil || n != 1 {
		t.Fatalf("flushed %d (%v) after the hour, want 1", n, err)
	}
	got := c.suppressed(t)
	if len(got) != 1 {
		t.Fatalf("%d summary lines", len(got))
	}
	s := got[0]
	if s.ActorLabel != "system:audit" || s.Outcome != OutcomeDenied || s.TargetKind != "hour" ||
		s.TargetID != "2026-09-27T14:00:00Z" ||
		!strings.Contains(s.Detail, "5 denial(s) from 2026-09-27 14:05 to 15:00 UTC") ||
		!strings.Contains(s.Detail, "203.0.113.9 ×5") {
		t.Fatalf("summary = %+v", s)
	}
	// Nothing twice.
	c.advance(time.Hour)
	if n, _ := c.FlushDenials(context.Background(), false); n != 0 {
		t.Fatalf("an empty hour wrote %d line(s)", n)
	}

	// And a new hour starts again from nothing.
	for i := 0; i < 3; i++ {
		c.anonymous("203.0.113.9")
	}
	if n := c.count(t, ActionAuthzDenied, ""); n != AnonymousPerSourcePerHour+3 {
		t.Fatalf("%d written after a new hour began", n)
	}
}

// Every address together can add at most the global ceiling — so a flood from
// many addresses is bounded too.
func TestEveryAddressTogetherHasACeiling(t *testing.T) {
	c := newClockedLogger(t, hour0)
	for a := 0; a < 10; a++ {
		for i := 0; i < 15; i++ {
			c.anonymous(fmt.Sprintf("198.51.100.%d", a))
		}
	}
	if n := c.count(t, ActionAuthzDenied, ""); n != AnonymousPerHour {
		t.Fatalf("%d written, want %d", n, AnonymousPerHour)
	}
	c.advance(time.Hour)
	c.anonymous("198.51.100.200") // the next denial writes the last hour's count
	got := c.suppressed(t)
	if len(got) != 1 || !strings.Contains(got[0].Detail, "30 denial(s) from 2026-09-27 14:05 to 15:00 UTC") {
		t.Fatalf("summary = %+v", got)
	}
	// And the hour after that is counted from its top.
	for i := 0; i < 25; i++ {
		c.anonymous("198.51.100.201")
	}
	c.advance(time.Hour)
	if _, err := c.FlushDenials(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if got := c.suppressed(t); len(got) != 2 || !strings.Contains(got[0].Detail, "5 denial(s) from 2026-09-27 15:00 to 16:00 UTC") {
		t.Fatalf("summary = %+v", got)
	}
}

// A signed-in account's denials are written whatever anonymous callers did,
// up to its own ceiling: a flood cannot hide an account probing the
// administration routes — nor, by filling the counting with addresses, free
// one from its ceiling.
func TestAnAccountIsNotHiddenByAFlood(t *testing.T) {
	c := newClockedLogger(t, hour0)
	for i := 0; i < maxTrackedSources+50; i++ {
		c.anonymous(fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255))
	}
	for i := 0; i < PersonPerHour+5; i++ {
		c.person(20)
	}
	if n := c.count(t, ActionAuthzDenied, "actor_user_id = ?", 20); n != PersonPerHour {
		t.Fatalf("%d of the account's denials written, want %d", n, PersonPerHour)
	}
	c.advance(time.Hour)
	if _, err := c.FlushDenials(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	got := c.suppressed(t)
	if len(got) != 1 || !strings.Contains(got[0].Detail, "user:20 ×5") {
		t.Fatalf("summary = %+v", got)
	}
}

// A stopping server writes the hour so far, rather than losing it.
func TestAStoppingServerWritesTheHourSoFar(t *testing.T) {
	c := newClockedLogger(t, hour0)
	for i := 0; i < 23; i++ {
		c.anonymous("203.0.113.9")
	}
	c.advance(17 * time.Minute)
	if n, err := c.FlushDenials(context.Background(), true); err != nil || n != 1 {
		t.Fatalf("flushed %d (%v)", n, err)
	}
	got := c.suppressed(t)
	// From when the counting began — the process's first denial at 14:05,
	// not the top of the hour — to the stop.
	if len(got) != 1 || !strings.Contains(got[0].Detail,
		"3 denial(s) from 2026-09-27 14:05 UTC until the server stopped at 14:22") {
		t.Fatalf("summary = %+v", got)
	}
	// An hour that ended before the stop is written as the hour it was.
	c3 := newClockedLogger(t, hour0)
	for i := 0; i < 23; i++ {
		c3.anonymous("203.0.113.9")
	}
	c3.advance(70 * time.Minute)
	if n, err := c3.FlushDenials(context.Background(), true); err != nil || n != 1 {
		t.Fatalf("flushed %d (%v)", n, err)
	}
	if got := c3.suppressed(t); len(got) != 1 || !strings.Contains(got[0].Detail, "3 denial(s) from 2026-09-27 14:05 to 15:00 UTC were") {
		t.Fatalf("summary = %+v", got)
	}
	// Nothing over the ceiling: nothing to write.
	c2 := newClockedLogger(t, hour0)
	c2.anonymous("203.0.113.9")
	if n, _ := c2.FlushDenials(context.Background(), true); n != 0 {
		t.Fatalf("wrote %d line(s) with nothing suppressed", n)
	}
}

// The counting is bounded: past the tracked sources, the rest are counted
// together and named as such, and the ceiling for every address still holds.
func TestTheCountingIsBounded(t *testing.T) {
	c := newClockedLogger(t, hour0)
	for i := 0; i < maxTrackedSources+50; i++ {
		c.anonymous(fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255))
	}
	if n := c.count(t, ActionAuthzDenied, ""); n != AnonymousPerHour {
		t.Fatalf("%d written", n)
	}
	c.advance(time.Hour)
	if _, err := c.FlushDenials(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	got := c.suppressed(t)
	if len(got) != 1 {
		t.Fatalf("%d summaries", len(got))
	}
	d := got[0].Detail
	total := maxTrackedSources + 50 - AnonymousPerHour
	if !strings.Contains(d, fmt.Sprintf("%d denial(s)", total)) || !strings.Contains(d, "other sources ×") ||
		strings.Count(d, "×") != summaryTopSources+1 {
		t.Fatalf("summary = %s", d)
	}
	c.Logger.mu.Lock()
	addresses, suppressed := len(c.denials.addresses), len(c.denials.suppressed)
	c.Logger.mu.Unlock()
	if addresses > maxTrackedSources || suppressed > maxTrackedSources {
		t.Fatalf("%d addresses and %d counts held in memory", addresses, suppressed)
	}
}

// Denials from many goroutines at once are counted exactly.
func TestTheCeilingHoldsUnderConcurrency(t *testing.T) {
	c := newClockedLogger(t, hour0)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				c.anonymous("203.0.113.9")
			}
		}()
	}
	wg.Wait()
	if n := c.count(t, ActionAuthzDenied, ""); n != AnonymousPerSourcePerHour {
		t.Fatalf("%d written under concurrency, want %d", n, AnonymousPerSourcePerHour)
	}
}

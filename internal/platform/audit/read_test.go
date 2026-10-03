package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// A page is the newest first, continues by id, and neither repeats nor skips a
// line — even when the log grows between pages.
func TestPagesContinueByIDWithoutRepeatingOrSkipping(t *testing.T) {
	c := newClockedLogger(t, hour0)
	ctx := context.Background()
	for i := 0; i < 250; i++ {
		if err := c.Write(ctx, Event{Action: ActionLoginFailed, Detail: fmt.Sprint("n", i)}); err != nil {
			t.Fatal(err)
		}
		c.advance(time.Second)
	}

	seen := map[int64]bool{}
	var order []int64
	before := int64(0)
	for page := 0; ; page++ {
		p, err := c.Page(adminCtx(), Filter{Before: before})
		if err != nil {
			t.Fatal(err)
		}
		if page == 0 && len(p.Records) != DefaultPageSize {
			t.Fatalf("first page holds %d", len(p.Records))
		}
		for _, r := range p.Records {
			if seen[r.ID] {
				t.Fatalf("line %d shown twice", r.ID)
			}
			seen[r.ID] = true
			order = append(order, r.ID)
		}
		if page == 0 {
			// The log grows between pages.
			for i := 0; i < 10; i++ {
				_ = c.Write(ctx, Event{Action: ActionLogout})
			}
		}
		if p.Next == 0 {
			break
		}
		before = p.Next
	}
	if len(order) != 250 {
		t.Fatalf("%d lines read across the pages, want the 250 there were when reading began", len(order))
	}
	if p, err := c.Page(adminCtx(), Filter{Limit: 1000}); err != nil || len(p.Records) != MaxPageSize {
		t.Fatalf("a page of 1000 asked for held %d (%v), want at most %d", len(p.Records), err, MaxPageSize)
	}
	for i := 1; i < len(order); i++ {
		if order[i] >= order[i-1] {
			t.Fatalf("not newest first at %d: %d then %d", i, order[i-1], order[i])
		}
	}
}

// Each filter selects what it says, and nothing else.
func TestFiltersSelectWhatTheySay(t *testing.T) {
	c := newClockedLogger(t, hour0)
	ctx := context.Background()
	jacob := int64(1)
	write := func(e Event) {
		t.Helper()
		if err := c.Write(ctx, e); err != nil {
			t.Fatal(err)
		}
		c.advance(time.Minute)
	}
	write(Event{ActorUserID: &jacob, ActorLabel: "jacob", Action: ActionLoginSucceeded, SourceIP: "192.0.2.7"})
	write(Event{ActorLabel: "anonymous", Action: ActionLoginFailed, Outcome: OutcomeFailure,
		SourceIP: "203.0.113.9", UserAgent: "Mozilla/5.0 (Evil_Bot 100%)"})
	write(Event{ActorLabel: "system:acquire", Action: ActionReleaseGrabbed, TargetKind: "release",
		TargetID: "0123abcd", Detail: "Severance.S02E03.1080p.WEB-DL | for Severance S02E03"})
	write(Event{ActorLabel: "system:acquire", Action: ActionReleaseGrabbed, Outcome: OutcomeFailure,
		TargetKind: "release", Detail: "Dune.2021.1080p | fetching failed"})
	write(Event{ActorUserID: &jacob, ActorLabel: "jacob", Action: ActionQueueRemoved, TargetID: "0123abcd"})
	write(Event{ActorLabel: "anonymous", Action: ActionAuthzDenied, Outcome: OutcomeDenied, TargetID: "GET /api/v1/admin/users"})

	actions := func(f Filter) []string {
		t.Helper()
		p, err := c.Page(adminCtx(), f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range p.Records {
			out = append(out, string(r.Action)+"/"+string(r.Outcome))
		}
		return out
	}
	for name, tc := range map[string]struct {
		f    Filter
		want string
	}{
		"a category":       {Filter{Category: "acquisition"}, "[acquisition.queue.removed/success acquisition.grabbed/failure acquisition.grabbed/success]"},
		"a whole word":     {Filter{Category: "auth"}, "[auth.login.failed/failure auth.login.succeeded/success]"},
		"an action":        {Filter{Action: ActionReleaseGrabbed}, "[acquisition.grabbed/failure acquisition.grabbed/success]"},
		"an outcome":       {Filter{Outcome: OutcomeFailure}, "[acquisition.grabbed/failure auth.login.failed/failure]"},
		"an actor":         {Filter{Actor: "system:acquire"}, "[acquisition.grabbed/failure acquisition.grabbed/success]"},
		"a target":         {Filter{Target: "0123abcd"}, "[acquisition.queue.removed/success acquisition.grabbed/success]"},
		"text in a detail": {Filter{Text: "severance"}, "[acquisition.grabbed/success]"},
		"text, literally":  {Filter{Text: "Evil_Bot 100%"}, "[auth.login.failed/failure]"},
		"a wildcard":       {Filter{Text: "%"}, "[auth.login.failed/failure]"},
		"an address":       {Filter{Text: "203.0.113"}, "[auth.login.failed/failure]"},
		"a range":          {Filter{Since: hour0.Add(2 * time.Minute), Until: hour0.Add(4 * time.Minute)}, "[acquisition.grabbed/failure acquisition.grabbed/success]"},
		"together":         {Filter{Category: "acquisition", Outcome: OutcomeSuccess, Actor: "jacob"}, "[acquisition.queue.removed/success]"},
	} {
		if got := fmt.Sprint(actions(tc.f)); got != tc.want {
			t.Errorf("%s: got %s, want %s", name, got, tc.want)
		}
	}
}

// What cannot be a filter is refused, not answered with everything.
func TestABadFilterIsRefused(t *testing.T) {
	c := newClockedLogger(t, hour0)
	long := make([]byte, MaxTextFilter+1)
	for i := range long {
		long[i] = 'a'
	}
	for name, f := range map[string]Filter{
		"a category that is not one": {Category: "auth.login"},
		"an outcome that is not one": {Outcome: "maybe"},
		"text too long":              {Text: string(long)},
		"a range backwards":          {Since: hour0, Until: hour0.Add(-time.Hour)},
		"a negative cursor":          {Before: -1},
	} {
		if _, err := c.Page(adminCtx(), f); !errors.Is(err, ErrBadFilter) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Only the permission reads, pages and counts alike.
func TestReadingNeedsThePermission(t *testing.T) {
	c := newClockedLogger(t, hour0)
	var manager authz.Role
	for _, r := range authz.BuiltinRoles() {
		if r.Name == authz.RoleManager {
			manager = r
		}
	}
	for name, ctx := range map[string]context.Context{
		"nobody": context.Background(),
		"a manager": authz.WithPrincipal(context.Background(), &authz.Principal{
			UserID: 5, Role: manager, State: authz.StateActive, MFASatisfied: true}),
		"automatic acquisition": authz.SystemPrincipal(context.Background(), authz.TaskAcquire),
	} {
		if _, err := c.Page(ctx, Filter{}); !authz.IsDenied(err) {
			t.Errorf("%s read a page: %v", name, err)
		}
		if _, err := c.Summary(ctx, time.Hour); !authz.IsDenied(err) {
			t.Errorf("%s read the counts: %v", name, err)
		}
	}
}

// Before and after come back as the JSON they were written as.
func TestBeforeAndAfterComeBackAsWritten(t *testing.T) {
	c := newClockedLogger(t, hour0)
	if err := c.Write(context.Background(), Event{Action: ActionRoleChanged, TargetKind: "user", TargetID: "20",
		Before: map[string]string{"role": "User"}, After: map[string]string{"role": "Manager"}}); err != nil {
		t.Fatal(err)
	}
	p, err := c.Page(adminCtx(), Filter{})
	if err != nil || len(p.Records) != 1 {
		t.Fatalf("%v %v", p, err)
	}
	b, _ := json.Marshal(map[string]any{"before": p.Records[0].Before, "after": p.Records[0].After})
	if string(b) != `{"after":{"role":"Manager"},"before":{"role":"User"}}` {
		t.Fatalf("read back %s", b)
	}
}

// The counts are by action and outcome, since the time asked.
func TestTheSummaryCountsByActionAndOutcome(t *testing.T) {
	c := newClockedLogger(t, hour0)
	ctx := context.Background()
	_ = c.Write(ctx, Event{Action: ActionLoginFailed, Outcome: OutcomeFailure}) // before the window
	c.advance(time.Hour)
	for i := 0; i < 3; i++ {
		_ = c.Write(ctx, Event{Action: ActionLoginFailed, Outcome: OutcomeFailure})
	}
	_ = c.Write(ctx, Event{Action: ActionLoginSucceeded})
	c.advance(10 * time.Minute)
	got, err := c.Summary(adminCtx(), 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got.By) != "[{auth.login.failed failure 3} {auth.login.succeeded success 1}]" {
		t.Fatalf("counts = %v", got.By)
	}
	if !got.Since.Equal(hour0.Add(40 * time.Minute)) {
		t.Fatalf("since %v: the window ends at the logger's clock", got.Since)
	}
	if CategoryOf(ActionReleaseGrabbed) != "acquisition" || CategoryOf("odd") != "odd" {
		t.Fatal("CategoryOf")
	}
}

// Every action this package declares has a category the screen can filter by,
// so no line is out of reach of the filter that should find it.
func TestEveryActionHasACategoryTheScreenOffers(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := 0
	inspect := func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || spec.Type == nil {
			return true
		}
		if id, ok := spec.Type.(*ast.Ident); !ok || id.Name != "Action" {
			return true
		}
		for _, v := range spec.Values {
			lit, ok := v.(*ast.BasicLit)
			if !ok {
				continue
			}
			name, _ := strconv.Unquote(lit.Value)
			found++
			if !isCategory(CategoryOf(Action(name))) {
				t.Errorf("%s is in no category the screen offers", name)
			}
		}
		return true
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, inspect)
	}
	if found < 50 {
		t.Fatalf("found only %d actions: the test is not reading the declarations", found)
	}
}

// A reader that remembers where it stopped carries on oldest first, and never
// sees a line twice or misses one — the notifier's read (ADR-0032).
func TestAfterCarriesOnOldestFirst(t *testing.T) {
	c := newClockedLogger(t, hour0)
	ctx := context.Background()
	if id, err := c.LastID(adminCtx()); err != nil || id != 0 {
		t.Fatalf("an empty log's newest id: %d %v", id, err)
	}
	for i := 0; i < 25; i++ {
		if err := c.Write(ctx, Event{Action: ActionLogout, Detail: fmt.Sprint("n", i)}); err != nil {
			t.Fatal(err)
		}
	}
	reader := authz.SystemPrincipal(ctx, authz.TaskNotify)
	var got []string
	var cursor int64
	for reads := 0; ; reads++ {
		if reads > 5 {
			t.Fatal("reading does not end")
		}
		recs, err := c.After(reader, cursor, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(recs) > 10 {
			t.Fatalf("a read of 10 held %d", len(recs))
		}
		if len(recs) == 0 {
			break
		}
		for _, r := range recs {
			if r.ID <= cursor {
				t.Fatalf("line %d came after the cursor %d", r.ID, cursor)
			}
			got = append(got, r.Detail)
		}
		cursor = recs[len(recs)-1].ID
	}
	if len(got) != 25 || got[0] != "n0" || got[24] != "n24" {
		t.Fatalf("read %v", got)
	}
	if last, err := c.LastID(reader); err != nil || last != cursor {
		t.Fatalf("newest id %d (%v), the reader stopped at %d", last, err, cursor)
	}
	if recs, err := c.After(reader, 0, 0); err != nil || len(recs) != 25 {
		t.Fatalf("no limit read %d (%v)", len(recs), err)
	}
	if _, err := c.After(reader, -1, 10); !errors.Is(err, ErrBadFilter) {
		t.Fatalf("a negative cursor: %v", err)
	}

	// Only whoever may read the log.
	for name, ctx := range map[string]context.Context{
		"nobody":                context.Background(),
		"automatic acquisition": authz.SystemPrincipal(context.Background(), authz.TaskAcquire),
	} {
		if _, err := c.After(ctx, 0, 10); !authz.IsDenied(err) {
			t.Errorf("%s read on: %v", name, err)
		}
		if _, err := c.LastID(ctx); !authz.IsDenied(err) {
			t.Errorf("%s read the newest id: %v", name, err)
		}
	}
}

package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// Reading the audit log over HTTP (ADR-0031): administrators only, strict
// about what a filter may be, paged by id, and the ceiling on anonymous
// denials visible end to end through the real router.

func auditEvents(t *testing.T, res response) []map[string]any {
	t.Helper()
	if res.Code != http.StatusOK {
		t.Fatalf("audit read: %d %s", res.Code, res.Raw)
	}
	raw, _ := res.Body["events"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		out = append(out, e.(map[string]any))
	}
	if int(res.Body["count"].(float64)) != len(out) {
		t.Fatalf("count %v for %d events", res.Body["count"], len(out))
	}
	return out
}

func eventID(e map[string]any) int64 { return int64(e["id"].(float64)) }

// The admin reads the log newest first, and a page continues where the last
// ended without repeating a line.
func TestTheAuditLogReadsNewestFirstAndInPages(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	stranger := r.client()
	stranger.visitPage("/login")
	for i := 0; i < 4; i++ {
		stranger.post("/api/v1/auth/login", map[string]any{"username": "jacob", "password": fmt.Sprint("wrong-guess-", i)})
	}

	all := auditEvents(t, admin.get("/api/v1/admin/audit"))
	if len(all) < 7 {
		t.Fatalf("an administrator's set-up and four failed sign-ins wrote only %d line(s)", len(all))
	}
	for i := 1; i < len(all); i++ {
		if eventID(all[i]) >= eventID(all[i-1]) {
			t.Fatalf("not newest first: %v then %v", all[i-1]["id"], all[i]["id"])
		}
	}
	first := all[len(all)-1]
	for _, k := range []string{"id", "occurred_at", "actor", "action", "category", "outcome"} {
		if _, ok := first[k]; !ok {
			t.Fatalf("an event has no %q: %v", k, first)
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, first["occurred_at"].(string)); err != nil {
		t.Fatalf("occurred_at: %v", err)
	}
	if first["category"] != audit.CategoryOf(audit.Action(first["action"].(string))) {
		t.Fatalf("category %v for action %v", first["category"], first["action"])
	}
	if note, _ := admin.get("/api/v1/admin/audit").Body["note"].(string); !strings.Contains(note, "as it was recorded") {
		t.Fatalf("note = %q", note)
	}

	// Two at a time, all the way down.
	var paged []int64
	path := "/api/v1/admin/audit?limit=2"
	for pages := 0; ; pages++ {
		if pages > len(all) {
			t.Fatal("paging does not end")
		}
		res := admin.get(path)
		got := auditEvents(t, res)
		if len(got) > 2 {
			t.Fatalf("limit=2 gave %d", len(got))
		}
		for _, e := range got {
			paged = append(paged, eventID(e))
		}
		next, ok := res.Body["next_before"].(float64)
		if !ok {
			break
		}
		if int64(next) != eventID(got[len(got)-1]) {
			t.Fatalf("next_before %v is not the last line shown (%v)", next, got[len(got)-1]["id"])
		}
		path = fmt.Sprintf("/api/v1/admin/audit?limit=2&before=%d", int64(next))
	}
	// Reading wrote nothing (decision 2), so the pages hold exactly what the
	// first read did.
	if len(paged) != len(all) {
		t.Fatalf("%d lines across the pages, %d in one read", len(paged), len(all))
	}
	for i, id := range paged {
		if id != eventID(all[i]) {
			t.Fatalf("line %d: %d paged, %d in one read", i, id, eventID(all[i]))
		}
	}
}

// Every filter is checked: a misspelt or impossible one is an error, never a
// page of everything that looks filtered.
func TestAnAuditFilterThatCannotBeIsRefused(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	for _, q := range []string{
		"actions=auth.login.failed",
		"outcome=success&outcome=denied",
		"limit=0", "limit=201", "limit=ten",
		"before=0", "before=-4", "before=abc",
		"category=auth.login", "category=AUTH",
		"outcome=maybe",
		"action=AUTH.LOGIN", "action=auth..login", "action=" + url.QueryEscape("auth.login'--"),
		"action=" + strings.Repeat("a", 65),
		"actor=" + strings.Repeat("a", 65),
		"target=" + strings.Repeat("a", 129),
		"q=" + strings.Repeat("a", audit.MaxTextFilter+1),
		"since=yesterday", "until=27/09/2026",
		"since=2026-09-10&until=2026-09-09",
	} {
		res := admin.get("/api/v1/admin/audit?" + q)
		if res.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", q, res.Code, res.Raw)
		}
	}
	for _, q := range []string{"weeks=2", "days=0", "days=91", "days=seven"} {
		if res := admin.get("/api/v1/admin/audit/summary?" + q); res.Code != http.StatusBadRequest {
			t.Errorf("summary %s: %d %s", q, res.Code, res.Raw)
		}
	}
	// And what can be, is.
	for _, q := range []string{
		"", "category=auth", "limit=1", "limit=200", "outcome=failure", "action=auth.login.succeeded",
		"actor=jacob", "q=" + url.QueryEscape("100%_"), "since=2026-09-10", "until=2026-09-10T12:00:00Z",
		"since=2026-09-10&until=2026-09-10",
	} {
		if res := admin.get("/api/v1/admin/audit?" + q); res.Code != http.StatusOK {
			t.Errorf("%s refused: %d %s", q, res.Code, res.Raw)
		}
	}
}

// The filters select over HTTP what they say.
func TestAuditFiltersSelectOverHTTP(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	// A failed sign-in from an address of its own, with a user agent that
	// looks like markup: it comes back as the text it was.
	stranger := r.client()
	stranger.ip = "198.51.100.23"
	stranger.visitPage("/login")
	if res := stranger.post("/api/v1/auth/login", map[string]any{
		"username": "jacob", "password": "not-the-password-at-all",
	}); res.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong password: %d %s", res.Code, res.Raw)
	}

	failed := auditEvents(t, admin.get("/api/v1/admin/audit?outcome=failure"))
	if len(failed) == 0 {
		t.Fatal("the failed sign-in is not in the log")
	}
	for _, e := range failed {
		if e["outcome"] != "failure" {
			t.Fatalf("outcome=failure gave %v", e)
		}
	}
	byText := auditEvents(t, admin.get("/api/v1/admin/audit?q=198.51.100.23"))
	if len(byText) == 0 || byText[0]["source_ip"] != "198.51.100.23" {
		t.Fatalf("q by address gave %v", byText)
	}
	for _, e := range auditEvents(t, admin.get("/api/v1/admin/audit?category=auth")) {
		if !strings.HasPrefix(e["action"].(string), "auth.") || e["category"] != "auth" {
			t.Fatalf("category=auth gave %v", e["action"])
		}
	}
	for _, e := range auditEvents(t, admin.get("/api/v1/admin/audit?actor=jacob")) {
		if e["actor"] != "jacob" {
			t.Fatalf("actor=jacob gave %v", e["actor"])
		}
	}
	if got := auditEvents(t, admin.get("/api/v1/admin/audit?since=2030-01-01")); len(got) != 0 {
		t.Fatalf("a range in the future gave %d line(s)", len(got))
	}
	if got := auditEvents(t, admin.get("/api/v1/admin/audit?q="+url.QueryEscape("%"))); len(got) != 0 {
		t.Fatalf("a wildcard matched %d line(s): it must match itself", len(got))
	}
}

// The summary counts by action, outcome and category, and says what the
// ceiling is.
func TestTheAuditSummaryCounts(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	res := admin.get("/api/v1/admin/audit/summary")
	if res.Code != http.StatusOK {
		t.Fatalf("summary: %d %s", res.Code, res.Raw)
	}
	if res.Body["days"].(float64) != 7 {
		t.Fatalf("days = %v", res.Body["days"])
	}
	total := int(res.Body["total"].(float64))
	all := auditEvents(t, admin.get("/api/v1/admin/audit?limit=200"))
	if total != len(all) {
		t.Fatalf("summary total %d, %d lines in the log", total, len(all))
	}
	sum := 0
	for _, a := range res.Body["actions"].([]any) {
		sum += int(a.(map[string]any)["count"].(float64))
	}
	if sum != total {
		t.Fatalf("actions add up to %d of %d", sum, total)
	}
	cats := res.Body["categories"].([]any)
	if len(cats) == 0 {
		t.Fatal("no categories")
	}
	sum, last := 0, total+1
	for _, c := range cats {
		c := c.(map[string]any)
		n := int(c["count"].(float64))
		if n > last {
			t.Fatalf("categories are not most first: %v", cats)
		}
		last = n
		byOutcome := 0
		for _, v := range c["outcomes"].(map[string]any) {
			byOutcome += int(v.(float64))
		}
		if byOutcome != n {
			t.Fatalf("%v: outcomes add up to %d of %d", c["category"], byOutcome, n)
		}
		sum += n
	}
	if sum != total {
		t.Fatalf("categories add up to %d of %d", sum, total)
	}
	if since, err := time.Parse(time.RFC3339, res.Body["since"].(string)); err != nil ||
		!since.Equal(r.clk.now().Add(-7*24*time.Hour).Truncate(time.Second)) {
		t.Fatalf("since = %v (%v): the window ends at the log's clock", res.Body["since"], err)
	}
	ceiling := res.Body["ceiling"].(map[string]any)
	if ceiling["anonymous_per_address_per_hour"].(float64) != audit.AnonymousPerSourcePerHour ||
		ceiling["anonymous_per_hour"].(float64) != audit.AnonymousPerHour ||
		ceiling["per_account_per_hour"].(float64) != audit.PersonPerHour {
		t.Fatalf("ceiling = %v", ceiling)
	}
	if res := admin.get("/api/v1/admin/audit/summary?days=90"); res.Code != http.StatusOK {
		t.Fatalf("days=90: %d", res.Code)
	}
}

// Nobody but an administrator — or a token an administrator scoped to it —
// can tell the audit routes exist.
func TestTheAuditLogIsHiddenFromEveryoneElse(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleManager, true)
	manager := r.redeemAndEnroll(code, "morgan", "manager-passphrase-1")
	code, _ = r.issueInvite(admin, authz.RoleUser, true)
	user := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")
	anonymous := r.client()

	withAudit, _ := r.mintToken(admin, "audit reader", authz.PermViewAuditLog)
	without, _ := r.mintToken(admin, "browser", authz.PermBrowse)

	for _, path := range []string{"/api/v1/admin/audit", "/api/v1/admin/audit/summary"} {
		for name, c := range map[string]*client{
			"a manager": manager, "a user": user, "nobody": anonymous, "a token without it": r.bearer(without),
		} {
			if res := c.get(path); res.Code != http.StatusNotFound {
				t.Errorf("%s reached %s: %d %s", name, path, res.Code, res.Raw)
			}
		}
		if res := r.bearer(withAudit).get(path); res.Code != http.StatusOK {
			t.Errorf("a token scoped to the audit log was refused %s: %d %s", path, res.Code, res.Raw)
		}
	}

	// Each refusal is itself a line — the person's, with who they are.
	denied := auditEvents(t, admin.get("/api/v1/admin/audit?action=authz.denied&actor=user:"+
		fmt.Sprint(r.userID("sam"))))
	if len(denied) != 2 {
		t.Fatalf("%d denial(s) recorded for sam, want 2", len(denied))
	}
}

// A flood of anonymous requests writes at most the ceiling, and the rest is
// one line once the hour is over; an account's denials in the same hour are
// still written.
func TestAnAnonymousFloodIsCappedAndCounted(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	user := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")

	scanner := r.client()
	scanner.ip = "198.51.100.77"
	for i := 0; i < 30; i++ {
		if res := scanner.get("/api/v1/admin/users"); res.Code != http.StatusNotFound {
			t.Fatalf("anonymous request %d: %d", i, res.Code)
		}
	}
	for i := 0; i < 3; i++ {
		user.get("/api/v1/admin/audit")
	}

	anon := auditEvents(t, admin.get("/api/v1/admin/audit?limit=200&action=authz.denied&actor=anonymous"))
	if len(anon) != audit.AnonymousPerSourcePerHour {
		t.Fatalf("%d anonymous denials written, want %d", len(anon), audit.AnonymousPerSourcePerHour)
	}
	if anon[0]["source_ip"] != "198.51.100.77" || anon[0]["target_id"] != "GET /api/v1/admin/users" {
		t.Fatalf("a denial reads %v", anon[0])
	}
	sams := auditEvents(t, admin.get("/api/v1/admin/audit?action=authz.denied&actor=user:"+fmt.Sprint(r.userID("sam"))))
	if len(sams) != 3 {
		t.Fatalf("%d of the account's denials written, want 3", len(sams))
	}

	// Nothing is counted aloud while the hour is still going.
	if got := auditEvents(t, admin.get("/api/v1/admin/audit?action=authz.denied.suppressed")); len(got) != 0 {
		t.Fatalf("a summary before the hour ended: %v", got)
	}
	r.clk.advance(time.Hour)
	if n, err := r.audit.FlushDenials(t.Context(), false); err != nil || n != 1 {
		t.Fatalf("flushed %d (%v)", n, err)
	}
	got := auditEvents(t, admin.get("/api/v1/admin/audit?action=authz.denied.suppressed"))
	if len(got) != 1 {
		t.Fatalf("%d summary line(s)", len(got))
	}
	s := got[0]
	if s["actor"] != "system:audit" || s["category"] != "authz" || s["outcome"] != "denied" ||
		!strings.Contains(s["detail"].(string), "198.51.100.77 ×10") {
		t.Fatalf("summary = %v", s)
	}
	if _, ok := s["actor_user_id"]; ok {
		t.Fatalf("the summary names an account: %v", s)
	}
}

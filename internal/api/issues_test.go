package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// ADR-0042, decision 1.
func TestAProblemIsReportedOnce(t *testing.T) {
	w := newScopeWorld(t)
	viewer, _ := w.scopedAccount("viewer", authz.RoleUser, []int64{w.kids, w.kidsTV}, 0)

	res := viewer.post("/api/v1/issues", map[string]any{"media_item_id": w.shown, "kind": "subtitles",
		"note": "the English track is empty"})
	if res.Code != http.StatusCreated || res.Body["state"] != "open" || res.Body["title"] != "Paddington" {
		t.Fatalf("report: %d %s", res.Code, res.Raw)
	}
	first := res.Body["id"]
	again := w.kid.post("/api/v1/issues", map[string]any{"media_item_id": w.shown, "kind": "subtitles"})
	if again.Code != http.StatusOK || again.Body["id"] != first || !strings.Contains(again.Raw, "already reported") {
		t.Errorf("the same problem again: %d %s", again.Code, again.Raw)
	}
	// An episode of a series is its own problem.
	if res := viewer.post("/api/v1/issues", map[string]any{"media_item_id": w.shownSeries, "kind": "audio",
		"season": 1, "episode": 2}); res.Code != http.StatusCreated || res.Body["episode"] != float64(2) {
		t.Errorf("an episode: %d %s", res.Code, res.Raw)
	}

	hidden := viewer.post("/api/v1/issues", map[string]any{"media_item_id": w.hiddenByRoot, "kind": "video"})
	missing := viewer.post("/api/v1/issues", map[string]any{"media_item_id": 987654, "kind": "video"})
	if hidden.Code != http.StatusNotFound || hidden.Raw != missing.Raw {
		t.Errorf("a hidden title: %d %s; a missing one: %s", hidden.Code, hidden.Raw, missing.Raw)
	}
	for name, body := range map[string]map[string]any{
		"kind":    {"media_item_id": w.shown, "kind": "smell"},
		"note":    {"media_item_id": w.shown, "kind": "other", "note": strings.Repeat("x", 501)},
		"episode": {"media_item_id": w.shownSeries, "kind": "audio", "episode": 3},
	} {
		if res := viewer.post("/api/v1/issues", body); res.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, res.Code, res.Raw)
		}
	}
	lines := w.r.auditDetails(t, "media.issue.reported")
	if len(lines) != 2 || lines[0] != "Paddington: subtitles — the English track is empty" {
		t.Errorf("audit lines %q", lines)
	}
}

// ADR-0042, decision 2.
func TestIssuesAreSeenAndResolvedByTheRightPeople(t *testing.T) {
	w := newScopeWorld(t)
	viewer, _ := w.scopedAccount("viewer", authz.RoleUser, []int64{w.kids}, 0)
	other, _ := w.scopedAccount("other", authz.RoleUser, []int64{w.kids, w.films}, 0)
	mine := viewer.post("/api/v1/issues", map[string]any{"media_item_id": w.shown, "kind": "video"})
	theirs := other.post("/api/v1/issues", map[string]any{"media_item_id": w.hiddenByRoot, "kind": "wrong_title"})
	// And one on a title the reporter can see too: theirs, not the reporter's.
	near := other.post("/api/v1/issues", map[string]any{"media_item_id": w.shown, "kind": "audio"})
	if mine.Code != http.StatusCreated || theirs.Code != http.StatusCreated || near.Code != http.StatusCreated {
		t.Fatalf("reports: %s / %s / %s", mine.Raw, theirs.Raw, near.Raw)
	}
	ids := func(c *client, q string) []string {
		t.Helper()
		res := c.get("/api/v1/issues" + q)
		var out []string
		for _, row := range res.Body["issues"].([]any) {
			out = append(out, row.(map[string]any)["title"].(string))
		}
		return out
	}
	if got := ids(viewer, ""); strings.Join(got, "|") != "Paddington" {
		t.Errorf("a reporter sees %q, want only their own", got)
	}
	if got := ids(w.kid, ""); strings.Join(got, "|") != "Paddington|Paddington" {
		t.Errorf("a restricted editor sees %q, want both in scope", got)
	}
	if got := ids(w.admin, ""); len(got) != 3 {
		t.Errorf("the administrator sees %q", got)
	}

	resolve := func(c *client, id any, text string) response {
		return c.post("/api/v1/issues/"+strconv.FormatInt(int64(id.(float64)), 10)+"/resolve",
			map[string]any{"resolution": text})
	}
	if res := resolve(viewer, mine.Body["id"], "fixed it myself"); res.Code != http.StatusForbidden {
		t.Errorf("a reporter resolved an issue: %d", res.Code)
	}
	if res := resolve(w.kid, theirs.Body["id"], "no"); res.Code != http.StatusNotFound {
		t.Errorf("an editor resolved an issue out of scope: %d", res.Code)
	}
	if res := resolve(w.kid, mine.Body["id"], ""); res.Code != http.StatusBadRequest {
		t.Errorf("an empty resolution: %d", res.Code)
	}
	res := resolve(w.kid, mine.Body["id"], "Replaced with the Blu-ray.")
	if res.Code != http.StatusOK || res.Body["state"] != "resolved" || res.Body["resolved_by"] != "kid" {
		t.Fatalf("resolve: %d %s", res.Code, res.Raw)
	}
	if res := resolve(w.kid, mine.Body["id"], "again"); res.Code != http.StatusConflict {
		t.Errorf("resolved twice: %d", res.Code)
	}
	if got := ids(viewer, ""); len(got) != 0 {
		t.Errorf("a resolved issue is still open: %q", got)
	}
	all := viewer.get("/api/v1/issues?all=true")
	if !strings.Contains(all.Raw, "Replaced with the Blu-ray.") {
		t.Errorf("the reporter cannot read the answer: %s", all.Raw)
	}
	if lines := w.r.auditDetails(t, "media.issue.resolved"); len(lines) != 1 ||
		lines[0] != "Paddington: video resolved — Replaced with the Blu-ray." {
		t.Errorf("audit lines %q", lines)
	}
}

package api

import (
	"net/http"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/request"
)

// End-to-end tests for the acquisition request surface.
//
// The property most of these are really about is a privacy one: what somebody
// is watching, and what they have asked for, is theirs. A listing that leaked
// across accounts would be the kind of defect nobody reports because nobody can
// see that it is happening.

func (r *rig) submit(as *client, kind, title string, year int) response {
	r.t.Helper()
	return as.post("/api/v1/requests", map[string]any{
		"kind": kind, "title": title, "year": year, "note": "",
	})
}

func requestTitles(res response) []string {
	rows, _ := res.Body["requests"].([]any)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		m, _ := row.(map[string]any)
		out = append(out, m["title"].(string))
	}
	return out
}

// ---------------------------------------------------------------------------
// Submitting
// ---------------------------------------------------------------------------

// TestARequestMustBeAskableFor covers the inputs that would produce a request
// nobody could act on, or one that collides with every other bad one.
//
// The punctuation case is the interesting one: MatchKey normalises "!!!" to the
// empty string, and an empty match key would collide with every other empty
// one — so the second person to submit junk would silently JOIN the first
// person's request. Refusing at the door is what keeps that from happening.
func TestARequestMustBeAskableFor(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")

	bad := []struct {
		why  string
		body map[string]any
	}{
		{"no kind", map[string]any{"title": "Arrival", "year": 2016}},
		{"a kind that is not a kind", map[string]any{"kind": "album", "title": "Arrival"}},
		{"no title", map[string]any{"kind": "movie", "title": "  "}},
		{"a title of pure punctuation", map[string]any{"kind": "movie", "title": "!!!"}},
		{"a year before cinema", map[string]any{"kind": "movie", "title": "Arrival", "year": 1600}},
		{"a year far in the future", map[string]any{"kind": "movie", "title": "Arrival", "year": 9999}},
	}
	for _, b := range bad {
		res := sam.post("/api/v1/requests", b.body)
		if res.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s — want 400", b.why, res.Code, res.Raw)
		}
	}

	// None of that created anything, which is the assertion that matters: a
	// refused submission that still wrote a row would be worse than one that
	// succeeded.
	if res := sam.get("/api/v1/requests"); res.Body["count"].(float64) != 0 {
		t.Errorf("refused submissions left %v rows behind", res.Body["count"])
	}
}

func TestASecondPersonAskingJoinsTheFirstRequest(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	c1, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(c1, "sam", "regular-passphrase-1")
	c2, _ := r.issueInvite(admin, authz.RoleUser, true)
	alex := r.redeemAndEnroll(c2, "alex", "regular-passphrase-2")

	if res := r.submit(sam, "movie", "The Matrix", 1999); res.Code != http.StatusCreated {
		t.Fatalf("first submission: %d %s", res.Code, res.Raw)
	}

	// Different spelling, same film. 200 rather than 201, because nothing was
	// created — and the body says so, or the person re-requests it tomorrow.
	res := r.submit(alex, "movie", "matrix", 1999)
	if res.Code != http.StatusOK {
		t.Fatalf("second submission: %d %s — want 200 (joined)", res.Code, res.Raw)
	}
	if res.Body["waiting"].(float64) != 2 {
		t.Errorf("waiting = %v, want 2", res.Body["waiting"])
	}

	// One request, and BOTH of them can see it — joining that left the second
	// person unable to find what they joined would be the same as not joining.
	if res := admin.get("/api/v1/requests"); res.Body["count"].(float64) != 1 {
		t.Errorf("count = %v, want 1: %s", res.Body["count"], res.Raw)
	}
	for _, c := range []struct {
		name string
		cl   *client
	}{{"sam", sam}, {"alex", alex}} {
		if got := requestTitles(c.cl.get("/api/v1/requests")); len(got) != 1 {
			t.Errorf("%s sees %v, want the one joined request", c.name, got)
		}
	}
}

func TestAUserCannotFloodTheQueue(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")

	for i := 0; i < request.MaxOpenPerUser; i++ {
		if res := r.submit(sam, "movie", "Film Number "+uid(int64(i)), 2000+i); res.Code != http.StatusCreated {
			t.Fatalf("submission %d: %d %s", i, res.Code, res.Raw)
		}
	}

	res := r.submit(sam, "movie", "One Too Many", 1999)
	if res.Code != http.StatusTooManyRequests {
		t.Errorf("the %dst request returned %d, want 429",
			request.MaxOpenPerUser+1, res.Code)
	}

	// 429 and not 403: they are allowed, they have used their share. And the
	// cap is per account, so it must not have stopped anybody else.
	c2, _ := r.issueInvite(admin, authz.RoleUser, true)
	alex := r.redeemAndEnroll(c2, "alex", "regular-passphrase-2")
	if res := r.submit(alex, "movie", "Somebody Elses Film", 1999); res.Code != http.StatusCreated {
		t.Errorf("one user's cap blocked another user: %d %s", res.Code, res.Raw)
	}
}

// ---------------------------------------------------------------------------
// Who sees what
// ---------------------------------------------------------------------------

// The privacy property: a request is a statement about what somebody watches.
func TestAUserSeesOnlyTheirOwnRequests(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	c1, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(c1, "sam", "regular-passphrase-1")
	c2, _ := r.issueInvite(admin, authz.RoleUser, true)
	alex := r.redeemAndEnroll(c2, "alex", "regular-passphrase-2")

	r.submit(sam, "movie", "Sams Film", 2001)
	r.submit(alex, "series", "Alexs Series", 2002)

	samSees := requestTitles(sam.get("/api/v1/requests"))
	if len(samSees) != 1 || samSees[0] != "Sams Film" {
		t.Errorf("sam sees %v, want only their own", samSees)
	}
	alexSees := requestTitles(alex.get("/api/v1/requests"))
	if len(alexSees) != 1 || alexSees[0] != "Alexs Series" {
		t.Errorf("alex sees %v, want only their own", alexSees)
	}

	// The approver sees both, which is the whole point of the queue.
	if got := requestTitles(admin.get("/api/v1/requests")); len(got) != 2 {
		t.Errorf("the approver sees %v, want both", got)
	}

	// And the listing SAYS which it is, so a short list is not mistaken for an
	// empty instance.
	if s := sam.get("/api/v1/requests").Body["scope"]; s != "your own requests" {
		t.Errorf("scope for a user = %v", s)
	}

	// Who else is waiting is not an ordinary user's business: it is a list of
	// what other people on this instance are watching.
	rows, _ := sam.get("/api/v1/requests").Body["requests"].([]any)
	if _, leaked := rows[0].(map[string]any)["followers"]; leaked {
		t.Error("a user's own listing carries the follower list")
	}
	rows, _ = admin.get("/api/v1/requests").Body["requests"].([]any)
	if _, ok := rows[0].(map[string]any)["followers"]; !ok {
		t.Error("the approver cannot see who is waiting")
	}
}

// ---------------------------------------------------------------------------
// Deciding
// ---------------------------------------------------------------------------

func TestApprovalDownloadsNothing(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")

	res := r.submit(sam, "movie", "Arrival", 2016)
	id := uid(int64(res.Body["id"].(float64)))

	res = admin.post("/api/v1/requests/"+id+"/approve", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", res.Code, res.Raw)
	}
	if res.Body["state"] != "approved" {
		t.Errorf("state = %v, want approved", res.Body["state"])
	}

	// §13 puts the legality of what this instance acquires on the operator, so
	// approval must not itself acquire anything. Nothing was handed to the
	// engine, and the request carries no info hash.
	if len(r.downloads.added)+len(r.downloads.addedBytes) != 0 {
		t.Errorf("approving a request started a download: %v %v",
			r.downloads.added, r.downloads.addedBytes)
	}
	if _, grabbed := res.Body["info_hash"]; grabbed {
		t.Error("an approved-but-not-grabbed request reports an info hash")
	}
}

func TestADenialMustCarryAReasonTheRequesterCanSee(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")

	res := r.submit(sam, "movie", "Arrival", 2016)
	id := uid(int64(res.Body["id"].(float64)))

	if res := admin.post("/api/v1/requests/"+id+"/deny", map[string]any{"reason": "  "}); res.Code != http.StatusBadRequest {
		t.Errorf("a reasonless denial returned %d, want 400", res.Code)
	}
	if res := admin.post("/api/v1/requests/"+id+"/deny",
		map[string]any{"reason": "already in the library"}); res.Code != http.StatusOK {
		t.Fatalf("deny: %d %s", res.Code, res.Raw)
	}

	rows, _ := sam.get("/api/v1/requests").Body["requests"].([]any)
	row := rows[0].(map[string]any)
	if row["state"] != "denied" {
		t.Fatalf("state = %v", row["state"])
	}
	if row["decision_reason"] != "already in the library" {
		t.Errorf("the requester cannot see why: %v", row["decision_reason"])
	}
}

func TestAUserCannotDecideTheirOwnRequest(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")

	res := r.submit(sam, "movie", "Arrival", 2016)
	id := uid(int64(res.Body["id"].(float64)))

	// 403 and not the 404 the ADMIN surface gives, and that is deliberate
	// rather than an oversight: these routes sit beside
	// /api/v1/accounts/requests/{id}/approve and are registered the same way.
	// Hiding them would disclose nothing useful anyway — every user already
	// knows an approval step exists, because their own request is waiting on
	// one. What must stay hidden is the administration surface, not the fact
	// that somebody approves things.
	for _, path := range []string{"/approve", "/deny"} {
		res := sam.post("/api/v1/requests/"+id+path, map[string]any{"reason": "because"})
		if res.Code != http.StatusForbidden {
			t.Errorf("a user reached %s: %d %s", path, res.Code, res.Raw)
		}
	}
	rows, _ := sam.get("/api/v1/requests").Body["requests"].([]any)
	if st := rows[0].(map[string]any)["state"]; st != "pending" {
		t.Errorf("state = %v after two refused decisions, want pending", st)
	}
}

// Two approvers clicking at once is ordinary. The second must be told what
// happened rather than shown a failure, and must not overwrite the first
// decision — which would leave two audit lines each claiming to be the one.
func TestARequestCanOnlyBeDecidedOnce(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")

	res := r.submit(sam, "movie", "Arrival", 2016)
	id := uid(int64(res.Body["id"].(float64)))

	if res := admin.post("/api/v1/requests/"+id+"/approve", nil); res.Code != http.StatusOK {
		t.Fatalf("first approve: %d %s", res.Code, res.Raw)
	}
	if res := admin.post("/api/v1/requests/"+id+"/deny",
		map[string]any{"reason": "changed my mind"}); res.Code != http.StatusConflict {
		t.Errorf("a second decision returned %d, want 409", res.Code)
	}

	rows, _ := admin.get("/api/v1/requests").Body["requests"].([]any)
	if st := rows[0].(map[string]any)["state"]; st != "approved" {
		t.Errorf("state = %v — the second decision overwrote the first", st)
	}
}

// A Manager holds PermAutoApproveOwn, and that permission means exactly one
// thing: their OWN submissions skip the queue. It is not authority over anybody
// else's request — that is PermApproveRequests, which a Manager also holds, so
// the distinction is asserted at the service's level of meaning rather than by
// the outcome.
func TestAutoApprovalAppliesToOnesOwnSubmissionOnly(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	c1, _ := r.issueInvite(admin, authz.RoleManager, true)
	morgan := r.redeemAndEnroll(c1, "morgan", "manager-passphrase-1")
	c2, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(c2, "sam", "regular-passphrase-1")

	res := r.submit(morgan, "movie", "Managers Film", 2003)
	if res.Code != http.StatusCreated || res.Body["state"] != "approved" {
		t.Errorf("a manager's own request = %d %v, want 201 approved",
			res.Code, res.Body["state"])
	}

	// A User's request is not auto-approved by anybody else's permission.
	res = r.submit(sam, "movie", "Users Film", 2004)
	if res.Body["state"] != "pending" {
		t.Errorf("a user's request = %v, want pending", res.Body["state"])
	}

	// And it is still downloaded nothing.
	if len(r.downloads.added)+len(r.downloads.addedBytes) != 0 {
		t.Error("auto-approval started a download")
	}
}

// The requester's own words must survive the round trip.
//
// They did not, the first time: the handler wrote its status message into the
// same "note" key the requester's text occupies, so a request submitted with a
// note came back carrying the server's sentence instead — and the UI displayed
// that as though the person had typed it. Found against the binary, because
// every test at the time only checked the status code.
func TestARequestKeepsTheRequestersOwnNote(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleUser, true)
	sam := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")

	const note = "rewatch night"
	res := sam.post("/api/v1/requests", map[string]any{
		"kind": "movie", "title": "The Matrix", "year": 1999, "note": note,
	})
	if res.Code != http.StatusCreated {
		t.Fatalf("submit: %d %s", res.Code, res.Raw)
	}
	if res.Body["note"] != note {
		t.Errorf("the submission response reports note=%q, want %q — the "+
			"server's own message has overwritten the requester's",
			res.Body["note"], note)
	}
	if res.Body["message"] == nil {
		t.Error("the response carries no explanation for the person")
	}

	// And on the way back out of the listing, which is where somebody actually
	// reads it.
	rows, _ := sam.get("/api/v1/requests").Body["requests"].([]any)
	if got := rows[0].(map[string]any)["note"]; got != note {
		t.Errorf("the listing reports note=%q, want %q", got, note)
	}
}

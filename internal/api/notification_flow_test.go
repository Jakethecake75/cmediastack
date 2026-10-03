package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/notify"
)

// Notifications over HTTP (ADR-0032): an administrator's, invisible to anyone
// else, and the webhook link never comes back in any answer.

const (
	flowWebhookID = "1234567890123456789"
	// Obviously fake, and plain enough that no secret scanner takes it for one.
	flowWebhookToken = "FAKE_api_webhook_token_used_by_tests_only_yyyyyyyyyyyyyyyyyyyyyyyyyy"
	flowWebhook      = "https://discord.com/api/webhooks/" + flowWebhookID + "/" + flowWebhookToken
)

// fakeDiscord stands in for Discord in the API rig.
type fakeDiscord struct {
	mu       sync.Mutex
	info     notify.Info
	checkErr error
	sendErr  error
	sent     []string
}

func (f *fakeDiscord) Check(context.Context, notify.Webhook) (notify.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.info, f.checkErr
}

func (f *fakeDiscord) Send(_ context.Context, _ notify.Webhook, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, content)
	return nil
}

func (f *fakeDiscord) take() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := strings.Join(f.sent, "\n")
	f.sent = nil
	return out
}

func (r *rig) deliver() string {
	r.t.Helper()
	if _, err := r.notifier.Run(authz.SystemPrincipal(context.Background(), authz.TaskNotify)); err != nil {
		r.t.Fatal(err)
	}
	return r.discord.take()
}

var notificationRoutes = []struct{ method, path string }{
	{http.MethodGet, "/api/v1/admin/notifications"},
	{http.MethodPut, "/api/v1/admin/notifications/webhook"},
	{http.MethodPut, "/api/v1/admin/notifications/categories"},
	{http.MethodPost, "/api/v1/admin/notifications/test"},
}

// Nobody but an administrator can tell the routes exist.
func TestNotificationsAreHiddenFromEveryoneElse(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleManager, true)
	manager := r.redeemAndEnroll(code, "morgan", "manager-passphrase-1")
	code, _ = r.issueInvite(admin, authz.RoleUser, true)
	user := r.redeemAndEnroll(code, "sam", "regular-passphrase-1")
	nobody := r.client()
	nobody.visitPage("/login") // a CSRF cookie, so a refusal is the route's and not the form's
	for _, rt := range notificationRoutes {
		for name, c := range map[string]*client{"a manager": manager, "a user": user, "nobody": nobody} {
			if res := c.do(rt.method, rt.path, map[string]any{}); res.Code != http.StatusNotFound {
				t.Errorf("%s reached %s %s: %d %s", name, rt.method, rt.path, res.Code, res.Raw)
			}
		}
	}
	if res := admin.get("/api/v1/admin/notifications"); res.Code != http.StatusOK || res.Body["configured"] != false {
		t.Fatalf("the administrator: %d %s", res.Code, res.Raw)
	}
	if got := r.discord.take(); got != "" {
		t.Fatalf("something was sent: %q", got)
	}
}

// The link goes in once and never comes out: not in any answer, not in the
// audit log.
func TestTheWebhookLinkNeverComesBack(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()

	var raws []string
	res := admin.do(http.MethodPut, "/api/v1/admin/notifications/webhook", map[string]any{"url": flowWebhook})
	if res.Code != http.StatusOK || res.Body["webhook"].(map[string]any)["name"] != "CMediaStack" {
		t.Fatalf("storing it: %d %s", res.Code, res.Raw)
	}
	raws = append(raws, res.Raw)
	res = admin.get("/api/v1/admin/notifications")
	if res.Code != http.StatusOK || res.Body["configured"] != true ||
		!strings.Contains(res.Body["note"].(string), "never returned") {
		t.Fatalf("status: %d %s", res.Code, res.Raw)
	}
	raws = append(raws, res.Raw)
	res = admin.post("/api/v1/admin/notifications/test", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("test: %d %s", res.Code, res.Raw)
	}
	raws = append(raws, res.Raw)
	res = admin.do(http.MethodPut, "/api/v1/admin/notifications/categories",
		map[string]any{"categories": []string{"security", "library"}})
	if res.Code != http.StatusOK {
		t.Fatalf("categories: %d %s", res.Code, res.Raw)
	}
	raws = append(raws, res.Raw, admin.get("/api/v1/admin/audit?limit=200").Raw)
	for _, raw := range raws {
		if strings.Contains(raw, flowWebhookToken[:12]) || strings.Contains(raw, "webhooks/") {
			t.Fatalf("the link came back: %s", raw)
		}
	}
	changes := auditEvents(t, admin.get("/api/v1/admin/audit?action=system.setting.changed"))
	if len(changes) != 2 || changes[0]["target_id"] != "notification.categories" ||
		fmt.Sprint(changes[0]["after"]) != "[security library]" ||
		fmt.Sprint(changes[0]["before"]) != "[security accounts operations]" ||
		changes[1]["target_id"] != "notification.discord_webhook" || changes[1]["source_ip"] != "203.0.113.10" {
		t.Fatalf("the changes read %v", changes)
	}
}

// A link that is not a Discord webhook, or one Discord does not confirm, is
// refused and nothing is stored; Discord out of reach, or asking to wait, is
// said as such.
func TestAWebhookThatCannotBeUsedIsNotStored(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	put := func(url string) response {
		return admin.do(http.MethodPut, "/api/v1/admin/notifications/webhook", map[string]any{"url": url})
	}
	for name, tc := range map[string]struct {
		url    string
		check  error
		status int
	}{
		"elsewhere":     {"https://evil.example/api/webhooks/1/" + flowWebhookToken, nil, http.StatusBadRequest},
		"plain http":    {"http://discord.com/api/webhooks/" + flowWebhookID + "/" + flowWebhookToken, nil, http.StatusBadRequest},
		"unknown":       {flowWebhook, notify.ErrWebhookGone, http.StatusBadRequest},
		"a follower":    {flowWebhook, notify.ErrNotPostable, http.StatusBadRequest},
		"out of reach":  {flowWebhook, fmt.Errorf("%w: it answered 502", notify.ErrUnavailable), http.StatusBadGateway},
		"asked to wait": {flowWebhook, &notify.RateLimited{RetryAfter: 30 * time.Second}, http.StatusServiceUnavailable},
	} {
		r.discord.checkErr = tc.check
		res := put(tc.url)
		if res.Code != tc.status {
			t.Errorf("%s: %d %s", name, res.Code, res.Raw)
		}
		if tc.status == http.StatusBadRequest && !strings.Contains(fmt.Sprint(res.Body["note"]), "Nothing was stored") {
			t.Errorf("%s: %s", name, res.Raw)
		}
		if tc.status == http.StatusServiceUnavailable && res.Header.Get("Retry-After") != "30" {
			t.Errorf("%s: Retry-After %q", name, res.Header.Get("Retry-After"))
		}
		if strings.Contains(res.Raw, flowWebhookToken[:12]) {
			t.Errorf("%s: the answer holds the link: %s", name, res.Raw)
		}
	}
	if res := admin.get("/api/v1/admin/notifications"); res.Body["configured"] != false {
		t.Fatalf("something was stored: %s", res.Raw)
	}
	if res := admin.do(http.MethodPut, "/api/v1/admin/notifications/webhook",
		map[string]any{"url": flowWebhook, "extra": true}); res.Code != http.StatusBadRequest {
		t.Fatalf("an unknown field: %d", res.Code)
	}
}

// Categories are checked, and a test needs a webhook and is limited.
func TestCategoriesAreCheckedAndTestsLimited(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	putCats := func(body map[string]any) response {
		return admin.do(http.MethodPut, "/api/v1/admin/notifications/categories", body)
	}
	for name, body := range map[string]map[string]any{
		"an unknown one": {"categories": []string{"security", "everything"}},
		"no list":        {},
		"not a list":     {"categories": "security"},
	} {
		if res := putCats(body); res.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, res.Code, res.Raw)
		}
	}
	if res := putCats(map[string]any{"categories": []string{}}); res.Code != http.StatusOK {
		t.Fatalf("none: %d %s", res.Code, res.Raw)
	}
	cats := admin.get("/api/v1/admin/notifications").Body["categories"].([]any)
	for _, c := range cats {
		if c.(map[string]any)["on"] != false {
			t.Fatalf("after choosing none: %v", cats)
		}
	}

	if res := admin.post("/api/v1/admin/notifications/test", nil); res.Code != http.StatusConflict {
		t.Fatalf("a test with no webhook: %d %s", res.Code, res.Raw)
	}
	admin.do(http.MethodPut, "/api/v1/admin/notifications/webhook", map[string]any{"url": flowWebhook})
	r.discord.sendErr = notify.ErrWebhookGone
	if res := admin.post("/api/v1/admin/notifications/test", nil); res.Code != http.StatusBadRequest ||
		!strings.Contains(res.Raw, "does not know this webhook") {
		t.Fatalf("a test to a forgotten webhook: %d %s", res.Code, res.Raw)
	}
	r.discord.sendErr = nil
	for i := 0; i < 4; i++ {
		if res := admin.post("/api/v1/admin/notifications/test", nil); res.Code != http.StatusOK {
			t.Fatalf("test %d: %d %s", i, res.Code, res.Raw)
		}
	}
	if res := admin.post("/api/v1/admin/notifications/test", nil); res.Code != http.StatusTooManyRequests {
		t.Fatalf("a sixth test in a minute: %d %s", res.Code, res.Raw)
	}
}

// What the application audits is what reaches the channel: an account
// request, named as the stranger named it, in a code span.
func TestWhatIsAuditedReachesTheChannel(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	if res := admin.do(http.MethodPut, "/api/v1/admin/notifications/webhook", map[string]any{"url": flowWebhook}); res.Code != http.StatusOK {
		t.Fatalf("%d %s", res.Code, res.Raw)
	}
	if got := r.deliver(); !strings.Contains(got, "**Setting changed** · `jacob` → `setting notification.discord_webhook`") {
		t.Fatalf("the first run sent %q", got)
	}

	stranger := r.client()
	stranger.visitPage("/signup")
	if res := stranger.post("/api/v1/auth/signup", map[string]any{
		"username": "everyone_ping", "email": "e@example.com", "password": "a-long-enough-passphrase",
	}); res.Code != http.StatusAccepted && res.Code != http.StatusCreated {
		t.Fatalf("signup: %d %s", res.Code, res.Raw)
	}
	got := r.deliver()
	if !strings.Contains(got, "👤 **Account requested — waiting for approval** · `anonymous` → `account_request everyone_ping`") ||
		strings.Contains(got, "e@example.com") || strings.Contains(got, "203.0.113.10") {
		t.Fatalf("the request reached the channel as %q", got)
	}
	if got := r.deliver(); got != "" {
		t.Fatalf("sent twice: %q", got)
	}
}

package api

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// A cast link (ADR-0077) is the file and account it was minted for: a
// Chromecast holding it reaches that file as that account does, and a link
// changed to name another file reaches nothing.
func TestACastLinkReachesOneFileAsItsAccount(t *testing.T) {
	w := newScopeWorld(t)
	res := w.kid.post("/api/v1/files/"+uid(w.shownFile)+"/cast", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("mint: %d %s", res.Code, res.Raw)
	}
	link, _ := res.Body["stream"].(string)
	if !strings.HasPrefix(link, "/api/v1/cast/"+uid(w.shownFile)+".") || !strings.HasSuffix(link, "/stream") {
		t.Fatalf("link = %q", link)
	}

	// Anonymous, as a Chromecast is; answered as the account's own request.
	tv := w.r.client()
	got := tv.get(link)
	own := w.kid.get("/api/v1/files/" + uid(w.shownFile) + "/stream")
	if got.Code != own.Code || got.Code == http.StatusNotFound {
		t.Fatalf("the link answered %d %s; the account's own request %d %s", got.Code, got.Raw, own.Code, own.Raw)
	}
	if got.Header.Get("Cross-Origin-Resource-Policy") != "cross-origin" {
		t.Errorf("CORP = %q; the Chromecast's page could not load it", got.Header.Get("Cross-Origin-Resource-Policy"))
	}

	token := strings.TrimSuffix(strings.TrimPrefix(link, "/api/v1/cast/"), "/stream")
	for name, bad := range map[string]string{
		"another file": uid(w.hiddenFile) + strings.TrimPrefix(token, uid(w.shownFile)),
		"a forged mac": token[:len(token)-2] + "xx",
		"not a token":  "nothing",
	} {
		if res := tv.get("/api/v1/cast/" + bad + "/stream"); res.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s", name, res.Code, res.Raw)
		}
	}
}

func TestACastLinkExpires(t *testing.T) {
	h := &Handlers{castKey: newCastKey()}
	now := time.Now()
	tok := h.castToken(42, 7, now.Add(time.Hour))
	if f, u, ok := h.readCastToken(tok, now); !ok || f != 42 || u != 7 {
		t.Fatalf("a fresh link reads %d %d %v", f, u, ok)
	}
	if _, _, ok := h.readCastToken(tok, now.Add(2*time.Hour)); ok {
		t.Error("a link worked after it expired")
	}
	if _, _, ok := (&Handlers{castKey: newCastKey()}).readCastToken(tok, now); ok {
		t.Error("a link signed with another key worked")
	}
}

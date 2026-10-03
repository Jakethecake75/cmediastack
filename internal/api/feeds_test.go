package api

import (
	"encoding/xml"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
)

func (w *scopeWorld) airSeasonTwo(t *testing.T) {
	t.Helper()
	ctx := authz.WithPrincipal(t.Context(), adminPrincipalCtx(t))
	for _, id := range []int64{w.shownSeries, w.hiddenSeries} {
		season := library.SeasonInput{Number: 2, Name: "Season 2", EpisodeCount: 2}
		for n, days := range []int{-3, 10} {
			season.Episodes = append(season.Episodes, library.EpisodeInput{
				ProviderID: id*1000 + int64(n), Number: n + 1, Title: "Part " + strconv.Itoa(n+1),
				Aired: time.Now().AddDate(0, 0, days)})
		}
		if err := w.episodes.Upsert(ctx, id, []library.SeasonInput{season}); err != nil {
			t.Fatal(err)
		}
	}
}

func mintFeed(t *testing.T, c *client) (string, response) {
	t.Helper()
	res := c.post("/api/v1/me/feeds", map[string]any{})
	if res.Code != http.StatusCreated {
		t.Fatalf("mint: %d %s", res.Code, res.Raw)
	}
	return res.Body["token"].(string), res
}

// ADR-0041, decision 1: the credential itself.
func TestAFeedTokenIsACredentialOfItsOwn(t *testing.T) {
	w := newScopeWorld(t)
	token, res := mintFeed(t, w.kid)
	if !strings.HasPrefix(token, "cms_feed_") ||
		res.Body["calendar"] != "https://media.example.com/api/v1/feeds/"+token+"/calendar.ics" ||
		res.Body["rss"] != "https://media.example.com/api/v1/feeds/"+token+"/rss" {
		t.Fatalf("minted %s", res.Raw)
	}
	anon := w.r.client()
	rss := func(tok string) int { return anon.get("/api/v1/feeds/" + tok + "/rss").Code }
	if code := rss(token); code != http.StatusOK {
		t.Fatalf("the feed with its token: %d", code)
	}
	if st := w.kid.get("/api/v1/me/feeds"); st.Body["exists"] != true || st.Body["last_used_at"] == nil ||
		strings.Contains(st.Raw, token) {
		t.Errorf("status %s", st.Raw)
	}

	replaced, _ := mintFeed(t, w.kid)
	if rss(token) != http.StatusNotFound || rss(replaced) != http.StatusOK {
		t.Errorf("minting again did not replace the old token")
	}
	if res := w.kid.do(http.MethodDelete, "/api/v1/me/feeds", nil); res.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", res.Code, res.Raw)
	}
	if rss(replaced) != http.StatusNotFound {
		t.Error("a revoked token still reads the feed")
	}
	if st := w.kid.get("/api/v1/me/feeds"); st.Body["exists"] != false {
		t.Errorf("status after revoking %s", st.Raw)
	}

	tok, _ := w.r.mintToken(w.kid, "script", authz.PermBrowse)
	if res := w.r.bearer(tok).post("/api/v1/me/feeds", map[string]any{}); res.Code/100 == 2 {
		t.Errorf("an API token minted a feed token: %d", res.Code)
	}
	if lines := w.r.auditDetails(t, "auth.feed_token.issued"); len(lines) != 2 {
		t.Errorf("issued lines %q", lines)
	}
	if lines := w.r.auditDetails(t, "auth.feed_token.revoked"); len(lines) != 1 {
		t.Errorf("revoked lines %q", lines)
	}
}

// ADR-0041, decision 1: what it opens, and what it does not.
func TestAFeedTokenReadsOnlyTheFeeds(t *testing.T) {
	w := newScopeWorld(t)
	w.airSeasonTwo(t)
	token, _ := mintFeed(t, w.kid)
	anon := w.r.client()

	cal := anon.get("/api/v1/feeds/" + token + "/calendar.ics")
	if cal.Code != http.StatusOK || !strings.Contains(cal.Raw, "Bluey S02E01") ||
		strings.Contains(cal.Raw, "Severance") {
		t.Errorf("the calendar is not the account's: %d %s", cal.Code, cal.Raw)
	}
	// Anywhere else, the token is nothing.
	if res := w.r.bearer(token).get("/api/v1/media"); res.Code/100 == 2 {
		t.Errorf("a feed token read the library through the API: %d", res.Code)
	}
	wrong := anon.get("/api/v1/feeds/cms_feed_" + strings.Repeat("A", 43) + "/rss")
	garbage := anon.get("/api/v1/feeds/nonsense/rss")
	if wrong.Code != http.StatusNotFound || garbage.Code != http.StatusNotFound || wrong.Raw != garbage.Raw {
		t.Errorf("wrong tokens: %d %s / %d %s", wrong.Code, wrong.Raw, garbage.Code, garbage.Raw)
	}

	// An account that cannot act has a token that does not either.
	if res := w.admin.post("/api/v1/admin/users/"+strconv.FormatInt(w.kidID, 10)+"/suspend",
		map[string]any{"reason": "testing"}); res.Code != http.StatusOK {
		t.Fatalf("suspend: %d %s", res.Code, res.Raw)
	}
	if res := anon.get("/api/v1/feeds/" + token + "/rss"); res.Code != http.StatusNotFound || res.Raw != wrong.Raw {
		t.Errorf("a suspended account's feed: %d %s", res.Code, res.Raw)
	}
}

// ADR-0041, decision 2: RFC 5545's rules.
func TestTheCalendarIsValidICalendar(t *testing.T) {
	aired := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	long := strings.Repeat("Ünïcödé, ", 12)
	cal := ICalendar([]library.CalendarEntry{
		{EpisodeID: 7, Series: "Law; Order", Season: 1, Number: 2, Title: "One, two\nthree", Aired: aired},
		{EpisodeID: 8, Series: "Shōgun", Season: 1, Number: 1, Title: long, Aired: aired, Have: true},
	}, time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC))

	if !strings.HasSuffix(cal, "END:VCALENDAR\r\n") || strings.Contains(strings.ReplaceAll(cal, "\r\n", ""), "\n") {
		t.Fatal("lines do not all end in CRLF")
	}
	for _, line := range strings.Split(strings.TrimSuffix(cal, "\r\n"), "\r\n") {
		if len(line) > 75 {
			t.Errorf("a line of %d octets: %q", len(line), line)
		}
		if !utf8.ValidString(line) {
			t.Errorf("a fold split a character: %q", line)
		}
	}
	unfolded := strings.ReplaceAll(cal, "\r\n ", "")
	for _, want := range []string{
		`SUMMARY:Law\; Order S01E02 · One\, two\nthree`,
		"UID:episode-7@cmediastack",
		"DTSTART;VALUE=DATE:20261003", "DTEND;VALUE=DATE:20261004",
		"DESCRIPTION:Not in the library yet.", "DESCRIPTION:In the library.",
		"SUMMARY:Shōgun S01E01 · " + strings.ReplaceAll(long, ",", `\,`),
	} {
		if !strings.Contains(unfolded, want) {
			t.Errorf("missing %q in\n%s", want, unfolded)
		}
	}
}

// ADR-0041, decision 3.
func TestTheFeedIsWhatArrived(t *testing.T) {
	w := newScopeWorld(t)
	for id, at := range map[int64]string{w.shownFile: "2026-09-01T00:00:00Z", w.hiddenFile: "2026-09-02T00:00:00Z"} {
		if _, err := w.r.database.ExecContext(t.Context(),
			`UPDATE media_file SET imported_at = ? WHERE id = ?`, at, id); err != nil {
			t.Fatal(err)
		}
	}
	items := func(c *client) []string {
		t.Helper()
		token, _ := mintFeed(t, c)
		res := w.r.client().get("/api/v1/feeds/" + token + "/rss")
		var doc rssDoc
		if err := xml.Unmarshal([]byte(res.Raw), &doc); err != nil {
			t.Fatalf("not RSS: %v\n%s", err, res.Raw)
		}
		if res.Header.Get("Content-Type") != "application/rss+xml; charset=utf-8" {
			t.Errorf("content type %q", res.Header.Get("Content-Type"))
		}
		var out []string
		for _, it := range doc.Channel.Items {
			out = append(out, it.Title)
		}
		return out
	}
	if got := items(w.kid); strings.Join(got, "|") != "Paddington" {
		t.Errorf("the restricted account's feed: %q", got)
	}
	if got := items(w.admin); strings.Join(got, "|") != "Heat|Paddington" {
		t.Errorf("the administrator's feed, newest first: %q", got)
	}
}

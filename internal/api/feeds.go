package api

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
)

// A calendar and a feed, behind a token in the address (ADR-0041).

// CalendarSource is the episodes on the calendar.
type CalendarSource interface {
	Calendar(ctx context.Context, from, to time.Time) ([]library.CalendarEntry, error)
}

// ArrivalSource is what arrived in the library.
type ArrivalSource interface {
	RecentArrivals(ctx context.Context, limit int) ([]importer.Arrival, error)
}

// The calendar's window: a month back, three ahead.
const (
	calendarBack  = 30 * 24 * time.Hour
	calendarAhead = 90 * 24 * time.Hour
)

func (h *Handlers) feedURLs(token string) map[string]string {
	base := strings.TrimRight(h.baseURL, "/")
	return map[string]string{
		"calendar": base + "/api/v1/feeds/" + token + "/calendar.ics",
		"rss":      base + "/api/v1/feeds/" + token + "/rss",
	}
}

// MyFeeds says whether the caller has a feed address.
func (h *Handlers) MyFeeds(w http.ResponseWriter, r *http.Request) {
	st, err := h.svc.FeedStatus(r.Context())
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	body := map[string]any{"exists": st.Exists}
	if st.Exists {
		body["created_at"] = st.CreatedAt
		if st.LastUsedAt != nil {
			body["last_used_at"] = *st.LastUsedAt
		}
	}
	writeJSON(w, http.StatusOK, body)
}

// MintFeeds issues the caller's feed address, replacing any other.
func (h *Handlers) MintFeeds(w http.ResponseWriter, r *http.Request) {
	token, err := h.svc.MintFeedToken(r.Context(), ClientIP(r.Context()), r.UserAgent())
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	body := map[string]any{"token": token,
		"message": "Shown once. Anyone with these addresses can read your calendar and what arrived, " +
			"within what your account can see, until you replace or revoke them."}
	for k, v := range h.feedURLs(token) {
		body[k] = v
	}
	writeJSON(w, http.StatusCreated, body)
}

// RevokeFeeds ends the caller's feed address.
func (h *Handlers) RevokeFeeds(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RevokeFeedToken(r.Context(), ClientIP(r.Context()), r.UserAgent()); err != nil {
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
}

// feedContext authenticates the path's token, answering 404 for anything that
// is not a working one.
func (h *Handlers) feedContext(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	p, err := h.svc.FeedPrincipal(r.Context(), r.PathValue("token"))
	if err != nil {
		if !errors.Is(err, identity.ErrFeedTokenInvalid) {
			writeProblem(w, http.StatusInternalServerError, "internal error")
			return nil, false
		}
		writeProblem(w, http.StatusNotFound, "not found")
		return nil, false
	}
	return authz.WithPrincipal(r.Context(), p), true
}

// CalendarFeed answers the account's episodes as iCalendar.
func (h *Handlers) CalendarFeed(w http.ResponseWriter, r *http.Request) {
	if h.calendar == nil {
		writeProblem(w, http.StatusNotImplemented, "episode tracking is not wired")
		return
	}
	ctx, ok := h.feedContext(w, r)
	if !ok {
		return
	}
	now := time.Now().UTC()
	entries, err := h.calendar.Calendar(ctx, now.Add(-calendarBack), now.Add(calendarAhead))
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=900")
	_, _ = w.Write([]byte(ICalendar(entries, now)))
}

// ICalendar renders episodes as an RFC 5545 calendar: CRLF line ends, text
// escaped, lines folded at 75 octets.
func ICalendar(entries []library.CalendarEntry, stamp time.Time) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(foldICS(s)); b.WriteString("\r\n") }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//CMediaStack//Episodes//EN")
	line("CALSCALE:GREGORIAN")
	line("X-WR-CALNAME:" + escapeICS("CMediaStack episodes"))
	for _, e := range entries {
		day := e.Aired.UTC().Format("20060102")
		next := e.Aired.UTC().AddDate(0, 0, 1).Format("20060102")
		summary := fmt.Sprintf("%s S%02dE%02d", e.Series, e.Season, e.Number)
		if e.Title != "" {
			summary += " · " + e.Title
		}
		held := "Not in the library yet."
		if e.Have {
			held = "In the library."
		}
		line("BEGIN:VEVENT")
		line(fmt.Sprintf("UID:episode-%d@cmediastack", e.EpisodeID))
		line("DTSTAMP:" + stamp.UTC().Format("20060102T150405Z"))
		line("DTSTART;VALUE=DATE:" + day)
		line("DTEND;VALUE=DATE:" + next)
		line("SUMMARY:" + escapeICS(summary))
		line("DESCRIPTION:" + escapeICS(held))
		line("TRANSP:TRANSPARENT")
		line("END:VEVENT")
	}
	line("END:VCALENDAR")
	return b.String()
}

// escapeICS escapes TEXT per RFC 5545 §3.3.11.
func escapeICS(s string) string {
	r := strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", `\n`)
	return r.Replace(s)
}

// foldICS folds a content line at 75 octets, never inside a UTF-8 sequence,
// continuing with CRLF and a space (RFC 5545 §3.1).
func foldICS(s string) string {
	if len(s) <= 75 {
		return s
	}
	var b strings.Builder
	limit := 75
	for len(s) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		b.WriteString(s[:cut])
		b.WriteString("\r\n ")
		s = s[cut:]
		limit = 74 // the continuation's leading space counts
	}
	b.WriteString(s)
	return b.String()
}

type rssDoc struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Items       []rssItem `xml:"item"`
}

type rssItem struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	GUID    string `xml:"guid"`
	PubDate string `xml:"pubDate"`
}

// RSSFeed answers what arrived in the library, as RSS 2.0.
func (h *Handlers) RSSFeed(w http.ResponseWriter, r *http.Request) {
	if h.arrivals == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	ctx, ok := h.feedContext(w, r)
	if !ok {
		return
	}
	arrivals, err := h.arrivals.RecentArrivals(ctx, 50)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	base := strings.TrimRight(h.baseURL, "/")
	doc := rssDoc{Version: "2.0", Channel: rssChannel{
		Title: "CMediaStack — new in the library", Link: base + "/#library",
		Description: "What arrived in the library, newest first."}}
	for _, a := range arrivals {
		title := filmName(a.Item)
		if a.File.Season != nil && a.File.Episode != nil {
			title = fmt.Sprintf("%s S%02dE%02d", a.Item.Title, *a.File.Season, *a.File.Episode)
		}
		doc.Channel.Items = append(doc.Channel.Items, rssItem{
			Title: title, Link: base + "/#library",
			GUID:    fmt.Sprintf("cmediastack-file-%d", a.File.ID),
			PubDate: a.File.ImportedAt.UTC().Format(time.RFC1123Z),
		})
	}
	out, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=900")
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(out)
}

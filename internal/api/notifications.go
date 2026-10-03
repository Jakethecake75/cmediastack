package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jakethecake75/cmediastack/internal/notify"
)

// Notifications (ADR-0032): one Discord webhook, and what is sent to it.
//
// As with the metadata key, what is absent matters most: nothing here returns
// the webhook link — not whole, not masked, not its token's first characters.
// The link is the credential for posting into the operator's channel. What an
// operator needs is whether one is set, where it posts, and whether delivery
// is working, and those are here.

// NotificationService is what the API needs from the notifier.
type NotificationService interface {
	Status(ctx context.Context) (notify.Status, error)
	SetWebhook(ctx context.Context, raw, sourceIP, userAgent string) (notify.Info, error)
	SetCategories(ctx context.Context, ids []notify.Category, sourceIP, userAgent string) error
	Test(ctx context.Context) error
}

const notificationsNote = "The webhook link is never returned by this endpoint, in any form. " +
	"Send a new one to replace it, or an empty one to remove it."

// NotificationStatus reports the webhook, the categories and how delivery is
// going.
func (h *Handlers) NotificationStatus(w http.ResponseWriter, r *http.Request) {
	if h.notifications == nil {
		writeProblem(w, http.StatusNotImplemented, "no notifier is wired")
		return
	}
	st, err := h.notifications.Status(r.Context())
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	cats := make([]map[string]any, 0, len(st.Categories))
	for _, c := range st.Categories {
		cats = append(cats, map[string]any{
			"id": string(c.ID), "label": c.Label, "on": c.On, "default": c.Default,
			"sends": c.Sends, "titles": c.Titles,
		})
	}
	delivery := map[string]any{"stopped": st.Stopped}
	if !st.LastSent.IsZero() {
		delivery["last_sent"] = st.LastSent.UTC().Format(time.RFC3339)
		delivery["last_lines"] = st.LastLines
	}
	if st.LastError != "" {
		delivery["error"] = st.LastError
		if !st.LastErrorAt.IsZero() {
			delivery["error_at"] = st.LastErrorAt.UTC().Format(time.RFC3339)
		}
	}
	if !st.Waiting.IsZero() {
		delivery["waiting_until"] = st.Waiting.UTC().Format(time.RFC3339)
	}
	body := map[string]any{
		"configured": st.Configured,
		"categories": cats,
		"delivery":   delivery,
		"every":      int(notify.Interval / time.Second),
		"note":       notificationsNote,
	}
	if st.Configured {
		wh := map[string]any{"name": st.Webhook.Name, "channel_id": st.Webhook.ChannelID}
		if !st.CheckedAt.IsZero() {
			wh["checked_at"] = st.CheckedAt.UTC().Format(time.RFC3339)
		}
		body["webhook"] = wh
	}
	writeJSON(w, http.StatusOK, body)
}

type webhookRequest struct {
	URL string `json:"url"`
}

// SetNotificationWebhook stores a webhook after Discord has confirmed it, or
// removes the one there is.
func (h *Handlers) SetNotificationWebhook(w http.ResponseWriter, r *http.Request) {
	if h.notifications == nil {
		writeProblem(w, http.StatusNotImplemented, "no notifier is wired")
		return
	}
	var in webhookRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	info, err := h.notifications.SetWebhook(r.Context(), in.URL, ClientIP(r.Context()), r.UserAgent())
	if err != nil {
		if writeDiscordProblem(w, err, "Nothing was stored. The webhook this instance had, if any, is unchanged.") {
			return
		}
		writeAuthzAware(w, err)
		return
	}
	if in.URL == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"removed": true,
			"note":    "Removed. Nothing is sent until a webhook is set again, and then only what happens after.",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"webhook": map[string]any{"name": info.Name, "channel_id": info.ChannelID},
		"note": "Stored after Discord confirmed it — a check that posts nothing. Messages begin with " +
			"what happens from now on; send a test to see where they land.",
	})
}

type categoriesRequest struct {
	Categories []string `json:"categories"`
}

// SetNotificationCategories chooses what is sent.
func (h *Handlers) SetNotificationCategories(w http.ResponseWriter, r *http.Request) {
	if h.notifications == nil {
		writeProblem(w, http.StatusNotImplemented, "no notifier is wired")
		return
	}
	var in categoriesRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Categories == nil {
		writeProblem(w, http.StatusBadRequest, "categories is a list: every category to send, or none")
		return
	}
	ids := make([]notify.Category, 0, len(in.Categories))
	for _, c := range in.Categories {
		ids = append(ids, notify.Category(c))
	}
	err := h.notifications.SetCategories(r.Context(), ids, ClientIP(r.Context()), r.UserAgent())
	switch {
	case errors.Is(err, notify.ErrUnknownCategory):
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": in.Categories})
}

// SendTestNotification sends a test message.
func (h *Handlers) SendTestNotification(w http.ResponseWriter, r *http.Request) {
	if h.notifications == nil {
		writeProblem(w, http.StatusNotImplemented, "no notifier is wired")
		return
	}
	err := h.notifications.Test(r.Context())
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"sent": true,
			"note": "Discord accepted it: look in the channel."})
		return
	case errors.Is(err, notify.ErrNotConfigured):
		writeProblem(w, http.StatusConflict, "no Discord webhook is set")
		return
	case errors.Is(err, notify.ErrTooManyTests):
		writeProblem(w, http.StatusTooManyRequests, err.Error())
		return
	}
	if writeDiscordProblem(w, err, "") {
		return
	}
	writeAuthzAware(w, err)
}

// writeDiscordProblem answers an error that came from Discord, or from the
// link, and reports whether it was one.
func writeDiscordProblem(w http.ResponseWriter, err error, note string) bool {
	var rl *notify.RateLimited
	status := 0
	switch {
	case errors.Is(err, notify.ErrNotAWebhook), errors.Is(err, notify.ErrWebhookGone),
		errors.Is(err, notify.ErrNotPostable), errors.Is(err, notify.ErrRefused):
		status = http.StatusBadRequest
	case errors.As(err, &rl):
		w.Header().Set("Retry-After", strconv.Itoa(int(rl.RetryAfter/time.Second)))
		status = http.StatusServiceUnavailable
	case errors.Is(err, notify.ErrUnavailable):
		status = http.StatusBadGateway
	default:
		return false
	}
	body := map[string]any{"error": err.Error()}
	if note != "" {
		body["note"] = note
	}
	writeJSON(w, status, body)
	return true
}

// Package notify sends what the operator chose from the audit log to a Discord
// webhook (ADR-0032).
//
// Three rules hold everywhere in it. The webhook link is a credential: it is
// sealed at rest, never returned, and never printed — not in an error, not in
// a log line. Anything a stranger could have written is sent inside a code
// span, with mentions switched off, so it can neither ping a server nor pass
// itself off as a link. And Discord's answer is obeyed: a request to wait is
// waited out, and a webhook Discord no longer knows is not asked again.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Errors the transport distinguishes. None of them carries the link.
var (
	// ErrNotAWebhook is a link that is not a Discord webhook's.
	ErrNotAWebhook = errors.New("notify: that is not a Discord webhook link")
	// ErrWebhookGone is Discord not knowing the webhook: it was deleted, or
	// the link's token is wrong (401 or 404). Asking again will not help.
	ErrWebhookGone = errors.New("notify: Discord does not know this webhook — it was deleted, or the link is wrong")
	// ErrNotPostable is a webhook that exists but cannot be posted to: one
	// that follows another channel, not an incoming webhook.
	ErrNotPostable = errors.New("notify: that webhook cannot be posted to; create an incoming webhook in the channel's settings")
	// ErrRefused is Discord refusing a request as malformed.
	ErrRefused = errors.New("notify: Discord refused the message")
	// ErrUnavailable is Discord out of reach, or failing.
	ErrUnavailable = errors.New("notify: Discord could not be reached")
)

// RateLimited is Discord asking to wait before the next request.
type RateLimited struct {
	RetryAfter time.Duration
}

func (r *RateLimited) Error() string {
	return fmt.Sprintf("notify: Discord asked to wait %s before the next message", r.RetryAfter.Round(time.Second))
}

// The hosts a webhook link may name, and the one it is stored under. All of
// them serve the same API.
var discordHosts = map[string]bool{
	"discord.com": true, "discordapp.com": true,
	"canary.discord.com": true, "ptb.discord.com": true,
}

// A webhook path: an optional API version, a snowflake id and the token.
var reWebhookPath = regexp.MustCompile(`^/api/(?:v[0-9]{1,2}/)?webhooks/([0-9]{15,21})/([A-Za-z0-9_-]{20,200})/?$`)

// Webhook is a parsed webhook link. It prints as its id alone: the token is
// the credential, and a value that can be printed by accident will be.
type Webhook struct {
	id    string
	token string
}

// ID is the webhook's id, which is not a secret.
func (w Webhook) ID() string { return w.id }

// String names the webhook without its token.
func (w Webhook) String() string { return "Discord webhook " + w.id }

// GoString keeps %#v from printing the token.
func (w Webhook) GoString() string { return w.String() }

// url is the link in its one stored form. Never logged.
func (w Webhook) url() string {
	return "https://discord.com/api/webhooks/" + w.id + "/" + w.token
}

// ParseWebhook reads a webhook link as Discord hands it out, and refuses
// anything else: another scheme or host, a user name, a query, a fragment, a
// path that is not a webhook's.
func ParseWebhook(raw string) (Webhook, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 512 {
		return Webhook{}, ErrNotAWebhook
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return Webhook{}, ErrNotAWebhook
	}
	if port := u.Port(); port != "" && port != "443" {
		return Webhook{}, ErrNotAWebhook
	}
	if !discordHosts[strings.ToLower(u.Hostname())] {
		return Webhook{}, ErrNotAWebhook
	}
	m := reWebhookPath.FindStringSubmatch(u.EscapedPath())
	if m == nil {
		return Webhook{}, ErrNotAWebhook
	}
	return Webhook{id: m[1], token: m[2]}, nil
}

// ValidateRedirect is the check every redirect target must pass: HTTPS, to a
// Discord host. The link carries the token in its path, so it must never be
// followed anywhere else.
func ValidateRedirect(u *url.URL) error {
	if u.Scheme != "https" || !discordHosts[strings.ToLower(u.Hostname())] {
		return errors.New("notify: refusing to follow a redirect away from Discord")
	}
	return nil
}

// Doer sends a request. The egress guard's client, in production.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Discord is the transport.
type Discord struct {
	client Doer
}

// NewDiscord builds the transport over a client — the guard's, for the
// notification profile, with ValidateRedirect on every redirect.
func NewDiscord(client Doer) *Discord { return &Discord{client: client} }

// Info is what Discord says about a webhook. The names are the operator's,
// set in Discord.
type Info struct {
	Name      string
	ChannelID string
	GuildID   string
}

// Bounds on what is read back from Discord.
const (
	maxResponse = 64 << 10
	// suppressEmbeds is Discord's SUPPRESS_EMBEDS message flag: nothing in a
	// message is previewed.
	suppressEmbeds = 1 << 2
	// incomingWebhook is the type of a webhook that can be posted to.
	incomingWebhook = 1
	// maxWait bounds how long a rate limit is honoured before trying again.
	maxWait = time.Hour
)

// Check asks Discord about a webhook. It posts nothing.
func (d *Discord) Check(ctx context.Context, w Webhook) (Info, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url(), nil)
	if err != nil {
		return Info{}, ErrNotAWebhook
	}
	req.Header.Set("Accept", "application/json")
	body, err := d.do(req)
	if err != nil {
		return Info{}, err
	}
	var v struct {
		ID        string `json:"id"`
		Type      int    `json:"type"`
		Name      string `json:"name"`
		ChannelID string `json:"channel_id"`
		GuildID   string `json:"guild_id"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return Info{}, fmt.Errorf("%w: its answer could not be read", ErrUnavailable)
	}
	if v.ID != w.id {
		return Info{}, fmt.Errorf("%w: it answered about another webhook", ErrRefused)
	}
	if v.Type != incomingWebhook {
		return Info{}, ErrNotPostable
	}
	return Info{Name: v.Name, ChannelID: v.ChannelID, GuildID: v.GuildID}, nil
}

// Send posts one message. Mentions are switched off and embeds suppressed on
// every message, whatever it says.
func (d *Discord) Send(ctx context.Context, w Webhook, content string) error {
	payload, err := json.Marshal(map[string]any{
		"content":          content,
		"allowed_mentions": map[string]any{"parse": []string{}},
		"flags":            suppressEmbeds,
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRefused, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url(), bytes.NewReader(payload))
	if err != nil {
		return ErrNotAWebhook
	}
	req.Header.Set("Content-Type", "application/json")
	_, err = d.do(req)
	return err
}

// userAgent names the software, and nothing about the instance.
const userAgent = "CMediaStack-notifications"

// do sends a request and turns Discord's answer into one of the errors above.
func (d *Discord) do(req *http.Request) ([]byte, error) {
	req.Header.Set("User-Agent", userAgent)
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, transportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, fmt.Errorf("%w: reading its answer: %w", ErrUnavailable, cause(err))
	}

	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		return body, nil
	case code == http.StatusTooManyRequests:
		return nil, &RateLimited{RetryAfter: retryAfter(resp, body)}
	case code == http.StatusUnauthorized || code == http.StatusNotFound:
		return nil, ErrWebhookGone
	case code >= 500:
		return nil, fmt.Errorf("%w: it answered %d", ErrUnavailable, code)
	default:
		return nil, fmt.Errorf("%w: it answered %d%s", ErrRefused, code, discordMessage(body))
	}
}

// retryAfter reads how long Discord asked to wait: retry_after in the body,
// in seconds and possibly fractional, else the Retry-After header. Bounded
// both ways, so a missing or absurd answer neither hammers nor stops for good.
func retryAfter(resp *http.Response, body []byte) time.Duration {
	var v struct {
		RetryAfter float64 `json:"retry_after"`
	}
	secs := 0.0
	if json.Unmarshal(body, &v) == nil && v.RetryAfter > 0 {
		secs = v.RetryAfter
	} else if f, err := strconv.ParseFloat(strings.TrimSpace(resp.Header.Get("Retry-After")), 64); err == nil {
		secs = f
	}
	d := time.Duration(secs * float64(time.Second))
	switch {
	case d < time.Second:
		return time.Second
	case d > maxWait:
		return maxWait
	}
	return d
}

// discordMessage is Discord's own reason for a refusal, cleaned and clipped.
func discordMessage(body []byte) string {
	var v struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &v) != nil || v.Message == "" {
		return ""
	}
	return ": " + clean(v.Message, 160)
}

// transportError reduces a failed request to its cause. The request's URL is
// the credential, and *url.Error prints it.
func transportError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: it did not answer in time", ErrUnavailable)
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, cause(err))
}

func cause(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

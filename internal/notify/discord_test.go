package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testID = "1234567890123456789"
	// Obviously fake, and plain enough that no secret scanner takes it for one.
	testToken = "FAKE_notify_webhook_token_used_by_tests_only_xxxxxxxxxxxxxxxxxxxxxxx"
	testLink  = "https://discord.com/api/webhooks/" + testID + "/" + testToken
)

// A link is accepted only in the shapes Discord hands out, and stored in one.
func TestOnlyADiscordWebhookLinkIsAccepted(t *testing.T) {
	for _, raw := range []string{
		testLink,
		"  " + testLink + "\n",
		testLink + "/",
		"https://discordapp.com/api/webhooks/" + testID + "/" + testToken,
		"https://canary.discord.com/api/webhooks/" + testID + "/" + testToken,
		"https://ptb.discord.com/api/webhooks/" + testID + "/" + testToken,
		"https://DISCORD.com/api/v10/webhooks/" + testID + "/" + testToken,
		"https://discord.com:443/api/webhooks/" + testID + "/" + testToken,
	} {
		w, err := ParseWebhook(raw)
		if err != nil {
			t.Errorf("%q refused: %v", raw, err)
			continue
		}
		if w.url() != testLink || w.ID() != testID {
			t.Errorf("%q stored as %s", raw, w.url())
		}
	}
	for _, raw := range []string{
		"",
		"http://discord.com/api/webhooks/" + testID + "/" + testToken,
		"https://discord.com.evil.example/api/webhooks/" + testID + "/" + testToken,
		"https://evil.example/api/webhooks/" + testID + "/" + testToken,
		"https://evil.example/discord.com/api/webhooks/" + testID + "/" + testToken,
		"https://127.0.0.1/api/webhooks/" + testID + "/" + testToken,
		"https://user@discord.com/api/webhooks/" + testID + "/" + testToken,
		testLink + "?wait=true",
		testLink + "?",
		testLink + "#x",
		"https://discord.com:8443/api/webhooks/" + testID + "/" + testToken,
		"https://discord.com/api/webhooks/abc/" + testToken,
		"https://discord.com/api/webhooks/" + testID + "/",
		"https://discord.com/api/webhooks/" + testID + "/" + testToken + "/github",
		"https://discord.com/api/webhooks/" + testID + "/tok%2Fen" + testToken,
		"https://discord.com/api/webhooks/" + testID + "/short",
		"https://discord.com/api/webhooks/" + testID + "/" + testToken + "/../../users/@me",
		"https://discord.com/api/channels/" + testID + "/messages",
		"discord.com/api/webhooks/" + testID + "/" + testToken,
		"javascript:alert(1)",
		testLink + strings.Repeat("a", 500),
	} {
		if _, err := ParseWebhook(raw); !errors.Is(err, ErrNotAWebhook) {
			t.Errorf("%q accepted: %v", raw, err)
		}
	}
}

// A webhook prints as its id, however it is printed.
func TestAWebhookNeverPrintsItsToken(t *testing.T) {
	w, err := ParseWebhook(testLink)
	if err != nil {
		t.Fatal(err)
	}
	holder := struct{ W Webhook }{w}
	for _, s := range []string{
		fmt.Sprint(w), fmt.Sprintf("%v %+v %#v %s", w, w, w, w), fmt.Sprintf("%v %+v %#v", holder, holder, holder),
	} {
		if strings.Contains(s, testToken) || strings.Contains(s, testToken[:12]) {
			t.Fatalf("printed the token: %s", s)
		}
	}
}

// A redirect is followed only to Discord.
func TestARedirectIsFollowedOnlyWithinDiscord(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://discord.com/api/webhooks/1/x": true,
		"https://ptb.discord.com/x":            true,
		"http://discord.com/api/webhooks/1/x":  false,
		"https://evil.example/":                false,
		"https://discord.com.evil.example/":    false,
		"https://192.168.1.1/":                 false,
	} {
		u, _ := url.Parse(raw)
		if err := ValidateRedirect(u); (err == nil) != ok {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

// fakeDoer answers as Discord would, and keeps what it was sent.
type fakeDoer struct {
	mu     sync.Mutex
	reqs   []*http.Request
	bodies []string
	answer func(*http.Request) (*http.Response, error)
}

func (f *fakeDoer) Do(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	var body string
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}
	f.reqs = append(f.reqs, r)
	f.bodies = append(f.bodies, body)
	answer := f.answer
	f.mu.Unlock()
	if answer == nil {
		return respond(http.StatusNoContent, "", nil), nil
	}
	return answer(r)
}

func respond(code int, body string, header map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range header {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: code, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

// A message goes as JSON with mentions switched off and embeds suppressed,
// whatever it says.
func TestAMessageSwitchesMentionsOffAndSuppressesEmbeds(t *testing.T) {
	f := &fakeDoer{}
	d := NewDiscord(f)
	w, _ := ParseWebhook(testLink)
	if err := d.Send(context.Background(), w, "@everyone hello"); err != nil {
		t.Fatal(err)
	}
	r := f.reqs[0]
	if r.Method != http.MethodPost || r.URL.String() != testLink || r.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("sent %s %s %q", r.Method, r.URL, r.Header.Get("Content-Type"))
	}
	var body struct {
		Content         string                     `json:"content"`
		AllowedMentions map[string]json.RawMessage `json:"allowed_mentions"`
		Flags           *int                       `json:"flags"`
	}
	if err := json.Unmarshal([]byte(f.bodies[0]), &body); err != nil {
		t.Fatal(err)
	}
	if body.Content != "@everyone hello" || string(body.AllowedMentions["parse"]) != "[]" ||
		body.Flags == nil || *body.Flags != 4 {
		t.Fatalf("body = %s", f.bodies[0])
	}
}

// Discord's answers are told apart, and no error carries the link.
func TestDiscordsAnswersAreToldApart(t *testing.T) {
	w, _ := ParseWebhook(testLink)
	for name, tc := range map[string]struct {
		answer func(*http.Request) (*http.Response, error)
		want   error
		wait   time.Duration
	}{
		"accepted":         {func(*http.Request) (*http.Response, error) { return respond(204, "", nil), nil }, nil, 0},
		"accepted, waited": {func(*http.Request) (*http.Response, error) { return respond(200, "{}", nil), nil }, nil, 0},
		"wait, in the body": {func(*http.Request) (*http.Response, error) {
			return respond(429, `{"message":"You are being rate limited.","retry_after":2.5,"global":false}`, nil), nil
		}, nil, 2500 * time.Millisecond},
		"wait, in the header": {func(*http.Request) (*http.Response, error) {
			return respond(429, ``, map[string]string{"Retry-After": "7"}), nil
		}, nil, 7 * time.Second},
		"wait, absurdly": {func(*http.Request) (*http.Response, error) {
			return respond(429, `{"retry_after":999999}`, nil), nil
		}, nil, time.Hour},
		"wait, unsaid": {func(*http.Request) (*http.Response, error) { return respond(429, ``, nil), nil }, nil, time.Second},
		"unknown webhook": {func(*http.Request) (*http.Response, error) {
			return respond(404, `{"message":"Unknown Webhook","code":10015}`, nil), nil
		}, ErrWebhookGone, 0},
		"wrong token": {func(*http.Request) (*http.Response, error) {
			return respond(401, `{"message":"Invalid Webhook Token","code":50027}`, nil), nil
		}, ErrWebhookGone, 0},
		"malformed": {func(*http.Request) (*http.Response, error) {
			return respond(400, `{"message":"Cannot send an empty message","code":50006}`, nil), nil
		}, ErrRefused, 0},
		"failing": {func(*http.Request) (*http.Response, error) { return respond(502, "bad gateway", nil), nil }, ErrUnavailable, 0},
		"unreachable": {func(r *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Post", URL: r.URL.String(), Err: errors.New("dial tcp: connection refused")}
		}, ErrUnavailable, 0},
		"too slow": {func(r *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Post", URL: r.URL.String(), Err: context.DeadlineExceeded}
		}, ErrUnavailable, 0},
		"a redirect away": {func(r *http.Request) (*http.Response, error) {
			return nil, &url.Error{Op: "Post", URL: r.URL.String(), Err: errors.New("notify: refusing to follow a redirect away from Discord")}
		}, ErrUnavailable, 0},
	} {
		d := NewDiscord(&fakeDoer{answer: tc.answer})
		err := d.Send(context.Background(), w, "hello")
		var rl *RateLimited
		switch {
		case tc.wait > 0:
			if !errors.As(err, &rl) || rl.RetryAfter != tc.wait {
				t.Errorf("%s: %v, want a wait of %s", name, err, tc.wait)
			}
		case tc.want == nil:
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
		default:
			if !errors.Is(err, tc.want) {
				t.Errorf("%s: %v, want %v", name, err, tc.want)
			}
		}
		if err != nil && (strings.Contains(err.Error(), testToken[:12]) || strings.Contains(err.Error(), "webhooks/")) {
			t.Errorf("%s: the error carries the link: %v", name, err)
		}
	}
	d := NewDiscord(&fakeDoer{answer: func(*http.Request) (*http.Response, error) {
		return respond(400, `{"message":"Cannot send an empty message","code":50006}`, nil), nil
	}})
	if err := d.Send(context.Background(), w, ""); err == nil || !strings.Contains(err.Error(), "Cannot send an empty message") {
		t.Errorf("Discord's reason is not said: %v", err)
	}
}

// A check asks Discord about the webhook and posts nothing; only an incoming
// webhook, the one asked about, passes.
func TestACheckPostsNothing(t *testing.T) {
	w, _ := ParseWebhook(testLink)
	// Discord answering 200 with a body; the client under test closes it.
	saying := func(body string) *fakeDoer {
		return &fakeDoer{answer: func(*http.Request) (*http.Response, error) {
			return respond(200, body, nil), nil
		}}
	}
	f := saying(`{"id":"` + testID + `","type":1,"name":"CMediaStack","channel_id":"42","guild_id":"7","token":"` + testToken + `"}`)
	info, err := NewDiscord(f).Check(context.Background(), w)
	if err != nil || info.Name != "CMediaStack" || info.ChannelID != "42" || info.GuildID != "7" {
		t.Fatalf("%+v %v", info, err)
	}
	if f.reqs[0].Method != http.MethodGet || f.bodies[0] != "" {
		t.Fatalf("a check sent %s with %q", f.reqs[0].Method, f.bodies[0])
	}
	for body, want := range map[string]error{
		`{"id":"` + testID + `","type":2,"name":"follower"}`: ErrNotPostable,
		`{"id":"999","type":1,"name":"another"}`:             ErrRefused,
		`not json`:                                           ErrUnavailable,
	} {
		if _, err := NewDiscord(saying(body)).Check(context.Background(), w); !errors.Is(err, want) {
			t.Errorf("%s: %v, want %v", body, err, want)
		}
	}
}

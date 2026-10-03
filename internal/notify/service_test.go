package notify

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
	"github.com/jakethecake75/cmediastack/internal/platform/tasks"
)

// ---------------------------------------------------------------------------
// the rig: a real audit log and cipher, settings in a map, Discord faked

type settings struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *settings) Setting(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	if !ok {
		return "", identity.ErrNotFound
	}
	return v, nil
}

func (s *settings) SetSetting(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = value
	return nil
}

// fakeDiscord keeps what it was sent, and answers as it is told.
type fakeDiscord struct {
	mu      sync.Mutex
	checks  int
	sent    []string
	to      []string // the webhook each message went to
	info    Info
	checkEr error
	// errs answers the sends in turn; past its end, a send succeeds.
	errs []error
}

func (f *fakeDiscord) Check(context.Context, Webhook) (Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.info, f.checkEr
}

func (f *fakeDiscord) Send(_ context.Context, w Webhook, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return err
		}
	}
	f.sent = append(f.sent, content)
	f.to = append(f.to, w.ID())
	return nil
}

func (f *fakeDiscord) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent, f.to = nil, nil
	return out
}

type rig struct {
	t        *testing.T
	svc      *Service
	discord  *fakeDiscord
	settings *settings
	log      *audit.Logger
	database *db.DB
	cipher   *secrets.Cipher
	mu       sync.Mutex
	clock    time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	d, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "notify.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipherFromBase64(key)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, settings: &settings{m: map[string]string{}}, database: d, cipher: cipher,
		clock:   time.Date(2026, 9, 27, 20, 0, 0, 0, time.UTC),
		discord: &fakeDiscord{info: Info{Name: "CMediaStack", ChannelID: "42", GuildID: "7"}}}
	r.log = audit.New(d, r.now)
	r.svc = NewService(r.settings, cipher, r.log, r.discord, r.now)
	return r
}

func (r *rig) now() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clock
}

func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clock = r.clock.Add(d)
}

func principal(role string, id int64, name string) context.Context {
	for _, ro := range authz.BuiltinRoles() {
		if ro.Name == role {
			return authz.WithPrincipal(context.Background(), &authz.Principal{
				UserID: id, Username: name, Role: ro, State: authz.StateActive, MFASatisfied: true})
		}
	}
	panic(role)
}

func admin() context.Context { return principal(authz.RoleAdmin, 1, "jacob") }

func task() context.Context { return authz.SystemPrincipal(context.Background(), authz.TaskNotify) }

func (r *rig) write(e audit.Event) {
	r.t.Helper()
	if err := r.log.Write(context.Background(), e); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) configure() {
	r.t.Helper()
	if _, err := r.svc.SetWebhook(admin(), testLink, "192.0.2.7", "firefox"); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) run() (string, error) {
	r.t.Helper()
	return r.svc.Run(task())
}

// runSends runs the task and returns what it sent, all messages joined.
func (r *rig) runSends() string {
	r.t.Helper()
	if _, err := r.run(); err != nil {
		r.t.Fatal(err)
	}
	return strings.Join(r.discord.messages(), "\n")
}

// ---------------------------------------------------------------------------

// A link is kept only once Discord has confirmed it, sealed, and never in the
// audit log; the first starts from the newest line.
func TestAWebhookIsCheckedSealedAndNeverWrittenDown(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.SetWebhook(admin(), "https://evil.example/api/webhooks/1/x", "", ""); !errors.Is(err, ErrNotAWebhook) {
		t.Fatalf("a link to elsewhere: %v", err)
	}
	r.discord.checkEr = ErrWebhookGone
	if _, err := r.svc.SetWebhook(admin(), testLink, "", ""); !errors.Is(err, ErrWebhookGone) {
		t.Fatalf("a link Discord does not know: %v", err)
	}
	if len(r.settings.m) != 0 {
		t.Fatalf("a refused link stored something: %v", r.settings.m)
	}

	r.discord.checkEr = nil
	info, err := r.svc.SetWebhook(admin(), testLink, "192.0.2.7", "firefox")
	if err != nil || info.Name != "CMediaStack" {
		t.Fatalf("%+v %v", info, err)
	}
	stored := r.settings.m[keyWebhook]
	if stored == "" || strings.Contains(stored, testToken[:12]) {
		t.Fatalf("stored %q", stored)
	}
	sealed, _ := base64.StdEncoding.DecodeString(stored)
	if _, err := r.cipher.DecryptString(sealed, "setting:metadata.tmdb_token"); err == nil {
		t.Fatal("the sealed link opens under another setting's context")
	}
	st, err := r.svc.Status(admin())
	if err != nil || !st.Configured || st.Webhook.Name != "CMediaStack" || st.CheckedAt.IsZero() {
		t.Fatalf("%+v %v", st, err)
	}

	// Nothing in the database holds the token: not the audit log, not a
	// setting in the clear.
	rows, err := r.database.QueryContext(context.Background(),
		`SELECT COALESCE(actor_label,'')||COALESCE(target_id,'')||COALESCE(detail,'')||COALESCE(before_json,'')||COALESCE(after_json,'') FROM audit_event`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var lines []string
	for rows.Next() {
		var s sql.NullString
		_ = rows.Scan(&s)
		lines = append(lines, s.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(lines, "\n")
	if strings.Contains(all, testToken[:12]) || strings.Contains(all, "webhooks/") {
		t.Fatalf("the audit log holds the link: %s", all)
	}
	if !strings.Contains(all, `a Discord webhook was stored, and Discord confirmed it: it posts as "CMediaStack"`) {
		t.Fatalf("the change is not audited: %s", all)
	}
}

// Only an administrator configures it — the settings AND the audit log, since
// choosing what leaves the log is reading it — and the task can do nothing
// but deliver.
func TestOnlyAnAdministratorConfiguresIt(t *testing.T) {
	r := newRig(t)
	settingsOnly := authz.Role{Name: "Settings", Rank: authz.RankManager,
		Permissions: authz.NewPermissionSet(authz.PermSystemSettings)}
	for name, ctx := range map[string]context.Context{
		"nobody":    context.Background(),
		"a manager": principal(authz.RoleManager, 5, "morgan"),
		"a user":    principal(authz.RoleUser, 6, "sam"),
		"the task":  task(),
		"settings without the audit log": authz.WithPrincipal(context.Background(), &authz.Principal{
			UserID: 7, Username: "half", Role: settingsOnly, State: authz.StateActive, MFASatisfied: true}),
	} {
		if _, err := r.svc.SetWebhook(ctx, testLink, "", ""); !authz.IsDenied(err) {
			t.Errorf("%s set the webhook: %v", name, err)
		}
		if err := r.svc.SetCategories(ctx, []Category{Library}, "", ""); !authz.IsDenied(err) {
			t.Errorf("%s chose the categories: %v", name, err)
		}
		if name == "settings without the audit log" {
			continue // may read the status and send a test: it sends nothing from the log
		}
		if _, err := r.svc.Status(ctx); !authz.IsDenied(err) {
			t.Errorf("%s read the status: %v", name, err)
		}
		if err := r.svc.Test(ctx); !authz.IsDenied(err) {
			t.Errorf("%s sent a test: %v", name, err)
		}
	}
}

// Nothing written before the webhook was set is sent: the first run sends the
// setting's own line, and nothing older.
func TestTheHistoryIsNotSent(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 5; i++ {
		r.write(audit.Event{Action: audit.ActionAccountRequested, TargetKind: "account_request", TargetID: fmt.Sprint("old", i)})
	}
	r.configure()
	got := r.runSends()
	if strings.Contains(got, "old") || !strings.Contains(got, "**Setting changed** · `jacob` → `setting notification.discord_webhook`") {
		t.Fatalf("sent %q", got)
	}
	if again := r.runSends(); again != "" {
		t.Fatalf("sent twice: %q", again)
	}
}

// What is sent is what the categories say: by default security, accounts and
// operations, and not the library or requests — which carry titles.
func TestWhatIsSentFollowsTheCategories(t *testing.T) {
	r := newRig(t)
	r.configure()
	r.runSends()
	jacob := int64(1)
	r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionLoginFailed, Outcome: audit.OutcomeDenied,
		SourceIP: "203.0.113.9", Detail: "throttled: ip:203.0.113.9"})
	r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionLoginFailed, Outcome: audit.OutcomeFailure,
		SourceIP: "203.0.113.9", Detail: "bad password"})
	r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionAccountRequested, TargetKind: "account_request", TargetID: "newcomer"})
	r.write(audit.Event{ActorUserID: &jacob, ActorLabel: "jacob", Action: audit.ActionLoginSucceeded, SourceIP: "192.0.2.7"})
	r.write(audit.Event{ActorLabel: "system:acquire", Action: audit.ActionReleaseGrabbed, TargetKind: "release",
		Detail: "Severance.S02E03.1080p.WEB-DL | for Severance S02E03"})
	r.write(audit.Event{ActorLabel: "sam", Action: audit.ActionRequestSubmitted, Detail: "Dune (2021)"})
	r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionAuthzDenied, Outcome: audit.OutcomeDenied,
		TargetID: "GET /api/v1/admin/users", SourceIP: "198.51.100.4"})

	got := r.runSends()
	for _, want := range []string{"🔒 **Sign-ins throttled** · `anonymous`: `throttled: ip:203.0.113.9`",
		"👤 **Account requested — waiting for approval** · `anonymous` → `account_request newcomer`"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	for _, not := range []string{"bad password", "192.0.2.7", "Severance", "Dune", "admin/users"} {
		if strings.Contains(got, not) {
			t.Errorf("sent %q: %q", not, got)
		}
	}
	// Two lines and nothing else: not the sign-in, not the anonymous denial.
	if n := strings.Count(got, "\n") + 1; n != 2 {
		t.Errorf("%d lines sent: %q", n, got)
	}

	if err := r.svc.SetCategories(admin(), []Category{Library, Requests}, "192.0.2.7", "firefox"); err != nil {
		t.Fatal(err)
	}
	r.write(audit.Event{ActorLabel: "system:acquire", Action: audit.ActionReleaseGrabbed, Outcome: audit.OutcomeFailure,
		Detail: "Dune.2021.1080p | fetching failed"})
	r.write(audit.Event{ActorLabel: "system:import", Action: audit.ActionMediaImported, TargetKind: "media_item",
		TargetID: "12", Detail: "Dune (2021), Bluray-1080p"})
	r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionAccountRequested, TargetKind: "account_request", TargetID: "second"})
	got = r.runSends()
	if !strings.Contains(got, "🎬 **Grab failed** · `system:acquire`: `Dune.2021.1080p | fetching failed`") ||
		!strings.Contains(got, "🎬 **Arrived in the library** · `system:import` → `media_item 12`: `Dune (2021), Bluray-1080p`") ||
		strings.Contains(got, "second") || strings.Contains(got, "Setting changed") {
		t.Fatalf("with library and requests only: %q", got)
	}
	if err := r.svc.SetCategories(admin(), []Category{"everything"}, "", ""); !errors.Is(err, ErrUnknownCategory) {
		t.Fatalf("an unknown category: %v", err)
	}
	st, _ := r.svc.Status(admin())
	var on []string
	for _, c := range st.Categories {
		if c.On {
			on = append(on, string(c.ID))
		}
	}
	if strings.Join(on, ",") != "library,requests" {
		t.Fatalf("status says %v", on)
	}
}

// A stranger's text arrives as text: a signup named to ping the server or to
// pass for a link does neither.
func TestAStrangersNameCannotPingOrPassForALink(t *testing.T) {
	r := newRig(t)
	r.configure()
	r.runSends()
	r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionAccountRequested, TargetKind: "account_request",
		TargetID: "@everyone [Fix your server](https://evil.example) `x`\n**now**"})
	got := r.runSends()
	want := "`account_request @everyone [Fix your server](https://evil.example) 'x' **now**`"
	if !strings.Contains(got, want) || strings.Count(got, "\n") != 0 {
		t.Fatalf("sent %q", got)
	}
}

// A run sends at most three messages; identical lines are one with a count;
// what does not fit is counted and not sent later.
func TestDeliveryIsBoundedAndCounted(t *testing.T) {
	r := newRig(t)
	r.configure()
	r.runSends()
	for i := 0; i < 40; i++ {
		r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionLoginFailed, Outcome: audit.OutcomeDenied,
			Detail: "throttled: user:jacob"})
	}
	got := r.runSends()
	if strings.Count(got, "Sign-ins throttled") != 1 || !strings.Contains(got, "`throttled: user:jacob` ×40") {
		t.Fatalf("forty identical lines sent as %q", got)
	}

	for i := 0; i < 300; i++ {
		r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionAccountRequested, TargetKind: "account_request",
			TargetID: fmt.Sprintf("applicant-%03d-%s", i, strings.Repeat("x", 40))})
	}
	summary, err := r.run()
	msgs := r.discord.messages()
	if err != nil || len(msgs) != maxMessages || !strings.Contains(msgs[2], "more — the Audit log has them all.") ||
		!strings.Contains(summary, "were counted, not listed") {
		t.Fatalf("%d messages (%v): %q", len(msgs), err, summary)
	}
	for _, m := range msgs {
		if width(m) > 2000 {
			t.Fatalf("a message of %d", width(m))
		}
	}
	if again := r.runSends(); again != "" {
		t.Fatalf("what did not fit was sent later: %q", again)
	}
}

// A send that fails is sent again next run; a message Discord refuses as
// malformed is passed over; a request to wait is obeyed.
func TestFailuresAreRetriedRefusalsPassedOverWaitsObeyed(t *testing.T) {
	r := newRig(t)
	r.configure()
	r.runSends()
	r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionAccountRequested, TargetKind: "account_request", TargetID: "first"})

	r.discord.errs = []error{fmt.Errorf("%w: it answered 502", ErrUnavailable)}
	if _, err := r.run(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a failed send: %v", err)
	}
	if st, _ := r.svc.Status(admin()); !strings.Contains(st.LastError, "502") {
		t.Fatalf("the failure is not on the screen: %+v", st)
	}
	if got := r.runSends(); !strings.Contains(got, "first") {
		t.Fatalf("not sent again: %q", got)
	}
	if st, _ := r.svc.Status(admin()); st.LastError != "" || st.LastSent.IsZero() {
		t.Fatalf("a delivery did not clear the failure: %+v", st)
	}

	r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionAccountRequested, TargetKind: "account_request", TargetID: "second"})
	r.discord.errs = []error{fmt.Errorf("%w: it answered 400", ErrRefused)}
	if _, err := r.run(); !errors.Is(err, ErrRefused) {
		t.Fatalf("a refused send: %v", err)
	}
	if got := r.runSends(); strings.Contains(got, "second") {
		t.Fatalf("a refused message was retried: %q", got)
	}

	r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionAccountRequested, TargetKind: "account_request", TargetID: "third"})
	r.discord.errs = []error{&RateLimited{RetryAfter: 90 * time.Second}}
	if _, err := r.run(); err == nil {
		t.Fatal("a rate limit was not reported")
	}
	r.advance(time.Minute)
	if summary, err := r.run(); err != nil || !strings.Contains(summary, "asked to wait") || len(r.discord.messages()) != 0 {
		t.Fatalf("a run before the wait was over: %q %v", summary, err)
	}
	if st, _ := r.svc.Status(admin()); st.Waiting.IsZero() {
		t.Fatalf("the wait is not on the screen: %+v", st)
	}
	r.advance(31 * time.Second)
	if got := r.runSends(); !strings.Contains(got, "third") {
		t.Fatalf("not sent after the wait: %q", got)
	}
}

// A webhook Discord no longer knows stops delivery — it is not asked again
// every minute — until it is replaced.
func TestAWebhookDiscordForgetsStopsDelivery(t *testing.T) {
	r := newRig(t)
	r.configure()
	r.runSends()
	r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionAccountRequested, TargetKind: "account_request", TargetID: "waiting"})
	r.discord.errs = []error{ErrWebhookGone}
	if _, err := r.run(); !errors.Is(err, ErrWebhookGone) {
		t.Fatalf("%v", err)
	}
	r.discord.errs = []error{errors.New("asked again")}
	for i := 0; i < 3; i++ {
		if _, err := r.run(); err == nil || !strings.Contains(err.Error(), "Replace the webhook") {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if len(r.discord.errs) != 1 {
		t.Fatal("Discord was asked again about a webhook it does not know")
	}
	if st, _ := r.svc.Status(admin()); !st.Stopped {
		t.Fatalf("status %+v", st)
	}

	r.discord.errs = nil
	if _, err := r.svc.SetWebhook(admin(), testLink, "", ""); err != nil {
		t.Fatal(err)
	}
	got := r.runSends()
	if !strings.Contains(got, "waiting") || !strings.Contains(got, "the Discord webhook was replaced") {
		t.Fatalf("after replacing it: %q", got)
	}
}

// A task is reported when it starts failing and when it recovers — never each
// repeat — with the tunnel and backups named for what they are.
func TestATaskIsReportedWhenItStartsFailingAndWhenItRecovers(t *testing.T) {
	r := newRig(t)
	r.configure()
	r.runSends()
	at := r.now()
	outcome := func(name, err string) {
		r.svc.TaskFinished(name, tasks.Outcome{StartedAt: at, Err: err})
		at = at.Add(time.Second)
	}
	outcome("database.backup", "")
	outcome("database.backup", "the backup directory /backups does not exist")
	outcome("database.backup", "the backup directory /backups does not exist")
	outcome("egress.health", "no route through wg0")
	outcome("acquire.search", "no indexer answered")
	outcome(TaskName, "Discord could not be reached")
	got := r.runSends()
	for _, want := range []string{
		"⚙️ **Backups are failing** · `database.backup`: `the backup directory /backups does not exist`",
		"⚙️ **The tunnel is down — transfers are paused** · `egress.health`: `no route through wg0`",
		"⚙️ **A scheduled task is failing** · `acquire.search`: `no indexer answered`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Count(got, "Backups are failing") != 1 || strings.Contains(got, TaskName) {
		t.Fatalf("sent %q", got)
	}

	outcome("database.backup", "the backup directory /backups does not exist")
	outcome("database.backup", "")
	outcome("database.backup", "")
	outcome("egress.health", "")
	got = r.runSends()
	if strings.Count(got, "Backups are working again") != 1 || !strings.Contains(got, "**The tunnel is back** · `egress.health`") ||
		strings.Contains(got, "failing**") || !strings.Contains(got, "failing since <t:") {
		t.Fatalf("recovery sent as %q", got)
	}

	// With operations off, nothing about tasks is kept for later.
	if err := r.svc.SetCategories(admin(), []Category{Security}, "", ""); err != nil {
		t.Fatal(err)
	}
	r.runSends()
	outcome("database.backup", "failing again")
	if got := r.runSends(); strings.Contains(got, "Backups") {
		t.Fatalf("operations off, sent %q", got)
	}
	if err := r.svc.SetCategories(admin(), []Category{Operations}, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := r.runSends(); strings.Contains(got, "Backups are failing") {
		t.Fatalf("a note from while operations were off was kept: %q", got)
	}
}

// Lines more than a day old when they would be sent are counted, not listed.
func TestOldLinesAreCountedNotListed(t *testing.T) {
	r := newRig(t)
	r.configure()
	r.runSends()
	for i := 0; i < 4; i++ {
		r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionAccountRequested, TargetKind: "account_request",
			TargetID: fmt.Sprint("long-ago-", i)})
	}
	r.advance(25 * time.Hour)
	r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionAccountRequested, TargetKind: "account_request", TargetID: "today"})
	got := r.runSends()
	if strings.Contains(got, "long-ago") || !strings.Contains(got, "⏳ **4 older line(s) were not sent one by one**") ||
		!strings.Contains(got, "today") {
		t.Fatalf("sent %q", got)
	}
}

// Test messages go to the webhook there is, at most five a minute.
func TestTestMessagesAreLimited(t *testing.T) {
	r := newRig(t)
	if err := r.svc.Test(admin()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("with no webhook: %v", err)
	}
	r.advance(time.Minute)
	r.configure()
	for i := 0; i < maxTests; i++ {
		if err := r.svc.Test(admin()); err != nil {
			t.Fatalf("test %d: %v", i, err)
		}
	}
	if err := r.svc.Test(admin()); !errors.Is(err, ErrTooManyTests) {
		t.Fatalf("a sixth: %v", err)
	}
	msgs := r.discord.messages()
	if len(msgs) != maxTests || !strings.Contains(msgs[0], "this channel is sent: Security, Accounts, Operations") {
		t.Fatalf("sent %q", msgs)
	}
	r.advance(time.Minute)
	if err := r.svc.Test(admin()); err != nil {
		t.Fatalf("a minute later: %v", err)
	}
}

// Removing the webhook stops everything, and a new one starts from the newest
// line again.
func TestRemovingTheWebhookStopsEverything(t *testing.T) {
	r := newRig(t)
	r.configure()
	r.runSends()
	if _, err := r.svc.SetWebhook(admin(), "", "192.0.2.7", "firefox"); err != nil {
		t.Fatal(err)
	}
	st, _ := r.svc.Status(admin())
	if st.Configured {
		t.Fatalf("status %+v", st)
	}
	r.discord.messages() // the farewell: TestLeavingAChannelIsSaidInIt
	r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionAccountRequested, TargetKind: "account_request", TargetID: "meanwhile"})
	r.svc.TaskFinished("database.backup", tasks.Outcome{StartedAt: r.now(), Err: "failing"})
	if got := r.runSends(); got != "" {
		t.Fatalf("sent with no webhook: %q", got)
	}
	r.configure()
	got := r.runSends()
	if strings.Contains(got, "meanwhile") || strings.Contains(got, "Backups") || !strings.Contains(got, "a Discord webhook was stored") {
		t.Fatalf("after setting it again: %q", got)
	}
	var removed int
	_ = r.database.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_event WHERE detail = 'the Discord webhook was removed: nothing is sent'`).Scan(&removed)
	if removed != 1 {
		t.Fatalf("the removal is not audited: %d", removed)
	}
}

// A stored link that does not open is reported, and can be replaced.
func TestADamagedLinkIsReportedAndReplaceable(t *testing.T) {
	r := newRig(t)
	r.settings.m[keyWebhook] = base64.StdEncoding.EncodeToString([]byte("not sealed by this key"))
	st, err := r.svc.Status(admin())
	if err != nil || !st.Configured || !strings.Contains(st.LastError, "does not open") {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := r.run(); err == nil {
		t.Fatal("a run over a damaged link reported nothing")
	}
	info, err := r.svc.SetWebhook(admin(), testLink, "", "")
	if err != nil || info.Name != "CMediaStack" {
		t.Fatalf("replacing it: %v", err)
	}
	if st, _ := r.svc.Status(admin()); st.LastError != "" {
		t.Fatalf("still reported: %+v", st)
	}
}

// Turning notifications off, or pointing them at another webhook, is said in
// the channel they leave, with who did it: an attacker with the
// administrator's session does it in front of the operator.
func TestLeavingAChannelIsSaidInIt(t *testing.T) {
	r := newRig(t)
	r.configure()
	r.runSends()
	other := "https://discord.com/api/webhooks/9876543210987654321/" + testToken

	if _, err := r.svc.SetWebhook(admin(), other, "", ""); err != nil {
		t.Fatal(err)
	}
	r.discord.mu.Lock()
	sent, to := append([]string(nil), r.discord.sent...), append([]string(nil), r.discord.to...)
	r.discord.mu.Unlock()
	r.discord.messages()
	if len(sent) != 1 || to[0] != testID ||
		!strings.HasPrefix(sent[0], "🔕 **Notifications now go to another webhook** · `jacob`: it posts as `CMediaStack` · <t:") {
		t.Fatalf("the old channel was told %q (to %v)", sent, to)
	}

	// The same webhook again is not a move.
	if _, err := r.svc.SetWebhook(admin(), other, "", ""); err != nil {
		t.Fatal(err)
	}
	if got := r.discord.messages(); len(got) != 0 {
		t.Fatalf("setting the same webhook again said %q", got)
	}

	if _, err := r.svc.SetWebhook(admin(), "", "", ""); err != nil {
		t.Fatal(err)
	}
	r.discord.mu.Lock()
	sent, to = append([]string(nil), r.discord.sent...), append([]string(nil), r.discord.to...)
	r.discord.mu.Unlock()
	if len(sent) != 1 || to[0] != "9876543210987654321" ||
		!strings.HasPrefix(sent[0], "🔕 **Notifications were turned off** · `jacob` · <t:") {
		t.Fatalf("turning off said %q (to %v)", sent, to)
	}

	// A channel Discord has forgotten is not told; the change is made anyway.
	r.discord.messages()
	r.configure()
	r.discord.messages()
	r.write(audit.Event{ActorLabel: "anonymous", Action: audit.ActionAccountRequested, TargetKind: "account_request", TargetID: "x"})
	r.discord.errs = []error{ErrWebhookGone}
	_, _ = r.run()
	if _, err := r.svc.SetWebhook(admin(), "", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := r.discord.messages(); len(got) != 0 {
		t.Fatalf("a forgotten webhook was told %q", got)
	}
	if st, _ := r.svc.Status(admin()); st.Configured {
		t.Fatal("not removed")
	}
}

package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
	"github.com/jakethecake75/cmediastack/internal/platform/tasks"
)

// Where the configuration lives: the setting table, as the metadata key does
// (ADR-0018). Only the webhook is secret.
const (
	keyWebhook     = "notification.discord_webhook_enc"
	sealContext    = "setting:notification.discord_webhook"
	keyWebhookInfo = "notification.discord_webhook_info"

	// SealedSetting and SealedContext are the webhook's setting and what it is
	// sealed under, for the key rotation (ADR-0054).
	SealedSetting = keyWebhook
	SealedContext = sealContext
	keyCategories = "notification.categories"
	keyCursor     = "notification.cursor"

	// SettingWebhook and SettingCategories are what the audit log names.
	SettingWebhook    = "notification.discord_webhook"
	SettingCategories = "notification.categories"
)

// Bounds on delivery (ADR-0032, decision 6).
const (
	// TaskName is the scheduled task that delivers, and Interval how often.
	TaskName = "notify.discord"
	Interval = time.Minute

	readLimit   = 500
	maxMessages = 3
	// staleAfter: a line older than this when it would be sent is counted,
	// not listed.
	staleAfter = 24 * time.Hour
	// maxNotes bounds the task failures held in memory for the next run.
	maxNotes = 100
	// maxTests is how many test messages a minute may be sent.
	maxTests = 5
)

// Errors the service distinguishes.
var (
	ErrNotConfigured   = errors.New("notify: no Discord webhook is set")
	ErrUnknownCategory = errors.New("notify: there is no such category")
	ErrTooManyTests    = errors.New("notify: at most five test messages a minute")
)

// SettingStore is the slice of the identity store the service uses.
type SettingStore interface {
	Setting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// AuditLog is what the service reads and writes.
type AuditLog interface {
	After(ctx context.Context, id int64, limit int) ([]audit.Record, error)
	LastID(ctx context.Context) (int64, error)
	Write(ctx context.Context, e audit.Event) error
}

// Transport is Discord, or a stand-in for it.
type Transport interface {
	Check(ctx context.Context, w Webhook) (Info, error)
	Send(ctx context.Context, w Webhook, content string) error
}

// Service is the notifier: its configuration, and the task that delivers.
type Service struct {
	settings  SettingStore
	cipher    *secrets.Cipher
	log       AuditLog
	transport Transport
	now       func() time.Time

	mu      sync.Mutex
	notes   []taskNote
	dropped int
	failing map[string]time.Time // task → since when, while it is failing
	state   delivery
	tests   []time.Time
}

// taskNote is a task that started failing, or recovered.
type taskNote struct {
	at     time.Time
	task   string
	failed bool
	err    string
	since  time.Time
}

// delivery is how the last attempts went. In memory: after a restart the
// first run finds out again.
type delivery struct {
	lastSent    time.Time
	lastLines   int
	lastError   string
	lastErrorAt time.Time
	// stopped is set when Discord does not know the webhook: nothing is sent
	// until it is replaced.
	stopped bool
	// notBefore is when Discord said the next message may be sent.
	notBefore time.Time
}

// NewService builds the notifier.
func NewService(settings SettingStore, cipher *secrets.Cipher, log AuditLog, transport Transport,
	now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{settings: settings, cipher: cipher, log: log, transport: transport, now: now,
		failing: map[string]time.Time{}}
}

// ---------------------------------------------------------------------------
// what the screen reads and changes

// CategoryState is a category and whether it is on.
type CategoryState struct {
	CategoryInfo
	On bool
}

// Status is what the Notifications screen shows. It holds nothing of the
// link, by construction.
type Status struct {
	Configured bool
	// Webhook is what Discord said about the webhook when it was stored.
	Webhook    Info
	CheckedAt  time.Time
	Categories []CategoryState
	LastSent   time.Time
	LastLines  int
	LastError  string
	// LastErrorAt is when LastError happened.
	LastErrorAt time.Time
	// Stopped is set when Discord does not know the webhook any more.
	Stopped bool
	// Waiting is when Discord said the next message may go, if later than now.
	Waiting time.Time
}

// Status reports the configuration and how delivery is going.
func (s *Service) Status(ctx context.Context) (Status, error) {
	if err := authz.RequirePermission(ctx, authz.PermSystemSettings); err != nil {
		return Status{}, err
	}
	// A stored link that does not open is still a link that is set: the
	// screen says so, and offers to replace it.
	_, configured, stored := s.webhook(ctx)
	on, err := s.categories(ctx)
	if err != nil {
		return Status{}, err
	}
	out := Status{Configured: configured || stored != nil}
	for _, ci := range Categories {
		out.Categories = append(out.Categories, CategoryState{CategoryInfo: ci, On: on[ci.ID]})
	}
	if configured {
		out.Webhook, out.CheckedAt = s.storedInfo(ctx)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.state
	out.LastSent, out.LastLines = d.lastSent, d.lastLines
	out.LastError, out.LastErrorAt = d.lastError, d.lastErrorAt
	if stored != nil {
		out.LastError = stored.Error()
	}
	out.Stopped = d.stopped
	if d.notBefore.After(s.now()) {
		out.Waiting = d.notBefore
	}
	return out, nil
}

// requireBoth is what changing notifications needs: the settings, and the
// audit log — choosing what leaves the log is reading it.
func requireBoth(ctx context.Context) error {
	if err := authz.RequirePermission(ctx, authz.PermSystemSettings); err != nil {
		return err
	}
	return authz.RequirePermission(ctx, authz.PermViewAuditLog)
}

// SetWebhook stores a webhook link after Discord has confirmed it — a check
// that posts nothing — or, given an empty link, removes the one there is. A
// link Discord does not confirm is not stored. The first webhook starts from
// the newest line of the log: the history is not sent.
func (s *Service) SetWebhook(ctx context.Context, raw, sourceIP, userAgent string) (Info, error) {
	if err := requireBoth(ctx); err != nil {
		return Info{}, err
	}
	// A stored value that does not open can still be replaced or removed.
	old, opened, stored := s.webhook(ctx)
	had := opened || stored != nil

	if strings.TrimSpace(raw) == "" {
		if !had {
			return Info{}, nil
		}
		if opened {
			s.farewell(ctx, old, "🔕 **Notifications were turned off**", "")
		}
		for _, k := range []string{keyWebhook, keyWebhookInfo, keyCursor} {
			if err := s.settings.SetSetting(ctx, k, ""); err != nil {
				return Info{}, fmt.Errorf("notify: removing the webhook: %w", err)
			}
		}
		s.resetDelivery()
		s.audit(ctx, SettingWebhook, "the Discord webhook was removed: nothing is sent", sourceIP, userAgent, nil, nil)
		return Info{}, nil
	}

	w, err := ParseWebhook(raw)
	if err != nil {
		return Info{}, err
	}
	info, err := s.transport.Check(ctx, w)
	if err != nil {
		return Info{}, err
	}

	sealed, err := s.cipher.EncryptString(w.url(), sealContext)
	if err != nil {
		return Info{}, fmt.Errorf("notify: sealing the webhook: %w", err)
	}
	// The cursor first: a webhook stored with no cursor would send from the
	// first line of the log.
	if _, set, err := s.cursor(ctx); err != nil {
		return Info{}, err
	} else if !set {
		last, err := s.log.LastID(ctx)
		if err != nil {
			return Info{}, err
		}
		if err := s.settings.SetSetting(ctx, keyCursor, strconv.FormatInt(last, 10)); err != nil {
			return Info{}, fmt.Errorf("notify: storing where to start: %w", err)
		}
	}
	if err := s.settings.SetSetting(ctx, keyWebhook, base64.StdEncoding.EncodeToString(sealed)); err != nil {
		return Info{}, fmt.Errorf("notify: storing the webhook: %w", err)
	}
	infoJSON, _ := json.Marshal(storedInfo{Name: info.Name, ChannelID: info.ChannelID, GuildID: info.GuildID,
		CheckedAt: s.now().UTC()})
	if err := s.settings.SetSetting(ctx, keyWebhookInfo, string(infoJSON)); err != nil {
		return Info{}, fmt.Errorf("notify: storing what Discord said: %w", err)
	}
	s.resetDelivery()
	if opened && old != w {
		s.farewell(ctx, old, "🔕 **Notifications now go to another webhook**", info.Name)
	}

	what := "a Discord webhook was stored"
	if had {
		what = "the Discord webhook was replaced"
	}
	s.audit(ctx, SettingWebhook, fmt.Sprintf("%s, and Discord confirmed it: it posts as %q", what, clean(info.Name, 80)),
		sourceIP, userAgent, nil, nil)
	return info, nil
}

// farewell tells the channel notifications are leaving that they are, and
// who made them: an attacker holding the administrator's session who turns
// notifications off, or points them at a channel of their own, does it in
// front of the operator. Best effort — a channel that cannot be reached cannot
// be told, and the change is made regardless.
func (s *Service) farewell(ctx context.Context, w Webhook, title, newName string) {
	now := s.now()
	s.mu.Lock()
	gone := s.state.stopped
	s.mu.Unlock()
	if gone {
		return
	}
	msg := title
	if p := authz.FromContext(ctx); p != nil {
		msg += " · " + code(p.Username, maxValue)
	}
	if newName != "" {
		msg += ": it posts as " + code(newName, maxValue)
	}
	_ = s.transport.Send(ctx, w, msg+" · "+stamp(now, now))
}

// SetCategories chooses what is sent.
func (s *Service) SetCategories(ctx context.Context, ids []Category, sourceIP, userAgent string) error {
	if err := requireBoth(ctx); err != nil {
		return err
	}
	want := map[Category]bool{}
	for _, id := range ids {
		if _, ok := categoryInfo(id); !ok {
			return fmt.Errorf("%w: %q", ErrUnknownCategory, clean(string(id), 40))
		}
		want[id] = true
	}
	before, err := s.categories(ctx)
	if err != nil {
		return err
	}
	list := func(set map[Category]bool) []string {
		out := []string{}
		for _, ci := range Categories {
			if set[ci.ID] {
				out = append(out, string(ci.ID))
			}
		}
		return out
	}
	after := list(want)
	b, _ := json.Marshal(after)
	if err := s.settings.SetSetting(ctx, keyCategories, string(b)); err != nil {
		return fmt.Errorf("notify: storing the categories: %w", err)
	}
	detail := "notifications send nothing"
	if len(after) > 0 {
		detail = "notifications send: " + strings.Join(after, ", ")
	}
	s.audit(ctx, SettingCategories, detail, sourceIP, userAgent, list(before), after)
	return nil
}

// Test sends a test message, so the operator can see where messages land.
func (s *Service) Test(ctx context.Context) error {
	if err := authz.RequirePermission(ctx, authz.PermSystemSettings); err != nil {
		return err
	}
	w, ok, err := s.webhook(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotConfigured
	}

	// Every test that reaches Discord counts, whatever it answers.
	now := s.now()
	s.mu.Lock()
	recent := s.tests[:0]
	for _, t := range s.tests {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	s.tests = recent
	if len(s.tests) >= maxTests {
		s.mu.Unlock()
		return ErrTooManyTests
	}
	s.tests = append(s.tests, now)
	s.mu.Unlock()

	on, err := s.categories(ctx)
	if err != nil {
		return err
	}
	var names []string
	for _, ci := range Categories {
		if on[ci.ID] {
			names = append(names, ci.Label)
		}
	}
	sends := "nothing — every category is off"
	if len(names) > 0 {
		sends = strings.Join(names, ", ")
	}
	msg := fmt.Sprintf("🔔 **A test message from CMediaStack** · this channel is sent: %s · %s", sends, stamp(now, now))
	if err := s.transport.Send(ctx, w, msg); err != nil {
		s.failed(err, now)
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// the task

// TaskFinished is told the outcome of every scheduled task. It notes a task
// that starts failing and one that recovers — never each repeat — for the
// next run to send. It must not block: the scheduler calls it inline.
func (s *Service) TaskFinished(name string, o tasks.Outcome) {
	if name == TaskName {
		return // its own failures are on its own screen
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	since, wasFailing := s.failing[name]
	switch {
	case !o.Succeeded() && !wasFailing:
		s.failing[name] = o.StartedAt
		s.note(taskNote{at: o.StartedAt, task: name, failed: true, err: o.Err})
	case o.Succeeded() && wasFailing:
		delete(s.failing, name)
		s.note(taskNote{at: o.StartedAt, task: name, since: since})
	}
}

// note keeps a task note for the next run, dropping the oldest past the
// bound. Called with the lock held.
func (s *Service) note(n taskNote) {
	s.notes = append(s.notes, n)
	if over := len(s.notes) - maxNotes; over > 0 {
		s.notes = append([]taskNote(nil), s.notes[over:]...)
		s.dropped += over
	}
}

// Run delivers what happened since the last run. It is the task's body, and
// runs as system:notify.
func (s *Service) Run(ctx context.Context) (string, error) {
	now := s.now()
	w, ok, err := s.webhook(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		// Nothing is configured: what failed meanwhile is not kept for later.
		s.mu.Lock()
		s.notes, s.dropped = nil, 0
		s.mu.Unlock()
		return "", nil
	}

	s.mu.Lock()
	d := s.state
	s.mu.Unlock()
	if d.stopped {
		return "", fmt.Errorf("not sending: %s. Replace the webhook on the Notifications screen", d.lastError)
	}
	if now.Before(d.notBefore) {
		return fmt.Sprintf("Discord asked to wait until %s", d.notBefore.UTC().Format("15:04:05 UTC")), nil
	}

	cursor, set, err := s.cursor(ctx)
	if err != nil {
		return "", err
	}
	if !set {
		// Configured with no cursor — it was lost. Start from now rather
		// than send the whole log.
		last, err := s.log.LastID(ctx)
		if err != nil {
			return "", err
		}
		return "", s.settings.SetSetting(ctx, keyCursor, strconv.FormatInt(last, 10))
	}
	on, err := s.categories(ctx)
	if err != nil {
		return "", err
	}
	records, err := s.log.After(ctx, cursor, readLimit)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	notes, dropped := s.notes, s.dropped
	s.notes, s.dropped = nil, 0
	s.mu.Unlock()

	b := batch{now: now}
	for _, r := range records {
		if cat, title, ok := classify(r); ok && on[cat] {
			b.addRecord(cat, title, r)
		}
	}
	if on[Operations] {
		for _, n := range notes {
			b.addNote(n)
		}
		if dropped > 0 {
			b.addLine(Operations, fmt.Sprintf("**%d more task failures and recoveries** were not kept", dropped), now)
		}
	}
	next := cursor
	if len(records) > 0 {
		next = records[len(records)-1].ID
	}

	lines := b.render()
	msgs, left := pack(lines, maxMessages)
	for _, m := range msgs {
		if err := s.transport.Send(ctx, w, m); err != nil {
			s.failed(err, now)
			if errors.Is(err, ErrRefused) {
				// Discord refuses what was written as malformed. Retrying
				// the same lines would refuse them for ever and hold up
				// everything after them, so they are passed over.
				_ = s.settings.SetSetting(ctx, keyCursor, strconv.FormatInt(next, 10))
				return "", fmt.Errorf("%w; its lines were passed over", err)
			}
			if on[Operations] {
				s.requeue(notes)
			}
			return "", err
		}
	}
	if next != cursor {
		if err := s.settings.SetSetting(ctx, keyCursor, strconv.FormatInt(next, 10)); err != nil {
			return "", fmt.Errorf("notify: recording where it stopped (what was sent may be sent again): %w", err)
		}
	}

	s.mu.Lock()
	if len(msgs) > 0 {
		s.state.lastSent, s.state.lastLines = now, len(lines)-left
	}
	s.state.lastError, s.state.lastErrorAt = "", time.Time{}
	s.mu.Unlock()

	switch {
	case len(msgs) == 0:
		return "", nil
	case left > 0:
		return fmt.Sprintf("sent %d line(s) in %d message(s); %d more were counted, not listed",
			len(lines)-left, len(msgs), left), nil
	}
	return fmt.Sprintf("sent %d line(s) in %d message(s)", len(lines), len(msgs)), nil
}

// requeue puts task notes back for the next run, ahead of newer ones.
func (s *Service) requeue(notes []taskNote) {
	if len(notes) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	newer := s.notes
	s.notes = nil
	for _, n := range append(append([]taskNote(nil), notes...), newer...) {
		s.note(n)
	}
}

// failed records how a send failed, and what that means for the next.
func (s *Service) failed(err error, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.lastError, s.state.lastErrorAt = err.Error(), now
	var rl *RateLimited
	switch {
	case errors.As(err, &rl):
		s.state.notBefore = now.Add(rl.RetryAfter)
	case errors.Is(err, ErrWebhookGone):
		s.state.stopped = true
	}
}

func (s *Service) resetDelivery() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = delivery{}
}

// ---------------------------------------------------------------------------
// what a run sends

// batch is one run's lines, identical ones counted together.
type batch struct {
	now   time.Time
	lines []*line
	index map[string]*line
	stale int
}

type line struct {
	icon  string
	text  string
	at    time.Time
	count int
}

func (b *batch) addLine(cat Category, text string, at time.Time) {
	if b.now.Sub(at) > staleAfter {
		b.stale++
		return
	}
	if b.index == nil {
		b.index = map[string]*line{}
	}
	key := string(cat) + "\x00" + text
	if l, ok := b.index[key]; ok {
		l.count++
		return
	}
	ci, _ := categoryInfo(cat)
	l := &line{icon: ci.Icon, text: text, at: at, count: 1}
	b.index[key] = l
	b.lines = append(b.lines, l)
}

// addRecord writes an audit line: the title in this program's words, and
// every value the log holds in a code span — who, what it was done to, and
// what happened. A person's address and user agent are never sent.
func (b *batch) addRecord(cat Category, title string, r audit.Record) {
	var sb strings.Builder
	sb.WriteString("**" + title + "**")
	if r.ActorLabel != "" {
		sb.WriteString(" · " + code(r.ActorLabel, maxValue))
	}
	if r.TargetID != "" {
		target := r.TargetID
		if r.TargetKind != "" {
			target = r.TargetKind + " " + r.TargetID
		}
		sb.WriteString(" → " + code(target, maxValue))
	}
	body := r.Detail
	if body == "" {
		body = compact(r.After)
	}
	if body != "" {
		sb.WriteString(": " + code(body, maxBody))
	}
	b.addLine(cat, sb.String(), r.OccurredAt)
}

// addNote writes a task that started failing, or recovered.
func (b *batch) addNote(n taskNote) {
	titles, special := taskTitles[n.task]
	var text string
	switch {
	case n.failed && special:
		text = "**" + titles[0] + "** · " + code(n.task, maxValue)
	case n.failed:
		text = "**A scheduled task is failing** · " + code(n.task, maxValue)
	case special:
		text = "**" + titles[1] + "** · " + code(n.task, maxValue)
	default:
		text = "**A scheduled task is working again** · " + code(n.task, maxValue)
	}
	if n.failed && n.err != "" {
		text += ": " + code(n.err, maxBody)
	}
	if !n.failed && !n.since.IsZero() {
		text += " · failing since " + stamp(n.since, b.now)
	}
	b.addLine(Operations, text, n.at)
}

// render is the run's lines, oldest first, and a count of any too old to
// list.
func (b *batch) render() []string {
	sort.SliceStable(b.lines, func(i, j int) bool { return b.lines[i].at.Before(b.lines[j].at) })
	out := make([]string, 0, len(b.lines)+1)
	if b.stale > 0 {
		out = append(out, fmt.Sprintf("⏳ **%d older line(s) were not sent one by one** — they are more than a day old; "+
			"the Audit log has them.", b.stale))
	}
	for _, l := range b.lines {
		s := l.icon + " " + l.text
		if l.count > 1 {
			s += fmt.Sprintf(" ×%d", l.count)
		}
		out = append(out, s+" · "+stamp(l.at, b.now))
	}
	return out
}

// ---------------------------------------------------------------------------
// the stored configuration

// webhook reads the stored link. Not configured is not an error; a stored
// value that does not open is.
func (s *Service) webhook(ctx context.Context) (Webhook, bool, error) {
	v, err := s.settings.Setting(ctx, keyWebhook)
	if errors.Is(err, identity.ErrNotFound) || (err == nil && v == "") {
		return Webhook{}, false, nil
	}
	if err != nil {
		return Webhook{}, false, fmt.Errorf("notify: reading the webhook: %w", err)
	}
	sealed, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return Webhook{}, false, errors.New("notify: the stored webhook is damaged; set it again")
	}
	raw, err := s.cipher.DecryptString(sealed, sealContext)
	if err != nil {
		return Webhook{}, false, errors.New("notify: the stored webhook does not open with this master key; set it again")
	}
	w, err := ParseWebhook(raw)
	if err != nil {
		return Webhook{}, false, errors.New("notify: the stored webhook is not a webhook link; set it again")
	}
	return w, true, nil
}

// categories is what is on: the stored choice, or the defaults.
func (s *Service) categories(ctx context.Context) (map[Category]bool, error) {
	on := map[Category]bool{}
	v, err := s.settings.Setting(ctx, keyCategories)
	switch {
	case errors.Is(err, identity.ErrNotFound) || (err == nil && v == ""):
		for _, ci := range Categories {
			on[ci.ID] = ci.Default
		}
		return on, nil
	case err != nil:
		return nil, fmt.Errorf("notify: reading the categories: %w", err)
	}
	var ids []Category
	if err := json.Unmarshal([]byte(v), &ids); err != nil {
		return nil, errors.New("notify: the stored categories are damaged; choose them again")
	}
	for _, id := range ids {
		if _, known := categoryInfo(id); known {
			on[id] = true
		}
	}
	return on, nil
}

// cursor is the id of the last audit line considered.
func (s *Service) cursor(ctx context.Context) (int64, bool, error) {
	v, err := s.settings.Setting(ctx, keyCursor)
	if errors.Is(err, identity.ErrNotFound) || (err == nil && v == "") {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("notify: reading where it stopped: %w", err)
	}
	// A cursor that is not a number is treated as none: the next run starts
	// from the newest line, rather than failing for ever or sending the log.
	n, ok := parseCursor(v)
	return n, ok, nil
}

func parseCursor(v string) (int64, bool) {
	n, err := strconv.ParseInt(v, 10, 64)
	return n, err == nil && n >= 0
}

type storedInfo struct {
	Name      string    `json:"name"`
	ChannelID string    `json:"channel_id"`
	GuildID   string    `json:"guild_id"`
	CheckedAt time.Time `json:"checked_at"`
}

func (s *Service) storedInfo(ctx context.Context) (Info, time.Time) {
	v, err := s.settings.Setting(ctx, keyWebhookInfo)
	if err != nil || v == "" {
		return Info{}, time.Time{}
	}
	var si storedInfo
	if json.Unmarshal([]byte(v), &si) != nil {
		return Info{}, time.Time{}
	}
	return Info{Name: si.Name, ChannelID: si.ChannelID, GuildID: si.GuildID}, si.CheckedAt
}

// audit writes a configuration change. Never the link.
func (s *Service) audit(ctx context.Context, setting, detail, sourceIP, userAgent string, before, after any) {
	p := authz.FromContext(ctx)
	e := audit.Event{
		Action: audit.ActionSystemSettingChanged, TargetKind: "setting", TargetID: setting,
		SourceIP: sourceIP, UserAgent: userAgent, Detail: detail,
	}
	if before != nil || after != nil {
		e.Before, e.After = before, after
	}
	if p != nil {
		id := p.UserID
		e.ActorUserID, e.ActorLabel = &id, p.Username
	}
	_ = s.log.Write(ctx, e)
}

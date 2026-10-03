// Package acquire fetches what the library wants without a person (ADR-0030):
// it watches the indexers' recent releases, searches for wanted items within a
// budget, and grabs what matches.
//
// # Why it may do so little
//
// A machine that fetches on its own can be wrong with nobody looking, can be
// relentless, and acts in the operator's name. So:
//
//   - It acts on the Wanted list and nothing else — monitored, aired, not on
//     disk. It never adds a title, and replaces a file only with upgrades on
//     (ADR-0036): one below its title's cutoff, by a better release.
//   - Every candidate is matched by search.MatchEpisode or search.MatchFilm,
//     the code a person's targeted search runs, and must be accepted by the
//     instance's default quality profile. With no default profile it does
//     nothing, and says so.
//   - It never grabs a release already in the queue (the queue is the
//     blocklist), a release with no seeders, anything for an item a download
//     is already under way for, or a release whose name also fits another
//     title in the library.
//   - It searches a few items a pass and grabs a few a pass.
//   - It runs as system:acquire — browse, search, queue, nothing else — and
//     every grab is audited.
package acquire

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/release"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// What one search of one item came to, as acquire_state spells it.
const (
	OutcomeGrabbed = "grabbed"
	OutcomeNothing = "nothing"
	OutcomeFailed  = "failed"
)

// Label is who the queue and the audit log say added an automatic download.
const Label = string(authz.TaskAcquire)

// Automatic reports whether a queued download is automatic acquisition's: no
// person, and its label. A person who happens to be called that is a person.
// What the queue marks as automatic, and what a stall gives up on (ADR-0034).
func Automatic(rec download.Record) bool {
	return rec.AddedBy == nil && rec.AddedLabel == Label
}

// Limits (ADR-0030, decisions 4 and 5).
const (
	// RecentLimit is how many recent releases each indexer is asked for.
	RecentLimit = 100
	// FirstBackoff is how long an item whose search found nothing grabbable
	// waits before it is searched again. Each fruitless search doubles it, up
	// to MaxBackoff; the recent-release pass keeps watching in between.
	FirstBackoff = 6 * time.Hour
	MaxBackoff   = 7 * 24 * time.Hour
	// UpgradeInterval is how often a file below its cutoff is searched for
	// an upgrade: counted from its last search, which for a file that has just
	// arrived is the one that fetched it (ADR-0036).
	UpgradeInterval = 7 * 24 * time.Hour
	// FailedRetry is how long an item waits after a search that FAILED —
	// every indexer down. It does not lengthen the back-off: the item was not
	// looked for.
	FailedRetry = time.Hour

	// passDeadline bounds one pass, whatever the indexers do.
	passDeadline = 10 * time.Minute
	// maxAttempts is how many candidates one item's grab tries when fetching
	// fails, before the item is left for the next pass.
	maxAttempts = 3
	// grabFailureTTL is how long a release whose fetch failed is left alone.
	// The recent-release pass sees the same release every fifteen minutes
	// until it scrolls away, and asking for it every time is how an indexer
	// account is lost.
	grabFailureTTL = 6 * time.Hour

	// Alternative titles are cached for a day, a failure to read them for an
	// hour, and a pass reads at most maxTitleLookups of them: a library that
	// wants two hundred series must not ask the provider two hundred questions
	// in one go. Titles not read yet are matched by their own name meanwhile.
	titleTTL        = 24 * time.Hour
	titleFailureTTL = time.Hour
	maxTitleLookups = 100
	maxCachedTitles = 10_000
)

// ErrNoDefaultProfile means the instance has no default quality profile, so a
// pass cannot know what to accept and does nothing (ADR-0030, decision 3).
var ErrNoDefaultProfile = errors.New("acquire: there is no default quality profile, so " +
	"nothing can be judged and nothing was fetched; choose one under the Search screen's form " +
	"(Make this the default)")

// ErrNoIndexers means nothing could be asked.
var ErrNoIndexers = errors.New("acquire: no indexers are enabled, so nothing can be found")

// ---------------------------------------------------------------------------
// What is wanted
// ---------------------------------------------------------------------------

// Want is one item on the Wanted list: one episode of a series, or a film. A
// want with Pack set is a whole season, made from its episodes when every one
// of them is wanted (ADR-0033); it is never on the list itself.
type Want struct {
	// Film marks a film; otherwise this is one episode, or with Pack a season.
	Film bool
	Pack bool
	// ItemID is the film, or the series the episode belongs to.
	ItemID int64
	// EpisodeID, Season and Episode name the episode. Zero for a film.
	EpisodeID int64
	Season    int
	Episode   int
	// Title, Year and TMDBID are the film's or the series'.
	Title  string
	Year   int
	TMDBID int64
	// When is when it became wanted: an episode's air date, a film's addition.
	When time.Time
	// ProfileID is the title's own quality profile; zero is the default
	// (ADR-0035).
	ProfileID int64
	// Upgrade marks an item that has a file below its cutoff, and Have is
	// that file as a release (ADR-0036).
	Upgrade bool
	Have    release.Parsed
	// HaveAudio is, for an album's upgrade, a lossy format it holds
	// (ADR-0062).
	HaveAudio string
	// Album is one album of the artist that is the item (ADR-0047): Title
	// is then the album's, Artist the artist's, and Namesake says the artist
	// has another album of the same folded title.
	Album    int64
	Artist   string
	Namesake bool
	// Book is a book: the item, its Title and its Author (ADR-0050).
	Book   bool
	Author string
	// Daily says the episode's series is searched by air date (ADR-0064).
	Daily bool
}

// airDate is an episode's air date as a release names it, empty for anything
// else or an episode not yet dated (ADR-0064).
func (w Want) airDate() string {
	if w.Film || w.Album > 0 || w.Book || w.When.IsZero() {
		return ""
	}
	return w.When.UTC().Format("2006-01-02")
}

// Key is what a download is for: the unit "one download per wanted item" is
// counted in.
type Key struct {
	Film    bool
	ItemID  int64
	Season  int
	Episode int
	// Album is an album's id; nothing else is set (ADR-0047).
	Album int64
	// Book marks a book: the item and nothing else (ADR-0050).
	Book bool
}

// Key returns the want's key.
func (w Want) Key() Key {
	if w.Album > 0 {
		return Key{Album: w.Album}
	}
	if w.Book {
		return Key{Book: true, ItemID: w.ItemID}
	}
	if w.Film {
		return Key{Film: true, ItemID: w.ItemID}
	}
	return Key{ItemID: w.ItemID, Season: w.Season, Episode: w.Episode}
}

// seasonKey names one season of one series.
type seasonKey struct {
	ItemID int64
	Season int
}

// seasonOf is the season an episode want belongs to.
func (w Want) seasonOf() seasonKey { return seasonKey{ItemID: w.ItemID, Season: w.Season} }

// packOf is the whole-season want an episode want's season makes.
func (w Want) packOf() Want {
	return Want{Pack: true, ItemID: w.ItemID, Season: w.Season,
		Title: w.Title, Year: w.Year, TMDBID: w.TMDBID, When: w.When, ProfileID: w.ProfileID}
}

// StateKey is what a search's outcome is kept against: an episode by its own
// id, a film by its item's.
type StateKey struct {
	Film bool
	// Album marks an album's id (ADR-0047); Book a book's item id (ADR-0050).
	Album bool
	Book  bool
	ID    int64
}

// StateKey returns the want's state key.
func (w Want) StateKey() StateKey {
	if w.Album > 0 {
		return StateKey{Album: true, ID: w.Album}
	}
	if w.Book {
		return StateKey{Book: true, ID: w.ItemID}
	}
	if w.Film {
		return StateKey{Film: true, ID: w.ItemID}
	}
	return StateKey{ID: w.EpisodeID}
}

// Name is how the operator would say it: "Severance S02E03", "Dune (2021)".
func (w Want) Name() string {
	if w.Upgrade {
		base := w
		base.Upgrade = false
		from := release.QualityOf(w.Have).Name
		if w.Album > 0 {
			from = w.HaveAudio
		}
		return base.Name() + " (an upgrade from " + from + ")"
	}
	if w.Album > 0 {
		name := w.Artist + " — " + w.Title
		if w.Year > 0 {
			name += fmt.Sprintf(" (%d)", w.Year)
		}
		return name
	}
	if w.Book {
		name := w.Title
		if w.Year > 0 {
			name += fmt.Sprintf(" (%d)", w.Year)
		}
		return name + " by " + w.Author
	}
	if w.Pack {
		return fmt.Sprintf("%s S%02d (the whole season)", w.Title, w.Season)
	}
	if w.Film {
		if w.Year > 0 {
			return fmt.Sprintf("%s (%d)", w.Title, w.Year)
		}
		return w.Title
	}
	return fmt.Sprintf("%s S%02dE%02d", w.Title, w.Season, w.Episode)
}

// target is what a grab for this want is sealed to, and the import files.
func (w Want) target() search.Target {
	if w.Album > 0 {
		return search.Target{ItemID: w.ItemID, Album: w.Album}
	}
	if w.Book {
		return search.Target{ItemID: w.ItemID, Book: true}
	}
	if w.Film {
		return search.Target{ItemID: w.ItemID, Film: true}
	}
	if w.Pack {
		return search.Target{ItemID: w.ItemID, Season: w.Season, Pack: true}
	}
	return search.Target{ItemID: w.ItemID, Season: w.Season, Episode: w.Episode}
}

// State is what automatic acquisition last did about one wanted item.
type State struct {
	// SearchedAt is when it last acted on the item; NextAt is when a search
	// may next look for it.
	SearchedAt time.Time
	NextAt     time.Time
	// Fruitless counts consecutive searches that found nothing grabbable.
	Fruitless int
	// Outcome is grabbed, nothing or failed; Detail says what, or why not.
	Outcome string
	Detail  string
}

// ---------------------------------------------------------------------------
// What it is built from
// ---------------------------------------------------------------------------

// Finder searches and fetches. *search.Service is one.
type Finder interface {
	Search(ctx context.Context, req search.Request) (search.Response, error)
	SearchEpisode(ctx context.Context, es search.EpisodeSearch) (search.Response, error)
	SearchFilm(ctx context.Context, fs search.FilmSearch) (search.Response, error)
	SearchSeason(ctx context.Context, ss search.SeasonSearch) (search.Response, error)
	SearchAlbum(ctx context.Context, as search.AlbumSearch) (search.Response, error)
	SearchBook(ctx context.Context, bs search.BookSearch) (search.Response, error)
	Grab(ctx context.Context, tk search.Ticket) (search.Grabbed, error)
}

// Queue starts downloads. *download.Manager is one.
type Queue interface {
	AddMagnet(ctx context.Context, magnet string, meta download.Meta) (download.Transfer, error)
	AddTorrent(data []byte, meta download.Meta) (download.Transfer, error)
	Start(hash string) error
}

// Titles names what else a series or a film is called. *metadata.Service is
// one.
type Titles interface {
	AlternativeTitles(ctx context.Context, seriesID int64) ([]string, error)
	FilmTitles(ctx context.Context, filmID int64) ([]string, error)
}

// Profiles supplies the default quality profile, and a title's own.
// *release.ProfileStore is one.
type Profiles interface {
	Default(ctx context.Context) (release.StoredProfile, bool, error)
	Get(ctx context.Context, id int64) (release.StoredProfile, error)
}

// Auditor writes the audit log. *audit.Logger is one.
type Auditor interface {
	Write(ctx context.Context, e audit.Event) error
}

// Gate says whether indexers may be asked now, and why not (ADR-0030,
// decision 6).
type Gate func() (open bool, why string)

// Config is the budget (ADR-0030, decisions 4 and 5).
type Config struct {
	// The two intervals are the scheduler's; they are here to be reported.
	RecentInterval time.Duration
	SearchInterval time.Duration
	// SearchesPerRun is how many items one search pass looks for.
	SearchesPerRun int
	// MaxGrabsPerRun is how many downloads one pass may start.
	MaxGrabsPerRun int
	// Upgrades lets a pass replace a file below its title's cutoff
	// (ADR-0036).
	Upgrades bool
}

// PackCheck says whether a .torrent holds every wanted episode of a season, and
// what it holds when it does not (importer.CheckPack over the file list).
type PackCheck func(torrent []byte, season int, want map[int]bool) (ok bool, why string)

// Deps is what New needs. Titles, Gate, Audit, Log and PackCheck may be nil;
// with no PackCheck, no season pack is ever grabbed (ADR-0033, decision 5).
type Deps struct {
	Store    *Store
	Finder   Finder
	Queue    Queue
	Titles   Titles
	Profiles Profiles
	Gate     Gate
	Audit    Auditor
	Log      *slog.Logger
	Now      func() time.Time
	// PackCheck reads a pack's file list before it is queued.
	PackCheck PackCheck
}

// Service runs the two passes.
type Service struct {
	store    *Store
	finder   Finder
	queue    Queue
	titles   Titles
	profiles Profiles
	gate     Gate
	audit    Auditor
	log      *slog.Logger
	now      func() time.Time
	cfg      Config
	packs    PackCheck

	// running holds one pass at a time. The two passes are separate
	// scheduled tasks and can start together; run side by side, both could
	// find the same episode and grab two releases of it.
	running chan struct{}

	// lookupBudget is maxTitleLookups; a field so a test can make it small.
	lookupBudget int

	mu       sync.Mutex
	titleFor map[titleKey]titleEntry
	failed   map[string]time.Time // releases whose fetch failed, and when
}

// New builds the service.
func New(d Deps, cfg Config) (*Service, error) {
	switch {
	case d.Store == nil || d.Finder == nil || d.Queue == nil || d.Profiles == nil:
		return nil, errors.New("acquire: a store, a finder, a queue and a profile source are required")
	case cfg.SearchesPerRun < 1 || cfg.MaxGrabsPerRun < 1:
		return nil, fmt.Errorf("acquire: %d searches and %d grabs a pass is not a budget",
			cfg.SearchesPerRun, cfg.MaxGrabsPerRun)
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{
		store: d.Store, finder: d.Finder, queue: d.Queue, titles: d.Titles,
		profiles: d.Profiles, gate: d.Gate, audit: d.Audit, log: d.Log, now: d.Now,
		cfg:          cfg,
		packs:        d.PackCheck,
		running:      make(chan struct{}, 1),
		lookupBudget: maxTitleLookups,
		titleFor:     map[titleKey]titleEntry{},
		failed:       map[string]time.Time{},
	}, nil
}

// Settings is the budget, for the Wanted screen.
func (s *Service) Settings() Config { return s.cfg }

// Report is what the Wanted screen shows beside each item.
type Report struct {
	States   map[StateKey]State
	InFlight map[Key]bool
	// AlbumsImported are the albums a download was imported for, which are
	// not grabbed again automatically (ADR-0047, decision 3).
	AlbumsImported map[int64]bool
}

// Report reads it.
func (s *Service) Report(ctx context.Context) (Report, error) {
	states, err := s.store.States(ctx)
	if err != nil {
		return Report{}, err
	}
	inFlight, err := s.store.InFlight(ctx)
	if err != nil {
		return Report{}, err
	}
	imported, err := s.store.AlbumsImported(ctx)
	if err != nil {
		return Report{}, err
	}
	return Report{States: states, InFlight: inFlight, AlbumsImported: imported}, nil
}

// ---------------------------------------------------------------------------
// A pass
// ---------------------------------------------------------------------------

// How a release was found, as the audit line and the queue's history say it.
const (
	howRecent = "found in the indexers' recent releases"
	howSearch = "found by searching for it"
)

// pass is one run's working state.
type pass struct {
	how       string
	profile   release.StoredProfile
	wanted    []Want
	wantedSet map[Key]bool
	inFlight  map[Key]bool
	// taken is what this pass has grabbed for, including every episode a
	// double episode covers.
	taken     map[Key]bool
	namesakes map[string][]Namesake
	// settled is every settled season's episode numbers; packable the ones a
	// pack may be grabbed for — every episode wanted and none in flight — and
	// seasonSearched the ones this pass has already searched as a season.
	settled        map[seasonKey][]int
	packable       map[seasonKey]bool
	seasonSearched map[seasonKey]bool
	// titleProfiles are the titles' own profiles read this pass (ADR-0035).
	titleProfiles map[int64]*release.StoredProfile
	// upgrades are the files below their cutoff, with upgrades on (ADR-0036).
	upgrades []Want
	grabs    int
	lookups  int
	ownOnly  map[titleKey]bool
}

// begin checks what a pass may do and reads what it needs. A non-empty summary
// means there is nothing to do and says why; done must be called otherwise.
// enter checks the caller's authority, takes the one-pass-at-a-time lock and
// asks the gate. A non-empty closed says why nothing may be asked, and the lock
// is not held; otherwise unlock must be called.
func (s *Service) enter(ctx context.Context) (unlock func(), closed string, err error) {
	// The task grants these (authz.TaskAcquire). Checked here as well, so a
	// caller that is not the task — a test, a future handler — cannot drive a
	// pass on authority it does not hold.
	for _, perm := range []authz.Permission{authz.PermBrowse, authz.PermInteractiveSearch,
		authz.PermManageQueue} {
		if err := authz.RequirePermission(ctx, perm); err != nil {
			return nil, "", err
		}
	}

	select {
	case s.running <- struct{}{}:
	case <-ctx.Done():
		return nil, "", fmt.Errorf("acquire: waiting for the other pass to finish: %w", ctx.Err())
	}
	unlock = func() { <-s.running }

	if s.gate != nil {
		if open, why := s.gate(); !open {
			unlock()
			return nil, "stopped before asking any indexer: " + why, nil
		}
	}
	return unlock, "", nil
}

func (s *Service) begin(ctx context.Context, how string) (p *pass, summary string, done func(), err error) {
	unlock, closed, err := s.enter(ctx)
	if err != nil || closed != "" {
		return nil, closed, nil, err
	}

	sp, found, err := s.profiles.Default(ctx)
	switch {
	case err != nil:
		unlock()
		return nil, "", nil, fmt.Errorf("acquire: reading the default quality profile: %w", err)
	case !found:
		unlock()
		return nil, "", nil, ErrNoDefaultProfile
	}

	wanted, err := s.store.Wanted(ctx)
	if err != nil {
		unlock()
		return nil, "", nil, err
	}
	var held []Want
	if s.cfg.Upgrades {
		if held, err = s.store.Upgradable(ctx); err != nil {
			unlock()
			return nil, "", nil, err
		}
	}
	if len(wanted) == 0 && len(held) == 0 {
		unlock()
		return nil, "nothing is wanted; no indexer was asked", nil, nil
	}
	inFlight, err := s.store.InFlight(ctx)
	if err != nil {
		unlock()
		return nil, "", nil, err
	}
	namesakes, err := s.store.Namesakes(ctx)
	if err != nil {
		unlock()
		return nil, "", nil, err
	}
	settled, err := s.store.SettledSeasons(ctx)
	if err != nil {
		unlock()
		return nil, "", nil, err
	}

	p = &pass{
		how: how, profile: sp, wanted: wanted,
		wantedSet: make(map[Key]bool, len(wanted)),
		inFlight:  inFlight,
		taken:     map[Key]bool{},
		namesakes: namesakes,
		ownOnly:   map[titleKey]bool{},

		settled:        settled,
		packable:       map[seasonKey]bool{},
		seasonSearched: map[seasonKey]bool{},
		titleProfiles:  map[int64]*release.StoredProfile{},
	}
	for _, w := range wanted {
		p.wantedSet[w.Key()] = true
	}
	if s.packs != nil {
		for sk := range settled {
			p.packable[sk] = p.wholeSeasonOpen(sk)
		}
	}
	// A file at its title's cutoff is enough (ADR-0036, decision 2).
	for _, w := range held {
		if !s.profileFor(ctx, p, w).Profile.MeetsCutoff(w.Have) {
			p.upgrades = append(p.upgrades, w)
		}
	}
	if len(wanted) == 0 && len(p.upgrades) == 0 {
		unlock()
		return nil, "nothing is wanted, and nothing is below its cutoff; no indexer was asked", nil, nil
	}
	return p, "", unlock, nil
}

// closed says what keeps the wanted items from being looked for: a download
// under way, a film with no year, a back-off still running (when states are
// given).
func (p *pass) closed(states map[StateKey]State, now time.Time) string {
	var flying, noYear, waiting int
	for _, w := range p.wanted {
		switch {
		case p.inFlight[w.Key()] || p.taken[w.Key()]:
			flying++
		case w.Film && w.Year <= 0:
			noYear++
		default:
			if st, ok := states[w.StateKey()]; ok && st.NextAt.After(now) {
				waiting++
			}
		}
	}
	parts := []string{fmt.Sprintf("%d wanted", len(p.wanted))}
	if flying > 0 {
		parts = append(parts, fmt.Sprintf("%d with a download under way", flying))
	}
	if noYear > 0 {
		parts = append(parts, fmt.Sprintf("%d film(s) with no year to search by", noYear))
	}
	if waiting > 0 {
		parts = append(parts, fmt.Sprintf("%d waiting out a back-off", waiting))
	}
	return strings.Join(parts, ", ")
}

// open reports whether a want may be looked for at all this pass.
func (p *pass) open(w Want) bool {
	if p.inFlight[w.Key()] || p.taken[w.Key()] {
		return false
	}
	// A film with no year cannot be told from its namesakes, so no release
	// could match it (search.MatchFilm). Not looked for, and not recorded as
	// searched: the Wanted screen says why instead.
	return !w.Film || w.Year > 0
}

// profileFor is the profile a want is judged by: its title's own, or the
// default (ADR-0035). A title's profile that cannot be read is the default, as
// deleting it would have made it.
func (s *Service) profileFor(ctx context.Context, p *pass, w Want) *release.StoredProfile {
	if w.ProfileID <= 0 || w.ProfileID == p.profile.ID {
		return &p.profile
	}
	if sp, ok := p.titleProfiles[w.ProfileID]; ok {
		return sp
	}
	sp, err := s.profiles.Get(ctx, w.ProfileID)
	if err != nil {
		s.log.Warn("a title's quality profile could not be read; the default judges it",
			slog.Int64("profile_id", w.ProfileID), slog.String("error", err.Error()))
		p.titleProfiles[w.ProfileID] = &p.profile
		return &p.profile
	}
	p.titleProfiles[w.ProfileID] = &sp
	return &sp
}

// judgedFor is a release from the feed — judged once, by the default — judged
// again by the profile of the title it matched, when that is another.
func (s *Service) judgedFor(ctx context.Context, p *pass, c search.Candidate, w Want) search.Candidate {
	prof := s.profileFor(ctx, p, w)
	if prof == &p.profile {
		return c
	}
	c.Accepted, c.Rejection = prof.Profile.Accepts(c.Parsed)
	return c
}

// wholeSeasonOpen reports whether every episode of a settled season is wanted
// and none is downloading or grabbed this pass: the only season a pack may be
// grabbed for (ADR-0033, decision 5).
func (p *pass) wholeSeasonOpen(sk seasonKey) bool {
	numbers := p.settled[sk]
	if len(numbers) == 0 {
		return false
	}
	for _, n := range numbers {
		k := Key{ItemID: sk.ItemID, Season: sk.Season, Episode: n}
		if !p.wantedSet[k] || p.inFlight[k] || p.taken[k] {
			return false
		}
	}
	return true
}

// cover is every episode a grab for a want would hold: a season's every
// episode for a pack, and for an episode every one its release name spans.
func (p *pass) cover(c search.Candidate, w Want) []Key {
	if w.Album > 0 || w.Book {
		return []Key{w.Key()}
	}
	if !w.Pack {
		return covered(c.Parsed, w.Key())
	}
	numbers := p.settled[w.seasonOf()]
	out := make([]Key, 0, len(numbers))
	for _, n := range numbers {
		out = append(out, Key{ItemID: w.ItemID, Season: w.Season, Episode: n})
	}
	return out
}

// seasonWants is the wanted episodes of one season, in episode order.
func (p *pass) seasonWants(sk seasonKey) []Want {
	var out []Want
	for _, w := range p.wanted {
		if !w.Film && w.seasonOf() == sk {
			out = append(out, w)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Episode < out[j].Episode })
	return out
}

// RunRecent asks every enabled indexer once for what it has most recently, and
// grabs what is wanted (ADR-0030, decision 4).
func (s *Service) RunRecent(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, passDeadline)
	defer cancel()
	p, summary, done, err := s.begin(ctx, howRecent)
	if err != nil || summary != "" {
		return summary, err
	}
	defer done()

	// Every folded name of every item that may be grabbed for, so one feed is
	// matched against the whole list at once.
	index := map[string][]*entry{}
	for _, w := range append(append([]Want(nil), p.wanted...), p.upgrades...) {
		if !p.open(w) {
			continue
		}
		e := &entry{want: w, titles: s.titlesFor(ctx, p, w)}
		seen := map[string]bool{}
		for _, t := range e.titles {
			if n := search.NormalizeTitle(t); n != "" && !seen[n] {
				seen[n] = true
				index[n] = append(index[n], e)
			}
		}
	}
	if len(index) == 0 {
		return "nothing wanted can be looked for (" + p.closed(nil, s.now()) +
			"); no indexer was asked", nil
	}

	resp, err := s.finder.Search(ctx, search.Request{
		Season: -1, Profile: &p.profile.Profile, Limit: RecentLimit,
	})
	switch {
	case errors.Is(err, search.ErrNoIndexers):
		return "", ErrNoIndexers
	case err != nil:
		return "", fmt.Errorf("acquire: asking for recent releases: %w", err)
	case resp.Queried > 0 && resp.Failed == resp.Queried:
		return "", fmt.Errorf("acquire: no indexer answered: %s", failures(resp))
	}

	var grabbed, missed []string
	why := map[Key]string{}
	seenFor := 0
	capped := false
	for _, h := range s.rankedForTitles(ctx, p, resp.Candidates, index) {
		c, w, refusal := h.c, h.w, h.refusal
		seenFor++
		if p.taken[w.Key()] {
			continue
		}
		if refusal == "" {
			var ok bool
			ok, refusal, err = s.eligible(ctx, p, c, *w)
			if err != nil {
				return "", err
			}
			if ok && p.grabs >= s.cfg.MaxGrabsPerRun {
				capped = true
				continue
			}
			if ok {
				detail, gerr := s.grab(ctx, p, c, *w)
				if gerr == nil {
					grabbed = append(grabbed, fmt.Sprintf("%s (%s)", w.Name(), detail))
					st := State{SearchedAt: s.now(), NextAt: s.now(),
						Outcome: OutcomeGrabbed, Detail: "grabbed " + detail + ", " + howRecent}
					if w.Pack {
						st.Detail = "grabbed " + detail + " (the whole season), " + howRecent
						s.recordSeason(ctx, p, w.seasonOf(), st)
					} else {
						s.record(ctx, *w, st)
					}
					delete(why, w.Key())
					continue
				}
				refusal = gerr.Error()
			}
		}
		if _, noted := why[w.Key()]; !noted {
			why[w.Key()] = w.Name() + " — " + refusal
		}
	}
	for _, line := range why {
		missed = append(missed, line)
	}
	sort.Strings(missed)

	var b strings.Builder
	fmt.Fprintf(&b, "asked %d indexer(s) for their recent releases: %d release(s)",
		resp.Queried, len(resp.Candidates))
	if resp.Failed > 0 {
		fmt.Fprintf(&b, " (%d did not answer: %s)", resp.Failed, failures(resp))
	}
	switch {
	case seenFor == 0:
		b.WriteString(", none of them anything wanted")
	case len(grabbed) > 0:
		fmt.Fprintf(&b, ". Grabbed %s", strings.Join(grabbed, "; "))
	}
	if len(missed) > 0 {
		fmt.Fprintf(&b, ". Not grabbed: %s", strings.Join(missed, "; "))
	}
	if capped {
		fmt.Fprintf(&b, ". The limit of %d grab(s) a pass was reached; the rest wait for the next",
			s.cfg.MaxGrabsPerRun)
	}
	s.noteOwnOnly(&b, p)
	return b.String(), nil
}

// feedHit is a release from the feed and the wanted item it is.
type feedHit struct {
	c       search.Candidate
	w       *Want
	refusal string
}

// rankedForTitles matches the feed against the wanted items and puts each
// item's releases in the order its own profile ranks them (ADR-0035). The feed
// comes back ranked by the default, which is the order for most items; a title
// with its own profile would otherwise take the first release the default liked
// — Dune's 1080p ahead of the 2160p it asked for. Items keep the order of their
// first release in the feed.
func (s *Service) rankedForTitles(ctx context.Context, p *pass, cands []search.Candidate,
	index map[string][]*entry) []feedHit {

	var order []Key
	groups := map[Key][]feedHit{}
	for _, c := range cands {
		entries := index[search.NormalizeTitle(c.Parsed.Title)]
		if len(entries) == 0 {
			continue
		}
		w, refusal := p.matchRecent(c, entries)
		if w == nil {
			continue
		}
		k := w.Key()
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], feedHit{c: s.judgedFor(ctx, p, c, *w), w: w, refusal: refusal})
	}
	var out []feedHit
	for _, k := range order {
		g := groups[k]
		if prof := s.profileFor(ctx, p, *g[0].w); prof != &p.profile {
			sort.SliceStable(g, func(i, j int) bool {
				a, b := g[i].c, g[j].c
				if a.Accepted != b.Accepted {
					return a.Accepted
				}
				if ra, rb := prof.Profile.Rank(a.Parsed), prof.Profile.Rank(b.Parsed); ra != rb {
					return ra > rb
				}
				if sa, sb := prof.Profile.Score(a.Parsed), prof.Profile.Score(b.Parsed); sa != sb {
					return sa > sb
				}
				return a.Seeders > b.Seeders
			})
		}
		out = append(out, g...)
	}
	return out
}

// entry is one wanted item in the recent-release index.
type entry struct {
	want   Want
	titles []string
}

// matchRecent finds which wanted item a release from the feed is, if any, and
// refuses one whose name fits more than one.
func (p *pass) matchRecent(c search.Candidate, entries []*entry) (*Want, string) {
	if c.Parsed.Season >= 0 && len(c.Parsed.Episodes) == 0 {
		return p.matchRecentPack(c, entries)
	}
	var hits []*entry
	for _, e := range entries {
		var rej *release.Rejection
		if e.want.Film {
			rej = search.MatchFilm(c.Parsed, search.FilmWant{
				ItemID: e.want.ItemID, Titles: e.titles, Year: e.want.Year})
		} else {
			rej = search.MatchEpisode(c.Parsed, search.EpisodeWant{
				ItemID: e.want.ItemID, Titles: e.titles, Year: e.want.Year,
				Season: e.want.Season, Episode: e.want.Episode, AirDate: e.want.airDate()})
		}
		if rej == nil {
			hits = append(hits, e)
		}
	}
	if len(hits) == 0 {
		return nil, ""
	}
	// A double episode matches both of its episodes; it is grabbed once, for
	// the first.
	best := hits[0]
	for _, h := range hits[1:] {
		if h.want.Film != best.want.Film || h.want.ItemID != best.want.ItemID {
			return &best.want, fmt.Sprintf("its name fits both %s and %s", title(best.want), title(h.want))
		}
		if h.want.Episode < best.want.Episode {
			best = h
		}
	}
	return &best.want, ""
}

// matchRecentPack finds which season a pack in the feed is, among the seasons a
// pack may be grabbed for. A pack of any other season is not looked at: its
// episodes come one at a time.
func (p *pass) matchRecentPack(c search.Candidate, entries []*entry) (*Want, string) {
	var hits []*entry
	seen := map[int64]bool{}
	for _, e := range entries {
		w := e.want
		if w.Film || w.Season != c.Parsed.Season || seen[w.ItemID] || !p.packable[w.seasonOf()] {
			continue
		}
		if search.MatchSeasonPack(c.Parsed, search.SeasonWant{ItemID: w.ItemID, Titles: e.titles,
			Year: w.Year, Season: w.Season}) == nil {
			seen[w.ItemID] = true
			hits = append(hits, e)
		}
	}
	if len(hits) == 0 {
		return nil, ""
	}
	best := hits[0].want.packOf()
	if len(hits) > 1 {
		return &best, fmt.Sprintf("its name fits both %s and %s", title(hits[0].want), title(hits[1].want))
	}
	return &best, ""
}

// title names a want's film or series.
func title(w Want) string {
	if w.Year > 0 {
		return fmt.Sprintf("%s (%d)", w.Title, w.Year)
	}
	return w.Title
}

// eligible says whether a release that IS the wanted item may be grabbed for
// it, and why not (ADR-0030, decision 3).
func (s *Service) eligible(ctx context.Context, p *pass, c search.Candidate, w Want) (bool, string, error) {
	if !c.Accepted {
		prof := s.profileFor(ctx, p, w)
		why := "the default profile (" + prof.Profile.Name + ") refuses it"
		if prof != &p.profile {
			why = "its own profile (" + prof.Profile.Name + ") refuses it"
		}
		if c.Rejection != nil && c.Rejection.Detail != "" {
			why += ": " + c.Rejection.Detail
		}
		return false, why, nil
	}
	if w.Upgrade {
		// A release of two episodes is refused below unless the other one is
		// wanted — missing — in which case it fills that gap as well.
		if u := s.profileFor(ctx, p, w).Profile.ShouldUpgrade(c.Parsed, w.Have, true); !u.Should {
			return false, "not an upgrade: " + u.Reason, nil
		}
	}
	if lang := namedLanguage(c.Parsed); lang != "" {
		return false, "it is named in " + lang + ": a dub, or the original of a title not " +
			"in English, and only a person can tell which is wanted", nil
	}
	if other, ok := p.namesake(c, w); ok {
		return false, fmt.Sprintf("its name also fits %s, which is in the library too, "+
			"so it could be either", other), nil
	}
	if w.Pack && !p.wholeSeasonOpen(w.seasonOf()) {
		return false, "not every episode of the season is wanted and free, so only single " +
			"episodes are grabbed for it", nil
	}
	for _, k := range p.cover(c, w) {
		if k == w.Key() {
			continue
		}
		if !p.wantedSet[k] {
			return false, fmt.Sprintf("it also holds %s, which is not wanted", code(k)), nil
		}
		if p.inFlight[k] || p.taken[k] {
			return false, fmt.Sprintf("it also holds %s, which is already downloading", code(k)), nil
		}
	}
	if c.Seeders < 1 {
		return false, "it has no seeders", nil
	}
	if strings.TrimSpace(c.DownloadURL) == "" {
		return false, "the indexer gave no link to fetch it by", nil
	}
	if s.failedRecently(c) {
		return false, "fetching it failed within the last few hours", nil
	}
	if c.InfoHash != "" {
		queued, err := s.store.Queued(ctx, c.InfoHash)
		if err != nil {
			return false, "", err
		}
		if queued {
			return false, "it was grabbed before (a release already in the queue is never grabbed again)", nil
		}
	}
	return true, "", nil
}

// namedLanguage is the language a release is named in, when it names one a
// machine should not choose (ADR-0030, decision 3). By scene convention a
// release in English names no language; one that does is a dub — or, for a
// title not in English, its original — and the matching cannot tell which: a
// title TMDB also lists in German matches "Die.Simpsons.S30E01.German". MULTi
// and dual-audio releases carry the original too, and are not refused.
func namedLanguage(p release.Parsed) string {
	for _, l := range p.Languages {
		switch l {
		case "MULTi", "Dual Audio":
		case "Dubbed":
			return "a dubbed language"
		case "Subbed":
			return "another language, subtitled"
		default:
			return l
		}
	}
	return ""
}

// namesake names another library item a release's title could be.
func (p *pass) namesake(c search.Candidate, w Want) (string, bool) {
	for _, n := range p.namesakes[search.NormalizeTitle(c.Parsed.Title)] {
		if n.Film != w.Film || n.ItemID == w.ItemID {
			continue
		}
		// A year in the release name that is not the other item's rules it
		// out: "Dune.2021" is not the 1984 film.
		if c.Parsed.Year > 0 && n.Year > 0 &&
			(c.Parsed.Year-n.Year > 1 || n.Year-c.Parsed.Year > 1) {
			continue
		}
		return n.Name(), true
	}
	return "", false
}

// code renders an episode key as S02E03.
func code(k Key) string { return fmt.Sprintf("S%02dE%02d", k.Season, k.Episode) }

// RunSearch searches for the wanted items that are due, a few a pass (ADR-0030,
// decision 4).
func (s *Service) RunSearch(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, passDeadline)
	defer cancel()
	p, summary, done, err := s.begin(ctx, howSearch)
	if err != nil || summary != "" {
		return summary, err
	}
	defer done()

	states, err := s.store.States(ctx)
	if err != nil {
		return "", err
	}
	due := append(p.due(states, s.now()), p.dueUpgrades(states, s.now())...)
	if len(due) == 0 {
		return "nothing is due (" + p.closed(states, s.now()) + ")", nil
	}

	var lines []string
	// searched counts searches, which is the budget; answered counts the due
	// items those searches answered for, which a season's search does for
	// every episode of it.
	searched, answered, failedN := 0, 0, 0
	capped := false
	for _, w := range due {
		if searched >= s.cfg.SearchesPerRun {
			break
		}
		if p.grabs >= s.cfg.MaxGrabsPerRun {
			capped = true
			break
		}
		if p.taken[w.Key()] {
			continue // a double episode grabbed this pass holds it
		}
		if sk := w.seasonOf(); !w.Film && p.packable[sk] {
			// The whole season is wanted and settled: one search for the
			// season, which is one request per indexer as an episode's is
			// (ADR-0033, decision 5).
			if p.seasonSearched[sk] {
				continue
			}
			p.seasonSearched[sk] = true
			searched++
			answered += len(p.seasonWants(sk))
			more, failed, err := s.searchSeason(ctx, p, sk, states)
			if errors.Is(err, search.ErrNoIndexers) {
				return "", ErrNoIndexers
			}
			if ctx.Err() != nil {
				return "", fmt.Errorf("acquire: the pass was cut short: %w", ctx.Err())
			}
			if err != nil {
				return "", err
			}
			if failed {
				failedN++
			}
			lines = append(lines, more...)
			continue
		}
		searched++
		answered++
		before := states[w.StateKey()]

		resp, err := s.searchFor(ctx, p, w)
		if errors.Is(err, search.ErrNoIndexers) {
			return "", ErrNoIndexers
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("acquire: the pass was cut short: %w", ctx.Err())
		}
		now := s.now()
		var st State
		switch {
		case err != nil:
			st = State{Outcome: OutcomeFailed, Fruitless: before.Fruitless,
				NextAt: now.Add(FailedRetry), Detail: "the search failed: " + err.Error()}
		case resp.Queried > 0 && resp.Failed == resp.Queried:
			st = State{Outcome: OutcomeFailed, Fruitless: before.Fruitless,
				NextAt: now.Add(FailedRetry), Detail: "no indexer answered: " + failures(resp)}
		default:
			st, err = s.pick(ctx, p, w, resp, before, now)
			if err != nil {
				return "", err
			}
		}
		if w.Upgrade && st.Outcome != OutcomeGrabbed {
			// Not urgent: a 720p held is a preference, not a gap. Looked
			// for again in a week, whatever came of this.
			st.NextAt = now.Add(UpgradeInterval)
		}
		st.SearchedAt = now
		s.record(ctx, w, st)

		switch st.Outcome {
		case OutcomeGrabbed:
			lines = append(lines, "grabbed "+w.Name()+" ("+strings.TrimPrefix(st.Detail, "grabbed ")+")")
		case OutcomeFailed:
			failedN++
			lines = append(lines, "failed for "+w.Name()+" — "+st.Detail)
		default:
			lines = append(lines, fmt.Sprintf("nothing for %s — %s; next in %s",
				w.Name(), st.Detail, roughly(st.NextAt.Sub(now))))
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "searched for %d of %d due: %s", answered, len(due), strings.Join(lines, "; "))
	if rest := len(due) - answered; rest > 0 {
		fmt.Fprintf(&b, ". %d more wait for the next pass", rest)
	}
	if capped {
		fmt.Fprintf(&b, ". The limit of %d grab(s) a pass was reached", s.cfg.MaxGrabsPerRun)
	}
	s.noteOwnOnly(&b, p)
	if searched > 0 && failedN == searched {
		// Red on the dashboard: nothing was looked for, and the operator
		// should find out from there rather than from an empty Wanted list.
		return "", errors.New(b.String())
	}
	return b.String(), nil
}

// searchSeason searches for a whole season whose every episode is wanted: a
// pack of it first, and failing that its episodes, one at a time, from the
// same results. Every episode's state is recorded, so the season backs off as
// one. failed is true when the search itself failed.
func (s *Service) searchSeason(ctx context.Context, p *pass, sk seasonKey,
	states map[StateKey]State) (lines []string, failed bool, err error) {

	wants := p.seasonWants(sk)
	pw := wants[0].packOf()
	titles := s.titlesFor(ctx, p, wants[0])
	resp, err := s.finder.SearchSeason(ctx, search.SeasonSearch{
		Want:     search.SeasonWant{ItemID: sk.ItemID, Titles: titles, Year: pw.Year, Season: sk.Season},
		Episodes: p.settled[sk], Profile: &s.profileFor(ctx, p, pw).Profile,
	})
	if errors.Is(err, search.ErrNoIndexers) {
		return nil, false, err
	}
	now := s.now()
	var why string
	switch {
	case err != nil:
		why = "the search failed: " + err.Error()
	case resp.Queried > 0 && resp.Failed == resp.Queried:
		why = "no indexer answered: " + failures(resp)
	}
	if why != "" {
		for _, w := range wants {
			s.record(ctx, w, State{Outcome: OutcomeFailed, Fruitless: states[w.StateKey()].Fruitless,
				SearchedAt: now, NextAt: now.Add(FailedRetry), Detail: why})
		}
		return []string{"failed for " + pw.Name() + " — " + why}, true, nil
	}

	detail, refusal, err := s.pickPack(ctx, p, pw, resp)
	if err != nil {
		return nil, false, err
	}
	if detail != "" {
		s.recordSeason(ctx, p, sk, State{Outcome: OutcomeGrabbed, SearchedAt: now, NextAt: now,
			Detail: "grabbed " + detail + " (the whole season)"})
		return []string{"grabbed " + pw.Name() + " (" + detail + ")"}, false, nil
	}

	for _, w := range wants {
		if !p.open(w) {
			continue
		}
		if p.grabs >= s.cfg.MaxGrabsPerRun {
			break // the rest are still due, and wait for the next pass
		}
		st, err := s.pick(ctx, p, w, resp, states[w.StateKey()], now)
		if err != nil {
			return nil, false, err
		}
		if st.Outcome == OutcomeNothing && refusal != "" {
			st.Detail += "; and no pack of the season could be grabbed: " + refusal
		}
		st.SearchedAt = now
		s.record(ctx, w, st)
		switch st.Outcome {
		case OutcomeGrabbed:
			lines = append(lines, "grabbed "+w.Name()+" ("+strings.TrimPrefix(st.Detail, "grabbed ")+")")
		case OutcomeFailed:
			lines = append(lines, "failed for "+w.Name()+" — "+st.Detail)
		default:
			lines = append(lines, fmt.Sprintf("nothing for %s — %s; next in %s",
				w.Name(), st.Detail, roughly(st.NextAt.Sub(now))))
		}
	}
	return lines, false, nil
}

// pickPack grabs the best eligible pack of a season among a season search's
// results. It returns what was grabbed, or why no pack was.
func (s *Service) pickPack(ctx context.Context, p *pass, pw Want, resp search.Response) (string, string, error) {
	var refusals tally
	attempts := 0
	for _, c := range resp.Candidates {
		if c.Target == nil || !c.Target.Pack || c.Target.ItemID != pw.ItemID || c.Target.Season != pw.Season {
			continue
		}
		ok, why, err := s.eligible(ctx, p, c, pw)
		if err != nil {
			return "", "", err
		}
		if !ok {
			refusals.add(why)
			continue
		}
		detail, gerr := s.grab(ctx, p, c, pw)
		switch {
		case gerr == nil:
			return detail, "", nil
		case errors.Is(gerr, errNoLongerWanted), errors.Is(gerr, errStartedMeanwhile):
			return "", gerr.Error(), nil
		case errors.Is(gerr, errGrabbedBefore), errors.Is(gerr, errPackMagnet), errors.Is(gerr, errPackShort):
			refusals.add(gerr.Error())
		default:
			refusals.add(gerr.Error())
			if attempts++; attempts >= maxAttempts {
				return "", refusals.String(), nil
			}
		}
	}
	return "", refusals.String(), nil
}

// due lists what a search pass may look for, in order: never searched first,
// newest first — a series or film just added, an episode just aired — then
// whichever has waited longest since its last search.
func (p *pass) due(states map[StateKey]State, now time.Time) []Want {
	var never, waited []Want
	for _, w := range p.wanted {
		if !p.open(w) {
			continue
		}
		st, ok := states[w.StateKey()]
		switch {
		case !ok || st.SearchedAt.IsZero():
			never = append(never, w)
		case !st.NextAt.After(now):
			waited = append(waited, w)
		}
	}
	sort.SliceStable(never, func(i, j int) bool { return never[i].When.After(never[j].When) })
	sort.SliceStable(waited, func(i, j int) bool {
		a, b := states[waited[i].StateKey()], states[waited[j].StateKey()]
		if !a.SearchedAt.Equal(b.SearchedAt) {
			return a.SearchedAt.Before(b.SearchedAt)
		}
		return waited[i].When.After(waited[j].When)
	})
	return append(never, waited...)
}

// dueUpgrades lists the files below their cutoff that may be searched for an
// upgrade: never searched, or last searched a week ago; the longest waiting
// first (ADR-0036, decision 4).
func (p *pass) dueUpgrades(states map[StateKey]State, now time.Time) []Want {
	var out []Want
	for _, w := range p.upgrades {
		if !p.open(w) {
			continue
		}
		st, ok := states[w.StateKey()]
		if ok && !st.SearchedAt.IsZero() && now.Before(st.SearchedAt.Add(UpgradeInterval)) {
			continue
		}
		out = append(out, w)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return states[out[i].StateKey()].SearchedAt.Before(states[out[j].StateKey()].SearchedAt)
	})
	return out
}

// searchFor runs the targeted search a person's Search button runs, judged by
// the default profile.
func (s *Service) searchFor(ctx context.Context, p *pass, w Want) (search.Response, error) {
	titles := s.titlesFor(ctx, p, w)
	profile := &s.profileFor(ctx, p, w).Profile
	if w.Film {
		return s.finder.SearchFilm(ctx, search.FilmSearch{
			Want:    search.FilmWant{ItemID: w.ItemID, Titles: titles, Year: w.Year},
			Profile: profile,
		})
	}
	return s.finder.SearchEpisode(ctx, search.EpisodeSearch{
		Want: search.EpisodeWant{ItemID: w.ItemID, Titles: titles, Year: w.Year,
			Season: w.Season, Episode: w.Episode, AirDate: w.airDate()},
		Profile: profile, ByDate: w.Daily,
	})
}

// pick grabs the best eligible candidate of a search for one item, and says
// what came of it.
func (s *Service) pick(ctx context.Context, p *pass, w Want, resp search.Response,
	before State, now time.Time) (State, error) {

	var refusals tally
	matched, attempts := 0, 0
	var lastFailure string
candidates:
	for _, c := range resp.Candidates {
		// A season's search answers for every episode of the season and for
		// its packs; only what is sealed to this item is this item's.
		if c.Target == nil || c.Target.Film != w.Film || c.Target.ItemID != w.ItemID ||
			c.Target.Pack != w.Pack || c.Target.Season != w.Season || c.Target.Episode != w.Episode ||
			c.Target.Album != w.Album || c.Target.Book != w.Book {
			continue
		}
		matched++
		eligible := s.eligible
		if w.Album > 0 || w.Book {
			eligible = s.ladderEligible
		}
		ok, why, err := eligible(ctx, p, c, w)
		if err != nil {
			return State{}, err
		}
		if !ok {
			refusals.add(why)
			continue
		}
		detail, gerr := s.grab(ctx, p, c, w)
		switch {
		case gerr == nil:
			return State{Outcome: OutcomeGrabbed, NextAt: now, Detail: "grabbed " + detail}, nil
		case errors.Is(gerr, errNoLongerWanted), errors.Is(gerr, errStartedMeanwhile):
			// A person acted while this ran. Not a failure of the search, and
			// nothing to back off from.
			return State{Outcome: OutcomeNothing, Fruitless: before.Fruitless,
				NextAt: now, Detail: gerr.Error()}, nil
		case errors.Is(gerr, errGrabbedBefore):
			// Only knowable once fetched: the feed did not carry its hash.
			refusals.add(gerr.Error())
		default:
			attempts++
			lastFailure = gerr.Error()
			if attempts >= maxAttempts {
				break candidates
			}
		}
	}

	if attempts > 0 {
		return State{Outcome: OutcomeFailed, Fruitless: before.Fruitless,
			NextAt: now.Add(FailedRetry),
			Detail: "every attempt to fetch a release failed; the last: " + lastFailure}, nil
	}

	fruitless := before.Fruitless + 1
	var d strings.Builder
	switch {
	case len(resp.Candidates) == 0:
		d.WriteString("the indexers found nothing")
	case matched == 0:
		fmt.Fprintf(&d, "%d result(s), none of them %s", len(resp.Candidates), w.Name())
	default:
		fmt.Fprintf(&d, "%d of %d result(s) were %s, and none could be grabbed: %s",
			matched, len(resp.Candidates), w.Name(), refusals.String())
	}
	if resp.Failed > 0 {
		fmt.Fprintf(&d, " (%d of %d indexers did not answer, so this may be incomplete)",
			resp.Failed, resp.Queried)
	}
	return State{Outcome: OutcomeNothing, Fruitless: fruitless,
		NextAt: now.Add(backoff(fruitless)), Detail: d.String()}, nil
}

// backoff is how long an item waits after its n-th fruitless search in a row:
// six hours, doubling, never more than a week.
func backoff(n int) time.Duration {
	d := FirstBackoff
	for i := 1; i < n && d < MaxBackoff; i++ {
		d *= 2
	}
	if d > MaxBackoff {
		d = MaxBackoff
	}
	return d
}

// tally counts refusals by reason, in the order first seen.
type tally struct {
	order []string
	n     map[string]int
}

func (t *tally) add(why string) {
	if t.n == nil {
		t.n = map[string]int{}
	}
	if t.n[why] == 0 {
		t.order = append(t.order, why)
	}
	t.n[why]++
}

func (t tally) String() string {
	parts := make([]string, 0, len(t.order))
	for _, why := range t.order {
		if n := t.n[why]; n > 1 {
			parts = append(parts, fmt.Sprintf("%d: %s", n, why))
		} else {
			parts = append(parts, why)
		}
	}
	return strings.Join(parts, "; ")
}

// ---------------------------------------------------------------------------
// Grabbing
// ---------------------------------------------------------------------------

// Why a grab stopped short of fetching anything, or of queueing what it
// fetched, without anything having failed.
var (
	errNoLongerWanted   = errors.New("no longer wanted: a person changed it while this pass ran")
	errStartedMeanwhile = errors.New("a download for it started while this pass ran")
	errGrabbedBefore    = errors.New("it was grabbed before (a release already in the queue " +
		"is never grabbed again)")
	errPackMagnet = errors.New("the pack is offered only as a magnet link, which says nothing " +
		"about what is inside until peers supply it; a person can grab it from the season's search")
	errPackShort = errors.New("the pack does not hold the season")
)

// stillWanted asks again about a want just before a grab: for a season, about
// every one of its episodes.
func (s *Service) stillWanted(ctx context.Context, p *pass, w Want) (bool, error) {
	if w.Upgrade {
		return s.store.StillUpgradable(ctx, w)
	}
	if !w.Pack {
		return s.store.StillWanted(ctx, w)
	}
	wants := p.seasonWants(w.seasonOf())
	if len(wants) != len(p.settled[w.seasonOf()]) {
		return false, nil
	}
	for _, e := range wants {
		ok, err := s.store.StillWanted(ctx, e)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

// recordSeason keeps one state for every wanted episode of a season.
func (s *Service) recordSeason(ctx context.Context, p *pass, sk seasonKey, st State) {
	for _, w := range p.seasonWants(sk) {
		s.record(ctx, w, st)
	}
}

// grab fetches a release for a wanted item and queues it, exactly as a
// person's Grab does — the same fetch, the same sealed target — and audits it.
func (s *Service) grab(ctx context.Context, p *pass, c search.Candidate, w Want) (string, error) {
	// Asked again at the last moment: the list was read before the search,
	// and a search takes seconds.
	still, err := s.stillWanted(ctx, p, w)
	if err != nil {
		return "", err
	}
	if !still {
		return "", errNoLongerWanted
	}
	cov := p.cover(c, w)
	inFlight, err := s.store.InFlight(ctx)
	if err != nil {
		return "", err
	}
	for _, k := range cov {
		if inFlight[k] {
			return "", errStartedMeanwhile
		}
	}

	target := w.target()
	tk := search.Ticket{
		IndexerID: c.IndexerID, DownloadURL: c.DownloadURL, InfoHash: c.InfoHash,
		Title: c.Title, Size: c.Size, Quality: c.Quality.Name, Target: &target,
	}
	got, err := s.finder.Grab(ctx, tk)
	if err != nil {
		s.noteFailure(c)
		s.auditGrab(ctx, p.how, c, w, "", "", audit.OutcomeFailure, err.Error())
		return "", fmt.Errorf("fetching %s failed: %w", c.Title, err)
	}

	// The feed does not always carry the info hash. Worked out from what was
	// fetched, so the blocklist holds for those too.
	hash, err := download.HashOf(got.Torrent, got.Magnet)
	if err != nil {
		s.noteFailure(c)
		s.auditGrab(ctx, p.how, c, w, "", got.IndexerName, audit.OutcomeFailure, err.Error())
		return "", fmt.Errorf("%s is not a usable torrent: %w", c.Title, err)
	}
	if queued, qerr := s.store.Queued(ctx, hash); qerr != nil {
		return "", qerr
	} else if queued {
		// Remembered as a failure too, so the next pass does not fetch it
		// again to find the same thing out.
		s.noteFailure(c)
		return "", errGrabbedBefore
	}
	if w.Pack {
		// Seen to hold the season before anything is downloaded (ADR-0033,
		// decision 5). Remembered either way, for the same reason as above.
		if len(got.Torrent) == 0 {
			s.noteFailure(c)
			return "", errPackMagnet
		}
		want := map[int]bool{}
		for _, k := range cov {
			want[k.Episode] = true
		}
		if ok, why := s.packs(got.Torrent, w.Season, want); !ok {
			s.noteFailure(c)
			return "", fmt.Errorf("%w: %s", errPackShort, why)
		}
	}

	meta := download.Meta{
		Title: c.Title, IndexerID: got.IndexerID, IndexerName: got.IndexerName,
		AddedLabel: Label,
		SeedRatio:  got.SeedRatio, SeedTime: got.SeedTime,
		Target: &download.Target{ItemID: target.ItemID, Season: target.Season,
			Episode: target.Episode, Film: target.Film, Pack: target.Pack, Album: target.Album, Book: target.Book},
	}
	var t download.Transfer
	if got.Magnet != "" {
		t, err = s.queue.AddMagnet(ctx, got.Magnet, meta)
	} else {
		t, err = s.queue.AddTorrent(got.Torrent, meta)
	}
	if err != nil {
		s.noteFailure(c)
		s.auditGrab(ctx, p.how, c, w, hash, got.IndexerName, audit.OutcomeFailure, err.Error())
		return "", fmt.Errorf("%s could not be added to the queue: %w", c.Title, err)
	}
	// A magnet has no metadata yet, so there may be nothing to start until it
	// resolves; the engine begins when it can. The same as a person's grab.
	_ = s.queue.Start(t.InfoHash)

	for _, k := range cov {
		p.taken[k] = true
	}
	p.grabs++
	s.auditGrab(ctx, p.how, c, w, t.InfoHash, got.IndexerName, audit.OutcomeSuccess, "")
	s.log.Info("automatic acquisition grabbed a release",
		slog.String("for", w.Name()), slog.String("title", c.Title),
		slog.String("indexer", got.IndexerName), slog.String("how", p.how),
		slog.String("info_hash", t.InfoHash))
	return fmt.Sprintf("%s, from %s", c.Title, got.IndexerName), nil
}

// auditGrab writes the line an operator answerable for their instance reads
// first: what was fetched, for what, from where, how it was found, and that
// no person pressed anything.
func (s *Service) auditGrab(ctx context.Context, how string, c search.Candidate, w Want,
	hash, indexerName string, outcome audit.Outcome, failure string) {

	if s.audit == nil {
		return
	}
	if hash == "" {
		hash = strings.ToLower(c.InfoHash)
	}
	if indexerName == "" {
		indexerName = c.IndexerName
	}
	// The release title, never the download URL: on many trackers the URL
	// carries the indexer's API key, and an audit log is the least-guarded
	// copy of anything.
	detail := fmt.Sprintf("%s | for %s | from %s | %s, automatically", c.Title, w.Name(), indexerName, how)
	if failure != "" {
		detail += " | " + failure
	}
	if err := s.audit.Write(ctx, audit.Event{
		ActorLabel: Label,
		Action:     audit.ActionReleaseGrabbed,
		Outcome:    outcome,
		TargetKind: "release",
		TargetID:   hash,
		Detail:     detail,
	}); err != nil {
		s.log.Error("an automatic grab could not be audited",
			slog.String("title", c.Title), slog.String("error", err.Error()))
	}
}

// record keeps a state, logging rather than failing the pass: what was grabbed
// is in the queue whatever happens to this row.
func (s *Service) record(ctx context.Context, w Want, st State) {
	if err := s.store.Record(ctx, w, st); err != nil {
		s.log.Error("what automatic acquisition did could not be recorded",
			slog.String("for", w.Name()), slog.String("error", err.Error()))
	}
}

// failureKey identifies a release across passes without keeping its link,
// which may carry the indexer's API key.
func failureKey(c search.Candidate) string {
	id := strings.ToLower(c.InfoHash)
	if id == "" {
		id = c.GUID
	}
	if id == "" {
		id = fmt.Sprintf("%s|%d", c.Title, c.Size)
	}
	return fmt.Sprintf("%d|%s", c.IndexerID, id)
}

func (s *Service) noteFailure(c search.Candidate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, at := range s.failed {
		if now.Sub(at) >= grabFailureTTL {
			delete(s.failed, k)
		}
	}
	s.failed[failureKey(c)] = now
}

func (s *Service) failedRecently(c search.Candidate) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.failed[failureKey(c)]
	return ok && s.now().Sub(at) < grabFailureTTL
}

// ---------------------------------------------------------------------------
// Names
// ---------------------------------------------------------------------------

type titleKey struct {
	film bool
	id   int64
}

type titleEntry struct {
	names    []string
	at       time.Time // when names were read
	failedAt time.Time // the last failed attempt to read them again
}

// due reports whether the names should be read (again).
func (e titleEntry) due(now time.Time) bool {
	if !e.failedAt.IsZero() && now.Sub(e.failedAt) < titleFailureTTL {
		return false
	}
	return e.at.IsZero() || now.Sub(e.at) >= titleTTL
}

// titlesFor is every name a release may use for the want's series or film: its
// own title first, then the provider's (ADR-0023, ADR-0026). Without them
// "The.Office.US" is a different show — and that is said, per pass, in the
// task's summary rather than hidden.
func (s *Service) titlesFor(ctx context.Context, p *pass, w Want) []string {
	titles := []string{w.Title}
	if s.titles == nil || w.TMDBID <= 0 {
		return titles
	}
	key := titleKey{film: w.Film, id: w.TMDBID}
	now := s.now()

	s.mu.Lock()
	e, cached := s.titleFor[key]
	s.mu.Unlock()

	if !cached || e.due(now) {
		if p.lookups >= s.lookupBudget {
			if !cached {
				p.ownOnly[key] = true
			}
			return appendNew(titles, e.names)
		}
		p.lookups++
		var names []string
		var err error
		if w.Film {
			names, err = s.titles.FilmTitles(ctx, w.TMDBID)
		} else {
			names, err = s.titles.AlternativeTitles(ctx, w.TMDBID)
		}
		if err != nil {
			// The names read before, if any, are still the best there are.
			e.failedAt = now
			if !cached {
				p.ownOnly[key] = true
			}
		} else {
			e = titleEntry{names: names, at: now}
		}
		s.mu.Lock()
		if len(s.titleFor) >= maxCachedTitles {
			s.titleFor = map[titleKey]titleEntry{}
		}
		s.titleFor[key] = e
		s.mu.Unlock()
	}
	return appendNew(titles, e.names)
}

// noteOwnOnly says how many titles were matched by their own name alone.
func (s *Service) noteOwnOnly(b *strings.Builder, p *pass) {
	if len(p.ownOnly) > 0 {
		fmt.Fprintf(b, ". %d title(s) were matched by their own name only: their other names "+
			"could not be read yet", len(p.ownOnly))
	}
}

// appendNew adds the names not already present, in order.
func appendNew(titles, more []string) []string {
	seen := make(map[string]bool, len(titles)+len(more))
	for _, t := range titles {
		seen[t] = true
	}
	for _, t := range more {
		if t = strings.TrimSpace(t); t != "" && !seen[t] {
			seen[t] = true
			titles = append(titles, t)
		}
	}
	return titles
}

// failures says which indexers did not answer, and why.
func failures(resp search.Response) string {
	var parts []string
	for _, o := range resp.Outcomes {
		if o.Err != "" {
			parts = append(parts, o.IndexerName+": "+o.Err)
		}
	}
	return strings.Join(parts, "; ")
}

// roughly renders a wait the way a person says it.
func roughly(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Round(24*time.Hour)/(24*time.Hour)))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Round(time.Hour)/time.Hour))
	case d >= 2*time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Round(time.Minute)/time.Minute))
	default:
		return "a moment"
	}
}

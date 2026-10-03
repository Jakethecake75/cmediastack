package acquire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/release"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// The rig is the real thing wherever that is affordable: a migrated database,
// the real search service (so matching, judging and ranking are the code a
// person's search runs), the real download queue store (so "in flight" and
// "grabbed before" are read from rows the manager writes), the real quality
// profiles and the real audit log. Only the network is fake: the indexers and
// the metadata provider.

var start = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// hash is a well-formed info hash, different for every n.
func hash(n int) string { return fmt.Sprintf("%040x", n) }

// ---------------------------------------------------------------------------
// fake indexers
// ---------------------------------------------------------------------------

type fakeIndexers struct{ defs []indexer.Definition }

func (f *fakeIndexers) Enabled(context.Context) ([]indexer.Definition, error) { return f.defs, nil }

// fakeClient is every indexer at once. An empty term is the recent-release
// feed; anything else is answered from byTerm, or from search when set.
type fakeClient struct {
	mu        sync.Mutex
	feed      []indexer.Result
	byTerm    map[string][]indexer.Result
	fail      error
	queries   []indexer.Query
	fetched   []string
	fetchFail error
	hold      time.Duration
	// torrents answers a link with a .torrent file rather than a magnet, by
	// the hash the link ends in: how a season pack is offered (ADR-0033).
	torrents map[string][]byte
	// onSearch runs inside a search — the moment a person could act while a
	// pass is waiting on an indexer.
	onSearch func(q indexer.Query)
}

func (f *fakeClient) Search(_ context.Context, d indexer.Definition, q indexer.Query) ([]indexer.Result, error) {
	f.mu.Lock()
	f.queries = append(f.queries, q)
	hook, fail := f.onSearch, f.fail
	var out []indexer.Result
	if q.Term == "" {
		out = append(out, f.feed...)
	} else {
		out = append(out, f.byTerm[strings.ToLower(q.Term)]...)
	}
	f.mu.Unlock()
	if hook != nil {
		hook(q)
	}
	if fail != nil {
		return nil, fail
	}
	for i := range out {
		out[i].IndexerID, out[i].IndexerName = d.ID, d.Name
	}
	return out, nil
}

// Download answers every link with a magnet for the hash the link ends in, or
// with the .torrent file registered for it.
func (f *fakeClient) Download(_ context.Context, _ indexer.Definition, rawURL string) (indexer.Payload, error) {
	f.mu.Lock()
	hold := f.hold
	f.mu.Unlock()
	// A slow indexer: long enough that two passes running side by side would
	// both be past their last check before either had queued anything.
	time.Sleep(hold)

	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetched = append(f.fetched, rawURL)
	if f.fetchFail != nil {
		return indexer.Payload{}, f.fetchFail
	}
	h := rawURL[strings.LastIndex(rawURL, "/")+1:]
	if data, ok := f.torrents[h]; ok {
		return indexer.Payload{Torrent: data}, nil
	}
	return indexer.Payload{Magnet: "magnet:?xt=urn:btih:" + h + "&dn=x"}, nil
}

func (f *fakeClient) snapshot() (queries []indexer.Query, fetched []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]indexer.Query(nil), f.queries...), append([]string(nil), f.fetched...)
}

func (f *fakeClient) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries, f.fetched = nil, nil
}

// rel is a release as an indexer offers it. The link ends in the hash, so
// what is fetched is what was offered.
func rel(title string, n, seeders int) indexer.Result {
	return indexer.Result{
		Title: title, GUID: "guid-" + title, InfoHash: hash(n), Seeders: seeders,
		DownloadURL: "https://tracker.example/dl/" + hash(n), Size: 2 << 30,
		Parsed: release.Parse(title),
	}
}

// ---------------------------------------------------------------------------
// fake queue and titles
// ---------------------------------------------------------------------------

// fakeQueue writes the row the manager writes, and runs nothing.
type fakeQueue struct {
	store *download.Store
	mu    sync.Mutex
	metas []download.Meta
	fail  error
}

func (q *fakeQueue) AddMagnet(ctx context.Context, magnet string, meta download.Meta) (download.Transfer, error) {
	if q.fail != nil {
		return download.Transfer{}, q.fail
	}
	h, err := download.HashOf(nil, magnet)
	if err != nil {
		return download.Transfer{}, err
	}
	if err := q.store.Put(ctx, download.Record{
		InfoHash: h, Title: meta.Title, IndexerID: meta.IndexerID, IndexerName: meta.IndexerName,
		Magnet: magnet, AddedBy: meta.AddedBy, AddedLabel: meta.AddedLabel,
		Status: download.StatusDownloading, Target: meta.Target,
	}); err != nil {
		return download.Transfer{}, err
	}
	q.mu.Lock()
	q.metas = append(q.metas, meta)
	q.mu.Unlock()
	return download.Transfer{InfoHash: h}, nil
}

func (q *fakeQueue) AddTorrent(data []byte, meta download.Meta) (download.Transfer, error) {
	if q.fail != nil {
		return download.Transfer{}, q.fail
	}
	if len(data) == 0 {
		return download.Transfer{}, errors.New("no torrent")
	}
	h, err := download.HashOf(data, "")
	if err != nil {
		return download.Transfer{}, err
	}
	if err := q.store.Put(context.Background(), download.Record{
		InfoHash: h, Title: meta.Title, IndexerID: meta.IndexerID, IndexerName: meta.IndexerName,
		Torrent: data, AddedBy: meta.AddedBy, AddedLabel: meta.AddedLabel,
		Status: download.StatusDownloading, Target: meta.Target,
	}); err != nil {
		return download.Transfer{}, err
	}
	q.mu.Lock()
	q.metas = append(q.metas, meta)
	q.mu.Unlock()
	return download.Transfer{InfoHash: h}, nil
}

func (q *fakeQueue) Start(string) error { return nil }

func (q *fakeQueue) added() []download.Meta {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]download.Meta(nil), q.metas...)
}

type fakeTitles struct {
	mu     sync.Mutex
	series map[int64][]string
	films  map[int64][]string
	err    error
	calls  int
}

func (f *fakeTitles) AlternativeTitles(_ context.Context, id int64) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.series[id], nil
}

func (f *fakeTitles) FilmTitles(_ context.Context, id int64) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.films[id], nil
}

func (f *fakeTitles) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// ---------------------------------------------------------------------------
// the rig
// ---------------------------------------------------------------------------

type rig struct {
	t        *testing.T
	database *db.DB
	clock    *clock
	store    *Store
	svc      *Service
	client   *fakeClient
	queue    *fakeQueue
	titles   *fakeTitles
	profiles *release.ProfileStore
	episodes *library.EpisodeStore
	queueDB  *download.Store
	// ctx is the scheduled task's authority, exactly as main.go mints it.
	ctx context.Context

	gateMu  sync.Mutex
	gateWhy string

	folders int
}

func newRig(t *testing.T, cfg Config) *rig {
	t.Helper()
	database, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "cms.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := &clock{t: start}
	stamp := start.Format(time.RFC3339Nano)
	if _, err := database.ExecContext(context.Background(), `
		INSERT INTO root_folder (id, path, kind, label, created_at, updated_at)
		VALUES (1, '/media/tv', 'series', 'TV', ?, ?), (2, '/media/films', 'movies', 'Films', ?, ?)`,
		stamp, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}

	profiles := release.NewProfileStore(database, c.now)
	if err := profiles.EnsureDefaults(context.Background()); err != nil {
		t.Fatal(err)
	}

	r := &rig{
		t: t, database: database, clock: c,
		store:    NewStore(database, c.now),
		client:   &fakeClient{byTerm: map[string][]indexer.Result{}},
		titles:   &fakeTitles{series: map[int64][]string{}, films: map[int64][]string{}},
		profiles: profiles,
		episodes: library.NewEpisodeStore(database, c.now),
		queueDB:  download.NewStore(database, c.now),
		ctx:      authz.SystemPrincipal(context.Background(), authz.TaskAcquire),
	}
	r.queue = &fakeQueue{store: r.queueDB}

	finder := search.New(&fakeIndexers{defs: []indexer.Definition{{ID: 1, Name: "Tracker"}}},
		r.client, nil)
	if cfg.SearchesPerRun == 0 {
		cfg.SearchesPerRun = 3
	}
	if cfg.MaxGrabsPerRun == 0 {
		cfg.MaxGrabsPerRun = 5
	}
	svc, err := New(Deps{
		Store: r.store, Finder: finder, Queue: r.queue, Titles: r.titles,
		PackCheck: func(torrent []byte, season int, want map[int]bool) (bool, string) {
			files, err := download.TorrentFiles(torrent)
			if err != nil {
				return false, err.Error()
			}
			cands := make([]importer.Candidate, 0, len(files))
			for _, f := range files {
				cands = append(cands, importer.Candidate{Path: f.Path, Bytes: f.Bytes})
			}
			return importer.CheckPack(cands, season, want)
		},
		Profiles: profiles, Audit: audit.New(database, c.now), Now: c.now,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Gate: func() (bool, string) {
			r.gateMu.Lock()
			defer r.gateMu.Unlock()
			return r.gateWhy == "", r.gateWhy
		},
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.svc = svc
	return r
}

func (r *rig) closeGate(why string) {
	r.gateMu.Lock()
	defer r.gateMu.Unlock()
	r.gateWhy = why
}

// exec runs raw SQL against the rig's database, failing the test on error.
func (r *rig) exec(query string, args ...any) {
	r.t.Helper()
	if _, err := r.database.ExecContext(context.Background(), query, args...); err != nil {
		r.t.Fatal(err)
	}
}

// addItem records a library item and returns its id.
func (r *rig) addItem(kind, title string, year int, tmdb int64, added time.Time) int64 {
	r.t.Helper()
	r.folders++
	root := 1
	if kind == "movie" {
		root = 2
	}
	var y, id any
	if year > 0 {
		y = year
	}
	if tmdb > 0 {
		id = tmdb
	}
	res, err := r.database.ExecContext(context.Background(), `
		INSERT INTO media_item (kind, title, year, sort_title, root_folder_id, folder,
		                        tmdb_id, added_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		kind, title, y, strings.ToLower(title), root, fmt.Sprintf("%s %d", title, r.folders), id,
		added.Format(time.RFC3339Nano), added.Format(time.RFC3339Nano))
	if err != nil {
		r.t.Fatal(err)
	}
	n, _ := res.LastInsertId()
	return n
}

// addSeries records a series whose seasons each have the given number of
// episodes, aired a week apart ending a day before the clock.
func (r *rig) addSeries(title string, year int, tmdb int64, episodesPerSeason ...int) int64 {
	r.t.Helper()
	id := r.addItem("series", title, year, tmdb, r.clock.now())
	total := 0
	for _, n := range episodesPerSeason {
		total += n
	}
	var seasons []library.SeasonInput
	k := 0
	for s, n := range episodesPerSeason {
		in := library.SeasonInput{Number: s + 1, Name: fmt.Sprintf("Season %d", s+1), EpisodeCount: n}
		for e := 1; e <= n; e++ {
			k++
			in.Episodes = append(in.Episodes, library.EpisodeInput{
				ProviderID: id*1000 + int64(k), Number: e, Title: fmt.Sprintf("Episode %d", e),
				Aired: r.clock.now().AddDate(0, 0, -7*(total-k)-1),
			})
		}
		in.Aired = in.Episodes[0].Aired
		seasons = append(seasons, in)
	}
	if err := r.episodes.Upsert(r.browse(), id, seasons); err != nil {
		r.t.Fatal(err)
	}
	return id
}

// addFilm records a film added at the clock's time.
func (r *rig) addFilm(title string, year int, tmdb int64) int64 {
	r.t.Helper()
	return r.addItem("movie", title, year, tmdb, r.clock.now())
}

// haveEpisodes records a file covering episodes first..last of a season.
func (r *rig) haveEpisodes(item int64, season, first, last int) {
	r.t.Helper()
	r.exec(`INSERT INTO media_file (item_id, season, episode, episode_last, root_folder_id,
	                                relative_path, imported_at)
	        VALUES (?, ?, ?, ?, 1, ?, ?)`,
		item, season, first, last, fmt.Sprintf("%d/S%02dE%02d-E%02d.mkv", item, season, first, last),
		r.clock.now().Format(time.RFC3339Nano))
}

// haveFilm records a file for a film.
func (r *rig) haveFilm(item int64) {
	r.t.Helper()
	r.exec(`INSERT INTO media_file (item_id, root_folder_id, relative_path, imported_at)
	        VALUES (?, 2, ?, ?)`, item, fmt.Sprintf("%d/film.mkv", item),
		r.clock.now().Format(time.RFC3339Nano))
}

// queued puts a row in the download queue, as a person's grab would.
func (r *rig) queued(n int, title, status string, target *download.Target) {
	r.t.Helper()
	if err := r.queueDB.Put(context.Background(), download.Record{
		InfoHash: hash(n), Title: title, Magnet: "magnet:?xt=urn:btih:" + hash(n),
		AddedLabel: "jacob", Status: download.StatusDownloading, Target: target,
	}); err != nil {
		r.t.Fatal(err)
	}
	if status != download.StatusDownloading {
		if err := r.queueDB.SetStatus(context.Background(), hash(n), status); err != nil {
			r.t.Fatal(err)
		}
	}
}

// imported records what the importer made of a download.
func (r *rig) imported(n int, outcome string) {
	r.t.Helper()
	r.exec(`INSERT INTO import_record (info_hash, outcome, detail, occurred_at) VALUES (?, ?, 'x', ?)`,
		hash(n), outcome, r.clock.now().Format(time.RFC3339Nano))
}

// episodeID finds an episode's id.
func (r *rig) episodeID(item int64, season, number int) int64 {
	r.t.Helper()
	var id int64
	if err := r.database.QueryRowContext(context.Background(),
		`SELECT id FROM episode WHERE item_id = ? AND season_number = ? AND number = ?`,
		item, season, number).Scan(&id); err != nil {
		r.t.Fatal(err)
	}
	return id
}

// browse is a person who may browse and edit the library.
func (r *rig) browse() context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 1, Name: "Admin", Rank: 100,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
	})
}

// runRecent and runSearch run a pass as the scheduled task does, failing the
// test on an error.
func (r *rig) runRecent() string {
	r.t.Helper()
	s, err := r.svc.RunRecent(r.ctx)
	if err != nil {
		r.t.Fatalf("recent-release pass: %v", err)
	}
	return s
}

func (r *rig) runSearch() string {
	r.t.Helper()
	s, err := r.svc.RunSearch(r.ctx)
	if err != nil {
		r.t.Fatalf("search pass: %v", err)
	}
	return s
}

// targets lists what the queue's automatic rows were grabbed for.
func (r *rig) targets() []string {
	r.t.Helper()
	var out []string
	for _, m := range r.queue.added() {
		switch {
		case m.Target == nil:
			out = append(out, "nothing")
		case m.Target.Film:
			out = append(out, fmt.Sprintf("film %d", m.Target.ItemID))
		case m.Target.Pack:
			out = append(out, fmt.Sprintf("%d S%02d", m.Target.ItemID, m.Target.Season))
		default:
			out = append(out, fmt.Sprintf("%d S%02dE%02d", m.Target.ItemID, m.Target.Season, m.Target.Episode))
		}
	}
	return out
}

// state reads what was recorded about one want.
func (r *rig) state(key StateKey) (State, bool) {
	r.t.Helper()
	states, err := r.store.States(r.ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	st, ok := states[key]
	return st, ok
}

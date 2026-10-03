package follow

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identify"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/tv"
)

// Adding a series before any of it is on disk (ADR-0025), against a real
// database, real stores and a real root folder on disk. Only the provider is
// canned.

var testNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func clock() time.Time { return testNow }

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// fakeProvider is Severance as TMDB describes it, and Dune (2021), with
// faults on request.
type fakeProvider struct {
	mu         sync.Mutex
	details    metadata.Details
	film       metadata.Details
	episodes   map[int][]metadata.Episode
	detailsErr error
	episodeErr map[int]error
	// calls counts every request, which is how "asked the provider nothing"
	// is checked rather than assumed; kinds records what each details request
	// asked about.
	calls int
	kinds []metadata.Kind
}

func (f *fakeProvider) Details(_ context.Context, kind metadata.Kind, id int64) (metadata.Details, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.kinds = append(f.kinds, kind)
	if f.detailsErr != nil {
		return metadata.Details{}, f.detailsErr
	}
	switch {
	case kind == metadata.KindSeries && id == f.details.ProviderID:
		return f.details, nil
	case kind == metadata.KindMovie && id == f.film.ProviderID:
		return f.film, nil
	}
	return metadata.Details{}, metadata.ErrNotFound
}

func (f *fakeProvider) Episodes(_ context.Context, _ int64, season int) ([]metadata.Episode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err := f.episodeErr[season]; err != nil {
		return nil, err
	}
	return f.episodes[season], nil
}

func (f *fakeProvider) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func severance() *fakeProvider {
	eps := func(n int, first time.Time) []metadata.Episode {
		out := make([]metadata.Episode, 0, n)
		for i := 1; i <= n; i++ {
			out = append(out, metadata.Episode{ProviderID: int64(i), Number: i,
				Title: "Episode", Aired: first.AddDate(0, 0, 7*(i-1)), Runtime: 55})
		}
		return out
	}
	return &fakeProvider{
		details: metadata.Details{
			Match: metadata.Match{ProviderID: 95396, Kind: metadata.KindSeries, Title: "Severance",
				OriginalTitle: "Severance", Year: 2022, Overview: "Work-life balance, surgically.",
				PosterPath: "sev.jpg"},
			IMDbID: "tt11280740",
			Seasons: []metadata.Season{
				{Number: 0, Name: "Specials", Episodes: 1},
				{Number: 1, Name: "Season 1", Episodes: 9, Aired: day(2022, 2, 17)},
				{Number: 2, Name: "Season 2", Episodes: 10, Aired: day(2025, 1, 17)},
				// Announced, as it really is: listed, empty, undated.
				{Number: 3, Name: "Season 3"},
			},
		},
		episodes: map[int][]metadata.Episode{
			0: eps(1, day(2021, 12, 15)),
			1: eps(9, day(2022, 2, 17)),
			2: eps(10, day(2025, 1, 17)),
		},
		episodeErr: map[int]error{},
		// As the live API answered while ADR-0026 was written.
		film: metadata.Details{
			Match: metadata.Match{ProviderID: 438631, Kind: metadata.KindMovie, Title: "Dune",
				OriginalTitle: "Dune", Year: 2021, PosterPath: "dune.jpg",
				Overview: "Paul Atreides, a brilliant and gifted young man."},
			IMDbID: "tt1160419", Runtime: 155, Released: day(2021, 9, 15),
		},
	}
}

type rig struct {
	t        *testing.T
	db       *db.DB
	svc      *Service
	provider *fakeProvider
	items    *importer.Store
	idents   *identify.Store
	episodes *library.EpisodeStore
	roots    *library.RootStore
	ctx      context.Context
	userID   int64
	base     string
	shows    string
	root     library.RootFolder
}

func newRig(t *testing.T) *rig {
	t.Helper()
	base := t.TempDir()
	database, err := db.Open(db.Options{Path: filepath.Join(base, "cms.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}

	// A real account, because an identification's decided_by is a foreign key:
	// a decision attributed to nobody who exists is not an attribution.
	stamp := testNow.Format(time.RFC3339Nano)
	if _, err := database.Exec(`INSERT INTO role (name, rank, builtin, created_at, updated_at)
		VALUES ('Admin', 100, 1, ?, ?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	res, err := database.Exec(`INSERT INTO app_user (username, email, password_hash, state, role_id,
		rating_ceiling, created_at, updated_at) VALUES ('jacob', 'j@example.com', 'x', 'active', 1, 0, ?, ?)`,
		stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	userID, _ := res.LastInsertId()

	ctx := authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: userID, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 1, Name: authz.RoleAdmin, Rank: 100,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
	})

	downloads := filepath.Join(base, "downloads")
	shows := filepath.Join(base, "media", "shows")
	for _, d := range []string{downloads, shows} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	roots := library.NewRootStore(database, downloads, clock)
	root, err := roots.Create(ctx, shows, library.KindSeries, "Shows")
	if err != nil {
		t.Fatal(err)
	}

	r := &rig{t: t, db: database, provider: severance(), ctx: ctx, userID: userID,
		base: base, shows: shows, root: root, roots: roots,
		items:    importer.NewStore(database, clock),
		idents:   identify.NewStore(database, clock),
		episodes: library.NewEpisodeStore(database, clock),
	}
	r.svc = NewService(database, r.items, r.idents, r.episodes, roots,
		func() tv.EpisodeProvider { return r.provider }, audit.New(database, clock),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return r
}

func addSeverance(monitor string) Request {
	return Request{Kind: "series", TMDBID: 95396, Monitor: monitor}
}

// count reads one number from the database.
func (r *rig) count(query string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.db.QueryRow(query, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// nothingWritten asserts the database holds no trace of an add.
func (r *rig) nothingWritten() {
	r.t.Helper()
	for table, q := range map[string]string{
		"items":           `SELECT COUNT(*) FROM media_item WHERE tmdb_id IN (95396, 438631)`,
		"identifications": `SELECT COUNT(*) FROM media_identification WHERE provider_id IN (95396, 438631)`,
		"seasons":         `SELECT COUNT(*) FROM season`,
		"episodes":        `SELECT COUNT(*) FROM episode`,
		"audit lines":     `SELECT COUNT(*) FROM audit_event WHERE action = 'media.added'`,
	} {
		if n := r.count(q); n != 0 {
			r.t.Errorf("%d %s were written", n, table)
		}
	}
}

// ---------------------------------------------------------------------------

func TestAddingASeriesRecordsAllOfItAsChosen(t *testing.T) {
	r := newRig(t)
	res, err := r.svc.Add(r.ctx, addSeverance("latest"))
	if err != nil {
		t.Fatal(err)
	}

	// Named by the provider's answer: the request carried only the id.
	it := res.Item
	if it.Title != "Severance" || it.Year != 2022 || it.TMDBID != 95396 || it.IMDbID != "tt11280740" ||
		it.Folder != "Severance (2022)" || it.RootFolderID != r.root.ID || it.Kind != importer.KindSeries {
		t.Errorf("added %+v", it)
	}
	if res.Seasons != 4 || res.Episodes != 20 || res.Latest != 2 {
		t.Errorf("seasons %d, episodes %d, latest %d; want 4, 20 (1+9+10), 2", res.Seasons, res.Episodes, res.Latest)
	}
	// Latest: season 2 and the announced season 3 followed; season 2's ten
	// aired episodes wanted.
	if res.Monitor != library.MonitorLatest || res.Monitored != 10 || res.Wanted != 10 {
		t.Errorf("monitor %s: %d monitored, %d wanted; want 10 and 10", res.Monitor, res.Monitored, res.Wanted)
	}

	// The identification is the person's.
	id, err := r.idents.Get(r.ctx, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if id.State != identify.StateConfirmed || id.DecidedBy == nil || *id.DecidedBy != r.userID ||
		id.ProviderID != 95396 {
		t.Errorf("identification = %s by %v, provider %d", id.State, id.DecidedBy, id.ProviderID)
	}

	// The episodes are there, and the wanted list says so.
	wanted, err := r.episodes.Wanted(r.ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(wanted) != 10 {
		t.Errorf("%d on the wanted list, want season 2's ten", len(wanted))
	}

	// And it was written down who did it.
	if n := r.count(`SELECT COUNT(*) FROM audit_event WHERE action = 'media.added'
		AND actor_user_id = ? AND target_id = ?`, r.userID, it.ID); n != 1 {
		t.Errorf("%d audit lines", n)
	}
}

// Nothing on disk: the folder is a name until the first import creates it.
func TestAddingASeriesCreatesNothingOnDisk(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Add(r.ctx, addSeverance("all")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(r.shows)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the root folder holds %d entries after an add, the first %q", len(entries), entries[0].Name())
	}
}

// All or nothing: a season that cannot be read means the series is not added
// at all — not added with a gap the scheduled refresh would later fill with
// the default flags.
func TestASeasonThatCannotBeReadAddsNothing(t *testing.T) {
	for name, fault := range map[string]error{
		"unavailable":  metadata.ErrUnavailable,
		"rate-limited": metadata.ErrRateLimited,
		"not found":    metadata.ErrNotFound,
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.provider.episodeErr[2] = fault
			_, err := r.svc.Add(r.ctx, addSeverance("future"))
			if !errors.Is(err, fault) {
				t.Fatalf("err = %v, want %v", err, fault)
			}
			r.nothingWritten()
		})
	}
}

// A failure after the item is written takes the item with it. Here the
// identification cannot be recorded, because the account the decision would be
// attributed to does not exist — decided_by is a foreign key — and the item
// that was inserted a statement earlier must not survive that.
func TestAFailureInsideTheAddTakesEverythingWithIt(t *testing.T) {
	r := newRig(t)
	ghost := authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: 4242, Username: "ghost", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 1, Name: authz.RoleAdmin, Rank: 100,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
	})
	if _, err := r.svc.Add(ghost, addSeverance("all")); err == nil {
		t.Fatal("the add succeeded although its identification could not be recorded")
	}
	r.nothingWritten()
}

func TestAddingTheSameSeriesTwiceAsksTheProviderNothing(t *testing.T) {
	r := newRig(t)
	first, err := r.svc.Add(r.ctx, addSeverance("all"))
	if err != nil {
		t.Fatal(err)
	}
	asked := r.provider.requests()

	again := addSeverance("none")
	again.Folder = "Severance, again"
	_, err = r.svc.Add(r.ctx, again)
	var conflict *importer.ConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, importer.ErrAlreadyInLibrary) {
		t.Fatalf("err = %v, want ErrAlreadyInLibrary", err)
	}
	if conflict.Existing.ID != first.Item.ID {
		t.Errorf("the answer names item %d, want %d", conflict.Existing.ID, first.Item.ID)
	}
	if n := r.provider.requests() - asked; n != 0 {
		t.Errorf("a duplicate cost %d provider requests", n)
	}
}

// A folder a scan already found is not adopted; nothing is written for it.
func TestAFolderAlreadyTakenIsNotAdopted(t *testing.T) {
	r := newRig(t)
	scanned, err := r.items.UpsertItem(r.ctx, importer.Item{Kind: importer.KindSeries,
		Title: "severance", RootFolderID: r.root.ID, Folder: "Severance (2022)"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.svc.Add(r.ctx, addSeverance("all"))
	var conflict *importer.ConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, importer.ErrFolderTaken) || conflict.Existing.ID != scanned.ID {
		t.Fatalf("err = %v, want ErrFolderTaken naming item %d", err, scanned.ID)
	}
	r.nothingWritten()
	if n := r.count(`SELECT COUNT(*) FROM media_identification WHERE item_id = ?`, scanned.ID); n != 0 {
		t.Error("the scanned item was identified")
	}
}

func TestAFolderTheOperatorNamesIsUsedAsTyped(t *testing.T) {
	r := newRig(t)
	req := addSeverance("all")
	req.Folder = "Severance"
	res, err := r.svc.Add(r.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Item.Folder != "Severance" {
		t.Errorf("folder = %q", res.Item.Folder)
	}
}

// Everything that can be refused without the provider is refused before it is
// asked anything.
func TestWhatCanBeRefusedIsRefusedBeforeTheProviderIsAsked(t *testing.T) {
	for name, tc := range map[string]struct {
		req  Request
		want error
	}{
		"a film with a monitoring choice": {Request{Kind: "movie", TMDBID: 438631, Monitor: "all"}, ErrNotAddable},
		"a film in a series root":         {Request{Kind: "movie", TMDBID: 438631, RootFolderID: -2}, ErrWrongRootFolder},
		"a film with no films root":       {Request{Kind: "movie", TMDBID: 438631}, ErrNoRootFolder},
		"not a kind":                      {Request{Kind: "album", TMDBID: 1, Monitor: "all"}, ErrNotAddable},
		"no id":                           {Request{Kind: "series", Monitor: "all"}, ErrNotAddable},
		"no monitoring":                   {Request{Kind: "series", TMDBID: 95396}, library.ErrNoSuchMonitoring},
		"a made-up choice":                {Request{Kind: "series", TMDBID: 95396, Monitor: "most"}, library.ErrNoSuchMonitoring},
		"an unusable folder":              {Request{Kind: "series", TMDBID: 95396, Monitor: "all", Folder: "Star Trek: Discovery"}, importer.ErrUnusableFolder},
		"a path":                          {Request{Kind: "series", TMDBID: 95396, Monitor: "all", Folder: "../elsewhere"}, importer.ErrUnusableFolder},
		"a films root":                    {Request{Kind: "series", TMDBID: 95396, Monitor: "all", RootFolderID: -1}, ErrWrongRootFolder},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			if tc.req.RootFolderID == -2 {
				tc.req.RootFolderID = r.root.ID
			}
			if tc.req.RootFolderID == -1 {
				films := filepath.Join(r.base, "media", "films")
				if err := os.MkdirAll(films, 0o755); err != nil {
					t.Fatal(err)
				}
				rf, err := r.roots.Create(r.ctx, films, library.KindMovies, "Films")
				if err != nil {
					t.Fatal(err)
				}
				tc.req.RootFolderID = rf.ID
			}
			_, err := r.svc.Add(r.ctx, tc.req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if n := r.provider.requests(); n != 0 {
				t.Errorf("the provider was asked %d time(s) about a request that was refused anyway", n)
			}
			r.nothingWritten()
		})
	}
}

// With one root folder for series the choice is obvious; with several it is
// the operator's.
func TestTheRootFolderIsChosenOnlyWhenThereIsNoChoice(t *testing.T) {
	r := newRig(t)
	second := filepath.Join(r.base, "media", "more-shows")
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	other, err := r.roots.Create(r.ctx, second, library.KindSeries, "More shows")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.svc.Add(r.ctx, addSeverance("all")); !errors.Is(err, ErrChooseRootFolder) {
		t.Fatalf("two roots, none chosen: err = %v, want ErrChooseRootFolder", err)
	}
	req := addSeverance("all")
	req.RootFolderID = other.ID
	res, err := r.svc.Add(r.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Item.RootFolderID != other.ID || res.Root.ID != other.ID {
		t.Errorf("added to root %d, want the chosen %d", res.Item.RootFolderID, other.ID)
	}

	missing := addSeverance("all")
	missing.RootFolderID = 999
	if _, err := r.svc.Add(r.ctx, missing); !errors.Is(err, ErrWrongRootFolder) {
		t.Errorf("a root folder that does not exist: err = %v, want ErrWrongRootFolder", err)
	}
}

func TestWithNoRootFolderForSeriesNothingCanBeAdded(t *testing.T) {
	r := newRig(t)
	if _, err := r.db.Exec(`DELETE FROM root_folder`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Add(r.ctx, addSeverance("all")); !errors.Is(err, ErrNoRootFolder) {
		t.Errorf("err = %v, want ErrNoRootFolder", err)
	}
}

func TestWithNoProviderNothingCanBeAdded(t *testing.T) {
	r := newRig(t)
	r.svc.provider = func() tv.EpisodeProvider { return nil }
	if _, err := r.svc.Add(r.ctx, addSeverance("all")); !errors.Is(err, metadata.ErrNoProvider) {
		t.Errorf("err = %v, want ErrNoProvider", err)
	}
	r.nothingWritten()
}

func TestAnIDTheProviderDoesNotHaveAddsNothing(t *testing.T) {
	r := newRig(t)
	req := addSeverance("all")
	req.TMDBID = 999999999
	if _, err := r.svc.Add(r.ctx, req); !errors.Is(err, metadata.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if n := r.count(`SELECT COUNT(*) FROM media_item`); n != 0 {
		t.Errorf("%d items", n)
	}
}

func TestAddingNeedsThePermissionToEditTheLibrary(t *testing.T) {
	r := newRig(t)
	viewer := authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: r.userID, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 3, Name: authz.RoleUser, Rank: 10,
			Permissions: authz.NewPermissionSet(authz.PermLogin, authz.PermBrowse, authz.PermStream)},
	})
	if _, err := r.svc.Add(viewer, addSeverance("all")); !authz.IsDenied(err) {
		t.Fatalf("err = %v, want a denial", err)
	}
	if n := r.provider.requests(); n != 0 {
		t.Errorf("a refused add cost %d provider requests", n)
	}
	r.nothingWritten()
}

// The added series' folder comes into being with its first episode: an import
// grabbed for it (ADR-0023) lands in the folder the add named, and the episode
// leaves the wanted list.
func TestAnAddedSeriesTakesItsFirstEpisodeIntoTheFolderItNamed(t *testing.T) {
	r := newRig(t)
	res, err := r.svc.Add(r.ctx, addSeverance("latest"))
	if err != nil {
		t.Fatal(err)
	}

	hash := strings.Repeat("ab", 20)
	dir := filepath.Join(r.base, "downloads", hash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "Severance.S02E03.1080p.WEB.H264-GRP.mkv"
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	const size = 16 << 20
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	imp := importer.New(r.items, r.roots, slog.New(slog.NewTextHandler(io.Discard, nil)), clock)
	out, err := imp.Import(r.ctx, importer.Source{
		InfoHash: hash, Dir: dir, ReleaseTitle: "Severance.S02E03.1080p.WEB.H264-GRP",
		Files:  []importer.Candidate{{Path: name, Bytes: size}},
		Target: &importer.Target{ItemID: res.Item.ID, Season: 2, Episode: 3},
	})
	if err != nil || out.Outcome != importer.OutcomeImported {
		t.Fatalf("import: %s %q %v", out.Outcome, out.Detail, err)
	}
	want := filepath.Join(r.shows, "Severance (2022)", "Season 02", "Severance (2022) - S02E03 [WEBDL-1080p].mkv")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("the episode is not where the add said the series lives: %v", err)
	}
	if out.Item.ID != res.Item.ID {
		t.Errorf("imported into item %d, want the added %d", out.Item.ID, res.Item.ID)
	}
	wanted, _ := r.episodes.Wanted(r.ctx, 100)
	for _, w := range wanted {
		if w.SeasonNumber == 2 && w.Number == 3 {
			t.Error("S02E03 is still wanted after it was imported")
		}
	}
	if len(wanted) != 9 {
		t.Errorf("%d wanted, want nine", len(wanted))
	}
}

// Removing a series added by mistake is the ordinary delete. With nothing on
// disk it moves nothing to trash, and everything the add wrote goes with it.
func TestAnAddedSeriesIsRemovedWithNothingToMove(t *testing.T) {
	r := newRig(t)
	res, err := r.svc.Add(r.ctx, addSeverance("all"))
	if err != nil {
		t.Fatal(err)
	}
	imp := importer.New(r.items, r.roots, slog.New(slog.NewTextHandler(io.Discard, nil)), clock)
	gone, err := imp.DeleteItem(r.ctx, res.Item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone.Trashed) != 0 || len(gone.Missing) != 0 {
		t.Errorf("deleting an added series moved %v and missed %v", gone.Trashed, gone.Missing)
	}
	// Everything the add wrote, except its audit line: the add happened.
	for table, q := range map[string]string{
		"items":           `SELECT COUNT(*) FROM media_item`,
		"identifications": `SELECT COUNT(*) FROM media_identification`,
		"candidates":      `SELECT COUNT(*) FROM media_identification_candidate`,
		"seasons":         `SELECT COUNT(*) FROM season`,
		"episodes":        `SELECT COUNT(*) FROM episode`,
	} {
		if n := r.count(q); n != 0 {
			t.Errorf("%d %s left behind", n, table)
		}
	}
	if entries, _ := os.ReadDir(r.shows); len(entries) != 0 {
		t.Errorf("the root holds %d entries", len(entries))
	}
}

// A scan works from files, and an added series has none: it is left alone,
// and does not make a root look as though its library vanished.
func TestAScanLeavesAnAddedSeriesAlone(t *testing.T) {
	r := newRig(t)
	res, err := r.svc.Add(r.ctx, addSeverance("all"))
	if err != nil {
		t.Fatal(err)
	}
	imp := importer.New(r.items, r.roots, slog.New(slog.NewTextHandler(io.Discard, nil)), clock)
	scan, err := imp.Scan(r.ctx, r.root.ID)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(scan.Missing) != 0 || scan.Added != 0 {
		t.Errorf("scan = %+v", scan)
	}
	if _, err := r.items.GetItem(r.ctx, res.Item.ID); err != nil {
		t.Errorf("the added series is gone after a scan: %v", err)
	}
	if n := r.count(`SELECT COUNT(*) FROM episode WHERE item_id = ?`, res.Item.ID); n != 20 {
		t.Errorf("%d episodes after a scan", n)
	}
}

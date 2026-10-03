package books

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

const lefthand = `{"docs":[{"key":"/works/OL59800W","title":"The Left Hand of Darkness",
	"author_name":["Ursula K. Le Guin"],"first_publish_year":1969,"cover_i":10618463,"edition_count":91},
	{"key":"/works/OL18955388W","title":"Ursula K. Le Guin's the left hand of darkness",
	"author_name":["Harold Bloom"],"first_publish_year":1987,"edition_count":2},
	{"key":"/authors/OL31353A","title":"not a work"},
	{"key":"/works/OL1W","title":"  "}]}`

// ADR-0048, decision 2.
func TestOpenLibraryIsAskedPolitely(t *testing.T) {
	var mu sync.Mutex
	var seen []time.Time
	var agents, queries []string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, time.Now())
		agents = append(agents, r.Header.Get("User-Agent"))
		queries = append(queries, r.URL.Path+"?"+r.URL.RawQuery)
		code := status
		mu.Unlock()
		w.WriteHeader(code)
		_, _ = w.Write([]byte(lefthand))
	}))
	defer srv.Close()
	ol := NewOpenLibrary(srv.Client(), srv.URL, "test")
	ol.interval = 150 * time.Millisecond
	ctx := t.Context()

	found, err := ol.SearchBooks(ctx, "the left hand of darkness")
	if err != nil || len(found) != 2 || found[0].ID != "OL59800W" || found[0].Author() != "Ursula K. Le Guin" ||
		found[0].Year != 1969 || found[0].Editions != 91 {
		t.Fatalf("search: %+v %v", found, err)
	}
	w, err := ol.Work(ctx, "OL59800W")
	if err != nil || w.Title != "The Left Hand of Darkness" || w.Year != 1969 {
		t.Fatalf("work: %+v %v", w, err)
	}
	if _, err := ol.Work(ctx, "OL31353A"); !errors.Is(err, ErrNotAWork) {
		t.Errorf("an author id was asked for as a work: %v", err)
	}
	if _, err := ol.Work(ctx, "OL999W"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a work the answer does not hold: %v", err)
	}
	mu.Lock()
	status = http.StatusTooManyRequests
	mu.Unlock()
	if _, err := ol.SearchBooks(ctx, "x"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a 429: %v", err)
	}
	mu.Lock()
	status = http.StatusInternalServerError
	mu.Unlock()
	if _, err := ol.SearchBooks(ctx, "x"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a 500: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 5 {
		t.Fatalf("%d requests: %q (the author id must not have been asked)", len(queries), queries)
	}
	if !strings.Contains(queries[1], "q=key%3A%2Fworks%2FOL59800W") || !strings.Contains(queries[0], "fields=") {
		t.Errorf("queries %q", queries)
	}
	for i, a := range agents {
		if !strings.HasPrefix(a, "CMediaStack/test (") || !strings.Contains(a, "github.com/jakethecake75/cmediastack") {
			t.Errorf("request %d's User-Agent %q", i, a)
		}
		// The client spaces when requests leave, 150 ms here; the server sees
		// when they arrive, and one request's transit can be slower than the
		// next's — found under the race detector at 136 ms.
		if i > 0 && seen[i].Sub(seen[i-1]) < 100*time.Millisecond {
			t.Errorf("requests %d and %d %s apart", i-1, i, seen[i].Sub(seen[i-1]))
		}
	}
}

type fakeCatalogue struct {
	works map[string]Work
	asked int
}

func (f *fakeCatalogue) SearchBooks(context.Context, string) ([]Work, error) { return nil, nil }
func (f *fakeCatalogue) Work(_ context.Context, id string) (Work, error) {
	f.asked++
	w, ok := f.works[id]
	if !ok {
		return Work{}, ErrNotFound
	}
	return w, nil
}

type rig struct {
	db    *db.DB
	svc   *Service
	cat   *fakeCatalogue
	roots *library.RootStore
	ctx   context.Context
}

func admin(ctx context.Context) context.Context {
	return authz.WithPrincipal(ctx, &authz.Principal{UserID: 1, Username: "jacob",
		State: authz.StateActive, MFASatisfied: true, UnrestrictedLibraries: true,
		Role: authz.Role{Name: "Admin", Rank: 100, Permissions: authz.NewPermissionSet(authz.AllPermissions...)}})
}

func newRig(t *testing.T) *rig {
	t.Helper()
	database, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "books.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	r := &rig{db: database, ctx: admin(t.Context())}
	r.roots = library.NewRootStore(database, t.TempDir(), time.Now)
	r.cat = &fakeCatalogue{works: map[string]Work{
		"OL59800W": {ID: "OL59800W", Title: "The Left Hand of Darkness", Authors: []string{"Ursula K. Le Guin"}, Year: 1969},
		"OL27448W": {ID: "OL27448W", Title: "The Lord of the Rings: Part 1/3", Authors: []string{"J.R.R. Tolkien"}},
	}}
	r.svc = NewService(database, r.roots, r.cat, audit.New(database, time.Now), time.Now)
	return r
}

func (r *rig) root(t *testing.T, kind, label string) int64 {
	t.Helper()
	dir := filepath.Join(t.TempDir(), label)
	rf, err := r.roots.Create(r.ctx, mkdir(t, dir), kind, label)
	if err != nil {
		t.Fatal(err)
	}
	return rf.ID
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ADR-0048, decision 3.
func TestABookIsAddedFromOpenLibrary(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Add(r.ctx, AddRequest{WorkID: "OL59800W"}); !errors.Is(err, ErrNoRootFolder) {
		t.Errorf("no books root: %v", err)
	}
	films := r.root(t, library.KindMovies, "Films")
	books := r.root(t, library.KindBooks, "Books")

	if _, err := r.svc.Add(r.ctx, AddRequest{WorkID: "OL59800W", RootFolderID: films}); !errors.Is(err, ErrWrongRootFolder) {
		t.Errorf("into a films root: %v", err)
	}
	asked := r.cat.asked
	if _, err := r.svc.Add(r.ctx, AddRequest{WorkID: "../etc"}); !errors.Is(err, ErrNotAWork) || r.cat.asked != asked {
		t.Errorf("a bad id: %v, %d asked", err, r.cat.asked-asked)
	}
	viewer := authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 2, Username: "v",
		State: authz.StateActive, MFASatisfied: true, UnrestrictedLibraries: true,
		Role: authz.Role{Name: "User", Permissions: authz.NewPermissionSet(authz.PermBrowse)}})
	if _, err := r.svc.Add(viewer, AddRequest{WorkID: "OL59800W"}); !authz.IsDenied(err) {
		t.Errorf("a User added a book: %v", err)
	}

	item, err := r.svc.Add(r.ctx, AddRequest{WorkID: "OL59800W"})
	if err != nil {
		t.Fatal(err)
	}
	if item.Folder != "Ursula K. Le Guin - The Left Hand of Darkness (1969)" || item.RootFolderID != books ||
		!item.Monitored || item.Kind != KindBook || item.Author != "Ursula K. Le Guin" {
		t.Errorf("added %+v", item)
	}
	// Read back through the library as any title is.
	got, err := importer.NewStore(r.db, time.Now).GetItem(r.ctx, item.ID)
	if err != nil || got.Author != "Ursula K. Le Guin" || got.OpenLibraryID != "OL59800W" || got.Year != 1969 {
		t.Errorf("read back %+v %v", got, err)
	}
	var detail string
	if err := r.db.QueryRowContext(r.ctx, `SELECT detail FROM audit_event WHERE action = ?`,
		audit.ActionMediaAdded).Scan(&detail); err != nil || !strings.Contains(detail, "OL59800W") {
		t.Errorf("audit %q %v", detail, err)
	}

	var conflict *importer.ConflictError
	if _, err := r.svc.Add(r.ctx, AddRequest{WorkID: "OL59800W"}); !errors.As(err, &conflict) ||
		!errors.Is(err, importer.ErrAlreadyInLibrary) || conflict.Existing.ID != item.ID {
		t.Errorf("a second add: %v", err)
	}
	if _, err := r.svc.Add(r.ctx, AddRequest{WorkID: "OL59800W", Folder: "Elsewhere"}); !errors.Is(err, importer.ErrAlreadyInLibrary) {
		t.Errorf("the same book in another folder: %v", err)
	}
	if _, err := r.svc.Add(r.ctx, AddRequest{WorkID: "OL27448W", Folder: item.Folder}); !errors.Is(err, importer.ErrFolderTaken) {
		t.Errorf("into a taken folder: %v", err)
	}
	if _, err := r.svc.Add(r.ctx, AddRequest{WorkID: "OL27448W", Folder: "a/b"}); !errors.Is(err, importer.ErrUnusableFolder) {
		t.Errorf("a folder with a separator: %v", err)
	}
	// No year: no "(0)"; a title with a slash made safe.
	lotr, err := r.svc.Add(r.ctx, AddRequest{WorkID: "OL27448W"})
	if err != nil || strings.Contains(lotr.Folder, "/") || strings.Contains(lotr.Folder, "(0)") ||
		!strings.HasPrefix(lotr.Folder, "J.R.R. Tolkien - The Lord of the Rings") {
		t.Errorf("folder %q %v", lotr.Folder, err)
	}
	if _, err := r.svc.Add(r.ctx, AddRequest{WorkID: "OL404W"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a work Open Library does not have: %v", err)
	}

	r.root(t, library.KindBooks, "More books")
	if _, err := r.svc.Add(r.ctx, AddRequest{WorkID: "OL404W"}); !errors.Is(err, ErrChooseRootFolder) {
		t.Errorf("two books roots and none named: %v", err)
	}
}

// ADR-0048, decisions 3 and 4.
func TestWhatBooksAreWanted(t *testing.T) {
	r := newRig(t)
	shelf := r.root(t, library.KindBooks, "Books")
	attic := r.root(t, library.KindBooks, "Attic")
	lh, err := r.svc.Add(r.ctx, AddRequest{WorkID: "OL59800W", RootFolderID: shelf})
	if err != nil {
		t.Fatal(err)
	}
	lotr, err := r.svc.Add(r.ctx, AddRequest{WorkID: "OL27448W", RootFolderID: attic})
	if err != nil {
		t.Fatal(err)
	}
	wanted := func(ctx context.Context) []int64 {
		t.Helper()
		list, err := r.svc.Wanted(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		var ids []int64
		for _, b := range list {
			ids = append(ids, b.ID)
		}
		return ids
	}
	if got := wanted(r.ctx); len(got) != 2 || got[0] != lotr.ID {
		t.Fatalf("wanted %v, newest first", got)
	}
	// Scoped: one library, one book; a ceiling does not hide a book.
	kid := authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 3, Username: "kid",
		State: authz.StateActive, MFASatisfied: true, LibraryIDs: []int64{shelf}, RatingCeiling: 1,
		Role: authz.Role{Name: "User", Permissions: authz.NewPermissionSet(authz.PermBrowse)}})
	if got := wanted(kid); len(got) != 1 || got[0] != lh.ID {
		t.Errorf("the kid's wanted books %v", got)
	}
	// Unmonitored, or held: not wanted.
	if _, err := importer.NewStore(r.db, time.Now).SetFilmMonitored(r.ctx, lotr.ID, false); err != nil {
		t.Fatalf("a book's monitoring: %v", err)
	}
	if _, err := r.db.ExecContext(r.ctx, `INSERT INTO media_file (item_id, root_folder_id, relative_path, imported_at)
		VALUES (?, ?, 'x.epub', 'x')`, lh.ID, shelf); err != nil {
		t.Fatal(err)
	}
	if got := wanted(r.ctx); len(got) != 0 {
		t.Errorf("wanted %v after one was unmonitored and the other held", got)
	}
}

// ADR-0048, decision 2: a connection that fails is tried once more, after the
// same wait; an answer is never retried.
func TestAFailedConnectionIsTriedOnceMore(t *testing.T) {
	var mu sync.Mutex
	calls, drop := 0, 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n, d := calls, drop
		mu.Unlock()
		if n <= d {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		if r.URL.Query().Get("q") == "busy" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(lefthand))
	}))
	defer srv.Close()
	// No connection reuse: Go's transport quietly retries a GET whose reused
	// connection broke, and every count here must be this client's own.
	client := srv.Client()
	client.Transport.(*http.Transport).DisableKeepAlives = true
	ol := NewOpenLibrary(client, srv.URL, "test")
	ol.interval = 10 * time.Millisecond
	// count reads the server's tally under its lock: the handler runs on the
	// server's goroutines.
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
	reset := func(d int) {
		mu.Lock()
		defer mu.Unlock()
		calls, drop = 0, d
	}

	if found, err := ol.SearchBooks(t.Context(), "left hand"); err != nil || len(found) != 2 || count() != 2 {
		t.Errorf("one reset: %d found, %v, %d calls", len(found), err, count())
	}
	reset(2)
	if _, err := ol.SearchBooks(t.Context(), "left hand"); !errors.Is(err, ErrUnavailable) || count() != 2 {
		t.Errorf("two resets: %v after %d calls, want unavailable after 2", err, count())
	}
	reset(0)
	if _, err := ol.SearchBooks(t.Context(), "busy"); !errors.Is(err, ErrUnavailable) || count() != 1 {
		t.Errorf("a 503 was retried: %v after %d calls", err, count())
	}
}

// A request that times out is not sent again: that would double the wait.
func TestATimeoutIsNotRetried(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	client := srv.Client()
	client.Timeout = 50 * time.Millisecond
	ol := NewOpenLibrary(client, srv.URL, "test")
	ol.interval = time.Millisecond
	if _, err := ol.SearchBooks(t.Context(), "slow"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a timeout: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("%d requests for one that timed out", calls)
	}
}

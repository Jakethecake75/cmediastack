package api

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/books"
	"github.com/jakethecake75/cmediastack/internal/identify"
	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/issue"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/music"
	"github.com/jakethecake75/cmediastack/internal/playback"
	"github.com/jakethecake75/cmediastack/internal/search"
	"github.com/jakethecake75/cmediastack/internal/subtitles"
)

// Libraries are root folders, and a rating ceiling hides what is above it
// (ADR-0037).

// scopeWorld is an instance with two film roots and two series roots, a title
// of each kind on each side of a restricted Manager's scope, and the services
// every route that names a title needs.
type scopeWorld struct {
	r     *rig
	admin *client
	// kid is a Manager restricted to the Kids roots, rated up to TV-PG.
	kid                                  *client
	kidID                                int64
	kids, films, kidsTV, tv              int64
	shown, hiddenByRoot, hiddenByRating  int64
	unrated, shownSeries, hiddenSeries   int64
	shownFile, hiddenFile, hiddenEpisode int64
	shownTMDB, hiddenTMDB                int64
	posters                              *fakeArtwork
	// dirs are the root folders' directories, by id.
	dirs                                 map[int64]string
	episodes                             *library.EpisodeStore
	discover                             *fakeDiscover
	musicRoot, hiddenArtist, hiddenAlbum int64
	catalogue                            *fakeCatalogue
	// asked is every indexer query, as "t q cat" (ADR-0046).
	asked []string
	// subtitleProvider answers subtitle searches (ADR-0055).
	subtitleProvider *fakeSubtitles
}

type fakeArtwork struct{ dir string }

func (f *fakeArtwork) Exists(name string) bool {
	_, err := os.Stat(filepath.Join(f.dir, filepath.FromSlash(name)))
	return err == nil
}
func (f *fakeArtwork) Open(name string) (*os.File, error) {
	return os.Open(filepath.Join(f.dir, filepath.FromSlash(name)))
}

func adminPrincipalCtx(t *testing.T) *authz.Principal {
	t.Helper()
	return &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
		UnrestrictedLibraries: true,
		Role: authz.Role{ID: 1, Name: "Admin", Rank: 100,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
	}
}

func newScopeWorld(t *testing.T) *scopeWorld {
	t.Helper()
	r := newRig(t)
	w := &scopeWorld{r: r, admin: r.bootstrapAdmin(), dirs: map[int64]string{}, discover: &fakeDiscover{}, catalogue: &fakeCatalogue{},
		subtitleProvider: &fakeSubtitles{}}
	ctx := authz.WithPrincipal(t.Context(), adminPrincipalCtx(t))

	root := func(label, kind string) int64 {
		dir := filepath.Join(t.TempDir(), strings.ReplaceAll(strings.ToLower(label), " ", "-"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		rf, err := r.roots.Create(ctx, dir, kind, label)
		if err != nil {
			t.Fatal(err)
		}
		w.dirs[rf.ID] = dir
		return rf.ID
	}
	w.kids, w.films = root("Kids", library.KindMovies), root("Films", library.KindMovies)
	w.kidsTV, w.tv = root("Kids TV", library.KindSeries), root("TV", library.KindSeries)
	w.musicRoot = root("Music", library.KindMusic)

	now := r.clk.now().UTC().Format(time.RFC3339Nano)
	item := func(kind string, rootID int64, title string, tmdb int64, cert string) int64 {
		var certV, rank any
		if library.RatingRank(cert) > 0 {
			certV, rank = cert, library.RatingRank(cert)
		}
		res, err := r.database.ExecContext(t.Context(), `
			INSERT INTO media_item (kind, title, sort_title, root_folder_id, folder, tmdb_id,
			                        certification, rating_rank, rating_source, added_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, CASE WHEN ? IS NULL THEN NULL ELSE 'provider' END, ?, ?)`,
			kind, title, strings.ToLower(title), rootID, title, tmdb, certV, rank, certV, now, now)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	w.shownTMDB, w.hiddenTMDB = 1001, 1002
	w.shown = item("movie", w.kids, "Paddington", w.shownTMDB, "PG")
	w.hiddenByRoot = item("movie", w.films, "Heat", w.hiddenTMDB, "PG")
	w.hiddenByRating = item("movie", w.kids, "Gremlins", 1003, "PG-13")
	w.unrated = item("movie", w.kids, "Home Video", 1004, "")
	w.shownSeries = item("series", w.kidsTV, "Bluey", 1005, "TV-Y")
	w.hiddenSeries = item("series", w.tv, "Severance", 95396, "TV-MA")
	w.hiddenArtist = item("artist", w.musicRoot, "Radiohead", 0, "")
	if res, err := r.database.ExecContext(t.Context(), `
		INSERT INTO album (item_id, musicbrainz_id, title, album_type, released_at, created_at, updated_at)
		VALUES (?, 'okc', 'OK Computer', 'album', '1997-05-21', ?, ?)`, w.hiddenArtist, now, now); err != nil {
		t.Fatal(err)
	} else {
		w.hiddenAlbum, _ = res.LastInsertId()
	}

	file := func(itemID, rootID int64, rel string) int64 {
		res, err := r.database.ExecContext(t.Context(), `
			INSERT INTO media_file (item_id, root_folder_id, relative_path, size_bytes, quality,
			                        revision, release_title, imported_at)
			VALUES (?, ?, ?, 1, 'WEBDL-1080p', 0, 'x', ?)`, itemID, rootID, rel, now)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	w.shownFile = file(w.shown, w.kids, "Paddington/Paddington.mkv")
	w.hiddenFile = file(w.hiddenByRoot, w.films, "Heat/Heat.mkv")

	episodes := library.NewEpisodeStore(r.database, r.clk.now)
	for _, id := range []int64{w.shownSeries, w.hiddenSeries} {
		season := library.SeasonInput{Number: 1, Name: "Season 1", EpisodeCount: 2}
		for n := 1; n <= 2; n++ {
			season.Episodes = append(season.Episodes, library.EpisodeInput{
				ProviderID: id*100 + int64(n), Number: n, Title: "Episode " + strconv.Itoa(n),
				Aired: r.clk.now().AddDate(0, 0, -30+n)})
		}
		if err := episodes.Upsert(ctx, id, []library.SeasonInput{season}); err != nil {
			t.Fatal(err)
		}
	}
	w.episodes = episodes
	_, byNumber, err := episodes.Seasons(ctx, w.hiddenSeries)
	if err != nil {
		t.Fatal(err)
	}
	w.hiddenEpisode = byNumber[1][0].ID

	for _, id := range []int64{w.shown, w.hiddenByRoot} {
		if _, err := r.database.ExecContext(t.Context(), `
			INSERT INTO media_identification (item_id, state, parsed_title, updated_at)
			VALUES (?, 'proposed', 'x', ?)`, id, now); err != nil {
			t.Fatal(err)
		}
	}

	w.posters = &fakeArtwork{dir: t.TempDir()}
	for _, id := range []int64{w.shownTMDB, w.hiddenTMDB} {
		p := filepath.Join(w.posters.dir, "poster", "tmdb", strconv.FormatInt(id, 10)+"-w342.jpg")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("\xff\xd8\xff poster"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	idSvc := identify.NewService(identify.NewStore(r.database, r.clk.now),
		func() metadata.Provider { return nil }, r.media, nil, r.audit, logger, r.clk.now)
	pb := playback.NewService(r.media, r.roots, playback.NewStore(r.database, r.clk.now),
		playback.NewProber(nil), playback.NewPositions(r.database, r.clk.now), nil, logger, 1)
	// The indexers answer with albums (ADR-0046); a grab gets a torrent.
	searcher := search.New(r.indexers, indexer.NewClientWithDoer(doerFunc(func(req *http.Request) (*http.Response, error) {
		body := musicFeed
		if strings.Contains(req.URL.Path, "/dl/") {
			body = grabTorrent
		} else {
			q := req.URL.Query()
			w.asked = append(w.asked, q.Get("t")+" "+q.Get("q")+" "+q.Get("cat"))
			if q.Get("cat") == "7000" {
				body = bookFeed
			}
		}
		return &http.Response{StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})), r.indexers)
	auth := NewSessionAuthenticator(r.store, r.svc.Policy().Session, false)
	rt := NewRouter(
		[]Middleware{Recovery(logger), RequestContext(logger),
			ClientIPResolver(nil), SecurityHeaders(time.Hour)},
		[]Middleware{CSRF(), Authenticate(auth, r.audit)},
	)
	RegisterRoutes(rt, New(Deps{
		Identity: r.svc, Auth: auth, Egress: r.egress, Indexers: r.indexers,
		Search: searcher, Profiles: r.profiles, Downloads: r.downloads,
		Roots: r.roots, Media: r.media, Scanner: r.scanner, Deleter: r.scanner,
		Tickets: r.tickets, Grabs: searcher, Requests: r.requests, Audit: r.audit,
		Episodes: episodes, Identify: idSvc, Playback: pb, Artwork: w.posters,
		Calendar: episodes, Arrivals: r.media, BaseURL: "https://media.example.com/",
		Issues:         issue.NewService(r.database, r.audit, r.clk.now),
		Discover:       w.discover,
		Music:          music.NewService(r.database, music.NewStore(r.database, r.clk.now), r.roots, w.catalogue, r.audit),
		Books:          books.NewService(r.database, r.roots, fakeBookCatalogue{}, r.audit, r.clk.now),
		Subtitles:      subtitles.NewService(r.store, subtitleCipher(t), w.subtitleProvider, r.media, r.roots, r.audit, nil),
		TrashRetention: 7 * 24 * time.Hour,
	}))
	r.rt = rt

	w.kid, w.kidID = w.scopedAccount("kid", authz.RoleManager, []int64{w.kids, w.kidsTV}, 2)
	return w
}

// scopedAccount issues an invite restricted to roots and a ceiling, and signs
// the account in.
func (w *scopeWorld) scopedAccount(name, role string, roots []int64, ceiling int) (*client, int64) {
	w.r.t.Helper()
	res := w.admin.post("/api/v1/invites", map[string]any{
		"role_id": w.r.roleID(role), "all_libraries": false, "library_ids": roots,
		"rating_ceiling": ceiling, "auto_approve": true, "ttl_hours": 24,
	})
	if res.Code != http.StatusCreated {
		w.r.t.Fatalf("invite: %d %s", res.Code, res.Raw)
	}
	c := w.r.redeemAndEnroll(res.Body["code"].(string), name, "a-long-passphrase-9")
	return c, w.userID(name)
}

func (w *scopeWorld) userID(name string) int64 {
	w.r.t.Helper()
	res := w.admin.get("/api/v1/admin/users")
	for _, u := range res.Body["users"].([]any) {
		m := u.(map[string]any)
		if m["username"] == name {
			return int64(m["id"].(float64))
		}
	}
	w.r.t.Fatalf("no user %s in %s", name, res.Raw)
	return 0
}

// Every route that names a title, an episode or a file answers a restricted
// account for one outside its scope exactly as it answers for one that does not
// exist: hidden is indistinguishable from absent.
func TestATitleOutOfScopeDoesNotExist(t *testing.T) {
	w := newScopeWorld(t)
	const absent = 987654
	id := func(v int64) string { return strconv.FormatInt(v, 10) }

	type tc struct {
		method, pattern string
		hidden          int64
		body            any
	}
	cases := []tc{
		{http.MethodGet, "/api/v1/media/{id}", w.hiddenByRoot, nil},
		// Not built yet (501 to everybody); here so that when they are, they
		// are held to the same rule.
		{http.MethodGet, "/api/v1/media/{id}/artwork", w.hiddenByRoot, nil},
		{http.MethodGet, "/api/v1/media/{id}/original", w.hiddenByRoot, nil},
		{http.MethodGet, "/api/v1/media/{id}/children", w.hiddenSeries, nil},
		{http.MethodGet, "/api/v1/media/{id}/albums", w.hiddenArtist, nil},
		{http.MethodGet, "/api/v1/albums/{id}", w.hiddenAlbum, nil},
		{http.MethodPut, "/api/v1/albums/{id}/monitored", w.hiddenAlbum, map[string]any{"monitored": false}},
		{http.MethodPost, "/api/v1/albums/{id}/search", w.hiddenAlbum, map[string]any{}},
		{http.MethodPut, "/api/v1/media/{id}/seasons/{season}/monitored", w.hiddenSeries, map[string]any{"monitored": false}},
		{http.MethodPut, "/api/v1/media/{id}/new-seasons", w.hiddenSeries, map[string]any{"follow": false}},
		{http.MethodPut, "/api/v1/media/{id}/season-folders", w.hiddenSeries, map[string]any{"season_folders": false}},
		{http.MethodPut, "/api/v1/media/{id}/daily", w.hiddenSeries, map[string]any{"daily": true}},
		{http.MethodPut, "/api/v1/episodes/{id}/monitored", w.hiddenEpisode, map[string]any{"monitored": false}},
		{http.MethodPut, "/api/v1/media/{id}/monitored", w.hiddenByRoot, map[string]any{"monitored": false}},
		{http.MethodPut, "/api/v1/media/{id}/quality-profile", w.hiddenByRoot, map[string]any{"profile_id": nil}},
		{http.MethodPut, "/api/v1/media/{id}/rating", w.hiddenByRoot, map[string]any{"certification": "G"}},
		{http.MethodPost, "/api/v1/episodes/{id}/search", w.hiddenEpisode, map[string]any{}},
		{http.MethodPost, "/api/v1/media/{id}/seasons/{season}/search", w.hiddenSeries, map[string]any{}},
		{http.MethodPost, "/api/v1/media/{id}/search", w.hiddenByRoot, map[string]any{}},
		{http.MethodGet, "/api/v1/files/{id}/playback", w.hiddenFile, nil},
		{http.MethodGet, "/api/v1/files/{id}/stream", w.hiddenFile, nil},
		{http.MethodPut, "/api/v1/files/{id}/position", w.hiddenFile, map[string]any{"position_ms": 60000, "duration_ms": 600000}},
		{http.MethodDelete, "/api/v1/files/{id}/position", w.hiddenFile, nil},
		{http.MethodGet, "/api/v1/files/{id}/convert", w.hiddenFile, nil},
		{http.MethodPost, "/api/v1/files/{id}/cast", w.hiddenFile, nil},
		{http.MethodGet, "/api/v1/files/{id}/subtitles", w.hiddenFile, nil},
		{http.MethodGet, "/api/v1/files/{id}/subtitles/{sid}", w.hiddenFile, nil},
		{http.MethodPost, "/api/v1/files/{id}/subtitles/fetch", w.hiddenFile, map[string]any{"language": "en"}},
		{http.MethodGet, "/api/v1/identify/{id}", w.hiddenByRoot, nil},
		{http.MethodPost, "/api/v1/identify/{id}/confirm", w.hiddenByRoot, map[string]any{"provider_id": 1}},
		{http.MethodPost, "/api/v1/identify/{id}/reject", w.hiddenByRoot, map[string]any{"why": "no"}},
		{http.MethodPost, "/api/v1/identify/{id}/reopen", w.hiddenByRoot, map[string]any{}},
	}

	// Every such route is in the table: a new one added without a line here
	// would otherwise go unasserted.
	names := regexp.MustCompile(`^/api/v1/(media|episodes|files|identify|albums)/\{id\}`)
	covered := map[string]bool{}
	for _, c := range cases {
		covered[c.method+" "+c.pattern] = true
	}
	for _, route := range w.r.rt.Routes() {
		if names.MatchString(route.Pattern) && !covered[route.Method+" "+route.Pattern] {
			t.Errorf("%s %s names a title and is not checked here", route.Method, route.Pattern)
		}
	}

	fill := func(pattern string, v int64) string {
		p := strings.ReplaceAll(pattern, "{id}", id(v))
		p = strings.ReplaceAll(p, "{season}", "1")
		return strings.ReplaceAll(p, "{sid}", "x1")
	}
	normal := func(raw string, v int64) string { return strings.ReplaceAll(raw, id(v), "ID") }
	for _, c := range cases {
		hidden := w.kid.do(c.method, fill(c.pattern, c.hidden), c.body)
		missing := w.kid.do(c.method, fill(c.pattern, absent), c.body)
		if hidden.Code != missing.Code || normal(hidden.Raw, c.hidden) != normal(missing.Raw, absent) {
			t.Errorf("%s %s: hidden answers %d %s; absent answers %d %s",
				c.method, c.pattern, hidden.Code, hidden.Raw, missing.Code, missing.Raw)
		}
		if hidden.Code/100 == 2 {
			t.Errorf("%s %s: a hidden title answered %d", c.method, c.pattern, hidden.Code)
		}
		// And the administrator, who sees everything, is not refused.
		if c.pattern == "/api/v1/media/{id}" || c.pattern == "/api/v1/media/{id}/children" {
			if res := w.admin.get(fill(c.pattern, c.hidden)); res.Code != http.StatusOK {
				t.Errorf("the administrator was refused %s: %d", c.pattern, res.Code)
			}
		}
	}

	// Hidden by its rating, in a root the account holds: the same.
	for _, v := range []int64{w.hiddenByRating, w.unrated} {
		if res := w.kid.get("/api/v1/media/" + id(v)); res.Code != http.StatusNotFound {
			t.Errorf("a title rated above the ceiling, or unrated: %d", res.Code)
		}
	}
	if res := w.kid.get("/api/v1/media/" + id(w.shown)); res.Code != http.StatusOK {
		t.Errorf("a title in scope: %d %s", res.Code, res.Raw)
	}
	if res := w.kid.get("/api/v1/media/" + id(w.shownSeries) + "/children"); res.Code != http.StatusOK {
		t.Errorf("a series in scope: %d %s", res.Code, res.Raw)
	}
}

// The lists a restricted account reads hold only what it may see: the library,
// the Wanted list, the root folders, the review queue, posters and a request's
// title.
func TestListsShowOnlyWhatIsInScope(t *testing.T) {
	w := newScopeWorld(t)

	ids := func(res response, key, field string) []int64 {
		t.Helper()
		var out []int64
		list, _ := res.Body[key].([]any)
		for _, row := range list {
			out = append(out, int64(row.(map[string]any)[field].(float64)))
		}
		slices.Sort(out)
		return out
	}

	if got := ids(w.kid.get("/api/v1/media"), "items", "id"); !slices.Equal(got, []int64{w.shown, w.shownSeries}) {
		t.Errorf("the library shows %v, want %v", got, []int64{w.shown, w.shownSeries})
	}
	if got := ids(w.admin.get("/api/v1/media"), "items", "id"); len(got) != 7 {
		t.Errorf("the administrator's library shows %v", got)
	}
	wanted := w.kid.get("/api/v1/wanted")
	for _, row := range wanted.Body["wanted"].([]any) {
		if int64(row.(map[string]any)["item_id"].(float64)) != w.shownSeries {
			t.Errorf("the Wanted list holds %v", row)
		}
	}
	if n := len(wanted.Body["wanted"].([]any)); n != 2 {
		t.Errorf("%d episodes wanted, want Bluey's two", n)
	}
	if got := ids(w.kid.get("/api/v1/rootfolders"), "root_folders", "id"); !slices.Equal(got, []int64{w.kids, w.kidsTV}) {
		t.Errorf("root folders %v, want the two granted", got)
	}
	if got := ids(w.kid.get("/api/v1/identify/pending"), "items", "item_id"); !slices.Equal(got, []int64{w.shown}) {
		t.Errorf("the review queue holds %v", got)
	}

	// A User — no library.edit — sees a poster only for a title in scope.
	viewer, _ := w.scopedAccount("viewer", authz.RoleUser, []int64{w.kids}, 0)
	poster := func(c *client, tmdb int64) int {
		return c.get("/api/v1/artwork/poster/tmdb/" + strconv.FormatInt(tmdb, 10)).Code
	}
	if code := poster(viewer, w.shownTMDB); code != http.StatusOK {
		t.Errorf("a poster in scope: %d", code)
	}
	if code := poster(viewer, w.hiddenTMDB); code != http.StatusNotFound {
		t.Errorf("a poster out of scope: %d", code)
	}
	if code := poster(w.admin, w.hiddenTMDB); code != http.StatusOK {
		t.Errorf("the administrator's poster: %d", code)
	}

	// An approved request cannot be linked to a hidden title; it answers as a
	// missing one does.
	res := w.kid.post("/api/v1/requests", map[string]any{"kind": "movie", "title": "Heat", "year": 1995})
	if res.Code/100 != 2 {
		t.Fatalf("request: %d %s", res.Code, res.Raw)
	}
	reqID := strconv.FormatInt(int64(res.Body["id"].(float64)), 10)
	if res.Body["state"] != "approved" { // a Manager's own are approved on submission
		if res := w.admin.post("/api/v1/requests/"+reqID+"/approve", map[string]any{}); res.Code != http.StatusOK {
			t.Fatalf("approve: %d %s", res.Code, res.Raw)
		}
	}
	hidden := w.kid.post("/api/v1/requests/"+reqID+"/item", map[string]any{"media_item_id": w.hiddenByRoot})
	missing := w.kid.post("/api/v1/requests/"+reqID+"/item", map[string]any{"media_item_id": 987654})
	if hidden.Code != http.StatusUnprocessableEntity || hidden.Code != missing.Code ||
		strings.Contains(hidden.Raw, "Heat") {
		t.Errorf("linking to a hidden title: %d %s; to a missing one: %d", hidden.Code, hidden.Raw, missing.Code)
	}
}

// An administrator changes what an existing account sees; it applies on the
// account's next request, and it is audited. A restricted approver cannot give
// anybody more than they have.
func TestAnAccountsAccessIsChangedAndAudited(t *testing.T) {
	w := newScopeWorld(t)
	path := "/api/v1/admin/users/" + strconv.FormatInt(w.kidID, 10) + "/access"
	sees := func(item int64) bool {
		return w.kid.get("/api/v1/media/"+strconv.FormatInt(item, 10)).Code == http.StatusOK
	}
	if sees(w.hiddenByRoot) {
		t.Fatal("the account saw the Films root before anything changed")
	}

	res := w.admin.do(http.MethodPut, path, map[string]any{"all_libraries": true, "rating_ceiling": 0})
	if res.Code != http.StatusOK {
		t.Fatalf("every library: %d %s", res.Code, res.Raw)
	}
	if !sees(w.hiddenByRoot) || !sees(w.unrated) || !sees(w.hiddenSeries) {
		t.Error("an account given every library, no ceiling, still does not see everything")
	}

	res = w.admin.do(http.MethodPut, path, map[string]any{"all_libraries": false,
		"library_ids": []int64{w.films}, "rating_ceiling": 3})
	if res.Code != http.StatusOK {
		t.Fatalf("films only: %d %s", res.Code, res.Raw)
	}
	if !sees(w.hiddenByRoot) || sees(w.shown) || sees(w.hiddenByRating) {
		t.Error("an account restricted to Films sees the wrong titles")
	}
	users := w.admin.get("/api/v1/admin/users")
	for _, u := range users.Body["users"].([]any) {
		m := u.(map[string]any)
		if m["username"] == "kid" && (m["all_libraries"] != false || m["rating_ceiling"] != float64(3) ||
			len(m["library_ids"].([]any)) != 1) {
			t.Errorf("the user list says %v", m)
		}
	}

	for _, bad := range []map[string]any{
		{"library_ids": []int64{w.films}},                    // all_libraries missing
		{"all_libraries": false, "library_ids": []int64{}},   // nothing
		{"all_libraries": false, "library_ids": []int64{99}}, // no such root
		{"all_libraries": true, "rating_ceiling": 6},         // no such rank
	} {
		if res := w.admin.do(http.MethodPut, path, bad); res.Code != http.StatusBadRequest {
			t.Errorf("%v: %d %s, want 400", bad, res.Code, res.Raw)
		}
	}
	if res := w.kid.do(http.MethodPut, path, map[string]any{"all_libraries": true}); res.Code != http.StatusNotFound {
		t.Errorf("a Manager reached the administrator's route: %d", res.Code)
	}
	if res := w.admin.do(http.MethodPut, "/api/v1/admin/users/1/access",
		map[string]any{"all_libraries": false, "library_ids": []int64{w.kids}}); res.Code/100 == 2 {
		t.Errorf("an administrator restricted themselves: %d", res.Code)
	}

	rows, err := w.r.database.QueryContext(t.Context(),
		`SELECT detail FROM audit_event WHERE action = 'user.grants.changed' AND outcome = 'success' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for rows.Next() {
		var d string
		_ = rows.Scan(&d)
		lines = append(lines, d)
	}
	_ = rows.Close()
	if len(lines) != 2 || !strings.Contains(lines[0], "every library") || !strings.Contains(lines[1], "rated up to rank 3") {
		t.Errorf("audit lines %q", lines)
	}

	// A restricted approver cannot approve an account wider than itself.
	w.admin.do(http.MethodPut, path, map[string]any{"all_libraries": false,
		"library_ids": []int64{w.kids}, "rating_ceiling": 2})
	if res := w.kid.post("/api/v1/invites", map[string]any{
		"role_id": w.r.roleID(authz.RoleUser), "all_libraries": true, "rating_ceiling": 0,
		"auto_approve": true, "ttl_hours": 24}); res.Code/100 == 2 {
		t.Errorf("a restricted Manager invited an unrestricted account: %d", res.Code)
	}
	if res := w.kid.post("/api/v1/invites", map[string]any{
		"role_id": w.r.roleID(authz.RoleUser), "library_ids": []int64{w.kids}, "rating_ceiling": 1,
		"auto_approve": true, "ttl_hours": 24}); res.Code != http.StatusCreated {
		t.Errorf("a restricted Manager could not invite within their own scope: %d %s", res.Code, res.Raw)
	}

	// And approving a request is held to the same rule.
	c := w.r.client()
	c.visitPage("/signup")
	if res := c.post("/api/v1/auth/signup", map[string]any{
		"username": "cousin", "email": "cousin@example.com", "password": "a-long-passphrase-7",
	}); res.Code/100 != 2 {
		t.Fatalf("signup: %d %s", res.Code, res.Raw)
	}
	pending := w.kid.get("/api/v1/accounts/requests")
	reqs, _ := pending.Body["requests"].([]any)
	if len(reqs) != 1 {
		t.Fatalf("pending requests: %s", pending.Raw)
	}
	approve := "/api/v1/accounts/requests/" + strconv.FormatInt(int64(reqs[0].(map[string]any)["id"].(float64)), 10) + "/approve"
	if res := w.kid.post(approve, map[string]any{"role_id": w.r.roleID(authz.RoleUser),
		"all_libraries": true, "rating_ceiling": 0}); res.Code/100 == 2 {
		t.Errorf("a restricted Manager approved an unrestricted account: %d", res.Code)
	}
	if res := w.kid.post(approve, map[string]any{"role_id": w.r.roleID(authz.RoleUser),
		"library_ids": []int64{w.films}, "rating_ceiling": 1}); res.Code/100 == 2 {
		t.Errorf("a restricted Manager granted a root it cannot see: %d", res.Code)
	}
	if res := w.kid.post(approve, map[string]any{"role_id": w.r.roleID(authz.RoleUser),
		"library_ids": []int64{w.kids}, "rating_ceiling": 1}); res.Code != http.StatusCreated {
		t.Errorf("a restricted Manager could not approve within their own scope: %d %s", res.Code, res.Raw)
	}
}

// A person rates a title by hand; it is audited, it decides who sees the
// title, and it can be given back to the provider.
func TestARatingIsSetByAPerson(t *testing.T) {
	w := newScopeWorld(t)
	path := "/api/v1/media/" + strconv.FormatInt(w.unrated, 10) + "/rating"
	if res := w.kid.get("/api/v1/media/" + strconv.FormatInt(w.unrated, 10)); res.Code != http.StatusNotFound {
		t.Fatalf("an unrated title was visible under a ceiling: %d", res.Code)
	}

	res := w.admin.do(http.MethodPut, path, map[string]any{"certification": "g"})
	if res.Code != http.StatusOK {
		t.Fatalf("rating: %d %s", res.Code, res.Raw)
	}
	got := w.admin.get("/api/v1/media/" + strconv.FormatInt(w.unrated, 10))
	rating, _ := got.Body["rating"].(map[string]any)
	if rating["certification"] != "G" || rating["source"] != "person" || rating["rank"] != float64(1) {
		t.Errorf("the title reads back %v", got.Body["rating"])
	}
	if res := w.kid.get("/api/v1/media/" + strconv.FormatInt(w.unrated, 10)); res.Code != http.StatusOK {
		t.Errorf("rated G, still hidden: %d", res.Code)
	}

	if res := w.admin.do(http.MethodPut, path, map[string]any{"certification": "15"}); res.Code != http.StatusBadRequest {
		t.Errorf("an unknown rating: %d", res.Code)
	}
	res = w.admin.do(http.MethodPut, path, map[string]any{"certification": nil})
	if res.Code != http.StatusOK || res.Body["rating"].(map[string]any)["rated"] != false {
		t.Errorf("given back: %d %s", res.Code, res.Raw)
	}
	if res := w.kid.get("/api/v1/media/" + strconv.FormatInt(w.unrated, 10)); res.Code != http.StatusNotFound {
		t.Errorf("unrated again, still visible: %d", res.Code)
	}

	viewer, _ := w.scopedAccount("viewer", authz.RoleUser, []int64{w.kids}, 0)
	if res := viewer.do(http.MethodPut, "/api/v1/media/"+strconv.FormatInt(w.shown, 10)+"/rating",
		map[string]any{"certification": "G"}); res.Code != http.StatusForbidden {
		t.Errorf("a user without library.edit rated a title: %d", res.Code)
	}

	rows, err := w.r.database.QueryContext(t.Context(),
		`SELECT detail FROM audit_event WHERE action = 'media.rating.changed' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for rows.Next() {
		var d string
		_ = rows.Scan(&d)
		lines = append(lines, d)
	}
	_ = rows.Close()
	if len(lines) != 2 || !strings.Contains(lines[0], "unrated → G (by hand)") ||
		!strings.Contains(lines[1], "G (by hand) → unrated") {
		t.Errorf("audit lines %q", lines)
	}
}

// ADR-0061 through the router: a series' new seasons switched off, said so
// by its seasons; a film is no series; the body must say which.
func TestASeriesNewSeasonsAreSwitchedThroughTheRouter(t *testing.T) {
	w := newScopeWorld(t)
	path := "/api/v1/media/" + strconv.FormatInt(w.shownSeries, 10)
	if res := w.admin.get(path + "/children"); res.Body["follow_new_seasons"] != true {
		t.Fatalf("by default: %v", res.Body["follow_new_seasons"])
	}
	res := w.admin.do(http.MethodPut, path+"/new-seasons", map[string]any{"follow": false})
	if res.Code != http.StatusOK || res.Body["follow_new_seasons"] != false {
		t.Fatalf("switching off: %d %s", res.Code, res.Raw)
	}
	if res := w.admin.get(path + "/children"); res.Body["follow_new_seasons"] != false {
		t.Errorf("after: %v", res.Body["follow_new_seasons"])
	}
	if res := w.admin.do(http.MethodPut, path+"/new-seasons", map[string]any{}); res.Code != http.StatusBadRequest {
		t.Errorf("no answer: %d", res.Code)
	}
	if res := w.admin.do(http.MethodPut, "/api/v1/media/"+strconv.FormatInt(w.shown, 10)+"/new-seasons",
		map[string]any{"follow": false}); res.Code != http.StatusNotFound {
		t.Errorf("a film: %d %s", res.Code, res.Raw)
	}

	// Season folders (ADR-0063): on by default, switched off on its own.
	if res := w.admin.get(path + "/children"); res.Body["season_folders"] != true {
		t.Errorf("season folders by default: %v", res.Body["season_folders"])
	}
	res = w.admin.do(http.MethodPut, path+"/season-folders", map[string]any{"season_folders": false})
	if res.Code != http.StatusOK || res.Body["season_folders"] != false || !strings.Contains(res.Raw, "No file already") {
		t.Fatalf("season folders off: %d %s", res.Code, res.Raw)
	}
	if res := w.admin.get(path + "/children"); res.Body["season_folders"] != false || res.Body["follow_new_seasons"] != false {
		t.Errorf("after: %v", res.Body)
	}
	if res := w.admin.do(http.MethodPut, path+"/season-folders", map[string]any{"follow": true}); res.Code != http.StatusBadRequest {
		t.Errorf("the other switch's field: %d", res.Code)
	}

	// Daily (ADR-0064): off by default, switched on.
	if res := w.admin.get(path + "/children"); res.Body["daily"] != false {
		t.Errorf("daily by default: %v", res.Body["daily"])
	}
	if res := w.admin.do(http.MethodPut, path+"/daily", map[string]any{"daily": true}); res.Code != http.StatusOK ||
		res.Body["daily"] != true || !strings.Contains(res.Raw, "by the day they aired") {
		t.Fatalf("daily on: %d %s", res.Code, res.Raw)
	}
	if res := w.admin.get(path + "/children"); res.Body["daily"] != true {
		t.Errorf("after: %v", res.Body["daily"])
	}
}

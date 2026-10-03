package api

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// The routes still answering 501, each waiting on a named record. A route may
// leave this list only by being built or removed with a record saying so
// (ADR-0038 removed four and built three; ADR-0039 built five; ADR-0040 four; ADR-0041 two; ADR-0042 one; ADR-0043 the last); a new stub may not appear without
// being added here, where somebody reading the diff will see it.
func TestNoRouteIsLeftUnbuiltWithoutARecord(t *testing.T) {
	w := newScopeWorld(t)
	want := []string{}
	var got []string
	for _, route := range w.r.rt.Routes() {
		if strings.HasPrefix(route.Pattern, "/api/v1/play/") {
			t.Errorf("%s %s is back; ADR-0038 removed the play-session routes", route.Method, route.Pattern)
		}
		if route.Stub {
			got = append(got, route.Method+" "+route.Pattern)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("unbuilt routes:\n got  %q\n want %q", got, want)
	}
}

// ADR-0038, decision 2.
func TestTheLibraryIsSearchedByTitle(t *testing.T) {
	w := newScopeWorld(t)
	if _, err := w.r.database.ExecContext(t.Context(), `
		INSERT INTO media_item (kind, title, sort_title, root_folder_id, folder, added_at, updated_at)
		VALUES ('movie', 'Pokémon: The First Movie', 'pokemon the first movie', ?, 'Pokemon', 'x', 'x'),
		       ('movie', 'Detective Pikachu, a Pokémon Story', 'detective pikachu', ?, 'Pikachu', 'x', 'x')`,
		w.films, w.films); err != nil {
		t.Fatal(err)
	}
	titles := func(c *client, q string) []string {
		t.Helper()
		res := c.get("/api/v1/search?q=" + q)
		if res.Code != http.StatusOK {
			t.Fatalf("search %q: %d %s", q, res.Code, res.Raw)
		}
		var out []string
		for _, it := range res.Body["items"].([]any) {
			out = append(out, it.(map[string]any)["title"].(string))
		}
		return out
	}
	// Accents and case folded; a title starting with the query comes first.
	if got := titles(w.admin, "POKEMON"); !slices.Equal(got,
		[]string{"Pokémon: The First Movie", "Detective Pikachu, a Pokémon Story"}) {
		t.Errorf("pokemon: %q", got)
	}
	// Every word, each the start of a word in the title.
	if got := titles(w.admin, "first%20pok"); !slices.Equal(got, []string{"Pokémon: The First Movie"}) {
		t.Errorf("first pok: %q", got)
	}
	if got := titles(w.admin, "okemon"); len(got) != 0 {
		t.Errorf("the middle of a word matched: %q", got)
	}
	// Scoped: the restricted account finds nothing in Films.
	if got := titles(w.kid, "heat"); len(got) != 0 {
		t.Errorf("a title out of scope was found: %q", got)
	}
	if got := titles(w.kid, "paddington"); !slices.Equal(got, []string{"Paddington"}) {
		t.Errorf("a title in scope: %q", got)
	}
	for _, q := range []string{"", "a", strings.Repeat("x", 101), "%3A%3A"} {
		if res := w.admin.get("/api/v1/search?q=" + q); res.Code != http.StatusBadRequest {
			t.Errorf("q=%q: %d, want 400", q, res.Code)
		}
	}
}

// ADR-0038, decision 3.
func TestATitlesArtworkIsItsPoster(t *testing.T) {
	w := newScopeWorld(t)
	viewer, _ := w.scopedAccount("viewer", authz.RoleUser, []int64{w.kids}, 0)
	art := func(c *client, id int64) response {
		return c.get("/api/v1/media/" + strconv.FormatInt(id, 10) + "/artwork")
	}
	if res := art(viewer, w.shown); res.Code != http.StatusOK || !strings.Contains(res.Raw, "poster") ||
		res.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("a title in scope: %d %q %s", res.Code, res.Header.Get("Content-Type"), res.Raw)
	}
	if res := art(viewer, w.hiddenByRoot); res.Code != http.StatusNotFound {
		t.Errorf("a title out of scope: %d", res.Code)
	}
	if res := art(w.admin, w.hiddenByRoot); res.Code != http.StatusOK {
		t.Errorf("the administrator: %d", res.Code)
	}
	if res := art(w.admin, w.unrated); res.Code != http.StatusNotFound {
		t.Errorf("a title with no cached poster: %d", res.Code)
	}
	got := w.admin.get("/api/v1/media/" + strconv.FormatInt(w.shown, 10))
	if got.Body["poster"] != "/api/v1/media/"+strconv.FormatInt(w.shown, 10)+"/artwork" {
		t.Errorf("a title's poster is %v", got.Body["poster"])
	}
}

// ADR-0038, decision 4.
func TestAnOriginalIsDownloadedAndAudited(t *testing.T) {
	w := newScopeWorld(t)
	file := filepath.Join(w.dirs[w.kids], "Paddington", "Paddington.mkv")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("the film itself"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/media/" + strconv.FormatInt(w.shown, 10) + "/original"

	res := w.kid.get(path)
	if res.Code != http.StatusOK || res.Raw != "the film itself" ||
		res.Header.Get("Content-Disposition") != "attachment; filename*=UTF-8''Paddington.mkv" {
		t.Fatalf("download: %d %q %q", res.Code, res.Header.Get("Content-Disposition"), res.Raw)
	}
	if res := w.kid.get("/api/v1/media/" + strconv.FormatInt(w.hiddenByRoot, 10) + "/original"); res.Code != http.StatusNotFound {
		t.Errorf("a title out of scope: %d", res.Code)
	}

	// Two files: the title's files must be named.
	if _, err := w.r.database.ExecContext(t.Context(), `
		INSERT INTO media_file (item_id, root_folder_id, relative_path, size_bytes, quality, revision,
		                        release_title, imported_at)
		VALUES (?, ?, 'Paddington/Paddington.extended.mkv', 1, 'WEBDL-1080p', 0, 'x', 'x')`,
		w.shown, w.kids); err != nil {
		t.Fatal(err)
	}
	if res := w.kid.get(path); res.Code != http.StatusConflict || len(res.Body["files"].([]any)) != 2 {
		t.Errorf("several files, none named: %d %s", res.Code, res.Raw)
	}
	if res := w.kid.get(path + "?file=" + strconv.FormatInt(w.shownFile, 10)); res.Raw != "the film itself" {
		t.Errorf("a named file: %d %s", res.Code, res.Raw)
	}
	if res := w.kid.get(path + "?file=" + strconv.FormatInt(w.hiddenFile, 10)); res.Code != http.StatusNotFound {
		t.Errorf("another title's file: %d", res.Code)
	}

	viewer, _ := w.scopedAccount("viewer", authz.RoleUser, []int64{w.kids}, 0)
	if res := viewer.get(path); res.Code != http.StatusForbidden {
		t.Errorf("a User without media.download_original: %d", res.Code)
	}

	var n int
	if err := w.r.database.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM audit_event WHERE action = 'media.downloaded' AND detail LIKE 'Paddington%Paddington.mkv'`).
		Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("%d audit lines for two downloads", n)
	}
}

package library

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

func scopedCtx(roots []int64, ceiling int) context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 7, Username: "kid", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 3, Name: "User", Rank: 10,
			Permissions: authz.NewPermissionSet(authz.PermLogin, authz.PermBrowse, authz.PermEditLibraryItems)},
		LibraryIDs: roots, RatingCeiling: ceiling,
	})
}

// ADR-0037, decisions 3 and 4: the scope is a WHERE clause, so a root not
// granted and a title rated above the ceiling are never read — and an unrated
// title is above every ceiling.
func TestTheScopeIsAppliedInSQL(t *testing.T) {
	r := newEpRig(t) // root 1 holds Severance (item 1), unrated
	now := testNow.Format(episodeTimeLayout)
	if _, err := r.db.ExecContext(t.Context(), `
		INSERT INTO root_folder (id, path, kind, label, created_at, updated_at)
		VALUES (2, '/media/kids', 'series', 'Kids', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	add := func(root int64, title, cert string) int64 {
		var rank any
		var certV any
		if RatingRank(cert) > 0 {
			rank, certV = RatingRank(cert), cert
		}
		res, err := r.db.ExecContext(t.Context(), `
			INSERT INTO media_item (kind, title, sort_title, root_folder_id, folder, tmdb_id,
			                        certification, rating_rank, added_at, updated_at)
			VALUES ('series', ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
			title, strings.ToLower(title), root, title, certV, rank, now, now)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	bluey := add(2, "Bluey", "TV-Y")
	gravity := add(2, "Gravity Falls", "TV-Y7")
	adventure := add(2, "Adventure Time", "TV-PG")
	unrated := add(2, "Home Video", "")
	grownUp := add(1, "Succession", "TV-MA")

	visible := func(ctx context.Context) []int64 {
		t.Helper()
		clause, args := Visible(ctx, "i")
		rows, err := r.db.QueryContext(ctx, `SELECT i.id FROM media_item i WHERE `+clause+` ORDER BY i.id`, args...)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var out []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	all := []int64{r.itemID, bluey, gravity, adventure, unrated, grownUp}
	if got := visible(browseCtx()); !slices.Equal(got, all) {
		t.Errorf("an unrestricted account sees %v, want %v", got, all)
	}
	if got := visible(authz.SystemPrincipal(t.Context(), authz.TaskImport)); !slices.Equal(got, all) {
		t.Errorf("background work sees %v", got)
	}
	if got := visible(context.Background()); len(got) != 0 {
		t.Errorf("nobody sees %v", got)
	}
	if got := visible(scopedCtx([]int64{2}, 0)); !slices.Equal(got, []int64{bluey, gravity, adventure, unrated}) {
		t.Errorf("the Kids root with no ceiling sees %v", got)
	}
	if got := visible(scopedCtx([]int64{2}, 2)); !slices.Equal(got, []int64{bluey, gravity, adventure}) {
		t.Errorf("the Kids root up to TV-PG sees %v — the unrated title must be hidden", got)
	}
	if got := visible(scopedCtx([]int64{1, 2}, 1)); !slices.Equal(got, []int64{bluey}) {
		t.Errorf("both roots up to rank 1 sees %v", got)
	}

	// Every library, with a ceiling.
	capped := authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 8, Username: "teen", State: authz.StateActive, MFASatisfied: true,
		UnrestrictedLibraries: true, RatingCeiling: 3,
		Role: authz.Role{Name: "User", Permissions: authz.NewPermissionSet(authz.PermBrowse)}})
	if got := visible(capped); !slices.Equal(got, []int64{bluey, gravity, adventure}) {
		t.Errorf("every library up to PG-13 sees %v", got)
	}

	// Through the store: a hidden series has no seasons to read, and cannot be
	// searched for, monitored or listed as wanted.
	if err := r.store.Upsert(r.ctx, r.itemID, []SeasonInput{season1(2)}); err != nil {
		t.Fatal(err)
	}
	kid := scopedCtx([]int64{2}, 0)
	if _, _, err := r.store.Seasons(kid, r.itemID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a hidden series' seasons: %v", err)
	}
	if err := r.store.SetSeasonMonitored(kid, r.itemID, 1, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("a hidden series' season was monitored: %v", err)
	}
	_, byNumber, _ := r.store.Seasons(r.ctx, r.itemID)
	if err := r.store.SetEpisodeMonitored(kid, byNumber[1][0].ID, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("a hidden episode was monitored: %v", err)
	}
	if _, err := r.store.ForSearch(kid, byNumber[1][0].ID); !errors.Is(err, ErrNoSuchEpisode) {
		t.Errorf("a hidden episode was searched for: %v", err)
	}
	if _, err := r.store.ForSeasonSearch(kid, r.itemID, 1); !errors.Is(err, ErrNoSuchSeason) {
		t.Errorf("a hidden season was searched for: %v", err)
	}
	if w, err := r.store.Wanted(kid, 100); err != nil || len(w) != 0 {
		t.Errorf("the hidden series' episodes are wanted: %d, %v", len(w), err)
	}
	if w, _ := r.store.Wanted(r.ctx, 100); len(w) != 2 {
		t.Errorf("an unrestricted account wants %d episodes, want 2", len(w))
	}
}

// ADR-0037, decision 3.
func TestCertificationsRank(t *testing.T) {
	for cert, want := range map[string]int{
		"G": 1, "TV-Y": 1, "TV-G": 1,
		"PG": 2, "TV-Y7": 2, "TV-Y7-FV": 2, "TV-PG": 2,
		"PG-13": 3, "TV-14": 3,
		"R": 4, "TV-MA": 4,
		"NC-17":   5,
		" pg-13 ": 3,
		"NR":      0, "": 0, "12A": 0, "Unrated": 0,
	} {
		if got := RatingRank(cert); got != want {
			t.Errorf("RatingRank(%q) = %d, want %d", cert, got, want)
		}
	}
	if got := CanonicalCertification(" tv-ma"); got != "TV-MA" {
		t.Errorf("canonical: %q", got)
	}
	if got := CanonicalCertification("15"); got != "" {
		t.Errorf("an unknown rating is canonical: %q", got)
	}
	for rank := 0; rank <= authz.MaxRatingRank; rank++ {
		if CeilingLabels[rank] == "" {
			t.Errorf("ceiling %d has no label", rank)
		}
	}
}

// ADR-0037, decision 4: every function that reads media_item in SQL either
// applies the scope (Visible) or says, in a comment inside it, why it is
// unscoped. A new read that does neither fails here — the way a title a
// restricted account should not see would otherwise reach it.
//
// The importer reads items through queryItems/queryFiles, whose scope argument
// the compiler already forces; those two functions are the ones allowed to
// hold the SQL themselves.
func TestEveryItemReadChoosesAScope(t *testing.T) {
	reads := regexp.MustCompile(`(?is)\b(FROM|JOIN)\s+media_item\b`)
	chose := regexp.MustCompile(`Visible\(|visibleTo\(|everything|// Unscoped:`)
	allowed := map[string]bool{
		"importer.queryItemsOn": true, "importer.queryFiles": true,
		// Writes that read media_item only to decide their own conditions.
		"importer.AddItem": true,
	}

	root := filepath.Join("..")
	fset := token.NewFileSet()
	checked := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			readsItems := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil && reads.MatchString(s) {
						readsItems = true
					}
				}
				return true
			})
			if !readsItems {
				continue
			}
			checked++
			name := f.Name.Name + "." + fn.Name.Name
			if allowed[name] {
				continue
			}
			body := string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
			if !chose.MatchString(body) {
				t.Errorf("%s (%s) reads media_item without Visible or an \"// Unscoped:\" reason",
					name, fset.Position(fn.Pos()))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 10 {
		t.Errorf("only %d functions read media_item — the walk is not finding them", checked)
	}
}

// ADR-0044, decision 4: a ceiling governs films and series, which carry a
// certification; music and books are governed by the libraries granted.
func TestACeilingIsForFilmsAndSeries(t *testing.T) {
	r := newEpRig(t)
	now := testNow.Format(episodeTimeLayout)
	add := func(kind, title string) int64 {
		res, err := r.db.ExecContext(t.Context(), `
			INSERT INTO media_item (kind, title, sort_title, root_folder_id, folder, added_at, updated_at)
			VALUES (?, ?, ?, 1, ?, ?, ?)`, kind, title, title, title, now, now)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	artist := add("artist", "Raffi")
	book := add("book", "Matilda")
	film := add("movie", "Unrated film")
	clause, args := ScopeClause(authz.Scope{AllLibraries: true, RatingCeiling: 1}, "")
	rows, err := r.db.QueryContext(t.Context(), `SELECT id FROM media_item WHERE `+clause+` ORDER BY id`, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []int64
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []int64{artist, book}) {
		t.Errorf("under a ceiling %v are visible, want the artist and the book only (not film %d)", got, film)
	}
}

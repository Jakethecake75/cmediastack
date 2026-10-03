package follow

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identify"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/metadata"
)

// Adding a film before it is on disk (ADR-0026).

// filmsRoot gives the rig a root folder for films, which it does not start
// with: a series-only instance must refuse a film for want of one.
func (r *rig) filmsRoot() library.RootFolder {
	r.t.Helper()
	dir := filepath.Join(r.base, "media", "films")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.t.Fatal(err)
	}
	rf, err := r.roots.Create(r.ctx, dir, library.KindMovies, "Films")
	if err != nil {
		r.t.Fatal(err)
	}
	return rf
}

func addDune() Request { return Request{Kind: "movie", TMDBID: 438631} }

func TestAddingAFilmRecordsItAsChosenAndWantsIt(t *testing.T) {
	r := newRig(t)
	films := r.filmsRoot()
	res, err := r.svc.Add(r.ctx, addDune())
	if err != nil {
		t.Fatal(err)
	}

	// Named by the provider's answer, in a films root, with no monitoring.
	it := res.Item
	if it.Kind != importer.KindMovie || it.Title != "Dune" || it.Year != 2021 || it.TMDBID != 438631 ||
		it.IMDbID != "tt1160419" || it.Folder != "Dune (2021)" || it.RootFolderID != films.ID {
		t.Errorf("added %+v", it)
	}
	if res.Root.ID != films.ID || res.Monitor != "" || res.Seasons != 0 || res.Episodes != 0 {
		t.Errorf("result = %+v; a film has no monitoring and no episodes", res)
	}

	// One request, for a FILM's details — no season was asked for.
	r.provider.mu.Lock()
	kinds := append([]metadata.Kind(nil), r.provider.kinds...)
	r.provider.mu.Unlock()
	if r.provider.requests() != 1 || len(kinds) != 1 || kinds[0] != metadata.KindMovie {
		t.Errorf("%d request(s), asking about %v; want one, about a film", r.provider.requests(), kinds)
	}
	if n := r.count(`SELECT COUNT(*) FROM season`) + r.count(`SELECT COUNT(*) FROM episode`); n != 0 {
		t.Errorf("%d seasons and episodes written for a film", n)
	}

	// The person's identification, as for a series.
	id, err := r.idents.Get(r.ctx, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if id.State != identify.StateConfirmed || id.DecidedBy == nil || *id.DecidedBy != r.userID ||
		id.ProviderID != 438631 {
		t.Errorf("identification = %s by %v, provider %d", id.State, id.DecidedBy, id.ProviderID)
	}

	// Adding it is wanting it.
	wanted, err := r.items.WantedFilms(r.ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(wanted) != 1 || wanted[0].ID != it.ID {
		t.Errorf("wanted films = %+v, want Dune", wanted)
	}

	// Nothing on disk.
	if entries, _ := os.ReadDir(films.Path); len(entries) != 0 {
		t.Errorf("the films root holds %d entries after an add", len(entries))
	}

	// And the audit line says what was added, as a film.
	var detail string
	if err := r.db.QueryRow(`SELECT detail FROM audit_event WHERE action = 'media.added'
		AND actor_user_id = ? AND target_id = ?`, r.userID, it.ID).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "added the film Dune (2021) (tmdb 438631)") || strings.Contains(detail, "monitoring") {
		t.Errorf("audit detail = %q", detail)
	}
}

// Twice is refused without asking the provider, as for a series — and a film
// and a series may carry the same provider id, because TMDB numbers them
// separately.
func TestAddingTheSameFilmTwiceAsksTheProviderNothing(t *testing.T) {
	r := newRig(t)
	r.filmsRoot()
	first, err := r.svc.Add(r.ctx, addDune())
	if err != nil {
		t.Fatal(err)
	}
	asked := r.provider.requests()
	again := addDune()
	again.Folder = "Dune, again"
	_, err = r.svc.Add(r.ctx, again)
	var conflict *importer.ConflictError
	if !errors.As(err, &conflict) || !errors.Is(err, importer.ErrAlreadyInLibrary) ||
		conflict.Existing.ID != first.Item.ID {
		t.Fatalf("err = %v, want ErrAlreadyInLibrary naming item %d", err, first.Item.ID)
	}
	if n := r.provider.requests() - asked; n != 0 {
		t.Errorf("a duplicate cost %d provider requests", n)
	}

	// The same number as a series is somebody else.
	r.provider.details.ProviderID = 438631
	if _, err := r.svc.Add(r.ctx, Request{Kind: "series", TMDBID: 438631, Monitor: "none"}); err != nil {
		t.Errorf("a series sharing the film's provider id was refused: %v", err)
	}
}

// The films root is chosen as the series root is: the only one, or the
// operator's choice among several.
func TestAFilmsRootIsChosenOnlyWhenThereIsNoChoice(t *testing.T) {
	r := newRig(t)
	r.filmsRoot()
	dir := filepath.Join(r.base, "media", "more-films")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := r.roots.Create(r.ctx, dir, library.KindMovies, "More films")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Add(r.ctx, addDune()); !errors.Is(err, ErrChooseRootFolder) {
		t.Fatalf("two films roots, none chosen: err = %v, want ErrChooseRootFolder", err)
	}
	req := addDune()
	req.RootFolderID = second.ID
	res, err := r.svc.Add(r.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Item.RootFolderID != second.ID {
		t.Errorf("added to root %d, want the chosen %d", res.Item.RootFolderID, second.ID)
	}
}

// A film the provider does not have, or a provider that fails, adds nothing.
func TestAFilmTheProviderCannotAnswerForAddsNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		id   int64
		err  error
		want error
	}{
		"no such film": {id: 999999999, want: metadata.ErrNotFound},
		"rate-limited": {id: 438631, err: metadata.ErrRateLimited, want: metadata.ErrRateLimited},
		"unavailable":  {id: 438631, err: metadata.ErrUnavailable, want: metadata.ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.filmsRoot()
			r.provider.detailsErr = tc.err
			req := addDune()
			req.TMDBID = tc.id
			if _, err := r.svc.Add(r.ctx, req); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if n := r.count(`SELECT COUNT(*) FROM media_item`); n != 0 {
				t.Errorf("%d items", n)
			}
			r.nothingWritten()
		})
	}
}

// Adding a film needs what adding a series needs.
func TestAddingAFilmNeedsThePermissionToEditTheLibrary(t *testing.T) {
	r := newRig(t)
	r.filmsRoot()
	viewer := authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: r.userID, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 3, Name: authz.RoleUser, Rank: 10,
			Permissions: authz.NewPermissionSet(authz.PermLogin, authz.PermBrowse, authz.PermStream)},
	})
	if _, err := r.svc.Add(viewer, addDune()); !authz.IsDenied(err) {
		t.Fatalf("err = %v, want a denial", err)
	}
	if n := r.provider.requests(); n != 0 {
		t.Errorf("a refused add cost %d provider requests", n)
	}
	r.nothingWritten()
}

// The whole of ADR-0026 below the API: an added film's grab, sealed for it,
// lands in the folder the add named — though the release calls it something
// else — and the film leaves the wanted list.
func TestAnAddedFilmTakesItsGrabIntoTheFolderItNamed(t *testing.T) {
	r := newRig(t)
	films := r.filmsRoot()
	res, err := r.svc.Add(r.ctx, addDune())
	if err != nil {
		t.Fatal(err)
	}

	hash := strings.Repeat("cd", 20)
	dir := filepath.Join(r.base, "downloads", hash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	release := "Dune.Part.One.2021.1080p.BluRay.x264-GRP"
	f, err := os.Create(filepath.Join(dir, release+".mkv"))
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
		InfoHash: hash, Dir: dir, ReleaseTitle: release,
		Files:  []importer.Candidate{{Path: release + ".mkv", Bytes: size}},
		Target: &importer.Target{ItemID: res.Item.ID, Film: true},
	})
	if err != nil || out.Outcome != importer.OutcomeImported {
		t.Fatalf("import: %s %q %v", out.Outcome, out.Detail, err)
	}
	want := filepath.Join(films.Path, "Dune (2021)", "Dune (2021) [Bluray-1080p].mkv")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("the film is not where the add said it lives: %v", err)
	}
	if out.Item.ID != res.Item.ID {
		t.Errorf("imported into item %d, want the added %d", out.Item.ID, res.Item.ID)
	}
	if n := r.count(`SELECT COUNT(*) FROM media_item WHERE kind = 'movie'`); n != 1 {
		t.Errorf("%d films; the release's own name made a twin", n)
	}
	if wanted, _ := r.items.WantedFilms(r.ctx, 0); len(wanted) != 0 {
		t.Errorf("still wanted after it arrived: %+v", wanted)
	}
}

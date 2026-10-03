package importer

import (
	"context"
	"errors"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
)

// The two halves of identification, and the permission wall between them.
//
// AttachIdentity records a fact ABOUT an item; Relabel changes what an operator
// browses to. The split is enforced twice over — by the signature, so the
// automatic path cannot express a rename, and by the permission, so a caller
// that got hold of the method anyway still cannot.

// browseOnly is the authority the identification pass actually runs with
// (authz.TaskIdentify).
func browseOnly() context.Context {
	return authz.SystemPrincipal(context.Background(), authz.TaskIdentify)
}

// movieRootID returns the films root the rig created.
func movieRootID(t *testing.T, r *rig) int64 {
	t.Helper()
	all, err := r.roots.List(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, rf := range all {
		if rf.Kind == library.KindMovies {
			return rf.ID
		}
	}
	t.Fatal("the rig has no films root")
	return 0
}

func TestAttachingAnIdentityNeedsOnlyBrowse(t *testing.T) {
	r := newRig(t)
	it, err := r.store.UpsertItem(r.ctx, Item{
		Kind: "movie", Title: "The Matrix", Year: 1999,
		SortTitle: "matrix, the", RootFolderID: movieRootID(t, r), Folder: "The Matrix (1999)",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := r.store.AttachIdentity(browseOnly(), it.ID, 603, "tt0133093"); err != nil {
		t.Fatalf("the identification pass cannot attach an id: %v", err)
	}

	got, err := r.store.GetItem(r.ctx, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The title is untouched, and nothing about the call could have touched it.
	if got.Title != "The Matrix" || got.Year != 1999 {
		t.Errorf("attaching an id changed the item to %q (%d)", got.Title, got.Year)
	}
}

// The wall. A caller with the pass's authority must not be able to rename
// anything, whatever it calls.
func TestRelabellingNeedsThePermissionToEditLibraryItems(t *testing.T) {
	r := newRig(t)
	it, err := r.store.UpsertItem(r.ctx, Item{
		Kind: "movie", Title: "matrix", RootFolderID: movieRootID(t, r), Folder: "matrix",
		SortTitle: "matrix",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = r.store.Relabel(browseOnly(), it.ID, "The Matrix", 1999)
	if err == nil {
		t.Fatal("the identification pass renamed a library item")
	}
	if !authz.IsDenied(err) {
		t.Errorf("err = %v, want an authorization denial", err)
	}

	got, _ := r.store.GetItem(r.ctx, it.ID)
	if got.Title != "matrix" {
		t.Errorf("the title changed to %q despite the refusal", got.Title)
	}
}

func TestRelabellingMovesTheSortTitleWithIt(t *testing.T) {
	r := newRig(t)
	it, err := r.store.UpsertItem(r.ctx, Item{
		Kind: "movie", Title: "matrix", RootFolderID: movieRootID(t, r), Folder: "matrix",
		SortTitle: "matrix",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := r.store.Relabel(r.ctx, it.ID, "The Matrix", 1999); err != nil {
		t.Fatal(err)
	}
	got, err := r.store.GetItem(r.ctx, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "The Matrix" || got.Year != 1999 {
		t.Errorf("item = %q (%d)", got.Title, got.Year)
	}
	// sort_title is stored rather than computed (migration 0008), so a rename
	// that left it behind would sort the library by a name nothing displays.
	if got.SortTitle != SortTitle("The Matrix") {
		t.Errorf("sort title = %q, want %q — the library would sort by the old name",
			got.SortTitle, SortTitle("The Matrix"))
	}
}

func TestIdentifyingSomethingThatIsNotThere(t *testing.T) {
	r := newRig(t)
	if err := r.store.AttachIdentity(browseOnly(), 4242, 1, ""); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("attach err = %v, want ErrItemNotFound", err)
	}
	if err := r.store.Relabel(r.ctx, 4242, "X", 2000); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("relabel err = %v, want ErrItemNotFound", err)
	}
}

// AdoptCanonicalTitle takes the provider's SPELLING of a title this software
// already agrees is the same one. The refusal lives in the method, so the
// background pass may call it: there is no argument it could pass that changes
// what an item is.
func TestAdoptingASpellingCannotChangeWhatSomethingIs(t *testing.T) {
	r := newRig(t)
	it, err := r.store.UpsertItem(r.ctx, Item{
		Kind: "movie", Title: "the matrix", Year: 1999, SortTitle: "the matrix",
		RootFolderID: movieRootID(t, r), Folder: "the.matrix.1999.720p.brrip",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Same title, better spelled: allowed, and allowed to the PASS.
	if err := r.store.AdoptCanonicalTitle(browseOnly(), it.ID, "The Matrix"); err != nil {
		t.Fatalf("the pass could not adopt a spelling: %v", err)
	}
	got, _ := r.store.GetItem(r.ctx, it.ID)
	if got.Title != "The Matrix" {
		t.Errorf("title = %q, want the provider's spelling", got.Title)
	}
	if got.SortTitle != SortTitle("The Matrix") {
		t.Errorf("sort title = %q", got.SortTitle)
	}

	// A different title: refused, whoever asks. "Arrival" and "The Arrival" are
	// both real 2016 films, which is exactly the pair this must not merge.
	for _, ctx := range []context.Context{browseOnly(), r.ctx} {
		if err := r.store.AdoptCanonicalTitle(ctx, it.ID, "The Matrix Reloaded"); !errors.Is(err, ErrNotTheSameTitle) {
			t.Errorf("err = %v, want ErrNotTheSameTitle", err)
		}
	}
	got, _ = r.store.GetItem(r.ctx, it.ID)
	if got.Title != "The Matrix" {
		t.Errorf("a refused adoption changed the title to %q", got.Title)
	}
}

// A leading article is the case that matters most, because the normalisation
// deliberately does NOT treat it as spelling (ADR-0019).
func TestAdoptingASpellingRefusesALeadingArticle(t *testing.T) {
	r := newRig(t)
	it, err := r.store.UpsertItem(r.ctx, Item{
		Kind: "movie", Title: "Arrival", Year: 2016, SortTitle: "arrival",
		RootFolderID: movieRootID(t, r), Folder: "Arrival (2016)",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.store.AdoptCanonicalTitle(browseOnly(), it.ID, "The Arrival"); !errors.Is(err, ErrNotTheSameTitle) {
		t.Errorf("\"Arrival\" was allowed to become \"The Arrival\": %v", err)
	}
}

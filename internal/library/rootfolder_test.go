package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

func testRootStore(t *testing.T, downloadDir string) *RootStore {
	t.Helper()
	database, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "lib.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return NewRootStore(database, downloadDir, nil)
}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestARootFolderRoundTrips(t *testing.T) {
	base := t.TempDir()
	dl := mkdir(t, base, "downloads")
	movies := mkdir(t, base, "media", "movies")
	s := testRootStore(t, dl)
	ctx := adminCtx()

	rf, err := s.Create(ctx, movies, KindMovies, "Films")
	if err != nil {
		t.Fatal(err)
	}
	if rf.Kind != KindMovies || rf.Label != "Films" {
		t.Errorf("got %+v", rf)
	}
	// Both directories are in the same temp filesystem, so hardlinks work and
	// the note should say so rather than being empty.
	if !rf.HardlinksOK {
		t.Errorf("hardlinks reported unavailable within one filesystem: %q", rf.HardlinkNote)
	}
	if rf.HardlinkNote == "" {
		t.Error("no hardlink note was recorded")
	}

	got, err := s.Get(ctx, rf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != rf.Path {
		t.Errorf("path = %q, want %q", got.Path, rf.Path)
	}
}

// If root A contains root B, "which root owns this file" has two answers, and a
// delete authorised against A reaches into B.
func TestRootFoldersMayNotNest(t *testing.T) {
	base := t.TempDir()
	dl := mkdir(t, base, "downloads")
	outer := mkdir(t, base, "media")
	inner := mkdir(t, base, "media", "movies")
	s := testRootStore(t, dl)
	ctx := adminCtx()

	if _, err := s.Create(ctx, outer, KindSeries, "everything"); err != nil {
		t.Fatal(err)
	}
	// A child of an existing root.
	if _, err := s.Create(ctx, inner, KindMovies, "films"); !errors.Is(err, ErrRootNested) {
		t.Errorf("nested root accepted: %v", err)
	}

	// And the other order: a parent of an existing root.
	s2 := testRootStore(t, dl)
	if _, err := s2.Create(ctx, inner, KindMovies, "films"); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.Create(ctx, outer, KindSeries, "everything"); !errors.Is(err, ErrRootNested) {
		t.Errorf("parent of an existing root accepted: %v", err)
	}
}

// "/media/movies" is not an ancestor of "/media/movies-4k", but a string prefix
// check says it is — and would refuse a perfectly ordinary layout.
func TestSiblingRootsWithASharedPrefixAreAllowed(t *testing.T) {
	base := t.TempDir()
	dl := mkdir(t, base, "downloads")
	a := mkdir(t, base, "media", "movies")
	b := mkdir(t, base, "media", "movies-4k")
	s := testRootStore(t, dl)
	ctx := adminCtx()

	if _, err := s.Create(ctx, a, KindMovies, "HD"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, b, KindMovies, "UHD"); err != nil {
		t.Fatalf("a sibling root sharing a name prefix was refused: %v", err)
	}
}

// If the library and the download directory overlap, the seeded torrent and the
// library copy are the same path: removing one destroys the other.
func TestARootMayNotOverlapTheDownloadDirectory(t *testing.T) {
	base := t.TempDir()
	dl := mkdir(t, base, "downloads")
	inside := mkdir(t, base, "downloads", "complete")
	s := testRootStore(t, dl)
	ctx := adminCtx()

	if _, err := s.Create(ctx, inside, KindMovies, "x"); !errors.Is(err, ErrRootInvalid) {
		t.Errorf("a root inside the download directory was accepted: %v", err)
	}
	if _, err := s.Create(ctx, dl, KindMovies, "x"); !errors.Is(err, ErrRootInvalid) {
		t.Errorf("the download directory itself was accepted as a root: %v", err)
	}
	// A root that CONTAINS the download directory is the same problem.
	s2 := testRootStore(t, inside)
	if _, err := s2.Create(ctx, dl, KindMovies, "x"); !errors.Is(err, ErrRootInvalid) {
		t.Errorf("a root containing the download directory was accepted: %v", err)
	}
}

// Two rows describing the same directory by different names would defeat the
// nesting check entirely.
func TestTheSameDirectoryCannotBeAddedTwiceUnderDifferentNames(t *testing.T) {
	base := t.TempDir()
	dl := mkdir(t, base, "downloads")
	real := mkdir(t, base, "media", "movies")
	link := filepath.Join(base, "films")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	s := testRootStore(t, dl)
	ctx := adminCtx()

	if _, err := s.Create(ctx, real, KindMovies, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, link, KindMovies, "b"); !errors.Is(err, ErrRootExists) {
		t.Errorf("the same directory was added twice through a symlink: %v", err)
	}
}

func TestUnusableRootFoldersAreRefused(t *testing.T) {
	base := t.TempDir()
	dl := mkdir(t, base, "downloads")
	s := testRootStore(t, dl)
	ctx := adminCtx()

	file := filepath.Join(base, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"missing":         filepath.Join(base, "does-not-exist"),
		"a file":          file,
		"relative":        "media/movies",
		"empty":           "",
		"relative parent": "../media",
	} {
		if _, err := s.Create(ctx, path, KindMovies, "x"); !errors.Is(err, ErrRootInvalid) {
			t.Errorf("%s root accepted: %v", name, err)
		}
	}

	// An unknown kind, which the database CHECK would also refuse — but a
	// constraint violation is a worse message than a named one.
	good := mkdir(t, base, "media", "movies")
	if _, err := s.Create(ctx, good, "photos", "x"); !errors.Is(err, ErrRootInvalid) {
		t.Errorf("an unknown kind was accepted: %v", err)
	}
}

// Writability is proven by writing. Checking mode bits is not the same thing:
// the process may run as a user the bits do not describe, the mount may be
// read-only, or an ACL may disagree.
func TestAReadOnlyRootIsRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root, which can write to anything; this check is meaningless here")
	}
	base := t.TempDir()
	dl := mkdir(t, base, "downloads")
	ro := mkdir(t, base, "readonly")
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o755) })

	s := testRootStore(t, dl)
	if _, err := s.Create(adminCtx(), ro, KindMovies, "x"); !errors.Is(err, ErrRootInvalid) {
		t.Errorf("a read-only root was accepted: %v", err)
	}
}

// Configuring where a library lives is EffectMutateLibraryPaths: repointing a
// root can orphan an entire library without deleting a byte.
func TestOnlyAnAdminMayConfigureRootFolders(t *testing.T) {
	base := t.TempDir()
	dl := mkdir(t, base, "downloads")
	movies := mkdir(t, base, "media", "movies")
	s := testRootStore(t, dl)

	for name, ctx := range map[string]context.Context{
		"a Manager": managerCtx(),
		"anonymous": context.Background(),
	} {
		if _, err := s.Create(ctx, movies, KindMovies, "x"); err == nil {
			t.Errorf("%s configured a root folder", name)
		}
	}

	rf, err := s.Create(adminCtx(), movies, KindMovies, "x")
	if err != nil {
		t.Fatal(err)
	}
	for name, ctx := range map[string]context.Context{
		"a Manager": managerCtx(),
		"anonymous": context.Background(),
	} {
		if err := s.Delete(ctx, rf.ID); err == nil {
			t.Errorf("%s deleted a root folder", name)
		}
	}
}

// Removing a root from the configuration and deleting a library are wildly
// different intentions. The destructive one must never be a side effect of the
// administrative one.
func TestDeletingARootFolderLeavesTheFilesAlone(t *testing.T) {
	base := t.TempDir()
	dl := mkdir(t, base, "downloads")
	movies := mkdir(t, base, "media", "movies")
	film := filepath.Join(movies, "film.mkv")
	if err := os.WriteFile(film, []byte("the film"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := testRootStore(t, dl)
	ctx := adminCtx()
	rf, err := s.Create(ctx, movies, KindMovies, "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, rf.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(film); err != nil {
		t.Fatalf("deleting a root folder destroyed the library: %v", err)
	}
	if _, err := s.Get(ctx, rf.ID); !errors.Is(err, ErrRootNotFound) {
		t.Errorf("the row survived: %v", err)
	}
}

// An operator whose library is on a different filesystem should learn that
// imports will copy — and cost twice the disk — at configuration time, not from
// a full-disk alert.
func TestAnUnhardlinkableRootIsRecordedRatherThanRefused(t *testing.T) {
	base := t.TempDir()
	movies := mkdir(t, base, "media", "movies")
	// A download directory that does not exist and cannot be created stands in
	// for a different filesystem: the probe cannot be made, so the answer is
	// "no, and here is why" rather than a refusal.
	s := testRootStore(t, "/proc/cmediastack-cannot-exist")
	ctx := adminCtx()

	rf, err := s.Create(ctx, movies, KindMovies, "x")
	if err != nil {
		t.Fatalf("a root was refused for being unhardlinkable: %v", err)
	}
	if rf.HardlinksOK {
		t.Error("hardlinks were reported available against an unusable download directory")
	}
	if rf.HardlinkNote == "" {
		t.Error("no reason was recorded")
	}
}

// Reading where the library lives is browsing; changing it is not.
func TestListingRootFoldersNeedsOnlyBrowse(t *testing.T) {
	base := t.TempDir()
	dl := mkdir(t, base, "downloads")
	movies := mkdir(t, base, "media", "movies")
	s := testRootStore(t, dl)

	if _, err := s.Create(adminCtx(), movies, KindMovies, "x"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(managerCtx())
	if err != nil {
		t.Fatalf("a Manager could not list root folders: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("rows = %d", len(rows))
	}
	if _, err := s.List(context.Background()); err == nil {
		t.Error("an anonymous caller listed root folders")
	}
}

// The vault a root opens is contained to that root, which is what ties the two
// halves of this package together.
func TestAVaultOpenedFromARootFolderIsContained(t *testing.T) {
	base := t.TempDir()
	dl := mkdir(t, base, "downloads")
	movies := mkdir(t, base, "media", "movies")
	secret := filepath.Join(base, "media", "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := testRootStore(t, dl)
	ctx := adminCtx()
	rf, err := s.Create(ctx, movies, KindMovies, "x")
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.OpenVault(ctx, rf.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v.Close() }()

	if _, err := v.Open("../secret.txt"); !errors.Is(err, ErrEscapes) {
		t.Errorf("a vault from a root folder was not contained: %v", err)
	}
}

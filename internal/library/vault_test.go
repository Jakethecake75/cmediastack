package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// rig builds a library root with a hostile neighbour beside it: an outside
// directory holding a file that must never be readable, writable or removable
// through the vault, and symlinks inside the root pointing at it.
type rig struct {
	vault   *Vault
	root    string
	outside string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "library")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{root, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The vector filepath.Clean does not catch and EvalSymlinks races on.
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(root, "etc")); err != nil {
		t.Fatal(err)
	}

	v, err := Open(1, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return &rig{vault: v, root: root, outside: outside}
}

func adminCtx() context.Context {
	var admin authz.Role
	for _, r := range authz.BuiltinRoles() {
		if r.Name == authz.RoleAdmin {
			admin = r
		}
	}
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 1, Username: "jacob", Role: admin,
		State: authz.StateActive, MFASatisfied: true,
	})
}

func managerCtx() context.Context {
	var mgr authz.Role
	for _, r := range authz.BuiltinRoles() {
		if r.Name == authz.RoleManager {
			mgr = r
		}
	}
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 2, Username: "manager", Role: mgr,
		State: authz.StateActive, MFASatisfied: true, UnrestrictedLibraries: true,
	})
}

// ---------------------------------------------------------------------------
// Containment — the claim this package exists to make
// ---------------------------------------------------------------------------

// Every shape of escape, through every operation that touches a path. The
// symlink cases are the ones that matter: a prefix check passes them, and
// resolving-then-checking races on them.
func TestNoPathEscapesItsRoot(t *testing.T) {
	r := newRig(t)
	ctx := adminCtx()

	escapes := []string{
		"../outside/secret.txt",
		"../../etc/passwd",
		"/etc/passwd",
		"escape/secret.txt",          // symlink to a sibling directory
		"./escape/./secret.txt",      // the same, dressed up
		"etc/passwd",                 // absolute symlink to /etc
		"a/../../outside/secret.txt", // traversal through a non-existent dir
		"escape/../outside/secret.txt",
	}

	for _, name := range escapes {
		if _, err := r.vault.Open(name); err == nil {
			t.Errorf("Open(%q) succeeded", name)
		}
		if _, err := r.vault.Stat(name); err == nil {
			t.Errorf("Stat(%q) succeeded", name)
		}
		if r.vault.Exists(name) {
			t.Errorf("Exists(%q) reported true", name)
		}
		if _, err := r.vault.Create(name); err == nil {
			t.Errorf("Create(%q) succeeded", name)
		}
		if err := r.vault.MkdirAll(name); err == nil {
			t.Errorf("MkdirAll(%q) succeeded", name)
		}
		if err := r.vault.Remove(ctx, name); err == nil {
			t.Errorf("Remove(%q) succeeded", name)
		}
		if err := r.vault.RemoveAll(ctx, name); err == nil {
			t.Errorf("RemoveAll(%q) succeeded", name)
		}
		if err := r.vault.Rename(ctx, "anything", name); err == nil {
			t.Errorf("Rename(-> %q) succeeded", name)
		}
	}

	// Nothing outside was touched by any of that.
	if b, err := os.ReadFile(filepath.Join(r.outside, "secret.txt")); err != nil || string(b) != "SECRET" {
		t.Errorf("the outside file was disturbed: %q %v", b, err)
	}
}

// The escape error is one error for every shape, because the caller's response
// to all of them is identical and telling them apart only helps somebody
// probing.
func TestAnEscapeIsReportedAsAnEscape(t *testing.T) {
	r := newRig(t)

	for _, name := range []string{"../x", "/etc/passwd", "escape/secret.txt"} {
		_, err := r.vault.Open(name)
		if !errors.Is(err, ErrEscapes) {
			t.Errorf("Open(%q) err = %v, want ErrEscapes", name, err)
		}
		// And it does not leak the resolved path it refused.
		if err != nil && strings.Contains(err.Error(), r.outside) {
			t.Errorf("the refusal leaks the outside path: %v", err)
		}
	}
}

// Removing a symlink must unlink the LINK, never what it points at. Otherwise
// "clean up this import" becomes "delete /etc".
func TestRemovingASymlinkDoesNotFollowIt(t *testing.T) {
	r := newRig(t)
	ctx := adminCtx()

	if err := r.vault.RemoveAll(ctx, "escape"); err != nil {
		t.Fatalf("RemoveAll(escape): %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.outside, "secret.txt")); err != nil {
		t.Fatalf("removing a symlink deleted what it pointed at: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(r.root, "escape")); err == nil {
		t.Error("the symlink itself survived")
	}
}

// Deleting a library is an operator's decision to make with their own hands. No
// permission should imply it and nothing here needs it.
func TestTheRootItselfCannotBeRemoved(t *testing.T) {
	r := newRig(t)
	ctx := adminCtx()

	for _, name := range []string{".", "", "/", "./", "  .  ", "a/.."} {
		if err := r.vault.RemoveAll(ctx, name); !errors.Is(err, ErrUnsafeName) {
			t.Errorf("RemoveAll(%q) err = %v, want ErrUnsafeName", name, err)
		}
		if err := r.vault.Remove(ctx, name); !errors.Is(err, ErrUnsafeName) {
			t.Errorf("Remove(%q) err = %v, want ErrUnsafeName", name, err)
		}
	}
	if _, err := os.Stat(r.root); err != nil {
		t.Fatalf("the root folder was removed: %v", err)
	}
}

// And the legitimate case still works, or none of the above means anything.
func TestOrdinaryPathsInsideTheRootWork(t *testing.T) {
	r := newRig(t)
	ctx := adminCtx()

	if err := r.vault.MkdirAll("Movies/Blade Runner 2049 (2017)"); err != nil {
		t.Fatal(err)
	}
	f, err := r.vault.Create("Movies/Blade Runner 2049 (2017)/film.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("bytes"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if !r.vault.Exists("Movies/Blade Runner 2049 (2017)/film.mkv") {
		t.Fatal("the file is not where it was created")
	}
	if err := r.vault.Rename(ctx,
		"Movies/Blade Runner 2049 (2017)/film.mkv",
		"Movies/Blade Runner 2049 (2017)/Blade Runner 2049 (2017).mkv"); err != nil {
		t.Fatal(err)
	}
	if err := r.vault.Remove(ctx, "Movies/Blade Runner 2049 (2017)/Blade Runner 2049 (2017).mkv"); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Authority — the effect is checked where the effect happens
// ---------------------------------------------------------------------------

// A Manager may operate the download queue and edit library items. They may NOT
// destroy bytes or repoint paths, and the check that stops them lives in the
// filesystem layer — so it holds no matter which route reaches here.
func TestAManagerCannotDestroyBytesOrMovePathsThroughTheVault(t *testing.T) {
	r := newRig(t)
	admin, mgr := adminCtx(), managerCtx()

	f, err := r.vault.Create("film.mkv")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if err := r.vault.Remove(mgr, "film.mkv"); err == nil {
		t.Error("a Manager removed a media file")
	}
	if err := r.vault.RemoveAll(mgr, "film.mkv"); err == nil {
		t.Error("a Manager removed a media path")
	}
	if err := r.vault.Rename(mgr, "film.mkv", "other.mkv"); err == nil {
		t.Error("a Manager renamed a media file")
	}
	if !r.vault.Exists("film.mkv") {
		t.Fatal("the file was destroyed despite the refusals")
	}

	// The Admin can, or the test above proves only that everything is broken.
	if err := r.vault.Remove(admin, "film.mkv"); err != nil {
		t.Errorf("an Admin could not remove a media file: %v", err)
	}
}

// Deny by default: a context carrying no principal is anonymous, and anonymous
// destroys nothing.
func TestAnAnonymousContextDestroysNothing(t *testing.T) {
	r := newRig(t)
	f, err := r.vault.Create("film.mkv")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	bare := context.Background()
	if err := r.vault.Remove(bare, "film.mkv"); err == nil {
		t.Error("an anonymous caller removed a media file")
	}
	if err := r.vault.RemoveAll(bare, "film.mkv"); err == nil {
		t.Error("an anonymous caller removed a media path")
	}
	if err := r.vault.Rename(bare, "film.mkv", "x.mkv"); err == nil {
		t.Error("an anonymous caller renamed a media file")
	}
	if !r.vault.Exists("film.mkv") {
		t.Error("the file was destroyed")
	}
}

// ---------------------------------------------------------------------------
// Hardlinking
// ---------------------------------------------------------------------------

// The import operation: the same bytes, a second name, seeding uninterrupted.
func TestLinkGivesTheLibraryItsOwnNameForTheSameBytes(t *testing.T) {
	r := newRig(t)

	src := filepath.Join(t.TempDir(), "download.mkv")
	if err := os.WriteFile(src, []byte("the film"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := "Movies/Blade Runner 2049 (2017)/Blade Runner 2049 (2017).mkv"
	if err := r.vault.Link(src, dst); err != nil {
		t.Fatal(err)
	}

	srcInfo, _ := os.Stat(src)
	dstInfo, err := r.vault.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(srcInfo, dstInfo) {
		t.Error("the link is not the same file")
	}
	// The download is still there, so seeding continues.
	if _, err := os.Stat(src); err != nil {
		t.Errorf("the source was consumed: %v", err)
	}
}

// A link destination is contained exactly as every other path is.
func TestLinkCannotPlaceAFileOutsideTheRoot(t *testing.T) {
	r := newRig(t)

	src := filepath.Join(t.TempDir(), "download.mkv")
	if err := os.WriteFile(src, []byte("the film"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, dst := range []string{
		"../outside/planted.mkv",
		"/tmp/planted.mkv",
		"escape/planted.mkv",
		"etc/planted.mkv",
	} {
		if err := r.vault.Link(src, dst); err == nil {
			t.Errorf("Link(-> %q) succeeded", dst)
		}
	}
	if _, err := os.Stat(filepath.Join(r.outside, "planted.mkv")); err == nil {
		t.Error("a file was planted outside the root")
	}
}

// When a hardlink is impossible — different filesystems — copying is the
// fallback, and it must produce the same bytes.
func TestCopyFromProducesTheSameBytes(t *testing.T) {
	r := newRig(t)

	src := filepath.Join(t.TempDir(), "download.mkv")
	want := strings.Repeat("film bytes ", 1000)
	if err := os.WriteFile(src, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	n, err := r.vault.CopyFrom(in, "Movies/x/x.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(want)) {
		t.Errorf("copied %d bytes, want %d", n, len(want))
	}

	f, err := r.vault.Open("Movies/x/x.mkv")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	got := make([]byte, len(want))
	if _, err := f.Read(got); err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Error("the copy does not match the source")
	}
}

func TestCopyFromCannotPlaceAFileOutsideTheRoot(t *testing.T) {
	r := newRig(t)
	for _, dst := range []string{"../outside/planted.mkv", "escape/planted.mkv", "/tmp/planted.mkv"} {
		if _, err := r.vault.CopyFrom(strings.NewReader("x"), dst); err == nil {
			t.Errorf("CopyFrom(-> %q) succeeded", dst)
		}
	}
}

// A closed vault refuses everything rather than panicking on a nil descriptor.
func TestAClosedVaultRefusesEverything(t *testing.T) {
	r := newRig(t)
	ctx := adminCtx()
	if err := r.vault.Close(); err != nil {
		t.Fatal(err)
	}
	v := &Vault{}

	if _, err := v.Open("x"); !errors.Is(err, ErrVaultClosed) {
		t.Errorf("Open err = %v", err)
	}
	if _, err := v.Create("x"); !errors.Is(err, ErrVaultClosed) {
		t.Errorf("Create err = %v", err)
	}
	if err := v.Remove(ctx, "x"); !errors.Is(err, ErrVaultClosed) {
		t.Errorf("Remove err = %v", err)
	}
	if err := v.Link("a", "b"); !errors.Is(err, ErrVaultClosed) {
		t.Errorf("Link err = %v", err)
	}
	if _, err := v.CopyFrom(strings.NewReader("a"), "b"); !errors.Is(err, ErrVaultClosed) {
		t.Errorf("CopyFrom err = %v", err)
	}
}

// No trash folder is an empty trash. A trash folder that cannot be read is not:
// answering "empty" would hide it — from the purge above all, which would then
// never free the space.
func TestAnUnreadableTrashIsNotAnEmptyOne(t *testing.T) {
	r := newRig(t)
	if items, err := r.vault.ListTrash(); err != nil || len(items) != 0 {
		t.Fatalf("no trash folder: %v, %v; want empty and no error", items, err)
	}
	// Not a directory: reading it fails, and not because it is absent.
	if err := os.WriteFile(filepath.Join(r.root, TrashDir), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if items, err := r.vault.ListTrash(); err == nil {
		t.Errorf("an unreadable trash listed as %v", items)
	}
}

package importer

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
)

// ADR-0053, decision 1: one file to the trash, the title kept; a file the
// caller may not see is not found; one already gone is forgotten and said.
func TestDeletingOneFileTrashesItAndKeepsTheTitle(t *testing.T) {
	r := newRig(t)
	res := r.importOne(0, "Film 2019 1080p BluRay x264-GRP")
	onDisk := filepath.Join(r.movies, res.File.RelPath)

	// Out of scope: an account limited to another library.
	elsewhere := authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 5, Username: "kid",
		State: authz.StateActive, MFASatisfied: true, LibraryIDs: []int64{res.Item.RootFolderID + 100},
		// Not admin.system, which sees every library whatever its grants.
		Role: authz.Role{Name: "Manager", Permissions: authz.NewPermissionSet(authz.PermBrowse, authz.PermDeleteMediaFiles)}})
	if _, err := r.imp.DeleteFile(elsewhere, res.File.ID); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("a file out of scope: %v", err)
	}
	if _, err := os.Stat(onDisk); err != nil {
		t.Fatalf("the out-of-scope delete moved the file: %v", err)
	}

	del, err := r.imp.DeleteFile(r.ctx, res.File.ID)
	if err != nil {
		t.Fatal(err)
	}
	if del.Trashed == "" || del.Item.ID != res.Item.ID {
		t.Fatalf("deleted %+v", del)
	}
	if _, err := os.Stat(onDisk); err == nil {
		t.Error("the file is still in the library")
	}
	if fi, err := os.Stat(filepath.Join(r.movies, del.Trashed)); err != nil || fi.Size() != 20*mib {
		t.Errorf("the trashed file is not recoverable: %v", err)
	}
	if _, err := r.store.GetItem(r.ctx, res.Item.ID); err != nil {
		t.Errorf("the title went with its file: %v", err)
	}
	if files, _ := r.store.FilesFor(r.ctx, res.Item.ID); len(files) != 0 {
		t.Errorf("the record survived: %+v", files)
	}
	if _, err := r.imp.DeleteFile(r.ctx, res.File.ID); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("the same file twice: %v", err)
	}

	// Its bytes already gone: forgotten, and nothing said to be trashed.
	again := r.importOne(1, "Other Film 2020 1080p BluRay x264-GRP")
	if err := os.Remove(filepath.Join(r.movies, again.File.RelPath)); err != nil {
		t.Fatal(err)
	}
	gone, err := r.imp.DeleteFile(r.ctx, again.File.ID)
	if err != nil || gone.Trashed != "" {
		t.Errorf("a file already gone: %+v %v", gone, err)
	}
	if files, _ := r.store.FilesFor(r.ctx, again.Item.ID); len(files) != 0 {
		t.Errorf("its record survived: %+v", files)
	}
}

// ADR-0053, decision 2: one trashed file unlinked now, by someone who may
// destroy media; nothing that is not in the trash, ever.
func TestOneTrashedFileCanBePurgedNow(t *testing.T) {
	r := newRig(t)
	res := r.importOne(0, "Film 2019 1080p BluRay x264-GRP")
	keep := r.importOne(1, "Other Film 2020 1080p BluRay x264-GRP")
	del, err := r.imp.DeleteFile(r.ctx, res.File.ID)
	if err != nil {
		t.Fatal(err)
	}
	root := res.Item.RootFolderID

	// Not in the trash: a library file, or a name the trash does not hold.
	for _, p := range []string{keep.File.RelPath, library.TrashDir + "/nothing.mkv", "../escape.mkv"} {
		if _, err := r.imp.PurgeOne(r.ctx, root, p); !errors.Is(err, ErrNotInTrash) {
			t.Errorf("%q: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(r.movies, keep.File.RelPath)); err != nil {
		t.Fatalf("a library file was touched: %v", err)
	}
	// Somebody who may not destroy media cannot.
	scanner := authz.SystemPrincipal(t.Context(), authz.TaskLibraryScan)
	if _, err := r.imp.PurgeOne(scanner, root, del.Trashed); err == nil {
		t.Error("the scanner purged a file")
	}
	freed, err := r.imp.PurgeOne(r.ctx, root, del.Trashed)
	if err != nil || freed != 20*mib {
		t.Fatalf("purge: %d %v", freed, err)
	}
	if _, err := os.Stat(filepath.Join(r.movies, del.Trashed)); !os.IsNotExist(err) {
		t.Errorf("the purged file is still there: %v", err)
	}
	if _, err := r.imp.PurgeOne(r.ctx, root, del.Trashed); !errors.Is(err, ErrNotInTrash) {
		t.Errorf("purged twice: %v", err)
	}
}

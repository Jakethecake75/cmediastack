package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
)

func purgeCtx() context.Context {
	return authz.SystemPrincipal(context.Background(), authz.TaskTrashPurge)
}

// importOne puts a film in the library and returns its item id.
func (r *rig) importOne(h byte, title string) Result {
	r.t.Helper()
	src := r.download(hash(h), title, map[string]int64{
		strings.ReplaceAll(title, " ", ".") + ".mkv": 20 * mib,
	})
	res, err := r.imp.Import(r.ctx, src)
	if err != nil || res.Outcome != OutcomeImported {
		r.t.Fatalf("import: %v %+v", err, res)
	}
	return res
}

// A media library is often the only copy, and an operator clicking the wrong
// row is the likeliest failure by a wide margin. Deleting must be undoable.
func TestDeletingAnItemTrashesItRatherThanUnlinkingIt(t *testing.T) {
	r := newRig(t)
	res := r.importOne(0, "Film 2019 1080p BluRay x264-GRP")
	onDisk := filepath.Join(r.movies, res.File.RelPath)

	del, err := r.imp.DeleteItem(r.ctx, res.Item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(del.Trashed) != 1 {
		t.Fatalf("trashed = %v", del.Trashed)
	}

	// Gone from the library...
	if _, serr := os.Stat(onDisk); serr == nil {
		t.Error("the file is still in the library after deletion")
	}
	if _, gerr := r.store.GetItem(r.ctx, res.Item.ID); !errors.Is(gerr, ErrItemNotFound) {
		t.Errorf("the item record survived: %v", gerr)
	}

	// ...but the BYTES are still there. That is the undo.
	trashed := filepath.Join(r.movies, del.Trashed[0])
	fi, err := os.Stat(trashed)
	if err != nil {
		t.Fatalf("the deleted file is not recoverable: %v", err)
	}
	if fi.Size() != 20*mib {
		t.Errorf("the trashed file is %d bytes", fi.Size())
	}
}

// The undo, exercised rather than described.
func TestATrashedFileCanBeRestored(t *testing.T) {
	r := newRig(t)
	res := r.importOne(0, "Film 2019 1080p BluRay x264-GRP")

	del, err := r.imp.DeleteItem(r.ctx, res.Item.ID)
	if err != nil {
		t.Fatal(err)
	}

	restored, err := r.imp.Restore(r.ctx, res.Item.RootFolderID, del.Trashed[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := os.Stat(filepath.Join(r.movies, restored)); serr != nil {
		t.Fatalf("the restored file is not on disk: %v", serr)
	}
	// And a scan picks it back up, which is how it rejoins the library.
	scan, err := r.imp.Scan(r.ctx, res.Item.RootFolderID)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Added != 1 {
		t.Errorf("a restored file was not picked up by a scan: %+v", scan)
	}
}

// The retention window IS the undo. A purge that ran immediately would leave
// none.
func TestAPurgeLeavesFilesInsideTheRetentionWindow(t *testing.T) {
	r := newRig(t)
	res := r.importOne(0, "Film 2019 1080p BluRay x264-GRP")
	if _, err := r.imp.DeleteItem(r.ctx, res.Item.ID); err != nil {
		t.Fatal(err)
	}

	p, err := r.imp.PurgeTrash(purgeCtx(), 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if p.Purged != 0 {
		t.Errorf("a freshly trashed file was purged inside its retention window")
	}

	items, err := r.imp.ListTrash(r.ctx, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("trash = %+v", items)
	}
	// An operator is told when it becomes unrecoverable, not a duration to do
	// arithmetic with.
	if !items[0].PurgeAfter.After(items[0].TrashedAt) {
		t.Errorf("purge-after is not after trashed-at: %+v", items[0])
	}
}

// And once the window has passed, the bytes really do go.
func TestAPurgeUnlinksFilesPastTheirRetentionWindow(t *testing.T) {
	r := newRig(t)
	res := r.importOne(0, "Film 2019 1080p BluRay x264-GRP")
	del, err := r.imp.DeleteItem(r.ctx, res.Item.ID)
	if err != nil {
		t.Fatal(err)
	}
	trashed := filepath.Join(r.movies, del.Trashed[0])

	// A retention of a nanosecond: everything is past it.
	p, err := r.imp.PurgeTrash(purgeCtx(), time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if p.Purged != 1 {
		t.Fatalf("purged = %d, want 1", p.Purged)
	}
	if p.Freed != 20*mib {
		t.Errorf("freed = %d bytes", p.Freed)
	}
	if _, serr := os.Stat(trashed); serr == nil {
		t.Error("the file survived a purge past its window")
	}
	// Where the space came from, not just a total.
	if len(p.PerRoot) != 1 {
		t.Errorf("per-root = %+v", p.PerRoot)
	}
}

// A zero retention would delete files the instant they were trashed, leaving no
// undo at all. Honouring an obviously wrong configuration is worse than
// refusing it.
func TestAZeroRetentionIsRefused(t *testing.T) {
	r := newRig(t)
	for _, d := range []time.Duration{0, -time.Hour} {
		if _, err := r.imp.PurgeTrash(purgeCtx(), d); err == nil {
			t.Errorf("a retention of %v was accepted", d)
		}
	}
}

// The purge is the only thing in this software that unlinks a media file
// without a person naming it. Nothing else may do it.
func TestOnlyThePurgePrincipalCanPurge(t *testing.T) {
	r := newRig(t)
	res := r.importOne(0, "Film 2019 1080p BluRay x264-GRP")
	if _, err := r.imp.DeleteItem(r.ctx, res.Item.ID); err != nil {
		t.Fatal(err)
	}

	for name, ctx := range map[string]context.Context{
		"the importer": authz.SystemPrincipal(context.Background(), authz.TaskImport),
		"the scanner":  authz.SystemPrincipal(context.Background(), authz.TaskLibraryScan),
		"anonymous":    context.Background(),
	} {
		p, err := r.imp.PurgeTrash(ctx, time.Nanosecond)
		if err == nil && p.Purged > 0 {
			t.Errorf("%s purged %d file(s)", name, p.Purged)
		}
	}

	// The purge principal can, or the above proves only that purging is broken.
	if p, err := r.imp.PurgeTrash(purgeCtx(), time.Nanosecond); err != nil || p.Purged != 1 {
		t.Errorf("the purge principal could not purge: %v %+v", err, p)
	}
}

// A record whose file is already gone describes nothing. Forgetting it is
// right, but the operator is told, because a library that drifted from its disk
// is worth knowing about.
func TestDeletingReportsRecordsWhoseFilesWereAlreadyGone(t *testing.T) {
	r := newRig(t)
	res := r.importOne(0, "Film 2019 1080p BluRay x264-GRP")

	if err := os.Remove(filepath.Join(r.movies, res.File.RelPath)); err != nil {
		t.Fatal(err)
	}

	del, err := r.imp.DeleteItem(r.ctx, res.Item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(del.Missing) != 1 {
		t.Errorf("missing = %v, want the one already-gone file", del.Missing)
	}
	if len(del.Trashed) != 0 {
		t.Errorf("trashed = %v, want none", del.Trashed)
	}
}

// A file dropped into the trash folder by hand has no timestamp in its name.
// Purging it on the next run — treating it as infinitely old — would destroy
// something an operator put there deliberately.
func TestAHandPlacedTrashFileIsNotTreatedAsInfinitelyOld(t *testing.T) {
	r := newRig(t)
	dir := filepath.Join(r.movies, library.TrashDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manually-put-here.mkv"), make([]byte, mib), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := r.imp.PurgeTrash(purgeCtx(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if p.Purged != 0 {
		t.Error("a hand-placed trash file was purged despite being new")
	}
	// It still appears in the listing, so it is not invisible.
	items, _ := r.imp.ListTrash(r.ctx, time.Hour)
	if len(items) != 1 || items[0].Name != "manually-put-here.mkv" {
		t.Errorf("trash listing = %+v", items)
	}
}

// Restoring derives the destination from the file's own name, so a hostile one
// cannot place anything outside the root.
func TestRestoringCannotEscapeTheRoot(t *testing.T) {
	for _, name := range []string{
		"../../etc/cron.d/x.mkv",
		"/etc/passwd",
		"..",
		"",
	} {
		p, err := RestoredPath(name)
		if err != nil {
			continue
		}
		for _, comp := range strings.Split(p, "/") {
			if comp == "." || comp == ".." || comp == "" {
				t.Errorf("RestoredPath(%q) = %q has component %q", name, p, comp)
			}
		}
		if strings.Count(p, "/") != 1 {
			t.Errorf("RestoredPath(%q) = %q, want exactly folder/file", name, p)
		}
	}
}

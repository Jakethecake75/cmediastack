package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jakethecake75/cmediastack/internal/library"
)

// Deletion, and the purge that eventually makes it real.
//
// # Deleting media is the highest-stakes thing this software does
//
// §2 asks that every destructive operation be reversible or audited. Deletion
// here is BOTH, and the reversible half is not a nicety: a media library is
// often the only copy, an operator clicking the wrong row is the likeliest
// failure by a wide margin, and "are you sure?" has never stopped anybody.
//
// So deleting an item does not unlink anything. It moves the files into the
// root's trash folder, where they sit for the retention window, and the purge
// task unlinks them afterwards. That gives an operator a real undo — the bytes
// are still there, under a name that says when they were trashed — rather than
// a dialog box.
//
// There is deliberately NO "delete and purge immediately" option. A checkbox
// that destroys data on a mis-click is how the reversibility gets lost, and an
// operator who genuinely wants the space back now can empty the trash, which is
// a separate, named act rather than a modifier on a delete.

// DeleteResult is what one deletion did.
type DeleteResult struct {
	Item    Item
	Trashed []string
	// Missing are files whose records were removed but whose bytes were already
	// gone from disk. Reported rather than silently ignored: it is how an
	// operator finds out the library and the disk had drifted.
	Missing []string
}

// DeleteItem moves an item's files to trash and forgets their records.
//
// The caller must already hold PermDeleteMediaFiles — the route enforces it,
// and the vault enforces the effect again at the point the files move. What is
// NOT done here is unlinking: see the package note above.
func (i *Importer) DeleteItem(ctx context.Context, itemID int64) (DeleteResult, error) {
	item, err := i.store.GetItem(ctx, itemID)
	if err != nil {
		return DeleteResult{}, err
	}
	files, err := i.store.FilesFor(ctx, itemID)
	if err != nil {
		return DeleteResult{}, err
	}

	vault, err := i.roots.OpenVault(ctx, item.RootFolderID)
	if err != nil {
		return DeleteResult{}, err
	}
	defer func() { _ = vault.Close() }()

	res := DeleteResult{Item: item}
	for _, f := range files {
		if !vault.Exists(f.RelPath) {
			// The record outlived the file. Forgetting it is right — it
			// describes nothing — but the operator is told, because a library
			// that had drifted from its disk is worth knowing about.
			res.Missing = append(res.Missing, f.RelPath)
			if ferr := i.store.ForgetFile(ctx, f.ID); ferr != nil {
				return res, ferr
			}
			continue
		}

		trashed, terr := vault.Supersede(ctx, f.RelPath, i.now())
		if terr != nil {
			// Stop at the first failure rather than pressing on. A
			// half-deleted item with some files trashed and some not is a state
			// an operator cannot reason about; leaving the rest in place means
			// they can retry.
			return res, fmt.Errorf("importer: moving %q to trash: %w", f.RelPath, terr)
		}
		res.Trashed = append(res.Trashed, trashed)

		if ferr := i.store.ForgetFile(ctx, f.ID); ferr != nil {
			// The bytes moved but the record did not. Said loudly: the library
			// now claims a file at a path that holds nothing.
			i.log.Error("a file was trashed but its record could not be removed",
				slog.String("path", f.RelPath), slog.String("error", ferr.Error()))
			return res, ferr
		}
	}

	// The item itself goes only once its files have: an item left behind by a
	// half-finished delete is a title the operator believes they removed.
	// (An item with no files at all is a series added and waiting for its
	// first episode, ADR-0025 — deleting one moves nothing and forgets the
	// rows, including its seasons and episodes, by cascade.)
	if err := i.store.ForgetItem(ctx, itemID); err != nil {
		return res, err
	}
	return res, nil
}

// PurgeResult is what one purge run did.
type PurgeResult struct {
	Purged int
	Freed  int64
	// PerRoot says where the space came from, because an operator watching a
	// particular disk fill up needs that rather than a total.
	PerRoot map[string]int
}

// PurgeTrash unlinks trashed files past their retention window.
//
// This is the only place in this software that unlinks a media file without a
// person having asked for that specific file, which is why it runs under a
// principal holding destroy authority and nothing else, and why the retention
// window exists at all: the gap between "deleted" and "gone" is the undo.
//
// A failure in one root does not stop the others. A disk that is not mounted is
// one root's problem.
func (i *Importer) PurgeTrash(ctx context.Context, retention time.Duration) (PurgeResult, error) {
	res := PurgeResult{PerRoot: map[string]int{}}
	if retention <= 0 {
		// A zero or negative retention would purge everything the instant it
		// was trashed, which is the design this exists to avoid. Refusing beats
		// honouring an obviously wrong configuration.
		return res, fmt.Errorf("importer: a trash retention of %v would delete files "+
			"the moment they are trashed, leaving no undo", retention)
	}

	roots, err := i.roots.List(ctx)
	if err != nil {
		return res, err
	}
	before := i.now().Add(-retention)

	for _, rf := range roots {
		vault, verr := i.roots.OpenVault(ctx, rf.ID)
		if verr != nil {
			i.log.Warn("a root folder could not be opened for a trash purge",
				slog.String("root", rf.Path), slog.String("error", verr.Error()))
			continue
		}
		purged, freed, perr := vault.PurgeTrash(ctx, before)
		_ = vault.Close()
		if perr != nil {
			i.log.Error("a trash purge failed",
				slog.String("root", rf.Path), slog.String("error", perr.Error()))
			continue
		}
		if purged > 0 {
			res.PerRoot[rf.Path] = purged
			i.log.Info("purged trashed files past their retention window",
				slog.String("root", rf.Path),
				slog.Int("files", purged),
				slog.Int64("bytes_freed", freed))
		}
		res.Purged += purged
		res.Freed += freed
	}
	return res, nil
}

// TrashItem is one file waiting in a root's trash.
type TrashItem struct {
	RootID    int64
	Root      string
	Path      string
	Name      string
	Bytes     int64
	TrashedAt time.Time
	// PurgeAfter is when this becomes unrecoverable, so an operator can see how
	// long they have rather than being told a retention period and doing the
	// arithmetic themselves.
	PurgeAfter time.Time
}

// ListTrash reports what is recoverable, and until when.
func (i *Importer) ListTrash(ctx context.Context, retention time.Duration) ([]TrashItem, error) {
	roots, err := i.roots.List(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]TrashItem, 0)
	for _, rf := range roots {
		vault, verr := i.roots.OpenVault(ctx, rf.ID)
		if verr != nil {
			continue
		}
		items, lerr := vault.ListTrash()
		_ = vault.Close()
		if lerr != nil {
			continue
		}
		for _, t := range items {
			out = append(out, TrashItem{
				RootID: rf.ID, Root: rf.Path, Path: t.Path, Name: t.Name,
				Bytes: t.Bytes, TrashedAt: t.TrashedAt,
				PurgeAfter: t.TrashedAt.Add(retention),
			})
		}
	}
	return out, nil
}

// ErrNothingToRestore means the named trashed file is not there.
var ErrNothingToRestore = errors.New("importer: no such file in the trash")

// Restore moves a trashed file back into the library.
//
// The undo that makes deletion reversible in practice rather than in principle.
// It restores to the item folder the file's name implies, not to wherever it
// came from: the original path is not recorded, and inventing one would be
// guessing. A scan then picks it up and records it properly.
func (i *Importer) Restore(ctx context.Context, rootID int64, trashPath string) (string, error) {
	vault, err := i.roots.OpenVault(ctx, rootID)
	if err != nil {
		return "", err
	}
	defer func() { _ = vault.Close() }()

	items, err := vault.ListTrash()
	if err != nil {
		return "", err
	}
	var found *library.Trashed
	for idx := range items {
		if items[idx].Path == trashPath {
			found = &items[idx]
			break
		}
	}
	if found == nil {
		return "", ErrNothingToRestore
	}

	dest, err := RestoredPath(found.Name)
	if err != nil {
		return "", err
	}
	if vault.Exists(dest) {
		return "", fmt.Errorf("importer: %q already exists; move it aside before restoring", dest)
	}
	if err := vault.Rename(ctx, found.Path, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// FileDeleteResult is what deleting one file did (ADR-0053).
type FileDeleteResult struct {
	File File
	Item Item
	// Trashed is where the file now is; empty when its bytes were already
	// gone and only the record was forgotten.
	Trashed string
}

// DeleteFile moves one file to its root's trash and forgets its record; the
// title stays, and what the file held is missing again (ADR-0053, decision 1).
// The file is read through the caller's scope, so one the caller may not see
// is not found. Nothing is unlinked.
func (i *Importer) DeleteFile(ctx context.Context, fileID int64) (FileDeleteResult, error) {
	files, err := i.store.queryFiles(ctx, visibleTo(ctx), `WHERE id = ?`, fileID)
	if err != nil {
		return FileDeleteResult{}, err
	}
	if len(files) == 0 {
		return FileDeleteResult{}, ErrItemNotFound
	}
	f := files[0]
	item, err := i.store.GetItem(ctx, f.ItemID)
	if err != nil {
		return FileDeleteResult{}, err
	}
	res := FileDeleteResult{File: f, Item: item}
	vault, err := i.roots.OpenVault(ctx, f.RootFolderID)
	if err != nil {
		return res, err
	}
	defer func() { _ = vault.Close() }()
	if vault.Exists(f.RelPath) {
		if res.Trashed, err = vault.Supersede(ctx, f.RelPath, i.now()); err != nil {
			return res, fmt.Errorf("importer: moving %q to trash: %w", f.RelPath, err)
		}
	}
	if err := i.store.ForgetFile(ctx, f.ID); err != nil {
		i.log.Error("a file was trashed but its record could not be removed",
			slog.String("path", f.RelPath), slog.String("error", err.Error()))
		return res, err
	}
	return res, nil
}

// ErrNotInTrash means a purge named something the trash does not hold.
var ErrNotInTrash = errors.New("importer: no such file in the trash")

// PurgeOne unlinks one trashed file now, whatever its retention (ADR-0053,
// decision 2). It must be an entry the trash lists: purging is for what was
// already deleted, and nothing else. It reports the bytes freed.
func (i *Importer) PurgeOne(ctx context.Context, rootID int64, trashPath string) (int64, error) {
	vault, err := i.roots.OpenVault(ctx, rootID)
	if err != nil {
		return 0, err
	}
	defer func() { _ = vault.Close() }()
	items, err := vault.ListTrash()
	if err != nil {
		return 0, err
	}
	for _, t := range items {
		if t.Path == trashPath {
			if err := vault.Remove(ctx, t.Path); err != nil {
				return 0, err
			}
			return t.Bytes, nil
		}
	}
	return 0, ErrNotInTrash
}

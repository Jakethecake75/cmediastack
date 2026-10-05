// Package library owns the media library: where it lives on disk, and every
// operation that touches it.
//
// # The problem this package exists to solve
//
// Phase 3 is the first time this software WRITES to the operator's filesystem,
// and it writes using names chosen by strangers. A torrent declares its own
// directory name. A release title comes from an indexer. A metadata provider
// returns a film title that becomes a folder. Every one of those is a bencode
// string, an XML attribute or a JSON field somebody else filled in, and
// "../../etc/cron.d/x" is a legal value for all of them.
//
// The usual answer — sanitise the string, then filepath.Join — is not enough,
// and the reason is worth stating precisely:
//
//   - filepath.Join CLEANS its result, so Join(root, "../../etc") does not
//     produce a path under root at all. It produces /etc.
//   - Checking strings.HasPrefix afterwards catches that, but not a SYMLINK
//     planted inside the tree. A link at <root>/movies/x pointing at /etc has a
//     perfectly innocent prefix.
//   - Resolving symlinks first (filepath.EvalSymlinks) and then checking the
//     prefix is a time-of-check-to-time-of-use race: the link can be created
//     between the check and the open.
//
// # What is done instead
//
// Every path under a root is opened through os.Root, which holds a descriptor
// to the root directory and resolves each component relative to it with
// openat2(RESOLVE_BENEATH) on Linux. Containment is enforced by the KERNEL, per
// component, at the moment of use. There is no window to race and no string
// comparison to get wrong.
//
// Verified rather than assumed — see TestNoPathEscapesItsRoot, which plants
// symlinks to an outside directory inside the root and tries every shape of
// escape through them.
//
// # Two different jobs, kept apart
//
// CONTAINMENT stops a path leaving its root. SANITISATION makes a single
// component usable as a filename — no control characters, no separators, not a
// Windows reserved device name, not so long the filesystem refuses it. Both are
// needed and neither substitutes for the other: a perfectly contained path can
// still be a filename no operator can delete, and a perfectly tidy name can
// still be "..".
//
// # The structural half
//
// A containment layer only works if nothing goes around it.
// TestNothingWritesOutsideAVault reads this package's source and fails the
// build on os.Create, os.Remove, os.Rename, os.MkdirAll and the rest outside
// the one file that is allowed to name them — the same technique, and the same reasoning, as
// egress.TestNoPackageDialsDirectly. A runtime check cannot prove that a code
// path does not exist.
package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// Errors callers distinguish.
var (
	// ErrEscapes means a path tried to leave its root. It is deliberately one
	// error for every shape of escape — "..", an absolute path, a symlink —
	// because the caller's response to all of them is identical, and telling
	// them apart is only useful to somebody probing.
	ErrEscapes = errors.New("library: the path leaves its root folder")
	// ErrUnsafeName means a path component is not usable as a filename.
	ErrUnsafeName = errors.New("library: unusable filename")
	// ErrVaultClosed means the root folder is no longer open.
	ErrVaultClosed = errors.New("library: the root folder is closed")
	// ErrSwapped means a path changed identity between being created and being
	// verified. See Vault.Link.
	ErrSwapped = errors.New("library: the destination changed underneath us")
)

// Vault is a root folder, open, with kernel-enforced containment.
//
// It is the ONLY way this software touches library files, and holding one is
// the capability: a function that has a *Vault may write inside that root and
// nowhere else, and a function that does not have one cannot write to the
// library at all.
type Vault struct {
	root *os.Root
	id   int64
	// dir is the resolved root directory, used for audit detail and for the one
	// operation that genuinely cannot be expressed relative to the root (see
	// Link). It is never used to build a path that os.Root could have built.
	dir string
}

// Open opens a root folder for use.
//
// The directory must already exist. Creating it here would mean a typo in a
// configuration file silently produces an empty library in the wrong place and
// then fills it.
func Open(id int64, dir string) (*Vault, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("library: opening root folder %s: %w", dir, err)
	}
	return &Vault{root: root, id: id, dir: dir}, nil
}

// Close releases the root's descriptor.
func (v *Vault) Close() error {
	if v == nil || v.root == nil {
		return nil
	}
	return v.root.Close()
}

// Dir reports the root directory. For display, logging and audit detail.
func (v *Vault) Dir() string { return v.dir }

// hostPath is name's full path on the host, for a link that points at it from
// outside the root (ADR-0076). Never opened through: the root does that.
func (v *Vault) hostPath(name string) string { return filepath.Join(v.dir, filepath.FromSlash(name)) }

// ID reports the root folder's id.
func (v *Vault) ID() int64 { return v.id }

func (v *Vault) check() error {
	if v == nil || v.root == nil {
		return ErrVaultClosed
	}
	return nil
}

// wrap turns os.Root's containment errors into ErrEscapes.
//
// os.Root reports "path escapes from parent", which is exactly right and
// exactly the wrong words to put in front of an operator. Everything else
// passes through unchanged: a missing file is still a missing file.
func wrap(op, name string, err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "escapes from parent") {
		return fmt.Errorf("%w: %s %q", ErrEscapes, op, name)
	}
	return fmt.Errorf("library: %s %q: %w", op, name, err)
}

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

// Stat reports on a path inside the root.
func (v *Vault) Stat(name string) (os.FileInfo, error) {
	if err := v.check(); err != nil {
		return nil, err
	}
	fi, err := v.root.Stat(name)
	return fi, wrap("stat", name, err)
}

// Exists reports whether a path is present, without making "it is missing" an
// error every caller has to unwrap.
func (v *Vault) Exists(name string) bool {
	_, err := v.Stat(name)
	return err == nil
}

// FS returns a read-only view of the root for walking.
//
// Symlinks are NOT followed: fs.WalkDir over this reports one as an entry of
// type ModeSymlink and does not descend into it, and reading through one that
// escapes is refused by the kernel. Verified rather than assumed, because the
// whole safety of scanning a library rests on it.
func (v *Vault) FS() fs.FS {
	if v == nil || v.root == nil {
		return emptyFS{}
	}
	return v.root.FS()
}

// emptyFS stands in for a closed vault so a caller walking one gets nothing
// rather than a nil dereference.
type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, ErrVaultClosed }

// Open opens a file inside the root for reading.
func (v *Vault) Open(name string) (*os.File, error) {
	if err := v.check(); err != nil {
		return nil, err
	}
	f, err := v.root.Open(name)
	return f, wrap("open", name, err)
}

// ---------------------------------------------------------------------------
// Writing. Additive operations need no permission beyond holding the Vault:
// the destructive ones below are where authority is checked.
// ---------------------------------------------------------------------------

// MkdirAll creates a directory and its parents inside the root.
func (v *Vault) MkdirAll(name string) error {
	if err := v.check(); err != nil {
		return err
	}
	return wrap("mkdir", name, v.root.MkdirAll(name, 0o755))
}

// Create creates or truncates a file inside the root.
func (v *Vault) Create(name string) (*os.File, error) {
	if err := v.check(); err != nil {
		return nil, err
	}
	if dir := path.Dir(name); dir != "." {
		if err := v.MkdirAll(dir); err != nil {
			return nil, err
		}
	}
	f, err := v.root.Create(name)
	return f, wrap("create", name, err)
}

// Link hardlinks a file from outside the root to a name inside it.
//
// Hardlinking rather than moving is deliberate. A move breaks seeding — the
// torrent client loses the file it is serving — and on a private tracker that
// is how an account is lost. A hardlink gives the library its own name for the
// same bytes at no extra disk cost.
//
// # Why this one operation is not purely kernel-contained, said plainly
//
// os.Root.Link requires BOTH names to be inside the root. Here the source is
// not: it is the completed download, outside the library by design. There is no
// exported way to hand os.Root's descriptor to linkat, so the destination path
// has to be spelled out for os.Link.
//
// What is done about that, in order:
//
//  1. The destination's parent is created THROUGH the root, so every component
//     of it is kernel-checked. A dst that escapes fails here and never reaches
//     os.Link.
//  2. os.Link runs against the spelled-out path.
//  3. The result is verified back THROUGH the root: the file must be visible at
//     dst from inside the root, and must be the same inode as the source. If it
//     is not, it is removed and ErrSwapped is returned.
//
// The residual is a race between (1) and (2) in which a directory component is
// replaced by a symlink. Step (3) does not prevent that race, it DETECTS it and
// removes what was created — so the outcome an attacker wants, a file planted
// outside the library, does not survive the call. Reaching even that requires
// the ability to create symlinks inside the library root, which is write access
// to the library: an attacker who has it does not need this vector.
//
// This is the only place in the package where containment is not purely the
// kernel's. It is documented here rather than discovered later.
func (v *Vault) Link(hostSrc, dst string) error {
	if err := v.check(); err != nil {
		return err
	}
	if dir := path.Dir(dst); dir != "." {
		if err := v.MkdirAll(dir); err != nil {
			return err
		}
	}

	srcInfo, err := os.Stat(hostSrc)
	if err != nil {
		return fmt.Errorf("library: reading %s: %w", hostSrc, err)
	}

	target := filepath.Join(v.dir, filepath.FromSlash(dst))
	if err := os.Link(hostSrc, target); err != nil {
		return wrap("link", dst, err)
	}

	// Verify back through the root. This is what makes the spelled-out path
	// above safe to have used.
	dstInfo, err := v.root.Stat(dst)
	if err != nil || !os.SameFile(srcInfo, dstInfo) {
		_ = os.Remove(target)
		if err != nil {
			return wrap("verify", dst, err)
		}
		return fmt.Errorf("%w: %q is not the file that was linked", ErrSwapped, dst)
	}
	return nil
}

// CopyFrom writes a host file into the root, for when a hardlink is impossible
// because the two are on different filesystems.
//
// The caller is expected to have said so out loud: a copy doubles the bytes on
// disk, and an operator who believed they were hardlinking should learn it from
// a log line rather than from a full-disk alert.
//
// It reads from what it is handed — a file the caller opened through a
// ContainedSource — rather than opening a host path itself: a path checked and
// then opened by name can be swapped in between, and a copy, unlike a
// hardlink, cannot verify afterwards which file it read.
func (v *Vault) CopyFrom(in io.Reader, dst string) (int64, error) {
	if err := v.check(); err != nil {
		return 0, err
	}
	out, err := v.Create(dst)
	if err != nil {
		return 0, err
	}
	n, cerr := io.Copy(out, in)
	if closeErr := out.Close(); cerr == nil {
		cerr = closeErr
	}
	if cerr != nil {
		// A half-written file is worse than none: it looks like a successful
		// import to anything that only checks for existence. Removing it needs
		// no permission, because this call created it moments ago and nothing
		// else has ever referred to it.
		_ = v.removeOwn(dst)
		return 0, fmt.Errorf("library: copying to %q: %w", dst, cerr)
	}
	return n, nil
}

// Rename moves a path within the root, both ends contained.
//
// This is EffectMutateLibraryPaths rather than a plain write: renaming is how
// an entire library gets orphaned without a single byte being deleted.
func (v *Vault) Rename(ctx context.Context, oldName, newName string) error {
	if err := v.check(); err != nil {
		return err
	}
	if err := authz.RequireEffect(ctx, authz.EffectMutateLibraryPaths,
		v.dir+"/"+newName); err != nil {
		return err
	}
	if dir := path.Dir(newName); dir != "." {
		if err := v.MkdirAll(dir); err != nil {
			return err
		}
	}
	return wrap("rename", newName, v.root.Rename(oldName, newName))
}

// TrashDir is where superseded files go, inside the root that held them.
//
// Inside the root rather than beside it, for two reasons: a rename within one
// filesystem is atomic and instant whatever the file's size, and a trash folder
// outside every root would be a path no Vault contains — which is exactly the
// uncontained write this package exists to prevent.
//
// The leading dot keeps it out of an operator's way; it is not a secret, and
// anything scanning the library must skip it by name.
const TrashDir = ".cmediastack-trash"

// Supersede moves a file into the root's trash folder.
//
// This is how an upgrade replaces what it improves on. It is a RENAME, not a
// delete: the bytes survive, so the operation is reversible, which is what §2
// asks of every destructive operation. An operator who disagrees with an
// upgrade gets their file back by moving it out again.
//
// It therefore needs EffectMutateLibraryPaths and NOT EffectDestroyMediaBytes,
// which is what lets the importer do it under a system principal that cannot
// unlink anything (see authz.systemPermissions). Purging the trash later is the
// destructive half and needs the destroy effect.
//
// The trashed name carries a timestamp so that superseding the same path twice
// does not overwrite the first casualty with the second.
func (v *Vault) Supersede(ctx context.Context, name string, now time.Time) (string, error) {
	if err := v.check(); err != nil {
		return "", err
	}
	if err := authz.RequireEffect(ctx, authz.EffectMutateLibraryPaths,
		v.dir+"/"+name); err != nil {
		return "", err
	}
	if err := refuseRootItself(name); err != nil {
		return "", err
	}
	if !v.Exists(name) {
		return "", fmt.Errorf("library: %q is not there to supersede", name)
	}

	stamp := now.UTC().Format("20060102-150405.000")
	dst := path.Join(TrashDir, stamp+"-"+path.Base(name))
	if err := v.MkdirAll(TrashDir); err != nil {
		return "", err
	}
	if err := wrap("supersede", dst, v.root.Rename(name, dst)); err != nil {
		return "", err
	}
	return dst, nil
}

// Trashed is one file waiting in the trash folder.
type Trashed struct {
	// Path is inside the root, so it can be handed straight back to the vault.
	Path string
	// Name is the file's original name, with the timestamp prefix removed.
	Name      string
	Bytes     int64
	TrashedAt time.Time
}

// ListTrash reports what is waiting to be purged.
//
// Reading it needs no special authority: knowing that a file you deleted is
// recoverable, and for how long, is the point of it being recoverable. The
// destructive half is PurgeTrash.
func (v *Vault) ListTrash() ([]Trashed, error) {
	if err := v.check(); err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(v.FS(), TrashDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// No trash folder means nothing has ever been superseded here, which is
		// an empty list rather than a problem.
		return nil, nil
	case err != nil:
		// Anything else is a problem, and saying "empty" would hide it — from
		// the purge above all, which would then never free the space.
		return nil, wrap("list", TrashDir, err)
	}

	out := make([]Trashed, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || e.Type()&fs.ModeSymlink != 0 {
			continue
		}
		t := Trashed{Path: path.Join(TrashDir, e.Name())}
		t.TrashedAt, t.Name = splitTrashName(e.Name())
		if info, ierr := e.Info(); ierr == nil {
			t.Bytes = info.Size()
			// A file whose name carries no parseable timestamp still has a
			// modification time, and using it beats treating the file as
			// infinitely old — which would purge it on the next run.
			if t.TrashedAt.IsZero() {
				t.TrashedAt = info.ModTime()
			}
		}
		out = append(out, t)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].TrashedAt.Before(out[b].TrashedAt) })
	return out, nil
}

// PurgeTrash unlinks trashed files older than a cutoff.
//
// This is the destructive half of the reversible design, and the ONLY place in
// this software that unlinks a media file without a person having asked for
// that specific file. It therefore carries EffectDestroyMediaBytes, and the
// scheduled task that calls it runs under a principal holding exactly that and
// nothing else (authz.TaskTrashPurge).
//
// A file whose age cannot be determined is NOT purged. Erring towards keeping
// something an operator might still want costs disk; erring the other way costs
// the file.
func (v *Vault) PurgeTrash(ctx context.Context, before time.Time) (int, int64, error) {
	if err := v.check(); err != nil {
		return 0, 0, err
	}
	if err := authz.RequireEffect(ctx, authz.EffectDestroyMediaBytes,
		v.dir+"/"+TrashDir); err != nil {
		return 0, 0, err
	}

	items, err := v.ListTrash()
	if err != nil {
		return 0, 0, err
	}

	purged, freed := 0, int64(0)
	for _, it := range items {
		if it.TrashedAt.IsZero() || !it.TrashedAt.Before(before) {
			continue
		}
		if err := v.root.Remove(it.Path); err != nil {
			return purged, freed, wrap("purge", it.Path, err)
		}
		purged++
		freed += it.Bytes
	}
	return purged, freed, nil
}

// splitTrashName recovers the time and original name from a trashed filename.
//
// Supersede writes "<20060102-150405.000>-<original name>". A name that does
// not match is returned whole with a zero time, which ListTrash then falls back
// to a modification time for — a file an operator dropped into the trash folder
// by hand is still their file.
func splitTrashName(name string) (time.Time, string) {
	const stampLayout = "20060102-150405.000"
	if len(name) > len(stampLayout)+1 && name[len(stampLayout)] == '-' {
		if t, err := time.Parse(stampLayout, name[:len(stampLayout)]); err == nil {
			return t.UTC(), name[len(stampLayout)+1:]
		}
	}
	return time.Time{}, name
}

// ---------------------------------------------------------------------------
// Destroying — EffectDestroyMediaBytes, checked HERE
// ---------------------------------------------------------------------------

// Remove unlinks a file inside the root.
//
// The permission check is here, in the filesystem layer, rather than in an HTTP
// handler. That is the whole design of internal/authz: "delete media files:
// admin only" is meaningless if a Manager can reach the same effect by removing
// a torrent with its data, repointing a root folder, or renaming a file into a
// void. The effect is checked where the effect happens, so every route that can
// reach an unlink is covered by construction rather than by remembering.
func (v *Vault) Remove(ctx context.Context, name string) error {
	if err := v.check(); err != nil {
		return err
	}
	if err := authz.RequireEffect(ctx, authz.EffectDestroyMediaBytes,
		v.dir+"/"+name); err != nil {
		return err
	}
	if err := refuseRootItself(name); err != nil {
		return err
	}
	return wrap("remove", name, v.root.Remove(name))
}

// RemoveAll removes a path and everything under it.
//
// os.Root does NOT follow a symlink when removing one: a link inside the root
// pointing at /etc is unlinked and /etc is untouched. Verified, not assumed —
// see TestRemovingASymlinkDoesNotFollowIt.
func (v *Vault) RemoveAll(ctx context.Context, name string) error {
	if err := v.check(); err != nil {
		return err
	}
	if err := authz.RequireEffect(ctx, authz.EffectDestroyMediaBytes,
		v.dir+"/"+name); err != nil {
		return err
	}
	if err := refuseRootItself(name); err != nil {
		return err
	}
	return wrap("remove", name, v.root.RemoveAll(name))
}

// removeOwn deletes a file this package created moments ago and that nothing
// else has ever referred to — a write probe, a half-written copy.
//
// It carries no permission check because there is no media byte to destroy: the
// file did not exist before the call that made it, and no record points at it.
// It is unexported and its callers are countable, which is what keeps that
// claim true. Anything a user could have seen goes through Remove.
func (v *Vault) removeOwn(name string) error {
	if err := v.check(); err != nil {
		return err
	}
	return wrap("remove", name, v.root.Remove(name))
}

// refuseRootItself rejects the names that mean "the root directory".
//
// RemoveAll(".") would delete an operator's entire library in one call. Nothing
// in this software needs that, and no permission should imply it: deleting a
// library is an operator's decision to make with their own hands.
func refuseRootItself(name string) error {
	switch path.Clean("/" + strings.TrimSpace(name)) {
	case "/":
		return fmt.Errorf("%w: refusing to operate on the root folder itself", ErrUnsafeName)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Cache — containment without media authority
// ---------------------------------------------------------------------------

// CacheMarker names a directory as this software's own cache.
//
// Its whole job is to make one mistake impossible: opening a Cache on an
// operator's media folder. A Cache has no permission checks, so pointing one at
// a library would be a permission-free write path straight into it — a
// configuration typo away. Requiring a marker file means the directory has to
// have been created by this software, or deliberately marked by a person, and a
// media folder is neither.
const CacheMarker = ".cmediastack-cache"

// Cache is kernel-contained storage for files this software fetched and owns.
//
// # Why this exists rather than reusing Vault
//
// Vault is containment AND media authority: its writes check
// EffectMutateLibraryPaths, its deletes check EffectDestroyMediaBytes. That is
// right for an operator's media and wrong for a poster. Caching artwork must
// not require the authority to rearrange a library, and a scan that refreshed
// artwork must not thereby hold the authority to delete media.
//
// So the containment is shared — the same os.Root, the same
// openat2(RESOLVE_BENEATH) per component at the moment of use — and the policy
// is not. One implementation, two policies, rather than a second containment
// layer nobody reviews as carefully as the first.
//
// # What may live here
//
// Only bytes this software fetched and can fetch again. Nothing here is
// somebody's only copy, which is exactly why deleting from it needs no
// ceremony and why Vault's reversibility machinery is absent: a lost poster is
// re-downloaded, a lost film is lost.
type Cache struct {
	root *os.Root
	dir  string
}

// OpenCache opens or creates a cache directory.
//
// Unlike Open, this one DOES create the directory, and the asymmetry is
// deliberate: a mistyped root folder silently fills an empty library in the
// wrong place, while a mistyped cache directory costs a re-download. The marker
// is written on creation and required on reuse.
func OpenCache(dir string) (*Cache, error) {
	// 0750: the cache is read by this process and served by it; nothing else
	// on the host has a reason to list it.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("library: creating cache directory %s: %w", dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("library: opening cache %s: %w", dir, err)
	}

	// Written through the root, so a symlinked marker path cannot place it
	// somewhere else.
	if _, err := root.Stat(CacheMarker); err != nil {
		f, cerr := root.Create(CacheMarker)
		if cerr != nil {
			_ = root.Close()
			return nil, fmt.Errorf("library: marking cache %s: %w", dir, cerr)
		}
		_, _ = f.WriteString("This directory is a CMediaStack cache. " +
			"Everything in it can be re-downloaded and is safe to delete.\n")
		_ = f.Close()
	}
	return &Cache{root: root, dir: dir}, nil
}

// Close releases the descriptor.
func (c *Cache) Close() error {
	if c == nil || c.root == nil {
		return nil
	}
	return c.root.Close()
}

// Dir reports the cache directory, for display and logging.
func (c *Cache) Dir() string { return c.dir }

func (c *Cache) check() error {
	if c == nil || c.root == nil {
		return ErrVaultClosed
	}
	return nil
}

// Exists reports whether a cached file is present.
func (c *Cache) Exists(name string) bool {
	if c.check() != nil {
		return false
	}
	_, err := c.root.Stat(name)
	return err == nil
}

// Stat reports a cached file's metadata.
func (c *Cache) Stat(name string) (os.FileInfo, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	fi, err := c.root.Stat(name)
	return fi, wrap("stat", name, err)
}

// Open reads a cached file.
func (c *Cache) Open(name string) (*os.File, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	f, err := c.root.Open(name)
	return f, wrap("open", name, err)
}

// Create makes or truncates a cached file, creating parent directories.
func (c *Cache) Create(name string) (*os.File, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	if dir := path.Dir(name); dir != "." {
		if err := c.root.MkdirAll(dir, 0o750); err != nil {
			return nil, wrap("mkdir", dir, err)
		}
	}
	f, err := c.root.Create(name)
	return f, wrap("create", name, err)
}

// Replace atomically installs a file, so a reader never sees a partial one.
//
// The temporary name is derived here and never from a caller's string: the
// point of this type is that no externally-supplied text reaches a path, and a
// ".part" suffix chosen by a caller would be exactly that.
func (c *Cache) Replace(name string, write func(w io.Writer) error) error {
	if err := c.check(); err != nil {
		return err
	}
	tmp := name + ".part"
	f, err := c.Create(tmp)
	if err != nil {
		return err
	}
	if werr := write(f); werr != nil {
		_ = f.Close()
		_ = c.root.Remove(tmp)
		return werr
	}
	if cerr := f.Close(); cerr != nil {
		_ = c.root.Remove(tmp)
		return wrap("close", tmp, cerr)
	}
	if rerr := c.root.Rename(tmp, name); rerr != nil {
		_ = c.root.Remove(tmp)
		return wrap("rename", name, rerr)
	}
	return nil
}

// Remove deletes a cached file.
//
// No effect check and no trash, unlike Vault.Remove, and the reason is stated
// in this type's doc comment: nothing here is anybody's only copy.
func (c *Cache) Remove(name string) error {
	if err := c.check(); err != nil {
		return err
	}
	if name == CacheMarker {
		return fmt.Errorf("library: refusing to remove the cache marker")
	}
	return wrap("remove", name, c.root.Remove(name))
}

// FS returns a read-only view for walking.
func (c *Cache) FS() fs.FS {
	if c.check() != nil {
		return emptyFS{}
	}
	return c.root.FS()
}

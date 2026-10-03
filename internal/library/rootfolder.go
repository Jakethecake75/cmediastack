package library

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// Kinds of root folder. What lives in a root decides how files inside it are
// named and organised.
const (
	KindMovies = "movies"
	KindSeries = "series"
	KindMusic  = "music"
	KindBooks  = "books"
)

// Errors the root-folder store distinguishes.
var (
	ErrRootNotFound = errors.New("library: no such root folder")
	ErrRootInvalid  = errors.New("library: the root folder cannot be used")
	ErrRootNested   = errors.New("library: root folders may not contain one another")
	ErrRootExists   = errors.New("library: that root folder is already configured")
	ErrRootInUse    = errors.New("library: the root folder still holds library records")
)

// RootFolder is a configured library location.
type RootFolder struct {
	ID    int64
	Path  string
	Kind  string
	Label string
	// HardlinksOK records whether a hardlink from the download directory into
	// this root was possible when it was checked.
	HardlinksOK  bool
	HardlinkNote string
	FreeBytes    int64
	CheckedAt    *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// RootStore persists root folders.
type RootStore struct {
	db  *db.DB
	now func() time.Time
	// downloadDir is where completed downloads live, used to decide whether
	// hardlinking into a root is possible. Empty means the engine is off, and
	// the question does not arise.
	downloadDir string
}

// NewRootStore builds the store.
func NewRootStore(database *db.DB, downloadDir string, now func() time.Time) *RootStore {
	if now == nil {
		now = time.Now
	}
	return &RootStore{db: database, now: now, downloadDir: downloadDir}
}

// validKind reports whether a kind is one the schema will accept.
func validKind(k string) bool {
	switch k {
	case KindMovies, KindSeries, KindMusic, KindBooks:
		return true
	}
	return false
}

// Create adds a root folder after checking it is usable.
//
// The checks are done now, once, rather than discovered during an import at
// three in the morning. Each one has a failure it exists to prevent:
//
//   - not a directory, or missing: the import writes nothing and every file
//     "imports" successfully into a path that does not exist.
//   - not writable: the same, discovered later.
//   - nested inside another root: "which root owns this file" has two answers,
//     and a delete authorised against one reaches into the other.
//   - overlapping the download directory: an import would hardlink a file onto
//     itself, and a later cleanup of one would destroy the other. The torrent
//     being seeded and the library copy must be different paths.
func (s *RootStore) Create(ctx context.Context, path, kind, label string) (RootFolder, error) {
	if err := authz.RequireEffect(ctx, authz.EffectMutateLibraryPaths, path); err != nil {
		return RootFolder{}, err
	}
	if !validKind(kind) {
		return RootFolder{}, fmt.Errorf("%w: %q is not a kind of library", ErrRootInvalid, kind)
	}

	real, err := statDir(path)
	if err != nil {
		return RootFolder{}, err
	}
	if err := s.checkWritable(real); err != nil {
		return RootFolder{}, err
	}
	if err := s.checkNotOverlappingDownloads(real); err != nil {
		return RootFolder{}, err
	}

	existing, err := s.list(ctx)
	if err != nil {
		return RootFolder{}, err
	}
	for _, e := range existing {
		if e.Path == real {
			return RootFolder{}, fmt.Errorf("%w: %s", ErrRootExists, real)
		}
		if contains(e.Path, real) || contains(real, e.Path) {
			return RootFolder{}, fmt.Errorf("%w: %s and %s", ErrRootNested, e.Path, real)
		}
	}

	ok, note := s.hardlinkCheck(real)
	free := freeBytes(real)
	now := s.now().UTC()

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO root_folder (path, kind, label, hardlinks_ok, hardlink_note,
		                         free_bytes, checked_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		real, kind, label, boolInt(ok), note, free,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return RootFolder{}, fmt.Errorf("library: saving the root folder: %w", err)
	}
	id, _ := res.LastInsertId()

	return RootFolder{
		ID: id, Path: real, Kind: kind, Label: label,
		HardlinksOK: ok, HardlinkNote: note, FreeBytes: free,
		CheckedAt: &now, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// checkWritable proves the directory can be written to by writing to it.
//
// Checking the mode bits is not the same thing: the process may be running as a
// user the bits do not describe, the filesystem may be mounted read-only, a
// quota may be exhausted, or an ACL may say something the mode does not. The
// only reliable test is the operation itself.
func (s *RootStore) checkWritable(dir string) error {
	v, err := Open(0, dir)
	if err != nil {
		return fmt.Errorf("%w: %q: %w", ErrRootInvalid, dir, err)
	}
	defer func() { _ = v.Close() }()

	probe := ".cmediastack-write-test"
	f, err := v.Create(probe)
	if err != nil {
		return fmt.Errorf("%w: %q is not writable: %w", ErrRootInvalid, dir, err)
	}
	_ = f.Close()
	// Removing the probe this call created needs no permission: nothing else
	// has ever referred to it, and leaving it behind would litter every root
	// folder with a stray dotfile.
	_ = v.removeOwn(probe)
	return nil
}

// checkNotOverlappingDownloads refuses a root that shares a tree with the
// download directory.
//
// If they overlap, the library copy and the torrent being seeded are the same
// path: a cleanup of one destroys the other, and an import would hardlink a
// file onto itself.
func (s *RootStore) checkNotOverlappingDownloads(real string) error {
	if s.downloadDir == "" {
		return nil
	}
	dl := realPathOrEmpty(s.downloadDir)
	if dl == "" {
		// The download directory does not exist yet, so it cannot overlap. Not
		// an error here: the engine may be off, or the directory created later.
		return nil
	}
	if contains(dl, real) || contains(real, dl) {
		return fmt.Errorf("%w: %s overlaps the download directory %s. The seeded "+
			"torrent and the library copy must be different paths, or removing "+
			"one destroys the other", ErrRootInvalid, real, dl)
	}
	return nil
}

// hardlinkCheck reports whether a hardlink from the download directory into
// this root would work.
//
// It is answered by trying it, because the alternative — comparing st_dev — is
// wrong often enough to matter: bind mounts, overlayfs and btrfs subvolumes all
// produce surprises in both directions.
//
// This is recorded rather than enforced. A root on a different filesystem is
// perfectly usable; it just means imports copy instead of linking, which
// doubles the bytes on disk. That is a decision for the operator, and the point
// of checking now is that they get to make it knowingly instead of receiving a
// full-disk alert.
func (s *RootStore) hardlinkCheck(real string) (bool, string) {
	if s.downloadDir == "" {
		return false, "the download engine is not configured, so this was not checked"
	}
	src, err := makeLinkProbe(s.downloadDir)
	if err != nil {
		return false, err.Error()
	}
	defer removeLinkProbe(src)

	v, err := Open(0, real)
	if err != nil {
		return false, err.Error()
	}
	defer func() { _ = v.Close() }()

	const probe = linkProbeName
	if err := v.Link(src, probe); err != nil {
		return false, "hardlinks are not possible from the download directory to here, " +
			"so imports will COPY, using twice the disk space: " + err.Error()
	}
	_ = v.removeOwn(probe)
	return true, "hardlinks work: an import costs no extra disk space and seeding continues"
}

// Get returns one root folder.
func (s *RootStore) Get(ctx context.Context, id int64) (RootFolder, error) {
	rows, err := s.query(ctx, `WHERE id = ?`, id)
	if err != nil {
		return RootFolder{}, err
	}
	if len(rows) == 0 {
		return RootFolder{}, ErrRootNotFound
	}
	return rows[0], nil
}

// List returns the root folders the caller may see: every one, or the ones it
// is granted (ADR-0037). Reading the library layout needs the browse
// permission, not the one that changes it.
func (s *RootStore) List(ctx context.Context) ([]RootFolder, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	scope := authz.ScopeFromContext(ctx)
	if scope.AllLibraries {
		return s.list(ctx)
	}
	all, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]RootFolder, 0, len(all))
	for _, rf := range all {
		if scope.AllowsLibrary(rf.ID) {
			out = append(out, rf)
		}
	}
	return out, nil
}

// list reads without a permission check, for internal callers that have already
// established authority — Create's nesting check, and startup.
func (s *RootStore) list(ctx context.Context) ([]RootFolder, error) {
	return s.query(ctx, `ORDER BY kind ASC, path ASC`)
}

// Delete forgets a root folder. The files are untouched.
//
// Deliberately: removing a root from the configuration and deleting a library
// are wildly different intentions, and the destructive one should never be a
// side effect of the administrative one. An operator who wants the files gone
// deletes them with their own hands.
func (s *RootStore) Delete(ctx context.Context, id int64) error {
	// Unscoped: counting what a root holds, to refuse forgetting one that is in use.
	if err := authz.RequireEffect(ctx, authz.EffectMutateLibraryPaths,
		fmt.Sprintf("root:%d", id)); err != nil {
		return err
	}

	// Refuse while the library still points here, and say how much is at stake.
	//
	// The foreign key would refuse this anyway, but as a constraint violation:
	// "FOREIGN KEY constraint failed" tells an operator nothing about what they
	// were about to do. Cascading instead would be worse — it would silently
	// erase the record of several hundred films while leaving every file on
	// disk, so the library would look empty and the disk would look full.
	var items, files int
	if err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM media_item WHERE root_folder_id = ?),
		       (SELECT COUNT(*) FROM media_file WHERE root_folder_id = ?)`,
		id, id).Scan(&items, &files); err != nil {
		return fmt.Errorf("library: checking the root folder for media: %w", err)
	}
	if items > 0 || files > 0 {
		return fmt.Errorf("%w: %d item(s) and %d file(s) in the library are still "+
			"recorded in this root folder. Remove them from the library first — "+
			"deleting this entry would leave the files on disk with nothing "+
			"describing them", ErrRootInUse, items, files)
	}

	res, err := s.db.ExecContext(ctx, `DELETE FROM root_folder WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("library: deleting the root folder: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRootNotFound
	}
	return nil
}

// Refresh re-checks free space and hardlink viability.
func (s *RootStore) Refresh(ctx context.Context, id int64) (RootFolder, error) {
	rf, err := s.Get(ctx, id)
	if err != nil {
		return RootFolder{}, err
	}
	ok, note := s.hardlinkCheck(rf.Path)
	free := freeBytes(rf.Path)
	now := s.now().UTC()

	if _, err := s.db.ExecContext(ctx, `
		UPDATE root_folder SET hardlinks_ok = ?, hardlink_note = ?, free_bytes = ?,
		       checked_at = ?, updated_at = ? WHERE id = ?`,
		boolInt(ok), note, free, now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano), id); err != nil {
		return RootFolder{}, fmt.Errorf("library: refreshing the root folder: %w", err)
	}

	rf.HardlinksOK, rf.HardlinkNote, rf.FreeBytes, rf.CheckedAt = ok, note, free, &now
	return rf, nil
}

// OpenVault opens a root folder for file operations.
func (s *RootStore) OpenVault(ctx context.Context, id int64) (*Vault, error) {
	rf, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return Open(rf.ID, rf.Path)
}

func (s *RootStore) query(ctx context.Context, where string, args ...any) ([]RootFolder, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, path, kind, label, hardlinks_ok, hardlink_note,
		       free_bytes, checked_at, created_at, updated_at
		FROM root_folder `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("library: reading root folders: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]RootFolder, 0)
	for rows.Next() {
		var rf RootFolder
		var hardlinks int
		var checked *string
		var created, updated string
		if err := rows.Scan(&rf.ID, &rf.Path, &rf.Kind, &rf.Label, &hardlinks,
			&rf.HardlinkNote, &rf.FreeBytes, &checked, &created, &updated); err != nil {
			return nil, fmt.Errorf("library: reading root folders: %w", err)
		}
		rf.HardlinksOK = hardlinks != 0
		rf.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		rf.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		if checked != nil {
			if t, perr := time.Parse(time.RFC3339Nano, *checked); perr == nil {
				rf.CheckedAt = &t
			}
		}
		out = append(out, rf)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

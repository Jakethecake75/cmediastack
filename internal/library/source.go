package library

import (
	"fmt"
	"os"
	"path/filepath"
)

// ContainedSource is a directory files are imported FROM — a completed
// download — opened so that paths inside it cannot escape it.
//
// # Why this exists
//
// The destination of an import is contained by a Vault. The SOURCE needs the
// same treatment, and it is easy to miss because it feels like ours: it is a
// directory this software created, under a path from the configuration.
//
// The paths INSIDE it are not ours. They come from the torrent's file list,
// which the uploader wrote, and "../../../../etc/shadow" is a legal entry. A
// plain filepath.Join of the download directory and that string does not
// produce a path under the download directory — Join cleans, so it produces
// /etc/shadow. Hardlinking that into a media library puts it somewhere this
// software serves over HTTP to anyone who may browse.
//
// That is not a hypothetical this package reasoned its way to. The structural
// test in this package caught exactly that line in the importer, which is what
// structural tests are for: the failure was one filepath.Join written by
// somebody thinking about episode numbering.
type ContainedSource struct {
	root *os.Root
	dir  string
}

// OpenSource opens a directory to import from.
func OpenSource(dir string) (*ContainedSource, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("library: opening the source directory %s: %w", dir, err)
	}
	return &ContainedSource{root: root, dir: dir}, nil
}

// Close releases the descriptor.
func (s *ContainedSource) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}

// Dir reports the source directory.
func (s *ContainedSource) Dir() string { return s.dir }

// HostPath verifies that rel is inside the source and returns its full path.
//
// Containment is proven by asking the KERNEL — the stat goes through the held
// descriptor with RESOLVE_BENEATH — rather than by inspecting the string. A
// path that escapes, by "..", by an absolute form, or through a symlink planted
// inside the download, fails here and never becomes a host path at all.
//
// The returned path is then safe to hand to os.Link, which cannot take a
// descriptor. The residual race — the path being swapped between this check and
// that link — is closed on the other side: Vault.Link verifies the linked inode
// is the one it stat'd and removes the result if it is not.
func (s *ContainedSource) HostPath(rel string) (string, error) {
	if s == nil || s.root == nil {
		return "", ErrVaultClosed
	}
	if _, err := s.root.Stat(rel); err != nil {
		return "", wrap("source", rel, err)
	}
	return filepath.Join(s.dir, filepath.FromSlash(rel)), nil
}

// Open opens a file inside the source for reading, through the held
// descriptor — so a path that escapes, by "..", by an absolute form or through
// a symlink, is refused by the kernel, and what is read is what was checked.
// A copy reads from this rather than from HostPath: a copy, unlike a hardlink,
// has no way to verify afterwards which file it read.
func (s *ContainedSource) Open(rel string) (*os.File, error) {
	if s == nil || s.root == nil {
		return nil, ErrVaultClosed
	}
	f, err := s.root.Open(rel)
	if err != nil {
		return nil, wrap("source", rel, err)
	}
	return f, nil
}

// Exists reports whether a contained path is present.
func (s *ContainedSource) Exists(rel string) bool {
	if s == nil || s.root == nil {
		return false
	}
	_, err := s.root.Stat(rel)
	return err == nil
}

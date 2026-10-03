package migrate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
)

// Where a source database is allowed to come from.
//
// Not an HTTP upload. Accepting an arbitrary file from a request and handing it
// to a database engine is a new hostile-input surface for the sake of saving an
// operator one `cp`, and the operator deploying this already has shell access
// to the host. The file goes in a directory beside the application's own
// database and is named in the request BY BASENAME ONLY, resolved through
// os.Root — the same containment every other read in this system uses
// (ADR-0015, ADR-0021).

// ErrNoSuchSource means the named file is not in the directory.
var ErrNoSuchSource = errors.New("migrate: no such file in the migration directory")

// ErrBadSourceName means the request tried to name something other than a file
// in the directory.
var ErrBadSourceName = errors.New("migrate: a source is named by filename alone")

// Sources is the directory operators place exported databases in.
type Sources struct {
	root *os.Root
	dir  string
}

// OpenSources opens (creating if needed) the migration directory.
func OpenSources(dir string) (*Sources, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("migrate: creating %s: %w", dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: opening %s: %w", dir, err)
	}
	return &Sources{root: root, dir: dir}, nil
}

// Close releases the descriptor.
func (s *Sources) Close() error {
	if s == nil || s.root == nil {
		return nil
	}
	return s.root.Close()
}

// Dir is the directory, for telling an operator where to put the file.
func (s *Sources) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// List reports the files an operator has placed there.
func (s *Sources) List() ([]string, error) {
	if s == nil {
		return nil, ErrNoSuchSource
	}
	entries, err := fs.ReadDir(s.root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("migrate: listing %s: %w", s.dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// Path resolves a caller's name to a path on disk, refusing anything that is
// not a plain filename in the directory.
//
// Two independent checks, deliberately. The name is validated as a bare
// filename first, so a traversal is refused with an explanation rather than
// succeeding somewhere unexpected; then the file is OPENED through os.Root, so
// a symlink planted in the directory cannot redirect the result. Only the
// second is a guarantee — the first exists to make a refusal legible.
func (s *Sources) Path(name string) (string, error) {
	if s == nil {
		return "", ErrNoSuchSource
	}
	clean := strings.TrimSpace(name)
	if clean == "" || clean != path.Base(clean) ||
		strings.ContainsAny(clean, `/\`) || clean == "." || clean == ".." {
		return "", fmt.Errorf("%w, not a path: %q", ErrBadSourceName, name)
	}

	f, err := s.root.Open(clean)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrNoSuchSource, clean)
	}
	info, statErr := f.Stat()
	_ = f.Close()
	if statErr != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q is not a regular file", ErrNoSuchSource, clean)
	}
	return path.Join(s.dir, clean), nil
}

package library

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Operations on UNCONTAINED host paths.
//
// # Why this file exists and why it is allowed to name os.Stat and friends
//
// Every other file in this package must go through a Vault, and
// TestNothingWritesOutsideAVault fails the build otherwise. This file and
// vault.go are the two exceptions, and the exception is narrow enough to state
// exactly:
//
// Configuring a root folder is inherently an operation on a path that no root
// contains — the path IS the root, and it does not exist as a configured
// location until these checks pass. There is nothing to contain it against.
// The same is true of the download directory, which lives outside every
// library by design.
//
// **The invariant that makes this safe is not containment, it is provenance.**
// Every path handled in this file comes from the OPERATOR, through
// configuration or an authenticated admin request. None of it comes from a
// torrent's declared name, a release title, an indexer's XML, or a metadata
// provider's JSON — the stranger-supplied strings that the Vault exists for.
// A function here must never be handed one of those, and the way that is kept
// true is that nothing in this file takes a name apart or joins one to
// another: each takes a whole path and inspects it.
//
// If a future change needs to place a stranger-supplied name somewhere, it does
// not belong here. It belongs behind a Vault.

// statDir resolves an operator-supplied path and confirms it is a directory.
//
// Symlinks are resolved HERE, once, at configuration time. That is a different
// job from the per-operation containment a Vault does, and it is done for a
// different reason: two rows describing the same directory by different names
// would defeat the nesting check, so "which root owns this file" would have two
// answers.
func statDir(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("%w: no path given", ErrRootInvalid)
	}
	if !filepath.IsAbs(p) {
		// A relative root folder resolves against whatever the working
		// directory happens to be, which in a container is a detail of the
		// image and in a service is a detail of the unit file. Both change
		// without anybody meaning to move a library.
		return "", fmt.Errorf("%w: %q is not an absolute path", ErrRootInvalid, p)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %w", ErrRootInvalid, p, err)
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %w", ErrRootInvalid, p, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%w: %q is not a directory", ErrRootInvalid, p)
	}
	return real, nil
}

// realPathOrEmpty resolves a path, returning "" if it does not exist.
//
// Used for the download directory, which may legitimately not exist yet — the
// engine may be off, or the directory created on first use.
func realPathOrEmpty(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	return real
}

// contains reports whether parent is, or is an ancestor of, child.
//
// Compared component-wise rather than with a string prefix: "/media/movies" is
// not an ancestor of "/media/movies-4k", but HasPrefix says it is, and a
// nesting check that gets that wrong refuses a perfectly ordinary layout.
func contains(parent, child string) bool {
	if parent == child {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// makeLinkProbe writes a small file in the download directory to hardlink from.
// The name is fixed and recognisable, so an operator who finds one left behind
// by a crash knows what it was.
const linkProbeName = ".cmediastack-link-test"

// makeLinkProbe creates the download directory 0750 when it does not exist yet
// — it holds what the engine downloads and seeds, which nothing else on the
// host needs to list — and the probe 0600, for the moment it exists.
func makeLinkProbe(downloadDir string) (string, error) {
	if err := os.MkdirAll(downloadDir, 0o750); err != nil {
		return "", fmt.Errorf("the download directory could not be created: %w", err)
	}
	p := filepath.Join(downloadDir, linkProbeName)
	if err := os.WriteFile(p, []byte("cmediastack hardlink probe"), 0o600); err != nil {
		return "", fmt.Errorf("the download directory is not writable: %w", err)
	}
	return p, nil
}

func removeLinkProbe(p string) {
	if p != "" {
		_ = os.Remove(p)
	}
}

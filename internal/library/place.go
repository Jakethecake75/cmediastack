package library

import (
	"errors"
	"io/fs"
	"strings"
)

// Place puts a file from a download into the library, as one copy the download
// shares (ADR-0076), and returns how many copies are now on disk.
//
//   - A hardlink, when the two are on one filesystem: one file, two names.
//   - Across filesystems, where a hardlink cannot be made: a copy, and then
//     the download's file is replaced by a symbolic link to it. The torrent
//     seeds the file that is played, and the bytes are on disk once.
//
// Two copies are left only when the download's file could not be replaced; the
// caller says so, because it doubles the disk used.
func Place(src *ContainedSource, rel string, vault *Vault, dst string) (copies int, err error) {
	host, err := src.HostPath(rel)
	if err != nil {
		return 0, err
	}
	lerr := vault.Link(host, dst)
	if lerr == nil {
		return 1, nil
	}
	if !crossDevice(lerr) {
		return 0, lerr
	}
	in, err := src.Open(rel)
	if err != nil {
		return 0, err
	}
	n, err := vault.CopyFrom(in, dst)
	_ = in.Close()
	if err != nil {
		return 0, err
	}
	if err := src.linkTo(rel, n, vault.hostPath(dst)); err != nil {
		return 2, nil //nolint:nilerr // placed; the second copy is the caller's to report
	}
	return 1, nil
}

// crossDevice reports EXDEV, the one link failure a copy can recover from.
// Every other is a real problem, and copying past it would turn a permissions
// bug into silent disk use.
func crossDevice(err error) bool {
	return errors.Is(err, fs.ErrInvalid) || strings.Contains(err.Error(), "cross-device")
}

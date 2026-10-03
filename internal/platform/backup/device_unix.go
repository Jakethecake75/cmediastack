//go:build unix

package backup

import (
	"os"
	"syscall"
)

// sameFilesystem reports whether two paths are on one filesystem, and whether
// it could tell at all.
//
// Only a signal, and a one-sided one: two ZFS datasets on one pool, or two
// logical volumes on one disk, are different filesystems on the same hardware.
// A "yes" is certain; a "no" is not proof the backups are safe from the disk.
func sameFilesystem(a, b string) (same, known bool) {
	fa, err := os.Stat(a)
	if err != nil {
		return false, false
	}
	fb, err := os.Stat(b)
	if err != nil {
		return false, false
	}
	sa, okA := fa.Sys().(*syscall.Stat_t)
	sb, okB := fb.Sys().(*syscall.Stat_t)
	if !okA || !okB {
		return false, false
	}
	return sa.Dev == sb.Dev, true
}

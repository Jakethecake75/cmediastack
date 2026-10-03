//go:build unix

package library

import (
	"math"
	"syscall"
)

// freeBytes reports free space on the filesystem holding dir.
//
// Bavail rather than Bfree: Bfree counts blocks reserved for root, which this
// process is not and must never be (§2: "Nothing runs as root. Ever."). Showing
// an operator space they cannot actually use is the kind of number that leads
// to a failed import at the worst moment.
//
// The product is computed unsigned and clamped rather than converted: no disk
// holds 8 EiB, and a filesystem that reports more is answered with the most an
// int64 can say, not with a number that wrapped negative.
func freeBytes(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0
	}
	avail, ok := asUint64(st.Bavail)
	if !ok {
		return 0
	}
	blockSize, ok := asUint64(st.Bsize)
	if !ok || blockSize == 0 {
		return 0
	}
	if avail > math.MaxInt64/blockSize {
		return math.MaxInt64
	}
	free := avail * blockSize
	if free > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(free)
}

// asUint64 is v as a uint64, or false for a negative v. The two fields have
// different types on different systems, and some report a negative count of
// available blocks when the reserved ones are in use.
func asUint64[T int32 | int64 | uint32 | uint64](v T) (uint64, bool) {
	if v < 0 {
		return 0, false
	}
	return uint64(v), true
}

// Space reports the free and total bytes of the filesystem holding dir, for
// the health report (ADR-0040). Free is what this process may use, as above.
func Space(dir string) (free, total int64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	blocks, ok1 := asUint64(st.Blocks)
	blockSize, ok2 := asUint64(st.Bsize)
	if !ok1 || !ok2 || blockSize == 0 {
		return 0, 0, syscall.EINVAL
	}
	if blocks > math.MaxInt64/blockSize {
		return freeBytes(dir), math.MaxInt64, nil
	}
	size := blocks * blockSize
	if size > math.MaxInt64 {
		return freeBytes(dir), math.MaxInt64, nil
	}
	return freeBytes(dir), int64(size), nil
}

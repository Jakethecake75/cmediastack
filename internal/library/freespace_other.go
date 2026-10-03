//go:build !unix

package library

import "errors"

// freeBytes is unavailable on this platform. Zero means "not known" throughout;
// it is displayed rather than enforced, so not knowing costs nothing but a
// blank field.
func freeBytes(string) int64 { return 0 }

// Space is unavailable on this platform.
func Space(string) (free, total int64, err error) {
	return 0, 0, errors.ErrUnsupported
}

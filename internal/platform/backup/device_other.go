//go:build !unix

package backup

// sameFilesystem cannot tell on this platform.
func sameFilesystem(string, string) (same, known bool) { return false, false }

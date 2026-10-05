package library

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// Across filesystems a file is copied once and the download's file becomes a
// link to the copy, so the torrent seeds what is played (ADR-0076). /dev/shm
// is a tmpfs on Linux, a different filesystem from the test's temp dir.
func TestPlaceKeepsOneCopyAcrossFilesystems(t *testing.T) {
	shm, err := os.MkdirTemp("/dev/shm", "cms-place-")
	if err != nil {
		t.Skip("no /dev/shm here:", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shm) })
	lib := t.TempDir()
	var a, b syscall.Stat_t
	if syscall.Stat(shm, &a) != nil || syscall.Stat(lib, &b) != nil || a.Dev == b.Dev {
		t.Skip("/dev/shm is on the same filesystem as the temp dir")
	}

	if err := os.WriteFile(filepath.Join(shm, "film.mkv"), []byte("twelve bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := OpenSource(shm)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	vault, err := Open(1, lib)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vault.Close() }()

	copies, err := Place(src, "film.mkv", vault, "Film (2020)/Film.mkv")
	if err != nil || copies != 1 {
		t.Fatalf("Place = %d, %v; want one copy", copies, err)
	}
	target, err := os.Readlink(filepath.Join(shm, "film.mkv"))
	if want := filepath.Join(lib, "Film (2020)", "Film.mkv"); err != nil || target != want {
		t.Errorf("the download's file links to %q (%v), want %q", target, err, want)
	}
	if got, err := os.ReadFile(filepath.Join(shm, "film.mkv")); err != nil || string(got) != "twelve bytes" {
		t.Errorf("reading the download's name gives %q, %v", got, err)
	}
}

// The download's file is replaced only by a link to a whole copy of it.
func TestAShortCopyIsNotLinkedTo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.mkv"), []byte("twelve bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := OpenSource(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	if err := src.linkTo("a.mkv", 11, "/elsewhere"); err == nil {
		t.Error("a file was replaced by a link to a copy one byte short")
	}
	if info, err := os.Lstat(filepath.Join(dir, "a.mkv")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("the download's file is no longer itself: %v %v", info, err)
	}
}

package download

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// What a finished download left on disk is what an import reads once the
// engine has let the transfer go (ADR-0026's addendum). The listing is the
// download's own directory, contained, and regular files only.
func TestFilesOnDiskListsWhatTheDownloadLeftAndNothingElse(t *testing.T) {
	data := t.TempDir()
	hash := strings.Repeat("ab", 20)
	dir := filepath.Join(data, hash)
	write := func(rel string, n int) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Dune.2021.1080p/Dune.2021.1080p.mkv", 4096)
	write("Dune.2021.1080p/Subs/English.srt", 12)

	// Somewhere else in the data directory, and outside it altogether.
	elsewhere := filepath.Join(data, strings.Repeat("cd", 20))
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "other.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Links pointing out: neither followed nor listed.
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "passwd.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, "Dune.2021.1080p", "more")); err != nil {
		t.Fatal(err)
	}

	e := &Engine{cfg: Config{DataDir: data}}
	files, err := e.FilesOnDisk(hash)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	want := []TransferFile{
		{Path: "Dune.2021.1080p/Dune.2021.1080p.mkv", Bytes: 4096, Completed: 4096},
		{Path: "Dune.2021.1080p/Subs/English.srt", Bytes: 12, Completed: 12},
	}
	if len(files) != len(want) {
		t.Fatalf("files = %+v, want %+v", files, want)
	}
	for i := range want {
		if files[i] != want[i] {
			t.Errorf("file %d = %+v, want %+v", i, files[i], want[i])
		}
	}
}

// A hash that is not one names no directory, and nothing is read; a download
// whose directory is gone says so.
func TestFilesOnDiskRefusesWhatIsNotADownload(t *testing.T) {
	data := t.TempDir()
	e := &Engine{cfg: Config{DataDir: data}}
	for _, bad := range []string{"", "../etc", strings.Repeat("ab", 20) + "/..", strings.Repeat("AB", 20)} {
		if _, err := e.FilesOnDisk(bad); err == nil {
			t.Errorf("%q was listed", bad)
		}
	}
	if _, err := e.FilesOnDisk(strings.Repeat("ef", 20)); err == nil ||
		!strings.Contains(err.Error(), "nothing of this transfer is on disk") {
		t.Errorf("a missing directory: err = %v", err)
	}
}

// A directory holding more files than any release is refused, not walked to
// the end.
func TestFilesOnDiskStopsAtTheCap(t *testing.T) {
	was := maxFilesOnDisk
	maxFilesOnDisk = 3
	t.Cleanup(func() { maxFilesOnDisk = was })

	data := t.TempDir()
	hash := strings.Repeat("12", 20)
	if err := os.MkdirAll(filepath.Join(data, hash), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b", "c", "d"} {
		if err := os.WriteFile(filepath.Join(data, hash, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e := &Engine{cfg: Config{DataDir: data}}
	if _, err := e.FilesOnDisk(hash); !errors.Is(err, ErrTooManyFiles) {
		t.Errorf("err = %v, want ErrTooManyFiles", err)
	}
	if err := os.Remove(filepath.Join(data, hash, "d")); err != nil {
		t.Fatal(err)
	}
	if files, err := e.FilesOnDisk(hash); err != nil || len(files) != 3 {
		t.Errorf("at the cap: %v, %v", files, err)
	}
}

package library

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// A copy reads what the contained source opens, and the source opens only what
// is inside it: an escape by "..", by an absolute path or through a symlink the
// download planted is refused by the kernel, not by inspecting the string.
func TestASourceOpensOnlyWhatIsInsideIt(t *testing.T) {
	base := t.TempDir()
	dl := filepath.Join(base, "download")
	if err := os.MkdirAll(dl, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dl, "film.mkv"), []byte("the film"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(outside, []byte("not the film"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dl, "planted.mkv")); err != nil {
		t.Fatal(err)
	}

	src, err := OpenSource(dl)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()

	f, err := src.Open("film.mkv")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || string(got) != "the film" {
		t.Errorf("read %q, %v", got, err)
	}
	for _, rel := range []string{"../secret.txt", outside, "planted.mkv"} {
		if f, err := src.Open(rel); err == nil {
			_ = f.Close()
			t.Errorf("Open(%q) escaped the download", rel)
		}
	}
	var closed *ContainedSource
	if _, err := closed.Open("film.mkv"); err == nil {
		t.Error("a nil source opened a file")
	}
}

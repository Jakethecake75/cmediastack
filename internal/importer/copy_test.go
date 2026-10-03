package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/library"
)

// When a hardlink is impossible the import copies — and it reads the file
// through the download's contained source, so the bytes copied are the file
// that was checked, and a file the download planted outside itself is never
// read at all.
func TestTheCopyFallbackReadsOnlyInsideTheDownload(t *testing.T) {
	base := t.TempDir()
	dl, films := filepath.Join(base, "download"), filepath.Join(base, "films")
	for _, d := range []string{dl, films} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dl, "film.mkv"), []byte("the film"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(base, "secret")
	if err := os.WriteFile(secret, []byte("not the film"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dl, "planted.mkv")); err != nil {
		t.Fatal(err)
	}
	source, err := library.OpenSource(dl)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	vault, err := library.Open(1, films)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = vault.Close() }()

	if err := copyInto(vault, source, "film.mkv", "Film (2019)/Film (2019).mkv"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(films, "Film (2019)", "Film (2019).mkv"))
	if err != nil || string(got) != "the film" {
		t.Errorf("copied %q, %v", got, err)
	}
	if err := copyInto(vault, source, "planted.mkv", "Planted/Planted.mkv"); err == nil {
		t.Error("a file the download planted outside itself was copied")
	}
	if _, err := os.Stat(filepath.Join(films, "Planted")); err == nil {
		t.Error("the refused copy left something in the library")
	}
}

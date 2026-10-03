package download

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// TestASeasonTargetIsStoredAndReadBack: the third shape of target (ADR-0033,
// decision 1) survives the queue, and a mixture of shapes is refused.
func TestASeasonTargetIsStoredAndReadBack(t *testing.T) {
	s, _ := testStore(t)
	season := &Target{ItemID: 4, Season: 2, Pack: true}
	if err := s.Put(t.Context(), targeted(0, season)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(t.Context(), hashOf(0))
	if err != nil {
		t.Fatal(err)
	}
	if got.Target == nil || *got.Target != *season {
		t.Errorf("target = %+v, want %+v", got.Target, *season)
	}

	for i, bad := range []*Target{
		{ItemID: 4, Season: 2, Episode: 3, Pack: true},
		{ItemID: 4, Pack: true, Film: true},
		{ItemID: 4, Season: -1, Pack: true},
	} {
		if err := s.Put(t.Context(), targeted(byte(i+1), bad)); err == nil {
			t.Errorf("%+v was stored; it is not one shape of target", *bad)
		}
	}
}

// TestATorrentsFilesAreListedWithoutTouchingDisk: what automatic acquisition
// checks a pack against before queueing it (ADR-0033, decision 5).
func TestATorrentsFilesAreListedWithoutTouchingDisk(t *testing.T) {
	data := makeMultiFileTorrent(t, "Show.S01.1080p", map[string]int{
		"Show.S01E01.1080p.mkv":          3000,
		"Season 1/Show.S01E02.1080p.mkv": 3000,
		"Show.S01.nfo":                   10,
	})
	files, err := TorrentFiles(data)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
		if f.Bytes <= 0 {
			t.Errorf("%s: %d bytes", f.Path, f.Bytes)
		}
	}
	sort.Strings(paths)
	want := []string{"Season 1/Show.S01E02.1080p.mkv", "Show.S01.nfo", "Show.S01E01.1080p.mkv"}
	if len(paths) != len(want) {
		t.Fatalf("files %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("files %v, want %v", paths, want)
			break
		}
	}

	single, _ := makeTorrent(t, "", "film.mkv", 4000)
	if files, err := TorrentFiles(single); err != nil || len(files) != 1 || files[0].Path != "film.mkv" {
		t.Errorf("a single-file torrent listed %+v, %v", files, err)
	}
	if _, err := TorrentFiles([]byte("not a torrent")); err == nil {
		t.Error("garbage was listed as a torrent")
	}
}

// makeMultiFileTorrent builds a .torrent of a directory holding the files.
func makeMultiFileTorrent(t *testing.T, name string, files map[string]int) []byte {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	for rel, size := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	info := metainfo.Info{PieceLength: 16 * 1024}
	if err := info.BuildFromFilePath(dir); err != nil {
		t.Fatal(err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := (&metainfo.MetaInfo{InfoBytes: infoBytes}).Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

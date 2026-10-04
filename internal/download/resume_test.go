package download

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
)

// A restart keeps what an unfinished file already has (ADR-0073): a piece
// marked complete is still complete when the storage is opened again, and the
// file is written under its own name in <dir>/<infohash>. With the library's
// part files, every piece of an unfinished file was marked incomplete on open.
func TestARestartKeepsTheFinishedPiecesOfAnUnfinishedFile(t *testing.T) {
	dir := t.TempDir()
	info := &metainfo.Info{PieceLength: 16384, Name: "film.mkv", Length: 4 * 16384, Pieces: make([]byte, 4*20)}
	ih := metainfo.Hash{7}

	st, note := fileStorage(dir)
	if note != "" {
		t.Fatalf("the record of finished pieces did not open: %s", note)
	}
	tt, err := st.OpenTorrent(context.Background(), info, ih)
	if err != nil {
		t.Fatal(err)
	}
	p := tt.Piece(info.Piece(0))
	if _, err := p.WriteAt(make([]byte, 16384), 0); err != nil {
		t.Fatal(err)
	}
	if err := p.MarkComplete(); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, ih.HexString(), "film.mkv")); err != nil {
		t.Errorf("the unfinished file is not under its own name: %v", err)
	}

	st, _ = fileStorage(dir)
	defer func() { _ = st.Close() }()
	tt, err = st.OpenTorrent(context.Background(), info, ih)
	if err != nil {
		t.Fatal(err)
	}
	if c := tt.Piece(info.Piece(0)).Completion(); !c.Ok || !c.Complete {
		t.Errorf("after a restart the finished piece reads %+v; it would be downloaded again", c)
	}
	if c := tt.Piece(info.Piece(1)).Completion(); c.Complete {
		t.Errorf("a piece never written reads complete: %+v", c)
	}
}

// What an earlier version left as "<file>.part" becomes the file, and a name
// that leads out of the transfer's directory is left alone.
func TestPartFilesFromAnEarlierVersionAreAdopted(t *testing.T) {
	dir := t.TempDir()
	torrentDir := filepath.Join(dir, "abc")
	info := &metainfo.Info{Name: "Film", PieceLength: 16384, Files: []metainfo.FileInfo{
		{Path: []string{"film.mkv"}, Length: 1}, {Path: []string{"..", "..", "outside"}, Length: 1}}}
	if err := os.MkdirAll(filepath.Join(torrentDir, "Film"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(torrentDir, "Film", "film.mkv.part"), filepath.Join(dir, "outside.part")} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	adoptPartFiles(torrentDir, info)
	if _, err := os.Stat(filepath.Join(torrentDir, "Film", "film.mkv")); err != nil {
		t.Errorf("the part file was not adopted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "outside")); err == nil {
		t.Error("a name leading out of the transfer's directory was renamed")
	}
}

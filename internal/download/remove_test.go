package download

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// Remove deletes the download's own folder (ADR-0070) and keeps its row,
// stopped, because the queue is automatic acquisition's blocklist.
func TestRemovingADownloadDeletesItsFilesAndKeepsItsRow(t *testing.T) {
	dir := t.TempDir()
	store, _ := testStore(t)
	torrentFile, hash := makeTorrent(t, dir, "payload.bin", 32*1024)
	m := NewManager(newEngine(t, testGuard(t, true), Config{DataDir: dir}), store,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := m.AddTorrent(torrentFile, Meta{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(dir, "other")
	if err := os.MkdirAll(keep, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := m.Remove(hash); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, hash)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the download's folder is still there: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("something beside the download was deleted: %v", err)
	}
	if rec, err := store.Get(t.Context(), hash); err != nil || rec.Status != StatusStopped {
		t.Errorf("row = %+v, %v; want it kept, stopped", rec, err)
	}
}

// A download the engine is no longer running — finished, say — is removed
// the same way; one that is neither running nor recorded does not exist.
func TestADownloadNotRunningCanStillBeRemoved(t *testing.T) {
	dir := t.TempDir()
	store, _ := testStore(t)
	_, hash := makeTorrent(t, dir, "payload.bin", 32*1024)
	if err := store.Put(t.Context(), Record{InfoHash: hash, Title: "x", Torrent: []byte("d4:infod6:lengthi1eee")}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(t.Context(), hash, StatusComplete); err != nil {
		t.Fatal(err)
	}
	m := NewManager(newEngine(t, testGuard(t, true), Config{DataDir: dir}), store,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := m.Remove(hash); err != nil {
		t.Fatalf("removing a finished download: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, hash)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the finished download's folder is still there: %v", err)
	}
	if rec, _ := store.Get(t.Context(), hash); rec.Status != StatusStopped {
		t.Errorf("status = %q, want stopped", rec.Status)
	}

	if err := m.Remove(hashOf(1)); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing what was never here = %v, want ErrNotFound", err)
	}
}

package download

import (
	"testing"
	"time"
)

// A magnet has no metadata when it is added, so Start runs before there is
// anything to download. Every caller (a person's grab, automatic acquisition,
// the restore at boot) calls Start straight after adding and ignores its
// error, so Start must begin the download once the metadata arrives — not
// fail and leave it at 0% with peers waiting forever (found live, v0.2.0).
func TestAMagnetStartedBeforeItsMetadataStillDownloads(t *testing.T) {
	seedDir := t.TempDir()
	torrentFile, hash := makeTorrent(t, seedDir, "payload.bin", 256*1024)
	seeder := newEngine(t, testGuard(t, true), Config{DataDir: seedDir, Seed: true, AcceptIncoming: true})
	seed(t, seeder, torrentFile, hash)

	leecher := newEngine(t, testGuard(t, true), Config{DataDir: t.TempDir()})
	if _, err := leecher.Add(t.Context(), "magnet:?xt=urn:btih:"+hash); err != nil {
		t.Fatalf("adding the magnet: %v", err)
	}
	if err := leecher.Start(hash); err != nil {
		t.Fatalf("Start before the metadata: %v", err)
	}
	if err := leecher.AddPeer(hash, "127.0.0.1", seeder.ListenPort()); err != nil {
		t.Fatalf("adding a peer: %v", err)
	}

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		for _, tr := range leecher.List() {
			if tr.Done {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, tr := range leecher.List() {
		t.Fatalf("never finished: metadata %v, %d of %d bytes", tr.MetadataGot, tr.Completed, tr.Bytes)
	}
}

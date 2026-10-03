package download

import (
	"encoding/base32"
	"encoding/hex"
	"strings"
	"testing"
)

// HashOf is what automatic acquisition checks the blocklist with (ADR-0030):
// it must be the hash the queue stores for the same payload, in the same
// spelling, or a removed release comes straight back.
func TestHashOfIsTheHashTheQueueStores(t *testing.T) {
	data, want := makeTorrent(t, "", "film.mkv", 40_000)
	if got, err := HashOf(data, ""); err != nil || got != strings.ToLower(want) {
		t.Fatalf("torrent: %q, %v; want %q", got, err, strings.ToLower(want))
	}
	// Given both, the bytes are what the engine would add.
	other := strings.Repeat("cd", 20)
	if got, _ := HashOf(data, "magnet:?xt=urn:btih:"+other); got != strings.ToLower(want) {
		t.Fatalf("both given: %q", got)
	}

	h := strings.Repeat("ab", 20)
	if got, err := HashOf(nil, "magnet:?xt=urn:btih:"+strings.ToUpper(h)+"&dn=x"); err != nil || got != h {
		t.Fatalf("hex magnet: %q, %v", got, err)
	}
	raw, _ := hex.DecodeString(h)
	b32 := base32.StdEncoding.EncodeToString(raw)
	if got, err := HashOf(nil, "magnet:?xt=urn:btih:"+b32); err != nil || got != h {
		t.Fatalf("base32 magnet: %q, %v", got, err)
	}

	for name, c := range map[string]struct {
		torrent []byte
		magnet  string
	}{
		"nothing":             {},
		"not a torrent":       {torrent: []byte("this is not a torrent")},
		"too large":           {torrent: make([]byte, MaxTorrentFileBytes+1)},
		"not a magnet":        {magnet: "https://tracker.example/x"},
		"a magnet of nothing": {magnet: "magnet:?dn=something"},
	} {
		if got, err := HashOf(c.torrent, c.magnet); err == nil {
			t.Errorf("%s: %q, want an error", name, got)
		}
	}
}

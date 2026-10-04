package api

import (
	"net/http"
	"strconv"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/download"
)

// ADR-0066, decision 2: deleting a title stops the downloads aimed at it, and
// only those — so nothing a deleted title grabbed can finish and be filed
// against whatever title is added next.
func TestDeletingATitleStopsTheDownloadsAimedAtIt(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	id := r.setupLibrary(admin, t.TempDir())

	mine, other, untargeted := hashFor("mine"), hashFor("other"), hashFor("untargeted")
	r.downloads.items = []download.Transfer{{InfoHash: mine}, {InfoHash: other}, {InfoHash: untargeted}}
	r.downloads.records = []download.Record{
		{InfoHash: mine, Target: &download.Target{ItemID: int64(id), Film: true}},
		{InfoHash: other, Target: &download.Target{ItemID: int64(id) + 1, Film: true}},
		{InfoHash: untargeted},
	}

	del := admin.do(http.MethodDelete, "/api/v1/admin/media/"+strconv.Itoa(id), nil)
	if del.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", del.Code, del.Raw)
	}
	if len(r.downloads.removed) != 1 || r.downloads.removed[0] != mine {
		t.Errorf("removed %v, want only the deleted title's download %s", r.downloads.removed, mine)
	}
	if n, _ := del.Body["downloads_stopped"].(float64); n != 1 {
		t.Errorf("downloads_stopped = %v, want 1: %s", del.Body["downloads_stopped"], del.Raw)
	}
}

// ADR-0066, decision 3: the Queue carries the engine's peer connection counts
// and the last failure, so a download stuck at zero says why.
func TestTheQueueSaysWhyPeersAreNotConnecting(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.downloads.conns = download.ConnStats{Attempted: 312, Failed: 312,
		LastError: "egress: socks5 proxy refused: connection not allowed by ruleset"}
	res := admin.get("/api/v1/queue")
	c, _ := res.Body["connections"].(map[string]any)
	if c["attempted"] != float64(312) || c["failed"] != float64(312) ||
		c["last_error"] != "egress: socks5 proxy refused: connection not allowed by ruleset" {
		t.Errorf("connections = %v: %s", c, res.Raw)
	}
}

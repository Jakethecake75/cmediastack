package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// ADR-0053: one file to the trash over HTTP, the title kept; one trashed file
// purged now; only by whoever may delete media; both audited.
func TestOneFileIsDeletedAndOneTrashedFilePurgedOverHTTP(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	dir := t.TempDir()
	id := r.setupLibrary(admin, dir)
	item := admin.get("/api/v1/media/" + strconv.Itoa(id))
	files, _ := item.Body["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("files: %s", item.Raw)
	}
	fileID := strconv.Itoa(int(files[0].(map[string]any)["id"].(float64)))
	onDisk := filepath.Join(dir, "Arrival (2016)", "Arrival.2016.1080p.BluRay-GRP.mkv")

	code, _ := r.issueInvite(admin, authz.RoleManager, true)
	mgr := r.redeemAndEnroll(code, "manager", "a-perfectly-fine-passphrase")
	if res := mgr.do(http.MethodDelete, "/api/v1/admin/files/"+fileID, nil); res.Code != http.StatusNotFound {
		t.Errorf("a Manager reached the file delete: %d", res.Code)
	}

	del := admin.do(http.MethodDelete, "/api/v1/admin/files/"+fileID, nil)
	if del.Code != http.StatusOK || !strings.Contains(del.Raw, "The title stays") {
		t.Fatalf("delete: %d %s", del.Code, del.Raw)
	}
	if _, err := os.Stat(onDisk); err == nil {
		t.Error("the file is still in the library")
	}
	if res := admin.get("/api/v1/media/" + strconv.Itoa(id)); res.Code != http.StatusOK {
		t.Errorf("the title went with its file: %d", res.Code)
	}
	if res := admin.do(http.MethodDelete, "/api/v1/admin/files/"+fileID, nil); res.Code != http.StatusNotFound {
		t.Errorf("the same file twice: %d", res.Code)
	}

	trash := admin.get("/api/v1/admin/trash")
	entry := trash.Body["items"].([]any)[0].(map[string]any)
	for _, p := range []string{"Arrival (2016)/x.mkv", "../../etc/passwd", ""} {
		if res := admin.post("/api/v1/admin/trash/purge", map[string]any{"root_id": entry["root_id"], "path": p}); res.Code != http.StatusBadRequest {
			t.Errorf("purge(%q): %d", p, res.Code)
		}
	}
	if res := mgr.post("/api/v1/admin/trash/purge", map[string]any{"root_id": entry["root_id"], "path": entry["path"]}); res.Code != http.StatusNotFound {
		t.Errorf("a Manager reached the purge: %d", res.Code)
	}
	res := admin.post("/api/v1/admin/trash/purge", map[string]any{"root_id": entry["root_id"], "path": entry["path"]})
	if res.Code != http.StatusOK || res.Body["bytes"] == float64(0) {
		t.Fatalf("purge: %d %s", res.Code, res.Raw)
	}
	if res := admin.post("/api/v1/admin/trash/purge", map[string]any{"root_id": entry["root_id"], "path": entry["path"]}); res.Code != http.StatusNotFound {
		t.Errorf("purged twice: %d %s", res.Code, res.Raw)
	}
	if left := admin.get("/api/v1/admin/trash"); left.Body["count"] != float64(0) {
		t.Errorf("the trash still holds it: %s", left.Raw)
	}

	ctx := authz.WithPrincipal(t.Context(), adminPrincipalFor(t, r))
	for action, what := range map[audit.Action]string{
		audit.ActionMediaFileDeleted: "moved to trash",
		audit.ActionMediaPurged:      "unlinked now",
	} {
		events, err := r.audit.List(ctx, audit.Query{Action: action})
		if err != nil || len(events) != 1 || !strings.Contains(events[0].Detail, what) {
			t.Errorf("%s: %+v %v", action, events, err)
		}
	}
}

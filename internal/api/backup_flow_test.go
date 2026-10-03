package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/backup"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

// withBackups rebuilds the router with a real backup service attached, writing
// into a fresh directory, and returns that directory.
func (r *rig) withBackups() string {
	r.t.Helper()
	key, err := secrets.GenerateKey()
	if err != nil {
		r.t.Fatal(err)
	}
	cipher, err := secrets.NewCipherFromBase64(key)
	if err != nil {
		r.t.Fatal(err)
	}
	dir := filepath.Join(r.t.TempDir(), "backups")
	svc, err := backup.New(r.database, filepath.Join(r.t.TempDir(), "flow.db"), cipher.BackupPassphrase(),
		backup.Policy{Dir: dir, CreateDir: true, Interval: 24 * time.Hour, Keep: 7 * 24 * time.Hour, KeepMin: 3},
		r.audit, r.clk.now)
	if err != nil {
		r.t.Fatal(err)
	}
	auth := NewSessionAuthenticator(r.store, r.svc.Policy().Session, false)
	rt := NewRouter(
		[]Middleware{Recovery(quietLogger()), RequestContext(quietLogger()),
			ClientIPResolver(nil), SecurityHeaders(time.Hour)},
		[]Middleware{CSRF(), Authenticate(auth, r.audit)},
	)
	RegisterRoutes(rt, New(Deps{
		Identity: r.svc, Auth: auth, Egress: r.egress, Indexers: r.indexers,
		Profiles: r.profiles, Roots: r.roots, Media: r.media,
		Scanner: r.scanner, Deleter: r.scanner, Tickets: r.tickets,
		Requests: r.requests, Backups: svc, Audit: r.audit,
		TrashRetention: 7 * 24 * time.Hour,
	}))
	r.rt = rt
	return dir
}

func TestABackupIsTakenFromTheAdminScreenAndListed(t *testing.T) {
	r := newRig(t)
	dir := r.withBackups()
	admin := r.bootstrapAdmin()

	res := admin.post("/api/v1/admin/system/backup", nil)
	if res.Code != http.StatusCreated {
		t.Fatalf("take: %d %s", res.Code, res.Raw)
	}
	b, _ := res.Body["backup"].(map[string]any)
	name, _ := b["name"].(string)
	sha, _ := res.Body["sha256"].(string)
	if !strings.HasPrefix(name, "cmediastack-") || len(sha) != 64 ||
		!strings.Contains(res.Body["message"].(string), "Copy the backup directory off this host") {
		t.Fatalf("take answered %s", res.Raw)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Fatalf("the backup is not on disk: %v", err)
	}

	r.clk.advance(time.Minute)
	if res := admin.post("/api/v1/admin/system/backup", nil); res.Code != http.StatusCreated {
		t.Fatalf("second: %d %s", res.Code, res.Raw)
	}

	list := admin.get("/api/v1/admin/system/backups")
	if list.Code != http.StatusOK || list.Body["count"].(float64) != 2 ||
		list.Body["dir"] != dir || !strings.Contains(list.Body["restore"].(string), "-restore-backup") {
		t.Fatalf("list: %d %s", list.Code, list.Raw)
	}
	first := list.Body["backups"].([]any)[0].(map[string]any)
	if first["name"] == name {
		t.Fatalf("the list is not newest first: %s", list.Raw)
	}

	// Recorded as the person who asked, from where they asked.
	evs, err := r.audit.List(authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 1, Username: "x",
		State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{Rank: 100, Permissions: authz.NewPermissionSet(authz.PermViewAuditLog)}}),
		audit.Query{Action: audit.ActionBackupCreated})
	if err != nil || len(evs) != 2 || evs[1].TargetID != name || evs[1].ActorUserID == nil ||
		evs[1].SourceIP != "203.0.113.10" {
		t.Fatalf("records = %+v, %v", evs, err)
	}
}

// Hidden, not merely forbidden: to anyone without admin.system the routes are
// not there at all.
func TestTheBackupRoutesAreHiddenFromEveryoneElse(t *testing.T) {
	r := newRig(t)
	dir := r.withBackups()
	admin := r.bootstrapAdmin()
	code, _ := r.issueInvite(admin, authz.RoleManager, true)
	manager := r.redeemAndEnroll(code, "morgan", "manager-passphrase-1")
	// Somebody with no account, holding the CSRF cookie any visitor to the
	// login page gets — so what answers is the route's hiding, not the CSRF
	// check in front of it.
	nobody := r.client()
	nobody.visitPage("/login")

	for who, c := range map[string]*client{"a manager": manager, "nobody": nobody} {
		if res := c.post("/api/v1/admin/system/backup", nil); res.Code != http.StatusNotFound {
			t.Errorf("%s: POST backup = %d %s", who, res.Code, res.Raw)
		}
		if res := c.get("/api/v1/admin/system/backups"); res.Code != http.StatusNotFound {
			t.Errorf("%s: GET backups = %d %s", who, res.Code, res.Raw)
		}
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("a refused request took a backup")
	}
}

// ADR-0029, decision 7: no route serves a backup and none restores one. A
// download route would let a stolen administrator session carry off the whole
// database in one request; a restore route would let it roll the instance
// back. Checked against the routing table, so a route added later that serves
// or restores one fails here rather than in review.
func TestNoRouteServesOrRestoresABackup(t *testing.T) {
	r := newRig(t)
	r.withBackups()
	allowed := map[string]bool{
		"POST /api/v1/admin/system/backup": true,
		"GET /api/v1/admin/system/backups": true,
	}
	for _, route := range r.rt.Routes() {
		id := route.ID()
		if strings.Contains(strings.ToLower(id), "backup") || strings.Contains(strings.ToLower(id), "restore") {
			if !allowed[id] && !strings.HasSuffix(id, "/trash/restore") {
				t.Errorf("%s: backups are taken and listed over HTTP, and nothing else", id)
			}
		}
	}
}

func TestBackupsAnswerHonestlyWhenNotWired(t *testing.T) {
	r := newRig(t) // no backup service
	admin := r.bootstrapAdmin()
	if res := admin.post("/api/v1/admin/system/backup", nil); res.Code != http.StatusNotImplemented {
		t.Fatalf("POST = %d %s", res.Code, res.Raw)
	}
	if res := admin.get("/api/v1/admin/system/backups"); res.Code != http.StatusNotImplemented {
		t.Fatalf("GET = %d %s", res.Code, res.Raw)
	}
}

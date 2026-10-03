package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/request"
)

// A file put by hand into the folder of a title a request is linked to, and
// recorded by the hourly scan, fulfils the request (ADR-0028, decision 3) —
// through the function the scheduled task runs, with the authority it runs
// with. Without this the request would say "nothing on disk yet" beside a film
// that plays, and go on counting against its requester's open requests.
func TestAScanThatFindsALinkedTitlesFileFulfilsTheRequest(t *testing.T) {
	base := t.TempDir()
	films := filepath.Join(base, "films")
	if err := os.MkdirAll(filepath.Join(films, "Dune (2021)"), 0o755); err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(db.Options{Path: filepath.Join(base, "cms.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var admin authz.Role
	for _, role := range authz.BuiltinRoles() {
		if role.Name == authz.RoleAdmin {
			admin = role
		}
	}
	adminCtx := authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 1, Username: "jacob",
		Role: admin, State: authz.StateActive, MFASatisfied: true})
	roots := library.NewRootStore(database, filepath.Join(base, "downloads"), nil)
	root, err := roots.Create(adminCtx, films, library.KindMovies, "Films")
	if err != nil {
		t.Fatal(err)
	}
	store := importer.NewStore(database, time.Now)
	imp := importer.New(store, roots, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	exec := func(q string, args ...any) int64 {
		t.Helper()
		res, err := database.ExecContext(t.Context(), q, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	role := exec(`INSERT INTO role (name, rank, builtin, created_at, updated_at) VALUES ('Admin', 100, 1, ?, ?)`, now, now)
	user := exec(`INSERT INTO app_user (username, email, password_hash, state, role_id, rating_ceiling, created_at, updated_at)
	              VALUES ('sam', 'sam@example.com', 'x', 'active', ?, 0, ?, ?)`, role, now, now)
	item := exec(`INSERT INTO media_item (kind, title, year, sort_title, root_folder_id, folder, added_at, updated_at)
	              VALUES ('movie', 'Dune', 2021, 'dune', ?, 'Dune (2021)', ?, ?)`, root.ID, now, now)

	requests := request.NewService(request.NewStore(database, time.Now), audit.New(database, time.Now), time.Now)
	rq, _, err := requests.Store().Create(t.Context(), request.NewRequest{Kind: request.KindMovie, Title: "Dune", Year: 2021, By: user})
	if err != nil {
		t.Fatal(err)
	}
	if err := requests.Store().Decide(t.Context(), rq.ID, request.StateApproved, user, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := requests.Store().Link(t.Context(), rq.ID, item); err != nil {
		t.Fatal(err)
	}

	// The operator copies the film into its folder by hand.
	f, err := os.Create(filepath.Join(films, "Dune (2021)", "Dune.2021.1080p.BluRay.x264-GRP.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(importer.MinVideoBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	sysCtx := authz.SystemPrincipal(t.Context(), authz.TaskLibraryScan)
	summary, err := runScans(sysCtx, roots, imp, requests, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "1 request(s) fulfilled by what is now on disk") {
		t.Errorf("summary = %q", summary)
	}
	got, err := requests.Store().ByID(t.Context(), rq.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != request.StateFulfilled || got.MediaItemID == nil || *got.MediaItemID != item {
		t.Errorf("request = %s, item %v; want fulfilled by %d", got.State, got.MediaItemID, item)
	}
}

type failingFulfiller struct{ calls int }

func (f *failingFulfiller) FulfilOnDisk(context.Context) (int64, error) {
	f.calls++
	return 0, errors.New("the database is locked")
}

// Closing requests is the scan's afterthought, not its purpose: when it fails,
// the scan — whose records are already written — does not.
func TestAScanDoesNotFailBecauseItsRequestsCouldNotBeClosed(t *testing.T) {
	base := t.TempDir()
	database, err := db.Open(db.Options{Path: filepath.Join(base, "cms.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	roots := library.NewRootStore(database, filepath.Join(base, "downloads"), nil)
	imp := importer.New(importer.NewStore(database, time.Now), roots,
		slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	f := &failingFulfiller{}
	sysCtx := authz.SystemPrincipal(t.Context(), authz.TaskLibraryScan)
	if _, err := runScans(sysCtx, roots, imp, f, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Errorf("the scan failed with its requests: %v", err)
	}
	if f.calls != 1 {
		t.Errorf("the requests were asked about %d times, want once", f.calls)
	}
}

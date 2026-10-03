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
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// A finished download the engine no longer holds is still imported — from
// what it left on disk (found verifying ADR-0026). With seeding off, the
// seeding task lets a finished transfer go at its next tick; a restart does
// the same to every finished one. Before, the import pass asked only the
// engine, was told "no such transfer", and moved on: complete in the queue,
// never in the library, and nothing said why.

// engineless is a download manager whose engine holds nothing.
type engineless struct {
	recs   []download.Record
	data   string
	onDisk func(hash string) ([]download.TransferFile, error)
}

func (e *engineless) Records(context.Context) ([]download.Record, error) { return e.recs, nil }
func (e *engineless) FilesOf(string) ([]download.TransferFile, error) {
	return nil, download.ErrNotFound
}
func (e *engineless) FilesOnDisk(h string) ([]download.TransferFile, error) { return e.onDisk(h) }
func (e *engineless) DataPathFor(h string) (string, error)                  { return filepath.Join(e.data, h), nil }

type importRig struct {
	store     *importer.Store
	imp       *importer.Importer
	downloads string
	films     string
	ctx       context.Context
}

func newImportRig(t *testing.T) *importRig {
	t.Helper()
	base := t.TempDir()
	r := &importRig{downloads: filepath.Join(base, "downloads"), films: filepath.Join(base, "films")}
	for _, d := range []string{r.downloads, r.films} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
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
	roots := library.NewRootStore(database, r.downloads, nil)
	if _, err := roots.Create(adminCtx, r.films, library.KindMovies, "Films"); err != nil {
		t.Fatal(err)
	}
	r.store = importer.NewStore(database, time.Now)
	r.imp = importer.New(r.store, roots, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	// The authority the scheduled import really runs with.
	r.ctx = authz.SystemPrincipal(t.Context(), authz.TaskImport)
	return r
}

func TestAFinishedDownloadTheEngineLetGoIsStillImported(t *testing.T) {
	r := newImportRig(t)
	hash := strings.Repeat("ab", 20)
	name := "Blade.Runner.2049.2017.1080p.BluRay.x264-GRP"
	dir := filepath.Join(r.downloads, hash, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".mkv"), make([]byte, 20<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &engineless{data: r.downloads,
		recs: []download.Record{{InfoHash: hash, Title: name, Status: download.StatusComplete}}}
	var listed []string
	d.onDisk = func(h string) ([]download.TransferFile, error) {
		listed = append(listed, h)
		return []download.TransferFile{{Path: name + "/" + name + ".mkv", Bytes: 20 << 20, Completed: 20 << 20}}, nil
	}

	summary, err := runImports(r.ctx, d, r.store, r.imp, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0] != hash {
		t.Errorf("what was on disk was asked about %v", listed)
	}
	if !strings.Contains(summary, "imported 1") {
		t.Errorf("summary = %q", summary)
	}
	placed := filepath.Join(r.films, "Blade Runner 2049 (2017)", "Blade Runner 2049 (2017) [Bluray-1080p].mkv")
	if _, err := os.Stat(placed); err != nil {
		t.Errorf("the film is not in the library: %v", err)
	}
}

// When what it left is gone too, the reason is recorded where the queue's
// history shows it — once an hour, not every pass.
func TestAFinishedDownloadWithNothingOnDiskSaysSo(t *testing.T) {
	r := newImportRig(t)
	hash := strings.Repeat("cd", 20)
	d := &engineless{data: r.downloads,
		recs:   []download.Record{{InfoHash: hash, Title: "Some.Film.2019.1080p.BluRay.x264-GRP", Status: download.StatusComplete}},
		onDisk: func(string) ([]download.TransferFile, error) { return nil, errors.New("no such directory") }}

	summary, err := runImports(r.ctx, d, r.store, r.imp, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "skipped 1") {
		t.Errorf("summary = %q", summary)
	}
	recs, err := r.store.RecordsFor(r.ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Outcome != importer.OutcomeSkipped ||
		!strings.Contains(recs[0].Detail, "not where it left them") {
		t.Errorf("records = %+v", recs)
	}
	if again, _ := r.store.ShouldAttempt(r.ctx, hash); again {
		t.Error("a skip is retried at once rather than hourly")
	}
}

package tv

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// A fixed clock, so "aired" and "announced" mean the same thing on every run.
var testNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func clock() func() time.Time { return func() time.Time { return testNow } }

func aired(days int) time.Time { return testNow.AddDate(0, 0, days) }

// epRig is a real database with one identified series in it.
//
// Real rather than faked: the refresher's whole job is to decide what to write
// through library.EpisodeStore, and a fake store would test the refresher
// against my idea of the store rather than against the store.
type epRig struct {
	store  *library.EpisodeStore
	raw    *db.DB
	ctx    context.Context
	itemID int64
}

func newEpRig(t *testing.T) *epRig {
	t.Helper()
	database, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "cms.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	now := testNow.Format(time.RFC3339Nano)
	if _, err := database.Exec(
		`INSERT INTO root_folder (id, path, kind, label, created_at, updated_at)
		 VALUES (1, '/media/tv', 'series', 'TV', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	res, err := database.Exec(
		`INSERT INTO media_item (kind, title, year, sort_title, root_folder_id, folder,
		                         tmdb_id, added_at, updated_at)
		 VALUES ('series', 'Severance', 2022, 'severance', 1, 'Severance', 95396, ?, ?)`,
		now, now)
	if err != nil {
		t.Fatal(err)
	}
	itemID, _ := res.LastInsertId()

	return &epRig{
		store: library.NewEpisodeStore(database, clock()),
		raw:   database,
		ctx: authz.WithPrincipal(context.Background(), &authz.Principal{
			UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
			Role: authz.Role{ID: 1, Name: "Admin", Rank: 100,
				Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
		}),
		itemID: itemID,
	}
}

package music

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

const radiohead = "a74b1b7f-71a5-4011-9441-d0b5e4122711"

// ADR-0044, decision 2.
func TestMusicBrainzIsAskedPolitely(t *testing.T) {
	var mu sync.Mutex
	var seen []time.Time
	var agents, paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, time.Now())
		agents = append(agents, r.Header.Get("User-Agent"))
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/release-group"):
			_, _ = w.Write([]byte(`{"release-group-count":4,"release-groups":[
				{"id":"b1392450-e666-3926-a536-22c65f834433","title":"OK Computer","primary-type":"Album","secondary-types":[],"first-release-date":"1997-05-21"},
				{"id":"11111111-1111-1111-1111-111111111111","title":"I Might Be Wrong","primary-type":"Album","secondary-types":["Live"],"first-release-date":"2001-11-12"},
				{"id":"22222222-2222-2222-2222-222222222222","title":"My Iron Lung","primary-type":"EP","secondary-types":[],"first-release-date":"1994"},
				{"id":"33333333-3333-3333-3333-333333333333","title":"Creep","primary-type":"Single","first-release-date":"1992"}]}`))
		case strings.HasPrefix(r.URL.Path, "/release"):
			_, _ = w.Write([]byte(`{"releases":[
				{"id":"later","date":"2009-03-24","media":[{"position":1,"tracks":[{"position":1,"title":"x"}]}]},
				{"id":"first","date":"1997-05-21","media":[
					{"position":1,"tracks":[{"position":1,"title":"Airbag","length":284000,"recording":{"id":"r1"}},
					                        {"position":2,"title":"Paranoid Android","length":383000,"recording":{"id":"r2"}}]},
					{"position":2,"tracks":[{"position":1,"title":"Lull","recording":{"id":"r3"}}]}]}]}`))
		case strings.HasPrefix(r.URL.Path, "/artist/"):
			_, _ = w.Write([]byte(`{"id":"` + radiohead + `","name":"Radiohead","sort-name":"Radiohead"}`))
		default:
			_, _ = w.Write([]byte(`{"artists":[{"id":"` + radiohead + `","name":"Radiohead","country":"GB","disambiguation":"","life-span":{"begin":"1991"}}]}`))
		}
	}))
	defer srv.Close()
	mb := NewMusicBrainz(srv.Client(), srv.URL, "test")
	mb.interval = 150 * time.Millisecond
	ctx := t.Context()

	found, err := mb.SearchArtists(ctx, "radiohead")
	if err != nil || len(found) != 1 || found[0].Name != "Radiohead" || found[0].Begin != "1991" {
		t.Fatalf("search: %+v %v", found, err)
	}
	groups, err := mb.Albums(ctx, radiohead)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, g := range groups {
		titles = append(titles, g.Title+"/"+g.Type)
	}
	if strings.Join(titles, "|") != "My Iron Lung/ep|OK Computer/album" {
		t.Errorf("albums %q: want albums and EPs, no live album, no single, oldest first", titles)
	}
	release, tracks, err := mb.Tracks(ctx, "b1392450-e666-3926-a536-22c65f834433")
	if err != nil || release != "first" || len(tracks) != 3 || tracks[2].Disc != 2 || tracks[1].LengthMS != 383000 {
		t.Errorf("tracks of the earliest release: %s %+v %v", release, tracks, err)
	}

	for i, ua := range agents {
		if !strings.HasPrefix(ua, "CMediaStack/test ( ") || !strings.Contains(ua, "github.com") {
			t.Errorf("request %d sent User-Agent %q", i, ua)
		}
	}
	for i := 1; i < len(seen); i++ {
		if gap := seen[i].Sub(seen[i-1]); gap < 140*time.Millisecond {
			t.Errorf("requests %d and %d were %v apart", i-1, i, gap)
		}
	}
	if !strings.Contains(paths[1], "type=album%7Cep") || !strings.Contains(paths[2], "status=official") {
		t.Errorf("asked %q", paths)
	}
	if _, err := mb.Artist(ctx, "../../etc"); !errors.Is(err, ErrNotFound) || len(agents) != 3 {
		t.Errorf("an id that is not one was sent: %v", err)
	}
}

type fakeCatalogue struct {
	artist ArtistMatch
	groups []ReleaseGroup
	tracks map[string][]TrackInfo
	asked  []string
}

func (f *fakeCatalogue) SearchArtists(context.Context, string) ([]ArtistMatch, error) {
	return []ArtistMatch{f.artist}, nil
}
func (f *fakeCatalogue) Artist(context.Context, string) (ArtistMatch, error) { return f.artist, nil }
func (f *fakeCatalogue) Albums(context.Context, string) ([]ReleaseGroup, error) {
	f.asked = append(f.asked, "albums")
	return f.groups, nil
}
func (f *fakeCatalogue) Tracks(_ context.Context, rg string) (string, []TrackInfo, error) {
	f.asked = append(f.asked, "tracks "+rg)
	t, ok := f.tracks[rg]
	if !ok {
		return "", nil, ErrNotFound
	}
	return "release-" + rg, t, nil
}

type rig struct {
	db    *db.DB
	svc   *Service
	cat   *fakeCatalogue
	ctx   context.Context
	root  int64
	clock time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	database, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "music.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	r := &rig{db: database, clock: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
	res, err := database.ExecContext(t.Context(), `INSERT INTO root_folder (path, kind, label, created_at, updated_at)
		VALUES ('/media/music', 'music', 'Music', 'x', 'x')`)
	if err != nil {
		t.Fatal(err)
	}
	r.root, _ = res.LastInsertId()
	r.cat = &fakeCatalogue{
		artist: ArtistMatch{MBID: radiohead, Name: "Radiohead", SortName: "Radiohead"},
		groups: []ReleaseGroup{
			{MBID: "pablo", Title: "Pablo Honey", Type: "album", Released: "1993-02-22"},
			{MBID: "okc", Title: "OK Computer", Type: "album", Released: "1997-05-21"},
			{MBID: "moon", Title: "A Moon Shaped Pool", Type: "album", Released: "2016-05-08"},
			{MBID: "next", Title: "The Next One", Type: "album", Released: "2027"},
		},
		tracks: map[string][]TrackInfo{"okc": {{Disc: 1, Number: 1, Title: "Airbag"}, {Disc: 1, Number: 2, Title: "Paranoid Android"}}},
	}
	store := NewStore(database, func() time.Time { return r.clock })
	r.svc = NewService(database, store, library.NewRootStore(database, t.TempDir(), time.Now), r.cat, nil)
	r.ctx = authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 1, Username: "jacob",
		State: authz.StateActive, MFASatisfied: true, UnrestrictedLibraries: true,
		Role: authz.Role{Name: "Admin", Rank: 100, Permissions: authz.NewPermissionSet(authz.AllPermissions...)}})
	return r
}

// ADR-0044, decision 3.
func TestAnArtistIsAddedWithItsAlbums(t *testing.T) {
	r := newRig(t)
	res, err := r.svc.Add(r.ctx, AddRequest{MBID: radiohead, Monitor: MonitorLatest})
	if err != nil {
		t.Fatal(err)
	}
	if res.Item.Folder != "Radiohead" || res.Albums != 4 || res.Wanted != 1 {
		t.Errorf("added %+v", res)
	}
	albums, _ := r.svc.store.Albums(r.ctx, res.Item.ID)
	var on []string
	for _, a := range albums {
		if a.Monitored {
			on = append(on, a.Title)
		}
	}
	if strings.Join(on, "|") != "A Moon Shaped Pool|The Next One" {
		t.Errorf("latest monitors %q, want the latest released and what is to come", on)
	}
	if strings.Join(r.cat.asked, ",") != "albums" {
		t.Errorf("adding asked %q: the track lists wait until they are needed", r.cat.asked)
	}

	// Again: refused, and nothing half-written.
	if _, err := r.svc.Add(r.ctx, AddRequest{MBID: radiohead, Monitor: MonitorAll}); !errors.Is(err, importer.ErrAlreadyInLibrary) {
		t.Errorf("a second add: %v", err)
	}
	if _, err := r.svc.Add(r.ctx, AddRequest{MBID: radiohead}); !errors.Is(err, ErrNoSuchMonitoring) {
		t.Errorf("no monitoring choice: %v", err)
	}
	var albumsTotal int
	_ = r.db.QueryRowContext(r.ctx, `SELECT COUNT(*) FROM album`).Scan(&albumsTotal)
	if albumsTotal != 4 {
		t.Errorf("%d albums after the refused adds", albumsTotal)
	}

	// All or nothing: an album the database refuses leaves no artist behind.
	saved := r.cat.artist
	r.cat.artist = ArtistMatch{MBID: "8f6bd1e4-fbe1-4f50-aa9b-94c450ec0f11", Name: "Portishead"}
	r.cat.groups = append(r.cat.groups, ReleaseGroup{MBID: "bad", Title: "Bad", Type: "single"})
	if _, err := r.svc.Add(r.ctx, AddRequest{MBID: r.cat.artist.MBID, Monitor: MonitorAll}); err == nil {
		t.Error("an add whose album was refused succeeded")
	}
	var partial int
	_ = r.db.QueryRowContext(r.ctx, `SELECT COUNT(*) FROM media_item WHERE title = 'Portishead'`).Scan(&partial)
	if partial != 0 {
		t.Error("a failed add left the artist behind")
	}
	r.cat.artist, r.cat.groups = saved, r.cat.groups[:4]

	// A track list, fetched the first time the album is opened.
	var okc int64
	for _, a := range albums {
		if a.MBID == "okc" {
			okc = a.ID
		}
	}
	a, err := r.svc.Album(r.ctx, okc)
	if err != nil || len(a.Tracks) != 2 || a.ReleaseID != "release-okc" || !a.TracksKnown {
		t.Fatalf("the album: %+v %v", a, err)
	}
	if _, err := r.svc.Album(r.ctx, okc); err != nil || strings.Count(strings.Join(r.cat.asked, ","), "tracks") != 1 {
		t.Errorf("opening it again asked again: %q", r.cat.asked)
	}

	viewer := authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 2, State: authz.StateActive,
		MFASatisfied: true, UnrestrictedLibraries: true,
		Role: authz.Role{Name: "User", Rank: 10, Permissions: authz.NewPermissionSet(authz.PermBrowse)}})
	if _, err := r.svc.Add(viewer, AddRequest{MBID: radiohead, Monitor: MonitorAll}); !authz.IsDenied(err) {
		t.Errorf("a viewer added an artist: %v", err)
	}
	restricted := authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 3, State: authz.StateActive,
		MFASatisfied: true, LibraryIDs: []int64{999},
		Role: authz.Role{Name: "User", Rank: 10, Permissions: authz.NewPermissionSet(authz.PermBrowse)}})
	if _, err := r.svc.store.Album(restricted, okc); !errors.Is(err, ErrNoSuchAlbum) {
		t.Errorf("an album out of scope: %v", err)
	}
}

// ADR-0044, decision 3: wanted is monitored, released, and missing a track.
func TestWhatAnArtistIsMissing(t *testing.T) {
	r := newRig(t)
	res, err := r.svc.Add(r.ctx, AddRequest{MBID: radiohead, Monitor: MonitorAll})
	if err != nil {
		t.Fatal(err)
	}
	wanted := func() []string {
		t.Helper()
		list, err := r.svc.store.WantedAlbums(r.ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, a := range list {
			out = append(out, a.Title)
		}
		return out
	}
	// Released and monitored, nothing held; the one still to come is not wanted.
	if got := strings.Join(wanted(), "|"); got != "A Moon Shaped Pool|OK Computer|Pablo Honey" {
		t.Errorf("wanted %q", got)
	}

	// OK Computer with both tracks held is not wanted; one missing, it is.
	albums, _ := r.svc.store.Albums(r.ctx, res.Item.ID)
	var okc int64
	for _, a := range albums {
		if a.MBID == "okc" {
			okc = a.ID
		}
	}
	if _, err := r.svc.Album(r.ctx, okc); err != nil {
		t.Fatal(err)
	}
	for i, rel := range []string{"Radiohead/OK Computer/01.flac", "Radiohead/OK Computer/02.flac"} {
		fr, err := r.db.ExecContext(r.ctx, `INSERT INTO media_file (item_id, root_folder_id, relative_path,
			size_bytes, quality, revision, release_title, imported_at) VALUES (?, ?, ?, 1, 'FLAC', 0, 'x', 'x')`,
			res.Item.ID, r.root, rel)
		if err != nil {
			t.Fatal(err)
		}
		fid, _ := fr.LastInsertId()
		if _, err := r.db.ExecContext(r.ctx, `UPDATE track SET file_id = ? WHERE album_id = ? AND number = ?`,
			fid, okc, i+1); err != nil {
			t.Fatal(err)
		}
		held := strings.Contains(strings.Join(wanted(), "|"), "OK Computer")
		if i == 0 && !held {
			t.Error("an album missing a track is not wanted")
		}
		if i == 1 && held {
			t.Error("an album with every track is wanted")
		}
	}

	// Switched off, it is not wanted.
	for _, a := range albums {
		if a.MBID == "pablo" {
			if _, err := r.svc.store.SetAlbumMonitored(r.ctx, a.ID, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := strings.Join(wanted(), "|"); got != "A Moon Shaped Pool" {
		t.Errorf("after unmonitoring: %q", got)
	}
	// The one to come becomes wanted once its year begins.
	r.clock = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := strings.Join(wanted(), "|"); !strings.Contains(got, "The Next One") {
		t.Errorf("a released album is not wanted: %q", got)
	}
}

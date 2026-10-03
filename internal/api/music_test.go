package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/music"
)

type fakeCatalogue struct{}

const portishead = "8f6bd1e4-fbe1-4f50-aa9b-94c450ec0f11"

func (fakeCatalogue) SearchArtists(context.Context, string) ([]music.ArtistMatch, error) {
	return []music.ArtistMatch{{MBID: portishead, Name: "Portishead", Country: "GB", Begin: "1991"}}, nil
}
func (fakeCatalogue) Artist(context.Context, string) (music.ArtistMatch, error) {
	return music.ArtistMatch{MBID: portishead, Name: "Portishead", SortName: "Portishead"}, nil
}
func (fakeCatalogue) Albums(context.Context, string) ([]music.ReleaseGroup, error) {
	return []music.ReleaseGroup{
		{MBID: "dummy", Title: "Dummy", Type: "album", Released: "1994-08-22"},
		{MBID: "third", Title: "Third", Type: "album", Released: "2008-04-28"},
	}, nil
}
func (fakeCatalogue) Tracks(_ context.Context, rg string) (string, []music.TrackInfo, error) {
	return "rel-" + rg, []music.TrackInfo{{Disc: 1, Number: 1, Title: "Mysterons"}, {Disc: 1, Number: 2, Title: "Sour Times"}}, nil
}

// ADR-0044: an artist is followed through the API, like a series.
func TestAnArtistIsFollowed(t *testing.T) {
	w := newScopeWorld(t)

	res := w.admin.get("/api/v1/music/artists?q=portishead")
	if res.Code != http.StatusOK || !strings.Contains(res.Raw, portishead) {
		t.Fatalf("search: %d %s", res.Code, res.Raw)
	}
	if res := w.admin.post("/api/v1/media", map[string]any{"kind": "artist", "musicbrainz_id": portishead,
		"monitor": "all"}); res.Code != http.StatusCreated || res.Body["albums"] != float64(2) {
		t.Fatalf("add: %d %s", res.Code, res.Raw)
	} else {
		id := strconv.FormatInt(int64(res.Body["item"].(map[string]any)["id"].(float64)), 10)
		albums := w.admin.get("/api/v1/media/" + id + "/albums")
		if albums.Code != http.StatusOK || albums.Body["count"] != float64(2) {
			t.Fatalf("albums: %d %s", albums.Code, albums.Raw)
		}
		first := albums.Body["albums"].([]any)[0].(map[string]any)
		if first["title"] != "Dummy" || first["year"] != float64(1994) || first["tracks_known"] != false {
			t.Errorf("an album %v", first)
		}
		aid := strconv.FormatInt(int64(first["id"].(float64)), 10)
		album := w.admin.get("/api/v1/albums/" + aid)
		if album.Code != http.StatusOK || len(album.Body["tracks"].([]any)) != 2 {
			t.Errorf("an album's tracks: %d %s", album.Code, album.Raw)
		}
		if res := w.admin.do(http.MethodPut, "/api/v1/albums/"+aid+"/monitored",
			map[string]any{"monitored": false}); res.Code != http.StatusOK || res.Body["monitored"] != false {
			t.Errorf("monitoring: %d %s", res.Code, res.Raw)
		}
		wanted := w.admin.get("/api/v1/wanted")
		if !strings.Contains(wanted.Raw, `"title":"Third"`) || strings.Contains(wanted.Raw, `"title":"Dummy"`) {
			t.Errorf("wanted albums: %s", wanted.Raw)
		}
	}
	if res := w.admin.post("/api/v1/media", map[string]any{"kind": "artist", "musicbrainz_id": portishead,
		"monitor": "all"}); res.Code != http.StatusConflict {
		t.Errorf("a second add: %d %s", res.Code, res.Raw)
	}

	// Out of scope (the Music root is not the kid's), and not the viewer's to edit.
	if got := w.kid.get("/api/v1/wanted"); strings.Contains(got.Raw, "Third") || strings.Contains(got.Raw, "OK Computer") {
		t.Errorf("the kid's Wanted list holds albums out of scope: %s", got.Raw)
	}
	viewer, _ := w.scopedAccount("viewer", authz.RoleUser, []int64{w.kids, w.musicRoot}, 0)
	if res := viewer.get("/api/v1/music/artists?q=x"); res.Code != http.StatusForbidden {
		t.Errorf("a User searched MusicBrainz: %d", res.Code)
	}
	if res := viewer.get("/api/v1/media?kind=artist"); res.Code != http.StatusOK || res.Body["count"] != float64(2) {
		t.Errorf("a User with the Music library lists artists: %d %s", res.Code, res.Raw)
	}
}

const musicFeed = `<?xml version="1.0"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
<channel>
<item><title>Portishead - Dummy (1994) [MP3 320]</title>
<enclosure url="https://indexer.example.com/dl/mp3.torrent" length="100000000"/>
<torznab:attr name="seeders" value="900"/></item>
<item><title>Portishead - Dummy (1994)</title>
<enclosure url="https://indexer.example.com/dl/unknown.torrent" length="100000000"/>
<torznab:attr name="seeders" value="800"/></item>
<item><title>Portishead - Dummy Live [FLAC]</title>
<enclosure url="https://indexer.example.com/dl/live.torrent" length="400000000"/>
<torznab:attr name="seeders" value="700"/></item>
<item><title>Portishead - Dummy (1994) [FLAC]</title>
<enclosure url="https://indexer.example.com/dl/flac.torrent" length="400000000"/>
<torznab:attr name="seeders" value="5"/></item>
</channel></rss>`

// ADR-0046: an album is searched for, the matches alone carry tickets, best
// first, and the grab hands the queue the album the server matched.
func TestAnAlbumSearchGrabsIntoThatAlbum(t *testing.T) {
	w := newScopeWorld(t)
	w.r.addIndexer(w.admin, "Tracker")
	added := w.admin.post("/api/v1/media", map[string]any{"kind": "artist", "musicbrainz_id": portishead,
		"monitor": "all"})
	if added.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", added.Code, added.Raw)
	}
	artist := int64(added.Body["item"].(map[string]any)["id"].(float64))
	albums := w.admin.get("/api/v1/media/" + strconv.FormatInt(artist, 10) + "/albums")
	dummy := int64(albums.Body["albums"].([]any)[0].(map[string]any)["id"].(float64))
	path := "/api/v1/albums/" + strconv.FormatInt(dummy, 10) + "/search"

	res := w.admin.post(path, map[string]any{})
	if res.Code != http.StatusOK {
		t.Fatalf("search: %d %s", res.Code, res.Raw)
	}
	if len(w.asked) != 1 || w.asked[0] != "search portishead dummy 3000" {
		t.Errorf("the indexer was asked %q", w.asked)
	}
	cands := res.Body["candidates"].([]any)
	if len(cands) != 4 || res.Body["matches"] != float64(2) {
		t.Fatalf("%d candidates, %v matches: %s", len(cands), res.Body["matches"], res.Raw)
	}
	var tickets []string
	for i, c := range cands {
		m := c.(map[string]any)
		tk, has := m["ticket"].(string)
		if (i < 2) != has {
			t.Errorf("%d %v: ticket %v", i, m["title"], has)
		}
		if has {
			tickets = append(tickets, tk)
		}
	}
	first := cands[0].(map[string]any)
	if first["title"] != "Portishead - Dummy (1994) [FLAC]" || first["quality"] != "FLAC" {
		t.Errorf("the FLAC does not lead: %v %v", first["title"], first["quality"])
	}
	if len(tickets) == 0 {
		t.Fatal("no ticket")
	}

	grab := w.admin.post("/api/v1/releases/grab", map[string]any{"ticket": tickets[0]})
	if grab.Code != http.StatusAccepted {
		t.Fatalf("grab: %d %s", grab.Code, grab.Raw)
	}
	if f, _ := grab.Body["for"].(map[string]any); f == nil || f["kind"] != "album" ||
		f["label"] != "Portishead — Dummy (1994)" || f["album_id"] != float64(dummy) {
		t.Errorf("the grab does not say it is for the album: %v", grab.Body["for"])
	}
	w.r.downloads.mu.Lock()
	if n := len(w.r.downloads.meta); n != 1 {
		t.Errorf("%d transfers", n)
	} else if got := w.r.downloads.meta[0].Target; got == nil || *got != (download.Target{ItemID: artist, Album: dummy}) {
		t.Errorf("the queue was handed %+v, want album %d of %d", got, dummy, artist)
	}
	w.r.downloads.mu.Unlock()

	// Searching is not a User's, even one who can see the album.
	viewer, _ := w.scopedAccount("viewer", authz.RoleUser, []int64{w.musicRoot}, 0)
	if res := viewer.post(path, map[string]any{}); res.Code != http.StatusForbidden {
		t.Errorf("a User searched for an album: %d", res.Code)
	}
}

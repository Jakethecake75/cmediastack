package acquire

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// addArtist records an artist in a music root.
func (r *rig) addArtist(title string) int64 {
	r.t.Helper()
	r.exec(`INSERT OR IGNORE INTO root_folder (id, path, kind, label, created_at, updated_at)
	        VALUES (3, '/media/music', 'music', 'Music', ?, ?)`,
		r.clock.now().Format(time.RFC3339Nano), r.clock.now().Format(time.RFC3339Nano))
	res, err := r.database.ExecContext(context.Background(), `
		INSERT INTO media_item (kind, title, sort_title, root_folder_id, folder, added_at, updated_at)
		VALUES ('artist', ?, ?, 3, ?, ?, ?)`, title, strings.ToLower(title), title,
		r.clock.now().Format(time.RFC3339Nano), r.clock.now().Format(time.RFC3339Nano))
	if err != nil {
		r.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// addAlbum records an album with tracks tracks (none: the list is not read),
// the first held of them held by a file.
func (r *rig) addAlbum(artist int64, title, released string, tracks, held int) int64 {
	r.t.Helper()
	now := r.clock.now().Format(time.RFC3339Nano)
	var rel any
	if released != "" {
		rel = released
	}
	res, err := r.database.ExecContext(context.Background(), `
		INSERT INTO album (item_id, musicbrainz_id, title, album_type, released_at, created_at, updated_at)
		VALUES (?, ?, ?, 'album', ?, ?, ?)`, artist, fmt.Sprintf("mb-%s-%s", title, released), title, rel, now, now)
	if err != nil {
		r.t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	for n := 1; n <= tracks; n++ {
		var file any
		if n <= held {
			fr, err := r.database.ExecContext(context.Background(), `
				INSERT INTO media_file (item_id, root_folder_id, relative_path, imported_at)
				VALUES (?, 3, ?, ?)`, artist, fmt.Sprintf("%d/%d/%02d.flac", artist, id, n), now)
			if err != nil {
				r.t.Fatal(err)
			}
			file, _ = fr.LastInsertId()
		}
		r.exec(`INSERT INTO track (album_id, disc, number, title, file_id) VALUES (?, 1, ?, ?, ?)`,
			id, n, fmt.Sprintf("Track %d", n), file)
	}
	if tracks > 0 {
		r.exec(`UPDATE album SET tracks_refreshed_at = ? WHERE id = ?`, now, id)
	}
	return id
}

func (r *rig) runAlbums() string {
	r.t.Helper()
	s, err := r.svc.RunAlbums(r.ctx)
	if err != nil {
		r.t.Fatalf("album pass: %v", err)
	}
	return s
}

// albumTargets is what the queue's automatic rows were grabbed for, as albums.
func (r *rig) albumTargets() []string {
	var out []string
	for _, m := range r.queue.added() {
		if m.Target != nil && m.Target.Album > 0 {
			out = append(out, fmt.Sprintf("%d/%d", m.Target.ItemID, m.Target.Album))
		} else {
			out = append(out, "not an album")
		}
	}
	return out
}

// ADR-0047, decisions 1, 2 and 4: a wanted album with its track list is
// searched for as a person's search would, the best release that is the album
// is grabbed and sealed to it, and the grab is audited as the machine's.
func TestAWantedAlbumIsFetched(t *testing.T) {
	r := newRig(t, Config{})
	portishead := r.addArtist("Portishead")
	dummy := r.addAlbum(portishead, "Dummy", "1994-08-22", 3, 0)
	third := r.addAlbum(portishead, "Third", "2008-04-28", 0, 0) // the list not read
	r.client.byTerm["portishead dummy"] = []indexer.Result{
		rel("Portishead - Dummy (1994) [MP3 320]", 1, 900),
		rel("Portishead - Dummy (1994) [FLAC]", 2, 5),
		rel("Portishead - Dummy Live [FLAC]", 3, 700),
		rel("Portishead - Dummy (1994)", 4, 800),
	}

	summary := r.runAlbums()
	if got := r.albumTargets(); len(got) != 1 || got[0] != fmt.Sprintf("%d/%d", portishead, dummy) {
		t.Fatalf("grabbed %v: %s", got, summary)
	}
	queries, fetched := r.client.snapshot()
	if len(queries) != 1 || queries[0].Term != "portishead dummy" || len(queries[0].Categories) != 1 ||
		queries[0].Categories[0] != 3000 {
		t.Errorf("asked %+v; Third, whose track list is not read, must not be searched for", queries)
	}
	if len(fetched) != 1 || !strings.HasSuffix(fetched[0], hash(2)) {
		t.Errorf("fetched %v, want the FLAC", fetched)
	}
	if !strings.Contains(summary, "grabbed Portishead — Dummy (1994)") {
		t.Errorf("summary %q", summary)
	}
	if m := r.queue.added()[0]; m.AddedLabel != Label || m.Title != "Portishead - Dummy (1994) [FLAC]" {
		t.Errorf("queued as %+v", m)
	}
	st, ok := r.state(StateKey{Album: true, ID: dummy})
	if !ok || st.Outcome != OutcomeGrabbed {
		t.Errorf("state %+v %v", st, ok)
	}
	if _, ok := r.state(StateKey{Album: true, ID: third}); ok {
		t.Error("Third has a state; it was never looked for")
	}
	var line string
	if err := r.database.QueryRowContext(context.Background(), `SELECT detail FROM audit_event
		WHERE action = ? AND actor_label = ?`, audit.ActionReleaseGrabbed, Label).Scan(&line); err != nil ||
		!strings.Contains(line, "for Portishead — Dummy (1994)") {
		t.Errorf("audit %q %v", line, err)
	}

	// Downloading: not searched again.
	r.client.reset()
	if s := r.runAlbums(); !strings.Contains(s, "1 downloading") {
		t.Errorf("while downloading: %s", s)
	}
	inFlight, err := r.store.InFlight(r.ctx)
	if err != nil || !inFlight[Key{Album: dummy}] || inFlight[Key{ItemID: portishead}] {
		t.Errorf("in flight %v %v: the album, and no episode of the artist", inFlight, err)
	}

	// Imported with tracks still missing: grabbed once, not again.
	if err := r.queueDB.SetStatus(context.Background(), hash(2), download.StatusComplete); err != nil {
		t.Fatal(err)
	}
	r.imported(2, "imported")
	r.clock.advance(8 * 24 * time.Hour)
	if s := r.runAlbums(); !strings.Contains(s, "1 imported once already") {
		t.Errorf("after the import: %s", s)
	}
	if q, _ := r.client.snapshot(); len(q) != 0 {
		t.Errorf("searched again for an album already imported once: %+v", q)
	}
	rep, err := r.svc.Report(r.ctx)
	if err != nil || !rep.AlbumsImported[dummy] {
		t.Errorf("the report does not say it was imported once: %+v %v", rep.AlbumsImported, err)
	}
}

// ADR-0047, decisions 1, 2 and 4: what is not wanted, not open, not due, or
// not grabbable is left alone, and the budget holds.
func TestAnAlbumIsFetchedOnlyByTheRules(t *testing.T) {
	r := newRig(t, Config{SearchesPerRun: 1})
	weezer := r.addArtist("Weezer")
	blue := r.addAlbum(weezer, "Weezer", "1994-05-10", 10, 0)
	green := r.addAlbum(weezer, "Weezer", "2001-05-15", 10, 0)
	r.addAlbum(weezer, "Pinkerton", "1996-09-24", 10, 10)       // held in full
	r.addAlbum(weezer, "Next", "2099-01-01", 10, 0)             // not released
	off := r.addAlbum(weezer, "Maladroit", "2002-05-14", 10, 0) // unmonitored below
	r.exec(`UPDATE album SET monitored = 0 WHERE id = ?`, off)

	wanted, err := r.store.WantedAlbums(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(wanted) != 2 || wanted[0].Album != green || wanted[1].Album != blue ||
		!wanted[0].Namesake || wanted[0].Year != 2001 || wanted[0].Artist != "Weezer" {
		t.Fatalf("wanted %+v", wanted)
	}

	// The tunnel is down: nothing is asked.
	r.closeGate("the tunnel is down")
	if s := r.runAlbums(); !strings.Contains(s, "the tunnel is down") {
		t.Errorf("gate closed: %s", s)
	}
	r.closeGate("")

	// One search a pass, newest first. A namesake's release naming no year is
	// not it; the one with no seeders is not grabbable.
	r.client.byTerm["weezer weezer"] = []indexer.Result{
		rel("Weezer - Weezer [FLAC]", 1, 50),
		rel("Weezer - Weezer (2001) [FLAC]", 2, 0),
		rel("Weezer - Weezer (1994) [FLAC]", 3, 40),
	}
	s := r.runAlbums()
	if len(r.queue.added()) != 0 {
		t.Fatalf("grabbed %v: %s", r.albumTargets(), s)
	}
	if !strings.Contains(s, "searched for 1 of 2 due") || !strings.Contains(s, "no seeders") {
		t.Errorf("summary %q", s)
	}
	st, _ := r.state(StateKey{Album: true, ID: green})
	if st.Outcome != OutcomeNothing || st.Fruitless != 1 || !st.NextAt.Equal(r.clock.now().Add(FirstBackoff)) {
		t.Errorf("green's state %+v", st)
	}

	// The next pass takes the other; the 1994 release is the Blue Album.
	if s := r.runAlbums(); len(r.albumTargets()) != 1 || r.albumTargets()[0] != fmt.Sprintf("%d/%d", weezer, blue) {
		t.Fatalf("grabbed %v: %s", r.albumTargets(), s)
	}

	// Green waits out its back-off, then is searched again; a release that
	// was ever in the queue is never grabbed again.
	r.client.reset()
	if s := r.runAlbums(); !strings.Contains(s, "no album is due") {
		t.Errorf("during the back-off: %s", s)
	}
	r.clock.advance(FirstBackoff)
	r.client.byTerm["weezer weezer"] = []indexer.Result{rel("Weezer - Weezer (2001) [FLAC]", 3, 40)}
	s = r.runAlbums()
	if len(r.albumTargets()) != 1 || !strings.Contains(s, "grabbed before") {
		t.Errorf("a release already queued was grabbed again: %v %s", r.albumTargets(), s)
	}
	if _, fetched := r.client.snapshot(); len(fetched) != 0 {
		t.Errorf("a release already in the queue was fetched to find that out: %v", fetched)
	}
	if st, _ := r.state(StateKey{Album: true, ID: green}); st.Fruitless != 2 {
		t.Errorf("the back-off did not grow: %+v", st)
	}
}

// ADR-0047, decision 1: the grab asks again, as an episode's does — an album a
// person stopped wanting while the indexers were being asked is not fetched.
func TestAWantedAlbumIsStillWantedAtTheGrab(t *testing.T) {
	r := newRig(t, Config{})
	artist := r.addArtist("Portishead")
	dummy := r.addAlbum(artist, "Dummy", "1994-08-22", 3, 0)
	r.client.byTerm["portishead dummy"] = []indexer.Result{rel("Portishead - Dummy (1994) [FLAC]", 1, 5)}
	r.client.onSearch = func(indexer.Query) {
		r.exec(`UPDATE album SET monitored = 0 WHERE id = ?`, dummy)
	}
	s := r.runAlbums()
	if len(r.queue.added()) != 0 || !strings.Contains(s, "no longer wanted") {
		t.Errorf("grabbed %v: %s", r.albumTargets(), s)
	}
}

// misjudging is a search that gets it wrong: it hands a release for another
// album, and a refused release, targets as though they were this album's.
type misjudging struct{ Finder }

func (m misjudging) SearchAlbum(ctx context.Context, as search.AlbumSearch) (search.Response, error) {
	resp, err := m.Finder.SearchAlbum(ctx, as)
	for i := range resp.Candidates {
		c := &resp.Candidates[i]
		switch c.Title {
		case "Portishead - Third (2008) [FLAC]":
			c.Accepted, c.Rejection = true, nil
			c.Target = &search.Target{ItemID: as.Want.ItemID, Album: as.Want.AlbumID + 1}
		case "Portishead - Dummy (1994)":
			c.Target = &search.Target{ItemID: as.Want.ItemID, Album: as.Want.AlbumID}
		}
	}
	return resp, err
}

// ADR-0047, decision 4: whatever the search says, only a release sealed to
// this album and accepted is grabbed for it.
func TestOnlyTheAlbumsOwnAcceptedReleaseIsGrabbed(t *testing.T) {
	r := newRig(t, Config{})
	r.svc.finder = misjudging{r.svc.finder}
	artist := r.addArtist("Portishead")
	r.addAlbum(artist, "Dummy", "1994-08-22", 3, 0)
	r.client.byTerm["portishead dummy"] = []indexer.Result{
		rel("Portishead - Third (2008) [FLAC]", 1, 900),
		rel("Portishead - Dummy (1994)", 2, 800),
	}
	s := r.runAlbums()
	if len(r.queue.added()) != 0 {
		t.Errorf("grabbed %v: %s", r.albumTargets(), s)
	}
	if !strings.Contains(s, "does not say whether it is FLAC") {
		t.Errorf("the refusal is not said: %s", s)
	}
}

// ADR-0062: with upgrades on, an album held lossy is searched for a lossless
// release after the wanted albums, once a week; a lossy release, however
// popular, is no upgrade; an album held lossless, or one the wanted pass has
// still to fetch, is not looked at; with upgrades off, nothing is.
func TestALossyAlbumIsUpgradedToLossless(t *testing.T) {
	r := newRig(t, Config{Upgrades: true})
	portishead := r.addArtist("Portishead")
	dummy := r.addAlbum(portishead, "Dummy", "1994-08-22", 3, 3)
	third := r.addAlbum(portishead, "Third", "2008-04-28", 2, 2)
	roseland := r.addAlbum(portishead, "Roseland NYC Live", "1998-11-02", 3, 1)
	r.exec(`UPDATE media_file SET quality = 'FLAC'`)
	r.exec(`UPDATE media_file SET quality = 'MP3' WHERE id IN (SELECT file_id FROM track WHERE album_id IN (?, ?))`, dummy, roseland)
	r.exec(`UPDATE media_file SET quality = 'AAC' WHERE id = (SELECT file_id FROM track WHERE album_id = ? AND number = 2)`, dummy)
	r.client.byTerm["portishead dummy"] = []indexer.Result{rel("Portishead - Dummy (1994) [MP3 320]", 1, 900)}
	// Unmonitored, and held lossy: not upgraded.
	pnc := r.addAlbum(portishead, "PNYC", "1998-01-01", 2, 2)
	r.exec(`UPDATE media_file SET quality = 'MP3' WHERE id IN (SELECT file_id FROM track WHERE album_id = ?)`, pnc)
	r.exec(`UPDATE album SET monitored = 0 WHERE id = ?`, pnc)
	// Still the wanted pass's — searched long ago, its back-off not yet run
	// out — and not to be searched as an upgrade beside it.
	r.exec(`INSERT INTO acquire_state (album_id, searched_at, next_at, fruitless, outcome)
	        VALUES (?, ?, ?, 3, 'nothing')`, roseland,
		r.clock.now().Add(-8*24*time.Hour).Format(time.RFC3339Nano), r.clock.now().Add(24*time.Hour).Format(time.RFC3339Nano))

	summary := r.runAlbums()
	queries, _ := r.client.snapshot()
	terms := map[string]bool{}
	for _, q := range queries {
		terms[q.Term] = true
	}
	if !terms["portishead dummy"] || terms["portishead third"] || terms["portishead pnyc"] ||
		terms["portishead roseland nyc live"] || len(r.albumTargets()) != 0 {
		t.Fatalf("asked %v, grabbed %v: %s", terms, r.albumTargets(), summary)
	}
	if !strings.Contains(summary, "Upgrades: searched for 1 of 1 due album upgrade(s)") ||
		!strings.Contains(summary, "Dummy (1994) (an upgrade from AAC)") || !strings.Contains(summary, "not lossless") {
		t.Errorf("summary %q", summary)
	}
	if _, ok := r.state(StateKey{Album: true, ID: third}); ok {
		t.Error("an album held lossless was looked at")
	}

	// A day later: not due. A week after the search: a FLAC release, grabbed.
	r.client.reset()
	r.clock.advance(24 * time.Hour)
	if s := r.runAlbums(); !strings.Contains(s, "Upgrades: no album upgrade is due") {
		t.Errorf("a day later: %s", s)
	}
	r.client.byTerm["portishead dummy"] = []indexer.Result{
		rel("Portishead - Dummy (1994) [MP3 320]", 1, 900),
		rel("Portishead - Dummy (1994) [FLAC]", 2, 4),
	}
	r.clock.advance(6 * 24 * time.Hour)
	summary = r.runAlbums()
	if got := r.albumTargets(); len(got) != 1 || got[0] != fmt.Sprintf("%d/%d", portishead, dummy) {
		t.Fatalf("grabbed %v: %s", got, summary)
	}
	if _, fetched := r.client.snapshot(); len(fetched) != 1 || !strings.HasSuffix(fetched[0], hash(2)) {
		t.Errorf("fetched %v, want the FLAC", fetched)
	}

	// Off: the same library, no upgrade looked for.
	off := newRig(t, Config{})
	a := off.addArtist("Portishead")
	off.addAlbum(a, "Dummy", "1994-08-22", 3, 3)
	off.exec(`UPDATE media_file SET quality = 'MP3'`)
	off.client.byTerm["portishead dummy"] = []indexer.Result{rel("Portishead - Dummy (1994) [FLAC]", 2, 4)}
	if s := off.runAlbums(); strings.Contains(s, "Upgrades") || len(off.albumTargets()) != 0 {
		t.Errorf("with upgrades off: %s", s)
	}
}

// ADR-0062, decisions 2 and 3, at their edges: an album the wanted pass just
// fetched lossy waits a week from that search before it is upgraded; and one
// the wanted pass has still to fetch is not taken by the upgrades when the
// wanted pass's budget ran out before it.
func TestAnAlbumUpgradeWaitsItsTurn(t *testing.T) {
	r := newRig(t, Config{Upgrades: true, SearchesPerRun: 1})
	artist := r.addArtist("Portishead")
	fetched := r.addAlbum(artist, "Portishead", "1997-09-29", 2, 2)
	r.addAlbum(artist, "Third", "2008-04-28", 2, 0)
	roseland := r.addAlbum(artist, "Roseland NYC Live", "1998-11-02", 3, 1)
	r.exec(`UPDATE media_file SET quality = 'MP3'`)
	two := r.clock.now().Add(-2 * 24 * time.Hour).Format(time.RFC3339Nano)
	r.exec(`INSERT INTO acquire_state (album_id, searched_at, next_at, outcome) VALUES (?, ?, ?, 'grabbed')`,
		fetched, two, two)

	r.runAlbums()
	queries, _ := r.client.snapshot()
	for _, q := range queries {
		if q.Term == "portishead portishead" || q.Term == "portishead roseland nyc live" {
			t.Errorf("asked for %q: %+v", q.Term, queries)
		}
	}
	if _, ok := r.state(StateKey{Album: true, ID: roseland}); ok {
		t.Error("the wanted album was searched as an upgrade")
	}
	r.client.reset()
	r.clock.advance(5 * 24 * time.Hour)
	r.runAlbums()
	asked := false
	queries, _ = r.client.snapshot()
	for _, q := range queries {
		asked = asked || q.Term == "portishead portishead"
	}
	if !asked {
		t.Errorf("a week after the search that fetched it, not looked for: %+v", queries)
	}
}

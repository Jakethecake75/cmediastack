package acquire

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/library"
)

// Season packs, fetched without a person (ADR-0033, decision 5): only for a
// settled season every episode of which is wanted, and only a pack seen to
// hold it.

const pack1 = "Severance.S01.1080p.WEB-DL.DDP5.1.H.264-GRP"

// packTorrent is a .torrent holding one file per named episode of season 1,
// and an .nfo.
func packTorrent(t *testing.T, episodes ...int) []byte {
	t.Helper()
	return seasonTorrent(t, 1, episodes...)
}

// seasonTorrent is packTorrent for any season.
func seasonTorrent(t *testing.T, season int, episodes ...int) []byte {
	t.Helper()
	dir := filepath.Join(t.TempDir(), pack1)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, e := range episodes {
		name := fmt.Sprintf("Severance.S%02dE%02d.1080p.WEB-DL-GRP.mkv", season, e)
		if err := os.WriteFile(filepath.Join(dir, name), make([]byte, 9<<20), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "Severance.S01.nfo"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 1 << 20}
	if err := info.BuildFromFilePath(dir); err != nil {
		t.Fatal(err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := (&metainfo.MetaInfo{InfoBytes: infoBytes}).Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// settledSeverance is a series of one three-episode season, the last of which
// aired more than a week ago, with nothing on disk.
func settledSeverance(r *rig) int64 {
	r.t.Helper()
	id := r.addSeries("Severance", 2022, 95396, 3)
	r.clock.advance(8 * 24 * time.Hour)
	return id
}

func TestAPackIsGrabbedForASettledSeasonWhollyWanted(t *testing.T) {
	t.Run("by searching", func(t *testing.T) {
		r := newRig(t, Config{})
		sev := settledSeverance(r)
		r.client.byTerm["severance"] = []indexer.Result{
			rel(pack1, 1, 20),
			rel("Severance.S01E01.1080p.WEB-DL.H264-GRP", 2, 90),
		}
		r.client.torrents = map[string][]byte{hash(1): packTorrent(t, 1, 2, 3)}

		summary := r.runSearch()

		if got := r.targets(); fmt.Sprint(got) != fmt.Sprintf("[%d S01]", sev) {
			t.Fatalf("grabbed for %v, want the season as one pack (summary: %s)", got, summary)
		}
		// Found running the binary: the summary said "searched for 1 of 3
		// due ... 2 more wait for the next pass" when the season's search had
		// answered for all three.
		if !strings.Contains(summary, "searched for 3 of 3 due") || strings.Contains(summary, "more wait") {
			t.Errorf("summary %q; the season's search answers for every episode of it", summary)
		}
		queries, _ := r.client.snapshot()
		if len(queries) != 1 || queries[0].Season != 1 || queries[0].Episode != 0 {
			t.Errorf("queries %+v; want one search, for the season", queries)
		}
		for e := 1; e <= 3; e++ {
			st, ok := r.state(StateKey{ID: r.episodeID(sev, 1, e)})
			if !ok || st.Outcome != OutcomeGrabbed || !strings.Contains(st.Detail, pack1) {
				t.Errorf("E%02d: %+v; every episode of the season must say the pack was grabbed", e, st)
			}
		}
		var detail string
		if err := r.database.QueryRowContext(context.Background(),
			`SELECT detail FROM audit_event WHERE action = 'acquisition.grabbed'`).Scan(&detail); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(detail, "for Severance S01 (the whole season)") {
			t.Errorf("audit detail %q", detail)
		}
		// Nothing of the season is grabbed again while the pack is under way.
		r.clock.advance(time.Hour)
		r.client.reset()
		r.runSearch()
		if got := r.targets(); len(got) != 1 {
			t.Errorf("grabbed %v; the season's download holds every episode of it", got)
		}
	})

	t.Run("from the recent releases", func(t *testing.T) {
		r := newRig(t, Config{})
		sev := settledSeverance(r)
		r.client.feed = []indexer.Result{rel(pack1, 1, 20)}
		r.client.torrents = map[string][]byte{hash(1): packTorrent(t, 1, 2, 3)}
		summary := r.runRecent()
		if got := r.targets(); fmt.Sprint(got) != fmt.Sprintf("[%d S01]", sev) {
			t.Fatalf("grabbed for %v (summary: %s)", got, summary)
		}
	})
}

func TestAPackIsNotGrabbedUnlessEverythingInItIsWanted(t *testing.T) {
	offer := func(r *rig) {
		r.client.byTerm["severance"] = []indexer.Result{
			rel(pack1, 1, 90),
			rel("Severance.S01E01.1080p.WEB-DL.H264-GRP", 2, 20),
			rel("Severance.S01E03.1080p.WEB-DL.H264-GRP", 3, 20),
		}
		r.client.feed = r.client.byTerm["severance"]
		r.client.torrents = map[string][]byte{hash(1): packTorrent(r.t, 1, 2, 3)}
	}
	noPack := func(t *testing.T, r *rig, sev int64) {
		t.Helper()
		for _, got := range r.targets() {
			if got == fmt.Sprintf("%d S01", sev) {
				t.Fatalf("the pack was grabbed: %v", r.targets())
			}
		}
	}

	t.Run("one episode already on disk", func(t *testing.T) {
		r := newRig(t, Config{})
		sev := settledSeverance(r)
		r.haveEpisodes(sev, 1, 2, 2)
		offer(r)
		// Not looked at, rather than looked at and refused every fifteen
		// minutes: the season is not one a pack may be grabbed for.
		if summary := r.runRecent(); strings.Contains(summary, "whole season") {
			t.Errorf("summary %q; a pack of a season not wholly wanted is not looked at", summary)
		}
		noPack(t, r, sev)
		r.runSearch()
		noPack(t, r, sev)
		got := fmt.Sprint(r.targets())
		if !strings.Contains(got, fmt.Sprintf("%d S01E01", sev)) || !strings.Contains(got, fmt.Sprintf("%d S01E03", sev)) {
			t.Errorf("grabbed %s; the rest of the season comes one episode at a time", got)
		}
	})
	t.Run("the season is still airing", func(t *testing.T) {
		r := newRig(t, Config{})
		sev := r.addSeries("Severance", 2022, 95396, 3) // last episode a day ago
		offer(r)
		r.runSearch()
		noPack(t, r, sev)
		r.runRecent()
		noPack(t, r, sev)
	})
	t.Run("one episode not monitored", func(t *testing.T) {
		r := newRig(t, Config{})
		sev := settledSeverance(r)
		r.exec(`UPDATE episode SET monitored = 0 WHERE id = ?`, r.episodeID(sev, 1, 2))
		offer(r)
		r.runSearch()
		noPack(t, r, sev)
		r.runRecent()
		noPack(t, r, sev)
	})
	t.Run("specials", func(t *testing.T) {
		r := newRig(t, Config{})
		sev := r.addItem("series", "Severance", 2022, 95396, r.clock.now())
		var eps []library.EpisodeInput
		for e := 1; e <= 3; e++ {
			eps = append(eps, library.EpisodeInput{ProviderID: int64(e), Number: e,
				Aired: r.clock.now().AddDate(0, 0, -30+e)})
		}
		if err := r.episodes.Upsert(r.browse(), sev, []library.SeasonInput{
			{Number: 0, EpisodeCount: 3, Aired: eps[0].Aired, Episodes: eps}}); err != nil {
			t.Fatal(err)
		}
		r.exec(`UPDATE episode SET monitored = 1 WHERE item_id = ?`, sev)
		r.exec(`UPDATE season SET monitored = 1 WHERE item_id = ?`, sev)
		r.client.feed = []indexer.Result{rel("Severance.S00.1080p.WEB-DL-GRP", 1, 90)}
		// A real pack of it, so nothing but the rule about season 0 refuses it.
		r.client.torrents = map[string][]byte{hash(1): seasonTorrent(t, 0, 1, 2, 3)}
		r.runRecent()
		noPack(t, r, sev)
		if got := r.targets(); len(got) != 0 {
			t.Errorf("grabbed %v for specials", got)
		}
	})
}

func TestAPackMustBeSeenToHoldTheSeason(t *testing.T) {
	t.Run("part of the season", func(t *testing.T) {
		r := newRig(t, Config{})
		sev := settledSeverance(r)
		r.client.byTerm["severance"] = []indexer.Result{rel(pack1, 1, 90)}
		r.client.torrents = map[string][]byte{hash(1): packTorrent(t, 1, 2)}

		r.runSearch()
		if got := r.targets(); len(got) != 0 {
			t.Fatalf("grabbed %v; the pack holds two of three episodes", got)
		}
		st, _ := r.state(StateKey{ID: r.episodeID(sev, 1, 3)})
		if st.Outcome != OutcomeNothing || !strings.Contains(st.Detail, "holds 2 of the season's 3 episodes") {
			t.Errorf("state %+v; want nothing grabbed, saying what the pack holds", st)
		}
		// One search answered for the season: its other episodes are not
		// searched for again in the same pass.
		if queries, _ := r.client.snapshot(); len(queries) != 1 {
			t.Errorf("%d searches in one pass; the season's one answered for all of it", len(queries))
		}

		// Remembered: the next look does not fetch it again to find the same
		// thing out.
		_, before := r.client.snapshot()
		r.client.feed = []indexer.Result{rel(pack1, 1, 90)}
		r.runRecent()
		if _, after := r.client.snapshot(); len(after) != len(before) {
			t.Errorf("fetched %d times, then %d; a pack found short was fetched again", len(before), len(after))
		}
	})

	t.Run("a magnet link", func(t *testing.T) {
		r := newRig(t, Config{})
		sev := settledSeverance(r)
		r.client.byTerm["severance"] = []indexer.Result{rel(pack1, 1, 90)} // answered with a magnet
		r.runSearch()
		if got := r.targets(); len(got) != 0 {
			t.Fatalf("grabbed %v from a magnet link", got)
		}
		st, _ := r.state(StateKey{ID: r.episodeID(sev, 1, 1)})
		if !strings.Contains(st.Detail, "magnet") {
			t.Errorf("state %+v; want the refusal to say it was a magnet link", st)
		}
	})
}

func TestASeasonDownloadHoldsEveryEpisode(t *testing.T) {
	r := newRig(t, Config{})
	sev := settledSeverance(r)
	season := &download.Target{ItemID: sev, Season: 1, Pack: true}
	r.queued(1, pack1, download.StatusDownloading, season)

	inFlight := func() map[Key]bool {
		t.Helper()
		m, err := r.store.InFlight(r.ctx)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	for e := 1; e <= 3; e++ {
		if !inFlight()[Key{ItemID: sev, Season: 1, Episode: e}] {
			t.Errorf("E%02d is not in flight while its season downloads", e)
		}
	}

	if err := r.queueDB.SetStatus(context.Background(), hash(1), download.StatusComplete); err != nil {
		t.Fatal(err)
	}
	if len(inFlight()) != 3 {
		t.Error("a finished pack not yet imported must still hold its season")
	}
	file := func(path, outcome string) {
		r.exec(`INSERT INTO import_record (info_hash, outcome, detail, source_path, occurred_at)
		        VALUES (?, ?, 'x', ?, ?)`, hash(1), outcome, path, r.clock.now().Format(time.RFC3339Nano))
		r.clock.advance(time.Second)
	}
	file("e1.mkv", "imported")
	file("e2.mkv", "failed")
	if len(inFlight()) != 3 {
		t.Error("with a file of the pack failing to import, the season must stay held")
	}
	file("e2.mkv", "imported")
	file("extra.mkv", "skipped")
	if got := inFlight(); len(got) != 0 {
		t.Errorf("in flight %v once every file was imported or skipped; want nothing held", got)
	}
}

// With nothing to read a pack's file list, no pack is grabbed at all: its
// episodes come one at a time (ADR-0033, decision 5).
func TestWithNoPackCheckNoPackIsGrabbed(t *testing.T) {
	r := newRig(t, Config{})
	sev := settledSeverance(r)
	r.svc.packs = nil
	r.client.byTerm["severance"] = []indexer.Result{
		rel(pack1, 1, 90),
		rel("Severance.S01E01.1080p.WEB-DL.H264-GRP", 2, 20),
	}
	r.client.feed = r.client.byTerm["severance"]
	r.client.torrents = map[string][]byte{hash(1): packTorrent(t, 1, 2, 3)}
	r.runSearch()
	r.runRecent()
	for _, got := range r.targets() {
		if got == fmt.Sprintf("%d S01", sev) {
			t.Fatalf("grabbed %v with no way to read the pack", r.targets())
		}
	}
	if len(r.targets()) == 0 {
		t.Error("nothing was grabbed; the season's episodes still come one at a time")
	}
}

// A pack of several seasons holds every season in its span while it is under
// way, so none of them is fetched again beside it (ADR-0057, decision 5).
func TestAPackOfSeveralSeasonsHoldsEachOfThem(t *testing.T) {
	r := newRig(t, Config{})
	sev := settledSeverance(r)
	r.exec(`INSERT INTO season (id, item_id, number, episode_count, updated_at) VALUES (902, ?, 2, 2, 'x')`, sev)
	for _, n := range []int{1, 2} {
		r.exec(`INSERT INTO episode (item_id, season_id, season_number, number, updated_at)
		        VALUES (?, 902, 2, ?, 'x')`, sev, n)
	}
	r.queued(1, "Severance.S01-S02.1080p.WEB-DL.H264-GRP", download.StatusDownloading,
		&download.Target{ItemID: sev, Season: 1, LastSeason: 2, Pack: true})
	m, err := r.store.InFlight(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []Key{{ItemID: sev, Season: 1, Episode: 1}, {ItemID: sev, Season: 1, Episode: 3},
		{ItemID: sev, Season: 2, Episode: 1}, {ItemID: sev, Season: 2, Episode: 2}} {
		if !m[k] {
			t.Errorf("%+v is not in flight while its seasons download", k)
		}
	}
	if len(m) != 5 {
		t.Errorf("%d in flight, want the five episodes of seasons 1 and 2", len(m))
	}
}

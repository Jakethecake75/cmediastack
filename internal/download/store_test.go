package download

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

func testStore(t *testing.T) (*Store, *db.DB) {
	t.Helper()
	database, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "q.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return NewStore(database, nil), database
}

func hashOf(n byte) string { return strings.Repeat(string('a'+rune(n%6)), 40) }

func TestAQueueRowRoundTrips(t *testing.T) {
	s, _ := testStore(t)

	want := Record{
		InfoHash: hashOf(0), Title: "Some.Movie.2019.1080p.BluRay-GRP",
		IndexerID: 3, IndexerName: "Tracker",
		Torrent: []byte("d4:infod6:lengthi1eee"), AddedLabel: "jacob",
		SeedRatio: 1.5, SeedTime: 48 * time.Hour,
	}
	if err := s.Put(t.Context(), want); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(t.Context(), want.InfoHash)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != want.Title || got.IndexerName != want.IndexerName {
		t.Errorf("got %+v", got)
	}
	if string(got.Torrent) != string(want.Torrent) {
		t.Errorf("torrent bytes = %q", got.Torrent)
	}
	if got.SeedTime != want.SeedTime || got.SeedRatio != want.SeedRatio {
		t.Errorf("seed policy = %v / %v", got.SeedRatio, got.SeedTime)
	}
	if got.Status != StatusQueued {
		t.Errorf("status = %q", got.Status)
	}
}

// Re-grabbing something already queued is an ordinary thing to do — an operator
// retrying a stalled transfer — and it must neither fail nor duplicate.
func TestReGrabbingUpdatesRatherThanDuplicating(t *testing.T) {
	s, _ := testStore(t)
	h := hashOf(1)

	if err := s.Put(t.Context(), Record{InfoHash: h, Title: "first", Magnet: "magnet:?xt=urn:btih:x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(t.Context(), Record{InfoHash: h, Title: "second", Magnet: "magnet:?xt=urn:btih:y"}); err != nil {
		t.Fatal(err)
	}

	rows, err := s.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Title != "second" {
		t.Errorf("title = %q, want the refreshed one", rows[0].Title)
	}
}

// A row must carry exactly one payload. Neither cannot be replayed; both is
// ambiguous about which was used.
func TestARowNeedsExactlyOnePayload(t *testing.T) {
	s, _ := testStore(t)

	if err := s.Put(t.Context(), Record{InfoHash: hashOf(2), Title: "x"}); err == nil {
		t.Error("a row with no payload was accepted")
	}
	if err := s.Put(t.Context(), Record{
		InfoHash: hashOf(3), Title: "x",
		Magnet: "magnet:?xt=urn:btih:z", Torrent: []byte("d"),
	}); err == nil {
		t.Error("a row with both payloads was accepted")
	}
}

func TestTheStoreRefusesAnythingThatIsNotAnInfoHash(t *testing.T) {
	s, _ := testStore(t)

	for _, bad := range []string{"", "../../etc/passwd", "zzz", strings.Repeat("a", 64)} {
		if err := s.Put(t.Context(), Record{InfoHash: bad, Title: "x", Magnet: "m"}); err == nil {
			t.Errorf("Put(%q) was accepted", bad)
		}
		if err := s.SetStatus(t.Context(), bad, StatusComplete); err == nil {
			t.Errorf("SetStatus(%q) was accepted", bad)
		}
	}
}

// An operator who stopped a transfer would be entitled to be angry if
// restarting the service started it again.
func TestAStoppedTransferIsNotResumable(t *testing.T) {
	s, _ := testStore(t)

	queued, stopped, complete := hashOf(0), hashOf(1), hashOf(2)
	for h, status := range map[string]string{
		queued: StatusQueued, stopped: StatusStopped, complete: StatusComplete,
	} {
		if err := s.Put(t.Context(), Record{
			InfoHash: h, Title: h, Magnet: "magnet:?xt=urn:btih:" + h, Status: status,
		}); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := s.Resumable(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].InfoHash != queued {
		t.Fatalf("resumable = %+v, want only the queued one", rows)
	}
}

// Completing stamps a time. An operator asking "when did this instance acquire
// that" needs an answer.
func TestCompletionIsTimestamped(t *testing.T) {
	s, _ := testStore(t)
	h := hashOf(4)

	if err := s.Put(t.Context(), Record{InfoHash: h, Title: "x", Magnet: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(t.Context(), h, StatusComplete); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	if got.CompletedAt == nil {
		t.Fatal("a completed row has no completion time")
	}
	if got.Status != StatusComplete {
		t.Errorf("status = %q", got.Status)
	}
}

func TestMissingRowsAreNamed(t *testing.T) {
	s, _ := testStore(t)

	if _, err := s.Get(t.Context(), hashOf(5)); !errors.Is(err, ErrRowNotFound) {
		t.Errorf("Get err = %v", err)
	}
	if err := s.SetStatus(t.Context(), hashOf(5), StatusComplete); !errors.Is(err, ErrRowNotFound) {
		t.Errorf("SetStatus err = %v", err)
	}
	if err := s.Delete(t.Context(), hashOf(5)); !errors.Is(err, ErrRowNotFound) {
		t.Errorf("Delete err = %v", err)
	}
}

// The headline for this increment: a restart must not orphan bytes on disk.
// The engine is rebuilt from scratch, exactly as a restart does, and the queue
// comes back from the database alone — no indexer is contacted.
func TestTheQueueSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	store, _ := testStore(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	torrentFile, hash := makeTorrent(t, "", "payload.bin", 32*1024)

	// --- first run ---
	first := NewManager(newEngine(t, testGuard(t, true), Config{DataDir: dir}), store, quiet)
	if _, err := first.AddTorrent(torrentFile, Meta{
		Title: "Some.Movie.2019.1080p.BluRay-GRP", IndexerName: "Tracker",
	}); err != nil {
		t.Fatal(err)
	}
	if len(first.List()) != 1 {
		t.Fatalf("the first engine has %d transfers", len(first.List()))
	}
	_ = first.Close()

	// --- restart: a brand new engine that has never seen this torrent ---
	second := NewManager(newEngine(t, testGuard(t, true), Config{DataDir: dir}), store, quiet)
	if len(second.List()) != 0 {
		t.Fatalf("a fresh engine already had %d transfers", len(second.List()))
	}

	n, err := second.Restore(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("restored = %d, want 1", n)
	}
	got := second.List()
	if len(got) != 1 || got[0].InfoHash != hash {
		t.Fatalf("after restore the engine holds %+v", got)
	}

	// The title the operator recognises survived, not the torrent's own name.
	rows, err := second.Records(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Title != "Some.Movie.2019.1080p.BluRay-GRP" {
		t.Errorf("rows = %+v", rows)
	}
}

// A transfer the operator stopped must stay stopped across a restart.
func TestARemovedTransferDoesNotComeBackAfterARestart(t *testing.T) {
	dir := t.TempDir()
	store, _ := testStore(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	torrentFile, hash := makeTorrent(t, "", "payload.bin", 32*1024)

	first := NewManager(newEngine(t, testGuard(t, true), Config{DataDir: dir}), store, quiet)
	if _, err := first.AddTorrent(torrentFile, Meta{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := first.Remove(hash); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()

	second := NewManager(newEngine(t, testGuard(t, true), Config{DataDir: dir}), store, quiet)
	n, err := second.Restore(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("restored = %d: a transfer the operator stopped came back", n)
	}

	// The row is kept, not deleted: what the instance acquired is a question
	// the operator is answerable for.
	rows, err := second.Records(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != StatusStopped {
		t.Errorf("rows = %+v, want one stopped row", rows)
	}
}

// A manager with no store still runs transfers. Production wires one; a test
// that does not must not crash.
func TestAManagerWithNoStoreStillWorks(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(newEngine(t, testGuard(t, true), Config{DataDir: dir}), nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	torrentFile, _ := makeTorrent(t, "", "payload.bin", 32*1024)
	if _, err := m.AddTorrent(torrentFile, Meta{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if n, err := m.Restore(t.Context()); err != nil || n != 0 {
		t.Errorf("Restore = %d, %v", n, err)
	}
	if rows, err := m.Records(t.Context()); err != nil || rows != nil {
		t.Errorf("Records = %v, %v", rows, err)
	}
}

// ---------------------------------------------------------------------------
// Targets (ADR-0023)
// ---------------------------------------------------------------------------

func targeted(n byte, tg *Target) Record {
	return Record{
		InfoHash: hashOf(n), Title: "Severance.S02E03.1080p.WEB.H264-GRP",
		Torrent: []byte("d4:infod6:lengthi1eee"), Target: tg,
	}
}

// What a download was grabbed for survives the queue, which is the only place
// it survives a restart.
func TestATargetRoundTrips(t *testing.T) {
	s, _ := testStore(t)
	if err := s.Put(t.Context(), targeted(0, &Target{ItemID: 4, Season: 2, Episode: 3})); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(t.Context(), hashOf(0))
	if err != nil {
		t.Fatal(err)
	}
	if got.Target == nil || *got.Target != (Target{ItemID: 4, Season: 2, Episode: 3}) {
		t.Errorf("target = %+v", got.Target)
	}

	if err := s.Put(t.Context(), targeted(1, nil)); err != nil {
		t.Fatal(err)
	}
	if plain, _ := s.Get(t.Context(), hashOf(1)); plain.Target != nil {
		t.Errorf("a grab from the general search came back with a target: %+v", plain.Target)
	}
}

// Season 0 is real — specials live there — and must not read as "no target".
func TestATargetInTheSpecialsSeasonIsATarget(t *testing.T) {
	s, _ := testStore(t)
	if err := s.Put(t.Context(), targeted(0, &Target{ItemID: 4, Season: 0, Episode: 1})); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(t.Context(), hashOf(0))
	if got.Target == nil || got.Target.Season != 0 {
		t.Errorf("target = %+v", got.Target)
	}
}

// The same release grabbed again from the general search keeps the episode it
// was first grabbed for; grabbed again for a different episode, it takes that.
func TestARegrabKeepsOrReplacesTheTargetButNeverLosesIt(t *testing.T) {
	s, _ := testStore(t)
	first := &Target{ItemID: 4, Season: 2, Episode: 3}
	if err := s.Put(t.Context(), targeted(0, first)); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(t.Context(), targeted(0, nil)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(t.Context(), hashOf(0)); got.Target == nil || *got.Target != *first {
		t.Errorf("a re-grab from the general search dropped the target: %+v", got.Target)
	}

	// A different item as well as a different episode: the three fields move
	// together, and a test where only the episode changed could not tell a
	// stale item id from a fresh one.
	second := &Target{ItemID: 5, Season: 2, Episode: 4}
	if err := s.Put(t.Context(), targeted(0, second)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(t.Context(), hashOf(0)); got.Target == nil || *got.Target != *second {
		t.Errorf("a re-grab for another episode kept the old target: %+v", got.Target)
	}
}

// A target is all three fields or it is refused.
func TestAPartialTargetIsRefused(t *testing.T) {
	s, _ := testStore(t)
	for _, bad := range []Target{{Season: 2, Episode: 3}, {ItemID: 4, Season: -1, Episode: 3}, {ItemID: 4, Season: 2}} {
		if err := s.Put(t.Context(), targeted(0, &bad)); err == nil {
			t.Errorf("target %+v was accepted", bad)
		}
	}
}

// A film target is its item and nothing else, and it comes back as a film —
// not as an episode with zeros in it, and not as nothing (ADR-0026).
func TestAFilmTargetRoundTrips(t *testing.T) {
	s, database := testStore(t)
	if err := s.Put(t.Context(), targeted(0, &Target{ItemID: 7, Film: true})); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(t.Context(), hashOf(0))
	if err != nil {
		t.Fatal(err)
	}
	if got.Target == nil || *got.Target != (Target{ItemID: 7, Film: true}) {
		t.Errorf("target = %+v, want the film", got.Target)
	}
	// On the row: the kind says film, and the episode columns say nothing.
	var kind string
	var season, episode any
	if err := database.QueryRowContext(t.Context(), `
		SELECT target_kind, target_season, target_episode FROM download_queue WHERE info_hash = ?`,
		hashOf(0)).Scan(&kind, &season, &episode); err != nil {
		t.Fatal(err)
	}
	if kind != "film" || season != nil || episode != nil {
		t.Errorf("row = %q %v %v, want film with no season or episode", kind, season, episode)
	}
	// And an episode's row says episode.
	if err := s.Put(t.Context(), targeted(1, &Target{ItemID: 4, Season: 2, Episode: 3})); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRowContext(t.Context(),
		`SELECT target_kind FROM download_queue WHERE info_hash = ?`, hashOf(1)).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "episode" {
		t.Errorf("an episode's row says %q", kind)
	}
}

// A re-grab replaces the whole target, kind and all: a release first matched to
// an episode and then to a film is the film's, with no episode numbers left
// behind — and the other way round.
func TestARegrabReplacesAFilmAndAnEpisodeWhole(t *testing.T) {
	s, _ := testStore(t)
	episode := &Target{ItemID: 4, Season: 2, Episode: 3}
	film := &Target{ItemID: 7, Film: true}
	for _, step := range []struct {
		put  *Target
		want *Target
	}{
		{episode, episode},
		{film, film},
		{nil, film},
		{episode, episode},
		{nil, episode},
	} {
		if err := s.Put(t.Context(), targeted(0, step.put)); err != nil {
			t.Fatal(err)
		}
		got, _ := s.Get(t.Context(), hashOf(0))
		if got.Target == nil || *got.Target != *step.want {
			t.Fatalf("after putting %+v the target is %+v, want %+v", step.put, got.Target, step.want)
		}
	}
}

// A film with an episode in it is neither shape, and is refused.
func TestAFilmWithAnEpisodeInItIsRefused(t *testing.T) {
	s, _ := testStore(t)
	for _, bad := range []Target{
		{ItemID: 7, Season: 2, Episode: 3, Film: true},
		{ItemID: 7, Episode: 3, Film: true},
		{ItemID: 7, Season: 1, Film: true},
		{Film: true},
	} {
		if err := s.Put(t.Context(), targeted(0, &bad)); err == nil {
			t.Errorf("target %+v was accepted", bad)
		}
	}
}

// A row whose target columns fit neither shape — written by hand, or by
// something other than Put — is read as having NO target, never as whichever
// half could be salvaged: the import would otherwise file the download on a
// guess, which is what a target exists to stop.
func TestARowThatFitsNeitherShapeHasNoTarget(t *testing.T) {
	s, database := testStore(t)
	for i, cols := range []struct {
		kind           any
		item, sea, epi any
		want           *Target
	}{
		{"film", 7, 2, nil, nil},
		{"film", 7, nil, 3, nil},
		{"episode", 4, nil, 3, nil},
		{"episode", 4, 2, nil, nil},
		{"film", nil, nil, nil, nil},
		// Written before migration 0015 and somehow missed by it: the episode
		// it always was.
		{nil, 4, 2, 3, &Target{ItemID: 4, Season: 2, Episode: 3}},
		{nil, 7, nil, nil, nil},
	} {
		n := byte(i)
		if err := s.Put(t.Context(), targeted(n, nil)); err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(t.Context(), `
			UPDATE download_queue SET target_kind = ?, target_item_id = ?, target_season = ?,
			       target_episode = ? WHERE info_hash = ?`,
			cols.kind, cols.item, cols.sea, cols.epi, hashOf(n)); err != nil {
			t.Fatalf("%d: %v", i, err)
		}
		got, err := s.Get(t.Context(), hashOf(n))
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case cols.want == nil && got.Target != nil:
			t.Errorf("%d %v: read as %+v, want no target", i, cols, got.Target)
		case cols.want != nil && (got.Target == nil || *got.Target != *cols.want):
			t.Errorf("%d %v: read as %+v, want %+v", i, cols, got.Target, cols.want)
		}
	}
}

// A pack of several seasons keeps its span; a re-grab for one season drops
// it; only a season target carries one, and the database refuses a span that
// runs backwards; a span under another kind is no target (ADR-0057).
func TestAPackOfSeveralSeasonsRoundTrips(t *testing.T) {
	s, database := testStore(t)
	span := &Target{ItemID: 4, Season: 1, LastSeason: 3, Pack: true}
	if err := s.Put(t.Context(), targeted(0, span)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(t.Context(), hashOf(0)); got.Target == nil || *got.Target != *span {
		t.Errorf("target = %+v", got.Target)
	}
	one := &Target{ItemID: 4, Season: 2, Pack: true}
	if err := s.Put(t.Context(), targeted(0, one)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(t.Context(), hashOf(0)); got.Target == nil || *got.Target != *one {
		t.Errorf("a re-grab for one season kept the span: %+v", got.Target)
	}
	for _, bad := range []Target{{ItemID: 4, Season: 3, LastSeason: 3, Pack: true},
		{ItemID: 4, Season: 1, Episode: 2, LastSeason: 3}} {
		if err := s.Put(t.Context(), targeted(1, &bad)); err == nil {
			t.Errorf("target %+v was accepted", bad)
		}
	}
	if _, err := database.ExecContext(t.Context(), `UPDATE download_queue SET target_last_season = 1
		WHERE info_hash = ?`, hashOf(0)); err == nil {
		t.Error("the database took a last season before the first")
	}
	if _, err := database.ExecContext(t.Context(), `UPDATE download_queue SET target_kind = 'episode',
		target_episode = 3, target_last_season = 4 WHERE info_hash = ?`, hashOf(0)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(t.Context(), hashOf(0)); got.Target != nil {
		t.Errorf("an episode with a last season read as %+v", got.Target)
	}
}

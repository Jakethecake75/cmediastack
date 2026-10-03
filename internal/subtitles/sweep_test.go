package subtitles

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// addFile records a film's file, written on disk, imported at the given time.
func (r *rig) addFile(t *testing.T, id int64, kind, title, rel string, tmdb int64, imported time.Time) {
	t.Helper()
	res, err := r.db.ExecContext(t.Context(), `INSERT INTO media_item (kind, title, sort_title, root_folder_id, folder,
		tmdb_id, added_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 'x', 'x')`, kind, title, strings.ToLower(title), r.root,
		filepath.Dir(rel), tmdb)
	if err != nil {
		t.Fatal(err)
	}
	item, _ := res.LastInsertId()
	if _, err := r.db.ExecContext(t.Context(), `INSERT INTO media_file (id, item_id, root_folder_id, relative_path,
		size_bytes, imported_at) VALUES (?, ?, ?, ?, 262144, ?)`, id, item, r.root, rel,
		imported.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(r.dir, filepath.Dir(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, rel), make([]byte, 256<<10), 0o600); err != nil {
		t.Fatal(err)
	}
}

type sweepClock struct{ t time.Time }

func (c *sweepClock) now() time.Time { return c.t }

func sweepCtx(t *testing.T) context.Context {
	return authz.SystemPrincipal(t.Context(), authz.TaskSubtitles)
}

// ADR-0056, decisions 1 and 2: nothing asked until there is a key and a
// language; what has a subtitle, in either spelling of its language, beside
// it or inside it, is not wanted; music never is.
func TestWhatSubtitlesAreWanted(t *testing.T) {
	r := newRig(t)
	clock := &sweepClock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	sweep := NewSweeper(r.svc, r.db, clock.now)
	if s, err := sweep.Run(sweepCtx(t)); err != nil || !strings.Contains(s, "no key") {
		t.Errorf("without a key: %q %v", s, err)
	}
	if _, err := r.svc.Configure(r.ctx, Config{APIKey: ptr("k")}); err != nil {
		t.Fatal(err)
	}
	if s, _ := sweep.Run(sweepCtx(t)); !strings.Contains(s, "no subtitle language") {
		t.Errorf("without a language: %q", s)
	}
	if _, err := r.svc.Configure(r.ctx, Config{Languages: []string{"en"}}); err != nil {
		t.Fatal(err)
	}
	// Dune (file 7, from the rig) has nothing. Heat has an .eng.srt beside it;
	// Arrival embeds an English text track; Blade Runner only a picture one.
	r.addFile(t, 8, "movie", "Heat", "Heat (1995)/Heat.mkv", 949, clock.t)
	if err := os.WriteFile(filepath.Join(r.dir, "Heat (1995)", "Heat.eng.srt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.addFile(t, 9, "movie", "Arrival", "Arrival (2016)/Arrival.mkv", 329865, clock.t)
	r.addFile(t, 10, "movie", "Blade Runner", "Blade Runner (1982)/BR.mkv", 78, clock.t)
	for _, st := range []struct {
		file int64
		lang string
		text int
	}{{9, "ENG", 1}, {10, "eng", 0}} {
		if _, err := r.db.ExecContext(t.Context(), `INSERT INTO media_stream (media_file_id, kind, stream_index,
			language, is_text) VALUES (?, 'subtitle', 2, ?, ?)`, st.file, st.lang, st.text); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.db.ExecContext(t.Context(), `INSERT INTO media_item (id, kind, title, sort_title, root_folder_id, folder,
		added_at, updated_at) VALUES (50, 'artist', 'Portishead', 'portishead', ?, 'Portishead', 'x', 'x')`, r.root); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(t.Context(), `INSERT INTO media_file (id, item_id, root_folder_id, relative_path,
		imported_at) VALUES (51, 50, ?, 'Portishead/x.flac', 'x')`, r.root); err != nil {
		t.Fatal(err)
	}
	s, err := sweep.Run(sweepCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	asked := map[int64]bool{}
	for _, q := range r.provider.queries {
		asked[q.TMDBID] = true
	}
	if len(r.provider.queries) != 2 || !asked[438631] || !asked[78] || !strings.Contains(s, "searched 2 of 2 due") {
		t.Errorf("asked %+v: %s", r.provider.queries, s)
	}
}

// ADR-0056, decision 3: never searched first, newest first; the budget; the
// back-off doubling; a fetch spoken for; the quota ends the pass.
func TestTheSweepIsBudgetedAndBacksOff(t *testing.T) {
	r := newRig(t)
	clock := &sweepClock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	sweep := NewSweeper(r.svc, r.db, clock.now)
	if _, err := r.svc.Configure(r.ctx, Config{APIKey: ptr("k"), Languages: []string{"en"}}); err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < SearchesPerPass+2; i++ {
		r.addFile(t, 100+i, "movie", "Film "+string(rune('A'+i)), "F"+string(rune('A'+i))+"/f.mkv", 1000+i,
			clock.t.Add(time.Duration(i)*time.Hour))
	}
	s, err := sweep.Run(sweepCtx(t))
	if err != nil || !strings.Contains(s, "searched 10 of 13 due") || !strings.Contains(s, "10 found nothing") {
		t.Fatalf("first pass: %q %v", s, err)
	}
	if first := r.provider.queries[0].TMDBID; first != 1011 {
		t.Errorf("the newest file is not first: %d", first)
	}
	// The next pass takes the three left; then nothing is due for a day.
	r.provider.queries = nil
	if s, _ := sweep.Run(sweepCtx(t)); !strings.Contains(s, "searched 3 of 3 due") {
		t.Errorf("second pass: %q", s)
	}
	if s, _ := sweep.Run(sweepCtx(t)); !strings.Contains(s, "no subtitle is due") {
		t.Errorf("third pass: %q", s)
	}
	clock.t = clock.t.Add(FirstWait)
	r.provider.queries = nil
	if s, _ := sweep.Run(sweepCtx(t)); !strings.Contains(s, "searched 10 of 13 due") {
		t.Errorf("a day later: %q", s)
	}
	var fruitless int
	var next string
	if err := r.db.QueryRowContext(t.Context(), `SELECT fruitless, next_at FROM subtitle_search WHERE media_file_id = 100`).
		Scan(&fruitless, &next); err != nil || fruitless != 2 || next != clock.t.Add(2*FirstWait).UTC().Format(time.RFC3339Nano) {
		t.Errorf("the back-off: %d, %s", fruitless, next)
	}

	// Found: written, and not searched again.
	r.provider.results = []Result{{FileID: 1, Language: "en", TMDBID: 1000, Release: "Film.A"}}
	clock.t = clock.t.Add(LongestWait)
	s, _ = sweep.Run(sweepCtx(t))
	if !strings.Contains(s, "1 fetched") {
		t.Errorf("a fetch: %q", s)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "FA", "f.en.srt")); err != nil {
		t.Errorf("not written: %v", err)
	}
	var actor string
	if err := r.db.QueryRowContext(t.Context(), `SELECT actor_label FROM audit_event WHERE action = 'media.subtitle_fetched'`).
		Scan(&actor); err != nil || actor != string(authz.TaskSubtitles) {
		t.Errorf("audited as %q %v", actor, err)
	}

	// The quota: the pass stops at the first refusal.
	r.provider.results, r.provider.err = nil, ErrQuota
	r.provider.queries = nil
	clock.t = clock.t.Add(LongestWait)
	if _, err := sweep.Run(sweepCtx(t)); err == nil || !strings.Contains(err.Error(), "used up") || len(r.provider.queries) != 1 {
		t.Errorf("the quota: %v after %d searches", err, len(r.provider.queries))
	}
}

// The sweep runs with browse at least: anonymous, nothing.
func TestTheSweepNeedsBrowse(t *testing.T) {
	r := newRig(t)
	if _, err := NewSweeper(r.svc, r.db, nil).Run(context.Background()); !authz.IsDenied(err) {
		t.Errorf("an anonymous sweep: %v", err)
	}
}

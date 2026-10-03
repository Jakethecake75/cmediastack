package acquire

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/indexer"
)

// Each title is fetched as its own profile says, or as the default does
// (ADR-0035): a 2160p release for the one film that asked for it, and not for
// the rest.
func TestATitlesProfileJudgesWhatIsFetchedForIt(t *testing.T) {
	setup := func(t *testing.T) (*rig, int64, int64) {
		r := newRig(t, Config{})
		dune := r.addFilm("Dune", 2021, 438631)
		arrival := r.addFilm("Arrival", 2016, 329865)
		var uhd int64
		if err := r.database.QueryRowContext(context.Background(),
			`SELECT id FROM quality_profile WHERE name = 'Ultra-HD'`).Scan(&uhd); err != nil {
			t.Fatal(err)
		}
		r.exec(`UPDATE media_item SET quality_profile_id = ? WHERE id = ?`, uhd, dune)
		return r, dune, arrival
	}
	releases := func() []indexer.Result {
		return []indexer.Result{
			rel("Dune.2021.2160p.WEB-DL.DDP5.1.HDR.H.265-GRP", 1, 50),
			rel("Dune.2021.1080p.BluRay.x264-GRP", 2, 50),
			rel("Arrival.2016.2160p.WEB-DL.DDP5.1.HDR.H.265-GRP", 3, 50),
			rel("Arrival.2016.1080p.BluRay.x264-GRP", 4, 50),
		}
	}
	grabbedTitles := func(r *rig) string {
		var out []string
		for _, m := range r.queue.added() {
			out = append(out, m.Title)
		}
		sort.Strings(out)
		return strings.Join(out, " | ")
	}
	want := "Arrival.2016.1080p.BluRay.x264-GRP | Dune.2021.2160p.WEB-DL.DDP5.1.HDR.H.265-GRP"

	t.Run("by searching", func(t *testing.T) {
		r, _, _ := setup(t)
		all := releases()
		r.client.byTerm["dune 2021"] = all[:2]
		r.client.byTerm["arrival 2016"] = all[2:]
		summary := r.runSearch()
		if got := grabbedTitles(r); got != want {
			t.Errorf("grabbed %q (summary: %s); want Dune in 2160p by its own profile and Arrival "+
				"in 1080p by the default", got, summary)
		}
	})
	t.Run("from the recent releases", func(t *testing.T) {
		r, _, _ := setup(t)
		r.client.feed = releases()
		summary := r.runRecent()
		if got := grabbedTitles(r); got != want {
			t.Errorf("grabbed %q (summary: %s)", got, summary)
		}
	})
	t.Run("the refusal names the profile that refused", func(t *testing.T) {
		r, dune, _ := setup(t)
		r.client.feed = []indexer.Result{rel("Dune.2021.720p.HDTV.x264-GRP", 5, 50)}
		summary := r.runRecent()
		if !strings.Contains(summary, "its own profile (Ultra-HD) refuses it") || len(r.queue.added()) != 0 {
			t.Errorf("summary %q; the 720p release is refused by Dune's own profile, and says so", summary)
		}
		_ = fmt.Sprint(dune)
	})
}

package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A search for a title is judged by the title's profile, then the default; 0
// and a named profile still win (ADR-0035).
func TestATitlesProfileJudgesItsSearches(t *testing.T) {
	// A film that names "Any" (4).
	lib := duneLibrary()
	dune := lib.items[7]
	dune.QualityProfileID = 4
	lib.items[7] = dune
	for _, tc := range []struct {
		body      string
		want      string
		fromTitle bool
	}{
		{`{}`, "Any", true},
		{`{"profile_id":0}`, "", false},
		{`{"profile_id":1}`, "HD-1080p", false},
	} {
		s := &fakeEpisodeSearch{}
		h := &Handlers{search: s, media: lib, profiles: twoProfiles()}
		code, body := filmSearch(t, h, "7", tc.body)
		got := ""
		if s.gotFilm.Profile != nil {
			got = s.gotFilm.Profile.Name
		}
		if code != http.StatusOK || got != tc.want || body["profile_title"] != tc.fromTitle {
			t.Errorf("film %s: %d, searched with %q, profile_title %v; want %q, %v",
				tc.body, code, got, body["profile_title"], tc.want, tc.fromTitle)
		}
	}

	// A series that names one: its episodes' searches and its seasons'.
	sub := severanceSubject()
	sub.QualityProfileID = 4
	season := severanceSeason()
	season.QualityProfileID = 4
	s := &fakeEpisodeSearch{}
	h := &Handlers{search: s, episodes: &fakeEpisodes{subject: sub, season: season}, profiles: twoProfiles()}
	if code, body := episodeSearch(t, h, "77", `{}`); code != http.StatusOK || s.got.Profile == nil ||
		s.got.Profile.Name != "Any" || body["profile_title"] != true {
		t.Errorf("episode: %d, searched with %+v, %v", code, s.got.Profile, body["profile_title"])
	}
	if code, _ := seasonSearch(t, h, "4", "2"); code != http.StatusOK || s.gotSeason.Profile == nil ||
		s.gotSeason.Profile.Name != "Any" {
		t.Errorf("season: %d, searched with %+v", code, s.gotSeason.Profile)
	}

	// A title on the default is judged by the default, as before.
	s2 := &fakeEpisodeSearch{}
	h2 := &Handlers{search: s2, media: duneLibrary(), profiles: twoProfiles()}
	if code, body := filmSearch(t, h2, "7", `{}`); code != http.StatusOK || s2.gotFilm.Profile == nil ||
		s2.gotFilm.Profile.Name != "HD-1080p" || body["profile_title"] != false || body["profile_default"] != true {
		t.Errorf("default: %d, %+v, %v", code, s2.gotFilm.Profile, body)
	}
}

// Setting a title's profile is library editing, and it is audited with what it
// was and what it became (ADR-0035).
func TestATitlesProfileIsSetAndAudited(t *testing.T) {
	r := newRig(t)
	admin := r.bootstrapAdmin()
	r.filmReady(t)
	uhd := profileID(t, admin, "Ultra-HD")
	res := admin.post("/api/v1/media", map[string]any{"kind": "movie", "tmdb_id": 438631})
	if res.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", res.Code, res.Raw)
	}
	film := fmt.Sprint(int64(res.Body["item"].(map[string]any)["id"].(float64)))

	res = admin.do(http.MethodPut, "/api/v1/media/"+film+"/quality-profile", map[string]any{"profile_id": uhd})
	if res.Code != http.StatusOK || res.Body["profile"] != "Ultra-HD" {
		t.Fatalf("setting: %d %s", res.Code, res.Raw)
	}
	if got := admin.get("/api/v1/media/" + film); got.Body["quality_profile_id"] != float64(uhd) {
		t.Errorf("the title reads back %v (%s)", got.Body["quality_profile_id"], got.Raw)
	}
	res = admin.do(http.MethodPut, "/api/v1/media/"+film+"/quality-profile", map[string]any{"profile_id": nil})
	if res.Code != http.StatusOK || !strings.Contains(res.Raw, "the default") {
		t.Errorf("back to the default: %d %s", res.Code, res.Raw)
	}
	if res := admin.do(http.MethodPut, "/api/v1/media/"+film+"/quality-profile",
		map[string]any{"profile_id": 999}); res.Code != http.StatusNotFound {
		t.Errorf("an unknown profile: %d, want 404", res.Code)
	}

	rows, err := r.database.QueryContext(t.Context(),
		`SELECT detail FROM audit_event WHERE action = 'media.quality_profile.changed' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, d)
	}
	_ = rows.Close()
	if len(lines) != 2 || !strings.Contains(lines[0], "Ultra-HD") || !strings.Contains(lines[1], "default") {
		t.Errorf("audit lines %q", lines)
	}

	code, _ := r.issueInvite(admin, "User", true)
	user := r.redeemAndEnroll(code, "friend", "invited-passphrase-1")
	if res := user.do(http.MethodPut, "/api/v1/media/"+film+"/quality-profile",
		map[string]any{"profile_id": uhd}); res.Code != http.StatusForbidden {
		t.Errorf("a user without library.edit: %d, want 403, as the monitoring route beside it", res.Code)
	}
}

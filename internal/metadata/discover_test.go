package metadata

import (
	"errors"
	"strings"
	"testing"
)

// ADR-0043, decision 1.
func TestDiscoverReadsItsLists(t *testing.T) {
	trending := newFake(t, `{"results":[
		{"id":1,"media_type":"movie","title":"Dune: Part Two","release_date":"2024-03-01","overview":"o"},
		{"id":2,"media_type":"tv","name":"Shōgun","first_air_date":"2024-02-27"},
		{"id":3,"media_type":"person","name":"Somebody"},
		{"id":0,"media_type":"movie","title":"No id"}]}`)
	got, err := trending.client("k").Discover(t.Context(), "trending")
	if err != nil || len(got) != 2 {
		t.Fatalf("trending: %+v %v", got, err)
	}
	if got[0].Kind != KindMovie || got[0].Year != 2024 || got[1].Kind != KindSeries || got[1].Title != "Shōgun" {
		t.Errorf("trending results %+v", got)
	}
	if p := trending.paths[0]; !strings.HasPrefix(p, "/trending/all/week?") || !strings.Contains(p, "include_adult=false") {
		t.Errorf("asked %s", p)
	}

	for section, want := range map[string]string{
		"popular-films": "/movie/popular?", "popular-series": "/tv/popular?", "upcoming-films": "/movie/upcoming?",
	} {
		f := newFake(t, `{"results":[{"id":5,"title":"A","name":"A"}]}`)
		got, err := f.client("k").Discover(t.Context(), section)
		if err != nil || len(got) != 1 || !strings.HasPrefix(f.paths[0], want) {
			t.Errorf("%s: %+v %v asked %v", section, got, err, f.paths)
		}
		if section == "popular-series" && got[0].Kind != KindSeries {
			t.Errorf("a popular series is a %s", got[0].Kind)
		}
	}
	f := newFake(t, `{}`)
	if _, err := f.client("k").Discover(t.Context(), "everything"); !errors.Is(err, ErrNoSuchSection) || len(f.paths) != 0 {
		t.Errorf("an unknown section: %v, asked %v", err, f.paths)
	}
}

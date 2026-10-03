package metadata

import (
	"strings"
	"testing"
)

// The shapes are TMDB's documented ones for /movie/{id}/release_dates and
// /tv/{id}/content_ratings (ADR-0037). A film's theatrical certification wins
// over its others; another country's is never read; a series has one rating.
func TestCertificationIsRead(t *testing.T) {
	film := newFake(t, `{"id":438631,"results":[
		{"iso_3166_1":"GB","release_dates":[{"certification":"12A","type":3}]},
		{"iso_3166_1":"US","release_dates":[
			{"certification":"","type":1},
			{"certification":"R","type":2},
			{"certification":"NR","type":4},
			{"certification":"PG-13","type":3}]}]}`)
	got, err := film.client("k").Certification(t.Context(), KindMovie, 438631)
	if err != nil || got != "PG-13" {
		t.Fatalf("film: %q, %v", got, err)
	}
	if p := film.paths[0]; !strings.HasPrefix(p, "/movie/438631/release_dates") {
		t.Errorf("asked %s", p)
	}

	// With no theatrical release, the lowest-typed release that has one.
	digital := newFake(t, `{"id":1,"results":[{"iso_3166_1":"US","release_dates":[
		{"certification":"R","type":5},{"certification":"PG","type":4}]}]}`)
	if got, _ := digital.client("k").Certification(t.Context(), KindMovie, 1); got != "PG" {
		t.Errorf("without a theatrical release: %q", got)
	}

	abroad := newFake(t, `{"id":2,"results":[{"iso_3166_1":"FR","release_dates":[
		{"certification":"12","type":3}]}]}`)
	if got, err := abroad.client("k").Certification(t.Context(), KindMovie, 2); err != nil || got != "" {
		t.Errorf("another country's rating was read: %q, %v", got, err)
	}

	series := newFake(t, `{"id":95396,"results":[{"iso_3166_1":"DE","rating":"16"},
		{"iso_3166_1":"US","rating":"TV-MA"}]}`)
	got, err = series.client("k").Certification(t.Context(), KindSeries, 95396)
	if err != nil || got != "TV-MA" {
		t.Fatalf("series: %q, %v", got, err)
	}
	if p := series.paths[0]; !strings.HasPrefix(p, "/tv/95396/content_ratings") {
		t.Errorf("asked %s", p)
	}

	if _, err := series.client("k").Certification(t.Context(), KindSeries, 0); err == nil {
		t.Error("an id of 0 was asked about")
	}
}

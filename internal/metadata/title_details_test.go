package metadata

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// ADR-0078: a title's page shows its backdrop, tagline and score, and an
// episode's card its still. Both are filenames, as a poster's path is.
func TestDetailsCarryTheBackdropAndScore(t *testing.T) {
	f := newFake(t, `{"id":95396,"name":"Severance","first_air_date":"2022-02-18",
		"tagline":"We're all just innies.","backdrop_path":"/back.jpg","vote_average":8.4}`)
	d, err := f.client("t").Details(context.Background(), KindSeries, 95396)
	if err != nil {
		t.Fatal(err)
	}
	if d.BackdropPath != "back.jpg" || d.Score != 8.4 || d.Tagline != "We're all just innies." {
		t.Errorf("backdrop %q, score %v, tagline %q", d.BackdropPath, d.Score, d.Tagline)
	}
}

func TestEpisodesCarryTheirStill(t *testing.T) {
	f := newFake(t, `{"season_number":1,"episodes":[
		{"id":1,"episode_number":1,"name":"Good News About Hell","still_path":"/one.jpg"},
		{"id":2,"episode_number":2,"name":"Half Loop","still_path":null}]}`)
	eps, err := f.client("t").Episodes(context.Background(), 95396, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 2 || eps[0].StillPath != "one.jpg" || eps[1].StillPath != "" {
		t.Errorf("stills = %+v", eps)
	}
}

type countingDetails struct {
	Provider
	asked int
}

func (c *countingDetails) Details(_ context.Context, kind Kind, id int64) (Details, error) {
	c.asked++
	return Details{Match: Match{ProviderID: id, Kind: kind, Overview: "A bear."}}, nil
}

// Opening a title costs the provider one request a day, however often it is
// opened; a film and a series with the same id are two titles; and only an
// account that may browse is answered.
func TestTitleDetailsAreKeptForADay(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	svc := NewService(settingsAnswer{}, nil, nil, func() *http.Client { return http.DefaultClient },
		func() time.Time { return now })
	p := &countingDetails{}
	svc.provider = p
	browser := holding(authz.PermBrowse)

	for i := 0; i < 3; i++ {
		d, err := svc.TitleDetails(browser, KindMovie, 1001)
		if err != nil || d.Overview != "A bear." {
			t.Fatalf("details: %+v %v", d, err)
		}
	}
	if p.asked != 1 {
		t.Errorf("the provider was asked %d times for one title in one day, want 1", p.asked)
	}
	if _, err := svc.TitleDetails(browser, KindSeries, 1001); err != nil || p.asked != 2 {
		t.Errorf("a series with a film's id was answered from the film: asked %d, %v", p.asked, err)
	}
	now = now.Add(titleKept)
	if _, err := svc.TitleDetails(browser, KindMovie, 1001); err != nil || p.asked != 3 {
		t.Errorf("a day later the provider was asked %d times, want 3: %v", p.asked, err)
	}
	if _, err := svc.TitleDetails(holding(authz.PermSubmitRequest), KindMovie, 1001); !authz.IsDenied(err) {
		t.Errorf("an account that may not browse was answered: %v", err)
	}
	svc.provider = nil
	if _, err := svc.TitleDetails(browser, KindMovie, 1001); !errors.Is(err, ErrNoProvider) {
		t.Errorf("with no provider: %v, want ErrNoProvider", err)
	}
}

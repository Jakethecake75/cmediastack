package tv

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/metadata"
)

// Reading a whole series for an add (ADR-0025): everything, or an error.

func TestReadingASeriesReadsEverySeasonIncludingEmptyOnes(t *testing.T) {
	p := &fakeEpisodeProvider{
		seasons: []metadata.Season{
			providerSeason(0, 1, -300), providerSeason(1, 3, -400), {Number: 2, Name: "Season 2"},
		},
		episodes: map[int][]metadata.Episode{
			0: providerEpisodes(1, -300),
			1: providerEpisodes(3, -400),
			// Season 2 is announced: the provider lists it and nothing in it.
		},
	}
	_, seasons, err := ReadSeries(context.Background(), p, 95396)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.asked) != 3 {
		t.Errorf("asked about seasons %v; an add has nothing stored to skip against", p.asked)
	}
	if len(seasons) != 3 {
		t.Fatalf("%d seasons", len(seasons))
	}
	for _, s := range seasons {
		// nil would mean "not asked about" to the store, and a season that WAS
		// asked about and holds nothing must say so.
		if s.Episodes == nil {
			t.Errorf("season %d came back with a nil episode list", s.Number)
		}
	}
	if len(seasons[1].Episodes) != 3 || seasons[1].EpisodeCount != 3 {
		t.Errorf("season 1 = %d episodes, count %d", len(seasons[1].Episodes), seasons[1].EpisodeCount)
	}
}

// A season that cannot be read ends the read, and nothing comes back — an add
// that recorded part of a series could not apply the monitoring choice to the
// rest.
func TestReadingASeriesStopsAtTheFirstSeasonItCannotRead(t *testing.T) {
	for name, fault := range map[string]error{
		"a failure":    metadata.ErrUnavailable,
		"a rate limit": metadata.ErrRateLimited,
		"a 404":        metadata.ErrNotFound,
	} {
		t.Run(name, func(t *testing.T) {
			p := &fakeEpisodeProvider{
				seasons: []metadata.Season{providerSeason(1, 3, -400), providerSeason(2, 2, -300),
					providerSeason(3, 2, -200)},
				episodes:   map[int][]metadata.Episode{1: providerEpisodes(3, -400), 3: providerEpisodes(2, -200)},
				episodeErr: map[int]error{2: fault},
			}
			_, seasons, err := ReadSeries(context.Background(), p, 95396)
			if !errors.Is(err, fault) {
				t.Fatalf("err = %v, want %v", err, fault)
			}
			if !strings.Contains(err.Error(), "season 2") {
				t.Errorf("the error does not say which season: %v", err)
			}
			if seasons != nil {
				t.Errorf("%d seasons came back with the error", len(seasons))
			}
			if len(p.asked) != 2 {
				t.Errorf("asked about %v; nothing after the failed season should be asked", p.asked)
			}
		})
	}
}

func TestReadingASeriesTheProviderDoesNotHave(t *testing.T) {
	p := &fakeEpisodeProvider{detailsErr: metadata.ErrNotFound}
	if _, _, err := ReadSeries(context.Background(), p, 1); !errors.Is(err, metadata.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if len(p.asked) != 0 {
		t.Errorf("seasons asked about for a series that does not exist: %v", p.asked)
	}
}

func TestReadingASeriesWithNoProvider(t *testing.T) {
	if _, _, err := ReadSeries(context.Background(), nil, 1); !errors.Is(err, metadata.ErrNoProvider) {
		t.Errorf("err = %v", err)
	}
}

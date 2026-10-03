package metadata

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Discover: the provider's lists of what is popular (ADR-0043).

// DiscoverSections are the lists, by the name the route uses.
var DiscoverSections = []string{"trending", "popular-films", "popular-series", "upcoming-films"}

// ErrNoSuchSection refuses a list that is not one of DiscoverSections.
var ErrNoSuchSection = errors.New("metadata: no such discover section")

// ErrNoDiscover means the provider cannot list what is popular.
var ErrNoDiscover = errors.New("metadata: the provider does not list what is popular")

// Discoverer is a provider that lists what is popular.
type Discoverer interface {
	Discover(ctx context.Context, section string) ([]Match, error)
}

type tmdbDiscoverResult struct {
	tmdbSearchResult
	// MediaType is set on trending results: movie, tv or person.
	MediaType string `json:"media_type"`
}

type tmdbDiscoverPage struct {
	Results []tmdbDiscoverResult `json:"results"`
}

// Discover returns the first page of one list, without adult titles. People
// on the trending list are dropped: they are not a title anybody can ask for.
func (t *TMDB) Discover(ctx context.Context, section string) ([]Match, error) {
	var path string
	kind := KindMovie
	switch section {
	case "trending":
		path = "/trending/all/week"
	case "popular-films":
		path = "/movie/popular"
	case "popular-series":
		path, kind = "/tv/popular", KindSeries
	case "upcoming-films":
		path = "/movie/upcoming"
	default:
		return nil, fmt.Errorf("%w: %q", ErrNoSuchSection, section)
	}
	v := url.Values{}
	v.Set("include_adult", "false")
	var page tmdbDiscoverPage
	if err := t.get(ctx, path, v, &page); err != nil {
		return nil, err
	}
	out := make([]Match, 0, len(page.Results))
	for _, r := range page.Results {
		k := kind
		switch r.MediaType {
		case "":
		case "movie":
			k = KindMovie
		case "tv":
			k = KindSeries
		default:
			continue
		}
		if r.ID <= 0 || r.title() == "" {
			continue
		}
		out = append(out, Match{
			ProviderID: r.ID, Kind: k, Title: r.title(), OriginalTitle: r.originalTitle(),
			Year: yearOf(r.date()), Overview: r.Overview,
			PosterPath: strings.TrimPrefix(r.PosterPath, "/"), Popularity: r.Popularity,
		})
	}
	return out, nil
}

// Discover asks the configured provider for one list.
func (svc *Service) Discover(ctx context.Context, section string) ([]Match, error) {
	p := svc.Provider()
	if p == nil {
		return nil, ErrNoProvider
	}
	d, ok := p.(Discoverer)
	if !ok {
		return nil, ErrNoDiscover
	}
	return d.Discover(ctx, section)
}

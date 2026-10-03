package metadata

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// A title's rating (ADR-0037): the US certification of a film, the US content
// rating of a series. Read from the provider's own lists rather than the
// details, which carry neither.

// ErrNoCertifications means the configured provider cannot say how a title is
// rated.
var ErrNoCertifications = errors.New("metadata: the provider does not report ratings")

// CertificationCountry is whose ratings are read. One country for every
// instance: each country's system ranks differently, and the ranks are what a
// ceiling is compared with.
const CertificationCountry = "US"

// Certifier is a provider that can say how a title is rated.
type Certifier interface {
	// Certification returns the title's rating in CertificationCountry, or ""
	// when the provider lists none there.
	Certification(ctx context.Context, kind Kind, providerID int64) (string, error)
}

// tmdbReleaseDates is the shape of /movie/{id}/release_dates.
type tmdbReleaseDates struct {
	ID      int64 `json:"id"`
	Results []struct {
		Country      string `json:"iso_3166_1"`
		ReleaseDates []struct {
			Certification string `json:"certification"`
			// Type is TMDB's release type: 1 premiere, 2 limited theatrical,
			// 3 theatrical, 4 digital, 5 physical, 6 TV.
			Type int `json:"type"`
		} `json:"release_dates"`
	} `json:"results"`
}

// tmdbContentRatings is the shape of /tv/{id}/content_ratings.
type tmdbContentRatings struct {
	ID      int64 `json:"id"`
	Results []struct {
		Country string `json:"iso_3166_1"`
		Rating  string `json:"rating"`
	} `json:"results"`
}

// Certification returns a title's US rating.
//
// A film has one certification per release, usually the same; the theatrical
// release's is preferred, then the earliest-typed release that has one. An
// empty string with no error means TMDB lists nothing for the US.
func (t *TMDB) Certification(ctx context.Context, kind Kind, providerID int64) (string, error) {
	if providerID <= 0 {
		return "", fmt.Errorf("metadata: %d is not an identifier", providerID)
	}
	id := strconv.FormatInt(providerID, 10)
	if kind == KindSeries {
		var r tmdbContentRatings
		if err := t.get(ctx, "/tv/"+id+"/content_ratings", nil, &r); err != nil {
			return "", err
		}
		for _, c := range r.Results {
			if strings.EqualFold(c.Country, CertificationCountry) {
				return strings.TrimSpace(c.Rating), nil
			}
		}
		return "", nil
	}

	var r tmdbReleaseDates
	if err := t.get(ctx, "/movie/"+id+"/release_dates", nil, &r); err != nil {
		return "", err
	}
	best, bestType := "", 0
	for _, c := range r.Results {
		if !strings.EqualFold(c.Country, CertificationCountry) {
			continue
		}
		for _, d := range c.ReleaseDates {
			cert := strings.TrimSpace(d.Certification)
			if cert == "" {
				continue
			}
			if d.Type == 3 {
				return cert, nil
			}
			if best == "" || d.Type < bestType {
				best, bestType = cert, d.Type
			}
		}
	}
	return best, nil
}

// Certification asks the configured provider how a title is rated.
func (svc *Service) Certification(ctx context.Context, kind Kind, providerID int64) (string, error) {
	p := svc.Provider()
	if p == nil {
		return "", ErrNoProvider
	}
	c, ok := p.(Certifier)
	if !ok {
		return "", ErrNoCertifications
	}
	return c.Certification(ctx, kind, providerID)
}

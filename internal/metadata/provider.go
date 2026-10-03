// Package metadata identifies what a library item actually is.
//
// # What is verified here and what is not, stated plainly
//
// This package talks to TMDB. Against the live service, the following were
// observed directly while it was written:
//
//   - the base is https://api.themoviedb.org/3, and it answers HTTP/2;
//   - an unauthenticated or bad-key request returns 401 with the body
//     {"status_code":7,"status_message":"Invalid API key: You must be granted a
//     valid key.","success":false};
//   - no rate-limit headers appear on that response, so this code does not
//     claim to parse any — a 429 is handled by STATUS, which is universal;
//   - the image CDN at image.tmdb.org needs no key, serves from BunnyCDN, and
//     CONTENT-NEGOTIATES: a URL ending .jpg returns WebP when the request says
//     it accepts WebP. See internal/artwork.
//
// When this was first written no SUCCESSFUL response had been observed, for
// want of an API key, and the field names came from TMDB's documentation. That
// is no longer so: increment 3j ran every call this package makes against the
// live service, and tmdb_live_test.go re-runs them whenever CMS_TMDB_TOKEN is
// set. Two documented fields turned out not to hold (see the comments on
// tmdbDetails), and later increments added the season, alternative-title and
// not-found shapes, each checked live before it was relied on.
//
// The defences written for the unverified version stay, because they are still
// the right behaviour towards a third party's API:
//
//  1. Decoding is tolerant. Every field is optional, a missing one yields a
//     zero value, and no absent field is an error. A response that is shaped
//     slightly differently produces a thin result, not a failure.
//  2. Check() makes a credential a button rather than a mystery, and it reports
//     what actually came back — including the raw body when the shape is not
//     what this expects.
package metadata

import (
	"context"
	"errors"
	"time"
)

// Kind is what sort of title is being identified.
type Kind string

const (
	KindMovie  Kind = "movie"
	KindSeries Kind = "series"
)

// Errors this package distinguishes.
var (
	// ErrNoProvider means no metadata provider is configured.
	ErrNoProvider = errors.New("metadata: no provider is configured")
	// ErrUnauthorized means the provider rejected the credential.
	ErrUnauthorized = errors.New("metadata: the provider rejected the API key")
	// ErrRateLimited means the provider asked us to slow down.
	ErrRateLimited = errors.New("metadata: the provider is rate-limiting this instance")
	// ErrUnavailable means the provider could not be reached or failed.
	ErrUnavailable = errors.New("metadata: the provider is unavailable")
	// ErrUnexpectedShape means the response parsed as JSON but did not contain
	// what this software expects. Distinguished because it is the failure mode
	// this package is most likely to have: see the package comment.
	ErrUnexpectedShape = errors.New("metadata: the provider's response was not the expected shape")
	// ErrNotFound means the provider has no such title, season or resource.
	//
	// Its own error rather than a shape problem, which is what a 404 used to be
	// reported as. The difference is whose problem it is: a mistyped id is the
	// operator's to fix, a provider answering nonsense is not — and adding a
	// series by id (ADR-0025) has to tell them which.
	ErrNotFound = errors.New("metadata: the provider has no such title")
)

// Query is a search.
type Query struct {
	Kind  Kind
	Title string
	// Year narrows the search. Zero means unknown, which is a real and common
	// case — the release-name parser often cannot find one.
	Year int
}

// Match is one candidate identification.
//
// Deliberately small. This is what a person picks from, not a catalogue: the
// fields are the ones that let somebody say "yes, that is the film I meant".
type Match struct {
	// ProviderID is the provider's own identifier.
	ProviderID int64
	Kind       Kind
	Title      string
	// OriginalTitle differs from Title for anything not released in English,
	// and is often the string a release name actually used.
	OriginalTitle string
	Year          int
	Overview      string
	// PosterPath is the provider's own path, for internal/artwork. It is a
	// filename, and artwork refuses anything else.
	PosterPath string
	// Popularity orders results. A search for "Dune" returns both films and a
	// documentary about the unmade one; ordering by what people actually mean
	// beats ordering by id.
	Popularity float64
}

// Details is everything known about one identified title.
type Details struct {
	Match
	// IMDbID is carried because it is the identifier release groups and other
	// tools agree on, where TMDB's own is not.
	IMDbID string
	// Runtime is minutes, zero when unknown.
	Runtime int
	// Released is the release or first-air date, zero when unknown.
	Released time.Time
	Genres   []string
	// Seasons is empty for a film and for a series whose seasons were not
	// requested.
	Seasons []Season
}

// Season is one season of a series, as the provider knows it.
//
// This is what ADR-0016 deliberately refused to invent: a table of episodes the
// instance does NOT have. It can exist now because a provider is the authority
// for it rather than the files on disk.
type Season struct {
	Number   int
	Name     string
	Episodes int
	Aired    time.Time
}

// Episode is one episode of a season, as the provider knows it.
//
// The point of this type is the episodes the instance does NOT have. A season
// assembled from files on disk is complete by construction; only a provider can
// say what is missing (ADR-0022).
type Episode struct {
	// ProviderID is the provider's own episode id, kept so that a renumbering
	// can be recognised rather than guessed at.
	ProviderID int64
	Number     int
	Title      string
	Overview   string
	// Aired is the zero time when the provider lists the episode with no date.
	// That is an ANNOUNCED episode and it is a different thing from one that
	// aired: treating a date nobody has as "already aired" would make every
	// unannounced episode of every running show wanted on day one.
	Aired time.Time
	// Runtime is minutes, zero when unknown.
	Runtime int
}

// Health is what a provider reports about itself.
type Health struct {
	OK bool
	// Detail is for an operator to read. On failure it says what came back,
	// including the raw body when the shape was wrong, because "it didn't work"
	// is not something anyone can act on.
	Detail string
	// ImageBase is the provider's image base URL, needed by internal/artwork.
	ImageBase string
	// Sizes are the poster sizes the provider offers.
	Sizes []string
	// CheckedAt is when this was established.
	CheckedAt time.Time
}

// Provider identifies titles.
//
// An interface rather than a concrete TMDB client for one specific reason, not
// for generality: TMDB's television data is weaker than TVDB's, and this
// software replaces Sonarr as well as Radarr. The cache, the matching and
// everything that stores an id must therefore not know which service answered.
// Everything else about a provider — its auth scheme, its URL shapes, its
// quirks — stays behind this line.
type Provider interface {
	// Name is the short, lowercase, filesystem-safe identifier that appears in
	// artwork paths and in stored rows.
	Name() string
	// Check proves the credential works and reports what the provider says
	// about itself.
	Check(ctx context.Context) (Health, error)
	// Search returns candidates, best first.
	Search(ctx context.Context, q Query) ([]Match, error)
	// Details returns everything about one identified title.
	Details(ctx context.Context, kind Kind, providerID int64) (Details, error)
	// Episodes returns one season's episodes.
	//
	// A season at a time rather than a whole series at once, because that is the
	// shape every provider offers and because a refresh only needs the seasons
	// that can have changed — a twelve-season show whose last episode aired in
	// 2013 should cost no requests at all.
	//
	// An empty slice with no error means the provider knows the series and lists
	// no episodes for that season, which is normal for an announced one.
	Episodes(ctx context.Context, seriesID int64, season int) ([]Episode, error)
	// AlternativeTitles returns the other names a series is known by.
	//
	// Release names follow scene convention rather than the provider's title —
	// The Office is released as "The.Office.US", Law & Order: Special Victims
	// Unit as "Law.and.Order.SVU" — so a search that recognised only the
	// provider's title would refuse the right show (ADR-0023).
	AlternativeTitles(ctx context.Context, seriesID int64) ([]string, error)
	// FilmTitles returns every name a film is known by: its title, its
	// original title and its alternative titles, in that order, without blanks
	// or repeats.
	//
	// The original title is included by name because a provider need not list
	// it among the alternatives — TMDB does not — and a film not made in
	// English is often released under it (ADR-0026).
	FilmTitles(ctx context.Context, filmID int64) ([]string, error)
}

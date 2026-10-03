package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TMDB is a client for themoviedb.org.
//
// See the package comment for exactly which parts of this were verified against
// the live service and which were not.

// DefaultTMDBBase is the API root. Verified: it answers HTTP/2 and returns the
// documented error envelope on a bad key.
const DefaultTMDBBase = "https://api.themoviedb.org/3"

// FallbackImageBase is used when /configuration has not been read yet.
//
// Verified against the live CDN, which needs no key. Kept as a constant rather
// than required from configuration so that artwork works on the first run,
// before any successful authenticated call.
const FallbackImageBase = "https://image.tmdb.org/t/p"

// maxBody caps a provider response. Metadata for one title is a few kilobytes;
// a search page is tens. Two megabytes is far above anything legitimate and is
// here to bound the read, not to be tuned.
const maxBody = 2 << 20

// TMDB implements Provider.
type TMDB struct {
	client  *http.Client
	base    string
	token   string
	imgBase string
}

// NewTMDB builds a client.
//
// token is either a v4 read access token or a v3 API key; both are sent as a
// Bearer credential, which is what TMDB documents for v4 and accepts for v4
// tokens. A v3 key sent this way is rejected with the 401 envelope, and Check
// says so in words rather than leaving an operator guessing which kind of key
// they pasted.
func NewTMDB(client *http.Client, base, token string) *TMDB {
	if strings.TrimSpace(base) == "" {
		base = DefaultTMDBBase
	}
	return &TMDB{
		client:  client,
		base:    strings.TrimSuffix(strings.TrimSpace(base), "/"),
		token:   strings.TrimSpace(token),
		imgBase: FallbackImageBase,
	}
}

// Name implements Provider. Lowercase and alphanumeric because it becomes a
// directory name under the artwork cache, which validates it.
func (t *TMDB) Name() string { return "tmdb" }

// ImageBase reports where artwork lives, for internal/artwork.
func (t *TMDB) ImageBase() string { return t.imgBase }

// tmdbError is the envelope observed on a real 401.
//
// This shape IS verified: {"status_code":7,"status_message":"Invalid API key:
// You must be granted a valid key.","success":false}
type tmdbError struct {
	StatusCode    int    `json:"status_code"`
	StatusMessage string `json:"status_message"`
	Success       *bool  `json:"success"`
}

// get performs a request and decodes into out.
//
// Every failure mode is mapped to one of this package's errors, because the
// three that matter are genuinely different jobs for whoever is reading:
// "your key is wrong", "slow down", and "their service is down".
func (t *TMDB) get(ctx context.Context, path string, q url.Values, out any) error {
	if t.token == "" {
		return ErrNoProvider
	}

	u := t.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	req.Header.Set("Accept", "application/json")

	res, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4<<10))
		_ = res.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return fmt.Errorf("%w: reading the response: %w", ErrUnavailable, err)
	}

	switch {
	case res.StatusCode == http.StatusUnauthorized ||
		res.StatusCode == http.StatusForbidden:
		var e tmdbError
		_ = json.Unmarshal(body, &e)
		if e.StatusMessage != "" {
			return fmt.Errorf("%w: %s", ErrUnauthorized, e.StatusMessage)
		}
		return fmt.Errorf("%w: HTTP %d", ErrUnauthorized, res.StatusCode)

	case res.StatusCode == http.StatusTooManyRequests:
		// By status, not by header. No rate-limit header was observed on the
		// live service, and parsing one this code has never seen would be
		// exactly the invented behaviour this project refuses to ship.
		return ErrRateLimited

	case res.StatusCode == http.StatusNotFound:
		// Observed on the live API, for a series, a season and a film that do
		// not exist alike: {"success":false,"status_code":34,"status_message":
		// "The resource you requested could not be found."}
		var e tmdbError
		_ = json.Unmarshal(body, &e)
		msg := e.StatusMessage
		if msg == "" {
			msg = "not found"
		}
		return fmt.Errorf("%w (%s): %s", ErrNotFound, path, msg)

	case res.StatusCode != http.StatusOK:
		return fmt.Errorf("%w: HTTP %d: %s", ErrUnavailable, res.StatusCode,
			snippet(body))
	}

	if err := json.Unmarshal(body, out); err != nil {
		// The most likely failure this package has, so it says what arrived
		// rather than "invalid character '<'". An operator reporting this can
		// be told what to do with it.
		return fmt.Errorf("%w: %w (body began: %s)", ErrUnexpectedShape, err, snippet(body))
	}
	return nil
}

func snippet(b []byte) string {
	const n = 200
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		s = s[:n] + "…"
	}
	if s == "" {
		return "(empty)"
	}
	return s
}

// ---------------------------------------------------------------------------
// Check
// ---------------------------------------------------------------------------

type tmdbConfiguration struct {
	Images struct {
		SecureBaseURL string   `json:"secure_base_url"`
		PosterSizes   []string `json:"poster_sizes"`
	} `json:"images"`
}

// Check proves the credential and learns where artwork lives.
//
// /configuration is the right endpoint for this: it is cheap, it requires
// authentication (so it proves the key), and it returns the image base URL and
// size list this software needs anyway. Using a search would prove the key and
// learn nothing.
func (t *TMDB) Check(ctx context.Context) (Health, error) {
	if t.token == "" {
		return Health{Detail: "no API key has been configured"}, ErrNoProvider
	}

	var cfg tmdbConfiguration
	if err := t.get(ctx, "/configuration", nil, &cfg); err != nil {
		return Health{
			OK:        false,
			Detail:    explain(err),
			CheckedAt: time.Now(),
		}, err
	}

	base := strings.TrimSuffix(cfg.Images.SecureBaseURL, "/")
	if base == "" {
		// The call succeeded and the field this software needs was absent.
		// Reported rather than papered over, and artwork still works from the
		// verified fallback.
		base = FallbackImageBase
		return Health{
			OK:        true,
			ImageBase: base,
			Sizes:     cfg.Images.PosterSizes,
			Detail: "the key works, but the response carried no images.secure_base_url; " +
				"falling back to " + FallbackImageBase,
			CheckedAt: time.Now(),
		}, nil
	}
	t.imgBase = base

	return Health{
		OK:        true,
		ImageBase: base,
		Sizes:     cfg.Images.PosterSizes,
		Detail: fmt.Sprintf("the key works; artwork at %s in %d sizes",
			base, len(cfg.Images.PosterSizes)),
		CheckedAt: time.Now(),
	}, nil
}

// explain turns an error into something an operator can act on.
func explain(err error) string {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return "the provider rejected the key. TMDB wants a v4 Read Access Token " +
			"(a long string beginning \"eyJ\"), not a v3 API key. Detail: " + err.Error()
	case errors.Is(err, ErrRateLimited):
		return "the provider is rate-limiting this instance; try again shortly"
	case errors.Is(err, ErrUnexpectedShape):
		return "the provider answered, but not with what this software expects. " +
			"Detail: " + err.Error()
	case errors.Is(err, ErrNotFound):
		// Only reachable from Check through a wrong base address: the key is
		// not what a 404 is about, and saying so stops somebody replacing a
		// working key.
		return "the provider answered 404 for its own configuration endpoint, which " +
			"means the address being asked is wrong rather than the key. Detail: " + err.Error()
	default:
		return err.Error()
	}
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// tmdbSearchResult covers BOTH /search/movie and /search/tv.
//
// The two differ in exactly two field names — title/name and
// release_date/first_air_date — so both are declared and whichever is present
// wins. Two near-identical structs would be two places to get it wrong.
type tmdbSearchResult struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Name  string `json:"name"`

	OriginalTitle string `json:"original_title"`
	OriginalName  string `json:"original_name"`

	ReleaseDate  string `json:"release_date"`
	FirstAirDate string `json:"first_air_date"`

	Overview   string  `json:"overview"`
	PosterPath string  `json:"poster_path"`
	Popularity float64 `json:"popularity"`
}

func (r tmdbSearchResult) title() string {
	if r.Title != "" {
		return r.Title
	}
	return r.Name
}

func (r tmdbSearchResult) originalTitle() string {
	if r.OriginalTitle != "" {
		return r.OriginalTitle
	}
	return r.OriginalName
}

func (r tmdbSearchResult) date() string {
	if r.ReleaseDate != "" {
		return r.ReleaseDate
	}
	return r.FirstAirDate
}

type tmdbSearchPage struct {
	Results      []tmdbSearchResult `json:"results"`
	TotalResults int                `json:"total_results"`
}

// Search returns candidates, best first.
func (t *TMDB) Search(ctx context.Context, q Query) ([]Match, error) {
	title := strings.TrimSpace(q.Title)
	if title == "" {
		return nil, fmt.Errorf("metadata: a search needs a title")
	}

	path := "/search/movie"
	yearKey := "year"
	if q.Kind == KindSeries {
		path = "/search/tv"
		yearKey = "first_air_date_year"
	}

	v := url.Values{}
	v.Set("query", title)
	v.Set("include_adult", "false")
	if q.Year > 0 {
		v.Set(yearKey, strconv.Itoa(q.Year))
	}

	var page tmdbSearchPage
	if err := t.get(ctx, path, v, &page); err != nil {
		return nil, err
	}

	out := make([]Match, 0, len(page.Results))
	for _, r := range page.Results {
		if r.ID <= 0 {
			// A result with no identifier cannot be attached to anything, so it
			// is dropped rather than shown as a choice that does nothing.
			continue
		}
		out = append(out, Match{
			ProviderID:    r.ID,
			Kind:          q.Kind,
			Title:         r.title(),
			OriginalTitle: r.originalTitle(),
			Year:          yearOf(r.date()),
			Overview:      r.Overview,
			PosterPath:    strings.TrimPrefix(r.PosterPath, "/"),
			Popularity:    r.Popularity,
		})
	}
	return out, nil
}

// yearOf reads the leading year from a TMDB date.
//
// Tolerant by design: TMDB dates are "2016-11-11", but an empty string and a
// year-only string are both real, and neither is an error — a title with no
// known release date is a fact about the title, not a failure to parse.
func yearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	n, err := strconv.Atoi(date[:4])
	if err != nil || n < 1800 || n > 2200 {
		return 0
	}
	return n
}

func dateOf(date string) time.Time {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ---------------------------------------------------------------------------
// Details
// ---------------------------------------------------------------------------

type tmdbDetails struct {
	tmdbSearchResult

	IMDbID  string `json:"imdb_id"`
	Runtime int    `json:"runtime"`
	// Television was documented to report a LIST of typical episode lengths.
	// In practice it is usually EMPTY: the live API returns [] for Severance,
	// and apparently for most series. Kept because it is still populated for
	// some, but it cannot be relied on.
	EpisodeRunTime []int `json:"episode_run_time"`
	// The only runtime a series reliably carries, and even then it is the
	// length of ONE episode — often a finale, which is the least typical one
	// there is. Used as a last resort so the field is an approximation rather
	// than always zero, and never as an authority: for anything that matters
	// (transcode planning, ADR-0005) the FILE's duration is the truth, and this
	// is metadata about a title.
	LastEpisode struct {
		Runtime int `json:"runtime"`
	} `json:"last_episode_to_air"`

	Genres []struct {
		Name string `json:"name"`
	} `json:"genres"`

	Seasons []struct {
		SeasonNumber int    `json:"season_number"`
		Name         string `json:"name"`
		EpisodeCount int    `json:"episode_count"`
		AirDate      string `json:"air_date"`
	} `json:"seasons"`

	// ExternalIDs arrives when append_to_response asks for it, which is how a
	// series' IMDb id is obtained — unlike a film, a series does not carry
	// imdb_id at the top level.
	ExternalIDs struct {
		IMDbID string `json:"imdb_id"`
	} `json:"external_ids"`
}

// Details returns everything about one identified title.
func (t *TMDB) Details(ctx context.Context, kind Kind, providerID int64) (Details, error) {
	if providerID <= 0 {
		return Details{}, fmt.Errorf("metadata: %d is not an identifier", providerID)
	}

	path := "/movie/" + strconv.FormatInt(providerID, 10)
	if kind == KindSeries {
		path = "/tv/" + strconv.FormatInt(providerID, 10)
	}
	// One request rather than two. A series' IMDb id lives under external_ids,
	// and a second round trip per title is what makes a library backfill take
	// an afternoon instead of a minute.
	v := url.Values{}
	v.Set("append_to_response", "external_ids")

	var d tmdbDetails
	if err := t.get(ctx, path, v, &d); err != nil {
		return Details{}, err
	}
	if d.ID <= 0 {
		return Details{}, fmt.Errorf("%w: the response carried no id", ErrUnexpectedShape)
	}

	out := Details{
		Match: Match{
			ProviderID:    d.ID,
			Kind:          kind,
			Title:         d.title(),
			OriginalTitle: d.originalTitle(),
			Year:          yearOf(d.date()),
			Overview:      d.Overview,
			PosterPath:    strings.TrimPrefix(d.PosterPath, "/"),
			Popularity:    d.Popularity,
		},
		IMDbID:   firstNonEmpty(d.IMDbID, d.ExternalIDs.IMDbID),
		Runtime:  d.Runtime,
		Released: dateOf(d.date()),
	}
	// Three sources, in descending order of how much they mean. A film has
	// "runtime" and that is the answer. A series usually has neither of the
	// others, and zero is the honest result — see the field comments.
	switch {
	case out.Runtime > 0:
	case len(d.EpisodeRunTime) > 0:
		out.Runtime = d.EpisodeRunTime[0]
	default:
		out.Runtime = d.LastEpisode.Runtime
	}
	for _, g := range d.Genres {
		if g.Name != "" {
			out.Genres = append(out.Genres, g.Name)
		}
	}
	for _, s := range d.Seasons {
		// Season 0 is "Specials". Kept, because an operator's files are
		// frequently in it, and dropping it would make those files permanently
		// unmatchable.
		if s.SeasonNumber < 0 {
			continue
		}
		out.Seasons = append(out.Seasons, Season{
			Number:   s.SeasonNumber,
			Name:     s.Name,
			Episodes: s.EpisodeCount,
			Aired:    dateOf(s.AirDate),
		})
	}
	return out, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// tmdbAlternativeTitles is the shape of /tv/{id}/alternative_titles.
//
// The TELEVISION shape. The film endpoint answers with "titles" where this one
// has "results", so a struct shared between them would decode one of the two as
// an empty list and report, without an error, that a film has no other names.
type tmdbAlternativeTitles struct {
	Results []struct {
		Country string `json:"iso_3166_1"`
		Title   string `json:"title"`
		Type    string `json:"type"`
	} `json:"results"`
}

// AlternativeTitles returns the other names TMDB knows a series by.
//
// Every one is returned, whatever its country or script: a title in Cyrillic
// will never match an ASCII release name and costs nothing to compare, while
// guessing which countries' names are "the scene's" would drop the one that
// mattered. Duplicates and blanks are removed; order is TMDB's.
func (t *TMDB) AlternativeTitles(ctx context.Context, seriesID int64) ([]string, error) {
	if seriesID <= 0 {
		return nil, fmt.Errorf("metadata: %d is not an identifier", seriesID)
	}
	var a tmdbAlternativeTitles
	if err := t.get(ctx, "/tv/"+strconv.FormatInt(seriesID, 10)+"/alternative_titles", nil, &a); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(a.Results))
	for _, r := range a.Results {
		title := strings.TrimSpace(r.Title)
		if title == "" || seen[title] {
			continue
		}
		seen[title] = true
		out = append(out, title)
	}
	return out, nil
}

// tmdbFilmTitles is the shape of /movie/{id}?append_to_response=alternative_titles.
//
// The FILM shape: the list is "titles" where the television endpoint says
// "results" — which is why tmdbAlternativeTitles is not reused. Checked against
// the live API while ADR-0026 was written.
type tmdbFilmTitles struct {
	ID                int64  `json:"id"`
	Title             string `json:"title"`
	OriginalTitle     string `json:"original_title"`
	AlternativeTitles struct {
		Titles []struct {
			Country string `json:"iso_3166_1"`
			Title   string `json:"title"`
			Type    string `json:"type"`
		} `json:"titles"`
	} `json:"alternative_titles"`
}

// FilmTitles returns every name TMDB knows a film by.
//
// One request, not two: the details carry the title and original title, and
// append_to_response brings the alternatives in the same answer. The original
// title is NOT among TMDB's alternative titles — Amélie's alternatives do not
// include "Le Fabuleux Destin d'Amélie Poulain" — so it is taken from the
// details by name. Every alternative is kept, whatever its country or script,
// for the reason AlternativeTitles gives.
func (t *TMDB) FilmTitles(ctx context.Context, filmID int64) ([]string, error) {
	if filmID <= 0 {
		return nil, fmt.Errorf("metadata: %d is not an identifier", filmID)
	}
	v := url.Values{}
	v.Set("append_to_response", "alternative_titles")

	var f tmdbFilmTitles
	if err := t.get(ctx, "/movie/"+strconv.FormatInt(filmID, 10), v, &f); err != nil {
		return nil, err
	}
	if f.ID <= 0 {
		return nil, fmt.Errorf("%w: the response carried no id", ErrUnexpectedShape)
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(f.AlternativeTitles.Titles)+2)
	add := func(title string) {
		title = strings.TrimSpace(title)
		if title == "" || seen[title] {
			return
		}
		seen[title] = true
		out = append(out, title)
	}
	add(f.Title)
	add(f.OriginalTitle)
	for _, a := range f.AlternativeTitles.Titles {
		add(a.Title)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: the answer for film %d carries no title at all",
			ErrUnexpectedShape, filmID)
	}
	return out, nil
}

// tmdbSeason is the shape of /tv/{id}/season/{n}.
type tmdbSeason struct {
	SeasonNumber int `json:"season_number"`
	Episodes     []struct {
		ID            int64  `json:"id"`
		EpisodeNumber int    `json:"episode_number"`
		Name          string `json:"name"`
		Overview      string `json:"overview"`
		AirDate       string `json:"air_date"`
		Runtime       int    `json:"runtime"`
	} `json:"episodes"`
}

// Episodes returns one season's episodes.
//
// This is the call that makes "what am I missing" answerable at all: everything
// else in this package describes titles the instance already holds, and this one
// describes episodes it does not (ADR-0022).
func (t *TMDB) Episodes(ctx context.Context, seriesID int64, season int) ([]Episode, error) {
	if seriesID <= 0 {
		return nil, fmt.Errorf("metadata: %d is not an identifier", seriesID)
	}
	if season < 0 {
		return nil, fmt.Errorf("metadata: %d is not a season number", season)
	}

	path := "/tv/" + strconv.FormatInt(seriesID, 10) + "/season/" + strconv.Itoa(season)
	var s tmdbSeason
	if err := t.get(ctx, path, nil, &s); err != nil {
		return nil, err
	}

	out := make([]Episode, 0, len(s.Episodes))
	for _, e := range s.Episodes {
		if e.EpisodeNumber < 0 {
			continue
		}
		out = append(out, Episode{
			ProviderID: e.ID,
			Number:     e.EpisodeNumber,
			Title:      e.Name,
			Overview:   e.Overview,
			// dateOf returns the zero time for "" and for an unparseable date,
			// which is exactly the ANNOUNCED case: TMDB lists a future episode
			// with air_date null long before anyone has scheduled it.
			Aired:   dateOf(e.AirDate),
			Runtime: e.Runtime,
		})
	}
	return out, nil
}

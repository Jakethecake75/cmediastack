// Package tv keeps a series' episode list current from a metadata provider.
//
// It sits ABOVE both internal/library, which stores seasons and episodes, and
// internal/metadata, which knows what a provider says. The first version of
// this code lived inside library, which made the storage package import the
// provider package — an inverted layering that surfaced as an import cycle in
// metadata's tests, and would otherwise have surfaced later as something
// harder to untangle. library.TestTheStorageLayerImportsNothingAboveIt now
// keeps it that way (ADR-0022).
package tv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/metadata"
)

// Refreshing a series' episode list from the provider.
//
// This is the only path by which an episode row comes into existence. Every
// argument it takes comes from a provider; there is no parameter through which
// a file, a filename or a release name could reach it (ADR-0022).

// EpisodeProvider is the slice of a metadata provider this needs.
//
// Deliberately two methods and not the whole Provider: this reads what a series
// contains and nothing else, and a narrower interface is a narrower thing to
// get wrong.
type EpisodeProvider interface {
	Details(ctx context.Context, kind metadata.Kind, providerID int64) (metadata.Details, error)
	Episodes(ctx context.Context, seriesID int64, season int) ([]metadata.Episode, error)
}

// SeriesSource hands over what this package needs to know about an item.
type SeriesSource interface {
	SeriesForRefresh(ctx context.Context, itemID int64) (SeriesRef, error)
}

// SeriesRef is the little an episode refresh needs about a library item.
type SeriesRef struct {
	ID     int64
	Title  string
	Kind   string
	TMDBID int64
}

// RefreshResult is what one series' refresh did.
type RefreshResult struct {
	ItemID   int64
	Title    string
	Seasons  int
	Episodes int
	// SeasonsRead were fetched and recorded.
	SeasonsRead int
	// SeasonsSkipped could not have changed, so were not asked about.
	SeasonsSkipped int
	// SeasonsFailed were worth asking about and were NOT read: the request
	// failed, or the provider had already told this refresh to stop. They are
	// asked about again next time.
	//
	// Kept apart from SeasonsSkipped deliberately. The first version counted a
	// failed season as skipped, and the summary then described a season whose
	// request had just returned a 502 as one that "could not have changed".
	SeasonsFailed int
}

// Summary is one line for an operator or a task log.
func (r RefreshResult) Summary() string {
	// "read N episodes", not "N episodes". A second refresh that skips every
	// finished season reads almost nothing, and "Severance: 4 seasons, 0
	// episodes" read — to the person who wrote it, on the live API — as though
	// the series had lost its contents.
	s := fmt.Sprintf("%s: %d season(s); fetched %d, skipped %d that could not "+
		"have changed", r.Title, r.Seasons, r.SeasonsRead, r.SeasonsSkipped)
	if r.SeasonsFailed > 0 {
		s += fmt.Sprintf(", and %d could NOT be read and will be asked about again",
			r.SeasonsFailed)
	}
	return s + fmt.Sprintf("; read %d episode(s)", r.Episodes)
}

// Refresher fills in what a series contains.
type Refresher struct {
	store    *library.EpisodeStore
	series   SeriesSource
	provider func() EpisodeProvider
	log      *slog.Logger
	now      func() time.Time
}

// NewRefresher builds one.
//
// provider is a function rather than a value for the same reason the
// identification service takes one: the credential can be set, changed and
// removed while the process runs, and holding a provider captured at startup
// would mean an operator configuring one has to restart to use it.
func NewRefresher(store *library.EpisodeStore, series SeriesSource,
	provider func() EpisodeProvider, log *slog.Logger, now func() time.Time) *Refresher {

	if log == nil {
		log = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &Refresher{store: store, series: series, provider: provider, log: log, now: now}
}

// ErrNotIdentified means the series has no provider id, so nothing can be asked
// about it.
var ErrNotIdentified = errors.New("library: this series has not been identified, " +
	"so there is no provider id to ask about its episodes")

// Refresh reads a series' seasons and episodes from the provider and records
// them.
//
// # Which seasons are fetched
//
// Not all of them. TMDB is one HTTP call per season, a twelve-season show is
// twelve calls, and most seasons never change again. A season is fetched when:
//
//   - nothing is stored for it yet, or
//   - the provider's episode count differs from what is stored — it gained or
//     lost an episode, or
//   - it holds an episode that has not aired yet, or that aired recently enough
//     that its details may still be being filled in.
//
// A season that fails none of those is left exactly as it is, and is counted as
// skipped so the log says what was not done rather than implying it was.
//
// # When the provider says stop
//
// A rate limit ends the refresh: nothing more is asked, what was read is still
// recorded, and the error wraps metadata.ErrRateLimited so a caller can tell
// "stopped part-way" from "failed". The first version logged the refusal and
// asked about the NEXT season, and the one after — every one of them into the
// same limit — then reported success, so RefreshAll went straight on to the
// next series.
func (r *Refresher) Refresh(ctx context.Context, itemID int64) (RefreshResult, error) {
	p := r.provider()
	if p == nil {
		return RefreshResult{}, metadata.ErrNoProvider
	}

	ref, err := r.series.SeriesForRefresh(ctx, itemID)
	if err != nil {
		return RefreshResult{}, err
	}
	if ref.Kind != "series" {
		return RefreshResult{}, library.ErrNotASeries
	}
	if ref.TMDBID <= 0 {
		return RefreshResult{}, ErrNotIdentified
	}

	details, err := p.Details(ctx, metadata.KindSeries, ref.TMDBID)
	if err != nil {
		return RefreshResult{}, fmt.Errorf("library: asking about %s: %w", ref.Title, err)
	}

	// Read once, not once per season. The first version called this inside the
	// loop, which turned a twelve-season refresh into twelve full reads of the
	// series to answer a question about one season each time.
	stored, episodes, err := r.store.Seasons(ctx, itemID)
	if err != nil {
		return RefreshResult{}, err
	}
	known := map[int]library.Season{}
	for _, s := range stored {
		known[s.Number] = s
	}

	out := RefreshResult{ItemID: itemID, Title: ref.Title}
	input := make([]library.SeasonInput, 0, len(details.Seasons))

	// stopped is the provider's instruction to stop, once it has given one.
	// Every season after that is recorded without being asked about.
	var stopped error

	for _, s := range details.Seasons {
		si := library.SeasonInput{
			Number:       s.Number,
			Name:         s.Name,
			EpisodeCount: s.Episodes,
			Aired:        s.Aired,
		}
		if !r.worthFetching(s, known, episodes) {
			// Episodes left nil, which Upsert reads as "not asked about" and
			// leaves alone — as opposed to an empty slice, which would mean the
			// provider lists none and would delete the season's contents.
			out.SeasonsSkipped++
			input = append(input, si)
			continue
		}
		if stopped != nil {
			out.SeasonsFailed++
			input = append(input, unread(si, known))
			continue
		}

		eps, err := p.Episodes(ctx, ref.TMDBID, s.Number)
		if err != nil {
			if ctx.Err() != nil {
				// Shutting down. Nothing can be recorded on a finished context,
				// and that is not the provider's fault.
				return out, ctx.Err()
			}
			// One season failing does not abandon the rest. A partial refresh
			// that records what it learned is more useful than an abort that
			// records nothing.
			out.SeasonsFailed++
			input = append(input, unread(si, known))
			if errors.Is(err, metadata.ErrRateLimited) {
				stopped = err
				r.log.Warn("the provider asked this refresh to stop; recording what was read",
					slog.String("series", ref.Title),
					slog.Int("season", s.Number))
				continue
			}
			r.log.Warn("could not read a season",
				slog.String("series", ref.Title),
				slog.Int("season", s.Number),
				slog.String("error", err.Error()))
			continue
		}
		out.SeasonsRead++

		si.Episodes = episodeInputs(eps)
		out.Episodes += len(si.Episodes)
		input = append(input, si)
	}
	out.Seasons = len(input)

	if err := r.store.Upsert(ctx, itemID, input); err != nil {
		return out, err
	}
	r.log.Info("refreshed a series' episodes",
		slog.String("series", ref.Title),
		slog.Int("seasons", out.Seasons),
		slog.Int("episodes", out.Episodes),
		slog.Int("fetched", out.SeasonsRead),
		slog.Int("skipped", out.SeasonsSkipped),
		slog.Int("failed", out.SeasonsFailed))
	if stopped != nil {
		return out, fmt.Errorf("library: %s: stopped with %d season(s) unread: %w",
			ref.Title, out.SeasonsFailed, stopped)
	}
	return out, nil
}

// episodeInputs is a provider's episode list in the store's input type.
//
// Never nil, even for an empty list: to Upsert, nil means "not asked about" and
// an empty slice means "the provider lists none", and a season that WAS read
// must say the second.
func episodeInputs(eps []metadata.Episode) []library.EpisodeInput {
	out := make([]library.EpisodeInput, 0, len(eps))
	for _, e := range eps {
		out = append(out, library.EpisodeInput{
			ProviderID: e.ProviderID,
			Number:     e.Number,
			Title:      e.Title,
			Overview:   e.Overview,
			Aired:      e.Aired,
			Runtime:    e.Runtime,
		})
	}
	return out
}

// ReadSeries asks the provider for everything a series contains — its details
// and every season's episodes — and writes nothing.
//
// For adding a series (ADR-0025), which records all of it in one transaction or
// none of it. So, unlike Refresh, it skips no season — there is nothing stored
// to compare against — and it stops at the first season it cannot read rather
// than carrying on: an add that recorded part of a series could not apply the
// operator's monitoring choice to the rest, which the scheduled refresh would
// then fill in with the defaults.
//
// One request for the details and one per season, in order. Measured against
// the live API: The Simpsons is 40 requests and 885 episodes, 7.2 seconds.
func ReadSeries(ctx context.Context, p EpisodeProvider, tmdbID int64) (metadata.Details, []library.SeasonInput, error) {
	if p == nil {
		return metadata.Details{}, nil, metadata.ErrNoProvider
	}
	details, err := p.Details(ctx, metadata.KindSeries, tmdbID)
	if err != nil {
		return metadata.Details{}, nil, fmt.Errorf("tv: asking about series %d: %w", tmdbID, err)
	}

	seasons := make([]library.SeasonInput, 0, len(details.Seasons))
	for _, s := range details.Seasons {
		if s.Number < 0 {
			continue
		}
		eps, err := p.Episodes(ctx, tmdbID, s.Number)
		if err != nil {
			if ctx.Err() != nil {
				return metadata.Details{}, nil, ctx.Err()
			}
			return metadata.Details{}, nil, fmt.Errorf("tv: reading season %d of %s: %w",
				s.Number, details.Title, err)
		}
		seasons = append(seasons, library.SeasonInput{
			Number:       s.Number,
			Name:         s.Name,
			EpisodeCount: s.Episodes,
			Aired:        s.Aired,
			Episodes:     episodeInputs(eps),
		})
	}
	return details, seasons, nil
}

// unread is a season whose episodes were NOT read this time, keeping the
// episode count it already had.
//
// Recording the provider's new count without the episodes that go with it
// would make the next refresh see agreement and never ask again: a season that
// gained an episode during a 502 would stay one short for good. The first
// version did exactly that — and its comment promised that "the next run will
// try this season again because its stored count will still disagree". The
// count it was relying on was the one it had just overwritten.
//
// A season never seen before has no count to keep. It is recorded with the
// provider's, and no episodes, and worthFetching asks about a season with no
// episodes on every run until one is read.
func unread(si library.SeasonInput, known map[int]library.Season) library.SeasonInput {
	si.Episodes = nil
	if prev, ok := known[si.Number]; ok {
		si.EpisodeCount = prev.EpisodeCount
	}
	return si
}

// settledFor is how long after an episode airs its details are still treated as
// possibly incomplete.
//
// Titles, overviews and runtimes arrive late and get corrected. A week is long
// enough to pick up those edits and short enough that a finished series stops
// costing requests almost immediately.
const settledFor = 7 * 24 * time.Hour

// worthFetching decides whether a season's episode list can have changed.
//
// Pure: it is given what is stored rather than reading it, so every branch is
// testable without a database and the caller reads the series once.
func (r *Refresher) worthFetching(s metadata.Season, known map[int]library.Season,
	bySeason map[int][]library.Episode) bool {

	stored, ok := known[s.Number]
	if !ok {
		// Never seen. Nothing to compare against, so ask.
		return true
	}
	if stored.EpisodeCount != s.Episodes {
		// Gained or lost an episode.
		return true
	}
	if len(bySeason[s.Number]) == 0 {
		// No episodes stored for a season that exists. Two cases, and both are
		// worth a request:
		//
		//   - a refresh that failed part-way, where asking is how it repairs
		//     itself; and
		//   - an ANNOUNCED season the provider lists with nothing in it yet —
		//     which is precisely the season about to gain episodes. Observed on
		//     the live API: Severance season 3, count 0, refetched on every run
		//     while every aired season was skipped. That is the right trade: a
		//     running show costs one request per refresh, a finished one none.
		return true
	}

	// An episode still to air, or one that aired recently, means the season's
	// contents can still move.
	cutoff := r.now().UTC().Add(-settledFor)
	for _, e := range bySeason[s.Number] {
		if e.Announced() || e.Aired.After(cutoff) {
			return true
		}
	}
	return false
}

// Policy is which series the scheduled refresh asks about (ADR-0022, decision 7
// as amended).
//
// A series with anything recent or still to come is asked every run. Every
// other series is asked once a week, because a finished series can be renewed:
// at a single request for a series whose seasons have not changed, six hundred
// of them cost under a hundred requests a day.
var Policy = library.RefreshPolicy{Recent: 90 * 24 * time.Hour, Every: 7 * 24 * time.Hour}

// RefreshAll refreshes the series the policy says are worth asking about.
//
// Bounded by design: this is a scheduled task spending a third party's rate
// limit. At most limit series are asked about per run, least recently asked
// first, so a library bigger than one run's worth is worked through in turn.
func (r *Refresher) RefreshAll(ctx context.Context, limit int) ([]RefreshResult, error) {
	ids, err := r.store.SeriesNeedingRefresh(ctx, Policy, limit)
	if err != nil {
		return nil, err
	}
	var out []RefreshResult
	for _, id := range ids {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		res, err := r.Refresh(ctx, id)
		if err != nil {
			if errors.Is(err, metadata.ErrRateLimited) {
				// Stop rather than hammer. What was read is recorded, and the
				// next run picks up where this left off: the seasons left
				// unread still disagree with the provider, so they are asked
				// about again.
				if res.ItemID != 0 {
					out = append(out, res)
				}
				return out, err
			}
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			r.log.Warn("could not refresh a series",
				slog.Int64("item", id), slog.String("error", err.Error()))
			continue
		}
		out = append(out, res)
	}
	return out, nil
}

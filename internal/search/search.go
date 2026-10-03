// Package search turns "I want this" into "grab that".
//
// It is the join between the three subsystems built before it: indexers supply
// candidates, internal/release parses and ranks them, and a quality profile
// decides what is acceptable. Nothing here talks to the network or the
// database directly; it composes the pieces that do.
//
// # The design constraint that shapes everything
//
// A search fans out to every enabled indexer, and indexers are unreliable in
// ordinary operation — rate limits, maintenance windows, a tracker that has
// been down for a week and nobody noticed. So the whole package is built around
// one rule: **one indexer failing must never fail the search.**
//
// That has a consequence worth stating, because it is the failure mode of every
// tool in this category: partial results that *look* complete are worse than an
// error. An operator who sees four results and does not know that three of
// their five indexers timed out will conclude the release does not exist. So a
// Response always carries which indexers answered, which failed, and why — and
// Partial() says so in one call.
//
// # Why rejections are returned rather than dropped
//
// "Why did it not grab anything?" is the question this kind of software is
// worst at answering. Every candidate that was found and then refused comes
// back with the reason, so the answer is in the response instead of in a debug
// log nobody enabled.
package search

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/release"
)

// Limits.
const (
	// MaxParallelIndexers bounds the fan-out. Trackers rate-limit, and hitting
	// twenty at once is how an operator earns a ban on all of them at the same
	// time.
	MaxParallelIndexers = 5
	// DefaultIndexerTimeout bounds one indexer, independently of the overall
	// deadline: a single slow tracker must not consume the whole budget and
	// starve the others.
	DefaultIndexerTimeout = 30 * time.Second
	// MaxCandidates caps the merged set.
	MaxCandidates = 5000
)

// IndexerSource supplies the indexers to query. It is the method that decrypts
// API keys, which is why this package takes an interface rather than the store:
// nothing here should be able to reach a key by accident.
type IndexerSource interface {
	Enabled(ctx context.Context) ([]indexer.Definition, error)
}

// Searcher queries one indexer.
type Searcher interface {
	Search(ctx context.Context, d indexer.Definition, q indexer.Query) ([]indexer.Result, error)
}

// HealthRecorder notes whether an indexer answered. Optional.
type HealthRecorder interface {
	RecordResult(ctx context.Context, indexerID int64, searchErr error) error
}

// Service runs searches.
type Service struct {
	source IndexerSource
	client Searcher
	// downloader is the grab half. It is discovered from client rather than
	// taken as a parameter: *indexer.Client is both, and a Searcher-only fake
	// in a test should not be forced to grow a Download method it will never
	// use. A service without one refuses to grab by name (ErrGrabUnavailable)
	// rather than appearing to work, and TestTheRealClientCanGrab asserts the
	// production type does satisfy it.
	downloader Downloader
	health     HealthRecorder
	timeout    time.Duration
	now        func() time.Time
}

// New builds the service.
func New(source IndexerSource, client Searcher, health HealthRecorder) *Service {
	s := &Service{
		source: source, client: client, health: health,
		timeout: DefaultIndexerTimeout, now: time.Now,
	}
	if d, ok := client.(Downloader); ok {
		s.downloader = d
	}
	return s
}

// Request is what the caller wants.
type Request struct {
	Term    string
	Season  int // -1 for a film
	Episode int
	// AirDate, YYYY-MM-DD, asks for a daily series' episode by date (ADR-0064).
	AirDate    string
	IMDBID     string
	TVDBID     string
	Categories []int
	// Profile decides acceptance and ranking. A nil profile means "return
	// everything, ranked by nothing" — useful for an interactive search where
	// the operator is the judge.
	Profile *release.Profile
	// IndexerIDs restricts the search. Empty means every enabled indexer.
	IndexerIDs []int64
	// Limit caps what each indexer returns. Zero is the indexer client's own
	// ceiling. A recent-releases pass asks for a page, not a thousand
	// (ADR-0030).
	Limit int
}

// Candidate is one release, with the verdict attached.
type Candidate struct {
	indexer.Result
	Quality release.Quality
	// Accepted is false when the profile refused it. Rejected candidates are
	// returned anyway: "why did nothing get grabbed" is the question this
	// software is worst at answering, and the answer belongs in the response.
	Accepted  bool
	Rejection *release.Rejection
	Score     int
	// SeenOn lists every indexer that offered this same release, after
	// deduplication. More than one is a mild quality signal and is worth
	// showing rather than hiding.
	SeenOn []string
	// Target is what this candidate was matched to, set only by an episode or
	// film search and only for a candidate that IS that episode or film. A
	// ticket sealed for it carries the target; see SearchEpisode and
	// SearchFilm.
	Target *Target
}

// IndexerOutcome records what one indexer did.
type IndexerOutcome struct {
	IndexerID   int64
	IndexerName string
	Results     int
	Duration    time.Duration
	Err         string
}

// Response is the merged result.
type Response struct {
	Candidates []Candidate
	Outcomes   []IndexerOutcome
	Queried    int
	Failed     int
	Elapsed    time.Duration
}

// Partial reports whether any indexer failed.
//
// A caller that ignores this shows an operator four results without mentioning
// that three of their five indexers timed out, and the operator concludes the
// release does not exist. That is the failure mode this whole package is shaped
// around.
func (r Response) Partial() bool { return r.Failed > 0 }

// Accepted returns only the candidates the profile allowed, best first.
func (r Response) Accepted() []Candidate {
	out := make([]Candidate, 0, len(r.Candidates))
	for _, c := range r.Candidates {
		if c.Accepted {
			out = append(out, c)
		}
	}
	return out
}

// Best returns the single candidate to grab, if any.
func (r Response) Best() (Candidate, bool) {
	accepted := r.Accepted()
	if len(accepted) == 0 {
		return Candidate{}, false
	}
	return accepted[0], true
}

// ErrNoIndexers means nothing is configured to search.
var ErrNoIndexers = errors.New("search: no indexers are enabled")

// Search queries every applicable indexer and merges the answers.
func (s *Service) Search(ctx context.Context, req Request) (Response, error) {
	started := s.now()

	defs, err := s.source.Enabled(ctx)
	if err != nil {
		return Response{}, fmt.Errorf("search: loading indexers: %w", err)
	}
	defs = filterIndexers(defs, req.IndexerIDs)
	if len(defs) == 0 {
		return Response{}, ErrNoIndexers
	}

	query := indexer.Query{
		Term: req.Term, Categories: req.Categories,
		Season: req.Season, Episode: req.Episode, AirDate: req.AirDate,
		IMDBID: req.IMDBID, TVDBID: req.TVDBID,
		Limit: req.Limit,
	}

	results, outcomes := s.fanOut(ctx, defs, query)

	resp := Response{
		Outcomes: outcomes,
		Queried:  len(defs),
		Elapsed:  s.now().Sub(started),
	}
	for _, o := range outcomes {
		if o.Err != "" {
			resp.Failed++
		}
	}

	resp.Candidates = s.judge(dedupe(results), req.Profile)
	return resp, nil
}

// fanOut queries indexers concurrently, bounded.
func (s *Service) fanOut(ctx context.Context, defs []indexer.Definition,
	q indexer.Query) ([]indexer.Result, []IndexerOutcome) {

	var (
		mu       sync.Mutex
		results  []indexer.Result
		outcomes = make([]IndexerOutcome, len(defs))
		wg       sync.WaitGroup
		sem      = make(chan struct{}, MaxParallelIndexers)
	)

	for i, d := range defs {
		wg.Add(1)
		go func(i int, d indexer.Definition) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				outcomes[i] = IndexerOutcome{
					IndexerID: d.ID, IndexerName: d.Name,
					Err: "the search was cancelled before this indexer was reached",
				}
				return
			}

			// Each indexer gets its own deadline. Without this a single slow
			// tracker consumes the whole budget and starves the others, which
			// looks exactly like "the release does not exist".
			idxCtx, cancel := context.WithTimeout(ctx, s.timeout)
			defer cancel()

			start := s.now()
			found, err := s.client.Search(idxCtx, d, q)
			outcome := IndexerOutcome{
				IndexerID: d.ID, IndexerName: d.Name,
				Duration: s.now().Sub(start), Results: len(found),
			}
			if err != nil {
				outcome.Err = err.Error()
				outcome.Results = 0
			}
			outcomes[i] = outcome

			if s.health != nil {
				// Health is recorded on a context that outlives the search's,
				// so a cancelled search still records why it was cancelled.
				_ = s.health.RecordResult(context.WithoutCancel(ctx), d.ID, err)
			}
			if err != nil {
				return
			}

			mu.Lock()
			results = append(results, found...)
			mu.Unlock()
		}(i, d)
	}

	wg.Wait()

	if len(results) > MaxCandidates {
		results = results[:MaxCandidates]
	}
	return results, outcomes
}

// deduped is one release plus the indexers that offered it.
//
// It exists so that dedupe can return the provenance alongside the result,
// rather than stashing it somewhere for judge() to find. The first version of
// this passed it through a package-level map, which is a data race the moment
// two searches overlap — and two searches overlapping is the normal case, since
// the RSS sync runs on a timer while an operator is using the UI.
type deduped struct {
	result indexer.Result
	seenOn []string
}

// dedupe merges the same release offered by several indexers.
//
// Identity is the infohash where there is one, because that is exactly what
// "the same torrent" means. Where there is not, the fallback is title plus
// size — not title alone, because two different encodes of the same film
// legitimately share a name, and collapsing them would silently discard the
// better one.
func dedupe(in []indexer.Result) []deduped {
	order := make([]string, 0, len(in))
	byKey := make(map[string]*deduped, len(in))

	for _, r := range in {
		key := strings.ToLower(r.InfoHash)
		if key == "" {
			key = strings.ToLower(r.Title) + "|" + fmt.Sprint(r.Size)
		}

		existing, ok := byKey[key]
		if !ok {
			byKey[key] = &deduped{result: r, seenOn: []string{r.IndexerName}}
			order = append(order, key)
			continue
		}

		if !contains(existing.seenOn, r.IndexerName) {
			existing.seenOn = append(existing.seenOn, r.IndexerName)
		}
		// Keep the copy with the most seeders: same content, better swarm.
		if r.Seeders > existing.result.Seeders {
			seen := existing.seenOn
			existing.result = r
			existing.seenOn = seen
		}
	}

	out := make([]deduped, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// judge applies the profile to every candidate and orders them.
func (s *Service) judge(results []deduped, profile *release.Profile) []Candidate {
	candidates := make([]Candidate, 0, len(results))

	for _, d := range results {
		r := d.result
		c := Candidate{
			Result:  r,
			Quality: release.QualityOf(r.Parsed),
			SeenOn:  d.seenOn,
		}

		if profile == nil {
			// No profile means the operator is judging. Everything is
			// "accepted" in the sense that nothing was refused on their behalf.
			c.Accepted = true
			candidates = append(candidates, c)
			continue
		}

		ok, rejection := profile.Accepts(r.Parsed)
		c.Accepted = ok
		c.Rejection = rejection
		c.Score = profile.Score(r.Parsed)
		candidates = append(candidates, c)
	}

	sortCandidates(candidates, profile)
	return candidates
}

// sortCandidates orders best-first, with rejected candidates last.
//
// Rejected ones are kept rather than dropped — they are the answer to "why did
// nothing get grabbed" — but they sort below everything acceptable so that
// Best() and the top of the UI are never a refusal.
func sortCandidates(cs []Candidate, profile *release.Profile) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.Accepted != b.Accepted {
			return a.Accepted
		}
		if profile != nil {
			ra, rb := profile.Rank(a.Parsed), profile.Rank(b.Parsed)
			if ra != rb {
				return ra > rb
			}
			if a.Parsed.Revision != b.Parsed.Revision {
				return a.Parsed.Revision > b.Parsed.Revision
			}
			if a.Score != b.Score {
				return a.Score > b.Score
			}
		}
		// Seeders last: it is a liveness signal, not a quality one, so it
		// breaks ties rather than driving the order. Sorting by seeders first
		// is how a library fills up with whatever happens to be popular.
		return a.Seeders > b.Seeders
	})
}

func filterIndexers(defs []indexer.Definition, ids []int64) []indexer.Definition {
	if len(ids) == 0 {
		return defs
	}
	want := make(map[int64]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := make([]indexer.Definition, 0, len(ids))
	for _, d := range defs {
		if want[d.ID] {
			out = append(out, d)
		}
	}
	return out
}

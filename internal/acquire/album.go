package acquire

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/search"
)

// Fetching wanted albums (ADR-0047) and books (ADR-0050): the search pass's
// rules, its lock, its gate, its budget and its grab, for what is ranked by a
// format ladder rather than a quality profile.

// losslessRank is FLAC's place on the format ladder (search.AudioQuality).
const losslessRank = 5

// ladder is one kind of thing such a pass fetches.
type ladder struct {
	// noun is "album" or "book", for the summary.
	noun string
	// none is the summary when nothing is wanted.
	none string
	// wanted reads the list; done says which are not to be fetched again.
	wanted func(ctx context.Context) ([]Want, error)
	done   func(ctx context.Context) (map[int64]bool, error)
	// doneKey is what done is keyed by for a want.
	doneKey func(w Want) int64
	// search asks the indexers for one want.
	search func(ctx context.Context, w Want) (search.Response, error)
	// gap, when set, is the least time between two searches for one want,
	// counted from the last whatever came of it (an upgrade's week).
	gap time.Duration
}

// RunAlbums searches for the wanted albums that are due, a few a pass, and
// then, with upgrades on, for lossless releases of albums held lossy
// (ADR-0062).
func (s *Service) RunAlbums(ctx context.Context) (string, error) {
	searchAlbum := func(ctx context.Context, w Want) (search.Response, error) {
		return s.finder.SearchAlbum(ctx, search.AlbumSearch{Want: search.AlbumWant{
			ItemID: w.ItemID, AlbumID: w.Album, Artists: []string{w.Artist}, Title: w.Title,
			Year: w.Year, Namesake: w.Namesake,
		}})
	}
	summary, err := s.runLadder(ctx, ladder{
		noun:    "album",
		none:    "no album is wanted with its track list known; no indexer was asked",
		wanted:  s.store.WantedAlbums,
		done:    s.store.AlbumsImported,
		doneKey: func(w Want) int64 { return w.Album },
		search:  searchAlbum,
	})
	if err != nil || !s.cfg.Upgrades {
		return summary, err
	}
	upgrades, err := s.runLadder(ctx, ladder{
		noun:   "album upgrade",
		none:   "no album holds a lossy track to upgrade",
		wanted: s.store.AlbumUpgrades,
		search: searchAlbum,
		gap:    UpgradeInterval,
	})
	if err != nil {
		return "", err
	}
	return summary + ". Upgrades: " + upgrades, nil
}

// RunBooks searches for the wanted books that are due, a few a pass. A book
// has no "once" rule: imported, it has its file and is not wanted (ADR-0050,
// decision 3).
func (s *Service) RunBooks(ctx context.Context) (string, error) {
	return s.runLadder(ctx, ladder{
		noun:   "book",
		none:   "no book is wanted with its author known; no indexer was asked",
		wanted: s.store.WantedBooks,
		search: func(ctx context.Context, w Want) (search.Response, error) {
			return s.finder.SearchBook(ctx, search.BookSearch{Want: search.BookWant{
				ItemID: w.ItemID, Title: w.Title, Author: w.Author,
			}})
		},
	})
}

func (s *Service) runLadder(ctx context.Context, l ladder) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, passDeadline)
	defer cancel()
	unlock, closedWhy, err := s.enter(ctx)
	if err != nil || closedWhy != "" {
		return closedWhy, err
	}
	defer unlock()

	wanted, err := l.wanted(ctx)
	if err != nil {
		return "", err
	}
	if len(wanted) == 0 {
		return l.none, nil
	}
	inFlight, err := s.store.InFlight(ctx)
	if err != nil {
		return "", err
	}
	done := map[int64]bool{}
	if l.done != nil {
		if done, err = l.done(ctx); err != nil {
			return "", err
		}
	}
	states, err := s.store.States(ctx)
	if err != nil {
		return "", err
	}
	p := &pass{how: howSearch, wanted: wanted, wantedSet: map[Key]bool{}, inFlight: inFlight,
		taken: map[Key]bool{}}
	for _, w := range wanted {
		p.wantedSet[w.Key()] = true
	}

	now := s.now()
	isDone := func(w Want) bool { return l.doneKey != nil && done[l.doneKey(w)] }
	due, flying, finished, waiting := dueLadder(p, isDone, states, now, l.gap)
	if len(due) == 0 {
		why := fmt.Sprintf("%d wanted, %d downloading", len(wanted), flying)
		if l.done != nil {
			why += fmt.Sprintf(", %d imported once already", finished)
		}
		return fmt.Sprintf("no %s is due (%s, %d waiting out a back-off)", l.noun, why, waiting), nil
	}

	var lines []string
	searched, failedN := 0, 0
	capped := false
	for _, w := range due {
		if searched >= s.cfg.SearchesPerRun {
			break
		}
		if p.grabs >= s.cfg.MaxGrabsPerRun {
			capped = true
			break
		}
		searched++
		before := states[w.StateKey()]
		resp, err := l.search(ctx, w)
		if errors.Is(err, search.ErrNoIndexers) {
			return "", ErrNoIndexers
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("acquire: the pass was cut short: %w", ctx.Err())
		}
		now := s.now()
		var st State
		switch {
		case err != nil:
			st = State{Outcome: OutcomeFailed, Fruitless: before.Fruitless,
				NextAt: now.Add(FailedRetry), Detail: "the search failed: " + err.Error()}
		case resp.Queried > 0 && resp.Failed == resp.Queried:
			st = State{Outcome: OutcomeFailed, Fruitless: before.Fruitless,
				NextAt: now.Add(FailedRetry), Detail: "no indexer answered: " + failures(resp)}
		default:
			if st, err = s.pick(ctx, p, w, resp, before, now); err != nil {
				return "", err
			}
		}
		if w.Upgrade && st.Outcome != OutcomeGrabbed {
			// A lossy album is a preference, not a gap: looked for again in
			// a week, whatever came of this (ADR-0062, decision 3).
			st.NextAt = now.Add(UpgradeInterval)
		}
		st.SearchedAt = now
		s.record(ctx, w, st)
		switch st.Outcome {
		case OutcomeGrabbed:
			lines = append(lines, "grabbed "+w.Name()+" ("+strings.TrimPrefix(st.Detail, "grabbed ")+")")
		case OutcomeFailed:
			failedN++
			lines = append(lines, "failed for "+w.Name()+" — "+st.Detail)
		default:
			lines = append(lines, fmt.Sprintf("nothing for %s — %s; next in %s",
				w.Name(), st.Detail, roughly(st.NextAt.Sub(now))))
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "searched for %d of %d due %s(s): %s", searched, len(due), l.noun, strings.Join(lines, "; "))
	if rest := len(due) - searched; rest > 0 {
		fmt.Fprintf(&b, ". %d more wait for the next pass", rest)
	}
	if capped {
		fmt.Fprintf(&b, ". The limit of %d grab(s) a pass was reached", s.cfg.MaxGrabsPerRun)
	}
	if searched > 0 && failedN == searched {
		return "", errors.New(b.String())
	}
	return b.String(), nil
}

// dueLadder lists the wants that may be searched for now — never searched
// first, newest first, then the longest waiting — and counts the rest by why
// they are not.
func dueLadder(p *pass, done func(Want) bool, states map[StateKey]State,
	now time.Time, gap time.Duration) (due []Want, flying, finished, waiting int) {
	var never, waited []Want
	for _, w := range p.wanted {
		st, ok := states[w.StateKey()]
		switch {
		case p.inFlight[w.Key()]:
			flying++
		case done(w):
			finished++
		case !ok || st.SearchedAt.IsZero():
			never = append(never, w)
		case gap > 0 && now.Before(st.SearchedAt.Add(gap)):
			waiting++
		case !st.NextAt.After(now):
			waited = append(waited, w)
		default:
			waiting++
		}
	}
	sort.SliceStable(never, func(i, j int) bool { return never[i].When.After(never[j].When) })
	sort.SliceStable(waited, func(i, j int) bool {
		return states[waited[i].StateKey()].SearchedAt.Before(states[waited[j].StateKey()].SearchedAt)
	})
	return append(never, waited...), flying, finished, waiting
}

// ladderEligible says whether a release that IS the wanted album or book may
// be grabbed for it, and why not (ADR-0047, decision 4; ADR-0050). No quality
// profile is consulted: the search's format ladder has refused what names no
// format.
func (s *Service) ladderEligible(ctx context.Context, _ *pass, c search.Candidate, w Want) (bool, string, error) {
	if w.Upgrade {
		// Lossless over lossy, the import's own rule (ADR-0062, decision 2).
		if q, rank := search.AudioQuality(c.Title); rank < losslessRank {
			return false, fmt.Sprintf("%s is not lossless, and an album is upgraded only to lossless", q), nil
		}
	}
	switch {
	case !c.Accepted:
		why := "refused"
		if c.Rejection != nil && c.Rejection.Detail != "" {
			why = c.Rejection.Detail
		}
		return false, why, nil
	case c.Seeders < 1:
		return false, "it has no seeders", nil
	case strings.TrimSpace(c.DownloadURL) == "":
		return false, "the indexer gave no link to fetch it by", nil
	case s.failedRecently(c):
		return false, "fetching it failed within the last few hours", nil
	}
	if queued, err := s.store.Queued(ctx, c.InfoHash); err != nil {
		return false, "", err
	} else if queued {
		return false, "it was grabbed before (a release already in the queue is never grabbed again)", nil
	}
	return true, "", nil
}

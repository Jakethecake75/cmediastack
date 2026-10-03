package search

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jakethecake75/cmediastack/internal/indexer"
)

// Errors the grab path distinguishes.
var (
	// ErrIndexerGone means the ticket named an indexer that is no longer
	// enabled, or no longer exists at all.
	ErrIndexerGone = errors.New("search: the indexer this release came from is no longer enabled")
	// ErrGrabUnavailable means no downloader was wired.
	ErrGrabUnavailable = errors.New("search: this service cannot grab; no downloader is wired")
)

// Downloader fetches what a candidate's link points at.
type Downloader interface {
	Download(ctx context.Context, d indexer.Definition, rawURL string) (indexer.Payload, error)
}

// Grabbed is a fetched release, ready for the download engine.
type Grabbed struct {
	indexer.Payload
	IndexerID   int64
	IndexerName string
	Title       string
	// SeedRatio and SeedTime are the indexer's seeding requirement as it stands
	// NOW, captured at the grab so the obligation is fixed by the rules in
	// force when it was incurred.
	SeedRatio float64
	SeedTime  time.Duration
}

// Grab turns a validated ticket into bytes or a magnet.
//
// The indexer is re-resolved against CURRENT state rather than trusted from the
// ticket. An operator who disables an indexer — because it was compromised,
// because its content turned out to be something they will not host, because
// they are done with it — has every right to expect that decision to take
// effect immediately. A ticket minted thirty minutes ago must not be a standing
// exemption from it. This is the same rule the API tokens follow: a capability
// is re-intersected with present authority every time it is used, never
// resolved once at issue.
func (s *Service) Grab(ctx context.Context, tk Ticket) (Grabbed, error) {
	if s.downloader == nil {
		return Grabbed{}, ErrGrabUnavailable
	}

	// Enabled is the only lookup used here, so "still enabled" is checked by
	// construction rather than by a boolean that could be read and ignored.
	defs, err := s.source.Enabled(ctx)
	if err != nil {
		return Grabbed{}, fmt.Errorf("search: loading indexers: %w", err)
	}
	var def *indexer.Definition
	for i := range defs {
		if defs[i].ID == tk.IndexerID {
			def = &defs[i]
			break
		}
	}
	if def == nil {
		return Grabbed{}, ErrIndexerGone
	}

	payload, err := s.downloader.Download(ctx, *def, tk.DownloadURL)
	if err != nil {
		return Grabbed{}, err
	}
	if payload.Magnet == "" && len(payload.Torrent) == 0 {
		// Defensive: a Downloader that returns neither and no error would
		// otherwise produce an empty transfer that looks like a successful grab.
		return Grabbed{}, fmt.Errorf("%w: the indexer returned nothing", indexer.ErrMalformed)
	}

	return Grabbed{
		Payload:     payload,
		IndexerID:   def.ID,
		IndexerName: def.Name,
		Title:       tk.Title,
		SeedRatio:   def.SeedRatio,
		SeedTime:    def.SeedTime,
	}, nil
}

package importer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/metadata"
)

// A title's rating (ADR-0037, decision 3). The provider's, fetched by a task,
// or a person's, which the task never overwrites. An unrated title is hidden
// from every account with a ceiling.

// ErrUnknownCertification refuses a rating this instance cannot rank.
var ErrUnknownCertification = errors.New("importer: that is not a rating this instance knows " +
	"(G, PG, PG-13, R, NC-17, TV-Y, TV-Y7, TV-G, TV-PG, TV-14, TV-MA)")

// UnratedRecheck is how long a title the provider did not rate waits before it
// is asked about again. A rated title is not asked again: ratings do not
// change often enough to spend a request an hour on.
const UnratedRecheck = 30 * 24 * time.Hour

// RatingSubject is a title the ratings task will ask about.
type RatingSubject struct {
	ID     int64
	Kind   string
	TMDBID int64
	Title  string
}

// TitlesToRate returns identified titles whose rating was never asked for, and
// unrated ones last asked long enough ago — never one a person rated.
//
// Browse, like attaching a provider id: a rating is a fact recorded about a
// title (ADR-0019).
func (s *Store) TitlesToRate(ctx context.Context, limit int) ([]RatingSubject, error) {
	// Unscoped: the ratings task rates the whole library (ADR-0037).
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	stale := s.now().UTC().Add(-UnratedRecheck).Format(timeLayout)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kind, tmdb_id, title FROM media_item
		WHERE tmdb_id IS NOT NULL AND tmdb_id > 0
		  AND COALESCE(rating_source, '') <> 'person'
		  AND (rating_checked_at IS NULL
		       OR (rating_rank IS NULL AND rating_checked_at < ?))
		ORDER BY COALESCE(rating_checked_at, ''), id
		LIMIT ?`, stale, limit)
	if err != nil {
		return nil, fmt.Errorf("importer: choosing titles to rate: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []RatingSubject
	for rows.Next() {
		var r RatingSubject
		if err := rows.Scan(&r.ID, &r.Kind, &r.TMDBID, &r.Title); err != nil {
			return nil, fmt.Errorf("importer: choosing titles to rate: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordProviderRating records what the provider said a title is rated: a
// certification this instance knows, or unrated. A person's rating is left as
// it is.
func (s *Store) RecordProviderRating(ctx context.Context, id int64, certification string) error {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return err
	}
	cert := library.CanonicalCertification(certification)
	var certV, rankV, sourceV any
	if cert != "" {
		certV, rankV, sourceV = cert, library.RatingRank(cert), "provider"
	}
	now := s.now().UTC().Format(timeLayout)
	if _, err := s.db.ExecContext(ctx, `
		UPDATE media_item
		SET certification = ?, rating_rank = ?, rating_source = ?, rating_checked_at = ?
		WHERE id = ? AND COALESCE(rating_source, '') <> 'person'`,
		certV, rankV, sourceV, now, id); err != nil {
		return fmt.Errorf("importer: recording a rating: %w", err)
	}
	return nil
}

// SetRating is a person rating a title, or with "" giving it back to the
// provider, which is asked again on the task's next run. Only a title the
// person may see; the item as it was and as it became are returned for the
// audit line.
func (s *Store) SetRating(ctx context.Context, id int64, certification string) (Item, Item, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return Item{}, Item{}, err
	}
	items, err := s.queryItems(ctx, visibleTo(ctx), `WHERE id = ?`, id)
	if err != nil {
		return Item{}, Item{}, err
	}
	if len(items) == 0 {
		return Item{}, Item{}, ErrItemNotFound
	}
	before := items[0]
	after := before

	var certV, rankV, sourceV, checkedV any
	if certification != "" {
		cert := library.CanonicalCertification(certification)
		if cert == "" {
			return Item{}, Item{}, ErrUnknownCertification
		}
		now := s.now().UTC().Format(timeLayout)
		certV, rankV, sourceV, checkedV = cert, library.RatingRank(cert), "person", now
		after.Certification, after.RatingRank, after.RatingSource = cert, library.RatingRank(cert), "person"
	} else {
		after.Certification, after.RatingRank, after.RatingSource = "", 0, ""
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE media_item
		SET certification = ?, rating_rank = ?, rating_source = ?, rating_checked_at = ?,
		    updated_at = ?
		WHERE id = ?`,
		certV, rankV, sourceV, checkedV, s.now().UTC().Format(timeLayout), id); err != nil {
		return Item{}, Item{}, fmt.Errorf("importer: setting a rating: %w", err)
	}
	return before, after, nil
}

// RatingPass is what one run of the ratings task did.
type RatingPass struct {
	Asked, Rated, Unrated int
}

// Summary says it for the tasks screen.
func (p RatingPass) Summary() string {
	return fmt.Sprintf("%d title(s) asked about: %d rated, %d unrated", p.Asked, p.Rated, p.Unrated)
}

// RateTitles asks the provider about up to limit titles and records each
// answer. A title the provider does not know is recorded as unrated, so it is
// not asked about every hour; anything else — no provider, a refused key, a
// rate limit, an outage — stops the pass, and what was not asked is asked next
// time.
func RateTitles(ctx context.Context, s *Store, c metadata.Certifier, limit int) (RatingPass, error) {
	var pass RatingPass
	subjects, err := s.TitlesToRate(ctx, limit)
	if err != nil {
		return pass, err
	}
	for _, sub := range subjects {
		kind := metadata.KindMovie
		if sub.Kind == KindSeries {
			kind = metadata.KindSeries
		}
		cert, err := c.Certification(ctx, kind, sub.TMDBID)
		switch {
		case errors.Is(err, metadata.ErrNotFound):
			cert = ""
		case err != nil:
			return pass, fmt.Errorf("asking about %s: %w", sub.Title, err)
		}
		pass.Asked++
		if err := s.RecordProviderRating(ctx, sub.ID, cert); err != nil {
			return pass, err
		}
		if library.RatingRank(cert) > 0 {
			pass.Rated++
		} else {
			pass.Unrated++
		}
	}
	return pass, nil
}

package library

import (
	"context"
	"errors"
	"fmt"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// ErrNoSuchSeason means the series has no such season.
var ErrNoSuchSeason = errors.New("library: no such season")

// SeasonSearchSubject is what a search for one season needs: the series, and
// the episode numbers the provider lists for the season (ADR-0033).
type SeasonSearchSubject struct {
	ItemID      int64
	Season      int
	SeriesTitle string
	SeriesYear  int
	TMDBID      int64
	// Episodes are the season's episode numbers, in order.
	Episodes []int
	// Have is how many of them are on disk.
	Have int
	// QualityProfileID is the series' own profile; zero is the default
	// (ADR-0035).
	QualityProfileID int64
	// LastSeason is the series' highest listed regular season: how far a
	// complete-series pack reaches (ADR-0057).
	LastSeason int
}

// ForSeasonSearch returns one season of a series, for its search.
func (s *EpisodeStore) ForSeasonSearch(ctx context.Context, itemID int64, season int) (SeasonSearchSubject, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return SeasonSearchSubject{}, err
	}
	sub := SeasonSearchSubject{ItemID: itemID, Season: season}
	visible, vargs := Visible(ctx, "i")
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.number,
		       EXISTS (SELECT 1 FROM media_file f
		               WHERE f.item_id = e.item_id AND f.season = e.season_number
		                 AND e.number BETWEEN f.episode AND COALESCE(f.episode_last, f.episode)),
		       i.title, COALESCE(i.year, 0), COALESCE(i.tmdb_id, 0),
		       COALESCE(i.quality_profile_id, 0),
		       (SELECT COALESCE(MAX(l.season_number), 0) FROM episode l
		        WHERE l.item_id = e.item_id AND l.season_number > 0)
		FROM episode e
		JOIN media_item i ON i.id = e.item_id
		WHERE e.item_id = ? AND e.season_number = ? AND `+visible+`
		ORDER BY e.number`, append([]any{itemID, season}, vargs...)...)
	if err != nil {
		return SeasonSearchSubject{}, fmt.Errorf("library: reading a season: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var n, have int
		if err := rows.Scan(&n, &have, &sub.SeriesTitle, &sub.SeriesYear, &sub.TMDBID,
			&sub.QualityProfileID, &sub.LastSeason); err != nil {
			return SeasonSearchSubject{}, fmt.Errorf("library: reading a season: %w", err)
		}
		sub.Episodes = append(sub.Episodes, n)
		sub.Have += have
	}
	if err := rows.Err(); err != nil {
		return SeasonSearchSubject{}, fmt.Errorf("library: reading a season: %w", err)
	}
	if len(sub.Episodes) == 0 {
		// A season with nothing listed has nothing a pack's files could be
		// told apart by, which for a search is the same as not existing.
		return SeasonSearchSubject{}, ErrNoSuchSeason
	}
	return sub, nil
}

package library

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// CalendarEntry is one episode on the calendar feed (ADR-0041).
type CalendarEntry struct {
	EpisodeID  int64
	ItemID     int64
	Series     string
	SeriesYear int
	Season     int
	Number     int
	Title      string
	Aired      time.Time
	Have       bool
}

// Calendar returns the episodes with an air date in [from, to) of every series
// the caller may see (ADR-0037), in air order.
func (s *EpisodeStore) Calendar(ctx context.Context, from, to time.Time) ([]CalendarEntry, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	visible, vargs := Visible(ctx, "i")
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.item_id, i.title, COALESCE(i.year, 0), e.season_number, e.number,
		       e.title, e.aired_at,
		       EXISTS (SELECT 1 FROM media_file f
		               WHERE f.item_id = e.item_id AND f.season = e.season_number
		                 AND e.number BETWEEN f.episode AND COALESCE(f.episode_last, f.episode))
		FROM episode e
		JOIN media_item i ON i.id = e.item_id
		WHERE `+visible+`
		  AND e.aired_at IS NOT NULL AND e.aired_at >= ? AND e.aired_at < ?
		ORDER BY e.aired_at, i.sort_title, e.season_number, e.number
		LIMIT 2000`,
		append(vargs, from.UTC().Format(episodeTimeLayout), to.UTC().Format(episodeTimeLayout))...)
	if err != nil {
		return nil, fmt.Errorf("library: reading the calendar: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []CalendarEntry
	for rows.Next() {
		var c CalendarEntry
		var aired sql.NullString
		var have int
		if err := rows.Scan(&c.EpisodeID, &c.ItemID, &c.Series, &c.SeriesYear, &c.Season, &c.Number,
			&c.Title, &aired, &have); err != nil {
			return nil, fmt.Errorf("library: reading the calendar: %w", err)
		}
		c.Aired = parseNullTime(aired)
		c.Have = have == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

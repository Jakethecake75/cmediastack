package acquire

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// wantedAlbumWhere is the Wanted screen's condition for an album
// (music.Store.WantedAlbums) — monitored, released, a track without a file —
// less the albums whose track list is not known, which the screen lists and
// the import could not file (ADR-0047, decision 2). "A track without a file"
// says both: an album with no track list has no track at all.
const wantedAlbumWhere = `
		a.monitored = 1
		AND a.released_at IS NOT NULL
		AND a.released_at <= ?
		AND EXISTS (SELECT 1 FROM track t WHERE t.album_id = a.id AND t.file_id IS NULL)`

// releasedBy is today, as an album's release date is compared.
func (s *Store) releasedBy() string { return s.now().UTC().Format("2006-01-02") }

// WantedAlbums returns every album automatic acquisition may look for, newest
// release first.
func (s *Store) WantedAlbums(ctx context.Context) ([]Want, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	// Unscoped: automatic acquisition works for the whole Wanted list (ADR-0047), not for one person's view of it.
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.item_id, a.title, a.released_at, i.title
		FROM album a JOIN media_item i ON i.id = a.item_id
		WHERE`+wantedAlbumWhere+`
		ORDER BY a.released_at DESC, a.id DESC`, s.releasedBy())
	if err != nil {
		return nil, fmt.Errorf("acquire: reading the wanted albums: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Want
	for rows.Next() {
		var w Want
		var released string
		if err := rows.Scan(&w.Album, &w.ItemID, &w.Title, &released, &w.Artist); err != nil {
			return nil, fmt.Errorf("acquire: reading the wanted albums: %w", err)
		}
		if len(released) >= 4 {
			w.Year, _ = strconv.Atoi(released[:4])
		}
		for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
			if at, perr := time.Parse(layout, released); perr == nil {
				w.When = at
				break
			}
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("acquire: reading the wanted albums: %w", err)
	}
	return out, s.markNamesakes(ctx, out)
}

// markNamesakes says which albums' artists have another album of the same
// folded title.
func (s *Store) markNamesakes(ctx context.Context, out []Want) error {
	if len(out) == 0 {
		return nil
	}
	folded, err := s.albumTitles(ctx)
	if err != nil {
		return err
	}
	for i := range out {
		mine := search.NormalizeTitle(out[i].Title)
		for _, o := range folded[out[i].ItemID] {
			if o.id != out[i].Album && o.title == mine {
				out[i].Namesake = true
			}
		}
	}
	return nil
}

// upgradableAlbumWhere is an album that may be upgraded (ADR-0062): monitored,
// holding a track in a lossy format the music import names (music.AudioQuality)
// — which it would replace with a lossless one, and nothing else
// (music.Better). A file of no known format is not taken for lossy.
const upgradableAlbumWhere = `
		a.monitored = 1
		AND EXISTS (SELECT 1 FROM track t JOIN media_file f ON f.id = t.file_id
		            WHERE t.album_id = a.id AND f.quality IN ('MP3', 'AAC', 'Vorbis', 'Opus'))`

// AlbumUpgrades returns every album that may be upgraded to lossless: the
// ones the wanted pass has nothing more to do for (ADR-0062, decision 2).
func (s *Store) AlbumUpgrades(ctx context.Context) ([]Want, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	// Unscoped: automatic acquisition works for the whole library (ADR-0062), not for one person's view of it.
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.item_id, a.title, COALESCE(a.released_at, ''), i.title,
		       (SELECT f.quality FROM track t JOIN media_file f ON f.id = t.file_id
		         WHERE t.album_id = a.id AND f.quality IN ('MP3', 'AAC', 'Vorbis', 'Opus') ORDER BY f.quality LIMIT 1)
		FROM album a JOIN media_item i ON i.id = a.item_id
		WHERE`+upgradableAlbumWhere+`
		ORDER BY a.id`)
	if err != nil {
		return nil, fmt.Errorf("acquire: reading the albums to upgrade: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Want
	for rows.Next() {
		w := Want{Upgrade: true}
		var released string
		if err := rows.Scan(&w.Album, &w.ItemID, &w.Title, &released, &w.Artist, &w.HaveAudio); err != nil {
			return nil, fmt.Errorf("acquire: reading the albums to upgrade: %w", err)
		}
		if len(released) >= 4 {
			w.Year, _ = strconv.Atoi(released[:4])
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("acquire: reading the albums to upgrade: %w", err)
	}
	if len(out) == 0 {
		return out, nil
	}
	// The wanted pass's own albums first: one still missing tracks and never
	// imported is fetched by it, in the best format found.
	wanted, err := s.WantedAlbums(ctx)
	if err != nil {
		return nil, err
	}
	imported, err := s.AlbumsImported(ctx)
	if err != nil {
		return nil, err
	}
	waiting := map[int64]bool{}
	for _, w := range wanted {
		if !imported[w.Album] {
			waiting[w.Album] = true
		}
	}
	kept := out[:0]
	for _, w := range out {
		if !waiting[w.Album] {
			kept = append(kept, w)
		}
	}
	return kept, s.markNamesakes(ctx, kept)
}

type foldedAlbum struct {
	id    int64
	title string
}

// albumTitles is every album's folded title, by artist.
func (s *Store) albumTitles(ctx context.Context) (map[int64][]foldedAlbum, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, item_id, title FROM album`)
	if err != nil {
		return nil, fmt.Errorf("acquire: reading album titles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][]foldedAlbum{}
	for rows.Next() {
		var id, item int64
		var title string
		if err := rows.Scan(&id, &item, &title); err != nil {
			return nil, fmt.Errorf("acquire: reading album titles: %w", err)
		}
		out[item] = append(out[item], foldedAlbum{id: id, title: search.NormalizeTitle(title)})
	}
	return out, rows.Err()
}

// AlbumsImported is every album a download grabbed for it was imported for:
// automatic acquisition grabs an album once (ADR-0047, decision 3).
func (s *Store) AlbumsImported(ctx context.Context) (map[int64]bool, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT q.target_album_id FROM download_queue q
		WHERE q.target_kind = 'album' AND q.target_album_id IS NOT NULL
		  AND EXISTS (SELECT 1 FROM import_record r
		              WHERE r.info_hash = q.info_hash AND r.outcome = 'imported')`)
	if err != nil {
		return nil, fmt.Errorf("acquire: reading the albums already imported: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("acquire: reading the albums already imported: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// wantedBookWhere is the Wanted screen's condition for a book — monitored, no
// file — with its author required: without one no release can be told from
// another book of the same name (ADR-0050, decision 2).
const wantedBookWhere = `
		i.kind = 'book'
		AND i.monitored = 1
		AND COALESCE(TRIM(i.author), '') <> ''
		AND NOT EXISTS (SELECT 1 FROM media_file f WHERE f.item_id = i.id)`

// WantedBooks returns every book automatic acquisition may look for, the most
// recently added first.
func (s *Store) WantedBooks(ctx context.Context) ([]Want, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}
	// Unscoped: automatic acquisition works for the whole Wanted list (ADR-0050), not for one person's view of it.
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.title, COALESCE(i.year, 0), i.author, i.added_at
		FROM media_item i
		WHERE`+wantedBookWhere+`
		ORDER BY i.added_at DESC, i.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("acquire: reading the wanted books: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Want
	for rows.Next() {
		w := Want{Book: true}
		var added string
		if err := rows.Scan(&w.ItemID, &w.Title, &w.Year, &w.Author, &added); err != nil {
			return nil, fmt.Errorf("acquire: reading the wanted books: %w", err)
		}
		w.When, _ = time.Parse(timeLayout, added)
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("acquire: reading the wanted books: %w", err)
	}
	return out, nil
}

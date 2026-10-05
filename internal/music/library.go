package music

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// The scan and the import of music files (ADR-0045).

// Roots opens the library's locations.
type Roots interface {
	List(ctx context.Context) ([]library.RootFolder, error)
	OpenVault(ctx context.Context, id int64) (*library.Vault, error)
}

// Library scans music roots and imports downloaded albums.
type Library struct {
	svc   *Service
	roots Roots
	log   *slog.Logger
	now   func() time.Time
}

// NewLibrary builds one.
func NewLibrary(svc *Service, roots Roots, log *slog.Logger, now func() time.Time) *Library {
	if log == nil {
		log = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &Library{svc: svc, roots: roots, log: log, now: now}
}

// Reasons a music file is left alone.
const (
	ReasonNoArtist   = "no followed artist owns this folder; add the artist from MusicBrainz"
	ReasonNoAlbum    = "the folder is not one of this artist's albums"
	ReasonNoTrack    = "no track of the album matches this file's number or title"
	ReasonTrackTaken = "another file already holds this track"
	ReasonNotInAlbum = "not inside an artist folder and an album folder"
	ReasonNoTracks   = "the album's track list is not known yet"
)

type artistRow struct {
	id     int64
	name   string
	folder string
}

// artistsIn reads the followed artists of a root, by folder.
func (l *Library) artistsIn(ctx context.Context, rootID int64) (map[string]artistRow, error) {
	// Unscoped: the scan and the import work for the whole library.
	rows, err := l.svc.db.QueryContext(ctx,
		`SELECT id, title, folder FROM media_item WHERE kind = ? AND root_folder_id = ?`, KindArtist, rootID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]artistRow{}
	for rows.Next() {
		var a artistRow
		if err := rows.Scan(&a.id, &a.name, &a.folder); err != nil {
			return nil, err
		}
		out[a.folder] = a
	}
	return out, rows.Err()
}

// heldFiles maps an album's tracks to the files they hold and those files'
// qualities.
func (l *Library) heldFiles(ctx context.Context, albumID int64) (map[int64]string, map[int64]string, error) {
	rows, err := l.svc.db.QueryContext(ctx, `
		SELECT t.id, f.relative_path, f.quality FROM track t JOIN media_file f ON f.id = t.file_id
		WHERE t.album_id = ?`, albumID)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	paths, quality := map[int64]string{}, map[int64]string{}
	for rows.Next() {
		var id int64
		var p, q string
		if err := rows.Scan(&id, &p, &q); err != nil {
			return nil, nil, err
		}
		paths[id], quality[id] = p, q
	}
	return paths, quality, rows.Err()
}

// knownMusic is what the database holds of a root's music: path to file id.
func (l *Library) knownMusic(ctx context.Context, rootID int64) (map[string]int64, error) {
	// Unscoped: the scan reconciles the whole root.
	rows, err := l.svc.db.QueryContext(ctx, `
		SELECT f.id, f.relative_path FROM media_file f
		WHERE f.root_folder_id = ? AND f.item_id IN (SELECT id FROM media_item WHERE kind = ?)`,
		rootID, KindArtist)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		out[p] = id
	}
	return out, rows.Err()
}

// albumWithTracks returns an album with its track list, fetching it first when
// it has never been read.
func (l *Library) albumWithTracks(ctx context.Context, cache map[int64]Album, id int64) (Album, error) {
	if a, ok := cache[id]; ok {
		return a, nil
	}
	a, err := l.svc.Album(ctx, id)
	if err != nil {
		return Album{}, err
	}
	cache[id] = a
	return a, nil
}

// recordFile writes a music file's row and gives its track the file.
func (l *Library) recordFile(ctx context.Context, artistID, rootID int64, rel string, size int64,
	infoHash string, hardlinked bool, trackID int64) (int64, error) {
	now := l.now().UTC().Format(timeLayout)
	var hash any
	if infoHash != "" {
		hash = infoHash
	}
	var fileID int64
	err := l.svc.db.InTx(ctx, func(tx db.Execer) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO media_file (item_id, root_folder_id, relative_path, size_bytes, quality, revision,
			                        release_title, info_hash, hardlinked, imported_at)
			VALUES (?, ?, ?, ?, ?, 0, '', ?, ?, ?)
			ON CONFLICT(root_folder_id, relative_path) DO UPDATE SET
			    item_id = excluded.item_id, size_bytes = excluded.size_bytes, quality = excluded.quality,
			    info_hash = excluded.info_hash, hardlinked = excluded.hardlinked, imported_at = excluded.imported_at`,
			artistID, rootID, rel, size, AudioQuality(rel), hash, boolInt(hardlinked), now); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT id FROM media_file WHERE root_folder_id = ? AND relative_path = ?`,
			rootID, rel).Scan(&fileID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE track SET file_id = ? WHERE id = ?`, fileID, trackID)
		return err
	})
	return fileID, err
}

// Scan walks a music root and records the files of followed artists' albums
// (ADR-0045, decision 4). It changes nothing on disk.
func (l *Library) Scan(ctx context.Context, rootID int64) (importer.ScanResult, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return importer.ScanResult{}, err
	}
	started := l.now()
	roots, err := l.roots.List(ctx)
	if err != nil {
		return importer.ScanResult{}, err
	}
	var root library.RootFolder
	for _, r := range roots {
		if r.ID == rootID {
			root = r
		}
	}
	if root.ID == 0 {
		return importer.ScanResult{}, library.ErrRootNotFound
	}
	vault, err := l.roots.OpenVault(ctx, rootID)
	if err != nil {
		return importer.ScanResult{}, err
	}
	defer func() { _ = vault.Close() }()

	res := importer.ScanResult{RootID: rootID, Root: root.Path}
	artists, err := l.artistsIn(ctx, rootID)
	if err != nil {
		return res, err
	}
	known, err := l.knownMusic(ctx, rootID)
	if err != nil {
		return res, err
	}
	seen := map[string]bool{}
	albumsOf := map[int64][]Album{}
	withTracks := map[int64]Album{}
	skip := func(p string, size int64, why string) {
		res.Skipped = append(res.Skipped, importer.Rejection{Path: p, Bytes: size, Reason: why})
	}

	walkErr := fs.WalkDir(vault.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			// One unreadable directory must not end the scan; it is logged.
			l.log.Warn("a directory could not be read during a music scan",
				slog.String("path", p), slog.String("error", err.Error()))
			return nil //nolint:nilerr // the rest of the root is still worth scanning
		}
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 || !IsAudio(p) {
			return nil
		}
		if res.Scanned >= importer.MaxScanFiles {
			res.Truncated = true
			return fs.SkipAll
		}
		res.Scanned++
		var size int64
		if info, ierr := d.Info(); ierr == nil {
			size = info.Size()
		}
		comps := strings.Split(p, "/")
		if len(comps) < 3 {
			skip(p, size, ReasonNotInAlbum)
			return nil
		}
		artist, ok := artists[comps[0]]
		if !ok {
			skip(p, size, ReasonNoArtist)
			return nil
		}
		if _, ok := albumsOf[artist.id]; !ok {
			if albumsOf[artist.id], err = l.svc.store.Albums(ctx, artist.id); err != nil {
				return err
			}
		}
		album, ok := AlbumOfFolder(comps[1], artist.name, albumsOf[artist.id])
		if !ok {
			skip(p, size, ReasonNoAlbum)
			return nil
		}
		full, err := l.albumWithTracks(ctx, withTracks, album.ID)
		if err != nil || len(full.Tracks) == 0 {
			skip(p, size, ReasonNoTracks)
			return nil //nolint:nilerr // one album's track list not being known is a line in the result
		}
		track, ok := MatchTrack(strings.Join(comps[2:], "/"), full.Tracks)
		if !ok {
			skip(p, size, ReasonNoTrack)
			return nil
		}
		seen[p] = true
		if id, already := known[p]; already && track.FileID == id {
			res.Updated++
			return nil
		}
		if track.FileID != 0 && track.FileID != known[p] {
			skip(p, size, ReasonTrackTaken)
			return nil
		}
		if _, err := l.recordFile(ctx, artist.id, rootID, p, size, "", false, track.ID); err != nil {
			l.log.Warn("a music file found by the scan could not be recorded",
				slog.String("path", p), slog.String("error", err.Error()))
			skip(p, size, err.Error())
			return nil
		}
		for i := range full.Tracks {
			if full.Tracks[i].ID == track.ID {
				full.Tracks[i].FileID = -1
			}
		}
		withTracks[album.ID] = full
		if _, already := known[p]; already {
			res.Updated++
		} else {
			res.Added++
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return res, fmt.Errorf("music: scanning %s: %w", root.Path, walkErr)
	}
	for p := range known {
		if !seen[p] {
			res.Missing = append(res.Missing, p)
		}
	}
	sort.Strings(res.Missing)
	res.Elapsed = l.now().Sub(started)
	if len(known) > 0 && float64(len(res.Missing))/float64(len(known)) > importer.MissingRatioGuard {
		return res, fmt.Errorf("%w: %d of %d recorded music files are not on disk in %s. Nothing has been "+
			"changed; check the mount", importer.ErrLibraryVanished, len(res.Missing), len(known), root.Path)
	}
	return res, nil
}

// ImportResult is what importing a downloaded album did.
type ImportResult struct {
	Placed   []string
	Replaced []string
	Kept     []string
	Skipped  []importer.Rejection
}

// Summary says it in a line.
func (r ImportResult) Summary() string {
	return fmt.Sprintf("%d track(s) placed, %d replaced, %d kept as they were, %d file(s) left in the download",
		len(r.Placed), len(r.Replaced), len(r.Kept), len(r.Skipped))
}

// ImportAlbum files a downloaded album track by track (ADR-0045, decision 5):
// each audio file matched to a track, hard-linked (or copied across
// filesystems) to its place, a track's lossy file replaced only by a lossless
// one and trashed.
func (l *Library) ImportAlbum(ctx context.Context, src *library.ContainedSource, files []string,
	albumID int64, infoHash string) (ImportResult, error) {
	var res ImportResult
	album, err := l.svc.Album(ctx, albumID)
	if err != nil {
		return res, err
	}
	if len(album.Tracks) == 0 {
		return res, fmt.Errorf("music: %s: %s", album.Title, ReasonNoTracks)
	}
	// Unscoped: the import files for the whole library.
	var rootID int64
	var artistFolder string
	if err := l.svc.db.QueryRowContext(ctx, `SELECT root_folder_id, folder FROM media_item WHERE id = ?`,
		album.ItemID).Scan(&rootID, &artistFolder); err != nil {
		return res, err
	}
	vault, err := l.roots.OpenVault(ctx, rootID)
	if err != nil {
		return res, err
	}
	defer func() { _ = vault.Close() }()
	multi := false
	for _, t := range album.Tracks {
		if t.Disc > 1 {
			multi = true
		}
	}
	existing, quality, err := l.heldFiles(ctx, albumID)
	if err != nil {
		return res, err
	}

	claimed := map[int64]bool{}
	ordered := slices.Clone(files)
	sort.Strings(ordered)
	for _, rel := range ordered {
		if !IsAudio(rel) {
			continue
		}
		track, ok := MatchTrack(rel, album.Tracks)
		if !ok || claimed[track.ID] {
			why := ReasonNoTrack
			if ok {
				why = "two files claim the same track"
			}
			res.Skipped = append(res.Skipped, importer.Rejection{Path: rel, Reason: why})
			continue
		}
		claimed[track.ID] = true
		newQuality := AudioQuality(rel)
		if old, has := existing[track.ID]; has {
			if !Better(newQuality, quality[track.ID]) {
				res.Kept = append(res.Kept, old)
				continue
			}
			if _, err := vault.Supersede(ctx, old, l.now()); err != nil {
				return res, fmt.Errorf("music: moving %s to the trash: %w", old, err)
			}
			res.Replaced = append(res.Replaced, old)
		}
		dst, err := TrackPath(artistFolder, album, track, path.Ext(rel), multi)
		if err != nil {
			res.Skipped = append(res.Skipped, importer.Rejection{Path: rel, Reason: err.Error()})
			continue
		}
		// One copy, which the download seeds (ADR-0076).
		copies, err := library.Place(src, rel, vault, dst)
		if err != nil {
			return res, err
		}
		hardlinked := copies == 1
		var size int64
		if info, serr := vault.Stat(dst); serr == nil {
			size = info.Size()
		}
		if _, err := l.recordFile(ctx, album.ItemID, rootID, dst, size, infoHash, hardlinked, track.ID); err != nil {
			return res, err
		}
		res.Placed = append(res.Placed, dst)
	}
	return res, nil
}

// ImportDownload files a completed download grabbed for an album (ADR-0046,
// decision 6): the download's directory, opened contained, and its files
// relative to it.
func (l *Library) ImportDownload(ctx context.Context, dir string, files []string,
	albumID int64, infoHash string) (ImportResult, error) {
	src, err := library.OpenSource(dir)
	if err != nil {
		return ImportResult{}, err
	}
	defer func() { _ = src.Close() }()
	return l.ImportAlbum(ctx, src, files, albumID, infoHash)
}

// ScanDispatch sends a scan to the music library for a music root, to the
// books library for a books root (ADR-0049), and to the video importer for any
// other.
type ScanDispatch struct {
	Video Scanner
	Music *Library
	Books Scanner
	Roots Roots
}

// Scanner scans one root folder.
type Scanner interface {
	Scan(ctx context.Context, rootID int64) (importer.ScanResult, error)
}

// Scan scans one root with whichever understands it.
func (d ScanDispatch) Scan(ctx context.Context, rootID int64) (importer.ScanResult, error) {
	roots, err := d.Roots.List(ctx)
	if err != nil {
		return importer.ScanResult{}, err
	}
	for _, r := range roots {
		if r.ID == rootID && r.Kind == library.KindMusic && d.Music != nil {
			return d.Music.Scan(ctx, rootID)
		}
		if r.ID == rootID && r.Kind == library.KindBooks && d.Books != nil {
			return d.Books.Scan(ctx, rootID)
		}
	}
	return d.Video.Scan(ctx, rootID)
}

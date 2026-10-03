package importer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/release"
)

// Scanning: reconciling the database with what is actually on disk.
//
// # Why this exists before anything else in Phase 3 is finished
//
// An operator adopting this software already HAS a library — that is the point
// of replacing the stack they are running. Until a scan exists, CMediaStack can
// see none of it: the database knows only about files its own importer placed,
// so a working instance looks empty beside forty terabytes of media.
//
// # The one rule this must never break
//
// **A scan does not change anything on disk.** It does not rename, move,
// reorganise or delete. Some software in this category "organises on scan", and
// that is how people lose libraries: a parser misreads a title, a thousand
// directories are renamed, and there is no undo. Here the layout an operator
// chose is the layout that stays, and the database records files where they
// are, under whatever names they have.
//
// TestAScanNeverChangesAnythingOnDisk snapshots the whole tree before and after
// and requires them byte-identical, so the rule is enforced rather than
// promised.

// Scan limits.
const (
	// MaxScanFiles bounds one scan. A library of a hundred thousand files is
	// within the stated scale (§13: ≤100k items), and this is well above it —
	// the cap exists so a root folder accidentally pointed at "/" produces a
	// bounded, honest report rather than an unbounded walk.
	MaxScanFiles = 500_000

	// MissingRatioGuard is the share of a root's known files that may vanish
	// between scans before the scan refuses to record them as missing.
	//
	// This is the most important number in this file. If a mount fails, the
	// root folder appears EMPTY, and a scan that dutifully marked everything
	// missing would erase the library record of every file on that disk — in
	// one pass, with nothing on disk changed to explain it. Refusing loudly
	// when most of a library disappears at once is the difference between an
	// operator remounting a disk and an operator restoring a backup.
	MissingRatioGuard = 0.20
)

// ErrLibraryVanished means most of a root's files disappeared at once, which is
// far more likely to be a failed mount than a deletion.
var ErrLibraryVanished = errors.New("importer: most of this root folder's files are missing")

// ScanResult is what one scan found.
type ScanResult struct {
	RootID  int64
	Root    string
	Scanned int
	Added   int
	Updated int
	// Missing are recorded files whose bytes are no longer on disk. Reported,
	// never acted on: a scan does not change the database's mind about a file
	// it cannot see, because the commonest reason it cannot see one is that a
	// disk is not mounted.
	Missing []string
	// Skipped are files the scan examined and did not record, with reasons.
	Skipped []Rejection
	// Truncated is set when the cap was hit, so a partial result cannot be
	// mistaken for a complete one.
	Truncated bool
	Elapsed   time.Duration
}

// Summary renders a scan for a log line.
func (r ScanResult) Summary() string {
	out := fmt.Sprintf("%d file(s) examined, %d added, %d updated", r.Scanned, r.Added, r.Updated)
	if n := len(r.Missing); n > 0 {
		out += fmt.Sprintf(", %d recorded file(s) no longer on disk", n)
	}
	if n := len(r.Skipped); n > 0 {
		out += fmt.Sprintf(", %d skipped", n)
	}
	if r.Truncated {
		out += fmt.Sprintf(" (STOPPED at the %d-file cap; this is a partial result)", MaxScanFiles)
	}
	return out
}

// Scan reconciles one root folder with the database.
//
// It reads. It records. It changes nothing on disk.
//
// Symlinked directories inside a root are NOT followed. That is the safe
// default rather than an oversight: a link pointing outside the root would
// otherwise index somebody's /etc, and the kernel refuses to read through it
// anyway (os.Root, ADR-0015). An operator who organises with internal symlinks
// will find those trees unscanned, which is visible in the result rather than
// silent.
func (i *Importer) Scan(ctx context.Context, rootID int64) (ScanResult, error) {
	started := i.now()

	roots, err := i.roots.List(ctx)
	if err != nil {
		return ScanResult{}, err
	}
	var root library.RootFolder
	for _, r := range roots {
		if r.ID == rootID {
			root = r
		}
	}
	if root.ID == 0 {
		return ScanResult{}, library.ErrRootNotFound
	}

	vault, err := i.roots.OpenVault(ctx, rootID)
	if err != nil {
		return ScanResult{}, err
	}
	defer func() { _ = vault.Close() }()

	res := ScanResult{RootID: rootID, Root: root.Path}

	// What the database believes is here, so the walk can tell new from known
	// and, afterwards, known-from-absent.
	known, err := i.knownPaths(ctx, rootID)
	if err != nil {
		return res, err
	}
	seen := make(map[string]struct{}, len(known))

	kind := KindMovie
	if root.Kind == library.KindSeries {
		kind = KindSeries
	}

	walkErr := fs.WalkDir(vault.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			// One unreadable directory must not end the scan. A permissions
			// problem on a single folder is a thing to report, not a reason to
			// leave the rest of a library unindexed.
			i.log.Warn("a directory could not be read during a scan",
				slog.String("path", p), slog.String("error", err.Error()))
			return nil
		}
		if res.Scanned >= MaxScanFiles {
			res.Truncated = true
			return fs.SkipAll
		}

		if d.IsDir() {
			if skipDir(p) {
				return fs.SkipDir
			}
			return nil
		}
		// A symlink is neither followed nor recorded. fs.WalkDir over an
		// os.Root does not descend into one (verified, not assumed), and
		// recording it would put a path in the library that the kernel will
		// refuse to open.
		if d.Type()&fs.ModeSymlink != 0 {
			res.Skipped = append(res.Skipped, Rejection{p, 0, "a symbolic link, which is not followed"})
			return nil
		}

		res.Scanned++
		cand := Candidate{Path: p}
		if info, ierr := d.Info(); ierr == nil {
			cand.Bytes = info.Size()
		}

		if !cand.IsVideo() {
			// Subtitles and artwork are not noise, they are simply not items.
			// Only genuinely unexpected files are worth reporting.
			if !cand.IsSubtitle() && !isQuietFile(p) {
				res.Skipped = append(res.Skipped, Rejection{p, cand.Bytes, ReasonNotAContainer})
			}
			return nil
		}
		if hasSampleMarker(p) {
			res.Skipped = append(res.Skipped, Rejection{p, cand.Bytes, ReasonSampleByName})
			return nil
		}
		if cand.Bytes < MinVideoBytes {
			res.Skipped = append(res.Skipped, Rejection{p, cand.Bytes, ReasonTooSmall})
			return nil
		}

		seen[p] = struct{}{}
		if _, already := known[p]; already {
			res.Updated++
			return nil
		}
		if rerr := i.recordFound(ctx, root, kind, p, cand.Bytes); rerr != nil {
			i.log.Warn("a file found by the scan could not be recorded",
				slog.String("path", p), slog.String("error", rerr.Error()))
			res.Skipped = append(res.Skipped, Rejection{p, cand.Bytes, rerr.Error()})
			return nil
		}
		res.Added++
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return res, fmt.Errorf("importer: scanning %s: %w", root.Path, walkErr)
	}

	// What the database has that the disk does not.
	for p := range known {
		if _, found := seen[p]; !found {
			res.Missing = append(res.Missing, p)
		}
	}
	res.Elapsed = i.now().Sub(started)

	// The guard. See MissingRatioGuard: a failed mount makes a root look empty,
	// and a scan that believed it would erase the record of everything on that
	// disk in one pass.
	if len(known) > 0 && float64(len(res.Missing))/float64(len(known)) > MissingRatioGuard {
		return res, fmt.Errorf("%w: %d of %d recorded files are not on disk in %s. "+
			"Nothing has been changed. This is far more often an unmounted disk "+
			"than a deletion — check the mount before treating these as gone",
			ErrLibraryVanished, len(res.Missing), len(known), root.Path)
	}
	return res, nil
}

// knownPaths is what the database believes this root holds.
func (i *Importer) knownPaths(ctx context.Context, rootID int64) (map[string]struct{}, error) {
	files, err := i.store.filesInRoot(ctx, rootID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(files))
	for _, f := range files {
		out[f.RelPath] = struct{}{}
	}
	return out, nil
}

// recordFound adds a file the scan discovered.
//
// The file stays exactly where it is, under exactly the name it has. The
// operator's layout is the one that survives — this software adopts a library,
// it does not reorganise one.
func (i *Importer) recordFound(ctx context.Context, root library.RootFolder,
	kind, relPath string, size int64) error {

	// The FOLDER decides the item, because that is what exists on disk. For a
	// film that is "Blade Runner 2049 (2017)"; for a series it is the show's
	// directory, which is the first component — the season folder below it
	// belongs to the same show.
	folder := firstComponent(relPath)
	if folder == "" || folder == relPath {
		// A file sitting loose at the root of a library. Its own name is all
		// there is to go on, so the item is named from the parse instead.
		folder = ""
	}

	// Two parses, because the folder and the filename answer DIFFERENT
	// questions, and using one for both loses information an existing library
	// already has.
	//
	// The FOLDER says what the item is. In every layout anyone uses, a film's
	// directory is named for the film and a series' directory for the series —
	// so it is the better source for title and year, and it is preferred.
	//
	// The FILENAME says which file this is: its season, its episode, its
	// quality, its group. The folder cannot know those.
	//
	// Measured, not assumed. Before this split, a real library scanned against
	// the binary produced:
	//
	//	the.matrix.1999.720p.brrip/matrix.mkv   -> "matrix", no year
	//	The Expanse (2015)/Season 02/…S02E05…   -> "The Expanse", no year
	//
	// because a thin filename inside an informative folder is extremely common
	// and the folder was only consulted when the filename yielded nothing at
	// all.
	parsed := release.Parse(path.Base(relPath))
	title := strings.TrimSpace(parsed.Title)
	year := parsed.Year

	if folder != "" {
		fromFolder := release.Parse(folder)
		if t := strings.TrimSpace(fromFolder.Title); t != "" {
			title = t
		}
		if fromFolder.Year > 0 {
			year = fromFolder.Year
		}
	}
	if title == "" {
		return fmt.Errorf("%w: nothing in %q or its folder reads as a title",
			ErrUnnameable, path.Base(relPath))
	}
	if folder == "" {
		safe, err := library.SafeComponent(title)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUnnameable, err)
		}
		folder = safe
	}

	item, err := i.store.UpsertItem(ctx, Item{
		Kind: kind, Title: title, Year: year,
		RootFolderID: root.ID, Folder: folder,
	})
	if err != nil {
		return err
	}

	f := File{
		ItemID: item.ID, RootFolderID: root.ID, RelPath: relPath,
		SizeBytes: size, Quality: release.QualityOf(parsed).Name,
		Revision: parsed.Revision, ReleaseTitle: path.Base(relPath),
		ReleaseGroup: parsed.Group,
		// Not hardlinked as far as this software knows: it did not place the
		// file and has no idea whether something else shares the inode. Saying
		// "no" here is the honest answer, and it is the safe one — an operator
		// deleting a file is warned about sharing only when we know of it.
		Hardlinked: false,
	}
	if kind == KindSeries && parsed.Season < 0 && parsed.AirDate != "" {
		// Named by date: the episode the series aired that day (ADR-0064).
		season, number, ok, aerr := i.store.EpisodeAiredOn(ctx, item.ID, parsed.AirDate)
		if aerr != nil {
			return aerr
		}
		if ok {
			parsed.Season, parsed.Episodes = season, []int{number}
		}
	}
	if kind == KindSeries && parsed.Season >= 0 && len(parsed.Episodes) > 0 {
		season := parsed.Season
		episode := parsed.Episodes[0]
		last := parsed.Episodes[len(parsed.Episodes)-1]
		f.Season, f.Episode, f.EpisodeLast = &season, &episode, &last
	}
	_, err = i.store.PutFile(ctx, f)
	return err
}

// skipDir reports whether a directory should not be descended.
func skipDir(p string) bool {
	base := path.Base(p)
	switch base {
	case library.TrashDir:
		// Superseded files. Descending would re-import an operator's own
		// upgrade history as if it were new media.
		return true
	case "@eaDir", ".AppleDouble", "lost+found", ".Trash-1000", "#recycle":
		// NAS and desktop housekeeping. Every one of these contains files that
		// look like media and are not.
		return true
	}
	return strings.HasPrefix(base, ".") && base != "."
}

// quietExtensions are files an existing library is full of and that nobody
// needs told about. Reporting them would bury the one genuinely odd file in a
// scan result thousands of lines long.
var quietExtensions = map[string]struct{}{
	".nfo": {}, ".jpg": {}, ".jpeg": {}, ".png": {}, ".webp": {}, ".tbn": {},
	".txt": {}, ".xml": {}, ".db": {}, ".ds_store": {}, ".sfv": {}, ".md5": {},
	".url": {}, ".torrent": {}, ".part": {}, ".!qb": {},
}

func isQuietFile(p string) bool {
	_, quiet := quietExtensions[strings.ToLower(path.Ext(p))]
	return quiet
}

// firstComponent returns the first path segment, or "" when there is only one.
func firstComponent(p string) string {
	if i := strings.IndexByte(p, '/'); i > 0 {
		return p[:i]
	}
	return ""
}

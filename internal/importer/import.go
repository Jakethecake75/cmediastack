package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/release"
)

// Errors the import path distinguishes.
var (
	ErrNoRootFolder = errors.New("importer: no root folder is configured for this kind of media")
	ErrNotBetter    = errors.New("importer: what is already in the library is as good or better")
	// ErrTargetGone means the download was grabbed for a series or film that
	// is no longer in the library.
	ErrTargetGone = errors.New("importer: what this was grabbed for is no longer in the library")
	// ErrNotTheTarget means the download is not the episode or film it was
	// grabbed for.
	ErrNotTheTarget = errors.New("importer: this is not what it was grabbed for")
)

// RootSource supplies the configured library locations.
type RootSource interface {
	List(ctx context.Context) ([]library.RootFolder, error)
	OpenVault(ctx context.Context, id int64) (*library.Vault, error)
}

// Source is a completed download the importer can read.
type Source struct {
	InfoHash string
	// Dir is the download's directory on the host filesystem.
	Dir string
	// ReleaseTitle is the name the indexer published, which is a better parse
	// subject than the torrent's own declared name. Empty falls back to the
	// directory name.
	ReleaseTitle string
	// Files are what is inside, relative to Dir.
	Files []Candidate
	// Target is what the download was grabbed FOR, when it was grabbed from an
	// episode search (ADR-0023) or a film search (ADR-0026). Nil means the
	// general search, where nothing was named and the item is worked out from
	// the release name as before.
	Target *Target
}

// Target is one episode of one series in the library, or one film.
type Target struct {
	ItemID  int64
	Season  int
	Episode int
	// Film marks a whole film: the item, and no season or episode.
	Film bool
	// Pack marks a whole season: the series and the season, and no episode.
	// Each file is imported as the episode its own name says (ADR-0033).
	Pack bool
	// LastSeason is a pack's last season when it holds several, from Season
	// to LastSeason (ADR-0057); zero for one season.
	LastSeason int
}

// Result is what one import did.
type Result struct {
	Outcome  string
	Detail   string
	Selected string
	Item     Item
	File     File
	// Hardlinked is false when the file had to be copied, which costs twice the
	// disk. Surfaced so a caller can say so rather than leave it to a full-disk
	// alert.
	Hardlinked bool
	Subtitles  int
	// Replaced is true when the file superseded one already in the library.
	Replaced bool
}

// Importer moves a completed download into the library.
type Importer struct {
	store *Store
	roots RootSource
	log   *slog.Logger
	now   func() time.Time
	// requests is optional. Nil means nothing is waiting on imports, which is
	// the correct behaviour for an instance with no request surface wired
	// rather than a reason to fail.
	requests RequestCloser
	// audit records each file that arrives (ADR-0032). Optional, as requests.
	audit Auditor
}

// Auditor is where an arrival is recorded.
type Auditor interface {
	Write(ctx context.Context, e audit.Event) error
}

// SetAuditor wires the audit log. Called once at startup.
func (i *Importer) SetAuditor(a Auditor) { i.audit = a }

// RequestCloser is told when a download became a library item.
//
// Deliberately this narrow. The importer has no business knowing that requests
// exist as a concept, who asked for what, or how approval works — it knows it
// imported an info hash and produced an item, and hands over exactly that. A
// wider interface here would make the import path depend on the shape of a
// feature it does not participate in.
type RequestCloser interface {
	FulfilFromImport(ctx context.Context, infoHash string, mediaItemID int64) (int64, error)
}

// SetRequestCloser wires the request surface. Called once at startup, following
// the same pattern as identity.Service.SetResetDelivery: an optional
// collaborator discovered at wiring time rather than a fifth constructor
// parameter that most callers would pass nil for.
func (i *Importer) SetRequestCloser(rc RequestCloser) { i.requests = rc }

// New builds an importer.
func New(store *Store, roots RootSource, log *slog.Logger, now func() time.Time) *Importer {
	if log == nil {
		log = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &Importer{store: store, roots: roots, log: log, now: now}
}

// Import takes one completed download into the library.
//
// # What it will not do
//
// It does not delete the download, and it does not move it. The file is
// HARDLINKED, so the same bytes carry two names: the library's and the one the
// torrent client is still seeding. A move would break seeding, and on a private
// tracker that is how an account is lost (ADR-0014, ADR-0015). Where a hardlink
// is impossible the file is copied and the result says so.
//
// # Every exit records something
//
// Success, skip and failure all write an import_record. "It downloaded and then
// nothing happened" is the complaint this category of software earns, and an
// operator who can read why a file was skipped does not have to file it.
func (i *Importer) Import(ctx context.Context, src Source) (Result, error) {
	if src.Target != nil && src.Target.Pack {
		// A pack records, audits and closes requests file by file.
		return i.importPack(ctx, src)
	}
	res, err := i.doImport(ctx, src)
	i.afterImport(ctx, src, res, err)
	return res, err
}

// afterImport is what follows every import of one file, whatever came of it:
// its record, its arrival in the audit log, and the requests it closes.
func (i *Importer) afterImport(ctx context.Context, src Source, res Result, err error) {
	// Record before returning, whatever happened. The detail is what an
	// operator reads, so it is written even when the error is returned too.
	rec := Record{
		InfoHash:   src.InfoHash,
		Outcome:    res.Outcome,
		Detail:     res.Detail,
		SourcePath: res.Selected,
	}
	if rec.Outcome == "" {
		rec.Outcome = OutcomeFailed
	}
	if rec.Detail == "" && err != nil {
		rec.Detail = err.Error()
	}
	if res.File.ID > 0 {
		id := res.File.ID
		rec.MediaFileID = &id
	}
	if rerr := i.store.RecordOutcome(ctx, rec); rerr != nil {
		i.log.Error("an import outcome could not be recorded",
			slog.String("info_hash", src.InfoHash), slog.String("error", rerr.Error()))
	}

	if res.Outcome == OutcomeImported && i.audit != nil {
		i.recordArrival(ctx, src, res)
	}

	// Close out anybody waiting on this. Only on a real import: a skip or a
	// failure means the thing they asked for is NOT in the library, and telling
	// them otherwise is worse than telling them nothing.
	//
	// A failure here is logged and swallowed. The file is imported either way,
	// and returning an error would turn "the requester was not notified" into
	// "the import failed", which is a far bigger lie than the one it fixes.
	if i.requests != nil && res.Outcome == OutcomeImported && res.Item.ID > 0 {
		if n, ferr := i.requests.FulfilFromImport(ctx, src.InfoHash, res.Item.ID); ferr != nil {
			i.log.Error("an import could not close its request",
				slog.String("info_hash", src.InfoHash), slog.String("error", ferr.Error()))
		} else if n > 0 {
			i.log.Info("import fulfilled a request",
				slog.String("info_hash", src.InfoHash), slog.Int64("requests", n))
		}
	}
}

func (i *Importer) doImport(ctx context.Context, src Source) (Result, error) {
	// 1. Which file is the media? See select.go — this is the step that keeps a
	//    sample, an .nfo or an executable out of the library.
	sel := Select(src.Files)
	if sel.Video.Path == "" {
		return Result{Outcome: OutcomeSkipped, Detail: sel.Summary()}, nil
	}

	// 2. What is it? The indexer's release title parses better than the
	//    torrent's own declared name, which the uploader chose and which is
	//    often the group's packaging rather than the release.
	subject := src.ReleaseTitle
	if strings.TrimSpace(subject) == "" {
		subject = path.Base(sel.Video.Path)
	}
	parsed := release.Parse(subject)

	// The chosen file's own name decides the container, because that is what
	// the bytes actually are — the release title may name a different one.
	if ext := strings.TrimPrefix(path.Ext(sel.Video.Path), "."); ext != "" {
		parsed.Container = ext
	}

	quality := release.QualityOf(parsed).Name
	// A file named by date, grabbed for an episode, is that episode when it is
	// the day the episode aired (ADR-0064).
	if t := src.Target; t != nil && !t.Film && !t.Pack && parsed.Season < 0 && parsed.AirDate != "" {
		season, number, ok, aerr := i.store.EpisodeAiredOn(ctx, t.ItemID, parsed.AirDate)
		if aerr != nil {
			return Result{Outcome: OutcomeFailed, Detail: aerr.Error(), Selected: sel.Video.Path}, aerr
		}
		if ok {
			// Another day's episode is refused by the target's own check below,
			// which says which one the file is.
			parsed.Season, parsed.Episodes = season, []int{number}
		}
	}
	layout, err := PlanLayout(parsed, quality)
	if err != nil {
		return Result{
			Outcome:  OutcomeSkipped,
			Detail:   err.Error(),
			Selected: sel.Video.Path,
		}, nil
	}

	// 3. Which item, which root? A download grabbed FOR an episode or a film
	//    goes to that series or film — its root, its folder — and nowhere else
	//    (ADR-0023, ADR-0026). Re-deriving the item from the release name
	//    instead is how "Severance.2022.S02E03" came to be filed under a second
	//    Severance in a folder called "Severance (2022)", while the episode the
	//    operator grabbed stayed on the first one's wanted list.
	var target *Item
	var rootID int64
	if src.Target != nil {
		item, placed, terr := i.planForTarget(ctx, *src.Target, layout, parsed, quality)
		if terr != nil {
			return Result{Outcome: OutcomeSkipped, Detail: terr.Error(), Selected: sel.Video.Path}, nil
		}
		target, layout, rootID = &item, placed, item.RootFolderID
	}

	// Otherwise by kind, and only from what the operator configured.
	wantKind := library.KindMovies
	itemKind := KindMovie
	if layout.IsTelevision {
		wantKind, itemKind = library.KindSeries, KindSeries
	}
	if target == nil {
		root, perr := i.pickRoot(ctx, wantKind, sel.Video.Bytes)
		if perr != nil {
			return Result{Outcome: OutcomeFailed, Detail: perr.Error(), Selected: sel.Video.Path}, perr
		}
		rootID = root.ID
	}

	vault, err := i.roots.OpenVault(ctx, rootID)
	if err != nil {
		return Result{Outcome: OutcomeFailed, Detail: err.Error(), Selected: sel.Video.Path}, err
	}
	defer func() { _ = vault.Close() }()

	// The download is opened as a CONTAINED source. Its file paths come from
	// the torrent's file list, which the uploader wrote, so joining one to the
	// download directory unchecked would let "../../../../etc/shadow" be
	// hardlinked into a library this software serves over HTTP. See
	// library.ContainedSource — this exact line was caught by a structural test
	// rather than by review.
	source, err := library.OpenSource(src.Dir)
	if err != nil {
		return Result{Outcome: OutcomeFailed, Detail: err.Error(), Selected: sel.Video.Path}, err
	}
	defer func() { _ = source.Close() }()

	hostSrc, err := source.HostPath(sel.Video.Path)
	if err != nil {
		return Result{
			Outcome:  OutcomeFailed,
			Detail:   "the download names a file outside itself: " + err.Error(),
			Selected: sel.Video.Path,
		}, err
	}

	// 4. Is this better than what is already there?
	var item Item
	if target != nil {
		item = *target
	} else {
		item, err = i.store.UpsertItem(ctx, Item{
			Kind: itemKind, Title: layout.Title, Year: layout.Year,
			RootFolderID: rootID, Folder: layout.Folder,
		})
		if err != nil {
			return Result{Outcome: OutcomeFailed, Detail: err.Error(), Selected: sel.Video.Path}, err
		}
	}

	return i.importFile(ctx, src.InfoHash, vault, source, hostSrc, item, rootID, layout, parsed, quality, subject, sel)
}

// importFile is steps 4 to 8 for one video already known to be what it is and
// where it goes: whether it is better than what is there, superseding the old
// file to the trash, placing it, recording it, and its subtitles. A single
// download and each file of a season pack run exactly this (ADR-0033), so the
// two cannot come to disagree about what an upgrade is.
func (i *Importer) importFile(ctx context.Context, infoHash string, vault *library.Vault,
	source *library.ContainedSource, hostSrc string, item Item, rootID int64, layout Layout,
	parsed release.Parsed, quality, subject string, sel Selection) (Result, error) {

	verdict, existing, detail := i.verdict(ctx, item, layout, parsed, quality, vault)
	if verdict == verdictSkip {
		return Result{
			Outcome: OutcomeSkipped, Detail: detail,
			Selected: sel.Video.Path, Item: item,
		}, nil
	}

	// 5. If this replaces something, move the old file to trash FIRST.
	//
	// A rename, not a delete: the bytes survive, so an operator who disagrees
	// with an upgrade gets their file back. That is what §2 means by
	// "reversible", and it is why the importer can run under a system principal
	// that holds no destroy authority at all.
	// The EXISTING file's path, not the new one. An upgrade to a different
	// quality produces a different filename — "[WEBDL-720p]" becomes
	// "[Bluray-1080p]" — so superseding the destination path finds nothing
	// there and leaves both files in the library. A same-quality PROPER lands
	// on the identical path, where not superseding first fails the link with
	// "file exists" and the upgrade silently never happens. One line has to
	// handle both, and the existing file's own path is the one that does.
	var superseded string
	if verdict == verdictReplace {
		old, serr := vault.Supersede(ctx, existing.RelPath, i.now())
		if serr != nil {
			return Result{Outcome: OutcomeFailed, Detail: serr.Error(), Selected: sel.Video.Path}, serr
		}
		superseded = old
		i.log.Info("an existing file was superseded and moved to trash",
			slog.String("was", existing.RelPath), slog.String("now", old))

		// Forget the old row. Its path now names a file in the trash folder,
		// and a record pointing there would make the library claim to hold a
		// file at a path it no longer occupies — which is worse than having no
		// record, because everything downstream believes it.
		if existing.ID > 0 && existing.RelPath != layout.RelPath {
			if ferr := i.store.ForgetFile(ctx, existing.ID); ferr != nil {
				i.log.Error("the superseded file's record could not be removed",
					slog.String("path", existing.RelPath), slog.String("error", ferr.Error()))
			}
		}
	}

	// 6. Place it. Hardlink first; copy only if the filesystems differ.
	hardlinked := true
	if err := vault.Link(hostSrc, layout.RelPath); err != nil {
		if !isCrossDevice(err) {
			return Result{Outcome: OutcomeFailed, Detail: err.Error(), Selected: sel.Video.Path}, err
		}
		hardlinked = false
		i.log.Warn("hardlinking was not possible, copying instead: this uses twice the disk space",
			slog.Int64("root_folder", rootID), slog.String("file", layout.RelPath))
		if cerr := copyInto(vault, source, sel.Video.Path, layout.RelPath); cerr != nil {
			return Result{Outcome: OutcomeFailed, Detail: cerr.Error(), Selected: sel.Video.Path}, cerr
		}
	}

	// 7. Record it.
	f := File{
		ItemID: item.ID, RootFolderID: rootID, RelPath: layout.RelPath,
		SizeBytes: sel.Video.Bytes, Quality: quality, Revision: parsed.Revision,
		ReleaseTitle: subject, ReleaseGroup: parsed.Group,
		InfoHash: infoHash, Hardlinked: hardlinked,
	}
	if layout.IsTelevision {
		season, episode, last := layout.Season, layout.Episode, layout.EpisodeLast
		f.Season, f.Episode, f.EpisodeLast = &season, &episode, &last
	}
	saved, err := i.store.PutFile(ctx, f)
	if err != nil {
		// The file is on disk but unrecorded, which is the state most likely to
		// confuse an operator later. Said loudly rather than swallowed.
		i.log.Error("a file was placed in the library but could not be recorded",
			slog.String("path", layout.RelPath), slog.String("error", err.Error()))
		return Result{Outcome: OutcomeFailed, Detail: err.Error(), Selected: sel.Video.Path}, err
	}

	// 8. Sidecars, best effort. A subtitle that fails to place is worth a log
	//    line and nothing more: the film imported, and that is the point.
	placed := i.placeSubtitles(source, sel, layout, vault)

	detail = fmt.Sprintf("%s -> %s", sel.Video.Path, layout.RelPath)
	if superseded != "" {
		detail += fmt.Sprintf(" (replaced an earlier file, moved to %s)", superseded)
	}
	if !hardlinked {
		detail += " (copied: the library and the downloads are on different filesystems, so this used twice the disk space)"
	}
	if placed > 0 {
		detail += fmt.Sprintf(" with %d subtitle file(s)", placed)
	}

	return Result{
		Outcome: OutcomeImported, Detail: detail, Selected: sel.Video.Path,
		Item: item, File: saved, Hardlinked: hardlinked, Subtitles: placed,
		Replaced: superseded != "",
	}, nil
}

// planForTarget checks a download against the episode or film it was grabbed
// for, and places it inside that item.
//
// Refusals are deliberate and each has a reason an operator can act on:
//
//   - The item was deleted while the download ran. Importing would re-create
//     it from the release name, undoing the operator's decision behind their
//     back.
//   - The item is not the kind the target says: a film target on a series, or
//     the other way round.
//   - The file is not what was grabbed. A release sealed as S02E03 whose file
//     turns out to be S02E04 is attached to nothing, rather than to the wrong
//     episode — which would then read as "on disk" and never be wanted again.
//     A film's download that turns out to be television is refused the same
//     way.
func (i *Importer) planForTarget(ctx context.Context, t Target, l Layout,
	p release.Parsed, quality string) (Item, Layout, error) {

	item, err := i.store.GetItem(ctx, t.ItemID)
	if errors.Is(err, ErrItemNotFound) {
		return Item{}, Layout{}, fmt.Errorf("%w (item %d); not re-created from the release name",
			ErrTargetGone, t.ItemID)
	}
	if err != nil {
		return Item{}, Layout{}, err
	}
	if t.Film {
		return i.planForFilm(item, p, quality)
	}
	if item.Kind != KindSeries {
		return Item{}, Layout{}, fmt.Errorf("%w: %s is not a series", ErrNotTheTarget, item.Title)
	}

	want := fmt.Sprintf("S%02dE%02d", t.Season, t.Episode)
	if !l.IsTelevision || l.Season != t.Season || t.Episode < l.Episode || t.Episode > l.EpisodeLast {
		got := "not a numbered episode"
		if l.IsTelevision {
			got = episodeCode(p)
		}
		return Item{}, Layout{}, fmt.Errorf("%w: grabbed for %s %s, but the file is %s",
			ErrNotTheTarget, item.Title, want, got)
	}

	placed, err := PlanEpisodeIn(item.Folder, item.Title, item.Year, p, quality, item.SeasonFolders)
	if err != nil {
		return Item{}, Layout{}, err
	}
	return item, placed, nil
}

// planForFilm places a download grabbed for a film inside that film's folder.
//
// Deliberately stricter than the general path about what counts as a film: a
// release carrying ANY episode marker — a season, an episode, an air date, an
// absolute number — is television, whatever PlanLayout would have made of it.
func (i *Importer) planForFilm(item Item, p release.Parsed, quality string) (Item, Layout, error) {
	if item.Kind != KindMovie {
		return Item{}, Layout{}, fmt.Errorf("%w: %s is not a film", ErrNotTheTarget, item.Title)
	}
	if p.IsEpisode() {
		return Item{}, Layout{}, fmt.Errorf("%w: grabbed for the film %s, but the download is "+
			"television (%s)", ErrNotTheTarget, item.Title, televisionOf(p))
	}
	placed, err := PlanFilmIn(item.Folder, item.Title, item.Year, p, quality)
	if err != nil {
		return Item{}, Layout{}, err
	}
	return item, placed, nil
}

// pickRoot chooses where to put something.
//
// Among roots of the right kind, the one with the most free space wins. That is
// a deliberately dull rule: anything cleverer — round-robin, fullest-first,
// keep-a-series-together — needs state or configuration, and the dull rule is
// the one an operator can predict without reading the code.
func (i *Importer) pickRoot(ctx context.Context, kind string, need int64) (library.RootFolder, error) {
	roots, err := i.roots.List(ctx)
	if err != nil {
		return library.RootFolder{}, err
	}
	var usable []library.RootFolder
	for _, r := range roots {
		if r.Kind == kind {
			usable = append(usable, r)
		}
	}
	if len(usable) == 0 {
		return library.RootFolder{}, fmt.Errorf("%w: nothing is configured for %q", ErrNoRootFolder, kind)
	}
	sort.SliceStable(usable, func(a, b int) bool {
		return usable[a].FreeBytes > usable[b].FreeBytes
	})

	// Free space is from the last check and is stale by definition, so it is
	// used to ORDER rather than to refuse. Refusing on a stale number would
	// block an import into a disk that was emptied an hour ago.
	if need > 0 && usable[0].FreeBytes > 0 && usable[0].FreeBytes < need {
		i.log.Warn("the chosen root folder may not have room",
			slog.String("root", usable[0].Path),
			slog.Int64("free_bytes_at_last_check", usable[0].FreeBytes),
			slog.Int64("need_bytes", need))
	}
	return usable[0], nil
}

// verdict says what to do about a file already in the library.
type verdict int

const (
	verdictImport verdict = iota
	verdictReplace
	verdictSkip
)

// verdict decides whether what is already in the library is good enough.
//
// The comparison is on the quality ladder and then the revision, which is what
// an operator means by "better": a PROPER of the same quality replaces the
// original, and a lower quality never replaces a higher one however new it is.
//
// It deliberately does NOT consult a quality profile here. A profile decides
// what is acceptable to GRAB; by the time bytes are on disk that decision has
// been made and re-litigating it would discard a file the operator asked for.
func (i *Importer) verdict(ctx context.Context, item Item, layout Layout,
	parsed release.Parsed, quality string, vault *library.Vault) (verdict, File, string) {

	var existing File
	var err error
	if layout.IsTelevision {
		existing, err = i.store.ExistingEpisode(ctx, item.ID, layout.Season, layout.Episode)
	} else {
		existing, err = i.store.ExistingMovie(ctx, item.ID)
	}
	if errors.Is(err, ErrFileNotFound) {
		return verdictImport, File{}, ""
	}
	if err != nil {
		// Unreadable state: import rather than skip. A duplicate file is a
		// nuisance; a silently missing import is the failure mode this whole
		// package is shaped around.
		i.log.Error("the existing file could not be read; importing anyway",
			slog.String("error", err.Error()))
		return verdictImport, File{}, ""
	}

	// A recorded file whose bytes are gone is not a reason to skip. Libraries
	// lose files to manual tidying, failed disks and restored backups, and
	// refusing to re-import into that gap is how a library stays broken.
	if !vault.Exists(existing.RelPath) {
		i.log.Info("the recorded file is missing from disk; re-importing",
			slog.String("path", existing.RelPath))
		// The row still points at a path with nothing at it. Forgetting it here
		// keeps the library from claiming two files once the new one lands at a
		// different name.
		if existing.ID > 0 && existing.RelPath != layout.RelPath {
			if ferr := i.store.ForgetFile(ctx, existing.ID); ferr != nil {
				i.log.Warn("a stale file record could not be removed",
					slog.String("path", existing.RelPath), slog.String("error", ferr.Error()))
			}
		}
		return verdictImport, File{}, ""
	}

	// Ranked against the DEFAULT ladder, which is a known limitation rather
	// than a claim: the profile that grabbed this release is not recorded on
	// the queue row yet, so a profile with a non-default ordering (one that
	// prefers 1080p WEB-DL to a 2160p remux, which on the i5-6500T is a
	// reasonable preference — ADR-0005) is not consulted here. See
	// release.DefaultRank. Both files stay on disk either way; the cost of
	// being wrong is a skipped upgrade an operator can force by deleting the
	// existing file, not a lost one.
	oldRank := release.DefaultRank(existing.Quality)
	newRank := release.DefaultRank(quality)

	// An upgrade REPLACES rather than simply imports: the destination filename
	// is derived from the quality, so a same-quality PROPER lands on the exact
	// path the old file occupies. Without the supersede step the link fails
	// with "file exists" and the upgrade silently never happens — which is how
	// this was found.
	switch {
	case newRank > oldRank:
		return verdictReplace, existing, ""
	case newRank == oldRank && parsed.Revision > existing.Revision:
		return verdictReplace, existing, ""
	case newRank == oldRank:
		return verdictSkip, existing, fmt.Sprintf("%s is already in the library at the same quality (%s); "+
			"this release is not a PROPER or REPACK of it", layout.Title, existing.Quality)
	default:
		return verdictSkip, existing, fmt.Sprintf("%s is already in the library at a better quality "+
			"(%s, this release is %s)", layout.Title, existing.Quality, quality)
	}
}

// placeSubtitles puts sidecars beside the video. Best effort by design.
func (i *Importer) placeSubtitles(source *library.ContainedSource, sel Selection,
	layout Layout, vault *library.Vault) int {

	placed := 0
	for _, s := range sel.Subtitles {
		dst, err := SubtitlePath(layout.RelPath, path.Base(s.Path))
		if err != nil {
			i.log.Warn("a subtitle could not be named",
				slog.String("subtitle", s.Path), slog.String("error", err.Error()))
			continue
		}
		if vault.Exists(dst) {
			continue
		}
		// Contained exactly as the video was: a subtitle's declared path is
		// written by the same uploader.
		hostSrc, herr := source.HostPath(s.Path)
		if herr != nil {
			i.log.Warn("a subtitle names a file outside the download",
				slog.String("subtitle", s.Path), slog.String("error", herr.Error()))
			continue
		}
		if err := vault.Link(hostSrc, dst); err != nil {
			if cerr := copyInto(vault, source, s.Path, dst); cerr != nil {
				i.log.Warn("a subtitle could not be placed",
					slog.String("subtitle", s.Path), slog.String("error", cerr.Error()))
				continue
			}
		}
		placed++
	}
	return placed
}

// copyInto copies a file of the download into the library, reading it through
// the download's own contained source — never by host path, which could be
// swapped between being checked and being read.
func copyInto(vault *library.Vault, source *library.ContainedSource, rel, dst string) error {
	in, err := source.Open(rel)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	_, err = vault.CopyFrom(in, dst)
	return err
}

// isCrossDevice reports whether an error is EXDEV, the one failure a copy can
// recover from. Every other link failure is a real problem and copying past it
// would turn a permissions bug into silent disk consumption.
func isCrossDevice(err error) bool {
	return errors.Is(err, os.ErrInvalid) ||
		strings.Contains(err.Error(), "cross-device") ||
		strings.Contains(err.Error(), "invalid cross-device link")
}

// recordArrival writes a file's arrival in the library to the audit log: what
// it is, where it went, and what it came from. A failure is logged, not
// returned — the file is in the library either way.
func (i *Importer) recordArrival(ctx context.Context, src Source, res Result) {
	var actor *int64
	label := string(authz.TaskImport)
	if p := authz.FromContext(ctx); p != nil && !authz.IsSystem(p) {
		id := p.UserID
		actor, label = &id, p.Username
	} else if p != nil {
		label = p.Username
	}

	what := res.Item.Title
	if res.Item.Year > 0 {
		what += fmt.Sprintf(" (%d)", res.Item.Year)
	}
	if f := res.File; f.Season != nil && f.Episode != nil {
		what += fmt.Sprintf(" S%02dE%02d", *f.Season, *f.Episode)
		if f.EpisodeLast != nil && *f.EpisodeLast != *f.Episode {
			what += fmt.Sprintf("–E%02d", *f.EpisodeLast)
		}
	}
	if res.File.Quality != "" {
		what += ", " + res.File.Quality
	}
	if res.Replaced {
		what += ", replacing the file it had"
	}

	if err := i.audit.Write(ctx, audit.Event{
		ActorUserID: actor, ActorLabel: label,
		Action:     audit.ActionMediaImported,
		TargetKind: "media_item", TargetID: fmt.Sprintf("%d", res.Item.ID),
		Detail: what,
		After: map[string]any{
			"path": res.File.RelPath, "release": res.File.ReleaseTitle,
			"info_hash": src.InfoHash, "hardlinked": res.Hardlinked,
		},
	}); err != nil {
		i.log.Error("an import could not be written to the audit log",
			slog.String("info_hash", src.InfoHash), slog.String("error", err.Error()))
	}
}

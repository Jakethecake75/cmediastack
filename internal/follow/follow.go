// Package follow adds a title to the library before any of it is on disk: a
// series (ADR-0025) or a film (ADR-0026).
//
// # Why this is a package of its own
//
// An add writes through three stores — the item (internal/importer), its
// identification (internal/identify), and for a series its seasons, episodes
// and monitoring (internal/library) — after reading the title from the
// provider. It holds the one transaction that makes all of that happen
// together or not at all. None of those packages could hold it: importer
// already imports tv and identify, and library is the storage layer, which
// imports nothing above it (library.TestTheStorageLayerImportsNothingAboveIt).
//
// # What an add does not do
//
// It downloads nothing and creates nothing on disk. The folder is a name in the
// database until the first file is imported into it. Finding releases is the
// episode search's job (ADR-0023) or the film search's (ADR-0026), started by
// a person.
package follow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identify"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/tv"
)

// Errors this package distinguishes. Everything else an add can fail with is
// another package's, wrapped: metadata's for the provider, importer's
// ConflictError for a duplicate or an occupied folder, library's for the
// monitoring choice.
var (
	// ErrNotAddable is a request that does not name something that can be
	// added, or that asks for something the kind does not have.
	ErrNotAddable = errors.New("follow: that is not something that can be added")
	// ErrNoRootFolder means no root folder for this kind is configured.
	ErrNoRootFolder = errors.New("follow: no root folder for this kind of media is configured")
	// ErrChooseRootFolder means several could hold it and none was chosen.
	ErrChooseRootFolder = errors.New("follow: more than one root folder could hold it; choose one")
	// ErrWrongRootFolder means the chosen root folder does not exist or holds
	// another kind of media.
	ErrWrongRootFolder = errors.New("follow: that root folder does not hold this kind of media")
)

// RootLister is the slice of the root-folder store an add needs.
type RootLister interface {
	List(ctx context.Context) ([]library.RootFolder, error)
}

// Request is what a person asks to add.
//
// Note what is absent: a title and a year. The item is named by the provider's
// answer for the id, never by the request (ADR-0025, decision 1).
type Request struct {
	// Kind is "series" or "movie".
	Kind string
	// TMDBID is the provider's id for the title, as its search returned it.
	TMDBID int64
	// RootFolderID is where the title will live. Zero means the only root
	// folder for its kind, when there is exactly one.
	RootFolderID int64
	// Folder names the title's folder inside the root. Empty means
	// "Title (Year)" from the provider. A name given here is used exactly as
	// typed or refused.
	Folder string
	// Monitor is all, future, latest or none, for a series; there is no
	// default. A film has no episodes to choose between and must leave it
	// empty: sent anyway, it is refused rather than ignored (ADR-0026).
	Monitor string
}

// Result is what was added. Monitor and the counts are a series'; a film's
// are empty.
type Result struct {
	Item    importer.Item
	Root    library.RootFolder
	Monitor library.Monitoring
	library.MonitoringResult
}

// Service adds titles.
type Service struct {
	db       *db.DB
	items    *importer.Store
	idents   *identify.Store
	episodes *library.EpisodeStore
	roots    RootLister
	provider func() tv.EpisodeProvider
	audit    *audit.Logger
	log      *slog.Logger
}

// NewService builds one.
//
// provider is a function for the reason every provider consumer takes one: the
// credential can be set, changed and removed while the process runs.
func NewService(database *db.DB, items *importer.Store, idents *identify.Store,
	episodes *library.EpisodeStore, roots RootLister, provider func() tv.EpisodeProvider,
	auditLog *audit.Logger, log *slog.Logger) *Service {

	if log == nil {
		log = slog.Default()
	}
	return &Service{db: database, items: items, idents: idents, episodes: episodes,
		roots: roots, provider: provider, audit: auditLog, log: log}
}

// Add puts a series in the library, with every episode the provider lists and
// the monitoring the person chose, or a film — or, if anything cannot be done,
// nothing.
//
// The order is the design (ADR-0025, decision 6): everything that can refuse
// cheaply is checked before the provider is asked anything; everything the
// provider is asked is read before anything is written; and everything written
// is written in one transaction.
func (s *Service) Add(ctx context.Context, req Request) (Result, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return Result{}, err
	}
	actor := authz.FromContext(ctx)

	// --- what can be refused without asking anybody -----------------------
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	var rootKind string
	var monitor library.Monitoring
	switch kind {
	case importer.KindSeries:
		rootKind = library.KindSeries
		m, err := library.ParseMonitoring(req.Monitor)
		if err != nil {
			return Result{}, err
		}
		monitor = m
	case importer.KindMovie:
		rootKind = library.KindMovies
		// Refused, not ignored: a field that means nothing here is a caller's
		// mistake or a misunderstanding, and dropping it hides both.
		if strings.TrimSpace(req.Monitor) != "" {
			return Result{}, fmt.Errorf("%w: a film has no episodes to choose between, so it "+
				"takes no monitoring choice; leave \"monitor\" out", ErrNotAddable)
		}
	default:
		return Result{}, fmt.Errorf("%w: kind must be \"series\" or \"movie\"", ErrNotAddable)
	}
	if req.TMDBID <= 0 {
		return Result{}, fmt.Errorf("%w: a provider id is required", ErrNotAddable)
	}
	folder := req.Folder
	if folder != "" {
		if err := importer.CheckFolderName(folder); err != nil {
			return Result{}, err
		}
	}
	root, err := s.chooseRoot(ctx, req.RootFolderID, rootKind)
	if err != nil {
		return Result{}, err
	}
	// Already here? Asked before the provider, so a second add of the same
	// title costs no requests. AddItem enforces it again, atomically.
	if existing, err := s.items.ItemWithProviderID(ctx, kind, req.TMDBID); err == nil {
		return Result{}, &importer.ConflictError{Err: importer.ErrAlreadyInLibrary, Existing: existing}
	} else if !errors.Is(err, importer.ErrItemNotFound) {
		return Result{}, err
	}

	// --- everything the provider says, before anything is written ---------
	p := s.provider()
	if p == nil {
		return Result{}, metadata.ErrNoProvider
	}
	var details metadata.Details
	var seasons []library.SeasonInput
	if kind == importer.KindSeries {
		details, seasons, err = tv.ReadSeries(ctx, p, req.TMDBID)
	} else {
		// One request: a film is its details.
		details, err = p.Details(ctx, metadata.KindMovie, req.TMDBID)
		if err != nil {
			err = fmt.Errorf("follow: asking about film %d: %w", req.TMDBID, err)
		}
	}
	if err != nil {
		return Result{}, err
	}
	title := strings.TrimSpace(details.Title)
	if title == "" {
		return Result{}, fmt.Errorf("%w: the provider's answer for %d carries no title",
			metadata.ErrUnexpectedShape, req.TMDBID)
	}
	if folder == "" {
		if folder, err = importer.FolderFor(title, details.Year); err != nil {
			return Result{}, err
		}
	}

	// --- all of it, or none of it ------------------------------------------
	var res Result
	err = s.db.InTx(ctx, func(tx db.Execer) error {
		// First, and a write: it takes the write lock before anything in this
		// transaction reads, so the duplicate and folder checks inside it are
		// judged against the latest state.
		item, err := s.items.AddItem(ctx, tx, importer.Item{
			Kind: kind, Title: title, Year: details.Year,
			RootFolderID: root.ID, Folder: folder,
			TMDBID: details.ProviderID, IMDbID: details.IMDbID,
		})
		if err != nil {
			return err
		}
		if err := s.idents.RecordChosen(ctx, tx, item.ID, identify.Candidate{
			Provider: "tmdb", ProviderID: details.ProviderID,
			Title: title, OriginalTitle: details.OriginalTitle, Year: details.Year,
			Overview: details.Overview, PosterPath: details.PosterPath,
		}, actor.UserID); err != nil {
			return err
		}
		res = Result{Item: item, Root: root}
		if kind != importer.KindSeries {
			return nil
		}
		if err := s.episodes.UpsertIn(ctx, tx, item.ID, seasons); err != nil {
			return err
		}
		applied, err := s.episodes.ApplyMonitoring(ctx, tx, item.ID, monitor)
		if err != nil {
			return err
		}
		res.Monitor, res.MonitoringResult = monitor, applied
		return nil
	})
	if err != nil {
		return Result{}, err
	}

	s.record(ctx, actor, res)
	if kind == importer.KindSeries {
		s.log.Info("a series was added",
			slog.String("title", res.Item.Title), slog.Int64("tmdb_id", res.Item.TMDBID),
			slog.String("root", root.Path), slog.String("folder", res.Item.Folder),
			slog.String("monitor", string(monitor)),
			slog.Int("seasons", res.Seasons), slog.Int("episodes", res.Episodes),
			slog.Int("wanted", res.Wanted))
	} else {
		s.log.Info("a film was added",
			slog.String("title", res.Item.Title), slog.Int("year", res.Item.Year),
			slog.Int64("tmdb_id", res.Item.TMDBID),
			slog.String("root", root.Path), slog.String("folder", res.Item.Folder))
	}
	return res, nil
}

// chooseRoot finds where a title of a kind will live — kind being a root
// folder's kind, "series" or "movies".
//
// With one root folder for the kind the choice is obvious and not asked for.
// With several, it is asked for rather than made: the importer's "most free
// space" rule suits one file, but a title lives in its folder for years, and
// which disk that is belongs to the operator.
func (s *Service) chooseRoot(ctx context.Context, id int64, kind string) (library.RootFolder, error) {
	all, err := s.roots.List(ctx)
	if err != nil {
		return library.RootFolder{}, err
	}
	var fitting []library.RootFolder
	for _, r := range all {
		if id != 0 && r.ID == id {
			if r.Kind != kind {
				return library.RootFolder{}, fmt.Errorf("%w: %s holds %s, not %s",
					ErrWrongRootFolder, r.Path, r.Kind, kind)
			}
			return r, nil
		}
		if r.Kind == kind {
			fitting = append(fitting, r)
		}
	}
	switch {
	case id != 0:
		return library.RootFolder{}, fmt.Errorf("%w: there is no root folder %d", ErrWrongRootFolder, id)
	case len(fitting) == 0:
		return library.RootFolder{}, fmt.Errorf("%w: none holds %s", ErrNoRootFolder, kind)
	case len(fitting) > 1:
		return library.RootFolder{}, fmt.Errorf("%w: %d hold %s", ErrChooseRootFolder, len(fitting), kind)
	}
	return fitting[0], nil
}

// record writes the audit line. After the commit, deliberately: an add that
// was rolled back did not happen, and the log should not say it did.
func (s *Service) record(ctx context.Context, actor *authz.Principal, res Result) {
	if s.audit == nil || actor == nil {
		return
	}
	name := res.Item.Title
	if res.Item.Year > 0 {
		name = fmt.Sprintf("%s (%d)", name, res.Item.Year)
	}
	detail := fmt.Sprintf("added the film %s (tmdb %d) in %s, folder %q",
		name, res.Item.TMDBID, res.Root.Path, res.Item.Folder)
	after := map[string]any{
		"kind": res.Item.Kind, "title": res.Item.Title, "tmdb_id": res.Item.TMDBID,
		"root_folder_id": res.Root.ID, "folder": res.Item.Folder,
	}
	if res.Item.Kind == importer.KindSeries {
		detail = fmt.Sprintf("added the series %s (tmdb %d) in %s, folder %q; monitoring %s: "+
			"%d season(s), %d episode(s), %d wanted", name, res.Item.TMDBID, res.Root.Path,
			res.Item.Folder, res.Monitor, res.Seasons, res.Episodes, res.Wanted)
		after["monitor"] = string(res.Monitor)
	}
	_ = s.audit.Write(ctx, audit.Event{
		ActorUserID: &actor.UserID,
		ActorLabel:  actor.Username,
		Action:      audit.ActionMediaAdded,
		Outcome:     audit.OutcomeSuccess,
		TargetKind:  "media_item",
		TargetID:    fmt.Sprintf("%d", res.Item.ID),
		Detail:      detail,
		After:       after,
	})
}

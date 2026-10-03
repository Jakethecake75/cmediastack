package migrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// Matching a Radarr database to this library.
//
// The whole operation is: for each folder this library holds, find the Radarr
// movie that lived in a folder of the same name, and record the ids Radarr
// already knows. Nothing is opened, nothing is moved, and no path from the
// source database reaches a filesystem call — the only thing taken from it is a
// string compared against a column (ADR-0021).

// Action is what would happen, or did, to one item.
type Action string

const (
	// ActionAttach records ids against an item that has none.
	ActionAttach Action = "attach"
	// ActionAlreadyCorrect means this library already holds the same id.
	ActionAlreadyCorrect Action = "already correct"
	// ActionConflict means this library holds a DIFFERENT id.
	//
	// Skipped unless the caller asks for it explicitly. A stored id is
	// something a person or an identification pass already decided, and a file
	// from another application is not automatically a better authority than
	// that decision.
	ActionConflict Action = "conflict"
)

// Match is one library item paired with the Radarr movie of the same folder.
type Match struct {
	ItemID int64
	Folder string
	Action Action

	// What this library holds now.
	CurrentTMDB  int64
	CurrentIMDb  string
	CurrentTitle string
	CurrentYear  int

	// What Radarr says.
	TMDBID int64
	IMDbID string
	Title  string
	Year   int

	// CaseInsensitive marks a match that needed the folder names to be
	// lowercased before they agreed — a Windows Radarr against a Linux library.
	// Reported because it is a weaker match than an exact one and an operator
	// looking at a surprising result deserves to know which kind it was.
	CaseInsensitive bool
}

// TitleChanges reports whether adopting Radarr's label would alter anything.
func (m Match) TitleChanges() bool {
	return m.Title != "" &&
		(!strings.EqualFold(m.Title, m.CurrentTitle) ||
			(m.Year > 0 && m.Year != m.CurrentYear))
}

// Plan is what a migration would do, or what one did.
type Plan struct {
	// SourceVersion is Radarr's own schema version, for the operator's record.
	SourceVersion  int
	MoviesRead     int
	ItemsInLibrary int

	Matches []Match

	// Unmatched are Radarr movies with no folder of that name here. Usually a
	// film the operator has not copied across yet.
	Unmatched []RadarrMovie
	// Unknown are folders here that Radarr has never heard of. The operator's
	// worklist: these still need identifying the ordinary way.
	Unknown []string
	// Ambiguous are folder names held more than once in this library, which
	// cannot be matched to a single movie without guessing.
	Ambiguous []string

	// Applied is false for a dry run.
	Applied bool
	// AdoptedTitles is whether Radarr's labels were taken as well as its ids.
	AdoptedTitles bool
}

// Counts summarises a plan by action.
func (p Plan) Counts() map[Action]int {
	out := map[Action]int{}
	for _, m := range p.Matches {
		out[m.Action]++
	}
	return out
}

// Summary is one line an operator can read without expanding anything.
func (p Plan) Summary() string {
	c := p.Counts()
	verb := "would attach"
	if p.Applied {
		verb = "attached"
	}
	s := fmt.Sprintf("read %d movies from Radarr (schema %d); %s ids to %d of "+
		"%d library items", p.MoviesRead, p.SourceVersion, verb,
		c[ActionAttach], p.ItemsInLibrary)
	if n := c[ActionAlreadyCorrect]; n > 0 {
		s += fmt.Sprintf("; %d already correct", n)
	}
	if n := c[ActionConflict]; n > 0 {
		s += fmt.Sprintf("; %d %s an id already recorded and %s skipped", n,
			plural(n, "disagrees with", "disagree with"), plural(n, "was", "were"))
	}
	if n := len(p.Unknown); n > 0 {
		s += fmt.Sprintf("; %d %s Radarr does not know", n, plural(n, "folder", "folders"))
	}
	if n := len(p.Ambiguous); n > 0 {
		s += fmt.Sprintf("; %d ambiguous %s skipped", n,
			plural(n, "folder name", "folder names"))
	}
	return s
}

// plural picks the right word. Trivial, and worth having rather than not:
// "1 folders Radarr does not know" is the sort of thing that makes an operator
// wonder what else was written carelessly.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Library is the slice of the library this package needs.
//
// Narrow on purpose. AttachIdentity takes ids and no title, so the default
// import cannot relabel anything even if the source database is wrong; Relabel
// is a separate method behind a separate permission, and is reached only when
// the caller explicitly asks to adopt Radarr's titles (ADR-0019, ADR-0021).
type Library interface {
	ListItems(ctx context.Context, kind string) ([]importer.Item, error)
	AttachIdentity(ctx context.Context, itemID, tmdbID int64, imdbID string) error
	Relabel(ctx context.Context, itemID int64, title string, year int) error
}

// Options control one migration.
type Options struct {
	// DryRun reports without writing. The default, and the caller must say
	// otherwise: an operation that touches hundreds of rows and cannot be
	// previewed is one nobody should run.
	DryRun bool
	// AdoptTitles takes Radarr's titles and years as well as its ids. Needs
	// media.edit, because it rewrites what an operator browses to.
	AdoptTitles bool
	// Overwrite applies Radarr's id to items that already carry a different
	// one. Off by default: a stored id is a decision somebody already made.
	Overwrite bool
}

// Service performs migrations.
type Service struct {
	lib   Library
	audit *audit.Logger
	log   *slog.Logger
}

// NewService builds one.
func NewService(lib Library, auditLog *audit.Logger, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{lib: lib, audit: auditLog, log: log}
}

// ErrNoLibrary means there is nothing here to match against.
var ErrNoLibrary = errors.New("migrate: this library holds no movies yet; " +
	"add a root folder and scan it first, then migrate the identities")

// Run plans a migration and, unless this is a dry run, applies it.
//
// Planning and applying are one call rather than two because the plan is built
// from the library as it is now, and a plan handed back to a second call would
// be a stale description of a library that may have changed in between — with
// item ids in it, which is the worst thing to act on stale.
func (svc *Service) Run(ctx context.Context, src *RadarrSource, opt Options) (Plan, error) {
	if err := authz.RequirePermission(ctx, authz.PermSystemSettings); err != nil {
		return Plan{}, err
	}
	if opt.AdoptTitles {
		// Checked HERE as well as inside Relabel. The store's check is the one
		// that enforces it; this one exists so that a caller without the
		// permission is refused before half a library has been rewritten.
		if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
			return Plan{}, err
		}
	}

	movies, err := src.Movies(ctx)
	if err != nil {
		return Plan{}, err
	}
	items, err := svc.lib.ListItems(ctx, "movie")
	if err != nil {
		return Plan{}, err
	}
	if len(items) == 0 {
		return Plan{}, ErrNoLibrary
	}

	plan := buildPlan(movies, items, src.Version(), opt)
	if opt.DryRun {
		return plan, nil
	}
	return svc.apply(ctx, plan, opt)
}

// buildPlan is the matching, with no I/O in it so that every edge can be tested
// without a database.
func buildPlan(movies []RadarrMovie, items []importer.Item, version int, opt Options) Plan {
	plan := Plan{
		SourceVersion:  version,
		MoviesRead:     len(movies),
		ItemsInLibrary: len(items),
		AdoptedTitles:  opt.AdoptTitles,
	}

	// Index the library by folder, and again case-insensitively. A folder name
	// held twice cannot be matched to one movie without guessing, so it is
	// recorded as ambiguous and left alone.
	exact := map[string]*importer.Item{}
	folded := map[string][]*importer.Item{}
	ambiguous := map[string]bool{}
	for i := range items {
		f := items[i].Folder
		if _, dup := exact[f]; dup {
			ambiguous[f] = true
		}
		exact[f] = &items[i]
		k := strings.ToLower(f)
		folded[k] = append(folded[k], &items[i])
	}

	matched := map[int64]bool{}
	for _, mv := range movies {
		item, insensitive := lookup(exact, folded, mv.Folder)
		if item == nil {
			plan.Unmatched = append(plan.Unmatched, mv)
			continue
		}
		if ambiguous[item.Folder] {
			continue
		}
		matched[item.ID] = true

		m := Match{
			ItemID: item.ID, Folder: item.Folder,
			CurrentTMDB: item.TMDBID, CurrentIMDb: item.IMDbID,
			CurrentTitle: item.Title, CurrentYear: item.Year,
			TMDBID: mv.TmdbID, IMDbID: mv.ImdbID,
			Title: mv.Title, Year: mv.Year,
			CaseInsensitive: insensitive,
		}
		switch item.TMDBID {
		case 0:
			m.Action = ActionAttach
		case mv.TmdbID:
			m.Action = ActionAlreadyCorrect
		default:
			m.Action = ActionConflict
		}
		plan.Matches = append(plan.Matches, m)
	}

	for f := range ambiguous {
		plan.Ambiguous = append(plan.Ambiguous, f)
	}
	for i := range items {
		if !matched[items[i].ID] && !ambiguous[items[i].Folder] {
			plan.Unknown = append(plan.Unknown, items[i].Folder)
		}
	}

	sort.Strings(plan.Ambiguous)
	sort.Strings(plan.Unknown)
	sort.Slice(plan.Matches, func(i, j int) bool {
		return plan.Matches[i].Folder < plan.Matches[j].Folder
	})
	return plan
}

// lookup finds the library item for a Radarr folder name.
//
// Exact first, then case-insensitively and only when that is unambiguous. The
// fallback exists because a Radarr on Windows and a library on Linux can
// disagree about capitalisation for the same folder; it is second because when
// the two agree exactly there is nothing to soften.
func lookup(exact map[string]*importer.Item, folded map[string][]*importer.Item,
	folder string) (*importer.Item, bool) {

	if it, ok := exact[folder]; ok {
		return it, false
	}
	if hits := folded[strings.ToLower(folder)]; len(hits) == 1 {
		return hits[0], true
	}
	return nil, false
}

func (svc *Service) apply(ctx context.Context, plan Plan, opt Options) (Plan, error) {
	actor := authz.FromContext(ctx)
	changed, relabelled := 0, 0

	for i := range plan.Matches {
		m := &plan.Matches[i]
		write := m.Action == ActionAttach ||
			(m.Action == ActionConflict && opt.Overwrite)

		if write {
			if err := svc.lib.AttachIdentity(ctx, m.ItemID, m.TMDBID, m.IMDbID); err != nil {
				// One row failing does not abandon the rest: this is a bulk
				// operation over hundreds of items and a partial result that
				// says what it did is far more useful than an abort that says
				// nothing about the 400 rows it had already written.
				svc.log.Warn("could not attach an identity during migration",
					slog.String("folder", m.Folder),
					slog.String("error", err.Error()))
				continue
			}
			changed++
		}

		// Titles only where the id is now right, so a conflict that was skipped
		// does not get relabelled to a film it is not.
		if opt.AdoptTitles && m.TitleChanges() &&
			(write || m.Action == ActionAlreadyCorrect) {
			if err := svc.lib.Relabel(ctx, m.ItemID, m.Title, m.Year); err != nil {
				svc.log.Warn("could not adopt a title during migration",
					slog.String("folder", m.Folder),
					slog.String("error", err.Error()))
				continue
			}
			relabelled++
		}
	}

	plan.Applied = true
	svc.log.Info("migrated identities from Radarr",
		slog.Int("attached", changed),
		slog.Int("relabelled", relabelled),
		slog.Int("movies_read", plan.MoviesRead))

	// One audit record for the operation, not one per row. Six hundred
	// near-identical entries would bury everything else in the log; the detail
	// carries the counts, and the library itself carries the result.
	if svc.audit != nil && actor != nil {
		detail := fmt.Sprintf(
			"Radarr schema %d: %d movies read, %d identities attached, %d titles adopted",
			plan.SourceVersion, plan.MoviesRead, changed, relabelled)
		if opt.Overwrite {
			detail += "; existing ids were overwritten on request"
		}
		_ = svc.audit.Write(ctx, audit.Event{
			ActorUserID: &actor.UserID,
			ActorLabel:  actor.Username,
			Action:      audit.ActionMediaIdentified,
			Outcome:     audit.OutcomeSuccess,
			TargetKind:  "library",
			TargetID:    strconv.Itoa(changed),
			Detail:      detail,
		})
	}
	return plan, nil
}

// ErrNotConfigured means this instance has no migration directory, so there is
// nothing to migrate from.
//
// Distinct from ErrNoSuchSource, which means the directory exists and the named
// file is not in it. They deserve different answers: one is "this instance does
// not offer that", the other is "you have not put the file there yet".
var ErrNotConfigured = errors.New("migrate: this instance has no migration directory")

// Runner is the whole migration surface behind one type: the directory
// operators place files in, and the service that reads them.
//
// It exists so the API depends on one thing rather than on a Sources and a
// Service separately, and so that opening the source — the only part that
// touches the filesystem — has exactly one call site.
type Runner struct {
	sources *Sources
	svc     *Service
}

// NewRunner builds one.
func NewRunner(sources *Sources, svc *Service) *Runner {
	return &Runner{sources: sources, svc: svc}
}

// Sources lists what an operator has placed in the migration directory.
func (r *Runner) Sources() (string, []string, error) {
	if r == nil || r.sources == nil {
		return "", nil, ErrNotConfigured
	}
	files, err := r.sources.List()
	return r.sources.Dir(), files, err
}

// RunRadarr opens the named source and migrates from it.
//
// The permission is checked by the service before anything is read. Opening a
// database on the strength of an unauthorized request — even read-only, even
// refusing afterwards — would let an unauthenticated caller probe which files
// exist and whether each is a valid SQLite database.
func (r *Runner) RunRadarr(ctx context.Context, name string, opt Options) (Plan, error) {
	if r == nil || r.sources == nil || r.svc == nil {
		return Plan{}, ErrNotConfigured
	}
	if err := authz.RequirePermission(ctx, authz.PermSystemSettings); err != nil {
		return Plan{}, err
	}
	path, err := r.sources.Path(name)
	if err != nil {
		return Plan{}, err
	}
	src, err := OpenRadarr(ctx, path)
	if err != nil {
		return Plan{}, err
	}
	defer func() { _ = src.Close() }()
	return r.svc.Run(ctx, src, opt)
}

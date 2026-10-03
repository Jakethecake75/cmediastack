package migrate

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
)

// The fixtures below are built to the schema reconstructed from Radarr's own
// FluentMigrator migrations, cross-checked against its Movie model class. They
// are not written from memory of what Radarr looks like, and the difference
// matters: the first reconstruction was wrong in exactly the way that would
// have broken this importer silently on every modern install (ADR-0021).
//
// What they are NOT is a real Radarr database. Until this has been run against
// one, "derived from Radarr's source" is the strongest claim available here,
// and it is the claim the documentation makes.

// radarrV4 is the shape since migration 207: the identifiers live in
// MovieMetadata, and Movies has no TmdbId, ImdbId, Title or Year AT ALL.
//
// Their absence is the point of the fixture. A query that reaches for
// Movies."TmdbId" — which is what writing this from memory produces — fails
// against this table, as it would against the operator's real file.
const radarrV4 = `
CREATE TABLE "VersionInfo" ("Version" INTEGER NOT NULL, "AppliedOn" DATETIME, "Description" TEXT);
CREATE TABLE "MovieMetadata" (
    "Id" INTEGER PRIMARY KEY AUTOINCREMENT,
    "TmdbId" INTEGER NOT NULL UNIQUE,
    "ImdbId" TEXT,
    "Images" TEXT NOT NULL DEFAULT '[]',
    "Genres" TEXT,
    "Title" TEXT NOT NULL,
    "SortTitle" TEXT,
    "CleanTitle" TEXT,
    "OriginalTitle" TEXT,
    "Status" INTEGER NOT NULL DEFAULT 0,
    "Runtime" INTEGER NOT NULL DEFAULT 0,
    "Year" INTEGER,
    "Overview" TEXT,
    "Popularity" REAL
);
CREATE TABLE "Movies" (
    "Id" INTEGER PRIMARY KEY AUTOINCREMENT,
    "Path" TEXT NOT NULL,
    "Monitored" INTEGER NOT NULL DEFAULT 1,
    "QualityProfileId" INTEGER NOT NULL DEFAULT 1,
    "Added" DATETIME,
    "Tags" TEXT,
    "AddOptions" TEXT,
    "MovieFileId" INTEGER NOT NULL DEFAULT 0,
    "MinimumAvailability" INTEGER NOT NULL DEFAULT 0,
    "MovieMetadataId" INTEGER NOT NULL UNIQUE,
    "LastSearchTime" DATETIME
);
INSERT INTO "VersionInfo" ("Version") VALUES (205), (242);
`

// radarrV3 is the shape BEFORE migration 207, which this refuses.
const radarrV3 = `
CREATE TABLE "VersionInfo" ("Version" INTEGER NOT NULL, "AppliedOn" DATETIME, "Description" TEXT);
CREATE TABLE "Movies" (
    "Id" INTEGER PRIMARY KEY AUTOINCREMENT,
    "ImdbId" TEXT,
    "Title" TEXT NOT NULL,
    "TmdbId" INTEGER NOT NULL,
    "Year" INTEGER,
    "Path" TEXT NOT NULL,
    "Monitored" INTEGER NOT NULL DEFAULT 1,
    "ProfileId" INTEGER
);
INSERT INTO "VersionInfo" ("Version") VALUES (206);
`

type film struct {
	path  string
	tmdb  int64
	imdb  string
	title string
	year  int
}

func newRadarr(t *testing.T, ddl string, films ...film) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "radarr.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), ddl); err != nil {
		t.Fatal(err)
	}

	v4 := strings.Contains(ddl, "MovieMetadata")
	for _, f := range films {
		if v4 {
			res, err := db.ExecContext(t.Context(),
				`INSERT INTO "MovieMetadata" ("TmdbId","ImdbId","Title","Year") VALUES (?,?,?,?)`,
				f.tmdb, f.imdb, f.title, f.year)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := res.LastInsertId()
			if _, err := db.ExecContext(t.Context(),
				`INSERT INTO "Movies" ("Path","MovieMetadataId") VALUES (?,?)`,
				f.path, id); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if _, err := db.ExecContext(t.Context(),
			`INSERT INTO "Movies" ("Path","TmdbId","ImdbId","Title","Year") VALUES (?,?,?,?,?)`,
			f.path, f.tmdb, f.imdb, f.title, f.year); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// fakeLibrary stands in for the store, recording what was asked of it.
type fakeLibrary struct {
	items     []importer.Item
	attached  map[int64][2]any
	relabeled map[int64][2]any
	editPerm  bool
}

func newLibrary(items ...importer.Item) *fakeLibrary {
	return &fakeLibrary{
		items:     items,
		attached:  map[int64][2]any{},
		relabeled: map[int64][2]any{},
		editPerm:  true,
	}
}

func (f *fakeLibrary) ListItems(context.Context, string) ([]importer.Item, error) {
	return f.items, nil
}

func (f *fakeLibrary) AttachIdentity(_ context.Context, id, tmdb int64, imdb string) error {
	f.attached[id] = [2]any{tmdb, imdb}
	return nil
}

func (f *fakeLibrary) Relabel(_ context.Context, id int64, title string, year int) error {
	if !f.editPerm {
		// A plain error, because this fake is standing in for the store and the
		// store's refusal is a *authz.Denial that the service only passes on.
		return errors.New("forbidden")
	}
	f.relabeled[id] = [2]any{title, year}
	return nil
}

func item(id int64, folder, title string, year int, tmdb int64) importer.Item {
	return importer.Item{ID: id, Kind: "movie", Folder: folder, Title: title,
		Year: year, TMDBID: tmdb}
}

func adminCtx() context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 1, Name: "Admin", Rank: 100,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
	})
}

// managerCtx can run the library but cannot change system settings.
func managerCtx() context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 2, Username: "sam", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 2, Name: "Manager", Rank: 50,
			Permissions: authz.NewPermissionSet(
				authz.PermLogin, authz.PermBrowse, authz.PermEditLibraryItems)},
	})
}

// settingsButNotEditCtx may migrate but may not relabel.
func settingsButNotEditCtx() context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 3, Username: "ops", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 3, Name: "Ops", Rank: 90,
			Permissions: authz.NewPermissionSet(
				authz.PermLogin, authz.PermBrowse, authz.PermSystemSettings)},
	})
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func open(t *testing.T, path string) *RadarrSource {
	t.Helper()
	src, err := OpenRadarr(t.Context(), path)
	if err != nil {
		t.Fatalf("opening the fixture: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })
	return src
}

// The identifiers come from MovieMetadata, because since Radarr v4 they are not
// in Movies at all. This is the finding the whole increment turns on.
func TestIdentitiesComeFromMovieMetadataAndNotFromMovies(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{"/movies/Arrival (2016)", 329865, "tt2543164", "Arrival", 2016})
	src := open(t, path)

	if src.Version() != 242 {
		t.Errorf("version = %d, want the HIGHEST row in VersionInfo (242)", src.Version())
	}
	movies, err := src.Movies(context.Background())
	if err != nil {
		t.Fatalf("reading movies: %v — if this is a 'no such column' error, the "+
			"query is reaching for a column Radarr deleted in migration 207", err)
	}
	if len(movies) != 1 {
		t.Fatalf("%d movies, want 1", len(movies))
	}
	got := movies[0]
	if got.TmdbID != 329865 || got.ImdbID != "tt2543164" {
		t.Errorf("ids = %d/%q, want 329865/tt2543164", got.TmdbID, got.ImdbID)
	}
	if got.Folder != "Arrival (2016)" {
		t.Errorf("folder = %q, want only the last segment of the path", got.Folder)
	}
	if got.Title != "Arrival" || got.Year != 2016 {
		t.Errorf("label = %q %d", got.Title, got.Year)
	}
}

// A database old enough to have a different shape is refused, and the refusal
// says what to do about it.
func TestAPreV4DatabaseIsRefusedWithInstructions(t *testing.T) {
	path := newRadarr(t, radarrV3,
		film{"/movies/Arrival (2016)", 329865, "tt2543164", "Arrival", 2016})

	_, err := OpenRadarr(t.Context(), path)
	if err == nil {
		t.Fatal("a pre-v4 database was accepted; its Movies table has a different " +
			"shape and every id read from it would be wrong or missing")
	}
	if !errors.Is(err, ErrTooOld) {
		t.Errorf("err = %v, want ErrTooOld", err)
	}
	// The version it reports and the instruction are what make this actionable.
	if !strings.Contains(err.Error(), "206") {
		t.Errorf("the refusal does not name the version it found: %v", err)
	}
	if !strings.Contains(err.Error(), "Upgrade Radarr") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
}

func TestSomethingThatIsNotRadarrIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "other.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE t (a INTEGER)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	if _, err := OpenRadarr(t.Context(), path); !errors.Is(err, ErrNotRadarr) {
		t.Errorf("err = %v, want ErrNotRadarr", err)
	}
}

// A Windows Radarr and a Linux library describe the same folder differently in
// every way except the one that matters.
func TestAPathFromAnyPlatformYieldsItsLastSegment(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"/movies/Arrival (2016)", "Arrival (2016)"},
		{`D:\Media\Movies\Arrival (2016)`, "Arrival (2016)"},
		{`\\nas\media\Movies\Arrival (2016)`, "Arrival (2016)"},
		{"/movies/Arrival (2016)/", "Arrival (2016)"},
		{`D:\Media\Movies\Arrival (2016)\`, "Arrival (2016)"},
		{"  /movies/Arrival (2016)  ", "Arrival (2016)"},
		{"Arrival (2016)", "Arrival (2016)"},
		{"", ""},
		{"/", ""},
		{`C:\`, "C:"},
	} {
		if got := LastSegment(c.in); got != c.want {
			t.Errorf("LastSegment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAWindowsRadarrMatchesALinuxLibrary(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{`D:\Media\Movies\Arrival (2016)`, 329865, "tt2543164", "Arrival", 2016})
	lib := newLibrary(item(1, "Arrival (2016)", "Arrival", 2016, 0))

	plan, err := NewService(lib, nil, quiet()).Run(adminCtx(), open(t, path),
		Options{DryRun: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Matches) != 1 || plan.Matches[0].Action != ActionAttach {
		t.Fatalf("plan = %+v", plan.Matches)
	}
	if got := lib.attached[1]; got[0] != int64(329865) {
		t.Errorf("attached = %v, want the TMDB id", got)
	}
}

// An id already recorded is a decision somebody made. A file from another
// application does not silently overrule it.
func TestAnIdentityAlreadyRecordedIsNotOverwritten(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{"/movies/Heat (1995)", 949, "tt0113277", "Heat", 1995})
	lib := newLibrary(item(1, "Heat (1995)", "Heat", 1995, 11111))

	plan, err := NewService(lib, nil, quiet()).Run(adminCtx(), open(t, path),
		Options{DryRun: false})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Matches[0].Action != ActionConflict {
		t.Fatalf("action = %q, want a conflict", plan.Matches[0].Action)
	}
	if _, wrote := lib.attached[1]; wrote {
		t.Fatal("a stored identity was overwritten without being asked for")
	}
	if !strings.Contains(plan.Summary(), "skipped") {
		t.Errorf("the summary does not mention the skip: %q", plan.Summary())
	}
}

func TestOverwritingAStoredIdentityIsExplicit(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{"/movies/Heat (1995)", 949, "tt0113277", "Heat", 1995})
	lib := newLibrary(item(1, "Heat (1995)", "Heat", 1995, 11111))

	if _, err := NewService(lib, nil, quiet()).Run(adminCtx(), open(t, path),
		Options{DryRun: false, Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	if got := lib.attached[1]; got[0] != int64(949) {
		t.Errorf("attached = %v, want Radarr's id once overwriting was asked for", got)
	}
}

// Two folders of the same name cannot be matched to one movie without guessing,
// and guessing is how a library gets the wrong identity attached.
func TestADuplicateFolderNameIsAmbiguousAndSkipped(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{"/movies/Heat (1995)", 949, "tt0113277", "Heat", 1995})
	lib := newLibrary(
		item(1, "Heat (1995)", "Heat", 1995, 0),
		item(2, "Heat (1995)", "Heat", 1995, 0), // a second root folder
	)

	plan, err := NewService(lib, nil, quiet()).Run(adminCtx(), open(t, path),
		Options{DryRun: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.attached) != 0 {
		t.Fatalf("an ambiguous folder was matched anyway: %v", lib.attached)
	}
	if len(plan.Ambiguous) != 1 || plan.Ambiguous[0] != "Heat (1995)" {
		t.Errorf("Ambiguous = %v, want the folder reported so an operator can fix it",
			plan.Ambiguous)
	}
	if len(plan.Matches) != 0 {
		t.Errorf("Matches = %+v, want none", plan.Matches)
	}
}

// The unmatched lists are the operator's worklist, and both directions matter.
func TestBothKindsOfMissBecomeAWorklist(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{"/movies/Arrival (2016)", 329865, "tt2543164", "Arrival", 2016},
		film{"/movies/Dune (2021)", 438631, "tt1160419", "Dune", 2021},
	)
	lib := newLibrary(
		item(1, "Arrival (2016)", "Arrival", 2016, 0),
		item(2, "Sintel (2010)", "Sintel", 2010, 0), // Radarr never knew this
	)

	plan, err := NewService(lib, nil, quiet()).Run(adminCtx(), open(t, path),
		Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Unmatched) != 1 || plan.Unmatched[0].Folder != "Dune (2021)" {
		t.Errorf("Unmatched = %+v, want the Radarr film with no folder here", plan.Unmatched)
	}
	if len(plan.Unknown) != 1 || plan.Unknown[0] != "Sintel (2010)" {
		t.Errorf("Unknown = %v, want the folder Radarr does not know", plan.Unknown)
	}
}

func TestADryRunWritesNothing(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{"/movies/Arrival (2016)", 329865, "tt2543164", "Arrival", 2016})
	lib := newLibrary(item(1, "Arrival (2016)", "Arrival", 2016, 0))

	plan, err := NewService(lib, nil, quiet()).Run(adminCtx(), open(t, path),
		Options{DryRun: true, AdoptTitles: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.attached) != 0 || len(lib.relabeled) != 0 {
		t.Fatalf("a dry run wrote: attached=%v relabelled=%v", lib.attached, lib.relabeled)
	}
	if plan.Applied {
		t.Error("a dry run reported itself as applied")
	}
	if plan.Counts()[ActionAttach] != 1 {
		t.Error("a dry run that writes nothing must still say what it would do")
	}
	if !strings.Contains(plan.Summary(), "would attach") {
		t.Errorf("summary = %q, which reads as though it happened", plan.Summary())
	}
}

// Ids yes, labels no. The default import cannot change what an operator browses
// to, even when the source database disagrees with this library.
func TestTitlesAreNotAdoptedByDefault(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{"/movies/Arrival (2016)", 329865, "tt2543164", "Arrival", 2016})
	lib := newLibrary(item(1, "Arrival (2016)", "Arrival 2016 1080p BluRay", 2016, 0))

	if _, err := NewService(lib, nil, quiet()).Run(adminCtx(), open(t, path),
		Options{DryRun: false}); err != nil {
		t.Fatal(err)
	}
	if len(lib.attached) != 1 {
		t.Fatal("the id was not attached")
	}
	if len(lib.relabeled) != 0 {
		t.Fatalf("a plain migration relabelled the library: %v", lib.relabeled)
	}
}

func TestAdoptingTitlesIsOptInAndDoesRelabel(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{"/movies/Arrival (2016)", 329865, "tt2543164", "Arrival", 2016})
	lib := newLibrary(item(1, "Arrival (2016)", "Arrival 2016 1080p BluRay", 2016, 0))

	if _, err := NewService(lib, nil, quiet()).Run(adminCtx(), open(t, path),
		Options{DryRun: false, AdoptTitles: true}); err != nil {
		t.Fatal(err)
	}
	got, ok := lib.relabeled[1]
	if !ok {
		t.Fatal("adopting titles was asked for and nothing was relabelled")
	}
	if got[0] != "Arrival" {
		t.Errorf("relabelled to %v, want Radarr's title", got)
	}
}

// Relabelling is media.edit. A principal that may run a migration but may not
// edit the library is refused BEFORE anything is written, not halfway through.
func TestAdoptingTitlesNeedsThePermissionToEdit(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{"/movies/Arrival (2016)", 329865, "tt2543164", "Arrival", 2016})
	lib := newLibrary(item(1, "Arrival (2016)", "Arrival 2016 1080p", 2016, 0))

	_, err := NewService(lib, nil, quiet()).Run(settingsButNotEditCtx(), open(t, path),
		Options{DryRun: false, AdoptTitles: true})
	if err == nil {
		t.Fatal("a principal without media.edit adopted titles")
	}
	if len(lib.attached) != 0 {
		t.Error("the refusal came after ids had already been written; it must " +
			"come before, or a partial run leaves the library half-migrated")
	}
}

func TestMigratingNeedsTheSystemSettingsPermission(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{"/movies/Arrival (2016)", 329865, "tt2543164", "Arrival", 2016})
	lib := newLibrary(item(1, "Arrival (2016)", "Arrival", 2016, 0))

	if _, err := NewService(lib, nil, quiet()).Run(managerCtx(), open(t, path),
		Options{DryRun: true}); err == nil {
		t.Fatal("a Manager ran a whole-library migration")
	}
}

// A conflict that was skipped must not be relabelled either, or the item would
// keep one film's id and take another film's name.
func TestAConflictIsNotRelabelledWhileAdoptingTitles(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{"/movies/Heat (1995)", 949, "tt0113277", "Heat", 1995})
	lib := newLibrary(item(1, "Heat (1995)", "Heat 1995 REMUX", 1995, 11111))

	if _, err := NewService(lib, nil, quiet()).Run(adminCtx(), open(t, path),
		Options{DryRun: false, AdoptTitles: true}); err != nil {
		t.Fatal(err)
	}
	if len(lib.relabeled) != 0 {
		t.Fatalf("an item whose id was left alone was renamed anyway: %v — it "+
			"would now carry one film's id and another film's name", lib.relabeled)
	}
}

// Capitalisation differs between a Windows Radarr and a Linux library for the
// same folder. Matched, and reported as the weaker match it is.
func TestACaseOnlyDifferenceMatchesAndSaysSo(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{`D:\Media\ARRIVAL (2016)`, 329865, "tt2543164", "Arrival", 2016})
	lib := newLibrary(item(1, "Arrival (2016)", "Arrival", 2016, 0))

	plan, err := NewService(lib, nil, quiet()).Run(adminCtx(), open(t, path),
		Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Matches) != 1 {
		t.Fatalf("matches = %+v", plan.Matches)
	}
	if !plan.Matches[0].CaseInsensitive {
		t.Error("a case-insensitive match was not reported as one")
	}
}

// Two library folders differing only in case cannot be told apart by a
// case-insensitive comparison, so neither is matched.
func TestACaseOnlyCollisionIsNotGuessedAt(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{"/movies/heat (1995)", 949, "tt0113277", "Heat", 1995})
	lib := newLibrary(
		item(1, "Heat (1995)", "Heat", 1995, 0),
		item(2, "HEAT (1995)", "Heat", 1995, 0),
	)

	plan, err := NewService(lib, nil, quiet()).Run(adminCtx(), open(t, path),
		Options{DryRun: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.attached) != 0 {
		t.Fatalf("one of two folders differing only in case was picked: %v", lib.attached)
	}
	if len(plan.Unmatched) != 1 {
		t.Errorf("Unmatched = %+v, want the Radarr film reported as unmatched", plan.Unmatched)
	}
}

// A movie Radarr has never placed on disk has no folder to match.
func TestAMovieWithNoPathIsSkipped(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{"", 329865, "tt2543164", "Arrival", 2016},
		film{"/movies/Dune (2021)", 438631, "tt1160419", "Dune", 2021},
	)
	src := open(t, path)
	movies, err := src.Movies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 1 || movies[0].Folder != "Dune (2021)" {
		t.Errorf("movies = %+v, want only the one with a path", movies)
	}
}

// An empty library is a mistake worth naming: the operator has almost certainly
// skipped the scan, and "0 matched" would not tell them that.
func TestAnEmptyLibrarySaysWhatIsMissing(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{"/movies/Arrival (2016)", 329865, "tt2543164", "Arrival", 2016})

	_, err := NewService(newLibrary(), nil, quiet()).Run(adminCtx(), open(t, path),
		Options{DryRun: true})
	if !errors.Is(err, ErrNoLibrary) {
		t.Fatalf("err = %v, want ErrNoLibrary", err)
	}
	if !strings.Contains(err.Error(), "scan") {
		t.Errorf("the error does not say what to do first: %v", err)
	}
}

// Running it twice changes nothing the second time.
func TestRunningItTwiceIsIdempotent(t *testing.T) {
	path := newRadarr(t, radarrV4,
		film{"/movies/Arrival (2016)", 329865, "tt2543164", "Arrival", 2016})
	lib := newLibrary(item(1, "Arrival (2016)", "Arrival", 2016, 0))
	svc := NewService(lib, nil, quiet())

	if _, err := svc.Run(adminCtx(), open(t, path), Options{DryRun: false}); err != nil {
		t.Fatal(err)
	}
	// The library now holds what the first run attached.
	lib.items[0].TMDBID = 329865
	lib.attached = map[int64][2]any{}

	plan, err := svc.Run(adminCtx(), open(t, path), Options{DryRun: false})
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.attached) != 0 {
		t.Errorf("the second run wrote again: %v", lib.attached)
	}
	if plan.Matches[0].Action != ActionAlreadyCorrect {
		t.Errorf("action = %q, want %q", plan.Matches[0].Action, ActionAlreadyCorrect)
	}
}

// A source is named by filename and nothing else.
//
// The os.Root open is the guarantee; the name check exists so that a refusal
// says what was wrong. Both are tested, because a test that only proves the
// combination works cannot tell which half is carrying it.
func TestASourceCannotNameAFileOutsideTheDirectory(t *testing.T) {
	dir := t.TempDir()
	src, err := OpenSources(filepath.Join(dir, "migrate"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	// Something worth reaching for, one level up and far away.
	secret := filepath.Join(dir, "secret.db")
	if err := os.WriteFile(secret, []byte("the master key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "migrate", "radarr.db"),
		[]byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink planted inside the directory, which the NAME check cannot catch
	// and os.Root must.
	if err := os.Symlink(secret, filepath.Join(dir, "migrate", "sneaky.db")); err != nil {
		t.Skipf("no symlinks on this filesystem: %v", err)
	}

	for _, name := range []string{
		"../secret.db", "/etc/passwd", `..\secret.db`, "sub/radarr.db",
		"", ".", "..", "sneaky.db",
	} {
		if got, err := src.Path(name); err == nil {
			t.Errorf("Path(%q) resolved to %q; a source is a filename in the "+
				"migration directory and nothing else", name, got)
		}
	}

	// The real one still works, or the test above proves only that everything
	// is refused.
	if _, err := src.Path("radarr.db"); err != nil {
		t.Errorf("a file that IS in the directory was refused: %v", err)
	}
}

// A nil Runner is the shape an instance takes when its migration directory
// could not be opened, and `Migrate: migrateRunner` puts a typed nil into an
// interface — so `h.migrate == nil` is FALSE at the handler and these methods
// are what actually run.
//
// That is the house style here (library.Cache.check does the same), and it only
// works if the methods are nil-safe AND say something a caller can turn into
// the right status. ErrNoSuchSource would have become a 404, which tells an
// operator to go and look for a file that was never the problem.
func TestANilRunnerSaysItIsNotConfiguredRatherThanPanicking(t *testing.T) {
	var r *Runner

	if _, _, err := r.Sources(); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Sources() = %v, want ErrNotConfigured", err)
	}
	if _, err := r.RunRadarr(adminCtx(), "radarr.db", Options{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("RunRadarr() = %v, want ErrNotConfigured", err)
	}
	// And a Runner built with nothing in it behaves the same way.
	empty := NewRunner(nil, nil)
	if _, err := empty.RunRadarr(adminCtx(), "radarr.db", Options{}); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("empty RunRadarr() = %v, want ErrNotConfigured", err)
	}
}

// The permission is checked before the file is opened.
//
// Opening a database on the strength of an unauthorized request — even
// read-only, even refusing straight afterwards — makes the endpoint an oracle
// for which files exist and which are valid SQLite databases.
func TestAnUnauthorizedCallerNeverReachesTheFile(t *testing.T) {
	dir := t.TempDir()
	sources, err := OpenSources(filepath.Join(dir, "migrate"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sources.Close() })

	// A real Radarr database, and a file that is not one. Both should be
	// indistinguishable to a caller without the permission.
	real := newRadarr(t, radarrV4,
		film{"/movies/Arrival (2016)", 329865, "tt2543164", "Arrival", 2016})
	body, err := os.ReadFile(real)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "migrate", "radarr.db"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "migrate", "junk.db"),
		[]byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(sources, NewService(newLibrary(
		item(1, "Arrival (2016)", "Arrival", 2016, 0)), nil, quiet()))

	var first string
	for _, name := range []string{"radarr.db", "junk.db", "absent.db"} {
		_, err := r.RunRadarr(managerCtx(), name, Options{DryRun: true})
		if err == nil {
			t.Fatalf("%s: a Manager ran a migration", name)
		}
		if first == "" {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Errorf("the refusal differs by filename (%q vs %q), so an "+
				"unauthorized caller can tell which files exist and which are "+
				"real databases", first, err.Error())
		}
	}
}

// The summary is the line an operator reads, so it is written for one.
func TestTheSummaryCountsInEnglish(t *testing.T) {
	one := Plan{MoviesRead: 3, SourceVersion: 242, ItemsInLibrary: 3,
		Unknown:   []string{"Sintel (2010)"},
		Ambiguous: []string{"Heat (1995)"},
		Matches:   []Match{{Action: ActionConflict}},
	}
	got := one.Summary()
	for _, bad := range []string{"1 folders", "1 folder names", "1 disagree ", "were skipped"} {
		if strings.Contains(got, bad) {
			t.Errorf("summary says %q:\n%s", bad, got)
		}
	}
	for _, want := range []string{"1 folder Radarr", "1 ambiguous folder name", "1 disagrees with"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q:\n%s", want, got)
		}
	}

	many := Plan{MoviesRead: 9, ItemsInLibrary: 9,
		Unknown:   []string{"a", "b"},
		Ambiguous: []string{"c", "d"},
		Matches:   []Match{{Action: ActionConflict}, {Action: ActionConflict}},
	}
	got = many.Summary()
	for _, want := range []string{"2 folders Radarr", "2 ambiguous folder names", "2 disagree with", "were skipped"} {
		if !strings.Contains(got, want) {
			t.Errorf("plural summary is missing %q:\n%s", want, got)
		}
	}
}

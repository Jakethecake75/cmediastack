# Season Packs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A season pack can be grabbed — by a person from a season's search, by automatic acquisition for a settled, wholly wanted season — and imported file by file.

**Architecture:** A third grab target (a season) flows search → ticket → queue → importer exactly as episode and film targets do. The importer gains a pack path that selects every playable file, numbers each by its own name, and runs each through the single-episode import code (factored out). Automatic acquisition treats a qualifying season as its search unit and checks a pack's file list with the importer's own planner before queueing.

**Tech Stack:** Go 1.26 (`CGO_ENABLED=0`), SQLite via modernc, hand-written JS UI (no build step).

**Spec:** [`docs/adr/0033-season-packs.md`](../../adr/0033-season-packs.md)

## Global Constraints

- Linux-only build: verify with `GOOS=linux CGO_ENABLED=0 go vet ./...` on Windows; run tests in WSL (`gotest.sh test ./...` in the session scratchpad).
- Not a git repository: no commits. A task is done when its package's tests pass.
- Every test cited in a document must exist (`docs.TestEveryCitedTestExists`); API-SURFACE.md and DATA-MODEL.md are generated (`go run ./internal/docs/cmd/gendocs`) and must be current.
- Comment and copy style: full sentences that say why; operator-facing reasons in plain words, as the ADR spells them.
- Season 0 is never fetched as a pack by a machine. Settled = every listed episode aired, rows == provider's `episode_count`, last aired > 7 days ago.
- Automatic pack grabs: `.torrent` only (no magnet), file list must hold every wanted episode of the season.

## Review Focus

1. A pack whose episodes sit in a nested folder (`Show.S02/Season 2/Show.S02E01.mkv`) with a `Sample/` folder beside them — every episode imported, samples not. (Task 4, `TestAPackImportsEachFileAsItsOwnEpisode` uses nested paths.)
2. A series never refreshed (no episode rows) receiving a pack — every file skipped as not listed, nothing created. (Task 4, `TestAPackFileMustNameAnEpisodeOfItsSeason`.)
3. A hostile `.torrent` whose file list holds `../` paths reaching the pre-queue planner — the planner reads names only and cannot touch disk. (Task 4, PlanPack test case with a traversal path.)
4. The series deleted while its pack downloaded — skipped with `ErrTargetGone`, nothing re-created. (Task 4, one case in `TestAPackFileMustNameAnEpisodeOfItsSeason`.)
5. A person grabbing a pack of a season they partly have — only missing or better files arrive. (Task 4, `TestAPackNeverReplacesABetterFile`.)

---

### Task 1: Naming several seasons

**Files:** Modify `internal/release/parse.go`; Test `internal/release/parse_test.go`

**Interfaces:** Produces `func NamesSeveralSeasons(name string) bool`.

- [ ] Write `TestSeveralSeasonsAreRecognised`: true for `Show.S01-S03.1080p`, `Show.S01-03.720p`, `Show.Seasons.1-3`, `Show.Season.1-3`, `Show.S01.S02.1080p`, `Show.Complete.Series.1080p`; false for `Show.S02.1080p`, `Show.S02.COMPLETE.1080p`, `Show.S02E01-E03.1080p`, `Show.2019.S02.1080p`.
- [ ] Run, see it fail to compile.
- [ ] Implement with one regex set over the lowercased name: an `s\d+ - s?\d+` range, a `seasons? \d+ - \d+` range, two different `s\d{1,3}` season tokens not followed by `e\d`, or `complete series`.
- [ ] `go test ./internal/release/` passes (including the fuzz seeds).

### Task 2: A season target, its matching and its search

**Files:** Modify `internal/search/target.go`, `internal/search/episode.go`; Create `internal/search/season.go`, `internal/search/season_test.go`

**Interfaces:**
- Produces: `Target.Pack bool` (json `"pk,omitempty"`); `Valid()` accepts a season: `Pack && Season >= 0 && Episode == 0 && !Film`; `Code()` renders `S02` for a pack.
- `const ReasonSeveralSeasons = "several_seasons"`.
- `type SeasonWant struct { ItemID int64; Titles []string; Year, Season int }`
- `func MatchSeasonPack(p release.Parsed, w SeasonWant) *release.Rejection`
- `type SeasonSearch struct { Want SeasonWant; Episodes []int; Term string; Profile *release.Profile; IndexerIDs []int64 }` — `Episodes` are the season's episode numbers a single-episode result may be sealed to.
- `func (s *Service) SearchSeason(ctx context.Context, ss SeasonSearch) (Response, error)` — one `Search` with `Season: n, Episode: 0`; a pack of the season gets `Target{Pack, ItemID, Season}`; a single episode of it whose number is in `Episodes` gets its episode target via `MatchEpisode`; everything else is refused with the first applicable rejection.

- [ ] `TestASeasonPackIsOnlyItsOwnSeason`: `Show.S02.1080p` matches season 2; `Show.S03…` → `ReasonNotThisEpisode`; `Show.S01-S03…` → `ReasonSeveralSeasons`; `Show.S02E01…` → `ReasonNotThisEpisode` (an episode, not a pack); `Other.S02…` → `ReasonNotThisSeries`; year two away → not this series; air-dated → `ReasonNotSeasonNumbered`.
- [ ] `TestASeasonSearchSealsWhatEachResultIs` (fake indexer as in `episode_test.go`): results `Show.S02.1080p`, `Show.S02E03.1080p`, `Show.S02E09.1080p` (not in Episodes), `Show.S01.1080p`; expect Pack target, episode target S02E03, rejection, rejection.
- [ ] Implement; `MatchEpisode`'s season-pack detail now says to use the season's search.
- [ ] `go test ./internal/search/` passes.

### Task 3: The queue carries a season

**Files:** Create `internal/platform/db/migrations/0019_season_target.sql`; Modify `internal/download/store.go`, `internal/download/metainfo.go`; Test `internal/download/store_test.go`, `internal/download/hashof_test.go`, `internal/platform/db/` migration test file

**Interfaces:**
- `download.Target.Pack bool`; `valid()` three shapes; `kindSeason = "season"`; `targetOf` reads it back.
- `func TorrentFiles(torrent []byte) ([]TransferFile, error)` — the file list of a `.torrent`, paths joined with `/`, never touching disk.
- Migration 0019: add `target_kind_v2 TEXT CHECK (… IN ('episode','film','season'))`, copy, drop `target_kind`, rename `target_kind_v2` to `target_kind`.

- [ ] `TestASeasonTargetIsStoredAndReadBack`: Put with `{ItemID, Season: 2, Pack: true}` reads back identically; `{Pack, Episode: 3}` and `{Pack, Film}` refused.
- [ ] `TestTheSeasonTargetMigrationKeepsEveryRow`: migrate to 18, insert an episode-target and a film-target row, migrate to latest, both read back and a `'season'` row inserts.
- [ ] A `TorrentFiles` test on a multi-file torrent built in the test.
- [ ] `go test ./internal/download/ ./internal/platform/db/` passes.

### Task 4: The importer's pack path

**Files:** Create `internal/importer/pack.go`, `internal/importer/pack_test.go`; Modify `internal/importer/import.go`, `internal/importer/store.go`, `internal/importer/select.go`, `cmd/cmediastack/main.go`

**Interfaces:**
- `importer.Target.Pack bool`.
- `func SelectPack(files []Candidate) (videos []Candidate, subs []Candidate, rejected []Rejection)`
- `type PackFile struct { Video Candidate; Parsed release.Parsed; Subtitles []Candidate }`
- `type PackPlan struct { Files []PackFile; Skipped []Rejection }`
- `func PlanPack(files []Candidate, season int, listed map[int]bool) PackPlan` — pure; numbering by base name (`SxxEyy`, bare `E03`/`Ep03`/`Episode 3` given the pack's season); not-listed, other-season, unnumbered and duplicate-episode files go to `Skipped`. `listed == nil` means "do not check against the provider".
- `func (s *Store) SeasonEpisodes(ctx context.Context, itemID int64, season int) (map[int]bool, error)`
- `func (i *Importer) importEpisodeFile(ctx context.Context, …) (Result, error)` — steps 4–8 of today's `doImport`, shared.
- `func (s *Store) ShouldAttemptPack(ctx context.Context, infoHash string) (bool, error)` and `func (s *Store) ImportedFrom(ctx context.Context, infoHash string) (map[string]bool, error)`.
- `Import` on a pack writes one record per file and returns a summary `Result` (Outcome imported if any file imported).
- main.go: `importSource` copies `Pack`; `runImports` uses `ShouldAttemptPack` for a season-target record.

- [ ] `TestAPackImportsEachFileAsItsOwnEpisode`: nested folders, `Sample/`, `.nfo`, files `S02E01`, `E02` → both imported to `Season 02/`, one record each.
- [ ] `TestAPackFileMustNameAnEpisodeOfItsSeason`: `S03E01` skipped (other season), `03 - Title.mkv` skipped (no episode), `S02E14` skipped (not listed), series deleted → `ErrTargetGone`, no rows listed → all skipped; PlanPack given `../../etc/x.mkv` only names it.
- [ ] `TestTwoFilesForOneEpisodeAreBothSkipped`.
- [ ] `TestAPackNeverReplacesABetterFile`: Bluray-1080p already on disk for E01; pack WEBDL-720p imports E02 only.
- [ ] `TestAPacksSubtitlesGoWithTheirOwnEpisode`: `S02E01.srt` beside both videos goes only with E01.
- [ ] `TestAPackIsRetriedOnlyForWhatFailed`: first run with no root for E02's placement failing (vault link failure injected via read-only dir or missing root) → retried, E01 not re-imported; all imported → never again.
- [ ] `go test ./internal/importer/ ./cmd/cmediastack/` passes.

### Task 5: Automatic acquisition takes packs

**Files:** Modify `internal/acquire/acquire.go`, `internal/acquire/store.go`; Create `internal/acquire/season.go`, `internal/acquire/season_test.go`; Modify `cmd/cmediastack/main.go` (wiring)

**Interfaces:**
- `Finder` gains `SearchSeason(ctx, search.SeasonSearch) (search.Response, error)`.
- `Deps.PackCheck func(torrent []byte, season int, want map[int]bool) (ok bool, why string)` — built in main from `download.TorrentFiles` + `importer.PlanPack`. Nil means packs are never grabbed automatically.
- `type seasonKey struct{ ItemID int64; Season int }`; `func (s *Store) SettledSeasons(ctx) (map[seasonKey][]int, error)` — every listed episode number of each settled, non-zero season.
- `pass.packable map[seasonKey]bool` — settled and every listed episode wanted and none in flight.
- `InFlight` expands a `'season'` row to every listed episode of the season while queued/downloading, or complete with `ShouldAttemptPack`'s failed-file condition.
- RunSearch: a due episode whose season is packable runs `SearchSeason` once; a pack is tried first, then single episodes from the same results; state recorded for every episode of the season.
- RunRecent: a feed result with `FullSeason` matched by `MatchSeasonPack` against packable seasons.

- [ ] `TestAPackIsGrabbedForASettledSeasonWhollyWanted` (rig as in `rig_test.go`): settled season 1 of 3 episodes, all wanted; indexer offers `Show.S01.1080p` torrent with 3 episode files → grabbed, queue target season, audited; recent pass the same.
- [ ] `TestAPackIsNotGrabbedUnlessEverythingInItIsWanted`: one episode on disk → single episodes grabbed instead; season airing last week → no pack; season 0 → no pack; one episode unmonitored → no pack.
- [ ] `TestAPackMustBeSeenToHoldTheSeason`: torrent holding 2 of 3 → refused with "holds 2 of the season's 3 episodes", remembered; magnet-only pack → refused.
- [ ] `TestASeasonDownloadHoldsEveryEpisode`: queued season row → all its episodes in flight; complete with every file imported/skipped → none.
- [ ] `go test ./internal/acquire/ ./cmd/cmediastack/` passes.

### Task 6: A person's season search

**Files:** Create `internal/api/season_search.go`, `internal/api/season_search_test.go`; Modify `internal/api/routes.go`, `internal/api/grab.go`, `internal/api/target.go` (names for a season), `internal/web/assets/app/app.js`; regenerate `docs/API-SURFACE.md`, `docs/DATA-MODEL.md`

**Interfaces:**
- Route `POST /api/v1/media/{id}/seasons/{season}/search`, `authz.PermInteractiveSearch`, `h.SearchForSeason`.
- The episodes source gains `ForSeasonSearch(ctx, itemID int64, season int) (SeasonSubject, error)` returning the series title, year, TMDB id and the season's listed episode numbers; `library.ErrNoSuchEpisode`-style not-found → 404.
- Grab copies `Pack` into `download.Target`; `forDownload` names a season `Severance S02`.
- UI: a *Search season* button beside the season's monitor switch, opening the existing targeted-search panel (`targetedSearch('season', …)`).

- [ ] `TestASeasonSearchTicketsOnlyTheSeason`: a pack of the season has a ticket that grabs into a `'season'` queue row; another season's pack has none; a user without `acquisition.search` gets 404; a series with no such season gets 404.
- [ ] Regenerate docs; `go test ./internal/api/ ./internal/docs/ ./internal/web/` passes.

### Task 7: Records and verification

**Files:** Modify `PROGRESS.md`, `docs/ESTIMATES.md`, `docs/adr/0030-automatic-acquisition.md` (limitation now points at 0033), `docs/RUNBOOK.md` (if it describes packs), `SECURITY.md` (the hostile file-list note)

- [ ] Full `go test ./...` in WSL; `GOOS=linux go vet ./...`; golangci-lint and gosec at the pinned versions.
- [ ] Mutation check: for each ADR verification row, break the guarded line and see its test fail.
- [ ] Run the built binary against a stand-in indexer serving a pack torrent and a local seed, if the harness from 4v/4x exists in the tree; otherwise record that the end-to-end run was not done.
- [ ] PROGRESS.md: increment 4y section, State at a glance row, ADR-0033 in Decisions locked; ESTIMATES: season packs done.

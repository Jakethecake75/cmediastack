# ADR-0033: Season packs — one download, a season's episodes, each file judged on its own

**Status:** accepted
**Date:** 2026-09-28
**Related:** [ADR-0023](0023-searching-for-a-wanted-episode.md),
[ADR-0030](0030-automatic-acquisition.md), [ADR-0016](0016-import-pipeline.md),
[ADR-0022](0022-episode-tracking.md), [ADR-0014](0014-download-engine.md)

## The problem

The operator is starting a library from nothing (the scope change of
2026-09-26). A series added today wants every episode ever aired, and older
seasons are very often released only as a *pack*: one torrent holding the
whole season. Nothing here can use one. ADR-0023 refused packs in the
matching ("the importer takes single episodes"), the importer refuses a
release name without an episode number, and ADR-0030 listed them first among
automatic acquisition's limitations: *"they must be found and imported by
hand, and cannot be yet."*

So a new series' back catalogue can arrive only if every episode was also
released on its own, which for many series it never was.

A pack is a different kind of download from an episode in three ways, and the
decisions below are about those:

- **It holds many files, and the uploader named them.** Which file is which
  episode can only be read from each file's own name, and a pack routinely
  holds samples, extras, a second copy, subtitles for every episode in one
  folder.
- **It covers episodes nobody asked about.** A person may already have some of
  the season, or not want some of it.
- **It is large.** A machine that grabs the wrong pack downloads tens of
  gigabytes to import nothing.

## Decisions

### 1. A third kind of target: a season

A grab is sealed to what the server matched it to (ADR-0023). That target gains
a third shape beside *an episode* and *a film*: **a season** — the series and
the season number, with no episode (`search.Target{Pack: true}`,
`download.Target{Pack: true}`, `target_kind = 'season'` in the queue). Each of
the three validates only its own shape: a season with an episode number, or a
film with a season, is refused at sealing, at queueing and at import.

`target_kind`'s CHECK allowed only `'episode'` and `'film'`, and SQLite cannot
change a CHECK in place, so migration 0019 rebuilds that one column: a new
column with the wider CHECK, the values copied, the old one dropped.

### 2. What is a pack of the wanted season

`search.MatchSeasonPack` is `MatchEpisode`'s sibling and runs the same title
and year checks, against the same alternative titles. A release is a pack of
season *n* only when its name:

- names season *n* and **no episode** (`S02`, `Season 2`, `S02.COMPLETE`);
- names **exactly one season**. `S01-S03`, `Seasons 1-3`, `S01.S02` and
  `Complete.Series` are refused as *several seasons*: the importer files one
  season per download, and a multi-season pack would download everything and
  import one season of it;
- names no air date and no absolute number.

A double episode (`S02E01E02`) is an episode, not a pack, and is matched as
before. A Usenet result is refused as it always was.

A pack is judged by the same profile as anything else, from its own name. The
quality it claims is the quality its files are filed with, as for a single
episode (`[WEBDL-1080p]` in every file name), because the pack's name is what
the uploader chose to describe all of it.

### 3. Who may grab a pack

**A person, from a season's search.** `POST /api/v1/media/{id}/seasons/{season}/search`
— `acquisition.search`, like the episode search — asks the indexers for the
season and judges every result against it: a pack of that season gets a
ticket sealed to the season; a single episode of it gets a ticket sealed to
that episode, exactly as the episode search would have issued; everything
else is refused with its reason. A person may grab any pack of the season,
whatever they already have and whatever they monitor: they chose it, and the
import (decision 4) never replaces a better file.

**Automatic acquisition, only for a season that is entirely wanted and
settled** (decision 5).

### 4. Importing a pack: every file, each by its own name

A download whose target is a season is imported file by file.

**Which files.** The same allowlist, sample markers and size floor as a single
download, and the same *under a fifth of the largest is a sample* rule. Not
*largest wins*: every file that survives is a candidate
(`importer.SelectPack`).

**Which episode each file is.** Parsed from the file's **own base name**, never
from the pack's name — the pack says `S02`, and only the file says which
episode it holds:

- `S02E03` (or a double, `S02E03E04`): taken, when it is the pack's season.
  Another season is skipped: *season 3, but this pack was grabbed for season
  2*.
- a bare `E03`, `Ep03` or `Episode 3`: given the pack's season. This is the
  one place a number without a season is accepted, and it is accepted because
  the season is sealed into the grab, not guessed from the name.
- anything else — `03 - Title.mkv`, `- 03 -` — is skipped: *does not say which
  episode it is*. A bare number is how absolute numbering is written, which
  ADR-0023 refuses to match, and a pack is no reason to start.

The episode must be one the provider lists for that season; one it does not is
skipped (*S02E14 is not an episode TMDB lists*). **Two files claiming the same
episode are both skipped**, and the record says which two: choosing one would
be a guess at what the uploader meant.

**Each file is then an episode import**, through the same code a single
episode's import runs (`importFile`, factored out of the old single-file
path so the two cannot drift): whether it is better than what is there (a
pack never replaces an equal or better file), superseding to the trash,
hardlink or copy, the file's record, the arrival in the audit log. A file that
is not better is skipped with the reason, and the rest of the pack continues.

**Subtitles.** A single download's subtitles are those beside its video. In a
pack every episode is beside every other, so that rule would give each episode
every subtitle in the folder. In a pack, a subtitle belongs to a file only
when its name begins with that file's name without its extension, or it sits
in `Subs/<that name>/`. The rest are left in the download.

### 5. Automatic acquisition: a pack only for a season wholly wanted and settled

ADR-0030 grabs a release only when every episode it holds is wanted and none
is downloading. For a pack that is the season, and one more condition:

- **The season is settled.** Every episode the provider lists for it has an
  air date that has passed, the number listed is the number the provider said
  the season has, and the last aired more than a week ago — the same *settled*
  the episode refresh uses. A season still airing is fetched an episode at a
  time, as before: its pack does not exist yet, and one that claims to is a
  fake or a partial.
- **Every episode of it is wanted** — monitored, aired, not on disk — **and
  none is downloading**. Once any episode of a season is in the library, the
  rest of it comes one episode at a time. Nothing a person has, or does not
  want, is downloaded by a machine.
- **Season 0 never.** Specials are not a season anybody releases as one.

**How it is found.** The search pass's unit becomes the season when the season
qualifies: one search for the season — which is one request per indexer, as an
episode search is — judged as a person's season search is. The best eligible
pack is grabbed; with none, the results are searched for the season's single
episodes, and those are grabbed as the episode search would have, within the
pass's limit. The outcome is recorded against every episode of the season, so
the season backs off as one. The recent-release pass matches a pack in the
feed against the seasons that qualify, with the same rule.

**It must be seen to hold the season before it is queued.** A pack that is
really one archive, a sample and an `.nfo`, or half a season, would download
in full and import nothing. The grab already fetches the `.torrent` to work out
its hash; its file list is now read too, and put through the same selection
and numbering the import will use (`importer.PlanPack`). Every wanted episode
of the season must be there, as a playable file, or the pack is refused —
*it holds 6 of the season's 10 episodes* — and remembered as a failure, so the
next pass does not fetch it again to find the same thing out. A **magnet link**
says nothing about what is inside until peers supply it, so automatic
acquisition refuses a pack offered only as a magnet. A person may grab one.

The per-pass limits of ADR-0030 are unchanged, and a pack counts as one grab.

### 6. A season's download holds every episode of the season

For one-download-per-item, a download for a season counts as under way for
**every episode of that season** — queued, downloading, or finished with any
file's import still failing. Once every file has been imported or skipped, it
holds nothing: an episode the pack did not have is wanted again, and the next
pass looks for it on its own. This is the rule a single episode already
follows (a skipped import frees the item), applied per episode.

### 7. Records, and retrying

Each file of a pack gets its own `import_record`, told apart by its
`source_path`. The table always allowed several per download. A single
download is done once anything is imported; a pack is not — nine files
imported and one failed on a full disk would otherwise never retry the tenth.
For a pack:

- while any file's latest outcome is **failed**, the download is offered again
  every pass, and only files with no `imported` record are attempted;
- when none has failed and some were **skipped**, it is offered again after
  the usual skip interval, since the provider may list the episode later;
- when every file was imported, never again.

A request linked to the pack's download is fulfilled by the first episode that
arrives, as a request is by any import (ADR-0028). Every arrival is its own
`media.imported` line in the audit log, so the log says which episodes came;
library notifications are off unless the operator chose them, and the
notifier's bounds hold (ADR-0032).

## Rejected alternatives

**Calling the single-file import once per file.** Records, *already imported*,
retries and request fulfilment are all keyed by the download's info hash, and
N imports under one hash overwrite one another's answer. It would have ended
as this design with worse bookkeeping.

**Filing the pack's folder under the season without reading which file is
which.** The library would not know which episodes arrived, and *wanted is
monitored, aired and absent* (ADR-0022) would stop being true.

**A threshold — grab a pack when most of the season is wanted.** It downloads
what a person already has or chose not to have, and for a two-episode gap it
downloads a season. Decision 5 takes the conservative end; the cost, stated
below, is that a partly-filled season fills one episode at a time.

**Multi-season packs.** One season per download keeps the target, the import,
and "which episodes does this download hold" one question each. Refused, and
listed below.

**Taking a bare number as an episode inside a pack.** `03 - Title.mkv` is
usually episode 3, and sometimes absolute episode 3 of a show numbered
across seasons. The matching refuses absolute numbering everywhere else, and a
file filed under the wrong episode reads as *on disk*, so it is never wanted
again.

**Choosing between two files for one episode** — the larger, or the better
quality. A pack with two copies of an episode is malformed or is two releases
in one folder, and a person can tell which they meant where a rule cannot.

## Known limitations

- *(A person may now grab a multi-season or complete-series pack from a
  season's search: [ADR-0057](0057-multi-season-packs.md). Automatic
  acquisition still never does.)*
- **Multi-season and complete-series packs** are never grabbed, and cannot be
  imported. A series released only as one complete pack cannot be fetched.
- **A season partly in the library** is completed one episode at a time;
  when its episodes were only ever released as a pack, they are found by a
  person's season search, and a person grabs the pack.
- **A season still airing** is never fetched as a pack by the machine.
- **A running series' last season** qualifies only once it is settled — a week
  after its last listed episode aired.
- **A file whose name gives no episode number** — `03 - Title.mkv`, a disc
  structure — is skipped, and its episode stays wanted.
- **The quality in the pack's name is taken for every file in it.** A pack
  that mixes qualities is filed as whatever its name claims.
- **A magnet-only pack** is never grabbed automatically.
- Everything ADR-0030 lists as not matched — daily shows, scene numbering that
  disagrees with TMDB, a title named in its own language — is not matched for
  packs either.

## Verification

| Claim | Test |
|---|---|
| Only a pack of exactly the wanted season matches; several seasons, episodes and other series do not | `search.TestASeasonPackIsOnlyItsOwnSeason`, `release.TestSeveralSeasonsAreRecognised` |
| A season search tickets packs of the season and single episodes of it, sealed to what they are | `search.TestASeasonSearchSealsWhatEachResultIs`, `api.TestASeasonSearchTicketsOnlyTheSeason` |
| A season target has one shape, stored and read back, and the migration keeps every existing target | `download.TestASeasonTargetIsStoredAndReadBack`, `db.TestTheSeasonTargetMigrationKeepsEveryRow` |
| Each file of a pack is imported as the episode its own name says, and nothing else | `importer.TestAPackImportsEachFileAsItsOwnEpisode`, `importer.TestAPackFileMustNameAnEpisodeOfItsSeason` |
| Two files for one episode are both skipped; a pack never replaces a better file | `importer.TestTwoFilesForOneEpisodeAreBothSkipped`, `importer.TestAPackNeverReplacesABetterFile` |
| A subtitle in a pack goes only with its own episode | `importer.TestAPacksSubtitlesGoWithTheirOwnEpisode` |
| A pack is retried for what failed and never re-imports what arrived | `importer.TestAPackIsRetriedOnlyForWhatFailed` |
| A machine grabs a pack only for a settled season wholly wanted, and only one seen to hold it | `acquire.TestAPackIsGrabbedForASettledSeasonWhollyWanted`, `acquire.TestAPackIsNotGrabbedUnlessEverythingInItIsWanted`, `acquire.TestAPackMustBeSeenToHoldTheSeason` |
| A season's download holds every episode of it until its import is done | `acquire.TestASeasonDownloadHoldsEveryEpisode` |

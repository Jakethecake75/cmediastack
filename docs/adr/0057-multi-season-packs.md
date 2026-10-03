# ADR-0057: Packs of several seasons

**Status:** accepted
**Date:** 2026-10-01
**Related:** [ADR-0033](0033-season-packs.md), [ADR-0030](0030-automatic-acquisition.md)

## The problem

ADR-0033 refused every release naming more than one season: `S01-S05`,
`Seasons 1-3`, `S01.S02`, `Complete.Series`. The import filed one season per
download, so such a pack would download everything and import one season.
A series that is only released as one complete pack therefore could not be
fetched at all. That is common for older and finished shows.

## Decisions

### 1. A person grabs one; the machine still does not

A season's search now matches a pack of several seasons **when the season
searched for is among them**. Its ticket is sealed to the whole span. Nowhere
else changes:

- automatic acquisition and the recent-release feed still refuse these packs
  (`MatchSeasonPack` is unchanged);
- the general search still offers no ticket for them.

A pack of five seasons is a large download. Which of a series' seasons are
settled, wanted and missing is a different question for each one. Answering
it for a whole pack, unattended, would mean the machine downloads a hundred
gigabytes to fill one gap. A person who searches a season and picks the
complete series has decided that.

### 2. The span, as the name gives it

- `S01-S05`, `S01-05`, `Seasons 1-5` and `Season.1-5` span seasons 1 to 5.
- `S01.S02` spans the lowest marker to the highest.
- `Complete Series` with no numbers spans season 1 to the **highest regular
  season the provider lists** when the grab is made. That span is sealed, so
  the import does not depend on a later refresh.

Season 0 (specials) is never in a span. The series is matched as for any
pack: the words that say "several seasons" (`Complete Series`,
`Seasons 1-3`) are taken off the title before it is compared.

### 3. The target and the queue

A season target keeps its first season in `Season`, and gains `LastSeason`,
which is zero for one season and greater than `Season` for several.
Migration 0031 adds `download_queue.target_last_season`. `Put` writes it only
for a season target, and the read side refuses one under any other kind. The
database checks that it sits beside a first season and is greater than it. The
CHECK does not name `target_kind`: that column is widened by dropping and
re-adding it (0019, 0027, 0029), which a CHECK naming it would block.

### 4. The import: every file names its season

Each file of a multi-season pack is imported as the season and episode its
own name says (`S02E03`). The rest of the file-by-file plan is ADR-0033's:

- a file naming a season outside the span is skipped as another season;
- a file naming an episode its season does not list is skipped;
- two files claiming one episode are both refused.

A bare `E03` is refused in a pack of several seasons: which season it belongs
to cannot be told. Inside a single season's pack, `E03` is still accepted.

### 5. In flight across its span

While a multi-season pack downloads, or its import is not done, every season
in its span counts as in flight. Automatic acquisition therefore does not
fetch the same episodes again beside it.

## Known limitations

- **Automatic acquisition never grabs one** (decision 1).
- **A file named only `03 - Title.mkv` or `E03` inside several seasons** is
  left in the download.
- **A complete-series pack of a running series** holds the seasons that
  existed when it was made. A later season is wanted as usual.

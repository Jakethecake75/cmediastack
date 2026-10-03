# ADR-0063: Season folders, a choice per series

**Status:** accepted
**Date:** 2026-10-03
**Related:** [ADR-0016](0016-import-pipeline.md), [ADR-0061](0061-following-new-seasons.md)

## The problem

Every episode is filed as `Series (Year)/Season 02/Series (Year) - S02E03.mkv`.
Some people keep a short series, or a miniseries, flat in its folder, and
Sonarr lets each series choose. Here there is no choice: a flat series imported
from elsewhere gains a `Season 01` folder beside its existing files the first
time an episode is fetched for it.

## Decisions

### 1. A switch on the series, on by default

A series has *season folders*, on unless switched off
(`media_item.season_folders`, migration 0035). Off, an episode imported for it
is filed directly in the series' folder, under the same name. Nothing else
about the name changes.

### 2. Only what is imported from now on

Switching it moves no file. The files already there stay where they are, and
the scan reads a series' episodes from their names in either layout, as it
always has. Moving a library's files is a rename feature this software does
not have, and doing it as a side effect of a switch would surprise anyone with
another program pointed at the same folder.

### 3. Set like the series' other switch

`PUT /api/v1/media/{id}/season-folders` with `{"season_folders": false}`
needs `library.edit` and is scoped like every title route. Anything that is
not a visible series answers as one that does not exist. The series' seasons
say whether it is on, and the series page has the switch beside *follow new
seasons*. In the store, the two switches are one setter over a fixed list of
columns.

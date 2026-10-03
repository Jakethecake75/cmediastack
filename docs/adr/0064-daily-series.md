# ADR-0064: Daily series, known by their air dates

**Status:** accepted
**Date:** 2026-10-03
**Related:** [ADR-0023](0023-searching-for-a-wanted-episode.md), [ADR-0063](0063-season-folders.md)

## The problem

A talk show or a news programme is released by date, not number:
`The.Daily.Show.2026.10.02.Guest.Name.1080p.WEB.h264-GRP`. ADR-0023 refused
every such release (*named by air date, which this software does not match*),
so these series could be followed but never fetched. A dated file in the
library was also left beside its series with no episode.

## Decisions

### 1. A release dated the day an episode aired is that episode

The provider gives each episode its air date. A release naming an air date,
and no season, is the episode of the series that aired that day. The series
still has to match, as for any release, and another day is another episode. A
release named by number is matched by number as before. This holds wherever an
episode is matched: a person's search, automatic acquisition and the
recent-release feed.

The same rule applies when files arrive:

- a download grabbed for an episode accepts a dated file of that day;
- the scan files a dated file under the episode its series aired that day,
  and leaves the file without one when the series aired none, or several.

### 2. A daily series is searched by date

Asked for an episode by number, an indexer does not find releases named by
date. A series has a *daily* switch, off by default
(`media_item.daily`, migration 0036), set beside its other switches
(ADR-0063): `PUT /api/v1/media/{id}/daily` with `{"daily": true}`. A daily
series' episodes are asked for by their air date:

- Torznab and Newznab get `season=2026&ep=10/02`, the convention Sonarr, Prowlarr
  and Jackett use;
- a Cardigann tracker gets the keywords with `2026.10.02`, and `.Query.Season`
  and `.Query.Ep` set to match.

An episode with no known air date is asked for by number.

### 3. What is not done

Season packs of a daily series (a year's episodes) are not matched. A daily
series' files are named by season and episode like any other: what the scene
names by date, the library files by number.

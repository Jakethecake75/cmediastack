# ADR-0062: Upgrading a held album to lossless, automatically

**Status:** accepted
**Date:** 2026-10-03
**Related:** [ADR-0036](0036-upgrades-to-the-cutoff.md), [ADR-0045](0045-music-files.md), [ADR-0047](0047-fetching-wanted-albums-automatically.md)

## The problem

Automatic acquisition fetches a wanted album once, in the best format it
finds (ADR-0047). An album held as MP3 stays MP3. The import already replaces
a lossy track with a lossless one and nothing else (ADR-0045, decision 5);
only a person ever looks for one. Films and episodes are upgraded to their
profile's cutoff when the operator turns upgrades on (ADR-0036), but albums
are not.

## Decisions

### 1. The same switch

`acquisition.upgrades` turns album upgrades on as well as film and episode
upgrades. It is off by default, and only with automatic acquisition on. A
second switch for music would let an operator who wants a library upgraded
miss half of it.

### 2. What is upgraded, and to what

An album is upgraded when all of these hold:

- it is monitored;
- it holds at least one track in a lossy format;
- the wanted pass has nothing more to do for it (ADR-0047).

That last condition is the wanted pass's own rule. An album still missing
tracks, with no download imported yet, is fetched by that pass first; once it
has had its one automatic download, its lossy tracks may be upgraded.

There is no music profile and no cutoff to name. The import's own rule is the
cutoff: **lossless over lossy**. A release is an upgrade only when its name
says FLAC or another lossless format. A better lossy release (MP3-320 over
V0) is not an upgrade, because the import would not take it. The rest of
ADR-0047's grab rules still apply: the album, seeders, a link, never a release
that was in the queue.

### 3. Seldom, and after what is wanted

Each album is searched for an upgrade **at most once a week**, counted from
its last search. For an album that has just arrived, that is the search that
fetched it. The upgrade pass runs after the wanted albums, in the same task,
with a budget of its own. Once the lossless release is imported, the album
holds no lossy track and leaves the list. A release missing a track leaves
that track lossy, and the album is looked at again a week later, never for
the same release.

## Known limitations

- **An album is not upgraded within lossy formats,** nor from FLAC to 24-bit
  FLAC, because the import does not replace a file for either.

# ADR-0047: Fetching wanted albums without a person

**Status:** accepted
**Date:** 2026-09-30
**Related:** [ADR-0030](0030-automatic-acquisition.md),
[ADR-0034](0034-stalled-downloads.md), [ADR-0046](0046-searching-for-and-grabbing-an-album.md)

## The problem

A person can search for an album and grab it (ADR-0046). Automatic
acquisition (ADR-0030) fetches wanted episodes and films when the operator turns
it on, and it knows nothing of albums. Its rules are the reason an operator can
leave it running: it acts only on the Wanted list, matches with the code a
person's search runs, grabs a few things a pass, never grabs a release twice,
stops when the tunnel is down, and audits every grab as `system:acquire`. An
album must be fetched under the same rules, not by a second, looser machine.

## Decisions

### 1. The same service, a pass of its own

Albums are searched by `acquire.Service.RunAlbums`, a scheduled task named
`acquire.albums`. It is registered only when `acquisition.automatic` is on,
and runs on the search pass's interval. It holds the same one-pass-at-a-time lock,
the same tunnel gate, the same budget (`searches_per_run`, `max_grabs_per_run`),
the same memory of releases whose fetch failed, the same grab and the same
audit line. It is a pass of its own and not part of the episode search,
because that pass cannot start without a default *video* quality profile. An
instance that only follows music has no reason to choose one.

A want is widened to hold an album: the artist is its item, the album its key,
and `acquire_state` gains `album_id`. SQLite cannot change the table's `CHECK`
in place, so migration 0028 rebuilds the table. The back-off is the episodes':
six hours after a fruitless search, doubling to a week, an hour after a search
no indexer answered.

The indexers' recent-release pass does not look for albums. A music feed is a
different category, and matching every recent release against every wanted
album is the cost the budget exists to bound. Albums are found by searching.

### 2. What is wanted: the Wanted screen's albums whose track list is known

An album is looked for when the Wanted screen lists it: monitored, released,
and a track without a file. **Its track list must also be known.** The import
files tracks by the list (ADR-0045), so a download for an album whose list has
not been read could not be filed. The `music.refresh` task reads the lists of
monitored albums, so such an album is looked for once its list arrives.

### 3. One automatic download an album

An album that already had a download **imported** is not grabbed again
automatically, however many of its tracks are still missing. A MusicBrainz
track list often has a track no release carries, such as a bonus or a
regional extra, and a machine that re-grabbed until every track was held would
fetch the same album forever. The Wanted screen says a download was imported
and tracks are still missing, and a person decides. An album with a download
under way is not searched for, as an episode is not.

### 4. What may be grabbed

A candidate must be **the album**, which is `search.MatchAlbum` through
`SearchAlbum` and the album sealed in its target. Its format must be named, as
the ladder requires (ADR-0046, decision 4). It must have seeders, a link, no
failed fetch in the last six hours, and must never have been in the queue. The
best is taken: the ladder, then seeders. No quality profile is consulted.
Upgrading a lossy album to a lossless one is not done automatically.

### 5. What the operator sees

The summary of each pass says what was searched and grabbed and why not. The
Wanted screen's albums carry the same *automatic* line as episodes and films:
searched when, next when, grabbed what or why nothing, or *downloading*. An
automatic album download is marked in the queue as automatic, so it is given
up if it stalls (ADR-0034).

# ADR-0022: Episodes — what a series HAS, and what it is missing

**Status:** accepted
**Date:** 2026-09-15
**Related:** [ADR-0016](0016-import-pipeline.md),
[ADR-0018](0018-metadata-and-artwork.md), [ADR-0019](0019-identification.md)

## The problem, stated without euphemism

A series in this library today is one row and a pile of files. It knows what it
holds. It has no idea what it is missing, because nothing has ever told it which
episodes exist.

That single absence is most of what separates this from Sonarr. Without it there
is no "wanted" list, no calendar, no season-completion figure, and no way to
search for the episode you do not have — which is the entire point of running
the software.

`0008_media.sql` said so at the time, and set the condition:

> There is no episode table listing episodes the instance does NOT have. That
> table is what drives "wanted", a calendar, and season-completion percentages,
> and it cannot be populated without a metadata provider telling us which
> episodes exist. **Inventing rows from the files we happen to hold would produce
> a season that is always 100% complete, which is worse than having no answer.**

The provider arrived in Phase 3. The condition is met. This is that increment.

## Decisions

### 1. The provider is the authority, and nothing else may create an episode

An episode row exists because TMDB says the episode exists. No code path creates
one from a filename, a release name, or a file on disk.

This is the whole discipline of the thing, and it is worth being blunt about
why. A season built from what is on disk is 100% complete by construction; a
"missing episodes" list built the same way is always empty. Software that
confidently tells an operator they have everything, when what it really means is
"I have a list of the things I have", is worse than software that says nothing —
the first is trusted and wrong, the second sends them to look for themselves.

`TestNoEpisodeIsEverCreatedFromAFile` enforces it structurally, in the same way
`library.TestNothingWritesOutsideAVault` does: a runtime check cannot prove a
code path does not exist.

### 2. Seasons get a table; the earlier note said what would earn one

> There is no season table either, for now. A season is
> `SELECT DISTINCT season FROM media_file WHERE item_id = ?`. It earns a table
> when it has attributes of its own — a poster, an overview, a monitored flag —
> and all three come from metadata.

It now has all three. The deciding one is **monitored**: an episode that has not
aired yet does not exist as a row until a refresh creates it, and it has to
inherit its monitored state from somewhere. Derive season monitoring from its
episodes and a season with no episodes yet has nothing to inherit from, so a
newly announced episode of a series the operator deliberately stopped following
would arrive monitored. The season row is where that intent lives.

### 3. Files are matched to episodes by NUMBER, not by a foreign key

`media_file` already carries `season`, `episode` and `episode_last`. It keeps
them, and no `episode_id` column is added.

A foreign key would be wrong rather than merely redundant: one file can hold
several episodes — a double-length pilot, or `S01E01E02` — and a single FK can
only point at one of them. The range is the truth, so the join is the truth:

```sql
ON  f.season  = e.season_number
AND e.number BETWEEN f.episode AND COALESCE(f.episode_last, f.episode)
```

It also keeps the scan and the importer entirely ignorant of episodes, which
means importing a file for a series that has never been identified still works
and simply shows up as a file with no episode beside it.

### 4. "Wanted" is monitored AND aired AND absent — and absent is the easy part

```
monitored = 1
AND aired_at IS NOT NULL AND aired_at <= now
AND no media_file covers it
```

The `aired_at IS NOT NULL` is load-bearing. TMDB lists announced episodes with
no date, and treating a date it does not have as "already aired" would put every
unannounced episode of every running show on the wanted list on day one. They
are reported separately as *announced*, which is a different fact and a useful
one.

### 5. Specials are stored and not monitored

Season 0 is kept, because operators' files are frequently in it and dropping it
would make those files permanently unmatchable — the same reasoning the provider
already applies. It is **not monitored by default**, because "everything ever
released including recap episodes and convention panels" is not what somebody
means by following a show. An operator can turn it on.

### 6. A refresh is idempotent, and removes what the provider no longer lists

Episodes are upserted on `(item_id, season_number, number)`. An episode the
provider has stopped listing — a renumbering, a split that was merged — is
deleted, because a stale row is a permanent phantom on the wanted list that no
amount of downloading will satisfy.

**A file is never touched by a refresh.** Deleting an episode row deletes the
belief that the episode exists; the file that matched it stays exactly where it
is and simply stops being attributed to anything.

**Amended during implementation.** The first version pruned episodes within a
season and never a *season*, so "a split that was merged" — the case named
above — left the merged-away season and all its episodes in place. A season the
provider no longer lists is now deleted too, its episodes with it by cascade.
An **empty** season list deletes nothing: a series losing every season in one
answer is far likelier to be a provider answering badly than a show that stopped
existing, and taking it literally would erase the operator's monitoring choices
for the whole series in one refresh.

### 7. Refreshing is a whole-library scheduled task, and is bounded

Series end. Refreshing a series that finished in 2013 every night spends a
third-party's rate limit on an answer that has not changed in a decade. The task
therefore refreshes:

- series whose latest episode aired within the last 90 days, or that have any
  episode still to air — the ones where the answer can change, and
- nothing else, unless an operator asks for a specific series.

**Amended during implementation — "nothing else" was wrong.** Series end, and
series are also *renewed*. A show between seasons — for most shows a gap of a
year or more — has no recent episode and nothing still to air, so under the rule
above it was never asked about again and its next season would never have
appeared. That is the one thing an operator following a show needs this software
to notice. Checked against the live instance rather than argued: Severance's
latest episode aired on 2025-03-20 and its third season is announced and empty,
and the original selection returned nothing for it.

The rule is now (`tv.Policy`, `library.SeriesNeedingRefresh`):

- asked **every run** (every 12 hours): never asked; an episode still to air or
  aired within 90 days; or a season with **no episodes in it** — an announced
  season, which is the one about to gain some, or a refresh that failed part-way;
- asked **weekly**: everything else;
- at most 200 series a run, least recently asked first, which keeps up with a
  thousand series of which a hundred are running.

A finished series whose seasons have not changed costs one request a week: the
season list is compared and every season is skipped.

And because renewals are now noticed, a new season needed a default that
respects an operator who stopped following the show. There is no series-level
switch; switching off **every regular season** is how that intent is expressed,
so a new season of such a series arrives unmonitored (decision 2's reasoning,
one level up). Switching off an old season, or leaving specials at their default,
is not stopping.

## Rejected alternatives

**Derive episodes from files.** Covered above: a library that is always 100%
complete. It is the single thing this ADR exists to refuse.

**Store one row per season with a JSON blob of episodes.** Fewer rows, and it
makes "which episodes are missing across the whole library" a full-table scan
with JSON parsing in the middle. Wanted is the query this exists to serve.

**An `episode_id` on `media_file`.** Cannot express a file covering a range, and
forces the importer to know about episodes it has no business knowing about.

**Fetching every season on every refresh.** TMDB is one HTTP call per season. A
twelve-season show is twelve calls, and most seasons never change again. The
refresh fetches a season when the provider's episode count for it differs from
what is stored, or when it contains an episode that has aired since the last
refresh, and otherwise leaves it alone.

## Known limitations

**Absolute numbering is not handled.** Anime released as `E087` with no season
is a real and common shape, and mapping it onto season/episode needs an absolute
number from the provider plus a scene-numbering table. Not built; such files
import as files with no episode attribution, which is the honest outcome.

**An episode's air date is the provider's, not a broadcaster's.** For a series
released all at once, every episode carries the same date and the whole season
becomes wanted simultaneously, which is correct. For a weekly show in another
timezone the date can be a day out, which means an episode can appear wanted a
few hours before it exists anywhere.

**No per-episode search yet.** This increment produces the list of what is
missing. Acting on it — sending each wanted episode to the indexer search that
already exists — is the next one.

## Addendum — what building it found

Eight defects. The first was found by a test written to check the ADR's own
claims, the second by the compiler. **Three to seven were found by reading the
code while writing the increment up, in code whose tests all passed** — one of
those tests asserted the wrong behaviour outright. The eighth only became
reachable once the seventh was fixed. Every fix now has a test that fails
against the version before it (mutation-verified).

1. **New episodes did not inherit their season's monitoring.** Decision 2 is the
   reason seasons have a table, and the first `Upsert` wrote the flag and never
   read it back: new episodes took a default from the season *number*, so a
   season the operator had switched off gained a monitored episode the day the
   provider announced one. The ADR, the migration and the code's own comments
   all described the inheritance; none implemented it. A test did.
2. **The storage package imported the provider package.** The refresher lived
   in `internal/library`, inverting the layering; it surfaced as an import cycle
   in the metadata tests. It moved to `internal/tv`, and
   `library.TestTheStorageLayerImportsNothingAboveIt` keeps it there.
3. **A failed season was retried never.** A season whose fetch failed recorded
   the provider's *new* episode count without the episodes, so the next refresh
   saw the counts agree and skipped it. An episode added during a 502 was lost
   for good — while the comment above the code promised the next run would "try
   this season again because its stored count will still disagree". The count
   it was relying on was the one it had just overwritten. A season not read now
   keeps the count it had.
4. **A failed season was reported as unchanged.** Failures were counted as
   skipped, and the summary called a season whose request had just failed one
   that "could not have changed". `SeasonsFailed` is its own figure, in the
   summary, the API (`seasons_failed`) and the task log, and the UI shows a
   partial refresh as an error rather than a green notice.
5. **A rate limit did not stop the refresh.** The refusal was logged and the
   next season asked, and the next, into the same limit; the refresh then
   reported success, so `RefreshAll` went on to the next series. A limit now
   ends the refresh, what was read is still recorded, and the error wraps
   `metadata.ErrRateLimited`. The API's 503 says which case it was — nothing
   read, or what was recorded before the limit — instead of one promise that was
   false in the only case that reached it.
6. **Seasons were never pruned.** Decision 6, amended above.
7. **Renewals were never noticed.** Decision 7, amended above.
8. **A renewed series the operator had dropped would have come back.** Only
   reachable once 7 was fixed; handled in the same change.

Also found while verifying, not in this increment's code: an invite-list test
that would have started failing on a fixed date, because `ListInvites` read the
wall clock while the service it sat beside used an injected one.

### Verified against the live provider

Severance (TMDB 95396), from a library holding five of its files, one of them
the double-length `S02E01-E02`:

```
S00  off  known=1  count=1   -
S01  mon  known=9  count=9   ##..#....
S02  mon  known=10 count=10  ##........
S03  mon  known=0  count=0   (announced)
```

The double-episode file counts for both episodes it holds; the specials season
is kept and unmonitored; season 3 is reported as announced rather than as
"0 of 0". Fourteen episodes wanted; switching season 1 off takes it to eight. A
second refresh fetches season 3 alone and skips the three that cannot have
changed. Episode counts agree with the provider's own per-season figures, and a
season that does not exist is an error, not an empty list — which matters,
because an empty list is read as "this season has no episodes" and deletes its
rows.

### Still not handled

- ~~A provider **404** surfaces as "the provider's response was not the
  expected shape"~~ — fixed in increment 4n, which needed the difference to tell
  an operator "no such series" from "the provider is broken": a 404 is now
  `metadata.ErrNotFound`, and the credential screen treats it as a bad address
  rather than a bad key ([ADR-0025](0025-adding-a-series-before-it-is-on-disk.md)).
- Absolute numbering, air dates and per-episode search: see Known limitations.


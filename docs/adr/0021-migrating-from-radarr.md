# ADR-0021: Migrating from Radarr — import the identities, not the files

**Status:** accepted
**Date:** 2026-09-14
**Supersedes:** nothing
**Related:** [ADR-0015](0015-library-path-containment.md),
[ADR-0016](0016-import-pipeline.md), [ADR-0019](0019-identification.md)

## The problem, stated without euphemism

§13 says there is an existing *arr stack with roughly 900 films in it. Adopting
this software without a migration path means re-adding 900 films by hand, which
nobody will do, which means the software is not adopted.

The obvious reading of "migration importer" is a second import pipeline that
reads Radarr's database and creates library rows from it. That reading is wrong,
and most of this ADR is about why.

## What is actually expensive

The files are already on disk, in a layout `Importer.Scan` already understands.
Scanning 900 folders costs a directory walk and produces 900 library items with
titles and years taken from the release names. That part is built and needs
nothing.

What Scan cannot produce is an **identity**. Working out that a folder called
`Arrival (2016)` is TMDB 329865 requires a provider lookup per film, and the
ambiguous ones — remakes, translated titles, films sharing a name, anything the
parser read badly — require a person to look at two candidates and choose
([ADR-0019](0019-identification.md)). At 900 films that is the whole cost of
adoption: ~900 provider calls against a rate limit, and an unknown number of
human decisions.

**Radarr already knows every one of those answers.** The operator, or Radarr on
their behalf, already made those decisions over several years.

So the migration is not a file importer. It is an **identity source**:

```
1. operator adds their existing movie folder as a root folder   (built)
2. Scan walks it and creates library items                      (built)
3. migrate radarr matches those items to Radarr's movies and
   attaches the TMDB and IMDb ids Radarr already holds          (this ADR)
```

Step 3 is a `UPDATE media_item SET tmdb_id = ?, imdb_id = ?` per matched row.
That is the entire write surface.

## Decisions

### 1. Items are matched by folder NAME, never by path

Radarr stores `Movies.Path` as an absolute path on the machine Radarr ran on:
`/movies/Arrival (2016)` in a container, `D:\Media\Movies\Arrival (2016)` on
Windows. None of those paths mean anything here.

`media_item.folder` is deliberately "the folder name inside the root, NOT an
absolute path" (`0008_media.sql`), so the last segment of Radarr's path is the
one part of it that is portable. Matching on that segment means:

- no prefix mapping for the operator to get wrong,
- no absolute path from a foreign machine ever entering this system,
- and the same file working whether Radarr saw it through a bind mount, an SMB
  share, or a drive letter.

Separators are normalised both ways, because a Windows Radarr writes `\` and a
Linux one writes `/`, and an operator moving from one to the other is the
common case rather than the odd one.

Nothing from the Radarr database is ever opened, joined to a path, or passed to
a filesystem call. The only thing taken from it is a string compared against a
column, plus two integers and a title.

### 2. It attaches identities and, by default, nothing else

`Store.AttachIdentity` takes ids and no title, on purpose
([ADR-0019](0019-identification.md)): a caller holding it cannot relabel
anything. This importer holds exactly that, so the default import cannot change
what an operator browses to even if Radarr's data is wrong.

Adopting Radarr's **titles** is a separate, opt-in flag that requires
`media.edit` and goes through `Relabel`. It is offered because Radarr's titles
are TMDB's canonical titles — which is what identification would have set
anyway — and because hand-reviewing 900 titles is not a real option. It is
opt-in because bulk-renaming somebody's library from a file is the destructive
half, and the permission split already exists to say so.

### 3. Dry run is the default, and the report is the product

`dry_run: true` unless the caller says otherwise. The report says how many
Radarr movies were read, how many matched an item, how many already carried the
same id, how many would change, and — listed individually — which folders did
not match anything. That last list is the useful output even on a successful
run: it is the operator's worklist.

An operation that touches 900 rows and cannot be previewed is one nobody should
run.

### 4. The source database is read-only, and its shape is detected, not assumed

Opened `mode=ro`. This software has no business writing to another
application's database, and the operator may well have copied it out of a
running instance.

**The schema is detected by reading the columns that exist**, not by trusting a
version number. Radarr's own `VersionInfo` table is read and reported, because
it makes a refusal message actionable, but behaviour is driven by
`PRAGMA table_info`.

That distinction is not pedantry. Radarr migration 207 (v4.0, 2022) moved
`TmdbId`, `ImdbId`, `Title` and `Year` **out of `Movies`** into a new
`MovieMetadata` table and deleted the original columns. An importer written from
memory of Radarr's schema reads `Movies.TmdbId` and gets nothing at all on every
modern install — silently, because the column simply is not there and a query
that names it fails in a way easy to mistake for "no movies".

This was not reasoned about. Radarr's migrations were read and the final column
set reconstructed from them mechanically; the first reconstruction was **wrong
in exactly that way**, because the chained `Delete.Column("a").Column("b")
.FromTable("Movies")` form spans lines and the pattern used did not match it.
The error was caught by checking the result against Radarr's `Movie` model
class, where `Title`, `TmdbId`, `ImdbId` and `Year` are visibly
read-through properties over `MovieMetadata` rather than fields. Two independent
derivations from the same source, disagreeing, is what found it.

### 5. Databases older than migration 207 are refused, with instructions

Supporting every historical Radarr schema is unbounded work for an operation
run once. A pre-207 database is refused with a message naming its version and
saying to upgrade Radarr first — which Radarr does for itself, reliably, and is
a far better migrator of its own data than this would be.

### 6. It is an administrative operation, audited, and idempotent

`system.settings`, like the other whole-library triggers. Every applied change
is audited. Re-running is safe: a row already carrying the id is counted as
`already correct` and not written.

## Rejected alternatives

**Import Radarr's movies as library rows.** A second pipeline that creates items
from a foreign database, with its own path handling, its own quality mapping and
its own duplicate rules — parallel to the one that already works, and reachable
only by operators who happen to have a Radarr. The scan already produces the
rows. Doubling the write paths into the library to save a directory walk is a
bad trade.

**Map path prefixes.** The obvious design: ask the operator for
`/movies → root folder 1`. It is more configuration, it is the step operators
get wrong, and it buys nothing that matching the last segment does not — while
introducing a foreign absolute path into a system that has spent
[ADR-0015](0015-library-path-containment.md) keeping them out.

**Accept the database as an HTTP upload.** Friendlier, and it makes an arbitrary
file from a request reach a database engine. The file is placed in a directory
beside the application's own database instead, and named in the request by
basename only, resolved through `os.Root`. An operator who can deploy this can
copy a file.

**Match on TMDB id already present.** Circular: the ids are what is missing.

## Known limitations

**Folders Radarr renamed after Radarr last wrote them will not match.** If the
operator renamed a folder on disk without telling Radarr, the two disagree and
the film lands on the unmatched list. That is the correct outcome — guessing
between similar folder names is how a library gets the wrong identity attached —
but it means the unmatched list needs reading rather than dismissing.

**Two folders with the same name under different roots** both match the same
Radarr movie. They are reported as ambiguous and skipped rather than picked
between.

**Series are not covered.** Sonarr's schema is a different shape, with episodes
and seasons, and episode-level tracking does not exist here yet
([DROPPED-FEATURES.md](../DROPPED-FEATURES.md)). The same approach applies when
it does.

*Later: episode tracking now exists ([ADR-0022](0022-episode-tracking.md)), so
this is unblocked and unbuilt. Sonarr's TVDB-keyed series need one step this
migration did not — see DROPPED-FEATURES.*

**Quality profiles, tags, monitored state, history and download clients are not
imported.** They describe how Radarr was configured, not what the library is,
and this software's equivalents are not the same objects.

---

## Addendum — 2026-09-26: the operator is starting a new library

The operator decided to start a new library rather than migrate one. This
importer stays as it is: it does nothing unless a Radarr database is placed in
the migration directory, and removing working, tested code nobody asked to
remove would be a change of its own. The Sonarr importer this ADR's approach
anticipated is no longer planned (DROPPED-FEATURES). What starting from nothing
needed instead is [ADR-0025](0025-adding-a-series-before-it-is-on-disk.md).

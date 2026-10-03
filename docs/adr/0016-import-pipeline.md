# ADR-0016 — The import pipeline, and the authority background work holds

**Status:** Accepted · 2026-09-12 · Builds on [ADR-0014](0014-download-engine.md), [ADR-0015](0015-library-path-containment.md)

## Context

A download finishes. Something has to decide *which file in it is the media*,
*what that media is*, *where it belongs*, and *whether it is better than what is
already there* — then place it without breaking seeding and without lying about
what it did.

Every one of those decisions fails silently when it fails. That is the defining
property of this part of the system, and it shapes every choice below.

## Decision

### 1. File selection is an allowlist, and the reason is asymmetry

A realistic torrent:

```
Movie.2019.1080p.BluRay.x264-GRP/
  Movie.2019.1080p.BluRay.x264-GRP.mkv     18 GiB   <- the film
  Sample/sample.mkv                        48 MiB   <- not the film
  Subs/2_English.srt                       71 KiB
  RARBG_DO_NOT_MIRROR.exe                   1 KiB   <- an executable
  movie.nfo                                 3 KiB
```

Choosing wrongly does not error. It produces a library entry that plays a
48-megabyte sample, or an `.exe` in a media folder that gets synced to a phone,
or a `.nfo` renamed to `Movie (2019).mkv`.

So selection is an **allowlist of container extensions**. A denylist has to be
right about every file type a stranger might invent a reason to include; an
allowlist has to be right about the dozen containers this software can play.
Only one of those is a finite problem.

Deliberately excluded and worth naming:

- **`.iso`, `.img`** — a disc image cannot be direct-played or probed without
  mounting. Importing one produces a library entry nothing can play, so it is
  skipped with a reason rather than accepted broken.
- **`.rar`, `.zip`, `.001`** — unpacking means running a decompressor over
  attacker-controlled input, which is a process-boundary problem (ADR-0007), not
  something to do inside the main binary.

### 2. Sample detection: different rules for directories and filenames

This is the part that looked simple and was not.

A **directory** named exactly `Sample` or `Extras` is unambiguous — nobody names
a folder that by accident — so an exact segment match is safe.

A **filename** is not, and matching the word anywhere in it discards real films.
The counterexamples are not contrived:

| Name | What a naive word match does |
|---|---|
| `Free.Samples.2012.1080p.BluRay-GRP.mkv` | discards a real film |
| `Trailer.Park.Boys.The.Movie.2014.1080p.mkv` | discards a real film |
| `The.Trailer.Park.Boys.S01E01.mkv` | discards a real series |
| `Resampled.2019.1080p.mkv` | substring matching discards this |

So in a filename the marker must be the **whole stem or its last word**, which
is where every convention puts it (`sample.mkv`, `Movie-sample.mkv`). A marker
buried mid-name is missed by this rule and caught by the **size rule** instead —
which is the reliable one, and the reason it exists: release groups are
inconsistent about naming samples and consistent about their size (a sample is a
minute or two of a two-hour film).

My own test caught the first version of this rule discarding *Free Samples*;
*Trailer Park Boys* I found while fixing it.

### 3. The import source is contained, not just the destination

**This was a real vulnerability, caught by a structural test rather than by
review.**

The importer built its source path with `filepath.Join(downloadDir, declaredPath)`.
The declared path comes from the torrent's file list, which the *uploader* wrote.
`Join` cleans, so `"../../../../etc/shadow"` does not produce a path under the
download directory — it produces `/etc/shadow`. Hardlinking that into a media
library puts it somewhere this software serves over HTTP to anyone who may
browse.

`library.TestNothingWritesOutsideAVault` fails the build on `filepath.Join` in
the import path, and it fired on that exact line. The fix is
`library.ContainedSource`: the download directory is opened as an `os.Root` and
every declared path is resolved through it, so containment is the kernel's, per
component, at the moment of use.

Kept as a regression test that bites: with the check removed, a file from
outside the download is hardlinked into the library within one test run.

The lesson is the one ADR-0013 already recorded for egress and ADR-0015 for
library paths: **the failure that matters is not a check returning the wrong
answer, it is a code path that never consults the check.** A runtime check
cannot prove such a path does not exist. This is the third time that technique
has caught something.

### 4. Upgrades replace reversibly, by moving to trash

An upgrade to a *different* quality produces a different filename, so leaving
the old file would put both in the library. An upgrade to the *same* quality — a
PROPER — produces the identical filename, so the link fails with "file exists"
and the upgrade silently never happens. Both were bugs found by the same test.

The old file is therefore **moved into a trash folder inside the same root**
(`.cmediastack-trash/<timestamp>-<name>`), and its database row forgotten. Not
deleted:

- §2 asks that destructive operations be reversible. A rename is; an unlink is
  not. An operator who disagrees with an upgrade moves their file back.
- A rename within one filesystem is atomic and instant whatever the file's size.
- Trash **inside** the root rather than beside it, because a folder outside
  every root is a path no Vault contains — exactly the uncontained write
  ADR-0015 exists to prevent.

### 5. Background work gets its own principal, and it is deliberately weak

The importer runs from the scheduler. There is no person behind it, so there is
nobody whose authority it borrows and nobody to hold responsible — which makes
`authz.SystemPrincipal` the one place in the codebase where authority appears
from nowhere, and therefore the thing an attacker who can influence a background
task would reach for.

Its grant is two permissions:

- `PermBrowse` — read the library.
- `PermManageRootFolders` — which yields `EffectMutateLibraryPaths`, needed for
  exactly one thing: moving a superseded file into trash.

**It explicitly does NOT hold `PermDeleteMediaFiles`.** Nothing scheduled may
unlink a media file. That is what makes the trash design load-bearing rather
than a nicety: with destroy authority the importer could simply unlink, and it
cannot.

> **Superseded by the addendum below.** The single shared grant described here
> was replaced by a per-task one when the trash purge was built — the purge must
> unlink and the importer must not, which a shared set cannot express. The
> conclusion above still holds for the importer, and the tests that assert it are
> now `TestEachBackgroundTaskHoldsExactlyItsOwnGrant` and
> `TestTheImporterCannotDestroyMediaBytes`.

`TestOnlySchedulingCodeCanMintASystemPrincipal` reads every package's source and
fails if `SystemPrincipal` is called outside `main.go`, where scheduled work is
registered. That structural check is what makes the grant safe to exist: the
authority is visible beside the task that receives it, not buried in a package
where nobody reviewing a handler would see it.

### 6. Hardlink, never move; and every exit is recorded

Hardlinking is ADR-0015's decision, and it holds here: the same bytes carry two
names, the torrent client keeps seeding, the library costs no extra disk.
Verified against the binary — the imported file and the download share inode
`975264`.

Where a hardlink is impossible (different filesystems, EXDEV **specifically** —
every other link error is a real problem and copying past it would turn a
permissions bug into silent disk consumption) the file is copied and the result
says so in words.

Success, skip **and** failure all write an `import_record`. "It downloaded and
then nothing happened" is the complaint this category of software earns, and
`GET /api/v1/queue/{hash}/history` is the answer.

## Consequences

**Accepted.**

- **Upgrade decisions use the DEFAULT quality ladder, not the profile that
  grabbed the release.** The grab does not record its profile on the queue row
  yet, so an operator whose profile prefers 1080p WEB-DL to a 2160p remux (a
  reasonable preference on the i5-6500T — ADR-0005) is not consulted here. Both
  files survive either way, so the cost of being wrong is a skipped upgrade an
  operator can force by deleting the existing file, not a lost one.
  `release.DefaultRank` documents this at the seam.
- **Nothing purges the trash.** Superseded files accumulate. Purging is the
  destructive half and will need its own authority decision rather than a line
  added to the system grant.
- **A season pack is refused**, not split. Selection picks one file; a pack
  needs per-file parsing and per-episode placement.
- **Identification is from the release name alone.** No metadata provider, so no
  TMDB/TVDB ids, no posters, and no table of episodes the instance does *not*
  have — inventing those rows from the files we hold would produce a season that
  is always 100% complete, which is worse than no answer.
- **An operator can edit nothing yet.** The parser is good, not omniscient, and
  a title it read wrongly currently needs a database edit.

---

## Addendum — 2026-09-12: deletion, the purge, and per-task authority

Building the trash purge proved one decision above wrong, and the correction is
worth recording rather than quietly editing.

### The shared system grant was a mistake

§5 above described `authz.SystemPrincipal` as holding one set of permissions for
every background task. That worked while the only tasks were the importer and
the scanner. The purge broke it: **the purge must unlink files, and the importer
must not.** A shared set containing `PermDeleteMediaFiles` would have handed
destroy authority to the importer as a side effect of building a cleanup job —
and the importer's whole reversible-upgrade design rests on it *not* having that.

So the grant is now per task, and each is smaller than the shared one was:

| Task | Grant | Why not more |
|---|---|---|
| `system:import` | browse, path-mutation | Path mutation is for moving a superseded file to trash. **Not destroy**: that is what makes an upgrade reversible |
| `system:library-scan` | browse **only** | A scan changes nothing on disk, not even a rename, so it has no business holding an effect that could |
| `system:trash-purge` | browse, **destroy** | It unlinks. **Not path-mutation**: it does not move things around a library |

An **unknown** task gets nothing at all rather than a default set — a typo in a
task name must produce work that cannot act, not work that quietly inherits
somebody else's authority.

`TestEachBackgroundTaskHoldsExactlyItsOwnGrant` walks `AllPermissions` for every
task and also asserts that every declared task is covered, so a new one added
without a line in the test fails the build.

### Deleting media does not delete media

A media library is often the only copy, an operator clicking the wrong row is
the likeliest failure by a wide margin, and "are you sure?" has never stopped
anybody. So `DELETE /api/v1/admin/media/{id}` moves the files into the root's
trash and forgets their records. Nothing is unlinked.

The retention window (`media.trash_retention`, 7 days) **is** the undo. The
purge task unlinks afterwards, and it is the only thing in this software that
unlinks a media file without a person having named that specific file.

There is deliberately **no "delete and purge now" parameter.** A checkbox that
destroys data on a mis-click is how reversibility gets lost. An operator who
wants the space back sooner empties the trash, which is a separate, named act.

Verified end to end against the binary:

```
DELETE  -> {"deleted":"Arrival","trashed":1,"note":"…Nothing was unlinked."}
on disk -> movies/.cmediastack-trash/20260912-215333.999-Arrival.2016.1080p.BluRay-GRP.mkv
TRASH   -> retention=168h  purge_after=2026-09-19T21:53:33
RESTORE -> "Arrival (2016)/Arrival.2016.1080p.BluRay-GRP.mkv"
```

### Smaller decisions in the same increment

- **A zero or negative retention is refused**, not honoured. It would delete
  files the instant they were trashed, which is the design the trash exists to
  avoid.
- **A file with no parseable timestamp is never purged.** Something an operator
  dropped into the trash folder by hand is still their file; erring towards
  keeping it costs disk, erring the other way costs the file.
- **Restore derives its destination from the file's own name**, not from a
  remembered original path. The original is not recorded — `Supersede` keeps the
  basename and a timestamp and nothing else — and that is a deliberate limit: a
  stored path would mean a restore could write anywhere the record named, while
  a name derived now is one the current containment rules apply to.
- **Restore only accepts paths inside the trash folder.** The vault would
  contain any path in the root, but containment is not the same as the operation
  being the right one; without this check, "restore" becomes "move any library
  file somewhere else".
- **A deletion stops at the first failure** rather than pressing on. A
  half-deleted item with some files trashed and some not is a state an operator
  cannot reason about; leaving the rest in place means they can retry.
- The structural test now also fails on a Vault method that writes through the
  root *without being declared destructive* — so a new method added with no
  authority decision is caught rather than reviewed for. Verified by adding a
  plausible `Tidy()` and watching it fail.

# ADR-0053: Deleting one file, and purging one trashed file now

**Status:** accepted
**Date:** 2026-10-01
**Related:** [ADR-0015](0015-library-path-containment.md), [ADR-0037](0037-libraries-and-rating-ceilings.md)

## The problem

Deletion works on a whole title. Its files go to the root's trash, which is
the undo, and a scheduled purge unlinks them after the retention window
(a week by default). Two things an operator needs are missing:

- **Deleting one file.** A bad episode, a broken remux, a duplicate format
  of a book. Today the only way is to delete the whole title and add it back.
- **Emptying the trash of one thing, now.** A file is in the trash and the
  disk is full, or the operator wants it gone now. Today they wait a week, or
  reach for a shell.

## Decisions

### 1. One file, to the trash

`DELETE /api/v1/admin/files/{id}`, under `library.delete`, as a title's
delete is. It moves that one file to its root's trash and forgets its row,
and nothing is unlinked. The title stays. What the file held becomes missing
again, so an episode, a film, an album's track or a book is wanted again
while it is monitored. That is usually the point: the file was bad.

The file is read through the caller's scope, so a file of a title the caller
may not see answers as a missing one (ADR-0037). The delete is audited as
`media.file_deleted`. A file whose bytes are already gone is forgotten and
reported, as a title's delete reports one.

### 2. One trashed file, unlinked now

`POST /api/v1/admin/trash/purge` with a root and a trash path, under
`library.delete`. It unlinks that one entry immediately, whatever its
retention, through the root's contained vault. The vault's own check of the
destroy-bytes effect still applies (ADR-0015). It is refused unless the path is
inside the trash: purging is for what was already deleted and nothing else.
It is audited as `media.purged`, with what was freed.

There is still **no "delete and purge" in one step**, and no "purge
everything". Two separate, named acts are what keep a mis-click recoverable.
A file is deleted to the trash, and only someone looking at it in the trash can
unlink it now.

### 3. Screens

A file row on a title's page offers *Delete file* to whoever holds
`library.delete`. The trash list offers *Purge now* beside *Put back*. Purge
asks again before it acts, because it is the one button in the software that
cannot be undone.

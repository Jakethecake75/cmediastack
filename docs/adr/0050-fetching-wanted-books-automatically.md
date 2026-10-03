# ADR-0050: Fetching wanted books without a person

**Status:** accepted
**Date:** 2026-09-30
**Related:** [ADR-0030](0030-automatic-acquisition.md),
[ADR-0047](0047-fetching-wanted-albums-automatically.md), [ADR-0049](0049-a-books-files.md)

## The problem

A person can search for a book and grab it (ADR-0049). Automatic acquisition
fetches episodes, films (ADR-0030) and albums (ADR-0047) when the operator
turns it on, and not books.

## Decisions

### 1. The album pass, for books

`acquire.Service.RunBooks`, the task `acquire.books`, is registered only with
`acquisition.automatic` on and runs on the search pass's interval. It is
**the album pass with a different list and a different search**. The two
share one runner, so they share the lock, the tunnel gate, the budget, the
back-off, the memory of failed fetches, the grab, and the `system:acquire`
audit line. A candidate must be `search.MatchBook`'s, sealed to the book,
with a named format, seeders and a link, and never in the queue.

### 2. What is wanted

A book is wanted when the Wanted screen lists it: a monitored book with no
file. It must also have an **author**, without which no release can be told
from another book of the same name (ADR-0049, decision 4). A book with no
author is left alone, and the screen says why.

### 3. No "once" rule

An album is grabbed automatically once, because its track list can name a
track no release carries (ADR-0047, decision 3). A book has one file, and a
book whose download was imported has it and is no longer wanted. A download
that brought no book file was skipped, so another release may be tried. The
queue still never yields the same release twice.

### 4. Kept, and shown

A book's state is kept in `acquire_state.item_id`, which a book's
`media_item` owns as a film's does; it is told from a film's by the item's
kind, so no migration is needed. A book download under way is read as that
book's, and never, as 5f's in-flight check read it, as an episode of the book's
item. The Wanted screen's books carry the same *automatic* line as everything
else.

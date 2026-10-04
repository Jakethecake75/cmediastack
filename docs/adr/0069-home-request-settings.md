# ADR-0069: Three sections — Home, Request, Settings

**Status:** accepted
**Date:** 2026-10-04
**Related:** [ADR-0011](0011-server-rendered-shells-no-build-step.md), [ADR-0037](0037-libraries-and-rating-ceilings.md), [ADR-0043](0043-discover.md)

## The problem

The app had grown to twenty-nine tabs in one bar, in the order they were
built. Watching something, asking for something and administering the instance
sat side by side, and finding, requesting and adding a title were three tabs.
The operator asked for three sections: a home for playback, one place to
request, and settings for everything else — agreed on a mock-up on 2026-10-04.

## Decisions

### 1. Three sections, each with its own tabs

The top bar holds **Home**, **Request** and **Settings**; the section's tabs
sit under it. A tab still belongs to the permission that reveals it, a section
with no visible tab is not shown, and every view keeps its `#hash`, so links
and bookmarks keep working.

| Section | Tabs |
|---|---|
| Home | Home, Library (and Watch, reached from a title) |
| Request | Search, Discover, Requests, Downloads, Wanted, Indexer search |
| Settings | Getting started, Storage, Identify, Indexers, Metadata, Notifications, Network, Accounts, Approvals, Invites, Sessions, API tokens, Security, Migrate, Audit log, Tasks, Backups, Health — grouped |

### 2. Home is for watching

Rows of posters: **Continue watching** (the account's own places in files not
yet finished, newest first), **Recently added**, **Movies**, **TV Shows**,
**Music**, **Books**. A poster opens the title. Continue watching comes from a
new `GET /api/v1/me/continue`, read under the account's library scope like any
item read (ADR-0037): a title the account may not see is not offered, even if
it once watched it.

### 3. One search to find, add or request

Request's **Search** is the place to find a title. An account that may add to
the library searches films, series, music and books and adds them, as the Add
tab did. An account that may only request searches films and series and asks
for one with **Request** — through a new `GET /api/v1/requests/search`, held by
`request.submit`, the same provider search Discover already exposes to it. The
free-text request form stays beneath, for what the provider does not know.

## Consequences

- The Overview tab is gone; Home replaces it. Its summary of the signed-in
  account — role, libraries, permissions — moves to the top of Security.
- An account that may only request can search the metadata provider by title,
  as Discover already let it browse.

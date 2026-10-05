# ADR-0075: Requests add themselves, ask for seasons or episodes, and ask for removals; four fixes

**Status:** accepted
**Date:** 2026-10-05
**Related:** [ADR-0028](0028-an-approved-request-is-satisfied-by-a-library-item.md), [ADR-0069](0069-home-request-settings.md), [ADR-0070](0070-one-search-live-downloads-remove-deletes.md), [ADR-0071](0071-transcoding-and-grab-on-add.md)

## The problems

The operator asked for these, after v0.4.4:

- **Add to library did nothing after approval.** It should not be needed: an
  approved request should be added on its own.
- **A User should be able to ask for a title to be removed** and its files
  deleted.
- **A series request should be able to name seasons or episodes.**
- **Downloads jumped around**, every two seconds.
- **Search needed a Clear button** for the search and what the indexers
  answered.
- **Dark mode should be a switch**, not the system's setting.

## Why Add to library did nothing

The requests list is on Search (ADR-0070). The button set the address to
`#find`, which it already was, so no `hashchange` fired and nothing loaded.
The button now loads the Search screen itself. It is kept only for a request
that could not be added on its own, such as one made in words.

## Decisions

### 1. A request names the provider's title, and approving it adds it

A request made from a search result now carries the provider's id
(`tmdb_id`, migration 0039). Approving it:

1. adds the title to the library, as **Add** does: a film, or a series with
   every aired episode wanted;
2. links the request to it (ADR-0028), so it is fulfilled when a file
   arrives;
3. starts the search at once (ADR-0071).

A title already in the library is linked, and what was asked for is marked
wanted. When more than one root folder could hold it, the approver picks one
beside **Approve** (`root_folder_id`). If the add fails, the request is still
approved, the message says why, and **Add to the library…** is shown.

A role that approves its own requests (Manager) gets the same on submission.

A request made in words has no id, and is approved as before.

### 2. A series request can name seasons or episodes

**Request** on a series offers *The whole series*, or seasons, or single
episodes. The season list comes from the provider through a new route for
requesters, `GET /api/v1/requests/series/{id}/seasons`.

- **Storage.** The choice is stored canonically, as `S1,S2E5`, and is part of
  the match key. Two people asking for different seasons hold two requests.
- **Approval.** Only what was named is wanted: the series is added with
  nothing monitored, then those seasons and episodes are turned on.
- **Fulfilment.** A request for part of a series is not fulfilled on linking
  just because another part is on disk. It is fulfilled when a file arrives.

### 3. A User can ask for a title to be removed

A title's page has **Ask to remove…** for an account that may request and may
not delete. It covers the whole film or series, or chosen seasons and
episodes of a series (the ones on disk). The request (`action: remove`) names
the library item, read in the asker's scope, and goes into the same queue.

- **Who approves.** Only somebody who may delete files (**Approve and
  delete**). A Manager may approve requests and not delete, and is refused
  with 403 before anything changes. A removal is never auto-approved.
- **The whole title.** Approval runs the title's delete: files to the trash,
  the title out of the library, its downloads stopped.
- **Seasons or episodes.** Their files go to the trash, and those seasons and
  episodes are no longer wanted, so automatic acquisition does not fetch them
  back.
- **Afterwards.** The request is fulfilled and reads *removed*. Removal
  requests are never fulfilled by an import.

### 4. Downloads keep their order

The torrent library lists torrents in Go map order, which changes on every
read. The list is now sorted newest first, by when each was added, with the
info hash breaking ties. Rows stay where they are between reads, so the page
no longer rebuilds every two seconds.

### 5. Clear

**Clear** beside **Search** empties the words, the year, the results, what
each indexer answered and any request being added for.

### 6. Light or dark, chosen

A **Light mode** / **Dark mode** button in the top bar sets `data-theme` on
the page and keeps the choice in this browser (`localStorage`). The default
is dark; the system's preference is no longer followed. The sign-in pages
read the same choice. The bundle sets the theme as it starts, so a page can
show dark for a moment before it turns light.

## Consequences

- Approving a request can now cause a download without anybody choosing the
  release. That was already true of **Add** since ADR-0071, and approval is a
  person's decision.
- One new route, for a series' seasons.
- Tests: `TestApprovingARequestAddsItsFilmAndStartsTheSearch`,
  `TestApprovingPartOfASeriesWantsOnlyThatPart`,
  `TestARemovalIsAskedForAndOnlyADeleterApprovesIt`,
  `TestARemovalOfPartOfASeriesTakesThoseFilesAndStopsWantingThem`,
  `TestAScopeHasOneCanonicalForm`, `TestTheQueueIsNewestFirstAndStable`.
- Requests made before this have no id, and keep the old flow.

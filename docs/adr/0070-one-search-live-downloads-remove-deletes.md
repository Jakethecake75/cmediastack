# ADR-0070: One search with posters, live downloads, Remove deletes

**Status:** accepted
**Date:** 2026-10-04
**Related:** [ADR-0069](0069-home-request-settings.md), [ADR-0030](0030-automatic-acquisition.md), [ADR-0037](0037-libraries-and-rating-ceilings.md), [ADR-0043](0043-discover.md)

## The problem

Once downloads worked, the operator asked for the Request section to be
simpler, and for Downloads to behave like a download client:

- Search, Requests and Indexer search were three tabs, and Discover a fourth.
- A title search was a list of names, when picking the right film is done by
  its poster (as in Jellyseerr).
- Downloads changed only on a reload, and opened on a block of engine notes
  and a "peer connections" diagnostic line.
- Remove stopped a download and left its files, and its row stayed listed as
  "stopped", so the page filled with everything ever removed.

## Decisions

### 1. Request is Search, Downloads and Wanted

Discover's tab is gone; its route stays for the API. **Search** is one tab
holding:

- the title search, with films, series, music, books, and **Releases** (the
  indexers) where the account may search them;
- the account's requests, with the form to ask in words;
- the problems it reported.

It is shown to an account that may add, request or search indexers. The old
links (`#add`, `#ask`, `#search`, `#requests`, `#discover`) open it.

### 2. Film and series results are posters

A film or series search answers with a grid of posters. Choosing one opens
its overview with **Add** or **Request**.

The poster route fetches only what this instance has recorded, so that a
request cannot make the server fetch an arbitrary image (identify's
`CachePoster`). Search results are therefore recorded too: each match's poster
path, against the account that searched (`offered_poster`). The poster route
serves:

- an editor, any recorded poster, as before;
- any other account, a poster of a title it can see (ADR-0037), or one it
  was itself offered by a search.

A poster still tells nobody what the library holds.

### 3. Downloads is live

The page re-reads the queue every two seconds while it is open and the window
is visible. The figures on each row change in place, without a rebuild. The
engine notes and the peer-connection line are no longer shown there; the API
still returns them.

### 4. Remove deletes the download and drops it from the list

Remove stops the transfer and deletes its folder in the download directory
(`<data_dir>/<info hash>`). That folder is the download's own copy: an import
hard-links or copies into the library, so a title already imported keeps its
file. A stopped row is not listed.

The row is kept in the database rather than deleted. The queue is automatic
acquisition's blocklist (ADR-0030, decision 8), and a removed release must not
be grabbed again by itself. A row the engine is not running, such as a
finished download, can be removed the same way.

## Consequences

- Remove can no longer be undone. Once a download is gone, the only way back
  is to grab it again.
- `offered_poster` grows by one row per distinct title an account has seen in
  a search. That is small, and pruning is left until it is not.

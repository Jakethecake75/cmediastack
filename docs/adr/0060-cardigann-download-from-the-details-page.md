# ADR-0060: A download link read from the tracker's details page

**Status:** accepted
**Date:** 2026-10-03
**Related:** [ADR-0058](0058-cardigann-public-definitions.md), [ADR-0059](0059-cardigann-sign-in.md)

## The problem

Some trackers do not put the `.torrent` or magnet link in their search
results, only a link to each torrent's details page. Their definitions say,
in a `download` block, how to find the real link on that page. ADR-0058
refused such definitions, so those trackers still needed a Prowlarr.

## Decisions

### 1. The link is found when the release is grabbed

A search result keeps the link the definition's `download` field gives,
which for these trackers is the details page. When it is grabbed:

1. that page is fetched, signed in if the tracker signs in (ADR-0059);
2. the block's `selectors` are tried in order, each with its `attribute` and
   `filters`;
3. the first that matches gives the link, resolved against the page.

A magnet is handed back unfetched, as a magnet always is. A `.torrent` link is
fetched as any other download. Only when the grab is made is one more request
spent, never once for each search result.

### 2. Checked as any download link

The link the page gives is the tracker's choice, so it passes the same rule
as a link in a feed:

- http(s) or a magnet, nothing else;
- no private address but the indexer's own;
- every redirect re-checked.

A details page whose selectors find nothing makes the grab fail, saying so.

### 3. What is not followed

The block's `before` request (a request to make before the download) and its
`infohash` form are refused when the definition is saved, naming them, like
anything else this build cannot follow.

## Known limitations

- **A `before` request and the `infohash` form** are not followed.

# ADR-0041: A calendar and a feed, behind a token in the address

**Status:** accepted
**Date:** 2026-09-29
**Related:** [ADR-0022](0022-episode-tracking.md),
[ADR-0037](0037-libraries-and-rating-ceilings.md), SECURITY.md (*API tokens*,
*The anonymous surface*)

## The problem

Sonarr's calendar is how many people know tonight's episode is out, and
Jellyfin's "recently added" is how they know it arrived. The air dates are
stored (ADR-0022) and every import is recorded, and two routes for them have been
registered since Phase 1 — `GET /api/v1/feeds/{token}/calendar.ics` and
`/rss` — answering 501, and marked *authenticated*.

That mark cannot stand. The clients are a phone's calendar and a feed reader.
They are given a URL and nothing else: no cookie, no header, no sign-in. The
records called these "per-user revocable tokens, never public", and the token
in the path is the only credential they can carry.

## Decisions

### 1. A feed token, one per account, which can read two routes and nothing else

`POST /api/v1/me/feeds` mints it: 256 bits, prefixed `cms_feed_`, shown once
with the two addresses, stored as its SHA-256 alone. Minting again replaces the
old token. `DELETE /api/v1/me/feeds` revokes it, and `GET /api/v1/me/feeds`
says whether one exists and when it was last used. All three are
**session-only**, like every credential route: an API token may not mint a
credential.

The two feed routes are **anonymous** in the routing table — no session is sent
— and the handler authenticates the path's token itself. A principal built from
a feed token holds **`media.browse` and nothing else**, and only when the
account's role holds it. It is the account's own scope (ADR-0037): a restricted
account's calendar holds only its libraries and titles within its ceiling. A
suspended, disabled or un-enrolled account's token answers as a wrong one does:
404, the same bytes. Tokens, being in an address, are:

- masked in every log line, like a password (`/feeds/[REDACTED]/`);
- rate-limited at 120 an hour from an address;
- never accepted anywhere but these two routes.

### 2. The calendar is the episodes of the account's series, from a month ago to three months ahead

An iCalendar file with one all-day event per episode with an air date in that
window, of every series the account can see. The event is titled *Severance
S02E03 · Who Is Alive?* and its description says whether the episode is held.
Each event's UID is stable, so a calendar updates rather than duplicates. Films
are left out: the release date is the provider's answer on the day the film was
added, and nothing refreshes it (ADR-0026).

### 3. The feed is what arrived in the library

RSS 2.0: the 50 most recent imports of titles the account can see, newest
first, each an item titled with the title and what arrived — *Severance S02E03*,
*Dune (2021)* — its date the import's, its link the instance's library page.

## Rejected alternatives

**A token in the query string**, as Sonarr has. The same credential in a
different place. The path form is the one the routing table already had, and the
redaction covers it.

**An API token with a feed scope.** An API token authenticates in a header; a
calendar app sends none. A token shaped differently and valid in exactly two
places is easier to reason about than one kind of token with a special case.

**Films in the calendar**, from the provider's release date. It is stale by
design.

## Known limitations

- Anybody who has the address can read the calendar and the feed until the token
  is replaced. That is what a calendar subscription is.
- Times are dates: an episode's air date carries no hour.

## Verification

| Claim | Test |
|---|---|
| A feed token is minted once, replaced, revoked, from a session only | `api.TestAFeedTokenIsACredentialOfItsOwn` |
| It reads the two feeds and nothing else, as its account, scoped; a wrong, revoked or suspended one is 404 alike | `api.TestAFeedTokenReadsOnlyTheFeeds` |
| The calendar is the window's episodes, stable, escaped and folded as RFC 5545 asks | `api.TestTheCalendarIsValidICalendar` |
| The feed is recent arrivals, scoped, newest first | `api.TestTheFeedIsWhatArrived` |
| The token never reaches a log line | `logging.TestAFeedTokenIsRedacted` |

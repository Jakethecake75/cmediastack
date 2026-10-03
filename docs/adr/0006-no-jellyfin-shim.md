# ADR-0006: No Jellyfin API compatibility shim

**Status:** accepted (Phase 0), confirmed in §13 — "No API"
**Decision:** CMediaStack serves its own web player and implements **no part**
of the Jellyfin (or Emby, or Plex) client API. Existing Jellyfin clients — the
Android TV app, Infuse, Findroid, the Roku channel — will not work against it.

## Why not

The appeal is obvious: implement enough of one HTTP API and inherit a decade of
client development on every TV platform. It is rejected for three reasons, in
increasing order of how much they cost.

**It is not one API, it is an API and its accidents.** Jellyfin's client surface
is large, partly undocumented, and its real contract is "what the clients
happen to depend on" — including behaviours that are bugs. Matching the spec is
not the work; matching the accidents is the work, it is discovered one broken
client at a time, and it never finishes.

**It contradicts the authentication model.** §2 is "there is no anonymous access
to anything", and every account carries mandatory app-based MFA. Jellyfin's
client protocol is built around a long-lived `AccessToken` in a header, obtained
by a flow that has no place for a second factor and is not revocable the way
this project's opaque server-side sessions are. A shim would either break every
client that authenticates the normal way, or carve a hole in the one rule §2
states most plainly. Both are worse than not having it.

**It would constrain the library model permanently.** Jellyfin's clients assume
Jellyfin's shapes — its item hierarchy, its image endpoints, its `UserData`. A
shim makes those shapes load-bearing, and every later decision has to be
compatible with a data model chosen by a different project for different
reasons. That is the cost that does not show up in the estimate and never goes
away.

## What is lost, stated plainly

**TV clients.** This is the real cost and it is not small. A browser on a
living-room TV is a worse experience than a native app, and no amount of
responsive CSS closes that gap. Anybody choosing this over Jellyfin is choosing
a worse television experience in exchange for one authenticated, audited service
instead of nine.

That trade is Jacob's to make and he made it. It should be re-examined the first
time watching something on a TV is annoying enough to matter.

## What is not foreclosed

- A **native client of this project's own**, speaking this project's API, with
  a real authentication flow including MFA. Expensive, but not contradictory.
- **Casting** to a device that plays a URL, which needs no client API — only a
  short-lived signed playback URL, which Phase 4 has to produce anyway.

## Consequences

- Phase 4 negotiates capabilities with **one** client — a browser this project
  ships — so [ADR-0005](0005-transcode-policy-skylake.md)'s direct-play-first
  policy can rely on an accurate capability report rather than a pessimistic
  one from a third-party app.
- There is no compatibility surface to keep working, so there is no class of bug
  that reads "version 10.9 of some client changed and playback broke".
- The web player is not a fallback. It is the product, and it has to be good.

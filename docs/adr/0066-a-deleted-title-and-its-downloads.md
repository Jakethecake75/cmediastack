# ADR-0066: A deleted title, its downloads, and why peers do not connect

**Status:** accepted
**Date:** 2026-10-04
**Related:** [ADR-0014](0014-download-engine.md), [ADR-0016](0016-import-pipeline.md), [ADR-0065](0065-socks5-from-the-web.md)

## The problem

Found on the operator's first real instance:

1. A series was added (id 1), a season of it grabbed, and the series deleted.
   Its queued download stayed, aimed at item 1. A film added next was given
   id 1 again — `media_item` used a plain `INTEGER PRIMARY KEY`, and SQLite
   hands out the largest freed rowid again — so the season download now said
   it was for the film, and had it finished, its import would have been
   judged against the film.
2. A download knew of 27 peers and connected to none, and nothing anywhere
   said why: the torrent library drops a failed peer connection silently.

## Decisions

### 1. A title's id is never used again

`media_item` is rebuilt with `AUTOINCREMENT` (migration 0037, foreign keys
off as for 0026, every column carried). A reference that outlives its title —
a queue row, a request, an issue, an audit line — then names nothing rather
than whatever title came next.

### 2. Deleting a title stops the downloads aimed at it

After a title is deleted, every queued transfer whose target is that title is
removed from the engine, as the Queue's own *Remove* does: the transfer stops
and its files stay where they are. A stopped transfer is never imported, so
nothing a deleted title grabbed can be filed against anything. The delete's
answer says how many were stopped.

### 3. The Queue says why peers are not connecting

The engine counts every outgoing peer connection it attempts through the
guard, how many failed, and keeps the last failure's text and time. The Queue
shows them — "Peer connections: 312 tried, 312 failed; last: egress: socks5
proxy refused: connection not allowed by ruleset" — so a download that sits at
zero says whether the proxy, the network or the swarm is the reason.

## Consequences

- Ids grow without reuse; nothing reads meaning into their gaps.
- A download stopped by a delete stays listed as stopped until it is removed,
  and can be started again from the Queue if it was wanted after all.
- The counts are the engine's since it started, across every transfer: enough
  to tell "nothing connects" from "this swarm is empty", not a per-peer log.

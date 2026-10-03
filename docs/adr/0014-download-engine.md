# ADR-0014 — The download engine: what is guaranteed, what is derived, what is only promised

**Status:** Accepted · 2026-09-12 · Builds on [ADR-0001](0001-egress-control-wireguard-netns.md), [ADR-0013](0013-egress-guard-design.md)

## Context

Requirements §2 says the download engine must never egress outside its
configured tunnel, and that if the tunnel is unreachable, downloads pause rather
than falling back to a direct connection. §12 says not to reinvent solved
problems — wrap a mature library rather than implementing BitTorrent.

Those two instructions pull against each other, and the tension is the whole
substance of this decision. Wrapping `anacrolix/torrent` is a few hundred lines.
Making §2 true for a protocol that is *structurally* hostile to it is the work.

BitTorrent is hostile to egress control in a specific way. It opens connections
to addresses supplied by strangers, over both TCP and UDP, from several
independent subsystems — peer connections, tracker announces, DHT, PEX, local
peer discovery — and most of those paths never touch an `http.Client`. A design
that routes "the downloads" through a proxy and declares victory has secured one
of five doors.

## Decision

### 1. Outbound peer connections are constrained exactly, not approximately

Two lines do it:

```go
tc.DialForPeerConns = false    // the library stops adding its own listening sockets as dialers
client.AddDialer(guardedDialer{dial: guard.For(ProfileName)})
```

`DialForPeerConns = false` is the one that matters. `AddDialer` **appends**; on
its own it adds a guarded path beside the library's default ones rather than
replacing them. Turning off `DialForPeerConns` empties the default set first, so
the guarded dialer becomes the only entry in `cl.dialers`.

**"We added a guarded dialer" and "ours is the only path out" are different
claims, and only the second one is the guarantee.**

### 2. That claim is tested behaviourally, because it cannot be tested structurally

The library exports no way to enumerate a client's dialers (`Dialers()` is
unexported). So `TestAClosedGateStopsPeerConnections` runs a seeder and a
leecher in the same process, on loopback, with the egress gate **shut**, and
asserts no bytes move.

This is the better test regardless. It asserts the property rather than the
mechanism, so it keeps working if the library changes how dialers are stored,
and it fails if some other path opens that a structural check would not know to
look at.

It is a real test, not a tautology: flipping `DialForPeerConns` back to `true`
makes it fail in 0.11 seconds with
`THE GATE LEAKED: 262144 bytes transferred with egress unhealthy`.

### 3. What CANNOT be constrained that way, stated plainly

- **DHT** binds its own UDP socket and speaks directly. A TCP dialer cannot
  carry it.
- **uTP** is UDP for the same reason.
- **Incoming connections** arrive at a listening socket. There is no dial to
  intercept.

No amount of care inside this package changes any of that. Pretending otherwise
would be the most dangerous thing in the codebase, because the failure is
silent: DHT announces the operator's real address to the swarm, nothing errors,
nothing logs, and the only symptom arrives months later in an envelope.

### 4. So the engine's configuration is DERIVED from the egress policy

`ConfigFor` takes the egress profile and returns the engine config. The operator
does not get to set these independently:

| Egress mode | DHT | uTP | Incoming | Why |
|---|---|---|---|---|
| Enforcement off | on | on | on | Nothing is tunnelled; said in a startup warning rather than implied |
| SOCKS5 / HTTP proxy | **off** | **off** | **off** | A proxy carries TCP and nothing else |
| Direct inside the namespace | on | on | on | The kernel carries the guarantee; the namespace has no bypassing route |

This coupling is the point. An operator **cannot** accidentally run DHT through
a SOCKS5 setup, because the two cannot be configured apart. The degraded mode is
degraded on purpose and says so in `Notes()`, which the queue API returns
alongside the queue — so "DHT is off, so magnet links may never resolve" appears
on the same screen as the stuck magnet it explains.

### 5. The engine refuses to start without a guard

`New` returns an error on a nil guard rather than starting unprotected. A
download engine that comes up outside its tunnel has already broken §2, and it
breaks it silently.

## Acquisition: how something gets into the queue

### The grab takes a ticket, never a URL

A search returns candidates. Each **accepted** candidate carries an opaque
`ticket`: the indexer's download URL, sealed with AES-256-GCM under the instance
master key, with the grabbing user's id as associated data and a 30-minute
expiry.

The alternative — a grab endpoint that takes a download URL — would make the
engine an open relay. Anyone who could reach the endpoint could have the server
fetch a destination of their choosing through the egress-guarded HTTP client,
and every check upstream (the indexer allowlist, the quality profile, the
ladder, the audit line naming a release) would describe something that was never
downloaded.

What sealing proves is narrow and worth stating precisely: **the client did not
change what the indexer offered.** It is not a claim that the URL is safe. The
indexer is still a third party, so:

- the URL is validated again before the fetch, by the same function the client's
  redirect check uses — one validator, held on the `Client`, because two copies
  drift and the half that drifts is the half nobody is looking at;
- **every redirect hop** is revalidated. A 302 to `169.254.169.254` is the
  obvious attack and it is the one that a design checking only the first URL
  walks straight into;
- the indexer is **re-resolved against current state** before anything is
  fetched. A ticket minted thirty minutes ago is not a standing exemption from
  an operator's decision to disable an indexer since.

Binding the ticket to the user id means a ticket handed to a colleague does not
let them grab with it — which is what makes the audit line naming who grabbed a
true statement rather than a guess.

### Grabbing is gated on the queue permission, not on search

Looking at what exists and causing the instance to acquire it are different acts
with different consequences. A role that may do the first without the second is
a policy an operator should be able to express, so `PermInteractiveSearch` gates
the search and `PermManageQueue` gates the grab.

## Persistence: why the queue is in the database

Without a persisted queue, a restart orphans every partial transfer. The bytes
stay on disk under the data directory, named by info hash, and nothing left
alive knows what they were or that anyone asked for them. That is the worst kind
of data loss: silent, invisible, and noticed only when the disk fills.

What is stored is what is needed to **re-add the transfer without contacting
anybody** — the magnet URI or the `.torrent` bytes, verbatim. A restart replays
from local state alone, because a tracker that is down, rate-limiting or deleted
must not cost an operator their queue.

What is deliberately **not** stored is the download URL. On many trackers it
carries the indexer's API key in its query string, and a row read by every queue
listing is the wrong place for a credential. The bytes it would have fetched are
already there, so the URL has no remaining use.

`Manager` exists so that "add a transfer" and "record that we added a transfer"
cannot come apart. If the API held an `Engine` and a `Store` separately, every
new call site would have to remember to write the row, and the one that forgot
would produce a transfer that vanishes on the next restart with its bytes still
on disk.

A queue row that cannot be written does not fail the grab — the transfer is
already running and refusing would stop nothing — but it is logged at ERROR and
the queue listing shows that transfer with status `unrecorded`, because "this
will disappear on restart" is exactly the state worth surfacing rather than
smoothing over.

## Seeding: the part that distributes

Seeding is not a detail of downloading. It is the part of this software that
**sends content to strangers**, and §13 puts the legality of that on the
operator. It gets its own policy and its own honesty.

**Obligations are tracked against durable totals, not the engine's counter.**
The torrent client's upload count is per-process. Judged against it alone, every
restart silently forgives everything already uploaded — an instance that seeded
back three times over reports near zero after a reboot and keeps distributing,
for weeks longer than the operator chose. The totals live in the queue row; the
scheduler adds the in-process delta each tick and detects a restarted counter by
it going backwards.

**When an indexer records no obligation, the instance seeds indefinitely.** It
does not stop at an invented threshold. An unknown requirement is not the same
as no requirement; a private tracker's requirement is the whole reason seeding
matters to an operator; and stopping early is the failure that costs an account.
An operator who does not want to distribute at all sets `download.seed: false` —
a decision they make once, in writing, rather than one this code makes for them
by guessing.

**Obligations are captured at grab time**, copied onto the row rather than
joined from the indexer later, because the obligation was incurred under the
rules in force then and editing an indexer must not silently rewrite what was
already promised.

Stopping seeding removes the torrent from the client and **leaves the files on
disk**. Unlinking bytes is `EffectDestroyMediaBytes`, a separate permission this
path does not hold.

## Consequences

**Accepted.**

- Under a SOCKS5 profile the engine is genuinely degraded: no DHT, no uTP,
  TCP-only, no incoming peers. Magnet links may never resolve. This is the
  honest cost of that deployment and ADR-0001's namespace guard is the way to
  avoid it.
- With NordVPN there is no port forwarding, so the instance is a passive peer
  regardless of `listen_port` (ADR-0001, accepted residual).
- Completion and seeding are noticed by a 30-second and a 60-second poll rather
  than by callbacks inside the torrent library. A poll reading the engine's own
  view has no ordering hazards and cannot wedge the library's event loop if the
  database is slow; the cost is that "complete" is recorded within a tick.
- `.torrent` blobs live in SQLite, capped at 2 MiB each by the fetch path. For
  a queue of this size that is unremarkable; for a queue of tens of thousands it
  would want revisiting.
- Only v1 (SHA-1, 40 hex) info hashes are accepted. A v2 hash is refused by name
  rather than producing a confusing "no such transfer".

**Not solved here.** Completion does not yet hand off to an importer — nothing
moves a finished download into the library, renames it, or hardlinks it. That is
Phase 3. Until then a finished transfer sits in the data directory with its
queue row marked complete.

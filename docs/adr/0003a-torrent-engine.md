# ADR-0003a: `anacrolix/torrent` in-process, with `qBittorrent-nox` as the documented fallback

**Status:** accepted (Phase 0), verified in increment 2e, built in Phase 2.

## Decision

BitTorrent is **`github.com/anacrolix/torrent`**, embedded in the downloader
process. No BitTorrent protocol implementation is written here.

If it proves inadequate, the fallback is **not** to write one: it is to drive
`qBittorrent-nox` over its Web API as a subprocess. That is a worse product — a
second service, a second config, the cross-service glue this whole project
exists to delete — and it is written down so the option is a decision rather
than a panic.

## Why in-process

The consolidation argument is the product argument. §1 is one process, one
config, one auth layer; shipping a torrent client alongside it reintroduces
precisely the seam being removed — a second service with its own credentials,
its own idea of where files live, and a webhook between them.

It is also what makes [ADR-0001](0001-egress-control-wireguard-netns.md)
enforceable. When the engine is a library, its dialer is *this* program's
dialer, and `egress.TestNoPackageDialsDirectly` can read the source and prove
nothing bypasses the guard. When it is a separate daemon, the guarantee becomes
"we configured qBittorrent correctly", which is a claim no test in this
repository can make.

## What had to be verified, and was

**It builds with `CGO_ENABLED=0`.** `anacrolix/torrent` can link
`anacrolix/go-libutp`, a C uTP implementation. Increment 2e confirmed the build
succeeds with it unlinked, using the pure-Go uTP path instead — so
[ADR-0002](0002-go-no-cgo.md) holds. This was a genuine risk to the whole stack
choice and was checked before committing to it, not after.

**`AddDialer` appends rather than replaces.** This is the finding that mattered.
Adding a guarded dialer does not remove the default one — both are used. What
makes the guarded dialer *exclusive* is setting `DialForPeerConns = false`
alongside it. Had that gone unnoticed, every peer connection would have had an
unguarded path available while the configuration looked correct, and the leak
would have been invisible.

That is recorded here because it is the exact shape of failure ADR-0001 is
built against: a control that appears configured and is not.

## Consequences

- **Seeding is distribution.** With no recorded obligation the engine seeds
  **indefinitely**, and §13 places the legality of that on the operator.
  `download.seed: false` is the one switch that stops it. Stated in
  [ADR-0014](0014-download-engine.md), in the code, and in the risk table.
- **DHT, uTP and incoming connections are derived from the egress mode**, not
  configured independently — a tunnelled instance that still speaks DHT
  announces itself to the swarm regardless of how its TCP is routed.
- Upstream is one maintainer's project. A real risk, mitigated by the fallback
  being written down.

## Related

- [ADR-0014](0014-download-engine.md) — grabs take a sealed ticket, never a URL.
- [ADR-0001](0001-egress-control-wireguard-netns.md) — why the dialer matters.

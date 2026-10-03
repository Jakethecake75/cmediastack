# ADR-0003: Tracker definitions are consumed from upstream Cardigann YAML, never written per-tracker

**Status:** accepted (Phase 0). **Partially implemented:** the Torznab/Newznab
client is built and tested; the Cardigann definition consumer reads public
trackers' definitions since increment 6h
([ADR-0058](0058-cardigann-public-definitions.md)), and signs in to private ones
since 6i ([ADR-0059](0059-cardigann-sign-in.md)).

## Decision

Where a tracker exposes a **Torznab or Newznab API**, talk to it directly.

Where it does not — which is most private trackers — do **not** write a scraper.
Consume the **Cardigann YAML definitions** that Jackett and Prowlarr already
maintain, as data, at runtime.

Under no circumstances does this repository accumulate per-tracker Go code.

## Why

This is the single largest maintenance sink in the software being replaced.
Prowlarr and Jackett each carry **hundreds** of tracker definitions, and those
definitions break constantly — a tracker changes a CSS class, adds a Cloudflare
rule, renames a category, and the definition needs an edit. That churn is
handled today by a community of people who use those trackers.

Writing scrapers here means inheriting that workload with a maintainer count of
one. It is the clearest example in this project of work that is enormous,
never-ending, and **already done by somebody else**, which is exactly the case
§12 means by "do not reinvent solved problems".

Consuming the definitions as data means a tracker change is fixed by pulling a
new YAML file, not by a release of this software.

## Why the definitions are not vendored

They are fetched or dropped in by the operator, not baked into the binary.

A vendored copy is a snapshot that starts rotting immediately, and it would tie
tracker fixes to this project's release cadence — reintroducing the problem the
decision exists to avoid. It also keeps a list of private-tracker definitions
out of this repository, which is the operator's business and not the software's.

## What is built today

`internal/indexer` speaks **Torznab and Newznab** (`indexer.KindTorznab`,
`indexer.KindNewznab`) with credentials sealed by the master key and never
returned over HTTP, not even to an administrator
([ADR-0018](0018-metadata-and-artwork.md) applies the same rule to the metadata
token).

That covers usenet indexers and any tracker fronted by Prowlarr or Jackett —
which, in a deployment that still runs one of those, is all of them. It does not
yet cover talking to a Cardigann-defined tracker **directly**, which is what
finishing the Prowlarr replacement requires.

*Correction, found in increment 4l: "any tracker fronted by Prowlarr or Jackett"
was true only of a Prowlarr or Jackett on a public address — the indexer egress
profile refused private destinations, so the usual deployment on the same host
or the LAN failed every search. Fixed in increment 4m by
[ADR-0024](0024-indexers-on-the-operators-network.md): the indexer's own
configured address may be private, and nothing else a feed names may be.*

## Consequences

- A Cardigann definition is a **program**, not a config file: it describes login
  flows, selectors and request sequences. Executing one means running
  attacker-adjacent instructions against an HTTP client, and it belongs behind
  the constraints in [ADR-0007](0007-process-boundaries.md) rather than in a
  convenient goroutine.
- The definition format is Jackett's and may change without notice. Accepted:
  the alternative is owning the trackers instead.
- Until the consumer exists, **Prowlarr is not fully replaced**. Recorded in
  PROGRESS.md rather than implied to be done.

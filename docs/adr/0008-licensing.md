# ADR-0008: AGPL-3.0

**Status:** accepted (Phase 0). `LICENSE` is the full GNU Affero General Public
License, version 3.

## Decision

AGPL-3.0, chosen as a **default** rather than a conviction, and reversible while
Jacob is the sole copyright holder.

## Why AGPL rather than GPL or MIT

This is network server software, which is the exact case the Affero clause was
written for. Under GPL-3.0, someone can run a modified copy as a service for
other people and owe nothing; §13 of the AGPL closes that by treating *use over
a network* as the trigger for the source obligation.

For a self-hosted media application that is the realistic form of any
appropriation worth caring about, so GPL-3.0 would carry copyleft's costs
without its benefit here.

MIT was declined because it permits exactly the outcome above and this project's
lineage argues against it: the software it replaces — Jellyfin, Radarr, Sonarr,
Prowlarr — is GPL-family, and much of what makes this feasible at all
(`anacrolix/torrent`, the Cardigann definitions, `ffmpeg`) exists because people
released it under copyleft terms.

## What AGPL does *not* do here

- **It does not restrict Jacob's own use.** Running it, modifying it, and never
  publishing a line is entirely permitted. The obligation attaches to conveying
  it, or to offering it as a service to others.
- **It does not make the instance's content anybody's business.** The licence
  covers the software. §13 puts the legality of what an instance indexes and
  stores on the operator, and no licence changes that.
- **It does not bind users of Jacob's instance.** Friends given accounts are
  users, not recipients of the software.

## Practical obligations this creates

1. **Dependency licences must stay compatible.** Anything Apache-2.0, BSD or MIT
   is fine inbound. A GPL-2.0-only dependency would be a genuine problem, and is
   worth checking before adding rather than after.
2. **Offer the source to remote users.** In practice: a link in the UI or a
   documented location. **Done in 6b** ([ADR-0052](0052-offering-the-source.md)):
   every page links to `server.source_url` with the build's version.
3. **`ffmpeg` is a separate matter.** It is invoked as a subprocess rather than
   linked ([ADR-0007](0007-process-boundaries.md)), which keeps its LGPL/GPL
   build configuration out of this binary's licensing entirely. That separation
   was chosen for security and is convenient here.

## Reversibility

Sole copyright holder, so relicensing is unilateral today. That stops the moment
an outside contribution is merged without a CLA. Worth knowing before the first
pull request, not after.

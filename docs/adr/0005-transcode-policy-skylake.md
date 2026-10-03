# ADR-0005: Transcode policy — direct play first, and the hardware decides

**Status:** accepted (Phase 0), restated in full before Phase 4
**Context:** §13 gives ~5 peak concurrent streams on an Intel i5-6500T, and asks
for the application to stay portable enough to run on almost anything.

## Decision

1. **Direct play is the default and the goal.** Transcoding is a fallback for a
   client that genuinely cannot play a file, never a normalisation step.
2. **What the hardware can do is *probed*, never assumed from a CPU model.**
3. **No ABR ladders.** One output, chosen once per session.
4. **No HDR tone mapping.** HDR content either direct-plays or does not play.

## Why direct play first, on this hardware in particular

An i5-6500T is 4 cores at 2.5 GHz with a 35 W budget. Five concurrent software
transcodes is not a thing it can do, and the arithmetic is not close — so a
design that transcodes by default is a design that fails at the stated load.

Direct play is also the *correct* answer far more often than media servers
behave as though it is. The reason they transcode so eagerly is usually a client
capability report that is pessimistic or absent, not a file that cannot be
played. Treating the fallback as the default costs quality, costs power, and on
this box costs the ability to serve the fifth viewer at all.

## Why the hardware is probed rather than looked up

The obvious implementation is a table: *Skylake, therefore H.264 and 8-bit HEVC
via Quick Sync, no Main10, no VP9 10-bit, no AV1.* That table is roughly right
and is exactly the wrong mechanism.

It is wrong because it is a claim about silicon made by software that is not
looking at the silicon. Skylake's HEVC support in particular is **hybrid** —
part fixed-function, part shader — and what a given kernel, Mesa and driver
stack will actually expose through VAAPI on any given day is not derivable from
the CPU's model number. It is wrong again the first time this runs somewhere
else: Jacob asked for portability, and a lookup table is a list of the machines
somebody thought of.

So the capability set comes from **querying VAAPI at startup** (the same
information `vainfo` prints) and is recorded as what this instance believes it
can do, visible in the UI. An instance with no usable VAAPI device is not
broken; it is an instance whose answer to "can you transcode that" is no, which
is a supported configuration.

This is the same stance the rest of the project already takes — the TMDB client
was verified against the live API rather than against its documentation, and the
egress guarantee is established by the kernel rather than by a code comment. A
capability this software asserts and has not checked is one it will eventually
be wrong about.

### What is expected, and must be confirmed on the actual box

Recorded as an expectation so that a future disagreement with reality is
visible as a disagreement, rather than quietly becoming the new truth:

| Codec | Expected on an i5-6500T | Confidence |
|---|---|---|
| H.264 8-bit decode/encode | yes, fixed function | high |
| HEVC 8-bit decode | yes, hybrid | **medium — verify** |
| HEVC 8-bit encode | yes, hybrid | **medium — verify** |
| HEVC 10-bit (Main10) | **no** — Kaby Lake onwards | high |
| VP9 | partial/hybrid decode at best | low |
| AV1 | no | high |

The 10-bit row is the one that matters, and it is the whole reason for the HDR
decision below.

## Why no HDR tone mapping

4K HDR is, in practice, HEVC Main10. This hardware cannot decode Main10, and
tone mapping in software on four Skylake cores is not a realtime operation for
one stream, let alone alongside four others.

Attempting it produces the worst available outcome: a stream that starts,
stutters, and burns the whole box while doing it — degrading the four sessions
that were working. So 4K HDR **direct-plays or it does not play**, and the
player says which, rather than trying and failing.

This is a real capability gap and it is written down as one. It is not mitigated
by a setting, and the honest fix is different hardware (Kaby Lake or newer for
Main10; anything with a modern iGPU for tone mapping).

## Why no ABR ladders

An adaptive ladder means encoding the same content several times at once. At
five concurrent viewers on four cores, a ladder converts a working instance into
a broken one, and it exists to solve a problem this deployment does not have:
viewers on a LAN, or on one home uplink, whose bandwidth does not vary the way a
mobile network's does.

One output per session, chosen when the session starts. If it is wrong, the
viewer changes it and the session restarts — which is a worse experience than a
ladder, on a box where a ladder is not available at any price.

## Consequences

- **Accepted:** an unplayable file is sometimes a *refusal with a reason* rather
  than a slow stream. This is deliberate, and the reason must say which
  capability was missing — "your browser cannot play HEVC" is actionable;
  "playback failed" is not.
- **Accepted:** clients that under-report capabilities will direct-play less
  often than they could. Better than the reverse.
- **Rejected:** transcode-by-default with a quality ladder, i.e. what Plex and
  Jellyfin do out of the box. It is the right default on a machine with headroom
  and the wrong one here, and pretending otherwise would produce software that
  demos well and fails at five viewers.
- **Unbuilt at time of writing.** Nothing in the tree shells out to `ffmpeg`
  yet; Phase 4 is greenfield against this policy. `grep -rn ffmpeg internal/`
  returning nothing is the current state, not an oversight.

## Related

- [ADR-0006](0006-no-jellyfin-shim.md) — no client-compatibility shim, so the
  capability negotiation is with a browser this project controls.
- [ADR-0007](0007-process-boundaries.md) — `ffmpeg` is a hostile-input executor
  and does not run inside the application process.

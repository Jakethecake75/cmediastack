# Estimates

What this costs, how the figure was reached, and how it has held up.

The purpose of this document is not precision. It is to make the schedule risk
legible, and to explain why the phases are ordered so that **stopping at any
gate leaves you better off than you are today** — which is the only real defence
against an estimate being wrong.

## The headline

**1.8–3.5 person-years** to the parity specified in §13 — one person, working
conventionally, replacing Radarr, Sonarr, Lidarr, Readarr, Bazarr, Prowlarr,
qBittorrent, Jellyseerr and Jellyfin at 80–90%.

That range is wide on purpose. The low end assumes nothing surprising in release
parsing or playback; the high end assumes both are as bad as they usually are.

### Where the number comes from

It is a bottom-up sum, not a comparison to the projects being replaced — those
represent far more than 3.5 person-years of accumulated work, but most of that
is the long tail of trackers, clients and edge cases that 80–90% parity
explicitly declines.

| Area | Range | What dominates |
|---|---|---|
| Foundation: identity, authz, config, audit, sessions, MFA | 0.25–0.4 | doing it properly rather than at all |
| Acquisition: indexers, search, quality, download engine, egress | 0.35–0.6 | release parsing, and proving the egress guarantee |
| Library: paths, import, scan, trash, metadata, identification | 0.35–0.6 | identification judgement; filesystem containment |
| **Playback: streaming, transcode, players, subtitles** | **0.4–0.9** | the widest band, and the least de-risked |
| Music and books (Lidarr, Readarr) | 0.3–0.7 | a second identification model, not a fourth media kind |
| Migration importers, polish, operations, docs | 0.15–0.3 | |

Playback is the widest band because it is the only area where the *hardware*
can make a correct implementation fail, and because client behaviour is
discovered rather than specified.

## Progress against it

Measured, not asserted — `PROGRESS.md` records what each increment found, and
the figures below are counted from the tree.

| | |
|---|---|
| Application code | ~47,700 lines of Go, and ~5,800 of hand-written HTML, CSS and JavaScript |
| Tests | ~46,000 lines |
| Test functions | 1,468 |
| Migrations | 37 |
| HTTP routes | 151 (15 anonymous; none answer 501) |
| Decision records | 67 (numbered 0001–0067, 0009 unwritten, 0003a) |

*Counted 2026-10-04, at increment 7c. At 4y it was 1,258 tests, 129 routes and 33 records; at 4u it was 1,143 tests, 122 routes and
29 records. Before that, the figures — 679 tests, 103
routes, 19 records, "Phases 4–5 not started" — were left standing for a whole
phase after they stopped being true, which is the failure this section exists to
avoid.*

Phases 0–3 are substantially complete: identity and the security spine,
acquisition end to end, and the library through identification. **Phase 4,
playback, is substantially complete too** — direct play with seeking and resume,
a remux for files a browser cannot decode, subtitles — and cheaper than its
band, because ADR-0005 decided on this hardware not to transcode video at all.
Along the way came work the phases did not name: episode tracking, adding titles
before they are on disk, requests that end in the library, encrypted backups,
and the operator runbook.

What remains of the stated scope, in the order it would matter:

- ~~**Automatic search**~~ — built in 4v (ADR-0030): the indexers' recent
  releases and a budgeted search of the Wanted list, off until the operator
  turns it on. ~~Season packs~~ — built in 4y (ADR-0033): a person's season
  search, and a machine's pack for a settled season it wants all of. What it
  does not do yet is in those records' limitations. ~~A profile per title~~ —
  4aa (ADR-0035). ~~Upgrades to a profile's cutoff~~ — 4ab (ADR-0036), off
  until chosen. ~~A download that stalls~~ — given up in 4z
  (ADR-0034) when automatic acquisition grabbed it, reported when a person did.
- ~~**Notifications**~~ — built in 4x (ADR-0032): one Discord webhook, fed from
  the audit log and the scheduler. ~~The audit-log screen~~ — built in 4w
  (ADR-0031), with a ceiling on the denials any stranger could make it write.
- ~~**Per-library visibility**~~ — built in 4ac (ADR-0037): a library is a root
  folder, with a rating ceiling from TMDB's US certifications.
- ~~**The routes that answered 501**~~ — none left — each to be built, or removed by a
  record where a built route already does its job. 4ad removed four (HLS play
  sessions) and built three (search, artwork, original download); 4ae built
  five (account administration); 4af four (operations); 4ag the two feeds; 4ah issues; 4ai Discover.
- ~~**Phase 5, music and books**~~ — complete. 5a follows artists from MusicBrainz
  (ADR-0044), 5b finds and imports their files (ADR-0045), 5c searches for,
  grabs and imports an album (ADR-0046), 5d fetches wanted albums automatically
  (ADR-0047), 5e adds books from Open Library (ADR-0048), 5f gives a book its file —
  scanned, searched for, grabbed, imported (ADR-0049), 5g fetches wanted books
  automatically (ADR-0050).

Roughly: **every item of the stated scope is built.** What is left is the
list of known gaps in [DROPPED-FEATURES.md](DROPPED-FEATURES.md) — subtitle
fetching, Cardigann indexer definitions, multi-season packs, master-key
rotation and the rest, each with the reason it was not done, now being worked
through as Phase 6 (6a: the breach check and signup proof-of-work; 6b: the source offer; 6c: deleting one file, purging now; 6d: rotating the master key; 6e: fetching a subtitle; 6f: the subtitle sweep; 6g: packs of several seasons; 6h: Cardigann definitions; 6i: signing in to them; 6j: the details page; 6k: following new seasons; 6l: album upgrades; 6m: season folders; 6n: daily series; and Phase 7, 7a: a SOCKS5 proxy from the web; 7b: v0.1.1; 7c: v0.1.2) — and the time
review and use take, which the next section is about.

### Has the estimate held?

Mostly, with one consistent pattern: **the code is never what takes the time —
finding out that the code is wrong is.**

The defects that cost real time in phases 1–3 were not difficult to fix once
seen. They were difficult to *see*: a `guardLastAdmin` wired into one call site
and not its neighbour; `AddDialer` appending rather than replacing, so a leak
was available while the configuration looked right; every client-side validation
message reporting "could not reach the server"; `url.ResolveReference` silently
dropping a path prefix; nine ADR links pointing at files that did not exist.

Each was found by running the thing — against a live API, a real browser, a
compiled binary — rather than by writing more unit tests. That is the activity
the estimate is really measuring, and it does not compress.

### The assumption worth naming

A **person**-year figure assumes a person writing the code. This build is not
being done that way, and the writing is genuinely much faster.

That does not divide the schedule by the same factor, because two things do not
speed up:

1. **Review.** Jacob is the only reviewer, and code he has not understood is
   code he cannot operate or repair. Generated faster, it still has to be read.
2. **Discovery.** Running it, noticing the poster is stretched, noticing the
   library still says `the matrix` — that is wall-clock time with a human in it.

So the useful reading is: the *writing* is no longer the bottleneck; **review
and discovery are**, and they are the two things this project's structural
tests, mutation testing and live verification exist to make cheaper.

## Phase order, and why it is this one

Each gate is chosen so that abandoning the project immediately afterwards still
leaves something better than the status quo.

| Phase | Gate | Value if you stop here |
|---|---|---|
| 0 — Design | ADRs, threat model, data model | the decisions are written down and re-usable |
| 1 — Foundation | login, MFA, approval, audit, RBAC | one authenticated front door — the *arr stack's worst property is that it has nine |
| 2 — Acquisition | indexers, search, downloads, egress | replaces Prowlarr + qBittorrent, **with a leak guarantee neither has** |
| 3 — Library | import, scan, trash, metadata, identification | replaces Radarr + Sonarr for films and series |
| 4 — Playback | streaming and the web player | replaces Jellyfin; the stack is now one service |
| 5 — Music/books | Lidarr, Readarr | full stated scope |

Phase 2 is deliberately early: the egress guarantee is the one capability the
existing stack cannot be configured into having, so it is the earliest point at
which this project is not merely a consolidation.

Phase 4 is deliberately late, despite being the most visible, for two reasons.
It is the widest estimate band, so a project that starts there learns least per
week. And a media server with nothing in its library is not testable — playback
needs phase 3's output to be exercised at all.

## What would move the numbers

**Upward:**

- Release-name parsing turning out worse than phase 2 suggested. It is the
  classic underestimate in this domain and it is still the most likely overrun.
- Browser playback behaviour — codec support, seeking, subtitle rendering —
  being discovered one client at a time.
- Any decision to revisit the Jellyfin shim. It would not be a feature; it would
  be a second project.

**Downward:**

- Dropping music and books explicitly rather than carrying them as scope. That
  is 0.3–0.7 of the range for a capability Jacob has not yet said he uses.
- Accepting a smaller playback surface for v1 — direct play only, transcoding
  deferred — which the hardware already pushes toward
  ([ADR-0005](adr/0005-transcode-policy-skylake.md)).

**Neither:**

- Buying newer hardware. It fixes 4K HDR, which is a capability gap, not a
  schedule one.

## The honest summary

The technology is not the risk. Every hard technical question so far has had a
findable answer, and the ones that bit hardest were found by running the
software rather than by reasoning about it.

The risk is **duration**: whether a single maintainer stays interested long
enough to finish, and then keeps operating it afterwards. The phase order is the
mitigation, and it is the reason each gate is a working replacement for
something rather than a layer of an unfinished one.

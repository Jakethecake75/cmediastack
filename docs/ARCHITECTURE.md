# Architecture

One process, one database, one configuration file, one authorization model.
This document says how the inside is arranged and — more usefully — which
arrangements are refused and why.

For the route table see [API-SURFACE.md](API-SURFACE.md); for the schema see
[DATA-MODEL.md](DATA-MODEL.md). Both are generated from the tree, so they cannot
describe software that is not there.

## Process roles

The binary is one file and selects its role with `--role`:

| Role | What it is | Why separate |
|---|---|---|
| `app` (default) | HTTP surface, library, metadata, scheduling | holds the master key, the database and the session material |
| `downloader` | the BitTorrent engine, and nothing else | runs in a network namespace; parses peer traffic |

`--role downloader` **refuses to start** unless it can prove outbound traffic
leaves through the tunnel — verified before the engine is constructed, so there
is no window in which an unprotected downloader runs
([ADR-0001](adr/0001-egress-control-wireguard-netns.md)).

A third role, the media executor, arrives with Phase 4 for the same reason:
`ffmpeg` parses hostile input and does not belong in the address space holding
the keys ([ADR-0007](adr/0007-process-boundaries.md)).

There is also `--recover`, which is not a role but a **console-only** path for
break-glass account recovery. `identity.TestOnlyTheCommandLineCanRecoverAnAccount`
reads the source and fails the build if it becomes reachable from HTTP.

## Modules, and the direction dependencies run

A modular monolith: typed interfaces between packages, **never HTTP between
modules**. The dependency graph is acyclic and flows one way.

```
                       cmd/cmediastack        ← wiring; the only place that knows
                              │                  how the parts fit together
                      internal/api            ← HTTP; owns no domain logic
                              │
   ┌──────────┬───────────────┼───────────────┬──────────────┐
   │          │               │               │              │
identity   importer        identify        search         download
   │          │  ╲             │              │               │
   │       library  artwork    │           indexer            │
   │          │       │     metadata          │               │
   │          └───────┴────────┴──────────────┴───────► release
   │                                                          │
   └──────────────────────► authz ◄──────────────────────── egress
                              │
                       platform/{db, audit, config, logging, metrics,
                                 secrets, tasks}
```

Two rules hold this shape:

**`internal/api` owns no domain logic.** Handlers parse a request, call one
method, and render the answer. Every rule that matters — what may be deleted,
what may be renamed, what may leave the machine — lives in the package that owns
the thing, so there is exactly one place to look.

**Everything depends on `authz`; `authz` depends on nothing.** It is the bottom
of the graph because a permission check that could be circular would be a
permission check that could be bypassed.

**There is no event bus.** The Phase 0 brief asked for typed interfaces *and* an
internal event bus. The interfaces are there; the bus was never built, because
nothing has yet wanted to notify a listener it does not already hold a reference
to. It is not missing so much as unneeded, and adding one before there is a
second subscriber would be adding indirection to a direct call. Recorded here
rather than quietly dropped.

## The four cross-cutting guarantees

These are not layers to pass through. They are properties enforced where the
effect happens, which is why they survive a caller that forgets them.

### 1. Nothing is anonymous

Twelve routes out of 103 are reachable without a session — login, signup, the
first-run wizard, password reset, `/healthz` and the anonymous asset bundle.

Every one also appears in `api.AnonymousAllowlist`, and `Router.register`
**panics at startup** if the two disagree in either direction. A developer
cannot make a route anonymous by asking; it has to be a reviewable line in a
diff.

Thirty-four routes are **hidden**: 404 rather than 403 when unauthorized, so
their existence is not disclosed. Fourteen are **session-only** — an API token
may not use them whatever its scope, because they manage credentials.

### 2. Authorization is over effects, checked where the effect occurs

Permissions are not checked at the route and trusted thereafter. `library.delete`
is checked in the filesystem layer, at the moment a file is unlinked — so a new
caller reaching that code path is checked too, whether or not its author thought
about it.

Background work gets a **system principal minted per task**, with its own grant:

| Task | Grant |
|---|---|
| Import | browse, root folders — *not* delete |
| Library scan | browse only — a scan changes nothing on disk |
| Trash purge | browse, delete — *not* path mutation |
| Identification | browse only — *not* edit library items |
| Playback history purge | browse only |
| Episode refresh | browse only — *not* edit library items, so it cannot switch a season the operator turned off back on |

Each is smaller than a shared "system" role would be, and the last one is the
clearest case: withholding `library.edit` from the identification pass is
stronger than intending not to use it
([ADR-0019](adr/0019-identification.md)).

`authz.TestOnlySchedulingCodeCanMintASystemPrincipal` reads every package's
source and fails the build if a system principal is minted outside the short
allowlist that registers scheduled tasks. A runtime check could not prove that.

### 3. Paths are contained by the kernel

Every filesystem root — library folders, the download directory, the artwork
cache — is an `os.Root`, which is `openat2(RESOLVE_BENEATH)`. Containment is
enforced by the kernel at the moment of use, per component, not by joining
strings and checking a prefix.

This matters most where a **third party supplies the string**: a metadata
provider's poster path is validated to a plain filename *and* written through a
root that cannot be escaped. Both checks were disabled deliberately to confirm
the kernel still refused the traversal
([ADR-0018](adr/0018-metadata-and-artwork.md)).

`library.TestNothingWritesOutsideAVault` fails the build if a package reaches
the filesystem without going through one.

### 4. Outbound traffic leaves where it is supposed to

`internal/egress` holds a per-subsystem transport policy, and
`egress.TestNoPackageDialsDirectly` reads the source to prove nothing dials
around it. The kernel-level namespace is primary; the SOCKS5 proxy is defence in
depth ([ADR-0013](adr/0013-egress-guard-design.md)).

## Request flow

```
  global middleware      panic recovery → request id → client IP → security headers
        │                (wraps the mux: runs before routing)
     routing
        │
  per-route middleware   rate limit → CSRF → authenticate → allowlist gate
        │                (runs after routing: acts on the matched route's
        │                 identity, not on a URL it re-parses)
     handler             parse, call one domain method, render
        │
     domain              the rules live here; authz checked at the effect
        │
     platform/db         the only place, with the repositories, that builds SQL
```

The split is a security property. Middleware that authenticated by re-parsing
the URL would be deciding about a *different* route than the one that will run.

## The web UI

Server-rendered shells with hand-written JavaScript and **no build step**
([ADR-0011](adr/0011-server-rendered-shells-no-build-step.md)) — overruling the
Phase 0 assumption of React and Vite. There is no `node_modules`, no bundler,
and no lockfile to audit; the assets served are the assets in the repository.

Structural tests keep it honest: no inline script or style survives
`web.TestTemplatesContainNothingInline`, no script builds markup from strings,
and every navigation entry, `data-view` section and loader must agree or the
build fails.

## What is deliberately not here

- **No Redis, no message broker.** The job queue is in-process with
  DB-persisted records.
- **No Jellyfin API shim** ([ADR-0006](adr/0006-no-jellyfin-shim.md)).
- **No telemetry and no phone-home.** The only outbound calls are to indexers
  and to the configured metadata provider, both operator-supplied.
- **No Postgres portability layer**
  ([ADR-0004](adr/0004-sqlite-only-v1.md)) — only `platform/db` and the
  repositories build SQL, which bounds a future port without pretending it is
  free.

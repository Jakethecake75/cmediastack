# ADR-0007: Hostile-input executors do not run in the application process

**Status:** accepted (Phase 0); partially implemented (the `downloader` role
exists and refuses to start); the media executors arrive with Phase 4.

## Decision

Code that parses attacker-controlled bytes with a large C codebase runs in a
**separate process** with its own reduced privileges — never inside the
application process, and never as a goroutine holding the application's
database handle, session keys and master key.

Three such executors are recognised:

| Executor | Input it parses | Status |
|---|---|---|
| The **download engine** | peer traffic, torrent metadata | built — `--role downloader` |
| **`ffmpeg` / `ffprobe`** | every video file in the library | Phase 4 |
| **Archive extraction** | `.rar`, `.zip` from a download | not built, and refused on purpose |

## Why

The application process holds everything worth stealing: the master key that
seals indexer credentials and the metadata token, the session signing material,
an open handle to the database with every account in it, and — through
`os.Root` handles — write access into the library.

`ffmpeg` is the clearest case. It is a magnificent piece of software and it is
also millions of lines of C whose entire job is to parse untrusted container and
codec data, with a long history of memory-safety CVEs in exactly that code. Its
input is not merely untrusted in theory: this application *downloads files from
strangers on the internet* and then points a parser at them. That is the threat
model, stated without euphemism.

A Go program is not made memory-safe by being written in Go if it links a C
parser into its address space. The boundary has to be a real one — a process,
with its own user, its own filesystem view, and no reason to be able to open the
database.

## What this rules out, and what it costs

**Ruled out: cgo-linked media libraries.** Binding libav* into the binary would
be faster and simpler and would put the CVE inside the process holding the
master key. This is also why [ADR-0002](0002-go-no-cgo.md)'s `CGO_ENABLED=0` is
described there as load-bearing rather than a preference: the two decisions
support each other, and the Dockerfile asserts it at build time rather than
trusting it.

**Ruled out: unpacking archives.** `internal/importer/select.go` deliberately
does not recognise `.rar`, `.zip`, `.7z` or `.001`. Every other media manager
unpacks them, because usenet and some trackers still ship that way. Doing so
means running a decompressor over attacker-controlled input, and — worse —
writing whatever it produces into a library path. Until there is a sandboxed
executor to do it in, the honest behaviour is to skip the file with a reason.
That is a real feature gap, recorded in
[DROPPED-FEATURES.md](../DROPPED-FEATURES.md).

**The cost is coordination.** A separate process needs a way to be given work
and to report back, needs its lifetime managed, and turns some in-memory
function calls into IPC. That is the price, and it is paid once per executor
rather than once per feature.

## How the boundary is actually established

In order of strength, the same order [ADR-0013](0013-egress-guard-design.md)
uses for egress — because the principle is identical and so is the mistake it
avoids:

1. **The kernel.** A different user, a read-only view of everything but the one
   directory it needs, no network where none is required, and seccomp narrowing
   the syscalls available. This is the layer that holds when the others are
   wrong.
2. **A structural test.** The application packages must not import a media
   parsing library, and a test reads the source and fails the build if one
   appears — the same mechanism as `egress.TestNoPackageDialsDirectly`.
3. **The process boundary in code.** `--role` selects what a process is, and the
   roles do not share a wiring function.

Layers 1 and 3 exist today for the downloader. **Layer 2 does not yet exist for
media**, because nothing in the tree parses media yet; it must be written in
Phase 4 alongside the first `ffmpeg` call, not after it.

## The seccomp gap, and what closing it cost

This section previously read: *"the container's seccomp profile is still
`unconfined`… it should be tightened before the ffmpeg executor ships."* Doing
that turned out to conflict with the executor itself.

Docker's **default** seccomp profile permits `clone` only when no namespace flag
is set — its rule is `(arg0 & 0x7E020000) == 0`, and `0x7E020000` is the sum of
all seven `CLONE_NEW*` flags. The media-parser jail asks for three of them. So
adopting the stock profile would have **silently disabled the jail**: the
container would start, `NewSandbox` would find the clone refused, and ffmpeg
would parse hostile files with no network isolation at all. A generic control
traded for a specific one, in the wrong direction, visible only in a log line.

`deploy/seccomp-cmediastack.json` is therefore Docker's default plus **one**
allowance, scoped to exactly `CLONE_NEWUSER|CLONE_NEWPID|CLONE_NEWNET`.
`CLONE_NEWNS`, `CLONE_NEWUTS`, `CLONE_NEWIPC` and `CLONE_NEWCGROUP` stay denied,
as do `setns`, `unshare` and `clone3` — so `clone` is the only route to a
namespace and only to those three.

The residual is written down in [SECURITY.md](../../SECURITY.md): permitting
unprivileged user namespaces re-opens kernel surface with a real CVE history.
The trade is that the process reaching it is the application, rather than a
compromised `ffmpeg` with an open network.

`playback.TestTheShippedSeccompProfilePermitsTheMediaSandbox` and
`playback.TestTheSeccompAllowanceIsNarrowerThanEveryNamespace` evaluate the
shipped file the way the kernel would, so the profile cannot drift from what the
sandbox needs in either direction.

## Related

- [ADR-0001](0001-egress-control-wireguard-netns.md) — the downloader's other
  reason to be a separate process: its network namespace.
- [ADR-0002](0002-go-no-cgo.md) — a pure-Go binary, asserted at build time.
- [ADR-0005](0005-transcode-policy-skylake.md) — what the media executor is
  permitted to attempt.

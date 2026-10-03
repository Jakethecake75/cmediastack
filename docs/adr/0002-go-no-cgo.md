# ADR-0002: Go, with `CGO_ENABLED=0` treated as a constraint rather than a flag

**Status:** accepted (Phase 0), asserted at build time.

## Decision

Go 1.26, built with `CGO_ENABLED=0`. Every dependency must work without cgo, and
the build **fails** if a cgo-linked binary is produced.

Jacob's stated options were Go, Rust, or .NET 8. Go was chosen for the standard
library's HTTP and crypto surface, for `os.Root` (see below), and because a
static binary with no runtime to install is what "portable enough to run on
almost anything" actually means.

## Why the flag is load-bearing

`CGO_ENABLED=0` is usually a packaging convenience. Here it decides three
things:

**It makes the binary genuinely static.** No glibc version to match, no
`musl`-vs-`glibc` container surprise, no runtime. One file, `scratch` base
image, runs on whatever the host is.

**It keeps C parsers out of the address space holding the master key.** This is
[ADR-0007](0007-process-boundaries.md)'s argument, and the flag is what enforces
it mechanically rather than by review. A media library that needs cgo cannot be
added by accident, because the build stops.

**It forces the SQLite decision.** `modernc.org/sqlite` is a pure-Go
translation, not a binding, so [ADR-0004](0004-sqlite-only-v1.md) follows from
this one. It is slower than `mattn/go-sqlite3`, which is irrelevant at ≤25 users
and ≤100k items, and it is the price of the two properties above.

## How it is enforced

Not by convention. `Makefile` sets it on every build, and the `Dockerfile`
verifies the *produced artefact* rather than trusting the environment:

```
RUN test -z "$(go version -m /out/cmediastack | grep -F 'CGO_ENABLED=1')" || \
    (echo "refusing: binary was built with cgo" && exit 1)
```

The distinction matters. Checking the variable proves what was asked for;
reading the build info out of the finished binary proves what happened.

## Consequences

- **`anacrolix/torrent` must not link `go-libutp`.** Verified in increment 2e:
  it builds with `CGO_ENABLED=0` with the C uTP implementation unlinked, using
  the pure-Go path instead. See [ADR-0003a](0003a-torrent-engine.md).
- **No cgo-linked image, video or archive libraries**, ever, in the application
  process. Where that work is genuinely needed it happens in a separate process
  (ADR-0007) — which is why artwork type detection in `internal/artwork` sniffs
  magic bytes in Go rather than asking a decoder.
- **`os.Root` is available**, which is the single largest security dependency in
  the tree: [ADR-0015](0015-library-path-containment.md) relies on
  `openat2(RESOLVE_BENEATH)` for kernel-enforced path containment, and a
  Join-and-prefix-check implementation would be a strictly weaker design.

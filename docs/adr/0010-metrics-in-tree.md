# ADR-0010 — Prometheus exposition written in-tree, not `prometheus/client_golang`

**Status:** Proposed
**Date:** 2026-09-10

## Context

Requirements §9 asks for a Prometheus `/metrics` endpoint. The obvious
implementation is `prometheus/client_golang`, and §12 says to wrap mature
libraries rather than reinvent solved problems.

Three things pushed the other way.

**The build environment cannot verify the dependency.** This container's egress
policy blocks `sum.golang.org` (403 on CONNECT); `proxy.golang.org` is
reachable, so modules download but their checksums cannot be checked against the
transparency log. Adding the dependency would mean committing a `go.sum` whose
entries nobody verified, in a project whose SECURITY.md promises "pinned
dependencies with lockfiles" and whose CI advertises a dependency audit. That
promise would be false, and quietly so.

**The dependency is large relative to the need.** The client pulls protobuf,
`prometheus/common` and `procfs` into an application whose threat model names a
compromised dependency as attacker persona P5.

**The need is small.** Phase 1 exposes counters, gauges and fixed-bucket
histograms in a stable text format. That is a serialiser and some atomics, not a
protocol implementation.

## Decision

Implement the Prometheus text exposition format (version 0.0.4) in
`internal/platform/metrics`. Provide `Counter`, `Gauge`, `Histogram` and their
labelled vector forms, plus a registry and an HTTP handler.

This is the same judgement that put TOTP in-tree: the "don't reinvent" rule
targets genuinely hard, high-risk problems — BitTorrent wire protocols, video
transcoding — not a text serialiser whose correctness is fully verifiable by
test.

## Consequences

**Good**

- Zero new dependencies, and no unverifiable `go.sum` entries.
- The output is pinned by `metrics_test.go`, including cumulative bucket
  semantics, label escaping and stable ordering between scrapes.
- Two hazards specific to metrics are handled explicitly and tested. Label
  values are escaped, so a hostile indexer name cannot inject a fabricated
  metric line. A wrong label count returns a throwaway collector rather than
  panicking, because a metrics bug must never break a request path.
- HTTP series are labelled by route **pattern**, never raw path. Labelling by
  path would let any caller create unbounded time series with `/media/1`,
  `/2`, `/3` — a denial of service against the monitoring system rather than
  the application.

**Bad**

- Exemplars, native histograms, the protobuf negotiation path and the
  `promhttp` middleware ecosystem are unavailable. None are needed today.
- If Prometheus changes the text format, this follows by hand. The format has
  been stable for a decade.
- Roughly 400 lines to maintain that a library would have provided.

**Reversal is cheap, deliberately.** Only this package's internals would change;
`metrics_test.go` documents the output that must not move. If the
checksum-verification problem goes away and you would rather have the upstream
client, swapping it in is a contained edit.

## Related

If CI gains a step that re-derives `go.sum` against the live transparency log
and fails on any difference, the first of the three reasons above disappears.
That is worth doing regardless, for every dependency the project already has.

---

## Addendum, 2026-09-12 — the constraint has lifted

`sum.golang.org` became reachable from the build environment. The premise above
is no longer true, so the decision is worth re-stating rather than left to look
like a standing preference:

**The in-tree exposition format stays.** It is written, tested, has no
dependencies, and a Prometheus scraper cannot tell the difference. Replacing
working tested code with a dependency now would be motion, not progress. If the
metric surface ever needs summaries, exemplars or native histograms, that is the
moment to revisit — not before.

**Two dependencies were taken once verification became possible**, both of which
this ADR's reasoning had been blocking:

- `github.com/skip2/go-qrcode` — the enrollment QR code. This was the flagged
  gap in [ADR-0011](0011-server-rendered-shells-no-build-step.md): manual entry
  of a 32-character base32 key is the highest-friction moment in the product and
  every account hits it.
- `github.com/makiuchi-d/gozxing` — **test only**, and it earns its place by
  being an *independent* implementation. `TestEnrollmentQRDecodesToTheRealOTPAuthURI`
  decodes the image the server actually produced and asserts it carries the same
  otpauth URI the manual path hands out. An encoder agreeing with itself proves
  nothing; a QR that renders but encodes the wrong seed is worse than no QR,
  because the user scans it and enrollment fails with a code that looks right.
  `go tool nm` confirms it contributes zero symbols to the shipped binary.

The follow-up this ADR asked for — a CI step that re-derives `go.sum` against the
live transparency log and fails on any difference — is now buildable and still
worth doing.

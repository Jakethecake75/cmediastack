# ADR-0013 — How the egress guarantee is actually established

**Status:** Accepted · 2026-09-12 · Implements [ADR-0001](0001-egress-control-wireguard-netns.md)

## Context

Requirements §2: the download engine must never egress outside its configured
tunnel, and if the tunnel is unreachable, downloads pause — they never fall back
to a direct connection.

That is a claim about what **cannot** happen. A check consulted before each dial
does not establish it. The failure mode that matters is not a check returning
the wrong answer; it is a code path that never consults the check at all — an
`http.Get` in an indexer client, a `net.Dial` in a tracker announce, a library
that builds its own transport. Every one of those is a single line, written by
somebody who was thinking about something else, and it is invisible in review
because it looks exactly like ordinary Go.

So the design question is not "where do we check" but "what makes the unchecked
path impossible".

## Decision

Three layers, in descending order of how much weight they carry.

### 1. The kernel (ADR-0001) — the layer the promise rests on

The downloader runs inside a network namespace whose only route out is a
WireGuard interface, behind a default-deny firewall it holds no capability to
modify. **If `internal/egress` were deleted entirely, a direct connection would
still be impossible, because there is no route for one.**

`egress.Verify` proves the process is in that namespace before the engine
starts, and `runDownloader` **exits non-zero** if it cannot. A downloader that
comes up unprotected has already broken the promise, and it breaks it silently —
the first evidence would be an infringement notice. Exiting loudly is the only
failure an operator can act on.

The check is behavioural, not a file parse. The obvious test — comparing
`/proc/self/ns/net` against `/proc/1/ns/net` — is **wrong for this deployment**:
the downloader container joins the WireGuard container's namespace, so PID 1
shares it and the links are identical. That check would report "not jailed" for
a correctly jailed process, and an operator who trusted it would disable the
guard. Instead, `Verify` asks the kernel which source address it would use to
reach a public destination and finds the interface that owns it. A UDP
"connection" performs route selection without sending a packet, so it costs
nothing, reaches nothing, and works with the tunnel down.

### 2. A structural test — what stops the unchecked path being written

`TestNoPackageDialsDirectly` reads the source of every package on the
acquisition path and fails the build on `net.Dial`, `http.Get`, `http.Post`,
`http.DefaultClient` and `http.DefaultTransport`. The directory list is written
out by name, so adding a package to the acquisition path is a deliberate act.

This is a tripwire rather than a wall — it is defeated by an alias or a
reflective call — but the failure it catches is not adversarial. It is a
teammate, or me in four months, reaching for the obvious API.

### 3. The guard — defence in depth, and the pause behaviour

`Guard.DialContext` returns `ErrEgressUnavailable` when the tunnel is not
verified healthy, and returns **no connection**. There is no fallback branch in
the package: not a direct dial, not a retry without the proxy.
`TestNoFallbackWhenUnhealthy` asserts this by exhausting every mode —
direct, socks5, http-proxy, blocked, and an unrecognised one — against a
listener that *would* have accepted, so a fallback shows up as a returned
connection rather than as an unrelated error.

Four decisions inside it are worth naming:

**Health starts DOWN.** Nothing dials until a probe has actually succeeded. An
unset gauge and a healthy gauge look identical on a dashboard.

**An unconfigured subsystem is blocked, not direct.** `Guard.For` returns a
`Dialer`, never `(Dialer, error)` — a caller who ignores an error gets a working
direct dialer, whereas a caller who ignores this gets one that cannot connect.
Failure has to be the easy path.

**The probe is the one exemption from the gate, and it is narrow.** Without it
the gate is a trap: once health drops, every dial fails including the probe that
would notice recovery, and only a restart would reopen it. `ProbeDialer` skips
the health gate and **nothing else** — the profile applies in full, a blocked
profile still refuses. `TestOnlyTheProbeBypassesTheGate` pins the caller count
at one.

**`notification` is the only profile exempt from the kill switch by default.** A
kill switch that also silences the alert telling you it fired is one you find
out about from your library being empty. Metadata is deliberately *not* exempt:
a TMDB query reveals what the operator is interested in.

## Consequences

**SOCKS5 is written in-tree** (RFC 1928, with RFC 1929 authentication), for the
same reason as the metrics exposition format: this build environment cannot
reach `sum.golang.org`, so a dependency's checksums could not be verified
against the transparency log, and SECURITY.md promises pinned and verified
dependencies. See ADR-0010. It is tested against a server written independently
from the same RFC — a mock mirroring the client's own assumptions would prove
nothing — including that a hostname is sent as a DOMAIN address rather than
resolved locally, which is the entire point of socks5h.

**`PATCH /api/v1/admin/egress` refuses permanently, with 409.** A runtime switch
for the control that prevents leaks is the single most valuable thing an
attacker with an admin session could flip, and it would leave no artefact beyond
an audit line. Keeping the policy in the config file means changing it needs
filesystem access and a restart, it is reviewable in a diff, and the security
lint gets to refuse an unsafe combination before the process starts. 409 and not
501: the endpoint is not unfinished, it is closed.

**The leak test does not prove the absence of a leak, and says so in its own
response body.** Nothing running inside the jail can prove that — a leak by
definition takes a path the process cannot observe. What it proves is that
routing matches the configuration. The response carries that caveat as a field,
because an operator who reads "leak test: passed" and concludes more than it
established is worse off than one who never ran it.

**SSRF filtering is per-profile and opt-in via `DenyPrivate`**, set on the four
profiles that dial addresses derived from hostile input (download, indexer,
metadata, subtitle) and not on the ones the operator controls end to end — an
operator pointing a notification webhook at a box on their own LAN is doing
something reasonable. All resolved addresses must pass, not merely one: a name
resolving to both a public and a loopback address is a rebind attempt, and
picking the public one would be choosing not to notice. This does not defeat a
DNS rebind between the check and the dial, and it does not try to: the
namespace's firewall has no route to the host network at all.

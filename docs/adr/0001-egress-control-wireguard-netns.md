# ADR-0001: Egress control by network namespace, with SOCKS5 as defence in depth

**Status:** accepted (Phase 0). The application-level half is built and tested;
the namespace itself is deployment configuration, and the process **refuses to
start** without evidence of it.

## Decision

The download engine's traffic is confined **by the kernel**, not by the
application: it runs in a network namespace whose only route out is a WireGuard
interface, with a firewall that drops anything else.

The application-level SOCKS5 proxy (NordVPN, per §13) is **secondary** — a
second layer that catches a misconfigured namespace, not the primary control.

When the tunnel is unreachable, transfers **pause**. They never fall back to
direct. §2 states this as a non-negotiable, and there is no setting that relaxes
it.

## Why the namespace is primary and the proxy is not

A proxy is a thing the application must remember to use. Every dial is a chance
to forget one, every new dependency is a chance for *it* to forget, and the
failure is silent — traffic goes out the default route and nothing looks wrong.
`internal/egress` therefore carries a structural test
(`egress.TestNoPackageDialsDirectly`) that reads the source and fails the build
when a package dials without going through the guard, precisely because this
class of mistake is invisible at runtime.

A namespace is a thing the application **cannot** forget, because the kernel
does the forgetting for it. A socket in a namespace with no route to the
internet does not reach the internet, regardless of what the program intended.

[ADR-0013](0013-egress-guard-design.md) states the general form of this: kernel
first, structural test second, guard code third. This ADR is where that ordering
was chosen.

## Why the process refuses to start

`runDownloader` verifies, before constructing the engine, that outbound traffic
would actually leave by the expected interface — by asking the kernel which
route it would use, not by checking that an interface named `wg0` exists.

If it cannot prove that, it exits non-zero.

This ordering matters and is commented in the code: **the jail exists before the
thing it contains**, so there is no window in which an unprotected downloader
runs. And refusing loudly is the point — a downloader that comes up unprotected
has already broken §2, silently, and the first evidence would be an infringement
notice. An operator can respond to a process that will not start.

Turning the check off (`egress.require_namespace_guard: false`) is permitted,
because an operator may have a perimeter of their own. It is not permitted to be
quiet: the process warns at startup, every time.

## Configuration, and the shape of the refusal

`egress.anonymity_enabled` turns on the posture. With it on, the security lint
requires **either** a namespace guard **or** a SOCKS5 proxy and refuses to boot
with neither — a half-configured tunnel is the configuration most likely to leak
while looking deliberate.

`exempt_from_kill_switch` starts with notification only, and the reason is worth
keeping: *a kill switch that also silences the alert telling you it fired is a
kill switch you find out about from your library being empty.*

## Consequences

- **NordVPN does not offer port forwarding**, so this instance is a passive peer:
  it connects out and is never connected to. Ratios suffer. Accepted residual,
  recorded in PROGRESS.md's risk table.
- **Remote DNS is mandatory** in SOCKS5 mode (`socks5h` semantics — the proxy
  resolves, this process never does). A tunnelled TCP connection preceded by a
  plaintext DNS lookup to the ISP's resolver announces exactly what the tunnel
  was for.
- **The namespace is not created by this software.** It is Compose/systemd
  configuration in the runbook. This software's job is to refuse to run outside
  one, which it does.

## Related

- [ADR-0013](0013-egress-guard-design.md) — how the guarantee is established.
- [ADR-0014](0014-download-engine.md) — peer dials, DHT and uTP derived from the
  egress mode rather than configured separately.
- [ADR-0007](0007-process-boundaries.md) — the downloader's other reason to be
  its own process.

# ADR-0024: Indexers on the operator's own network

**Status:** accepted — the operator approved relaxing the control on 2026-09-26
**Date:** 2026-09-26
**Related:** [ADR-0003](0003-indexer-definitions-cardigann.md),
[ADR-0013](0013-egress-guard-design.md), [ADR-0023](0023-searching-for-a-wanted-episode.md)

## The problem

Every indexer request goes through the egress profile `indexer`, which refuses
private, loopback and link-local destinations (`DenyPrivate`). The reason is
sound and stays sound: an indexer's **feed** decides which URLs this software
fetches next — download links, and the redirects behind them — so a hostile or
compromised indexer could otherwise point the server at a router's admin page,
another service on the LAN, or a cloud metadata endpoint. That is server-side
request forgery, and the control stops it.

It also refuses the ordinary way to run an indexer aggregator. A Prowlarr or
Jackett on the same host, in the same compose file or elsewhere on the LAN is at
a private address, and every search to it was refused. Increment 4l measured it
on a running instance:

```
Prowlarr at http://192.168.1.10:9696 -> refused an unsafe URL: 192.168.1.10 is not a routable public address
Prowlarr at http://localhost:9696    -> destination is a private, loopback or link-local address
```

Until Cardigann definitions are consumed directly (ADR-0003), that aggregator is
the only route to most trackers, so this made acquisition unusable in the usual
deployment.

## Decision

**A private destination is allowed for exactly one thing: the host and port the
operator typed as that indexer's address.** Everything else stays refused.

### 1. The exemption is the operator's own value, and nothing a feed can reach

The operator wrote the indexer's base URL; it is not hostile input. What an
indexer's *feed* names — a download link, a redirect — is, and it gets no
exemption at all. So:

- a search to `http://192.168.1.10:9696` is allowed, because that is the
  indexer's configured address;
- a download link in that indexer's feed pointing at `http://192.168.1.10:9696`
  is allowed too — Prowlarr and Jackett both proxy downloads through
  themselves, so their own address is the one that must be reachable;
- a download link or redirect pointing at **any other** private destination —
  another port on the same machine, the router, another host on the LAN — is
  refused exactly as before.

The match is on **host and port together**, the host compared without regard to
case or a trailing dot, the port filled in from the scheme when it is not
written. `192.168.1.10:9696` does not exempt `192.168.1.10:22`.

### 2. Link-local stays refused, even for the configured address

The allowance covers the networks an operator plausibly runs services on:
loopback, RFC 1918 and unique-local, and carrier-grade NAT (which is Tailscale's
range). It does **not** cover link-local (`169.254.0.0/16`, `fe80::/10`), which is
where cloud metadata endpoints live and where no indexer legitimately sits, nor
multicast, unspecified or reserved addresses. An indexer saved with a
link-local address is refused when it is saved, not when it is first searched.

### 3. The exemption is built in, not passed along

Each indexer gets an HTTP client whose dialer carries its one allowed address,
built by `egress.Guard.HTTPClientAllowing` and cached per address. The
alternative — a context value saying "this request may go private" — would put
the exemption in something every function between the caller and the dialer can
copy, and would depend on the HTTP transport dialling with the request's
context, which is an implementation detail of the standard library rather than a
promise. A structural test pins `HTTPClientAllowing` to `internal/indexer`, the
same way the system principal and the health probe are pinned to their callers.

### 4. The pre-flight check agrees with the dialer

The indexer client validates a URL before dialling it and on every redirect
(ADR-0013: two checks, because they fail in different places). Both now take the
indexer's allowed address, from the same function, so the two cannot disagree
about what is permitted.

## What this does not change

- **Downloads.** The download engine's peer connections keep `DenyPrivate` with
  no exemption. A peer address comes from a tracker or the DHT; none of it is the
  operator's.
- **Metadata and subtitles.** No exemption. Their destinations are third-party
  services on public addresses.
- **SOCKS5.** With the indexer profile routed through a SOCKS5 proxy, a LAN
  indexer is still unreachable — the proxy is not on the operator's network.
  An indexer on the LAN needs the indexer profile in `direct` mode.

## Rejected alternatives

**Turn `DenyPrivate` off for the indexer profile.** One line, and it would work.
It would also let every indexer's feed reach every private address the host can,
which is the attack the control exists for.

**An operator-maintained allowlist of private networks.** More flexible and much
easier to get wrong: `192.168.0.0/16` in that list is the whole LAN, router
included, for every indexer's feed. The exemption above needs no configuration,
because the operator already wrote the one address that matters.

**Put Prowlarr behind a public hostname.** Works today, costs the operator a
reverse-proxy entry and exposes an aggregator holding tracker credentials to the
internet to satisfy a rule meant to protect them. The wrong trade.

## Known limitations

- A **hostname** allowance is by name. If the operator's DNS for that name later
  answers with a different private address, it is allowed; if it answers with a
  link-local one, it is refused. DNS the operator controls is trusted like the
  URL they typed.
- A download link whose host is a different **spelling** of the same machine —
  `http://prowlarr:9696` when the indexer is configured as
  `http://192.168.1.10:9696` — is refused, because it is not the address the
  operator wrote. The refusal names the host, and configuring the indexer by the
  name its links use fixes it.
- Non-ASCII hostnames do not match, and are refused.

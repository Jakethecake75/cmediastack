# ADR-0065: A SOCKS5 proxy set from the web

**Status:** accepted
**Date:** 2026-10-03
**Related:** [ADR-0001](0001-egress-control-wireguard-netns.md), [ADR-0013](0013-egress-guard-design.md) (superseded in part), [ADR-0024](0024-indexers-on-the-operators-network.md)

## The problem

The Proxmox deployment (Phase 7) runs the app in an LXC container with no
WireGuard namespace, and the operator asked for every login and key to be
asked for on the web page, the SOCKS5 proxy included. ADR-0013 keeps egress
policy in the config file and answers `PATCH /api/v1/admin/egress` with 409,
because a runtime switch for the control that prevents leaks is the most
valuable thing a stolen admin session could flip: pointing every search and
download at an attacker's proxy discloses what the instance holds and wants.

The operator chose a proxy that can be changed from the web at any time,
guarded, over one that can be set only during setup.

## Decisions

### 1. The proxy, and only the proxy, can be set from the web

One SOCKS5 proxy: `host:port`, a username, a password, and which of the
`download`, `indexer`, `metadata` and `subtitle` profiles use it (`download`
by default). `notification` and `update` are never offered. The tunnel
interface, the namespace guard, the kill switch, `anonymity_enabled` and the
probe stay file-only, and `PATCH /api/v1/admin/egress` still answers 409.

It is stored in the `setting` table beside the other web-set credentials:
`egress.proxy` holds the address, username and profiles as JSON;
`egress.proxy.password` holds the password sealed with the master key under
the context `egress:proxy`, rotated by `-rotate-key` with the rest.

### 2. Changing it needs more than a session

`PUT /api/v1/admin/egress/proxy` (`admin.network`) takes the administrator's
password **and** a current authenticator code — not a recovery code, and not a
code already used in its time step. Wrong ones count toward the account's
sign-in lockout, as a failed sign-in does. A stolen session cookie alone
cannot change it.

Every change writes an `egress.proxy.changed` audit line naming the old and
new address, never a password, and that line is sent to Discord whatever the
operator's category choices are: an operator learns of a change they did not
make.

### 3. Checked by the boot's lint before it is stored

The would-be result — the file's profiles with the proxy laid over them — is
run through the same security lint as the boot. A combination it refuses, such
as a proxied indexer with direct metadata, is refused at save time with the
lint's own words.

### 4. Applied on restart, never falling back to direct

At boot, each ticked profile the file leaves `direct` becomes `socks5` with the
stored address and credentials and `remote_dns` on. A profile the file sets to
anything else keeps the file's setting, and the screen names it.

If the result no longer passes the lint — the file changed since the proxy was
saved — the ticked profiles become `blocked`, the lint's words are logged at
ERROR and shown on the screen, and the app starts. Traffic meant for the proxy
goes nowhere rather than direct, and the screen that fixes it stays reachable.
A proxy that cannot be reached fails the same way any socks5 profile does.

A change applies on restart. `POST /api/v1/admin/system/restart` stops the
process gracefully and exits with status 3, so systemd's `Restart=always` and
Docker's `restart: unless-stopped` start it again.

## Consequences

- An attacker needs the administrator's session, password and authenticator
  together to redirect traffic, and the operator hears about it on Discord.
  That is weaker than ADR-0013's "filesystem access and a restart", and is the
  price of the operator's choice.
- A proxied `indexer` profile cannot reach an indexer on the LAN: the proxy is
  not on the LAN (ADR-0024). The screen says so.
- SOCKS5 carries TCP only. Under it the download engine turns DHT and uTP off
  (ADR-0001), so a proxied instance is a TCP-only, passive peer.

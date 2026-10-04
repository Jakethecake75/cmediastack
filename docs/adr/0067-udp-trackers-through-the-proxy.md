# ADR-0067: UDP trackers through the SOCKS5 proxy

**Status:** accepted
**Date:** 2026-10-04
**Related:** [ADR-0001](0001-egress-control-wireguard-netns.md), [ADR-0013](0013-egress-guard-design.md), [ADR-0065](0065-socks5-from-the-web.md), [ADR-0066](0066-a-deleted-title-and-its-downloads.md)

## The problem

On the operator's instance a release with 421 seeders found 22 peers and
downloaded nothing. Under a SOCKS5 profile the engine dropped every `udp://`
tracker — ADR-0001 assumed NordVPN's SOCKS5 endpoints "do not reliably relay
UDP" — and this torrent listed thirteen UDP trackers and one HTTPS one. The
operator's qBittorrent, using the same NordVPN SOCKS5 proxy for peer
connections on a machine with no VPN, works: libtorrent sends UDP through the
proxy with SOCKS5's UDP ASSOCIATE (RFC 1928 §7), and NordVPN relays it.

## Decisions

### 1. The guard opens UDP sockets for a profile, through its proxy

`Guard.ListenPacket(subsystem, alias)` returns a `net.PacketConn`. Under a
socks5 profile it is a UDP association: a TCP control connection to the proxy,
authenticated as for CONNECT, and every datagram sent to the relay the proxy
names with the SOCKS5 UDP header in front. Under a blocked profile it refuses;
under direct it is an ordinary UDP socket.

### 2. Tracker hostnames are resolved by the proxy, not here

The torrent library resolves a UDP tracker's hostname with the system resolver
before every send — a lookup outside the proxy, which a socks5 profile promises
never to make (`remote_dns`). So when a torrent is added under a proxy, each
`udp://` tracker's host is replaced by a placeholder in `127.88.0.0/16` that
the library sends to without a lookup; the association turns a placeholder
back into the tracker's name and sends it to the proxy as a domain, to be
resolved there. Replies are matched by transaction id, not by address, so the
library accepts them from the tracker's real address.

### 3. A proxy that will not relay UDP changes nothing

At start, the engine asks the proxy for an association. If it refuses, UDP
trackers are dropped as before and the Queue's notes say the proxy refused UDP.
An association that fails later makes its tracker's sends fail — never a direct
send, and never the library's panic on a refused socket.

### 4. DHT and uTP stay off under a proxy

DHT matches replies by address and bootstraps from hostnames, so it does not
fit the placeholder scheme; uTP would carry transfers over UDP for little gain
once peers are found. Both stay off under a proxy, as ADR-0001 says.

## Consequences

- Under a proxy that relays UDP, a torrent's UDP trackers are announced to
  through it: peers are found as qBittorrent finds them.
- `127.88.0.0/16` is reserved for these placeholders inside the engine; they
  never leave the process as destinations.
- Incoming connections remain impossible behind a proxy, as for qBittorrent:
  NordVPN forwards no ports.

# ADR-0068: Finding peers behind a proxy without UDP, and saying how a download is doing

**Status:** accepted
**Date:** 2026-10-04
**Related:** [ADR-0066](0066-a-deleted-title-and-its-downloads.md), [ADR-0067](0067-udp-trackers-through-the-proxy.md)

## The problem

v0.1.2 asked NordVPN's SOCKS5 proxy for a UDP association, and it refused
(*connect reply: EOF*). The operator's qBittorrent, on the same proxy, was
checked: every `udp://` tracker "Permission denied", DHT 0 nodes, firewalled —
and it downloads anyway, from `http://tracker.opentrackr.org:1337/announce`,
the HTTP form of the largest open tracker, which hands it about 140 peers.
Our engine, offered only `udp://tracker.opentrackr.org:1337` by the torrent,
found 22 peers, connected to 7, two of them seeders, and received nothing: a
newcomer with one or two seeders waits for an upload slot it may not get.

A test with a seeder and a downloader in one process, the downloader through a
SOCKS5 proxy, finishes in a tenth of a second — the data path through a proxy
is sound. The shortage is peers.

## Decisions

### 1. Behind a proxy that refuses UDP, ask each UDP tracker over HTTP

When the proxy refuses UDP, every `udp://host:port` tracker is also asked as
`http://host:port/announce`, through the proxy, by name. The big open trackers
answer both; one that does not simply fails its announce. Where UDP works —
direct, or a proxy that relays it — trackers are used as the torrent lists
them.

### 2. The Queue says how each download is doing

Each download shows its peers connected (and seeding), being connected to and
waiting to be tried, the bytes received, and its speed between the last two
looks. "Nothing arriving" beside two connected seeders is a different problem
from "no peers", and the Queue now says which.

## Consequences

- A torrent that lists only UDP trackers can still find a swarm behind NordVPN.
- An HTTP announce to a tracker that has no HTTP endpoint costs one failed
  request per announce interval.

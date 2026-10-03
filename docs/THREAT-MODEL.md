# Threat model

What this system is defending, from whom, and what it explicitly does not
defend against.

[`SECURITY.md`](../SECURITY.md) lists the controls that are *enforced today*.
This document is the analysis they answer to: if a control there has no threat
here, it is probably ceremony; if a threat here has no control there, that is a
gap and should be written down as one.

## Deployment being modelled

One instance, on a Proxmox host, **exposed to the public internet behind a
reverse proxy** (§13). Media on NFS/SMB or bind-mounted ZFS. Up to ~25 accounts,
all invited or approved by one operator, ~5 concurrent streams. The operator is
also the administrator and is the only administrator there will ever be.

The public exposure is the decision that shapes everything below. A LAN-only
instance would justify a much smaller model; this one is reachable by anyone who
finds the hostname.

## Assets, in the order an attacker would want them

| # | Asset | Why it is worth taking |
|---|---|---|
| 1 | **The master key** | seals every indexer credential and the metadata token; unsealing them is credential theft against third parties |
| 2 | **Accounts and session material** | an account is a foothold; the admin account is the whole instance |
| 3 | **Private-tracker credentials** | reusable against the tracker; losing a private-tracker account is often permanent |
| 4 | **The library and what it reveals** | what an operator has and watches is sensitive on its own, and it is evidence |
| 5 | **The host** | it is a hypervisor guest on a home network with NFS mounts |
| 6 | **Outbound network identity** | a leaked source IP while torrenting is the failure §2 exists to prevent |

Asset 6 is unusual and it is why [ADR-0001](adr/0001-egress-control-wireguard-netns.md)
is treated as a security control rather than a networking preference.

## Trust boundaries

```
 internet ──┬─► reverse proxy ──► app role ──► database, master key, os.Root handles
            │                        │
            │                        ├──► metadata provider   (outbound, TLS, operator token)
            │                        └──► indexers            (outbound, sealed credentials)
            │
            └─► (no path) ───────► downloader role ──► peers  (netns; tunnel only)
                                        │
                                   media files ──► [Phase 4] ffmpeg executor
```

Crossings, and what is assumed at each:

1. **Internet → app.** Everything is hostile. No request is trusted for
   anything, including its claim to be from the proxy.
2. **App → database/keys.** Inside one address space. This boundary does not
   exist, which is exactly why parsers are kept out of it.
3. **Peers → downloader.** Fully hostile, and *this software asked for the
   bytes*. Separate process, separate namespace.
4. **Downloaded file → media executor.** Fully hostile. The largest parser
   surface in the system, and the one not yet built.
5. **Provider → app.** A third party's JSON and a third party's strings. Trusted
   for content, never for structure — a provider's poster path is validated to a
   plain filename before it reaches a path, then written through an `os.Root`
   anyway.

## Adversaries

### A1 — The internet, unauthenticated

The largest population and the least capable. Scanners, credential stuffing,
anyone who found the hostname.

**Wants:** any authenticated foothold.
**Reaches:** twelve anonymous routes, and nothing else.

The whole posture against A1 is that the anonymous surface is small, enumerated
and machine-checked: every anonymous route also appears in
`api.AnonymousAllowlist`, and the router **panics at startup** if the two
disagree. Every other API route answers an anonymous caller 404, as a route
that does not exist would, so probing does not map the application.

Probing is still recorded — that is how an operator sees it — and so every
anonymous request for a protected route used to write an audit row: A1 could
fill the disk and bury the log without authenticating. Those rows now have a
ceiling, 20 an hour from one address and 120 from all, with the rest counted in
one line an hour; the worst a flood adds is 121 rows an hour from any number of
addresses ([ADR-0031](adr/0031-reading-the-audit-log.md)).

Registration is open by design (§13), which means A1 can create an
`account_request` at will. That is why a request **is not a user** — it holds no
session, no token and no permission, and cannot authenticate. The realistic A1
attacks are therefore resource exhaustion of that table and credential stuffing
against `/api/v1/auth/login`, and since 6a (ADR-0051) a signup pays a proof-of-work before
any Argon2id time is spent, and a password in a known breach is refused.

Mandatory app-based MFA on every account means a correct password is not
sufficient for anybody, including the administrator.

### A2 — An approved user, acting beyond their role

Someone the operator invited. Most likely to be curious rather than malicious,
and most likely to succeed, because they are already inside.

**Wants:** other people's data, administrative capability, files they were not
given.
**Reaches:** every authenticated route, subject to permissions.

This is the adversary the effect-based authorization model is built for.
Checking a permission at the route and trusting it afterwards fails against A2
the first time a second code path reaches the same effect; checking it *where
the effect occurs* does not. Request visibility is a **scope computed from the
principal**, not a permission, so a user cannot see other people's requests by
finding an endpoint that forgot to filter.

What A2 can **read** is what its grant says: every library, or the root folders
it was granted, and titles within its rating ceiling (ADR-0037). The scope is
applied in the SQL of every read made for a person, and a title outside it
answers as an absent one does. The queue is the exception, and says so: a
Manager managing downloads sees every download's name.

What a Manager can **cause** includes adding a series or a film (ADR-0025,
ADR-0026): rows only — nothing downloaded, nothing written to disk — at a
bounded cost in provider requests, audited as `media.added`. What an account
that may search and queue can cause includes grabbing for a film; the film a
release is filed under comes from the sealed ticket, never from the request.

Privilege escalation is structurally bounded: **every role assignment is
strictly downward.** An Admin cannot mint an Admin — deliberately, because a
peer cannot be demoted, so it would convert a stolen session into permanent
independent access. The cost is the single-administrator limitation, mitigated
by console-only break-glass recovery.

### A3 — A stolen session or API token

**Wants:** to persist.
**Reaches:** whatever the victim could.

Sessions are opaque and server-side rather than JWTs, for one reason: immediate
revocation. A self-contained token cannot be revoked without the database lookup
it exists to avoid.

Rotation with a short grace window means a stolen cookie replayed after the
victim's next request is detected as **reuse**, which ends the session. That
also ends the *victim's* session — deliberate, and a real usability cost on
flaky networks, recorded as such.

API tokens are separated from sessions precisely here: fourteen routes are
session-only, so a stolen token cannot change a password, enrol an
authenticator, or manage credentials, whatever its scope.

Persistence has to leave a mark, and the marks now reach the operator rather
than waiting in a log (ADR-0032): an API token issued, a password changed, an
authenticator enrolled, a recovery code used, a role changed — and, before any
of that, *password accepted, authenticator code refused*, which says a password
is known. They go to the operator's Discord channel within a minute. Detection,
not prevention. An attacker holding the administrator's session can turn
notifications off or point them at a channel of their own — and either is said,
with the account that did it, in the channel they leave. What happened in the
minute before may not have been sent yet; the audit log still holds it.

### A4 — A malicious file

A release from a tracker, chosen by this software, downloaded automatically, and
then parsed.

**Wants:** code execution in a process that holds keys.
**Reaches:** the download engine today; `ffmpeg` in Phase 4.

This is the adversary most media servers underrate, and the reasoning is in
[ADR-0007](adr/0007-process-boundaries.md): `ffmpeg` is millions of lines of C
whose job is parsing untrusted container data, with a CVE history to match.
Writing the application in Go buys nothing if a C parser is linked into the same
address space — hence `CGO_ENABLED=0`, asserted against the built artefact
rather than the environment.

Two consequences of taking A4 seriously that look like missing features:

- **Archives are not unpacked.** `.rar`, `.zip`, `.7z` are skipped with a
  reason. Every competitor unpacks them.
- **Import is by allowlist**, not by excluding what looks dangerous.

**Closed in increment 4c.** This section used to record the container's
seccomp profile as `unconfined`. The container now runs under
`deploy/seccomp-cmediastack.json`: Docker's default profile plus one narrowly
scoped allowance for the namespace flags the media-parser jail needs, which the
default profile refuses.

### A5 — A network observer, and the swarm

**Wants:** to link this instance's IP to what it is transferring.
**Reaches:** the downloader's traffic; the swarm sees every peer.

Torrent swarms are public and monitored — this is not speculative. The control
is the kernel (a namespace with one route out), not the application, because an
application-level proxy is something a dial can forget and the failure is
silent.

Two derived behaviours that a leak analysis makes non-optional:

- **DHT, uTP and incoming connections follow the egress mode.** A tunnelled
  instance that still announces on DHT has told the swarm who it is regardless
  of how its TCP is routed.
- **Remote DNS is mandatory** in SOCKS5 mode. A tunnelled connection preceded by
  a plaintext lookup to the ISP's resolver announces exactly what the tunnel was
  for.

**Artwork is a quieter case of the same thing.** Posters are cached and served
by this instance rather than linked to the provider's CDN — otherwise every page
view would make the *operator's own browser* announce their library to a third
party, once per row. That is why the review screen fetches posters through
`/api/v1/artwork/poster/...`, and why a candidate poster's URL is reconstructed
from a stored row rather than accepted from a request.

### A5a — A hostile or compromised indexer

**Wants:** to make this instance fetch something of the indexer's choosing — a
router's admin page, another service on the LAN, a cloud metadata endpoint — or
to plant a release named to escape the library.
**Reaches:** everything in its feed: release names, download links, and the
redirects behind them.

Everything a feed names is hostile input. A release name reaches a path only
through `SafeComponent` and inside an `os.Root`, so none can escape the library
(ADR-0015, ADR-0016); a grab takes a sealed ticket, never a URL from the client
(ADR-0014); and every download link and redirect is refused if it points at a
private, loopback or link-local address (`DenyPrivate`, ADR-0013).

A release named to be *taken for* something it is not — `Dune.1984` offered to a
search for the 2021 film, an episode of a namesake series offered as a film —
is refused by the matching before a ticket exists (ADR-0023, ADR-0026), and a
film's release without a year is refused outright, since nothing else tells a
remake from its original. What a matching release is filed under is the item
sealed into its ticket, never a folder built from its name. A torrent's file
list is equally hostile: when a finished download is imported from what it left
on disk, the walk is contained by `os.Root` and lists regular files only, so a
symbolic link in the download's directory — however it came to be there — leads
nowhere the import will follow.

**One exemption, and why it gives an attacker nothing** (ADR-0024): an
indexer's own configured host and port may be on the operator's network, so a
Prowlarr or Jackett on the LAN works. The address is the one the operator
typed; a feed cannot choose it, extend it to another port, or lend it to a
different indexer. An attacker who controls that aggregator can make this
instance fetch from the aggregator — which they already control — and from
nowhere else private. Link-local stays refused even there.

### A6 — The operator's own mistake

Statistically the most likely cause of a bad outcome, and treated as an
adversary rather than a support issue.

Mitigations are behavioural: destructive operations are reversible (trash, not
`unlink`) or audited, including **refusals**; the egress config refuses to boot
half-configured; a scan never changes anything on disk and says so in its own
response; and the identification pass proposes rather than acts, because a wrong
automatic rename looks deliberate.

### A7 — A supply-chain compromise

**Reaches:** everything, at build time.

Partially addressed and honestly so. The web UI has **no build step and no
`node_modules`** ([ADR-0011](adr/0011-server-rendered-shells-no-build-step.md)),
which removes the largest dependency surface in a typical application of this
shape. Go dependencies are pinned with checksums. Cardigann definitions are
consumed as data and are *not* vendored, which is better for freshness and means
an operator is trusting Jackett's repository — stated plainly rather than
implied.

**Not addressed:** no reproducible-build verification, no SBOM, no signature on
releases.

## Explicit non-goals

Naming these matters as much as the model; each is a place where a reader might
otherwise assume a defence exists.

1. **Nation-state adversaries.** Out of scope at every layer.
2. **Physical access to the host.** Disk encryption is the host's job. Anyone
   with the disk has the master key.
3. **A malicious administrator.** The administrator *is* the operator. There is
   no separation of duties to enforce and pretending otherwise would be theatre.
4. **DRM-style protection of the library from its own users.** An account that
   can watch a file can copy it.
5. **Anonymity as a property of the application.** Egress control prevents
   accidental leaks from the download path. It is not Tor, it does not protect
   against a hostile VPN provider, and it says nothing about the operator's
   metadata calls.
6. **Availability under attack.** Rate limiting exists; a determined DoS against
   a home uplink succeeds. The reverse proxy is the right place for that fight.
7. **Legality of the content.** §13 places this on the operator, and no control
   here changes it. Seeding is distribution, and with no recorded obligation the
   engine seeds **indefinitely** — `download.seed: false` is the one switch that
   stops it.

## Residual risks being carried

| Risk | Severity | Why it is accepted |
|---|---|---|
| Unprivileged user namespaces permitted in the container — the one allowance in `deploy/seccomp-cmediastack.json` | **Medium** | the media-parser jail cannot exist without it; the trade is written out in SECURITY.md. (This row said the profile was `unconfined` until 4n; that was fixed in 4c) |
| A capped account learns a hidden title exists | **Low** | enforced since 4ac (ADR-0037): hidden reads as absent on every route that names a title. What remains — the queue's names to `acquisition.queue`, and *already in the library* on an add by a restricted `library.edit` — is written in ADR-0037's limitations |
| Exactly one administrator | Low | strictly-downward roles are the reason; break-glass recovery is the mitigation |
| ~~No proof-of-work or breached-password check on signup~~ | — | built in 6a (ADR-0051); the breach check fails open when HIBP is unreachable, and used challenges are remembered in memory |
| Session-reuse detection revokes the victim too | Low | deliberate; revisit if it fires on mobile networks |
| ~~Source obligation (AGPL §13) not yet offered in the UI~~ | — | every page offers it since 6b ([ADR-0052](adr/0052-offering-the-source.md)) |
| Cardigann definitions trusted from upstream | Low | the alternative is maintaining hundreds of scrapers alone |

# CMediaStack

One self-hosted service that replaces Radarr, Sonarr, Lidarr, Readarr, Bazarr,
Prowlarr, qBittorrent, Jellyseerr and Jellyfin.

**Status: Phase 4 (Playback), increments 4a–4x** — the counts, and what is and is not
built, are in [`PROGRESS.md`](PROGRESS.md). The full account lifecycle works end
to end **in a browser**: first-run wizard → administrator → login → authenticator
enrollment → working session, plus signup, invites, admin approval, per-device
session management, scoped API tokens, password reset, and suspension that kills
live sessions, tokens and outstanding invites instantly. Prometheus metrics and
a scheduled-task framework run on the management listener.

The UI is six hand-written pages embedded in the binary — **no Node, no npm, no
bundler and no third-party frontend code** ([ADR-0011](docs/adr/0011-server-rendered-shells-no-build-step.md)).
It is verified in real Chromium against the compiled binary: zero CSP
violations, zero page errors.

Phase 2 has begun with the egress guard: `--role downloader` **refuses to start
unless it can prove its outbound traffic leaves through the tunnel**, and no
egress mode falls back to a direct connection when that tunnel is down
([ADR-0013](docs/adr/0013-egress-guard-design.md)) — building the jail before
anything that needs it is the point. Release-name parsing and
quality profiles landed next: the part that decides what to grab and when to
replace it, with a corpus that found six real bugs and a fuzzer that found two
more in under a minute. Indexer clients followed: Torznab over the guard, with
XXE and billion-laughs tested against real payloads rather than asserted, and
API keys sealed so tightly that the admin listing never decrypts them at all.
The three now join up: `POST /api/v1/releases/search` fans out to every indexer,
parses what comes back, judges it against a quality profile, and returns what it
**refused and why** — because "why did it not grab anything" is the question this
kind of software is worst at answering.

The download engine then moved in behind the guard
([ADR-0014](docs/adr/0014-download-engine.md)). Two lines make the egress promise
true for BitTorrent — `DialForPeerConns = false` empties the library's own dialer
set so `AddDialer` installs ours as the *only* one — and that claim is tested by
running a real seeder and leecher on loopback with the gate shut and asserting no
bytes move. What the dialer cannot cover is said plainly rather than papered
over: DHT and uTP are UDP, so under a SOCKS5 profile the engine **forces them
off** rather than letting an operator believe their traffic is tunnelled while it
is not. A grab takes an encrypted ticket bound to the grabbing user, never a URL,
so the client cannot choose what the server fetches; the queue is persisted with
the torrent verbatim, so a restart resumes without contacting an indexer at all.

Phase 3 then began the way Phase 2 did — with the jail, not the thing inside it.
Importing means writing to an operator's disk using names strangers chose, so the
containment layer came first ([ADR-0015](docs/adr/0015-library-path-containment.md)).
It is `os.Root`, which resolves every path component against a held descriptor
with `openat2(RESOLVE_BENEATH)`: containment enforced by the **kernel**, per
component, at the moment of use, rather than by a string comparison that a
symlink defeats. The test suite implements the usual `filepath.Join`-plus-prefix
version beside it and watches it read `/etc/passwd` through a planted link.

The importer then landed on top of it: a finished download becomes a library
entry, hardlinked so the torrent client keeps seeding the same bytes
([ADR-0016](docs/adr/0016-import-pipeline.md)). Choosing *which* file in a
torrent is the media is an allowlist rather than a denylist, because a denylist
has to be right about every file type a stranger might include and an allowlist
only about the dozen containers this software can play. That structural test
earned its keep again here: it caught the importer joining the torrent's own
declared file path to the download directory, where `../../../../etc/shadow`
would have been hardlinked into a library served over HTTP.

A scan then made it usable with a library that already exists, which is the case
anyone replacing this stack actually has. A scan **changes nothing on disk** —
software that "organises on scan" is how people lose libraries — and it refuses
outright when most of a root's files vanish at once, because that is far more
often an unmounted disk than a deletion, and acting on it would erase the record
of a whole library in one pass.

Administration of other people's accounts came last, and asking its obvious
question turned up two defects. *Can an operator lock themselves out of their own
instance?* Today no, because no account may act on itself, a peer or a superior,
and Admin is the top rank. But that will stop being true the moment role editing
lands, so there is a second guard underneath — and a mutation test that turns the
rank rules off found that guard was wired into the role-change path and **not**
the suspension path: a Manager suspended the only administrator with a 200 OK.
Reading `audit_event` out of a running instance found the other one — refused
suspensions were logged and refused *role changes* were not, so "can I promote
myself to Admin?" was refused and then invisible.

That surfaced a resilience problem rather than a security one — an instance has
exactly one administrator, so losing both the authenticator and the recovery
codes means a database edit — and the obvious fix turned out to be the wrong one.
Letting an Admin mint another Admin makes them a *peer*, and a peer cannot be
demoted: a stolen admin session, which today expires and can be revoked, would
become permanent independent access. So recovery went to the host instead, where
an operator's authority actually comes from: `-recover <username>` resets an
authenticator, needs a shell and the master key, and adds no reachable surface —
no route, no permission, no principal leads there. It hands back no session. The
account returns to `awaiting_mfa` and enrolls again like anyone else.

Requests came last, and made the instance usable by somebody who is not the
operator ([ADR-0017](docs/adr/0017-acquisition-requests.md)). **Approving a
request downloads nothing** — Overseerr approves-and-fetches, and that is the
wrong default when §13 makes the operator answerable for whatever the instance
acquires: automatic fulfilment means the machine matching a title to a release,
which is the exact step with the longest list of known failures here. The
machine narrows; a person chooses. And who sees whose request is a *scope*
computed from the principal rather than a permission, because expressed as a
permission the difference disappears and every user reads every other user's
watchlist.

Metadata came last, and brought this software its first outbound dependency on a
commercial service, a long-lived credential, and a path by which a third party's
bytes and **filenames** reach the operator's disk
([ADR-0018](docs/adr/0018-metadata-and-artwork.md)). That third one is the shape
of the project's one real vulnerability, so the order was the same as before:
the jail first. A provider may supply a plain filename and nothing else; the
destination is built from values this software chose; and the write goes through
`os.Root` regardless — demonstrated by disabling both application-level checks
and watching the kernel refuse the traversal anyway. The live CDN then earned
its test twice over: it answered 404 to a URL that every fixture had accepted,
and it serves **WebP from a URL ending `.jpg`**, which is why the file type is
decided by sniffing bytes rather than by trusting anyone.

A key then arrived and the rest was verified too — as a test that runs with
`CMS_TMDB_TOKEN` set and skips without it, so the suite stays runnable by anyone
offline. Two of the documentation-derived assumptions were wrong: TMDB's
`episode_run_time` comes back **empty** for most series, and an *announced*
season legitimately has zero episodes and no air date — which is precisely the
row an episode table must not turn into nine imaginary episodes. When that table
arrived ([ADR-0022](docs/adr/0022-episode-tracking.md)) the announced season
became the reason a finished-looking series is still asked about: it is the
season about to gain episodes.

Identification then had to decide what to do with all that
([ADR-0019](docs/adr/0019-identification.md)). Radarr matches automatically and
lets you correct afterwards; this does not, because the two failure modes are
not equal — a missed identification leaves an item looking exactly as it does
today, and a wrong one **relabels your library** with somebody else's title and
poster, looking deliberate. So the machine proposes and a person confirms, and
automatic acceptance needs an exact title, an exact year *and* a clear margin
over the runner-up. The margin is not caution for its own sake: **two films
called *Arrival* came out in 2016**, the provider returns both, and a confidence
threshold would pick one of them with even odds, forever.

The review screen that follows from that exists now, and building it found four
defects that only a browser and a live provider could have shown. The one worth
repeating: the pass caches a poster for what it **accepts**, so what it
*proposes* — the one screen whose entire job is to be looked at — had no artwork
at all. Posters are fetched on first view instead, and only for a title this
instance already recorded, so a request can choose *between* the posters it knows
about and cannot introduce one. The screen shows pictures, titles and the reason
each candidate is a candidate, and deliberately **not** the scores: a number next
to a film invites trusting it rather than looking, and the two 2016 films called
*Arrival* come back textually identical — same title, same year, same
explanation, both scoring 1.00. Only the poster tells them apart.

Starting a library from nothing came next, because that is what the operator
is doing ([ADR-0025](docs/adr/0025-adding-a-series-before-it-is-on-disk.md)).
Until then an item existed only once a file did — found by a scan or built by an
import from a release name — so following a show meant downloading an episode
of it first. Now a series is added from the provider: its id names it, **nothing
is downloaded and nothing is written to disk**, and every season and episode is
recorded with the operator's choice of what to want, in one transaction or not
at all. The choice has no default, because *all* and *future* differ by an
entire back catalogue on the wanted list.

Films followed, together with their own search, for the reason episodes got one
([ADR-0026](docs/adr/0026-adding-a-film-and-searching-for-it.md)): an added film
filed by its release name gets a twin the first time a release spells it
differently, and *Star Wars* is released as
`Star.Wars.Episode.IV.A.New.Hope.1977`. The search judges every release against
the film — its title, its original title, the provider's alternative titles and
its year — and only one that **is** the film can be grabbed, with the film sealed
into the grab. A year is required, because titles recur: the provider lists four
films titled *Dune*. Checking it end to end against the live
provider found an older bug in the acquisition path: a download that finished
shortly before a restart — or before the seeding task let it go, which with
seeding off is at once — stayed complete in the queue and was never imported,
and nothing said so. Imports now read what a download left on disk.

Whether a release is the film and whether it is *good enough* are different
questions, and until
[ADR-0027](docs/adr/0027-a-default-quality-profile.md) only a search that named a
quality profile asked the second one — and the film and episode searches named
none, so a camera recording of a film still in cinemas came back with a Grab
button. Now the instance has one default profile, *HD-1080p* unless an
administrator chooses another or none, and every search that names no profile
is judged by it. *No profile* is still one choice away on every search, and
every answer says what judged it.

Requests end in the library now
([ADR-0028](docs/adr/0028-an-approved-request-is-satisfied-by-a-library-item.md)).
Writing that decision found that no screen had ever told a grab which request it
was for, so a request approved in the browser stayed *approved* after its film
had arrived and been watched. Now an approver adds the title from the request —
the Add screen opens searching for what the request says — and the request is
linked to what was added. It is fulfilled when a file of that title arrives,
whichever search grabbed it, and the requester can see what it became.

The database is backed up now
([ADR-0029](docs/adr/0029-encrypted-verified-backups.md)): once a day and once at
startup when the newest backup is a day old, encrypted with a passphrase derived
from the master key, and checked — SQLite's integrity check, the migrations,
and a decryption of the written file — before it is kept. Nothing downloads or
restores a backup over HTTP; both happen on the host.

And what is wanted can now arrive by itself
([ADR-0030](docs/adr/0030-automatic-acquisition.md)) — off unless the operator
turns it on. Every fifteen minutes each indexer is asked once for its recent
releases, and a few wanted items are searched for; a release is downloaded
when it matches exactly one of them by the rules a person's search uses, the
default profile accepts it, it has seeders, and it was never in the queue —
at most five a pass. Removing a download is how an operator says *not that
release*; unmonitoring, *not that title*. Every automatic grab is in the audit
log as `system:acquire`, and the Wanted screen says beside every item what was
done about it and why nothing was grabbed. Building it found that
`The.Office.S01E02` fits two series in a library that holds both, so a release
whose name fits two titles is grabbed for neither.

The audit log that records all of this finally has a screen
([ADR-0031](docs/adr/0031-reading-the-audit-log.md)) — the administrator's, and
invisible to everyone else. Building it found that anybody on the internet could
write to the log without limit: a request for a protected page without a session
is refused and recorded, which is the point, but at ten a second that is 864,000
rows a day, carried by every backup. Those denials are now written one by one up
to a ceiling — twenty an hour from an address, 120 from all of them, sixty from
an account, which a flood can neither hide nor lift — and the rest are counted in
one line an hour.

And what goes wrong now comes to the operator rather than waiting to be found
([ADR-0032](docs/adr/0032-notifications-to-discord.md)): a Discord webhook, set
on a screen, receives within a minute a task that starts failing or recovers —
the tunnel, backups — a flood of denials, *password accepted, authenticator
code refused*, an API token issued, an account waiting for approval; and, if
the operator turns them on, grabs, arrivals and requests. The link is a
credential and is treated as one — checked with Discord before it is kept,
sealed, never shown again — and a username chosen by a stranger arrives as
text: it can neither ping the server nor pass for a link. Turning notifications
off, or pointing them somewhere else, is said in the channel they leave.

See [`PROGRESS.md`](PROGRESS.md) for exactly what is and is not built — including
the bugs each increment found, several of which only appeared against the
compiled binary.

## Run it

```bash
make genkey                          # generate a master key
export CMS_MASTER_KEY=<that value>
cp config/config.example.yaml config/config.yaml

make build
make check-config                    # validate without starting anything
./bin/cmediastack --config config/config.yaml
```

Then open the address in a browser. The first visit lands on `/setup`, which
creates the first administrator and closes itself permanently. Log in, enroll an
authenticator app by scanning the code shown (or typing the key by hand), and
store the recovery codes.

If you lose the authenticator *and* the recovery codes, recover from the host:

```bash
./bin/cmediastack --config config/config.yaml -recover <username>
```

It clears that account's authenticator, kills its sessions and API tokens, and
leaves it at `awaiting_mfa` — so the next sign-in goes through enrollment again.
It hands back no session and grants no role, it needs the master key, and it is
written to the audit log as `user.recovered`. Add `-recover-password` to also
read a new password from stdin.

Backups land in `backups/` beside the database unless `backup.dir` says
otherwise — copy them off the host, because a backup on the database's disk does
not survive that disk. Check any copy, and restore one, from the host:

```bash
./bin/cmediastack --config config/config.yaml -verify-backup FILE
./bin/cmediastack --config config/config.yaml -restore-backup FILE -restore-to NEW.db
```

The restore writes a new database file — never over one — ends every session and
API token in it, and prints the steps to put it in place. Both need the master
key the backup was taken under. [`docs/RUNBOOK.md`](docs/RUNBOOK.md) has the
whole procedure, including decrypting a backup with the standard `age` tool if
this software is gone.

```bash
make test          # everything
make test-race     # with the race detector
make acceptance    # just the four Phase 1 acceptance criteria
make lint          # golangci-lint, at the version CI pins
make security      # the same gates CI runs: vet, govulncheck, gosec
```

### In a container

```bash
echo "CMS_MASTER_KEY=$(make -s genkey)" > .env

# The library must be WRITABLE by the container's user. This is the step that
# bites: the app validates a root folder by writing a probe file, so a
# read-only or wrongly-owned mount fails at the first thing you do.
sudo chown -R 65532:65532 /srv/media

docker compose up -d
docker compose logs -f          # watch for "media parser sandbox is available"
```

Two lines in that log are worth reading before anything else:

| Line | Meaning |
|---|---|
| `media parser sandbox is available` | ffmpeg runs jailed, with no network. If it says **NOT available**, your runtime is dropping the seccomp profile — parsing still works but without isolation |
| `media tools are MISSING` | there is no ffmpeg on `PATH`; nothing can be played. Should never happen with the shipped image, which carries static builds |

The compose file mounts the library read-write on purpose. The app imports into
it, renames on import, and moves deletions into a trash directory inside the
root rather than unlinking them — which is what makes a delete reversible. To
narrow it, mount only the directories that are actually root folders; there is
no browse-only library mode.

## Read in this order

| Document | What it answers |
|---|---|
| [`PROGRESS.md`](PROGRESS.md) | **Start here.** What exists, what does not, what is next. Hand this to a new session to resume. |
| [`SECURITY.md`](SECURITY.md) | Every security claim mapped to the test that proves it, and an honest list of what is not enforced yet |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | How it is structured, what consolidation buys, why this stack |
| [`docs/DATA-MODEL.md`](docs/DATA-MODEL.md) | The single schema; the state machines that matter |
| [`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md) | STRIDE, trust boundaries, five attacker personas, 23 threats → mitigation → test |
| [`docs/API-SURFACE.md`](docs/API-SURFACE.md) | Routes, the anonymous allowlist, how authorization is enforced |
| [`docs/DROPPED-FEATURES.md`](docs/DROPPED-FEATURES.md) | What is deliberately not being built, and why |
| [`docs/ESTIMATES.md`](docs/ESTIMATES.md) | Honest effort estimates, and what they mean at your available hours |
| [`docs/RUNBOOK.md`](docs/RUNBOOK.md) | Standing it up on Proxmox and Docker, the reverse proxy, NordVPN, backups and restores, upgrades, and what to do when something breaks |
| [`docs/adr/`](docs/adr/) | Twenty-nine decision records for the non-obvious tradeoffs |

## The six things worth knowing

1. **Egress control lives in the kernel, not the app.** NordVPN offers no port
   forwarding and its SOCKS5 endpoints do not reliably relay UDP. The download
   engine runs in a network namespace behind a WireGuard tunnel with a
   default-deny firewall it has no capability to modify — if `internal/egress`
   were deleted, a direct connection would still be impossible, because there
   is no route for one. The app layer verifies that jail and refuses to start
   without it, and a build-failing source scan stops anyone reaching for
   `http.Get` on the acquisition path.
   [ADR-0001](docs/adr/0001-egress-control-wireguard-netns.md),
   [ADR-0013](docs/adr/0013-egress-guard-design.md)

2. **Permissions are defined over effects, not routes.** "Delete media files:
   Admin only" is meaningless if a Manager can reach the same unlink through
   the download queue. `authz.RequireEffect` is called where the effect
   happens, so every route that reaches it is checked.

3. **The anonymous surface is twelve routes, listed literally.** Registering a
   thirteenth panics unless the literal allowlist is edited too. The
   application shell and its JS bundle are *not* anonymous, and a test scans the
   anonymous bundle to prove it names no authenticated endpoint. Exactly one
   route — the root — answers a denial with a redirect to the login page rather
   than a 404, and `register()` panics on a second.
   [ADR-0012](docs/adr/0012-root-redirect-carve-out.md)

4. **The test hardware caps the media server.** An i5-6500T (Skylake) cannot
   hardware-decode HEVC 10-bit and therefore cannot tone-map HDR at all. 4K HDR
   is direct-play-only on that box.
   [ADR-0005](docs/adr/0005-transcode-policy-skylake.md)

5. **The media parser is jailed, and it is handed a file descriptor rather
   than a path.** This software downloads files chosen by strangers and points
   ffmpeg at them, so ffprobe and ffmpeg run in a namespace with no network —
   an exploited parser cannot call out, cannot reach the LAN, and cannot reach
   the NFS server the media is mounted from. And because the parser is given an
   open descriptor instead of a filename, it cannot open a file it was never
   told the location of: the kernel-enforced containment of `os.Root` extends
   *into* ffmpeg instead of stopping at the process boundary.
   [ADR-0020](docs/adr/0020-playback.md)

6. **The schedule is the main risk, not the technology.** At the specified
   parity this is a 1.8–3.5 person-year build.
   [`docs/ESTIMATES.md`](docs/ESTIMATES.md) sets out the phase order so that
   stopping at any gate leaves you better off than you are today.

## Licence

AGPL-3.0. See [`LICENSE`](LICENSE) and
[ADR-0008](docs/adr/0008-licensing.md).

CMediaStack indexes and downloads whatever its operator points it at. The
operator is responsible for the legality of that content.

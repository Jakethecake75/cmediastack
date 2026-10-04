# Operator runbook

How to stand CMediaStack up on the deployment it was specified for — Proxmox VE,
Docker Compose, a NAS holding the media, NordVPN for downloads, the public
internet behind a reverse proxy — and how to keep it running: backups and
restores, upgrades, what to watch, and what to do when something breaks.

Every command here is one this build supports today. Where a step has **not been
run against the real thing** — NordVPN through gluetun is the one that matters —
it says so, and says how to check it yourself before trusting it.

Paths are given two ways where they differ: **inside the container**
(`/config/...`) and **on the host** (`./config/...`, beside `docker-compose.yml`).
The commands the binary prints use the container's.

---

## 0. Proxmox: one container, one command

The quickest deployment, and the one Getting started is written for. In the
Proxmox VE host's shell:

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/Jakethecake75/cmediastack/main/ct/cmediastack.sh)"
```

`ct/cmediastack.sh` asks for **default** settings (next free ID, `cmediastack`,
2 cores, 2048 MB, 64 GB, DHCP on `vmbr0`, the first storage that takes
containers) or **advanced** ones, then:

- downloads the newest Debian 12 template with `pveam` if it is missing;
- creates an **unprivileged** container with **`nesting=1`** — the media-parser
  sandbox (ADR-0020) needs nesting to create its namespaces — and starts it;
- runs `install/cmediastack-install.sh` inside it, which installs ffmpeg, a
  `cmediastack` system user, the latest release (checked against its
  `SHA256SUMS`), a master key, a self-signed certificate and a hardened systemd
  unit, and starts it.

Then:

1. **Open `https://<container-ip>:8443/setup` at once.** Whoever reaches it
   first becomes the only administrator. The browser warns about the
   self-signed certificate once.
2. **Copy the master key** somewhere that is not the container — it unlocks
   every stored credential and every backup:
   `pct exec <id> -- cat /etc/cmediastack/cmediastack.env`.
3. Sign in, enrol an authenticator, and follow **Settings → Getting started**: TMDB,
   OpenSubtitles, a SOCKS5 proxy, *Add the installer's folders*, an indexer,
   Discord. Each is an ordinary setting you can change later.

**Downloads and privacy.** There is no WireGuard namespace in this container.
Until a SOCKS5 proxy is set on the Network tab, downloads leave by the
container's own address. With one set (ADR-0065: your password and an
authenticator code, then *Restart now*), the ticked traffic goes through it or
nowhere. NordVPN's proxy does not relay UDP, so each UDP tracker is asked over
HTTP at the same address instead (the notes on Request → Downloads say which);
DHT and uTP stay off. Each download there shows its peers connected, connecting
and waiting, and its speed.

**Its address.** The certificate and base URL name the container's address.
Give the container a fixed address or a DHCP reservation; if it changes, run the
update (below) and it follows.

**Where things are**, inside the container: the binary in `/opt/cmediastack`,
configuration and the key in `/etc/cmediastack`, the database and backups in
`/var/lib/cmediastack`, media under `/media/{movies,tv,music,books,downloads}`.
`journalctl -u cmediastack` is the log.

**Updating.** Run the same one-line command inside the container
(`pct enter <id>`). It fetches the latest release, checks it, swaps it in, and
puts the old binary back if the new one does not come up within a minute.

The sections below are the Docker deployment.

## 1. The host

**Run it in a VM, not an LXC container.** Proxmox's own documentation recommends
a QEMU VM for application containers such as Docker. There is a second reason
specific to this software: the media-parser jail (ADR-0020) creates user, PID and
network namespaces, which is exactly what an unprivileged LXC container restricts;
inside a VM it just works, under the seccomp profile the compose file ships.

A VM that suits an i5-6500T:

| | |
|---|---|
| OS | Debian 12, or anything with a current Docker Engine and Compose v2 |
| CPU | 4 vCPU. Nothing here transcodes video — playback is direct play or a remux (ADR-0005) — so no GPU passthrough is needed |
| Memory | 4 GiB. The application is capped at 1 GiB by the compose file |
| Disk | 32 GiB for the system and `./config` — the database, the artwork cache and, by default, the backups |
| Time | NTP on (`timedatectl`). Authenticator codes are time-based; a clock a minute out rejects them |

Install Docker Engine and the Compose plugin from Docker's own repository, then:

```bash
sudo mkdir -p /srv/cmediastack && sudo chown "$USER" /srv/cmediastack
cd /srv/cmediastack
# copy the repository here (git clone, or the folder you keep it in)
```

### The media

The library lives on the NAS and is mounted into the VM, then into the container
at `/media`. NFS is the straightforward route:

```bash
# /etc/fstab on the VM
nas.lan:/export/media  /srv/media  nfs4  defaults,_netdev  0  0
```

**It must be writable by uid 65532**, the container's user: the application
imports into the library, renames on import, and moves deletions into a trash
folder inside each root (ADR-0016). Either `chown -R 65532:65532` on the export,
or map the client's writes to the dataset's owner on the NAS
(`all_squash,anonuid=…,anongid=…` on a Linux NFS server). A root folder that is
not writable is refused when you add it, with the reason.

**Put downloads on the same filesystem as the library**, so an import is a
hardlink and a finished download keeps seeding without taking twice the space.
The compose file mounts no download directory; if you turn downloads on, use one
inside the media mount, as a sibling of the root folders — never inside one:

```yaml
    environment:
      CMS_DOWNLOAD_ENABLED: "true"
      CMS_DOWNLOAD_DATA_DIR: "/media/downloads"
```

```bash
sudo mkdir /srv/media/downloads && sudo chown 65532:65532 /srv/media/downloads
```

If the media are on the Proxmox host's own ZFS pool rather than a NAS, export the
dataset to the VM over NFS the same way; the rules above do not change.

---

## 2. First run

**The first-run wizard is anonymous until an administrator exists.** Whoever
reaches `/setup` first creates the only administrator this instance will ever
have. So the first run happens *before* the reverse proxy is public, over an SSH
tunnel.

1. Generate the master key, and **store it somewhere that is not this host** —
   a password manager, and a copy offline:

   ```bash
   echo "CMS_MASTER_KEY=$(head -c32 /dev/urandom | base64)" > .env && chmod 600 .env
   ```

   (`make -s genkey` prints the same thing, if `make` is installed.)

   It seals every stored credential and every backup. Lose it and both are gone;
   nothing can recover them (SECURITY.md, *The master key*).

2. Start it:

   ```bash
   docker compose up -d
   docker compose logs -f cmediastack
   ```

   Read two lines before anything else: `media parser sandbox is available`
   (if it says **NOT available**, the seccomp profile is not being applied —
   check that `./deploy/seccomp-cmediastack.json` is where the compose file
   says), and the `backups` line naming the backup directory.

3. From your own machine, tunnel to it and create the administrator:

   ```bash
   ssh -L 8080:127.0.0.1:8080 you@the-vm
   # then browse http://localhost:8080/setup
   ```

   The compose file leaves `CMS_BASE_URL` at its default, `http://localhost:8080`,
   which is what makes signing in over plain HTTP on the tunnel work. Create the
   administrator, sign in, scan the authenticator code, and **store the recovery
   codes** with the master key. `/setup` then answers 404, permanently.

4. Only now: set `CMS_BASE_URL` to the public address (§3) and restart.

### Before you go public: the settings that matter

| Where | What |
|---|---|
| **Metadata** tab | Paste TMDB's *API Read Access Token* (from TMDB's API settings page). It is checked against TMDB before it is stored and is never shown again. Without it, adding titles by name and knowing what a series is missing do not work |
| **Storage** tab | Add a root folder per library — `/media/movies`, `/media/tv`. Each is checked by writing to it, and whether a hardlink from the download directory works is tested there and then |
| **Indexers** tab | Your Prowlarr or Jackett, by its Torznab address on your network (e.g. `http://192.168.1.10:9696/1/api`) and its API key (ADR-0024: the indexer's own address may be on your network; nothing a feed names may be) |
| **Search** tab | The default quality profile — *HD-1080p* on a new instance — judges every search that names none (ADR-0027) |
| **Notifications** tab | A Discord webhook, so what goes wrong reaches you (§7). In Discord: the channel's settings, *Integrations*, *Webhooks*, a new webhook, *Copy Webhook URL*. Paste it here; Discord is asked about it — nothing is posted — before it is kept, and it is never shown again |
| **Storage** tab, and who sees what | **A library is a root folder** (ADR-0037). To keep a children's library apart, give it its own roots — `/media/kids-films`, `/media/kids-tv` — and approve or invite a child's account with *Only the ones ticked* and a rating ceiling. With a ceiling, a title with no rating is hidden too: `metadata.ratings` fetches the US rating of each identified title hourly, and a home video can be rated by hand on its page. An existing account is changed on the **Users** tab, *Change access*; it applies on its next request |
| `CMS_REGISTRATION_MODE` | `open` lets anyone *request* an account, which an approver must accept; `invite` needs an invite code; `closed` removes the signup routes altogether. Open signup on a public address is the most-attacked configuration this software supports — its caps are enforced, but `invite` is the calmer choice if you know everyone who will use it |

---

## 3. The reverse proxy and TLS

The application listens on `127.0.0.1:8080` of the VM and speaks plain HTTP; the
proxy terminates TLS. Caddy obtains and renews certificates by itself:

```
# /etc/caddy/Caddyfile
media.example.com {
	reverse_proxy 127.0.0.1:8080
}
```

Then tell the application where it is and who its proxy is:

```yaml
    environment:
      CMS_BASE_URL: "https://media.example.com"
      CMS_TRUSTED_PROXIES: "172.18.0.1/32"
```

- `CMS_BASE_URL` beginning `https://` is what marks the session cookie `Secure`.
- `CMS_TRUSTED_PROXIES` is the address the proxy's connections **arrive from**,
  which is not `127.0.0.1`: Docker relays a connection to a published loopback
  port from the bridge network's gateway. Find it with
  `docker network inspect cmediastack_default -f '{{(index .IPAM.Config 0).Gateway}}'`
  (the network is named after the project directory). **Check it worked**: sign
  in through the proxy and open **Sessions** — your session should show your own
  public address. If it shows `172.x.x.x`, the forwarded header is being
  ignored and every per-address rate limit is counting the proxy as one
  client. Trust nothing wider than that one address (SECURITY.md, *Operator
  responsibilities*).
- The application sets HSTS, the CSP and the other security headers itself;
  the proxy does not need to.
- Open only 80 and 443 on the VM's firewall. The management listener
  (`/healthz`, `/readyz`, `/metrics`) is bound to loopback inside the
  container and is not published.

**An authenticating proxy in front is recommended, not assumed.** With open
signup on the internet, the signup route is where scanners land, and it spends
Argon2id time per request. Authelia or Authentik behind Caddy's `forward_auth`
means a scanner never reaches application code. Everything here works without
one; MFA is mandatory for every account regardless.

---

## 4. Downloads behind NordVPN

> **Not yet run against a real NordVPN account.** The containment was tested in
> 4j against a stand-in with gluetun's topology — the downloader refused to start
> outside the tunnel, could not flush the firewall or route around it, and
> nothing leaked when the tunnel died. The real gluetun image, a real NordVPN
> server and this exact compose layout have not been run together. Do the checks
> at the end of this section before trusting it.

**How it works today.** The download engine runs inside the application process
(`download.enabled`), and the application runs **inside gluetun's network
namespace**, so every packet it sends leaves through the WireGuard tunnel or not
at all — gluetun's firewall is the guarantee, and the application has no
capability to change it (ADR-0001). The split layout commented out in
`docker-compose.yml` — a separate `downloader` beside the application — cannot be
used yet: that role verifies the jail and runs an engine nothing can talk to,
and says so in its log.

A consequence to know: with the whole application in the tunnel, **metadata
lookups and indexer searches leave through NordVPN too.** That is the point —
a lookup says "this instance holds X" as plainly as a search says "somebody here
wants X" (ADR-0018) — and it means they pause when the tunnel is down.

### Compose

Uncomment the `gluetun` service, and change `cmediastack` to live in its
namespace: move `ports` from `cmediastack` to `gluetun`, and give `cmediastack`
`network_mode: "service:gluetun"`.

```yaml
  gluetun:
    image: qmcgaw/gluetun:v3.40.0
    cap_add: [NET_ADMIN]
    devices: [/dev/net/tun:/dev/net/tun]
    environment:
      VPN_SERVICE_PROVIDER: nordvpn
      VPN_TYPE: wireguard
      WIREGUARD_PRIVATE_KEY: ${NORD_WIREGUARD_KEY:?}
      SERVER_COUNTRIES: ${NORD_COUNTRIES:-Netherlands}
      # Your LAN, so the app can reach Prowlarr and nothing else outside the tunnel.
      FIREWALL_OUTBOUND_SUBNETS: "192.168.1.0/24"
    ports:
      - "127.0.0.1:8080:8080"
    restart: unless-stopped

  cmediastack:
    # ...everything else as before, minus ports...
    network_mode: "service:gluetun"
    depends_on:
      gluetun:
        condition: service_healthy
        restart: true
```

NordVPN's WireGuard ("NordLynx") private key comes from the manual-setup
section of your NordVPN account; gluetun's own documentation for its NordVPN
provider describes how to obtain it, and is the authority on its variables for
the version you pin. Put the key in `.env` as `NORD_WIREGUARD_KEY`.

### The application's side

```yaml
    environment:
      CMS_DOWNLOAD_ENABLED: "true"
      CMS_DOWNLOAD_DATA_DIR: "/media/downloads"
      CMS_ANONYMITY_ENABLED: "true"        # enforce: pause when the tunnel is down
      CMS_REQUIRE_NAMESPACE_GUARD: "true"
      CMS_TUNNEL_INTERFACE: "wg0"          # see below — check, do not assume
```

**Find the tunnel's interface name rather than trusting `wg0`:**

```bash
docker run --rm --network container:$(docker compose ps -q gluetun) alpine ip route get 1.1.1.1
```

The `dev` in the answer is the interface the kernel uses for the internet from
inside the namespace. Set `CMS_TUNNEL_INTERFACE` to it and restart.

Optionally set `CMS_EGRESS_PROBE_TARGET` to a public `host:port` (e.g.
`1.1.1.1:443`). Without it, the health check verifies *routing* only, which
cannot see a tunnel that is up and carrying nothing; with it, every
`probe_interval` the check also opens a connection through the tunnel — a
privacy cost you choose, which is why it is off by default.

### Check it before trusting it

1. **Network** tab: the tunnel is named, enforced, and **up**; *Run the leak
   test* says **Routes through the tunnel**, with the tunnel as the actual
   interface.
2. The address the world sees, from inside the namespace:

   ```bash
   docker run --rm --network container:$(docker compose ps -q gluetun) curlimages/curl -s https://ipinfo.io/ip
   ```

   It must be a NordVPN address, not your own.
3. Stop the tunnel (`docker compose stop gluetun`): the application becomes
   unreachable — it lives in that namespace — and nothing it had in flight
   leaves another way. Start gluetun, then **restart the application**
   (below).

### When gluetun restarts, restart the application

If gluetun's *container* is restarted or recreated — an update, a crash — the
namespace the application joined is destroyed and a new one made; the
application stays in the old one, alive and unreachable (observed in 4j). It
fails closed — nothing leaks — but nothing works either, and its own health
check, which runs inside that old namespace, still passes. `depends_on … restart:
true` covers a restart Compose itself performs; for anything else:

```bash
docker compose restart cmediastack
```

A reconnect gluetun performs *inside* its container keeps the namespace and
needs nothing.

### Letting it fetch what is wanted by itself

Off by default ([ADR-0030](adr/0030-automatic-acquisition.md)). With it on,
**everything on the Wanted screen is downloaded** when an indexer offers a
release of it that the default quality profile accepts — nobody presses Grab.
Turn it on only when:

1. **Network** says the tunnel is up and the leak test passes (above).
2. **Indexers**: each one answers its test.
3. A default quality profile is chosen — under the **Search** form, *Make this
   the default*. With none it fetches nothing, and its two tasks fail saying so.
4. **Wanted** lists what you want downloaded, and nothing else. Unmonitor what
   you do not want — on the Wanted screen, a series' seasons and episodes, or a
   film's page.

Then, in the compose file's `environment`:

```yaml
      CMS_ACQUISITION_AUTOMATIC: "true"
```

or in the configuration file, where the budget can be changed too:

```yaml
acquisition:
  automatic: true
  rss_interval: 15m      # each indexer asked once for its recent releases (at least 10m)
  search_interval: 15m   # and this often...
  searches_per_run: 3    # ...this many wanted items are searched for
  max_grabs_per_run: 5   # the most downloads one pass may start
```

and restart. The log says `automatic acquisition is on`, and **Tasks** lists
`acquire.recent`, `acquire.search`, `acquire.albums` (wanted albums whose
track list is read, ADR-0047) and `acquire.books` (wanted books with an author,
ADR-0050). None runs at start — the first passes
come one interval later — so run them from **Tasks** to see them work now.

What to expect:

- **A new episode** arrives within about one interval of an indexer listing it.
- **A back catalogue** is searched a few items at a time: twelve an hour with
  the defaults. A new series with fifty aired episodes takes a few hours to go
  through. An item whose search found nothing waits 6 hours, then 12, 24, 48,
  up to a week — its recent releases are still watched meanwhile.
- **A whole season at once** ([ADR-0033](adr/0033-season-packs.md)): a season
  that finished airing more than a week ago, none of which you have and all of
  which is monitored, is searched as one season and fetched as a pack when one
  is offered as a `.torrent` holding every episode — then imported file by
  file. Once any episode of a season is in the library, the rest of it comes
  one episode at a time. To fetch a pack of a season you partly have, use
  **Search season** on the series' page: the import takes only what is missing
  or better.
- **Wanted** says beside every item what was done about it and when it will be
  searched next; **Queue** says *automatic acquisition* for what it added; the
  audit log has every grab as `acquisition.grabbed` by `system:acquire`.

**Better copies** ([ADR-0036](adr/0036-upgrades-to-the-cutoff.md)): set
`acquisition.upgrades: true` (or `CMS_ACQUISITION_UPGRADES=true`) and restart.
A file below its title's quality cutoff — the 720p that was all there was — is
then searched for at most weekly, after everything missing; a better release
replaces it, and the old file goes to the trash, restorable from **Trash**.
An album held as MP3, AAC, Vorbis or Opus is searched the same way for a
lossless release ([ADR-0062](adr/0062-upgrading-albums-to-lossless.md)).

**A title that should be fetched differently** — the one film worth 2160p,
an old sitcom where 720p is plenty: choose its profile in the *Quality* row on
its page ([ADR-0035](adr/0035-a-quality-profile-per-title.md)). Its searches and
automatic acquisition judge it by that from then on; *The default* puts it back.

**A download that stops moving** ([ADR-0034](adr/0034-stalled-downloads.md)):
after `download.stall_after` (a day) with no progress while the instance runs,
one automatic acquisition grabbed is given up — stopped, never grabbed again —
and what it was for is searched for afresh. One a person grabbed is only marked:
**Queue** says *No progress since …*, and **Remove** lets another release be
found. The partial data stays in the download directory either way.

To refuse one download, **Remove** it from the queue: that release is never
grabbed again, but what it was for is still wanted and another release will be
looked for. To stop fetching an item at all, **Unmonitor** it. To stop
everything, set `CMS_ACQUISITION_AUTOMATIC` to `false` and restart.

---

## 5. Backups and restores

What is backed up, how it is protected and why: ADR-0029 and SECURITY.md,
*Backups*. What to do:

### Where they go

By default, `/config/backups` — on the VM's disk, with the database. That
survives a bad upgrade or a mistake, not the loss of that disk. **Point them at
the NAS instead**, or copy them there:

```yaml
    environment:
      CMS_BACKUP_DIR: "/backups"
    volumes:
      - /srv/media/cmediastack-backups:/backups:rw
```

```bash
sudo mkdir -m 0700 /srv/media/cmediastack-backups
sudo chown 65532:65532 /srv/media/cmediastack-backups
```

The directory must exist first; a named one is never created, because a missing
one is usually an unmounted share. If the share was not mounted when the
container started, Docker made an empty directory on the VM's disk instead: the
**Backups** tab says when backups share the database's filesystem. Copying off
is also fine — `rsync -a ./config/backups/ nas:/backups/cmediastack/` from cron —
the files are encrypted and safe to put anywhere.

### Checking one

The **Backups** tab lists them. To check a copy — any copy, wherever it has been:

```bash
docker compose exec cmediastack /usr/local/bin/cmediastack \
  -verify-backup /config/backups/cmediastack-20260927T125201.275Z.db.age
```

It decrypts into a private temporary directory, runs SQLite's checks and the
migration comparison, prints what the backup holds and its SHA-256 — which the
audit log's `system.backup.created` line for it also carries — and removes the
plaintext. Do it once a month; a backup nobody has checked is a hope. (The
container's `/tmp` is 256 MiB; for a larger database run it with
`-e TMPDIR=/config/tmp` after creating that directory.)

### Restoring

1. Take one more backup first (**Backups → Back up now**) if the instance runs
   at all: a restore rolls back everything since the backup.
2. Write the restored database to a new file. With the server still running:

   ```bash
   docker compose exec cmediastack /usr/local/bin/cmediastack \
     -restore-backup /config/backups/<file> -restore-to /config/restored.db
   ```

   Or, with it stopped: `docker compose run --rm cmediastack --config
   /config/config.yaml -restore-backup /config/backups/<file> -restore-to
   /config/restored.db`.

   It refuses a target that exists, and a backup taken by a newer build. It
   **ends every session and revokes every API token** in the restored copy, and
   writes the restore into its audit log. It prints the rest.
3. Put it in place — on the host, with the server stopped. Move the old
   database **and its `-wal` and `-shm`**: a `-wal` left beside the restored
   file would be replayed into it.

   ```bash
   docker compose stop cmediastack
   sudo mkdir ./config/replaced
   sudo mv ./config/cmediastack.db ./config/cmediastack.db-wal ./config/cmediastack.db-shm ./config/replaced/ 2>/dev/null
   sudo mv ./config/restored.db ./config/cmediastack.db
   docker compose start cmediastack
   ```

   Do not open `restored.db` with another tool before moving it: it is in WAL
   mode, and a read-only open creates `-wal` and `-shm` beside it.
4. Sign in again — everyone has to — and review **Accounts**: anyone suspended
   since the backup is active again in it, and passwords changed since are the
   old ones.

An older backup is fine: the server applies the migrations it lacks when it
starts. A backup from a *newer* build needs that build.

### If this software is gone

A backup is a standard age file, so it does not need CMediaStack to be
readable. You need the master key and the `age` tool. Derive the passphrase —
either way; they agree:

```bash
# Python, standard library only; reads the key from the environment
export CMS_MASTER_KEY='<the key>'
python3 -c 'import base64,hmac,hashlib,os;k=base64.b64decode(os.environ["CMS_MASTER_KEY"]);p=hmac.new(bytes(32),k,hashlib.sha256).digest();print(base64.urlsafe_b64encode(hmac.new(p,b"cmediastack backup v1\x01",hashlib.sha256).digest()).decode().rstrip("="))'

# or OpenSSL 3 (puts the key on the command line: do it on your own machine)
openssl kdf -keylen 32 -kdfopt digest:SHA256 \
  -kdfopt hexkey:"$(printf %s "$CMS_MASTER_KEY" | base64 -d | od -An -tx1 | tr -d ' \n')" \
  -kdfopt info:"cmediastack backup v1" -binary HKDF | base64 | tr '+/' '-_' | tr -d '=\n'
```

Then decrypt, and paste the passphrase when `age` asks for it:

```bash
umask 077
age -d -o cmediastack.db cmediastack-20260927T125201.275Z.db.age
```

What comes out is the SQLite database. Tested with the `age` 1.1.1 that Ubuntu
ships, against a backup written by the library's 1.3.2.

---

## 6. Upgrading

```bash
cd /srv/cmediastack
# update the repository, then:
docker compose build --pull
```

1. **Backups → Back up now**, and note the file name.
2. `docker compose up -d`. Migrations apply at startup, each in its own
   transaction; the log says `applied migrations` with their numbers.
3. If the new build misbehaves: go back to the **old** image and restore the
   backup from step 1 with it (§5). A backup taken by the new build carries
   migrations the old one does not know, and the old build refuses it — which
   is why step 1 is before the upgrade.

---

## 7. What to watch

| Where | What it tells you |
|---|---|
| **Tasks** tab | Every scheduled job: last run, next run, and failures in red. `database.backup` failing means no new backups — the message says why (an unmounted directory, a full disk, a snapshot that failed its checks) |
| **Backups** tab | The newest backup and when the next is due; a warning when they share the database's disk |
| **Network** tab | Whether the tunnel is required and up, and the leak test |
| **Wanted** tab | With automatic acquisition on: what was done about each item, why nothing was grabbed, and when it is searched next |
| **Metadata** tab | Whether TMDB still accepts the key |
| **Audit log** tab | Everything recorded about who did what, newest first: sign-ins and failures, denials, account and role changes, every grab — a person's or `system:acquire`'s — and every settings change. The first line counts the last week by kind |
| **Notifications** tab, and your Discord channel | What reaches you without looking. Where it posts, when it last sent, and whether Discord is refusing it |
| `docker compose logs cmediastack` | JSON lines. The compose file rotates them at 10 MB × 5. `CMS_LOG_LEVEL=debug` for more |
| The container's health | `docker compose ps` — the probe asks `/readyz`, which pings the database |

**Reading the audit log.** The **Audit log** tab filters by kind, outcome, who
and any text — an address, a release name — and pages back with *Older*.
Everything in it is shown as it was recorded, and much of it (user agents,
usernames at signup, release names) was written by whoever made the request.
A run of `authz.denied` from one address is an attack or a bug; both are worth
knowing about (SECURITY.md, *Operator responsibilities*).

Anybody can make the instance write an `authz.denied` line by asking for a
protected page without signing in, so those lines have a ceiling: 20 an hour
from one address, 120 an hour from every address together, 60 an hour from one
account. The rest are counted, and once the hour is over one line —
`authz.denied.suppressed`, by `system:audit` — says how many there were and
from where. The `audit.denials` task writes it within five minutes of the hour
ending; a stop the container is told about (`docker compose stop`, an upgrade)
writes the hour so far. **One address in every line** means
`CMS_TRUSTED_PROXIES` is wrong (§3), and everyone shares one ceiling.

For a script, `GET /api/v1/admin/audit` answers the same filters as JSON
(`category`, `action`, `outcome`, `actor`, `target`, `q`, `since`, `until`,
`limit` up to 200, and `before` from the previous page's `next_before`), to an
API token scoped to `admin.audit`. When the application will not start, read it
from the database instead — read-only (`apt install sqlite3` on the VM):

```bash
sudo sqlite3 -readonly ./config/cmediastack.db \
  "SELECT occurred_at, actor_label, action, outcome, detail FROM audit_event ORDER BY id DESC LIMIT 50;"
```

**Notifications** (ADR-0032). Once a Discord webhook is set, a task checks the
audit log every minute and posts what the categories say, at most three
messages a minute:

- **Security** (on): denials over the ceiling; an account refused a route;
  sign-ins throttled; *password accepted, authenticator code refused* —
  somebody has that password; a recovery code used; an authenticator enrolled;
  a password changed or a reset link made; an API token issued; recovery from
  the host; roles, suspensions and grants; settings and indexers changed; a
  restore.
- **Accounts** (on): an account request waiting for approval; an invite used.
- **Operations** (on): a scheduled task that starts failing, and when it
  recovers — *The tunnel is down*, *Backups are failing* — and the kill switch.
- **Library** and **Requests** (off): grabs, arrivals, deletions; requests made
  and answered. Off because they send titles to Discord.

Successful sign-ins are never sent: a message for every one teaches you to
ignore the channel. Anything a stranger could have written arrives in a code
span, and no message can ping the server. *Send a test message* shows where
messages land.

With the whole application inside gluetun's namespace (§4), nothing leaves
while the tunnel is down — *The tunnel is down* included. It arrives with *The
tunnel is back*.

**Metrics** (`/metrics`, Prometheus text) are on the management listener, bound
to loopback inside the container, and deliberately not published. To scrape
them, bind `CMS_MANAGEMENT_ADDR` to an address Prometheus can reach on a
network only it shares — the configuration lint refuses all-interfaces
(`0.0.0.0`), not a specific address.

---

## 8. When something breaks

**Start at the Health tab** (ADR-0040): every check at once — database, egress,
tasks, backups, each root folder's space, the parser sandbox, the metadata
provider — each with a sentence, and the process's recent log records beneath,
filtered by level and text. The same report is `GET /health/detail` for a
script. It is not a substitute for the host's log: the records start at the
process's start and are gone when it stops.

| Symptom | Cause, and what to do |
|---|---|
| The container exits at once with `insecure or invalid configuration` (exit 78) | The lint's list says every problem at once. Fix them all, then restart |
| `Permission denied` adding a root folder | The mount is not writable by uid 65532 (§1) |
| `media parser sandbox is NOT available` | The seccomp profile is not applied, or the host forbids user namespaces. Playback works, without the jail |
| `media tools are MISSING` | The image lacks ffmpeg — it should not; rebuild from the Dockerfile |
| Authenticator codes are refused | The VM's clock. `timedatectl` should say synchronised |
| Lost the authenticator **and** the recovery codes | Break-glass recovery from the host — below. It needs the master key, and is audited as `user.recovered` |
| `database.backup` fails: *does not exist … not mounted* | The named backup directory is missing: mount the share, or create the directory (§5) |
| `database.backup` fails: *failed its checks* | The live database has a problem SQLite can see. Run the `database.integrity` task from **Tasks**; if it fails too, restore the newest good backup (§5) |
| `-verify-backup`: *not taken with this master key* | `CMS_MASTER_KEY` is not the key of the instance that took it |
| `-verify-backup`: *damaged* | The copy was changed or cut short after it was written. Use another copy; compare SHA-256s with the audit log to find a good one |
| Downloads paused, **Network** says the tunnel is down | gluetun's log first (`docker compose logs gluetun`). If gluetun's container restarted, restart the application (§4) |
| After gluetun restarted, the site is unreachable | The application is stranded in the old namespace: `docker compose restart cmediastack` (§4) |
| Every visitor shows as one address; everyone is rate-limited together | `CMS_TRUSTED_PROXIES` is unset or wrong (§3) |
| Notifications: *Discord does not know this webhook* | It was deleted in Discord, or the link was mistyped. Nothing is sent until a new one is saved — nothing was lost: it starts where it stopped |
| Notifications: *Discord asked to wait* | Discord is rate-limiting the webhook. It waits as long as asked, then carries on |
| Notifications: nothing arrives, no error | The categories: *Library* and *Requests* are off by default. *Send a test message* to see where messages land |
| `authz.denied.suppressed` lines in the audit log | More denials in an hour than the ceiling writes one by one (§7). The line names the busiest sources: one unfamiliar address is a scanner, and the reverse proxy or a firewall is the place to stop it; your own address is a bookmark or a script asking for something it may not |
| Searches for titles say no provider is configured | Set the key on the **Metadata** tab |
| `acquire.recent` and `acquire.search` fail: *there is no default quality profile* | Choose one under the **Search** form (*Make this the default*). Without one, automatic acquisition fetches nothing (§4) |
| They say *stopped before asking any indexer: … the tunnel is not verified* | The tunnel (§4). Nothing is asked until it is back; nothing needs doing to them |
| `acquire.search` fails: *no indexer answered* | Every indexer is down or refusing: the **Indexers** tab says which, and why |
| Something wanted is never grabbed | Its line on **Wanted** says why. *None of them …*: the indexers have nothing under that name — search by hand with another name. *Refuses it*: the default profile. *No seeders*: wait. *Its name also fits …*: two titles in the library share a name, and only a person can tell which a release is — grab it by hand from its **Search**. *Named in …*: the release names a language — a dub, or a title not in English; grab the one you want by hand |
| A download automatic acquisition started is not wanted | **Remove** it (that release is never grabbed again), and **Unmonitor** the item, or another release will be fetched |

### Break-glass recovery

An instance has one administrator (SECURITY.md, *Break-glass recovery*). If its
authenticator and recovery codes are both lost:

```bash
docker compose exec cmediastack /usr/local/bin/cmediastack -recover jacob
```

That clears the account's authenticator, recovery codes, sessions and API
tokens, and leaves it waiting to enroll again at the next sign-in. To replace
the password as well, pass it on stdin — never as an argument, where it would
land in the shell history and in `ps`:

```bash
read -rs P && printf '%s\n' "$P" | docker compose exec -T cmediastack \
  /usr/local/bin/cmediastack -recover jacob -recover-password; unset P
```

## Rotating the master key

When the key may have leaked, or on whatever schedule you keep (ADR-0054):

```sh
docker compose stop cmediastack
export CMS_MASTER_KEY_NEW=$(head -c32 /dev/urandom | base64)   # or: make genkey
docker compose run --rm -e CMS_MASTER_KEY_NEW cmediastack -rotate-key
```

It refuses while the server is running, and changes nothing unless every
stored secret opens under the current key. Then put the new key where the
server reads `CMS_MASTER_KEY`, start it, and **keep the old key with your
existing backups** — they stay encrypted under it.

# Proxmox deployment and guided setup — design

*Agreed in conversation on 2026-10-03. Phase 7.*

## What the operator asked for

- CMediaStack on GitHub, public: <https://github.com/Jakethecake75/cmediastack>.
- Installed the way the community-scripts ("tteck") Proxmox helpers install an
  app: one line pasted into the Proxmox host shell, a few prompts, and an LXC
  container running the app; the same line run inside that container updates it.
- Everything that needs a login or a key — the SOCKS5 proxy, TMDB,
  OpenSubtitles, and the rest — asked for on the web page during first setup,
  not in a file.

Decided along the way:

| Question | Answer |
|---|---|
| Repository visibility | Public — the one-line install and the release download need no token |
| SOCKS5 from the web | **Editable at any time**, guarded: password and a fresh authenticator code, audited, announced on Discord, applied on restart. This replaces ADR-0013's "egress is file-only" for the proxy alone |
| Where the media lives | **Inside the container**, on its own disk |
| How the container gets the app | A release binary built by GitHub Actions (approach A); not a build in the container, not Docker in the LXC |

## 1. The repository and its releases

- `.gitattributes` keeps every text file LF (done with the first commit).
- `.github/workflows/release.yml`, on a tag `v*`: runs the tests, builds
  `cmediastack-linux-amd64` and `cmediastack-linux-arm64` with the Dockerfile's
  flags (`CGO_ENABLED=0 -trimpath -buildvcs=true -ldflags "-s -w -X
  main.Version=<tag>"`), writes `SHA256SUMS`, and attaches all three to a GitHub
  release. The tests gate the release: a tag whose tests fail publishes nothing.
- The Docker deployment stays as it is, beside this one.

## 2. `ct/cmediastack.sh` — on the Proxmox host

Run as `bash -c "$(curl -fsSL https://raw.githubusercontent.com/Jakethecake75/cmediastack/main/ct/cmediastack.sh)"`.

community-scripts' shared `build.func` cannot be reused: it fetches the
in-container script from *their* repository by app name. This script follows
the same shape, self-contained:

- **Refuses** when not run as root on a Proxmox VE host (`pveversion` present)
  unless it is inside an existing CMediaStack container, where it updates (below).
- **Prompts** with `whiptail`, as tteck's do. *Default settings* takes all of:
  next free container ID, hostname `cmediastack`, Debian 12, unprivileged,
  `nesting=1`, 2 cores, 2048 MB RAM, **64 GB disk** (the media is inside — the
  prompt says so), `vmbr0` with DHCP, the first storage that holds container
  root disks. *Advanced settings* asks for each of those.
- **Creates** the container: downloads the Debian 12 template with `pveam` if
  missing, `pct create` with the answers, `pct start`, waits up to 60 s for an
  IPv4 address and for DNS to resolve `github.com`.
- **Installs** by running `install/cmediastack-install.sh` inside it with
  `pct exec`, fetched from the same branch as this script.
- **Finishes** by printing `https://<ip>:8443/setup`, that the certificate is
  self-signed (the browser warns once), and that whoever opens `/setup` first
  becomes the only administrator — so open it now.

**Update**, the same line run inside the container (detected by
`/opt/cmediastack/cmediastack` existing): fetch the latest release, verify it
against `SHA256SUMS`, compare with the installed version and stop if equal,
`systemctl stop`, swap the binary (the old one kept as `cmediastack.old`),
`systemctl start`, and wait for `/readyz`. If the new binary does not come up
ready within 60 s, the old one is put back and started, and the script says so.
The app's own pre-upgrade backup and migrations run at start as they do today.

## 3. `install/cmediastack-install.sh` — inside the container

- `apt-get install` `ffmpeg`, `curl`, `ca-certificates`, `openssl`.
- A system user and group `cmediastack`, no shell, no home.
- The latest release binary to `/opt/cmediastack/cmediastack`, verified against
  `SHA256SUMS`; the architecture from `dpkg --print-architecture`.
- `/etc/cmediastack/cmediastack.env`, root:cmediastack, mode 0640:
  `CMS_MASTER_KEY` (32 random bytes, base64). The script prints a warning that
  this key unlocks every stored credential and backup and must be copied
  somewhere that is not this container. It is **not** printed.
- `/etc/cmediastack/config.yaml` from `config/config.example.yaml`'s values, with:
  `server.addr: 0.0.0.0:8443`, `server.base_url: https://<ip>:8443`,
  `server.tls_cert_file`/`tls_key_file` pointing at a self-signed certificate
  (`openssl req -x509`, EC P-256, ten years, SANs for the IP and the hostname),
  `database.path: /var/lib/cmediastack/cmediastack.db`,
  `download.data_dir: /media/downloads`, `download.enabled: true`, and
  `egress.anonymity_enabled: false` with `egress.require_namespace_guard:
  false` — there is no tunnel or namespace in this container; SOCKS5 set from
  the web is its egress control, and a socks5 profile fails closed by itself.
  The script says that until a proxy is set, downloads leave by the
  container's own address.
  TLS is not optional here: the app marks its session cookie `Secure` unless
  the base URL is `http://localhost`, so plain HTTP on a LAN address cannot sign
  in at all.
- `/var/lib/cmediastack` (0700) and `/media/{movies,tv,music,books,downloads}`,
  owned by `cmediastack`.
- `cmediastack.service`: `User=cmediastack`, `EnvironmentFile=`,
  `Restart=always`, `NoNewPrivileges=yes`, `ProtectSystem=strict` with
  `ReadWritePaths=/var/lib/cmediastack /media`, `ProtectHome=yes`,
  `PrivateTmp=yes`, `CapabilityBoundingSet=` (empty). **Not** `RestrictNamespaces`
  or `PrivateUsers`: the media-parser jail creates user, PID and network
  namespaces (ADR-0020), which is also why the container needs `nesting=1`.
- Starts it and waits for `/readyz`; then reads the journal for
  `media parser sandbox is available` and, if it says NOT available, prints why
  that matters and that `nesting=1` is the usual cause.

## 4. Guided setup — *Getting started*

After `/setup` creates the administrator and they enrol an authenticator, the
browser goes to a new admin page, **Getting started**. It stays in the admin
menu afterwards; nothing records that it was "finished", because every step is
an ordinary setting that can be changed later.

It is a checklist, not a second copy of each form: each step reads the
setting's own status and shows whether it is done, and its button opens the tab
that already sets it. Skipping a step is not opening it. The steps:

1. **TMDB** — the API Read Access Token (Metadata tab).
2. **OpenSubtitles** — key, account, languages (Metadata tab).
3. **SOCKS5 proxy** — §5 (Network tab, new).
4. **Libraries** — root folders (Storage tab). This step alone also offers one
   button that adds the four library folders the install created —
   `/media/movies`, `/media/tv`, `/media/music`, `/media/books` — through
   `POST /api/v1/admin/rootfolders`, skipping any already added or absent.
   `/media/downloads` is the download directory, not a library.
5. **An indexer** — Torznab/Newznab/Cardigann (Indexers tab).
6. **Discord** — the webhook (Notifications tab).

MusicBrainz and Open Library need no account, and the page says so rather than
leaving the reader to wonder.

## 5. SOCKS5 from the web — ADR-0065

Supersedes ADR-0013's refusal of runtime egress changes for the proxy, and
nothing else: the tunnel
interface, the namespace guard, the kill switch and the probe stay in the file,
and `PATCH /api/v1/admin/egress` still answers 409.

**What is stored**, in the `setting` table beside the other web-set
credentials (no migration): the proxy's `host:port`, a username, the password
sealed with the master key (context `egress:proxy`, added to `rotatedKinds`),
and which profiles use it — `download`, `indexer`,
`metadata`, `subtitle`. `download` is ticked by default; `notification` and
`update` are never offered.

**Changing it** — `PUT /api/v1/admin/egress/proxy`, `PermManageNetwork`:

- The body carries the administrator's **password and a current authenticator
  code**, both checked, as at sign-in, against the account making the request;
  a failure is audited and counts toward the account's lockout like a failed
  sign-in. A stolen session cookie alone cannot change it.
- The address must be a hostname or address and port; a private address is
  accepted (a proxy on the LAN is legitimate) — the profiles it carries then
  reach only it.
- Every change is audited (`egress.proxy.changed`, old and new address, never a
  password) and **sent to Discord whatever the category settings say** — the
  operator learns of a change they did not make.
- The address, username, profiles and the overlaid result are checked by the
  same security lint the boot runs, before anything is stored: a combination
  the lint refuses (a proxied indexer with direct metadata, say) is refused
  here with the lint's own words, not discovered at the next start.
- The response says the change applies on restart. The existing
  `GET /api/v1/admin/egress` gains a `proxy` object: what is stored (never the
  password), what is in force now, whether they differ, and which profiles the
  file set. Clearing it is the same call
  with the proxy removed.

**Applying it** — at boot, after the config file and environment are read and
before the security lint: each ticked profile the file leaves `direct` becomes
`socks5` with the stored address and credentials and `remote_dns: true`. A
profile the file sets to anything else keeps the file's setting, and the screen
names it as set in the file. The lint then runs on the result. If it refuses
— the file changed since the proxy was saved — the ticked profiles are set to
`blocked` instead, the lint's words are logged at ERROR and shown in the
`proxy` status, and the app starts: traffic meant for the proxy goes nowhere,
never direct, and the screen that fixes it stays reachable. A proxy that cannot
be reached fails closed the same way, as every socks5 profile does today.
The overlay does not touch `anonymity_enabled` or the kill switch.

**Restart** — `POST /api/v1/admin/system/restart`, admin-only, audited: the
same graceful stop as SIGTERM, then exit status 3 so that `Restart=always`
(systemd) and `restart: unless-stopped` (Docker) bring it back. The SOCKS5
screen offers it after a save.

**Known consequence, stated in the UI:** a proxied `indexer` profile cannot
reach an indexer on the LAN — the proxy is not on the LAN (ADR-0024). The step
says so beside the tick box.

## 6. Testing

- Go, as every increment: `identity`/`api` tests for the password-and-code
  check (wrong password, wrong code, replayed code, lockout counting), the
  overlay (ticked and unticked, file-set profiles win, the lint still refuses an
  unsafe result), sealing and rotation, the forced notification, the restart;
  mutation-verified; the structural tests (route lists, rotation counts) updated.
- A live check of the Go side on the running binary: save a proxy through the
  web, restart, and see a download profile's traffic go to a SOCKS5 stand-in.
- Shell: `shellcheck` on both scripts. The install script run in a Debian 12
  environment end to end (systemd, the TLS sign-in, the sandbox line).
- **The host script cannot be run here — there is no Proxmox.** Its first real
  run is the operator's; it is written so that each step names what it is doing
  and stops on the first failure with the command that failed.
- The release workflow is checked by pushing a `v0.1.0` tag and installing from
  the release it publishes.

## Not built

A Proxmox VM variant; Docker inside the LXC; Let's Encrypt or any public
certificate (behind the operator's own reverse proxy, RUNBOOK §3 applies); a
WireGuard tunnel inside the LXC — SOCKS5 is this deployment's egress control,
with ADR-0001's limits on it (TCP only, no DHT or uTP).

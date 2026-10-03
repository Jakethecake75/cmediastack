# Proxmox deployment and guided setup — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One line on a Proxmox host makes an LXC running CMediaStack, and the web asks for every login and key — including a SOCKS5 proxy — after first sign-in.

**Architecture:** A new `internal/egressproxy` package stores the proxy in the `setting` table (password sealed), checks a change with the existing config lint, and overlays it onto the egress profiles at boot. A re-authentication method in `identity` guards the change; a restart route applies it. GitHub Actions publishes static binaries on a tag; `ct/` and `install/` shell scripts in the community-scripts shape fetch and run them.

**Tech Stack:** Go 1.26 (Linux, `CGO_ENABLED=0`), SQLite, vanilla JS SPA, bash + whiptail, systemd, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-10-03-proxmox-deploy-and-guided-setup-design.md`

## Global Constraints

- Every increment: ADR first; mutation-verified tests (`mutate.py`); a live check on the running binary; PROGRESS, ESTIMATES, SECURITY, DROPPED-FEATURES updated; gates green (vet, lint, gosec, govulncheck, race, all tests, docs tests).
- Go is vetted with `GOOS=linux CGO_ENABLED=0 go vet ./...` on Windows and tested in WSL (`scratchpad/w.sh go test …`).
- Seal context for the proxy password: `egress:proxy`. Setting keys: `egress.proxy` (JSON, no password) and `egress.proxy.password` (base64 of the sealed password).
- Profiles the proxy may carry: `download`, `indexer`, `metadata`, `subtitle` — `download` ticked by default; never `notification` or `update`.
- Restart exit status: **3**. HTTPS port in the LXC: **8443**. Disk default: **64 GB**. Template: Debian 12. Container: unprivileged, `nesting=1`.
- `PATCH /api/v1/admin/egress` still answers 409.
- Commit after each task with the attribution line; push only at the end, after the operator says so.

## Review Focus

1. **The proxy is unreachable or rejects the login** — traffic for its profiles fails, never goes direct. Pinned by Task 2's `TestATickedProfileIsNeverLeftDirect`.
2. **The config file changes after a proxy was saved so the overlay no longer passes the lint** — the app still starts, those profiles are `blocked`, the status says why. Task 2 `TestAnOverlayTheLintRefusesBlocksInsteadOfBricking`.
3. **Clearing the proxy** — the sealed password goes too; nothing stale is overlaid. Task 2 `TestClearingTheProxyRemovesItsPassword`.
4. **A replayed authenticator code, or a recovery code, offered to re-authenticate** — refused. Task 1 `TestReauthenticationRefusesAReplayedCodeAndRecoveryCodes`.
5. **The container's DHCP address changes** — the base URL and certificate no longer match and sign-in breaks. The update path re-checks the address and rewrites both. Task 8 `ensure_address` check.

---

### Task 1: Re-authentication

**Files:**
- Modify: `internal/identity/reset.go` (beside `RegenerateRecoveryCodes`)
- Test: `internal/identity/reauth_test.go`

**Interfaces:**
- Produces: `func (svc *Service) Reauthenticate(ctx context.Context, password, code, sourceIP, userAgent string) error` — the principal from `authz.FromContext(ctx)`; returns `nil`, `ErrLoginFailed`, or `ErrThrottled`.

- [ ] **Step 1: Write the failing tests** in `reauth_test.go`, using the package's existing enrolled-admin fixture:
  - `TestReauthenticationNeedsBothThePasswordAndACode` — right password + current code → `nil`; wrong password → `ErrLoginFailed`; right password + wrong code → `ErrLoginFailed`; no principal in ctx → `ErrLoginFailed`.
  - `TestReauthenticationRefusesAReplayedCodeAndRecoveryCodes` — the same code twice in one step → second is `ErrLoginFailed`; an unused recovery code as `code` → `ErrLoginFailed`, and it is still unused afterwards.
  - `TestReauthenticationFailuresCountTowardLockout` — `policy.LoginMaxAttempts` wrong passwords → the next call, even correct, is `ErrThrottled`; a wrong password writes an `audit.ActionLoginFailed` failure line, a wrong code an `audit.ActionMFAFailed` one.
- [ ] **Step 2: Run** `w.sh go test ./internal/identity/ -run Reauth` — FAIL (undefined).
- [ ] **Step 3: Implement** — check `RecentFailures("login", "user:<lower username>")` against `LoginMaxAttempts`; `VerifyPassword` (failure: `RecordAttempt("login", userKey, false)` + `ActionLoginFailed` audit, as `ChangePassword` does); then `VerifyTOTP` + `ConsumeTOTPCounter` exactly as `VerifyMFA` does, with **no** recovery-code fallback (failure: `RecordAttempt("mfa", "mfa:<id>", false)` + `ActionMFAFailed`).
- [ ] **Step 4: Run** the tests — PASS. Mutate: drop the throttle check, the counter consumption, the password check.
- [ ] **Step 5: Commit** `identity: re-authentication with a password and a fresh code`.

### Task 2: `internal/egressproxy` — store, overlay, validation

**Files:**
- Create: `internal/egressproxy/proxy.go`, `internal/egressproxy/proxy_test.go`
- Modify: `internal/platform/config/config.go` (`EgressProfile`), `internal/platform/config/lint.go:150`, `cmd/cmediastack/main.go:1288` (`egressProfiles`)

**Interfaces:**
- Produces:
  - `type Setting struct { Address, Username, Password string; Profiles []string }` — `Password` is write-only: `Load` fills it, JSON for status never carries it.
  - `var Offered = []string{"download", "indexer", "metadata", "subtitle"}`
  - `const PlainSetting = "egress.proxy"; const SealedSetting = "egress.proxy.password"; const SealedContext = "egress:proxy"`
  - `type SettingStore interface { Setting(ctx, key string) (string, error); SetSetting(ctx, key, value string) error }` (what `identity.Store` already satisfies)
  - `func NewStore(s SettingStore, c *secrets.Cipher) *Store`; `(*Store) Load(ctx) (Setting, bool, error)`; `(*Store) Save(ctx, Setting) error` — an empty `Address` clears both keys.
  - `type Status struct { Stored, InForce Setting; Differs bool; FileSet []string; Problem string }` (passwords blanked)
  - `func Apply(base config.Config, s Setting, getenv func(string) string) (config.Config, Status)` — the boot overlay.
  - `func Validate(base config.Config, s Setting, getenv func(string) string) error` — the save-time check.
  - `config.EgressProfile.Password string \`yaml:"-"\`` — set only by the overlay.

- [ ] **Step 1: Write the failing tests** in `proxy_test.go`:
  - `TestAProxyIsStoredSealedAndLoaded` — `Save` then `Load` round-trips; the raw `egress.proxy` value does not contain the password; `egress.proxy.password` decrypts only with context `egress:proxy`.
  - `TestClearingTheProxyRemovesItsPassword` — `Save(Setting{})` after a save → `Load` reports not set and both keys are empty.
  - `TestTickedDirectProfilesBecomeSocks5` — base with all four `direct`; ticked `download`,`metadata` → those are `socks5`, address/username/`Password` set, `RemoteDNS: true`; `indexer` stays `direct`.
  - `TestAProfileTheFileSetKeepsTheFilesSetting` — base `download: blocked`, ticked `download` → stays `blocked`; `Status.FileSet == ["download"]`.
  - `TestATickedProfileIsNeverLeftDirect` — for every subset of `Offered`, after `Apply` no ticked profile has mode `direct`.
  - `TestAnOverlayTheLintRefusesBlocksInsteadOfBricking` — base with `anonymity_enabled: true`, `require_namespace_guard: false`, ticked `indexer` only → lint refuses (indexer proxied, metadata direct); the ticked profiles are `blocked`; `Status.Problem` holds the lint's text; `config.Lint` passes on the result.
  - `TestValidateRefusesWhatTheLintRefuses` — `indexer` without `metadata`/`subtitle` → error containing `metadata`; `Address` not `host:port` → error; a profile outside `Offered` → error; `download` alone → `nil`.
  - In `internal/platform/config/lint_test.go`: `TestAnOverlaidPasswordSatisfiesTheUsernameRule` — `Username` set, `PasswordEnv` empty, `Password` set → no complaint; neither → the existing complaint.
- [ ] **Step 2: Run** `w.sh go test ./internal/egressproxy/ ./internal/platform/config/` — FAIL.
- [ ] **Step 3: Implement.** Lint line 150 becomes `prof.Username != "" && prof.PasswordEnv == "" && prof.Password == ""`. `egressProfiles` uses `p.Password` when non-empty, else `os.Getenv(p.PasswordEnv)`. `Apply` copies `base.Egress.Profiles` (never mutates the caller's map), overlays, runs `config.Lint(out, getenv)`, and on refusal sets the ticked-and-overlaid profiles to `config.EgressBlocked` and records the problem. `Validate` checks shape, then refuses if `Apply` reported a problem.
- [ ] **Step 4: Run** — PASS. Mutate: the `direct`-only condition, `RemoteDNS: true`, the blocked fallback, the clear path, the lint rule's new clause.
- [ ] **Step 5: Commit** `egressproxy: a SOCKS5 proxy stored from the web and overlaid at boot`.

### Task 3: Boot wiring, rotation, restart

**Files:**
- Modify: `cmd/cmediastack/main.go` (`main`, `runApp` after `database.Migrate`), `cmd/cmediastack/rotate.go:43-104`, `cmd/cmediastack/rotate_test.go:252`
- Test: `cmd/cmediastack/restart_test.go`

**Interfaces:**
- Consumes: Task 2's `NewStore`, `Apply`.
- Produces: `var errRestart = errors.New("restart requested")`; `api.Deps.Restart func()`; `api.Deps.Proxy *egressproxy.Controller` where `func NewController(store *Store, base config.Config, getenv func(string) string, inForce Status) *Controller` with `Status(ctx) (Status, error)` and `Save(ctx, Setting) error` (Validate, then Store.Save) — add these two to `proxy.go` in this task.

- [ ] **Step 1: Write the failing tests:**
  - `TestARestartRequestEndsRunWithErrRestart` — drive the helper that wraps the signal context (`restartable(ctx) (context.Context, func(), func() bool)`): calling the returned trigger cancels the context and reports requested; not calling it reports not requested.
  - `TestExitStatusForARestartIsThree` — `exitCode(errRestart) == 3`, `exitCode(lintErr) == 78`, `exitCode(other) == 1`, `exitCode(nil) == 0`.
  - Extend `TestEverySealedValueIsRotated`'s map with `"egressproxy/proxy.go": 1`, and the rotation round-trip test with a stored proxy password.
- [ ] **Step 2: Run** `w.sh go test ./cmd/cmediastack/` — FAIL.
- [ ] **Step 3: Implement.** `main` calls `os.Exit(exitCode(err))` with the existing messages. In `runApp`, after migrate: load the stored proxy, `cfg, status = egressproxy.Apply(cfg, stored, os.Getenv)`, log `status.Problem` at ERROR when set, keep the pre-overlay `cfg` for `NewController`. Wrap the signal context with `restartable`; after graceful shutdown return `errRestart` when requested. Rotation: append `"SOCKS5 proxy password"` to `rotatedKinds` (index 7) and give the settings loop an explicit `kind` per entry instead of `rotatedKinds[2+i]`.
- [ ] **Step 4: Run** — PASS; mutate the exit mapping and the rotation entry.
- [ ] **Step 5: Commit** `main: overlay the stored proxy at boot; restart on request`.

### Task 4: API — save the proxy, restart, notify

**Files:**
- Modify: `internal/api/egress.go`, `internal/api/routes.go:309-311,339-352`, `internal/api/auth.go:90` (`Deps`), `internal/platform/audit/audit.go`, `internal/notify/catalog.go`, `internal/notify/service.go:483`
- Create: `internal/api/restart.go`
- Test: `internal/api/egress_proxy_test.go`, `internal/notify/service_test.go`

**Interfaces:**
- Consumes: Task 1 `Reauthenticate`; Task 3 `Deps.Proxy`, `Deps.Restart`.
- Produces: routes `PUT /api/v1/admin/egress/proxy` (`rt.Admin`, `authz.PermManageNetwork`) and `POST /api/v1/admin/system/restart` (`rt.Admin`, `authz.PermSystemSettings`); `audit.ActionEgressProxyChanged = "egress.proxy.changed"`, `audit.ActionSystemRestarted = "system.restarted"`; `GET /api/v1/admin/egress` body gains `"proxy": Status`.

- [ ] **Step 1: Write the failing tests:**
  - `TestAProxyIsSavedOnlyWithThePasswordAndACode` — body `{address, username, password, profiles, current_password, code}`; missing or wrong `current_password`/`code` → 403 and nothing stored; correct → 200 with `{"applies_on_restart": true}` and stored.
  - `TestAProxyTheLintRefusesIsRefusedWithItsWords` — `profiles: ["indexer"]` → 422 whose detail contains `metadata`.
  - `TestAProxyChangeIsAuditedWithoutItsPassword` — one `egress.proxy.changed` line naming old and new address; the password appears in no audit line.
  - `TestTheEgressStatusShowsTheProxyAndNeverItsPassword` — `GET /api/v1/admin/egress` has `proxy.stored.address`, `proxy.differs`, and no password anywhere in the body.
  - `TestRestartIsAuditedAndCallsRestart` — 202, a `system.restarted` line, `Deps.Restart` called once; a non-admin gets the usual refusal.
  - `TestTheEgressPatchIsStillClosed` (existing) still passes.
  - notify: `TestAProxyChangeIsSentWhateverTheCategories` — all categories off, an `egress.proxy.changed` record → sent; another Security record → not sent.
- [ ] **Step 2: Run** `w.sh go test ./internal/api/ ./internal/notify/` — FAIL.
- [ ] **Step 3: Implement.** Handler order: decode → `h.svc.Reauthenticate` (`ErrThrottled` → 429, `ErrLoginFailed` → 403) → `Deps.Proxy.Save` (validation error → 422) → audit → 200. `catalog` `entry` gains `always bool`; `classify` returns it and `Run` sends when `on[cat] || always`; entries: `ActionEgressProxyChanged {category: Security, title: "SOCKS5 proxy changed", always: true}`, `ActionSystemRestarted {category: Security, title: "Restarted from the web"}`. Restart handler writes 202 before calling `Deps.Restart`. Update the route lists the structural tests hold (`allowlist.go` if it lists admin routes, `api.TestATitleOutOfScope…` is unaffected), then `go generate ./internal/docs/`.
- [ ] **Step 4: Run** — PASS, including `./internal/docs/`. Mutate: the re-auth call, the `always` clause, the audit write, the password blanking in status.
- [ ] **Step 5: Commit** `api: set the SOCKS5 proxy with re-authentication; restart from the web`.

### Task 5: UI — Network proxy form, Getting started, live check

**Files:**
- Modify: `internal/web/assets/app/app.js` (views list at :30-60, Network view code, enroll redirect), `internal/web/templates/app.html` (sections at :221, :288), `internal/web/assets/auth/auth.js:388`, `internal/web/assets/app/app.css` if needed

**Interfaces:**
- Consumes: Task 4 routes and the `proxy` status.

- [ ] **Step 1:** Network tab: a *SOCKS5 proxy* card — address, username, password (blank keeps the stored one only if the address is unchanged; otherwise required), four profile checkboxes (`download` checked by default; ticking `indexer` ticks and locks `metadata` and `subtitle`, with the ADR-0024 note that a LAN indexer is then unreachable), current password and authenticator code, Save; after a save, a *Restart now* button calling the restart route and polling `/api/v1/auth/session` until it answers. Shows `proxy.differs` ("saved, applies on restart") and `proxy.problem` prominently.
- [ ] **Step 2:** New view `{ id: 'start', label: 'Getting started', perm: 'admin.system' }` first among admin entries; a checklist of the six steps, each reading its status from the existing GETs (`/admin/metadata`, `/admin/subtitles`, `/admin/egress`, `/admin/rootfolders`, `/admin/indexers`, `/admin/notifications`) and opening `#metadata`/`#network`/`#storage`/`#indexers`/`#notifications`; the Libraries step's *Add the installer's folders* button POSTs the four `/media/…` folders with kinds `movies`, `series`, `music`, `books`, skipping ones already present and reporting a refused one by its message. A line says MusicBrainz and Open Library need no account.
- [ ] **Step 3:** `auth.js` *go to the app* after enrolment → `/#start` (a non-admin falls back to the overview by `currentView`).
- [ ] **Step 4: Live check** (headless Chrome via `scratchpad/cdp.mjs`, a SOCKS5 stand-in in the scratchpad): fresh instance → setup → enrol → lands on Getting started; add the four folders; save a proxy with wrong code (refused) then right code; restart; `GET /admin/egress` shows it in force; a Torznab search through a proxied `indexer` profile arrives at the stand-in. Screenshots to `Claude outputs/7a-*.png`.
- [ ] **Step 5: Commit** `web: Getting started and the SOCKS5 proxy form`.

### Task 6: ADR-0065 and the documents

**Files:**
- Create: `docs/adr/0065-socks5-from-the-web.md`
- Modify: `docs/adr/0013-egress-guard-design.md` (a "Superseded in part by ADR-0065" line at decision on PATCH), `SECURITY.md` (egress section, *Not yet enforced*), `PROGRESS.md` (Phase 7 section, header counts), `docs/ESTIMATES.md`, `docs/DROPPED-FEATURES.md`, `config/config.example.yaml` (a note that the web can set the proxy)

- [ ] **Step 1:** Write ADR-0065 from spec §5: context, the decision, what stays file-only, the re-auth/audit/Discord/restart guards, the blocked fallback, consequences. (In execution the ADR is written before Task 2's code, per the workflow.)
- [ ] **Step 2:** Update the documents; `go generate ./internal/docs/`.
- [ ] **Step 3: Gates:** vet, `gates_offline.sh` (lint, gosec), `make security`, race over all packages, all tests, `mutate.py` for Tasks 1–4's mutants. Expected: all green, every mutant killed or deleted as equivalent.
- [ ] **Step 4: Commit** `docs: ADR-0065 and Phase 7a`.

### Task 7: Release workflow

**Files:**
- Create: `.github/workflows/release.yml`

- [ ] **Step 1:** On `push: tags: ["v*"]`, `permissions: contents: write`. Job `test`: same steps as `ci.yml`'s `test` job (setup-go `1.26`, `go vet`, `go test -race ./...`). Job `release` (`needs: test`): matrix `goarch: [amd64, arm64]`, build `CGO_ENABLED=0 GOOS=linux GOARCH=${{ matrix.goarch }} go build -trimpath -buildvcs=true -ldflags="-s -w -X main.Version=${GITHUB_REF_NAME}" -o cmediastack-linux-${{ matrix.goarch }} ./cmd/cmediastack`, upload artifacts; final step downloads both, writes `SHA256SUMS` with `sha256sum`, and creates the release with `gh release create "$GITHUB_REF_NAME" cmediastack-linux-* SHA256SUMS --generate-notes` (`GH_TOKEN: ${{ github.token }}`). Actions pinned by major version as `ci.yml` does.
- [ ] **Step 2: Verify** locally that the YAML parses (`py -c "import yaml…"` or `actionlint` if available); the real check is Task 10.
- [ ] **Step 3: Commit** `ci: publish static binaries on a tag`.

### Task 8: `install/cmediastack-install.sh`

**Files:**
- Create: `install/cmediastack-install.sh`, `install/cmediastack.service`

- [ ] **Step 1:** The script, `set -Eeuo pipefail`, each step announced with a `msg` function and stopping on the first failure with the failing command. Steps exactly as spec §3. Functions: `fetch_release [version]` (arch from `dpkg --print-architecture`, verify `sha256sum -c --ignore-missing`; honour `CMS_BINARY=/path` to install a local binary for testing), `write_config`, `ensure_address` (reads the primary IPv4; if it differs from the one in `base_url` or the certificate's SAN, rewrites both and says so), `write_tls <ip> <hostname>`, `install_service`, `wait_ready` (polls `http://127.0.0.1:9090/readyz`, the management listener, for up to 60 s), `check_sandbox` (journal grep). Called with the argument `update`, the script instead runs `fetch_release` into `cmediastack.new`, stops if the version equals the installed one, stops the service, keeps the old binary as `cmediastack.old`, swaps, starts, and on a failed `wait_ready` puts the old one back and starts it; then `ensure_address`.
- [ ] **Step 2:** `cmediastack.service` with the directives in spec §3, `ExecStart=/opt/cmediastack/cmediastack --config /etc/cmediastack/config.yaml`.
- [ ] **Step 3: Verify:** `shellcheck install/cmediastack-install.sh` clean (shellcheck via `pip install shellcheck-py` on Windows if not present). Run the script end to end in a systemd-enabled Debian 12 WSL distro (imported from the official Debian 12 rootfs into the scratchpad) with `CMS_BINARY` pointing at a local Linux build: the service is active, `/readyz` answers, sign-in over `https://<ip>:8443` works in headless Chrome, the sandbox line is read. Then change the address in `config.yaml` and run `ensure_address`: both rewritten.
- [ ] **Step 4: Commit** `install: the in-container installer and its service`.

### Task 9: `ct/cmediastack.sh`, README and RUNBOOK

**Files:**
- Create: `ct/cmediastack.sh`
- Modify: `README.md` (an *Install on Proxmox* section with the one-liner at the top), `docs/RUNBOOK.md` (a §0 *Proxmox LXC* section: what the script does, the key to copy, static IP/DHCP reservation advice, updating, that the Docker sections still apply to the Docker path)

- [ ] **Step 1:** The script per spec §2: `header_info`, root and `pveversion` checks, update mode when `/opt/cmediastack/cmediastack` exists (runs the install script with `update`, Task 8), `whiptail` Default/Advanced, `pveam update`/`pveam download` of the newest `debian-12-standard`, `pct create … --unprivileged 1 --features nesting=1 --ostype debian --net0 name=eth0,bridge=<br>,ip=dhcp|<cidr>,gw=<gw> --rootfs <storage>:<disk>`, start, wait for IPv4 and DNS (60 s), `pct exec <id> -- bash -c "$(curl -fsSL <raw URL>/install/cmediastack-install.sh)"`, final message per spec. The raw URL's branch from `CMS_BRANCH` (default `main`).
- [ ] **Step 2: Verify:** `shellcheck ct/cmediastack.sh` clean; `bash -n`; run it on a non-Proxmox host and confirm it refuses with its message. It cannot be run further here.
- [ ] **Step 3: Commit** `ct: the Proxmox host script; README and RUNBOOK`.

### Task 10: First release and push

- [ ] **Step 1:** Ask the operator before pushing the branch and the tag `v0.1.0` (a tag publishes a release).
- [ ] **Step 2:** `git push`, `git tag v0.1.0 && git push origin v0.1.0`; watch the run through the public API until the release has `cmediastack-linux-amd64`, `cmediastack-linux-arm64`, `SHA256SUMS`.
- [ ] **Step 3:** Re-run Task 8's WSL install **without** `CMS_BINARY`, so it fetches and verifies the published release.
- [ ] **Step 4:** Tell the operator the one-liner, that the host script's first real run is theirs, and what to look for.

# CMediaStack — Progress

Hand this file to a new session to resume. It is the authoritative statement of
what exists.

**Last updated:** 2026-09-30
**Current phase:** 5 (Music and books) — Phase 4's increments 4a–4z and 4aa–4ai complete, and 5a–5g: Phase 5 is complete. Phase 6 works through the known gaps: 6a–6n. Eight of them are
library and acquisition work done while Phase 4 was open: 4g (migrating from
Radarr), 4k (episode tracking), 4l (searching for a wanted episode), 4m
(indexers on the operator's own network), 4n (adding a series before any of it
is on disk), 4o (adding a film, and searching for it into the library), 4p
(one quality profile judging every search unless another is chosen) and 4q (an
approved request added to the library, and fulfilled when its title arrives).
4r made the CI gates that read the code pass: lint, SAST, dependency audit, secret scan.
4s backs the database up: encrypted, checked before it is kept, restored only from the host.
4t gives the metadata key and the tunnel a screen each; 4u is the operator runbook,
docs/RUNBOOK.md. 4v fetches what is wanted without a person, when the operator turns
it on: the indexers' recent releases, a budgeted search, and a machine's stricter rules.
4w gives the audit log a screen, and the denials any stranger can make it write a ceiling.
4x sends what goes wrong — and what the operator chooses from the audit log — to a Discord channel.
4y takes a whole season at once: a pack, grabbed by a person from a season's search or by automatic
acquisition for a settled season it wants all of, and imported file by file.
4z notices a download that stopped moving: automatic acquisition's is given up and its item searched for again; a person's is marked and left to them. 4aa lets a title name its own quality profile, and 4ab upgrades a file below its cutoff, when chosen.
4ac makes a library a root folder an account can be limited to, with a rating ceiling — enforced in the SQL of every read made for a person.
4ad removes the four play-session routes nothing needed and builds library search, a title's artwork and its original download.
4ae edits roles below Admin, lets an administrator create an account without ever knowing its password, and lets a person change their email.
4af gives the administrator every health check at once, the process's recent (redacted) log records, and the settings — read here, changed in the file.
4ag gives each account a calendar of its episodes and a feed of what arrived, behind a token in the address that reads those two and nothing else.
4ah lets anyone who may ask for titles report a problem with one, once, and whoever can fix it resolve it.
4ai lists what is popular, shared by every account and asked for where it is shown. **No route answers 501 any more**.
5a follows artists from MusicBrainz — albums, tracks and what is wanted — the first of Phase 5.
5b finds music files by folder and track number, scanned and imported.
5c searches for an album, grabs it into that album, and imports it track by track.
5d lets automatic acquisition fetch wanted albums, once each, under its own rules.
5e adds books from Open Library, one at a time, wanted until a file holds them.
5f gives a book its file: scanned, searched for, grabbed and imported, the best format kept.
5g lets automatic acquisition fetch wanted books, through the same runner as albums.
6a builds the breached-password check and signup proof-of-work the configuration had promised since Phase 1.
6b offers this build's source on every page, as the AGPL asks.
6c deletes one file to the trash, and purges one trashed file now.
6d rotates the master key from the host, every sealed value or none.
6e fetches a file's subtitle from OpenSubtitles, with the operator's own key.
6f fetches wanted subtitles every six hours, ten searches a pass, backing off.
6g lets a person grab a pack of several seasons, each file filed under its own season.
6h searches public trackers from their pasted Cardigann definitions.
6i signs in to private ones, the operator's credentials sealed and sent only to the tracker.
6j reads a download link off a tracker's details page when its definition says to.
6k lets a series' new seasons go unfollowed while its old ones stay as chosen.
6l upgrades albums held lossy to lossless, with the same switch as films and episodes.
6m lets a series file its episodes without season folders.
6n matches, searches and files daily series by their air dates.
Phase 6 stops at 6n (2026-10-03): the owner deferred audiobooks and anime's
absolute numbering, to be added if they are needed.
**Scope change, 2026-09-26:** the operator is starting a new library rather than
migrating one. Migrating from Sonarr is dropped; the Radarr importer (4g) stays,
and does nothing unless a Radarr database is placed in its directory
**Build:** `make build` · **Tests:** `make test` · **Acceptance:** `make acceptance`

---

## State at a glance

| | |
|---|---|
| Go packages | 36 |
| Tests | 1428, all passing, `go vet` and `-race` clean. Five fuzz targets |
| Static analysis | **0 findings** from golangci-lint v2.14.0 and from gosec v2.29.0 run as the SAST job runs it, both pinned in CI (twenty-seven `#nosec`, each with its reason on the line — SECURITY.md). `govulncheck`: nothing reached (run again at 6l, after 6h made `golang.org/x/net/html` reached code). One advisory against a required module, GO-2026-5932 for `golang.org/x/crypto/openpgp`, is in a package nothing here imports; there is no fixed version. gitleaks: nothing, with seven fake test credentials allowlisted by value |
| Routes registered | 149 (15 anonymous, 49 admin-hidden, 18 session-only). **0 of 149 routes still return 501**: every route Phase 1 registered is built or was removed by a record, and `api.TestNoRouteIsLeftUnbuiltWithoutARecord` keeps it so |
| **Can the downloader leak?** | **It refuses to start unless it can prove it cannot.** Verified against the binary: exits non-zero on the wrong interface |
| **Can you log in?** | **Yes, in a browser.** First run → wizard → login → authenticator enrollment → working session |
| **Is there a UI?** | **Yes.** No build step, no third-party frontend code |
| **Can you watch something?** | **Yes.** Direct play with seeking and resume, a remux for files a browser cannot decode, and subtitles. Verified in real Chromium **and inside the built container** |
| **Does it know what a series is missing?** | **Yes.** Episode lists come from the metadata provider and from nowhere else; *wanted* is monitored, aired and absent; a renewed series is noticed. Verified against the live API |
| **Can you start a library from nothing?** | **Yes, series and films.** Search the provider and add: a series with every season and episode and your choice of which you want, a film straight onto the Wanted list — nothing downloaded, nothing written to disk. Verified against the live API through the binary and in a browser |
| **Can it fetch what is missing by itself?** | **Yes, when you turn it on** (`acquisition.automatic`, off by default). Every 15 minutes each indexer is asked once for its recent releases and three wanted items are searched for; a release is grabbed only when it is exactly one wanted item, the default profile accepts it, it has seeders and it was never in the queue — five a pass at most, nothing while the tunnel is down, every grab audited as `system:acquire`. The Wanted screen says beside every item what was done and why not. Verified on the running binary: grabbed from the feed, finished, imported, gone from the list |
| **Can it fetch a whole season at once?** | **Yes — a season pack.** A person's *Search season* offers packs of the season and its single episodes; automatic acquisition grabs a pack for a season that finished airing over a week ago and that it wants every episode of, once the pack's `.torrent` is read and seen to hold them all. The import files each file as the episode its own name says, never over a better file. Verified on the running binary, a person's grab and a machine's, and in Chromium |
| **Does it replace a file with a better one?** | **Yes, when you turn it on** (`acquisition.upgrades`, off by default). A file below its title's quality cutoff is searched for an upgrade at most weekly, after everything missing; only a release the profile says is better is fetched, and the old file goes to the trash. Verified on the running binary |
| **What about a download that never finishes?** | **A machine gives up its own; a person is told.** After a day with no progress while the instance runs, a download automatic acquisition grabbed is stopped, never grabbed again, and what it was for is searched for afresh; one a person grabbed is marked on the Queue screen and left for them to remove. Each is an `acquisition.stalled` line. Verified on the running binary |
| **Can it go and get what is missing?** | **Yes, one episode or one film at a time, chosen by a person** — through a Prowlarr or Jackett on your own network. Only a release that *is* the episode or film can be grabbed, and it is filed under it whatever the release calls itself. Every search is judged by the instance's default quality profile unless the person picks another — *HD-1080p* on a new instance, so a camera recording is refused unless somebody asks for it. Verified end to end on the running binary: search, grab, import — `Dune.Part.One.2021` into *Dune (2021)* — gone from the wanted list |
| **Can a child have an account that sees only their library?** | **Yes.** A library is a root folder: an account sees every one or the ones ticked when it was approved or invited, and a rating ceiling hides whatever is rated above it — and whatever has no rating. Ratings are TMDB's US certifications, fetched hourly, or set by hand. Out of scope reads exactly as absent on every route that names a title. Verified on the running binary, through the API and in Chrome |
| **Can somebody else ask for something?** | **Yes, and it ends in the library.** A user asks; an approver agrees, adds the title from the request and searches for it; the request is fulfilled when a file of it arrives, and the requester can see what it became. Approving downloads nothing. Verified against the running binary as two people, in a browser and through the API |
| **Can you see who did what?** | **Yes — the Audit log tab, for the administrator.** Every line the instance recorded, newest first, filtered by kind, outcome, who and any text, with the week counted at the top. Anybody can make it write a denial by asking for a page without signing in, so those have a ceiling — 20 an hour from one address, 120 from all, 60 from one account — and the rest are counted in one line an hour. Verified on the running binary: a flood of thirty wrote twenty, and the stopping server wrote the count |
| **Does it tell you when something goes wrong?** | **Yes — in a Discord channel, once you give it a webhook.** Within a minute: a task that starts failing or recovers — *the tunnel is down*, *backups are failing* — a flood of denials, *password accepted, authenticator code refused*, an API token issued, an account waiting for approval; grabs, arrivals and requests only if you turn them on, because they send titles. Nothing a stranger wrote can ping the server or pass for a link. Verified against Discord itself and, end to end, against a stand-in: a grab by automatic acquisition and its arrival in the library reached the channel |
| **Is the database backed up?** | **Yes — daily, encrypted, and checked before it is kept.** At startup and hourly, a backup is taken when the newest is a day old: a snapshot that passed SQLite's integrity and foreign-key checks, encrypted with a passphrase derived from the master key, decrypted again to confirm it. Kept a week, never fewer than the newest three. Nothing downloads or restores one over HTTP: `-verify-backup` and `-restore-backup` on the host. Verified against the running binary, and a backup decrypted by the distribution's own `age` |
| **Does the container hold up?** | **Yes, checked rather than asserted.** uid 65532 in all four IDs, no shell, no package manager, the parser jail live under the project's seccomp profile. `docker compose up` runs end to end from an empty database |
| Verified end to end | Against the compiled binary in real Chromium: zero CSP violations, zero page errors |

The working flow today:

```
GET  /setup                         → wizard (404s forever once an account exists)
POST /api/v1/setup                  → first administrator, in awaiting_mfa
POST /api/v1/auth/login             → password ok → {"next":"enroll"|"mfa"}
GET  /api/v1/auth/mfa/enroll        → secret + otpauth:// URI
POST /api/v1/auth/mfa/enroll/confirm→ account active, 10 recovery codes shown once
GET  /api/v1/me                     → works; every other route enforces its own permission
POST /api/v1/auth/signup            → account REQUEST (never an account)
GET  /api/v1/accounts/requests      → approval queue (Manager+)
POST /api/v1/accounts/requests/{id}/approve → creates the user, in awaiting_mfa
POST /api/v1/admin/users/{id}/suspend       → freezes and kills live sessions instantly
POST /api/v1/auth/logout            → revokes the session

POST /api/v1/invites                 → single-use invite; code shown once
GET  /api/v1/invites                 → outstanding invites (never the codes)
DELETE /api/v1/invites/{id}          → revoke
POST /api/v1/auth/signup             → with invite_code, skips the queue entirely
GET  /api/v1/me/sessions             → own devices
DELETE /api/v1/me/sessions/{id}      → remote revoke (own sessions only)
POST /api/v1/me/password             → requires the current password
POST /api/v1/me/mfa/recovery-codes   → regenerate, retiring the old set
POST /api/v1/auth/reset/initiate     → identical response for any username
POST /api/v1/auth/reset/complete     → sets the password, revokes all sessions
POST /api/v1/admin/users/{id}/reset-link → admin-minted reset (the working delivery path)

POST /api/v1/me/tokens               → scoped API token; value shown once
GET  /api/v1/me/tokens               → own tokens (never the values)
DELETE /api/v1/me/tokens/{id}        → revoke, immediately
Authorization: Bearer cms_pat_...    → authenticates any non-credential route

GET  /api/v1/admin/system/tasks      → next run, last run, failure history
POST /api/v1/admin/system/tasks/{name}/run → manual trigger
POST /api/v1/admin/system/backup     → an encrypted, checked backup, now
GET  /api/v1/admin/system/backups    → the backups and the policy — never the files
-verify-backup FILE                  → decrypt, check and report a copy (on the host)
-restore-backup FILE -restore-to NEW → a new, fenced database file (on the host)
GET  /metrics  (management listener) → Prometheus text exposition

GET  /                               → app shell (303 → /login when signed out)
GET  /login  /signup  /reset  /setup → the anonymous pages
GET  /enroll                         → authenticator setup, awaiting_mfa only
GET  /assets/auth/  /assets/app/     → the two bundles, one anonymous, one not
GET  /api/v1/roles                   → roles the caller may actually assign

GET  /api/v1/admin/egress            → tunnel health + per-subsystem policy
PATCH /api/v1/admin/egress           → 409, permanently: policy is config, not runtime
POST /api/v1/admin/egress/leak-test  → re-verify routing; contacts nothing
--role downloader                    → exits non-zero unless egress verifies
```

---

## Phase 0 — Design ✅ (the documents were written late; see below)

Deliverables: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md),
[`docs/DATA-MODEL.md`](docs/DATA-MODEL.md),
[`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md),
[`docs/API-SURFACE.md`](docs/API-SURFACE.md),
[`docs/DROPPED-FEATURES.md`](docs/DROPPED-FEATURES.md),
[`docs/ESTIMATES.md`](docs/ESTIMATES.md), and nine ADRs (0001–0008 plus 0003a).

**This section said "all deliverables complete" for three phases while none of
those files existed.** The decisions were real and were made — they are visible
throughout the code, which cites them by number — but they had only ever been
written out in conversation. The tables here and in README.md linked to them
anyway, `SECURITY.md` cited a threat model that was not there, and two ADRs that
*did* exist cited ADR-0001, which did not. Twenty-two dangling links in all.

Found before Phase 4, when reading ADR-0005 for the transcode policy turned up
nothing. `docs.TestEveryLinkedFileExists` now fails the build on a relative link
to a file that is not there, which is the same guarantee
`docs.TestEveryCitedTestExists` already gave for test names: a document may not
claim something exists without it existing.

Two of the six are no longer hand-written. `API-SURFACE.md` is generated from
the real routing table and `DATA-MODEL.md` by applying the migrations to a
throwaway database and reading the schema back — with the migrations' own
comments carried through, so the *reasons* survive into the document.
`docs.TestTheGeneratedDocumentsAreCurrent` fails while either is stale. A
hand-maintained mirror of the route table is a document somebody consults to ask
"is anything anonymous that should not be?", and it would have rotted into
exactly the defect above.

---

## Phase 1 — Foundation ✅ (substantially; see the remaining-estimate table)

### Increment 1a — platform and authorization ✅

| Component | Package | Notes |
|---|---|---|
| Config + **security lint that refuses insecure boots** | `platform/config` | 12 tests |
| Structured logging with **handler-level redaction** | `platform/logging` | Cannot be bypassed by call site, `.With()`, or groups |
| Secrets: AES-256-GCM, context-bound | `platform/secrets` | Ciphertext cannot be relocated between fields |
| Database: SQLite WAL, checksummed migrations, tx helper, backup | `platform/db` | FK enforcement asserted; rollback on error *and* panic |
| **Authorization engine: effect-based RBAC, escalation guards, object scoping** | `authz` | 21 tests |
| Append-only audit log | `platform/audit` | No mutation method exists, asserted by reflection |
| Argon2id + TOTP (RFC 6238 vectors) + recovery codes | `identity` | |
| Router, allowlist enforcement, full middleware chain | `api` | |
| Hardened container, compose, CI, SECURITY.md, AGPL | — | |

### Increment 1b — sessions and the account lifecycle ✅

| Component | Notes |
|---|---|
| **Session store** | Opaque, server-side, `sha256(secret)` at rest. Rotation every 15 min with a 30 s grace; a superseded secret presented after that revokes the whole session as a detected replay |
| **Real authenticator** | Replaces `NoSessions`. Resolves the cookie, rebuilds the principal from stored role, grants and ceiling on every request |
| **Login flow** | Throttled per user *and* per IP; timing equalised with `SpendVerificationTime`; one error for every failure mode |
| **MFA enrollment** | Secret not stored until a correct code proves the app has it — storing earlier could lock a user out permanently under mandatory MFA |
| **TOTP replay prevention** | Consumed counter recorded with a conditional UPDATE, so a code cannot be reused inside its 30 s step |
| **Signup → approval** | Request holds no session, no token, no permission. Approver sets role, libraries and rating ceiling in the same action that creates the user |
| **Suspension** | Sets state *and* revokes live sessions in one call — immediate, not at next expiry |
| **First-run wizard** | Account count checked per request; closes permanently once any account exists. No default account, no bootstrap credential |
| **Background maintenance** | Hourly purge of expired requests, dead sessions and old throttle records |
| **Go version pinned coherently** | go.mod, Dockerfile and CI all at 1.26 (x/crypto requires it) |

### Increment 1c — invites, sessions, password reset ✅

| Component | Notes |
|---|---|
| **Invites** | Single-use, expiring, SHA-256 at rest, code shown once. Redemption with `auto_approve` creates the account outright and skips the queue; without it, a pre-linked pending request. `registration.mode: invite` now works |
| **Invite escalation guard** | Issuance runs the same `CanAssignRole` check as approval, because an invite *is* an approval made in advance |
| **Invites die with their issuer** | Suspending a user revokes their outstanding invites: those are pre-approved grants made on authority that has just been withdrawn |
| **Session list + remote revoke** | Ownership is a predicate in the UPDATE's `WHERE`, not a preceding SELECT, so there is no window and no way to skip it |
| **Password reset** | Single-use, 30-minute, SHA-256 at rest; issuing a new token invalidates outstanding ones; completing a reset revokes every session and does **not** sign the user in |
| **Reset delivery** | `ResetDelivery` interface with a refusing default — there is no mail transport in this build, and pretending to send would be worse than saying so. The working path is an admin-minted link, which goes through the escalation guard |
| **Password change** | Requires the current password even though the caller holds a session; revokes other sessions but not the current one |
| **Recovery-code regeneration** | Re-authenticates with the password, then retires the whole previous set |
| **Maintenance** | Now also purges expired invites and reset tokens |

### Increment 1d — scoped API tokens ✅

| Component | Notes |
|---|---|
| **Scoped tokens** | `cms_pat_` prefix (so secret scanners can match it), 256-bit, SHA-256 at rest, optional TTL, value shown once |
| **Scope ≤ issuer, at issuance** | A User cannot mint themselves an admin token. A partially over-scoped request is refused whole rather than trimmed |
| **Scope re-intersected at every use** | The token's permissions are intersected with the owner's **current** role, so demoting someone narrows every token they hold without anyone remembering to revoke them |
| **Credential management is session-only** | `Route.SessionOnly` refuses tokens outright, before any permission check, on password change, MFA enrollment, recovery codes, token issuance and session management. A leaked token is a scoped, revocable grant — never a permanent takeover |
| **Dies with its owner** | Suspension revokes tokens as well as sessions and invites, making §7.2's "immediately revokes all sessions, API tokens" literal |
| **No credential fallback** | A request carrying a bad Bearer token is anonymous, never silently upgraded to the cookie's session |
| **CSRF exemption for Bearer** | CSRF defends against *ambient* credentials; an Authorization header is not ambient and cannot be set cross-origin without a preflight this server never approves. Requiring a token there would break every POST while adding nothing |

### Increment 1f — the web UI ✅

Six pages, hand-written, embedded in the binary. **No Node, no npm, no bundler,
no third-party frontend code at all** — ADR-0011 records why the Phase 0
assumption of a Vite build was overruled.

| Component | Notes |
|---|---|
| **Pages** | `/login` (password → second factor), `/signup`, `/reset`, `/setup`, `/enroll`, and the application shell at `/` |
| **App sections** | Overview, Approvals, Invites, Sessions, API tokens, Security, Tasks — tabs appear only for permissions the caller holds, and that is cosmetic; the server refuses the routes regardless |
| **Two bundles, and the split is now testable** | `assets/auth` is anonymous, `assets/app` is not, and they share no file. A test scans the anonymous bundle for any `/api/` path outside a nine-entry allowlist — splitting the bundles achieves nothing on its own; that assertion is what makes it real |
| **No inline script or style anywhere** | The CSP has no `unsafe-inline`, so rather than thread a nonce through every page the pages emit nothing inline at all. A nonce that is never emitted cannot leak. Enforced by tests over both the rendered HTML and the shipped JavaScript |
| **No `innerHTML`** | Every database string reaches the DOM through `textContent`. The test fails the build on `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `eval` and `document.write` |
| **Pages carry no data** | A shell plus one value — the password policy's minimum length, so the form's hint cannot drift from the rule the server enforces. Everything else comes from the JSON API, through the same middleware and authorization path a script would use. There is deliberately **no second set of server-side form handlers**: each would be another place to forget a permission check |
| **One route may redirect** | `GET /{$}` sends a denied navigation to `/login`, because §2 promises a visitor sees a login screen. Everything else still 404s. ADR-0012; `register()` panics on a second one |
| **A suspended account is indistinguishable from an unknown one at the root** | Redirecting only the anonymous case would make the root an oracle: present a cookie, learn from the status whether the account is real. Status, `Location` and body are compared byte for byte |
| **`GET /api/v1/roles`** | New. An approver could not previously enumerate the roles they may assign, so the approval form had nothing to fill its dropdown with. The list is produced by running the real `CanAssignRole` guard once per role, not by reimplementing its rule |
| **The safest role is the default** | The list is ordered least-privileged first, server-side. A dropdown takes its default from the first option, so descending rank would mean an approver who clicks Approve without reading grants the most powerful role they can. That ordering is a security property, which is why it is not left to the UI |
| **Enrollment shows a scannable code** | Added 2026-09-12 once the checksum constraint lifted. Manual key entry survives behind a `<details>` and opens automatically if the image cannot be rendered — it is a complete path, not a consolation prize. A round-trip test decodes the image with an *independent* library and asserts it carries the same otpauth URI: an encoder agreeing with itself proves nothing, and a QR that renders but encodes the wrong seed is worse than none |

**Verified in real Chromium against the compiled binary**, not only in tests:
the full first-run → login → second factor → task trigger → invite → token →
signup → approve → enroll → recovery-codes flow, across three browser contexts.
**Zero CSP violations, zero page errors, zero failed same-origin requests.**

#### Two defects the browser found that no unit test had

- **Approving a duplicate username returned a bare `500 internal error`.**
  Reachable in ordinary use, not just in a test: signup deliberately accepts a
  request for a name that already exists rather than becoming an enumeration
  oracle, so duplicates reach the approval queue by design. The approver was
  left clicking a button that silently never worked. Now a `409` that says what
  happened, the request stays in the queue so it can be denied, and
  `TestApprovingADuplicateUsernameIsReportedNotSwallowed` covers it.
- **A stale success banner could be read as the answer to the click you just
  made.** Every mutation now clears both banners before it starts. Harmless
  nine times out of ten; the tenth is the time the new request failed.

#### ~~The QR code question~~ — closed 2026-09-12

`sum.golang.org` became reachable, so dependencies can be verified against the
transparency log again. Two were taken, both previously blocked by that:

- `skip2/go-qrcode` — the enrollment code. Ships in the binary.
- `makiuchi-d/gozxing` — **test only**, and it earns its place by being an
  independent implementation. `go tool nm` confirms zero symbols in the binary.

The in-tree metrics exposition and SOCKS5 client **stay**. They are written,
tested and dependency-free, and nothing on the wire can tell the difference.
Replacing working tested code with a dependency is motion, not progress. See the
addendum on [ADR-0010](docs/adr/0010-metrics-in-tree.md).

### Increment 1e — metrics and scheduled tasks ✅

| Component | Notes |
|---|---|
| **Prometheus `/metrics`** | Counters, gauges and fixed-bucket histograms in the 0.0.4 text format. On the **management listener only** — metric names and labels leak operational shape |
| **Written in-tree, no client library** | See ADR-0010. Decisive reason: this build environment cannot reach `sum.golang.org`, so that dependency's checksums could not be verified against the transparency log, and committing an unverifiable `go.sum` would make SECURITY.md's "pinned and verified dependencies" false |
| **Cardinality bomb prevented** | HTTP metrics are labelled by route **pattern**, not raw path. Labelling by path would let any caller create unbounded series with `/media/1`, `/2`, `/3` — a denial of service against the monitoring system |
| **Label escaping** | Quotes, backslashes and newlines escaped, so a hostile indexer name cannot produce an unparseable line or inject a fabricated metric |
| **Proxy health starts DOWN** | An unset gauge and a healthy gauge look identical on a dashboard; the kill-switch signal must fail visibly |
| **Later-phase metrics declared now** | They read zero until their subsystem exists, so a dashboard built today keeps working when Phase 2 lands |
| **Scheduled tasks** | Visible next run, last run, duration, failure count and a bounded history; manual trigger |
| **Never runs concurrently with itself** | Overlapping purges corrupt each other's assumptions, and a slow task would pile up runs. A scheduled tick that lands mid-run is dropped; a manual trigger is refused with 409 rather than queued |
| **A task panic is contained** | Recorded as a failure with the panic value. One bad task does not take the process down |
| Registered tasks | `identity.maintenance` (hourly), `metrics.sample` (per minute), `database.integrity` (manual only) |

### Phase 1 acceptance criteria

| # | Criterion | Status | Proof |
|---|---|---|---|
| (a) | Route enumeration proves every route outside the §7.1 allowlist rejects anonymous requests | ✅ **PASS** | `TestEveryNonAllowlistedRouteRejectsAnonymous` — 66 protected routes, all 404, no distinguishable body |
| (b) | A pending account cannot obtain a session or reach any resource | ✅ **PASS** | Unit: 4 tests. End-to-end: `TestSignupApprovalAndFirstLogin` shows a pending applicant refused login against the real database |
| (c) | A low-privilege user cannot reach any admin route or record by any API path | ⚠️ **PARTIAL** | Routes: ✅ 22 admin routes 404 for a User, proven over HTTP with a real session. **Records: first real object-level proof —** `TestUserCannotRevokeAnotherUsersSession` shows Alice holding Bob's session ID cannot revoke it, and neither can an Admin through the self-service route. Media records still wait for Phase 3 |
| (d) | A Manager cannot approve an account into Admin or escalate | ✅ **PASS** | Unit: 6 tests. End-to-end: `TestManagerCannotApproveIntoAdminOverHTTP` runs the real attack through the real stack and shows approving into User still works |

`make acceptance` runs these.

### Notable negative tests added in 1f

- `TestAnonymousBundleNamesNoAuthenticatedEndpoint` — the whole point of splitting the bundles
- `TestSuspendedAccountIsIndistinguishableFromAnonymousAtTheRoot` — status, `Location` and body compared byte for byte
- `TestTemplatesContainNothingInline` and `TestScriptsAvoidBlockedAndUnsafeAPIs` — CSP violations fail silently in a browser, so they are caught here instead
- `TestApprovingADuplicateUsernameIsReportedNotSwallowed` — the regression the browser found; verified to fail without the fix
- `TestAssignableRolesPutTheSafestOptionFirst` — and that an Admin is never offered the Admin role
- `TestAssetsRefuseAnythingButAFlatKnownName` — traversal, nesting, unknown names, the bare directory
- `TestAssetHandlerIsConfinedToItsBundle`, `TestTemplatesAreNotServable`
- `TestUnmatchedPathIs404ForAnonymous` — which caught `GET /` registered as a catch-all, swallowing every unrouted path

### Notable negative tests added in 1e

- `TestTaskNeverRunsConcurrentlyWithItself` — a real concurrency probe, not a flag check
- `TestPanicIsContainedAndRecordedAsFailure` — and the scheduler still works after
- `TestLabelValuesAreEscaped` — quote, newline and backslash injection into metric labels
- `TestWrongLabelCountDoesNotPanic` — a metrics bug must not break the request path
- `TestProxyHealthStartsDown`
- `TestStopWaitsForInFlightRuns`, `TestHistoryIsBounded`, `TestRegistrationGuards`
- `TestConcurrentUse` — 50 goroutines writing and scraping simultaneously

### Notable negative tests added in 1d

- `TestTokenScopeCannotExceedTheIssuer` — five separate escalation attempts
- `TestTokenNarrowsWhenItsOwnerIsDemoted` — the token is never touched
- `TestTokenCannotReachCredentialManagement` — nine routes, with an all-permissions token
- `TestSuspensionKillsAPITokens`, `TestRevokedTokenStopsImmediately`, `TestExpiredTokenIsRefused`
- `TestUserCannotRevokeAnotherUsersToken` — object-level, again
- `TestBadBearerTokenDoesNotFallBackToTheCookie`
- `TestTokenPrincipalIsMarkedAsAToken` — `CredentialSession` is the zero value, so this asserts the guard can actually fire
- `TestTokenCanPostWithoutACSRFToken` — the positive half of the CSRF exemption

### Notable negative tests added in 1c

- `TestUserCannotRevokeAnotherUsersSession` — the object-level check, with the ID in hand
- `TestInviteIsSingleUse`, `TestExpiredAndRevokedInvitesAreRefused`
- `TestManagerCannotIssueAnAdminInvite` — and can still issue a User one
- `TestSuspendingAnIssuerRevokesTheirOutstandingInvites`
- `TestInviteListNeverContainsCodes`
- `TestResetInitiateIsNotAnEnumerationOracle` — including that it does not leak that delivery is unimplemented
- `TestResetDoesNotBypassMFA` — the post-reset login still lands on the second factor
- `TestResetTokenIsSingleUseAndExpires`
- `TestManagerCannotMintAResetForAnAdmin`
- `TestRegeneratingRecoveryCodesInvalidatesTheOldSet`, `TestRecoveryCodeIsSingleUse`
- `TestChangePasswordRevokesOtherSessionsOnly`

### Notable negative tests added in 1b

- `TestTOTPCodeCannotBeReplayed` — the same code, inside its step, from a second session
- `TestSupersededSessionSecretIsTreatedAsReuse` — rotation, then replay of the old secret
- `TestSuspensionKillsLiveSessionsImmediately` — no clock advance, no re-login
- `TestSignupIsNotAnEnumerationOracle` — byte-identical response for a taken address
- `TestLoginFailuresAreIndistinguishable` — unknown user vs wrong password
- `TestSmuggledPrivilegedFieldsAreRejected` — `role_id` in a signup body
- `TestSetupIsUnreachableOnceAnAccountExists` — including that no second admin is created
- `TestStateChangingRequestWithoutCSRFTokenIsRefused`

### Still not built *(as Phase 1 closed — later phases moved several of these)*

- **Password-reset delivery** — no mail transport; admin-minted links are the
  working path. `ResetDelivery` is the seam to fill
- **Profile edit** (`PATCH /api/v1/me`)
- ~~HIBP k-anonymity breach checking — designed, not built~~ — built in 6a (ADR-0051)
- ~~Proof-of-work on signup — configured, not implemented~~ — built in 6a (ADR-0051)
- ~~Notification transports — the in-app approval queue works; Discord deferred~~ —
  Discord built in 4x (ADR-0032); no other transport
- ~~Master key rotation — design in SECURITY.md~~ — built in 6d (ADR-0054)
- ~~seccomp profile is `unconfined` in compose, marked, pending Phase 4~~ —
  done in 4c; `deploy/seccomp-cmediastack.json`
- ~~No QR code on enrollment~~ — done, 2026-09-12
- ~~**No library, queue or playback UI**~~ — built across Phases 2–4
- ~~42 of 81 routes still return 501~~ — 23 of 110 now do, and the count is
  derived from the routing table rather than remembered. See the header, and
  `docs.TestTheProgressHeadlineNumbersAreTrue`

### Remaining Phase 1 estimate

**8–22 hours.** Phase 1 is substantially done: the security spine, the identity
subsystem and a working browser UI on top of them.

What is left is small and optional:

| Item | Estimate | Worth it? |
|---|---|---|
| ~~QR code on enrollment~~ | ~~2–3 h~~ | ✅ done |
| `PATCH /api/v1/me` (profile edit) | 2–4 h | Marginal at five users |
| ~~Admin user-management UI (list, suspend, role change)~~ | ~~6–10 h~~ | ✅ done — increment 3f |
| Audit-log viewer | 4–8 h | Nice; the data is already there |

**Recommendation: stop here and go to Phase 2.** The admin user list is the only
gap that will actually bite, and it is a few hours whenever it does. Phase 2 is
where the project either becomes what you asked for or does not.

> **Closed in [increment 3f](#increment-3f--admin-user-management-).** It bit
> roughly as predicted, and building it turned up two defects the API-only
> version had been hiding: a half-wired lockout guard and a missing class of
> audit record.

---

## Phase 2 — Acquisition 🚧

### Increment 2a — the egress guard ✅

Built first, and on purpose: §2 promises the download engine never egresses
outside its tunnel, so the jail exists before the thing it contains. There is no
window in which an unprotected downloader runs, because there is no downloader
yet. [ADR-0013](docs/adr/0013-egress-guard-design.md).

| Component | Notes |
|---|---|
| **`--role downloader` refuses to start** | Verified against the binary: with `wg0` configured and `eth0` actually carrying traffic, it logs the mismatch and exits 1. A downloader that comes up unprotected has already broken the promise, and it breaks it *silently* |
| **The check is behavioural, not a file parse** | The obvious test — comparing `/proc/self/ns/net` with `/proc/1/ns/net` — is **wrong here**: the downloader container joins the WireGuard container's namespace, so PID 1 shares it and the links match. That check would report "not jailed" for a correctly jailed process. Instead the kernel is asked which source address it would use for a public destination, and which interface owns it. No packet is sent |
| **No fallback, proven by exhaustion** | `TestNoFallbackWhenUnhealthy` runs every mode — direct, socks5, http-proxy, blocked, unrecognised — against a listener that *would* have accepted, so a fallback shows up as a returned connection rather than an unrelated error |
| **The structural guarantee** | `TestNoPackageDialsDirectly` reads the source of every acquisition package and fails the build on `net.Dial`, `http.Get`, `http.DefaultClient`. A runtime check cannot prove a code path does not exist |
| **Health starts DOWN** | Nothing dials until a probe succeeds. Probed once at startup so a restart does not pause egress for an interval for no reason |
| **Unconfigured means blocked** | `Guard.For` returns a `Dialer`, never `(Dialer, error)`: a caller who ignores an error gets a working direct dialer; a caller who ignores this gets one that cannot connect |
| **The probe is the only gate exemption** | Without it the gate is a trap — once health drops, the probe that would notice recovery also fails. It skips the health gate and nothing else; a blocked profile still refuses. A test pins the caller count at one |
| **`notification` is the only default exemption** | A kill switch that also silences the alert telling you it fired is one you discover from your library being empty. Metadata is deliberately *not* exempt: a TMDB query reveals what you are interested in |
| **SOCKS5 written in-tree** | RFC 1928 + RFC 1929, same reason as ADR-0010. Tested against a server written independently from the same RFC — a mock mirroring the client's assumptions proves nothing |
| **socks5h is enforced twice** | The config lint refuses `socks5` without `remote_dns` at boot; the dialer refuses it again, so a programmatic caller cannot construct the leak either. A test proves the hostname goes out as a DOMAIN address rather than being resolved locally |
| **UDP is refused, not faked** | SOCKS5 UDP ASSOCIATE is unimplemented and NordVPN's endpoints do not reliably relay UDP anyway. Saying so beats silently losing DHT and every `udp://` tracker |
| **SSRF filtering per profile** | `DenyPrivate` on download, indexer, metadata and subtitle — the four that dial addresses derived from hostile input. **All** resolved addresses must pass: a name resolving to both a public and a loopback address is a rebind attempt, and picking the public one is choosing not to notice |
| **`PATCH /api/v1/admin/egress` → 409, permanently** | A runtime switch for the control that prevents leaks is the first thing an attacker with an admin session reaches for. 409 not 501: it is closed, not unfinished |
| **The leak test states its own limits** | The response carries a `caveat` field saying it cannot prove the absence of a leak — a leak takes a path this process cannot observe. An operator who reads "passed" and concludes more than that is worse off than one who never ran it |

**35 new tests.** The ones worth naming:

- `TestNoFallbackWhenUnhealthy` — the §2 headline, by exhaustion
- `TestNoPackageDialsDirectly` — the structural property
- `TestSOCKS5SendsTheHostnameRatherThanResolvingIt` — what socks5h is *for*
- `TestSOCKS5RejectedCredentialsFailWithoutLeakingThem` — the error reaches the log
- `TestGateClosesUnderTraffic` — a dialer obtained *before* the drop still refuses after it
- `TestOnlyTheProbeBypassesTheGate`, `TestProbeDialerBypassesTheGateButNotThePolicy`
- `TestHTTPConnectRefusesDataSentBeforeTheTunnelOpens` — silently losing the first bytes of a BitTorrent handshake is a bug somebody chases for days
- `TestEgressPolicyCannotBeChangedOverHTTP`, `TestEgressStatusNeverReturnsTheProxyPassword`
- `TestProbeReportsUnhealthyWhenRoutingCannotBeConfirmed` — inconclusive is never healthy

### What 2a does NOT do

- **No download engine.** That is 2b. `--role downloader` verifies its jail and
  then exits saying so.
- **It does not write the firewall.** ADR-0001's nftables rules and the
  WireGuard container live in `docker-compose.yml` and are the operator's to
  deploy. This code *verifies* the jail; it does not build it.
- **It cannot prove the absence of a leak.** See the caveat above.

### Increment 2b — release parsing and quality profiles ✅

The piece flagged as the highest underestimated risk in the whole build, so it
went in early and got a real corpus rather than a handful of happy cases.

A release name is the only description of a file that exists **before** it is
downloaded, so the decision to grab, the decision to upgrade and the decision of
where it belongs are all made from a string somebody typed at 3am in 2009. There
is no specification.

| Component | Notes |
|---|---|
| **Parser** | Title, year, season/episode (single, multi, range, full-season, daily), resolution, source, codec, audio, channels, HDR, editions, languages, flags, PROPER/REPACK revision, group, container |
| **It admits when it is guessing** | `Confidence` and `Unmatched` are part of the output. A caller automating a grab on a low-confidence parse is making a decision the parser did not support, and an operator looking at a wrong result can see which tokens it failed to understand |
| **Quality is a source-and-resolution PAIR** | Ranking on resolution alone gives a library of upscales; on source alone, a DVD over a 4K stream. A 1080p WEBRip and a 1080p remux are not the same file |
| **Half-known quality rounds DOWN** | An unlabelled release must not clear a cutoff by being vague |
| **Ranking lives in the profile, not the code** | Nothing assumes a universal "better". Preferring 1080p WEB-DL to a 2160p remux is a legitimate choice — and on the i5-6500T, which cannot tone-map HDR at all (ADR-0005), it is the correct one. The shipped default cuts off at Bluray-1080p and a test enforces that |
| **The cutoff stops churn** | Once what is held meets the cutoff, upgrading stops. Without it, an operator who asked for 1080p gets their library rewritten every time a marginally different 1080p release appears |
| **Bad profiles fail when saved** | Compile() names the problem. The alternative is a profile that silently rejects everything at 3am |
| **Operator patterns cannot hang the pipeline** | Terms are literal unless wrapped in slashes, and compile through the standard library — RE2, so no backtracking. A test throws `(a+)+b` at 5000 a's |

#### Bugs the corpus found that no amount of reading would have

- **`WEB-DL` yields the group "DL"** — and so does every WEB-DL release ever
  posted. The fix is not to check whether the tail looks like a quality term
  ("DL" does not) but whether the tail **joined to what precedes the dash** is
  one. That keeps `x264-DEMAND` yielding DEMAND.
- **Group extraction swallowed the whole tail.** Allowing dots in `-([\w.]+)$`
  matches everything after the last dash *anywhere*, so
  `S02E05-E07.1080p.BluRay.x264` produced the "group" `E07.1080p.BluRay.x264`
  and deleted every quality term before anything read them.
- **`Blade Runner 2049 (2017)` parsed as a 2049 release.** Separator classes are
  *consumed*, so the dot before `2017` was eaten by the `2049` match and the
  real year was never seen. Zero-width boundaries fixed it.
- **`S05E14` left a stray "S" on every title.** Anchoring on the first capture
  is the season *number*, not the token.
- **`DDP5.1`, `DD5.1`, `AAC2.0`** lost their audio codec: the channel count is
  welded on with no separator.
- **`DD-EX`** was read as a group named EX. It is Dolby Digital EX — a
  vocabulary gap, not a logic bug.

#### Bugs the fuzzer found, both within 60 seconds

- **Invalid UTF-8 grew the string.** `strings.Map` substitutes U+FFFD, three
  bytes where there was one, so a *sanitised* title could come out longer than
  its input. Invalid bytes are now dropped.
- **`0001.01.01` set the year to 1.** The air-date path wrote the year without
  the plausibility check the film path uses.

#### Performance: 361µs → 184µs, then stopped on purpose

Both fixes came from a profile and neither was visible by reading:

- Regexes compiled **inside** `Parse` — and `leftovers()` did it once per token.
- `knownTerm` ran **sixty separate patterns per token**: 55% of total time. Now
  one anchored alternation built at init.

What remains is `collect()`, one scan per additive vocabulary. Merging those
would roughly halve it again, and it has **not** been done: that means
restructuring the most correctness-critical matching in the package to speed up
a background task that already handles a thousand-result page in 0.2s.
`BenchmarkParse` is in the tree so the next person sees the number first.

### Increment 2c — indexer clients ✅

Torznab/Newznab over the egress guard. An indexer is a third party the operator
chose to trust for **search**, which is not the same as trusting it with the
process: its response is XML written by somebody else, carrying URLs written by
the uploader, and every field reaches a database, a UI and a download client.

| Component | Notes |
|---|---|
| **Cannot reach the network except through the guard** | And the tripwire was strengthened to make that structural. `TestNoPackageDialsDirectly` now bans `http.Transport` outright in acquisition packages, because the dangerous version is the one that looks fine: a literal that simply omits `DialContext` works perfectly and silently leaves by the wrong route. Construction moved to `egress.Guard.HTTPClient`, so there is no legitimate use left to carve an exception for — **verified by planting one and watching the build fail** |
| **XXE and billion-laughs are impossible, and it is TESTED** | Go's `encoding/xml` resolves no external entities and expands no internal ones. That is a claim about somebody else's code, so `TestXXEIsImpossible` throws `file:///etc/passwd`, an SSRF fetch at `169.254.169.254`, and a parameter entity at it, and asserts nothing resolved |
| **Responses are bounded before parsing** | 8 MiB cap applied to the **decompressed** stream, not `Content-Length` — which the indexer controls. A test sends a small Content-Length with an oversized body |
| **Every result URL is re-checked** | The download link is the uploader's, not the operator's. Results pointing at loopback, cloud metadata, `file://` or `gopher://` are dropped — not the feed, just the item |
| **Redirects are re-validated** | A redirect is a URL the far end chose *after* the request was made, so it has had no validation at all otherwise |
| **API keys are sealed and context-bound** | AES-256-GCM under `indexer:<id>:apikey`. A blob lifted from one indexer's row into another **fails to decrypt** rather than authenticating to the wrong tracker — tested by doing exactly that |
| **`List` never decrypts; `Enabled` does** | The key is not stripped from the admin response — it was never unsealed. "Can this endpoint return an API key?" is answered by which method it can reach, not by remembering to strip a field. The API interface deliberately does not expose `Enabled` |
| **The key never reaches a log or an error** | The protocol puts it in the query string, so it is in the *indexer's* log whatever we do. `*url.Error` prints the full URL; it is unwrapped, and every URL in a message goes through `redactURL` |
| **An empty key on update means "leave it alone"** | The alternative silently breaks every indexer the moment somebody renames one |
| **One bad item does not lose the feed** | A malformed item in a thousand-item response costs that item |
| **An unparseable date stays zero** | "Now" would make an old release look new and sort it to the top of every list |

Admin surface implemented: `GET/POST /api/v1/admin/indexers`, `PATCH` and
`DELETE` on `{id}`. All admin-hidden, all audited. **Verified against the
running binary**: the key goes in, the listing does not contain it, and a
`file://` indexer is refused at save time with a message naming the problem.

### Increment 2d — the search and decision pipeline ✅

The join between the three subsystems before it: indexers supply candidates,
`internal/release` parses and ranks them, a quality profile judges. This is the
first point at which the whole acquisition path is visible to a person —
`POST /api/v1/releases/search`.

| Component | Notes |
|---|---|
| **One indexer failing never fails the search** | Trackers are unreliable in *ordinary* operation. The response always carries which indexers answered, which failed and why, and `Partial()` says so in one call |
| **Partial results that look complete are the real hazard** | An operator who sees four results and does not know three indexers timed out concludes the release does not exist. The response says so as a boolean **and in words** |
| **Per-indexer deadlines, not just an overall one** | One slow tracker must not consume the whole budget and starve the others — which looks exactly like "nothing found" |
| **Fan-out is bounded to 5** | Hitting twenty trackers at once is how an operator earns a ban on all of them simultaneously |
| **Rejections come back, with reasons** | "Why did it not grab anything?" is the question this category of software is worst at answering. The answer is in the response, not in a debug log nobody enabled |
| **Deduplication by infohash, falling back to title+size** | Not title alone: two different encodes legitimately share a name, and collapsing them would silently discard the better one. The healthier swarm wins, and `SeenOn` records every indexer that offered it |
| **Seeders break ties, they do not drive the order** | Seeders is a liveness signal, not a quality one. Sorting by it first is how a library fills up with whatever happens to be popular |
| **Rejected candidates sort last** | So `Best()` and the top of the UI are never a refusal |
| **The search response carries no download URL** | A grab will go through a separate audited endpoint taking the candidate's identity. If the client supplied the URL it would choose what gets downloaded, and every check in the pipeline would become advisory |
| **Quality profiles are persisted, seeded once** | Re-seeding on every boot would quietly undo an operator's edits and resurrect profiles they deleted — the kind of behaviour people find infuriating and cannot explain. Compiling happens **before** the write, so a bad profile fails when it is saved |

#### Two real bugs the smoke test found that the unit tests could not

Both were found by starting the binary and using it, not by any test:

- **The kill switch enforced a policy nobody chose.** With
  `anonymity_enabled: false` — the default — the operator has not asked for a
  tunnel, yet every subsystem was paused because one did not exist. A fresh
  install with no WireGuard simply would not work, and the reason would be a
  health gauge nobody had looked at. Enforcement is now the operator's choice
  (`egress.Config.Enforce`), and the admin page says **in words** when egress is
  not being enforced rather than showing a green "healthy" beside nothing.
  Turning it off lifts the tunnel gate and *nothing else*: blocked profiles are
  still blocked and `DenyPrivate` still refuses.
- **The security lint refused the configuration the documentation recommends.**
  `download: direct` with the namespace guard on is the ADR-0001 deployment, and
  `config.example.yaml` says so in a comment — but the lint rejected it outright
  with "there is no configuration in which that is intended". An operator
  following the example could not boot, and the lint won the argument. It now
  refuses only the case that is actually wrong: direct with no guard **and** no
  proxy.

Also verified end to end against the binary: with anonymity on and no tunnel,
the search is paused and the error reads
`requesting https://tracker.example.org/api?apikey=REDACTED&...` — the API-key
redaction working in the place it matters, an error bound for a log.

### Increment 2e — the download engine and the acquisition path ✅

`anacrolix/torrent` behind the egress guard, a persisted queue, and the grab
path that joins search to download. **ADR-0014** records the reasoning; the
headline claims and how each is established:

| Claim | How it is established |
|---|---|
| Outbound peer connections cannot bypass the guard | `DialForPeerConns = false` empties the library's default dialer set, then `AddDialer` installs ours as the only one |
| That is true, not just intended | `TestAClosedGateStopsPeerConnections` — a real seeder and leecher on loopback with the gate shut. Flipping the flag back makes it fail in 0.11s with `THE GATE LEAKED: 262144 bytes transferred` |
| DHT/uTP cannot leak under a proxy | They are **forced off** by `ConfigFor` under a SOCKS5 profile; the engine will not let the two be configured apart |
| The degraded mode is visible | `Notes()` rides along with every queue listing, so "DHT is off, so magnets may never resolve" is on the same screen as the stuck magnet |
| A grab cannot be pointed anywhere | The endpoint takes a **sealed ticket**, never a URL — AES-256-GCM, bound to the grabbing user, 30-minute expiry |
| The indexer is still not trusted | The URL is revalidated before the fetch and **on every redirect hop**; the indexer is re-resolved against current state first |
| A restart does not orphan bytes | The queue is persisted with the magnet/`.torrent` verbatim and replayed locally — no indexer is contacted |
| Seeding obligations survive a restart | Cumulative totals live in the row; the per-process counter going backwards is detected as a restart |

Endpoints now real: `GET /api/v1/queue` (joins live progress with persisted
identity), `POST /api/v1/queue/{id}/remove` (audited), `POST /api/v1/releases/grab`
(audited on success *and* failure).

**Bugs this increment found and fixed**, several only visible against the
compiled binary or a live browser:

- The engine would not start on a host with no IPv6 stack. Real portability bug;
  fixed with a bind probe rather than an interface inspection.
- `DataPathFor` joined a caller-supplied string onto the data directory.
  `filepath.Join` *cleans*, so `../../etc/passwd` escaped the tree entirely. No
  caller passed unvalidated input yet — it was a trap laid for the import path.
- **Magnet-only indexers returned zero results.** The feed parser judged every
  download link by the fetch path's http(s)-only rule, so a `magnet:` enclosure
  was silently dropped. Silently, as an empty search rather than an error, which
  is the worst way for it to fail.
- The indexer client had **two URL validators** — a pre-flight check and a
  redirect check that could drift. Now one function on the `Client`, with a test
  asserting every constructor sets it.
- `added_at` was the zero time on every queue entry: the engine never set it.
  Found by reading the real binary's JSON.
- A test asserting malformed identifiers never reach the engine was **vacuous** —
  the fake only recorded *successful* removals, so it passed with validation
  disabled. Likewise `TestARedirectToLinkLocalIsStillRefused` was passing by
  refusing the test server's own loopback origin, never exercising a redirect.

**Not done here.** Completion does not hand off to an importer: nothing moves a
finished download into the library, renames or hardlinks it. That is Phase 3.
A finished transfer sits in the data directory with its row marked `complete`.

---

---

## Phase 3 — Library 🚧

### Increment 3a — root folders and path containment ✅

Phase 3 is the first time this software **writes to the operator's filesystem**,
and it writes using names chosen by strangers: a torrent declares its own
directory name, a release title comes from an indexer, a film title comes from a
metadata provider. `../../etc/cron.d/x` is a legal value for all three. So the
containment layer was built before anything that needs it, exactly as the egress
guard was in 2a.

**The usual approach is not enough, and it is worth being precise about why:**

| Approach | What defeats it |
|---|---|
| `filepath.Join` | It **cleans**, so `Join(root, "../../etc")` is not under root at all — it is `/etc` |
| Join + `HasPrefix` check | A **symlink** planted inside the tree. `<root>/x` → `/etc` has an innocent prefix |
| `EvalSymlinks` then check | Time-of-check-to-time-of-use: the link can be created between the check and the open |

What is used instead is `os.Root` (Go 1.24+), which holds a descriptor to the
root and resolves each component with `openat2(RESOLVE_BENEATH)`. Containment is
enforced by the **kernel**, per component, at the moment of use.

Measured rather than asserted — `TestTheVaultRefusesWhatTheObviousImplementationAllows`
keeps the naive Join+prefix version in the tree beside ours and shows it leaking:

```
the naive Join+prefix implementation leaked 3 of 4 paths:
  escape/secret.txt     -> SECRET
  etc/passwd            -> root:x
  ./escape/./secret.txt -> SECRET
```

`os.Root` refuses all four, through every operation: open, stat, create, mkdir,
remove, removeall, rename, link and copy.

| Claim | How it is established |
|---|---|
| No path escapes its root, by any route | `TestNoPathEscapesItsRoot` — 8 escape shapes × 8 operations, with real symlinks planted in the tree |
| Removing a symlink unlinks the **link**, not its target | `TestRemovingASymlinkDoesNotFollowIt` — otherwise "clean up this import" becomes "delete /etc" |
| Nothing goes around the containment layer | `TestNothingWritesOutsideAVault` reads the source and fails the build on `os.Create`, `os.Rename`, `os.Remove`, `os.Link`, even `filepath.Join`. Two files are allowed to name them, both documented |
| Every destructive method checks an effect | `TestEveryDestructiveMethodChecksAnEffect` parses the vault's own source — a new method added without the check would be invisible in review |
| A Manager cannot destroy bytes or repoint paths | `TestAManagerCannotDestroyBytesOrMovePathsThroughTheVault` — the check is in the **filesystem layer**, so it holds whatever route reaches it |
| The library cannot be deleted in one call | `TestTheRootItselfCannotBeRemoved` — `RemoveAll(".")` is refused; deleting a library is an operator's own decision |
| A stranger's name always lands inside the root | `TestASanitisedNameAlwaysLandsInsideTheRoot`, plus `FuzzSafeComponent` (2.1M executions, no failures) |
| Root folders cannot nest or overlap the downloads | `TestRootFoldersMayNotNest`, `TestARootMayNotOverlapTheDownloadDirectory` — verified against the binary too |

**Containment and sanitisation are kept as separate jobs**, because neither
substitutes for the other: a perfectly contained path can still be a filename an
operator cannot delete from a shell, and a perfectly tidy name can still be
`..`. Sanitisation keeps Unicode (a film's title in its own script is its
correct name), caps at 255 **bytes** not characters, cuts on a rune boundary,
preserves extensions, escapes Windows reserved device names, and refuses a name
that was entirely illegal characters rather than writing a file called `___`.

**Hardlink viability is checked when a root is added, by trying it** — not by
comparing `st_dev`, which bind mounts, overlayfs and btrfs subvolumes all get
wrong in both directions. Verified against a real filesystem boundary:

```
/dev/shm/cms-library   hardlinks=False
  "hardlinks are not possible from the download directory to here, so imports
   will COPY, using twice the disk space: invalid cross-device link"
```

That is a number an operator budgets around, and the point of checking now is
that they learn it at configuration time rather than from a full-disk alert.

**Bugs found this increment**

- `SafeComponent("/")` returned `"_"` — a file named from nothing, colliding
  with every other all-illegal name. Now refused so the caller can fall back to
  something meaningful.
- My own fuzz invariant ("output is never longer than input") was wrong: the
  Windows-reserved escape adds one byte deliberately, and truncation runs after
  it. Relaxed to a **bounded** +1 rather than deleted.
- The structural test caught my own new file the moment I wrote it. Rather than
  weakening the ban, the host-path boundary moved into `hostfs.go` with the
  invariant stated explicitly: what makes those calls safe is **provenance**
  (operator-supplied configuration), not containment.

### Increment 3b — the importer ✅

A download finishes and becomes a library entry. **ADR-0016** records the
reasoning. Verified end to end against the compiled binary, with a real
multi-file torrent:

```
imported  Blade Runner 2049 2017 1080p BluRay x264-SMOKE
  Blade.Runner.2049...SMOKE.mkv -> Blade Runner 2049 (2017)/Blade Runner 2049 (2017) [Bluray-1080p].mkv
  with 1 subtitle file(s)   hardlinked=true
```

and on disk:

| Library | Download |
|---|---|
| `Blade Runner 2049 (2017) [Bluray-1080p].mkv` (inode 975264) | the same inode 975264 — still seeding |
| `Blade Runner 2049 (2017) [Bluray-1080p].en.srt` | `Subs/2_English.srt` |
| — | `Sample/sample.mkv` — left behind |
| — | `readme.nfo` — left behind |
| — | `SMOKE_DO_NOT_MIRROR.exe` — **left behind** |

| Claim | How it is established |
|---|---|
| Nothing but a video container can be imported | An **allowlist**, not a denylist: `TestOnlyAllowlistedContainersCanBeChosen`, `FuzzSelectNeverChoosesANonContainer` (1.2M executions) |
| A download cannot name a file outside itself | `TestADownloadCannotNameAFileOutsideItself` — with the check removed, a file from outside is hardlinked into the library within one run |
| Samples are caught, real films are not | `TestSampleMarkersMatchWholeWords` and `TestAFilmWhoseTitleContainsASampleWordSurvives` — *Free Samples*, *Trailer Park Boys*, *Resampled* all survive |
| An upgrade is reversible | `TestAnUpgradeIsReversible` — the old file moves to trash inside the root, not to nowhere |
| Background work cannot delete media | `authz.TestTheImporterCannotDestroyMediaBytes`, `TestEachBackgroundTaskHoldsExactlyItsOwnGrant` (walks `AllPermissions` per task) |
| Background authority can only be minted where tasks are registered | `authz.TestOnlySchedulingCodeCanMintASystemPrincipal` — reads every package's source |
| Importing never consumes the download | `TestImportingNeverConsumesTheDownload` |
| Every outcome is recorded, including skips | `TestAnUnimportableDownloadIsRecordedWithAReason`; `GET /api/v1/queue/{hash}/history` |

**Bugs found this increment**

- **A real vulnerability.** The importer joined the torrent's declared file path
  to the download directory unchecked. `filepath.Join` cleans, so
  `../../../../etc/shadow` becomes `/etc/shadow` — hardlinked into a library
  served over HTTP. Caught by `TestNothingWritesOutsideAVault` firing on that
  line, not by review. Third time that technique has caught something.
- **Upgrades could never happen.** A same-quality PROPER lands on the identical
  filename, so the link failed with "file exists" and the upgrade silently did
  not occur. Found by a test; fixed by superseding first.
- **And then the fix was wrong**: it superseded the *new* path, so a
  different-quality upgrade found nothing there and left both files in the
  library. The existing file's own path is the one that handles both cases.
- **The sample rule discarded real films.** My first version matched the word
  anywhere in a filename, which loses *Free Samples (2012)*; while fixing it I
  found it also loses *Trailer Park Boys: The Movie*.
- **Language tags read from the wrong place.** Scanning a subtitle filename
  backwards still found "en" in `En.Route.2019.1080p.BluRay.srt` — backwards
  scanning only changes which wrong answer wins. Only the last two words may
  carry a tag.
- **Four token tests pinned to a 501.** They used `/api/v1/media` as a probe for
  "the credential was accepted" and asserted the exact status its stub returned,
  so implementing the handler broke them. Now they assert the property.

**Not done.** Nothing purges the trash; superseded files accumulate. Upgrade
decisions use the default quality ladder rather than the profile that grabbed
the release. Season packs are refused rather than split. Identification is from
the release name alone — no metadata provider, so no posters, no external ids,
and no record of episodes the instance does *not* have.

### Increment 3c — the library scan ✅

An operator adopting this software already **has** a library — that is the point
of replacing the stack they are running. Until a scan existed, CMediaStack could
see none of it.

**The rule a scan must never break: it changes nothing on disk.** Some software
in this category "organises on scan", and that is how people lose libraries — a
parser misreads a title, a thousand directories are renamed, and there is no
undo. `TestAScanNeverChangesAnythingOnDisk` snapshots every path, size and
content hash before and after and requires them identical. Teaching the scan to
rename files fails it immediately.

**The guard that matters most.** If a mount fails, a root folder appears *empty*.
A scan that dutifully marked everything missing would erase the library record of
every file on that disk in one pass, with nothing on disk changed to explain it.
So a scan that finds more than 20% of a root's known files absent **refuses**,
changes nothing, and says what is actually likely:

```
most of this root folder's files are missing: 10 of 10 recorded files are not on
disk in /media/movies. Nothing has been changed. This is far more often an
unmounted disk than a deletion — check the mount before treating these as gone
```

Verified against the binary on a deliberately messy library — several layouts,
artwork, extras, a NAS housekeeping folder, and a symlink pointing at `/tmp`:

```
=== scan root 1 (movies) ===
   9 file(s) examined, 4 added, 0 updated, 3 skipped
     skipped: Arrival…/Sample/sample.mkv   (a sample, trailer or extra by its path)
     skipped: Arrival…/keygen.exe          (not a media container this software can play)
     skipped: escape                       (a symbolic link, which is not followed)
=== library ===
   movie  Arrival (2016)          movie  Dune (2021)
   movie  the matrix (1999)       series The Expanse (2015)
```

`@eaDir` was never even descended into; the tree was byte-identical afterwards.

| Claim | How it is established |
|---|---|
| A scan changes nothing on disk | `TestAScanNeverChangesAnythingOnDisk` — full-tree content hashes before and after |
| A vanished library is refused, not recorded | `TestAVanishedLibraryIsRefusedRatherThanRecorded`, `api.TestAVanishedLibraryIsRefusedOverHTTPWithAnExplanation` (409, not 500 — nothing is broken) |
| A few genuine deletions are still reported | `TestAFewDeletedFilesAreReportedNotRefused` — the guard must not fire on the ordinary case |
| Symlinks out of a root are never indexed | `TestAScanDoesNotFollowSymlinksOutOfTheRoot` — `fs.WalkDir` over an `os.Root` reports them and does not descend, verified empirically before being relied on |
| The trash and NAS housekeeping folders are skipped | `TestAScanSkipsTheTrashAndHousekeepingFolders` — descending the trash would re-import an operator's own upgrade history as new media |
| Artwork does not drown the report | `TestArtworkAndMetadataFilesAreNotReportedAsProblems` — one `.exe` is visible among five quiet files |
| Scanning twice does not duplicate | `TestScanningTwiceChangesNothing` |
| The listing does not leak the directory layout | `api.TestTheLibraryListingDoesNotLeakAbsolutePaths` |

**Bugs found this increment** — both against the binary, on a real library:

- `the.matrix.1999.720p.brrip/matrix.mkv` recorded as **"matrix", no year**, and
  `The Expanse (2015)/Season 02/…S02E05…` as **"The Expanse", no year**. The
  folder was only consulted when the filename yielded *nothing at all*, and a
  thin filename inside an informative folder is extremely common. The folder
  says what the item **is**; the filename says which file it **is**. Now parsed
  separately.

**Not done.** A scan does not notice a file that moved (it reads as one missing
and one added). Files found by a scan are recorded as not hardlinked, because
this software did not place them and cannot know.

### Increment 3d — deletion, the trash, and per-task authority ✅

The destructive half of the library, and the gap flagged in 3b and 3c. See the
**addendum to ADR-0016**.

**Deleting media does not delete media.** A library is often the only copy and
clicking the wrong row is the likeliest failure by a wide margin, so a delete
moves files to the root's trash and forgets their records — nothing is unlinked.
The retention window (7 days) **is** the undo. There is deliberately no "delete
and purge now" parameter: a checkbox that destroys data on a mis-click is how
reversibility gets lost.

Verified end to end against the binary:

```
DELETE  -> {"deleted":"Arrival","trashed":1,"note":"…Nothing was unlinked."}
on disk -> movies/.cmediastack-trash/20260912-215333.999-Arrival.2016.1080p…mkv
TRASH   -> retention=168h0m0s  purge_after=2026-09-19T21:53:33
RESTORE -> "Arrival (2016)/Arrival.2016.1080p.BluRay-GRP.mkv"
```

**The shared background grant turned out to be wrong, and the purge proved it.**
The purge must unlink; the importer must not. A shared permission set with
`PermDeleteMediaFiles` in it would have handed destroy authority to the importer
as a side effect of building a cleanup job. Authority is now **per task**, and
every task's grant got *smaller*:

| Task | Grant | Notably absent |
|---|---|---|
| `system:import` | browse, path-mutation | **destroy** — that is what makes an upgrade reversible |
| `system:library-scan` | browse only | **everything else** — a scan changes nothing on disk |
| `system:trash-purge` | browse, destroy | **path-mutation** — it unlinks, it does not rearrange a library |

An unknown task gets nothing at all: a typo must produce work that cannot act,
not work that inherits somebody else's authority.

| Claim | How it is established |
|---|---|
| Deleting media never unlinks | `importer.TestDeletingAnItemTrashesItRatherThanUnlinkingIt`, `api.TestDeletingMediaTrashesItAndSaysSo` |
| The undo actually works | `importer.TestATrashedFileCanBeRestored` (and a scan picks it back up), `api.TestATrashedFileCanBeRestoredOverHTTP` |
| The retention window is real in both directions | `importer.TestAPurgeLeavesFilesInsideTheRetentionWindow`, `TestAPurgeUnlinksFilesPastTheirRetentionWindow` |
| Only the purge principal can purge | `importer.TestOnlyThePurgePrincipalCanPurge` — the importer, the scanner and anonymous all fail |
| Each task holds exactly its own grant | `authz.TestEachBackgroundTaskHoldsExactlyItsOwnGrant` — walks `AllPermissions` per task, and fails if a new task is undeclared |
| A Manager still cannot delete media | `api.TestAManagerCannotDeleteMediaOverHTTP` — the route is invisible and the file survives |
| Restore cannot become "move any library file" | `api.TestRestoreOnlyAcceptsPathsInTheTrash`, `importer.TestRestoringCannotEscapeTheRoot` |
| A hand-placed trash file is not purged as infinitely old | `importer.TestAHandPlacedTrashFileIsNotTreatedAsInfinitelyOld` |
| A zero retention is refused, not honoured | `importer.TestAZeroRetentionIsRefused` |
| A new Vault method that writes without an authority decision fails the build | `library.TestEveryDestructiveMethodChecksAnEffect` — now also catches undeclared writers; verified by adding a plausible `Tidy()` |

**Not done.** ~~No per-file delete (only whole items)~~ — built in 6c (ADR-0053). Restoring does not put the
file back at its original path — that is not recorded, deliberately. Nothing
warns before deleting an item whose bytes are shared with a still-seeding
download, though the file listing already reports `hardlinked`.

### Increment 3e — the web UI for everything built so far ✅

Until now every capability was reachable only with `curl`, while §2 promises
**one web UI**. Five new views — Library, Search, Queue, Storage, Indexers —
following [ADR-0011](docs/adr/0011-server-rendered-shells-no-build-step.md): no
framework, no build step, no `innerHTML` anywhere.

Driven end to end in real Chromium against the compiled binary: **zero CSP
violations, zero script errors**, through add-root-folder → scan → browse →
filter → file detail → search → add-indexer → queue → delete → trash → restore.

Decisions worth recording:

- **Progress bars are `<progress>`**, not styled divs. The CSP forbids inline
  style and this project's own test bans `element.style.x` outright, so a div
  whose width is set from JavaScript is not available — and the native element
  is the accessible answer anyway: a screen reader announces it.
- **Rejected search results are shown**, with the reason, because "why did it
  not grab anything" is the question this software category is worst at
  answering. They carry no Grab button and say why not.
- **Colour is never the only signal.** Every accepted/rejected row also carries
  the reason in words.
- **The engine's notes sit on the queue screen**, not in a log — "DHT is off, so
  magnets may never resolve" belongs above the stuck magnet it explains.
- **A transfer with no queue row is shown as `unrecorded`**, with a line saying
  it will vanish on restart. That is exactly the state worth surfacing.
- **The indexer API-key field is cleared on submit**, and asserted absent from
  the whole DOM afterwards: a key sitting in a form field is one screenshot away
  from disclosure.
- **The file listing says "Shared with a download: yes — still seeding"**, so an
  operator deleting a file knows the same inode is being served to strangers.

**Bugs found this increment**

- I invented a parallel `.row` / `.row-head` class vocabulary when the UI
  already had `.item` / `.title` / `.meta` / `.actions`. Two vocabularies mean
  every style change has to be made twice and drifts the first time somebody
  forgets. Unified on the existing one.
- **A new test caught a real defect in my own code**:
  `TestEveryDataViewHasALoader` found the Search view had no loader, so leaving
  the tab and returning showed the *previous* search's results as current —
  with Grab buttons whose tickets had been expiring the whole time.
- The trash panel read **"kept for 168h0m0s"**. An operator should not do
  arithmetic to learn that means a week; now "kept for 7 days".
- My first attempt at that fix silently did not apply — `gofmt` had realigned
  the map keys, so the patch matched nothing and the tests still passed. Caught
  by re-running the browser test rather than trusting the edit.

| Claim | How it is established |
|---|---|
| No script builds markup from strings | `web.TestNoScriptBuildsMarkupFromStrings`, `web.TestScriptsAvoidBlockedAndUnsafeAPIs` — release names are written by strangers |
| Every nav entry has a section and every section a nav entry | `web.TestEveryNavEntryHasASectionAndEverySectionHasANavEntry` — a mismatch is silent in both directions |
| Every data view has a loader | `web.TestEveryDataViewHasALoader` — which is how the stale-results defect above was found |
| Zero CSP violations in a real browser | The Chromium run collects every CSP refusal and script error and fails on any |

**Not done.** No requests/approvals surface for ordinary users (Jellyseerr's
job), no playback, no metadata artwork. The UI polls nothing — a download's
progress updates when you revisit the Queue tab, not live.

---

### Increment 3f — admin user management ✅

The gap this document has called *"the only gap that will actually bite"* since
Phase 1. `GET /api/v1/admin/users`, `PATCH /api/v1/admin/users/{id}`, and an
**Accounts** view: list every account, suspend, reactivate, change role.

Small on the surface. The interesting part is what happens when you ask the
question this endpoint makes askable for the first time: **can an operator lock
themselves out of their own instance?**

**What protects it today, and what does not.** `authz.CanModifyUser` does nearly
all the work: no account may modify itself, a peer, or a superior. Admin is the
top rank, so today the last administrator cannot be suspended or demoted by
anybody — including themselves. The last-admin guard below is therefore
currently unreachable, and `internal/identity/admin.go` says so rather than
claiming a protection that never fires.

**The guard was half-wired, and a mutation test found it.** Disabling
`CanModifyUser`'s two rank rules — simulating exactly the world role editing
will create — produced this:

```
the admin demotes itself         -> 409 refused   (guardLastAdmin fired)
the admin suspends itself        -> 200 OK        <- no guard on this path
a manager suspends the admin     -> 200 OK        <- none here either
                                    0 administrators left
```

`guardLastAdmin` was called from `ChangeUserRole` and not from `SuspendUser`.
A guard that covers one route to an outcome and not the other is not a guard.
Fixed, and the same mutation now passes: with both rank rules off, all four
lockout attempts are refused and the instance keeps its administrator.

**A §8 audit gap, found by reading `audit_event` out of a running instance.**
Refused suspensions were logged; refused *role changes* were not. So
"can I promote myself to Admin?" was refused and then invisible — the single
most interesting line an audit log can carry, missing. Denials decided inside a
service are exactly the ones route-level logging cannot see, because reaching
the route already required the permission. Now `user.role.change`,
`user.role.assign`, `user.reactivate` and `user.suspend` all record refusals
with actor, source IP and user agent.

**A limitation worth stating plainly: an instance can never have a second
administrator.** Every path that assigns a role goes through
`authz.CanAssignRole`, which is strictly downward, and role editing is not
built. The setup wizard creates one Admin and closes permanently. Visible in the
running instance — `GET /api/v1/roles` returns User and Manager only. **This is
a bus factor of one**: lose both the authenticator and the recovery codes and
recovery is a database edit. `api.TestAnInstanceCanNeverHaveASecondAdministrator`
pins it so it stays a decision rather than a surprise. **It should be fixed**
(see *Risks being carried*).

**Smaller decisions**

- **`last_login_at` is not set by enrolling an authenticator**, only by a
  completed sign-in. It looks like an omission; it is not. An account that
  enrolled once and never came back must read "never", or the one column an
  operator uses to find dormant accounts reports the opposite of the truth.
  Verified both ways against the binary.
- **Reactivation does not hand back access nobody ever had.** An account that
  never enrolled returns to `awaiting_mfa`, not `active` — reactivation restores
  the access somebody had, it does not grant access they never established.
- **The list omits email** (§8 PII minimisation). An account screen acts on
  usernames; nothing here needs to mail anybody.
- **A PATCH must do exactly one thing.** State and role in one request is
  refused with 400: it makes the audit line ambiguous about what was intended.
- **Rows you may not act on say why**, rather than silently losing their
  buttons. "You may not act on yourself, a peer or a superior" is not a rule
  anybody infers from an absence.
- **409, not 403, for the last-admin refusal.** The actor *has* the authority;
  the instance is refusing the outcome. An operator should be able to tell those
  apart, and "internal error" would send them hunting a bug.

| Claim | How it is established |
|---|---|
| No path leaves the instance without an administrator | `api.TestTheLastAdministratorCannotBeRemoved` — four paths, count re-checked after **each**, and it passes with both authz rank rules disabled |
| The guard itself is correct, not just unreachable | `identity.TestTheLastAdminGuardBites`, `identity.TestTheGuardAllowsWhatIsSafe` |
| Only an administrator who can sign in counts as one | `identity.TestOnlyAnAdministratorWhoCanSignInCounts` — suspended and un-enrolled admins are not rescue paths |
| Refused escalations are audited with provenance | `api.TestARefusedEscalationIsRecorded` — asserts the record, its actor and its source IP |
| Reactivation never bypasses MFA | `api.TestReactivationDoesNotHandBackAccessNobodyEverHad` |
| A role change reaches an already-open session | `api.TestARoleChangeAppliesToAnAlreadyOpenSession` — both directions; demotion is the one that matters |
| A Manager cannot neutralise an Admin | Covered in the lockout test; escalation by *denial* works as well as escalation by permission |
| The second administrator problem is known | `api.TestAnInstanceCanNeverHaveASecondAdministrator` |

Driven in real Chromium against the compiled binary: Accounts tab → suspend →
badge flips → reactivate → back to `awaiting mfa`, **zero CSP violations, zero
script errors**, and the admin's own row correctly offering nothing but an
explanation.

**Not done.** No account *deletion* (suspension is the reversible answer and
deletion needs its own decision about what happens to that user's history). No
per-user permission overrides. No audit-log viewer — the data is all there
*(4w built it)*.
No password-reset-link button in the UI, though the endpoint exists.

---

### Increment 3g — break-glass recovery ✅

**I talked myself out of the fix I recommended at the end of 3f, and this is the
better one.**

The risk was real: one administrator, forever, so losing both the authenticator
and the recovery codes means a database edit. My proposed fix was to let an Admin
assign the Admin role. Working through it, that is worse:

> Promoting somebody to Admin makes them a **peer**, and `CanModifyUser` refuses
> action on a peer — so the promotion cannot be undone from inside the
> application. Today a stolen admin session lasts as long as the session: it
> cannot change the password (that needs the old one) and the real operator can
> revoke it. With peer promotion available, the same stolen session mints a
> second administrator with its own password and its own authenticator, and
> revoking sessions stops helping. That trades a recovery problem for a
> **persistence** problem — and the recovery problem needs bad luck, while the
> persistence problem needs an attacker.

So recovery went where an operator's authority actually comes from: the host.

```
$ cmediastack -config /config/config.yaml -recover jacob

Recovered "jacob" (Admin).
  was:              active, had an authenticator
  authenticator:    cleared, along with its recovery codes
  password:         unchanged
  sessions revoked: 3
  tokens revoked:   0

The account is now awaiting_mfa. […] Nothing here bypasses the second factor.
```

Using it needs a shell on the machine, the data directory and the master key —
strictly stronger authentication than anything the application can offer — and
it adds **no reachable surface**: no route, no permission, no principal leads
there. It also covers the case a second administrator does not: it works whether
or not you remembered to plan ahead.

**What it deliberately does not do**

- **It does not hand back a session.** The account returns to `awaiting_mfa` and
  goes through normal enrollment. MFA is mandatory (§13); a recovery path that
  skipped it would be the way *around* that requirement, not an exception to it.
  Proven against the binary: after recovery the password alone answers `enroll`,
  and an admin route still returns 409 until a new authenticator exists.
- **It does not grant or change a role.** You get your administrator back; you
  cannot make one.
- **It takes no password on the command line.** An argument lands in shell
  history and in `ps` output for every user on the box, which would make the
  recovery console a way to *leak* a credential rather than replace one. Stdin
  only.
- **It fails if it cannot be audited.** Everything else in `identity` treats an
  audit failure as non-fatal; this does not. An unrecorded credential reset on an
  administrator is indistinguishable from an attack. The record's actor is
  `console:recovery` with no user id — the actor is whoever had a shell, and
  claiming otherwise would be a lie in the one log that must not contain any.
- **Old recovery codes die with the enrollment they belonged to**, and the TOTP
  replay counter resets — a high-water mark carried across to a new secret would
  refuse a legitimate first code at some times and not others.

| Claim | How it is established |
|---|---|
| Nothing in the application can reach it | `identity.TestOnlyTheCommandLineCanRecoverAnAccount` — reads every package's source; fifth use of the technique. Verified by adding a call in a handler and watching it fail |
| Nothing else can un-enroll an account | `identity.TestOnlyRecoveryCanClearAnEnrollment` |
| Recovery never bypasses the second factor | `identity.TestRecoveryRestoresAccessWithoutBypassingMFA`, and end to end against the binary |
| It is also the answer to a compromise | `identity.TestRecoveryRevokesEverythingTheAccountHeld` — the session is proven live *before* the recovery, so the assertion cannot pass vacuously |
| A console is not a way to set a weak password | `identity.TestRecoveryCanReplaceAPasswordButNotAWeakOne` |

Verified end to end: locked out → `-recover` → old session dead (404) → password
answers `enroll` → admin route still 409 → new authenticator, 10 fresh recovery
codes → administering again.

**Still true, and still a limitation:** an instance has one administrator. That
is now a *resilience* choice with a documented recovery path rather than an
unrecoverable corner, which is a different thing from being fixed.

---

### Increment 3h — acquisition requests ✅

The Jellyseerr half of the brief, and the last thing standing between this and
being usable by somebody who is not the operator. See
**[ADR-0017](docs/adr/0017-acquisition-requests.md)**.

**Approving a request downloads nothing, and that is the central decision.**
Overseerr approves-and-fetches; that is the obvious design and the wrong default
here. §13 makes the operator answerable for whatever this instance acquires, and
automatic fulfilment means the *machine* matching a title to a release — the
exact step increment 3b already found unreliable, where a sample rule discarded
*Free Samples (2012)* and a language tag read `En.Route.2019` as English. A
wrong match there produced a wrong label; a wrong match here produces an
acquisition. So the machine narrows and a human chooses, and an approved request
with no release chosen says so in words on the row rather than looking finished.

**Who sees whose request is a SCOPE, not a permission.** A user may list
requests — their own are in there. Expressed as a permission alone that
difference disappears and everybody reads everybody's watchlist. The route is
gated on `request.submit`; the scope is computed from the principal and passed
to the store as a filter. Mutation-tested: removing it makes each user see every
other user's list.

**Duplicate detection is deliberately crude, because the two errors are not
equal.** A *missed* duplicate costs a human a moment. A *false* one silently
attaches somebody to a request for a different film and their own request is
never made. So `MatchKey` normalises spelling and punctuation and refuses to
normalise meaning: `Dune` is not `Dune: Part Two`, and `Dune` with no year is
not `Dune (2021)`. `Rocky II` vs `Rocky 2` is an accepted miss, recorded as one.

**The rule's own test found the only real bug in it**: `WALL·E` and `WALL-E` —
the two commonest spellings of one film — did not collide, because the middle
dot vanished while the hyphen became a separator.

**Bugs the browser found that a green test suite did not**

Both are the same shape: correct in the database, wrong by the time a person
read it.

- **The requester's own note was overwritten by the server's message.** Every
  other endpoint uses `note` for its own explanation; a request already has a
  `note` the person typed. A request submitted with "rewatch night" came back
  saying "Waiting for approval." — and the UI showed that as though the
  requester had written it. Found by reading the binary's JSON.
- **Every client-side validation message claimed the server was unreachable.**
  `fail({status: 0}, '…')` reuses the error banner and looks harmless, but
  `failure()` maps status 0 to "could not reach the server" — so an operator who
  left a field blank was told their server was down. Wrong in **three** places
  since the approvals view was written.

**Also in this increment:** `api.NewHandlers` had grown to seventeen positional
parameters, several of them interfaces that one concrete type satisfies —
`*importer.Importer` is passed as *both* the scanner and the deleter, so two
adjacent arguments could be swapped and everything would still compile and run
with the wrong dependency answering. Now `api.New(api.Deps{…})`.

| Claim | How it is established |
|---|---|
| A user reads only their own requests | `api.TestAUserSeesOnlyTheirOwnRequests` — removing the scope makes every user see everybody's |
| Approval acquires nothing | `api.TestApprovalDownloadsNothing`, and verified against the binary: the queue stays empty |
| A second asker joins rather than duplicating | `api.TestASecondPersonAskingJoinsTheFirstRequest`; enforced underneath by a partial unique index |
| Meaning is never normalised away | `request.TestMatchKeyCollapsesNoiseAndNothingElse` — eight pairs a looser rule would wrongly merge |
| One account cannot fill the queue | `api.TestAUserCannotFloodTheQueue` — and the cap does not block anybody else |
| A request can only be decided once | `api.TestARequestCanOnlyBeDecidedOnce` — the check is in the UPDATE's WHERE clause, not a read-then-write |
| A denial carries a reason the requester sees | `api.TestADenialMustCarryAReasonTheRequesterCanSee` |
| Only a real import closes a request | `importer.TestAnImportThatDidNotImportClosesNothing` |
| Bookkeeping failure never fails an import | `importer.TestAFailureToCloseARequestDoesNotFailTheImport` |
| The requester's own words survive | `api.TestARequestKeepsTheRequestersOwnNote` |
| No validation message poses as a network failure | `web.TestNoValidationMessageMasqueradesAsANetworkFailure` |

Driven in real Chromium against the compiled binary as **two different people**:
a User submitting and seeing only their own, an approver seeing all five with
follower names — zero CSP violations, zero script errors.

**Not done.** No watchlist — a request for an unreleased film has nowhere to
wait, and "keep looking until it appears" needs its own decision about how often
an instance may query an indexer unprompted. No notifications of any kind: a
requester learns their request was fulfilled by looking. No request editing or
withdrawal. `POST /api/v1/issues` is still unimplemented — "this file is broken"
is a different workflow from "please get this".

---

### Increment 3i — metadata and artwork ✅

Closes the absence ADR-0016 recorded deliberately. See
**[ADR-0018](docs/adr/0018-metadata-and-artwork.md)**.

This adds three things this software did not have: an outbound dependency on a
commercial service, a long-lived credential for it, and **a path by which bytes
and filenames chosen by a third party reach the operator's disk**. The third is
the same shape as the import pipeline, which is where this project's one real
vulnerability was found — so the order was the same as Phases 2 and 3: the jail
first, the thing inside it second.

**A provider's string never reaches a filesystem path.** TMDB returns
`poster_path: "/f89U3….jpg"`; nothing but TMDB's own correctness stops it
returning `"/../../../../etc/cron.d/x"`, and `filepath.Join` *cleans*, so the
naive version succeeds. The destination is built from values this software chose
and validated; the provider's string builds the URL and nothing else. And the
write goes through `os.Root` anyway — **demonstrated, not asserted**: with both
application-level checks disabled, the kernel refused every traversal
(`the path leaves its root folder: mkdir "../../etc/cron.d"`) and nothing was
written outside the cache.

**`library.Cache` — containment without media authority.** `Vault` is
containment *and* media effects; caching a poster must not require the authority
to rearrange a library. Same `os.Root`, no permission checks — which makes one
mistake catastrophic, so a Cache requires a marker file and refuses to remove
it. A media folder does not have one.

**The bytes decide what a file is, and that is load-bearing in production
today.** These images are served back to a browser from this app's own origin,
so an HTML file cached as `.jpg` is stored XSS against the operator. The type
comes from magic bytes; SVG is refused outright (it is XML and executes script).
The live CDN proved the point: **a URL ending `.jpg` returns a WebP**, because
BunnyCDN content-negotiates on the Accept header this code sends.

**The half-tunnelled instance is now a configuration error.** An indexer search
says "somebody here wants X"; a metadata lookup says "this instance *holds* X",
and an artwork fetch says it again from a second host. Proxying the first and
leaving the others direct does not protect an operator — it gives them a belief
that stops them looking. The lint refuses it, and deliberately does *not* count
a namespace-guarded `direct` as proxied: that guard covers the download
process's namespace, and the application process does not run in it.

**The credential is sealed in the database and never readable back** — not
masked, not the first few characters, not to an administrator. A key that does
not work is **never stored**: it is checked before it is written, and the
response says so, or an operator who thinks a bad key was saved goes hunting for
a way to remove it.

**What is verified and what is not, stated plainly.** Verified against the live
service: the API base, the exact 401 envelope (used as a test fixture), the
absence of rate-limit headers (so this code parses none and handles 429 by
status), the image CDN's behaviour, and the full admin round trip through the
compiled binary.

**Then an API key arrived and the rest was verified too — see 3j below.**

| Claim | How it is established |
|---|---|
| A provider's string cannot escape the cache | `artwork.TestAProvidersPathNeverReachesTheFilesystem` — and it still holds with both application checks disabled |
| The destination comes from checked values only | `artwork.TestTheDestinationIsBuiltFromCheckedValues` — a malformed ref is refused before anything is fetched |
| Only real images are cached | `artwork.TestOnlyRealImagesAreCached` — HTML, SVG and a near-JPEG refused while the host insists they are images |
| It works against the real CDN | `artwork.TestAgainstTheRealImageCDN` — caught two bugs fixtures could not |
| A Cache cannot be silently pointed at a library | `artwork.TestACacheRefusesToOpenSomethingThatIsNotACache` |
| A new Cache writer is a decision | `library.TestEveryDestructiveMethodChecksAnEffect`, extended past the `*Vault` receiver |
| Artwork cannot bypass the tunnel | `egress.TestNoPackageDialsDirectly`, with `../artwork` added |
| A half-tunnelled config is refused | `config.TestAHalfTunnelledInstanceIsRefused`, and `TestTheNamespaceGuardDoesNotExcuseDirectMetadata` |
| The credential is never returned | `api.TestTheMetadataCredentialIsNeverReturned` — including any 12-character prefix of it |
| A bad key is not stored | `api.TestARejectedKeyIsNotStoredAndTheAnswerSaysSo`, and confirmed against the live API: the settings table stayed empty |
| The real 401 envelope is understood | `metadata.TestTheRealErrorEnvelopeIsUnderstood` — the exact bytes TMDB returned |
| A thin response is a thin result, not a failure | `metadata.TestAThinResponseIsAThinResultNotAFailure` |
| Films and series field names are both handled | `metadata.TestSeriesAndFilmFieldNamesAreBothUnderstood` — including the differently-named year parameter |

**Bugs the real service found that fixtures could not**

- **`url.ResolveReference` silently discarded the `/t/p/w200` prefix.** A
  provider path beginning with `/` is *root-relative*, so resolving it against
  the base produced `https://image.tmdb.org/f89U….jpg`. Every fixture agreed
  with the mistake, because a fake server answers whatever path it is asked for.
  The real CDN answered 404. The fix is stronger than the original: a provider
  may supply a plain **filename** and nothing else.
- **A `.jpg` URL returns a WebP.** See above.
- **An unconfigured provider reported `{"ok":false,"detail":""}`** — the exact
  "something is wrong and nothing about what" pattern this code's own comments
  complain about, and nothing *was* wrong. Found by reading the binary's JSON.

**And one unrelated finding, surfaced by the sweep rather than by the work.**
`search.TestATamperedTicketIsRefused` failed once — *"a ticket with byte 266
changed was accepted"* — and had never failed before. A flaky test about
tampering is the worst kind, because the lesson people learn from it is to
re-run it.

The cause is base64 malleability, measured rather than guessed: when the sealed
length is not a multiple of three, the final character carries bits nothing
reads, and **15 other characters decode to identical bytes**. The test flips
each byte to `'A'`, and `'A'` is sometimes one of the fifteen. Ticket lengths
vary with content, so it fails perhaps one run in several.

Not a forgery: the ciphertext is identical and the AEAD is behaving correctly.
But one ticket had sixteen valid tokens, and anything that ever keyed on the
token *string* rather than on what it decodes to — single-use tracking, rate
limiting, correlating an audit line to a grab — would have treated them as
different tickets. `Open` now decodes `.Strict()`, so a ticket has exactly one
spelling.

`search.TestATicketHasExactlyOneSpelling` pins it, and — the part that matters —
it **searches for a ticket length that has slack** rather than sealing one fixed
ticket and hoping. Without that it would have been intermittent in exactly the
same way as the test it replaces; with it, removing `.Strict()` fails every run
with "15 alternative spellings accepted".

**Not done.** Nothing uses the provider yet: an operator can configure it, prove
it works and search it by hand, but no library item is identified automatically
and no artwork is fetched, because nothing yet knows which title is which. That
matching is the next increment and needs its own decision — a wrong automatic
match relabels somebody's library. No episode table, so ADR-0016's absence
stands: still no "wanted", no calendar, no season percentages.

---

### Increment 3j — verified against the live API ✅

3i shipped with one thing unproven: no *successful* TMDB response had ever been
parsed, because the instance had never had a key. One arrived, so it was
settled. See the **addendum to [ADR-0018](docs/adr/0018-metadata-and-artwork.md)**.

The verification is not a note saying it was done. It is
`internal/metadata/tmdb_live_test.go`, which runs when `CMS_TMDB_TOKEN` is set
and skips when it is not:

```
CMS_TMDB_TOKEN=eyJ... go test ./internal/metadata/ -run Live -v
```

The suite must stay runnable by anyone, offline, with no credential — but *"do
the struct tags match reality?"* has to be answerable by running something
rather than by reading documentation again.

**What held.** Everything the code depends on, first run: image base and its
seven poster sizes; a film's `title`/`original_title`/`release_date`/`overview`/
`poster_path`; a series' `name`/`first_air_date` and the separately-named
`first_air_date_year` parameter; a film's `imdb_id`, `runtime` and `genres`;
and the round trip with the most ways to go wrong —
`append_to_response=external_ids` giving a series' IMDb id in **one** request.

Through the compiled binary: the key is verified before storage, **sealed at
rest** (267 bytes of ciphertext, no plaintext and no `eyJ` prefix in the row),
never echoed back, and a real search returns real results.

And the composition, which is where a mismatch between two separately-evidenced
halves would live: a real poster path from a real search, through
`internal/artwork`'s checks, onto disk at `poster/tmdb/329865-w342.webp`. Note
the extension — the CDN served **WebP for a path ending `.jpg`**, again.

**What did not hold — two findings.**

- **`episode_run_time` is empty.** Documented as a list of typical episode
  lengths; the live API returns `[]` for Severance and apparently most series,
  so the fallback that read it yielded zero for every series. Now falls back to
  `last_episode_to_air.runtime`, with an honest comment rather than a confident
  one: that is the length of *one* episode, often a finale, which is the least
  typical there is. For anything that actually depends on duration the **file**
  is the authority, not this.
- **My own test assertion was wrong about seasons.** It required every season
  above zero to have episodes. Severance season 3 is announced, unaired,
  `episode_count: 0`, null `air_date` — real, correct data that the assertion
  called a failure. The rule is now conditional.

That second case is worth more than its fix. An announced season the instance
cannot possibly have files for is exactly what ADR-0016 refused to invent,
arriving from the provider as a first-class fact. When the episode table is
built, that row must not become "0% complete" and must not become nine imaginary
episodes either.

| Claim | How it is established |
|---|---|
| The struct tags match the live API | `metadata.TestLiveCheck`, `TestLiveSearchFilm`, `TestLiveSearchSeries`, `TestLiveDetailsFilm`, `TestLiveDetailsSeries` |
| One request, not two, for a series' IMDb id | `metadata.TestLiveDetailsSeries` — `external_ids` really does arrive appended |
| A real provider path survives the containment layer | `metadata.TestLiveProviderPathReachesTheArtworkStore` |
| A search with no matches is an empty result, not an error | `metadata.TestLiveSearchForSomethingThatDoesNotExist` |
| The suite still runs with no credential | Every live test skips without `CMS_TMDB_TOKEN`; the full sweep is unchanged |

**And the flake fix from 3i was itself flaky.**
`TestATicketHasExactlyOneSpelling` searched three padding lengths for a sealed
ticket whose length has base64 slack — but a ticket carries an expiry whose JSON
representation changes length as trailing zeros come and go, so the base length
shifted *between* the attempts and the scan could miss. It failed a later sweep
with "could not produce a ticket whose length has encoding slack": the same
intermittency it was written to remove, one level up. Fixed by holding the clock
still, which makes three consecutive lengths cover all three residues by
construction. Now: 60/60 runs pass with the fix, 30/30 fail without it.

**Still not verified**, and named: failure behaviour that needs the provider to
misbehave — a genuine 429, a 5xx, a malformed body from the real host. Those
stay on fixtures, which is the right tool for them; the alternative is waiting
for TMDB to have a bad day.

---

### Increment 3k — identification: propose, confirm, and a threshold ✅

The question 3j ended on, answered in the direction I argued for: **the machine
proposes and a person confirms**, with automatic acceptance possible but hard to
earn. See **[ADR-0019](docs/adr/0019-identification.md)**.

Radarr matches automatically and lets you correct afterwards. The reason not to
copy that: a missed identification leaves an item looking exactly as it does
today, and a wrong one **relabels your library** — somebody else's title, poster
and synopsis, looking deliberate, because it looks like the software knows.

**Automatic acceptance requires an exact title, an exact year, AND a margin over
the runner-up.** The margin is the part that matters, and it rests on a fact
rather than a worry: **two films called *Arrival* came out in 2016**, the live
provider returns both, and both are exact matches for an item parsed from
`Arrival.2016.1080p.BluRay`. A confidence *threshold* accepts one of them with
even odds, forever. Only a margin refuses.
`identify.TestLiveAmbiguityIsReal` asserts that is still true of the real
provider, so if TMDB ever deduplicates the test says so rather than quietly
becoming a tautology.

**Popularity ranks and never convinces.** It is added to a sort key inside
`Rank`, never to `Scored.Score`, which is what `Decide` reads. The bound on it
was *worked out rather than tuned*, and the answer is a limitation: the gap a
nudge would like to cross (no year: exact 0.875 vs article-only 0.7625) is
**exactly the same size** as the gap it must not (years matching: 1.0 vs
0.8875), because the year contributes equally to both. No constant can take one
without the other. The safe half is taken and the cost is written down.

**Two normalisation defects the live provider found, both invisible to
fixtures** — a fixture contains the titles its author thought of:

- **"The Arrival" scored a perfect match for "Arrival".** Stripping a leading
  article treats it as a spelling difference; it is a *word*, and there are
  distinct films that differ only by it. `StripLeadingArticle` is now separate
  from `NormaliseTitle`, and the two surfaces make opposite calls on it —
  correctly. `request` strips it ("Matrix" and "The Matrix" are the same
  request); `identify` does not.
- **`unicode.IsDigit` excludes superscripts**, so *Alien³* normalised to `alien`
  and collided with *Alien*. `IsNumber` includes it.

**One normalisation, two policies.** `release.NormaliseTitle` is now shared with
`internal/request`. If the two surfaces normalised differently, a title that
merged two requests would fail to match the library item those requests were
about, and nobody would ever work out why. The request tests passed the refactor
unchanged, which is the proof it preserved behaviour.

| Claim | How it is established |
|---|---|
| The ambiguity is real, not hypothetical | `identify.TestLiveAmbiguityIsReal` — against the live provider |
| Two equally good candidates are never guessed between | `identify.TestTwoFilmsCalledArrivalAreNeverGuessedBetween` |
| An unambiguous match IS accepted | `identify.TestAnUnambiguousExactMatchIsAccepted`, `TestLiveAnUnambiguousFilmIsAccepted` |
| A near title is never accepted | `identify.TestAcceptanceRequiresAnExactTitle` — *Alien* vs *Aliens* |
| An adjacent year is never accepted | `identify.TestAcceptanceRequiresAnExactYear` — that is how a remake becomes its original |
| No year means never automatic | `identify.TestAnItemWithNoYearIsNeverAcceptedAutomatically`, `TestLiveATitleWithNoYearIsAlwaysProposed` |
| A leading article does not make a different film identical | `identify.TestALeadingArticleDoesNotMakeADifferentFilmIdentical` |
| A superscript is part of the title | `identify.TestASuperscriptIsPartOfTheTitle` |
| Popularity orders but never decides | `identify.TestPopularityNudgesTheOrderAndNeverTheDecision` |
| The provider's original title counts equally | `identify.TestTheProvidersOriginalTitleCountsEqually` — release groups use it far more than catalogues do |
| Every verdict is explained in words | `identify.TestEveryVerdictExplainsItself` |

**Persistence landed in the same increment**, with one refusal at its centre.

**An automatic pass must be safe to re-run**, and a re-run that quietly undid
yesterday's corrections would make the feature worse than useless — the operator
would fix the same items forever and never work out why. So `decided_by` is
load-bearing: NULL means the software decided, a user id means a person did, and
the pass refuses to touch those rows. There is no override flag; `Reopen` is the
explicit act. A **rejection** is a decision too, so an operator who concluded the
provider does not have an item is not asked again every pass.

The mutation proving it is worth quoting, because the second half is the real
damage: with the refusal removed, a rejected item is re-proposed *and* the
operator's own words — "this is a family video" — are silently replaced by the
machine's generic sentence.

**And a property falls out of the exact-title rule: an automatic acceptance can
never relabel anything.** Acceptance requires the title to match exactly, so
there is nothing to rewrite — `applyAutomatic` writes ids and artwork and leaves
the title alone, with a comment rather than a silent invariant. Relabelling only
happens when a *person* confirms a candidate whose title differs, looking at
both strings. `authz.TaskIdentify`'s grant is therefore `PermBrowse` and nothing
else: withholding the permission beats intending not to use it.

**Candidates are stored rather than re-searched**, because a person must confirm
what they were *shown* — re-searching at review time risks the provider's
results moving in between, and `Confirm` refuses an id that was not offered.

| Claim | How it is established |
|---|---|
| A pass never overwrites a person | `identify.TestAnAutomaticPassNeverOverwritesAPerson` — mutation-verified |
| A rejection stands too | `identify.TestARejectionAlsoStandsAgainstTheNextPass` |
| The software may revisit its OWN decision | `identify.TestTheSoftwareMayRevisitItsOwnDecision` |
| Reopen is the only way past a person | `identify.TestReopenIsTheOnlyWayPastAPersonsDecision` |
| An automatic acceptance never rewrites a title | `identify.TestAnAutomaticAcceptanceNeverRewritesATitle` — mutation-verified |
| The pass cannot edit library items at all | `authz.TestTheIdentificationPassCannotEditLibraryItems` |
| Only an offered candidate can be confirmed | `identify.TestOnlyAnOfferedCandidateCanBeConfirmed` |
| One bad item does not stop a pass | `identify.TestOneBadItemDoesNotStopThePass` |
| A provider-level failure does | `identify.TestAProviderLevelFailureStopsThePass` |
| An artwork failure fails nothing | `identify.TestArtworkFailureNeverFailsAnIdentification` |
| A confirmation needs the permission to edit | `identify.TestConfirmingNeedsThePermissionToEditLibraryItems` |

### Increment 3l — the review screen, and four defects only a browser found ✅

The routes, the scheduled pass and the review screen. Wiring, mostly — except
that putting it in front of a real browser and the live provider found four
defects, **none of which any test at the time would have caught**, because each
lived in the gap between a component that was correct and a screen nobody had
looked at.

**A library kept whatever spelling a release group used.** The first end-to-end
run identified `the matrix.1999.720p.brrip` correctly and left the library
reading `the matrix`. Correct by the rule — an automatic acceptance never
relabels — and obviously wrong to look at. The rule was about **meaning, not
bytes**: the constraint is that a pass must not relabel an item as a *different
film*, never that a library should keep a release group's casing.

So there is a third method. `AdoptCanonicalTitle` takes the provider's spelling
and refuses anything whose normalised form differs:

    "the matrix" -> "The Matrix"     same title, better spelled    allowed
    "Arrival"    -> "The Arrival"    these normalise differently   refused

`AttachIdentity` / `AdoptCanonicalTitle` / `Relabel` — ids, spelling, meaning.
Three methods, three authorities, and the middle one is reachable by a background
pass *precisely because* it cannot reach the third.

**Nothing that read a library could see an identification.** `importer.Item`
never carried `tmdb_id` or `imdb_id`, though the columns had held them since
migration 0008. The pass wrote them; every read path was blind. No page could
tell an identified item from an unidentified one, and no page could find a
poster. Both are now on the read path, with a `poster` URL in the JSON — the URL
rather than the ingredients, so no client is tempted to assemble a *provider*
URL instead.

**The IMDb id was never fetched at all** — both write paths passed `""`. It is
the id release groups and indexers agree on, and it lives only on the details
endpoint, so it costs one extra request per identified item. Spent deliberately:
a background pass already spends one request per item, it happens once in an
item's life, and the alternative is to spend the same request later while
somebody waits. Writing it exposed that `Confirm` hard-coded `KindMovie` for
artwork, so **every series an operator confirmed would silently have got neither
artwork nor an id**.

**The review screen would have shown four broken images.** The pass caches a
poster for what it *accepts*; what it *proposes* is exactly what a person has to
look at, and those had none. On the one screen whose job is to be looked at,
every picture was a 404.

They are fetched on first view instead — nothing is spent until somebody looks.
That lets an authenticated request cause an outbound fetch, so the containment is
the point: the remote path is read from **the candidate row this software stored**
when the provider answered a search, an id with no such row is `ErrNotRecorded`
and nothing is fetched, and `internal/artwork` still refuses anything that is not
a plain filename and still writes through `os.Root`. A request can choose
*between* posters this instance already knows about; it cannot introduce one.

**What the screen shows: posters, titles, and the reason each candidate is a
candidate — not the scores.** The score orders the list; it is not an argument,
and a number next to a film invites trusting it rather than looking. The live run
makes that case better than the reasoning does: `Arrival (2016)` returns two
candidates that are textually identical — same title, same year, same
explanation, both scoring 1.00 — because there really are two 2016 films called
*Arrival*. Nothing but the poster tells them apart.

| Claim | How it is established |
|---|---|
| A spelling change cannot change what something is | `importer.TestAdoptingASpellingCannotChangeWhatSomethingIs` — mutation-verified |
| Nor sneak in a leading article | `importer.TestAdoptingASpellingRefusesALeadingArticle` — mutation-verified |
| An identified item gets the id other tools use | `identify.TestAnIdentifiedItemGetsTheIdentifierOtherToolsUse` — mutation-verified |
| A series is looked up as a series | `identify.TestConfirmingASeriesAsksTheProviderAboutASeries` — mutation-verified |
| A details failure costs nothing already earned | `identify.TestAFailedDetailsLookupDoesNotCostAnItemItsIdentification` |
| A poster is fetched only for a recorded title | `identify.TestAPosterIsOnlyFetchedForATitleThisInstanceRecorded` — mutation-verified |
| From the recorded path, not a reconstructed one | `identify.TestACandidatesPosterIsFetchedFromTheRecordedPath` |
| And only for somebody who may browse | `identify.TestFetchingAPosterNeedsThePermissionToBrowse` — mutation-verified |
| Every view is registered in all three places | `web.TestEveryNavEntryHasASectionAndEverySectionHasANavEntry`, `web.TestEveryDataViewHasALoader` |

Verified in a real browser against the live provider: four candidate posters
loaded, **all four from this instance and none from a third party**, a
confirmation renamed the library entry, and the library then read *Arrival
(2016)*, *Dune (2021)*, *The Matrix (1999)*.

**Not done.** Episode-level identification. The provider supplies season lists,
but nothing records episodes yet, so a series is identified as a whole and its
files are matched by the release-name parser alone.

---

## Phase 4 — Playback 🚧

Architecture first: **[ADR-0020](docs/adr/0020-playback.md)**.

### Increment 4a — the media parser, jailed ✅

Nothing in this project parsed media before now, and the moment it does it
becomes the largest hostile-input surface in the system: **this software
downloads files chosen by strangers and then points ffmpeg at them.** ffmpeg is
indispensable and is also millions of lines of C whose entire job is parsing
untrusted container data, with a CVE history to match. Every media server has
this exposure. 4a is about its blast radius.

**The parser runs in a namespace jail with no network.** `CLONE_NEWUSER |
CLONE_NEWNET | CLONE_NEWPID`, mapped to an unprivileged uid, empty environment,
hard timeout, capped output. `CLONE_NEWNET` is the one that matters: a new
network namespace holds nothing but a down loopback, so an exploited parser
**cannot call out, cannot reach the LAN, and cannot reach the NFS server the
media is mounted from.** Remote code execution becomes code execution in a box
with no exits.

Measured rather than asserted:

| | unsandboxed | sandboxed |
|---|---|---|
| `net.DialTimeout("tcp", "1.1.1.1:443")` | `DIAL-OK` | `network is unreachable` |
| interfaces visible | `[lo ifb0 ifb1 eth0]` | `[lo]` |
| `os.Getuid()` | 0 | 65534 |

**The parser is handed a file descriptor, never a path.** This is the decision
worth defending. Passing a path makes path safety a shared responsibility
between a Go program that is careful and a C program that is not — every
symlink, every `..`, every race between checking a path and opening it comes
back into scope. Passing a descriptor deletes the question: the child cannot
open a file it was never told the location of, so
[ADR-0015](docs/adr/0015-library-path-containment.md)'s `os.Root` containment
extends *into* ffmpeg rather than stopping at the process boundary.

It costs nothing. ffprobe reads `/dev/fd/3` identically to a path, **including
seeking for metadata stored at the end of a file** — verified against a
non-`faststart` MP4, which is the case that would have failed had it been
reading a stream. A pipe was the obvious alternative and is the wrong one: on a
40 GB remux, reading to the end to learn the duration is not a probe, it is a
copy.

**The capability check cannot start anything.** Asking "will this kernel build
the jail?" needs a child, and the obvious candidate — `/proc/self/exe` with an
argument it will reject — is a trap: it depends on this binary's flag parser
continuing to treat unknown flags as fatal. The day somebody makes parsing
lenient, a capability check would quietly start a second copy of the application
inside a network namespace. So the jail is built around a program that does not
exist, and the errno says which stage failed: `ENOENT` means the namespaces were
created and only the exec failed; `EPERM`/`EINVAL` means the clone was refused.

**Probes expire.** A file can change underneath the library — a re-download, an
operator's own re-encode — and a probe kept forever then produces a playback
failure that looks like a bug in the player. `Get` refuses a probe whose
recorded size and modification time no longer match, rather than returning it
with a flag: a caller that has to remember to check the flag will forget.
Compared to the second on both sides, because filesystems disagree about
modification-time resolution and full precision would re-probe the whole library
every time it crossed a mount that rounds.

**Streams are a table, not a JSON blob**, because of the question
[ADR-0005](docs/adr/0005-transcode-policy-skylake.md) makes an operator ask:
*what in my library will not play on this box?* That is a `WHERE codec = 'hevc'
AND bit_depth > 8`, and a JSON scan of every row in the other design.

| Claim | How it is established |
|---|---|
| An exploited parser cannot reach the network | `playback.TestTheProbeSandboxCannotDialOut` — mutation-verified |
| It cannot even see an interface | `playback.TestTheProbeSandboxHasNoNetworkInterfaces` — mutation-verified |
| It is not the application's user | `playback.TestTheSandboxDropsToAnUnprivilegedUser` — mutation-verified |
| It inherits no environment | `playback.TestAParserInheritsNoEnvironment` |
| A parser is never given a path | `playback.TestNoMediaToolIsEverGivenAPath` — structural, mutation-verified |
| Exactly one descriptor is handed over | `playback.TestOnlyOneDescriptorIsHandedToAParser` — structural |
| The jail still asks for every namespace it claims | `playback.TestTheSandboxStillAsksForEveryNamespaceItClaims` — structural |
| A child that will not finish is killed | `playback.TestASandboxedChildIsKilledWhenItOverrunsItsTimeout` |
| Output cannot exhaust the parent | `playback.TestOutputFromAChildIsCapped` — mutation-verified |
| An unlinked file is still probed through its fd | `playback.TestAProbeNeedsNothingButAnOpenFile` |
| A vaulted file composes with all of it | `playback.TestAVaultedFileCanBeProbedThroughItsDescriptor` |
| HDR comes from the transfer function | `playback.TestHDRIsDetectedFromTheTransferFunction` |
| Wide primaries alone are not HDR | `playback.TestWideColourPrimariesAloneDoNotMeanHDR` — mutation-verified |
| Bit depth comes from the pixel format | `playback.TestBitDepthComesFromThePixelFormat` — mutation-verified |
| Text and bitmap subtitles stay distinguishable | `playback.TestASubtitleTrackSaysWhetherItIsTextOrPictures` |
| Cover art is not a video track | `playback.TestEmbeddedCoverArtIsNotAVideoTrack` |
| A changed file has no valid probe | `playback.TestAProbeOfADifferentFileIsNotReturned` — mutation-verified |
| Sub-second drift does not invalidate one | `playback.TestSubSecondClockDriftDoesNotInvalidateAProbe` |
| Re-probing replaces the streams | `playback.TestReprobingReplacesTheStreamsRatherThanAddingToThem` — mutation-verified |
| The library can be asked what it cannot play | `playback.TestTheLibraryCanBeAskedWhatItCannotPlay` |

Tests run against **real media generated by ffmpeg and parsed by the real
ffprobe through the real sandbox**. Fixtures of ffprobe's JSON were considered
and rejected: that tests this package's parsing against a string its author
wrote, which is precisely the mistake ADR-0018 records from the metadata client,
where two documented field names turned out to be wrong.

**Not wired.** `internal/playback` has no caller yet — no route, no scheduled
pass. The bytes-on-the-wire half (`http.ServeContent` over a vaulted descriptor,
range requests, the player) is 4b.

**Known gap carried forward:** the container's seccomp profile is still
`unconfined`, which weakens the kernel layer for every role. It should be fixed
before the transcoder ships, because the transcoder is a larger hostile-input
surface than the prober.

### Increment 4b — direct play ✅

Something plays. A file is opened through its vault, served with range requests,
and rendered by a `<video>` element in a browser — verified, not asserted:
`readyState` 4, **1920×1080 decoded**, playback advancing, and a seek to 110 s of
a 120 s file served as three `206` responses at different offsets.

**`http.ServeContent`, never `http.ServeFile`.** ServeFile takes a path and
resolves it itself, putting path handling back where
[ADR-0015](docs/adr/0015-library-path-containment.md) spent an increment taking
it out of. ServeContent takes an `io.ReadSeeker` — which is what a vault's
`os.Root` hands back — and still implements range requests, conditional requests
and the 206s that make seeking work. The whole playback path therefore holds the
same property the parser does: **nothing turns a request into a filesystem
path.**

Mutation-tested with ServeFile as the alternative, which is how anyone would
write this: it **served the master key** from a path above the library, and a
symlink out of it too.

**The audio question is about the track the browser will choose.** Direct play
means handing the browser the whole container and letting it pick; there is no
way to say "use track 3" without remuxing, which is not direct play. So a remux
carrying TrueHD first and AC3 second is refused — otherwise the viewer gets a
picture and silence, which is this category of software's most common complaint.
Tracks *other* than the default that the browser cannot decode are a **caveat**,
not a refusal: the film plays, switching language will not, and saying so beats
letting somebody find out.

**A refusal names every missing capability at once.** A file that is HEVC *and*
10-bit *and* HDR fails three ways, and a viewer told about them one at a time —
switching browsers between each — is being walked through a maze that was fully
mapped on the first attempt.

#### The fix that was nearly wrong

The first browser run failed with `MEDIA_ERR_SRC_NOT_SUPPORTED` on an MKV the
server had just called playable. `canPlayType("video/x-matroska")` returns `""`,
which looked like the answer, and removing Matroska from the capability list was
nearly committed — a change that would have refused **most of a real library**.

Serving the actual bytes settled it instead:

| file | served as | result |
|---|---|---|
| `.mkv` of VP9 + Opus | `video/x-matroska` | **plays** |
| `.mkv` of VP9 + Opus | `video/webm` | **plays** |
| `.webm` of VP9 + Opus | `video/webm` | **plays** |
| `.mkv` of H.264 + AAC | anything | `MEDIA_ERR_SRC_NOT_SUPPORTED` |

Chromium demuxes Matroska fine. It refused **H.264** — the test browser is the
open-source Chromium build without the proprietary codecs real Chrome ships. The
container was never the problem; the "bug" was the test environment.

The rule this establishes is about `canPlayType` rather than about MKV: it is
advisory and pessimistic, so it may **widen** what is attempted and never narrow
it. The player follows that — it asks the browser only when the server has
already refused.

A second finding fell out of the same experiment: **ffprobe cannot distinguish
Matroska from WebM.** All three fixtures, a genuine `.webm` included, report
`matroska,webm`. Which is fine, because the codecs decide playback and those
ffprobe reports exactly.

#### And one the browser found in the UI

The refusal screen listed every blocker **twice**. Two things call the loader —
the Play button and the hashchange it causes — and both responses arrived after
the synchronous clear. Guarding the caller would have fixed that case and not
the real one: a viewer who presses Play on a second film before the first answer
arrives. A generation counter discards any late response for a film nobody is
looking at any more.

| Claim | How it is established |
|---|---|
| A path above the library cannot be streamed | `playback.TestAFileOutsideTheLibraryCannotBeStreamed` — mutation-verified; ServeFile served the master key |
| Nor a symlink out of it | `playback.TestASymlinkOutOfTheLibraryCannotBeStreamed` — mutation-verified |
| A range returns exactly the bytes asked for | `playback.TestARangeRequestReturnsExactlyTheBytesItAskedFor` |
| An open-ended range serves to the end | `playback.TestAnOpenEndedRangeServesToTheEndOfTheFile` |
| A range past the end is 416, not a short 206 | `playback.TestARangeBeyondTheEndIsRefused` |
| A player gets the headers it needs to seek | `playback.TestAWholeFileIsServedWithTheHeadersAPlayerNeeds` |
| Watching needs the permission to browse | `playback.TestStreamingNeedsThePermissionToBrowse` — mutation-verified |
| Content types are decided here, not by the host | `playback.TestContentTypesAreDecidedHereAndNotByTheHost` — mutation-verified |
| A hostile filename cannot inject a header | `playback.TestAHostileFilenameCannotEscapeTheDispositionHeader` |
| A refusal names every missing capability | `playback.TestARefusalNamesEveryCapabilityThatIsMissing` — mutation-verified |
| The audio question is about the default track | `playback.TestTheAudioQuestionIsAboutTheTrackTheBrowserWillChoose` — mutation-verified |
| An unplayable alternate is a caveat, not a refusal | `playback.TestAnUnplayableAlternateTrackIsACaveatAndNotARefusal` |
| An unstated bit depth means 8 | `playback.TestAnUnstatedBitDepthIsTreatedAsEight` — mutation-verified |
| Matroska stays claimed, on evidence | `playback.TestTheDefaultCapabilitySetStillClaimsMatroska` |
| The matroska/webm family is one answer | `playback.TestTheMatroskaAndWebMFamilyIsOneAnswer` |
| HDR says the server cannot fix it | `playback.TestTheHDRRefusalSaysTheServerCannotFixIt` |
| A small file gets a shorter probe leash | `playback.TestASmallFileGetsAShorterProbeLeash` |

**Not done.** Remuxing, transcoding, subtitles. (Resume landed in 4d.)

### Increment 4c — the seccomp gap, which fought back ✅

ADR-0007 and ADR-0020 both said the container's `seccomp:unconfined` should be
tightened **before the transcoder ships**. Going to do that found a direct
conflict with the jail built in 4a.

Docker's **default** profile permits `clone` only when no namespace flag is set:

    allow clone when (arg0 & 0x7E020000) == 0    [without CAP_SYS_ADMIN]
    deny  clone3 outright
    defaultAction: SCMP_ACT_ERRNO

`0x7E020000` is exactly the sum of all seven `CLONE_NEW*` flags — checked
against the real profile, not from memory. The jail asks for three of them
(`0x70000000`), so `(arg0 & mask) != 0`, no rule matches, and it falls through
to `EPERM`.

**Adopting the stock profile would therefore have silently disabled the
sandbox.** The container starts, `NewSandbox` finds the clone refused, logs its
warning, and ffmpeg parses hostile files with no network isolation at all — a
specific verified control traded for a generic one, discoverable only by reading
a log line twice.

So the repository ships `deploy/seccomp-cmediastack.json`: Docker's default plus
**one** allowance, scoped with `MASKED_EQ` to exactly
`CLONE_NEWUSER|CLONE_NEWPID|CLONE_NEWNET`. `CLONE_NEWNS`, `CLONE_NEWUTS`,
`CLONE_NEWIPC` and `CLONE_NEWCGROUP` remain denied, as do `setns`, `unshare` and
`clone3` — `clone` is the only route to a namespace, and only to those three.

**The residual is real and is written down.** Permitting unprivileged user
namespaces re-opens kernel surface with a long privilege-escalation CVE history.
The trade is that the process reaching it is the application, rather than a
compromised `ffmpeg` with an open network. Recorded in SECURITY.md rather than
glossed.

The tests evaluate the shipped JSON **the way the kernel would** — first
matching rule wins, capability sets honoured — because a profile is only as good
as the decision it produces for the exact flags `sandboxAttr()` passes.

Writing them also corrected something: the first draft asserted that `ptrace`
and `process_vm_readv` were denied. They are **allowed** by Docker's default,
which unblocked `ptrace` once the kernel hardened it. The profile was right and
the test was wrong, which is the correct way round to find out.

| Claim | How it is established |
|---|---|
| The profile permits the jail | `playback.TestTheShippedSeccompProfilePermitsTheMediaSandbox` — mutation-verified: the stock profile gives `clone(0x70000000) -> SCMP_ACT_ERRNO` |
| The allowance is narrower than "every namespace" | `playback.TestTheSeccompAllowanceIsNarrowerThanEveryNamespace` — mutation-verified |
| It is a tightening, not `unconfined` renamed | `playback.TestTheShippedSeccompProfileStillDeniesByDefault` |
| The profile explains itself in the file | `playback.TestTheSeccompAllowanceSaysWhyItIsThere` |
| Compose actually loads it | `playback.TestComposeLoadsTheShippedSeccompProfile` — mutation-verified |

**And one flake, fixed properly rather than re-run.** The full `-race` sweep
failed `download.TestAClosedGateStopsPeerConnections` — which reported its own
control case had not completed, refusing to pass vacuously. It passes every time
in isolation (the control transfer takes **200 ms**), and was being pushed past
its 20-second cap only by the race detector plus two dozen packages competing
for four cores. That is the worst way for a test to be wrong: green locally,
red in CI, for reasons unrelated to the code.

The cap is now 90 seconds — a *failure* timeout, not a wait, since the loop
returns the instant the transfer completes — and it logs how long the control
took, so a future flake is diagnosable rather than mysterious. The 4-second leak
window was deliberately left alone: it watches for **any** bytes rather than for
completion, and a loopback connection is milliseconds even on a loaded machine.

**Correction to 4b's closing note.** That summary said remux was the obvious next
step because "most of a library is MKV and browsers cannot play it". The browser
experiment showed Chromium **does** play MKV, so the premise was wrong. The real
case for remux is narrower: Firefox and Safari do not demux Matroska, and the
genuinely valuable variant is **copy the video, transcode only the audio** for
files whose default track is TrueHD or DTS — cheap on an i5-6500T, and it fixes
the "picture but silence" class rather than the container class.

### Increment 4d — resume ✅

A media server that starts every session at zero is a toy: a film watched over
three evenings is three first acts. This is small and it is the difference
between the player being usable and being a demonstration.

**Per person, not per file** — and the primary key says so rather than an
application rule saying so. Two people watching the same film are in different
places, and a position stored against the file alone would have each dragging
the other back.

**"Finished" needs two halves, and each fixes what the other gets wrong.** A
percentage alone is wrong for long films — 5% of three hours is nine minutes, so
stopping with a reel to go would mark it watched. A fixed cap alone is wrong for
short ones — three minutes left in a 22-minute episode is the whole last act.
Taking the smaller of `5%` and `3 minutes` gives roughly where the credits start
at every length: ~66 s for a sitcom, ~2¼ minutes for a drama, 3 minutes for an
epic. Both halves are mutation-verified: removing either one breaks the test.

**A position is disbelieved when the file changed.** The duration is stored
alongside it, so a re-encode that trims a few frames is still the same film and
an extended cut twenty minutes longer is not — 01:12:30 into it is a different
scene. Without that, a resume would silently drop somebody into the wrong place
with no way to tell that had happened.

**`fetch(..., {keepalive: true})`, not `navigator.sendBeacon`.** The write that
decides whether resume works at all is the one as the tab closes, and sendBeacon
— the usual answer — cannot set headers, so it cannot carry the CSRF token this
application requires. The write would have been refused, silently, at exactly
the moment it mattered. `pagehide` and `visibilitychange` are the triggers,
because those are what fire on mobile Safari and on a backgrounded tab being
discarded.

**Resume is applied on `loadedmetadata`, not immediately.** Setting
`currentTime` before the element knows the duration is silently ignored — the
shape of bug that works locally and fails on a slow connection.

**History is purged on a schedule.** Playback history says what somebody watched
and when, which [THREAT-MODEL.md](docs/THREAT-MODEL.md) lists as an asset in its
own right. `config.media.playback_history_retention` already defaulted to 180
days and nothing honoured it; `playback.retention` does now, under a system
principal granted **browse only** — a retention task holding
`PermDeleteMediaFiles` would be one typo away from being a library-deletion task.

Two things caught me while building it, both by tests:

- **`ResumeAt` discarded every position for an unprobed file.** The "past the
  end" check compared against a duration of zero, throwing away the one case
  `describesDuration` deliberately treats leniently two lines earlier.
- **`authz.TestEachBackgroundTaskHoldsExactlyItsOwnGrant` refused to pass** when
  a fifth system task appeared without a line covering it — exactly what an
  exhaustive structural test is for.

| Claim | How it is established |
|---|---|
| Finished is right at every length | `playback.TestFinishedMeansNearEnoughTheEndAtEveryLength` — mutation-verified against BOTH halves of the rule |
| A finished film starts again | `playback.TestAFinishedFilmStartsFromTheBeginning` — mutation-verified |
| Barely started is not a resume | `playback.TestBarelyStartedIsNotAResume` |
| A different cut is not trusted | `playback.TestAPositionInADifferentCutIsNotTrusted` — mutation-verified |
| An unknown duration does not cost a place | `playback.TestAnUnknownDurationDoesNotDiscardAPosition` — found a real bug |
| Two people keep separate places | `playback.TestTwoPeopleKeepSeparatePlaces` — mutation-verified |
| The user comes from the context, never an argument | `playback.TestAPositionIsRecordedForTheCallerAndNobodyElse` |
| A position past the end is clamped | `playback.TestAPositionPastTheEndIsClampedToIt` |
| History can be forgotten on request | `playback.TestAPositionCanBeForgotten` |
| And is purged on the retention window | `playback.TestOldPositionsArePurgedAndRecentOnesAreNot` |
| A zero window means unset, not "delete all" | `playback.TestPurgingWithNoWindowIsRefused` |
| Deleting a file forgets everybody's place | `playback.TestDeletingAFileForgetsEverybodysPlaceInIt` |
| The purge task holds browse and nothing else | `authz.TestEachBackgroundTaskHoldsExactlyItsOwnGrant` |

Verified in a browser across a **full page reload**: watched to 76.4 s, three
position writes accepted, reopened at exactly 76.4 s with *"Resuming from
1:16"*; then finished and reopened at 0.0 with *"You finished this before, so it
starts from the beginning."*

### Increment 4e — converting what cannot play ✅

The commonest unplayable file is not one a browser cannot decode. It is one
whose **video is fine and whose soundtrack is not** — an H.264 film in a
Matroska container with TrueHD or DTS as the default track, which is most of
what a remux tracker distributes. Every browser plays the picture and nothing
else, and "picture but silence" is this category of software's most-reported
complaint.

Fixing it is a **stream copy of the video** and a re-encode of one audio track.
Measured end to end through the running binary: **120 seconds of video converted
in 2.28 s**, about 52× realtime, because the expensive part is copied rather
than encoded. That is a different activity from the transcoding
[ADR-0005](docs/adr/0005-transcode-policy-skylake.md) is cautious about, and it
is why this is worth doing on an i5-6500T when that is not.

**Proven by measuring audio, not "did it load".** An earlier probe in this
increment reported both the original and the converted file as "loaded", which
proved only that the *video* decoded. `webkitAudioDecodedByteCount` is the thing
that distinguishes playing from playing with sound:

| stream | video decoded | **audio decoded** |
|---|---|---|
| the file as it is (AC3) | 502,692 bytes | **0** |
| converted | 683,193 bytes | **106,160** |

**The output codec is chosen by the browser from a server-side allowlist.** Not
a constant, because this genuinely differs between builds of the *same* browser:
a Chromium compiled without the proprietary codecs has Opus and no AAC at all,
which is what this project's own browser tests run against. Sending everybody
AAC would work everywhere it ships and produce silence everywhere it does not —
the exact failure the conversion exists to fix. The player asked for
`?audio=opus` on its own.

**A conversion that would not help is refused with the reason.** If the *video*
is the problem — HEVC, 10-bit, HDR — a remux moves the same pictures into a
different box. `PlanRemux` says so, the UI shows no button, and the endpoint
answers 409 rather than producing a stream that fails in a new way.

**Bounded, because the hardware has no headroom.** `config.media.max_concurrent_transcodes`
existed and nothing honoured it; it does now. An unset limit becomes a small
number rather than "as many as arrive".

**Not seekable, said out loud.** The output is a pipe being produced as the
viewer watches, so there is no file to serve a byte range from. The response
says `Accept-Ranges: none` and the player says so in words, rather than offering
a scrubber that does nothing.

#### The structural test that was inspecting nothing

`TestNoMediaToolIsEverGivenAPath` matched only `.Run(`, and the remuxer reaches
ffmpeg through `.Pipe(`. Extending it to both looked like the fix. It was not:
the regex captured the text *inside the call*, and the remuxer builds
`args := []string{...}` above and passes `args...` on one line — so the test saw
an empty argument list and passed.

A hardcoded library path in those arguments went **undetected**, verified by
putting one there. The check read as a guarantee and inspected nothing, which is
the exact failure the structural tests exist to prevent, occurring inside one.

It now scans the whole file of anything that starts a media tool, with a short
allowlist of the three paths that legitimately appear and a reason beside each.
Mutation-verified in both shapes: a path literal in the args, and a
`filepath.Join` anywhere in the file.

| Claim | How it is established |
|---|---|
| A conversion fixes undecodable audio | `playback.TestARemuxFixesUndecodableAudio` |
| A container-only problem copies the audio | `playback.TestAContainerOnlyProblemCopiesTheAudio` — mutation-verified |
| It refuses what it cannot fix, with a reason | `playback.TestARemuxRefusesWhatItCannotFix` — mutation-verified |
| VP8 is never muxed into an MP4 | `playback.TestVP8IsNotMuxedIntoAnMP4` — mutation-verified |
| Only an allowlisted codec reaches ffmpeg | `playback.TestOnlyAnAllowlistedAudioTargetIsAccepted` — mutation-verified |
| And the check is repeated where the argument is built | `playback.TestStreamingRefusesACodecItWasNotAskedToProduce` |
| Conversions are bounded | `playback.TestConversionsAreBounded` — mutation-verified |
| An unset limit is small, not unlimited | `playback.TestAnUnsetLimitIsSmallRatherThanUnlimited` |
| Slots are counted correctly under concurrency | `playback.TestSlotsAreCountedCorrectlyUnderConcurrency` |
| The video is copied and the audio rebuilt | `playback.TestAConversionCopiesTheVideoAndRebuildsTheAudio` — real ffmpeg, real jail |
| No media tool is ever given a path | `playback.TestNoMediaToolIsEverGivenAPath` — rewritten, mutation-verified |

**Not done.** Seeking within a converted stream — it needs ffmpeg restarted at
an offset, and the player then has to carry the offset in its own arithmetic.

### Increment 4f — subtitles a library already has ✅

Tracks inside the container and sidecar files beside the video, converted to
WebVTT on the way out because a `<track>` element accepts nothing else.
**Fetching subtitles from a provider — the larger half of replacing Bazarr — is
not built** and is recorded in
[DROPPED-FEATURES.md](docs/DROPPED-FEATURES.md).

**The content type is the security property.** A subtitle is text a stranger
wrote, displayed in the operator's own browser, same-origin, with their session
cookie. What makes markup inside it inert is that `text/vtt` sends it to the
browser's **WebVTT cue parser** rather than its HTML parser — a closed tag set,
no attributes that run anything, no script. ffmpeg also strips unknown tags,
which is useful and is not relied on: it is a property of a converter, not a
boundary.

Verified rather than asserted. A subtitle carrying
`<script>window.__PWNED=…;document.title='PWNED'</script>` and an
`<img src=x onerror=…>` was served to a real Chromium and turned on:

| what was checked | result |
|---|---|
| `cue.getCueAsHTML()` node tree | a single `#text` node — no `SCRIPT`, no `IMG` |
| `window.__PWNED` / `__PWNED2` | `null` / `null` |
| `document.title` | unchanged |
| every response's content type | `text/vtt; charset=utf-8`, `nosniff` |

**Bitmap tracks are listed and refused, not hidden.** PGS, VobSub, DVB and XSUB
are pictures of text; showing them means drawing them onto the video, which
[ADR-0005](docs/adr/0005-transcode-policy-skylake.md) says this server does not
do. They come back `usable: false` with a sentence saying why, and the UI shows
"1 of 2" rather than a count that does not match the menu. Tested against a
**real PGS stream**, synthesised segment by segment for the fixture, because
ffmpeg cannot encode text subtitles to bitmap and a synthetic probe would only
have tested the code against itself.

#### Two bugs the live run found

**A subtitle can convert to timings with no words in them.** A malformed ASS
file — `Format:` listing fewer fields than its `Dialogue:` lines use — converts
*without an error*: ffmpeg carries the timings across and emits every cue empty.
The response was valid WebVTT several hundred bytes long, so the length check
saw nothing wrong, and a viewer turning the track on got an invisible one and no
explanation. `toWebVTT` now refuses it, the route answers **422**, and the
player names the track that failed.

**And then that message did not appear.** The conversion path passed `null`
where the direct-play path passed a failure handler, so a broken subtitle on a
converted stream said nothing at all. A failure handler that some callers pass
and others do not is not a failure handler; there is one now and both use it.

#### The structural rule grew a second half

`TestNoMediaToolIsEverGivenAPath` **failed on `subtitles.go`, correctly**: that
file reads a directory and matches names against the video's stem, and it also
built ffmpeg's argument vector. Those two things in one file are one variable
name apart from a path reaching a parser. The conversion moved to `vtt.go`,
which is handed an already-open descriptor and an integer and has no filename to
give away. Carving an exception would have put the allowlist in the one file
where path handling and ffmpeg actually meet.

That test still only catches path-*shaped* literals, which is what made
[the 4e failure](#the-structural-test-that-was-inspecting-nothing) possible.
`TestTheOnlyInputAMediaToolGetsIsTheDescriptor` now checks the place a path
would have to arrive at: whatever follows ffmpeg's `-i` must be the literal
`/dev/fd/3`. Confirmed by planting a bare variable there — **the older test
passed and the new one failed.**

**Both containment layers were confirmed by removing one.** With id resolution
deleted, `Arrival (2016)/secrets.srt` — a real library file that was never
offered — was served, while `../outside.srt` and `/etc/passwd` were still
refused by the vault's `os.Root`. They stop different escapes.

| Claim | How it is established |
|---|---|
| Markup in a subtitle is inert in a real browser | live Chromium: `getCueAsHTML()` is one `#text` node, neither payload ran |
| It is served as subtitles and not as markup | `playback.TestAHostileSubtitleIsServedAsSubtitlesAndNotAsMarkup` — mutation-verified |
| A bitmap track is listed and refused with a reason | `playback.TestABitmapTrackIsListedAndRefusedWithAReason` — mutation-verified; live against a real PGS stream |
| An id cannot name a file of its own | `playback.TestASubtitleIdCannotNameAFileOfItsOwn` — mutation-verified |
| Sidecars are found for the right film only | `playback.TestSidecarsAreFoundForTheRightFilm` — mutation-verified |
| An embedded track is taken by its real stream index | `playback.TestAnEmbeddedTrackIsTakenByItsRealStreamIndex` — mutation-verified, real ffmpeg |
| A conversion with no cue text is refused | `playback.TestASubtitleThatConvertsToTimingsWithNoTextIsRefused` — mutation-verified |
| …and a well-formed ASS file still converts | `playback.TestAWellFormedASSFileIsConverted` |
| What counts as a cue with text in it | `playback.TestWhatCountsAsACueWithTextInIt` — mutation-verified |
| Listing and fetching both need the permission | `playback.TestSubtitlesNeedThePermissionToBrowse` |
| The only input a media tool gets is the descriptor | `playback.TestTheOnlyInputAMediaToolGetsIsTheDescriptor` — mutation-verified |

**Also verified live:** cues parse and display over a direct-played file (three
cues, one active at t=2s); tracks do **not** carry over when switching films;
and subtitles survive a conversion — which matters more there than on direct
play, because a remuxed stream carries video and one audio track and no
subtitles at all.

**Not done.** ASS styling is flattened — positioning, fonts, colours and karaoke
timing all become plain cues, because WebVTT has no equivalent. Whether a track
produces usable cues is only known after converting it, so a malformed file
appears in the menu and then reports a failure rather than being filtered out
beforehand.

### Increment 4g — migrating a Radarr library ✅

Not a second import pipeline. The obvious reading of "migration importer" is
something that reads Radarr's database and creates library rows from it; that
reading is wrong, and [ADR-0021](docs/adr/0021-migrating-from-radarr.md) is
mostly about why.

**The files are already on disk and `Scan` already finds them.** What Scan
cannot produce is an identity: working out that a folder called `Arrival (2016)`
is TMDB 329865 costs a provider lookup per film and a person's decision on every
ambiguous one. At ~900 films that is the entire cost of adoption — and Radarr
already made every one of those decisions. So the migration is an **identity
source**, and its whole write surface is `tmdb_id` and `imdb_id`.

```
1. add the existing movie folder as a root folder   (built in Phase 3)
2. Scan walks it and creates library items          (built in Phase 3)
3. Migrate attaches the ids Radarr already holds    (this increment)
```

#### The finding that this turns on

Radarr migration 207 (v4.0, 2022) moved `TmdbId`, `ImdbId`, `Title` and `Year`
**out of `Movies`** into a new `MovieMetadata` table and deleted the originals.
An importer written from memory reads `Movies.TmdbId` and fails on every modern
install.

This was not reasoned about. Radarr's repository was cloned and the final column
set reconstructed from its 140 migration files mechanically — and **the first
reconstruction was wrong in exactly that way**, because the chained
`Delete.Column("a").Column("b").FromTable("Movies")` form spans lines and the
pattern used did not match it. The error surfaced when the result was checked
against Radarr's own `Movie` model class, where `Title`, `TmdbId`, `ImdbId` and
`Year` are visibly read-through properties over `MovieMetadata` rather than
fields. Two independent derivations from the same source, disagreeing.

| | `Movies` at head | `MovieMetadata` |
|---|---|---|
| reconstruction v1 | Title, TmdbId, ImdbId, Year, Path, … | the same fields again |
| reconstruction v2 (correct) | Path, Monitored, MovieFileId, **MovieMetadataId**, … | TmdbId, ImdbId, Title, Year, … |

The fixtures are built to the corrected schema, and `Movies` in them has no
`TmdbId` column **at all** — so the wrong query fails in the test exactly as it
would against the operator's file. Mutation-verified by writing the wrong query.

#### Matching

**By folder NAME, never by path.** Radarr stores an absolute path on the machine
it ran on; `media_item.folder` is deliberately a folder name and not a path
(`0008_media.sql`), so the last segment is the one portable part. That means no
prefix mapping for an operator to get wrong, and **no absolute path from a
foreign machine ever enters the system**. Verified live with a Windows Radarr
(`D:\Media\Movies\Arrival (2016)`) against a Linux library.

**Nothing from the source database is ever opened, joined to a path, or passed
to a filesystem call.** The only things taken from it are a string compared
against a column, two integers and a title.

#### What it refuses

| Situation | Answer |
|---|---|
| A pre-v4 database | 422, naming the version found and saying to upgrade Radarr first — which migrates its own data far better than this could |
| A file that is not Radarr | 422 |
| A source named as a path (`../cms.db`, `/etc/passwd`) | 404; a source is a filename, resolved inside the migration directory through `os.Root` |
| An item that already carries a **different** id | skipped and reported, because a stored id is a decision somebody made. Overwriting is a separate opt-in |
| A folder name held twice | reported ambiguous and skipped rather than guessed between |
| An empty library | 409 saying to add a root folder and scan first |

**Titles are not adopted by default.** `AttachIdentity` takes ids and no title
([ADR-0019](docs/adr/0019-identification.md)), so a plain migration cannot
relabel anything even if the source is wrong. Adopting Radarr's labels is an
opt-in flag behind `media.edit`, refused **before** any write rather than
halfway through.

**No HTTP upload.** Accepting an arbitrary file from a request and handing it to
a database engine is a new hostile-input surface to save an operator one `cp`.

| Claim | How it is established |
|---|---|
| Ids come from MovieMetadata, not Movies | `migrate.TestIdentitiesComeFromMovieMetadataAndNotFromMovies` — mutation-verified with the wrong query |
| A Windows path matches a Linux library | `migrate.TestAPathFromAnyPlatformYieldsItsLastSegment`, `migrate.TestAWindowsRadarrMatchesALinuxLibrary` — mutation-verified against `filepath.Base` |
| A stored identity is not overwritten | `migrate.TestAnIdentityAlreadyRecordedIsNotOverwritten` — mutation-verified |
| …and overwriting is explicit | `migrate.TestOverwritingAStoredIdentityIsExplicit` |
| An ambiguous folder is skipped, not guessed | `migrate.TestADuplicateFolderNameIsAmbiguousAndSkipped` — mutation-verified |
| Titles are not adopted by default | `migrate.TestTitlesAreNotAdoptedByDefault` |
| …and adopting them needs `media.edit`, checked first | `migrate.TestAdoptingTitlesNeedsThePermissionToEdit` — mutation-verified |
| A skipped conflict is not relabelled either | `migrate.TestAConflictIsNotRelabelledWhileAdoptingTitles` — mutation-verified |
| A source cannot name a file outside the directory | `migrate.TestASourceCannotNameAFileOutsideTheDirectory` — including a planted symlink |
| An unauthorized caller never reaches the file | `migrate.TestAnUnauthorizedCallerNeverReachesTheFile` — mutation-verified; the refusal is byte-identical for a real database, a junk file and an absent one |
| A pre-v4 database is refused with instructions | `migrate.TestAPreV4DatabaseIsRefusedWithInstructions` |
| A nil runner says "not configured", not "not found" | `migrate.TestANilRunnerSaysItIsNotConfiguredRatherThanPanicking` |
| Running it twice changes nothing | `migrate.TestRunningItTwiceIsIdempotent` |

**Verified live** through the binary in real Chromium: dry run, the three
worklists, apply, the library actually carrying the ids afterwards, idempotence
on a second apply, and the four refusals.

**Not verified: a real Radarr database.** Everything above is derived from
Radarr's source and tested against fixtures built to it. The one thing that
would turn that into "verified against a real library" is a copy of an actual
`radarr.db` — which is a five-minute run once one exists.

#### Also in this increment: PROGRESS.md was lying

This file calls itself "the authoritative statement of what exists" and its
header had drifted three phases: 16 packages when there were 27, 346 tests when
there were 778, and "42 of 81 routes still return 501" when 23 of 110 did. Every
one of those is derivable, so none of them needed a person to maintain it — it
needed a test. `Route.Stub` is now recorded by `register()` from the handler it
was actually given, the generated API surface prints the count, and
`docs.TestTheProgressHeadlineNumbersAreTrue` fails the build when the header
disagrees with the tree. **It caught this increment's own header going stale
twice while the increment was being written.**

### Increment 4h — the container had no ffmpeg in it ✅

Not a feature. Before building anything else I checked whether the thing
actually deploys, because §2 makes hard claims about the container and none of
them had ever been run.

**The shipped image could not play anything.** The runtime base is
`distroless/static` — no shell, no package manager, no ffmpeg — and
`FFprobePath` is the string `"ffprobe"`, resolved from `PATH`. So the image
built, started, browsed a library, and answered **every** playback request with
a bare `500` and nothing in the log. Probing, direct play, remuxing and
subtitles were all non-functional in the only supported deployment, for the
whole of Phase 4.

Every test in `internal/playback` passed throughout, because they run on a host
where ffmpeg happens to be installed. The code was right and the deployment was
not, and `go test` cannot see the difference.

**Three defects, not one:**

| | Fix |
|---|---|
| The tools were absent | Static `ffmpeg`/`ffprobe` copied in from a digest-pinned stage. Not `apt-get install`, which would put a package manager and a shell back into an image that exists to have neither |
| The boot log said *"media parser sandbox is available"* and nothing else about media — true, and it reads as "media parsing works" | Startup checks for both tools and logs an `ERROR` naming what is missing, **before** the line about the jail |
| The failure was an internal error with no log line | `ErrToolMissing`, and the routes answer `503` naming the missing program |

A fourth, found while fixing it: the Dockerfile's header claimed **"pinned base
images by digest so a rebuild is reproducible"** while all three bases were
floating tags. A comment is not a build step. All three are pinned now, and
`playback.TestEveryBaseImageIsPinnedByDigest` fails the build if one is not.

#### What the container verification actually established

Against the built image, running:

| Claim (§2) | How it was checked | Result |
|---|---|---|
| Nothing runs as root, ever, including in the container | `/proc/<pid>/status` on the host | **uid 65532** in all four of real, effective, saved, filesystem |
| Nothing to pivot to inside the container | listed the image filesystem | no shell, no package manager |
| The media parser runs in a jail | boot log, under Docker's **default** seccomp | **NOT available** — the kernel refused the user namespace |
| …and `deploy/seccomp-cmediastack.json` is what fixes that | the same, with the project's profile + `no-new-privileges` + `--cap-drop ALL` | **available**, and a real probe returned `"sandboxed": true` |
| ffmpeg works inside the jail inside the container | fetched a subtitle through the API | WebVTT, correct cues |

The seccomp profile from increment 4c had only ever been
[decoded arithmetically](#increment-4e--converting-what-cannot-play-). Both
halves of its claim now hold against a running container: without it the jail is
impossible, with it the jail is built.

| Claim | How it is established |
|---|---|
| The runtime image provides the media tools | `playback.TestTheContainerImageProvidesTheMediaTools` — mutation-verified against both the old Dockerfile and an `apt-get` version |
| Every base image is pinned by digest | `playback.TestEveryBaseImageIsPinnedByDigest` — mutation-verified |
| A missing tool is named, not swallowed | `playback.TestAMissingMediaToolIsNamedRatherThanBecomingAnInternalError` — mutation-verified in both shapes |
| …and a host that has them reports none missing | `playback.TestAHostWithTheToolsReportsNoneMissing` |

**Cost of the fix:** the image goes from ~30 MB to 419 MB, which is what
shipping a media server costs and is not negotiable away.

**Residual, stated:** the ffmpeg binaries are third-party artefacts
(`mwader/static-ffmpeg`, pinned by digest) in the trust path of the component
ADR-0020's jail exists to contain. They are mode `0555`, so the process cannot
rewrite the parser it executes. Building ffmpeg from source was rejected as
disproportionate.

### Increment 4i — running the compose file as written ✅

4h fixed the image. This ran the **deployment**: `docker compose up` with the
file as shipped, a fresh database, and a first run through the wizard. Three
more defects, all of the same kind — a claim that had never been executed.

#### 1. The library was mounted read-only, and that is not a degraded mode

`docker-compose.yml` mounted the library `:ro`, with a comment reading *"Read-
only until Phase 3 needs to write imports; when that changes, narrow it"*.
Phase 3 landed. Nobody changed it. **The comment predicted the bug and
prevented nothing.**

The failure is not "imports do not work". A root folder cannot be **added**:
`RootStore.Create` validates one by writing a probe file, so the deployment
stopped at the first thing an operator does —

```
library: the root folder cannot be used: "/media/movies" is not writable:
  create ".cmediastack-write-test": openat ...: read-only file system
```

— leaving an instance that looks configured and can hold no library at all. The
library is not an archive this application only reads: it imports into it,
renames on import, and moves a deletion into a trash directory **inside the
root** rather than unlinking it (ADR-0016), which is the only reason a delete is
reversible.

#### 2. The ownership note said "readable"

With `:rw` the next run said `permission denied`: the host directory is
root-owned and the container is 65532. The compose file's identity comment said
the mount *"must be readable by this UID"*. It must be writable. It now says so,
with the `chown` line, and so does the README.

#### 3. The container healthcheck could not detect an unhealthy container

It ran `--check`, which validates **configuration**. A process whose listener
has died, whose database has gone away, or which is wedged entirely still has
valid configuration — so the container reports healthy forever and
`restart: unless-stopped` never fires. It also named `/config/config.yaml`, a
file the compose deployment never creates, and `--check` on a missing path
printed *"configuration is valid"* and exited 0: a green light about nothing.

Replaced with `--healthcheck`, which GETs `/readyz` on the management listener
— and `/readyz` pings the database, so a pass means the process is alive **and**
its storage is reachable. Distroless has no shell and no curl, so the binary
probes itself.

Demonstrated by SIGSTOPping the container's process — alive, serving nothing:

| probe | result |
|---|---|
| `--check` (what shipped) | `exit 0: configuration is valid` |
| `--healthcheck` (now) | `exit 1: healthcheck: http://127.0.0.1:9090/readyz: ... timeout` |

`--check` also now says which file it validated, or that there was none.

#### What the deployment does now, end to end

Fresh `docker compose up`, empty database, nothing pre-seeded:

| Step | Result |
|---|---|
| 12 migrations on a **read-only rootfs** with `/config` rw | applied; SQLite WAL fine |
| First-run wizard → admin → authenticator enrollment | 201 / 200 / 200 |
| Add root folder, scan `/media/movies` | 3 items from 10 files, 3 ms |
| Probe a file (no cache) | `"sandboxed": true` |
| Range request on the stream | `206`, 1024 bytes |
| Extract a subtitle (ffmpeg, in the jail, in the container) | WebVTT, correct cues |
| Delete an item | moved to `.cmediastack-trash/` inside the root — the write `:ro` blocked |
| Healthcheck | `healthy`, and fails when the process is stopped |

| Claim | How it is established |
|---|---|
| The deployment mounts the library writable | `library.TestTheDeploymentMountsTheLibraryWritable` — mutation-verified, including the case where the mount is renamed and the test would otherwise check nothing |
| The healthcheck probes the instance | `library.TestTheContainerHealthcheckProbesTheInstance` — mutation-verified |

**The pattern across 4h and 4i:** every one of these was a claim written in a
comment and never executed. The image "had" ffmpeg, the mount "was" sufficient,
the healthcheck "checked" health. Six defects in two increments, none of which
any unit test could have found, all of which one `docker compose up` did.

### Increment 4j — running the egress jail ✅

ADR-0001's claim is the one where being wrong has consequences outside the
application: the downloader cannot leak because it shares a VPN container's
network namespace and has no capability to change the rules. The compose entries
for it were commented out and had never been run.

Built a stand-in for gluetun — same namespace topology, same kill-switch shape,
no VPN credential needed, because what is under test is the containment and not
WireGuard's cryptography. Then ran the real binary in it.

#### What held

| Claim | Result |
|---|---|
| Refuses to start outside the tunnel | `refusing to start the download engine`, **exit 1**, naming both interfaces |
| Starts inside it | `egress verified \| interface=wg0 \| source_ip=10.2.0.2` |
| A compromised downloader cannot flush the firewall | `iptables -F` → **exit 4**, "Permission denied" |
| …cannot route around it | `ip route add default ... dev eth0` → **exit 2**, "Operation not permitted" |
| …cannot delete the tunnel | `ip link del wg0` → **exit 2**, "Operation not permitted" |
| Nothing falls back to direct when the tunnel dies | route removed and wg0 down: DNS could not resolve, the gateway could not be reached |

The containment is real. Every break-out attempt was refused, and with the
tunnel dead nothing leaked — it failed closed.

#### Three defects found in the process

**1. A downloader that had just verified its tunnel was told nothing was
tunnelled.** `ConfigFor` was given only `anonymity_enabled` — application-level
proxying — and treated its absence as "no tunnel at all", so the recommended
ADR-0001 deployment logged `egress verified | interface=wg0` and
`nothing is tunnelled` on consecutive lines. That is the inverse of the failure
that function exists to prevent, and worse in one way: **an alarm that fires
when everything is correct is one an operator learns to ignore.** It now takes
the namespace as a separate fact, and the "nothing is tunnelled" warning fires
only when it is true.

**2. The tunnel was verified once, at startup, and never again.** If the VPN
container restarts, the namespace this process joined is destroyed and the
process stays in the old one. Verified: after restarting the tunnel container
the downloader was still `Up`, with **no default route, no eth0, and a dead
wg0** — alive, inert, silent, and never restarted, because `restart:
unless-stopped` does not fire on a process that does not exit.

It now re-verifies on `probe_interval` and **exits** when it cannot. In a
container that is not giving up; it is handing the problem to the thing that can
fix it. Demonstrated end to end: tunnel dies → process exits → restart policy
loops it refusing → tunnel returns → **starts clean, no human involved**.

**3. A private `probe_target` crash-loops forever.** The guarded dialer refuses
private, loopback and link-local destinations — the SSRF rule applies to the
probe like everything else — so `192.168.1.1:443`, an entirely natural choice,
can never succeed. With defect 2 fixed that turns into an endless restart loop
reporting a tunnel failure that is really one line of configuration. The lint
now refuses a literal private address at boot and says which.

#### A limitation, stated rather than papered over

**Routing verification cannot see a tunnel that is up and carrying nothing.**
The orphaned namespace is exactly that case: it still had `default dev wg0` with
wg0 UP, so the route check passed while nothing could leave. `probe_target`
exists for this and is the only thing that closes it; it is empty by default
because dialing a third party every minute is a privacy cost this project does
not impose on an operator who did not ask for it. Both halves are now in the
config file.

#### Two corrections I had to make to my own work

I twice reported a result the evidence did not support, and both are worth
recording because the pattern is the same: measuring the wrong thing and
believing it.

- I demonstrated `probe_target` catching the orphaned namespace — on a **private**
  target, which the dialer was refusing in *both* states. The catch was
  coincidental. Chasing it is what turned up defect 3.
- I attributed a flaky QR test to uneven module scaling, then measured it: 400
  enrollments at four different render sizes including exact integer scaling,
  and with `TRY_HARDER` — **0.5% either way**. The encoder was never the
  problem; gozxing fails on ~1 valid QR in 200. Changing the product would have
  fixed nothing. The test now retries with fresh enrollments (a false failure at
  ~1 in 8,000,000) and still fails when the QR encodes the wrong thing,
  mutation-verified.

That flake was a 0.5% spurious red on a suite this log keeps citing as a gate.

| Claim | How it is established |
|---|---|
| The downloader stops when the tunnel stops verifying | `main.TestTheDownloaderStopsWhenTheTunnelStopsVerifying` — mutation-verified |
| A healthy tunnel runs until shutdown | `main.TestAHealthyTunnelRunsUntilShutdown` — mutation-verified |
| A shutdown is not reported as a tunnel failure | `main.TestAProbeCancelledByShutdownIsNotAFailure` |
| An operator who opted out is not probed | `main.TestWithTheGuardDisabledNothingIsProbed` — mutation-verified |
| An unset interval is a floor, not a busy loop | `main.TestAnUnsetProbeIntervalDoesNotMeanNever` — mutation-verified |
| A namespaced downloader is not told nothing is tunnelled | `download.TestANamespacedDownloaderIsNotToldNothingIsTunnelled` — mutation-verified |
| …and the warning still fires when it is true | `download.TestWithNoEnforcementEverythingIsOnAndSaidPlainly` — mutation-verified |
| A proxy profile still forces UDP off | `download.TestAProxyProfileStillForcesUDPOffEvenWhenJailed` |
| A private probe target is refused at boot | `config.TestAPrivateProbeTargetIsRefusedAtBoot` — mutation-verified |
| …and a public one is not | `config.TestAPublicOrEmptyProbeTargetIsAccepted` — mutation-verified |
| The lint and the dialer agree about what is private | `config.TestTheLintAgreesWithTheDialerAboutPrivateAddresses` — mutation-verified; it caught a hand-written copy of the rule that had already drifted (missing CGNAT and the TEST-NET ranges), which is why the copy is gone and the lint calls `egress.IsRestricted` |

**Still not built:** the split deployment has no IPC seam, so `--role downloader`
runs an engine the app role cannot reach. The code says so at startup and the
compose comments now say so too; it is useful today for verifying the jail and
for running the engine where the whole app is inside the namespace.

### Increment 4k — episodes: what a series is missing ✅

Architecture first: **[ADR-0022](docs/adr/0022-episode-tracking.md)**.

Until now a series was one row and a pile of files. It knew what it held and had
no idea what it was missing, because nothing had ever told it which episodes
exist — and "what am I missing" is most of the reason Sonarr is run at all.
`0008_media.sql` set the condition for building this in Phase 1: *inventing
episode rows from the files an instance holds produces a season that is always
100% complete, which is worse than having no answer.* The provider arrived in
3i; this is the increment that condition was waiting for.

```
GET  /api/v1/media/{id}/children                  → seasons, episodes, have/known/count   (was 501)
GET  /api/v1/wanted                               → monitored ∧ aired ∧ not on disk
PUT  /api/v1/media/{id}/seasons/{n}/monitored     → a season and its episodes     (library.edit)
PUT  /api/v1/episodes/{id}/monitored              → one episode                   (library.edit)
POST /api/v1/admin/media/{id}/refresh-episodes    → ask the provider now          (admin.system, hidden)
task episodes.refresh, every 12 hours             → running series every run, the rest weekly
```

**The rule the increment is built around:** an episode row exists because the
provider said so. `library.EpisodeStore.Upsert` is the only writer of the season
and episode tables and takes provider data by construction, and
`library.TestNoEpisodeIsEverCreatedFromAFile` reads the source and fails the
build if any other file writes them.

**Files are matched to episodes by number, not by foreign key.** One file can
hold several episodes — `S02E01-E02` on the live library — and a key can point at
one. The join is a range, so the scan and the importer stay ignorant of episodes
entirely.

**Wanted means aired.** TMDB lists announced episodes with no date; treating a
missing date as "already aired" would put every unscheduled episode of every
running show on the wanted list on day one. They are *announced*, said as their
own state in the API and the UI.

**The UI** shows each season as *have of known*, the provider's count beside it
when they differ, an announced season as announced rather than "0 of 0", and
collapses every season with nothing in it needing attention: on the live series
the specials fold away and seasons 1 and 2, which have gaps, stay open. A
**Wanted** screen lists what is missing across the library.

#### What building it found

Eight defects; the detail is in ADR-0022's addendum. The pattern is the useful
part: **five of them were found by reading the code to write this entry, in code
whose tests all passed**, and one of those tests asserted the wrong behaviour.

| # | Defect | Found by |
|---|---|---|
| 1 | New episodes ignored the season's monitoring — a season switched off gained a monitored episode the day one was announced. The ADR, the migration and the comments all described inheritance; nothing implemented it | A test of the ADR's claim |
| 2 | The storage package imported the provider package (the refresher lived in `library`) | The compiler: an import cycle. Moved to `internal/tv`; a structural test holds the layering |
| 3 | **A season whose fetch failed was never retried.** It recorded the provider's new count without the episodes, so the next refresh saw agreement. The comment above it promised the opposite | Reading |
| 4 | A failed season was counted as *skipped*, and reported as one that "could not have changed" | Reading |
| 5 | **A rate limit did not stop anything.** Each remaining season was asked into the same limit, the refresh reported success, and the pass moved on to the next series | Reading |
| 6 | A season the provider stopped listing was never removed — the "merged season" case ADR-0022 names | Reading |
| 7 | **A renewal was never noticed.** "Series end" was true and "so stop asking" was not: a show between seasons has nothing recent and nothing to air, so it was never asked about again. On the live instance the old selection returned *nothing* for Severance, whose third season is announced | Reading, then confirmed against the live database |
| 8 | Once 7 was fixed, a renewed series the operator had dropped would have come back monitored | Reasoning about 7's fix |

And one outside this increment: an invite-list test that would have begun
failing on a fixed date, because `ListInvites` read the wall clock while its
service used an injected one. Now `svc.Now()`.

#### Verified against the live provider

Severance (TMDB 95396), five of its files on disk, one of them the double
episode:

```
S00  off  known=1  count=1   -
S01  mon  known=9  count=9   ##..#....      # = on disk  . = wanted
S02  mon  known=10 count=10  ##........
S03  mon  known=0  count=0   (announced)
```

Fourteen wanted; switching season 1 off takes it to eight, in the UI and over the
API. A second refresh fetches season 3 alone and skips three. Per-season episode
counts agree with the provider's own figures; a season that does not exist is an
error, not an empty list. In real Chromium against the binary: a clean refresh is
a green notice, a partial one is an error, and a 503 met part-way re-reads the
list — the last two played to the page by intercepting this instance's own route,
because a live provider will not fail on request. Reverting the UI change makes
that run fail on both.

| Claim | How it is established |
|---|---|
| No episode is ever created from a file | `library.TestNoEpisodeIsEverCreatedFromAFile` — reads the source |
| The storage layer imports nothing above it | `library.TestTheStorageLayerImportsNothingAboveIt` — mutation-verified |
| A season knows what it is missing | `library.TestASeasonKnowsWhatItIsMissing` |
| A file covering a range counts for every episode in it | `library.TestAFileCoveringARangeCountsForEveryEpisodeInIt`, and the live `S02E01-E02` |
| Announced is not overdue | `library.TestAnAnnouncedEpisodeIsNotWanted`, `api.TestAnAnnouncedEpisodeIsSaidToBeAnnounced` |
| Specials are kept and start off | `library.TestSpecialsAreKeptAndNotMonitored` |
| New episodes inherit the season's monitoring | `library.TestANewEpisodeInheritsTheSeasonsMonitoredState`, `library.TestANewEpisodeInAMonitoredSeasonIsMonitored` |
| A refresh never overrides the operator | `library.TestARefreshNeverOverridesTheOperatorsChoice`; the task holds browse only (`authz.TestEachBackgroundTaskHoldsExactlyItsOwnGrant`) |
| What the provider dropped goes; files do not | `library.TestAnEpisodeTheProviderDroppedIsRemovedAndItsFileIsNot`, `library.TestASeasonTheProviderNoLongerListsIsRemoved` — mutation-verified |
| An empty answer removes nothing | `library.TestAnEmptySeasonListRemovesNothing` — mutation-verified |
| A renewal is noticed | `library.TestAFinishedSeriesIsStillAskedAboutEveryWeek`, `library.TestAnAnnouncedSeasonKeepsItsSeriesOnTheList` — mutation-verified |
| A dropped series stays dropped; a followed one is followed | `library.TestARenewedSeriesTheOperatorDroppedStaysDropped`, `library.TestARenewedSeriesTheOperatorFollowsIsFollowed`, `library.TestFollowingOnlyTheSpecialsIsNotFollowingTheSeries` — mutation-verified |
| A zero refresh policy is refused, not read as "everything, every run" | `library.TestARefreshPolicyWithAZeroInItIsRefused` — mutation-verified |
| A finished season is not fetched again | `tv.TestAFinishedSeasonIsNotFetchedAgain` |
| A failed season is asked about again | `tv.TestASeasonThatFailedIsAskedAboutAgain` — mutation-verified |
| …and is reported as failed, not unchanged | `tv.TestOneSeasonFailingDoesNotLoseTheRest`, `tv.TestTheSummaryDoesNotCallAFailureUnchanged`, `api.TestARefreshThatMissedASeasonSaysSo` — mutation-verified |
| A rate limit stops the refresh and the pass, and keeps what was read | `tv.TestARateLimitStopsTheRefreshAndKeepsWhatWasRead`, `tv.TestRefreshAllStopsAtALimitMetPartWayThroughASeries` — mutation-verified |
| The 503 says what was recorded before the limit | `api.TestARateLimitSaysWhatWasRecordedBeforeIt` — mutation-verified |
| Switching monitoring needs `library.edit` | `library.TestMonitoringNeedsThePermissionToEdit` |

#### Also found: the copy on the ThinkPad did not compile

Checking the synced copy by hashing every file on both sides — rather than
trusting a list of what had changed — found it had drifted in six places:

| File | On the ThinkPad |
|---|---|
| `go.mod`, `go.sum` | From before the torrent library was added: no `anacrolix/torrent` at all, so the module could not even resolve its imports |
| `internal/download/metainfo.go` | Never sent. It defines `loadMetaInfo`, which `engine.go` calls |
| `internal/importer/identity_test.go` | Never sent |
| `internal/identify/score.go`, `score_test.go` | An older version, sent mid-increment and never re-sent |

So **the copy on the ThinkPad did not build**, and had not since the download
engine reached it. Every earlier sync sent a hand-written list of changed files, and
these were never on one. All six are current now, and a sync is checked by
comparing a digest of every file on both sides, which is how the next drift
would be caught rather than trusted away.

Two things came out of the same check. `go.mod` listed the torrent library as
an *indirect* dependency although `internal/download` imports it; `go mod tidy`
fixed that, and the Dockerfile's module steps were replayed against an empty
module cache to confirm the result downloads, verifies and builds. And a
leftover probe program, `cmd/zzprobe`, from confirming that the torrent library
builds without cgo, was in the working tree and counted here as a package. It is
deleted, which is why the package count stayed at 28 across an increment that
added one.

**Not built:** searching for what is wanted (the list exists; nothing sends it to
the indexers), a calendar, absolute numbering, per-series settings, and the
Sonarr migration this unblocks — all in
[DROPPED-FEATURES.md](docs/DROPPED-FEATURES.md).

**Noted, not fixed:**

- A TMDB **404** reads "the provider's response was not the expected shape",
  with TMDB's own "could not be found" after it. Predates this increment;
  changing the mapping touches the credential screen's error handling.
- Timestamps are stored as RFC 3339 text with trimmed fractional seconds, which
  does not sort correctly *within one second* (`…:00.5Z` sorts before `…:00Z`).
  House style across the stores, so every comparison it affects is wrong by
  under a second; recorded rather than migrated.

### Increment 4l — searching for a wanted episode ✅

Architecture first: **[ADR-0023](docs/adr/0023-searching-for-a-wanted-episode.md)**.

4k produced a list of what a series is missing; nothing acted on it. Now a
missing episode on the series page or the Wanted screen has a **Search** button,
and what it returns is judged against *that episode*.

```
POST /api/v1/episodes/{id}/search   → every candidate, judged against the episode (acquisition.search)
POST /api/v1/releases/grab          → unchanged route; the ticket now carries the episode, sealed
GET  /api/v1/queue                  → each transfer says which episode it was grabbed for
```

**Only the episode itself can be grabbed.** A result is the episode when its
series name matches the series' title *or one of the provider's alternative
titles* — folded for case, accents, punctuation, `&` and apostrophes — its year
is within one of the series', and its season and episode (or range) cover the
wanted one. Everything else comes back with the reason and no ticket: *a
different series: "Severance Pay"*, *S02E04, not S02E03*, *a whole-season pack*.
A person still chooses; nothing grabs on its own (decision 1 of the ADR, and the
same rule as requests in ADR-0017).

**The grab carries the episode to the import.** The episode is sealed into the
grab ticket beside the release, copied onto the queue row (migration 0014), and
read by the importer, which files the download under **that series — its root,
its folder** — and refuses a file whose numbering does not cover the episode.
Before this the importer worked the series out from the release name, and
`Severance.2022.S02E03…` built the folder `Severance (2022)` and created a second
Severance beside the one being followed. `importer.TestWithoutATargetTheSeriesIsWorkedOutFromTheName`
keeps that old behaviour pinned for downloads grabbed from the general search,
as the control that shows the target is what changed the outcome.

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **An indexer on the operator's own network is refused.** The `indexer` egress profile denies private addresses as SSRF protection, which also refuses a Prowlarr or Jackett on the LAN or the same host — the ordinary way to run one, and until Cardigann lands the only route to most trackers. Measured on the running instance: `192.168.1.10:9696` → *not a routable public address*; `localhost:9696` → *resolved to 127.0.0.1*. Both **saved without a warning**. The claim "covers anything fronted by Prowlarr or Jackett" in DROPPED-FEATURES and ADR-0003 was therefore false for the usual deployment; both are corrected | Not fixed in 4l, because it relaxes a security control; approved and **fixed in 4m** |
| 2 | **A skipped download was retried every minute, forever.** The import task re-attempted anything not yet imported, so a download skipped for a reason that cannot change — a disc image, a season pack, a file that is not the episode it was grabbed for — wrote an `import_record` row and an INFO line every minute for as long as it existed. Found writing the test for the target refusal, which is a new permanent skip | Fixed: a skip is retried hourly (`importer.SkipRetryInterval`), a failure still at once, and an unchanged outcome moves the existing record's time instead of adding a row |
| 3 | A queue test could not tell a stale series id from a fresh one — re-grabbing a release for a different episode of the *same* series looked correct whichever id survived. A mutation found it | Fixed: the test changes the series too |
| 4 | Two SECURITY.md "not yet" entries had been false since Phase 3: path containment "still to come" (it is `os.Root`, 3a) and "no user-management screen" (it has one, 3f) | Corrected |
| 5 | `go.mod` drifted from the imports again: this increment's accent folding imports `golang.org/x/text`, still listed as indirect. The same thing happened with the torrent library and was fixed in 4k | Tidied, and CI now runs `go mod tidy -diff`, so a third time fails the build instead of waiting to be noticed |

#### Verified

Against the live TMDB API: the three alternative titles ADR-0023 relies on —
*The Office (US)*, *Law and Order SVU*, *Daredevil* — are there
(`metadata.TestLiveAlternativeTitlesCarryTheSceneNames`). Against the running
instance: an episode search with no indexers is a 409 with a sentence; with one
indexer pointed at a real host that is not an indexer, the search reads
Severance's twelve alternative titles from TMDB, queries through the real client
and egress guard, and reports the indexer's own `HTTP 404` as a partial result
rather than as "nothing found". In real Chromium against the binary: Search is
offered on missing, aired episodes and not on ones held or announced; results
open under the episode; a match has a Grab button and a different episode has
the reason instead; the grab reports *Queued for S02E03*; every Wanted row can be
searched. The match-and-grab half was played to the page by intercepting this
instance's own routes, since no live indexer will produce a chosen release on
demand; the server side of that half is `api.TestAnEpisodeSearchGrabsIntoThatEpisode`,
which runs the real router, search service, indexer client, tickets and grab
path with only the network and the torrent engine canned.

| Claim | How it is established |
|---|---|
| Only the episode itself can be grabbed | `search.TestEachReleaseIsJudgedAgainstTheEpisode`, `api.TestOnlyTheEpisodeCarriesATicket`, `api.TestAnEpisodeSearchGrabsIntoThatEpisode` — mutation-verified |
| The scene's names for a series are recognised; without them they are not | `search.TestAnAlternativeTitleMatchesTheScenesName`, `search.TestTitlesFoldToTheFormTheSceneUses`, `search.FuzzNormalizeTitle` — mutation-verified |
| The right Battlestar, not the wrong Doctor Who | `search.TestTheYearIsAllowedToBeOneOut` — mutation-verified |
| A season pack is shown and never grabbable | `search.TestEachReleaseIsJudgedAgainstTheEpisode` — mutation-verified |
| A different search term does not widen what is grabbable | `search.TestAnotherTermDoesNotWidenWhatMatches` |
| Matching makes a release eligible, not acceptable | `search.TestAMatchingReleaseTheProfileRefusesIsStillRefused` |
| The episode travels sealed, and only sealed | `search.TestATicketCarriesItsTarget`, `api.TestAGrabHandsTheSealedEpisodeToTheQueue` — mutation-verified |
| It survives the queue, including a restart | `download.TestATargetRoundTrips`, `download.TestATargetInTheSpecialsSeasonIsATarget`, `download.TestARegrabKeepsOrReplacesTheTargetButNeverLosesIt` — mutation-verified |
| …and reaches the import | `main.TestAnImportIsToldWhatTheGrabWasFor` — mutation-verified |
| The import files it under that series, on that series' root, in its folder as it is on disk | `importer.TestAGrabForAnEpisodeLandsInThatSeries`, `…LandsOnThatSeriesRoot`, `importer.TestTheSeriesFolderIsUsedAsItIsOnDisk` — mutation-verified |
| A file that is not the episode is attached to nothing | `importer.TestAFileThatIsNotTheTargetIsRefused` — mutation-verified |
| A series deleted mid-download is not re-created | `importer.TestATargetThatWasDeletedIsNotRecreated` — mutation-verified |
| A skip is retried hourly, a failure at once, and an unchanged skip adds no rows | `importer.TestASkippedDownloadIsNotRetriedEveryMinute`, `importer.TestAFailedImportIsRetriedAtOnce`, `importer.TestRetryingAnUnchangedSkipDoesNotGrowTheRecord` — mutation-verified |
| Missing alternative titles are said, not hidden | `api.TestMissingAlternativeTitlesAreSaid` |
| A User cannot search the indexers for an episode | `api.TestSearchingForAnEpisodeNeedsThePermissionToSearch` |
| The queue says what an episode grab is for | `api.TestTheQueueSaysWhatAnEpisodeGrabWasFor` — mutation-verified |

Twenty-six mutations across the increment; every one fails a test.

**Not built:** automatic grabbing (what it would need is written down in
DROPPED-FEATURES — built since, in 4v), searching a season or the whole wanted
list at once, season packs, TVDB-id searching. Indexers on the operator's own
network: 4m.

### Increment 4m — indexers on the operator's own network ✅

Architecture first: **[ADR-0024](docs/adr/0024-indexers-on-the-operators-network.md)**,
which relaxes a security control and was approved by the operator before it was
built.

4l found that every indexer on a private address was refused: the `indexer`
egress profile denies private destinations, because an indexer's *feed* decides
what this software fetches next and a hostile one could point it at the router
or a metadata endpoint. That also refused a Prowlarr or Jackett on the LAN, the
same host or the same compose file — the usual deployment, and until Cardigann
lands the only route to most trackers.

**The exemption is one address: the host and port the operator typed for that
indexer.** It is built into that indexer's HTTP client — one per address,
cached — rather than carried in a request context, and a structural test pins
the function that creates it to `internal/indexer`. The pre-flight check and the
redirect check use the same per-indexer rule, so they cannot disagree.

| | Allowed? |
|---|---|
| Search `http://192.168.1.10:9696` — the configured address | Yes |
| A download link in its feed on `http://192.168.1.10:9696` | Yes — Prowlarr and Jackett proxy downloads through themselves |
| A link or redirect to `192.168.1.10:22`, the router, another indexer | **No**, as before |
| An indexer saved at `169.254.169.254` | **No** — refused on the form, and at dial time too |
| A peer, a metadata or subtitle request to a private address | **No** — this changes only the indexer profile |

#### Verified end to end, on the running binary

A Torznab stand-in on `127.0.0.1` — a private address like any LAN one —
configured as an indexer, then driven through the whole loop:

```
save 169.254.169.254 as an indexer   -> 400: link-local, not somewhere a service on your network runs
save http://127.0.0.1:18181          -> 201
search Severance S02E03              -> 3 results (the one linked to another private port dropped from the feed);
                                        1 grabbable; the pack and S02E04 refused with their reasons
grab                                 -> .torrent fetched from the indexer's own address; queued "for S02E03"
engine                               -> data verified, marked complete
import                               -> Severance/Season 02/Severance (2022) - S02E03 [WEBDL-1080p].mkv
                                        hardlinked (same inode as the seeding copy), under the ONE Severance
library                              -> season 2 "3 of 10"; S02E03 gone from the wanted list (14 -> 13)
```

One honest substitution: no peer on loopback could supply the data — the
download profile refuses private peers, correctly — so the bytes were placed in
the engine's data directory and the engine verified them against the torrent's
piece hashes. Everything else is the production path. The first attempt was
skipped as "too small to be a feature or an episode", which is right: the stand-in's
release was 1 MiB, under the importer's 8 MiB floor, and the second used 16 MiB.
In real Chromium: a live search through the LAN indexer shows exactly the S02E04
release as grabbable, and S02E03, now on disk, no longer offers a search.

| Claim | How it is established |
|---|---|
| The configured address is reachable, and only it — not another port on the same host | `egress.TestTheAllowedDestinationAndOnlyItIsReachable`, `indexer.TestALinkToAnotherPrivateAddressIsStillRefused` — mutation-verified |
| A LAN indexer is searched and its own links fetched | `indexer.TestAnIndexerOnTheOperatorsNetworkWorks` — mutation-verified |
| A redirect away from it is refused, by the redirect check | `indexer.TestARedirectAwayFromTheIndexerIsRefused` — mutation-verified |
| One indexer's allowance is not another's | `indexer.TestOneIndexersAllowanceIsNotAnothers` |
| A hostname allowance is by name, not by machine | `egress.TestAHostnameAllowanceIsByName` — mutation-verified |
| Link-local is refused even as the configured address — at save, pre-flight and dial | `indexer.TestAnIndexerAddressIsCheckedWhenSaved`, `indexer.TestTheIndexersOwnAddressIsStillNotLinkLocal`, `egress.TestLinkLocalIsRefusedEvenWhenItIsTheConfiguredAddress` — mutation-verified |
| A public indexer's feed still cannot reach a private address | `indexer.TestTheBaseRuleStillRefusesPrivateAddresses` |
| The exemption is created in one place | `egress.TestOnlyTheIndexerClientMayAllowAPrivateDestination` — mutation-verified |

Fourteen mutations; thirteen failed a test at once. The fourteenth removed
`OnOperatorNetwork`'s explicit exclusion of link-local and multicast, and
nothing changed — because none of those is in the three ranges it allows. The
exclusion was dead code, and it is gone; the tests that pin those answers
stayed.

**Not handled:** a LAN indexer through a SOCKS5-routed indexer profile (the proxy
is not on the LAN); download links that name the same machine by a different
spelling than the configured one; non-ASCII hostnames. All in DROPPED-FEATURES.

### Increment 4n — adding a series before any of it is on disk ✅

Architecture first: **[ADR-0025](docs/adr/0025-adding-a-series-before-it-is-on-disk.md)**.

The operator said they are starting a new library rather than migrating one.
That exposed the gap every earlier increment could step around: a library item
came into being only when a scan found files or an import built one from a
release name, so on an empty instance the only way to follow a show was to
download an episode of it first, wait for identification, confirm it and
refresh — backwards. Now:

```
GET  /api/v1/metadata/search?kind=series&title=…   → unchanged: the provider's candidates
POST /api/v1/media  {kind, tmdb_id, monitor, root_folder_id?, folder?}   → 201: the series, every episode, the choice applied (library.edit)
```

- **The request names an id, never a title.** Title, year, IMDb id and the
  default folder come from the provider's answer; a body carrying `title` is a
  400, not silently ignored.
- **Nothing is downloaded and nothing is written to disk.** The folder is a name
  until the first import creates it — `follow.TestAnAddedSeriesTakesItsFirstEpisodeIntoTheFolderItNamed`
  imports an episode into an added series and finds it at
  `Severance (2022)/Season 02/…`.
- **All or nothing.** Every season is read from the provider first, then one
  transaction writes the item, its identification, every season and episode,
  and the monitoring choice. A season that cannot be read adds nothing.
- **Which episodes are wanted is chosen, with no default** — `all`, `future`,
  `latest` or `none`, each expressed in the season and episode flags ADR-0022
  already has, and each meeting a renewal as chosen without a series-level
  switch.
- **The add is a person's identification**, recorded as confirmed by them, so
  the identification pass never re-proposes it, the Identify screen can reopen
  it, and its poster is one this instance may fetch.
- **One item per title, one per folder, nothing adopted.** A second add of the
  same series is a 409 naming the first, decided before any provider request; a
  folder a scan already found is a 409 naming that item.
- **Films are designed and not built** (decision 9): an added film could only be
  acquired through the general search, whose import names the film from the
  release — the mechanism that created a second *Severance* before ADR-0023. It
  ships with a film search that carries the film into the grab. `kind: movie`
  answers 501 with that reason.

A screen to do it: **Add**, beside Library, for Managers and Admins.

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **A provider 404 was reported as "not the expected shape"** — recorded as unhandled in ADR-0022. Adding by id needs the difference, since a mistyped id is the operator's to fix. The live body is `{"success":false,"status_code":34,…}` for a series, a season and a film alike | Fixed: `metadata.ErrNotFound`. The single-series refresh also stops calling a provider failure an internal error: a 502 for the provider, a 409 for a series the provider no longer has |
| 2 | **Titles with a colon made ugly names.** `SafeComponent` turns `:` `?` `*` into `_`, so the provider's *Star Trek: Discovery* would have become the folder `Star Trek_ Discovery (2017)`. Release names never contain a colon, which is why it had not come up | Fixed: a small substitution table before `SafeComponent` — `Star Trek - Discovery (2017)`, `What If... (2021)` — used for an added series' folder and for the files an episode search imports into it |
| 3 | **Per-library grants and rating ceilings are enforced nowhere.** They are stored at approval and carried on every principal, but nothing that lists or serves media applies them: `authz.ScopeFor` has no caller outside its tests, the `library` table the grants reference has never held a row (so a grant naming one would fail its foreign key), the UI always sends `library_ids: []`, and media items carry no rating. **Every account with `media.browse` sees the whole library.** 1b's line "approver sets role, libraries and rating ceiling" describes the form, not an effect | **Not fixed** — it needs a decision (what a "library" is; root folders are the obvious answer) and whether it is wanted at all. Now stated in SECURITY.md and DROPPED-FEATURES |
| 4 | Three statements had outlived their facts: SECURITY.md said no successful metadata response had ever been parsed and nothing used the provider (false since 3j and 3k); THREAT-MODEL's residual-risk table and DROPPED-FEATURES' flagged list still carried the `unconfined` seccomp profile replaced in 4c. The metadata package's own comment said the same as the first | Corrected |
| 5 | My first race test proved nothing. Eight goroutines adding one title at once passed thirty runs out of thirty **against a check-then-insert version too** — the scheduler never opened the window. Measured by hand instead: with SQLite's WAL, check-then-insert does not create a duplicate; the loser's insert fails with `SQLITE_BUSY_SNAPSHOT`, "database is locked", which would reach the operator as an internal error | The add is one conditional statement, and `importer.TestAnAddThatLosesARaceIsToldSo` interleaves two adds by hand: the second waits, then gets a clean conflict. It kills the check-then-insert mutant; the goroutine test did not |
| 6 | The monitoring SQL excluded specials twice, once explicitly and once because the first followed season is never below 1. The explicit half was dead code — a mutation removing it changed nothing | Removed; the rule is `from ≥ 1`, and a mutation setting `from` to 0 fails the tests |
| 7 | **The CI lint job is very likely red, and not because of this increment.** It pins golangci-lint `latest`; run locally with 2.14 (the preinstalled 2.5 refuses a Go 1.26 module outright) the tree has **226 findings**: `noctx` 86 (mostly `httptest.NewRequest` and context-free SQL in tests, a few real — a context-free `net.Dial` in the egress verifier, a bare `http.Client.Get` in `main.go`), `errcheck` 43 (unchecked `rows.Close`), `misspell` 27 (almost all "hardlinked", a false positive), `gosec` 25, `errorlint` 23, `nilerr` 14, `staticcheck` 8. This increment's new code had four, now fixed | **Not fixed here** — a cleanup with its own review, and some findings are deliberate (the CSRF cookie is readable by script on purpose). Worth doing next, since a lint job that is always red is one nobody reads. **Done in 4r** |

#### Verified

Against the live TMDB API, through the compiled binary, **starting from an empty
database** — setup, enrolment, a TMDB token, one root folder for series:

```
add Severance, future           201  0.5s  4 seasons, 20 episodes, 0 wanted
                                           S00 off · S01 off · S02 ON with its 10 aired episodes off · S03 (announced) ON
add Severance again             409        names item 1
add The Simpsons, latest        201  4.9s  39 seasons, 885 episodes, 15 wanted: season 37, and season 38 (begins 2026-09-27)
add Star Trek: Discovery, none  201  0.8s  folder "Star Trek - Discovery (2017)", 136 episodes, 0 wanted
add What If...?, all            201  0.5s  folder "What If... (2021)", 26 wanted
a film                          501        says why, and that Search still acquires films
an id TMDB does not have        422        "the provider has no such series" — the provider's own 404 underneath
no monitoring choice            400
a body naming the title         400
on disk under the TV root       nothing
the identification              confirmed, decided by a person, verdict "chosen"; poster served as image/webp
```

In real Chromium, on the same fresh instance: search the provider for *Andor*,
**Add…**, the choice starts on "Choose…" and Add refuses in words without one
(not "could not reach the server"), *latest* → *Added Andor: 3 season(s), 29
episode(s), 12 wanted*, **Open in the library** shows season 2 followed and
missing, season 1 and the specials not, and *Nothing of this series is on disk
yet*. Adding *Severance* again names the existing item and offers it once. Zero
console errors, zero CSP violations.

| Claim | How it is established |
|---|---|
| The provider's id names the series; the request cannot | `api.TestARequestCannotNameWhatItIsAdding`, `follow.TestAddingASeriesRecordsAllOfItAsChosen` |
| Nothing is created on disk; the first import creates the folder the add named | `follow.TestAddingASeriesCreatesNothingOnDisk`, `follow.TestAnAddedSeriesTakesItsFirstEpisodeIntoTheFolderItNamed` |
| All or nothing | `follow.TestASeasonThatCannotBeReadAddsNothing`, `follow.TestAFailureInsideTheAddTakesEverythingWithIt`, `tv.TestReadingASeriesStopsAtTheFirstSeasonItCannotRead` — mutation-verified |
| One item per title, decided before the provider is asked; the loser of a race is told so | `importer.TestTheSameTitleCannotBeAddedTwice`, `importer.TestAnAddThatLosesARaceIsToldSo`, `follow.TestAddingTheSameSeriesTwiceAsksTheProviderNothing` — mutation-verified |
| A film and a series may share a number | `importer.TestAFilmAndASeriesMayShareAProviderID` — mutation-verified |
| A folder already taken is not adopted | `importer.TestAnOccupiedFolderIsNotAdopted`, `follow.TestAFolderAlreadyTakenIsNotAdopted` — mutation-verified |
| Each monitoring choice does what the ADR's table says | `library.TestMonitoringAllWantsEveryEpisodeThatHasAired`, `…FutureWantsOnlyWhatIsStillToCome`, `…LatestFollowsTheCurrentSeasonAndAfter`, `…NoneWantsNothing` — mutation-verified |
| …and meets a renewal as chosen, including a finished series under *future* | `library.TestEachChoiceMeetsARenewalAsChosen`, `library.TestFutureOnAFinishedSeriesStillFollowsARenewal` — mutation-verified |
| There is no default choice | `library.TestThereIsNoDefaultChoice` — mutation-verified |
| An episode dated today is still to come | `library.TestAnEpisodeDatedTodayIsStillToCome` — mutation-verified |
| The add is a person's identification, with everything a confirmation leaves | `identify.TestAChosenTitleIsAPersonsDecision`, `identify.TestAChosenTitleIsWhatTheRestOfIdentificationExpects` — mutation-verified |
| Everything refusable is refused before the provider is asked | `follow.TestWhatCanBeRefusedIsRefusedBeforeTheProviderIsAsked` — mutation-verified |
| Adding needs `library.edit` — at the route, in the service and in the store; no background task holds it | `api.TestAManagerMayAddAndAUserMayNot`, `follow.TestAddingNeedsThePermissionToEditTheLibrary`, `importer.TestAddingNeedsThePermissionToEditTheLibrary` |
| Folder names: a provider's title written as people write filenames; an operator's name used as typed or refused | `importer.TestAProvidersTitleIsWrittenTheWayPeopleWriteFilenames`, `importer.TestAFolderNameIsTakenAsTypedOrRefused` — mutation-verified |
| A 404 is "no such title" | `metadata.TestANotFoundIsNoSuchTitleNotABadShape`, `metadata.TestLiveDetailsForATitleThatDoesNotExist` |
| Every refusal says whose problem it is | `api.TestEveryRefusalSaysWhy`, `api.TestAProviderFailureDuringARefreshIsSaidToBeTheProviders` — mutation-verified |
| A scan leaves an added series alone; removing one moves nothing | `follow.TestAScanLeavesAnAddedSeriesAlone`, `follow.TestAnAddedSeriesIsRemovedWithNothingToMove` |

Thirty-four mutations. Thirty were killed at once. Four were not, and each
taught something: the race test could not see a check-then-insert (finding 5);
the explicit specials rule was dead (finding 6); one mutant did not compile as
written; and making the route's permission `media.browse` changed nothing a
User could do, because the service refuses them itself — the route's own
decision is held by the generated access document, `docs.TestTheGeneratedDocumentsAreCurrent`,
which fails when it changes. After the fixes, all thirty-four fail a test.

**Not built:** adding a film (next, with its search); approving a request adding
the title (ADR-0017 still creates nothing); changing a series' monitoring choice
after it is added (the four choices exist; only the add applies one); posters in
the Add screen's search results (ADR-0018's bound: only recorded titles).

### Increment 4o — adding a film, and searching for it into the library ✅

Architecture first: **[ADR-0026](docs/adr/0026-adding-a-film-and-searching-for-it.md)**.

The second half of ADR-0025's design (its decision 9), built as that decision
said it had to be: a film can be added only together with a search that carries
the film into its download. Alone, an added film would be acquired through the
general search, whose import names the film from the release — and
`Star.Wars.Episode.IV.A.New.Hope.1977` is not *Star Wars (1977)*.

```
POST /api/v1/media  {kind: "movie", tmdb_id, root_folder_id?, folder?}   → 201: the film, on the Wanted list (library.edit)
POST /api/v1/media/{id}/search  {term?, profile_id?, indexer_ids?}       → every release judged against the film; tickets only for the film (acquisition.search)
GET  /api/v1/wanted                                                      → films as well as episodes
```

- **Adding a film is adding a series without episodes**: the provider's id names
  it, one request, the folder is `Title (Year)`, nothing is written to disk, it
  is a person's identification, one per id and one per folder. A monitoring
  choice sent for a film is **refused, not ignored**.
- **Adding a film is wanting it.** No monitoring switch, and no release-date
  rule: the provider's date is not refreshed, and a stale date hides films. An
  unreleased film is added and wanted like any other.
- **Every release is judged against the film**, in order: television is refused;
  the title must be one of the film's names — its title, its original title and
  the provider's alternative titles, all from one request; the release must
  name a year, because remakes share titles; and the year must be within one.
  The first check that fails is the reason shown.
- **The indexers are asked by the folded title and the year** — `dune 2021`,
  `amelie 2001` — as a general search, not by IMDb id. The term can be changed,
  which changes what is asked and never what can match.
- **The film is sealed into the grab**, as an episode is. The queue row now says
  which kind of target it carries (migration 0015), and the import files the
  download under that film, on its root, named from its title:
  `Dune.Part.One.2021…` becomes `Dune (2021)/Dune (2021) [Bluray-1080p].mkv`.
- **A transfer says what it is for, by name** — *Dune (2021)*, *Severance (2022)
  S02E03* — in the grab's answer and in the queue.

On screen: **Add** now asks *Series* or *Film*; a film added there can be
searched for at once; **Wanted** lists films above episodes; a film not on disk
has **Search** on its page; and the film search offers its term back to be
changed and asked again.

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **A finished download could stay complete and never be imported, and nothing said so** — since 3b. The import pass asked only the torrent engine for a download's files, and the engine lets a finished transfer go: at the seeding task's next tick when seeding is off, and every finished transfer at a restart. If either came before the import's next pass — it runs each minute — the pass was told "no such transfer", and a comment called that "nothing alarming". Found live: the *Star Wars* grab made in the browser finished sixteen seconds before a restart, and stayed complete and unimported with no history at all | Fixed: the import reads what the download left on disk — its own directory, walked through `os.Root`, regular files only, capped — and one whose files are gone is recorded as skipped with the reason, retried hourly. The same *Star Wars* download then imported, hardlinked, into *Star Wars (1977)* |
| 2 | **My first version stored every episode target's kind as NULL.** A constant named `targetEpisode` was shadowed by `Put`'s local variable of the same name, so the column received that variable, which was nil. Episode targets still round-tripped — the read side takes an unmarked row with all three numbers for the episode it was before migration 0015 — so the round-trip tests passed | Fixed, and the constants renamed with a comment saying why. Caught only by the test that reads the column itself, which is why `download.TestAFilmTargetRoundTrips` does |
| 3 | **The queue told a finished download it was waiting for metadata** — since 3e. A row the engine no longer runs has no live progress, and the screen showed the magnet sentence for it | Fixed: a finished row says it finished, a stopped one that it stopped. Checked in Chromium |
| 4 | **Twenty-five of the lint job's findings were the word "hardlinked"**, which misspell's dictionary knows only as "hardline" | `.golangci.yml` teaches it the word. The tree now has **201 findings** (226 at 4n); this increment's code adds none |

#### Verified

Against the live TMDB API, through the compiled binary, **from an empty
database** with the download engine on, a films root, a TV root, and a Torznab
indexer on this machine serving eight releases — the right *Dune* under the
scene's name with five seeders, its namesakes, and releases of two other films:

```
add Dune (438631)                 201  folder "Dune (2021)", no monitoring, "on the Wanted list"
add Star Wars (11)                201  "Star Wars (1977)"
add Amélie (194)                  201  "Amélie (2001)"
add Avengers: Secret Wars         201  "Avengers - Secret Wars (2027)" — TMDB: Planned, December 2027 — and wanted
add Dune again                    409  names the first
a film with a monitoring choice   400
an id TMDB does not have          422
search Dune (2021)                the indexer was asked t=search q="dune 2021", no IMDb id
                                  TICKET  Dune.Part.One.2021.1080p.BluRay  (5 seeders; matched by the live alternative "Dune: Part One")
                                  TICKET  Dune.2021.HDCAM                  (no profile named; with HD-1080p it is the film and refused)
                                  refused Dune.Part.Two.2024 · Dune.1984 (a different film) · Dune.Prophecy.S01E01 (television)
                                          · Dune.1080p (names no year) · the Star Wars and Amélie releases (different films)
search Star Wars (1977)           only Star.Wars.Episode.IV.A.New.Hope.1977 is grabbable
search Amélie (2001)              only Le.Fabuleux.Destin.d'Amelie.Poulain.2001 — its original title
grab Dune.Part.One.2021           202, "for": Dune (2021); the queue row: film, item 1, no season, no episode
import                            Dune (2021)/Dune (2021) [Bluray-1080p].mkv, hardlinked; no "Dune Part One (2021)";
                                  four films, not five; Dune left the wanted list
```

In real Chromium, on the same instance: **Add** → *Film* → *Arrival* (2016) →
**Add…** asks nothing but an optional folder name and says where it will live →
*Added Arrival (2016). It is on the Wanted list.* → **Search for it now** shows
eight results, none of them *Arrival*, each saying *No grab button: this is not
the film*; asking again for `star wars` still matches nothing. **Wanted** lists
the films above the episodes, headed; **Search** on *Star Wars* offers one grab,
and grabbing says *Queued for Star Wars (1977)*. **Queue** names both films.
*Avengers: Secret Wars* in the library says *Not on disk yet* and searches for
`avengers secret wars 2027`; *Dune* shows its file and **Play**. 4n's series
flow was run again through the new screen — add a series with *latest*, open it,
add one already there and be offered it once. Zero console errors, zero CSP
violations.

| Claim | How it is established |
|---|---|
| A film is added by id, with no monitoring choice, one request, nothing on disk, as a person's identification, and is wanted | `follow.TestAddingAFilmRecordsItAsChosenAndWantsIt`, `follow.TestWhatCanBeRefusedIsRefusedBeforeTheProviderIsAsked`, `api.TestAddingAFilmThroughTheAPIPutsItOnTheWantedList` — mutation-verified |
| A duplicate costs no request; a film and a series may share a number | `follow.TestAddingTheSameFilmTwiceAsksTheProviderNothing` — mutation-verified |
| Every release is judged in the ADR's order | `search.TestEachReleaseIsJudgedAgainstTheFilm`, `search.TestAFilmWithNoYearMatchesNothing` — mutation-verified |
| The original title and the alternatives are what make real release names match | `search.TestAFilmIsFoundUnderEveryNameItGoesBy`, `metadata.TestFilmTitlesAreTheTitleTheOriginalAndTheAlternatives`, `metadata.TestLiveFilmTitlesCarryTheNamesReleasesUse` — mutation-verified |
| The indexers are asked a general search for the folded title and year; another term changes nothing that matches | `search.TestAFilmIsAskedForByItsFoldedTitleAndYear`, `search.TestAFilmSearchMarksTheMatchesAndSaysWhyTheRestAreNot`, `search.TestAnotherTermDoesNotWidenWhatMatchesAFilm` — mutation-verified |
| Only the film gets a ticket, and the film is sealed into it in one shape | `api.TestOnlyTheFilmCarriesATicket`, `search.TestATicketCarriesAFilm`, `search.TestATargetIsAnEpisodeOrAFilmAndNotBoth`, `search.TestAMixedTargetIsNotOpened` — mutation-verified |
| The film reaches the queue from the ticket, and the row says film | `api.TestAFilmSearchGrabsIntoThatFilm`, `download.TestAFilmTargetRoundTrips`, `download.TestARowThatFitsNeitherShapeHasNoTarget`, `db.TestMigration15MarksEveryTargetedRowAsAnEpisode` — mutation-verified |
| The import files it under the film, on its root, named from its title — and refuses what does not fit | `importer.TestAGrabForAFilmLandsInThatFilm`, `importer.TestAGrabForAFilmLandsOnThatFilmsRoot`, `importer.TestAFilmIsNamedFromItsOwnTitle`, `importer.TestAFilmTargetThatDoesNotFitIsRefused`, `follow.TestAnAddedFilmTakesItsGrabIntoTheFolderItNamed` — mutation-verified |
| Wanted is a film with no file, and only a film | `importer.TestAFilmIsWantedUntilItHasAFile`, `api.TestWantedListsFilmsBesideEpisodes` — mutation-verified |
| A transfer says what it is for, by name | `api.TestAGrabHandsTheSealedFilmToTheQueue`, `api.TestTheQueueNamesWhatEachGrabWasFor` — mutation-verified |
| A finished download the engine let go is imported from what it left on disk, and that listing is contained | `main.TestAFinishedDownloadTheEngineLetGoIsStillImported`, `main.TestAFinishedDownloadWithNothingOnDiskSaysSo`, `download.TestFilesOnDiskListsWhatTheDownloadLeftAndNothingElse`, `download.TestFilesOnDiskRefusesWhatIsNotADownload` — mutation-verified |

Sixty-seven mutations, all killed. Two had to be rewritten before they would
compile, and neither was a survivor in disguise: both failed their tests once
they built.

**Not built:** monitoring a film without wanting it (only an automatic search
would make it matter); a release-date rule for films; searching by IMDb id (the
indexers' capabilities are not read); a quality profile on the film or episode
page's search (the API takes one; the screen does not offer it, so a camera
recording of a film still in cinemas is grabbable from there) — **built in 4p**;
approving a request adding the title (ADR-0017 still creates nothing).

---

### Increment 4p — one quality profile judges every search ✅

Architecture first: **[ADR-0027](docs/adr/0027-a-default-quality-profile.md)**.

The gap 4o left open and named: the episode and film searches sent no quality
profile and the screen offered none, so a camera recording of a film still in
cinemas came back with a Grab button. The general search had the same gap one
choice away — it started on *No profile*.

```
GET  /api/v1/quality-profiles                              → each profile says whether it is the default; default_id (acquisition.search)
PUT  /api/v1/admin/quality-profiles/default  {profile_id}  → a profile's id, or 0 for none (admin.system, hidden, audited)
POST /api/v1/releases/search · /api/v1/episodes/{id}/search · /api/v1/media/{id}/search
     profile_id absent → the default · 0 → none · an id → that profile · negative → 400
```

- **The instance has one default profile, or none.** A flag on the profile's
  row, and a partial unique index, so the database holds at most one whatever
  the code does. A new instance's default is *HD-1080p*. An existing instance
  gets the same from migration 0016, if its **built-in** *HD-1080p* is still
  there — an operator's own profile of that name is not promoted.
- **One rule for every interactive search.** Naming no profile means the
  default; `0` means none; an id means that profile. The general search changes
  meaning — absent used to mean unjudged — because two meanings for "no profile
  given" is how one screen ends up applying the wrong one.
- **Every answer says what judged it**: the profile, whether it was the default,
  and — when nothing did because the instance has no default — a sentence
  saying so and that every matching release can be grabbed.
- **Choosing the default is an administrator's**, on the permission that already
  governs saving a profile. The route is hidden from everyone else, and each
  change is audited as a changed setting, with what it was and what it became.
  *None* is allowed, on purpose, and the audit says who chose it.
- **The default cannot be deleted** until another is chosen — deleting it would
  quietly turn every search back to unjudged — and seeding the built-in
  profiles again does not restore a default somebody cleared.

On screen: the Search screen and the episode and film search panels each show
**Judged by**, starting on the default, marked *(default)*, with every other
profile and *No profile — judge them yourself*. The panels offer the term and
the profile back together. An administrator whose chosen profile is not the
default sees **Make this the default** beside it.

#### Verified

Through the compiled binary, against the live TMDB API and 4o's Torznab
indexer. On 4o's instance, upgraded in place:

```
migration 0016                    the built-in HD-1080p became the default; the other three did not
film search, no profile named     judged by HD-1080p (the default): only Dune.Part.One.2021.1080p.BluRay is grabbable
film search, profile_id 0         both Dunes grabbable — the camera recording too, now only when asked for
general search, no profile named  judged by HD-1080p (the default)
PUT default HD-720p               200 "Searches that name no profile are judged by HD-720p."
                                  the general search then grabs nothing: HD-720p accepts none of the eight releases served
PUT default HD-1080p              200, previous HD-720p
audit                             system.setting.changed "the default quality profile is now HD-1080p (was HD-720p)"
```

Then **from an empty database**: the four built-in profiles were seeded with
*HD-1080p* as the default, and 4o's whole run — add four films, search, grab,
import `Dune.Part.One.2021` into *Dune (2021)* — passed again, with the camera
recording refused unless `profile_id` 0 was sent.

In real Chromium, on that new instance: the Search screen starts on *HD-1080p
(default)* and says searches are judged by it; the default is not offered
*Make this the default*; a search for `dune` offers six Grab buttons and none on
the camera recording, which says *forbidden term*. Choosing *HD-720p* offers
**Make this the default**; clicking it says so and the list marks *HD-720p
(default)*; it was changed back the same way. *Amélie*'s panel says *judged by
HD-1080p (the default) · 1 of 8 result(s) are Amélie (2001)*; asked again under
*HD-720p* the same release says *No grab button: the profile refused this
release.*; under *No profile*, *judged by nobody: no profile*. An episode's
panel has the same form. 4o's film flow and 4n's series flow were run again on
the same instance and pass. Zero console errors, zero CSP violations.

| Claim | How it is established |
|---|---|
| A new instance is judged by HD-1080p; an existing one gets that from the migration only through its built-in HD-1080p | `release.TestANewInstanceIsJudgedByHD1080p`, `db.TestMigration16MakesTheBuiltinHD1080pTheDefault` — mutation-verified |
| One default at most, changed or cleared in one transaction; an unknown id changes nothing; seeding does not undo a clearing | `release.TestTheDefaultCanBeChangedAndCleared`, `release.TestTheDatabaseRefusesASecondDefault`, `release.TestAnUnknownProfileCannotBeTheDefault`, `release.TestSeedingDoesNotRestoreADefaultSomebodyCleared` — mutation-verified |
| Only `admin.system` chooses the default, and every change is audited with what it was | `release.TestChoosingTheDefaultNeedsSystemSettings`, `api.TestChoosingTheDefaultIsAnAdministratorsAndIsAudited` — mutation-verified |
| The default cannot be deleted | `release.TestTheDefaultProfileCannotBeDeleted` — mutation-verified |
| Absent means the default, `0` none, an id that profile, a negative id is refused; an unjudged answer says why | `api.TestEverySearchIsJudgedByTheDefaultUnlessAnotherIsChosen`, `api.TestWithNoDefaultASearchIsUnjudgedAndSaysSo` — mutation-verified |
| The film and the episode search read `profile_id` by the same rule | `api.TestTheFilmSearchReadsTheProfileByTheSameRule`, `api.TestTheEpisodeSearchReadsTheProfileByTheSameRule` — mutation-verified |

Twenty-three mutations, all killed.

**Not built:** a screen that edits a profile (the four built-in ones can be
chosen between, not changed); a profile per series or per film, which an
automatic search will need; the import still ranks upgrades by the default
ladder rather than by the profile that grabbed the release (ADR-0016).

---

### Increment 4q — an approved request ends in the library ✅

Architecture first:
**[ADR-0028](docs/adr/0028-an-approved-request-is-satisfied-by-a-library-item.md)**.

Writing the decision record found that ADR-0017's lifecycle had never worked
from the browser. A request ended when a grab that *named it* was imported, and
no screen ever named one — so a request approved on screen stayed *approved — no
release chosen yet* after its film had arrived, and went on counting against its
requester's twenty open requests. And since 4n and 4o, the reliable way to
acquire a title is through the library — add it, search from its page — which is
exactly the grab that names no request.

```
POST /api/v1/requests/{id}/item  {media_item_id}  → the library item that satisfies an approved request (request.approve, audited)
POST /api/v1/media                                 → a refusal now says which conflict, in a word: already_in_library | folder_taken
GET  /api/v1/requests                              → a linked request carries its item, named to whoever may browse the library
```

- **A request is satisfied by a library item, and an approver says which.** The
  request is words and the item is the provider's; the approver is the one who
  can tell which *Dune* was meant, so nothing links on its own. Only an
  approved request, only to an item of the kind it asked for; one item per
  request and several requests per item; changeable while the request is open,
  and audited with the item it replaced.
- **Adding from the request.** The Requests screen offers **Add to the
  library…**, and the Add screen opens searching the provider for what the
  request says. Whatever the approver adds is linked. When the title is already
  in the library, the refusal offers **Link the request to it** — for that
  conflict only, never for an item that merely occupies the folder.
- **Fulfilment follows the item**: an import into it, whichever grab brought
  the file; the link itself, when a file is already there; or the hourly scan,
  when somebody put one there by hand. A series request is fulfilled by the
  series' first file. The importer's hook is unchanged.
- **The requester sees what it became** — *in the library — nothing on disk
  yet*, then *in your library*, with the title's name — and the name goes only
  to somebody who may browse the library, in the request and in the answer to
  linking it.

Approving still downloads nothing, and ADR-0017's grab link still works for an
API client that sends one.

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **No screen had ever linked a grab to a request** — since 3h. The grab route takes `request_id`; `app.js` never sent one, and no test drove a request from approval to import through the screen. A request approved in the browser therefore never became *in your library*, and occupied one of its requester's twenty open requests for good | Fixed by ADR-0028: a request ends through the item it is linked to, whichever grab — or scan — brings the file. ADR-0017 carries an addendum saying so |

#### Verified

On 4p's instance, against the live TMDB API and the same Torznab indexer, as two
people — the administrator and **sam**, a User invited for the purpose. sam asked
for five titles, typed as a person would (`Amelie`, no accent); all five were
approved.

In real Chromium, as the administrator: every approved row said *approved — not
in the library yet* and offered **Add to the library…**. For *Blade Runner 2049*
the Add screen opened with *Adding for sam’s request for the film “Blade Runner
2049” (2017)*, the form filled in and the provider already asked; adding it said
*Added Blade Runner 2049 (2017). It is on the Wanted list. Linked to Blade Runner
2049 (2017)…* in one sentence, and the banner went. *Arrival*, *Star Wars* and
*Amélie* were already in the library: each refusal offered **Open in the
library** and **Link the request to it** once, and linking *Star Wars* — already
on disk — said *which is already on disk, so the request is fulfilled*. *Andor*
was added as a series, with its monitoring choice. Leaving the Add screen, or
**Not for the request**, ended the errand. As sam, the same five rows said what
each had become, with no buttons. Zero console errors, zero CSP violations.

Then through the API:

```
Amélie        film search from its own page → grab (no request named) → import       → fulfilled, by system:import
Blade Runner  a file put in its folder by hand → the hourly scan task                 → "1 request(s) fulfilled by what is now on disk"
Star Wars     already on disk when linked                                             → fulfilled on linking, by the administrator
Arrival, Andor  linked, nothing on disk                                               → approved, "in the library as …"
sam links a request                                                                   403
a pending request · a series for a film request · an item that does not exist         409 · 400 · 422
audit         five request.linked; request.fulfilled by jacob, by system:import, by system:scan
```

| Claim | How it is established |
|---|---|
| An approved request is linked to an item, audited, and not fulfilled while nothing is on disk | `request.TestAnApprovedRequestIsLinkedToTheItemThatSatisfiesIt`, `api.TestAnApprovedRequestIsAddedToTheLibraryAndFulfilledWhenItsFilmArrives` — mutation-verified |
| Only an approved request, only to an item of its kind, and a refused link changes nothing | `request.TestOnlyAnApprovedRequestCanBeLinked`, `request.TestARequestIsLinkedOnlyToAnItemOfItsKind`, `api.TestALinkThatCannotBeRightIsRefused` — mutation-verified |
| Linking is an approver's alone | `request.TestLinkingARequestNeedsThePermissionToApprove`, `api.TestLinkingARequestIsTheApproversAlone` — mutation-verified |
| A link can be corrected, and the audit says what it replaced | `request.TestALinkCanBeChangedWhileTheRequestIsOpen` — mutation-verified |
| An import into the linked item fulfils it, whichever grab brought the file; a grab that named the request still does, and records where the download went | `request.TestAnImportIntoTheLinkedItemFulfilsTheRequest`, `request.TestAGrabThatNamedTheRequestStillFulfilsItByItsHash` — mutation-verified |
| An item already on disk fulfils the request when it is linked, with the approver on record | `request.TestLinkingAnItemAlreadyOnDiskFulfilsTheRequestAtOnce` — mutation-verified |
| A file a scan records fulfils the linked request, through the scheduled task's own function, and a failure there does not fail the scan | `request.TestAFileAScanRecordedFulfilsTheLinkedRequest`, `main.TestAScanThatFindsALinkedTitlesFileFulfilsTheRequest`, `main.TestAScanDoesNotFailBecauseItsRequestsCouldNotBeClosed` — mutation-verified |
| Deleting the linked item leaves the request approved and unlinked | `request.TestDeletingTheLinkedItemUnlinksTheRequest` |
| A refused add says which conflict, so a request is offered only the title already there | `api.TestARequestCanBeLinkedToATitleAlreadyInTheLibrary`, `api.TestAnAddRefusedForItsFolderSaysSoInAWord` — mutation-verified |
| The linked item is named only to somebody who may browse | `api.TestARequestNamesItsItemOnlyToSomebodyWhoMayBrowse`, `api.TestALinkNamesTheItemOnlyToAnApproverWhoMayBrowse` — mutation-verified |

Thirty mutations, all killed. One candidate was left out as equivalent:
dropping `media_item_id IS NOT NULL` from the scan's query changes nothing,
because an unlinked request's `EXISTS` has no item to find a file for.

**Not built:** withdrawing or closing an approved request that will never be
acquired (ADR-0017); notifying the requester; a request for a series says
nothing about which episodes, so it is fulfilled by the first file.

---

### Increment 4r — the gates that read the code pass ✅

The jobs in CI that read the code rather than run it were all red, and one could
not have been green: `golangci-lint-action@v6` refuses golangci-lint v2, and
`.golangci.yml` is a version-2 configuration. 4n recorded the lint findings and
left them for "a cleanup with its own review". This is that review, and it
reached the other two gates on the way: **201 lint findings and 24 SAST findings
are 0 and 0**, with both scanners pinned to the releases that say so; the
dependency audit's **three reachable vulnerabilities are fixed**; and the secret
scan's **seven findings** — every one a made-up test password — are allowlisted
by value. `make lint` and `make security` run the same pinned versions CI does.

**Two files have to be copied in by hand.** The connection to the operator's
machine refuses writes to `.github/workflows/` and to the `Makefile`, so both
travel as attachments: the new `ci.yml` — the lint action at v9, golangci-lint
pinned to v2.14.0, gosec pinned to v2.29.0 instead of `master` — and the
`Makefile` with `make lint` and the same pins. Until `ci.yml` is copied in, the
lint job stays red on the action, not on the code.

Most findings were mechanical: a context for every request, query, dial and
subprocess (78 of them in tests); a `rows.Close` error ignored in so many words;
the underlying error wrapped where a sentinel already was (`%w: %w`); eight
staticcheck rewrites; and "Separacion", which is Severance's Spanish title in
test data. The point of the pass was the rest: every finding judged, and a
suppression only where the flagged thing is deliberate, with the reason on the
line.

#### Found along the way — defects the findings pointed at

| # | What | Status |
|---|---|---|
| 1 | **The lint job could never pass**: action v6 refuses golangci-lint v2 | `ci.yml` updated, as above — attached rather than synced |
| 2 | **Signup answered "request recorded" for any database failure**, not only for the duplicate address it exists to hide. A request lost to a locked database told its sender to wait for an approval that could never come | Only a uniqueness violation is masked (`db.IsUniqueViolation`, tested against the real driver: UNIQUE and PRIMARY KEY yes, FOREIGN KEY and NOT NULL no). Anything else is a failure |
| 3 | **A stored metadata credential that could not be read was treated as "no key"**, under a comment saying the caller logs an error it was never given | Returned; startup logs it and runs without a provider, as it did |
| 4 | **An unreadable trash folder was listed as empty** — so the purge would never free its space, and nothing would say why | Only a missing trash folder is empty; any other failure is an error |
| 5 | **The copy fallback checked a download's file through `os.Root`, then opened it again by host path** — a race the hardlink path closes by verifying the inode afterwards, and the copy did not | The copy reads through the download's contained source; `Vault.CopyFrom` takes a reader, never a path |
| 6 | **A torrent's re-verification error was discarded** | Returned. Not tested: it needs storage that fails a read mid-hash |
| 7 | **A clock before 1970 wrapped into a TOTP time step centuries away**, and a code for that step verified | No step before 1970: nothing verifies, no code is produced, nothing is consumed |
| 8 | **Break-glass recovery exited 0 when its summary could not be written** — after the account had been recovered | Fails, saying the recovery happened and was audited. Checked against the binary with its output on `/dev/full`: exit 1, the account awaiting enrollment, `user.recovered` in the log |
| 9 | **Three structural guards skipped any file they could not read** — only scheduling code mints a system principal, only the console recovers, only recovery clears an enrollment | A file a guard cannot read now fails it |
| 10 | **Three reachable vulnerabilities in the torrent library's dependencies** (`govulncheck`): a weak random source for WebSocket masks in `gorilla/websocket`, and a panic on a crafted message in each of `pion/dtls` and `pion/stun`. OpenTelemetry had one more, imported but not reached | All four modules at fixed releases (websocket 1.5.3, dtls 3.1.4, stun 3.1.5, OpenTelemetry 1.46.0); the audit reports none reached and none imported. The new `go.sum` lines were checked against the checksum database; `go mod verify` passes; the race suite, the engine's real loopback transfers among it, passes |
| 11 | **The secret scan would flag seven made-up test credentials** | `.gitleaks.toml` allowlists those values, not test files: a realistic secret planted in a test was still found |

Hardening with no defect behind it: SOCKS5 lengths and the destination port go
through checked conversions (both were range-checked already); a stored hash's
salt and key are bounded at 1 KiB; the artwork cache, and a download directory
the hardlink probe has to create, are made 0750, and the probe itself 0600.

What is still suppressed is deliberate, and SECURITY.md lists it: HMAC-SHA1 for
TOTP (RFC 6238's default, what authenticator apps implement); the session and
CSRF cookies' `Secure` flag coming from the configuration (true everywhere but
`http://localhost`), and the CSRF cookie readable by script by design; `ffprobe`
and `ffmpeg` started from the sandbox's allowlist; the operator's own `--config`
path; generated documents written 0644; two audit actions whose names contain
"token".

| Claim | How it is established |
|---|---|
| Signup hides a duplicate address and nothing else | `api.TestSignupIsNotAnEnumerationOracle`, `api.TestASignupThatCouldNotBeRecordedIsNotReportedAsRecorded`, `db.TestAUniqueViolationIsToldApartFromOtherFailures` — mutation-verified |
| A credential that cannot be read is reported, not taken for "none" | `metadata.TestLoadTellsAnUnsetKeyFromAnUnreadableOne` — mutation-verified |
| An unreadable trash is not an empty one | `library.TestAnUnreadableTrashIsNotAnEmptyOne` — mutation-verified |
| The copy fallback reads only inside the download | `importer.TestTheCopyFallbackReadsOnlyInsideTheDownload`, `library.TestASourceOpensOnlyWhatIsInsideIt` — mutation-verified |
| There is no TOTP step before 1970 | `identity.TestATimeBefore1970HasNoTOTPStep` — mutation-verified |
| A stored hash's salt and key are bounded | `identity.TestAHashWithAnOverlongSaltOrKeyIsMalformed` — mutation-verified |
| A recovery whose summary is lost fails, and says it happened | `main.TestARecoveryWhoseSummaryCannotBeWrittenSaysSo` — mutation-verified, and against the binary |

Ten mutations, all killed. golangci-lint v2.14.0: 0 issues. gosec v2.29.0 with
the SAST job's arguments: 0 issues, ten `#nosec`. `govulncheck`: 0 reached,
0 imported. gitleaks v8.30.1 on the working tree: 0. Full suite with `-race`.

### Increment 4s — the database is backed up, encrypted, and checked before it is kept ✅

Requirements §8 asks for encrypted, verifiable backups. What existed was a
primitive (`db.BackupTo`, a `VACUUM INTO`), an audit action nothing wrote, and
`POST /api/v1/admin/system/backup` answering 501. Nothing took a backup — and
the operator is about to start a library from nothing, which is when a backup
regime should begin. [ADR-0029](docs/adr/0029-encrypted-verified-backups.md)
was written first; building it changed four of its decisions, and the record
says which and why.

| What | How |
|---|---|
| **Taken on a clock restarts cannot reset** | `database.backup` runs hourly **and as the scheduler starts** (`tasks.Task.RunAtStart`, new), and takes a backup when the newest on disk is older than `backup.interval` (24 h). The disk remembers when the last one was; a ticker would forget at every restart. A newest backup dated from the future does not stop the schedule |
| **Checked before it is kept** | A `VACUUM INTO` a snapshot created `0600` *beside the database* — never in the backup directory, which may be a NAS — opened immutable (`db.OpenSnapshot`, new), and required to pass SQLite's `integrity_check`, its `foreign_key_check`, and a comparison of its migrations with this build's, number by number and checksum by checksum, none missing and no gap |
| **Encrypted with age** | `filippo.io/age` v1.3.2, a scrypt recipient at 2^15, the passphrase unpadded base64url of HKDF-SHA256(master key, `cmediastack backup v1`). One secret to keep; a passphrase only its holder can write a file for, so a planted backup cannot restore; decryptable by the standard `age` tool. The derivation is pinned to an answer computed by Python's `hmac` and by `openssl kdf` |
| **Confirmed after writing** | Encrypted into a file no listing counts, read back from disk — bytes must be what was written, and must decrypt, every chunk authenticated, to the checked snapshot — and only then renamed `cmediastack-YYYYMMDDTHHMMSS.mmmZ.db.age` (no colons: SMB). The audit log records the file, its size, schema version and SHA-256 |
| **Kept by age, above a floor** | Deleted after `backup.keep` (7 days) by the hourly check only — never one of the newest `backup.keep_min` (3), even while backups are failing. Taking backups deletes nothing. Only regular files named as backups are touched; a symlink or directory named like one is not |
| **The admin screen** | A Backups tab (`admin.system`): the policy in words, the next due, the backups newest first, *Back up now*, and a warning when they share the database's filesystem. No download, no restore — it says where those happen |
| **Two routes** | `POST /api/v1/admin/system/backup` (was 501) and `GET /api/v1/admin/system/backups` (new), hidden from everyone without `admin.system`; the first audited with who and from where. A second backup asked for while one runs is refused with 409, not queued |
| **On the host** | `-verify-backup FILE` decrypts into a private temporary directory, checks, reports what the backup holds and its SHA-256, and removes the plaintext — saying which of *not a backup*, *not this key's* or *damaged* it is when it fails. `-restore-backup FILE -restore-to NEW` writes a new file, refusing one that exists or has a `-wal`, `-shm` or `-journal` beside it, and a backup this build cannot read |
| **A restore is fenced** | Before it hands the database over it ends every session, revokes every API token and writes `system.backup.restored` into the restored audit log — or a session stolen and ended since the backup, or a token revoked since, would work again. It prints the steps to put the file in place, with the configured paths |
| **Configuration** | `backup.dir` (default `backups` beside the database; `CMS_BACKUP_DIR`), `interval`, `keep`, `keep_min`. The lint refuses an interval under an hour, a non-positive `keep`, and `keep_min` 0. **Only the default directory is created**: a named one that is missing is more often an unmounted share, and creating it would put the backups on the host's own disk |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **`db.Options.ReadOnly` could never have worked.** `Open` sets WAL mode — a write to the file's header — and a read-only connection refuses it, so it failed on any database not already in WAL mode, a backup's snapshot included. Its comment said it was "used by the backup verifier"; nothing used it | Removed. A file to be checked is opened with `db.OpenSnapshot` (immutable: no locks, no side files, no writes) |
| 2 | **The database path went into a SQLite URI unescaped.** A directory named with `#` or `?` made SQLite open — and create — a different file: everything before that character, in the parent directory. Found by probing the driver before relying on it: a database under `we ird#dir?x` created an empty `we ird` beside it and answered "no such table" | The path is escaped and made absolute (a relative one would be read as the URI's authority). `db.TestADatabaseUnderAnAwkwardPathIsThatFile`, `TestARelativeDatabasePathOpens` |
| 3 | **`VACUUM INTO` creates its file `0644` less the umask** — a plaintext copy of the whole database, readable by any user who can reach the directory | The snapshot is created empty and `0600` first; SQLite accepts an empty file and keeps its mode. Checked by a test that looks while the snapshot exists |
| 4 | **The ADR's first draft refused a backup directory inside a library root**, for no reason that survived being written down — and the NAS beside the media is one of the best places a backup can go | Dropped; the never-create rule addresses the real hazard |
| 5 | **The live database was created `0644` less the umask** — password hashes, sealed credentials, email addresses and the audit log, readable by every user on the host. Found by listing the built container's `/config`, where the new backups were `0600` beside a database any account could read | Created `0600`; SQLite gives the `-wal` and `-shm` the database's mode, so all three are private. One created before the fix is narrowed when next opened, best effort. `db.TestANewDatabaseIsReadableByItsOwnerAlone`, `TestAnExistingDatabaseOthersCouldReadIsNarrowed`; checked again in a fresh container |
| 6 | **"0 turns scheduled backups off" was false as written.** A bare `0` is not a duration to the YAML decoder, and a file saying `interval: 0` does not load — found by writing it into a file and starting the binary on it | Every place that says how now says `0s`; `config.TestTurningScheduledBackupsOffIsWrittenZeroS` pins both halves |

Two facts learned rather than defects: `VACUUM` rebuilds every index, so damage
to an index in the live database is repaired on the way into the backup (the
integrity tests therefore damage the snapshot itself); and a restored database
is in WAL mode, so opening it read-only with another tool creates `-wal` and
`-shm` beside it — the driver script did exactly that, and the restore correctly
refused the next target because of it. The runbook says to put the file in place
without opening it.

#### Verified against the running binary

The fresh3 instance (two accounts, nine library items, six requests) on the new
build:

- **At startup** the check found no backup and took one: 468 KiB, schema
  version 17, 219 ms. Directory `0700`, file `0600`, no snapshot left beside
  the database.
- **From the admin screen and the API** (20 checks): a backup taken, its
  answer's SHA-256 the file's own; three asked for at once gave `[201, 409,
  409]`; the user *sam* got 404 from both routes; no route serves a file or
  restores; the task triggered by hand reported the newest backup's age and was
  recorded as the person who triggered it; four copies planted with names 8–11
  days old were pruned (`deleted 4 older than 7 days; 3 kept`), a file that is
  not a backup left alone, and each deletion audited.
- **`-verify-backup`** reported the backup good — size, SHA-256 matching the
  audit record, schema, *2 accounts, 9 library items, 4 files, 6 requests* —
  and exited 1 with a reason for a wrong key (naming `CMS_MASTER_KEY`), a
  flipped byte (*damaged*) and the live database (*not an encrypted backup*).
- **The standard tool**: the distribution's `age` 1.1.1 decrypted a backup
  written by the library's 1.3.2, with the passphrase from the runbook's
  `openssl kdf` recipe — which agreed with the Python one — and Python's
  `sqlite3` found the result intact, schema 17.
- **A restore, put in place**: refused over the live database; written to a new
  `0600` file with nothing beside it; *15 sessions ended*; its newest audit line
  the restore. With the server stopped, the old database and its `-wal` and
  `-shm` moved aside and the restored file moved in: the session from before
  answered 404 instead of 200, signing in again worked, all nine library items
  were back, and the startup check found a recent backup and took none.
- **In Chromium**: the Backups tab states the policy and the next due, lists
  newest first with sizes, *Back up now* adds one at the top and says what it
  did, the page has no other control, and *sam* has no Backups tab.
- **In the built container**, with the shipped compose file: the startup check
  took a backup into `/config/backups` (`0700`, file `0600`, uid 65532);
  `docker compose exec … -verify-backup` and `-restore-backup` worked as the
  runbook writes them, and so did `docker compose run --rm …` with the server
  stopped; the runbook's put-in-place steps brought it back healthy, its
  startup check finding the backup a minute old; `TMPDIR=/config/tmp` moved
  the verification's plaintext and left the directory empty; a named
  `CMS_BACKUP_DIR` that did not exist failed the task with the unmounted-disk
  message. A fresh container created its database, `-wal` and `-shm` `0600`.

| Claim | How it is established |
|---|---|
| A backup is encrypted, decrypts with age itself, and is recorded with who took it and its hash | `backup.TestABackupIsEncryptedCheckedAndRecorded` — mutation-verified |
| The passphrase derivation cannot drift | `secrets.TestTheBackupPassphraseNeverChanges` — mutation-verified |
| A snapshot that fails its checks is not kept, and its plaintext is private while it exists and gone after | `backup.TestASnapshotThatFailsItsChecksIsNotKept`, `TestADatabaseWithABrokenReferenceIsNotBackedUpAsGood`, `TestABackupIsOfTheFullyMigratedDatabase` — mutation-verified |
| Only a file confirmed on disk is named a backup | `backup.TestConfirmCatchesAFileThatIsNotWhatWasWritten` — mutation-verified |
| The schedule is kept by the disk, not the process | `backup.TestTheHourlyCheckTakesABackupOnlyWhenOneIsDue`, `TestTheScheduleIsKeptByTheBackupsOnDisk`, `TestAFutureDatedBackupDoesNotStopTheSchedule`, `tasks.TestATaskCanRunAsTheSchedulerStarts` — mutation-verified |
| Pruning is by age, above a floor, only by the schedule, and touches nothing else | `backup.TestPruningGoesByAgeKeepsTheNewestAndTouchesNothingElse`, `TestAFailingScheduleKeepsItsLastBackups`, `TestTakingBackupsDeletesNothing` — mutation-verified |
| One backup at a time | `backup.TestOneBackupAtATime` — mutation-verified, and three at once against the binary |
| Hidden, and never served or restored over HTTP | `api.TestTheBackupRoutesAreHiddenFromEveryoneElse`, `api.TestManagerCannotReachAdminOnlyRoutes`, `api.TestNoRouteServesOrRestoresABackup`, `backup.TestOnlyAnAdministratorCanTakeOrListBackups` — mutation-verified |
| A backup that cannot be opened says why, and one demanding too much work is refused before doing it | `backup.TestABackupThatCannotBeOpenedSaysWhy` — mutation-verified |
| A restore writes a new file only, never beside a stale `-wal`, never leaving an unreadable or damaged backup behind, and fences what it restores | `backup.TestARestoreNeverWritesOverAnything`, `TestABackupFromANewerBuildIsNotRestored`, `TestABackupOfADamagedDatabaseIsNotRestored`, `TestARestoredDatabaseIsFencedBeforeItIsHandedOver`, `TestAnOlderBackupIsRestoredAndMigratedByTheServer` — mutation-verified |
| Verifying leaves no plaintext and changes nothing | `backup.TestAVerifiedBackupReportsWhatItHolds`, `TestVerifyingFencesNothing` — mutation-verified |
| A named directory is never created | `backup.TestANamedBackupDirectoryIsNeverCreated` — mutation-verified |
| Migrations are compared exactly | `db.TestCheckMigrations` — mutation-verified |

Thirty-eight mutations, all killed. golangci-lint v2.14.0: 0 issues. gosec
v2.29.0 as the SAST job runs it: 0 issues, eighteen `#nosec` — the eight new
ones are file opens: seven in the backup package, and the database's own
creation — each listed in SECURITY.md.
`govulncheck` with age and its one new dependency, `filippo.io/hpke`: 0
reached, 0 imported; `go mod verify` passes. gitleaks: 0.

What it does not do, on purpose or not yet, is in ADR-0029's *Known
limitations*: the default directory shares the database's disk; a restore rolls
back suspensions and password changes (it says so); there is no point-in-time
recovery; and master-key rotation, when built, must keep the old key for old
backups.

---

### Increment 4t — the metadata key and the tunnel get a screen each ✅

Writing the runbook meant writing "open the Metadata screen", and there was no
Metadata screen — though the error a title search returns without a key already
told operators to set one "on the Metadata screen". The provider key could be set
only with `curl` and an API token; the tunnel's health, and the leak test, could
be read only the same way. For a NordVPN deployment that is the one status an
operator most needs to see.

| What | How |
|---|---|
| **Metadata** tab (`admin.system`) | The provider and whether it works, in words; *Save and check* (the key is checked against TMDB before it is stored, as before); *Check now*; *Remove the key*. The field is emptied the moment it is submitted — accepted or refused — and the key never comes back: the API has never returned it, and the page never holds it |
| **Network** tab (`admin.network`) | Whether a tunnel is required; when it is, whether it is up; every subsystem's route and whether it pauses without the tunnel; *Run the leak test*, with its caveat that routing is not proof of the absence of a leak. Nothing on it changes the policy, which stays configuration (PATCH answers 409, as before) |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **A key loaded at startup read as broken.** Until something asked the provider, its health was `ok: false` with nothing to say, which the new screen showed as *not working* — about a key that worked | The status says `checked: false` and that the key is stored and not checked since the server started; the screen shows *not checked yet*. `api.TestAKeyNotYetCheckedSaysSoRatherThanBroken` — mutation-verified |
| 2 | **The screen's first draft said "Tunnel wg0 — up" with enforcement off**, because the guard reports healthy when nothing is required of it — the exact reassurance `EgressStatus`'s own comment exists to prevent | The tunnel is named and judged only when enforced; otherwise *No tunnel is required — not enforced*, beside the warning in words |
| 3 | A blocked route read "private addresses allowed" | *Nothing leaves by this route* |

Verified in Chromium on the running binary, with the real provider: a key
loaded at startup shows *not checked yet*, *Check now* makes it *working*; a
bogus key is refused with *Nothing was stored* and the working key stays in
use; *Remove the key* leaves *No provider*; the real key goes back in, stored
and verified; the field is empty after every submission and the key appears
nowhere in the page. Network, unenforced: the warning and *No tunnel is
required*, six subsystems, *update* blocked; the leak test answers *Does NOT
route through the tunnel* with its caveat. Network on a second instance that
enforces a tunnel it does not have: *Tunnel wg0 — down*, every subsystem but
the exempt *notification* marked as pausing. A user sees neither tab.

---

### Increment 4u — the operator runbook ✅

[`docs/RUNBOOK.md`](docs/RUNBOOK.md): the deployment this was specified for,
start to finish, and what to do on the days after.

| Section | What it covers |
|---|---|
| The host | A VM rather than an LXC container, and why (Proxmox's own advice, and the parser jail's namespaces); sizing for the i5-6500T — no GPU, since nothing transcodes video; the NAS mount, writable by uid 65532; downloads on the library's filesystem so imports hardlink |
| First run | The master key kept off the host; **the wizard finished over an SSH tunnel before anything is public**, because whoever reaches `/setup` first is the administrator; the four settings that matter before going public |
| Reverse proxy | Caddy; `CMS_BASE_URL` and `CMS_TRUSTED_PROXIES`, which is the bridge gateway, not loopback — and how to check it worked from the Sessions screen |
| NordVPN | The topology that works today — the whole application in gluetun's namespace — its compose and settings, finding the tunnel's interface rather than assuming `wg0`, the checks to run before trusting it, and restarting the application after gluetun's container restarts |
| Backups | Where they go, checking one, restoring and putting it in place, and decrypting one with `age` when this software is gone |
| Upgrades | A backup first, and why the rollback needs the old image |
| Watching, and failures | The screens and logs to read, the audit log until it has a screen, metrics, and a table of symptoms to causes |

**What was run, and what was not.** Every backup command in it was run as
written against the built container (4s), and the `age` recipe against a real
backup. The NordVPN section has **not** been run against a real NordVPN
account — the containment was tested in 4j against a stand-in with gluetun's
topology — and it says so at its top, with the checks to run first. The Proxmox,
Caddy and NFS steps are standard practice, written down rather than exercised
here.

Writing it made four things visible:

- **The first-run wizard is a race** until it is finished. Nothing in the
  documents said so; SECURITY.md now does, under *The anonymous surface* and
  *Operator responsibilities*.
- **The audit log has no screen** (`GET /api/v1/admin/audit` is 501). The
  runbook reads it with `sqlite3` as a stopgap. *4w gave it one.*
- **If gluetun's container restarts, the application is stranded** in the old
  namespace — failing closed, unreachable, and passing its own health check.
  The runbook says to restart it; nothing does so automatically.
- **Metrics cannot be scraped from outside the container** without binding the
  management listener to a specific address; the compose file publishes none.

---

### Increment 4v — what is wanted, fetched without a person, within limits ✅

Asked for by the operator: "automatic". Everything acquired until now, a person
chose — ADR-0023 deferred automatic grabbing on purpose, and three records since
noted what it would need. [ADR-0030](docs/adr/0030-automatic-acquisition.md) was
written first; building it added five decisions to it, each recorded there.

| What | How |
|---|---|
| **Off until the configuration says on** | `acquisition.automatic` (and `CMS_ACQUISITION_AUTOMATIC`), false by default; the lint refuses it with the download engine off, a feed interval under 10 minutes, and a budget outside 1–50 searches or 1–100 grabs a pass |
| **The Wanted list, and nothing else** | Monitored episodes aired and not on disk; monitored films with no file. Films gained the **monitored switch** ADR-0026 rejected (migration 0018, `PUT /api/v1/media/{id}/monitored`, library editing), now that there is a search for it to stop. It never adds a title, never replaces a file, never takes a season pack. `acquire.Store.Wanted` is the screen's conditions written again without its display cap, and a differential test holds the two to one answer |
| **Two ways of finding** | `acquire.recent`: each enabled indexer asked once for its 100 most recent releases (an empty `t=search`), matched against the whole list through an index of folded names. `acquire.search`: three due items a pass, never-searched first and newest first, then the longest waiting — the same targeted search a person's button runs |
| **Judged as a person's search is, then stricter** | `search.MatchEpisode` / `MatchFilm`, the code the targeted searches run; accepted by the **default** profile (none → nothing fetched, and the task is red saying so). Then what a machine may not choose: no seeders; a release ever in the queue, by its hash in the feed or — when the feed carries none — worked out from what was fetched (`download.HashOf`); an item a download is under way for; a name that fits two titles in the library, or two wanted titles; a double episode unless both its episodes are wanted; a release named in a language — a dub, or a title not in English, which only a person can tell apart (MULTi and dual audio are not refused) |
| **What "in flight" means** | Queued, downloading, or finished and not imported — except a download the import **skipped** (the release was wrong: the item is wanted again, the release blocklisted). One the import **failed** (the disk, a root folder) stays in flight: another release would fail the same way. A download counts for every episode its name spans |
| **The budget** | Back-off after a fruitless search: 6 h, doubling to a week; an hour after a failed one, without lengthening the wait. A failed fetch left alone for six hours. Alternative titles cached a day, a failure an hour, at most 100 read a pass. At most 5 grabs a pass. The two passes never run at once; neither runs at start |
| **Not while the tunnel is down** | With egress enforced and the tunnel not verified, both passes stop before asking any indexer, and say so |
| **Its own authority** | `authz.TaskAcquire` — `system:acquire` — holds browse, interactive search and the queue, nothing else; a pass checks all three itself. Every grab is audited as `acquisition.grabbed` by `system:acquire` with no user id, and every failed attempt too; the queue says *automatic acquisition* |
| **A person still decides** | Wanted says beside every item what was done — downloading, grabbed, searched and why nothing ("1 of 2 results were Severance S02E10, and none could be grabbed: the default profile (HD-1080p) refuses it: quality WEBDL-2160p…"), a failed search — and when the next search is. *Unmonitor* on every Wanted row and on a film's page. The item is asked about again just before a grab, so an unmonitor during a pass wins. Removing a download says it blocklists the release and that the item is still wanted |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **The first definition of "in flight" freed an item whose import had failed.** The importer retries a failure every minute — a missing root folder, a full disk — so a pass would have grabbed another release of every such item, and another, each failing the same way | Only a *skipped* import frees an item. `acquire.TestWhatCountsAsInFlight`, `acquire.TestOneDownloadPerWantedItem` — mutation-verified |
| 2 | **`The.Office.S01E02` fits both series in a library that holds both**, and the first index picked whichever came first. A person sees two shows in the results; a machine does not | Refused when the name fits two titles in the library (own titles) or two wanted titles (alternative titles too). `acquire.TestANameThatFitsTwoTitlesIsGrabbedForNeither`, `TestANameTwoWantedTitlesAnswerToIsGrabbedForNeither` — mutation-verified |
| 3 | **A double episode grabbed for E01 left E02 wanted**, so the next pass would fetch E02 again | In flight covers every episode a queued release's name spans (bounded to 50); a double episode is taken only when every episode in it is wanted |
| 4 | **The answer to adding a film would have called it unmonitored** — `AddItem` and `UpsertItem` return the item they were given, not the row they wrote, and the row is monitored by default. Found reading the code the new field goes through | Both say `monitored` as the row has it. `api.TestAFilmIsUnmonitoredThroughTheAPI` checks the answer to the add |
| 5 | The runbook's first draft sent the operator to a Profiles screen that does not exist | The default profile is chosen under the Search form; the error, the runbook and the ADR say so |
| 6 | Two passes side by side could both pass their last check while a slow indexer served the torrent | They never run at once. `acquire.TestTheTwoPassesNeverGrabTwiceForOneItem` holds each fetch for 200 ms, so without the lock it fails every time rather than now and then — mutation-verified |
| 7 | **The release parser cut titles at any tag word, wherever it stood** — probing language tags for this increment: `The.French.Connection.1971` parsed as *The*, `Russian.Doll.S01E01` as nothing, `Uncut.Gems.2019` as nothing, `Charlottes.Web.2006` as *Charlottes*. No search, a person's or a machine's, could ever match those titles. In the parser since 2b | With a year or an episode marker in the name, the tags are read after it and a word before it is title. `release.TestATagWordBeforeTheYearIsPartOfTheTitle`; every existing parser test and the fuzzer pass — mutation-verified |
| 8 | **Nothing judged a release's language.** A French dub of *Dune* ranks with the original, and a title TMDB also lists in German matches a German release | A release named in a language is refused automatically, and stays a person's choice. `acquire.TestAReleaseNamedInALanguageIsAPersonsChoice` — mutation-verified |

#### Verified against the running binary

On fresh3 — the library built over the last increments, now migrated to 18 —
with a stand-in indexer whose feed a script rewrites mid-run: 21 episodes and
2 films wanted, nothing searched. **Recent releases**: one request, an empty
`t=search` with `limit=100`; *Slow Horses* S06E02, a *Severance*
S01E01E02 and *Arrival (2016)* grabbed and nothing else fetched; a dead release
and a 2160p one refused, each with its reason. The three finished, were
imported where they were grabbed for — the double episode as E01–E02 — and left
the Wanted list. **Search**: three of nineteen due, the unreleased film first
(nothing found, back in six hours), then the newest episode, then the next;
the second pass took the next three. **Unmonitor** a film: off the list, reads
back unmonitored. **The blocklist**: a release that appeared was grabbed and
removed by a person; the next pass refused it without fetching it; published
again without its hash, it was fetched once, recognised and not queued, and
another release was grabbed. Five `acquisition.grabbed` lines by
`system:acquire`, none naming a person or a link. **In Chromium**: the Wanted
screen's sentence and every item's line, *Unmonitor* from Wanted and a film's
*Monitor* and *Unmonitor*, *Added by automatic acquisition* in the queue, and a
user who sees the lines and no switches — no CSP violations, no page errors.
**The kill switch**: a copy of the instance enforcing a tunnel it does not have
— both passes said *stopped before asking any indexer* and the indexer saw
nothing.

| Claim | How it is established |
|---|---|
| It acts on what the Wanted screen lists, and a person's change during a pass wins | `acquire.TestTheWantedListIsTheWantedScreens`, `acquire.TestAPersonsChangeDuringAPassWins` — mutation-verified |
| It grabs a matching release from the feed, as a person's grab is made, and on the record | `acquire.TestARecentReleaseOfAWantedEpisodeIsGrabbed` — mutation-verified |
| It refuses what a machine may not choose | `acquire.TestDeadAndRefusedReleasesAreNotGrabbed`, `TestTheQueueIsTheBlocklist`, `TestOneDownloadPerWantedItem`, `TestADoubleEpisodeIsGrabbedOnceAndHoldsBoth`, `TestANameThatFitsTwoTitlesIsGrabbedForNeither`, `TestANameTwoWantedTitlesAnswerToIsGrabbedForNeither`, `TestAReleaseNamedInALanguageIsAPersonsChoice` — mutation-verified |
| A tag word before the year or the episode is title | `release.TestATagWordBeforeTheYearIsPartOfTheTitle` — mutation-verified |
| Its budget holds | `acquire.TestTheSearchPassIsBudgetedAndBacksOff`, `TestBackoffDoublesToAWeek`, `TestAFailedSearchIsRetriedInAnHour`, `TestAFailedFetchIsNotRepeatedEveryPass`, `TestAPassGrabsNoMoreThanItsLimit`, `TestAlternativeTitlesAreUsedCachedAndBudgeted` — mutation-verified |
| It stops with no default profile, with the tunnel down, and for anyone but its task | `acquire.TestNoDefaultProfileMeansNothingIsFetched`, `TestAClosedGateAsksNoIndexer`, `TestAPassNeedsTheTasksAuthority`, `authz.TestEachBackgroundTaskHoldsExactlyItsOwnGrant` — mutation-verified |
| The two passes never both grab | `acquire.TestTheTwoPassesNeverGrabTwiceForOneItem` — mutation-verified |
| The screens say what it did | `api.TestTheWantedScreenSaysWhatAutomaticAcquisitionIsDoing`, `TestTheWantedScreenSaysWhenAutomaticAcquisitionIsOff`, `TestTheQueueSaysWhichDownloadsAreAutomatic` — mutation-verified |
| A film can be kept without being wanted, by whoever may edit the library | `importer.TestAFilmCanBeKeptWithoutBeingWanted`, `api.TestAFilmCanBeKeptWithoutBeingWanted`, `api.TestAFilmIsUnmonitoredThroughTheAPI` — mutation-verified |
| Off by default, and refused when it could not work | `config.TestAutomaticAcquisitionIsOffByDefault`, `config.TestAutomaticAcquisitionSettingsThatCannotWorkAreRefused` — mutation-verified |

Forty-five mutations, all killed — five needed rewriting to compile, and two
survived the first run: a test that checked an upper-case info hash made only
of digits, and one that could not tell a double episode's second half being
skipped from being refused. Both tests were fixed; both mutants now die.

What it does not do is in ADR-0030's *Known limitations*: season packs, scene
numbering, names TMDB does not list, a namesake that is not in the library, a
download that never finishes (it holds its item until removed), and a person's
grab from the general search, which carries no target and so does not count as
in flight.

---

### Increment 4w — the audit log gets a screen, and anonymous noise a ceiling ✅

Proposed after 4v: the runbook sent the operator to `sqlite3`, and SECURITY.md
asked them to watch a log nothing could read. [ADR-0031](docs/adr/0031-reading-the-audit-log.md)
was written first; building it added the second half of its title.

| What | How |
|---|---|
| **The administrator reads it** | `admin.audit`, which only Admin holds. Both routes are hidden — 404 to everyone else, and each refusal is a line of its own — and the data layer checks the permission again. A token scoped to `admin.audit` reads it too: that is how a log is shipped elsewhere |
| **Every column, newest first** | `GET /api/v1/admin/audit`: when, who (and a person's id), what, the outcome, the target, the address, the user agent, the detail, and before and after as the JSON they were written as. 100 a page, 200 at most, continued by id (`before=` the last page's `next_before`) — not by offset, which repeats or skips a line while the log grows |
| **Filters, strictly** | The kind (an action's first word), the action, the outcome, who, the target, a time or date range, and text matched as itself in the detail, target, actor, address and user agent. An unknown parameter, one given twice, a limit out of range, an action of the wrong shape, a range that ends before it starts: each a 400 saying what is wrong, never a page of everything that looks filtered |
| **The week at a glance** | `GET /api/v1/admin/audit/summary`: counts by action and outcome and by kind over 7 days (up to 90), and the ceiling — the screen's first line |
| **Reading is not audited** | Only the administrator reads it, and a line for every page viewed would bury what the log is for |
| **The ceiling** | Anybody can make the instance write an `authz.denied` line: ask for a protected page without a session. Now written one by one up to 20 an hour from one address and 120 from all addresses together; a signed-in account's up to 60, counted apart, so a flood can neither hide an account's denials nor lift its ceiling; 4,096 of each told apart an hour, at most. The rest are counted, and one `authz.denied.suppressed` line by `system:audit` says how many and from where — written by the next denial after the hour, by the `audit.denials` task within five minutes of it, or by a server that is stopping. `cms_authz_denials_total` still counts every denial |
| **The screen** | *Audit log*, before *Tasks*: the week's counts, a page of lines with *Older*, menus for the kind and the outcome, boxes for who and for text; each line's user agent and before and after under *More*. All of it goes in as text |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **Any stranger could write to the audit log without limit** — a row for every request for a protected route, with its address and user agent. Ten requests a second is 864,000 rows a day, carried by every backup, and all a screen of the newest lines would show. So since 1a; reading the log is what made it visible | The ceiling (ADR-0031, decision 4). `audit.TestOneAddressAddsAtMostItsCeilingAnHour`, `TestEveryAddressTogetherHasACeiling`, `api.TestAnAnonymousFloodIsCappedAndCounted` — mutation-verified |
| 2 | **The first ceiling counted accounts and addresses in one bounded table**, so a flood from 4,096 addresses — an IPv6 prefix holds far more — left accounts untracked, and an untracked account had no ceiling at all | Counted apart. `audit.TestAnAccountIsNotHiddenByAFlood` floods past the bound first — mutation-verified |
| 3 | The first summary ended its window at the wall clock while every row is stamped by the logger's | The window ends at the logger's clock. `audit.TestTheSummaryCountsByActionAndOutcome`, `api.TestTheAuditSummaryCounts` — mutation-verified |
| 4 | **A stop that ran out of time would have lost the hour's count**: the first shutdown code returned before writing it, and with its deadline spent. And Docker kills a container ten seconds after asking it to stop — before the application's own twenty-second grace was up | The count is written whatever the shutdown did, under a deadline of its own; the compose file gives the container 30 seconds. `config.TestTheContainerIsGivenLongerToStopThanTheApplicationTakes` — mutation-verified |
| 5 | Search boxes drew as the browser's own — `input[type=search]` was missing from the shared rule, the library's filter included. Seen in the screenshot, not by any assertion | Styled with the other inputs |
| 6 | **The count's line misdated itself**: the first live run's said *the 43 minutes from 19:00* for a process that had started at 19:41 — the hour's top, not when the counting began — and a stop after an hour had ended would have called that hour cut short | It says when the counting began and when it ended: *from 20:05 UTC until the server stopped at 20:05*, *from 14:05 to 15:00 UTC*. `audit.TestAStoppingServerWritesTheHourSoFar`, `TestEveryAddressTogetherHasACeiling` — mutation-verified |

#### Verified against the running binary

On fresh3, restarted onto the new binary, over the lines the earlier increments
wrote. **The flood**: thirty anonymous requests for `/api/v1/admin/users` from one
address — every one 404, twenty written, each with the address, the user agent
and the route, and the database holding exactly those twenty. A user got 404 from
both routes, and both refusals were written as theirs. Eight malformed filters
and a 91-day summary: 400 each, saying why. Two pages of five continued by id
without a repeat; `category=acquisition` and `actor=system:acquire` held only
what they said, over 4v's grabs; a stranger's user agent carrying markup came back
byte for byte. A token scoped to `admin.audit` read the log, got 404 from
`/admin/users`, and once revoked read nothing. **Stopped** with SIGTERM: the log
said it wrote the count, and one line by `system:audit` read *11 denial(s) from
2026-09-27 20:05 UTC until the server stopped at 20:05 … 127.0.0.1 ×11* — the
flood's ten, and the revoked token's request, which is anonymous. The
`audit.denials` task run by hand wrote nothing more. **In Chromium**: the week's
counts, a hundred lines newest first, *Older* adding the rest without a repeat,
*Denials* showing only `authz` lines with the count among them, the stranger's
user agent shown as the text it was with none of it markup, *Nothing matches
that*, and a user with no tab and a 404 — no CSP violations, no page errors.
**In the container** — the 4w binary in the image 4s built, run by
`docker-compose.yml` as written: thirty anonymous requests through the published
port, then `docker compose stop` — exit 0 in a quarter of a second, and the count
written: *10 denial(s) … 172.18.0.1 ×10*. That address is the bridge's gateway,
which is what every visitor looks like until `CMS_TRUSTED_PROXIES` is set
(RUNBOOK §3).

| Claim | How it is established |
|---|---|
| Only the administrator — or a token they scoped to it — reads the log, and its routes are invisible to everyone else | `api.TestTheAuditLogIsHiddenFromEveryoneElse`, `api.TestManagerCannotReachAdminOnlyRoutes`, `audit.TestReadingNeedsThePermission` — mutation-verified |
| Pages are newest first, continue by id, never exceed 200, and neither repeat nor skip while the log grows | `audit.TestPagesContinueByIDWithoutRepeatingOrSkipping`, `api.TestTheAuditLogReadsNewestFirstAndInPages` — mutation-verified |
| Filters select what they say; text matches itself; a kind is a whole word | `audit.TestFiltersSelectWhatTheySay`, `api.TestAuditFiltersSelectOverHTTP` — mutation-verified |
| A filter that cannot be is refused | `audit.TestABadFilterIsRefused`, `api.TestAnAuditFilterThatCannotBeIsRefused` — mutation-verified |
| Before and after come back as the JSON they were written as | `audit.TestBeforeAndAfterComeBackAsWritten` — mutation-verified |
| The ceiling holds — per address, for every address, per account, bounded, under concurrency — and what is over it is one line, also when the server stops | `audit.TestOneAddressAddsAtMostItsCeilingAnHour`, `TestEveryAddressTogetherHasACeiling`, `TestAnAccountIsNotHiddenByAFlood`, `TestTheCountingIsBounded`, `TestTheCeilingHoldsUnderConcurrency`, `TestAStoppingServerWritesTheHourSoFar`, `api.TestAnAnonymousFloodIsCappedAndCounted` — mutation-verified |
| Every kind of line can be filtered for, on the screen as in the API | `audit.TestEveryActionHasACategoryTheScreenOffers`, `web.TestTheAuditScreenOffersEveryCategory` — mutation-verified |

Forty-four mutations, all killed — three needed rewriting to compile.

What it does not do is in ADR-0031: no export (a CSV would carry strangers'
user agents into a spreadsheet as formulas) and no trimming, which would be a
decision of its own; an unended hour's count is lost if the process dies; the
ceiling is per process; addresses are as the proxy settings resolve them; and
text search reads every row the other filters leave.

---

### Increment 4x — notifications, to one Discord webhook ✅

§13 asked for Discord notifications, later; ADR-0013 had reserved an egress
profile for them, exempt from the kill switch. [ADR-0032](docs/adr/0032-notifications-to-discord.md)
was written first.

| What | How |
|---|---|
| **One Discord webhook** | Set on the new *Notifications* screen. Discord is asked about it — a `GET`, which posts nothing — before it is kept; stored sealed in the `setting` table, bound to its own context; never returned, not even masked. Only `https` links to Discord's hosts, in the shape Discord hands them out. Sent through the `notification` egress profile; a redirect is followed only within Discord |
| **Read from the audit log** | `notify.discord`, every minute, from a cursor in the `setting` table. It runs as `system:notify`, which holds `admin.audit` and nothing else. The first webhook starts from the newest line; the history is not sent. Configuring it needs `admin.audit` as well as `admin.system` |
| **Chosen by category** | Security, accounts and operations on; library and requests off, because they send titles. The table in ADR-0032 is `notify.catalog`, line for line. Successful sign-ins are never sent |
| **Tasks that start failing, and recover** | From the scheduler, in memory: the first failure after a success, the first success after failures — *The tunnel is down — transfers are paused*, *Backups are failing* — never each repeat |
| **A file arriving is audited** | `media.imported`, by `system:import` or the person who imported: the one change to the library the log did not record, and the library event most worth hearing about |
| **A stranger's text stays text** | Every value from the log in a code span: backticks replaced, control and bidirectional-override characters removed, clipped. Every message has mentions off and embeds suppressed. Times are Discord's own tags, in the reader's timezone; no links to the instance |
| **Bounded, at least once** | At most three messages a run under Discord's 2,000 characters, counted in UTF-16 units; identical lines one with a count; what does not fit counted in a closing line; lines a day old counted, not listed. The cursor moves after Discord accepts. A 429 is waited out; a webhook Discord no longer knows stops delivery until it is replaced; a message refused as malformed is passed over |
| **Leaving a channel is said in it** | Turning notifications off, or pointing them at another webhook, posts to the channel being left: who did it, and what the new webhook calls itself |
| **The screen** | *Notifications*, after *Metadata*: where it posts, when it last sent, what is going wrong; the link's field, emptied as soon as it is submitted; each category with what it sends, and a warning on the two that send titles; *Send a test message* (five a minute); *Remove the webhook* |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **Files arriving in the library were not audited.** Additions, deletions and restores were; the import — the change a household most wants to hear about — was not | `media.imported`. `importer.TestAnArrivalIsAudited` — mutation-verified |
| 2 | **The guard against dialling around the egress guard skipped any listed package it could not find**, so a misspelt entry guarded nothing and passed | A missing package fails unless it is named as not yet built. `egress.TestNoPackageDialsDirectly` — mutation-verified with a misspelling |
| 3 | **Removing the webhook would have silenced the channel unseen** — the removal is audited, but an audit line cannot reach a channel that is no longer configured. Found writing the threat model's paragraph on a stolen administrator session | The channel being left is told, with who. `notify.TestLeavingAChannelIsSaidInIt` — mutation-verified |
| 4 | The test-message limit counted attempts refused before Discord was asked (no webhook set) | Only a test that reaches Discord counts. `api.TestCategoriesAreCheckedAndTestsLimited` |
| 5 | A failing task's message is sent as the task wrote it — the live run's said `requesting http://127.0.0.1:18283/api?apikey=REDACTED…`: the indexer's key redacted, its address not | Sent as is — it is what the operator needs — and said: ADR-0032's limitations, SECURITY.md |
| 6 | A checked box read *Library (off unless chosen)* | *(off by default)* |

#### Verified against the running binary

**Against Discord itself**: a webhook link Discord does not know — one `GET`
through the `notification` profile, *Unknown Webhook*, 400, nothing stored; a
look-alike host refused before any request; the link in neither the server's
log nor the audit log. **End to end against a stand-in** — `discord.com`
pointed at a local TLS server whose CA only the instance trusted: the webhook
stored after a `GET`, nothing posted; the first run sent only its own
setting's line; every message JSON, mentions off, embeds suppressed; a
stranger's signup arrived as `account_request newcomer…` with neither the email
nor the address; the stand-in indexer stopped — *A scheduled task is failing ·
`acquire.recent`* — and started — *working again … failing since*, once; with
the library on, a release published to the indexer was grabbed by automatic
acquisition, downloaded, imported, and both *Grabbed* and *Arrived in the
library · `system:import` → `media_item 6`: `Severance (2022) S02E07,
WEBDL-1080p`* reached the channel; a 429 waited out, and a run inside the wait
asked nothing; a 404 stopped delivery with no further requests until the
webhook was replaced, and then everything waiting arrived; a 400 passed over;
turning notifications off said so in the channel, naming `jacob`. **In
Chromium**: where it posts and when it last sent, the categories with the
title warnings, a test message that reached the stand-in, a bad link refused
with the field emptied, the link nowhere in the page, and a user with no tab
and a 404 — no CSP violations, no page errors.

| Claim | How it is established |
|---|---|
| Only a Discord webhook link is kept, after Discord confirms it, sealed, and it never comes back — in an answer, an error, the audit log or a printed value | `notify.TestOnlyADiscordWebhookLinkIsAccepted`, `TestAWebhookIsCheckedSealedAndNeverWrittenDown`, `TestAWebhookNeverPrintsItsToken`, `TestDiscordsAnswersAreToldApart`, `TestARedirectIsFollowedOnlyWithinDiscord`, `api.TestTheWebhookLinkNeverComesBack`, `api.TestAWebhookThatCannotBeUsedIsNotStored` — mutation-verified |
| Only an administrator configures it, with the audit-log permission as well; the routes are invisible to everyone else; the task can read the log and nothing more | `notify.TestOnlyAnAdministratorConfiguresIt`, `api.TestNotificationsAreHiddenFromEveryoneElse`, `api.TestManagerCannotReachAdminOnlyRoutes`, `authz.TestEachBackgroundTaskHoldsExactlyItsOwnGrant` — mutation-verified |
| What is sent is what the categories say, from where the log stood when the webhook was set | `notify.TestTheHistoryIsNotSent`, `TestWhatIsSentFollowsTheCategories`, `api.TestWhatIsAuditedReachesTheChannel` — mutation-verified |
| A stranger's text cannot ping, link or hide | `notify.TestAStrangersTextCannotBecomeMarkup`, `TestAMessageSwitchesMentionsOffAndSuppressesEmbeds`, `TestAStrangersNameCannotPingOrPassForALink` — mutation-verified |
| Delivery is bounded, retried, and obeys Discord | `notify.TestDeliveryIsBoundedAndCounted`, `TestMessagesKeepToDiscordsLimit`, `TestFailuresAreRetriedRefusalsPassedOverWaitsObeyed`, `TestAWebhookDiscordForgetsStopsDelivery`, `TestOldLinesAreCountedNotListed`, `TestTestMessagesAreLimited` — mutation-verified |
| A task is reported when it starts failing and when it recovers, once each | `notify.TestATaskIsReportedWhenItStartsFailingAndWhenItRecovers` — mutation-verified |
| Leaving a channel is said in it; removing stops everything | `notify.TestLeavingAChannelIsSaidInIt`, `TestRemovingTheWebhookStopsEverything`, `TestADamagedLinkIsReportedAndReplaceable` — mutation-verified |
| An arrival is audited, and the log can be read on from a cursor | `importer.TestAnArrivalIsAudited`, `audit.TestAfterCarriesOnOldestFirst` — mutation-verified |
| The notifier cannot dial around the egress guard | `egress.TestNoPackageDialsDirectly` — mutation-verified |

Fifty-five mutations, all killed — two needed rewriting to compile.

What it does not do is in ADR-0032: another transport, a message to one person,
a channel per category, links or embeds. With the whole application inside
gluetun's namespace, *the tunnel is down* cannot leave until the tunnel is back;
a task's failure noted in memory is lost if the process stops before it is sent;
and Discord sees what is sent.

---

### Increment 4y — season packs: a season at once, each file judged on its own ✅

The operator is starting a library from nothing, and older seasons are often
released only as packs; ADR-0030 listed them first among what automatic
acquisition could not do. [ADR-0033](docs/adr/0033-season-packs.md) was written
first.

| What | How |
|---|---|
| **A third kind of target** | A grab is sealed to an episode, a film, or now a **season** — the series and the season, no episode — through the ticket, the queue (`target_kind = 'season'`; migration 0019 rebuilds the column, whose CHECK SQLite cannot widen in place) and the import. Each shape is refused as a mixture everywhere it is read |
| **What is a pack of the season** | `search.MatchSeasonPack`: the series by any of its titles, a year within one, one season, no episode, no air date. A name covering **several seasons** — `S01-S03`, `Seasons 1-3`, `S01.S02`, `Complete.Series` — is refused (`release.NamesSeveralSeasons`), because the parser reads the first season of `S01-S03` and would have taken it for season 1 |
| **A person: Search season** | `POST /api/v1/media/{id}/seasons/{season}/search`, and a *Search season* button beside a season with anything missing. One search for the season: a pack of it gets a ticket sealed to the season, a single episode of it one sealed to that episode, the rest their reasons |
| **Importing a pack** | Every playable file, by the same allowlist, sample and size rules — not *largest wins*. Each file numbered by **its own name**: `S02E03`, or a bare `E03`/`Episode 3` given the pack's season; `03 - Title.mkv` is skipped, since a bare number is how absolute numbering is written. An episode the provider does not list is skipped; **two files claiming one episode are both skipped**. Each file is then the single-episode import — `importFile`, factored out so the two cannot drift: never replacing a better file, superseding to the trash, hardlinked. A subtitle goes only with the file whose name it begins with |
| **Records and retrying** | One `import_record` per file. A pack is retried at once while any file is failing — only the files not yet imported — after an hour when only skips remain, never once all arrived |
| **Automatic acquisition** | A pack only for a **settled** season — every listed episode aired, as many listed as the provider said, the last more than a week ago; never season 0 — **every episode of which is wanted and none downloading**. Such a season is searched once as a season: a pack first, then its episodes from the same results; its state is recorded for every episode. The recent-release pass matches packs against the same seasons. Before a pack is queued its `.torrent` is read and its file list put through the importer's own planner: every wanted episode must be there, or it is refused and remembered. A magnet-only pack is left to a person |
| **In flight** | A season's download holds every episode of the season until every file of it has been imported or skipped |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **`Show.S01-S03` parses as a pack of season 1.** The parser stops at the first season marker; a three-season pack would have been grabbed for season 1 and imported as a third of itself | `release.NamesSeveralSeasons`, asked before anything else. `release.TestSeveralSeasonsAreRecognised`, `search.TestASeasonPackIsOnlyItsOwnSeason` — mutation-verified |
| 2 | **A single download's subtitle rule would give every episode of a pack every subtitle in its folder** — "beside the video" is every file of the season | A pack's subtitle belongs to the file whose name it begins with, or `Subs/<that name>/`. `importer.TestAPacksSubtitlesGoWithTheirOwnEpisode` — mutation-verified |
| 3 | **"Anything imported means done" would never retry a pack's failed file** — nine episodes in and one refused by a full disk, forever | Per-file judgement for packs. `importer.TestAPackIsRetriedOnlyForWhatFailed` — mutation-verified |
| 4 | **A repeated skip was compared with the download's last row**, which in a pack is usually another file's, so every retry added a row per file | Compared with the same file's last row. `importer.TestRetryingAPacksSkipsDoesNotGrowTheRecord` — mutation-verified |
| 5 | **`Show.Complete.Series` parses with the words in its title**, and was refused as a different series — which reads as though the right one might be grabbable | Several seasons is asked first |
| 6 | **The search pass counted a season's search as one of its episodes** — *searched for 1 of 3 due … 2 more wait for the next pass*, when the season's search had answered for all three. Found running the binary | Counted by what the searches answered for. Asserted in `acquire.TestAPackIsGrabbedForASettledSeasonWhollyWanted` |
| 7 | The Overview page still says *Phase 1 is identity and the security spine* and that the library, acquisition and playback answer 501 | Noticed, not changed: not this increment's |

#### Verified against the running binary

A fresh instance on schema 19, a Torznab stand-in on `127.0.0.1`, three
series as the episode refresh would have left them. **A person**: *Severance*
season 1's search offered the pack and a single S01E02 with tickets, and
refused `S01-S02` (*several seasons in one release*) and the season-2 pack;
the pack's grab was queued *for Severance (2022) S01 (the whole season)*. Its
seven files — three episodes (one named only `Episode 3 - In Perpetuity.mkv`,
two in a `Season 1/` folder), a sample, an `.nfo` and two subtitles — became
three episodes under `Season 01/`, hardlinked, with the E01 subtitle beside E01
and the stray `English.srt` left behind; the sample and the `.nfo` were recorded
as skipped with their reasons. **A machine**: *Slow Horses* season 1, settled
and wholly wanted, was searched once as a season: the indexer offered a short
pack first — its `.torrent` was fetched, read, and refused — and the real one
was grabbed, audited as `system:acquire` *for Slow Horses S01 (the whole
season)*, downloaded, and imported as three episodes. The Wanted list held
nothing of either. One honest substitution, as in 4m: no peer on loopback could
supply the data, so the bytes were placed in the engine's data directory and
the engine verified them against the piece hashes. **In Chromium**: *Search
season* on a season with nothing on disk; the panel said *3 of 4 result(s) are
S01 or one of its episodes*, marked the pack as *The whole season in one
download* and the single one as *One episode of the season: S01E02*, and gave
the multi-season pack no grab button — no console errors.

| Claim | How it is established |
|---|---|
| Only a pack of exactly the wanted season matches | `search.TestASeasonPackIsOnlyItsOwnSeason`, `release.TestSeveralSeasonsAreRecognised` — mutation-verified |
| A season's search seals each result to what it is | `search.TestASeasonSearchSealsWhatEachResultIs`, `search.TestASeasonTargetHasOneShape`, `api.TestASeasonSearchTicketsOnlyTheSeason`, `api.TestAGrabHandsTheSealedSeasonToTheQueue`, `library.TestASeasonIsFoundWithItsSeriesAndEpisodes` — mutation-verified |
| A season target has one shape, survives the queue and the migration, and reaches the import | `download.TestASeasonTargetIsStoredAndReadBack`, `db.TestTheSeasonTargetMigrationKeepsEveryRow`, `download.TestATorrentsFilesAreListedWithoutTouchingDisk`, `main.TestAnImportIsToldAGrabWasForASeason` — mutation-verified |
| Each file of a pack is its own episode, and nothing else | `importer.TestAPackImportsEachFileAsItsOwnEpisode`, `importer.TestAPackFileMustNameAnEpisodeOfItsSeason`, `importer.TestPlanningAPackReadsNamesOnly`, `importer.TestTwoFilesForOneEpisodeAreBothSkipped`, `importer.TestAPackNeverReplacesABetterFile`, `importer.TestAPacksSubtitlesGoWithTheirOwnEpisode` — mutation-verified |
| A pack is retried for what failed, and its skips wait | `importer.TestAPackIsRetriedOnlyForWhatFailed`, `importer.TestAPackWithSkipsWaitsTheSkipInterval`, `importer.TestRetryingAPacksSkipsDoesNotGrowTheRecord` — mutation-verified |
| A machine grabs a pack only for a settled season wholly wanted, and only one seen to hold it | `acquire.TestAPackIsGrabbedForASettledSeasonWhollyWanted`, `acquire.TestAPackIsNotGrabbedUnlessEverythingInItIsWanted`, `acquire.TestAPackMustBeSeenToHoldTheSeason` — mutation-verified |
| A season's download holds every episode of it until its import is done | `acquire.TestASeasonDownloadHoldsEveryEpisode` — mutation-verified |

Thirty-seven mutations, all killed; the race detector is clean. **The first run of them proved nothing**: the harness passed its test pattern through a shell that read `^(...)$` as a syntax error, and a syntax error exits non-zero exactly as a failing test does — every mutant "died" without a test running. Rerun with the pattern quoted, each test first shown to pass unmutated and each kill required to be that test failing: twenty-eight died at once, four needed rewriting to compile, and six survived. Two guards no test reached — a retry re-examining a file that had already arrived (the upgrade rule caught it, so only the record was wrong), and a season searched twice in one pass when nothing was grabbed; a specials test that the magnet rule refused before season 0 was ever asked; no test at all for an instance with no pack check; a feed pack of a season not wholly wanted, looked at and refused rather than not looked at, which the test missed because by the time it ran nothing was open; and a guard in the library that repeated what its own query answers — removed. The tests were fixed, and all thirty-seven die. golangci-lint found four things in the new
code — an error returned as nil without saying why, two result sets closed by
hand rather than deferred, and a switch — all fixed; gosec found nothing.

What it does not do is in ADR-0033: multi-season and complete-series packs, a
season partly in the library (a person's *Search season* does it), a season
still airing, a file with no episode number in its name, a pack mixing
qualities, a magnet-only pack for a machine, and everything ADR-0030 does not
match.

---

### Increment 4z — a download that stops moving: a machine gives up its own, a person is told ✅

ADR-0030 left *"a download that never finishes blocks its item until a person
removes it"*; since 4y a dead pack holds a whole season.
[ADR-0034](docs/adr/0034-stalled-downloads.md) was written first.

| What | How |
|---|---|
| **Stalled** | No verified progress — the engine's completed bytes not rising, a magnet's metadata not arriving — for `download.stall_after`: a day by default, `0` off, under an hour refused. Counted from the later of the last progress and the engine's start, so an instance that was off has not watched anything fail |
| **Progress on the row** | `progress_bytes`, `progressed_at`, `stalled_at` (migration 0020); the `download.stalls` task records progress every five minutes. Fewer bytes than recorded — a restart re-verifying — is not progress |
| **Automatic acquisition's: given up** | Stopped, kept in the queue — which is the blocklist — and its item wanted again; the next pass looks for another release. `acquire.Automatic` is the one rule for what is automatic: no person, and the label |
| **A person's: reported** | Marked, left running, and holding its item: removing it stays their call. Cleared if it moves again |
| **On the record** | Once each, `acquisition.stalled` by `system:download`: the release, how long it had not moved, what was done. Discord under **Library**, off by default, because it names a release |
| **The Queue screen** | *No progress since …* — and either *Given up: it is never grabbed again, and what it was for is searched for afresh*, or *It is still running. Remove it to let another release be found* |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **The mutation harness for 4y had tested nothing** — its `-run` pattern reached a shell as a syntax error, which exits non-zero as a failing test does. Found when a `|` in a test filter broke the same way | Harness fixed and made to prove each test passes unmutated and that a kill is that test failing; 4y rerun and corrected above |
| 2 | Two guards were redundant — the stall task skipping a row already marked (the store's update marks a row once), and the queue checking a given-up row was stopped (a given-up row always is) | Removed; a mutant of each survived, which is how they were found |

#### Verified against the running binary

A fresh instance with `stall_after: 1h` and nothing able to seed: a person
grabbed *Severance* season 1 from its search, and automatic acquisition grabbed
*Slow Horses* season 1. Sixty-five minutes later `download.stalls` said *gave up
1 stalled automatic download(s): Slow.Horses… 1 download(s) a person grabbed have
stopped moving: Severance…*; the machine's row was `stopped` and the person's
still `downloading`, both marked; two `acquisition.stalled` lines by
`system:download`, *given up; what it was for is wanted again* and *left running
for the person who grabbed it to remove*. The next search pass looked for *Slow
Horses* season 1 again at once and refused the same pack — *it was grabbed
before*; the queue answered `stalled` for both, `given_up` only for the
machine's.

| Claim | How it is established |
|---|---|
| Stalled is no verified progress for the threshold, counted only while the engine runs; off at zero | `download.TestAStallIsNoProgressForTheThreshold`, `config.TestTheStallThresholdIsChecked` — mutation-verified |
| Progress is kept, and moving again clears the mark; a stall is marked once | `download.TestProgressIsKeptAndClearsTheMark` — mutation-verified |
| A machine's stall is given up, a person's reported | `download.TestAMachinesStallIsGivenUpAPersonsIsReported`, `acquire.TestOnlyAutomaticAcquisitionsDownloadsAreAutomatic` — mutation-verified |
| A given-up download frees its item, and its release is not grabbed again | `acquire.TestAGivenUpDownloadFreesItsItem` |
| On the record once, and Library news | `main.TestAStallIsWrittenToTheAuditLog`, `notify.TestAStalledDownloadIsLibraryNews` — mutation-verified |
| The queue says so | `api.TestTheQueueSaysADownloadStalled` — mutation-verified |

Seventeen mutations, all killed with the corrected harness; one needed rewriting
to compile, and one survivor was the redundant check above. The race detector
is clean.

What it does not do is in ADR-0034: a download that trickles is never stalled,
an instance restarted more often than the threshold finds nothing, the partial
data stays, and a person's stalled download holds its item until they remove it.

---

### Increment 4aa — a quality profile per title ✅

One profile judged every search nobody chose one for and everything automatic
acquisition fetched (ADR-0027, ADR-0030), and both records deferred the choice
per title. [ADR-0035](docs/adr/0035-a-quality-profile-per-title.md) was written
first.

| What | How |
|---|---|
| **A title names a profile** | `media_item.quality_profile_id` (migration 0021): a profile, or NULL for the default — every title that exists. `ON DELETE SET NULL`: deleting a profile a title names returns the title to the default |
| **Who chooses** | `PUT /api/v1/media/{id}/quality-profile` — `library.edit`, as monitoring is; audited as `media.quality_profile.changed`, with what it was and what it became. A *Quality* row on the title's page: a choice for whoever may edit the library, a sentence for everyone else |
| **Searches for a title** | ADR-0027's rule one step longer: no profile named is **the title's, then the default**; `0` is still none, a number still that profile. Episode, season and film searches; every answer says `profile_title` |
| **Automatic acquisition** | Each item searched for, judged and ranked by its title's profile. The recent releases, judged once by the default, are judged again for a title with its own — and **ranked again**: judged but not re-ranked, Dune took the 1080p ahead of the 2160p it asked for, because its profile allows both. A refusal names the profile that refused |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **Judging the feed again was not enough.** The feed comes back ranked by the default; a title whose profile allows what the default likes took the first release the default liked | The feed is grouped by the item each release is and each group ranked by its title's profile. `acquire.TestATitlesProfileJudgesWhatIsFetchedForIt` — mutation-verified |
| 2 | A refusal of a title's release said *the default profile refuses it* whatever refused it | Names the title's own. The test first passed with the wrong wording, because the rejection's detail named the profile anyway; tightened when its mutant survived |

#### Verified

On the running binary, through the API: *Andor* season 1's search was judged by
the default *HD-1080p*, with its 720p HDTV pack grabbable; after `PUT
…/quality-profile` to *Ultra-HD* — *Its searches, and automatic acquisition,
judge it by Ultra-HD* — the title read back profile 3, the same search said
`profile_title: true` and refused the 720p pack (*quality HDTV-720p is not in
profile "Ultra-HD"*), and putting it back said *the default (HD-1080p)*. **Not
checked in a browser**: the Chrome extension was not connected, so the *Quality*
row on the title's page has been syntax-checked, not seen.

| Claim | How it is established |
|---|---|
| A title names a profile or the default; an unknown one is refused; deleting it returns the title to the default | `importer.TestATitleCanNameItsProfile` — mutation-verified |
| Only `library.edit` sets it, and it is audited | `api.TestATitlesProfileIsSetAndAudited` — mutation-verified |
| A search for a title is judged by its profile, then the default; 0 and a named profile still win | `api.TestATitlesProfileJudgesItsSearches`, `library.TestASearchSubjectCarriesTheTitlesProfile` — mutation-verified |
| Automatic acquisition fetches for each title by its own profile, from searches and the recent releases | `acquire.TestATitlesProfileJudgesWhatIsFetchedForIt` — mutation-verified |

Sixteen mutations, all killed; one survived its first run and its test was
tightened (above).

What it does not do is in ADR-0035: the import's upgrade rule still ranks by the
default ladder, a file below the title's cutoff is not replaced, and a pass
still needs a default profile.

---

### Increment 4ab — upgrades to the cutoff: off until chosen, after what is wanted, never worse ✅

Automatic acquisition stopped at a file's arrival, however far below its
profile's cutoff (ADR-0030, decision 2), and a library started from nothing is
full of first copies. [ADR-0036](docs/adr/0036-upgrades-to-the-cutoff.md) was
written first.

| What | How |
|---|---|
| **Off until chosen** | `acquisition.upgrades` (`CMS_ACQUISITION_UPGRADES`), false by default, refused without `acquisition.automatic`; the start-up line says whether it is on |
| **What is upgraded** | A monitored episode or film whose file is below its title's profile's cutoff (ADR-0035), read from the quality the import recorded. Not a file covering two episodes |
| **Only better** | `release.Profile.ShouldUpgrade` against the file held — higher quality, or a PROPER/REPACK, or a better preferred score; never equal — and every rule a grab already follows. A release of two episodes is not an upgrade of one whose neighbour has a file |
| **After what is wanted, weekly** | The search pass spends its budget on the Wanted list first. Each file is searched for an upgrade at most once a week from its last search — the one that fetched it, for a file just arrived. The recent releases carry upgrades at no extra request |
| **Replaced into the trash** | The import supersedes the old file into the trash, as for any upgrade (ADR-0016). The audit line and the pass's summary say *an upgrade from WEBDL-720p* |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **The import judges *better* by the default ladder, not the title's profile.** An upgrade a profile chose that the ladder disagreed with would be fetched and not imported | For the built-in profiles the two agree, and `release.TestTheBuiltinProfilesRankAsTheImportDoes` holds them to it; a custom profile that disagrees is a stated limitation |
| 2 | A check refusing a two-episode release as an upgrade was redundant — and too strict: the rule every grab follows already refuses it when the other episode has a file, and when the other is missing the release rightly fills it | Removed when its mutant survived |
| 3 | An upgrade moves only the old video to the trash; a subtitle placed beside it stays, under the old name. Seen running the binary | Stated in ADR-0036's limitations |

#### Verified against the running binary

With `upgrades: true`: *Severance* season 1 arrived as a WEB-DL 1080p pack —
below *HD-1080p*'s Blu-ray cutoff. The indexer then published a Blu-ray
S01E01 and a second WEB-DL S01E02. The recent-release pass grabbed *Severance
S01E01 (an upgrade from WEBDL-1080p)* and refused the other — *not an upgrade:
not better than what is held*; the import replaced E01 — *replacing the file it
had* — and the WEB-DL copy was in `.cmediastack-trash`, restorable.

| Claim | How it is established |
|---|---|
| Off unless chosen, and only with automatic acquisition | `acquire.TestUpgradesAreOffUnlessChosen`, `config.TestUpgradesNeedAutomaticAcquisition` — mutation-verified |
| A file below its title's cutoff is upgraded by a better release; one at its cutoff is left alone | `acquire.TestAFileBelowItsCutoffIsUpgraded`, `acquire.TestAFileAtItsCutoffIsLeftAlone`, `acquire.TestAnUpgradeMustBeBetter` — mutation-verified |
| What is wanted first; an upgrade weekly; from the feed too | `acquire.TestWhatIsWantedComesBeforeUpgrades`, `acquire.TestAnUpgradeIsLookedForWeekly`, `acquire.TestAnUpgradeFromTheRecentReleases` — mutation-verified |
| Two-episode files and releases are left alone; a person's change wins | `acquire.TestADoubleEpisodeFileIsNotUpgraded`, `acquire.TestAReleaseOfTwoEpisodesIsNotAnUpgradeOfOne`, `acquire.TestAPersonsChangeStopsAnUpgrade` — mutation-verified |
| The built-in profiles rank as the import does | `release.TestTheBuiltinProfilesRankAsTheImportDoes` — mutation-verified |

Thirteen mutations, all killed; the fourteenth's check was the redundant one
above, and went.

What it does not do is in ADR-0036: two-episode files, a custom profile that
ranks against the ladder, and subtitles left beside an upgrade.

---

### Increment 4ac — libraries are root folders, and a rating ceiling hides what is above it ✅

Since Phase 1 an approver could send library ids and a rating ceiling for a new
account; both were stored, carried on every principal, and applied by nothing —
every account that could browse saw the whole library. The records deferred it
for a decision about what a library is. The operator made it on 2026-09-29: a
root folder, and ratings from the provider.
[ADR-0037](docs/adr/0037-libraries-and-rating-ceilings.md) was written first.

| What | How |
|---|---|
| **A library is a root folder** | Migration 0022 drops the `library` table that never held a row and its `library_grant`; `root_folder_grant` names root folders, and forgetting a root forgets its grants |
| **Nothing changes by itself** | `app_user.all_libraries`, **1 for every account that existed**; 0 only when somebody ticks a list. An administrator and background work see everything |
| **Ratings** | A film's US certification (theatrical first) and a series' US content rating, from TMDB, ranked 1 (G, TV-Y, TV-G) to 5 (NC-17). `metadata.ratings`, hourly, 50 titles, as `system:ratings` (browse only); an unrated title asked again after 30 days. **Unrated is above every ceiling** |
| **By hand** | `PUT /api/v1/media/{id}/rating` — `library.edit`, audited `media.rating.changed`; the task never replaces a person's; `null` gives it back to the provider. A *Rating* row on the title's page |
| **Enforced in SQL** | `library.Visible` is a WHERE clause: the title list, a title and its files, seasons and episodes, Wanted, every playback route, searches for a title, the monitoring, profile and rating switches, the identification review, root folders to browse, a request's item, posters to anyone without `library.edit`. **Hidden answers exactly as absent does** |
| **No grant wider than the grantor's** | `authz.CanGrant`, at approval and in an invite: a restricted approver grants only roots they see and a ceiling at or below theirs |
| **Changing an account** | `PUT /api/v1/admin/users/{id}/access`, `admin.users`, audited `user.grants.changed`, applies on the next request. *Change access* on the Users tab; a library picker and ceiling on the approval and invite forms |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **Saving or forgetting a playback position never looked the file up.** A hidden file's id saved a position; an absent one failed on the foreign key — an oracle for which ids exist | Both resolve the file through the scoped lookup first |
| 2 | **Listing a file's subtitles answered 200 with an empty list for a file that does not exist**, because the fallback for an unprobeable file swallowed the lookup's error | The file is looked up first |
| 3 | **Linking a request to a title named the title back** whoever could see it: the request store reads the item unscoped | The link is refused, as for a missing id, when the approver cannot see the title |
| 4 | The queue named its targets through the same read as everything else, so scoping it would have told a restricted Manager *a film no longer in the library* about a download in flight | The queue reads names unscoped, on purpose (the release name says it anyway); a request's item reads scoped and says *not in the library you can see* |
| 5 | Adding the scope to the importer's one item query would have silently emptied every internal read made without a principal | The scope is a required argument — `visibleTo(ctx)` or `everything` — so each read chose; `library.TestEveryItemReadChoosesAScope` holds every other package's SQL to the same. It found twelve unmarked reads on its first run; each now says why it is unscoped |
| 6 | **In Chrome, the access picker offered no libraries**, so a restricted grant could not be saved. `app.js` already declared a `loadRoots` for the Storage screen, and a second declaration of a function silently replaces the first everywhere | Renamed; `web.TestNoFunctionIsDeclaredTwice` fails the build on a duplicate. Seen only in a browser — every API test passed |
| 7 | The Overview still said *Phase 1 is identity and the security spine … answer 501 until their phase lands* | Replaced with what the tabs are, and what the *Libraries* figure means |
| 8 | Three mutants first survived: two were malformed (one did not compile, one broke the SQL rather than removing the check), and one showed the certification test's data could not tell a theatrical preference from a lowest-type one | The mutations rewritten; the test given a limited release rated differently |

#### Verified

| Claim | How it is established |
|---|---|
| A root folder is what is granted; existing accounts keep every library; a forgotten root takes its grant | `identity.TestAGrantNamesRootFolders` — mutation-verified |
| The scope is SQL; unrated is hidden from a ceiling | `library.TestTheScopeIsAppliedInSQL` — mutation-verified |
| Every route naming a title answers hidden as absent — and a new such route must join the test | `api.TestATitleOutOfScopeDoesNotExist` — mutation-verified |
| Lists, Wanted, root folders, the review queue, posters, request links | `api.TestListsShowOnlyWhatIsInScope` — mutation-verified |
| No grant wider than the grantor's | `authz.TestAGrantIsNoWiderThanTheGrantors`, `api.TestAnAccountsAccessIsChangedAndAudited` — mutation-verified |
| Ratings fetched, a person's kept | `importer.TestRatingsAreFetchedAndAPersonsIsKept`, `metadata.TestCertificationIsRead`, `api.TestARatingIsSetByAPerson` — mutation-verified |
| Every read of `media_item` chooses a scope | `library.TestEveryItemReadChoosesAScope` — mutation-verified |

Twenty-eight mutations, all killed.

**On the running binary:** two roots, *Kids* and *Films*, a film scanned into
each and rated by hand (*Paddington* PG, *Heat* R). An invite for *Kids*, up to
PG, redeemed by *kid*: `/me` said `library_ids: [1], rating_ceiling: 2`; the
library held *Paddington* only; *Heat* answered `404 {"error":"not found"}`,
the same bytes as an id that does not exist; *Heat*'s file would not stream and
*Paddington*'s did (20 MiB); the root folders were *Kids*. Rated PG-13,
*Paddington* left kid's library too. The administrator's *every library, no
ceiling* applied on kid's next request, and the audit log had each
`media.rating.changed` and the `user.grants.changed`.

**In Chrome** (headless, driven over the DevTools protocol, because the
extension was not connected): *Change access* on the Accounts tab, *Only the
ones ticked*, *Kids*, *up to PG, TV-PG*, *Save access* — *kid now sees Kids ·
rated up to PG, TV-PG* — the invite form's picker, the *Rating* row on a title's
page, and kid's own library holding one film. No page errors.
Screenshots: `Claude outputs/4ac-*.png`.

What it does not do is in ADR-0037: other countries' certifications, a rating
per episode, a library that is not a root folder, and a queue that names what
it downloads whoever is looking.

---

### Increment 4ad — the library routes left unbuilt: four removed, three built ✅

Seven library and playback routes had answered 501 since Phase 1. Phase 4 built
playback without four of them, and a route that answers 501 forever is a promise
nobody is keeping. [ADR-0038](docs/adr/0038-the-library-routes-left-unbuilt.md)
was written first.

| What | How |
|---|---|
| **The play-session routes are gone** | `POST /api/v1/play/sessions`, its manifest, segments and progress: HLS, which needs a third-party player in the page (ADR-0011) and is the shape of the adaptive bitrate ADR-0005 refuses. The session is the signed-in one, the bytes are `/files/{id}/stream` and `/convert`, progress is `/files/{id}/position` |
| **`GET /api/v1/search?q=`** | The library's titles containing every word of the query at the start of a word, folded as the release matcher folds (`pokemon` finds *Pokémon*), those starting with it first, 50 at most. Scoped (ADR-0037); never the provider |
| **`GET /api/v1/media/{id}/artwork`** | The title's cached poster, keyed by the title so its scope decides. A title's JSON now points its `poster` here |
| **`GET /api/v1/media/{id}/original`** | `media.download_original`: the file as stored, as an attachment, resumable; `?file=` for a title with several; scoped; audited `media.downloaded` once per download, not per resumed range; 30 an hour. A *Download* button beside *Play* |
| **Unbuilt, pinned** | `api.TestNoRouteIsLeftUnbuiltWithoutARecord` lists the thirteen routes still answering 501; one leaves the list by being built or removed with a record, and a new one cannot appear unnoticed |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **A poster the instance could not fetch answered 500** — with no metadata provider configured, for a candidate it had recorded. On the provider-keyed route too, since 3i | A 404, as for any missing poster; a denial still answers as a denial |
| 2 | Two mutants did not compile at first | Rewritten; all eleven killed |

#### Verified

| Claim | How it is established |
|---|---|
| The play-session routes are gone, and the unbuilt list is exactly thirteen | `api.TestNoRouteIsLeftUnbuiltWithoutARecord` — mutation-verified |
| Search folds, matches word starts, puts prefixes first, is scoped and bounded | `api.TestTheLibraryIsSearchedByTitle` — mutation-verified |
| A title's artwork is its poster, scoped | `api.TestATitlesArtworkIsItsPoster` — mutation-verified |
| A download is an attachment, chosen by file, scoped, audited | `api.TestAnOriginalIsDownloadedAndAudited` — mutation-verified |

On the running binary: `search?q=padd` found *Paddington*, `q=x` was refused by
name, *Paddington*'s original came down whole (20 MiB) and the audit log said
`media.downloaded | Paddington (2014): Paddington.2014.1080p.BluRay.x264-GRP.mkv`;
`/api/v1/play/sessions` answered 404.

---

### Increment 4ae — account administration: roles edited, accounts created, an email changed ✅

Five account routes had answered 501 since Phase 1, and the three built-in roles
were fixed: the seed rewrote their permissions at every start.
[ADR-0039](docs/adr/0039-account-administration.md) was written first.

| What | How |
|---|---|
| **A pending request, alone** | `GET /api/v1/accounts/requests/{id}`, `account.approve`; decided, expired and unknown are all 404; the password hash never leaves the store |
| **Roles listed** | `GET /api/v1/admin/roles`: rank, permissions, whether they are the defaults, how many hold it, and which permissions are Admin's alone |
| **A role edited** | `PATCH /api/v1/admin/roles/{id}`, `{"permissions": [...]}` or `{"defaults": true}`. Admin is not editable; only a role below the actor, with permissions the actor holds; `admin.system`, `admin.users` and `admin.network` stay Admin's; `auth.login` cannot be removed. Migration 0023 keeps a chosen role past the seed; Admin is reseeded whatever happens. Applies to holders and their tokens on the next request; audited `user.role_permissions.changed`, a *Security* notification |
| **An account created** | `POST /api/v1/admin/users`: username, email, role and grant (ADR-0037), strictly downward and no wider than the actor's. A random password nobody holds, and a one-time link that sets one, for three days — the administrator never knows it. `user.created` |
| **One's own email** | `PATCH /api/v1/me` with the current password; session-only, so an API token cannot redirect where a reset would go; a taken or malformed address is refused by name. The username does not change: it is how the audit log names the account |
| **Screens** | *Create an account* and *Roles* on the Accounts tab; *Change email* on Security |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **Nothing validated an email address or a username** — signup stores what it is sent | The two new paths validate; signup is unchanged, and noted here |
| 2 | Nothing tested that a role may only be edited by an actor who outranks it — every API route to it is Admin's | `identity.TestAnEditedRoleSurvivesTheSeed` now edits as a Manager-ranked actor too |
| 3 | In Chrome, an `email` input had no style — the stylesheet named text, search, number and password — and twenty-three permissions made one long column | Styled; the permissions are a grid |

#### Verified

| Claim | How it is established |
|---|---|
| A pending request reads alone; a decided one is 404 | `api.TestAPendingRequestIsReadAlone` — mutation-verified |
| Roles are listed | `api.TestRolesAreListed` |
| A role is edited within its limits, at once, audited | `api.TestARoleIsEditedWithinLimits` — mutation-verified |
| An edit survives the seed; Admin does not; defaults reseed; only a lower rank | `identity.TestAnEditedRoleSurvivesTheSeed` — mutation-verified |
| An account starts with a link, not a password, strictly downward | `api.TestAnAdministratorCreatesAnAccount` — mutation-verified |
| An email changes with the password, from a session only | `api.TestAnEmailIsChangedWithThePassword` — mutation-verified |

Thirteen mutations, all killed. In Chrome, against the running binary: an
account created (*cousin*, awaiting MFA, with its link), `media.download_original`
ticked for *User* and saved — *User changed* — and the administrator's email
changed. No page errors. Screenshot: `Claude outputs/4ae-accounts.png`.

---

### Increment 4af — operations: every health check at once, recent logs, settings read not written ✅

Four administration routes had answered 501 since Phase 1, and an operator
asking whether the instance was well read the container's log, the config file,
the Tasks and Backups screens, one at a time.
[ADR-0040](docs/adr/0040-operations-routes.md) was written first.

| What | How |
|---|---|
| **`GET /health/detail`** | `admin.system`, hidden. Each check `ok`, `warn` or `fail` with a sentence — database, egress, tasks (a last run that failed, named), backups (none, or older than twice the interval), each root folder's space (unreadable fails; under 5 % and 10 GiB warns), the media parser sandbox, the metadata provider — and the worst of them overall, with version, schema and uptime. `/healthz` still says `ok` and nothing else |
| **`GET /api/v1/admin/system/logs`** | The last 1 000 records the process wrote, kept in a ring **behind the redaction handler**, so it holds what the output holds. Level, text, at most 500, newest first. Gone at restart; the host's log is the history |
| **Settings** | `GET` is the effective configuration as the file spells it; it holds no secret — the master key is named by its variable. `PATCH` answers **409, permanently**, saying the file is where settings change, as the egress policy's `PATCH` does |
| **A Health screen** | The report and the recent records, filtered, for the administrator |

#### Verified

| Claim | How it is established |
|---|---|
| The report names each check; the overall state is the worst | `api.TestHealthDetailSaysWhatIsWrong` — mutation-verified |
| The ring keeps recent records, redacted, newest first, bounded | `logging.TestTheRingKeepsRecentRedactedRecords`, `api.TestRecentLogsAreReadable` — mutation-verified |
| Settings are read and never written | `api.TestSettingsAreReadNotWritten` — mutation-verified |

Eleven mutations, all killed — including one that put the ring in front of the
redaction handler. In Chrome, against the running binary: *Overall: warn* —
every check `ok` but *no metadata provider is configured* — both root folders'
free space measured, and the start-up records listed with their attributes. No
page errors. Screenshot: `Claude outputs/4af-health.png`.

---

### Increment 4ag — a calendar and a feed, behind a token in the address ✅

Two feed routes had answered 501 since Phase 1, marked *authenticated* — which a
phone's calendar and a feed reader can never be: they are given an address and
send nothing else. [ADR-0041](docs/adr/0041-feeds.md) was written first.

| What | How |
|---|---|
| **A feed token** | `POST /api/v1/me/feeds` mints one per account — `cms_feed_`, 256 bits, shown once with both addresses, stored as its SHA-256; minting again replaces it; `DELETE` revokes; `GET` says whether one exists and when it was last read. Session-only. Audited `auth.feed_token.issued` / `.revoked` |
| **The feed routes are anonymous** | Their token is checked by the handler: a principal of its account holding **`media.browse` only**, in the account's scope (ADR-0037). A wrong, revoked or suspended account's token is 404, the same bytes. 120 an hour from an address. The anonymous surface is now 14 routes — a change `api.TestAllowlistAndRegistrationsAgree` made deliberate |
| **Never logged** | `/feeds/…/` and `cms_feed_…` are masked in every log line, as a password is |
| **The calendar** | iCalendar: every episode with an air date from a month back to three months ahead, of the series the account can see — all-day, stable UIDs, *held* or not, escaped and folded per RFC 5545 |
| **The feed** | RSS 2.0: the fifty most recent arrivals the account can see, newest first |
| **Screen** | *Calendar and feed* on the Security tab: issue, replace, revoke, copy |

#### Verified

| Claim | How it is established |
|---|---|
| Minted once, replaced, revoked, from a session only, audited | `api.TestAFeedTokenIsACredentialOfItsOwn` — mutation-verified |
| Reads the two feeds only, as its account, scoped; wrong, revoked or suspended alike | `api.TestAFeedTokenReadsOnlyTheFeeds` — mutation-verified |
| Valid iCalendar: CRLF, escaping, 75-octet folds that never split a character | `api.TestTheCalendarIsValidICalendar` — mutation-verified |
| The feed is recent arrivals, scoped, newest first | `api.TestTheFeedIsWhatArrived` — mutation-verified |
| The token never reaches a log line | `logging.TestAFeedTokenIsRedacted` — mutation-verified |

Eleven mutations: ten killed, one after the fold test was tightened to check
each line is valid UTF-8 (the unfolded text was intact either way). The
eleventh — removing the state check from the feed principal — is equivalent: a
suspended account's principal cannot act, so the read is refused one step later
with the same 404. On the running binary: the feed listed *Heat (1995)* then
*Paddington*, the calendar began `BEGIN:VCALENDAR\r\n`, a wrong token answered
`404 {"error":"not found"}`, and the token appeared nowhere in the log.

---

### Increment 4ah — reporting a problem with a title ✅

`POST /api/v1/issues` had answered 501 since Phase 1, and a household's *the
sound is out of sync* arrived by text message and was forgotten.
[ADR-0042](docs/adr/0042-reporting-a-problem-with-a-title.md) was written first.

| What | How |
|---|---|
| **A report** | `POST /api/v1/issues`: a title the reporter can see, a kind (`video`, `audio`, `subtitles`, `wrong_title`, `other`), optionally a season and episode, a note of at most 500 characters. `request.submit`. A hidden title answers as a missing one does |
| **Once per problem** | A partial unique index allows one *open* issue per title, kind and episode; a second report of the same answers with the first and adds nothing |
| **Who sees them** | `GET /api/v1/issues` — a reporter their own; whoever holds `library.edit` every one on a title in their scope. `?all=true` includes resolved ones, with the answer |
| **Resolved** | `POST /api/v1/issues/{id}/resolve` with what was done: `library.edit`, only in scope, once |
| **Audited, and sent if chosen** | `media.issue.reported` / `.resolved`, in the *Requests* notification category (off by default: it names titles) |
| **Screens** | *Something wrong?* on a title's page; *Problems reported* on the Requests tab, with *Resolve* for an editor |

#### Verified

| Claim | How it is established |
|---|---|
| A report names a visible title and a kind, once; bad input is refused; audited | `api.TestAProblemIsReportedOnce` — mutation-verified |
| Reporters see their own, editors every one in scope; only an editor resolves, once, in scope; audited | `api.TestIssuesAreSeenAndResolvedByTheRightPeople` — mutation-verified |

Nine mutations. One survived first because the other person's report was on a
title the reporter could not see, so scope hid it rather than ownership; the test
now has a second person's report on the same film. Another showed a redundant
branch, rewritten. In Chrome: *Paddington*, *The subtitles*, reported; listed
open on the Requests tab; resolved — *Resolved by jacob: Replaced with the
Blu-ray.* No page errors.

---

### Increment 4ai — Discover: what is popular, asked for from where it is shown ✅

The last route answering 501. Requests started from a title typed from memory;
Jellyseerr's front page is how a household finds something to ask for.
[ADR-0043](docs/adr/0043-discover.md) was written first.

| What | How |
|---|---|
| **Four lists** | `GET /api/v1/discover/{section}` — `trending`, `popular-films`, `popular-series`, `upcoming-films` — TMDB's first page, no adult titles, no people |
| **Shared, so free to browse** | Each list is fetched once in six hours for every account and kept in memory: at most sixteen provider requests a day, whoever browses. `media.browse` |
| **Marked per caller** | *in the library* — a title of that kind with that id the caller can see (a series and a film sharing an id are not confused); *requested* — an open request the caller can see, matched by kind, folded title and year |
| **Not for a capped account** | An account with a rating ceiling gets an empty list saying why: the lists carry no rating (ADR-0037). The provider is not asked |
| **No posters** | A poster is fetched only for a recorded title (ADR-0018); a page of the catalogue's posters would tell the image server what this household browsed |
| **Screen** | *Discover*, with *Request* beside anything not already here or asked for |

#### Verified

| Claim | How it is established |
|---|---|
| Each list is read from its endpoint, without adult titles or people | `metadata.TestDiscoverReadsItsLists` — mutation-verified |
| One fetch per six hours for everybody; marked in-library by kind and requested by folded title | `api.TestDiscoverIsSharedAndMarked` — mutation-verified |
| No Discover for a capped account; no provider is 409, an unreachable one 502 | `api.TestDiscoverIsNotForACappedAccount` — mutation-verified |

Nine mutations, all killed (one rewritten after it did not compile). On the
running binary, which has no metadata key on this machine: *no metadata provider
is configured; set one on the Metadata screen*, 409. **Not checked against the
live provider**: TMDB's list endpoints are read with the documented shapes the
search already uses, and a key is needed to see them answer.

**With this, no route answers 501.** Of the twenty Phase 1 left unbuilt, 4ad–4ai
built sixteen and removed four (ADR-0038). `api.TestNoRouteIsLeftUnbuiltWithoutARecord`
now holds the list at empty: a stub cannot be registered again unnoticed.

---

## Phase 5 — Music and books ✅

The operator decided on 2026-09-29 that both are wanted.

### Increment 5a — music: artists, albums and tracks, from MusicBrainz ✅

Root folders had accepted `music` since Phase 3 and nothing else understood it.
[ADR-0044](docs/adr/0044-music-artists-albums-and-tracks.md) was written first.

| What | How |
|---|---|
| **An artist is a title** | `media_item` gains the kinds `artist` and `book`, so scope, search, requests, issues and deletion work on them unchanged; albums and tracks are rows of `album` and `track`, as seasons and episodes are |
| **Rebuilding a referenced table** | SQLite cannot alter a `CHECK`, and eight tables reference `media_item`. A migration whose first line is `-- cms: foreign_keys=off` now runs on one connection with foreign keys off, and is rolled back unless `PRAGMA foreign_key_check` finds nothing; the pragma is put back whatever happens. Migration 0026 rebuilds `media_item` so, carrying every column |
| **MusicBrainz, politely** | No key; a User-Agent naming this software; at most one request a second; through the egress guard's `metadata` profile. Albums are release groups of type Album or EP with no secondary type — no compilations, live albums, remixes, soundtracks or singles. A track list is the earliest official release's |
| **Adding an artist** | `POST /api/v1/media` with `kind: artist`, a MusicBrainz id and a monitoring choice (`all`, `future`, `latest`, `none`, no default), under `library.edit`: artist and albums in one transaction or not at all, nothing on disk. Track lists are fetched when an album is first opened, and by `music.refresh` (12-hourly: each artist weekly, fifty missing track lists) as `system:music-refresh`, browse only |
| **Wanted** | An album monitored, released (a year or month counts once begun), and missing a track — or whose track list is unknown and of which nothing is held. On the Wanted screen |
| **Ceilings are for films and series** | Music and books carry no certification; under ADR-0037's rule every album would be hidden from every capped account. The ceiling now governs films and series, and a *Kids' music* root folder governs a child's music |
| **Routes and screens** | `GET /api/v1/music/artists?q=` (library.edit), `GET /api/v1/media/{id}/albums`, `GET /api/v1/albums/{id}`, `PUT /api/v1/albums/{id}/monitored`, all scoped. *Artist (music)* on Add; an artist's page lists albums, held counts, tracks and monitoring |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | Adding an artist answered *adding to the library is not wired* in a test rig with no film adder: the handler checked for the video adder before reading the kind | The kind is read first |
| 2 | **In Chrome, an artist's page showed the film-only *Quality* and *Rating* rows** — the rating row saying the artist was hidden from capped accounts, which after decision 4 it is not — and offered *The picture* and *The subtitles* as problems | Shown for films and series only |
| 3 | Two mutants survived: one checked too little of an id's shape to prove anything, one showed nothing tested that a failed add leaves nothing behind | The mutation sharpened; an all-or-nothing case added |

#### Verified

| Claim | How it is established |
|---|---|
| A foreign-keys-off migration keeps every reference, and is refused when one would dangle | `db.TestAMigrationMayRebuildAReferencedTable` — mutation-verified |
| Migration 0026 keeps every title, column and reference | `db.TestTheMusicMigrationKeepsEveryTitle` |
| MusicBrainz is asked with a User-Agent, once a second, for albums and EPs, the earliest release's tracks, and never with an id that is not one | `music.TestMusicBrainzIsAskedPolitely` — mutation-verified |
| An artist is added with its albums, all or nothing, with the monitoring chosen; track lists lazily | `music.TestAnArtistIsAddedWithItsAlbums` — mutation-verified |
| What is wanted | `music.TestWhatAnArtistIsMissing` — mutation-verified |
| A ceiling hides films and series, not music or books | `library.TestACeilingIsForFilmsAndSeries` — mutation-verified |
| Through the API, scoped; album routes join the hidden-reads-as-absent table | `api.TestAnArtistIsFollowed`, `api.TestATitleOutOfScopeDoesNotExist` |

Fifteen mutations, all killed. **Against the real MusicBrainz**, through the
running binary: *Portishead* found (GB), added with five albums and EPs in 1.2
seconds — *Dummy* (1994), *All Mine*, *Portishead*, *Magic Doors*, *Third* —
*Dummy* opened to its eleven tracks from *Mysterons* to *Glory Box*, and all five
on the Wanted screen. In Chrome, the artist's page and its tracks, no page
errors. Screenshot: `Claude outputs/5a-artist.png`.

What it does not do yet: import, search and acquire music — the next increments.

---

### Increment 5b — music files: found by folder and track number, filed by the track list ✅

Artists and albums were known and no file could be one of them.
[ADR-0045](docs/adr/0045-music-files.md) was written first.

| What | How |
|---|---|
| **What music is** | `.flac .mp3 .m4a .aac .ogg .opus .wav`, its quality named by its container; lossless (FLAC, WAV) outranks lossy, and a lossless file is never replaced by a lossy one |
| **Which track a file is** | Its leading number (`03 - `, `03.`, `1-03`, `103` on two discs, a `CD2` folder), or a two-digit number inside a longer name (`Portishead - Dummy - 11 - …`), that the album's track list has; otherwise exactly one track's folded title in its name. Nothing, or two, and it is left alone |
| **Which album a folder is** | The artist's album whose folded title the folder name starts with, once `(1994)`, `[FLAC]` and a leading `<artist> - ` are set aside; the longest title wins |
| **The scan** | A music root is walked as `<artist folder>/<album folder>/…`; only a followed artist's folder counts — a folder nobody follows is reported, never guessed. Files are recorded and their tracks given them; nothing changes on disk; most of a root vanishing is refused as for video. The scan task and *Scan* send a music root to this scanner and every other to the video importer |
| **The import** | `ImportAlbum` files a downloaded album track by track into `<artist>/<album> (<year>)/<NN> - <title>.<ext>` (`<D>-<NN>` on several discs), hard-linked or copied across filesystems; a lossy track gives way to a lossless one, which goes to the trash; files matching nothing stay in the download. Wired to grabs in the next increment |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | `ImportAlbum` sorted the caller's list of files in place | It sorts a copy |
| 2 | Three mutants survived first: the title fallback hid a missing inner-number rule, a trailing `[FLAC]` still matched without its stripping, and one mutant did not compile | Cases only the real rules satisfy (`portishead_dummy_11_gl0ry.flac`, `[1994] Dummy`); the mutation rewritten |

#### Verified

| Claim | How it is established |
|---|---|
| Audio and its quality; lossless outranks lossy | `music.TestAudioFilesAndTheirQuality` — mutation-verified |
| A file is matched by number, then title; ambiguity refused; discs | `music.TestAFileIsMatchedToATrack` — mutation-verified |
| Paths from the album's names, safely, per disc; the album of a folder | `music.TestMusicIsFiledByTheTrackList` — mutation-verified |
| The scan: followed artists only, reasons for the rest, the disk unchanged, vanishing refused, dispatched by root kind | `music.TestTheScanFindsFollowedArtists` — mutation-verified |
| The import: placed, lossless over lossy, the replaced file in the trash | `music.TestADownloadedAlbumIsImported` — mutation-verified |

Fourteen mutations, all killed. On the running binary, with *Portishead* followed
from the real MusicBrainz: `Dummy (1994) [FLAC]` holding `01 - Mysterons`,
`02 - Sour Times`, `03 - Strangers`, `11 - Glory Box` and `12 - Bonus Remix` —
*5 file(s) examined, 4 added, 1 skipped*, the bonus remix with *no track of the
album matches this file's number or title*, and *Dummy* holding 4 of 11.

---

### Increment 5c — an album searched for, grabbed and imported ✅

An album could be followed and imported and not fetched.
[ADR-0046](docs/adr/0046-searching-for-and-grabbing-an-album.md) was written first.

| What | How |
|---|---|
| **Which release is the album** | Not parsed: folded as every title is, it must start with the artist's name and then the album's title, and the next word must be the end, a year or a tag — a format (`flac`, `320`, `v0`, …), a source (`cd`, `web`, `vinyl`) or an edition word (`deluxe`, `remaster`, …). *Dummy Live*, *Third*, a discography and another artist's *Dummy* are refused, each with its reason |
| **A year when it is needed** | An artist's album titles rarely recur, so a release naming no year is accepted — unless the artist has another album of the same folded title (Weezer's *Weezer*s), when a year within one of the album's is required. A release naming years, none of them within one, is refused either way |
| **Quality, from the name** | FLAC 24-bit, FLAC (and ALAC), MP3-320, MP3-V0, then MP3, AAC, Vorbis and Opus. A name that says no format is refused, as a video release of unknown quality is. Matches sort by that ladder, then seeders; no video quality profile applies |
| **The indexers are asked for audio** | Torznab category 3000 and `<artist> <album>`, folded. Another term changes what is asked, not what can match |
| **The target is sealed** | A fourth target beside episode, film and season: the artist and the album, in the grab ticket, on the queue row (migration 0027 rebuilds `target_kind` to allow `album` and adds `target_album_id`), and read back only in that exact shape |
| **The import** | A completed download grabbed for an album goes to the music library's `ImportAlbum` (ADR-0045), never to the video importer; the outcome is recorded against the download like every import |
| **Route and screens** | `POST /api/v1/albums/{id}/search` under `acquisition.search`, scoped: a hidden album answers as a missing one. *Search* on every album of an artist's page and every album on the Wanted screen; the queue names the grab *Portishead — Dummy (1994)* |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | The Library list called every title that was not a series a *film* — an artist included (since 5a) | It names artists and books by their kind |
| 2 | Two mutants survived first: one did not compile, and the route's own "only the album gets a ticket" check was never exercised, because the real search never accepts a non-match | The mutation rewritten; `api.TestOnlyTheAlbumCarriesATicket` feeds the route a search that does |

#### Verified

| Claim | How it is established |
|---|---|
| A release is the album by its folded name; namesakes need a year | `search.TestAReleaseIsJudgedAgainstTheAlbum` — mutation-verified |
| The quality ladder, read from the name | `search.TestAReleaseNameSaysItsAudioQuality` — mutation-verified |
| An album search asks for audio, marks the matches, refuses Unknown and ranks by format | `search.TestAnAlbumSearchMarksTheMatchesBestFirst` — mutation-verified |
| An album target is one shape, sealed in the ticket | `search.TestATicketCarriesAnAlbum` — mutation-verified |
| The queue stores it, keeps it through a re-grab, and reads back no other shape | `download.TestAnAlbumTargetIsStoredAndReadBack` — mutation-verified |
| Migration 0027 keeps every row and allows only the album shape | `db.TestTheAlbumTargetMigrationKeepsEveryRow` |
| Only the album carries a ticket, whatever the search says | `api.TestOnlyTheAlbumCarriesATicket` — mutation-verified |
| Search, grab and the queue's target through the real router | `api.TestAnAlbumSearchGrabsIntoThatAlbum` — mutation-verified |
| A hidden album is searched for as a missing one | `api.TestATitleOutOfScopeDoesNotExist` — mutation-verified |
| An album download goes to the music library, and its outcome is recorded | `main.TestADownloadForAnAlbumGoesToTheMusicLibrary` — mutation-verified |

Twenty-one mutations, all killed. **End to end on the running binary**, with
*Portishead* followed from the real MusicBrainz and a Torznab stand-in on
`127.0.0.1` offering five releases:

```
search Dummy        -> asked "portishead dummy" in category 3000; 2 of 5 are Portishead — Dummy
                       [FLAC] first (5 seeders) over [MP3 320] (900); Dummy Live and Third refused
                       as other albums, the unlabelled one as unknown_format
grab the FLAC       -> 202, queued for "Portishead — Dummy (1994)"
engine              -> the album's bytes verified against the torrent's pieces, complete
import              -> "3 track(s) placed", hard-linked (the same inode as the seeding copy) to
                       Portishead/Dummy (1994)/01 - Mysterons.flac, 02 - Sour Times, 03 - Strangers
library             -> Dummy holds 3 of 11
```

The same substitution as ADR-0026's check: no peer on loopback, so the bytes
were put where the engine looks and it verified them. In headless Chrome the
*Search* buttons on the artist page and the Wanted screen open the results with
a *Grab* only on *Third (2008) [FLAC]*.

What it does not do yet: search for wanted albums without a person, or upgrade
a held album.

---

### Increment 5d — wanted albums fetched without a person ✅

A person could search for an album; automatic acquisition could not.
[ADR-0047](docs/adr/0047-fetching-wanted-albums-automatically.md) was written first.

| What | How |
|---|---|
| **The same machine** | `acquire.Service.RunAlbums`, the task `acquire.albums`, registered only with `acquisition.automatic` on and run on the search pass's interval. It takes the same one-pass-at-a-time lock, asks the same tunnel gate, spends the same budget, remembers the same failed fetches, grabs through the same code and writes the same `system:acquire` audit line. A pass of its own because the episode search will not start without a default *video* profile |
| **What it looks for** | The Wanted screen's albums — monitored, released, a track without a file — whose track list is known: *a track without a file* says that too. An album whose list is not read yet is left until `music.refresh` reads it, and the screen says so |
| **What it grabs** | `SearchAlbum`'s best release sealed to the album (a named format, the ladder, then seeders), with seeders and a link, never in the queue before, not failed in the last six hours. No quality profile is read |
| **Once an album** | An album a download was imported for is not grabbed again, however many tracks are missing: a MusicBrainz list often has a track no release carries. The screen says *imported once* and leaves it to a person |
| **Kept, and shown** | `acquire_state` gains `album_id` (migration 0028 rebuilds it: exactly one of episode, film or album). Back-off as for episodes: six hours, doubling to a week. The Wanted screen's albums carry the *automatic* line — searched when and what came of it, downloading, imported once, or why not searchable |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | An album download in the queue would have been read by the in-flight check as an episode of its artist | Read as the album it is (`acquire.TestAWantedAlbumIsFetched`) |
| 2 | Four mutants survived first. One showed the "has a track list" clause was dead — "a track without a file" already says it — and it was removed; two were checks the real search never reaches (a refused release, another album's) and now have a search that gets it wrong; one was the queue check before fetching, which the grab repeats after | Clause removed; `acquire.TestOnlyTheAlbumsOwnAcceptedReleaseIsGrabbed`; the test now asserts nothing is fetched |
| 3 | A stand-in answering every search with the same torrent made *Third* look grabbed with *Dummy*'s bytes — and the import files by track number, so it would have filed them as *Third*'s. A test artefact; real releases are named for what they hold, and the match needs the album's name (ADR-0046) | The stand-in answers only the search that names the album |

#### Verified

| Claim | How it is established |
|---|---|
| A wanted album with a track list is searched for, the best of it grabbed and sealed, audited as the machine's; downloading and imported once are left alone | `acquire.TestAWantedAlbumIsFetched` — mutation-verified |
| What is not wanted, not open, not due or not grabbable is left alone; namesakes, the gate, the budget, the back-off | `acquire.TestAnAlbumIsFetchedOnlyByTheRules` — mutation-verified |
| An album a person stopped wanting mid-pass is not fetched | `acquire.TestAWantedAlbumIsStillWantedAtTheGrab` — mutation-verified |
| Only the album's own accepted release, whatever the search says | `acquire.TestOnlyTheAlbumsOwnAcceptedReleaseIsGrabbed` — mutation-verified |
| Migration 0028 keeps every row; one of three, gone with its album | `db.TestTheAlbumStateMigrationKeepsEveryRow` |
| The Wanted screen's line for an album | `api.TestTheWantedScreenSaysWhatWasDoneAboutAnAlbum` — mutation-verified |

Twenty-one mutations, all killed. **On the running binary**, with *Portishead*
followed from the real MusicBrainz, `acquisition.automatic` on and a Torznab
stand-in on `127.0.0.1`:

```
acquire.albums   -> "no album is wanted with its track list known; no indexer was asked"
                    (the screen: "Its track list has not been read from MusicBrainz yet…")
music.refresh    -> 5 track list(s) fetched
acquire.albums   -> 3 of 5 due, newest first: Third, Magic Doors, Portishead — the indexers
                    found nothing; next in 6 hours. 2 more wait for the next pass
acquire.albums   -> All Mine: nothing; grabbed Portishead — Dummy (1994)
                    ([FLAC], from LAN Torznab); audited by system:acquire, "automatically"
library.import   -> imported 1: Portishead/Dummy (1994)/01 - Mysterons.flac, 02, 03
acquire.albums   -> "no album is due (5 wanted, 0 downloading, 1 imported once already,
                    4 waiting out a back-off)"; Dummy 3 of 11, "imported once"
```

Every question to the indexer was category 3000.

---

### Increment 5e — books, from Open Library ✅

`media_item` had accepted the kind `book` since migration 0026 and nothing added one.
[ADR-0048](docs/adr/0048-books-from-open-library.md) was written first.

| What | How |
|---|---|
| **A book, not an author** | Added on its own, as a film is. Open Library's works of an author are not a bibliography — the live search for *The Left Hand of Darkness* returned Le Guin's novel, Harold Bloom's study of it, an omnibus and a Portuguese translation, each a "work" — so following an author would want things nobody asked for |
| **Open Library, politely** | No key; a User-Agent naming this software; one request a second; through the egress guard's `metadata` profile. A work id must be `OL…W`. A book is read with one request, `q=key:/works/<id>`, which carries the authors and first year the work record does not. A connection that fails is tried once more; an answer or a timeout never is. 45 seconds a request: its search answered the same question in 2 s and in 38 s within a minute |
| **Adding** | `POST /api/v1/media` `{kind: book, openlibrary_id}` under `library.edit`; a `books` root (named when there are several); folder `<Author> - <Title> (<Year>)`; refused, with nothing written, when the book or the folder is taken; a field a book has not (`monitor`, `tmdb_id`) refused rather than ignored; audited. `GET /api/v1/books/search?q=` under `library.edit` |
| **Wanted, and scoped** | Monitored with no file is wanted; the film's switch unmonitors a book. Wanted lists books under their own heading. A book is a title like any other for scope; no rating ceiling applies |
| **Screens** | *Book* on Add, with the number of editions beside each result — what tells the book everybody means from a study guide; *Books* on Wanted; the author under a book's title |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | Every title's JSON carried a `rating` saying *"Unrated: hidden from every account with a rating ceiling"* — false for artists and books since 5a confined the ceiling to films and series | Said only for films and series |
| 2 | An artist with no files showed a film's *Not on disk yet* and a *Search* that would have searched for it as a film (since 5a) | An artist's page is its albums |
| 3 | A book's *Something wrong?* offered *The sound* | Wrong title, or something else |
| 4 | Open Library reset the first connection, then took 21 seconds — the first live search failed | One retry on a failed connection; 45 s; `books.TestAFailedConnectionIsTriedOnceMore`, `TestATimeoutIsNotRetried` |
| 5 | Two mutants survived first: the generic non-200 refusal (the test only sent a 429, which has its own case) and the duplicate check (unobservable while the folder check caught the same add) | A 500 in the test; the same book added under another folder |

#### Verified

| Claim | How it is established |
|---|---|
| Open Library is asked with a User-Agent, once a second, by key for a work, and never with an id that is not one | `books.TestOpenLibraryIsAskedPolitely` — mutation-verified |
| A failed connection is tried once more; a timeout and an answer never | `books.TestAFailedConnectionIsTriedOnceMore`, `books.TestATimeoutIsNotRetried` — mutation-verified |
| Adding: root, folder, duplicates, permission, audit | `books.TestABookIsAddedFromOpenLibrary` — mutation-verified |
| What is wanted: monitored, no file, scoped, newest first | `books.TestWhatBooksAreWanted` — mutation-verified |
| Search, add, wanted, unmonitor and scope through the router; no rating on a book | `api.TestABookIsAddedAndWanted` — mutation-verified |

Twenty-six mutations, all killed. **Against the real Open Library**, through
the binary: *the left hand of darkness* → `OL59800W` first with 91 editions;
added into `Ursula K. Le Guin - The Left Hand of Darkness (1969)`; a second add
409 *already_in_library*; `OL999999999W` 422; on the Wanted list; nothing on
disk. In headless Chrome: *Book* on Add found *Dune (1965)* by Frank Herbert,
155 editions, first of the series; *Add book*; both books under *Books* on
Wanted; the book's page says *by Frank Herbert*, *Not on disk yet*, and offers
no search yet.

What it does not do yet: a book's files — scanning, importing, searching for
and grabbing a release, and fetching wanted books automatically.

---

### Increment 5f — a book's file: found, searched for, grabbed and imported ✅

A book could be added and wanted and never held.
[ADR-0049](docs/adr/0049-a-books-files.md) was written first.

| What | How |
|---|---|
| **A book file** | `.epub .azw3 .mobi .pdf`, ranked EPUB > AZW3 > MOBI > PDF. One file a book: a better format replaces it, and the old one goes to the trash; a worse or equal one is left where it is. Filed as `<book folder>/<Title> - <Author>.<ext>` |
| **The scan** | A `books` root goes to the books library, not the video importer. A book file inside a book's folder is that book's; with several formats there, the best is recorded and the rest reported; a file outside every book's folder is reported, never guessed at. Nothing on disk changes; most of a root vanishing is refused |
| **Which release is the book** | Brackets set aside, and a scene name's trailing `-GROUP`. The title must be one run of the folded words, and every word outside it a word of the author's name, *by*, a year or a tag — with the surname among them. *Dune Messiah*, Brian Herbert's *Dune*, Bloom's study of Le Guin and an omnibus are each refused with the reason |
| **Search, grab, import** | The title's own `POST /api/v1/media/{id}/search` searches for a book when it is one: Torznab category 7000, `<surname> <title>`, a ticket only for the book in a named format, best format first. A fifth target, `book`, sealed and queued (migration 0029). A completed book download goes to the books library, its best file filed and hard-linked, the outcome recorded against it |
| **Screens** | *Search* on a wanted book's page and on the Wanted screen's books; results ranked by format with no profile to choose; a book's file has *Download* and no *Play* |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | The first rule — drop the author's words, what is left must be the title — failed a title that contains its author's name (*Oscar Wilde: A Life*) | The title found as a run, the rest checked |
| 2 | Setting aside a trailing `-GROUP` would have turned `Frank Herbert - Dune-Messiah` into *Dune* | Only from a scene name, which has no spaces |
| 3 | Five mutants survived first: the unknown-author guard (only its explanation was observable), the route's own ticket check (the real search never reaches it), the scan's choice of format (the directory happened to list the EPUB first), a case with a bracket that hid the scene-group rule, and one malformed mutation | Each given a test that only the real rule passes |
| 4 | The race detector caught 5e's retry test reading the stand-in server's call count without the lock its handler held | Read under the lock |

#### Verified

| Claim | How it is established |
|---|---|
| A release is the book; Dune Messiah, another Herbert, a study, an omnibus are not | `search.TestAReleaseIsJudgedAgainstTheBook` — mutation-verified |
| The format ladder from a release name | `search.TestAReleaseNameSaysItsBookFormat` — mutation-verified |
| A book search asks category 7000, marks the matches, best format first | `search.TestABookSearchMarksTheMatchesBestFirst` — mutation-verified |
| A book target is one shape, sealed; stored and read back only so | `search.TestATicketCarriesABook`, `download.TestABookTargetIsStoredAndReadBack` — mutation-verified |
| Migration 0029 keeps every row and allows a book | `db.TestTheBookTargetMigrationKeepsEveryRow` |
| Book files, the ladder, the path | `books.TestBookFilesAndTheirFormats` — mutation-verified |
| The scan: own folder only, the best format, reasons, disk unchanged, vanishing refused | `books.TestTheScanFindsABooksFileInItsFolder` — mutation-verified |
| The import: filed, a worse format kept out, a better one replacing into the trash, one row, not a film, not gone | `books.TestADownloadedBookIsImported` — mutation-verified |
| A books root is scanned by the books library | `music.TestTheScanFindsFollowedArtists` — mutation-verified |
| Only the book carries a ticket; no author, no search | `api.TestOnlyTheBookCarriesATicket` — mutation-verified |
| Search and grab through the router into the book | `api.TestABookSearchGrabsIntoThatBook` — mutation-verified |
| A book download goes to the books library, its outcome recorded | `main.TestADownloadForABookGoesToTheBooksLibrary` — mutation-verified |

Twenty-eight mutations, all killed. **End to end on the running binary**, with
the real Open Library, a Torznab stand-in on `127.0.0.1` and the real engine:

```
search  -> asked "guin the left hand of darkness" in 7000; 2 of 4 are the book:
           [EPUB] first (3 seeders) over [PDF] (900); Bloom's study not_this_book;
           the unlabelled one unknown_format
grab    -> 202, queued for "The Left Hand of Darkness (1969)"
import  -> imported 1: .../The Left Hand of Darkness - Ursula K. Le Guin.epub, EPUB,
           hard-linked (inode 189051, the same as the seeding copy); Wanted's books empty
scan    -> Dune added by hand with "A scan.pdf" and "Dune.epub" in its folder, and an
           Unknown/ folder: "4 file(s) examined, 1 added, 1 updated, 2 skipped" — the PDF
           a lesser format, Unknown/x.epub owned by no book
```

In headless Chrome the book's page lists its EPUB with *Download* and no *Play*.

What it does not do yet: fetch wanted books without a person.

---

### Increment 5g — wanted books fetched without a person ✅

[ADR-0050](docs/adr/0050-fetching-wanted-books-automatically.md) was written first.

| What | How |
|---|---|
| **One pass for both ladders** | The album pass became a runner both kinds share: `acquire.books` beside `acquire.albums`, registered only with `acquisition.automatic` on, on the search pass's interval — one lock, one gate, one budget, one back-off, one memory of failed fetches, one grab and one `system:acquire` audit line. What differs is the list and the search |
| **What is wanted** | The Wanted screen's books — monitored, no file — that have an author; one without is left alone and the screen says why |
| **No "once" rule** | An imported book has its file and is no longer wanted; a download that brought no book file was skipped, so another release may be tried. The queue still never yields the same release twice |
| **Kept, and shown** | A book's state is its item's `acquire_state` row, told from a film's by the item's kind — no migration. The Wanted screen's books carry the *automatic* line |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | 5f's in-flight check read a book download as an episode of the book's item | Read as the book (`acquire.TestAWantedBookIsFetched`) |
| 2 | Two mutants survived first: the "sealed as a book" check (the real search never makes the other shape) and one malformed mutation | `acquire.TestOnlyTheBooksOwnReleaseIsGrabbed` with a search that gets it wrong; the mutation rewritten |
| 3 | Telling a book's state from a film's joined `media_item` into `acquire.States` without saying why it reads unscoped — `library.TestEveryItemReadChoosesAScope` failed the build | The reason is on the query |

#### Verified

| Claim | How it is established |
|---|---|
| A wanted book with an author is searched for and the best of it grabbed, sealed and audited; downloading and held are left alone; a film's state stays a film's | `acquire.TestAWantedBookIsFetched` — mutation-verified |
| A book whose download brought nothing is looked for again, not the release tried | `acquire.TestABookWhoseDownloadBroughtNothingIsLookedForAgain` — mutation-verified |
| Only a release sealed as the book is grabbed, whatever the search says | `acquire.TestOnlyTheBooksOwnReleaseIsGrabbed` — mutation-verified |
| The Wanted screen's line for a book, and for one with no author | `api.TestTheWantedScreenSaysWhatWasDoneAboutABook` — mutation-verified |
| The album pass, now the shared runner, is unchanged | `acquire.TestAWantedAlbumIsFetched`, `TestAnAlbumIsFetchedOnlyByTheRules`, `TestAWantedAlbumIsStillWantedAtTheGrab`, `TestOnlyTheAlbumsOwnAcceptedReleaseIsGrabbed` |

Eleven mutations, all killed. **On the running binary**, with the real Open
Library, `acquisition.automatic` on and a Torznab stand-in on `127.0.0.1`:
*The Left Hand of Darkness* and *Dune* added, both *Not searched for yet*;
`acquire.books` — *searched for 2 of 2 due book(s): nothing for Dune (1965) by
Frank Herbert — the indexers found nothing; next in 6 hours; grabbed The Left
Hand of Darkness (1969) by Ursula K. Le Guin*, audited *automatically*; the
import filed the EPUB and the book left the Wanted list; the next pass, *no book
is due (1 wanted, 0 downloading, 1 waiting out a back-off)*. Every question was
category 7000.

**Phase 5 is complete**: music and books, each followed or added, scanned,
searched for, grabbed, imported and fetched automatically under the same rules
as films and series.

The `downloader` role exists as a flag and **refuses to start**, by design: it
will not run until the network-namespace guard from ADR-0001 exists.

---

## Phase 6 — The known gaps 🚧

Phases 0–5 built the stated scope. What DROPPED-FEATURES.md lists under *Not
yet* is worked through here, the gaps that mislead first.

### Increment 6a — the breach check and signup proof-of-work, which the configuration promised ✅

`auth.breach_check_enabled` defaulted to **true** and `registration.proof_of_work_bits`
to **18**; neither was read by any code. A switch that reads as on and does
nothing is worse than a missing feature.
[ADR-0051](docs/adr/0051-the-breach-check-and-signup-proof-of-work.md) was written first.

| What | How |
|---|---|
| **Breached passwords refused** | Wherever a person sets one over the network — the setup wizard, signup, a reset, a change — a password in Have I Been Pwned's corpus is refused with *it has appeared in a known data breach; choose another*. The length and variety rules decide first, so HIBP is never asked about a password the policy refuses anyway. Break-glass recovery at the host does not check |
| **k-anonymously** | Only the first five hex characters of the password's SHA-1 leave the host, to the range API, with `Add-Padding: true` so the answer's size says nothing; padding entries (count 0) are nobody's password; the match is made here. Through the `metadata` egress profile, five seconds |
| **Failing open** | HIBP unreachable: the password is accepted and a warning logged. An outage at a third party must not stop people setting passwords; the check raises the floor, it is not the floor |
| **Signup's proof-of-work** | `GET /api/v1/auth/signup/challenge` (anonymous — the surface is 15) hands out a challenge sealed with the master key: a random nonce, the difficulty, ten minutes. A signup must carry a counter for which `SHA-256(challenge:counter)` begins with that many zero bits; the server checks one hash **before** the breach check's request and the Argon2id, refuses a forged, expired or unsolved one, and spends it by its nonce. A challenge issued before the operator raised the difficulty pays the new price. A closed registration still answers exactly as a missing route does — the proof is asked for only after the mode is checked |
| **In the browser** | `auth.js` solves it with a SHA-256 written in the file (ADR-0011 loads nothing else; SubtleCrypto's promise per hash is far too slow), in slices so the page stays responsive: 18 bits is a few hundred thousand hashes, about a quarter of a second |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | The two switches themselves, documented as defaults since Phase 1 | Built |
| 2 | SECURITY.md still listed `POST /api/v1/issues` as unimplemented, though 4ah built it | Removed |
| 3 | The proof test's "forged" token flipped the first character to `A` — when it already was `A`, the forgery was the token and spent it, one run in sixty-four | Always a different character; run twenty times |
| 4 | One mutant survived first: nothing pinned how many zero bits `Solved` counts, so "half the bits" passed | `identity.TestSolvedCountsLeadingZeroBitsExactly` checks every difficulty to 16 against a bit-by-bit count |
| 5 | One mutant is equivalent: the challenge is decoded strictly, but its solution's hash already binds the exact spelling, so another spelling is unsolved either way | Kept strict, as the grab ticket is |
| 6 | gosec read *Passwords* in the Pwned Passwords address as a credential; and SECURITY.md's table of exceptions had fallen behind — the feed token's prefix and action names (4ag) and the album column (5d) were on their lines but not in it | A reason on the line; the table brought up to the code, twenty-five in all |

#### Verified

| Claim | How it is established |
|---|---|
| Five characters out, padding asked for, padding entries ignored, an error read as one | `identity.TestPwnedPasswordsIsAskedKAnonymously` — mutation-verified |
| The policy first, then the check; unreachable accepts; off is off | `identity.TestTheBreachCheckIsTheLastWord` — mutation-verified |
| Setup, signup, change and reset each refuse a breached password and say why; the refused reset keeps its token | `api.TestABreachedPasswordIsRefusedWherePeopleSetThem` — mutation-verified |
| A challenge is sealed, solved, spent once, expires, and pays today's price | `identity.TestASignupChallengeIsSealedSolvedAndSpent`, `identity.TestSolvedCountsLeadingZeroBitsExactly` — mutation-verified |
| A closed registration says nothing of the proof; an open one asks for it | `identity.TestAClosedRegistrationSaysNothingOfTheProof` — mutation-verified |
| A signup pays its proof through the router, once | `api.TestASignupPaysItsProofOfWork` — mutation-verified |
| The anonymous surface is 15, and the anonymous bundle names only anonymous routes | `api.TestAllowlistAndRegistrationsAgree`, `web.TestAnonymousBundleNamesNoAuthenticatedEndpoint` |

Twenty-three mutations killed, one equivalent. The page's SHA-256 matched
Node's on every padding boundary, and found 18 bits in 113,557 tries in 249 ms.
**On the running binary, against the real Pwned Passwords:** the setup wizard
refused *correct horse battery staple* and *Password123456!* in 0.1 s each and
accepted a fresh random one; a signup without the puzzle was refused; in
headless Chrome the signup page solved the 18-bit challenge and the request was
*submitted for review*, and a breached password there was refused with the
sentence.

---

### Increment 6b — the source, offered on every page ✅

ADR-0008 called offering the source to remote users "the cheapest AGPL
obligation there is to satisfy", and left it undone.
[ADR-0052](docs/adr/0052-offering-the-source.md) was written first.

| What | How |
|---|---|
| **Every page** | One layout renders every page, so every page — login, signup, reset and setup included, the ones a stranger sees — ends *CMediaStack `<version>` · Source code (AGPL-3.0)*, the version the one `-version` prints |
| **The address is the operator's** | `server.source_url` (or `CMS_SOURCE_URL`), the project's repository by default — right only for an unmodified build, as the configuration's comment says; whoever runs a modified one owes their own source |
| **Only a web address** | The lint refuses anything but an absolute `http` or `https` address with a host; a `javascript:` URL in a footer link is a script. `html/template` would neutralise one anyway, and a test holds it to that |

#### Verified

| Claim | How it is established |
|---|---|
| Every page offers the source with the version; a script address never renders as a link | `web.TestEveryPageOffersTheSource` — mutation-verified |
| The address is an absolute http(s) one, the project's by default | `config.TestTheSourceAddressIsAnAbsoluteWebAddress` — mutation-verified |

Six mutations, all killed. On the running binary, in headless Chrome, the login
page ends *CMediaStack dev · Source code (AGPL-3.0)*, linking to the repository.

---

### Increment 6c — one file deleted, one trashed file purged now ✅

Deletion worked on a whole title, and the trash emptied itself after a week
and not before. [ADR-0053](docs/adr/0053-deleting-one-file-and-purging-now.md)
was written first.

| What | How |
|---|---|
| **One file, to the trash** | `DELETE /api/v1/admin/files/{id}` under `library.delete`: the file moves to its root's trash and its record is forgotten; nothing is unlinked; the title stays, and what the file held — an episode, a film, a track, a book — is missing again, so wanted while it is monitored. Read through the caller's scope; audited as `media.file_deleted`; a file already gone from disk is forgotten and said |
| **One trashed file, now** | `POST /api/v1/admin/trash/purge` under `library.delete`: one entry the trash lists, unlinked whatever its retention, through the vault, whose own check of the destroy-bytes effect still applies; anything not in the trash is refused; audited as `media.purged` with what was freed |
| **Still two acts** | No "delete and purge" in one step and no "purge everything": a file is deleted to the trash, and only somebody looking at it there can unlink it now |
| **Screens** | *Delete file* on a file's row; *Purge now* beside *Put back* in the trash, which asks again on itself — *Click again to unlink for good* — and forgets the question after five seconds, with no browser dialog |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | The scope test's account held `admin.system`, which sees every library whatever its grants — so "out of scope" was not | Given browse and delete only |
| 2 | Two mutants are equivalent, and both are the design working: reading the file unscoped is still stopped by the scoped read of its title that follows; opening the purge route to a Manager still leaves the file, because the vault refuses the destroy effect to anyone without it | Recorded |

#### Verified

| Claim | How it is established |
|---|---|
| One file to the trash, the title kept, the record forgotten; out of scope not found; already gone, forgotten | `importer.TestDeletingOneFileTrashesItAndKeepsTheTitle` — mutation-verified |
| One trashed file purged now; nothing outside the trash; not by a principal that may not destroy | `importer.TestOneTrashedFileCanBePurgedNow` — mutation-verified |
| Both over HTTP, refused to a Manager, path-checked, audited | `api.TestOneFileIsDeletedAndOneTrashedFilePurgedOverHTTP` — mutation-verified |

Fourteen mutations: twelve killed, two equivalent. **On the running binary:**
*Heat (1995)* scanned in; its file deleted — moved to
`.cmediastack-trash/…-Heat.1995.1080p.BluRay.x264-GRP.mkv`, the title kept and
back on the Wanted list; *purge now* freed 20,971,520 bytes and the trash was
empty. In headless Chrome: *Delete file* on the file's row, then in the trash
*Purge now* → *Click again to unlink for good* → *The trash is empty.*

---

### Increment 6d — rotating the master key ✅

Every stored secret is sealed under `CMS_MASTER_KEY`, and SECURITY.md had
called rotation "design only" since Phase 1: a key that may have leaked could
not be replaced without every account re-enrolling its authenticator.
[ADR-0054](docs/adr/0054-rotating-the-master-key.md) was written first.

| What | How |
|---|---|
| **On the host** | `cmediastack -rotate-key`, beside `-recover` and `-restore-backup`: the old key from `CMS_MASTER_KEY`, the new from `CMS_MASTER_KEY_NEW` — the environment, never a flag. Refused while the server answers its readiness probe (it holds the old key), when the two are the same, or when the new one is not a key |
| **All or nothing** | Every authenticator secret, indexer API key, the metadata provider's token and the Discord webhook is opened under the old key with its own context and re-sealed under the new one, in one transaction; one that will not open changes nothing and is named. The audit log counts what was re-sealed, as `system.master_key.rotated` by *host*, and never a key |
| **Nothing escapes it** | Each package that seals a stored value exports its context, and the rotation uses those; a test reads every call that seals with the cipher and fails the build for one the rotation does not re-seal — the only exceptions being grab tickets and signup challenges, which expire in minutes |
| **Backups keep the old key** | Backups are encrypted under a passphrase derived from the key and are not rewritten; the command says to keep the old key with them, and what to do next |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | Three mutants did not compile at first (an unused variable, an unused import); rewritten, they were killed | — |
| 2 | The command's counts were misaligned by one long label | Widened |

#### Verified

| Claim | How it is established |
|---|---|
| Every stored secret re-sealed under its own context, and no longer opens under the old key | `main.TestTheMasterKeyIsRotated` — mutation-verified |
| One that will not open changes nothing, and is named | `main.TestARotationThatCannotOpenEverythingChangesNothing` — mutation-verified |
| The command's guards, its audit line without a key, and what it tells the operator | `main.TestTheRotateCommand` — mutation-verified |
| Every call that seals a stored value is rotated | `main.TestEverySealedValueIsRotated` |

Eleven mutations, all killed. **On the running binary:** an account enrolled
and an indexer added; `-rotate-key` while the server ran — *the server is
running: it holds the current key …*, exit 1; stopped, *Rotated: 2 sealed
value(s)* (one authenticator secret, one indexer key); started under the new key,
the existing authenticator's code signed in, the indexer's key opened, and the
audit log held *the master key was rotated: 1 authenticator secret(s), 1 indexer
API key(s) …*

---

### Increment 6e — a file's subtitle, fetched from OpenSubtitles ✅

Subtitles a file had were served since 4f; nothing fetched one, and the
`subtitle` egress profile had been configured and unused since Phase 1.
[ADR-0055](docs/adr/0055-fetching-a-subtitle.md) was written first.

| What | How |
|---|---|
| **The operator's own key** | OpenSubtitles.com refuses every search without an API key (*"You cannot consume this service"*, asked live), so it is off until one is entered on the Metadata screen (`admin.system`), with an optional account for a bigger daily quota — the token from signing in kept in memory only. The key and the password are sealed in the settings table and never returned; `-rotate-key` re-seals them, as 6d's structural test now insists. Languages are two-letter codes |
| **One file, on request** | `POST /api/v1/files/{id}/subtitles/fetch` with a language, under `library.edit`, scoped. *Subtitles (en)* on a video file's row, one button per wanted language |
| **Which subtitle** | Asked for the language, the title — a film's TMDB id, or an episode's series, season and number — and the file's OpenSubtitles hash, computed here through the root's vault (size plus the 64-bit sum of the first and last 64 KiB). Taken only in that language and, when it names one, for this title; an unidentified title takes only a hash match. A hash match first, then untranslated, then most downloaded |
| **What is written** | Asked for as SRT and refused unless it is one: UTF-8, at most 2 MiB, a `-->` timing line, not a web page. `<video stem>.<language>.srt` beside the video through the vault, never over an existing sidecar. The download link only over `https`, and the API key never sent to it — it may be another host. Audited as `media.subtitle_fetched`; the day's remaining downloads passed on |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | The client's first test expected the API key on every request — including the download link, which may be another host. The code was right; the test now asserts the key never follows the link | — |
| 2 | One mutant showed a gap: a Manager's refused configuration was in fact stored, the test seeing only the status read that followed being denied | The test asserts nothing was stored |

#### Verified

| Claim | How it is established |
|---|---|
| The hash, against values worked out by hand | `subtitles.TestTheHashIsOpenSubtitlesHash` — mutation-verified |
| Only an SRT is written | `subtitles.TestOnlyAnSRTIsTaken` — mutation-verified |
| The language, the title, a hash for an unidentified one; the ranking | `subtitles.TestTheSubtitleChosenIsTheFiles` — mutation-verified |
| The client: headers, a sorted lower-case query, one sign-in kept, quota and refusal said, an https SRT, the key never to the link | `subtitles.TestOpenSubtitlesIsAskedAsItAsks` — mutation-verified |
| Configuration sealed, never shown, an administrator's alone | `subtitles.TestOpenSubtitlesIsConfigured` — mutation-verified |
| A file's subtitle written beside it, never over one, scoped, audited | `subtitles.TestASubtitleIsFetchedForAFile`, `subtitles.TestAnEpisodeAndAnUnidentifiedTitleAreAskedForProperly` — mutation-verified |
| Through the router; a hidden file answers as a missing one | `api.TestASubtitleIsFetchedThroughTheRouter`, `api.TestATitleOutOfScopeDoesNotExist` |
| The new secrets are rotated | `main.TestTheMasterKeyIsRotated`, `main.TestEverySealedValueIsRotated` — mutation-verified |

Twenty-seven mutations, all killed. **On the running binary, against the real
OpenSubtitles:** before a key, *not configured* (409); with a wrong one, the real
API was asked in 0.2 s and the instance said *OpenSubtitles refused the API key
or the account* (502). In headless Chrome the Metadata screen shows *a key is set
· languages: en*, and *Subtitles (en)* on a file's row reports the refusal.
**Not verified live:** a successful download — that needs an OpenSubtitles API
key, which the operator enters; the matching and writing are verified against a
stand-in of the documented API.

What it does not do yet: fetch wanted subtitles without a person — the next
increment.

---

### Increment 6f — wanted subtitles, fetched without a person ✅

6e fetched a file's subtitle when someone asked. Bazarr's other half is the
sweep. [ADR-0056](docs/adr/0056-fetching-wanted-subtitles-automatically.md) was
written first.

| What | How |
|---|---|
| **A task with browse alone** | `subtitles.fetch`, every six hours, as `system:subtitles`. No background task holds `library.edit`; a sidecar beside a video changes no title, monitoring or existing file, so the sweep — a method no route reaches — writes it with browse, as identification records proposals. The person's fetch keeps its own check. Without a key or a language the pass asks nothing and says so |
| **What is wanted** | A film's or episode's file, in each wanted language, with no sidecar in that language (`.srt`, `.vtt`, `.ass`, `.ssa`, under its two- or three-letter code) and no embedded **text** track in it as the probe recorded it. A picture track (PGS, VobSub) does not count. Music and books are never wanted |
| **Budgeted, backing off** | Ten searches a pass: never-searched first, newest file first, then the longest waiting. Nothing found waits a day, doubling to thirty. OpenSubtitles unreachable or the key refused waits an hour; the day's quota waits a day and ends the pass, as a refused key does. Each outcome kept in `subtitle_search` (migration 0030), forgotten with the file |
| **The same fetch** | Through 6e's code: the same matching, the same SRT check, never over a sidecar. Audited as `media.subtitle_fetched` by `system:subtitles` |

#### Verified

| Claim | How it is established |
|---|---|
| Nothing asked without a key or a language; a sidecar or embedded text track in either spelling is enough; a picture track is not; music never | `subtitles.TestWhatSubtitlesAreWanted` — mutation-verified |
| The budget, the order, the back-off doubling, a fetch written and audited, the quota ending the pass | `subtitles.TestTheSweepIsBudgetedAndBacksOff` — mutation-verified |
| The sweep needs browse; the task holds exactly that | `subtitles.TestTheSweepNeedsBrowse`, `authz.TestEachBackgroundTaskHoldsExactlyItsOwnGrant` — mutation-verified |
| It reads the whole library knowingly | `library.TestEveryItemReadChoosesAScope` (an `Unscoped:` reason) |

Thirteen mutations, all killed. **On the running binary:** the task is
scheduled every six hours; before a key it said *OpenSubtitles has no key;
nothing was asked*, with a key and no language *no subtitle language is
wanted*. With a wrong key, English and French wanted, a film and an album on
disk, the real OpenSubtitles was asked once in 0.3 s and the pass ended:
*searched 1 of 2 due … stopped: OpenSubtitles refused the key* — two due, the
album never among them. Run again at once, the refused language waited and
only the other was asked. **Not verified live:** a successful download, as in
6e — that needs the operator's key.

---

### Increment 6g — packs of several seasons ✅

ADR-0033 refused every release naming more than one season, so a show only
released as one complete pack could not be fetched.
[ADR-0057](docs/adr/0057-multi-season-packs.md) was written first.

| What | How |
|---|---|
| **A person's choice** | A season's search now matches `S01-S05`, `Seasons 1-3`, `S01.S02` and `Complete Series` when the season searched for is among them, ticketed for the whole span. Automatic acquisition, the recent-release feed and the general search still refuse them: the season search takes several seasons only when told how far the series reaches, and only a person's search tells it |
| **The span** | The lowest and highest season the name gives; a complete series reaches the highest regular season the provider lists when the grab is made. Never season 0. "Complete Series" and "Seasons 1-3" are taken off the title before the series is compared |
| **The queue** | A season target gains a last season (`target_last_season`, migration 0031), sealed in the ticket; the database refuses one that does not follow its first. Labelled *The Wire (2002) S01-S02 (seasons 1 to 2)* |
| **The import** | Each file as the season and episode its own name says, against that season's listed episodes; a season outside the span is skipped; a bare `E03` is refused in a pack of several seasons, since its season cannot be told; one episode number in two seasons is two episodes |
| **In flight** | While it downloads or its import is unfinished, every season of the span is held, so automatic acquisition fetches none of them beside it |

#### Verified

| Claim | How it is established |
|---|---|
| The span a name gives, a complete series to its last | `release.TestTheSeasonsOfAPackAreItsSpan`, `release.TestSeveralSeasonsAreRecognised` — mutation-verified |
| Matched for a person's search only; the series, the season among the span, never specials; the target's shape | `search.TestAPersonsSeasonSearchTakesSeveralSeasons` — mutation-verified |
| How far the series reaches, specials aside | `library.TestASeasonIsFoundWithItsSeriesAndEpisodes` — mutation-verified |
| The span stored, replaced by a re-grab, refused out of shape by the store and the database | `download.TestAPackOfSeveralSeasonsRoundTrips` — mutation-verified |
| Each file filed under its own season; outside the span, unlisted, bare-numbered files left | `importer.TestAPackOfSeveralSeasonsFilesEachAsItsOwnSeason` — mutation-verified |
| Every season of the span in flight | `acquire.TestAPackOfSeveralSeasonsHoldsEachOfThem` — mutation-verified |
| Ticketed, named, and grabbed whole through the handlers | `api.TestAPackOfSeveralSeasonsIsTicketedAndGrabbedWhole` — mutation-verified |

Twenty-eight mutations, all killed, after two showed gaps in the import test:
one episode number in two seasons, and an episode listed in another season of
the span, are now both in it. **On the running binary**, with a Torznab
stand-in serving a real two-season .torrent and the series' episodes written
as a refresh would (this check has no TMDB key): season 2's search ticketed
*The.Wire.Complete.Series* as S01-S03 and *The.Wire.S01-S02* as S01-S02, and
refused *S03-S05*. The grab was queued as *The Wire (2002) S01-S02 (seasons 1
to 2)*. The import filed S01E01, S01E02, S02E01 and S02E02 under their own
seasons, and skipped the season-3 file (*grabbed for seasons 1 to 2*) and
`Episode 3.mkv` (*does not say which episode it is*).

What it does not do: grab one automatically, by design.

---

### Increment 6h — trackers searched from their Cardigann definitions ✅

ADR-0003 decided in Phase 0 that a tracker without a Torznab API is reached
through its Cardigann definition, consumed as data, and only the Torznab and
Newznab client was ever built: every other tracker still needed a Prowlarr or
Jackett in front of it. [ADR-0058](docs/adr/0058-cardigann-public-definitions.md)
was written first.

| What | How |
|---|---|
| **A pasted definition** | An indexer of kind `cardigann` keeps the definition YAML the operator pasted from Jackett's or Prowlarr's repository (`indexer.definition`, migration 0032). Never vendored, never fetched; a broken one is fixed by pasting the new one. An update without one keeps it; a change of kind drops it |
| **Checked when saved** | Every part this build cannot follow is named at once: a `login` block (only public trackers, for now), a `download` block, `preprocessingfilters`, `rows.dateheaders`, another method or response type, an unknown filter, template function, CSS feature or category, no title, neither a download nor a magnet field |
| **The request** | Each search path in turn, GET or POST, its inputs over the common ones; the definition's Go templates (`.Keywords`, `.Query.*`, `.Categories`, `.Config.*` from the settings' defaults, `if`/`range`/`and`/`or`, `join`, `re_replace`), a string literal read as written as Jackett reads it; keyword filters; the site's categories from the Newznab ones asked for |
| **The page** | HTML through `golang.org/x/net/html` — already in the module graph, now a direct requirement — and an in-tree CSS subset (type, class, id, the attribute forms, the four combinators, selector lists, `:nth-child`, `:first-child`, `:last-child`, `:contains`, `:has`, `:not`); or JSON by dotted path, nested arrays reading their parent with `..`. Fields in the definition's order, `text` templates reading the ones before; `optional`, `default`, `remove`, `case`; eighteen filters; `andmatch` |
| **The results** | The same `indexer.Result` a feed item becomes: links resolved against the base address and checked by the same rule, a row with no title or no usable link dropped, the size and row caps of a feed, sizes like *1.5 GB* and dates like *2 hours ago* read. Judged, ticketed and grabbed like any other |
| **The form** | *Cardigann definition* in the Indexers screen's protocol list, with a box for the YAML |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | The Indexers form's *Base URL* field had never been styled — `type="url"` was missing from the stylesheet's list of inputs — and drew as a bare browser box. Seen in the screenshot of the new form | Styled, with the new text box |
| 2 | Creating an indexer always answered *the API key is sealed under this indexer's id*, also when no key was given | Said only when a key was |

#### Verified

| Claim | How it is established |
|---|---|
| The request a definition describes, each row read into a result, unsafe links and rows missing a required field dropped, a path to another private host refused | `indexer.TestAPublicTrackerIsSearchedFromItsDefinition` — mutation-verified |
| A JSON tracker: POST, nested rows, parent fields, optional fields | `indexer.TestAJSONTrackerIsSearched` — mutation-verified |
| What cannot be followed is named when saved | `indexer.TestADefinitionIsCheckedWhenSaved` — mutation-verified |
| The CSS subset, and anything beyond it refused | `indexer.TestTheSelectorSubset` — mutation-verified |
| Filters, sizes and dates | `indexer.TestCardigannFiltersAndValues` — mutation-verified |
| The definition kept, required, dropped with the kind | `indexer.TestACardigannDefinitionIsKeptWithItsIndexer` — mutation-verified |
| Saved and listed through the router; refused with the reason | `api.TestACardigannIndexerIsSavedFromItsDefinition` — mutation-verified |

Fifty-one mutations, all killed, after eleven survivors were dealt with:

- three were malformed and rewritten;
- eight showed real gaps, now in the tests: a template literal with a
  backslash, a template's `<no value>`, a POST's empty query string, a titled
  row missing a required field, and selector cases separating `+` from `~`,
  `^=` and `~=` from `*=`, and `:has` from matching itself.

**On the running binary**, with an HTML tracker stand-in on the LAN:

- The definition was pasted and saved. A private tracker's was refused: *it
  signs in (a login block)*.
- A search for *Heat* asked the tracker `/search/Heat/?order=seeders&q=Heat`
  and came back with two results, with size and seeders. A third row, whose
  link pointed at the cloud metadata address, was dropped.
- The grab fetched the `.torrent` from the tracker and queued it.
- In headless Chrome, the Indexers screen shows the definition box only when
  *Cardigann definition* is chosen.

What it does not do yet: private trackers (signing in), and a download link
read from the details page.

---

### Increment 6i — signing in to a private tracker ✅

6h searched public trackers and refused a definition that signs in, which is
most of the ones worth having. [ADR-0059](docs/adr/0059-cardigann-sign-in.md)
was written first.

| What | How |
|---|---|
| **The operator's settings, sealed** | The values for a definition's settings — a username and password, or a cookie copied from a browser — are given when the indexer is saved, sealed together under the indexer's id (`indexer.settings_enc`, migration 0033), opened only on the search path, never returned: the listing says *signs in with saved settings* and nothing more. A value for a setting the definition does not declare is refused, against a pasted definition or the stored one. `-rotate-key` re-seals them, as its structural test now insists |
| **The methods** | `form` (the login page read, its form found, the form's own fields kept — not its buttons, nor a box left unticked — the definition's `selectorinputs` and `inputs` over them, submitted to the form's action), `post`, `get` and `cookie`; then the definition's `cookies`, its `error` selectors (the tracker's own message is the error) and its `test` page, whose selector must match. A captcha is refused when saved |
| **Credentials go only to the tracker** | Every sign-in request — the login path, a form's action, a submit path, the test page — must be at the base address's scheme, host and port, and passes the same URL check as a search. A form that posts elsewhere is refused before anything is sent |
| **A session in memory** | One cookie jar per indexer, kept in memory and never written; a restart signs in again, and a changed definition or setting starts afresh. A search that lands on the login page signs in once more and asks again. The grab of a `.torrent` goes through the same session |
| **The form** | *Tracker username*, *password* and *cookie* beside the definition box |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | **Every search of a signing-in tracker hung on the running binary.** Starting a session held the client's lock while asking for the indexer's HTTP client, which takes the same lock — but only when an egress guard is wired, which no test client had | Fixed; `indexer.TestASigningInTrackerIsSearchedThroughTheGuardedClient` signs in through the production client with a deadline, and fails with the lock taken twice |
| 2 | `books.TestOpenLibraryIsAskedPolitely` failed once under the race detector: it measured the spacing of requests where the server receives them, 136 ms against a 140 ms floor, while the client spaces them where they leave, at 150 ms | The test's margin allows for transit; the client was right |

#### Verified

| Claim | How it is established |
|---|---|
| The form filled in and submitted, the session kept, the test page checked, a lapsed session signed in again, the grab through the session, new settings a new session, the tracker's refusal its error, a silent failure caught by the test page | `indexer.TestATrackerIsSignedInToAsItsDefinitionSays` — mutation-verified |
| Credentials only to the tracker's own scheme, host and port | `indexer.TestCredentialsGoOnlyToTheTrackersOwnAddress` — mutation-verified |
| The cookie and post methods; undeclared settings refused | `indexer.TestACookieOrAPostSignsIn` — mutation-verified |
| Through the guarded client, without hanging | `indexer.TestASigningInTrackerIsSearchedThroughTheGuardedClient` |
| Sealed under their own context, never listed, kept, checked, dropped with the kind | `indexer.TestCardigannSettingsAreSealed` — mutation-verified |
| Never over HTTP; refused with the reason | `api.TestCardigannSettingsNeverComeBackOverHTTP` — mutation-verified |
| Re-sealed by `-rotate-key` | `main.TestTheMasterKeyIsRotated`, `main.TestEverySealedValueIsRotated` — mutation-verified |

Thirty-five mutations, all killed. Five needed more work:

- three were malformed and rewritten;
- one showed that the test page was never the only thing catching a failed
  sign-in, so a silent failure is now in the test;
- one showed nothing tested that the settings are sealed under their own
  context, so a sealed value moved into the API key's column is now refused.

**On the running binary**, with a stand-in tracker that signs in with a form:

- With a wrong password, the search said *signing in was refused: Wrong
  username or password*.
- With the right one, the listing said *has_settings* and never the password.
- The tracker saw:
  1. the login page fetched;
  2. a post with its hidden token;
  3. the test page;
  4. the search, signed in;
  5. the `.torrent` fetched for the grab, signed in.
- In headless Chrome, the form shows the sign-in fields beside the definition
  box.

What it does not do: captchas, two-factor sign-in, Cloudflare challenges, and a
download link only on a details page.

---

### Increment 6j — a download link read from the details page ✅

Some trackers list only a link to each torrent's details page, and their
definitions say in a `download` block how to find the real link there; 6h
refused them. [ADR-0060](docs/adr/0060-cardigann-download-from-the-details-page.md)
was written first.

| What | How |
|---|---|
| **At the grab, not the search** | The result keeps the details page's link. Grabbing it fetches that page — signed in, if the tracker signs in — tries the block's `selectors` in order, each with its `attribute` and `filters`, and takes the first that matches, resolved against the page. One more request, only for what is grabbed |
| **Checked as any link** | What the page gives goes on to the same download path a feed's link does: a magnet handed back unfetched, an http(s) link checked against the indexer's URL rule and every redirect re-checked. A page whose selectors find nothing fails the grab, saying so |
| **Refused when saved** | A block with a `before` request, the `infohash` form, no selectors, or a selector outside the CSS subset |

#### Verified

| Claim | How it is established |
|---|---|
| The details page read signed in, the selectors in turn, a filter on the link, a magnet handed back, a link to the metadata address or a details page at one refused, nothing found said | `indexer.TestADownloadLinkIsReadFromTheDetailsPage` — mutation-verified |
| What cannot be followed is named when saved | `indexer.TestADefinitionIsCheckedWhenSaved` — mutation-verified |

Thirteen mutations. Twelve killed, after three needed more work:

- two were malformed and rewritten;
- one showed that no test grabbed a details link that was itself a refused
  address, which a test now does.

The thirteenth was equivalent: the details page's link was being checked
twice, once where it was read and once by the download it goes on to. The
first check was deleted.

**On the running binary**, with the signing-in stand-in tracker listing only
details links: the tracker saw the search, then the details page `/t/1`,
then the `.torrent`, all signed in.

---

### Increment 6k — whether a series' new seasons are followed ✅

A new season started monitored unless every regular season was off. Nothing
said "keep these seasons, but not the next one" — an old show's revival went
straight onto the wanted list. [ADR-0061](docs/adr/0061-following-new-seasons.md)
was written first.

| What | How |
|---|---|
| **One switch, on by default** | `media_item.follow_new_seasons` (migration 0034). Off, a season the provider lists for the first time starts unmonitored with its episodes; the seasons already known keep what they are set to. It only ever adds a way to say no: a series with every regular season off still takes nothing new |
| **The route** | `PUT /api/v1/media/{id}/new-seasons` with `{"follow": …}`, under `library.edit`, scoped like every title route; a film is no series and answers as nothing. The series' seasons say whether it is on |
| **The page** | *New seasons are (not) followed* and its button above a series' seasons |

#### Verified

| Claim | How it is established |
|---|---|
| Off: the next season unmonitored, the known ones untouched; on again, the one after monitored; only a visible series, only with `library.edit` | `library.TestASeriesNewSeasonsNeedNotBeFollowed` — mutation-verified |
| The old rule still holds beside it | `library.TestARenewedSeriesTheOperatorDroppedStaysDropped` — mutation-verified |
| Through the router; a film refused; a hidden series as nothing | `api.TestASeriesNewSeasonsAreSwitchedThroughTheRouter`, `api.TestATitleOutOfScopeDoesNotExist` |

Ten mutations, all killed, two after being rewritten because they did not
compile. **On the running binary:** a series read *New seasons are followed*;
in headless Chrome, *Stop following new seasons* gave the note *A season the
provider lists for the first time will start unmonitored*, and the row held 0.
**Not verified live:** a season actually arriving, which needs a TMDB key — the
refresh is tested against the episode store directly.

---

### Increment 6l — an album held lossy, upgraded to lossless ✅

Automatic acquisition fetched a wanted album once and an album held as MP3
stayed MP3; films and episodes were upgraded to their cutoff (ADR-0036), albums
were not. [ADR-0062](docs/adr/0062-upgrading-albums-to-lossless.md) was
written first.

| What | How |
|---|---|
| **The same switch** | `acquisition.upgrades` upgrades albums too |
| **What is upgraded** | A monitored album holding a track in a lossy format the music import names — MP3, AAC, Vorbis, Opus; a file of no known format is not taken for lossy — that the wanted pass has nothing more to do for: one still missing tracks and never imported is that pass's, even when its budget ran out before it |
| **To what** | Lossless only, the import's own rule (`music.Better`): a release whose name says FLAC or another lossless format. A more popular MP3-320 is *not lossless, and an album is upgraded only to lossless*. Every rule of an album's grab still applies |
| **Seldom** | At most once a week from its last search — for an album just fetched, the search that fetched it — after the wanted albums, in the same task, with its own budget. The summary names it *an upgrade from MP3* |

#### Verified

| Claim | How it is established |
|---|---|
| Lossy upgraded, lossless or unmonitored not, a lossy release refused, weekly, off when upgrades are off | `acquire.TestALossyAlbumIsUpgradedToLossless` — mutation-verified |
| A week from the search that fetched it; the wanted pass's album not taken when its budget ran out | `acquire.TestAnAlbumUpgradeWaitsItsTurn` — mutation-verified |

Nine mutations. Eight killed, after three showed gaps the tests now cover: an
unmonitored album held lossy, a wanted album passed over by the wanted pass's
budget, and an album the wanted pass had just fetched. The ninth was
equivalent: a clause letting the weekly gap override the album's next-search
time duplicated what the week every unsuccessful upgrade records already did.
The clause was deleted.

**On the running binary**, with upgrades on, Dummy held as three MP3s, and the
Torznab stand-in offering the FLAC:

- the album task said *Upgrades: … grabbed Portishead — Dummy (1994) (an
  upgrade from MP3)*;
- the import replaced all three tracks with FLAC;
- the next pass said *no album holds a lossy track to upgrade*.

---

### Increment 6m — season folders, a choice per series ✅

Every episode was filed in a `Season NN` folder; a series kept flat elsewhere
gained one beside its files the first time an episode was fetched.
[ADR-0063](docs/adr/0063-season-folders.md) was written first.

| What | How |
|---|---|
| **A switch, on by default** | `media_item.season_folders` (migration 0035). Off, an episode imported for the series — grabbed alone or in a pack — is filed in the series' own folder under the same name |
| **Nothing moved** | Only what is imported from now on. The files already there stay, and the scan reads a series' episodes from their names in either layout, as it always did |
| **Set like the other switch** | `PUT /api/v1/media/{id}/season-folders` with `{"season_folders": …}`, under `library.edit`, scoped. 6k's switch and this one are now one setter over a fixed list of statements (`library.SetSeriesFlag`, `SeriesSettings`), so no column name is ever built from a value; the API's two routes share one handler. The series' seasons say both; the page has both buttons |

#### Verified

| Claim | How it is established |
|---|---|
| A grabbed episode and a pack's files filed flat when the switch is off | `importer.TestASeriesWithoutSeasonFoldersIsFiledFlat`, `importer.TestAnEpisodeIsNamedTheWayItsSeriesFolderIs` — mutation-verified |
| Set on its own, read back, an unknown switch refused | `library.TestASeriesSeasonFoldersAreSwitched` — mutation-verified |
| Through the router, the other switch untouched, the other switch's field refused; a hidden series as nothing | `api.TestASeriesNewSeasonsAreSwitchedThroughTheRouter`, `api.TestATitleOutOfScopeDoesNotExist` — mutation-verified |

Eleven mutations, all killed. **On the running binary**, with 6g's two-season
stand-in: season folders switched off (*No file already here is moved*), the
pack grabbed and imported, and S01E01, S01E02, S02E01 and S02E02 lay directly
in `The Wire (2002)/`.

---

### Increment 6n — daily series, known by their air dates ✅

A talk show or a news programme is released by date —
`The.Daily.Show.2026.10.02.Guest.Name` — and every such release was refused
(*named by air date, which this software does not match*): these series could
be followed but never fetched. [ADR-0064](docs/adr/0064-daily-series.md) was
written first.

| What | How |
|---|---|
| **A release dated the day an episode aired is that episode** | Wherever an episode is matched — a person's search, automatic acquisition, the recent-release feed — when the series matches and no season is named. Another day is another episode; a numbered release still matches by number. The year a dated release names is its air date's, not the series': `The.Daily.Show.2026.10.02` is not a 2026 show, while `The.Daily.Show.(1990).2026.10.02` is still another show than the one from 1996 |
| **As files arrive** | A download grabbed for an episode files a dated file of its day as that episode — another day's is refused, naming the episode it is. The scan files a dated file under the episode its series aired that day, and under none when it aired none, or two |
| **Searched by date** | A series' *daily* switch (`media_item.daily`, migration 0036, `PUT /api/v1/media/{id}/daily`, beside the other two): Torznab and Newznab are asked `season=2026&ep=10/02`, the convention Sonarr, Prowlarr and Jackett share; a Cardigann tracker gets the keywords with `2026.10.02` and `.Query.Season`/`.Query.Ep` to match. An episode with no air date is asked for by number |

#### Found along the way

| # | What | Status |
|---|---|---|
| 1 | The first automatic test grabbed nothing: the parser reads a dated release's year as the release's year, and `The.Daily.Show.2026.10.02` was refused as *a different series* — a 2026 show, not the one from 1996. The search test had passed only because its want had no year | The air date's year is not compared; a test with the series' year pins it |

#### Verified

| Claim | How it is established |
|---|---|
| The day's release matches, another day's and another show's do not, a dated name's year is the date's, a daily series is asked by date and another is not | `search.TestADailyEpisodeIsKnownByItsAirDate` — mutation-verified |
| Torznab asked `season=YYYY&ep=MM/DD`, a malformed date falls back; a Cardigann tracker's keywords and query | `indexer.TestADailyEpisodeIsAskedForByDate` — mutation-verified |
| A dated file grabbed for its episode imported, another day's refused naming it; the scan by date, not a special's, not two of a day | `importer.TestADatedFileIsTheEpisodeOfItsDay` — mutation-verified |
| Automatic acquisition by date for a daily series, by number otherwise, and from the feed | `acquire.TestADailyEpisodeIsFetchedByItsAirDate` — mutation-verified |
| A person's search knows the air date and asks by it when daily; the switch and the search subject; through the router | `api.TestAnEpisodeSearchKnowsItsAirDate`, `library.TestASeriesSeasonFoldersAreSwitched`, `api.TestASeriesNewSeasonsAreSwitchedThroughTheRouter` — mutation-verified |

Twenty-six mutations. Twenty-five killed, after six needed more work:

- four were malformed and rewritten;
- two showed gaps the tests now cover: a special airing the same day as an
  episode, and a dated name with a year of its own.

The twenty-sixth was equivalent: when a grab imported a dated file, it was
checked against the grabbed episode twice. The first check was deleted; the
remaining one also says which episode the file is.

**On the running binary**, with a Torznab stand-in offering The Daily Show's
2026-10-01 and 2026-10-02 releases:

- With the series made daily, a person's search for S30E120 (aired
  2026-10-02) asked `season=2026&ep=10/02&t=tvsearch`.
- The 10-02 release matched and the 10-01 one did not.
- The grab went in as *The Daily Show (1996) S30E120*, and the import filed
  it as `Season 30/The Daily Show (1996) - S30E120 [WEBDL-1080p].mkv`.

---

## Decisions locked

| # | Decision | ADR |
|---|---|---|
| 1 | Egress control via WireGuard in a network namespace; SOCKS5 secondary | [0001](docs/adr/0001-egress-control-wireguard-netns.md) |
| 2 | Go with `CGO_ENABLED=0` | [0002](docs/adr/0002-go-no-cgo.md) |
| 3 | Cardigann YAML consumed from upstream | [0003](docs/adr/0003-indexer-definitions-cardigann.md) |
| 3a | `anacrolix/torrent`; `qBittorrent-nox` documented fallback | [0003a](docs/adr/0003a-torrent-engine.md) |
| 4 | SQLite only for v1 | [0004](docs/adr/0004-sqlite-only-v1.md) |
| 5 | Direct-play-first; no tone mapping on Skylake; no ABR ladders | [0005](docs/adr/0005-transcode-policy-skylake.md) |
| 6 | No Jellyfin API shim | [0006](docs/adr/0006-no-jellyfin-shim.md) |
| 7 | Process boundaries for hostile-input executors | [0007](docs/adr/0007-process-boundaries.md) |
| 8 | AGPL-3.0 | [0008](docs/adr/0008-licensing.md) |
| 10 | Metrics exposition written in-tree rather than taking prometheus/client_golang | [0010](docs/adr/0010-metrics-in-tree.md) |
| 11 | Server-rendered shells, hand-written UI, **no JavaScript build step** — overrules the Phase 0 Vite assumption | [0011](docs/adr/0011-server-rendered-shells-no-build-step.md) |
| 12 | Exactly one route (`GET /{$}`) may redirect a denied navigation; every other route stays invisible | [0012](docs/adr/0012-root-redirect-carve-out.md) |
| 13 | How the egress guarantee is actually established: kernel first, structural test second, guard third | [0013](docs/adr/0013-egress-guard-design.md) |
| 14 | Download engine: peer dials constrained exactly, DHT/uTP derived from egress mode, grabs take a sealed ticket never a URL, queue persisted locally | [0014](docs/adr/0014-download-engine.md) |
| 15 | Library path containment by `os.Root` (kernel `RESOLVE_BENEATH`), not by Join-and-prefix; hardlink by default, never move | [0015](docs/adr/0015-library-path-containment.md) |
| 19 | Identification: the machine proposes and a person confirms; automatic acceptance needs an exact title, an exact year AND a margin over the runner-up; popularity ranks but never decides | [0019](docs/adr/0019-identification.md) |
| 18 | Metadata and artwork: a provider's string never reaches a path; containment without media authority (`library.Cache`); the bytes decide a file's type; a half-tunnelled egress config is refused; the credential is sealed and never readable back | [0018](docs/adr/0018-metadata-and-artwork.md) |
| 17 | Acquisition requests: approval makes a request grabbable and downloads **nothing**; visibility is a scope computed from the principal, not a permission; duplicate matching refuses to normalise meaning | [0017](docs/adr/0017-acquisition-requests.md) |
| 16 | Import selection by allowlist; the download is a contained source too; upgrades supersede to trash; background authority granted **per task**, each smaller than a shared set would be | [0016](docs/adr/0016-import-pipeline.md) |
| 20 | Playback: probe and remux in a jail, handed a descriptor never a path; direct play first; subtitles leave as WebVTT or not at all | [0020](docs/adr/0020-playback.md) |
| 21 | Migrating from Radarr is an identity source, not a second import pipeline: the files are found by the scan, the ids come from Radarr | [0021](docs/adr/0021-migrating-from-radarr.md) |
| 22 | Episodes: the provider is the only authority; files match by number; wanted is monitored ∧ aired ∧ absent; running series asked every run and the rest weekly, because series are renewed | [0022](docs/adr/0022-episode-tracking.md) |
| 23 | Episode search: a person starts it and chooses; only the episode itself gets a ticket; the episode is sealed into the ticket and the import files the download under that series | [0023](docs/adr/0023-searching-for-a-wanted-episode.md) |
| 24 | An indexer's own configured host and port may be on the operator's network; nothing a feed names may be; link-local never | [0024](docs/adr/0024-indexers-on-the-operators-network.md) |
| 25 | Adding a series: the provider's id names it, nothing touches disk, every episode is written with the monitoring choice in one transaction or not at all, the choice has no default, and a film waits for a search that carries it | [0025](docs/adr/0025-adding-a-series-before-it-is-on-disk.md) |
| 26 | Films: adding one is wanting it; its search judges every release by television, name, year present and year within one; it asks by folded title and year, not by IMDb id; the film is sealed into the grab, and the queue row says what kind of target it carries | [0026](docs/adr/0026-adding-a-film-and-searching-for-it.md) |
| 27 | One default quality profile, or none: every interactive search is judged by it when it names none, `0` asks for none, and only an administrator chooses it, audited; the default cannot be deleted | [0027](docs/adr/0027-a-default-quality-profile.md) |
| 28 | An approved request is satisfied by a library item an approver links it to; it is fulfilled when that item has a file — by import, by the link, or by a scan — and the item's name goes only to whoever may browse | [0028](docs/adr/0028-an-approved-request-is-satisfied-by-a-library-item.md) |
| 29 | Backups: the database only, as a checked snapshot, encrypted with age to a passphrase derived from the master key, confirmed by decrypting it; due by the age of the newest on disk; pruned by age above a floor, only by the schedule; never served or restored over HTTP; a restore writes a new file and ends every session and token in it | [0029](docs/adr/0029-encrypted-verified-backups.md) |
| 30 | Automatic acquisition: off until the configuration says on; the Wanted list and nothing else, films gaining a monitored switch; a person's matching, the default profile, then a machine's stricter rules — seeders, the queue as blocklist, one download per item, no name that fits two titles; recent releases once per indexer and a budgeted search with a back-off; nothing while the tunnel is down; its own grant, audited | [0030](docs/adr/0030-automatic-acquisition.md) |
| 31 | The audit log is read by the administrator only, on a hidden screen the data layer checks again; reading is not audited; every column, newest first, paged by id, filters refused when they cannot be; anonymous denials written up to 20 an hour from an address and 120 from all, an account's up to 60 and counted apart, the rest counted in one line an hour; no export, no trimming | [0031](docs/adr/0031-reading-the-audit-log.md) |
| 32 | Notifications: one Discord webhook, a sealed credential checked with Discord before it is kept and never read back; what is sent read from the audit log by a task that may read it and nothing else, chosen by category — titles off by default; a stranger's text in code spans with mentions off; at most three messages a minute, at least once; leaving a channel is said in it | [0032](docs/adr/0032-notifications-to-discord.md) |
| 33 | Season packs: a third target, a season; matched to exactly one season and no episode; imported file by file, each numbered by its own name, two claiming one episode both refused, never over a better file; grabbed by a machine only for a settled season it wants all of, and only once its `.torrent` is seen to hold it; a season's download holds every episode until its import is done | [0033](docs/adr/0033-season-packs.md) |
| 34 | Stalled downloads: no verified progress for `download.stall_after` (a day) while the process runs; progress kept on the queue row; automatic acquisition's given up — stopped, kept as the blocklist, its item wanted again — and a person's only marked; each written once as `acquisition.stalled`, Library news | [0034](docs/adr/0034-stalled-downloads.md) |
| 35 | A quality profile per title: NULL is the default; `library.edit` chooses, audited; a search for a title with no profile named is judged by the title's, then the default; automatic acquisition judges and ranks each item by its title's; a pass still needs a default | [0035](docs/adr/0035-a-quality-profile-per-title.md) |
| 36 | Upgrades: `acquisition.upgrades`, off by default and only with automatic acquisition; a monitored file below its title's cutoff, never a two-episode one; only a release `ShouldUpgrade` approves; after the Wanted list, each file at most weekly; replaced into the trash | [0036](docs/adr/0036-upgrades-to-the-cutoff.md) |
| 37 | Libraries are root folders; every existing account keeps all of them; a rating is TMDB's US certification or a person's, and unrated is above every ceiling; the scope is SQL on every read made for a person and hidden answers as absent; no grant wider than the grantor's; the queue is not scoped | [0037](docs/adr/0037-libraries-and-rating-ceilings.md) |
| 38 | The HLS play-session routes are removed — the session is the signed-in one and the bytes are the file routes; the library is searched by title, scoped, never the provider; a title's artwork is keyed by the title; an original download is its own permission, scoped and audited | [0038](docs/adr/0038-the-library-routes-left-unbuilt.md) |
| 39 | Roles below Admin are editable and keep their edit across restarts; admin.system, admin.users and admin.network stay Admin's and auth.login cannot be removed; an administrator creates an account with a one-time link, never a password; a person changes only their email, with the password, from a session | [0039](docs/adr/0039-account-administration.md) |
| 40 | One detailed health report of named checks, the worst deciding; the recent log records kept in memory behind redaction; settings read over HTTP and changed only in the file | [0040](docs/adr/0040-operations-routes.md) |
| 41 | A calendar and a feed behind a per-account token in the address: anonymous routes the handler authenticates, browse only, the account's scope, masked in logs; episodes a month back to three ahead; arrivals newest first | [0041](docs/adr/0041-feeds.md) |
| 42 | A problem is reported against a visible title and a kind, once while open; reporters see their own, editors every one in scope and resolve it | [0042](docs/adr/0042-reporting-a-problem-with-a-title.md) |
| 43 | Discover: four TMDB lists, fetched once in six hours for everybody, marked in-library by kind and requested by folded title, not shown to an account with a rating ceiling, no posters | [0043](docs/adr/0043-discover.md) |
| 44 | Music: an artist is a `media_item`, albums and tracks hang from it; a foreign-keys-off migration rebuilds a referenced table, checked by `foreign_key_check`; MusicBrainz politely, albums and EPs without secondary types, the earliest release's tracks; a ceiling governs films and series only | [0044](docs/adr/0044-music-artists-albums-and-tracks.md) |
| 45 | Music files: a file is a track by its number, then its title; an album by its folder's folded title; the scan follows only followed artists; the import files track by track, lossless over lossy, replaced into the trash | [0045](docs/adr/0045-music-files.md) |
| 46 | Albums: a release is the album when its folded name starts with the artist and the album and goes on with a year, a tag or nothing; a year only for a namesake; a format ladder, Unknown refused; the album sealed as a fourth target; imported by the music library | [0046](docs/adr/0046-searching-for-and-grabbing-an-album.md) |
| 47 | Wanted albums fetched automatically: a pass of automatic acquisition's own, its lock, gate, budget, grab and audit; only albums with a known track list; an album once | [0047](docs/adr/0047-fetching-wanted-albums-automatically.md) |
| 48 | Books: one at a time from Open Library, not by author; a work id; one request, retried only when the connection fails; wanted until a file holds it | [0048](docs/adr/0048-books-from-open-library.md) |
| 49 | A book's file: EPUB > AZW3 > MOBI > PDF, one a book; the scan keeps to a book's folder; a release is the book when its title is one run and the rest is the author, years and tags; a fifth sealed target | [0049](docs/adr/0049-a-books-files.md) |
| 50 | Wanted books fetched automatically: the album pass's runner with a different list and search; an author required; no once rule; a book's state is its item's row | [0050](docs/adr/0050-fetching-wanted-books-automatically.md) |
| 51 | The breach check: HIBP's range API, five characters out, padded, failing open, everywhere a person sets a password but recovery; signup's proof-of-work: a sealed, single-use hashcash challenge checked before any Argon2id | [0051](docs/adr/0051-the-breach-check-and-signup-proof-of-work.md) |
| 52 | Every page offers this build's source and version; `server.source_url`, the project's by default, an absolute http(s) address only | [0052](docs/adr/0052-offering-the-source.md) |
| 53 | One file deleted to the trash, the title kept and wanted again; one trashed file unlinked now, from the trash only; still two separate acts | [0053](docs/adr/0053-deleting-one-file-and-purging-now.md) |
| 54 | The master key rotated on the host with the server stopped: every stored secret re-sealed under its own context in one transaction or none; a test holds every sealed value to the rotation; backups keep the old key | [0054](docs/adr/0054-rotating-the-master-key.md) |
| 55 | A file's subtitle from OpenSubtitles with the operator's own key, sealed and rotated; matched by the title and the file's hash; only an SRT, beside the file, never over one | [0055](docs/adr/0055-fetching-a-subtitle.md) |
| 56 | Wanted subtitles fetched by a task holding browse alone; ten searches a pass, a day's wait doubling to thirty; the quota ends the pass | [0056](docs/adr/0056-fetching-wanted-subtitles-automatically.md) |
| 57 | A pack of several seasons matched by a person's season search only, sealed to its span (a complete series to the last listed season); each file filed under its own season | [0057](docs/adr/0057-multi-season-packs.md) |
| 58 | Public trackers searched from their pasted Cardigann definitions — HTML or JSON, an in-tree CSS subset, the definitions' templates and filters; anything else refused when saved, naming it | [0058](docs/adr/0058-cardigann-public-definitions.md) |
| 59 | A tracker signed in to as its definition says — form, post, get or cookie — with the operator's settings sealed and rotated, sent only to the tracker's own origin; the session in memory | [0059](docs/adr/0059-cardigann-sign-in.md) |
| 60 | A download link read off the tracker's details page when the release is grabbed, by the definition's selectors, and checked as any link | [0060](docs/adr/0060-cardigann-download-from-the-details-page.md) |
| 61 | A series' *follow new seasons* switch, on by default, adding a way to say no beside the every-season-off rule | [0061](docs/adr/0061-following-new-seasons.md) |
| 62 | With upgrades on, an album held lossy is searched weekly for a lossless release, after the wanted albums; lossless over lossy is the only upgrade | [0062](docs/adr/0062-upgrading-albums-to-lossless.md) |
| 63 | A series' *season folders* switch, on by default, for what is imported from now on; nothing is moved | [0063](docs/adr/0063-season-folders.md) |
| 64 | A release dated the day an episode aired is that episode, wherever it is matched or filed; a daily series is searched by date | [0064](docs/adr/0064-daily-series.md) |
| 9 | Opaque server-side sessions, not JWTs | See the header comment in `internal/identity/session.go` — immediate revocation is a hard requirement, and a self-contained token cannot do it without the database lookup a JWT exists to avoid |

### Defaults taken in the absence of an answer — override any of these

- **AGPL-3.0.** Reversible while you are the sole copyright holder.
- ≤25 users, ≤100k library items.
- **No custom post-processing scripts, ever.**
- Trash window 7 days; playback history retention 180 days.
- Session rotation every 15 minutes, 30-second grace.
- Module path `github.com/jakethecake75/cmediastack` — one `sed` to change.

---

## Risks being carried

| Risk | Severity | Status |
|---|---|---|
| Schedule: 1.8–3.5 person-years at specified parity | **High** | Phase order makes each gate independently valuable |
| Release parsing complexity underestimated | High | Phase 2, with buffer |
| `anacrolix/torrent` parity gaps | Medium | Verified in 2e: builds `CGO_ENABLED=0` with `go-libutp` unlinked; `AddDialer` appends, so `DialForPeerConns=false` is what makes ours exclusive |
| Seeding is distribution, and §13 puts its legality on the operator | **High** | Stated in ADR-0014 and in the code. With no recorded obligation the instance seeds **indefinitely** — `download.seed: false` is the one switch that stops it |
| NordVPN has no port forwarding → passive peer | Medium | Provider limitation; accepted residual |
| i5-6500T cannot tone-map HDR | Medium | Designed around; 4K HDR is direct-play-only |
| Public exposure + open registration | Medium | Caps enforced by config lint; authenticating proxy still recommended |
| **An instance can have only ONE administrator** | Low (was Medium) | Every role assignment is strictly downward and role editing is unbuilt, so the setup wizard's Admin is the only one there will ever be. **Mitigated in 3g** by break-glass recovery from the host (`-recover`), which needs no second admin and works whether or not you planned ahead. Letting an Admin mint an Admin was considered and **rejected**: a peer cannot be demoted, so it would turn a stolen session into permanent independent access |
| Reuse detection revokes the victim's session too | Low | Deliberate: a detected theft ends the session. Worth revisiting if it fires on flaky mobile networks |
| `modernc.org/sqlite` slower than the cgo driver | Low | Irrelevant at this scale |

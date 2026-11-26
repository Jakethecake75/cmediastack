# Design loop

Repeated visual design passes on the web interface. Rule #1: functionality does
not change. Screenshots: `node scripts/screenshot.js <label>` builds the
binary, starts a fresh throwaway instance in WSL (`scripts/live.sh`, port 8090),
walks setup → enrollment → every view, and writes `screenshots/<label>/`.

Files in scope: `internal/web/assets/app/app.css`, `internal/web/assets/auth/auth.css`
(styles); templates and scripts only where markup must change, never IDs,
classes, `data-*` attributes, routes or field names that scripts depend on.
Constraints the tests in `internal/web/web_test.go` hold: no inline styles or
scripts in templates (CSP), no markup built from strings in scripts.

## FUNCTIONALITY CHECKLIST

Anonymous pages (`auth` bundle: auth.css, auth.js, remote.js)
- [ ] `/setup` — form `#setup` (username, email, password) → POST /api/v1/setup, then redirects to /login
- [ ] `/login` — `#credentials` (username, password) → `#second-factor` (code) step; `#back` "Start over"; `#error` alert; links to /signup and /reset
- [ ] `/signup` — form `#signup` (username, email, password, invite, note), challenge fetched first; `#done` message
- [ ] `/reset` — `#initiate` (username); `#complete` (password) with a token; `#done`
- [ ] `/enroll` — QR `#qr`, `#manual` details with `#secret` + `#copy-secret`, `#uri-link`; `#confirm` (code) → `#codes` list, `#copy-codes`, `#go-app` → /#start; `#logout`
- [ ] Footer: version + Source code link (AGPL) on every page
- [ ] Theme follows `localStorage['cms-theme']` (light/dark) on auth pages too

App shell (`/`, app bundle: app.css, app.js)
- [ ] Top bar: brand, `#nav` section buttons (Home / Request / Settings, `data-section`, `aria-current`), `#whoami`, `#theme` toggle (aria-pressed, persists), `#logout`
- [ ] `#subnav` section tabs (`data-target`, `aria-current`), Settings grouped (`.subnav-group`: Setup, Library, People, Your account, System)
- [ ] Hash routing: `#<view>`, `#title/<id>`; old ids (add, ask, search, requests, discover) open `#find`; unknown → first tab
- [ ] `#error` / `#notice` banners
- [ ] `.content.wide` on home, library, title

Views (`section.view[data-view]`)
- [ ] home — `#home-rows` shelves of posters; empty state
- [ ] library — `#library-kind`, `#library-filter`; poster grid → `#title/<id>`
- [ ] title — title page, Play → watch, report problem
- [ ] watch — player controls `#pl-back`, `#pl-play`, `#pl-back10`, `#pl-fwd10`, `#watch-seek-range`, `#pl-volume`, `#pl-mute`, `#pl-cc`, `#pl-speed`, `#pl-settings`, `#pl-info-btn`, `#pl-cast`, `#pl-pip`, `#pl-full`; TV remote focus
- [ ] find — `#find-form` (`#find-kind`, `#find-term`, `#find-year`, `#search-profile`, `#find-clear`); requests list; `#request-form` (details "Ask for it in words"); problems list + `#issues-all`
- [ ] queue (Downloads) — list (API currently 501 → error shown; pre-existing)
- [ ] wanted — list
- [ ] start — Getting started checklist
- [ ] storage — `#root-form` (`#root-path`, `#root-kind`, `#root-label`); root list
- [ ] indexers — `#indexer-form` (name, url, kind, username, password, cookie, key, ratio, hours); list
- [ ] metadata — `#metadata-form` (`#metadata-token`); `#subtitles-form` (key, user, password, languages)
- [ ] network — `#proxy-form` (address, user, password), `#proxy-current`, `#proxy-code`
- [ ] notifications — `#notifications-form` (`#notifications-webhook`, `#notifications-categories`)
- [ ] identify — `#identify-run`; list
- [ ] migrate — import from another server
- [ ] users (Accounts) — `#create-user-form` (username, email, role, libraries, rating ceiling); account cards; role permission editors + Save permissions
- [ ] approvals — pending list
- [ ] invites — `#invite-form` (`#invite-role`, `#invite-ttl`, `#invite-note`, `#invite-auto`); list, revoke
- [ ] security — `#password-form`, `#email-form`, `#recovery-form`
- [ ] sessions — list, revoke
- [ ] tokens — `#token-form` (`#token-name`, `#token-ttl`); list, revoke
- [ ] tasks — list, run now
- [ ] backups — list, take a backup
- [ ] health — checks; `#logs-form` (`#logs-level`, `#logs-text`)
- [ ] audit — `#audit-filter` (`#audit-category`, `#audit-outcome`, `#audit-actor`, `#audit-text`); list
- [ ] Remote (`remote.js`): TV/remote key handling on every page

## DESIGN SYSTEM

Set in pass 1 (2026-11-25). Tokens live in `:root` of both app.css and
auth.css (the bundles share no file by design, so the values are kept in step
by hand: change one, change both).

Palette (dark default / light via `data-theme="light"`)
- Surfaces: `--bg` #101010 / #f4f6f9, `--surface` #1a1a1a / #fff, `--surface-2` #242424 / #eef1f5
- Lines: `--line` #303030 / #d8dee6 (dividers, card edges); `--field-line` #6b6b6b / #8a94a3 (form fields, ≥3:1)
- Text: `--text` #f0f0f0 / #1b2027, `--muted` #9a9a9a / #5b6673
- Accent: `--accent` #4c8dff / #1a5fd0 (links, focus, current tab); `--accent-strong` #2b6be0 / #1a5fd0 (filled buttons, white text ≥4.5:1); `--accent-hover` #2560cc / #164fae
- Status: `--ok` #5bd68a / #1e7a44, `--danger` #ff6b6b / #c62828, `--warn` #ffc857 / #8a5d12, with tints `--ok-bg`, `--err-bg`, `--warn-bg`; `--warn-text` for warning copy
- The player is always dark (picture, not page).

Type (system UI stack, `--mono` for codes, keys, permissions, task names)
- 11 badge/eyebrow (uppercase, +0.05em) · 12 fine print · 13 labels, meta · 14 hints · 15 body · 17 h3 section · 20 shelf title · 22 auth h2 · 24 page h2 · clamp() for title-page display
- Hints max 72ch, line-height 1.55; body 1.5; headings 1.25–1.3

Spacing: 4 · 8 · 12 · 16 · 24 · 32 · 48. Bars pad sideways by `--chrome-pad` = clamp(16px, 4vw, 72px).

Radii: 6 small chips/codes · 8 controls · 10 (`--radius`) cards · 999 pills (badges, subnav tabs, Play)

Components
- Buttons: `.primary` filled accent-strong; `.ghost` outlined; `.danger` outlined red; unclassed = secondary (surface-2 + field-line). All have hover; disabled 0.55.
- Focus: 2px `--accent` outline, offset 2px, on input/select/textarea/summary/button/a.
- Cards: `.item`, `.inline-form`, `.stat`, `.find-bar` = surface + 1px line + radius 10, padding 14–16.
- Forms: fields stack (block), max 360px (numbers 10rem); label 13/600 muted; buttons 16px below.
- Badges: pill, 11px uppercase; good/bad/warn = status colour on its tint.
- Navigation: section bar with current = surface-2 + 2px accent underline; tabs = pills, current = accent text + accent border; on phones the tabs scroll sideways and the open one is scrolled into view.
- Empty states: dashed line box, centred muted text, balanced wrap.

## DESIGN BACKLOG

Priority order. (H) high, (M) medium, (L) low.

1. (H) Seed a demo library in `scripts/screenshot.js` (root folders + titles with artwork, a request, an issue, a pending signup, an invite) so Home, Library, Title, Watch, Approvals and Invites can be audited with real content instead of empty states.
2. (M) Getting started: six equal blue "Set it up" buttons; make the not-yet-current steps secondary so the next step stands out.
3. (M) Accounts: neutral notes ("This is you…", "Holds every permission, always.") use warning amber `.why`; decide which `.why` lines are cautions and tone the informational ones to muted.
4. (M) Health and Tasks: a failing/warn card is marked only by a small badge; add an edge marker (e.g. `:has(.badge.warn)`), and make "Overall: warn" stand out (needs a class from JS).
5. (M) Network: the "Egress is NOT being enforced" warning card looks like the status cards around it; give it the warn edge.
6. (L) Downloads: the "Finished" heading is left with nothing under it when the queue API fails (501 today).
7. (L) Theme button: says "Light mode" with `aria-pressed="true"` while dark is on; screen readers announce it inverted. Script/markup change: needs care under rule #1.
8. (L) Search has no page h2, unlike Library/Downloads/Wanted.
9. (L) Enroll: the QR and manual-entry box are indented by the step list's padding, so the QR is not centred on the card.
10. (L) Network "Per subsystem": six one-line cards are tall; Identify's "Run a pass now" sits far right while other views' actions sit left.
11. (L) Accounts on phones: each role's permission list is ~1000px tall; consider two columns at 375 or a collapsible list.
12. (L) app.css mixes px and rem and several radii; fold remaining literals into the scale above.

Not design (found while verifying, pre-existing, left alone): creating an API token
with the form's default scopes as a fresh admin is refused with "requested scope
exceeds your own permissions" (same on the untouched UI).

## DONE

Pass 1 (2026-11-25), commit "style: design pass 1 …"
- [x] Light theme status colours redefined (ok/danger/warn) — red/green/amber text and badges now ≥4.5:1 on white
- [x] Filled buttons use `--accent-strong` — white label ≥4.5:1 in dark (was 3.2:1)
- [x] Field borders `--field-line` — inputs visible (≥3:1) in both themes, app and auth
- [x] Hover states for primary/ghost/danger/unclassed buttons, nav and tabs; auth links
- [x] Unclassed buttons (Back up now, Audit Show, Notifications Save) styled as secondary, not browser grey
- [x] Focus ring on textarea and summary too
- [x] One `.badge` rule (two conflicting ones merged): pill, tinted status variants
- [x] Type scale: page h2 24, section h3 17, hints 14 at ≤72ch
- [x] Current section/tab clearer: accent underline on section, accent pill on tab
- [x] Phone top bar two rows (was three); tabs scroll sideways (were six rows), open tab scrolled into view
- [x] Bars aligned with wide views' gutter (`--chrome-pad`); brand at full contrast
- [x] Forms: fields stack, button never beside the last field; no extra top gap; status card and form card no longer touch; number fields narrower
- [x] View-level action rows spaced (Back up now, leak test, Older)
- [x] Toolbars aligned (Library "Show", Audit filters); Health log filter on one row, log fields in mono
- [x] Tasks: Run now beside each task, names in mono — page much shorter
- [x] Security account cards one row on desktop, two-up on phones
- [x] Access picker (libraries + rating ceiling) a tidy two-column block; role permissions read down columns, ticked ones brighter, locked ones dimmed
- [x] Notification categories: name reads as the card title
- [x] Audit log: denied/failure lines marked at the left edge; badge flush left
- [x] Footer: themed colour and link colour (was browser purple); auth pages no longer scroll by the footer
- [x] Auth: "Copy all" no longer wraps; recovery codes two-up on phones; footer links bigger targets; brand full contrast; "optional" readable

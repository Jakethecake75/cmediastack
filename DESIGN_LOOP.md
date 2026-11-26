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

(empty — pass 5 is a verification pass: a whole-site review; if it finds nothing material, STATUS: COMPLETE)

## DEFERRED — needs a script or markup behaviour change (outside rule #1)

Each of these would change what a script does or add content, so the design
loop leaves them for a deliberate change by the owner:
- Title page: no Home/Library tab is marked while a title is open (keep the tab the person came from).
- Health: "Overall: warn" and warn/error log records need a level class from JS to stand out.
- Theme button: "Light mode" with `aria-pressed="true"` while dark is on reads inverted to screen readers.
- Search has no page h2 (adding one adds content).
- Episode cards: missing / to air / announced share one look (needs a state class from JS); missing episodes are `disabled`, so a keyboard or TV remote cannot reach them (`aria-disabled` would change behaviour).
- Won't do: a badge that wraps to its own line keeps its 8px margin (cosmetic, phones only); role permission lists are ~1000px tall on phones (that is the content; columns are too narrow at 375); px/rem unification (no visible effect).

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

Pass 2 (2026-11-25)
- [x] Screenshot seed: 6 films + 2 series (placeholder files, scanned); film and series title pages shot
- [x] Fixed what pass 1 worsened: locked permission reasons readable (only the box dims); unticked options no longer look disabled outside role lists; odd account card spans the row on phones; Tasks on phones keep full-width text
- [x] Missing-picture posters: tinted card, title set like a cover, hidden from screen readers (caption says it); title page shrinks the empty poster so the title leads
- [x] Phone gutter shared by bars and wide views; shelf/grid values on the scale; tiles: hover lighter than keyboard focus
- [x] Library toolbar sized like a filter bar; "Files and options" no longer looks disabled
- [x] Settings tabs: each group (title + tabs) wraps as one; no width jump on the open tab
- [x] Warnings as states: Search warning, Downloads engine-off, Network/Backups warnings, Wanted automatic line; empty "Finished" heading hidden
- [x] Health/Tasks: trouble marked at the card edge; health checks in a grid
- [x] Getting started: only the first step to do is the blue call to action
- [x] Role "Holds every permission" note muted (information, not warning); ceiling caution spaced from the next hint
- [x] Audit filters on a grid with a primary Show (matches Health); Health log filter tidy on phones; Search fields even on phones, controls one height
- [x] Footer sits at the window's foot on short pages; spacing fixes (notification Save, backups note, Downloads headings, resolved-issues checkbox)
- [x] Fields 16px on phones (no zoom on focus), app and sign-in pages
- [x] Sign-in pages: hints 14px, code fields in mono, recovery codes 5 across on desktop, QR centred on the card, manual-entry hover
- [x] Backlog done: Getting started (#2), Accounts notes (#3), Health/Tasks edge (#4), Network warning (#5), Downloads heading (#6), Enroll QR (#9), compact lists (#10)
- [x] Won't do: Identify's "Run a pass now" on the right is a normal count-left/action-right toolbar

Pass 3 (2026-11-25)
- [x] Screenshot browser serves generated artwork + descriptions (`mockArtwork` in screenshot.js; server untouched); one film left bare; title pages and the player now shot
- [x] Fixed what earlier passes worsened: player volume back to white, player text buttons lost the secondary frame; no-artwork title page no longer squashed on phones
- [x] Player: white 3px focus ring on controls and menus (TV remote), message reads as the message (17/600, capitalised)
- [x] Missing-picture titles hyphenate instead of clipping; shelves run to the window edge with room for the focus ring
- [x] Series page: "Nothing on disk yet" sits by the facts, not floating
- [x] Phones: every view shares the bars' 16px gutter; banners keep page margins; library captions two lines
- [x] Light theme: cards lift (soft shadow), fields white not grey, field edge #7d8796 (≥3:1 on page), empty-state edges visible, missing-poster card darker than the page, lighter poster shadow, disabled boxes visible
- [x] Sign-in pages: error/ok/warn tinted like the app's; ghost buttons use the field edge
- [x] Consistency: Notifications "What is sent" Save is primary; Downloads engine-off box matches other warnings; Storage folder warning marked; Audit filter is a card like Health's; view-level buttons 12px below their text; Wanted note muted
- [x] noscript banner uses fixed amber (readable whatever the theme)
- [x] Won't do: `.item .title` as flex (would collapse spaces between inline text runs)

Pass 4 (2026-11-25)
- [x] Screenshot browser mocks a series' seasons (on disk, missing, to air, announced); app views reload at each width so phone shots match a phone
- [x] Episodes not on disk: picture dimmed, caption readable (was ~2.9:1); wide placeholders no longer repeat the caption; episode names 15/600
- [x] "S1:E1 - Low Tide" is a muted caption for Play, not a second tagline
- [x] Player: Info on the transport row (three rows of controls), message box fits its text, Info hover, year on the scale; `#pl-message` role="status"
- [x] One warning look everywhere (Search, Network, Backups, Storage, Downloads): amber edge, 3px left bar, tint, rounded
- [x] Library grid tiles top-aligned (captions of different length no longer stagger rows)
- [x] Accounts "This is you…" muted like the role note
- [x] Audit text filters labelled (`aria-label`); dead `var(--x, #fallback)` colours removed

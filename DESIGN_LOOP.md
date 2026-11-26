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

(set in the first pass — see below)

## DESIGN BACKLOG

(empty)

## DONE

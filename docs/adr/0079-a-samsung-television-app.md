# ADR-0079: A Samsung television app, and the web interface learns a remote

**Status:** accepted
**Date:** 2026-11-24
**Related:** [ADR-0011](0011-server-rendered-shells-no-build-step.md), [ADR-0072](0072-a-full-screen-player-and-cheaper-playback.md), [ADR-0077](0077-casting-to-a-chromecast.md)

## The problem

The operator wants CMediaStack on the living-room television, a 2024 Samsung
(UN43DU7200, Tizen 9.0, armv7), as an app of its own rather than a cast from a
phone. A television has no mouse and no keyboard: a remote with four arrows,
OK and Back. The web interface was built for a pointer. Only the player knew
any keys, and it used all four arrows for seeking and volume.

## Decisions

### 1. The app is a launcher; the server is the app

`tizen/` is a Tizen web application of three files. The first time it runs it
asks for the server's address and keeps it; from then on it opens that
address in its own window, so the television runs the interface the server
serves.

- **Not a copy of the interface.** Packaging `assets/app` into the app would
  mean a second build of the interface on every release, signed and installed
  by hand, and a television running an interface older than its server's API.
  A launcher is installed once.
- **Not a frame.** The server's policy says `frame-ancestors 'none'`, and that
  stays: the page is the top window.
- **The address is the person's.** The app names no server, so its policy
  allows any origin (`<access origin="*">`, `allow-navigation *`). It holds
  two privileges, the network and the remote's keys, and no other.
- **Changing the server is reinstalling the app.** A setting for something
  done once would be a screen to build and test for nobody.

Installing it needs the television in developer mode and a Samsung
certificate; `tizen/README.md` says how. It is not published in Samsung's
store.

### 2. The remote's keys belong to the web interface

`assets/auth/remote.js` is loaded by every page, the sign-in pages and the
app alike, before the page's own bundle. On anything that is not a
television (its user agent names neither Tizen, SMART-TV nor Web0S) it does
nothing at all. On a television:

- **An arrow moves to the nearest thing that can be chosen that way.** A
  candidate must lie wholly beyond the focused element's edge; distance along
  the way counts once and across it three times, so Down from a poster picks
  the one under it. Nothing that way: focus stays.
- **A field keeps the arrows it moves in**: a slider its left and right, a
  text field with text in it its left and right, a text area all four.
- **With nothing focused**, the first arrow starts in the open view.
- **OK** is the browser's own: it presses the focused button or link.
- **Back** is Escape in the player (which closes its menu, then leaves it);
  on Home or the sign-in page it leaves the app; elsewhere it is the
  browser's Back, and the views are in the history already.
- **Play, pause, fast-forward and rewind** are asked for with the
  television's own API when the page has it, and handed to the player as the
  keys it already knows. A website opened inside an app may not be given that
  API; then those keys stay the television's.

It listens on `window`, which hears a key after every listener on `document`,
so a key the player has used is left alone.

### 3. The player on a television

The arrows have to reach the player's controls, so on a television: left and
right seek only from the picture itself, up and down move (the television
has its own volume), OK on the picture plays and pauses, and opening the
player puts the focus on the picture. A control reached by keys brings the
controls into view, on any device.

### 4. One file crosses the bundle boundary, in the safe direction

The two bundles share no file (ADR-0011), so that the anonymous one cannot
carry the app's API client. `remote.js` lives in the anonymous bundle and the
app loads it: anonymous code inside the authenticated page. It is kept to
keys and focus by `web.TestTheRemoteCallsNoAPI`, which fails on any API path,
request, storage or navigation in it, and `web.TestEveryPageRenders` allows
exactly that one crossing.

## What is not done

- **Other televisions.** LG's webOS would take the same remote code (its Back
  key, 461, is handled) and a launcher of its own; none is built.
- **Text larger for the distance.** The interface is drawn as for a desktop
  at 1920 pixels.
- **The store.** Samsung's review is for apps that name their service; this
  one is installed from a computer in developer mode.

## Verification

- `web.TestEveryPageRenders`: every page loads `remote.js` before its bundle,
  and loads nothing else from the other bundle — mutation-verified.
- `web.TestTheRemoteCallsNoAPI` — mutation-verified.
- The remote's movement, fields and Back were checked in headless Chromium
  with a Tizen user agent, on a test page: ten cases, each of five mutations
  of `remote.js` failing one. The project carries no JavaScript test runner
  (ADR-0011), so this check is not in the tree.
- The launcher was packaged, signed with a Samsung certificate, installed on
  the UN43DU7200 over the network and launched.

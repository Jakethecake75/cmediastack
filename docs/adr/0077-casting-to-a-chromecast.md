# ADR-0077: Casting to a Chromecast from the player

**Status:** accepted
**Date:** 2026-10-05
**Supersedes:** [ADR-0073](0073-player-loading-torrent-resume-casting.md) decision 3, and [ADR-0074](0074-sync-subtitle-cache-cast-button.md) decision 3
**Related:** [ADR-0041](0041-feeds.md), [ADR-0037](0037-libraries-and-rating-ceilings.md)

## The problem

Pressing Cast said *"This video cannot be cast from here. The browser's own
menu can cast the whole tab"*. The operator wants to cast from the player, as
Jellyfin, Emby and the streaming sites do, not cast the tab.

The button used the browser's Remote Playback API. Chrome cannot hand a
Media Source stream to a Chromecast, and since ADR-0073 every converted
stream is one, so the API refused.

ADR-0073 rejected the Cast SDK for three reasons. The operator's setup answers
two of them:

- **The certificate.** The server is reached through Nginx Proxy Manager with
  a Let's Encrypt certificate, which a Chromecast trusts.
- **The cookie.** Answered below with a signed link.
- **Google's script.** Allowed, by exact paths.

## Decisions

### 1. The Chromecast fetches the film by a signed link

`POST /api/v1/files/{id}/cast` (needs `media.stream`, for a file the caller
can see) returns two addresses:

- `/api/v1/cast/{token}/convert`
- `/api/v1/cast/{token}/stream`

The token is `file.account.expiry` plus an HMAC-SHA256 over them, with a key
made at start-up and kept nowhere. A link:

- names **one file and one account**;
- lasts **six hours**;
- dies at a restart; the player asks for a new one each time it casts.

The two anonymous routes check the token. They then act as that account,
holding only `media.browse` and `media.stream`, with its libraries and rating
ceiling, and only while it may still sign in. The answer is exactly what the
account's own `/files/{id}/convert` or `/stream` would give. A wrong, changed
or expired link is 404.

These routes answer `Cross-Origin-Resource-Policy: cross-origin` and
`Access-Control-Allow-Origin: *`, because the Chromecast's player is a page on
Google's origin. They are not rate-limited: a player reads a film by many
range requests, and the link cannot be guessed.

### 2. The player is the sender

The first press of **Cast** loads Google's Cast sender:

- **Script.** `cast_sender.js`, the framework it loads, and the sender Chrome
  ships for its own version (`/eureka/clank/`, found when the policy blocked
  it in a live check). The content security policy allows exactly those
  three paths on `www.gstatic.com`.
- **Receiver.** The Default Media Receiver.
- **Picker.** Chrome's device picker opens.
- **Stream.** The player sends the Chromecast the link to the **converted**
  stream: H.264 and AAC, the picture copied when it already is H.264, from
  the second the viewer is at. A file that cannot be converted is sent as it
  is, by the direct link.

While casting:

- **Controls.** Play, pause and the scrubber drive the TV.
- **Clock.** The player's clock is the TV's.
- **Seeking.** A converted stream cannot seek, so a seek restarts the TV at
  that second, as it restarts the player's own stream. A direct file is
  seeked by the TV.
- **Place.** The film's position is saved as it plays.
- **Stopping.** Pressing Cast again, or the TV stopping, carries on in the
  browser from where the TV got to.

Safari has no Cast sender and keeps its AirPlay picker. A browser with
neither is told which ones can cast.

### 3. What it needs

The Chromecast fetches from the address the browser uses. That address must:

- resolve and be reachable from the TV's network;
- have a certificate the Chromecast trusts.

The bare `https://10.0.1.250:8443` with the installer's self-signed
certificate does neither for a Chromecast. When the TV cannot play the link,
the player says so.

## Consequences

- **Anonymous routes.** Two more: 17.
- **Script.** The app page may load three script paths from Google, and does so
  only when somebody presses Cast.
- **Subtitles.** Not sent to the TV.
- **Untested here.** Casting is untested against a real Chromecast: this
  machine has none. The sender's loading, the link and the stream it serves
  are tested.
- Tests: `TestACastLinkReachesOneFileAsItsAccount`,
  `TestACastLinkExpires`, and the cast route in
  `TestATitleOutOfScopeDoesNotExist`.

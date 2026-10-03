# ADR-0055: Fetching a subtitle from OpenSubtitles

**Status:** accepted
**Date:** 2026-10-01
**Related:** [ADR-0001](0001-egress-control-wireguard-netns.md), [ADR-0015](0015-library-path-containment.md),
[ADR-0054](0054-rotating-the-master-key.md)

## The problem

Subtitles a file already has are served: embedded text tracks and sidecars
(4f). Nothing fetches one. That is most of what Bazarr does, and the
`subtitle` egress profile has been configured, and unused, since Phase 1.

## Decisions

### 1. OpenSubtitles.com, with the operator's own key

The provider is OpenSubtitles.com's REST API, the one Bazarr and most players
use. It refuses every search without an API key (*"You cannot consume this
service"*), so, as with TMDB, the feature is **off until the operator enters
their own key** on the Metadata screen (`admin.system`). An account's username
and password may be added too. OpenSubtitles grants a signed-in account more
downloads a day than a key alone, and the session token from signing in is
kept in memory only.

The key and the password are sealed under the master key in the settings
table, like the TMDB token, and are among what `-rotate-key` re-seals
(ADR-0054). The test that holds every sealed value to the rotation would fail
the build otherwise. The username is not a secret and is stored plain. Neither
the key nor the password is ever returned by the API; the status says only
whether each is set.

The **languages** wanted are a list of two-letter codes on the same screen.
There is no default: a fetch names its language.

### 2. One file, on request

`POST /api/v1/files/{id}/subtitles/fetch` with a language, under
`library.edit`, because it adds a file to the library. The file is read
through the caller's scope (ADR-0037). Fetching wanted subtitles without a
person is the next increment.

### 3. Which subtitle is the file's

OpenSubtitles is asked for the language and for **the title**: a film by its
TMDB id; an episode by its series' TMDB id, season and number. It is also
given the file's **OpenSubtitles hash**: the file's size plus the 64-bit sum of
its first and last 64 KiB. The hash is read through the root's contained vault
and computed here. A result is taken only when it is in the language asked
for, and, if it names a title, when that title is this one. A hash match ranks
first, as a subtitle timed to this exact file. Then come subtitles neither
machine- nor AI-translated, then download count. A title the library has not
identified, with no TMDB id, is searched by hash alone, so only a hash match is
taken.

### 4. What is written, and where

The subtitle is asked for as SRT and must look like one: UTF-8 text, at most
2 MiB, with at least one `-->` timing line. Anything else is refused. A
provider, or something between, could send HTML or a binary, and this file
lands in the library. It is written as `<video stem>.<language>.srt` beside
the video, through the root's contained vault, created only if no such file
exists. A sidecar already there is never replaced. The download link the
provider answers with is fetched through the `subtitle` egress profile like
the API itself, and only from an `https` address. Each fetch is audited as
`media.subtitle_fetched`. What OpenSubtitles says of the day's remaining
downloads is passed on.

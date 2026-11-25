# ADR-0080: Staying signed in

**Status:** accepted
**Date:** 2026-11-25
**Related:** [ADR-0079](0079-a-samsung-television-app.md); the session design is in `internal/identity/session.go` and SECURITY.md §Sessions

## The problem

The operator signed in twice in an afternoon on the television, and "almost
every time" the site was opened in a browser. A year between sign-ins is what
was asked for; a week would be too often. Three causes were found:

1. **The cookie had no expiry.** A cookie without one belongs to the browser's
   session and is dropped when the browser closes. A television's app is
   closed every time it is left, so every launch was a sign-in.
2. **Twelve hours unused ended a session.** Opening the site the next evening
   was a sign-in. The absolute limit was 30 days.
3. **A lost rotation is read as theft.** The session's secret is replaced
   every 15 minutes, in the answer to whichever request comes due. A browser
   cancels film, sound and picture requests whenever the viewer seeks or
   scrolls, often before the answer's headers arrive; the new secret is then
   lost, the old one is presented after the 30-second grace, and the session
   is revoked as stolen. With a year-long session this would end every
   session sooner or later.

## Decisions

### 1. The cookie lasts as long as the session

`cms_session` carries `Max-Age`: at sign-in, the session's whole absolute
life; at a rotation, what is left of it. Max-Age rather than Expires, so the
browser's clock does not matter. The server's timeouts still decide; the
cookie only stops the browser forgetting first.

### 2. A year, and three months unused

The defaults become `session_idle_timeout: 2160h` (90 days) and
`session_absolute_timeout: 8760h` (a year). Chrome keeps no cookie past 400
days, so a year is about as long as a browser allows. Both stay in the
configuration for an operator who wants less.

What a longer session costs is the life of a stolen cookie. It is HttpOnly
(no script can read it), Secure and SameSite=Lax; the secret still rotates and
a replayed one still revokes the session; every session is listed on the
account's Sessions screen to be ended; suspending an account ends them all;
and signing in still needs the second factor.

### 3. Film and picture requests do not rotate

A request with `Sec-Fetch-Dest` of `video`, `audio`, `track` or `image` slides
the idle window but never rotates the secret. Page loads and the app's own
requests (`document`, `empty`) rotate as before, so a session in use still
changes its secret every 15 minutes, and a replayed secret is still theft. A
browser that sends no `Sec-Fetch-Dest` rotates on every request, as before.

Re-issuing a secret whenever an old one turns up was considered and rejected:
it would let a thief holding a superseded secret take the session while the
person it belongs to was signed out, which is the exact case reuse detection
exists for.

## Verification

- `api.TestTheSessionCookieLastsAsLongAsTheSession` — mutation-verified.
- `api.TestAFilmOrPictureRequestNeverRotatesTheSession` — four kinds of
  request, each answered and its answer lost; the session survives —
  mutation-verified.
- `api.TestTheAppsOwnRequestsStillRotate` — mutation-verified.
- `api.TestACodeEnteredLateKeepsTheSession`: a code entered after a rotation
  was due keeps the session — mutation-verified.
- `api.TestSupersededSessionSecretIsTreatedAsReuse` and
  `api.TestSessionExpiresOnIdleTimeout` unchanged.

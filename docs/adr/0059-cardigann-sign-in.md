# ADR-0059: Signing in to a tracker its definition describes

**Status:** accepted
**Date:** 2026-10-03
**Related:** [ADR-0058](0058-cardigann-public-definitions.md), [ADR-0054](0054-rotating-the-master-key.md), [ADR-0003](0003-indexer-definitions-cardigann.md)

## The problem

ADR-0058 searches public trackers from their Cardigann definitions and refuses
a definition with a `login` block. Most trackers worth having are private, so
Prowlarr is still needed for them.

## Decisions

### 1. The definition's settings, filled in by the operator and sealed

A definition declares its settings: usually `username` and `password`, or a
`cookie` copied from a signed-in browser. The operator gives their values when
saving the indexer. The values are kept together as one sealed value per
indexer (`indexer.settings_enc`, sealed under that indexer's id like its API
key) and are never returned. The listing says only which settings are set.
`-rotate-key` re-seals them. A value for a setting the definition does not
declare is refused, and a setting not given takes the definition's default.

### 2. The sign-in methods

| Method | What is done |
|---|---|
| `form` | The login page is read, its form found (`form`, or the definition's selector), the form's own fields kept, the definition's `inputs` and `selectorinputs` put over them, and the form submitted to its action with its method |
| `post` | The `inputs` posted to the login path |
| `get` | The login path asked with the `inputs` as its query |
| `cookie` | The `cookie` setting, `name=value; name=value`, given to the session as it is |

Then:

- the definition's `cookies` are added;
- each of its `error` selectors is checked against what came back, and a
  match fails the sign-in with the page's own message;
- its `test` path is asked, and its selector must match.

A definition needing a captcha is refused when it is saved.

### 3. Credentials go only to the tracker's own address

A sign-in request carrying the operator's values (the login form's action,
the post, the get) must go to the scheme, host and port of the indexer's base
address. A form whose action names any other origin is refused, and so is a
login path that does. The page is the tracker's, and a password is not sent
wherever a page or a definition points. Every request passes the same URL
check as a search.

### 4. The session lives in memory

The session cookies are kept in memory, one jar per indexer, and never
written to disk. A restart signs in again. The jar resets when the
indexer's definition or settings change. A search whose page lands on the
login path signs in once more and asks again; a second failure is the
search's error. The grab of a `.torrent` uses the same session, since a
private tracker's download links need it.

## Known limitations

- **Captchas, two-factor sign-in and Cloudflare challenges** are not answered.
  Where a definition offers the `cookie` method, a cookie copied from a
  browser is the way through.
- **A download link only on the details page** is read from it since 6j
  ([ADR-0060](0060-cardigann-download-from-the-details-page.md)).

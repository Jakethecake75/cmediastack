# ADR-0012 — One route may redirect a denied navigation; every other route stays invisible

**Status:** Accepted · 2026-09-11

## Context

Two requirements collide the moment the UI exists, and the collision is real
rather than a matter of wording.

§2 says an unauthenticated visitor gets exactly one thing: a login screen.

§7.1 and §7.3, as implemented in Phase 1, say an anonymous caller learns
nothing — not 401, not 403, **not a redirect**: 404, identical to a route that
does not exist. `TestEveryNonAllowlistedRouteRejectsAnonymous` asserts exactly
that, in those words, over every registered route.

Before increment 1f there was no conflict, because there were no pages. With
pages, satisfying §7.1 literally means a person who types the instance's
hostname into a browser receives `{"error":"not found"}` as a bare JSON body.
That is not a login screen, and it is not a reasonable thing to hand somebody
whose only mistake was visiting the site.

The tempting fix — content-negotiate on `Accept: text/html` or
`Sec-Fetch-Mode: navigate` and redirect whenever a browser seems to be asking —
was rejected. It makes the set of routes whose existence is admitted depend on
headers the caller controls, which means an attacker chooses which routes
disclose themselves by setting one. Whatever that set is, it should be a list
somebody wrote down.

## Decision

Exactly one route answers a denial with a redirect: `GET /{$}`, the root.

- Anonymous, or an account that cannot act → `303` to `/login`.
- An approved account that has not yet enrolled → `303` to `/enroll`.
- An API token → `403`, unchanged. A token never navigates.

Every other route — `/app/`, `/enroll`, `/assets/app/`, all 23 admin routes,
everything — is untouched and still returns `404` to a caller who may not have
it.

The mechanism is a `Browser` field on `Route`, set only by the `Page` helper.
`register()` panics if it is combined with `Hidden`, with a non-GET method, or
with an anonymous route, so a second one cannot be added by accident. The tests
name the single permitted route in a constant, `browserRedirectRoute`, and
assert every other route's behaviour against the original rule. If that constant
ever becomes a list, that is the moment to re-argue this decision.

## Why the disclosure is acceptable here and nowhere else

The redirect tells an anonymous caller that `/` exists. Every origin has a root,
and `/login` is already an anonymous route that anyone may fetch. The
information gained is zero — which is precisely why this route, and not a
general rule about browsers, is the carve-out.

Two details keep it from becoming something worse:

- **The destination is a constant in the source.** No `?next=`, no `Referer`,
  nothing derived from the request. An open redirect on a login flow is a
  phishing primitive, and the way not to have one is to have no caller-supplied
  destination at all.

- **A suspended account gets the same response as an unknown one.** This is the
  subtle half. Answering `403` to a recognized-but-suspended caller while an
  unknown caller gets a redirect would turn the root into an oracle: present a
  cookie, learn from the status code whether the account is real. The
  `ReasonNotActive` case therefore redirects too, and
  `TestSuspendedAccountIsIndistinguishableFromAnonymousAtTheRoot` compares
  status, `Location` and body byte for byte.

The denial is still written to the audit log before the redirect is rendered, so
the friendlier response costs nothing in forensics.

## Consequences

A signed-out person who follows a deep link into `/app/` gets a `404` rather
than a redirect. The application bundle handles this itself — any API call
returning 401/404 sends the browser to `/login` — so the case only arises for a
cold navigation to a URL a signed-out person should not have had. Accepting the
rough edge is cheaper than widening the carve-out.

# ADR-0011 — Server-rendered shells and a hand-written UI, with no JavaScript build step

**Status:** Accepted · 2026-09-11 · Supersedes the Phase 0 assumption of a Vite build

## Context

Phase 0's architecture assumed the web UI would be a bundled single-page
application with two Vite entry points, one anonymous and one authenticated.
Increment 1f is where that assumption had to be cashed in, and it does not
survive contact with the rest of the brief.

Three requirements point the other way:

- **Single binary, `CGO_ENABLED=0`, runs on almost anything.** A Vite build
  means Node in the build image, a `node_modules` tree, a lockfile and a second
  toolchain that has to be present and correct for anyone rebuilding from
  source.
- **SECURITY.md promises pinned and verified dependencies.** A minimal Vite +
  framework install is several hundred transitive npm packages. That is a
  supply chain an order of magnitude larger than this project's entire Go
  dependency set, attached to the one component that renders authenticated
  pages in a browser.
- **The environment this was built in cannot verify checksums.** The same
  constraint recorded in ADR-0010 applies: `sum.golang.org` is unreachable from
  here, and npm's registry is no better placed. Committing a lockfile nobody
  could verify would make the SECURITY.md claim false a second time.

Against that, the UI Phase 1 actually needs is six pages and about a thousand
lines of behaviour: sign in, enroll an authenticator, approve accounts, manage
sessions and tokens, change credentials, run a task. That is not a workload a
framework is required for.

## Decision

Server-rendered HTML shells from Go's `html/template`, with hand-written CSS and
JavaScript, all embedded in the binary with `embed`. No Node, no npm, no
bundler, and no third-party frontend code of any kind.

Four properties follow, and each is load-bearing rather than incidental:

1. **Two bundles stay, and the split is now testable.** `assets/auth` is served
   anonymously, `assets/app` is not, and the two directories share no file.
   `TestAnonymousBundleNamesNoAuthenticatedEndpoint` scans the anonymous bundle
   for any `/api/` path outside a nine-entry allowlist. Splitting the bundles
   achieves nothing on its own; that assertion is what makes the split real.

2. **No inline script or style anywhere.** The CSP is nonce-based with no
   `unsafe-inline`. Rather than thread a nonce through every page, the pages
   emit nothing inline at all — no `<script>` bodies, no `<style>` elements, no
   `on*=` attributes — so every executable byte arrives from `'self'`. A nonce
   that is never emitted cannot leak. Two tests enforce it, one over the
   rendered HTML and one over the shipped JavaScript.

3. **Pages carry no data.** A template renders a shell and one value: the
   password policy's minimum length, so the hint on the form cannot drift from
   the rule the server applies. Everything else arrives from the JSON API
   afterwards, through the same handlers, middleware and authorization path a
   script would use. There is deliberately no parallel set of server-side form
   handlers — each one would be a second place to forget a permission check, and
   the ones you forget are the ones nobody tests.

4. **No `innerHTML`.** Every string from the database reaches the DOM through
   `textContent`. `TestScriptsAvoidBlockedAndUnsafeAPIs` fails the build on
   `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `eval` and `document.write`.

## Consequences

**The UI requires JavaScript.** There is no progressive-enhancement fallback,
and the `<noscript>` block says so plainly rather than showing a form that
cannot submit. This is the direct cost of item 3 above, and it is the right
trade: one authorization path that is tested beats two paths where one is.

**~~No QR code on the enrollment page.~~ Resolved 2026-09-12.** This was the
weakest point in the UI: manual entry of a 32-character base32 key is the
highest-friction moment in the product, and every account hits it. It was
blocked only by the checksum constraint in ADR-0010, which has since lifted.
Enrollment now shows a scanned code first, with the `otpauth://` tap-through for
phones and manual key entry behind a `<details>` — the manual path is still a
complete route, not a consolation prize, and it opens automatically if the image
cannot be rendered. The QR is a `data:` URI in the same response that already
carried the secret in plain text, rather than a second endpoint that would be
another way to fetch an authenticator seed with its own access class to get
wrong. A round-trip test decodes it with an independent library.

**Styling is hand-written and will stay modest.** Roughly 300 lines of CSS per
bundle, dark and light, no icon set, no animation. Phase 4's player is the point
at which that choice deserves re-examination — not before.

**The bundle boundary needs the test to stay honest.** Nothing stops a future
edit from importing an admin path into `auth.js`; the test is the whole of the
enforcement. It is cheap and it runs on every build, but it is a tripwire, not a
wall.

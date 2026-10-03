# ADR-0018 — Metadata and artwork: a third party, a credential, and a path to disk

**Status:** Accepted · 2026-09-13 · Builds on [ADR-0013](0013-egress-guard-design.md), [ADR-0015](0015-library-path-containment.md), [ADR-0016](0016-import-pipeline.md)

## Context

ADR-0016 recorded a deliberate absence:

> **Identification is from the release name alone.** No metadata provider, so no
> TMDB/TVDB ids, no posters, and no table of episodes the instance does *not*
> have — inventing those rows from the files we hold would produce a season that
> is always 100% complete, which is worse than no answer.

Closing that gap adds three things this software did not previously have: an
outbound dependency on a commercial service, a long-lived credential for it, and
a path by which **bytes and filenames chosen by a third party reach the
operator's disk**.

The third is the same shape as the import pipeline, which is where this
project's one real vulnerability was found.

## Decision

### 1. The jail before the thing inside it — again

Phase 2 built the egress guard before the download engine; Phase 3 built path
containment before the importer. The same order here: `internal/artwork`, the
network-to-filesystem path, was built and verified first, and the provider
client second.

That ordering paid immediately: the artwork layer is the only part of this work
that **can** be verified against the live service, because `image.tmdb.org`
needs no API key.

### 2. A provider's string never reaches a filesystem path

TMDB returns `poster_path: "/f89U3ADr1oiB1s9GkdPOEpXUk5H.jpg"`. Nothing but that
provider's own correctness stops it returning `"/../../../../etc/cron.d/x"`, and
the naive `filepath.Join(cacheDir, posterPath)` *cleans*, so the traversal
succeeds — exactly the defect ADR-0016 records in the importer.

Two independent rules, in that order:

1. **The destination is built from values this software chose** — a kind, a
   numeric id, a size token — each validated as it is a path segment. The
   provider's string is used to build the **URL** and nothing else.
2. **The write goes through `os.Root` anyway.** Rule 1 is the design; rule 2 is
   what holds when rule 1 is wrong.

Rule 2 is not asserted, it is demonstrated: with **both** application-level
checks disabled, the kernel refused every traversal —
`library: the path leaves its root folder: mkdir "../../etc/cron.d"` — and
nothing was written outside the cache.

Beyond that, a provider may supply only a **plain filename**: one segment,
letters/digits/dot/dash, with a picture's extension. A provider that cannot
supply a path cannot supply a traversal, an absolute URL, a different host, or a
query string.

### 3. `library.Cache`: containment without media authority

`Vault` is containment **and** media authority — its writes check
`EffectMutateLibraryPaths`, its deletes `EffectDestroyMediaBytes`. That is right
for an operator's media and wrong for a poster: caching artwork must not require
authority to rearrange a library, and a scan that refreshed artwork must not
thereby hold authority to delete media.

So the *containment* is shared and the *policy* is not. One `os.Root`
implementation, two wrappers.

A Cache has no permission checks, which makes one mistake catastrophic: pointing
it at a media folder would be a permission-free write path into somebody's
library, a configuration typo away. So a Cache **requires a marker file**
(`.cmediastack-cache`), written through the root on creation and refused for
removal. A media folder does not have one.

`TestEveryDestructiveMethodChecksAnEffect` was extended to cover Cache methods,
because it matched on the `*Vault` receiver and a writer added with a different
receiver **in the same file** would have been silently unasserted — the exact
shape of gap that test exists to close. A Cache write needs no permission, which
is precisely why adding one must be deliberate. Verified by adding a plausible
`Tidy()` and watching it fail.

### 4. The bytes decide what a file is, not the header and not the URL

These images are served back to a browser **from this application's own origin**.
A file that is really HTML, cached as `.jpg` and served to a logged-in operator,
is stored cross-site scripting against the app itself.

So the type is decided by sniffing magic bytes, and anything unrecognised is
refused **before** it is written. Three formats: JPEG, PNG, WebP. Notably absent:
SVG, which is XML, executes script, and would be a stored-XSS delivery mechanism
dressed as a picture.

**This is load-bearing in production today, not defensive theory.** The live CDN
content-negotiates: a URL ending `.jpg`, requested with an `Accept` header that
mentions WebP, **returns a WebP**. Trusting the extension would mislabel the file
and serve it with the wrong `Content-Type`. That was discovered by fetching a
real poster, and it is asserted in `TestAgainstTheRealImageCDN`.

### 5. The half-tunnelled instance is a configuration error

An indexer search discloses *what somebody here wants*. A metadata lookup
discloses *what this instance holds* — and an artwork fetch discloses it again,
from a second host.

An operator who proxies indexer traffic and leaves metadata direct has not been
protected; they have been given a **belief** that they are protected, which is
worse than knowing they are not, because the belief is what stops them looking.

So the configuration lint refuses that combination, and `internal/artwork` was
added to `egress.TestNoPackageDialsDirectly` alongside the metadata client.

The lint deliberately does **not** count a namespace-guarded `direct` as
proxied: under ADR-0001 the guard applies to the download process's network
namespace, and the application process — which is what makes these requests —
does not run in it. Counting it would be exactly the false reassurance the check
exists to prevent.

An instance configured `direct` throughout is a **decision**, not a mistake, and
the lint has nothing to say about it.

### 6. The credential lives in the database, sealed, and is never readable back

Not in `config.yaml`, which is the file people paste into forum posts when asking
for help. In the settings table, sealed with the instance master key and bound
to its purpose by the AAD.

**No endpoint returns it, in any form** — not masked, not the first few
characters, not to an administrator. A masked key still discloses its shape and
prefix, which is how people confirm a guess. What an operator needs to know is
whether one is set and whether it works; both are available.

**A key that does not work is never stored.** It is checked against the provider
*before* it is written, and the response says so explicitly — otherwise an
operator who thinks a bad key was saved goes looking for a way to remove it.
Verified against the live API: a bad token produces a 400 carrying TMDB's own
message, and the settings table stays empty.

### 7. What has been verified, and what has not

Stated plainly, because the honest answer is "not all of it".

**Verified against the live service:**

- the API base answers HTTP/2 at `https://api.themoviedb.org/3`;
- a bad key returns 401 with
  `{"status_code":7,"status_message":"Invalid API key: You must be granted a valid key.","success":false}`
  — the exact bytes are a test fixture;
- **no rate-limit headers appear**, so this code parses none and handles 429 by
  status, which is universal;
- `image.tmdb.org` needs no key, serves from BunnyCDN, and content-negotiates;
- the full admin round trip: the compiled binary calls the real API through the
  egress guard, classifies the real 401, explains it, and stores nothing.

**Verified after the fact** — see the addendum below. The struct tags were
originally taken from documentation; they have since been checked against live
responses, and two of them were wrong.

The tolerance built for that uncertainty was kept, because it earned its place
the moment the real API was called:

- **Decoding is tolerant.** Every field is optional; a missing one yields a zero
  value; no absent field is an error. A response shaped slightly differently
  produces a thin result, not a failure. `episode_run_time` came back empty, and
  the result was a zero runtime rather than a failed lookup.
- **`ErrUnexpectedShape` is a distinct error** that quotes what actually arrived.
- **`Check()` makes the first real call a button rather than a mystery.**

## Consequences

**Accepted.**

- **Nothing uses the provider yet.** An operator can configure it, prove it
  works, and search it by hand. Automatic identification of library items —
  matching a parsed release name to a provider id — is the next increment and
  needs its own decision, because a wrong automatic match relabels somebody's
  library.
- **No episode table yet**, so ADR-0016's absence stands: there is still no
  "wanted", no calendar and no season-completion percentage. The provider can
  now supply the season list that makes them possible.
- **No artwork is fetched yet.** The store is built and verified; nothing calls
  it, because nothing yet knows which title is which.
- **One provider.** The `Provider` interface exists because TMDB's television
  data is weaker than TVDB's and this software replaces Sonarr as well as
  Radarr — not as speculative generality.
- **Metadata lookups disclose the library to a third party.** That is inherent
  to having a metadata provider at all; what this software adds is the lint that
  stops an operator being wrong about which route it leaves by.

---

## Addendum — 2026-09-13: verified against the live API

An API key arrived, so the one unverified claim above could be settled. The
verification is not a note saying it was done: it is
`internal/metadata/tmdb_live_test.go`, which runs when `CMS_TMDB_TOKEN` is set
and skips when it is not. The suite must stay runnable by anyone, offline, with
no credential — but "do the struct tags match reality?" must be answerable by
running something rather than by reading documentation again.

### What held

Everything the code depends on, on the first run: the image base and its seven
poster sizes; a film's `title` / `original_title` / `release_date` / `overview`
/ `poster_path`; a series' `name` / `first_air_date` and the separately-named
`first_air_date_year` search parameter; a film's top-level `imdb_id`, `runtime`
and `genres`; and — the round trip with the most ways to go wrong —
`append_to_response=external_ids` producing a series' IMDb id in **one** request
rather than two.

Also confirmed end to end through the compiled binary: the key is verified
before storage, sealed at rest (267 bytes of ciphertext, no plaintext and no
`eyJ` prefix anywhere in the row), never echoed back, and a real search returns
real results.

And the composition, which is where a mismatch between two separately-evidenced
halves would live: a real poster path from a real search, through
`internal/artwork`'s checks, cached at `poster/tmdb/329865-w342.webp`. Note the
extension: the CDN served **WebP** for a path ending `.jpg`, again.

### What did not hold

**`episode_run_time` is empty.** Documented as a list of typical episode
lengths; the live API returns `[]` for Severance, and apparently for most
series. The fallback that read it yielded zero for every series.

The fix is a third source — `last_episode_to_air.runtime` — and an honest
comment rather than a confident one: that is the length of *one* episode, often
a finale, which is the least typical episode there is. A series' metadata
runtime is an approximation and is now documented as one. For anything that
actually depends on duration (transcode planning, ADR-0005) the **file** is the
authority, not this.

**My own test assertion was wrong about seasons.** It required every season
above zero to have episodes. Severance season 3 is announced, unaired, with
`episode_count: 0` and a null `air_date` — real, correct data that the assertion
called a failure. The rule is now conditional: a season that has *aired* must
have episodes, and a season with episodes must have an air date.

That case is worth more than the fix. It is exactly what ADR-0016 refused to
invent — a season the instance cannot possibly have files for — arriving from
the provider as a first-class fact. When the episode table is built, an
announced season with zero episodes is the row that must not become "0% complete"
and must not become nine imaginary episodes either.

### What is still not verified

Nothing in the request path. What remains unexercised is failure behaviour that
needs the provider to misbehave: a genuine 429, a 5xx, a malformed body from the
real host. Those are covered by fixtures, and fixtures are the right tool for
them — the alternative is waiting for TMDB to have a bad day.

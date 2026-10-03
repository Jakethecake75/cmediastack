# ADR-0058: Searching trackers from their Cardigann definitions

**Status:** accepted
**Date:** 2026-10-03
**Related:** [ADR-0003](0003-indexer-definitions-cardigann.md), [ADR-0024](0024-indexers-on-the-operators-network.md)

## The problem

ADR-0003 decided that a tracker without a Torznab API is reached through the
Cardigann definition Jackett and Prowlarr already maintain, consumed as data.
Only the Torznab and Newznab client was built. Every other tracker therefore
still needs a Prowlarr or Jackett in front of it, so Prowlarr is not replaced.

## Decisions

### 1. A definition is pasted, kept with its indexer, and never vendored

An indexer of kind `cardigann` carries the definition's YAML, pasted by the
operator from the upstream repository. It is checked when it is saved, and the
check names every part of it this build cannot follow, rather than failing at
the first search. Updating a broken definition means pasting the new one;
nothing waits for a release of this software. No definition ships in the
binary or in this repository, as ADR-0003 requires.

The base address is the operator's choice. The form offers the definition's
own `links`, and the address is checked like any indexer's (ADR-0024).

### 2. Public trackers only, for now

A definition with a `login` block is refused at save, with the reason. Signing
in means:

- storing a password or a cookie for a site, sealed and rotated;
- following each definition's own login form, its error selectors, and
  sometimes a captcha;
- knowing when a session has lapsed.

That is a second increment. Public trackers need none of it. Settings take
the defaults the definition gives.

### 3. What is followed

| Part | Followed |
|---|---|
| `search.paths` | Each path in turn, `get` or `post`, its own `inputs` over the common ones; a path limited to `categories` is asked only for those |
| `inputs`, `keywordsfilters` | Go templates, as the format writes them: `.Keywords`, `.Query.*`, `.Categories`, `.Config.*`, `.Today.Year`, `.True`, `.False`, `and`/`or`/`eq`/`if`/`range`, `join` and `re_replace`. `$raw` is appended to the query string as written |
| `rows` | A CSS selector over the HTML, or a dotted path to an array in a JSON response; `after`, and `remove` |
| `fields` | A `selector` with `attribute`, `remove`, `optional` and `default`, or a `text` template that may read the fields before it (`.Result.*`) |
| `filters` | `querystring`, `regexp`, `re_replace`, `replace`, `split`, `trim`, `prepend`, `append`, `tolower`, `toupper`, `urldecode`, `urlencode`, `dateparse`, `timeago`, `fuzzytime`, `htmldecode`, `diacritics` and `strdump` |
| `caps.categorymappings` | The site's categories, mapped to the Newznab numbers the rest of the software uses, both ways |

The CSS selectors are an in-tree subset over `golang.org/x/net/html`, which is
already in the module graph:

- type, `.class`, `#id`, `*`;
- the attribute forms `[a]`, `=`, `^=`, `$=`, `*=` and `~=`;
- descendant and child combinators, and selector lists;
- `:nth-child`, `:first-child`, `:last-child`, `:contains`, `:has` and `:not`.

No new module is added.

A definition using anything else (another filter, `login`, a `download`
block that fetches the details page, a captcha, a CSS feature outside the
subset) is refused at save, naming what it used.

### 4. The results are indexer results

A row becomes the same `indexer.Result` a Torznab item does, and is judged by
the same matching, profile and grab. Every link is resolved against the base
address and passes the same URL check: an http(s) download or a magnet, never
another private address. A row without a title, or without either a download
link or a magnet, is dropped. The response is capped in size and in rows as a
feed is. A definition is the operator's own configuration; the page it reads
is the tracker's, and it is treated as hostile like a feed.

## Known limitations

- **Private and semi-private trackers** need the login increment.
- **A tracker whose download link is only on the details page** needs the
  `download` block's second fetch — built in 6j,
  [ADR-0060](0060-cardigann-download-from-the-details-page.md).
- **Definitions are not fetched.** The operator pastes them.

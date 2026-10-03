# ADR-0019 — Identification: the machine proposes, a person confirms

**Status:** Accepted · 2026-09-13 · Builds on [ADR-0016](0016-import-pipeline.md), [ADR-0018](0018-metadata-and-artwork.md)

## Context

A library item's title came from a release name — `the.matrix.1999.720p.brrip`
— parsed by software that is good and not omniscient. Attaching a provider id
fixes the title, the year, the artwork and every future search. Getting it wrong
does the same thing with the wrong film.

Radarr and Sonarr match automatically and let the operator correct afterwards.
That is the obvious design and it was worth asking whether it is the right one
here.

## Decision

### 1. The machine proposes; a person confirms

The two failure modes are not equal, and neither is loud:

| | What happens | Cost |
|---|---|---|
| **Missed** identification | The item looks exactly as it does today: a parsed title, no poster | Nothing is lost; a person can fix it |
| **Wrong** identification | The item is **relabelled**. Somebody else's title, poster and synopsis | The file is untouched, but what the operator browses to is now a different film — and it *looks deliberate*, because it looks like the software knows |

So automatic acceptance exists and is deliberately hard to earn.

### 2. What automatic acceptance requires

All three, together:

1. **An exact normalised title match, articles included.** Not a close one:
   *Alien* and *Aliens* are one character apart and are different films, and no
   edit-distance threshold separates that pair from a genuine typo.
2. **An exact year match.** Not adjacent — an adjacent year is a real and common
   disagreement, and it is precisely the disagreement that distinguishes a
   remake from its original.
3. **A clear margin over the runner-up.**

An item with no year can never be accepted automatically, and that follows from
(2) rather than being a separate rule: without a year, a title is a question.

### 3. The margin rule, and why a confidence threshold is not enough

**This is the decision the design is built around, and it rests on an empirical
fact rather than a worry.** Two films called *Arrival* were released in 2016.
The live provider returns both — ids 329865 and 472349 — and both are an exact
title and exact year match for a library item parsed from
`Arrival.2016.1080p.BluRay`.

A threshold on the winner's score alone accepts one of them, with even odds,
forever. Only a margin can refuse.

`identify.TestLiveAmbiguityIsReal` asserts that this is still true of the real
provider. If TMDB ever deduplicates, that test says so rather than quietly
becoming a tautology.

### 4. Popularity ranks; it never convinces

A provider orders by popularity, and that is the right order to *show* somebody:
a search for "Dune" should lead with the film most people mean. It is not
evidence of identity — the more popular *Arrival* is not thereby the one in the
operator's library.

So popularity is added to a **sort key computed in `Rank`**, never written into
`Scored.Score`, which is what `Decide` reads. The displayed order can prefer the
likely title; the decision to act without asking cannot see it.

**The bound is where this gets interesting, and it was worked out rather than
tuned.** There are two gaps, and they are the same size:

- the gap a nudge would *like* to cross — with no year on either side, an exact
  title scores 0.875 and an article-only agreement 0.7625;
- the gap it *must not* cross — with years matching, 1.0 against 0.8875.

Both are 0.1125, because the year contributes equally to each. **No bound can
take the first without taking the second.** A constant tuned until one example
looked right would have hidden that rather than resolved it. The safe half is
taken and the cost is recorded in *Consequences* below.

### 5. One normalisation, two policies

`release.NormaliseTitle` is shared with `internal/request`. If the request
surface and the identification surface normalised differently, a title that
merged two requests would fail to match the library item those requests were
about, and nobody would ever work out why.

What is *not* shared is the decision. `request` asks "did two people ask for the
same thing?"; `identify` asks "is this library item that title?". The same pair
of functions gives opposite answers to a leading article, correctly:

- **`request` strips it.** Somebody typing "Matrix" and somebody typing "The
  Matrix" want the same film.
- **`identify` does not.** *Arrival* and *The Arrival* are both real 2016 films,
  and the live provider returns both.

`StripLeadingArticle` is therefore separate from `NormaliseTitle`, and the
separation was forced by live data: until they were told apart, "The Arrival"
scored a **perfect** match for "Arrival".

### 6. Two normalisation defects the live provider found

- **A leading article is not a spelling difference.** See above.
- **`unicode.IsDigit` excludes superscripts.** Dropping everything non-digit
  made *Alien³* normalise to `alien`, colliding with *Alien* — two different
  films scoring as an exact match. `unicode.IsNumber` includes the superscript,
  and they stop colliding.

Both were invisible to fixtures, because a fixture contains the titles its
author thought of.

### 7. Every verdict is explained in words

A person confirming an identification is answering "is this the same film?", and
`0.82` does not help them answer it. So each candidate carries its reasoning —
*"the original title "Das Boot" matches exactly; the year matches (1981)"* — and
each verdict explains itself, especially when something that looks like a good
match was **not** accepted:

> 2 candidates match "Arrival" (2016) equally well — "Arrival" (2016, id 329865)
> and "Arrival" (2016, id 472349) — so a person has to choose.

## Consequences

**Accepted.**

- **A mis-parsed title is always proposed, never accepted.** "matrix" with no
  year will not be identified automatically even when the answer is obvious to a
  human. That is the conservative half of §1 doing its job, and the cost is one
  confirmation on a title that deserved a glance anyway.
- **For an item that lost BOTH its article and its year, the literal match leads
  and the likely one is second** — see §4. The real fix is upstream: that case
  arises because a scan reads a *filename* where the folder carried the year
  (increment 3c), and a year recovered there separates the candidates entirely.
- **The weights in `combine` are not calibrated against a corpus, and are not
  trusted to be.** They order candidates; the only decision taken without a
  person requires exact *signals* and a margin, not a high number. A score that
  merely ranks does not need calibration; one that decides does.
- **The weights order; they do not decide.** See above.

---

## Addendum — persistence, and the property that falls out

### The refusal the whole table is built around

An automatic pass over a library must be **safe to re-run** — that is what makes
it automatic. A re-run that quietly undid yesterday's corrections would make the
feature worse than useless: the operator would fix the same items forever and
never work out why.

So `decided_by` is the load-bearing column. NULL means the software decided;
a user id means a person did, and `SaveProposal` refuses to touch those rows at
all. There is no override flag — a caller that wants to redo a person's decision
calls `Reopen`, by name, with a person behind it.

A **rejection** is a decision too. An operator who has looked at an item and
concluded the provider does not have it is not asked again on every pass.

The mutation that proves it is worth quoting, because the second half is the
real damage: with the refusal removed, a rejected item is re-proposed *and* the
operator's own words — "this is a family video" — are silently replaced by the
machine's generic sentence.

### The software may revisit its own decision; only a person's is a wall

An automatic acceptance is recorded with `decided_by` NULL. That is not a
technicality: it means a later pass may revisit its own conclusion when the
provider's data improves, while a person's stands.

### Automatic acceptance can never relabel anything

This falls out of the exact-title rule rather than being a separate safeguard,
and it is the reason the pass needs no permission to edit library items.

Acceptance requires the title to match **exactly**. So an automatically
identified item's title already agrees with the provider's, and there is nothing
to rewrite. `applyAutomatic` therefore writes ids and artwork and deliberately
leaves `Title` and `Year` empty — with a comment saying why, rather than relying
on the invariant holding silently.

Relabelling only ever happens when a **person** confirms a candidate whose title
differs from the parsed one, looking at both strings while they do it.

`authz.TaskIdentify`'s grant is `PermBrowse` **and nothing else** —
not `PermEditLibraryItems`, not `PermManageRootFolders`. Withholding the
permission is stronger than intending not to use it, and
`authz.TestTheIdentificationPassCannotEditLibraryItems` says so.

### Candidates are stored, not re-searched

Identification runs as a background pass; review happens later, item by item.
Re-searching at review time would mean a provider request per item glanced at —
and worse, **the thing confirmed might not be the thing proposed**, because the
provider's results moved in between. A person confirms what they were shown, and
`Confirm` refuses an id that was not among the stored candidates.

A later search replaces the candidate set wholesale rather than merging, so
nobody chooses between options from two different searches, one of which this
software no longer considers plausible.

### What the pass does when things go wrong

- **A per-item failure does not stop it.** A library half-examined because one
  title errored is worse than a slow one, and the counts make a pass that
  achieved nothing visible as such.
- **A provider-level failure does.** A rate limit or a rejected key is about the
  provider, not the item; carrying on would spend the rest of the library
  learning the same thing.
- **An artwork failure fails nothing.** A missing picture is cosmetic; an
  identification that failed because a CDN was slow is not, and the two must not
  be able to become each other.

## Addendum: the review surface, and four things building it found

The routes, the scheduled pass and the review screen are built. Putting them in
front of a browser and a live provider found four defects, none of which any
test at the time would have caught.

### A library kept whatever spelling a release group used

The first end-to-end run identified `the matrix.1999.720p.brrip` correctly and
left the library reading **`the matrix`**. Correct by the rule above — automatic
acceptance never relabels — and obviously wrong to look at.

The rule was about **meaning, not bytes**. ADR-0019's constraint is that a
background pass must not relabel an item as a *different film*; it was never
that a library should keep a release group's casing. So there is now a third
method, `AdoptCanonicalTitle`, which takes the provider's spelling and **refuses
anything whose normalised form differs**:

    "the matrix" -> "The Matrix"      same title, better spelled     allowed
    "Arrival"    -> "The Arrival"     these normalise differently    refused

The refusal is inside the method rather than in the caller's discipline, so the
pass cannot change what an item *is* by calling it — it can only change how the
same thing is written. `importer.TestAdoptingASpellingCannotChangeWhatSomethingIs`
and `importer.TestAdoptingASpellingRefusesALeadingArticle` both fail when the
comparison is removed.

`AttachIdentity` / `AdoptCanonicalTitle` / `Relabel` is therefore the full split:
ids, spelling, and meaning — three methods, three authorities, and the middle one
is reachable by a background pass precisely because it cannot reach the third.

### Nothing that read a library could see an identification

`importer.Item` never carried `tmdb_id` or `imdb_id`, though the columns had held
them since migration 0008. The pass wrote them and every read path was blind to
them: no page could tell an identified item from an unidentified one, and no page
could find a poster. Both fields are now on the read path and in `itemJSON`,
alongside a `poster` URL — the URL rather than the ingredients, so no client is
tempted to assemble a *provider* URL instead.

### The IMDb id was never fetched at all

Both `applyAutomatic` and `Confirm` passed `""` for it. The provider carries it
only on the details endpoint, so it costs one extra request per identified item.

Spent, deliberately: it is a background pass that already spends one request per
item, it happens once in an item's life, and the alternative is to spend the same
request later — one item at a time, while somebody waits for a search. It never
fails an identification; an item with a provider id and no IMDb id is identified,
and one that *lost* its identification to a details timeout would not be.

Writing that also exposed that `Confirm` hard-coded `metadata.KindMovie` for
artwork. A series' details and artwork live behind different provider endpoints,
so every series an operator confirmed would silently have got neither.
`identify.TestConfirmingASeriesAsksTheProviderAboutASeries` fails on the old line.

### The review screen would have shown four broken images

The pass caches a poster for what it **accepts**. What it *proposes* is exactly
what a person has to look at — and those had no artwork at all. On the one screen
whose job is to be looked at, every picture was a 404.

Fetching all candidate posters when a proposal is saved would mean downloading
five images per ambiguous item whether or not anybody ever opens the screen. So
they are fetched on **first view** instead, which spends nothing until somebody
actually looks.

That means an authenticated request can cause this instance to make an outbound
request, so the containment matters:

- The remote path is read from the **candidate row this software stored** when
  the provider answered a search. The request supplies a provider and an id.
- An id with no such row is `identify.ErrNotRecorded`, and **nothing is fetched**.
- `internal/artwork` still refuses anything that is not a plain filename, and
  still writes through `os.Root`.

So a request can choose *between* posters this instance already knows about, and
cannot introduce one. `identify.TestAPosterIsOnlyFetchedForATitleThisInstanceRecorded`
fails when the path is reconstructed from the id instead of read, and
`identify.TestFetchingAPosterNeedsThePermissionToBrowse` fails when the permission
check is dropped.

### What the screen shows, and why

Posters, titles, and the reason each candidate is a candidate — **not** the
scores. The score orders the list; it is not an argument, and a number next to a
film is an invitation to trust it rather than look.

The live run makes the case better than the reasoning does. Searching for
`Arrival (2016)` returns two candidates that are textually identical — same
title, same year, same explanation, both scoring 1.00 — because there really are
two 2016 films called *Arrival*. Nothing but the poster and the synopsis tells
them apart, which is precisely why this screen exists rather than a threshold.

### Still not built

Episode-level identification for series. The provider supplies season lists, but
nothing yet records episodes, so a series is identified as a whole and its files
are matched by the release-name parser alone.

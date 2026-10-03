# ADR-0035: A quality profile per title

**Status:** accepted
**Date:** 2026-09-29
**Related:** [ADR-0027](0027-a-default-quality-profile.md),
[ADR-0030](0030-automatic-acquisition.md), [ADR-0033](0033-season-packs.md)

## The problem

One profile judges every search nobody chose a profile for (ADR-0027), and it
judges everything automatic acquisition fetches (ADR-0030). An instance has one
taste: *HD-1080p* for everything, or 2160p for everything. Real libraries are not
like that — the one film worth 60 GB in 2160p, the old sitcom where 720p is
plenty. Both records deferred the choice per title; ADR-0030 named it among its
rejected alternatives: *"The per-item choice can be added without changing
this."* It is added here.

## Decisions

### 1. A title may name a profile; none means the default

A series or a film gains `quality_profile_id` (migration 0021): a profile, or
NULL for *the instance's default*. NULL is the value for every title that
exists, so nothing changes until somebody chooses. A season or an episode has
none of its own: a series is one taste.

Deleting a profile a title names does not delete or refuse anything: the
reference is `ON DELETE SET NULL`, and the title goes back to the default.
(The default itself cannot be deleted — ADR-0027.)

### 2. Who chooses

`PUT /api/v1/media/{id}/quality-profile` with `{"profile_id": N}`, or `null`
for the default. **`library.edit`** — the permission that already decides what a
title is monitored as, because this decides what it is fetched as. Not
administrator-only as the default is: the default changes every title, this
changes one. It is audited, `media.quality_profile.changed`, with the profile
before and after, because it changes what the instance downloads with nobody
looking.

### 3. ADR-0027's rule, one step longer

A search **for a title** — an episode's, a season's, a film's — that names no
profile is judged by **the title's profile, then the default**. `0` is still
*none*, and a number is still that profile: a person can always ask for
something else for one search. The general search names no title and is
unchanged. Every answer says which profile judged it and whether it was the
title's.

### 4. Automatic acquisition judges each item by its title's

Each wanted item is searched for, judged and ranked by its title's profile, or
the default. A release from the indexers' recent releases, judged by the
default once for the whole feed, is judged again by the title's profile when the
title has one — so a 2160p release is fetched for the one film that asked for
it and refused for the rest.

A pass still needs a default profile to run. Without one it does nothing and
says so, as before: the default is what most titles are judged by, and a pass
that fetched only the few titles with their own profile would look, on the
Wanted screen, like one that was broken for the rest.

## Rejected alternatives

**A profile per season or per episode.** A series is watched as one thing; a
per-season choice would be a setting nobody finds again.

**Administrator-only.** The default is instance-wide and ADR-0027 made it the
administrator's; a title's profile is curation, which is `library.edit`.

**Choosing it when a title is added.** The add screens stay as they are: a title
starts on the default and is changed on its page, which is where anybody will
look for it afterwards.

## Known limitations

- **The import's upgrade rule is unchanged.** Which of two files is better is
  still judged by the default ladder (a stated limitation of the importer), not
  by the title's profile, and a file below the title's cutoff is not replaced
  automatically — that is the next record's.
- **A pass needs a default profile**, even for titles that name their own.

## Verification

| Claim | Test |
|---|---|
| A title names a profile or the default; an unknown profile is refused; deleting the profile returns the title to the default | `importer.TestATitleCanNameItsProfile` |
| Only `library.edit` sets it, and it is audited | `api.TestATitlesProfileIsSetAndAudited` |
| A search for a title is judged by its profile, then the default; 0 and a named profile still win | `api.TestATitlesProfileJudgesItsSearches` |
| Automatic acquisition fetches for each title by its own profile, from searches and the recent releases | `acquire.TestATitlesProfileJudgesWhatIsFetchedForIt` |

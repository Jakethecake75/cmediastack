# ADR-0043: Discover — what is popular, asked for from where it is shown

**Status:** accepted
**Date:** 2026-09-29
**Related:** [ADR-0017](0017-acquisition-requests.md),
[ADR-0018](0018-metadata-and-artwork.md),
[ADR-0037](0037-libraries-and-rating-ceilings.md)

## The problem

`GET /api/v1/discover/{section}` has answered 501 since Phase 1, the last route
that does. Jellyseerr's front page — what is trending, what is popular, what is
coming out — is how a household finds something to ask for. Requests exist
(ADR-0017), but they start from a title typed from memory.

## Decisions

### 1. Four sections, from the metadata provider

| Section | TMDB |
|---|---|
| `trending` | `/trending/all/week`, films and series |
| `popular-films` | `/movie/popular` |
| `popular-series` | `/tv/popular` |
| `upcoming-films` | `/movie/upcoming` |

The first page of each, without adult titles. Each result is its kind, title,
year, overview and provider id, **whether it is already in the library** — a
title the caller can see carries that id — and whether the caller has an open
request for it by that title and year.

### 2. One request per section per six hours, for everybody

The lists are the same for every account, so they are fetched once and kept in
memory for six hours: at most sixteen requests a day to the provider, however
many people browse. `media.browse` is enough to read them, which it would not be
if every visit spent a request. Nothing about who asked reaches the provider.

### 3. Not for an account with a rating ceiling

The provider's lists carry no certification without a request per title. An
account with a ceiling (ADR-0037) is shown no Discover at all: an empty list
that says why, not a list of titles it may not be allowed to ask for. The
library's own rules are unchanged: whatever such an account requests still
arrives rated, or hidden until it is.

### 4. No posters

A poster is fetched only for a title this instance recorded (ADR-0018), so a page
of posters for the provider's catalogue would be forty requests to the image
server, announcing what this household browsed. Discover is text: title, year,
the first lines of the overview, and *Request* beside each.

## Rejected alternatives

**Searching the provider from Discover.** Search is the *Add* screen's, under
`library.edit`, because it spends a request per keystroke.

**Per-account recommendations.** They need a watch history the provider would
see, or a model this instance does not have.

## Known limitations

- The lists are TMDB's and in English; a region's cinema listings differ.
- A title stays marked *requested* only while the request is open.

## Verification

| Claim | Test |
|---|---|
| Each section is read from its endpoint, without adult titles, and unknown sections are refused | `metadata.TestDiscoverReadsItsLists` |
| A section is fetched once in six hours for everybody, marked in-library and requested per caller | `api.TestDiscoverIsSharedAndMarked` |
| An account with a ceiling sees none, and no provider is a 409 | `api.TestDiscoverIsNotForACappedAccount` |

# ADR-0038: The library routes left unbuilt — four removed, three built

**Status:** accepted
**Date:** 2026-09-29
**Related:** [ADR-0005](0005-transcode-policy-skylake.md),
[ADR-0011](0011-server-rendered-shells-no-build-step.md),
[ADR-0018](0018-metadata-and-artwork.md), [ADR-0020](0020-playback.md),
[ADR-0037](0037-libraries-and-rating-ceilings.md)

## The problem

Seven routes under the library and playback headings were registered in Phase
1 with their access classes and have answered 501 since: four for playback
sessions (`POST /api/v1/play/sessions`, its `manifest.m3u8`, its segments and
its progress), a library search, a title's artwork and a title's original file.
Phase 4 built playback a different way, and three of the seven were never
needed for it. A route that answers 501 forever is a promise nobody is keeping.

## Decisions

### 1. The four play-session routes are removed

They describe HLS: a server-side session, a playlist and numbered segments.
Playback was built without any of it (ADR-0020):

- **The session is the signed-in session.** A `<video>` request carries the
  cookie; there is no second authorization model to mint (ADR-0020, decision 4).
- **The bytes** are `GET /api/v1/files/{id}/stream`, with range requests, and
  `GET /api/v1/files/{id}/convert` for a file whose sound a browser cannot play.
- **Progress** is `PUT /api/v1/files/{id}/position`.

HLS would add nothing but costs. Desktop Chrome and Firefox do not play it
natively, so it would need a third-party player in the page, which ADR-0011
refuses. It is also the shape adaptive bitrate takes, and ADR-0005 refuses
adaptive bitrate on this hardware. Seeking within a converted stream, which
segments would have given, stays a stated limitation of ADR-0020.

### 2. `GET /api/v1/search?q=` searches the library

The library's titles — films and series — whose title contains every word of
the query, compared case- and accent-folded, as the release matcher already
folds them. A title starting with the query comes first, then alphabetical order.
At most 50. **Scoped** like every read made for a person (ADR-0037). Two to 100
characters; anything else is refused by name. It does not search the provider;
adding a title does that, under `library.edit`.

### 3. `GET /api/v1/media/{id}/artwork` is a title's poster

The poster cached for the title's provider id, served as the provider-keyed
route serves it. Because it is keyed by the title, the title's scope decides it:
a title out of scope and a title with no poster both answer 404. The library's
own pages use it from now on (`poster` in a title's JSON). The provider-keyed
route stays, for the add and identification screens that show candidates not in
the library.

### 4. `GET /api/v1/media/{id}/original` downloads a title's file

`media.download_original` — Manager and Admin by default. It serves the file as
stored, as an attachment named after the file, with range requests so a large
download can resume. `?file=` names one of the title's files; a title with one
file needs none, and one with several refuses and lists their ids. It is
**scoped** like playback, and **audited** as `media.downloaded`. A copy leaving
the instance is worth a line, which streaming, a copy that is watched, is not.

## Rejected alternatives

**Keeping the play-session routes for a future player.** A future player that
wants them can add them back with a record of its own. Holding a 501 open for
it misleads everyone who reads the route table meanwhile.

**Searching the provider from `/search`.** It spends a third party's rate limit
on every keystroke, from a permission (`media.browse`) every account holds.

**Not auditing downloads**, as streams are not. A stream is somebody watching.
A download is a copy that has left the instance.

## Known limitations

- Search matches titles, not episode titles, people or years.
- A download of a file being replaced by an upgrade at that moment is served
  from the descriptor already open: the old file, whole.

## Verification

| Claim | Test |
|---|---|
| The play-session routes are gone | `api.TestNoRouteIsLeftUnbuiltWithoutARecord` |
| Search folds case and accents, puts prefix matches first, is scoped and bounded | `api.TestTheLibraryIsSearchedByTitle` |
| A title's artwork is its cached poster, scoped | `api.TestATitlesArtworkIsItsPoster` |
| An original is served as an attachment, chosen by file when there are several, scoped and audited | `api.TestAnOriginalIsDownloadedAndAudited` |

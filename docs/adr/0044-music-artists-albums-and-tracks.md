# ADR-0044: Music — artists, albums and tracks, from MusicBrainz

**Status:** accepted
**Date:** 2026-09-29
**Related:** [ADR-0022](0022-episode-tracking.md),
[ADR-0025](0025-adding-a-series-before-it-is-on-disk.md),
[ADR-0037](0037-libraries-and-rating-ceilings.md), docs/DROPPED-FEATURES.md
(*Music and books*)

## The problem

Phase 5 is the Lidarr and Readarr replacements. The operator decided on
2026-09-29 that both are wanted. Root folders have accepted the kinds `music` and
`books` since Phase 3, and nothing else understood them.

Music is not a fourth kind of film. What is followed is an **artist**. What is
wanted is an **album**: a release group, of which there are many releases, each
with its own track list. The authority is MusicBrainz, not TMDB. The files are
tracks, named by the tags inside them as often as by the folder around them.

This record settles how music fits the model everything else is built on. It
covers the catalogue: adding an artist, their albums and tracks, what is wanted.
Import, search and acquisition come after, each with its own record.

## Decisions

### 1. An artist is a title; albums and tracks hang from it like seasons and episodes

A followed artist is a `media_item` of the new kind **`artist`**, so everything
that works on titles works on artists: the library list, search, scope
(ADR-0037), requests, issues and deletion. Their albums are rows of `album` and
their tracks rows of `track`, as a series' seasons and episodes are rows of
`season` and `episode` (ADR-0022). Books will be the kind **`book`** (their own
record).

Allowing the new kinds needs `media_item`'s kind `CHECK` widened, and SQLite
cannot alter a `CHECK`: the table is rebuilt. Eight tables reference it, and
dropping a referenced table with foreign keys on would cascade-delete their
rows. So the migrator learns one thing: **a migration that says, on its first
line, `-- cms: foreign_keys=off` runs on a single connection with foreign keys
off, and is refused and rolled back unless `PRAGMA foreign_key_check` finds
nothing** before it commits. It is the rebuild procedure SQLite documents,
checked rather than trusted.

### 2. MusicBrainz, asked politely

The catalogue is MusicBrainz's: an artist is a MusicBrainz artist id; an album
a release group; its track list that of one official release, the earliest.
MusicBrainz needs no key. It asks every client to send a User-Agent naming it
and to make at most one request a second, and the client does both. Its traffic
goes through the egress guard on the `metadata` profile, as TMDB's does.

Albums are the release groups whose primary type is *Album* or *EP*.
Compilations, live albums, remixes and soundtracks, marked by secondary types,
are left out, and so are singles, as Lidarr's default profile does. They can be
added later by a person.

### 3. Adding an artist: every album listed, a monitoring choice made

`POST /api/v1/media` with `kind: artist` and a MusicBrainz id, under
`library.edit`, as adding a series is (ADR-0025). Nothing is written to disk.
The artist, their albums and each album's tracks are written in one transaction
or not at all. The choice of which albums are wanted has no default — `all`,
`future` (the ones not yet released), `latest` or `none`. An album is
**wanted** when it is monitored, released, and missing at least one track.

The provider is asked for the track lists lazily: the album list when the artist
is added, a track list when an album is first shown or searched for, and again
by a weekly refresh. An artist with forty albums costs one request to add, not
forty-one.

### 4. Ratings are for films and series

A rating ceiling hides what is rated above it and whatever is unrated
(ADR-0037). Music and books carry no certification, so under that rule every
album would be hidden from every account with a ceiling. **The ceiling applies
to films and series only.** Music and books are governed by the other half of
ADR-0037, the libraries an account is granted: a *Kids' music* root folder is
how a child's account gets music.

## Rejected alternatives

**Separate tables for artists, beside `media_item`.** Scope, search, requests,
issues and deletion would each need a second implementation, and each second
implementation is a place to forget ADR-0037.

**Editing the `CHECK` in `sqlite_schema` with `writable_schema`.** SQLite
documents it, with warnings, for exactly this. The rebuild is the procedure it
recommends.

**Every release group, singles and compilations included.** It is how a
discography of forty albums becomes a Wanted list of four hundred.

## Known limitations

- One release's track list stands for the album. A deluxe edition's bonus
  tracks are not wanted.
- MusicBrainz's search is by name. Two artists with one name are told apart by
  their disambiguation, which the person adding reads.

## Verification

| Claim | Test |
|---|---|
| A foreign-keys-off migration keeps every referencing row, and is refused when a reference would dangle | `db.TestAMigrationMayRebuildAReferencedTable` |
| MusicBrainz is asked with a User-Agent, at most once a second, for albums and EPs without secondary types | `music.TestMusicBrainzIsAskedPolitely` |
| Adding an artist writes the artist, albums and track lists in one transaction, with the monitoring chosen | `music.TestAnArtistIsAddedWithItsAlbums` |
| An album is wanted when monitored, released and missing a track | `music.TestWhatAnArtistIsMissing` |
| A ceiling hides films and series, not music or books | `library.TestACeilingIsForFilmsAndSeries` |
| The API adds an artist, lists its albums, sets an album's monitoring, and lists wanted albums, scoped | `api.TestAnArtistIsFollowed` |

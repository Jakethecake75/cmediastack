# ADR-0046: Searching for an album, grabbing it, and importing what arrives

**Status:** accepted
**Date:** 2026-09-30
**Related:** [ADR-0023](0023-searching-for-a-wanted-episode.md),
[ADR-0026](0026-adding-a-film-and-searching-for-it.md), [ADR-0033](0033-season-packs.md),
[ADR-0044](0044-music-artists-albums-and-tracks.md), [ADR-0045](0045-music-files.md)

## The problem

An album can be followed (ADR-0044) and imported from a download
(ADR-0045), and there is no way to get a download of one. A film and an
episode are searched for, matched by name and grabbed with the match sealed
into the ticket, so the import files the download under what the server
decided it was, whatever the release calls itself. An album needs the same
thing. The parts that fit a film do not fit it: the video parser reads
`Dune.2021.2160p.WEB-DL`, not `Portishead - Dummy (1994) [FLAC]`, and a video
quality profile has nothing to say about a FLAC.

## Decisions

### 1. The target is the artist and the album, sealed into the ticket

`search.Target`, `download.Target` and the queue row gain an **album**: the
artist is the item, as for every other target, and the album's id sits
beside it. Migration 0027 widens `target_kind` to `'album'`, which SQLite can
do only by rebuilding the column as migration 0019 did, and adds
`target_album_id`. An album target has no season, no episode, and is neither
a film nor a pack; any other shape is refused on the way in and read back as
no target at all, as for the others.

A target that lost its album would name the artist with season 0 and episode
0. That is not a valid episode, so it would be refused, not guessed at.

### 2. Which release is the album: its folded name starts with the artist and then the album

Release names for music follow no one convention. `Portishead - Dummy (1994)
[FLAC]`, `Portishead-Dummy-CD-FLAC-1994-GRP` and `Portishead.Dummy.1994.MP3`
all occur. None of them is parsed into parts. Each is **folded** the way every
other title is compared (`search.NormalizeTitle`: accents, case, punctuation),
and then it is the album when:

1. the folded name **starts with the folded artist**, then **the folded album
   title**, on word boundaries; and
2. the word after the album title is **the end, a year, or a tag**. A tag is a
   format (`flac`, `mp3`, `320`, `v0`, `24bit`, …), a source (`cd`, `web`,
   `vinyl`) or an edition word (`deluxe`, `remaster`, `remastered`, `expanded`,
   `edition`, `anniversary`, `reissue`). This is what tells *Dummy* from *Dummy
   Live*, and *Portishead* from *Portishead Roseland NYC Live*.

A discography and a compilation do not start with the album's title, so they
are refused as a different album.

### 3. A year is needed only when the title is ambiguous

A film without a year is refused, because titles recur between films
(ADR-0026). An artist's album titles rarely recur. When the artist has **no
other album of the same folded title**, a release that names no year is
accepted. When it has one, as with Weezer's several *Weezer* albums, a release
needs a year within one of the album's. A release that names years, none
within one of the album's, is refused in either case. A 2014 remaster of a 1994
album usually names both years, so any year within one is enough.

### 4. Quality: a ladder read from the name, and Unknown is refused

The quality is read from the name, since no file has been seen yet:

| Rank | Quality | From |
|---|---|---|
| 6 | FLAC 24-bit | `flac` with `24bit`, `24 bit`, `24-96`, `24-192`, `hi-res` |
| 5 | FLAC | `flac`, `alac`, `lossless` |
| 4 | MP3-320 | `320` |
| 3 | MP3-V0 | `v0` |
| 2 | MP3 / AAC / Vorbis / Opus | `mp3`, `aac`, `m4a`, `ogg`, `opus` |
| 0 | Unknown | none of these |

A release whose format cannot be read is **refused**, as a video release of
unknown quality is unless a profile lists it: a grab of it could be a lossy
rip of anything. Matches sort by rank, then by seeders. A video quality
profile is not applied.

### 5. The indexers are asked for audio

A search sends Torznab category **3000 (Audio)**, which includes its
subcategories, and the term `<artist> <album>`, folded. A term the operator
types in its place changes what is asked for, never what can match.

### 6. The import: an album target goes to the music library

The import pass sends a completed download with an album target to
`music.Library.ImportAlbum` (ADR-0045, decision 5), not to the video
importer. Its outcome is recorded against the download as every import's is:
imported when a track was placed or replaced, skipped with the summary
otherwise, so a failed import is retried hourly rather than every pass. The
video importer is never asked to guess what a music download is.

### 7. The route

`POST /api/v1/albums/{id}/search` needs `acquisition.search`. It answers for
an album the caller may see, and a hidden one answers exactly as a missing one
does (ADR-0037). Only a candidate that is the album and is accepted carries a
ticket. The queue labels an album download *Portishead — Dummy (1994)*.

## Not decided here

Searching for wanted albums automatically, as the acquisition loop does for
episodes and films, is the next increment. So is upgrading a held album to a
better release.

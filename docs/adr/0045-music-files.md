# ADR-0045: Music files — found by folder and track number, filed by the album's track list

**Status:** accepted
**Date:** 2026-09-30
**Related:** [ADR-0015](0015-library-path-containment.md),
[ADR-0016](0016-import-pipeline.md), [ADR-0044](0044-music-artists-albums-and-tracks.md)

## The problem

An artist's albums and tracks are known (ADR-0044), and no file can yet be one of
them. Two ways in are needed: a music folder that already exists — the scan —
and an album that was downloaded, the import. The video importer answers a
different question, which film or episode a release is, from its name. A
music download is an album of a dozen files, and the question is which file is
which track.

## Decisions

### 1. What a music file is

A file whose extension is `.flac`, `.mp3`, `.m4a`, `.aac`, `.ogg`, `.opus` or
`.wav`. Its quality is named by its container — FLAC, MP3, AAC, Vorbis, Opus,
WAV — and ranked **FLAC and WAV above the lossy formats**: a lossless file is
never replaced by a lossy one. Bit rate is not read; that needs the file parsed,
and the parser jail is for video (ADR-0020).

### 2. Where it is filed

`<artist folder>/<album title> (<year>)/<NN> - <track title>.<ext>`, and
`<D>-<NN> - …` for an album on more than one disc. Every component is built
from MusicBrainz's names by the rules that already make a film's folder safe:
no separators, no leading dot, no control characters (ADR-0015).

### 3. Which track a file is: its number, then its name

A file is matched to a track of **the album it is for** by, in order:

1. a leading track number in the file name — `03 - Strangers.flac`,
   `03. Strangers`, `03 Strangers`, and `1-03 …` or `103 …` on a multi-disc
   album — that the track list has;
2. otherwise the track's title, folded as release names are (ADR-0023), found
   in the file name, when exactly one track's title is.

A file matching no track, or two, is left alone and reported. Tags inside the
file are not read (decision 1).

### 4. The scan finds a followed artist's albums in their folders

A scan of a music root walks `<artist folder>/<album folder>/…`. The artist is
the followed artist whose folder that is. **A folder no followed artist owns is
reported and left alone**: an artist is followed from MusicBrainz, never guessed
from a folder name. The album is that artist's album whose folded title the
album folder's name starts with, once a year in brackets and anything in square
brackets are set aside. Each file is recorded and its track's file set. A scan,
as for video, changes nothing on disk, and refuses to believe that most of a
root vanished at once.

### 5. The import files a downloaded album, track by track

`ImportAlbum` takes a download — a contained source (ADR-0016) — and the album
it was grabbed for. Each audio file is matched to a track (decision 3) and
hard-linked, or copied across filesystems, to its place. A track that already
has a file keeps it unless the new one is lossless and the old one lossy; the
replaced file goes to the trash (ADR-0016). Files that match nothing stay in the
download and are reported. The import runs as `system:import`, whose grant
already covers it.

## Rejected alternatives

**Reading tags.** Tags would identify a file whose name does not, and they come
from a parser reading a stranger's bytes, which belongs in the jail. The file
names of a released album carry the track number almost always; tag reading can
come later, jailed.

**Creating artists from folders a scan finds.** A folder name is not an
identity. Films and series do it, then ask the provider to confirm (ADR-0019).
For music, adding from MusicBrainz is one step, and a folder the scan could not
place is shown to the operator.

## Known limitations

- A file named only by its title with a typo, or by a track number the chosen
  release does not have (a bonus track), is not matched.
- Bit rate and bit depth are not compared: an MP3 at 128 kb/s is not replaced by
  one at 320.

## Verification

| Claim | Test |
|---|---|
| Audio files and their quality; lossless outranks lossy | `music.TestAudioFilesAndTheirQuality` |
| A file is matched by number, then by title, and ambiguity is refused | `music.TestAFileIsMatchedToATrack` |
| Paths are built from the album's names, safely, per disc | `music.TestMusicIsFiledByTheTrackList` |
| The scan records a followed artist's albums, reports what it cannot place, and changes nothing on disk | `music.TestTheScanFindsFollowedArtists` |
| The import files a downloaded album, keeps lossless over lossy, trashes what it replaces | `music.TestADownloadedAlbumIsImported` |

# ADR-0076: One copy of an imported file, seeded from the library

**Status:** accepted
**Date:** 2026-10-05
**Related:** [ADR-0003a](0003a-torrent-engine.md), [ADR-0073](0073-player-loading-torrent-resume-casting.md), [ADR-0053](0053-deleting-one-file-and-purging-now.md)

## The problem

The operator found two copies of an imported film: the one in the library,
which plays, and the one in the download directory, which seeds.

An import hardlinks the download into the library: one file, two names. A
hardlink cannot cross filesystems, and on Proxmox the library is often a
separate mount from the downloads. There the importer fell back to a copy,
said in the history that it used twice the disk space, and kept both.

## Decision

### 1. Across filesystems: copy, then link the download to the copy

`library.Place` is now the one way the importer, the music library and the
books library put a download's file into the library:

1. **Hardlink** when both are on one filesystem, as before.
2. **Otherwise copy**, then replace the download's file with a **symbolic
   link** to the library's copy. The link is made beside the file, inside the
   download (through its held `os.Root`), and renamed over it, so the name
   always resolves.

The torrent seeds through the link, so the file that seeds is the file that
plays, and its bytes are on disk once.

- The download's file is replaced only when the copy is the same size and the
  original is a regular file.
- If the link cannot be made, both copies stay. The history and the log say
  so, as they did for every copy before.
- The file's "shared with a download" flag is true for a link as it is for a
  hardlink.
- Subtitles are still copied: they are small.

### 2. A finished download never fetches again

When a download is marked complete, the engine stops it from downloading
data (`DisallowDataDownload`); it still seeds.

Without this, a file trashed or renamed in the library would leave the link
dangling. The torrent library would then find the piece unreadable, fetch it
again, and write it through the link, putting the file back into the library
under its old name. Finished downloads are not resumed after a restart
(`Resumable` excludes them), so the seal is needed only while the process
that finished them runs.

### 3. Removing a download removes the link, not the film

**Remove** (ADR-0070) deletes the download's folder. A symbolic link inside
it is removed, not followed, so the library's copy stays.

## Consequences

- A library on another filesystem uses its disk space once, not twice.
- After a restart, a finished download is not seeding, as before. Seeding
  across restarts is a separate decision.
- The library package's structural test still bans `os.Symlink`. The link is
  made through the download's `os.Root`, outside every library.
- Tests: `TestPlaceKeepsOneCopyAcrossFilesystems` (a tmpfs download and a
  disk library), `TestAShortCopyIsNotLinkedTo`.

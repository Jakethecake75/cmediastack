# ADR-0073: The player loads its own stream, downloads survive a restart, casting

**Status:** accepted
**Date:** 2026-10-04
**Related:** [ADR-0072](0072-a-full-screen-player-and-cheaper-playback.md), [ADR-0071](0071-transcoding-and-grab-on-add.md), [ADR-0003a](0003a-torrent-engine.md)

## The problems

After v0.4.2 the operator reported three things:

- Project Hail Mary still stopped to buffer, less often than before.
- When the server restarts, a download starts again from nothing.
- The player has no way to cast to a TV or a Chromecast.

## Decisions

### 1. The player fetches a converted stream itself

The film was measured in the operator's own Chrome, on the copy path (no
transcode):

- Over 86 seconds of playback, Chrome never held more than **2.7 seconds**
  ahead of the picture, and often less than one.
- It stalled at 1:28.

The server can send that stream at 75 Mb/s, and the film averages 32.5 Mb/s.
Chrome holds so little because a stream that cannot be read by range is
fetched only a few seconds ahead. Any hiccup on the network is then a stall.

For a converted stream the player now does the fetching:

- It reads the stream with `fetch` and appends it to a Media Source
  `SourceBuffer`.
- It stops reading at **60 seconds ahead**, and lets go of what was played
  more than 20 seconds ago.
- When the browser's memory limit for one stream is reached
  (`QuotaExceededError`), it keeps what fits. For 4K at 40 Mb/s, about
  20–30 seconds fit in Chrome.

In local runs over HTTPS, a copied 4K stream held 20–31 s ahead and a 1080p
transcode 31 s and rising, where Chrome alone kept 2.5 s. Stats for Nerds
says which is loading the stream. The MIME type comes from the plan:

- **Picture:** H.264 for a transcode; for a copy or remux, the source's codec
  (10-bit HEVC as `hvc1.2.4`).
- **Sound:** AAC or Opus when rebuilt, otherwise the copied codec.

When the browser has no Media Source, or will not take that type through it,
the player gives it the address as before. Direct play is unchanged: a file
read by range is buffered properly by the browser.

### 2. A restart keeps an unfinished download

The torrent library's file storage writes an unfinished file as
`<name>.part` by default. When a torrent is opened, it marks **every piece of
any file still in that state as not finished**. Its own comment calls this a
TODO. The record of finished pieces was kept (the bolt database in the
download directory), but erased at each start. The operator's download of
*Air* showed exactly that: after a restart, the bytes finished equalled the
bytes received since the restart.

The engine now builds the storage itself:

- part files off, so files are written under their own names in
  `<data_dir>/<infohash>/`;
- the bolt record opened explicitly.

If the bolt record will not open, the engine runs on an in-memory record, and
a note on the Downloads page says a restart will fetch unfinished files
again. Before, this fallback happened silently.

A `.part` file left by an earlier version is renamed to its own name when the
torrent is next opened. Only names inside the transfer's own directory are
renamed, since the names come from the torrent. Pieces it holds that the
record has already lost are fetched again, once.

### 3. Casting is the browser's

A Cast button appears in the top bar while a device can take the video
(`HTMLMediaElement.remote.watchAvailability`), and opens the browser's own
device picker (`remote.prompt()`). Chrome and Edge cast to a Cast device and
Safari to AirPlay. Nothing is added to the server, and no third-party script
is loaded.

The Cast SDK, with the Chromecast fetching the stream from the server itself,
is not used:

- the device cannot carry the session cookie, so every stream would need a
  separate address that works without signing in;
- the Proxmox install serves HTTPS with a certificate a Chromecast does not
  trust;
- Cast's script is loaded from Google, which the content security policy
  does not allow.

The operator's laptop saw no Cast device at all (the Presentation API reported
none for the default receiver), so casting is untested against a real device.
A browser can always cast the whole tab from its own menu.

## Consequences

- A converted stream uses more memory in the browser, up to the browser's own
  limit for one stream.
- One fetch per playback, as before, but the player now reports a refused
  conversion (503, 409) with the server's message instead of a format error.
- Downloads in progress during the upgrade keep their data, but pieces
  already lost from the record are fetched again.

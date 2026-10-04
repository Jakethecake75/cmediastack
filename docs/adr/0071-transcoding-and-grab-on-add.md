# ADR-0071: Transcoding, grabbing on add, and finished downloads

**Status:** accepted
**Date:** 2026-10-04
**Supersedes:** [ADR-0005](0005-transcode-policy-skylake.md) decision 4 (no HDR tone mapping) and its refusal of a software video transcode; [ADR-0030](0030-automatic-acquisition.md)'s "off by default" for the Proxmox install
**Related:** [ADR-0020](0020-playback.md), [ADR-0065](0065-socks5-from-the-web.md), [ADR-0070](0070-one-search-live-downloads-remove-deletes.md)

## The problem

The first downloads on the operator's instance would not play.

- **Project Hail Mary** is HEVC Main10, 4K HDR10, with TrueHD audio.
- **Widow's Bay** is HEVC Main10 with EAC3 audio.

No browser decodes 10-bit HEVC. The server could only remux, which keeps the
video, so it refused both, saying "this needs a video transcode, which this
server does not do". ADR-0005 chose that for a 4-core i5-6500T. The operator
asked for transcoding.

Two further problems:

- Adding a film put it on the Wanted list and nothing more. Automatic
  acquisition is off unless the configuration turns it on, and the installer
  never did.
- The operator asked for Downloads to separate what is finished from what is
  still downloading.

## Decisions

### 1. Convert transcodes the video when a remux would not help

Conversion stays one button, **Convert and play**. It still remuxes when that
is enough, which is cheap. When the browser cannot decode the video
(the codec, 10-bit, HDR), the plan becomes a transcode:

- **Video:** H.264 High, 8-bit `yuv420p`, libx264 `veryfast`, CRF 21, a
  keyframe every 48 frames.
- **Size:** scaled to at most **1080p**, or **720p** if the viewer chooses.
  The aspect ratio is kept and the width is even; a source no taller is not
  scaled.
- **HDR (PQ or HLG):** tone-mapped to SDR BT.709 with `zscale` and `tonemap`
  (Hable). Debian 12's ffmpeg has both, and was checked by running the chain.
- **Audio:** the default track, re-encoded to stereo AAC (or Opus).

Measured on a 28-thread machine: 10 s of 4K HDR10 HEVC became 1080p SDR H.264
in 3 s, about three cores' worth at real time. A 1080p source costs much less.
The container's default rises to 4 cores and 4 GB. Existing containers can be
given more on the Proxmox host (`pct set <id> --cores 4 --memory 4096`).
Hardware encoding (VAAPI) is left for later: it needs `/dev/dri` passed into
the container.

The admission limit on conversions (ADR-0005's concern) stays.

### 2. A converted stream can seek

The output is a pipe, so a seek restarts it. `GET /files/{id}/convert` takes:

- `start`: whole seconds, from 0 up to the file's duration;
- `height`: 720 or 1080, and nothing else.

ffmpeg seeks with `-ss` before its input. The player keeps the offset:

- its own scrubber and clock show the film's time, not the stream's;
- a saved position is the offset plus the stream's time, against the file's
  duration;
- subtitles are shifted by the offset;
- resume starts the stream at the saved place.

### 3. Adding a title starts its search

The Proxmox install turns automatic acquisition on: new installs get
`acquisition.automatic: true`, and an update adds it where it is absent. When
a title is added, the search for its kind runs at once: films and series run
`acquire.search`, music runs `acquire.albums`, books run `acquire.books`. A
title never searched comes first, so the best release the default profile
accepts is grabbed without waiting for the next pass. The usual rules still
hold: matched to exactly one wanted item, seeded, and never grabbed before.

Until a SOCKS5 proxy is set, a grab leaves by the container's own address, the
same as one by hand. Getting started already lists the proxy before the
indexers.

### 4. Downloads: Downloading and Finished

Downloads shows two lists:

- **Downloading:** everything still transferring.
- **Finished:** complete or seeding.

Both refresh live (ADR-0070).

## Consequences

- One viewer transcoding 4K HDR can use most of a small container's CPU, and
  the conversion limit (2 by default) is what keeps a third viewer from
  starting.
- Tone mapping is an approximation: colours differ from an HDR display, but
  the film is watchable rather than refused.
- An operator who wants nothing fetched automatically sets
  `acquisition.automatic: false`; the update does not undo that.

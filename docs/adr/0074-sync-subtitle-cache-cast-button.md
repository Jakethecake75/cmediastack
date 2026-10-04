# ADR-0074: Sound in step with a copied picture, subtitles read out once, a cast button

**Status:** accepted
**Date:** 2026-10-04
**Related:** [ADR-0073](0073-player-loading-torrent-resume-casting.md), [ADR-0072](0072-a-full-screen-player-and-cheaper-playback.md), [ADR-0071](0071-transcoding-and-grab-on-add.md), [ADR-0020](0020-playback.md)

## The problems

With v0.4.3 the buffering was gone, and the operator reported three more
problems:

- The sound was well out of step with the picture.
- Choosing the Finnish subtitles of Project Hail Mary said they would not load.
- There was still no cast button.

## Decisions

### 1. A copy starts its sound at the picture's keyframe

A copied picture can only begin at a keyframe. This UHD encode has one every
10.4 seconds. ffmpeg's default accurate seek trims the re-encoded sound to the
asked-for second. Measured on a test file, starting at 0:15: the picture began
at the keyframe at 0:10.4 and the sound at 0:15, which put a 4.6-second gap at
the start of the stream. A browser does not honour a gap like that. The
operator's player resumes part-way on every open (here at 6:14), so the sound
was out by up to a GOP every time.

- **The sound starts with the picture.** A copy or remux that starts part-way
  passes `-noaccurate_seek`, so both begin at the keyframe. Measured: both
  streams start at 0, as they do from the beginning. A transcode cuts both
  exactly at the second, as before.
- **The server says where it started.** It reads the first video packet after
  seeking there (one packet, with ffprobe, in the sandbox) and sends the time
  as the `X-Stream-Start` header. A transcode reports the asked-for second.
- **The player follows it.** The player fetches the stream itself (ADR-0073),
  so it reads the header and moves its clock and subtitles to match. A browser
  given the address alone cannot see the header: its sound is still in step,
  but its clock can be ahead by up to a GOP.

### 2. Embedded subtitles are read out of the film once

A text track inside a film is spread through the whole file, so taking one out
means reading all of it: minutes for a 38 GB UHD disc. The conversion was
bounded at 30 seconds, so every embedded track of a large film failed, with
the sentence the operator saw.

- **One pass, cached.** The first time a film's subtitles are needed, every
  embedded text track is read out in a single pass into one small Matroska
  file, as WebVTT. It lives in `subtitles/` beside the database and is named
  by the file's id and size, so a replaced file is read again. After that,
  each track is converted from that small file in well under a second, and
  the film is not touched.
- **Started early.** The read starts when the player opens the film (its
  track list is asked for then), so it is usually done before a track is
  chosen.
- **Waiting is said, not failed.** A request waits up to 20 seconds for the
  read. If it is still running, the answer is **503** with `Retry-After: 15`.
  The player says the subtitles are being read out of the file, and tries
  again until they load.
- **Your choice stays put.** Once captions have been on, Chrome turns a track
  on by itself whenever new ones are attached, for example after a jump. The
  player now puts the viewer's choice back whenever the track list changes.

### 3. The cast button is always there

ADR-0073 showed the button only while the browser reported a device for this
video. On the operator's laptop it reported none, so there was no button. The
button now shows wherever the browser can cast at all (`remote.prompt`), and
says why when nothing answers. With no device it says the TV or Chromecast
must be on the same network, and that a VPN can hide it. Otherwise it points
to the browser's own *Cast…* menu, which casts the whole tab.

## Consequences

- A film's subtitles take one full read of the file the first time. On the
  operator's server that is minutes for a UHD disc, and nothing after.
- The cache is a few hundred kilobytes per film and is never pruned. A deleted
  film leaves its file behind.
- `-noaccurate_seek` starts a copy up to a GOP before the asked-for second.
  The viewer sees those seconds again, with sound.

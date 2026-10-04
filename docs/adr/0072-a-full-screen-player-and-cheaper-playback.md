# ADR-0072: A full-screen player, and playback that keeps up

**Status:** accepted
**Date:** 2026-10-04
**Related:** [ADR-0071](0071-transcoding-and-grab-on-add.md), [ADR-0020](0020-playback.md), [ADR-0005](0005-transcode-policy-skylake.md)

## The problem

After ADR-0071 the HEVC films played, but slowly, stopping to buffer every few
seconds. The operator also asked for a player like Plex's in place of the
browser's own controls under a page heading.

The transcode was measured on Debian 12's ffmpeg with a 20-second 4K HDR10
HEVC file at 40 Mb/s, pinned to cores of a much faster CPU than the target's:

| Cores | Decode only | ADR-0071's transcode (veryfast, 1080p) | superfast | superfast, 720p |
|---|---|---|---|---|
| 4 | 2.90× | 1.25× | 1.45× | 2.01× |
| 2 | 1.83× | 0.74× | 0.86× | 1.22× |

The container's default was 2 cores until ADR-0071. Below 1×, the stream falls
behind and the browser stops each time it runs dry. ADR-0005 predicted this:
decoding 10-bit HEVC and tone-mapping it in software is not a real-time job for
a few Skylake cores. No ffmpeg setting fixes that, so the fix is to transcode
less, transcode cheaper, and stop less often when it must.

## Decisions

### 1. A browser that decodes HEVC gets the original picture

Chrome and Edge on Windows (with a GPU that decodes HEVC) and Safari decode
HEVC Main10 and show HDR, tone-mapping it themselves on an SDR screen. For them
the video is copied, not transcoded, and only the sound is rebuilt. That is a
remux, about 25× real time.

- The player asks the browser (`navigator.mediaCapabilities.decodingInfo`,
  4K 10-bit PQ HEVC in MP4) and, when it says yes, adds `hevc=1` to
  `/files/{id}/playback` and `/files/{id}/convert`.
- `hevc=1` widens the capability set to HEVC, 10-bit and HDR for the
  conversion plan only. Direct play is still decided without it, because a
  browser that decodes HEVC in MP4 may not open it in Matroska.
- Copied HEVC is tagged `hvc1`, the tag Safari requires.
- If the browser fails to decode the copied picture, the player falls back to a
  transcode once, without asking.

### 2. A transcode is cheaper

- libx264 `superfast` instead of `veryfast`: about 15% faster, with a larger
  stream, which matters little on a home network.
- After two stalls within 90 seconds at 1080p, the player drops to 720p and says
  so, unless the viewer chose a quality. At 720p the tone mapping runs on fewer
  pixels, and it is the cheapest setting measured.

### 3. The server works ahead of the browser

Chrome reads a piped stream only about 2.5 seconds ahead of the picture
(measured). Until now ffmpeg wrote straight into the response. It stopped as
soon as the browser stopped reading, so the stream never had more than those
few seconds in hand, and any dip in a transcode's speed was a stall.

Now a second goroutine reads ffmpeg's output into memory, up to **64 MiB per
conversion** (`ConvertAhead`), while the browser reads from that store. ffmpeg
works at full speed until the store is full: a transcode that runs faster than
real time on average builds a lead, and short dips use it up instead of
stalling the picture. Conversions are already limited (2 by default), so this
is at most 128 MiB. If the viewer goes away, ffmpeg is stopped and what it had
written is discarded.

### 3a. Fragments of a second (v0.4.2)

v0.4.1 still stalled on the operator's server, on the copy path with no
transcode at all: every 10.4 seconds, at the same points in the film. The UHD
encode has a keyframe every 10.4 s, and ffmpeg cut one MP4 fragment per
keyframe (`frag_keyframe`): about 42 MB each, which ffmpeg can only send
whole. Chrome starts fetching the next fragment shortly before it is needed,
and at the 65–75 Mb/s the server delivered on that network, 42 MB takes about
5 seconds. On localhost it took well under a second, which is why the local
checks did not show it.

Every conversion now passes `-frag_duration 1000000`: a fragment is cut at a
keyframe or after one second, whichever comes first. The same 40 seconds went
from 5 fragments (largest 53 MB) to 43 (mean 4.9 MB). Chrome plays fragments
that do not start on a keyframe.

### 4. A stall waits for a buffer

A stream that is barely keeping up stalls, plays half a second, and stalls
again. On a stall the player pauses until 10 seconds have arrived, or until
nothing more arrives for 3 seconds (the browser holding all it will), then
resumes. The viewer gets fewer, longer waits instead of constant stutter.

### 5. The player fills the window

Play opens a full-window player and starts on its own: direct play, else the
copy, else the transcode. Its controls appear on movement and hide after three
seconds of playback:

- **Top:** back; volume and mute; picture-in-picture and full screen, each
  shown only when the browser offers it.
- **Bottom:** the year and title; subtitles, speed and settings menus; the bar,
  with elapsed time, time remaining and the clock time it ends; back 10 s,
  play/pause, forward 10 s; **Info**.
- **Settings:** Quality (Original, 1080p, 720p, as the file allows), Aspect
  ratio (fit, fill, stretch) and Stats for nerds (mode, source, picture size,
  buffer ahead, dropped frames). How fast the stream *arrives* is not shown:
  the browser's own read-ahead limit makes that read about 1× whatever the
  server can do.
- **Info:** what is in the file, as the old page showed it.
- **Keys:** space or k plays and pauses, ← → jump 10 s, ↑ ↓ change the volume,
  m mutes, f goes full screen, Esc closes a menu or leaves the player.

Seeking a converted stream within what has already arrived is a seek; past
it, the stream restarts there (ADR-0071). Subtitles are the server's WebVTT
tracks, chosen from the player's own menu now that the browser's controls are
gone.

Left out because there is no data for them: **Chapters** and **Cast & Crew**.
Casting is also left out: a cast device cannot carry the session cookie the
streams require.

## Consequences

- The best fix for a weak server is still a release the browser plays as it
  is: 1080p H.264 plays directly at no cost.
- Hardware encoding (VAAPI) remains for later. It would remove the encode
  cost, but not the 10-bit decode, which a Skylake GPU cannot do.
- A converted stream at 2× speed needs the server to produce it twice as fast;
  the speed menu does not warn about that.

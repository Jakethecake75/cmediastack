# ADR-0020: Playback — the parser is jailed, the file is a descriptor, and the bytes are served directly

**Status:** accepted. Increment 4a (probing) implements the sandbox; the rest of
Phase 4 builds on it.
**Context:** [ADR-0005](0005-transcode-policy-skylake.md) sets the transcode
policy, [ADR-0006](0006-no-jellyfin-shim.md) says the only client is a browser
this project ships, and [ADR-0007](0007-process-boundaries.md) says hostile-input
parsers do not run in the process holding the master key. This ADR is how those
three are actually satisfied.

## The problem, stated without euphemism

This software downloads files from strangers on the internet and then points
`ffmpeg` at them.

`ffmpeg` is indispensable and is also millions of lines of C whose entire job is
parsing untrusted container and codec data, with a long CVE history in exactly
that code. Every media server has this exposure. Most treat it as a fact of
life; the point of this ADR is that it is a fact of life with a blast radius,
and the blast radius is a choice.

## Decisions

### 1. The parser runs in a namespace jail with no network

`ffprobe` and `ffmpeg` are started with `CLONE_NEWUSER | CLONE_NEWNET |
CLONE_NEWPID`, mapped to an unprivileged uid, with a hard timeout and capped
output.

`CLONE_NEWNET` is the one that matters. A new network namespace has nothing in
it but a down loopback, so a parser that is successfully exploited **cannot call
out, cannot reach the LAN, and cannot reach the NFS server the media is mounted
from.** It converts remote code execution into code execution in a box with no
exits — still bad, no longer a pivot.

This was verified rather than assumed:

| Check | Unsandboxed | Sandboxed |
|---|---|---|
| `net.DialTimeout("tcp", "1.1.1.1:443")` | `DIAL-OK` | `network is unreachable` |
| interfaces in `/proc/self/net/dev` | several | `lo` only |
| `id -u` | 0 | 65534 |

`playback.TestTheProbeSandboxCannotDialOut` and
`playback.TestTheProbeSandboxHasNoNetworkInterfaces` keep those true.

### 2. The parser is given a file descriptor, never a path

The application opens the media file through its `os.Root`
([ADR-0015](0015-library-path-containment.md)) and passes the **open descriptor**
to the child, which reads `/dev/fd/3`.

This is the decision worth arguing for. Passing a path means the parser resolves
it, so path handling becomes a shared responsibility between a Go program that
is careful and a C program that is not — and every symlink, every `..`, every
race between check and open is back in scope.

Passing a descriptor removes the question. The child cannot open a file it was
not handed, because it was never told where anything is. The kernel-enforced
containment of `os.Root` extends all the way into the parser, which is exactly
what a `RESOLVE_BENEATH` design is for.

It costs nothing: `ffprobe` reads `/dev/fd/3` identically to a path, including
seeking for metadata stored at the **end** of a file — verified against a
non-`faststart` MP4, which is the case that would have failed had it been
reading a stream.

### 3. Direct play serves bytes from a descriptor too, with `ServeContent`

`http.ServeFile` takes a path and is therefore not usable here. `ServeContent`
takes an `io.ReadSeeker` — which is what `os.Root.Open` returns — and implements
HTTP range requests, conditional requests and the `206` handling a `<video>`
element needs for seeking.

So the same property holds on the serving side: **nothing in the playback path
ever turns a request into a filesystem path.** A request names a library file by
id; the id is resolved to a descriptor through the root; the descriptor is what
gets served.

### 4. A playback request is authorized by the session, not by a signed URL

A `<video src>` cannot set an `Authorization` header, which is why media servers
reach for tokens in query strings. This project does not need to: the player is
same-origin with the application ([ADR-0006](0006-no-jellyfin-shim.md)), so the
session cookie is sent with the media request like any other.

That keeps one authorization model instead of two, and it keeps revocation
meaningful — a signed URL outlives the session that minted it by design, which
is the opposite of the reason this project chose opaque server-side sessions
over JWTs in the first place — immediate revocation.

**Short-lived signed URLs remain the mechanism for casting**, if casting is ever
built, because a cast receiver is a different device with no cookie. That is a
deliberate, separate, later decision — not the default.

### 5. What can be played is decided from the file and the client, and a refusal says why

The probe records container, codecs, profile, bit depth, resolution and track
layout. The player reports what it can decode. Direct play happens when they
agree ([ADR-0005](0005-transcode-policy-skylake.md)).

When they do not, and the hardware cannot bridge the gap, the answer is a
**refusal naming the missing capability** — "this file is HEVC 10-bit and this
server cannot transcode it" — not a spinner. A viewer can act on the first and
cannot act on the second, and on an i5-6500T the second is the likely outcome
for 4K HDR by design rather than by accident.

## Rejected alternatives

**Linking libav\* into the process.** Faster, simpler, and puts the CVE in the
address space holding the master key. Also forbidden by
[ADR-0002](0002-go-no-cgo.md), which the Dockerfile asserts against the built
artefact rather than trusting the environment.

**Probing at import time only.** Tempting — one probe per file, forever. It is
wrong because a file can change underneath the library (a re-download, an
operator's own re-encode), and a stale probe produces a playback failure that
looks like a bug in the player. The probe is cached against **size and
modification time**, so a changed file is re-probed and an unchanged one is not.

**A pipe instead of a descriptor.** `ffprobe` accepts `pipe:0`, and a pipe
cannot seek. For an MP4 with its `moov` atom at the end — which is most files
that were not written for streaming — that means reading the whole file to
learn its duration. On a 40 GB remux that is not a probe, it is a copy.

**Trusting the client's capability report.** Browsers under-report, and
`MediaSource.isTypeSupported` is advisory. The report is used to *narrow* what is
attempted, never to widen it: a client that claims HEVC support still gets
direct play only if the file is something this server would serve anyway.

## Addendum: what a browser actually does, and the fix that was nearly wrong

Direct play is built, and the first end-to-end run through a real browser failed
with `MEDIA_ERR_SRC_NOT_SUPPORTED` on an MKV the server had just described as
*"plays as it is — H264 1920x1080 in a matroska container"*.

The obvious culprit was the server's capability list claiming Matroska. Asking
the browser appeared to confirm it:

    canPlayType("video/x-matroska")                        -> ""
    canPlayType("video/x-matroska; codecs="avc1.640028"")  -> ""

Removing Matroska was nearly committed. It would have refused **most of a real
library**, and it would have been wrong.

Serving the actual bytes settled it:

| file | served as | result |
|---|---|---|
| `.mkv` of VP9 + Opus | `video/x-matroska` | **plays** — 320×180, 3.008 s |
| `.mkv` of VP9 + Opus | `video/webm` | **plays** |
| `.webm` of VP9 + Opus | `video/webm` | **plays** |
| `.mkv` of H.264 + AAC | any type | `MEDIA_ERR_SRC_NOT_SUPPORTED` |

Chromium demuxes Matroska perfectly well. What it refused was **H.264** — the
test browser is the open-source Chromium build, which ships without the
proprietary codecs real Chrome has. The container was never the problem, and the
"bug" was a property of the test environment.

**The rule this establishes is about `canPlayType`, not about MKV.** It is
advisory and it is pessimistic: it said no to a file that played. So it may be
used to **widen** what is attempted and never to narrow it. The player follows
that — it asks the browser only when the server has already refused, never to
overturn an acceptance — and `playback.TestTheDefaultCapabilitySetStillClaimsMatroska`
records why the list looks the way it does.

A second thing fell out of the same experiment: **ffprobe cannot tell a Matroska
file from a WebM one.** All three fixtures above — including a genuine `.webm` —
report `format_name` as `matroska,webm`, because one demuxer handles both. That
is fine, because the container name is not what decides playback. The codecs
are, and those ffprobe reports exactly.

### What direct play actually is, and the audio consequence

Serving a file as it is means handing the **whole container** to the browser,
and the browser picks the tracks. There is no way to say "use track 3" without
remuxing, which is not direct play.

So the audio question is never *"does this file contain a track I can decode"*.
It is *"will the track the browser reaches for be one it can decode"* — the one
marked default, or the first. A remux carrying TrueHD first and AC3 second is
refused, because the viewer would otherwise get a picture and silence.

Tracks **other** than the default that the browser cannot decode are a **caveat**
rather than a refusal: the film plays, and switching language mid-film will not.
Saying so beats letting somebody discover it.

### Bytes, verified

`http.ServeContent` over a vaulted descriptor, in a browser: `readyState` 4,
1920×1080 decoded, playback advanced, and a seek to 110 s of a 120 s file served
as three `206` responses at different offsets. `http.ServeFile` was mutation-
tested as the alternative and **served the master key** from a path above the
library.

## Addendum: converting, and what it is allowed to change

Direct play covers the files a browser can already open. The rest divide in two,
and the division is the whole design:

- **The video is fine; the container or the soundtrack is not.** An H.264 film
  in Matroska with TrueHD audio. A stream COPY of the video plus a re-encode of
  one audio track fixes it, at about **50× realtime** — measured, 120 s of video
  in 2.28 s — because the expensive part is copied.
- **The video itself is the problem.** HEVC, 10-bit, HDR. A remux moves the same
  pictures into a different box and changes nothing, so it is refused with the
  reason rather than attempted.

`PlanRemux` decides which, and the refusal names the capability exactly as a
playability blocker does.

### The output codec is the client's choice from the server's list

Not a constant. A Chromium built without the proprietary codecs has **Opus and
no AAC at all** — that is what this project's browser tests run against, and it
is what a Linux distribution usually ships. Hardcoding AAC would work everywhere
it is present and produce silence everywhere it is not, which is the failure
being fixed.

So the client names a codec and the name is matched against a fixed map before
anything reaches ffmpeg's argument vector. `aac` and `opus` are the only
entries; there is no path by which a request string becomes an argument, and the
check is repeated where the argument is built rather than trusted to have
happened at the HTTP edge.

### Measured by audio, because "it loaded" proves nothing

An early probe reported both the original and the converted file as loaded,
which proved only that the video decoded. `webkitAudioDecodedByteCount` is the
measurement that matters:

| stream | audio decoded |
|---|---|
| the file as it is (AC3 default track) | **0 bytes** |
| converted | **106,160 bytes** |

### Bounded, and not seekable

Conversions are admission-controlled from
`config.media.max_concurrent_transcodes`, which existed and was honoured by
nothing. An unset value becomes a small number rather than unlimited: on four
cores, an unbounded number of ffmpeg processes takes the working sessions down
with the new one.

The output is a pipe produced as the viewer watches, so there is no file to
serve a byte range from. The response says `Accept-Ranges: none` and the player
says so in words. Seeking would mean restarting ffmpeg at an offset and carrying
that offset in the player's own arithmetic — a real design, and not this
increment.

## Addendum: subtitles, and the two things that make them safe

Subtitles arrive in a library two ways — inside the container, and as files the
importer placed beside the video — and a browser accepts neither. A `<track>`
element takes WebVTT and nothing else, so every track is converted on the way
out. That conversion is ffmpeg, which means a media parser, which means
everything above applies unchanged: fd 3, the jail, no network, no environment.

### A subtitle is text a stranger wrote

This is the part worth being precise about, because the obvious worry is the
wrong one. The cue text of a downloaded subtitle can contain anything —
`<script>`, an `<img onerror=>`, entities — and the file is displayed to the
operator in their own browser, same-origin, with their session cookie.

Two things make that inert, and only the second is a guarantee:

1. **ffmpeg's WebVTT encoder drops tags it does not recognise.** Useful, and not
   relied upon. It is a property of a converter that exists to convert, not a
   security boundary, and a future version that passed markup through would not
   be a bug in ffmpeg.
2. **The response is `text/vtt`, so the browser hands it to the WebVTT cue
   parser rather than to the HTML parser.** WebVTT's cue grammar has a closed
   set of tags (`b`, `i`, `u`, `c`, `v`, `ruby`, `lang`), no attributes that run
   anything, and no script. Cue content becomes text nodes. This is the
   guarantee, and it is why the content type is a security property here rather
   than a formality.

Verified rather than assumed. A subtitle carrying
`<script>window.__PWNED=…;document.title='PWNED'</script>` and an
`<img src=x onerror=…>` was served to a real Chromium and turned on. The
browser's own `getCueAsHTML()` produced a single `#text` node — no `SCRIPT`
element, no `IMG` element — neither payload ran, and the document title was
untouched. The cue rendered the script's *inner text* as dialogue, which is
ugly and harmless.

`X-Content-Type-Options: nosniff` is set globally, so a browser cannot decide
the file is something more interesting than it claims.

### Bitmap tracks are listed and refused, not hidden

PGS, VobSub, DVB and XSUB are pictures of text. There is nothing to hand a
`<track>`: showing them means drawing them onto the video, which is a transcode
of the picture, which [ADR-0005](0005-transcode-policy-skylake.md) says this
server does not do. They are reported with `usable: false` and a sentence
explaining why, because an operator who can see that a PGS track exists and why
it is not offered has been told something true, and a list that silently omits
it has not.

### A track with no text is not a track

Found live, not by reasoning. A malformed ASS file — one whose `Format:` line
lists fewer fields than its `Dialogue:` lines use — converts *without an error*:
ffmpeg reads the timings, puts the text in the wrong field, and emits every cue
empty. The result was several hundred bytes of valid WebVTT, so a length check
saw nothing wrong, and the viewer got a subtitle track that displayed nothing
and explained nothing.

`toWebVTT` now refuses a conversion with no cue text at all, the route answers
`422`, and the player says which track failed. That last part was itself a bug
the same run found: the conversion path passed no failure handler, so a broken
subtitle on a converted stream said nothing. A failure handler that some callers
pass and others do not is not a failure handler.

### An id is a handle, not a name

A client selects a track by an id the server just issued (`e<stream index>` for
an embedded track, `s<n>` for a sidecar) and the server resolves it against a
freshly built list before opening anything. Nothing in the request becomes a
path or a stream index. The same containment as artwork
([ADR-0018](0018-metadata-and-artwork.md)).

Both layers were confirmed by removing the first: with id resolution deleted,
`Arrival (2016)/secrets.srt` — a real file in the library that was never offered
— was served, while `../outside.srt` and `/etc/passwd` were still refused by the
vault's `os.Root`. They stop different escapes, and neither is redundant.

### What this is not

It serves the subtitles a library already **has**. Fetching them from a provider
— OpenSubtitles and the rest, which is the larger half of replacing Bazarr — is
not built. See [DROPPED-FEATURES.md](../DROPPED-FEATURES.md).

ASS styling does not survive: positioning, fonts, colours and karaoke timing all
flatten to plain cues, because WebVTT has no equivalent and inventing one would
be worse. For dialogue that is invisible; for signs and songs typeset by a
fansub group it is a real loss, and it is stated rather than discovered.

### The structural rule grew a second half

`subtitles.go` does path work — it reads a directory and matches names against
the video's stem — and the first version of it also built ffmpeg's argument
vector. `TestNoMediaToolIsEverGivenAPath` failed on it, correctly: those two
things in one file are one variable name apart from a path reaching a parser.
The conversion moved to `vtt.go`, which is handed an already-open descriptor and
an integer and so has no filename to give away.

That test still only catches path-*shaped* literals, which is what made mistake
#18 possible. `TestTheOnlyInputAMediaToolGetsIsTheDescriptor` now checks the
place a path would have to arrive at: whatever follows ffmpeg's `-i` must be the
literal `/dev/fd/3`. Confirmed by planting a bare variable there — the older
test passed, the new one failed.

## Addendum: the container had no ffmpeg in it

Everything above describes how a media parser is confined. None of it described
whether the shipped image contained one, and it did not.

The runtime base is `distroless/static` — no shell, no package manager, and no
ffmpeg. `FFprobePath` is the string `"ffprobe"`, resolved from `PATH` by `exec`.
So the image built, started, browsed a library, and answered **every** playback
request with a bare `500` and nothing in the log. Probing, the direct-play
decision, remuxing and subtitles were all non-functional in the only supported
deployment, for the whole of Phase 4.

Every test in `internal/playback` passed throughout, because they run on a host
where ffmpeg happens to be installed. That is the shape of the gap: the code was
right, the deployment was not, and `go test` cannot see the difference.

### Three things were wrong, not one

1. **The tools were absent.** Fixed by copying static `ffmpeg` and `ffprobe`
   into the runtime image from a digest-pinned stage. Not `apt-get install`,
   which would put a package manager and a shell back into an image that exists
   to have neither.
2. **The boot log was actively misleading.** The only line about media said
   *"media parser sandbox is available"* — true, and it reads as "media parsing
   works". A guarantee about a jail says nothing about whether the thing it
   jails exists. Startup now checks for both tools and logs an `ERROR` naming
   what is missing, **before** the line about the jail.
3. **The failure had no diagnosis.** `exec: "ffprobe": executable file not
   found` became an internal error. There is now an `ErrToolMissing` and the
   routes answer `503` with the name of the missing program.

### What the static binaries cost

They are third-party artefacts in the trust path of the most
security-sensitive component here — the thing this ADR's jail exists to contain.
They are pinned by digest, so a rebuild cannot silently get different bytes, and
they are mode `0555`, so the process cannot rewrite the parser it executes.
Building ffmpeg from source in the Dockerfile was considered and rejected as
disproportionate: its configure matrix is a maintenance project of its own, and
a bad local build would be a worse outcome than a pinned known-good one.

### The jail, finally verified where it runs

The seccomp work in 4c was reasoned about arithmetically and never run in a
container. It has now been, and both halves hold:

| Run as | Result |
|---|---|
| Docker's **default** seccomp profile | `media parser sandbox is NOT available: this kernel refused to create a user namespace` |
| `deploy/seccomp-cmediastack.json`, `no-new-privileges`, `--cap-drop ALL` | `media parser sandbox is available`, and a real probe returned `"sandboxed": true` |

A subtitle was extracted by ffmpeg, inside the jail, inside the container. The
process runs as **uid 65532 in all four of real, effective, saved and
filesystem** — §2's "nothing runs as root, ever, including in the container",
checked against `/proc/<pid>/status` on the host rather than asserted.

## Known limitations

**Unprivileged user namespaces can be disabled.** Some hardened kernels and some
container runtimes turn them off (`user.max_user_namespaces=0`, or a seccomp
profile blocking `clone` flags). Where that is the case the jail cannot be
created, and the honest behaviours are: refuse to probe, or probe unsandboxed
with a loud warning. This project takes the second — with the warning at `WARN`
every time, not once at startup — because refusing would make the library
unusable on a host where every other media server works. That trade is recorded
here so it is a decision rather than a surprise.

**A mount namespace is not used yet.** `CLONE_NEWNS` with a minimal `/` would
also remove the child's view of the filesystem. It is not done in 4a because
`/dev/fd/3` resolves through `/proc`, and remounting `/proc` inside a new mount
namespace needs care that is not worth taking before the sandbox is otherwise
proven. The descriptor already removes the parser's *need* for paths; the mount
namespace would remove its *ability* to use them, which is strictly better and
is the next hardening step.

**A subtitle's validity is only known after converting it.** The listing reports
what tracks exist; whether a given one produces usable cues is discovered when a
viewer turns it on, and a malformed file fails then with a message rather than
being filtered out of the list beforehand. Converting every track on every
listing to find out would spend an ffmpeg per track per page view. The cost of
the choice is a subtitle that appears in the menu and then reports a failure.

**The seccomp profile is no longer `unconfined`** — and closing it turned out to
conflict with this ADR rather than merely follow it. Docker's default profile
permits `clone` only when no namespace flag is set, so adopting it would have
silently prevented the jail from ever being built. The container now loads
`deploy/seccomp-cmediastack.json`: the default plus one allowance scoped to
exactly the three namespaces above. See
[ADR-0007](0007-process-boundaries.md) for the reasoning and
[SECURITY.md](../../SECURITY.md) for the residual.

## Related

- [ADR-0005](0005-transcode-policy-skylake.md) — what the executor may attempt.
- [ADR-0007](0007-process-boundaries.md) — why it is a separate process at all.
- [ADR-0015](0015-library-path-containment.md) — the `os.Root` the descriptor
  comes from.

package playback

import (
	"fmt"
	"strings"
)

// Whether a file can be handed to a browser as it is, and if not, what exactly
// is in the way.
//
// # Why a refusal has to be specific
//
// ADR-0005 makes direct play the default and a refusal the honest alternative
// on hardware that cannot transcode its way out. That only works if the refusal
// says something a person can act on. "Playback failed" is the sentence every
// media server produces and nobody can do anything about; "this file is HEVC
// 10-bit and your browser decodes 8-bit only" tells somebody whether to change
// browser, change file, or stop trying.
//
// So the decision reports EVERY blocker rather than the first one. A file that
// is HEVC and 10-bit and HDR fails three ways, and a viewer who is told about
// them one at a time — switching browsers between each — is being led through a
// maze that was fully mapped on the first attempt.

// Blocker is one reason a file cannot be played as it is.
type Blocker struct {
	// Code is stable and machine-readable, for a UI that wants to group or
	// count. It is not shown to anybody.
	Code string
	// What is the specific thing: "hevc", "10-bit", "truehd".
	What string
	// Says is the sentence a person reads. It names the capability, because
	// naming the capability is the entire point.
	Says string
}

// Blocker codes.
const (
	BlockContainer  = "container"
	BlockVideoCodec = "video-codec"
	BlockBitDepth   = "bit-depth"
	BlockHDR        = "hdr"
	BlockAudioCodec = "audio-codec"
	BlockNoStreams  = "no-streams"
)

// Client is what the player says it can decode.
//
// Used to NARROW what is attempted and never to widen it. Browsers under-report
// and `MediaSource.isTypeSupported` is advisory, so a client claiming HEVC still
// only gets direct play if the file is something this server would serve anyway.
type Client struct {
	// Containers, as ffprobe names them: "mp4", "matroska", "webm". A client
	// sends the names it knows; matching is by membership, because ffprobe
	// reports a container as every format that could demux it.
	Containers  []string
	VideoCodecs []string
	AudioCodecs []string
	// MaxBitDepth is 8 for almost every browser. Zero means unstated, which is
	// treated as 8 rather than as unlimited: guessing high here produces a
	// stream that starts and then shows nothing.
	MaxBitDepth int
	// HDR is whether the client can present HDR. Almost never true, and the
	// server cannot bridge the gap on the target hardware (ADR-0005).
	HDR bool
}

// Plan is the answer.
type Plan struct {
	// DirectPlay is whether the file can be served as it is.
	DirectPlay bool
	// Blockers is empty when DirectPlay is true, and holds EVERY reason
	// otherwise.
	Blockers []Blocker
	// Caveats are things that will probably work and might not. They do not
	// prevent direct play; they are stated because the alternative is a viewer
	// watching a film in silence and having no idea why.
	Caveats []string
	// Summary is the whole answer in one sentence, for a UI that has room for
	// one line.
	Summary string
}

// DefaultAudio returns the track a browser will most likely choose: the one
// marked default, or the first if none is.
//
// This is the crux of what direct play actually IS. Serving a file as it is
// means handing the whole container to the browser, and the BROWSER picks the
// tracks. There is no way to say "use track 3" without remuxing, which is not
// direct play. So the question is never "does this file contain an audio track
// I can decode" — it is "will the track the browser reaches for be one it can
// decode".
func DefaultAudio(streams []AudioStream) (AudioStream, bool) {
	if len(streams) == 0 {
		return AudioStream{}, false
	}
	for _, a := range streams {
		if a.Default {
			return a, true
		}
	}
	return streams[0], true
}

// Decide works out whether p can be handed to c as it is.
func Decide(p Probe, c Client) Plan {
	var plan Plan

	if len(p.Video) == 0 && len(p.Audio) == 0 {
		plan.Blockers = append(plan.Blockers, Blocker{
			Code: BlockNoStreams, What: "nothing",
			Says: "this file contains no video and no audio that could be played",
		})
		plan.Summary = plan.Blockers[0].Says
		return plan
	}

	if !containerSupported(p.Container, c.Containers) {
		plan.Blockers = append(plan.Blockers, Blocker{
			Code: BlockContainer, What: p.Container,
			Says: fmt.Sprintf("your browser cannot open a %s container",
				friendlyContainer(p.Container)),
		})
	}

	if len(p.Video) > 0 {
		v := p.Video[0]

		if !contains(c.VideoCodecs, v.Codec) {
			plan.Blockers = append(plan.Blockers, Blocker{
				Code: BlockVideoCodec, What: v.Codec,
				Says: fmt.Sprintf("your browser cannot decode %s video",
					strings.ToUpper(v.Codec)),
			})
		}

		// Unstated means 8, not unlimited. Guessing high produces a stream that
		// starts and then shows nothing, which is worse than a refusal.
		max := c.MaxBitDepth
		if max <= 0 {
			max = 8
		}
		if v.BitDepth > max {
			plan.Blockers = append(plan.Blockers, Blocker{
				Code: BlockBitDepth, What: fmt.Sprintf("%d-bit", v.BitDepth),
				Says: fmt.Sprintf("this file is %d-bit and your browser decodes "+
					"%d-bit only", v.BitDepth, max),
			})
		}

		if v.HDR && !c.HDR {
			// A transcode maps it to SDR (ADR-0071); the offer to do that is
			// made beside the blockers.
			plan.Blockers = append(plan.Blockers, Blocker{
				Code: BlockHDR, What: hdrName(v.ColorTransfer),
				Says: fmt.Sprintf("this file is %s HDR and your browser cannot "+
					"display it as it is", hdrName(v.ColorTransfer)),
			})
		}
	}

	// The audio track the browser will reach for — not any track in the file.
	if def, ok := DefaultAudio(p.Audio); ok {
		if !contains(c.AudioCodecs, def.Codec) {
			plan.Blockers = append(plan.Blockers, Blocker{
				Code: BlockAudioCodec, What: def.Codec,
				Says: fmt.Sprintf("your browser cannot decode %s audio, which is "+
					"the track this file plays by default",
					strings.ToUpper(def.Codec)),
			})
		} else if alt := unplayableAlternates(p.Audio, def, c); alt > 0 {
			// Not a blocker: the default track works. Worth saying, because a
			// viewer who switches to the other language mid-film and gets
			// silence would otherwise have nothing to go on.
			plan.Caveats = append(plan.Caveats, fmt.Sprintf(
				"%d of the other audio tracks in this file are in formats your "+
					"browser cannot decode; switching to one will play silently", alt))
		}
	}

	plan.DirectPlay = len(plan.Blockers) == 0
	plan.Summary = summarise(plan, p)
	return plan
}

// unplayableAlternates counts audio tracks other than the default that the
// client cannot decode.
func unplayableAlternates(streams []AudioStream, def AudioStream, c Client) int {
	n := 0
	for _, a := range streams {
		if a.Index == def.Index {
			continue
		}
		if !contains(c.AudioCodecs, a.Codec) {
			n++
		}
	}
	return n
}

// containerSupported matches by membership, because ffprobe reports a container
// as EVERY format that could demux the file — "matroska,webm" is one answer,
// and a client that knows "matroska" can open it.
func containerSupported(probed string, supported []string) bool {
	if probed == "" {
		return false
	}
	for _, name := range strings.Split(probed, ",") {
		if contains(supported, strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

// friendlyContainer picks one name out of ffprobe's list for a sentence. The
// first is the one ffmpeg considers primary, and a message reading "a
// mov,mp4,m4a,3gp,3g2,mj2 container" helps nobody.
func friendlyContainer(probed string) string {
	name, _, _ := strings.Cut(probed, ",")
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	return "unknown"
}

func hdrName(transfer string) string {
	switch transfer {
	case "smpte2084":
		return "HDR10"
	case "arib-std-b67":
		return "HLG"
	}
	return "HDR"
}

func contains(haystack []string, needle string) bool {
	if needle == "" {
		return false
	}
	for _, h := range haystack {
		if strings.EqualFold(strings.TrimSpace(h), needle) {
			return true
		}
	}
	return false
}

// summarise writes the one-line answer.
//
// Deliberately not a list joined with commas. A viewer reading "your browser
// cannot decode HEVC video, this file is 10-bit and your browser decodes 8-bit
// only, this file is HDR10 HDR..." learns less than one reading that it is
// three things at once and which they are.
func summarise(plan Plan, p Probe) string {
	if plan.DirectPlay {
		if len(p.Video) > 0 {
			v := p.Video[0]
			return fmt.Sprintf("plays as it is — %s %dx%d in a %s container",
				strings.ToUpper(v.Codec), v.Width, v.Height,
				friendlyContainer(p.Container))
		}
		return fmt.Sprintf("plays as it is — audio in a %s container",
			friendlyContainer(p.Container))
	}
	if len(plan.Blockers) == 1 {
		return plan.Blockers[0].Says
	}
	what := make([]string, 0, len(plan.Blockers))
	for _, b := range plan.Blockers {
		what = append(what, b.What)
	}
	return fmt.Sprintf("cannot play as it is: %s", strings.Join(what, ", "))
}

// ChromeLike is a conservative capability set for a current desktop browser.
//
// It exists so that a server-side decision can be made BEFORE a client has
// reported anything — the library listing wants to show "will this play?" and
// there is no browser in that conversation.
//
// # Matroska IS here, and canPlayType is why that took two tries
//
// A real end-to-end run failed with MEDIA_ERR_SRC_NOT_SUPPORTED on an MKV, and
// the obvious culprit was this list claiming Matroska. Asking the browser
// seemed to confirm it:
//
//	canPlayType("video/x-matroska")                         -> ""
//	canPlayType("video/x-matroska; codecs="avc1.640028"")   -> ""
//
// That was nearly the fix. It would have been wrong. Serving the actual bytes
// settled it instead:
//
//	.mkv of VP9+Opus  served as video/x-matroska  -> PLAYS, 320x180, 3.008s
//	.mkv of VP9+Opus  served as video/webm        -> PLAYS
//	.mkv of H.264+AAC served as any type          -> MEDIA_ERR_SRC_NOT_SUPPORTED
//
// Chromium demuxes Matroska perfectly well. What it refused was H.264 — the
// test browser is the open-source Chromium build, which ships without the
// proprietary codecs that real Chrome has. The container was never the problem,
// and removing it would have refused most of a real library on the strength of
// a method that guessed.
//
// The lesson is about canPlayType rather than about MKV: it is ADVISORY and it
// is pessimistic, so it may be used to WIDEN what is attempted and never to
// narrow it. The player follows that rule — it asks the browser only when the
// server has already refused, never to overturn an acceptance.
//
// HEVC is absent because the cost of being wrong optimistically is a stream
// that starts and shows nothing, while the cost of being pessimistic is a
// warning next to a file that the player then offers to try anyway.
var ChromeLike = Client{
	Containers:  []string{"mp4", "mov", "matroska", "webm"},
	VideoCodecs: []string{"h264", "vp8", "vp9", "av1"},
	AudioCodecs: []string{"aac", "mp3", "opus", "vorbis", "flac"},
	MaxBitDepth: 8,
}

// ChromeLikeHEVC is ChromeLike for a browser that says it decodes HEVC Main10
// and shows HDR (ADR-0072): Chrome or Edge with a GPU that decodes HEVC, and
// Safari. It decides conversions only, so such a file is copied rather than
// transcoded; direct play is still decided with ChromeLike.
var ChromeLikeHEVC = Client{
	Containers:  ChromeLike.Containers,
	VideoCodecs: append([]string{"hevc"}, ChromeLike.VideoCodecs...),
	AudioCodecs: ChromeLike.AudioCodecs,
	MaxBitDepth: 10,
	HDR:         true,
}

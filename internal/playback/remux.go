package playback

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// Remuxing: keeping the video and rebuilding everything around it.
//
// # What this is for, and what it is not
//
// The common unplayable file is not one a browser cannot decode. It is one
// whose VIDEO is fine and whose container or default audio track is not — an
// H.264 film in a Matroska container with TrueHD audio, which is most of what a
// remux tracker distributes. Firefox and Safari will not open the container at
// all; every browser plays the picture and nothing else.
//
// Fixing that is a stream COPY of the video and a re-encode of one audio track.
// Measured on this hardware: 20 seconds of video remuxed in 825 ms, about 25x
// realtime, because the expensive part — the video — is copied rather than
// encoded. That is the whole reason this is worth doing on an i5-6500T when the
// full transcode ADR-0005 is cautious about is not.
//
// It is NOT a video transcoder. If the browser cannot decode the video codec,
// or the file is 10-bit, or it is HDR, a remux changes nothing: the same
// pictures come out in a different box. RemuxPlan says so rather than producing
// a stream that fails in a new way.

// ErrRemuxWouldNotHelp means the thing blocking playback is the video itself.
var ErrRemuxWouldNotHelp = errors.New("playback: a remux would not make this playable")

// ErrTooManyRemuxes means the admission limit is reached.
var ErrTooManyRemuxes = errors.New("playback: too many streams are already being converted")

// AudioTarget is a codec this server will re-encode audio to.
//
// A fixed, short allowlist, and that is the security property: a client asks for
// one of these BY NAME and the name is matched against this list before
// anything reaches ffmpeg's argument vector. There is no path by which a
// request string becomes a codec argument.
type AudioTarget string

const (
	// AudioAAC is the default and the right answer for a real browser: every
	// mainstream one decodes it.
	AudioAAC AudioTarget = "aac"
	// AudioOpus exists because "every mainstream browser" is not every browser
	// anyone actually runs. It is royalty-free, and a Chromium built without
	// the proprietary codecs — which is what a Linux distribution usually ships
	// and what this project's own browser tests run against — has Opus and no
	// AAC at all.
	AudioOpus AudioTarget = "opus"
)

// encoderFor maps a target to the ffmpeg encoder name. Unknown targets have no
// entry, so they cannot become arguments.
var encoderFor = map[AudioTarget]string{
	AudioAAC:  "aac",
	AudioOpus: "libopus",
}

// ParseAudioTarget accepts a client's request for an output codec, or refuses.
func ParseAudioTarget(s string) (AudioTarget, bool) {
	t := AudioTarget(strings.ToLower(strings.TrimSpace(s)))
	if _, ok := encoderFor[t]; ok {
		return t, true
	}
	return "", false
}

// videoCodecsMP4 is what may be placed in an MP4 container by stream copy.
//
// VP8 is deliberately absent: it is a WebM codec and muxing it into MP4
// produces a file nothing will play. A remux that "succeeds" into an unplayable
// container is worse than a refusal, because the failure surfaces in the
// browser rather than here.
var videoCodecsMP4 = map[string]bool{
	"h264": true, "hevc": true, "av1": true, "vp9": true, "mpeg4": true,
}

// RemuxPlan is what a remux of this file would involve, or why it is pointless.
type RemuxPlan struct {
	// Possible is whether remuxing would make the file playable.
	Possible bool
	// Why explains a refusal, in the same register as a playability Blocker:
	// naming what is wrong rather than reporting that something is.
	Why string
	// AudioIndex is the track to carry over, chosen the way a browser would
	// choose it — see DefaultAudio.
	AudioIndex int
	// ReencodeAudio is false when the existing audio can simply be copied,
	// which is the case when only the CONTAINER was the problem.
	ReencodeAudio bool
	// VideoIndex is the track to copy.
	VideoIndex int
}

// PlanRemux decides whether a remux helps, given what the client can decode.
func PlanRemux(p Probe, c Client, target AudioTarget) RemuxPlan {
	if len(p.Video) == 0 {
		return RemuxPlan{Why: "this file has no video track to carry over"}
	}
	v := p.Video[0]

	// The refusals a remux cannot fix. Each is about the PICTURES, and a remux
	// moves pictures without changing them.
	switch {
	case !contains(c.VideoCodecs, v.Codec):
		return RemuxPlan{Why: fmt.Sprintf(
			"the video is %s, which your browser cannot decode. Converting the "+
				"container would produce the same pictures in a different box; "+
				"this needs a video transcode, which this server does not do",
			strings.ToUpper(v.Codec))}
	case !videoCodecsMP4[v.Codec]:
		return RemuxPlan{Why: fmt.Sprintf(
			"%s cannot be carried into an MP4 container without re-encoding it",
			strings.ToUpper(v.Codec))}
	case v.HDR && !c.HDR:
		return RemuxPlan{Why: "this file is HDR and a remux does not change the " +
			"pictures, so it would still be unwatchable"}
	}
	max := c.MaxBitDepth
	if max <= 0 {
		max = 8
	}
	if v.BitDepth > max {
		return RemuxPlan{Why: fmt.Sprintf(
			"this file is %d-bit and a remux does not change the pictures",
			v.BitDepth)}
	}

	plan := RemuxPlan{Possible: true, VideoIndex: v.Index, AudioIndex: -1}

	if def, ok := DefaultAudio(p.Audio); ok {
		plan.AudioIndex = def.Index
		// Copied when the browser can already decode it — which is the case
		// where only the container was in the way. Re-encoding perfectly good
		// audio costs CPU for nothing and loses quality on the way.
		plan.ReencodeAudio = !contains(c.AudioCodecs, def.Codec)
		// An audio codec MP4 cannot carry has to be re-encoded even if the
		// browser could have decoded it in its original container.
		if !plan.ReencodeAudio && !audioCodecsMP4[def.Codec] {
			plan.ReencodeAudio = true
		}
	}
	return plan
}

// audioCodecsMP4 is what may be copied into an MP4 container.
var audioCodecsMP4 = map[string]bool{
	"aac": true, "mp3": true, "ac3": true, "eac3": true, "opus": true,
	"alac": true, "dts": true,
}

// Remuxer streams converted media.
type Remuxer struct {
	streamer *Streamer
	sandbox  *Sandbox
	log      *slog.Logger

	// Admission control. ADR-0005's whole premise is that this hardware has no
	// headroom, and an unbounded number of ffmpeg processes on four cores is
	// the failure that takes the working sessions down with the new one.
	limit  int
	mu     sync.Mutex
	active int
}

// NewRemuxer builds one. limit <= 0 falls back to a small, safe number rather
// than to unlimited: "unset" must never mean "as many as arrive".
func NewRemuxer(s *Streamer, sandbox *Sandbox, log *slog.Logger, limit int) *Remuxer {
	if log == nil {
		log = slog.Default()
	}
	if limit <= 0 {
		limit = 2
	}
	return &Remuxer{streamer: s, sandbox: sandbox, log: log, limit: limit}
}

// Active reports how many conversions are running.
func (r *Remuxer) Active() (int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active, r.limit
}

func (r *Remuxer) admit() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active >= r.limit {
		return false
	}
	r.active++
	return true
}

func (r *Remuxer) release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active > 0 {
		r.active--
	}
}

// Stream converts a file and writes it to w as it goes.
//
// # Why this is not seekable, and why that is said out loud
//
// The output is a pipe: ffmpeg is producing it as the viewer watches. There is
// no file to serve a byte range from, so a <video> element cannot seek within
// it — dragging the scrubber produces nothing. Pretending otherwise by
// accepting a Range header and ignoring it would give a player that looks like
// it seeks and does not.
//
// The honest alternative — restarting ffmpeg at an offset for each seek — is a
// real design and is not this increment. Until then the response says
// Accept-Ranges: none, so a browser knows before it tries.
func (r *Remuxer) Stream(w http.ResponseWriter, req *http.Request, fileID int64,
	plan RemuxPlan, target AudioTarget) error {

	if err := authz.RequirePermission(req.Context(), authz.PermBrowse); err != nil {
		return err
	}
	if !plan.Possible {
		return fmt.Errorf("%w: %s", ErrRemuxWouldNotHelp, plan.Why)
	}
	encoder, ok := encoderFor[target]
	if !ok {
		// Unreachable through the HTTP surface, which parses the target before
		// it gets here. Checked anyway: this is the line standing between a
		// request string and an ffmpeg argument.
		return fmt.Errorf("playback: %q is not an audio target this server will produce", target)
	}

	if !r.admit() {
		return ErrTooManyRemuxes
	}
	defer r.release()

	f, ref, err := r.streamer.Open(req.Context(), fileID)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	args := []string{
		"-loglevel", "error",
		// ffmpeg reads stdin by default and would consume the parent's.
		"-nostdin",
		// fd 3, handed over by the parent. Never a path (ADR-0020).
		"-i", "/dev/fd/3",
		"-map", fmt.Sprintf("0:%d", plan.VideoIndex),
	}
	if plan.AudioIndex >= 0 {
		args = append(args, "-map", fmt.Sprintf("0:%d", plan.AudioIndex))
	}
	// The video is COPIED. This is the whole economy of the thing.
	args = append(args, "-c:v", "copy")
	if plan.AudioIndex >= 0 {
		if plan.ReencodeAudio {
			args = append(args, "-c:a", encoder, "-b:a", "192k",
				// Downmixed to stereo. A browser plays stereo; a 7.1 track
				// re-encoded to 7.1 AAC and then downmixed by the browser
				// sounds worse and costs more than downmixing once, here.
				"-ac", "2")
		} else {
			args = append(args, "-c:a", "copy")
		}
	}
	args = append(args,
		// The flags that make an MP4 writable to a pipe: no final seek back to
		// patch the header, and a moov that is valid before the file ends.
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4",
		"pipe:1",
	)

	w.Header().Set("Content-Type", "video/mp4")
	// Said explicitly. ServeContent is not involved here, and a browser that
	// assumed ranges were available would seek into nothing.
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("Cache-Control", "private, max-age=0, no-store")
	w.Header().Set("Content-Disposition", "inline")

	start := time.Now()
	r.log.Info("converting a stream",
		slog.String("file", ref.RelativePath),
		slog.Bool("audio_reencoded", plan.ReencodeAudio),
		slog.String("audio_target", string(target)))

	// The request's context cancels the child, so a viewer who closes the tab
	// stops the conversion rather than leaving an ffmpeg finishing a film
	// nobody is watching.
	n, err := r.sandbox.Pipe(req.Context(), w, f, FFmpegPath, args...)

	r.log.Info("conversion finished",
		slog.String("file", ref.RelativePath),
		slog.Int64("bytes", n),
		slog.Duration("took", time.Since(start)),
		slog.Bool("failed", err != nil))
	return err
}

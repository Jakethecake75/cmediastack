package playback

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// What a media file actually contains, as opposed to what its name claims.
//
// The release name is a claim by a stranger; this is the file. Every decision
// in ADR-0005 — direct play, transcode, or a refusal naming what was missing —
// is made from what is here, so the fields exist because a decision needs them
// and not because ffprobe reports them.

// ProbeTimeout is how long a probe may take before it is killed.
//
// Generous, because a large file on a cold NFS mount is slow for reasons that
// are not the file's fault, and stingy enough that a file crafted to make the
// parser spin does not occupy a worker indefinitely.
const ProbeTimeout = 90 * time.Second

// FFprobePath and FFmpegPath are resolved from PATH by exec. Named constants so
// there is one place to change if they are ever configured.
const (
	FFprobePath = "ffprobe"
	FFmpegPath  = "ffmpeg"
)

// Probe is everything known about one media file.
type Probe struct {
	// Container is ffprobe's format_name, which is a comma-separated list of
	// every format that could demux this file. Kept whole rather than reduced
	// to one name: "mov,mp4,m4a,3gp,3g2,mj2" is one answer, not six, and
	// picking a member of it would be inventing precision.
	Container string
	Duration  time.Duration
	Bitrate   int64
	SizeBytes int64

	Video     []VideoStream
	Audio     []AudioStream
	Subtitles []SubtitleStream

	// ProbedAt, and the identity of the file when it was probed. A probe is
	// cached against size and modification time because a file CAN change
	// underneath the library — a re-download, an operator's own re-encode —
	// and a stale probe produces a playback failure that looks like a bug in
	// the player.
	ProbedAt time.Time
	ModTime  time.Time

	// Sandboxed records whether the jail was used. A probe taken without it is
	// still a probe; it was simply taken under a weaker guarantee, and saying
	// so beats assuming the stronger one later.
	Sandboxed bool
}

// VideoStream is one video track.
type VideoStream struct {
	Index   int
	Codec   string // h264, hevc, av1, vp9, mpeg4...
	Profile string // "High", "Main 10", "Simple Profile"
	Level   int
	Width   int
	Height  int
	// BitDepth is derived from pix_fmt rather than read directly, because
	// ffprobe reports bits_per_raw_sample inconsistently across codecs while
	// pix_fmt is always present and unambiguous.
	BitDepth       int
	PixelFormat    string
	FrameRate      float64
	ColorTransfer  string
	ColorPrimaries string
	// HDR is the decision ADR-0005 turns on, not a description. It is true for
	// the two transfer functions that actually mean HDR in practice.
	HDR bool
}

// AudioStream is one audio track.
type AudioStream struct {
	Index         int
	Codec         string
	Channels      int
	ChannelLayout string
	SampleRate    int
	Language      string
	Title         string
	Default       bool
}

// SubtitleStream is one subtitle track carried inside the container.
type SubtitleStream struct {
	Index    int
	Codec    string
	Language string
	Title    string
	Forced   bool
	Default  bool
	// Text is whether this track is text rather than pictures. The distinction
	// is the whole reason this field exists: a text track can be converted to
	// WebVTT and handed to the browser, while a bitmap track (PGS, VobSub) can
	// only be shown by drawing it onto the video — which is a transcode, and on
	// this hardware that may mean it cannot be shown at all.
	Text bool
}

// Prober turns a file into a Probe.
type Prober struct {
	sandbox *Sandbox
}

// NewProber builds one.
func NewProber(s *Sandbox) *Prober { return &Prober{sandbox: s} }

// Probe reads what is in a file.
//
// It takes an OPEN FILE, not a path, and that is the design rather than a
// convenience: the caller opens through its os.Root, so the containment that
// stops a library path escaping its root extends all the way into ffprobe. The
// child is handed a descriptor and is never told where anything is (ADR-0020).
func (p *Prober) Probe(ctx context.Context, f *os.File) (Probe, error) {
	if p == nil || p.sandbox == nil {
		return Probe{}, ErrSandboxUnavailable
	}

	info, err := f.Stat()
	if err != nil {
		return Probe{}, fmt.Errorf("playback: stat before probing: %w", err)
	}

	// Rewind. A caller may have read a magic-byte header first, and ffprobe
	// starts from wherever the descriptor is.
	if _, err := f.Seek(0, 0); err != nil {
		return Probe{}, fmt.Errorf("playback: rewind before probing: %w", err)
	}

	res, err := p.sandbox.Run(ctx, ProbeTimeout, f, FFprobePath,
		"-v", "error",
		"-show_format",
		"-show_streams",
		"-of", "json",
		// NOT a path. fd 3 is the file, handed over by the parent.
		"/dev/fd/3",
	)
	if err != nil {
		return Probe{}, err
	}

	probe, err := parseProbe(res.Stdout)
	if err != nil {
		return Probe{}, err
	}
	probe.SizeBytes = info.Size()
	probe.ModTime = info.ModTime().UTC()
	probe.Sandboxed = res.Sandboxed
	return probe, nil
}

// --- ffprobe's JSON, transcribed from a live run rather than from the docs ---

type ffFormat struct {
	FormatName string `json:"format_name"`
	Duration   string `json:"duration"`
	BitRate    string `json:"bit_rate"`
}

type ffDisposition struct {
	Default int `json:"default"`
	Forced  int `json:"forced"`
}

type ffStream struct {
	Index          int           `json:"index"`
	CodecName      string        `json:"codec_name"`
	CodecType      string        `json:"codec_type"`
	Profile        string        `json:"profile"`
	Level          int           `json:"level"`
	Width          int           `json:"width"`
	Height         int           `json:"height"`
	PixFmt         string        `json:"pix_fmt"`
	ColorTransfer  string        `json:"color_transfer"`
	ColorPrimaries string        `json:"color_primaries"`
	AvgFrameRate   string        `json:"avg_frame_rate"`
	RFrameRate     string        `json:"r_frame_rate"`
	Channels       int           `json:"channels"`
	ChannelLayout  string        `json:"channel_layout"`
	SampleRate     string        `json:"sample_rate"`
	Disposition    ffDisposition `json:"disposition"`
	Tags           struct {
		Language string `json:"language"`
		Title    string `json:"title"`
	} `json:"tags"`
}

type ffOutput struct {
	Format  ffFormat   `json:"format"`
	Streams []ffStream `json:"streams"`
}

func parseProbe(raw []byte) (Probe, error) {
	var out ffOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return Probe{}, fmt.Errorf("playback: ffprobe produced something that is "+
			"not the JSON it was asked for: %w", err)
	}
	if out.Format.FormatName == "" && len(out.Streams) == 0 {
		return Probe{}, fmt.Errorf("playback: ffprobe found no format and no " +
			"streams — this is not a media file this server can read")
	}

	p := Probe{
		Container: out.Format.FormatName,
		Duration:  parseSeconds(out.Format.Duration),
		Bitrate:   parseInt64(out.Format.BitRate),
		ProbedAt:  time.Now().UTC(),
	}

	for _, s := range out.Streams {
		switch s.CodecType {
		case "video":
			// An embedded cover image is a video stream as far as a container
			// is concerned. Excluding it here stops an audio file, or a film
			// with poster art muxed in, from looking like it has two video
			// tracks — and stops the first one picked being a JPEG.
			if isCoverArt(s) {
				continue
			}
			p.Video = append(p.Video, VideoStream{
				Index: s.Index, Codec: s.CodecName, Profile: s.Profile,
				Level: s.Level, Width: s.Width, Height: s.Height,
				BitDepth:       bitDepth(s.PixFmt),
				PixelFormat:    s.PixFmt,
				FrameRate:      parseRational(s.AvgFrameRate, s.RFrameRate),
				ColorTransfer:  s.ColorTransfer,
				ColorPrimaries: s.ColorPrimaries,
				HDR:            isHDR(s.ColorTransfer),
			})
		case "audio":
			p.Audio = append(p.Audio, AudioStream{
				Index: s.Index, Codec: s.CodecName, Channels: s.Channels,
				ChannelLayout: s.ChannelLayout,
				SampleRate:    int(parseInt64(s.SampleRate)),
				Language:      normaliseLanguage(s.Tags.Language),
				Title:         s.Tags.Title,
				Default:       s.Disposition.Default == 1,
			})
		case "subtitle":
			p.Subtitles = append(p.Subtitles, SubtitleStream{
				Index: s.Index, Codec: s.CodecName,
				Language: normaliseLanguage(s.Tags.Language),
				Title:    s.Tags.Title,
				Forced:   s.Disposition.Forced == 1,
				Default:  s.Disposition.Default == 1,
				Text:     isTextSubtitle(s.CodecName),
			})
		}
	}
	return p, nil
}

// isCoverArt spots an attached picture masquerading as a video stream.
//
// ffprobe marks it with disposition.attached_pic, but also produces it for
// formats that do not set that flag, so the codec is checked too: a "video"
// stream whose codec is a still-image format is a cover.
func isCoverArt(s ffStream) bool {
	switch s.CodecName {
	case "mjpeg", "png", "bmp", "gif", "webp":
		return true
	}
	return false
}

// bitDepth reads the depth out of a pixel format name.
//
// pix_fmt rather than bits_per_raw_sample, which ffprobe reports
// inconsistently across codecs. The names are regular: yuv420p is 8-bit,
// yuv420p10le is 10-bit, yuv420p12le is 12-bit. Anything unrecognised is 8,
// because 8 is what "no marker" has always meant.
func bitDepth(pixFmt string) int {
	if pixFmt == "" {
		return 0
	}
	for _, d := range []int{16, 14, 12, 10, 9} {
		if strings.Contains(pixFmt, "p"+strconv.Itoa(d)) {
			return d
		}
	}
	return 8
}

// isHDR decides from the transfer function.
//
// Two values mean HDR in practice: smpte2084 is PQ (HDR10, HDR10+, Dolby
// Vision's base layer) and arib-std-b67 is HLG. Colour primaries are NOT used:
// bt2020 primaries appear on plenty of SDR content and would produce false
// positives, which on this hardware means refusing to play something that would
// have played (ADR-0005).
func isHDR(transfer string) bool {
	switch transfer {
	case "smpte2084", "arib-std-b67":
		return true
	}
	return false
}

// isTextSubtitle separates tracks a browser can be given from tracks that have
// to be drawn onto the picture.
func isTextSubtitle(codec string) bool {
	switch codec {
	case "subrip", "srt", "ass", "ssa", "webvtt", "mov_text", "text", "stl", "subviewer":
		return true
	}
	// dvd_subtitle, hdmv_pgs_subtitle, dvb_subtitle, xsub: pictures.
	return false
}

// normaliseLanguage turns ffprobe's absent-language markers into an empty
// string, so a caller can test one thing instead of three.
func normaliseLanguage(l string) string {
	switch strings.ToLower(strings.TrimSpace(l)) {
	case "", "und", "unknown", "none":
		return ""
	}
	return strings.ToLower(strings.TrimSpace(l))
}

func parseSeconds(s string) time.Duration {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f <= 0 {
		return 0
	}
	return time.Duration(f * float64(time.Second))
}

func parseInt64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// parseRational turns "24000/1001" into 23.976.
//
// avg_frame_rate is preferred and r_frame_rate is the fallback, because
// avg_frame_rate is 0/0 for a stream with no frames while r_frame_rate is a
// guess that is usually right.
func parseRational(primary, fallback string) float64 {
	for _, s := range []string{primary, fallback} {
		num, den, ok := strings.Cut(strings.TrimSpace(s), "/")
		if !ok {
			continue
		}
		n, err1 := strconv.ParseFloat(num, 64)
		d, err2 := strconv.ParseFloat(den, 64)
		if err1 != nil || err2 != nil || d == 0 || n == 0 {
			continue
		}
		return n / d
	}
	return 0
}

package playback

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func hdrFilm() Probe {
	return Probe{
		Container: "matroska,webm", Duration: 2 * time.Hour,
		Video: []VideoStream{{Index: 0, Codec: "hevc", Width: 3840, Height: 2160, BitDepth: 10,
			ColorTransfer: "smpte2084", HDR: true}},
		Audio: []AudioStream{{Index: 1, Codec: "truehd", Channels: 8, Default: true}},
	}
}

// When the pictures are the problem, converting transcodes them (ADR-0071):
// H.264 8-bit, tone-mapped when HDR, the default audio rebuilt. When only the
// container or the sound is, it is still a remux.
func TestConvertingTranscodesOnlyWhenTheVideoIsTheProblem(t *testing.T) {
	p := PlanConvert(hdrFilm(), ChromeLike, AudioAAC)
	if !p.Possible || !p.TranscodeVideo || !p.ToneMap || !p.ReencodeAudio || p.AudioIndex != 1 {
		t.Errorf("HDR HEVC: %+v; want a tone-mapped transcode with the audio rebuilt", p)
	}
	if p.SourceWidth != 3840 || p.SourceHeight != 2160 || p.Transfer != "smpte2084" {
		t.Errorf("the source is not carried into the plan: %+v", p)
	}

	tenBit := hdrFilm()
	tenBit.Video[0].HDR, tenBit.Video[0].ColorTransfer = false, ""
	if p := PlanConvert(tenBit, ChromeLike, AudioAAC); !p.TranscodeVideo || p.ToneMap {
		t.Errorf("10-bit SDR HEVC: %+v; want a transcode without tone mapping", p)
	}

	remux := Probe{Container: "matroska,webm",
		Video: []VideoStream{{Index: 0, Codec: "h264", Width: 1920, Height: 1080, BitDepth: 8}},
		Audio: []AudioStream{{Index: 1, Codec: "ac3", Channels: 6, Default: true}}}
	if p := PlanConvert(remux, ChromeLike, AudioAAC); !p.Possible || p.TranscodeVideo {
		t.Errorf("H.264 with AC3: %+v; want a remux, which costs a twenty-fifth as much", p)
	}

	if p := PlanConvert(Probe{Audio: remux.Audio}, ChromeLike, AudioAAC); p.Possible {
		t.Errorf("no video track: %+v; there is nothing to play", p)
	}
}

func argAfter(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// The arguments: a start before the input, H.264 out, scaled to the asked
// height with an even width and the aspect kept, tone-mapped when planned.
func TestTranscodeArgumentsScaleToneMapAndSeek(t *testing.T) {
	p := PlanConvert(hdrFilm(), ChromeLike, AudioAAC)
	args := convertArgs(p, "aac", StreamOptions{Start: 90 * time.Second, Height: 720})
	if i, j := slices.Index(args, "-ss"), slices.Index(args, "-i"); i < 0 || j < i || args[i+1] != "90" {
		t.Errorf("the start is not a seek before the input: %v", args)
	}
	if argAfter(args, "-c:v") != "libx264" || argAfter(args, "-pix_fmt") != "yuv420p" {
		t.Errorf("not 8-bit H.264: %v", args)
	}
	vf := argAfter(args, "-vf")
	if !strings.HasPrefix(vf, "zscale=w=1280:h=720:tin=smpte2084") || !strings.Contains(vf, "tonemap=tonemap=hable") {
		t.Errorf("-vf = %q; want 720p and tone mapping from PQ", vf)
	}

	sdr := p
	sdr.ToneMap, sdr.SourceWidth, sdr.SourceHeight = false, 1919, 1080
	if vf := argAfter(convertArgs(sdr, "aac", StreamOptions{Height: 720}), "-vf"); vf != "scale=1278:720,format=yuv420p" {
		t.Errorf("-vf = %q; want an even width and the aspect kept", vf)
	}
	if vf := argAfter(convertArgs(sdr, "aac", StreamOptions{}), "-vf"); vf != "format=yuv420p" {
		t.Errorf("-vf = %q; a 1080p source at the default 1080p is not scaled", vf)
	}
	hlg := p
	hlg.Transfer = "arib-std-b67"
	if vf := argAfter(convertArgs(hlg, "aac", StreamOptions{}), "-vf"); !strings.Contains(vf, "tin=arib-std-b67") {
		t.Errorf("-vf = %q; HLG is tone-mapped from HLG", vf)
	}

	if past := convertArgs(p, "aac", StreamOptions{Start: 3 * time.Hour}); slices.Contains(past, "-ss") {
		t.Errorf("a start past the end of a two-hour film was sent: %v", past)
	}

	remux := RemuxPlan{Possible: true, VideoIndex: 0, AudioIndex: 1, ReencodeAudio: true}
	args = convertArgs(remux, "aac", StreamOptions{})
	if argAfter(args, "-c:v") != "copy" || slices.Contains(args, "-vf") || slices.Contains(args, "-ss") {
		t.Errorf("a remux from the start is a copy with no filter and no seek: %v", args)
	}
}

// Only 720 and 1080 are heights a client may ask for.
func TestOnlyAllowlistedHeightsAreAccepted(t *testing.T) {
	for _, s := range []string{"720", "1080"} {
		if _, ok := ParseHeight(s); !ok {
			t.Errorf("%s refused", s)
		}
	}
	for _, s := range []string{"", "480", "2160", "720;rm", "-1"} {
		if _, ok := ParseHeight(s); ok {
			t.Errorf("%q accepted", s)
		}
	}
}

// Through the real ffmpeg: a 10-bit HDR HEVC file comes out as 8-bit BT.709
// H.264 a browser plays, and a start trims what came before it.
func TestATranscodeTurnsTenBitHDRIntoPlayableVideo(t *testing.T) {
	haveFFmpeg(t)
	s := quietSandbox(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "hdr.mkv")
	out, err := exec.CommandContext(t.Context(), FFmpegPath, "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=duration=3:size=640x360:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le",
		"-x265-params", "colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:log-level=error",
		"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc",
		"-c:a", "ac3", "-ac", "2", "-f", "matroska", src).CombinedOutput()
	if err != nil {
		t.Skipf("could not build the fixture (%v): %s", err, firstLine(out))
	}
	files := &fakeFiles{byID: map[int64]FileRef{1: {ID: 1, RootFolderID: 7, RelativePath: "hdr.mkv"}}}
	rx := NewRemuxer(NewStreamer(files, &fakeVaults{dirs: map[int64]string{7: dir}}), s,
		slog.New(slog.NewTextHandler(io.Discard, nil)), 2)
	plan := RemuxPlan{Possible: true, VideoIndex: 0, AudioIndex: 1, ReencodeAudio: true,
		TranscodeVideo: true, ToneMap: true, SourceWidth: 640, SourceHeight: 360, Transfer: "smpte2084",
		Duration: 3 * time.Second}

	stream := func(start time.Duration) string {
		req := httptest.NewRequestWithContext(adminContext(), http.MethodGet, "/x", nil)
		w := httptest.NewRecorder()
		if err := rx.Stream(w, req, 1, plan, AudioAAC, StreamOptions{Start: start}); err != nil {
			t.Fatalf("transcoding: %v", err)
		}
		f := filepath.Join(dir, "out"+strconv.Itoa(int(start.Seconds()))+".mp4")
		if err := os.WriteFile(f, w.Body.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		return f
	}
	probe := func(f, entries string) string {
		info, err := exec.CommandContext(t.Context(), FFprobePath, "-v", "error",
			"-show_entries", entries, "-of", "csv=p=0", f).CombinedOutput()
		if err != nil {
			t.Fatalf("probing %s: %v %s", f, err, info)
		}
		return strings.TrimSpace(string(info))
	}

	whole := stream(0)
	if got := probe(whole, "stream=codec_name,pix_fmt,color_transfer"); !strings.Contains(got, "h264,yuv420p,bt709") ||
		!strings.Contains(got, "aac") {
		t.Errorf("the transcode produced %q; want 8-bit BT.709 H.264 and AAC", got)
	}
	d0, _ := strconv.ParseFloat(probe(whole, "format=duration"), 64)
	d2, _ := strconv.ParseFloat(probe(stream(2*time.Second), "format=duration"), 64)
	if d0 < 2.5 || d2 > 1.5 {
		t.Errorf("durations %.2f from the start and %.2f from 2 s; want about 3 and 1", d0, d2)
	}
}

// A browser that decodes HEVC gets the 4K HDR film's own picture, copied and
// tagged hvc1, with only the sound rebuilt (ADR-0072); one that does not gets
// the transcode.
func TestABrowserThatDecodesHEVCGetsTheOriginalPicture(t *testing.T) {
	p := PlanConvert(hdrFilm(), ChromeLikeHEVC, AudioAAC)
	if !p.Possible || p.TranscodeVideo || !p.ReencodeAudio || p.AudioIndex != 1 {
		t.Fatalf("HDR HEVC for an HEVC browser: %+v; want a copy with the audio rebuilt", p)
	}
	args := convertArgs(p, "aac", StreamOptions{HEVC: true})
	if argAfter(args, "-c:v") != "copy" || argAfter(args, "-tag:v") != "hvc1" || slices.Contains(args, "-vf") {
		t.Errorf("not a tagged copy: %v", args)
	}
	// One fragment per keyframe would be ten seconds of UHD at a time.
	if argAfter(args, "-frag_duration") != "1000000" {
		t.Errorf("fragments are not capped at a second: %v", args)
	}
	if h264 := convertArgs(RemuxPlan{Possible: true, VideoCodec: "h264", AudioIndex: -1}, "aac", StreamOptions{}); slices.Contains(h264, "-tag:v") {
		t.Errorf("H.264 was given an HEVC tag: %v", h264)
	}
	if p := PlanConvert(hdrFilm(), ChromeLike, AudioAAC); !p.TranscodeVideo {
		t.Errorf("for any other browser it is still a transcode: %+v", p)
	}
	if !slices.Equal(ChromeLike.VideoCodecs, []string{"h264", "vp8", "vp9", "av1"}) {
		t.Errorf("ChromeLike was changed by ChromeLikeHEVC: %v", ChromeLike.VideoCodecs)
	}
}

type countingReader struct{ n, size atomic.Int64 }

func (r *countingReader) Read(p []byte) (int, error) {
	left := r.size.Load() - r.n.Load()
	if left <= 0 {
		return 0, io.EOF
	}
	k := min(int64(len(p)), left)
	r.n.Add(k)
	return int(k), nil
}

type gatedWriter struct{ gate chan struct{} }

func (w gatedWriter) Write(p []byte) (int, error) { <-w.gate; return len(p), nil }

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("the viewer went away") }

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
	}
}

// A converted stream is produced ahead of a browser that is not reading yet
// (ADR-0072), and a reader that goes away leaves nothing stuck behind it.
func TestAConvertedStreamIsProducedAheadOfTheReader(t *testing.T) {
	src := &countingReader{}
	src.size.Store(4 << 20)
	gate := make(chan struct{})
	done := make(chan int64)
	go func() { n, _ := copyAhead(gatedWriter{gate}, src, 8<<20); done <- n }()
	waitFor(t, "the stream was not read while the writer waited", func() bool { return src.n.Load() == 4<<20 })
	close(gate)
	if n := <-done; n != 4<<20 {
		t.Errorf("copied %d bytes, want %d", n, 4<<20)
	}

	src = &countingReader{}
	src.size.Store(4 << 20)
	if _, err := copyAhead(failingWriter{}, src, 1<<20); err == nil {
		t.Error("a failed write was not reported")
	}
	waitFor(t, "the rest was not drained after the writer failed", func() bool { return src.n.Load() == 4<<20 })
}

// A copy that starts part-way starts its sound at the picture's keyframe, not
// at the asked-for second, so the two stay together (ADR-0074); a transcode
// cuts both at the second.
func TestACopyStartsItsSoundWithItsPicture(t *testing.T) {
	copyPlan := PlanConvert(hdrFilm(), ChromeLikeHEVC, AudioAAC)
	args := convertArgs(copyPlan, "aac", StreamOptions{Start: 374 * time.Second, HEVC: true})
	if i, j := slices.Index(args, "-noaccurate_seek"), slices.Index(args, "-i"); i < 0 || j < i {
		t.Errorf("a copy from 6:14 does not start its sound at the keyframe: %v", args)
	}
	if slices.Contains(convertArgs(copyPlan, "aac", StreamOptions{HEVC: true}), "-noaccurate_seek") {
		t.Error("a copy from the start was given a seek flag")
	}
	transcode := PlanConvert(hdrFilm(), ChromeLike, AudioAAC)
	if slices.Contains(convertArgs(transcode, "aac", StreamOptions{Start: 374 * time.Second}), "-noaccurate_seek") {
		t.Error("a transcode, which cuts exactly, was told not to")
	}
}

// Where a copy really starts is read from ffprobe's first packet after the
// seek: a keyframe, and never later than the asked-for second.
func TestTheKeyframeACopyStartsAtIsRead(t *testing.T) {
	if k, ok := parseKeyframe("10.417000,K_\n", 15*time.Second); !ok || k != 10417*time.Millisecond {
		t.Errorf("got %v, %v; want 10.417s", k, ok)
	}
	for _, out := range []string{"", "10.417000,__", "abc,K_", "-1,K_", "16.000000,K_"} {
		if _, ok := parseKeyframe(out, 15*time.Second); ok {
			t.Errorf("%q was accepted", out)
		}
	}
}

package playback

import (
	"strings"
	"testing"
)

func movie(container string, v VideoStream, audio ...AudioStream) Probe {
	return Probe{Container: container, Video: []VideoStream{v}, Audio: audio}
}

func h264() VideoStream {
	return VideoStream{Index: 0, Codec: "h264", Profile: "High",
		Width: 1920, Height: 1080, BitDepth: 8, PixelFormat: "yuv420p"}
}

func hevc10() VideoStream {
	return VideoStream{Index: 0, Codec: "hevc", Profile: "Main 10",
		Width: 3840, Height: 2160, BitDepth: 10, PixelFormat: "yuv420p10le",
		ColorTransfer: "smpte2084", ColorPrimaries: "bt2020", HDR: true}
}

func aac() AudioStream {
	return AudioStream{Index: 1, Codec: "aac", Channels: 2, Default: true}
}

// The common case, and the one the whole policy exists to maximise.
func TestAnOrdinaryFilePlaysAsItIs(t *testing.T) {
	plan := Decide(movie("mov,mp4,m4a,3gp,3g2,mj2", h264(), aac()), ChromeLike)

	if !plan.DirectPlay {
		t.Fatalf("an H.264/AAC MP4 was refused: %+v", plan.Blockers)
	}
	if len(plan.Blockers) != 0 {
		t.Errorf("blockers on a playable file: %+v", plan.Blockers)
	}
	if !strings.Contains(plan.Summary, "H264") {
		t.Errorf("summary = %q", plan.Summary)
	}
}

// ffprobe reports a container as EVERY format that could demux the file. A
// client that knows "matroska" can open "matroska,webm", and matching the whole
// string would refuse every MKV in the library.
func TestAContainerMatchesAnyNameItReports(t *testing.T) {
	plan := Decide(movie("matroska,webm", h264(), aac()), ChromeLike)
	if !plan.DirectPlay {
		t.Fatalf("an MKV was refused: %+v — ffprobe's container is a list, and "+
			"membership is the match", plan.Blockers)
	}
}

// The reason the refusal exists at all: it has to say what is missing.
func TestARefusalNamesEveryCapabilityThatIsMissing(t *testing.T) {
	// HEVC, 10-bit, HDR, and TrueHD: four separate reasons at once.
	p := movie("matroska,webm", hevc10(),
		AudioStream{Index: 1, Codec: "truehd", Channels: 8, Default: true})

	plan := Decide(p, ChromeLike)
	if plan.DirectPlay {
		t.Fatal("a 4K HDR HEVC remux with TrueHD audio was accepted for direct play")
	}

	got := map[string]Blocker{}
	for _, b := range plan.Blockers {
		got[b.Code] = b
	}
	for _, code := range []string{BlockVideoCodec, BlockBitDepth, BlockHDR, BlockAudioCodec} {
		if _, ok := got[code]; !ok {
			t.Errorf("no %s blocker; a viewer told about these one at a time is "+
				"being walked through a maze that was fully mapped on the first "+
				"attempt. got %+v", code, plan.Blockers)
		}
	}

	// Each sentence has to name the capability, not merely report failure.
	for _, b := range plan.Blockers {
		if b.Says == "" || b.What == "" {
			t.Errorf("blocker %+v says nothing actionable", b)
		}
		if strings.Contains(strings.ToLower(b.Says), "unsupported") ||
			strings.Contains(strings.ToLower(b.Says), "failed") {
			t.Errorf("blocker %q is the sentence nobody can act on", b.Says)
		}
	}
	if !strings.Contains(got[BlockVideoCodec].Says, "HEVC") {
		t.Errorf("the video blocker does not name the codec: %q", got[BlockVideoCodec].Says)
	}
	if !strings.Contains(got[BlockAudioCodec].Says, "TRUEHD") {
		t.Errorf("the audio blocker does not name the codec: %q", got[BlockAudioCodec].Says)
	}
}

// HDR is a blocker for direct play, named by its format. A transcode now maps
// it to SDR (ADR-0071), so the sentence no longer says the server cannot help.
func TestTheHDRBlockerNamesTheFormat(t *testing.T) {
	plan := Decide(movie("matroska,webm",
		VideoStream{Index: 0, Codec: "h264", Width: 3840, Height: 2160,
			BitDepth: 8, ColorTransfer: "smpte2084", HDR: true},
		aac()), ChromeLike)

	var hdr *Blocker
	for i := range plan.Blockers {
		if plan.Blockers[i].Code == BlockHDR {
			hdr = &plan.Blockers[i]
		}
	}
	if hdr == nil {
		t.Fatalf("no HDR blocker: %+v", plan.Blockers)
	}
	if strings.Contains(hdr.Says, "cannot convert") {
		t.Errorf("the HDR message still says the server cannot help: %q", hdr.Says)
	}
	if !strings.Contains(hdr.Says, "HDR10") {
		t.Errorf("the HDR message does not name the format: %q", hdr.Says)
	}
}

// HLG is HDR too, and is named differently because it is a different thing.
func TestHLGIsNamedAsHLG(t *testing.T) {
	plan := Decide(movie("matroska,webm",
		VideoStream{Index: 0, Codec: "h264", BitDepth: 8,
			ColorTransfer: "arib-std-b67", HDR: true},
		aac()), ChromeLike)
	for _, b := range plan.Blockers {
		if b.Code == BlockHDR {
			if !strings.Contains(b.Says, "HLG") {
				t.Errorf("HLG content was described as %q", b.Says)
			}
			return
		}
	}
	t.Fatalf("no HDR blocker: %+v", plan.Blockers)
}

// The crux of what direct play IS: the browser picks the tracks, so the question
// is whether the track it will REACH FOR is one it can decode — not whether the
// file contains a decodable track somewhere.
func TestTheAudioQuestionIsAboutTheTrackTheBrowserWillChoose(t *testing.T) {
	// TrueHD is the default; AC3 is present but the browser will not reach it.
	p := movie("matroska,webm", h264(),
		AudioStream{Index: 1, Codec: "truehd", Channels: 8, Default: true},
		AudioStream{Index: 2, Codec: "aac", Channels: 2})

	plan := Decide(p, ChromeLike)
	if plan.DirectPlay {
		t.Fatal("accepted because the file contains an AAC track somewhere — " +
			"the browser will play the default track, which is TrueHD, and the " +
			"viewer gets silence")
	}
	found := false
	for _, b := range plan.Blockers {
		if b.Code == BlockAudioCodec && strings.Contains(b.Says, "default") {
			found = true
		}
	}
	if !found {
		t.Errorf("the audio blocker does not explain that it is about the "+
			"default track: %+v", plan.Blockers)
	}
}

// With no default marked, the first track is what a browser reaches for.
func TestWithNoDefaultMarkedTheFirstTrackIsTheOneThatMatters(t *testing.T) {
	playable := movie("matroska,webm", h264(),
		AudioStream{Index: 1, Codec: "aac"},
		AudioStream{Index: 2, Codec: "truehd"})
	if !Decide(playable, ChromeLike).DirectPlay {
		t.Error("refused although the first track is AAC")
	}

	unplayable := movie("matroska,webm", h264(),
		AudioStream{Index: 1, Codec: "truehd"},
		AudioStream{Index: 2, Codec: "aac"})
	if Decide(unplayable, ChromeLike).DirectPlay {
		t.Error("accepted although the first track is TrueHD")
	}
}

// A caveat is not a blocker. The film plays; switching language mid-film does
// not, and a viewer who hits silence deserves to have been told.
func TestAnUnplayableAlternateTrackIsACaveatAndNotARefusal(t *testing.T) {
	p := movie("matroska,webm", h264(),
		AudioStream{Index: 1, Codec: "aac", Default: true},
		AudioStream{Index: 2, Codec: "truehd"},
		AudioStream{Index: 3, Codec: "dts"})

	plan := Decide(p, ChromeLike)
	if !plan.DirectPlay {
		t.Fatalf("refused although the default track is AAC: %+v", plan.Blockers)
	}
	if len(plan.Caveats) != 1 {
		t.Fatalf("caveats = %+v, want one about the other tracks", plan.Caveats)
	}
	if !strings.Contains(plan.Caveats[0], "2") {
		t.Errorf("the caveat does not say how many: %q", plan.Caveats[0])
	}
	if !strings.Contains(plan.Caveats[0], "silent") {
		t.Errorf("the caveat does not say what the viewer will experience: %q",
			plan.Caveats[0])
	}
}

// An unstated bit depth is 8, not unlimited. Guessing high produces a stream
// that starts and then shows nothing, which is worse than a refusal.
func TestAnUnstatedBitDepthIsTreatedAsEight(t *testing.T) {
	c := Client{
		Containers:  []string{"matroska"},
		VideoCodecs: []string{"h264"},
		AudioCodecs: []string{"aac"},
		// MaxBitDepth deliberately zero.
	}
	p := movie("matroska,webm",
		VideoStream{Index: 0, Codec: "h264", BitDepth: 10, PixelFormat: "yuv420p10le"},
		aac())

	plan := Decide(p, c)
	if plan.DirectPlay {
		t.Fatal("a 10-bit file was accepted for a client that did not say it " +
			"could decode 10-bit — an unstated capability is not a capability")
	}
}

// A client's claim narrows what is attempted; it does not widen it past what
// the file actually is.
func TestAClientThatClaimsMoreStillOnlyGetsWhatTheFileIs(t *testing.T) {
	generous := Client{
		Containers:  []string{"matroska", "mp4"},
		VideoCodecs: []string{"h264", "hevc", "av1"},
		AudioCodecs: []string{"aac", "truehd", "dts"},
		MaxBitDepth: 10,
		HDR:         true,
	}
	// Everything the client claims — so this one really does play.
	if !Decide(movie("matroska,webm", hevc10(),
		AudioStream{Index: 1, Codec: "truehd", Default: true}), generous).DirectPlay {
		t.Error("a client claiming everything was still refused")
	}

	// But a codec it did not claim is still refused.
	if Decide(movie("matroska,webm",
		VideoStream{Index: 0, Codec: "vp9", BitDepth: 8}, aac()),
		generous).DirectPlay {
		t.Error("VP9 was accepted for a client that did not claim it")
	}
}

// An audio-only file is a legitimate thing to play.
func TestAnAudioOnlyFilePlays(t *testing.T) {
	p := Probe{Container: "mov,mp4,m4a,3gp,3g2,mj2",
		Audio: []AudioStream{{Index: 0, Codec: "aac", Default: true}}}
	plan := Decide(p, ChromeLike)
	if !plan.DirectPlay {
		t.Fatalf("an AAC file was refused: %+v", plan.Blockers)
	}
	if !strings.Contains(plan.Summary, "audio") {
		t.Errorf("summary = %q", plan.Summary)
	}
}

// A file with nothing in it is a named refusal, not a silent success.
func TestAFileWithNoStreamsIsRefusedPlainly(t *testing.T) {
	plan := Decide(Probe{Container: "matroska,webm"}, ChromeLike)
	if plan.DirectPlay {
		t.Fatal("a file with no streams was accepted for playback")
	}
	if len(plan.Blockers) != 1 || plan.Blockers[0].Code != BlockNoStreams {
		t.Errorf("blockers = %+v", plan.Blockers)
	}
}

// A container the client cannot open is its own reason, separate from codecs.
func TestAnUnopenableContainerIsItsOwnReason(t *testing.T) {
	p := movie("avi", h264(), aac())
	plan := Decide(p, ChromeLike)
	if plan.DirectPlay {
		t.Fatal("an AVI was accepted")
	}
	var got *Blocker
	for i := range plan.Blockers {
		if plan.Blockers[i].Code == BlockContainer {
			got = &plan.Blockers[i]
		}
	}
	if got == nil {
		t.Fatalf("no container blocker: %+v", plan.Blockers)
	}
	// The sentence must name one container, not ffprobe's whole list.
	if strings.Contains(got.Says, ",") {
		t.Errorf("the message reads out ffprobe's format list: %q", got.Says)
	}
}

// The default capability set is deliberately conservative, because being wrong
// optimistically produces a stream that starts and shows nothing.
func TestTheDefaultCapabilitySetDoesNotClaimHEVC(t *testing.T) {
	if contains(ChromeLike.VideoCodecs, "hevc") {
		t.Error("ChromeLike claims HEVC. Some browsers manage it and the cost " +
			"of being wrong is a black picture, while the cost of being " +
			"pessimistic is a warning beside a file that would have played")
	}
	if ChromeLike.HDR {
		t.Error("ChromeLike claims HDR")
	}
	if ChromeLike.MaxBitDepth != 8 {
		t.Errorf("ChromeLike.MaxBitDepth = %d", ChromeLike.MaxBitDepth)
	}
}

// What a real browser actually does with a Matroska file, established by
// serving the bytes rather than by asking canPlayType.
//
// This test exists because the opposite was nearly committed. An end-to-end run
// failed with MEDIA_ERR_SRC_NOT_SUPPORTED on an MKV; canPlayType("video/x-matroska")
// returns "", which looked like the answer; and removing Matroska from
// ChromeLike would have refused most of a real library.
//
// Serving the actual bytes to Chromium:
//
//	.mkv of VP9+Opus  as video/x-matroska  -> plays, 320x180, 3.008s
//	.mkv of VP9+Opus  as video/webm        -> plays
//	.mkv of H.264+AAC as anything          -> MEDIA_ERR_SRC_NOT_SUPPORTED
//
// The refusal was H.264: the test browser is the open-source Chromium build
// without the proprietary codecs real Chrome ships. The container was never the
// problem.
func TestTheDefaultCapabilitySetStillClaimsMatroska(t *testing.T) {
	if !contains(ChromeLike.Containers, "matroska") {
		t.Fatal("ChromeLike no longer claims Matroska. Browsers demux it — " +
			"verified by serving the bytes — and dropping it would refuse most " +
			"of a real library on the strength of canPlayType, which guesses")
	}
	// The commonest file in a library, and it must not be refused for its
	// container.
	plan := Decide(movie("matroska,webm", h264(), aac()), ChromeLike)
	if !plan.DirectPlay {
		t.Fatalf("an H.264/AAC MKV was refused: %+v", plan.Blockers)
	}
}

// ffprobe cannot tell a Matroska file from a WebM one: the demuxer handles
// both, so it reports "matroska,webm" for each. Verified against three files
// built for the purpose — a VP9 .mkv, the same content as a real .webm, and an
// H.264 .mkv — all three of which ffprobe called "matroska,webm".
//
// Which is fine, because the container name is not what decides playback. The
// CODECS are, and those ffprobe reports exactly.
func TestTheMatroskaAndWebMFamilyIsOneAnswer(t *testing.T) {
	webmish := movie("matroska,webm",
		VideoStream{Index: 0, Codec: "vp9", Width: 1920, Height: 1080, BitDepth: 8},
		AudioStream{Index: 1, Codec: "opus", Channels: 2, Default: true})
	if !Decide(webmish, ChromeLike).DirectPlay {
		t.Error("a VP9/Opus file in the matroska family was refused")
	}

	// And the codecs still decide: the same container with codecs a browser
	// cannot decode is refused, for the codecs and not for the container.
	hevcish := movie("matroska,webm", hevc10(), aac())
	plan := Decide(hevcish, ChromeLike)
	if plan.DirectPlay {
		t.Fatal("a 10-bit HEVC HDR file was accepted")
	}
	for _, b := range plan.Blockers {
		if b.Code == BlockContainer {
			t.Errorf("refused for its container rather than its codecs: %+v", b)
		}
	}
}

// A WebM of VP9 and Opus is what this Chromium genuinely plays, and it is the
// case an end-to-end browser run can actually prove.
func TestAWebMOfVP9AndOpusPlays(t *testing.T) {
	p := movie("matroska,webm",
		VideoStream{Index: 0, Codec: "vp9", Width: 1920, Height: 1080, BitDepth: 8},
		AudioStream{Index: 1, Codec: "opus", Channels: 2, Default: true})
	plan := Decide(p, ChromeLike)
	if !plan.DirectPlay {
		t.Errorf("a VP9/Opus WebM was refused: %+v", plan.Blockers)
	}
}

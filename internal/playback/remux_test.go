package playback

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// ---------------------------------------------------------------------------
// What a remux can and cannot fix
// ---------------------------------------------------------------------------

// The case this whole increment exists for: the video is fine and the audio is
// not. Every browser plays the picture and nothing else, which is this category
// of software's most common complaint.
func TestARemuxFixesUndecodableAudio(t *testing.T) {
	p := movie("matroska,webm", h264(),
		AudioStream{Index: 1, Codec: "truehd", Channels: 8, Default: true})

	plan := PlanRemux(p, ChromeLike, AudioAAC)
	if !plan.Possible {
		t.Fatalf("a remux was refused for an H.264 film with TrueHD audio: %s", plan.Why)
	}
	if !plan.ReencodeAudio {
		t.Error("the TrueHD track was going to be copied; a browser cannot decode it")
	}
	if plan.VideoIndex != 0 || plan.AudioIndex != 1 {
		t.Errorf("wrong tracks chosen: %+v", plan)
	}
}

// And the cheaper case: only the container was wrong. Re-encoding perfectly
// good audio costs CPU for nothing and loses quality doing it.
func TestAContainerOnlyProblemCopiesTheAudio(t *testing.T) {
	p := movie("matroska,webm", h264(), aac())

	plan := PlanRemux(p, ChromeLike, AudioAAC)
	if !plan.Possible {
		t.Fatalf("refused: %s", plan.Why)
	}
	if plan.ReencodeAudio {
		t.Error("AAC audio was going to be re-encoded to AAC — the container was " +
			"the problem, and the audio is already what a browser wants")
	}
}

// The refusals that matter, because each is a case where producing a stream
// would fail in a NEW way rather than not at all.
func TestARemuxRefusesWhatItCannotFix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		probe  Probe
		expect string
	}{
		{
			"the browser cannot decode the video",
			movie("matroska,webm",
				VideoStream{Index: 0, Codec: "hevc", Width: 1920, Height: 1080, BitDepth: 8},
				aac()),
			"video transcode",
		},
		{
			"the file is 10-bit",
			movie("matroska,webm",
				VideoStream{Index: 0, Codec: "h264", BitDepth: 10,
					PixelFormat: "yuv420p10le"}, aac()),
			"10-bit",
		},
		{
			"the file is HDR",
			movie("matroska,webm",
				VideoStream{Index: 0, Codec: "h264", BitDepth: 8,
					ColorTransfer: "smpte2084", HDR: true}, aac()),
			"HDR",
		},
		{
			"VP8 cannot go into an MP4",
			movie("matroska,webm",
				VideoStream{Index: 0, Codec: "vp8", BitDepth: 8}, aac()),
			"MP4 container",
		},
		{
			"there is no video at all",
			Probe{Container: "matroska,webm", Audio: []AudioStream{aac()}},
			"no video track",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := PlanRemux(tc.probe, ChromeLike, AudioAAC)
			if plan.Possible {
				t.Fatalf("a remux was offered for %s; it moves pictures without "+
					"changing them, so this would fail in the browser instead of "+
					"here", tc.name)
			}
			if !strings.Contains(plan.Why, tc.expect) {
				t.Errorf("Why = %q, want it to mention %q", plan.Why, tc.expect)
			}
			// And the refusal has to be a sentence, not a code.
			if len(plan.Why) < 30 {
				t.Errorf("Why = %q, which tells a viewer nothing", plan.Why)
			}
		})
	}
}

// VP8's absence from the MP4 list is the one that would fail silently: the
// remux would succeed and produce a file nothing plays.
func TestVP8IsNotMuxedIntoAnMP4(t *testing.T) {
	if videoCodecsMP4["vp8"] {
		t.Fatal("vp8 is allowed into MP4. It is a WebM codec; the remux would " +
			"succeed and the browser would get a container it cannot use, " +
			"which is worse than the refusal it replaces")
	}
	for _, want := range []string{"h264", "hevc", "av1", "vp9"} {
		if !videoCodecsMP4[want] {
			t.Errorf("%s cannot be carried into MP4, which is wrong", want)
		}
	}
}

// ---------------------------------------------------------------------------
// The one line between a request string and ffmpeg's argument vector
// ---------------------------------------------------------------------------

// A client names an output codec. That name is matched against a fixed list
// before anything reaches a process.
func TestOnlyAnAllowlistedAudioTargetIsAccepted(t *testing.T) {
	for _, good := range []string{"aac", "AAC", " opus ", "Opus"} {
		if _, ok := ParseAudioTarget(good); !ok {
			t.Errorf("ParseAudioTarget(%q) refused a codec this server produces", good)
		}
	}
	for _, bad := range []string{
		"", "mp3", "flac",
		// The shapes that would matter if this were a passthrough.
		"aac -f null /etc/passwd",
		"../../bin/sh",
		"aac;rm -rf /",
		"libfdk_aac",
	} {
		if got, ok := ParseAudioTarget(bad); ok {
			t.Errorf("ParseAudioTarget(%q) accepted it as %q", bad, got)
		}
	}
}

// And the check is repeated where the argument is actually built, not trusted
// to have happened earlier.
func TestStreamingRefusesACodecItWasNotAskedToProduce(t *testing.T) {
	r := newRemuxRig(t)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil).WithContext(r.ctx)
	w := httptest.NewRecorder()

	err := r.remuxer.Stream(w, req, 1,
		RemuxPlan{Possible: true, VideoIndex: 0, AudioIndex: -1},
		AudioTarget("libfdk_aac -y /etc/shadow"), StreamOptions{})
	if err == nil {
		t.Fatal("an unlisted codec reached the conversion")
	}
	if w.Body.Len() != 0 {
		t.Error("bytes were written before the refusal")
	}
}

// ---------------------------------------------------------------------------
// Admission control
// ---------------------------------------------------------------------------

type remuxRig struct {
	remuxer *Remuxer
	dir     string
	ctx     context.Context
}

func newRemuxRig(t *testing.T) *remuxRig {
	t.Helper()
	sr := newStreamRig(t)
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &remuxRig{
		remuxer: NewRemuxer(sr.streamer,
			NewSandbox(quiet, true), quiet, 2),
		dir: sr.dir,
		ctx: sr.ctx,
	}
}

// ADR-0005's premise is that this hardware has no headroom. An unbounded number
// of ffmpeg processes on four cores takes the working sessions down with the
// new one.
func TestConversionsAreBounded(t *testing.T) {
	r := newRemuxRig(t)

	got, limit := r.remuxer.Active()
	if got != 0 || limit != 2 {
		t.Fatalf("Active() = %d/%d at rest", got, limit)
	}

	// Occupy both slots: two calls, each taking one.
	first := r.remuxer.admit()
	second := r.remuxer.admit()
	if !first || !second {
		t.Fatal("could not take the two slots")
	}
	if r.remuxer.admit() {
		t.Fatal("a third conversion was admitted against a limit of two")
	}

	// And the HTTP path reports it rather than queueing silently.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil).WithContext(r.ctx)
	err := r.remuxer.Stream(httptest.NewRecorder(), req, 1,
		RemuxPlan{Possible: true, VideoIndex: 0, AudioIndex: -1}, AudioAAC, StreamOptions{})
	if err == nil || !strings.Contains(err.Error(), "already being converted") {
		t.Fatalf("err = %v, want a refusal naming the limit", err)
	}

	r.remuxer.release()
	if !r.remuxer.admit() {
		t.Error("a slot did not come back when a conversion finished")
	}
}

// An unset limit must not mean "as many as arrive".
func TestAnUnsetLimitIsSmallRatherThanUnlimited(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, given := range []int{0, -1, -100} {
		r := NewRemuxer(nil, nil, quiet, given)
		if _, limit := r.Active(); limit <= 0 || limit > 4 {
			t.Errorf("NewRemuxer(limit=%d) allows %d at once", given, limit)
		}
	}
}

// Slots are taken and returned under concurrency, because they are taken from
// HTTP handlers and nothing else serialises them.
func TestSlotsAreCountedCorrectlyUnderConcurrency(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := NewRemuxer(nil, nil, quiet, 3)

	var wg sync.WaitGroup
	var admitted, refused int
	var mu sync.Mutex
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r.admit() {
				mu.Lock()
				admitted++
				mu.Unlock()
				time.Sleep(time.Millisecond)
				r.release()
				return
			}
			mu.Lock()
			refused++
			mu.Unlock()
		}()
	}
	wg.Wait()

	if admitted+refused != 50 {
		t.Errorf("%d admitted + %d refused != 50", admitted, refused)
	}
	if n, _ := r.Active(); n != 0 {
		t.Errorf("%d slots were never returned", n)
	}
}

// ---------------------------------------------------------------------------
// The real thing
// ---------------------------------------------------------------------------

// A conversion end to end, through the real ffmpeg in the real jail, checked by
// probing what came out.
//
// The assertion that matters is the one about the VIDEO: it must be the same
// bytes, copied. If it were re-encoded this would still "work" and would cost
// twenty-five times as much CPU, which is the difference between the target
// hardware coping and not.
func TestAConversionCopiesTheVideoAndRebuildsTheAudio(t *testing.T) {
	haveFFmpeg(t)
	s := quietSandbox(t)

	dir := t.TempDir()
	src := filepath.Join(dir, "hard.mkv")
	out, err := exec.CommandContext(t.Context(), FFmpegPath, "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=4:size=320x180:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4",
		"-c:v", "libvpx-vp9", "-b:v", "300k", "-deadline", "realtime", "-cpu-used", "8",
		// AC3: a browser plays the picture and nothing else.
		"-c:a", "ac3", "-b:a", "192k", "-ac", "2",
		"-f", "matroska", src).CombinedOutput()
	if err != nil {
		t.Skipf("could not build the fixture (%v): %s", err, firstLine(out))
	}

	files := &fakeFiles{byID: map[int64]FileRef{
		1: {ID: 1, RootFolderID: 7, RelativePath: "hard.mkv"},
	}}
	vaults := &fakeVaults{dirs: map[int64]string{7: dir}}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	rx := NewRemuxer(NewStreamer(files, vaults), s, quiet, 2)

	ctx := adminContext()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	plan := RemuxPlan{Possible: true, VideoIndex: 0, AudioIndex: 1, ReencodeAudio: true}
	if err := rx.Stream(w, req, 1, plan, AudioOpus, StreamOptions{}); err != nil {
		t.Fatalf("converting: %v", err)
	}

	if w.Body.Len() < 1000 {
		t.Fatalf("the conversion produced %d bytes", w.Body.Len())
	}
	if got := w.Header().Get("Content-Type"); got != "video/mp4" {
		t.Errorf("Content-Type = %q", got)
	}
	// The output is a pipe, so there is nothing to serve a byte range from.
	// Saying "none" is what stops a browser seeking into nothing.
	if got := w.Header().Get("Accept-Ranges"); got != "none" {
		t.Errorf("Accept-Ranges = %q, want none — the output is not seekable "+
			"and a player that thinks it is will produce a scrubber that does "+
			"nothing", got)
	}

	// Probe what came out.
	result := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(result, w.Body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := exec.CommandContext(t.Context(), FFprobePath, "-v", "error",
		"-show_entries", "stream=codec_type,codec_name",
		"-of", "csv=p=0", result).CombinedOutput()
	if err != nil {
		t.Fatalf("probing the output (%v): %s", err, out)
	}
	got := string(info)
	t.Logf("the conversion produced: %s", strings.ReplaceAll(strings.TrimSpace(got), "\n", " | "))

	if !strings.Contains(got, "vp9") {
		t.Errorf("the video is not VP9 any more: %q — it was supposed to be "+
			"copied, and re-encoding it costs about 25x the CPU", got)
	}
	if !strings.Contains(got, "opus") {
		t.Errorf("the audio was not rebuilt: %q", got)
	}
	if strings.Contains(got, "ac3") {
		t.Errorf("the AC3 track survived: %q — the viewer would still get "+
			"silence", got)
	}
}

// adminContext is a principal that may browse, for the conversion tests, which
// build their own rig rather than borrowing one.
func adminContext() context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 1, Username: "jacob", State: authz.StateActive,
		MFASatisfied: true,
		Role: authz.Role{ID: 1, Name: "Admin", Rank: 100,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
	})
}

// noPermissionContext is an approved, active, MFA-satisfied account that has
// been granted nothing — the state a new user is in between approval and an
// operator choosing a role.
//
// Deliberately not an unauthenticated context: "no principal at all" is caught
// by the middleware long before a package like this one sees it, so testing
// with it would prove only that the middleware exists. The account that can log
// in and may do nothing is the one that actually reaches these methods.
func noPermissionContext() context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 9, Username: "nobody", State: authz.StateActive,
		MFASatisfied: true,
		Role:         authz.Role{ID: 9, Name: "Pending", Rank: 0},
	})
}

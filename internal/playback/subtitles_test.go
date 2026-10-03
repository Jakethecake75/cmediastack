package playback

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type subRig struct {
	subs *Subtitles
	dir  string
	ctx  context.Context
}

// newSubRig builds a library holding one film, so sidecars can be dropped
// beside it and found the way the importer would have named them.
func newSubRig(t *testing.T) *subRig {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "Arrival (2016)"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(dir, "Arrival (2016)", "Arrival (2016).mkv"),
		[]byte("not really a film"), 0o600); err != nil {
		t.Fatal(err)
	}

	files := &fakeFiles{byID: map[int64]FileRef{
		1: {ID: 1, RootFolderID: 7,
			RelativePath: "Arrival (2016)/Arrival (2016).mkv"},
	}}
	vaults := &fakeVaults{dirs: map[int64]string{7: dir}}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))

	return &subRig{
		subs: NewSubtitles(NewStreamer(files, vaults), NewSandbox(quiet, true)),
		dir:  dir,
		ctx:  adminContext(),
	}
}

func (r *subRig) put(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, "Arrival (2016)", name),
		[]byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const srtBody = "1\n00:00:01,000 --> 00:00:03,000\nHello.\n\n"

// A bitmap track is listed — an operator needs to know it is there — and marked
// unusable with the reason, rather than offered and then failing.
func TestABitmapTrackIsListedAndRefusedWithAReason(t *testing.T) {
	r := newSubRig(t)
	p := Probe{Subtitles: []SubtitleStream{
		{Index: 2, Codec: "subrip", Language: "eng", Text: true},
		{Index: 3, Codec: "hdmv_pgs_subtitle", Language: "eng", Text: false},
	}}

	tracks, err := r.subs.List(r.ctx, 1, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 {
		t.Fatalf("%d tracks, want 2: %+v", len(tracks), tracks)
	}
	if !tracks[0].Usable {
		t.Error("the SRT track was marked unusable")
	}
	if tracks[1].Usable {
		t.Fatal("a PGS track was offered; there is nothing to hand a <track>, " +
			"and it would fail in the browser instead of here")
	}
	if !strings.Contains(tracks[1].Why, "pictures of text") {
		t.Errorf("Why = %q, which does not explain the difference", tracks[1].Why)
	}
	if len(tracks[1].Why) < 40 {
		t.Errorf("Why = %q, which tells an operator nothing", tracks[1].Why)
	}
}

// Sidecars are found by the naming convention the importer uses, and only for
// THIS film — a folder holding two films must not mix their subtitles.
func TestSidecarsAreFoundForTheRightFilm(t *testing.T) {
	r := newSubRig(t)
	r.put(t, "Arrival (2016).en.srt", srtBody)
	r.put(t, "Arrival (2016).fr.srt", srtBody)
	r.put(t, "Arrival (2016).srt", srtBody)
	// Another film in the same directory, and its subtitle.
	r.put(t, "Dune (2021).mkv", "x")
	r.put(t, "Dune (2021).en.srt", srtBody)
	// Not a subtitle at all.
	r.put(t, "Arrival (2016).nfo", "x")

	tracks, err := r.subs.List(r.ctx, 1, Probe{})
	if err != nil {
		t.Fatal(err)
	}

	var langs []string
	for _, tr := range tracks {
		if tr.Embedded {
			t.Errorf("an embedded track appeared with no probe: %+v", tr)
		}
		langs = append(langs, tr.Language)
		if strings.Contains(tr.file, "Dune") {
			t.Errorf("another film's subtitle was offered: %q", tr.file)
		}
		if strings.HasSuffix(tr.file, ".nfo") {
			t.Errorf("a non-subtitle file was offered: %q", tr.file)
		}
	}
	if len(tracks) != 3 {
		t.Fatalf("%d sidecars, want 3: %v", len(tracks), langs)
	}
	// The untagged one has no language, which is different from guessing one.
	found := map[string]bool{}
	for _, l := range langs {
		found[l] = true
	}
	for _, want := range []string{"en", "fr", ""} {
		if !found[want] {
			t.Errorf("no sidecar with language %q; got %v", want, langs)
		}
	}
}

// An id names something the server offered. A client cannot introduce one.
func TestASubtitleIdCannotNameAFileOfItsOwn(t *testing.T) {
	r := newSubRig(t)
	r.put(t, "Arrival (2016).en.srt", srtBody)
	// Something worth stealing, in the same folder and out of it.
	r.put(t, "secrets.srt", "WEBVTT\n\nthe master key\n")
	secret := filepath.Join(filepath.Dir(r.dir), "outside.srt")
	if err := os.WriteFile(secret, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(secret) })

	for _, id := range []string{
		"s99",                        // past the end of the list
		"e42",                        // a stream that does not exist
		"../outside.srt",             // a path
		"/etc/passwd",                // an absolute one
		"Arrival (2016)/secrets.srt", // a real file that was not offered
		"secrets.srt",                // the same, unqualified
		"",                           // nothing
	} {
		t.Run(id, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil).WithContext(r.ctx)
			w := httptest.NewRecorder()
			err := r.subs.Serve(w, req, 1, Probe{}, id)
			if err == nil {
				t.Fatalf("id %q was served: %q", id, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "master key") ||
				strings.Contains(w.Body.String(), "outside") {
				t.Fatal("a file that was never offered reached the response")
			}
		})
	}
}

// The real conversion, through the real ffmpeg in the real jail.
func TestASidecarIsConvertedToWebVTT(t *testing.T) {
	haveFFmpeg(t)
	r := newSubRig(t)
	if !r.subs.sandbox.Available() {
		t.Skip("no sandbox on this kernel")
	}
	r.put(t, "Arrival (2016).en.srt",
		"1\n00:00:01,000 --> 00:00:03,000\nFirst line.\n\n"+
			"2\n00:00:04,000 --> 00:00:06,000\nSecond line.\n\n")

	tracks, err := r.subs.List(r.ctx, 1, Probe{})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("tracks = %+v (%v)", tracks, err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil).WithContext(r.ctx)
	w := httptest.NewRecorder()
	if err := r.subs.Serve(w, req, 1, Probe{}, tracks[0].ID); err != nil {
		t.Fatalf("serving: %v", err)
	}

	body := w.Body.String()
	if !strings.HasPrefix(body, "WEBVTT") {
		t.Fatalf("the output is not WebVTT, so a <track> will refuse it:\n%s", body)
	}
	for _, want := range []string{"First line.", "Second line.", "-->"} {
		if !strings.Contains(body, want) {
			t.Errorf("the conversion lost %q:\n%s", want, body)
		}
	}
	// SubRip's comma is WebVTT's full stop. Getting this wrong produces cues a
	// browser silently ignores.
	if strings.Contains(body, "00:00:01,000") {
		t.Error("SubRip timestamps survived; WebVTT uses a full stop and a " +
			"browser drops cues it cannot parse")
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/vtt") {
		t.Errorf("Content-Type = %q — served as anything else, a browser will "+
			"not treat it as subtitles", got)
	}
	if w.Header().Get("Content-Length") == "" {
		t.Error("no Content-Length on a response held entirely in memory")
	}
}

// A subtitle is text chosen by a stranger. What makes it safe is that it is
// served as WebVTT and handed to the browser's VTT parser, never to its HTML
// parser — so the content type is the security property, and the response must
// not carry anything that would make a browser reconsider.
func TestAHostileSubtitleIsServedAsSubtitlesAndNotAsMarkup(t *testing.T) {
	haveFFmpeg(t)
	r := newSubRig(t)
	if !r.subs.sandbox.Available() {
		t.Skip("no sandbox on this kernel")
	}
	r.put(t, "Arrival (2016).en.srt",
		"1\n00:00:01,000 --> 00:00:03,000\n"+
			`<script>alert(1)</script><img src=x onerror=alert(2)>`+"\n\n"+
			"2\n00:00:04,000 --> 00:00:06,000\nan & ampersand, a \"quote\"\n\n")

	tracks, _ := r.subs.List(r.ctx, 1, Probe{})
	if len(tracks) != 1 {
		t.Fatalf("tracks = %+v", tracks)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil).WithContext(r.ctx)
	w := httptest.NewRecorder()
	if err := r.subs.Serve(w, req, 1, Probe{}, tracks[0].ID); err != nil {
		t.Fatalf("serving: %v", err)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/vtt") {
		t.Fatalf("Content-Type = %q. A subtitle served as HTML is a script the "+
			"operator downloaded from a stranger", ct)
	}
	if strings.Contains(ct, "html") {
		t.Fatal("the response claims to be HTML")
	}
	// It is still WebVTT, and the cues still arrive — refusing the file outright
	// would be the wrong fix for the right worry.
	if !strings.HasPrefix(w.Body.String(), "WEBVTT") {
		t.Errorf("a subtitle with markup in it stopped being WebVTT:\n%s", w.Body.String())
	}
	t.Logf("served as %s:\n%s", ct, w.Body.String())
}

// Listing is browsing, and so is fetching.
func TestSubtitlesNeedThePermissionToBrowse(t *testing.T) {
	r := newSubRig(t)
	r.put(t, "Arrival (2016).en.srt", srtBody)
	nobody := noPermissionContext()

	if _, err := r.subs.List(nobody, 1, Probe{}); err == nil {
		t.Error("a principal with no permissions listed subtitles")
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil).WithContext(nobody)
	w := httptest.NewRecorder()
	if err := r.subs.Serve(w, req, 1, Probe{}, "s0"); err == nil {
		t.Error("a principal with no permissions fetched a subtitle")
	}
}

// Bitmap sidecars are not offered either, for the same reason embedded ones are
// not: there is nothing to hand a <track>.
func TestBitmapSidecarsAreNotOffered(t *testing.T) {
	r := newSubRig(t)
	r.put(t, "Arrival (2016).en.srt", srtBody)
	r.put(t, "Arrival (2016).en.sub", "binary-ish")
	r.put(t, "Arrival (2016).en.idx", "binary-ish")

	tracks, err := r.subs.List(r.ctx, 1, Probe{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range tracks {
		if strings.HasSuffix(tr.file, ".sub") || strings.HasSuffix(tr.file, ".idx") {
			t.Errorf("a bitmap sidecar was offered: %q", tr.file)
		}
	}
	if len(tracks) != 1 {
		t.Errorf("%d tracks, want only the SRT", len(tracks))
	}
}

// An embedded track is taken by the index the PROBE reported, so a file whose
// subtitles do not start at stream 0 still works.
func TestAnEmbeddedTrackIsTakenByItsRealStreamIndex(t *testing.T) {
	haveFFmpeg(t)
	s := quietSandbox(t)

	dir := t.TempDir()
	srt := filepath.Join(dir, "s.srt")
	if err := os.WriteFile(srt, []byte(
		"1\n00:00:01,000 --> 00:00:03,000\nEmbedded line.\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	film := filepath.Join(dir, "film.mkv")
	// Video, audio, THEN subtitles: the subtitle stream is index 2.
	if out, err := exec.CommandContext(t.Context(), FFmpegPath, "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=4:size=160x90:rate=25",
		"-f", "lavfi", "-i", "sine=duration=4",
		"-i", srt,
		"-c:v", "mpeg4", "-q:v", "8", "-c:a", "aac", "-c:s", "srt",
		"-metadata:s:s:0", "language=eng",
		film).CombinedOutput(); err != nil {
		t.Skipf("could not build the fixture (%v): %s", err, firstLine(out))
	}

	files := &fakeFiles{byID: map[int64]FileRef{
		1: {ID: 1, RootFolderID: 7, RelativePath: "film.mkv"},
	}}
	subs := NewSubtitles(NewStreamer(files, &fakeVaults{dirs: map[int64]string{7: dir}}), s)

	// The probe says index 2, which is what must be mapped.
	p := Probe{Subtitles: []SubtitleStream{
		{Index: 2, Codec: "subrip", Language: "eng", Text: true},
	}}
	tracks, err := subs.List(adminContext(), 1, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) == 0 || !tracks[0].Embedded {
		t.Fatalf("tracks = %+v", tracks)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil).WithContext(adminContext())
	w := httptest.NewRecorder()
	if err := subs.Serve(w, req, 1, p, tracks[0].ID); err != nil {
		t.Fatalf("serving the embedded track: %v", err)
	}
	if !strings.Contains(w.Body.String(), "Embedded line.") {
		t.Errorf("the embedded track did not come out:\n%s", w.Body.String())
	}
}

// A conversion that produces timings and no words is a failure, not a track.
//
// This came out of a live run, not out of thinking about it. A malformed ASS
// file — one whose Events "Format:" line lists fewer fields than its Dialogue
// lines use — converts without an error: ffmpeg reads the timings, puts the
// text in the wrong field, and emits every cue empty. The response was 130
// bytes of perfectly valid WebVTT, so the length check saw nothing wrong, and
// the viewer got a subtitle track that displayed nothing and explained nothing.
func TestASubtitleThatConvertsToTimingsWithNoTextIsRefused(t *testing.T) {
	haveFFmpeg(t)
	r := newSubRig(t)
	if !r.subs.sandbox.Available() {
		t.Skip("no sandbox on this kernel")
	}
	// The exact shape that found this: four Format fields, six used.
	r.put(t, "Arrival (2016).en.ass",
		"[Script Info]\nScriptType: v4.00+\n\n[Events]\n"+
			"Format: Layer, Start, End, Style, Text\n"+
			"Dialogue: 0,0:00:02.00,0:00:07.00,Default,Fear is the mind-killer.\n")

	tracks, err := r.subs.List(r.ctx, 1, Probe{})
	if err != nil || len(tracks) != 1 {
		t.Fatalf("tracks = %+v (%v)", tracks, err)
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil).WithContext(r.ctx)
	w := httptest.NewRecorder()
	err = r.subs.Serve(w, req, 1, Probe{}, tracks[0].ID)
	if err == nil {
		t.Fatalf("an empty conversion was served as a subtitle track:\n%q",
			w.Body.String())
	}
	if !errors.Is(err, ErrEmptySubtitle) {
		t.Errorf("err = %v, want ErrEmptySubtitle so a caller can say WHY", err)
	}
}

// The same file written correctly still works — the refusal above must be about
// the empty result, not about ASS.
func TestAWellFormedASSFileIsConverted(t *testing.T) {
	haveFFmpeg(t)
	r := newSubRig(t)
	if !r.subs.sandbox.Available() {
		t.Skip("no sandbox on this kernel")
	}
	r.put(t, "Arrival (2016).en.ass", assFixture)

	tracks, _ := r.subs.List(r.ctx, 1, Probe{})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x", nil).WithContext(r.ctx)
	w := httptest.NewRecorder()
	if err := r.subs.Serve(w, req, 1, Probe{}, tracks[0].ID); err != nil {
		t.Fatalf("serving a well-formed ASS file: %v", err)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Fear is the mind-killer.") {
		t.Errorf("the dialogue did not survive:\n%s", body)
	}
	// The styling override is dropped, which is documented rather than fixed:
	// WebVTT has no equivalent and inventing one would be worse than plain
	// cues. Asserted so that it stays a known loss instead of a surprise.
	if strings.Contains(body, "an8") || strings.Contains(body, "&H") {
		t.Errorf("ASS style overrides reached the cue text:\n%s", body)
	}
}

const assFixture = `[Script Info]
ScriptType: v4.00+
PlayResX: 1920
PlayResY: 1080

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Arial,72,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:02.00,0:00:07.00,Default,,0,0,0,,{\an8\c&HFF0000&}A styled line
Dialogue: 0,0:00:08.00,0:00:13.00,Default,,0,0,0,,Fear is the mind-killer.
`

// hasCueText decides whether a conversion is worth serving, so its edges are
// worth stating rather than inferring from one integration test.
func TestWhatCountsAsACueWithTextInIt(t *testing.T) {
	for _, c := range []struct {
		name string
		vtt  string
		want bool
	}{
		{"a normal cue", "WEBVTT\n\n00:01.000 --> 00:03.000\nHello.\n", true},
		{"timings and nothing else", "WEBVTT\n\n00:01.000 --> 00:03.000\n\n00:04.000 --> 00:06.000\n", false},
		{"a header and no cues", "WEBVTT\n", false},
		{"whitespace is not text", "WEBVTT\n\n00:01.000 --> 00:03.000\n   \t \n", false},
		{"cue settings are not payload", "WEBVTT\n\n00:01.000 --> 00:03.000 line:90% align:middle\n", false},
		{"a NOTE is not payload", "WEBVTT\n\nNOTE this file is empty\n", false},
		{"the second cue carries it", "WEBVTT\n\n00:01.000 --> 00:03.000\n\n00:04.000 --> 00:06.000\nThere.\n", true},
		{"CRLF", "WEBVTT\r\n\r\n00:01.000 --> 00:03.000\r\nHello.\r\n", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := hasCueText([]byte(c.vtt)); got != c.want {
				t.Errorf("hasCueText = %v, want %v for:\n%q", got, c.want, c.vtt)
			}
		})
	}
}

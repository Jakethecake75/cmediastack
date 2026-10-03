package playback

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Converting a subtitle to WebVTT.
//
// # Why this is a file of its own
//
// It is here rather than in subtitles.go because subtitles.go does path work —
// it reads a directory, matches names against the video's stem, and joins them
// — and this builds an ffmpeg argument vector. Those two things in one file are
// one variable name apart from a path reaching a media tool, which is the thing
// ADR-0020 exists to prevent.
//
// The split is not cosmetic. Nothing in this file has a filename to give away:
// it is handed an ALREADY-OPEN descriptor and an integer, and that is all it
// can pass on. TestNoMediaToolIsEverGivenAPath enforces the same rule by
// reading the source, which is how the split stays true after everyone has
// forgotten this paragraph.

// ErrEmptySubtitle means the conversion produced no usable cues.
var ErrEmptySubtitle = errors.New("playback: that subtitle track has no text in it")

// toWebVTT converts one subtitle stream of an open file and returns the result.
//
// streamIndex < 0 means "whatever the input's only subtitle stream is", which
// is the sidecar case: the file IS the subtitle.
//
// Run rather than Pipe: subtitles are kilobytes, and having the whole answer in
// hand means a Content-Length and a proper status code instead of a
// half-written response when the conversion fails.
func (s *Subtitles) toWebVTT(ctx context.Context, f *os.File, streamIndex int) ([]byte, error) {
	args := []string{
		"-loglevel", "error",
		// ffmpeg reads stdin by default and would consume the parent's.
		"-nostdin",
		// fd 3, handed over by the parent. Never a path (ADR-0020).
		"-i", "/dev/fd/3",
	}
	if streamIndex >= 0 {
		// The stream index comes from the PROBE, not from the request.
		args = append(args, "-map", "0:"+strconv.Itoa(streamIndex))
	}
	args = append(args, "-c:s", "webvtt", "-f", "webvtt", "pipe:1")

	res, err := s.sandbox.Run(ctx, SubtitleTimeout, f, FFmpegPath, args...)
	if err != nil {
		return nil, err
	}
	if len(res.Stdout) == 0 {
		return nil, fmt.Errorf("%w: it converted to nothing at all", ErrEmptySubtitle)
	}
	// A conversion can "succeed" and produce timings with no words in them.
	//
	// Found live, on a malformed ASS file: ffmpeg read the Dialogue lines,
	// carried the timings across, and emitted every cue empty. The response was
	// several hundred bytes of valid WebVTT, so a length check saw nothing
	// wrong — and a viewer who turned the track on got an invisible one and no
	// reason at all.
	//
	// A track with no text is not a track. Saying so is the same courtesy as
	// refusing a bitmap track with an explanation instead of offering it.
	if !hasCueText(res.Stdout) {
		return nil, fmt.Errorf("%w: it converted to timings with no text in "+
			"them, which usually means the file is malformed", ErrEmptySubtitle)
	}
	return res.Stdout, nil
}

// hasCueText reports whether any cue in a WebVTT document has words in it.
//
// Deliberately crude: everything after a timing line and before the next blank
// line is that cue's payload, which is all of the WebVTT grammar this question
// needs. Cue settings live ON the timing line, so they cannot be mistaken for
// payload, and the header, NOTE and STYLE blocks are never preceded by one.
func hasCueText(b []byte) bool {
	inCue := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.Contains(line, "-->"):
			inCue = true
		case strings.TrimSpace(line) == "":
			inCue = false
		case inCue:
			return true
		}
	}
	return false
}

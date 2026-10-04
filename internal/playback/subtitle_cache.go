package playback

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// The subtitle cache (ADR-0074). Paths live here, apart from the files that
// start media tools; the read itself is readOutTracks, in vtt.go.

// embeddedText is the stream indices of a file's embedded text tracks, in the
// order they are read out, so a track's place in the cached file is its
// position here.
func embeddedText(tracks []SubtitleTrack) []int {
	var out []int
	for _, t := range tracks {
		if t.Embedded && t.Usable {
			out = append(out, t.index)
		}
	}
	return out
}

// prepare starts reading a file's embedded text tracks into the cache, unless
// they are there or already being read. It returns the cached file's path and
// the read in progress, nil when there is none to wait for.
func (s *Subtitles) prepare(ctx context.Context, fileID int64, p Probe,
	tracks []SubtitleTrack) (string, *subtitleRead) {

	indices := embeddedText(tracks)
	if s.cacheDir == "" || len(indices) == 0 {
		return "", nil
	}
	// The size is in the name, so a replaced file is read again.
	key := strconv.FormatInt(fileID, 10) + "-" + strconv.FormatInt(p.SizeBytes, 10)
	dest := filepath.Join(s.cacheDir, key+".mkv")

	s.mu.Lock()
	defer s.mu.Unlock()
	if rd, ok := s.reading[key]; ok {
		return dest, rd
	}
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	rd := &subtitleRead{done: make(chan struct{})}
	s.reading[key] = rd
	// Not tied to the request: a viewer who stops waiting has still started
	// the only read this file needs.
	bg := context.WithoutCancel(ctx)
	go func() {
		rd.err = s.readOut(bg, fileID, indices, dest)
		close(rd.done)
		if rd.err == nil {
			s.mu.Lock()
			delete(s.reading, key)
			s.mu.Unlock()
		}
	}()
	return dest, rd
}

// readOut copies the embedded text tracks, as WebVTT, into one small
// Matroska file at dest, in a single read of the film.
func (s *Subtitles) readOut(ctx context.Context, fileID int64, indices []int, dest string) error {
	ref, err := s.streamer.files.FileForPlayback(ctx, fileID)
	if err != nil {
		return err
	}
	f, err := s.openInVault(ctx, fileID, ref.RelativePath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".*.part")
	if err != nil {
		return err
	}
	err = s.readOutTracks(ctx, f, indices, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("playback: reading the subtitles out of the file: %w", err)
	}
	return os.Rename(tmp.Name(), dest)
}

package subtitles

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
	"unicode/utf8"
)

// hashChunk is how much of each end of a file the hash reads.
const hashChunk = 64 << 10

// ErrTooSmall means a file is too small to hash.
var ErrTooSmall = errors.New("subtitles: the file is smaller than the 128 KiB the hash reads")

// Hash is OpenSubtitles' hash of a file: its size plus the 64-bit sum of the
// little-endian words of its first and last 64 KiB, as sixteen hex digits
// (ADR-0055, decision 3).
func Hash(r io.ReaderAt, size int64) (string, error) {
	if size < 2*hashChunk {
		return "", ErrTooSmall
	}
	sum := uint64(size) // #nosec G115 -- a file's size is never negative here, checked above
	buf := make([]byte, hashChunk)
	for _, off := range []int64{0, size - hashChunk} {
		if _, err := r.ReadAt(buf, off); err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("subtitles: reading the file to hash it: %w", err)
		}
		for i := 0; i < hashChunk; i += 8 {
			sum += binary.LittleEndian.Uint64(buf[i : i+8])
		}
	}
	return fmt.Sprintf("%016x", sum), nil
}

// CheckSRT refuses what is not a plausible SRT: too large, not UTF-8 (a
// byte-order mark allowed), or without a single timing line.
func CheckSRT(body []byte) error {
	switch {
	case len(body) > MaxSubtitleBytes:
		return fmt.Errorf("%w: it is larger than %d bytes", ErrNotASubtitle, MaxSubtitleBytes)
	case !utf8.Valid(body):
		return fmt.Errorf("%w: it is not UTF-8 text", ErrNotASubtitle)
	case !bytes.Contains(body, []byte("-->")):
		return fmt.Errorf("%w: it has no timing line", ErrNotASubtitle)
	case bytes.Contains(bytes.ToLower(body[:min(len(body), 512)]), []byte("<html")):
		return fmt.Errorf("%w: it is a web page", ErrNotASubtitle)
	}
	return nil
}

// Choose picks the subtitle for a file from what was offered, or reports
// that none is it (ADR-0055, decision 3). A result must be in the language
// asked for; when it names a title, that title must be this one; with no
// title to ask by, only a hash match will do. A hash match ranks first, then
// a subtitle nobody machine-translated, then the most downloaded.
func Choose(results []Result, q Query) (Result, bool) {
	var ok []Result
	for _, r := range results {
		if r.Language != q.Language {
			continue
		}
		if q.TMDBID == 0 && q.ParentTMDBID == 0 && !r.HashMatch {
			continue
		}
		if !names(r, q) {
			continue
		}
		ok = append(ok, r)
	}
	if len(ok) == 0 {
		return Result{}, false
	}
	sort.SliceStable(ok, func(i, j int) bool {
		a, b := ok[i], ok[j]
		if a.HashMatch != b.HashMatch {
			return a.HashMatch
		}
		if a.Translated != b.Translated {
			return !a.Translated
		}
		return a.Downloads > b.Downloads
	})
	return ok[0], true
}

// names reports whether a result, when it says what it is for, says this.
func names(r Result, q Query) bool {
	switch {
	case q.ParentTMDBID > 0:
		if r.ParentTMDBID != 0 && r.ParentTMDBID != q.ParentTMDBID {
			return false
		}
		if r.Season != 0 && r.Season != q.Season {
			return false
		}
		return r.Episode == 0 || r.Episode == q.Episode
	case q.TMDBID > 0:
		// A film's search takes no episode's subtitle.
		return r.ParentTMDBID == 0 && (r.TMDBID == 0 || r.TMDBID == q.TMDBID)
	}
	return true
}

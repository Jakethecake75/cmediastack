// Package importer turns a completed download into a library entry.
//
// # The decision that matters most here
//
// A torrent is a directory of files chosen by a stranger. A realistic one looks
// like this:
//
//	Movie.2019.1080p.BluRay.x264-GRP/
//	  Movie.2019.1080p.BluRay.x264-GRP.mkv     18 GiB   <- the film
//	  Sample/sample.mkv                        48 MiB   <- not the film
//	  Subs/2_English.srt                       71 KiB
//	  RARBG_DO_NOT_MIRROR.exe                   1 KiB   <- an executable
//	  movie.nfo                                 3 KiB
//	  poster.jpg                              412 KiB
//
// Choosing wrongly does not produce an error. It produces a library entry that
// plays a 48-megabyte sample, or a `.exe` sitting in a media folder that gets
// synced to a phone, or a `.nfo` renamed to `Movie (2019).mkv` because the
// naming step does not look at content. Every one of those is silent.
//
// So selection is an ALLOWLIST of container extensions, not a denylist of
// things to avoid. A denylist has to be right about every file type a stranger
// might invent a reason to include; an allowlist has to be right about the
// dozen containers this software can actually play. Only one of those is a
// finite problem.
package importer

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Limits on what counts as media.
const (
	// MinVideoBytes is the floor below which a file is not a feature or an
	// episode, whatever its extension. Chosen low deliberately: a 20-minute
	// animated episode at a modest bitrate is genuinely small, and refusing an
	// operator's legitimate file is worse than importing a large sample, which
	// the sample rules below catch anyway.
	MinVideoBytes = 8 << 20 // 8 MiB

	// SampleFraction is the share of the largest candidate below which a file
	// is treated as a sample even if nothing in its name says so. Release
	// groups are inconsistent about naming samples, but they are consistent
	// about size: a sample is a minute or two of a two-hour film.
	SampleFraction = 0.20
)

// videoExtensions is the allowlist. Lowercase, with the dot.
//
// Deliberately absent:
//
//   - .iso and .img — a disc image cannot be direct-played, cannot be probed
//     for streams without mounting it, and importing one produces a library
//     entry nothing can play. Skipping it with a clear reason beats importing
//     something broken.
//   - .rar, .zip, .7z, .001 — an archive is not media. Unpacking one means
//     running a decompressor over attacker-controlled input, which is a
//     process-boundary problem (ADR-0007) and not something to do casually
//     inside the main binary.
//   - .exe, .scr, .lnk and everything else. They are not here because nothing
//     that is not a video container is here.
var videoExtensions = map[string]struct{}{
	".mkv": {}, ".mp4": {}, ".m4v": {}, ".avi": {}, ".mov": {},
	".wmv": {}, ".mpg": {}, ".mpeg": {}, ".m2ts": {}, ".mts": {},
	".ts": {}, ".webm": {}, ".flv": {}, ".ogv": {}, ".divx": {},
	".vob": {}, ".3gp": {}, ".asf": {}, ".rm": {}, ".rmvb": {},
}

// subtitleExtensions ride along with the video they belong to.
var subtitleExtensions = map[string]struct{}{
	".srt": {}, ".ass": {}, ".ssa": {}, ".vtt": {}, ".sub": {}, ".idx": {},
}

// sampleMarkers are words that mean "not the feature". How they are matched
// depends on WHERE they appear — see hasSampleMarker, which is where the care
// is: the same word is conclusive in a directory name and ambiguous in a
// filename.
var sampleMarkers = map[string]struct{}{
	"sample": {}, "samples": {}, "trailer": {}, "trailers": {},
	"extras": {}, "extra": {}, "featurettes": {}, "featurette": {},
	"behind the scenes": {}, "deleted scenes": {}, "interviews": {},
	"proof": {}, "screens": {}, "screenshots": {},
}

// Candidate is one file inside a completed download.
type Candidate struct {
	// Path is relative to the download's own directory, with forward slashes.
	Path  string
	Bytes int64
}

// Ext returns the lowercase extension, with the dot.
func (c Candidate) Ext() string { return strings.ToLower(path.Ext(c.Path)) }

// IsVideo reports whether the extension is an allowlisted container.
func (c Candidate) IsVideo() bool {
	_, ok := videoExtensions[c.Ext()]
	return ok
}

// IsSubtitle reports whether the file is a sidecar subtitle.
func (c Candidate) IsSubtitle() bool {
	_, ok := subtitleExtensions[c.Ext()]
	return ok
}

// Selection is what the importer decided to take, and what it refused.
type Selection struct {
	// Video is the file to import. Empty Path means nothing was suitable.
	Video Candidate
	// Subtitles are sidecars that belong with it.
	Subtitles []Candidate
	// Rejected explains, per file, why it was not chosen. This is the whole
	// answer to "it downloaded and then nothing happened", and it is kept even
	// on success so an operator can see that the sample was recognised as one
	// rather than wonder whether it was imported by mistake.
	Rejected []Rejection
}

// Rejection is one file and the reason it was not the media.
type Rejection struct {
	Path   string
	Bytes  int64
	Reason string
}

// Reasons, as an operator would read them.
const (
	ReasonNotAContainer = "not a media container this software can play"
	ReasonTooSmall      = "too small to be a feature or an episode"
	ReasonSampleByName  = "a sample, trailer or extra by its path"
	ReasonSampleBySize  = "far smaller than the main file, so a sample"
	ReasonNotTheLargest = "another file in this download is the main one"
	ReasonSubtitle      = "a subtitle, imported alongside the video"
)

// Select decides which file in a download is the media.
//
// The rules run in this order, and the order matters:
//
//  1. Extension allowlist. Everything else is out, including archives and
//     executables, and it is out because it is not on the list rather than
//     because somebody thought to exclude it.
//  2. Path markers: a file under Sample/ or Extras/ is not the feature, however
//     large. Directory names are matched exactly and filenames only at the end,
//     so "Free Samples (2012)" and "Trailer Park Boys" survive — see
//     hasSampleMarker.
//  3. Absolute size floor.
//  4. Relative size: anything under a fifth of the largest remaining candidate
//     is a sample whatever it is called. Release groups are inconsistent about
//     naming samples and consistent about their size.
//  5. Largest wins.
//
// A download with no suitable file is not an error here. It is a Selection with
// an empty Video and a Rejected list that says why, which is what the caller
// records so an operator can read it later.
func Select(files []Candidate) Selection {
	var sel Selection

	// Pass 1: split by kind, and take the sidecars out of the running.
	videos, subs, rejected := sortOut(files)
	sel.Subtitles, sel.Rejected = subs, rejected
	if len(videos) == 0 {
		sortRejections(sel.Rejected)
		return sel
	}

	// Pass 2: largest first, then drop anything dwarfed by it.
	sort.SliceStable(videos, func(i, j int) bool { return videos[i].Bytes > videos[j].Bytes })
	largest := videos[0]
	floor := int64(float64(largest.Bytes) * SampleFraction)

	sel.Video = largest
	for _, f := range videos[1:] {
		reason := ReasonNotTheLargest
		if f.Bytes < floor {
			reason = ReasonSampleBySize
		}
		sel.Rejected = append(sel.Rejected, Rejection{f.Path, f.Bytes, reason})
	}

	// Sidecars are kept only if they plausibly belong to the chosen video: the
	// same directory, or a Subs/ directory beside it. A subtitle from an
	// unrelated folder would be renamed to match the film and then be wrong in
	// a way nobody notices until they turn subtitles on.
	sel.Subtitles = subtitlesFor(largest, sel.Subtitles)
	sortRejections(sel.Rejected)
	return sel
}

// sortOut is rules 1 to 3: the videos that could be the media, the subtitles,
// and why every other file is not. Shared by Select and SelectPack, so a pack
// and a single download refuse the same files for the same reasons.
func sortOut(files []Candidate) (videos, subs []Candidate, rejected []Rejection) {
	for _, f := range files {
		switch {
		case f.IsSubtitle():
			subs = append(subs, f)
		case !f.IsVideo():
			rejected = append(rejected, Rejection{f.Path, f.Bytes, ReasonNotAContainer})
		case hasSampleMarker(f.Path):
			rejected = append(rejected, Rejection{f.Path, f.Bytes, ReasonSampleByName})
		case f.Bytes < MinVideoBytes:
			rejected = append(rejected, Rejection{f.Path, f.Bytes, ReasonTooSmall})
		default:
			videos = append(videos, f)
		}
	}
	return videos, subs, rejected
}

// hasSampleMarker reports whether a path says "not the feature".
//
// # Two different rules, because the two positions carry different meaning
//
// A DIRECTORY named exactly "Sample" or "Extras" is unambiguous — nobody names
// a folder that by accident, so an exact segment match is safe and complete.
//
// A FILENAME is not. The marker has to sit where release groups actually put
// it: as the whole name ("sample.mkv") or as the last word before the extension
// ("Movie.2019.1080p-GRP.sample.mkv", "Movie-sample.mkv"). Matching the word
// anywhere in the name is what discards an operator's film, and the examples
// are not contrived:
//
//	Free.Samples.2012.1080p.BluRay-GRP.mkv        a real film
//	Trailer.Park.Boys.The.Movie.2014.1080p.mkv    a real film
//	The.Trailer.Park.Boys.S01E01.mkv              a real series
//	Resampled.2019.1080p.mkv                      substring matching loses this
//
// A marker buried mid-name ("Movie.2019.sample.1080p.mkv") is missed by this
// rule and caught by the size rule instead, which is the reliable one and the
// reason it exists: release groups are inconsistent about naming samples and
// consistent about their size.
func hasSampleMarker(p string) bool {
	segments := strings.Split(strings.ToLower(p), "/")
	if len(segments) == 0 {
		return false
	}

	// Directories: exact match on the whole segment.
	for _, seg := range segments[:len(segments)-1] {
		if _, bad := sampleMarkers[strings.TrimSpace(seg)]; bad {
			return true
		}
	}

	// Filename: the whole stem, or its last word.
	stem := strings.TrimSpace(strings.TrimSuffix(segments[len(segments)-1],
		path.Ext(segments[len(segments)-1])))
	if _, bad := sampleMarkers[stem]; bad {
		return true
	}
	words := splitWords(stem)
	if len(words) == 0 {
		return false
	}
	_, bad := sampleMarkers[words[len(words)-1]]
	return bad
}

// splitWords breaks a filename segment on the separators release names use.
func splitWords(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		switch r {
		case '.', '_', '-', ' ', '[', ']', '(', ')', '{', '}':
			return true
		}
		return false
	})
}

// subtitlesFor keeps the sidecars that plausibly belong to the chosen video.
func subtitlesFor(video Candidate, subs []Candidate) []Candidate {
	videoDir := path.Dir(video.Path)
	out := make([]Candidate, 0, len(subs))
	for _, s := range subs {
		dir := path.Dir(s.Path)
		if dir == videoDir || path.Dir(dir) == videoDir {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func sortRejections(r []Rejection) {
	sort.SliceStable(r, func(i, j int) bool {
		if r[i].Bytes != r[j].Bytes {
			return r[i].Bytes > r[j].Bytes
		}
		return r[i].Path < r[j].Path
	})
}

// Summary renders a selection for a log line or an import record.
func (s Selection) Summary() string {
	if s.Video.Path == "" {
		if len(s.Rejected) == 0 {
			return "the download contained no files"
		}
		return fmt.Sprintf("no media file was suitable: %d file(s) examined, "+
			"the largest was %q (%s)", len(s.Rejected),
			s.Rejected[0].Path, s.Rejected[0].Reason)
	}
	out := fmt.Sprintf("chose %q", s.Video.Path)
	if n := len(s.Subtitles); n > 0 {
		out += fmt.Sprintf(" with %d subtitle file(s)", n)
	}
	if n := len(s.Rejected); n > 0 {
		out += fmt.Sprintf("; %d other file(s) were not media", n)
	}
	return out
}

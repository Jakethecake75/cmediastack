package playback

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// Subtitles: the ones that are already there.
//
// # What this is and is not
//
// This serves subtitles a library already holds — tracks inside the container,
// and sidecar files the importer placed beside the video. It does NOT fetch
// them from a provider, which is the larger half of replacing Bazarr and is its
// own increment.
//
// # Everything becomes WebVTT, because that is what a browser accepts
//
// A <track> element takes WebVTT and nothing else. SubRip, ASS and MicroDVD all
// have to be converted, which ffmpeg does in milliseconds — these are kilobytes
// of text, not gigabytes of pictures.
//
// The conversion loses ASS styling: positioning, fonts, colours and karaoke
// timing all flatten to plain cues. For dialogue that is invisible; for signs
// and songs typeset by a fansub group it is a real loss, and it is stated here
// rather than discovered.
//
// # Bitmap tracks cannot be served at all
//
// PGS and VobSub are pictures of text. There is nothing to hand a <track>: they
// can only be drawn onto the video, which is a transcode of the picture — and
// on the target hardware (ADR-0005) that is the thing this project does not do.
// They are listed, because an operator needs to know they exist, and marked
// unusable with the reason.

// ErrNoSuchSubtitle means the id does not name a track this file has.
var ErrNoSuchSubtitle = errors.New("playback: no such subtitle track")

// ErrSubtitlesPreparing means a file's subtitles are still being read out of
// it (ADR-0074). The same request works shortly.
var ErrSubtitlesPreparing = errors.New("this film's subtitles are still being read out of " +
	"the file, which takes a few minutes for a large one")

// subtitleWait is how long a request waits for that before saying so. A
// variable only so a test can shorten it.
var subtitleWait = 20 * time.Second

// SubtitleTimeout bounds a conversion. Generous for a text file, and short
// enough that a crafted subtitle cannot occupy a worker.
const SubtitleTimeout = 30 * time.Second

// subtitleExtensions are the sidecar formats worth offering. Each is text that
// ffmpeg can convert; bitmap sidecars (.sub/.idx) are deliberately absent for
// the same reason embedded bitmap tracks are.
var subtitleExtensions = map[string]bool{
	".srt": true, ".vtt": true, ".ass": true, ".ssa": true, ".sub": false,
}

// SubtitleTrack is one subtitle a viewer could choose.
type SubtitleTrack struct {
	// ID is the opaque handle a client asks for.
	//
	// Opaque on purpose. A client names a track by an id the server just
	// issued, and the server resolves it against a freshly built list — so a
	// request can only select something already offered, and there is no
	// filename in it to point somewhere else. The same containment as the
	// artwork route (ADR-0018).
	ID string

	Language string
	Title    string
	Forced   bool
	Default  bool
	Codec    string

	// Embedded is whether it lives inside the container.
	Embedded bool

	// Usable is whether a browser can be handed this at all.
	Usable bool
	// Why explains an unusable track, in words rather than a code.
	Why string

	// index and file are how to reach it. Unexported: a caller selects by ID,
	// and nothing outside this package should be assembling either.
	index int
	file  string
}

// bitmapFormatNames are what people call these formats.
//
// Worth a table rather than deriving one from the codec name: ffmpeg calls PGS
// "hdmv_pgs_subtitle", and mechanically trimming that gives "HDMV_PGS", which
// is not what anybody says or searches for.
var bitmapFormatNames = map[string]string{
	"hdmv_pgs_subtitle": "PGS",
	"dvd_subtitle":      "VobSub",
	"dvb_subtitle":      "DVB",
	"xsub":              "XSUB",
}

func subtitleFormatName(codec string) string {
	if name, ok := bitmapFormatNames[codec]; ok {
		return name
	}
	return strings.ToUpper(strings.TrimSuffix(codec, "_subtitle"))
}

// Subtitles finds and converts them.
type Subtitles struct {
	streamer *Streamer
	sandbox  *Sandbox

	// cacheDir, when set, holds each file's embedded text tracks, read out
	// in one pass (ADR-0074). A track inside a film is spread through the
	// whole file, so taking one out reads all of it: minutes for a UHD disc,
	// far past SubtitleTimeout. Read once, every track is then a small file.
	cacheDir string
	mu       sync.Mutex
	reading  map[string]*subtitleRead
}

// subtitleRead is one file's tracks being read out; err is set before done
// closes.
type subtitleRead struct {
	done chan struct{}
	err  error
}

// NewSubtitles builds it.
func NewSubtitles(s *Streamer, sandbox *Sandbox) *Subtitles {
	return &Subtitles{streamer: s, sandbox: sandbox, reading: map[string]*subtitleRead{}}
}

// List reports every subtitle available for a file: the text tracks inside it,
// the unusable ones with a reason, and any sidecars beside it.
func (s *Subtitles) List(ctx context.Context, fileID int64, p Probe) ([]SubtitleTrack, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, err
	}

	out := make([]SubtitleTrack, 0, len(p.Subtitles))
	for _, sub := range p.Subtitles {
		t := SubtitleTrack{
			ID:       "e" + strconv.Itoa(sub.Index),
			Language: sub.Language, Title: sub.Title,
			Forced: sub.Forced, Default: sub.Default,
			Codec: sub.Codec, Embedded: true,
			Usable: sub.Text,
			index:  sub.Index,
		}
		if !sub.Text {
			t.Why = fmt.Sprintf("%s subtitles are pictures of text rather than "+
				"text, so there is nothing to hand a browser. Showing them means "+
				"drawing them onto the video, which this server does not do",
				subtitleFormatName(sub.Codec))
		}
		out = append(out, t)
	}

	sidecars, err := s.sidecars(ctx, fileID)
	if err != nil {
		// Not fatal. A directory that cannot be listed costs the sidecars and
		// nothing else; the embedded tracks are still worth returning.
		return out, nil //nolint:nilerr // the embedded tracks are the answer; sidecars are extra
	}
	out = append(out, sidecars...)
	return out, nil
}

// sidecars finds subtitle files beside the video.
//
// Through the vault's fs.FS, so the listing is confined to the library the same
// way every other read is. The names come from the directory rather than from a
// caller, which is what keeps a request from naming a file of its own.
func (s *Subtitles) sidecars(ctx context.Context, fileID int64) ([]SubtitleTrack, error) {
	ref, err := s.streamer.files.FileForPlayback(ctx, fileID)
	if err != nil {
		return nil, err
	}
	vault, err := s.streamer.vaults.OpenVault(ctx, ref.RootFolderID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = vault.Close() }()

	dir := path.Dir(ref.RelativePath)
	if dir == "." {
		dir = ""
	}
	listDir := dir
	if listDir == "" {
		listDir = "."
	}
	entries, err := fs.ReadDir(vault.FS(), listDir)
	if err != nil {
		return nil, err
	}

	// The importer names a sidecar <video stem>[.<lang>].<ext> in the video's
	// own directory, so that prefix is what identifies one as belonging to this
	// film rather than to another in the same folder.
	base := path.Base(ref.RelativePath)
	stem := strings.TrimSuffix(base, path.Ext(base))

	var out []SubtitleTrack
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(path.Ext(name))
		if !subtitleExtensions[ext] {
			continue
		}
		if !strings.HasPrefix(name, stem+".") {
			continue
		}
		// What sits between the stem and the extension, when anything does:
		// "Film.en.srt" -> "en".
		middle := strings.TrimSuffix(strings.TrimPrefix(name, stem+"."), strings.TrimPrefix(ext, "."))
		lang := strings.Trim(middle, ".")

		out = append(out, SubtitleTrack{
			ID:       "s" + strconv.Itoa(len(out)),
			Language: normaliseLanguage(lang),
			Title:    "",
			Codec:    strings.TrimPrefix(ext, "."),
			Embedded: false,
			Usable:   true,
			file:     path.Join(dir, name),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].file < out[j].file })
	// Re-issue ids after sorting, so an id always matches its position in the
	// list a client was given.
	for i := range out {
		out[i].ID = "s" + strconv.Itoa(i)
	}
	return out, nil
}

// Serve converts one subtitle to WebVTT and writes it.
//
// The id is resolved against a freshly built list, so it can only name a track
// this file actually has. Nothing in the request becomes a path or a stream
// index; both come from the server's own listing.
func (s *Subtitles) Serve(w http.ResponseWriter, r *http.Request, fileID int64,
	p Probe, id string) error {

	ctx := r.Context()
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return err
	}

	tracks, err := s.List(ctx, fileID, p)
	if err != nil {
		return err
	}
	var chosen *SubtitleTrack
	for i := range tracks {
		if tracks[i].ID == id {
			chosen = &tracks[i]
			break
		}
	}
	if chosen == nil {
		return ErrNoSuchSubtitle
	}
	if !chosen.Usable {
		return fmt.Errorf("%w: %s", ErrNoSuchSubtitle, chosen.Why)
	}

	// An embedded track comes from the cache when there is one (ADR-0074).
	if chosen.Embedded && s.cacheDir != "" {
		dest, rd := s.prepare(ctx, fileID, p, tracks)
		if rd != nil {
			select {
			case <-rd.done:
				if rd.err != nil {
					return rd.err
				}
			case <-time.After(subtitleWait):
				return ErrSubtitlesPreparing
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		// #nosec G304 -- dest is built from the file's id and size, not a request.
		cached, err := os.Open(dest)
		if err != nil {
			return err
		}
		defer func() { _ = cached.Close() }()
		body, err := s.toWebVTT(ctx, cached, slices.Index(embeddedText(tracks), chosen.index))
		if err != nil {
			return err
		}
		return writeVTT(w, body)
	}

	// Which file to open, and which stream to take from it.
	openPath := chosen.file
	if chosen.Embedded {
		ref, err := s.streamer.files.FileForPlayback(ctx, fileID)
		if err != nil {
			return err
		}
		openPath = ref.RelativePath
	}

	f, err := s.openInVault(ctx, fileID, openPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	// A sidecar IS the subtitle, so there is no stream to select within it.
	streamIndex := -1
	if chosen.Embedded {
		streamIndex = chosen.index
	}
	body, err := s.toWebVTT(ctx, f, streamIndex)
	if err != nil {
		return err
	}
	return writeVTT(w, body)
}

func writeVTT(w http.ResponseWriter, body []byte) error {
	// text/vtt, and nosniff is already set globally. A subtitle is text chosen
	// by a stranger, and the reason it is safe is that a <track> element hands
	// it to the browser's WebVTT parser rather than to its HTML parser. Serving
	// it as anything else would be the mistake.
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "private, max-age=0, no-store")
	_, _ = w.Write(body)
	return nil
}

// openInVault opens a path inside the file's library location.
func (s *Subtitles) openInVault(ctx context.Context, fileID int64, rel string) (*os.File, error) {
	ref, err := s.streamer.files.FileForPlayback(ctx, fileID)
	if err != nil {
		return nil, err
	}
	vault, err := s.streamer.vaults.OpenVault(ctx, ref.RootFolderID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = vault.Close() }()
	return vault.Open(rel)
}

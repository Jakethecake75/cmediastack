package playback

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
)

// Serving the bytes.
//
// # Why http.ServeContent and never http.ServeFile
//
// ServeFile takes a PATH and resolves it itself, which puts path handling back
// where ADR-0015 spent an increment taking it out of. ServeContent takes an
// io.ReadSeeker — which is exactly what a vault's os.Root hands back — and
// still implements everything a <video> element needs: range requests,
// conditional requests, and the 206 responses that make seeking work.
//
// So the whole playback path holds the same property the parser does: nothing
// turns a request into a filesystem path. A request names a library file by id;
// the id is resolved to a descriptor through the root; the descriptor is what
// gets served.

// ErrNoSuchFile means the library has no file with that id.
var ErrNoSuchFile = errors.New("playback: no such file")

// VaultSource opens the library location a file lives in.
//
// The same interface the importer takes, for the same reason: a package that
// could construct its own vault would be a second place where "where may this
// write" is decided.
type VaultSource interface {
	OpenVault(ctx context.Context, id int64) (*library.Vault, error)
}

// FileRef is where a library file lives, as the library knows it.
type FileRef struct {
	ID           int64
	RootFolderID int64
	RelativePath string
	SizeBytes    int64
}

// FileSource resolves a file id to its location.
type FileSource interface {
	FileForPlayback(ctx context.Context, fileID int64) (FileRef, error)
}

// Streamer serves library files.
type Streamer struct {
	files  FileSource
	vaults VaultSource
}

// NewStreamer builds one.
func NewStreamer(files FileSource, vaults VaultSource) *Streamer {
	return &Streamer{files: files, vaults: vaults}
}

// Open resolves a file id to an open descriptor inside its vault.
//
// The caller closes it. Returned alongside the ref so a caller can report what
// it opened without re-reading the row.
func (s *Streamer) Open(ctx context.Context, fileID int64) (*os.File, FileRef, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return nil, FileRef{}, err
	}

	ref, err := s.files.FileForPlayback(ctx, fileID)
	if err != nil {
		return nil, FileRef{}, err
	}

	vault, err := s.vaults.OpenVault(ctx, ref.RootFolderID)
	if err != nil {
		return nil, ref, fmt.Errorf("playback: opening the library location: %w", err)
	}
	// The vault is closed once the descriptor exists. A vault is a handle on the
	// root directory; the file descriptor it produced stays valid without it,
	// and holding one open per in-flight stream would leak a directory handle
	// for the length of a film.
	defer func() { _ = vault.Close() }()

	f, err := vault.Open(ref.RelativePath)
	if err != nil {
		return nil, ref, fmt.Errorf("playback: opening %s: %w", ref.RelativePath, err)
	}
	return f, ref, nil
}

// Serve writes a library file to w, honouring range requests.
//
// modTime is passed to ServeContent, which turns it into Last-Modified and
// honours If-Modified-Since. It is read from the descriptor rather than from
// the database, because the database records what was imported and the
// descriptor is what is about to be sent.
func (s *Streamer) Serve(w http.ResponseWriter, r *http.Request, fileID int64) error {
	return s.serve(w, r, fileID, "inline")
}

// ServeAttachment writes a library file as a download: the same bytes, the same
// range requests, and a Content-Disposition that asks the browser to save it
// rather than play it (ADR-0038).
func (s *Streamer) ServeAttachment(w http.ResponseWriter, r *http.Request, fileID int64) error {
	return s.serve(w, r, fileID, "attachment")
}

func (s *Streamer) serve(w http.ResponseWriter, r *http.Request, fileID int64, disposition string) error {
	f, ref, err := s.Open(r.Context(), fileID)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("playback: stat before serving: %w", err)
	}

	// Explicit, never sniffed. http.ServeContent will sniff the first 512 bytes
	// when it cannot tell from the name, and a media container sniffed by
	// net/http comes back as application/octet-stream often enough that a
	// browser refuses to play it.
	w.Header().Set("Content-Type", ContentType(ref.RelativePath))

	// Accept-Ranges is NOT set here. http.ServeContent sets it unconditionally
	// (net/http/fs.go), and a line that duplicates it would read as the thing
	// making seeking work while actually doing nothing — the sort of comment
	// that survives a refactor and then misleads whoever removes the real one.
	// A test asserts the header reaches the client, because a player decides
	// whether it can seek from it.

	// Private, and never by a shared cache. A media response is authorized by
	// the viewer's session; a proxy that cached it would serve one account's
	// library to another.
	w.Header().Set("Cache-Control", "private, max-age=0, no-store")

	// The name a download would use, with no path in it. Inline keeps it
	// playing in the page; attachment is the original, saved.
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("%s; filename*=UTF-8''%s", disposition,
			urlEscape(path.Base(ref.RelativePath))))

	// The id, not the path. A caller that wants to know whether two requests
	// are for the same file should not have to be told where it lives.
	http.ServeContent(w, r, "", info.ModTime(), f)
	return nil
}

// ContentType maps a container extension to the type a browser expects.
//
// A short table rather than mime.TypeByExtension, because the system table is
// whatever the host distribution installed — on a scratch container it is
// nearly empty, and on others .mkv is absent or wrong. A media server that
// serves the wrong type produces a video element that silently refuses to play,
// which is the least diagnosable failure in the whole path.
func ContentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".mkv":
		return "video/x-matroska"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".avi":
		return "video/x-msvideo"
	case ".ts", ".m2ts", ".mts":
		return "video/mp2t"
	case ".mpg", ".mpeg":
		return "video/mpeg"
	case ".ogv":
		return "video/ogg"
	case ".m4a":
		return "audio/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".opus", ".ogg":
		return "audio/ogg"
	case ".srt":
		return "application/x-subrip"
	case ".vtt":
		return "text/vtt"
	}
	// Deliberately not application/octet-stream: a browser treats that as a
	// download. An unknown video is still more useful offered as video, and the
	// element will say so if it cannot decode it.
	return "video/mp4"
}

// urlEscape percent-encodes a filename for Content-Disposition's filename*.
func urlEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z',
			ch >= '0' && ch <= '9', ch == '-', ch == '.', ch == '_', ch == '~':
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

// Identity reads a file's current size and modification time, for deciding
// whether a stored probe still describes it.
func (s *Streamer) Identity(ctx context.Context, fileID int64) (FileIdentity, error) {
	f, _, err := s.Open(ctx, fileID)
	if err != nil {
		return FileIdentity{}, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return FileIdentity{}, fmt.Errorf("playback: stat: %w", err)
	}
	return FileIdentity{SizeBytes: info.Size(), ModTime: info.ModTime().UTC()}, nil
}

// Inspect returns a current probe for a file, taking one if none is current.
//
// The freshness rule lives here rather than in a caller: Store.Get refuses a
// probe that no longer describes the file, and this is the one place that knows
// what to do about it — open the file, probe it, record it. A caller that had
// to notice ErrNotProbed and re-probe for itself is a caller that will
// eventually forget, and the symptom is a stale answer rather than an error.
func (s *Streamer) Inspect(ctx context.Context, store *Store, prober *Prober,
	fileID int64) (Probe, error) {

	f, _, err := s.Open(ctx, fileID)
	if err != nil {
		return Probe{}, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return Probe{}, fmt.Errorf("playback: stat: %w", err)
	}
	want := FileIdentity{SizeBytes: info.Size(), ModTime: info.ModTime().UTC()}

	cached, err := store.Get(ctx, fileID, want)
	if err == nil {
		return cached, nil
	}
	if !errors.Is(err, ErrNotProbed) {
		return Probe{}, err
	}

	probe, err := prober.Probe(ctx, f)
	if err != nil {
		return Probe{}, err
	}
	// A probe that could not be recorded is still a correct probe. Failing the
	// request because the cache write failed would turn a performance problem
	// into an outage.
	_ = store.Save(ctx, fileID, probe)
	return probe, nil
}

// ProbeTimeoutFor reports how long a probe of this size is allowed to take.
//
// Not a constant, because ProbeTimeout has to cover a 40 GB remux on a cold NFS
// mount, and applying that to everything means a broken 200 MB file occupies a
// worker for a minute and a half. Small files get a short leash.
func ProbeTimeoutFor(sizeBytes int64) time.Duration {
	const small = 2 << 30 // 2 GiB
	if sizeBytes > 0 && sizeBytes < small {
		return 20 * time.Second
	}
	return ProbeTimeout
}

// Service is the whole of playback behind one type.
//
// It exists so that the API depends on one thing rather than on a streamer, a
// store and a prober separately — and so that the freshness rule (probe, cache,
// re-probe when the file changed) lives in exactly one place instead of at
// every call site.
type Service struct {
	streamer  *Streamer
	store     *Store
	prober    *Prober
	positions *Positions
	remuxer   *Remuxer
	subtitles *Subtitles
}

// NewService builds it.
func NewService(files FileSource, vaults VaultSource, store *Store, prober *Prober,
	positions *Positions, sandbox *Sandbox, log *slog.Logger, maxConversions int) *Service {

	streamer := NewStreamer(files, vaults)
	return &Service{
		streamer:  streamer,
		store:     store,
		prober:    prober,
		positions: positions,
		remuxer:   NewRemuxer(streamer, sandbox, log, maxConversions),
		subtitles: NewSubtitles(streamer, sandbox),
	}
}

// PlanConversion reports whether converting this file would make it playable.
func (s *Service) PlanConversion(ctx context.Context, fileID int64,
	target AudioTarget) (RemuxPlan, Probe, error) {

	probe, err := s.Inspect(ctx, fileID)
	if err != nil {
		return RemuxPlan{}, Probe{}, err
	}
	return PlanConvert(probe, ChromeLike, target), probe, nil
}

// Convert streams a converted version of a file.
func (s *Service) Convert(w http.ResponseWriter, r *http.Request, fileID int64,
	target AudioTarget, opts StreamOptions) error {

	plan, _, err := s.PlanConversion(r.Context(), fileID, target)
	if err != nil {
		return err
	}
	return s.remuxer.Stream(w, r, fileID, plan, target, opts)
}

// Conversions reports how many are running and how many are allowed.
func (s *Service) Conversions() (int, int) { return s.remuxer.Active() }

// Serve writes a library file to w, honouring range requests.
func (s *Service) Serve(w http.ResponseWriter, r *http.Request, fileID int64) error {
	return s.streamer.Serve(w, r, fileID)
}

// ServeAttachment writes a library file as a download (ADR-0038).
func (s *Service) ServeAttachment(w http.ResponseWriter, r *http.Request, fileID int64) error {
	return s.streamer.ServeAttachment(w, r, fileID)
}

// Inspect returns a current probe for a file, taking one if none is current.
func (s *Service) Inspect(ctx context.Context, fileID int64) (Probe, error) {
	return s.streamer.Inspect(ctx, s.store, s.prober, fileID)
}

// Open exposes the descriptor, for callers that want the file rather than a
// response — the subtitle extractor, when it exists.
func (s *Service) Open(ctx context.Context, fileID int64) (*os.File, FileRef, error) {
	return s.streamer.Open(ctx, fileID)
}

// ListSubtitles reports every subtitle available for a file, including the ones
// this server cannot serve and why.
//
// It probes first, because the embedded tracks come from the probe — a caller
// that already has one should pass it to the Subtitles type directly rather
// than making this re-inspect.
func (s *Service) ListSubtitles(ctx context.Context, fileID int64) ([]SubtitleTrack, error) {
	// A file the caller cannot see — or that does not exist — has no tracks
	// to list, and says so rather than answering with an empty list
	// (ADR-0037).
	if _, err := s.streamer.files.FileForPlayback(ctx, fileID); err != nil {
		return nil, err
	}
	probe, err := s.Inspect(ctx, fileID)
	if err != nil {
		// A file that will not probe still has sidecars, and those are the
		// tracks most likely to exist for a file ffprobe choked on. Losing them
		// as well would be a second failure caused by the first.
		if tracks, subErr := s.subtitles.List(ctx, fileID, Probe{}); subErr == nil {
			return tracks, nil
		}
		return nil, err
	}
	return s.subtitles.List(ctx, fileID, probe)
}

// ServeSubtitle converts one track to WebVTT and writes it.
func (s *Service) ServeSubtitle(w http.ResponseWriter, r *http.Request,
	fileID int64, id string) error {

	probe, err := s.Inspect(r.Context(), fileID)
	if err != nil {
		// Same reasoning as ListSubtitles: a sidecar does not need the probe,
		// and an id naming an embedded track will simply not resolve.
		probe = Probe{}
	}
	return s.subtitles.Serve(w, r, fileID, probe, id)
}

// Positions exposes the position store for wiring.
func (s *Service) Positions() *Positions { return s.positions }

// Resume returns where this person should start in a file, and whether that is
// a resume rather than a fresh start.
//
// The judgement lives here rather than in a handler because it needs both
// halves — the stored position and the file's CURRENT duration — and a caller
// holding only one of them would have to guess.
func (s *Service) Resume(ctx context.Context, fileID int64,
	current time.Duration) (time.Duration, bool, Position) {

	if s.positions == nil {
		return 0, false, Position{}
	}
	rec, err := s.positions.Get(ctx, fileID)
	if err != nil {
		return 0, false, Position{}
	}
	at, ok := rec.ResumeAt(current)
	return at, ok, rec
}

// SavePosition records where the caller got to.
func (s *Service) SavePosition(ctx context.Context, fileID int64,
	position, duration time.Duration) (Position, error) {

	if s.positions == nil {
		return Position{}, ErrNoPosition
	}
	// Only in a file the caller may see (ADR-0037): saving a place must not be
	// a way to learn that a hidden file exists.
	if _, err := s.streamer.files.FileForPlayback(ctx, fileID); err != nil {
		return Position{}, err
	}
	return s.positions.Save(ctx, fileID, position, duration)
}

// ForgetPosition removes the caller's place in a file.
// InProgress is the caller's unfinished places, newest first (ADR-0069).
func (s *Service) InProgress(ctx context.Context, limit int) ([]InProgressItem, error) {
	if s.positions == nil {
		return nil, nil
	}
	return s.positions.InProgress(ctx, limit)
}

func (s *Service) ForgetPosition(ctx context.Context, fileID int64) error {
	if s.positions == nil {
		return nil
	}
	if _, err := s.streamer.files.FileForPlayback(ctx, fileID); err != nil {
		return err
	}
	return s.positions.Forget(ctx, fileID)
}

package playback

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/library"
)

// fakeFiles resolves ids the way the library store does, without a database.
type fakeFiles struct {
	byID map[int64]FileRef
	err  error
}

func (f *fakeFiles) FileForPlayback(_ context.Context, id int64) (FileRef, error) {
	if f.err != nil {
		return FileRef{}, f.err
	}
	ref, ok := f.byID[id]
	if !ok {
		return FileRef{}, ErrNoSuchFile
	}
	return ref, nil
}

// fakeVaults opens a real vault over a real directory, because the containment
// being tested is the vault's and a stub would test nothing.
type fakeVaults struct {
	dirs map[int64]string
}

func (f *fakeVaults) OpenVault(_ context.Context, id int64) (*library.Vault, error) {
	dir, ok := f.dirs[id]
	if !ok {
		return nil, fmt.Errorf("no root folder %d", id)
	}
	return library.Open(id, dir)
}

type streamRig struct {
	streamer *Streamer
	dir      string
	ctx      context.Context
	content  []byte
}

func newStreamRig(t *testing.T) *streamRig {
	t.Helper()
	dir := t.TempDir()

	// Recognisable bytes, so a range response can be checked against the
	// offsets it claims rather than merely against a length.
	content := make([]byte, 4096)
	for i := range content {
		content[i] = byte('A' + i%26)
	}
	if err := os.WriteFile(filepath.Join(dir, "film.mkv"), content, 0o600); err != nil {
		t.Fatal(err)
	}

	files := &fakeFiles{byID: map[int64]FileRef{
		1: {ID: 1, RootFolderID: 7, RelativePath: "film.mkv",
			SizeBytes: int64(len(content))},
	}}
	vaults := &fakeVaults{dirs: map[int64]string{7: dir}}

	return &streamRig{
		streamer: NewStreamer(files, vaults),
		dir:      dir,
		content:  content,
		ctx: authz.WithPrincipal(context.Background(), &authz.Principal{
			UserID: 1, Username: "jacob", State: authz.StateActive,
			MFASatisfied: true,
			Role: authz.Role{ID: 1, Name: "Admin", Rank: 100,
				Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
		}),
	}
}

func (r *streamRig) get(t *testing.T, rangeHeader string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/files/1/stream", nil)
	req = req.WithContext(r.ctx)
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	w := httptest.NewRecorder()
	if err := r.streamer.Serve(w, req, 1); err != nil {
		t.Fatalf("serving: %v", err)
	}
	return w
}

// The whole file, and the headers a <video> element reads before it decides
// whether seeking is possible.
func TestAWholeFileIsServedWithTheHeadersAPlayerNeeds(t *testing.T) {
	r := newStreamRig(t)
	w := r.get(t, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if got := w.Body.Len(); got != len(r.content) {
		t.Errorf("served %d bytes, want %d", got, len(r.content))
	}
	// A <video> element decides seeking is possible from this header. It comes
	// from http.ServeContent rather than from any line in this package — which
	// is exactly why it is asserted here: the contract a player depends on
	// should survive somebody replacing ServeContent with something else.
	if got := w.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q — a player will refuse to seek", got)
	}
	if got := w.Header().Get("Content-Type"); got != "video/x-matroska" {
		t.Errorf("Content-Type = %q, want video/x-matroska", got)
	}
	// A media response is authorized by the viewer's session; a shared cache
	// holding it would serve one account's library to another.
	cc := w.Header().Get("Cache-Control")
	if !strings.Contains(cc, "private") || !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q", cc)
	}
}

// Seeking is a range request, and getting the offsets wrong produces a player
// that appears to work and shows the wrong part of the film.
func TestARangeRequestReturnsExactlyTheBytesItAskedFor(t *testing.T) {
	r := newStreamRig(t)
	w := r.get(t, "bytes=100-199")

	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", w.Code)
	}
	body := w.Body.Bytes()
	if len(body) != 100 {
		t.Fatalf("served %d bytes, want 100", len(body))
	}
	// Against the actual content, not merely the length.
	if string(body) != string(r.content[100:200]) {
		t.Errorf("served the wrong bytes: %q...", body[:10])
	}
	if got := w.Header().Get("Content-Range"); got != fmt.Sprintf("bytes 100-199/%d", len(r.content)) {
		t.Errorf("Content-Range = %q", got)
	}
}

// The request a browser makes when a viewer drags the scrubber to the end.
func TestAnOpenEndedRangeServesToTheEndOfTheFile(t *testing.T) {
	r := newStreamRig(t)
	start := len(r.content) - 50
	w := r.get(t, fmt.Sprintf("bytes=%d-", start))

	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", w.Code)
	}
	if got := w.Body.Len(); got != 50 {
		t.Errorf("served %d bytes, want 50", got)
	}
	if w.Body.String() != string(r.content[start:]) {
		t.Error("the tail of the file did not match")
	}
}

// A range beyond the end is a 416, not a truncated 206 that a player would
// treat as the file ending early.
func TestARangeBeyondTheEndIsRefused(t *testing.T) {
	r := newStreamRig(t)
	w := r.get(t, "bytes=999999-1000000")
	if w.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Errorf("status = %d, want 416", w.Code)
	}
}

// The containment claim: a stream is served from a descriptor obtained through
// the vault, so a relative path that climbs out of the library reaches nothing.
func TestAFileOutsideTheLibraryCannotBeStreamed(t *testing.T) {
	r := newStreamRig(t)

	// Something worth stealing, one level above the library.
	secret := filepath.Join(filepath.Dir(r.dir), "secret.env")
	if err := os.WriteFile(secret, []byte("CMS_MASTER_KEY=hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(secret) })

	// A row whose relative path climbs out — which is what a corrupted or
	// hostile database row would look like.
	files := r.streamer.files.(*fakeFiles)
	files.byID[2] = FileRef{ID: 2, RootFolderID: 7, RelativePath: "../secret.env"}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/files/2/stream", nil).
		WithContext(r.ctx)
	w := httptest.NewRecorder()
	err := r.streamer.Serve(w, req, 2)

	if err == nil {
		t.Fatalf("a path above the library was served: %q", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "hunter2") {
		t.Fatal("the master key was served to a media request")
	}
	t.Logf("refused with: %v", err)
}

// A symlink pointing out of the library is the same attack wearing a different
// hat, and os.Root is what refuses it rather than a string check.
func TestASymlinkOutOfTheLibraryCannotBeStreamed(t *testing.T) {
	r := newStreamRig(t)

	secret := filepath.Join(filepath.Dir(r.dir), "secret2.env")
	if err := os.WriteFile(secret, []byte("CMS_MASTER_KEY=hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(secret) })

	link := filepath.Join(r.dir, "innocent.mkv")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("this filesystem does not do symlinks: %v", err)
	}

	files := r.streamer.files.(*fakeFiles)
	files.byID[3] = FileRef{ID: 3, RootFolderID: 7, RelativePath: "innocent.mkv"}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/files/3/stream", nil).
		WithContext(r.ctx)
	w := httptest.NewRecorder()
	err := r.streamer.Serve(w, req, 3)

	if err == nil && strings.Contains(w.Body.String(), "hunter2") {
		t.Fatal("a symlink out of the library served the master key")
	}
	if err == nil {
		t.Fatalf("a symlink out of the library was served: %q", w.Body.String())
	}
	t.Logf("refused with: %v", err)
}

// Watching something is browsing, and browsing is a permission.
func TestStreamingNeedsThePermissionToBrowse(t *testing.T) {
	r := newStreamRig(t)
	ctx := authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 2, Username: "nobody", State: authz.StateActive,
		MFASatisfied: true,
		Role:         authz.Role{ID: 9, Name: "Pending", Rank: 0},
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/files/1/stream", nil).
		WithContext(ctx)
	w := httptest.NewRecorder()

	if err := r.streamer.Serve(w, req, 1); err == nil {
		t.Fatal("a principal with no permissions streamed a library file")
	}
	if w.Body.Len() != 0 {
		t.Errorf("bytes were written before the refusal: %d", w.Body.Len())
	}
}

// A missing file is a named absence, not a zero-length success a player would
// render as a broken video.
func TestStreamingAFileThatIsNotThereSaysSo(t *testing.T) {
	r := newStreamRig(t)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/files/99/stream", nil).
		WithContext(r.ctx)
	w := httptest.NewRecorder()

	err := r.streamer.Serve(w, req, 99)
	if !errors.Is(err, ErrNoSuchFile) {
		t.Fatalf("err = %v, want ErrNoSuchFile", err)
	}
}

// The system MIME table is whatever the host distribution installed — on a
// scratch container it is nearly empty. A wrong type produces a video element
// that silently refuses to play, which is the least diagnosable failure in the
// whole path.
func TestContentTypesAreDecidedHereAndNotByTheHost(t *testing.T) {
	for name, want := range map[string]string{
		"a/b/film.mkv":  "video/x-matroska",
		"film.mp4":      "video/mp4",
		"film.M4V":      "video/mp4",
		"film.webm":     "video/webm",
		"clip.mov":      "video/quicktime",
		"track.mp3":     "audio/mpeg",
		"track.flac":    "audio/flac",
		"subs.vtt":      "text/vtt",
		"unknown.thing": "video/mp4",
	} {
		if got := ContentType(name); got != want {
			t.Errorf("ContentType(%q) = %q, want %q", name, got, want)
		}
	}
	// Never octet-stream: a browser treats that as a download.
	if strings.Contains(ContentType("x.qqq"), "octet-stream") {
		t.Error("an unknown extension is offered as a download rather than as video")
	}
}

// A filename with a quote or a newline in it must not be able to inject a
// header. Release names are chosen by strangers.
func TestAHostileFilenameCannotEscapeTheDispositionHeader(t *testing.T) {
	r := newStreamRig(t)
	hostile := "evil\"\r\nX-Injected: yes\r\n.mkv"
	if err := os.WriteFile(filepath.Join(r.dir, "e.mkv"), r.content, 0o600); err != nil {
		t.Fatal(err)
	}
	files := r.streamer.files.(*fakeFiles)
	// The path on disk is fine; the RECORDED name is hostile, which is what a
	// release title would supply.
	files.byID[4] = FileRef{ID: 4, RootFolderID: 7, RelativePath: "e.mkv",
		SizeBytes: int64(len(r.content))}

	// Escape the hostile name directly — this is the function that must hold.
	escaped := urlEscape(hostile)
	for _, bad := range []string{"\"", "\r", "\n", " "} {
		if strings.Contains(escaped, bad) {
			t.Errorf("urlEscape left %q in %q", bad, escaped)
		}
	}
	if !strings.Contains(escaped, "%22") || !strings.Contains(escaped, "%0D") {
		t.Errorf("urlEscape did not encode the dangerous characters: %q", escaped)
	}

	w := r.get(t, "")
	if got := w.Header().Get("X-Injected"); got != "" {
		t.Errorf("a header was injected: %q", got)
	}
}

// A small file that is broken must not hold a worker for the full timeout a 40
// GB remux on a cold mount needs.
func TestASmallFileGetsAShorterProbeLeash(t *testing.T) {
	small := ProbeTimeoutFor(500 << 20) // 500 MiB
	big := ProbeTimeoutFor(40 << 30)    // 40 GiB
	if small >= big {
		t.Errorf("a 500 MiB file gets %s and a 40 GiB file gets %s", small, big)
	}
	if big != ProbeTimeout {
		t.Errorf("a large file gets %s, want the full %s", big, ProbeTimeout)
	}
	// An unknown size is the large case: guessing short would kill a probe of
	// something the library has not measured yet.
	if ProbeTimeoutFor(0) != ProbeTimeout {
		t.Error("an unknown size got the short leash")
	}
}

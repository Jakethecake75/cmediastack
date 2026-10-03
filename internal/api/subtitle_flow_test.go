package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
	"github.com/jakethecake75/cmediastack/internal/subtitles"
)

// fakeSubtitles is OpenSubtitles answering with whatever results it holds.
type fakeSubtitles struct {
	mu      sync.Mutex
	results []subtitles.Result
	keys    []string
}

func (f *fakeSubtitles) Search(_ context.Context, cred subtitles.Credentials, _ subtitles.Query) ([]subtitles.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, cred.APIKey)
	return f.results, nil
}
func (f *fakeSubtitles) Download(context.Context, subtitles.Credentials, int64) (subtitles.Download, error) {
	return subtitles.Download{Link: "https://dl.example/x.srt", Remaining: 3}, nil
}
func (f *fakeSubtitles) Fetch(context.Context, string) ([]byte, error) {
	return []byte("1\n00:00:01,000 --> 00:00:02,000\nMarmalade.\n"), nil
}
func (f *fakeSubtitles) Forget() {}

func subtitleCipher(t *testing.T) *secrets.Cipher {
	t.Helper()
	k, _ := secrets.GenerateKey()
	c, err := secrets.NewCipherFromBase64(k)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// ADR-0055: OpenSubtitles configured by an administrator, never shown back;
// the languages read by whoever may fetch; a file's subtitle fetched beside it.
func TestASubtitleIsFetchedThroughTheRouter(t *testing.T) {
	w := newScopeWorld(t)
	if res := w.admin.get("/api/v1/admin/subtitles"); res.Code != http.StatusOK || res.Body["configured"] != false {
		t.Fatalf("status: %d %s", res.Code, res.Raw)
	}
	file := "/api/v1/files/" + strconv.FormatInt(w.shownFile, 10) + "/subtitles/fetch"
	if res := w.kid.post(file, map[string]any{"language": "en"}); res.Code != http.StatusConflict ||
		!strings.Contains(res.Raw, "not configured") {
		t.Errorf("before a key: %d %s", res.Code, res.Raw)
	}
	res := w.admin.do(http.MethodPut, "/api/v1/admin/subtitles", map[string]any{
		"api_key": "a-secret-key", "username": "me", "password": "a-secret-password", "languages": []string{"en", "fr"}})
	if res.Code != http.StatusOK || res.Body["configured"] != true || strings.Contains(res.Raw, "a-secret") {
		t.Fatalf("configure: %d %s", res.Code, res.Raw)
	}
	if res := w.admin.do(http.MethodPut, "/api/v1/admin/subtitles", map[string]any{"languages": []string{"english"}}); res.Code != http.StatusBadRequest {
		t.Errorf("a language that is not a code: %d", res.Code)
	}
	if res := w.kid.get("/api/v1/admin/subtitles"); res.Code != http.StatusNotFound {
		t.Errorf("a Manager read the configuration: %d", res.Code)
	}
	if res := w.kid.get("/api/v1/subtitles/languages"); res.Code != http.StatusOK || !strings.Contains(res.Raw, `"fr"`) {
		t.Errorf("languages: %d %s", res.Code, res.Raw)
	}

	w.subtitleProvider.results = []subtitles.Result{{FileID: 9, Language: "en", TMDBID: w.shownTMDB, Release: "Paddington.2014"}}
	res = w.kid.post(file, map[string]any{"language": "en"})
	if res.Code != http.StatusOK || res.Body["path"] != "Paddington/Paddington.en.srt" {
		t.Fatalf("fetch: %d %s", res.Code, res.Raw)
	}
	if b, err := os.ReadFile(filepath.Join(w.dirs[w.kids], "Paddington", "Paddington.en.srt")); err != nil ||
		!strings.Contains(string(b), "Marmalade") {
		t.Errorf("the sidecar: %q %v", b, err)
	}
	if w.subtitleProvider.keys[0] != "a-secret-key" {
		t.Errorf("searched with %q", w.subtitleProvider.keys[0])
	}
	if res := w.kid.post(file, map[string]any{"language": "en"}); res.Code != http.StatusConflict {
		t.Errorf("a second time: %d %s", res.Code, res.Raw)
	}
	viewer, _ := w.scopedAccount("viewer", authz.RoleUser, []int64{w.kids}, 0)
	if res := viewer.post(file, map[string]any{"language": "fr"}); res.Code != http.StatusForbidden {
		t.Errorf("a User fetched a subtitle: %d", res.Code)
	}
}

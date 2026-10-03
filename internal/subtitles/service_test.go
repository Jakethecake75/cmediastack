package subtitles

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

type fakeProvider struct {
	results []Result
	queries []Query
	creds   []Credentials
	body    string
	err     error
	forgot  int
}

func (f *fakeProvider) Search(_ context.Context, cred Credentials, q Query) ([]Result, error) {
	f.queries = append(f.queries, q)
	f.creds = append(f.creds, cred)
	return f.results, f.err
}
func (f *fakeProvider) Download(context.Context, Credentials, int64) (Download, error) {
	return Download{Link: "https://dl.example/x.srt", Remaining: 9}, nil
}
func (f *fakeProvider) Fetch(context.Context, string) ([]byte, error) { return []byte(f.body), nil }
func (f *fakeProvider) Forget()                                       { f.forgot++ }

type rig struct {
	db       *db.DB
	svc      *Service
	provider *fakeProvider
	dir      string
	root     int64
	film     int64
	ctx      context.Context
	settings *identity.Store
	cipher   *secrets.Cipher
}

func admin(ctx context.Context) context.Context {
	return authz.WithPrincipal(ctx, &authz.Principal{UserID: 1, Username: "jacob", State: authz.StateActive,
		MFASatisfied: true, Role: authz.Role{Name: "Admin", Rank: 100, Permissions: authz.NewPermissionSet(authz.AllPermissions...)}})
}

func newRig(t *testing.T) *rig {
	t.Helper()
	database, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "subs.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	key, _ := secrets.GenerateKey()
	cipher, _ := secrets.NewCipherFromBase64(key)
	r := &rig{db: database, ctx: admin(t.Context()), provider: &fakeProvider{
		body: "1\n00:00:01,000 --> 00:00:02,000\nHello.\n"}, cipher: cipher}
	r.dir = filepath.Join(t.TempDir(), "films")
	if err := os.MkdirAll(filepath.Join(r.dir, "Dune (2021)"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.dir, "Dune (2021)", "Dune.mkv"), make([]byte, 256<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	roots := library.NewRootStore(database, t.TempDir(), time.Now)
	rf, err := roots.Create(r.ctx, r.dir, library.KindMovies, "Films")
	if err != nil {
		t.Fatal(err)
	}
	r.root = rf.ID
	res, err := database.ExecContext(t.Context(), `INSERT INTO media_item (kind, title, year, sort_title, root_folder_id,
		folder, tmdb_id, added_at, updated_at) VALUES ('movie', 'Dune', 2021, 'dune', ?, 'Dune (2021)', 438631, 'x', 'x')`, rf.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.film, _ = res.LastInsertId()
	if _, err := database.ExecContext(t.Context(), `INSERT INTO media_file (id, item_id, root_folder_id, relative_path,
		size_bytes, imported_at) VALUES (7, ?, ?, 'Dune (2021)/Dune.mkv', 262144, 'x')`, r.film, rf.ID); err != nil {
		t.Fatal(err)
	}
	r.settings = identity.NewStore(database, cipher, identity.Argon2Params{}, time.Now)
	r.svc = NewService(r.settings, cipher, r.provider, importer.NewStore(database, time.Now), roots,
		audit.New(database, time.Now), nil)
	return r
}

func ptr(s string) *string { return &s }

// ADR-0055, decision 1: the key and the password sealed, never shown; the
// languages checked; an administrator's alone.
func TestOpenSubtitlesIsConfigured(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Fetch(r.ctx, 7, "en"); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("before a key: %v", err)
	}
	if _, err := r.svc.Configure(r.ctx, Config{Languages: []string{"en", "english"}}); !errors.Is(err, ErrBadLanguage) {
		t.Errorf("a language that is not a code: %v", err)
	}
	editor := authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 2, Username: "m", State: authz.StateActive,
		MFASatisfied: true, Role: authz.Role{Name: "Manager", Permissions: authz.NewPermissionSet(authz.PermEditLibraryItems, authz.PermBrowse)}})
	if _, err := r.svc.Configure(editor, Config{APIKey: ptr("k")}); !authz.IsDenied(err) {
		t.Errorf("a Manager configured OpenSubtitles: %v", err)
	}
	if v, _ := r.settings.Setting(t.Context(), SealedKey); v != "" {
		t.Error("a Manager's refused change was stored anyway")
	}
	st, err := r.svc.Configure(r.ctx, Config{APIKey: ptr("the-key"), Username: ptr("me"), Password: ptr("pw"),
		Languages: []string{"FR", "en", "fr"}})
	if err != nil || !st.HasKey || !st.HasPassword || st.Username != "me" || strings.Join(st.Languages, ",") != "en,fr" {
		t.Fatalf("configured: %+v %v", st, err)
	}
	stored, _ := r.settings.Setting(t.Context(), SealedKey)
	if strings.Contains(stored, "the-key") {
		t.Error("the key is stored in the clear")
	}
	raw, _ := base64.StdEncoding.DecodeString(stored)
	if k, err := r.cipher.DecryptString(raw, SealedKeyContext); err != nil || k != "the-key" {
		t.Errorf("the key does not open under its context: %q %v", k, err)
	}
	var detail string
	if err := r.db.QueryRowContext(t.Context(), `SELECT detail FROM audit_event WHERE target_id = 'subtitles'`).Scan(&detail); err != nil ||
		strings.Contains(detail, "the-key") || strings.Contains(detail, "pw") {
		t.Errorf("audit %q %v", detail, err)
	}
	if r.provider.forgot == 0 {
		t.Error("a new account did not drop the old sign-in")
	}
	// Removing the password; the key kept.
	if st, _ := r.svc.Configure(r.ctx, Config{Password: ptr("")}); st.HasPassword || !st.HasKey {
		t.Errorf("after removing the password: %+v", st)
	}
}

// ADR-0055, decisions 2 to 4: the file's subtitle found and written beside it,
// never over one; out of scope not found.
func TestASubtitleIsFetchedForAFile(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Configure(r.ctx, Config{APIKey: ptr("the-key"), Languages: []string{"en"}}); err != nil {
		t.Fatal(err)
	}
	r.provider.results = []Result{
		{FileID: 1, Language: "en", TMDBID: 438631, Release: "Dune.2021.WEB", Downloads: 5},
		{FileID: 2, Language: "en", TMDBID: 438631, Release: "Dune.2021.BluRay", HashMatch: true},
	}
	res, err := r.svc.Fetch(r.ctx, 7, "EN")
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != "Dune (2021)/Dune.en.srt" || !res.HashMatch || res.Release != "Dune.2021.BluRay" || res.Remaining != 9 {
		t.Errorf("fetched %+v", res)
	}
	q := r.provider.queries[0]
	if q.TMDBID != 438631 || q.Language != "en" || q.Hash != "0000000000040000" || r.provider.creds[0].APIKey != "the-key" {
		t.Errorf("asked %+v with %+v", q, r.provider.creds[0])
	}
	if b, err := os.ReadFile(filepath.Join(r.dir, "Dune (2021)", "Dune.en.srt")); err != nil || !strings.Contains(string(b), "Hello.") {
		t.Errorf("the sidecar: %q %v", b, err)
	}
	if _, err := r.svc.Fetch(r.ctx, 7, "en"); !errors.Is(err, ErrHave) {
		t.Errorf("a second time: %v", err)
	}
	r.provider.results = []Result{{FileID: 3, Language: "fr", TMDBID: 1}}
	if _, err := r.svc.Fetch(r.ctx, 7, "fr"); !errors.Is(err, ErrNoMatch) {
		t.Errorf("nothing that is this film's: %v", err)
	}
	var detail string
	if err := r.db.QueryRowContext(t.Context(), `SELECT detail FROM audit_event WHERE action = ?`,
		audit.ActionMediaSubtitleFetched).Scan(&detail); err != nil || !strings.Contains(detail, "matched to this file's hash") {
		t.Errorf("audit %q %v", detail, err)
	}

	elsewhere := authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 3, Username: "kid", State: authz.StateActive,
		MFASatisfied: true, LibraryIDs: []int64{r.root + 50},
		Role: authz.Role{Name: "Manager", Permissions: authz.NewPermissionSet(authz.PermBrowse, authz.PermEditLibraryItems)}})
	if _, err := r.svc.Fetch(elsewhere, 7, "de"); !errors.Is(err, importer.ErrFileNotFound) {
		t.Errorf("out of scope: %v", err)
	}
	viewer := authz.WithPrincipal(t.Context(), &authz.Principal{UserID: 4, Username: "v", State: authz.StateActive,
		MFASatisfied: true, Role: authz.Role{Name: "User", Permissions: authz.NewPermissionSet(authz.PermBrowse)}})
	if _, err := r.svc.Fetch(viewer, 7, "de"); !authz.IsDenied(err) {
		t.Errorf("a User fetched a subtitle: %v", err)
	}
}

// An episode is asked for by its series, season and number; an unidentified
// title by the hash alone.
func TestAnEpisodeAndAnUnidentifiedTitleAreAskedForProperly(t *testing.T) {
	r := newRig(t)
	if _, err := r.svc.Configure(r.ctx, Config{APIKey: ptr("k")}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(t.Context(), `UPDATE media_item SET kind = 'series', tmdb_id = 95396 WHERE id = ?`, r.film); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(t.Context(), `UPDATE media_file SET season = 2, episode = 3 WHERE id = 7`); err != nil {
		t.Fatal(err)
	}
	r.provider.results = nil
	_, _ = r.svc.Fetch(r.ctx, 7, "en")
	if q := r.provider.queries[0]; q.ParentTMDBID != 95396 || q.Season != 2 || q.Episode != 3 || q.TMDBID != 0 {
		t.Errorf("an episode asked as %+v", q)
	}
	if _, err := r.db.ExecContext(t.Context(), `UPDATE media_item SET kind = 'movie', tmdb_id = NULL WHERE id = ?`, r.film); err != nil {
		t.Fatal(err)
	}
	_, _ = r.svc.Fetch(r.ctx, 7, "en")
	if q := r.provider.queries[1]; q.TMDBID != 0 || q.ParentTMDBID != 0 || q.Hash == "" {
		t.Errorf("an unidentified film asked as %+v", q)
	}
	// Too small to hash, and unidentified: nothing to match by, nobody asked.
	if err := os.WriteFile(filepath.Join(r.dir, "Dune (2021)", "Dune.mkv"), []byte("tiny"), 0o600); err != nil {
		t.Fatal(err)
	}
	asked := len(r.provider.queries)
	if _, err := r.svc.Fetch(r.ctx, 7, "en"); !errors.Is(err, ErrNoHash) || len(r.provider.queries) != asked {
		t.Errorf("nothing to match by: %v, %d more searches", err, len(r.provider.queries)-asked)
	}
}

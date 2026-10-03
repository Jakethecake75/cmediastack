package subtitles

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

// Where the configuration lives: the setting table, the secrets sealed
// (ADR-0055, decision 1).
const (
	keyAPIKey    = "subtitles.opensubtitles_key_enc"
	keyUsername  = "subtitles.opensubtitles_username"
	keyPassword  = "subtitles.opensubtitles_password_enc"
	keyLanguages = "subtitles.languages"

	// SealedKey and SealedPassword are the two sealed settings, and each
	// context what it is sealed under, for the key rotation (ADR-0054).
	SealedKey             = keyAPIKey
	SealedKeyContext      = "setting:subtitles.opensubtitles_key"
	SealedPassword        = keyPassword
	SealedPasswordContext = "setting:subtitles.opensubtitles_password"
)

// Errors of configuration and fetching.
var (
	// ErrBadLanguage means a language is not a two-letter code.
	ErrBadLanguage = errors.New("subtitles: a language is a two-letter code, like en or fr")
	// ErrNoMatch means OpenSubtitles offered nothing that is this file's.
	ErrNoMatch = errors.New("subtitles: OpenSubtitles has no subtitle in that language for this file")
	// ErrHave means the file already has a sidecar in that language.
	ErrHave = errors.New("subtitles: the file already has a subtitle in that language beside it")
	// ErrNoHash means a file whose title has no TMDB id could not be hashed
	// either, so nothing could be matched to it.
	ErrNoHash = errors.New("subtitles: the title is not identified and the file could not be hashed, " +
		"so nothing could be matched to it")
)

var languageCode = regexp.MustCompile(`^[a-z]{2}$`)

// SettingStore is where the configuration is kept.
type SettingStore interface {
	Setting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Subjects reads a file and its title through the caller's scope.
type Subjects interface {
	FileSubject(ctx context.Context, fileID int64) (importer.FileSubject, error)
}

// Vaults opens a root folder.
type Vaults interface {
	OpenVault(ctx context.Context, id int64) (*library.Vault, error)
}

// Provider is OpenSubtitles; *Client is one.
type Provider interface {
	Search(ctx context.Context, cred Credentials, q Query) ([]Result, error)
	Download(ctx context.Context, cred Credentials, fileID int64) (Download, error)
	Fetch(ctx context.Context, link string) ([]byte, error)
	Forget()
}

// Service configures and fetches.
type Service struct {
	settings SettingStore
	cipher   *secrets.Cipher
	provider Provider
	subjects Subjects
	vaults   Vaults
	audit    *audit.Logger
	log      *slog.Logger
}

// NewService builds one.
func NewService(settings SettingStore, cipher *secrets.Cipher, provider Provider, subjects Subjects,
	vaults Vaults, auditLog *audit.Logger, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{settings: settings, cipher: cipher, provider: provider, subjects: subjects,
		vaults: vaults, audit: auditLog, log: log}
}

// Status is what is configured, never the secrets themselves.
type Status struct {
	HasKey      bool
	Username    string
	HasPassword bool
	Languages   []string
}

func (svc *Service) setting(ctx context.Context, key string) (string, error) {
	v, err := svc.settings.Setting(ctx, key)
	if errors.Is(err, identity.ErrNotFound) {
		return "", nil
	}
	return v, err
}

func (svc *Service) unseal(ctx context.Context, key, sealContext string) (string, error) {
	v, err := svc.setting(ctx, key)
	if err != nil || strings.TrimSpace(v) == "" {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return "", fmt.Errorf("subtitles: the stored %s is not base64: %w", key, err)
	}
	return svc.cipher.DecryptString(raw, sealContext)
}

// Status reads the configuration.
func (svc *Service) Status(ctx context.Context) (Status, error) {
	if err := authz.RequirePermission(ctx, authz.PermSystemSettings); err != nil {
		return Status{}, err
	}
	var st Status
	for key, set := range map[string]*bool{keyAPIKey: &st.HasKey, keyPassword: &st.HasPassword} {
		v, err := svc.setting(ctx, key)
		if err != nil {
			return Status{}, err
		}
		*set = strings.TrimSpace(v) != ""
	}
	var err error
	if st.Username, err = svc.setting(ctx, keyUsername); err != nil {
		return Status{}, err
	}
	if st.Languages, err = svc.Languages(ctx); err != nil {
		return Status{}, err
	}
	return st, nil
}

// Languages are the languages wanted, as two-letter codes.
func (svc *Service) Languages(ctx context.Context) ([]string, error) {
	v, err := svc.setting(ctx, keyLanguages)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, l := range strings.Split(v, ",") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

// Config is a change to the configuration. A nil secret is left as it is; an
// empty one is removed. Nil languages are left as they are.
type Config struct {
	APIKey    *string
	Username  *string
	Password  *string
	Languages []string
}

// Configure stores the configuration, sealing the secrets, and audits what
// changed — never a secret.
func (svc *Service) Configure(ctx context.Context, c Config) (Status, error) {
	if err := authz.RequirePermission(ctx, authz.PermSystemSettings); err != nil {
		return Status{}, err
	}
	var langs []string
	if c.Languages != nil {
		seen := map[string]bool{}
		for _, l := range c.Languages {
			l = strings.ToLower(strings.TrimSpace(l))
			if !languageCode.MatchString(l) {
				return Status{}, fmt.Errorf("%w: %q", ErrBadLanguage, l)
			}
			if !seen[l] {
				seen[l] = true
				langs = append(langs, l)
			}
		}
		sort.Strings(langs)
	}
	var changed []string
	seal := func(key, sealContext string, v *string, what string) error {
		if v == nil {
			return nil
		}
		value := ""
		if strings.TrimSpace(*v) != "" {
			sealed, err := svc.cipher.EncryptString(strings.TrimSpace(*v), sealContext)
			if err != nil {
				return err
			}
			value = base64.StdEncoding.EncodeToString(sealed)
			changed = append(changed, what+" set")
		} else {
			changed = append(changed, what+" removed")
		}
		return svc.settings.SetSetting(ctx, key, value)
	}
	if err := seal(keyAPIKey, SealedKeyContext, c.APIKey, "the API key"); err != nil {
		return Status{}, err
	}
	if err := seal(keyPassword, SealedPasswordContext, c.Password, "the password"); err != nil {
		return Status{}, err
	}
	if c.Username != nil {
		if err := svc.settings.SetSetting(ctx, keyUsername, strings.TrimSpace(*c.Username)); err != nil {
			return Status{}, err
		}
		changed = append(changed, "the username set to "+strings.TrimSpace(*c.Username))
	}
	if c.Languages != nil {
		if err := svc.settings.SetSetting(ctx, keyLanguages, strings.Join(langs, ",")); err != nil {
			return Status{}, err
		}
		changed = append(changed, "the languages set to "+strings.Join(langs, ", "))
	}
	// A new account, or a new key, signs in afresh.
	svc.provider.Forget()
	if p := authz.FromContext(ctx); p != nil && svc.audit != nil && len(changed) > 0 {
		_ = svc.audit.Write(ctx, audit.Event{ActorUserID: &p.UserID, ActorLabel: p.Username,
			Action: audit.ActionSystemSettingChanged, Outcome: audit.OutcomeSuccess,
			TargetKind: "setting", TargetID: "subtitles",
			Detail: "OpenSubtitles: " + strings.Join(changed, "; ")})
	}
	return svc.Status(ctx)
}

func (svc *Service) credentials(ctx context.Context) (Credentials, error) {
	key, err := svc.unseal(ctx, keyAPIKey, SealedKeyContext)
	if err != nil {
		return Credentials{}, err
	}
	if key == "" {
		return Credentials{}, ErrNotConfigured
	}
	user, err := svc.setting(ctx, keyUsername)
	if err != nil {
		return Credentials{}, err
	}
	pass, err := svc.unseal(ctx, keyPassword, SealedPasswordContext)
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{APIKey: key, Username: strings.TrimSpace(user), Password: pass}, nil
}

// Fetched is what fetching one subtitle did.
type Fetched struct {
	Path      string
	Release   string
	HashMatch bool
	Remaining int
}

// SidecarPath is where a fetched subtitle goes: "<video stem>.<lang>.srt".
func SidecarPath(videoRel, language string) string {
	return strings.TrimSuffix(videoRel, path.Ext(videoRel)) + "." + language + ".srt"
}

// Fetch finds the subtitle in a language for one file, and writes it beside
// the file (ADR-0055, decisions 2 to 4). It is a person's: library.edit, and
// the file read through their scope.
func (svc *Service) Fetch(ctx context.Context, fileID int64, language string) (Fetched, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return Fetched{}, err
	}
	language = strings.ToLower(strings.TrimSpace(language))
	if !languageCode.MatchString(language) {
		return Fetched{}, ErrBadLanguage
	}
	subject, err := svc.subjects.FileSubject(ctx, fileID)
	if err != nil {
		return Fetched{}, err
	}
	cred, err := svc.credentials(ctx)
	if err != nil {
		return Fetched{}, err
	}
	actor := "a person"
	if p := authz.FromContext(ctx); p != nil {
		actor = p.Username
	}
	return svc.fetch(ctx, subject, cred, language, actor)
}

// fetch is one file's subtitle in one language, found and written — the same
// for a person and for the sweep (ADR-0056, decision 4). The caller has
// decided it may.
func (svc *Service) fetch(ctx context.Context, subject importer.FileSubject, cred Credentials, language,
	actor string) (Fetched, error) {
	vault, err := svc.vaults.OpenVault(ctx, subject.RootFolderID)
	if err != nil {
		return Fetched{}, err
	}
	defer func() { _ = vault.Close() }()
	dst := SidecarPath(subject.RelPath, language)
	if vault.Exists(dst) {
		return Fetched{}, ErrHave
	}

	q := Query{Language: language}
	if f, err := vault.Open(subject.RelPath); err == nil {
		if info, serr := f.Stat(); serr == nil {
			if h, herr := Hash(f, info.Size()); herr == nil {
				q.Hash = h
			}
		}
		_ = f.Close()
	}
	if subject.Kind == importer.KindSeries {
		q.ParentTMDBID, q.Season, q.Episode = subject.TMDBID, subject.Season, subject.Episode
	} else {
		q.TMDBID = subject.TMDBID
	}
	if q.TMDBID == 0 && q.ParentTMDBID == 0 && q.Hash == "" {
		return Fetched{}, ErrNoHash
	}

	results, err := svc.provider.Search(ctx, cred, q)
	if err != nil {
		return Fetched{}, err
	}
	best, ok := Choose(results, q)
	if !ok {
		return Fetched{}, ErrNoMatch
	}
	dl, err := svc.provider.Download(ctx, cred, best.FileID)
	if err != nil {
		return Fetched{}, err
	}
	body, err := svc.provider.Fetch(ctx, dl.Link)
	if err != nil {
		return Fetched{}, err
	}
	out, err := vault.Create(dst)
	if err != nil {
		return Fetched{}, err
	}
	_, werr := out.Write(body)
	if cerr := out.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return Fetched{}, fmt.Errorf("subtitles: writing %s: %w", dst, werr)
	}
	res := Fetched{Path: dst, Release: best.Release, HashMatch: best.HashMatch, Remaining: dl.Remaining}
	if svc.audit != nil {
		how := "matched by title"
		if best.HashMatch {
			how = "matched to this file's hash"
		}
		ev := audit.Event{ActorLabel: actor, Action: audit.ActionMediaSubtitleFetched, Outcome: audit.OutcomeSuccess,
			TargetKind: "media_file", TargetID: fmt.Sprint(subject.FileID),
			Detail: fmt.Sprintf("%s: %s subtitle %q, %s, written as %s", subject.Title, language, best.Release, how, dst)}
		if p := authz.FromContext(ctx); p != nil && p.UserID > 0 {
			ev.ActorUserID = &p.UserID
		}
		_ = svc.audit.Write(ctx, ev)
	}
	svc.log.Info("a subtitle was fetched", slog.String("path", dst), slog.Bool("hash_match", best.HashMatch),
		slog.Int("downloads_remaining", dl.Remaining))
	return res, nil
}

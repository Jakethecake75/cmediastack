package metadata

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

// Service owns the configured provider and its credential.
//
// # Where the key lives, and why not in the configuration file
//
// In the settings table, sealed with the instance master key, exactly like an
// indexer's API key (ADR-0004's storage, §8's handling). Not in config.yaml and
// not in an environment variable, for three reasons:
//
//   - It can be set from the web UI, so an operator who has one does not have
//     to find a file, edit it and restart a container to use it.
//   - config.yaml is the file people paste into forum posts when asking for
//     help. A credential in it leaves by that route eventually.
//   - It is rotated by replacing a row rather than by a deployment.
//
// The key is NEVER returned over HTTP, not even to an administrator and not
// even masked — the same rule the indexer surface follows. What the admin
// screen shows is whether one is configured and whether it works, which is the
// question anybody actually has.
type Service struct {
	settings SettingStore
	cipher   *secrets.Cipher
	audit    *audit.Logger
	// newClient builds the HTTP client for a provider. Injected so the egress
	// guard is supplied by the wiring rather than reached for here.
	newClient func() *http.Client
	now       func() time.Time

	mu       sync.RWMutex
	provider Provider
	health   Health
}

// SettingStore is the tiny slice of the identity store this needs.
//
// Narrow deliberately: this package has no business with accounts, and an
// interface that carried the whole store would make it look as though it might.
type SettingStore interface {
	Setting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// settingKey is where the sealed credential lives.
const settingKey = "metadata.tmdb_token_enc"

// sealContext binds the ciphertext to its purpose, so a sealed value lifted
// from this row cannot be decrypted as anything else.
const sealContext = "setting:metadata.tmdb_token"

// SealedSetting and SealedContext are where the sealed credential lives and
// what it is sealed under, for the key rotation (ADR-0054).
const (
	SealedSetting = settingKey
	SealedContext = sealContext
)

// NewService builds the service. It does not load the key; call Load.
func NewService(settings SettingStore, cipher *secrets.Cipher, auditLog *audit.Logger,
	newClient func() *http.Client, now func() time.Time) *Service {

	if now == nil {
		now = time.Now
	}
	return &Service{settings: settings, cipher: cipher, audit: auditLog,
		newClient: newClient, now: now}
}

// Load reads the stored credential and builds a provider from it.
//
// A missing credential is not an error: an instance with no metadata provider
// is a supported configuration, and the rest of the software works without one.
func (svc *Service) Load(ctx context.Context) error {
	raw, err := svc.settings.Setting(ctx, settingKey)
	switch {
	case errors.Is(err, identity.ErrNotFound), err == nil && strings.TrimSpace(raw) == "":
		// No key has been set: no provider, which is a state this software
		// runs in.
		return nil
	case err != nil:
		// A database problem. Returned, so the caller can log it — the
		// instance still starts without a provider, but somebody is told why.
		return fmt.Errorf("metadata: reading the stored credential: %w", err)
	}
	sealed, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return fmt.Errorf("metadata: the stored credential is not decodable: %w", err)
	}
	token, err := svc.cipher.DecryptString(sealed, sealContext)
	if err != nil {
		// The commonest cause is a changed master key, and saying so is the
		// difference between an operator rotating a key and an operator
		// wondering why their provider stopped working.
		return fmt.Errorf("metadata: the stored credential could not be decrypted "+
			"(has the master key changed?): %w", err)
	}

	svc.mu.Lock()
	svc.provider = NewTMDB(svc.newClient(), DefaultTMDBBase, token)
	svc.mu.Unlock()
	return nil
}

// Provider returns the configured provider, or nil.
func (svc *Service) Provider() Provider {
	svc.mu.RLock()
	defer svc.mu.RUnlock()
	return svc.provider
}

// Configured reports whether a credential is set.
func (svc *Service) Configured() bool { return svc.Provider() != nil }

// AlternativeTitles asks the configured provider what else a series is called.
//
// Resolved per call, like everything here, so a credential set or removed
// while the process runs takes effect at once.
func (svc *Service) AlternativeTitles(ctx context.Context, seriesID int64) ([]string, error) {
	p := svc.Provider()
	if p == nil {
		return nil, ErrNoProvider
	}
	return p.AlternativeTitles(ctx, seriesID)
}

// FilmTitles asks the configured provider every name a film goes by
// (ADR-0026). Resolved per call, like AlternativeTitles.
func (svc *Service) FilmTitles(ctx context.Context, filmID int64) ([]string, error) {
	p := svc.Provider()
	if p == nil {
		return nil, ErrNoProvider
	}
	return p.FilmTitles(ctx, filmID)
}

// Status is what the admin surface shows.
type Status struct {
	Configured bool
	Provider   string
	Health     Health
}

// Status reports what is configured and what was last learned about it.
func (svc *Service) Status(ctx context.Context) (Status, error) {
	if err := authz.RequirePermission(ctx, authz.PermSystemSettings); err != nil {
		return Status{}, err
	}
	svc.mu.RLock()
	defer svc.mu.RUnlock()

	st := Status{Configured: svc.provider != nil, Health: svc.health}
	if svc.provider != nil {
		st.Provider = svc.provider.Name()
	}
	return st, nil
}

// SetToken stores a credential and proves it works before keeping it.
//
// Checked BEFORE it is stored, and a failing key is not stored at all. The
// alternative — save, then test — leaves an instance configured with something
// that does not work and an operator who has to remember to look at a health
// line. An empty token removes the credential, which is how a provider is
// turned off.
func (svc *Service) SetToken(ctx context.Context, token string) (Health, error) {
	if err := authz.RequirePermission(ctx, authz.PermSystemSettings); err != nil {
		return Health{}, err
	}
	actor := authz.FromContext(ctx)
	token = strings.TrimSpace(token)

	if token == "" {
		if err := svc.settings.SetSetting(ctx, settingKey, ""); err != nil {
			return Health{}, err
		}
		svc.mu.Lock()
		svc.provider, svc.health = nil, Health{}
		svc.mu.Unlock()
		svc.write(ctx, actor, "the metadata provider was removed")
		return Health{Detail: "no metadata provider is configured"}, nil
	}

	candidate := NewTMDB(svc.newClient(), DefaultTMDBBase, token)
	h, err := candidate.Check(ctx)
	if err != nil {
		// Deliberately NOT stored. See the doc comment.
		return h, err
	}

	sealed, err := svc.cipher.EncryptString(token, sealContext)
	if err != nil {
		return Health{}, err
	}
	if err := svc.settings.SetSetting(ctx, settingKey,
		base64.StdEncoding.EncodeToString(sealed)); err != nil {
		return Health{}, err
	}

	svc.mu.Lock()
	svc.provider, svc.health = candidate, h
	svc.mu.Unlock()

	svc.write(ctx, actor, "a metadata provider credential was stored and verified")
	return h, nil
}

// Check re-tests the configured provider.
func (svc *Service) Check(ctx context.Context) (Health, error) {
	if err := authz.RequirePermission(ctx, authz.PermSystemSettings); err != nil {
		return Health{}, err
	}
	p := svc.Provider()
	if p == nil {
		return Health{Detail: "no metadata provider is configured"}, ErrNoProvider
	}
	h, err := p.Check(ctx)

	svc.mu.Lock()
	svc.health = h
	svc.mu.Unlock()
	return h, err
}

// Search proxies a provider search.
//
// Gated on editing library items rather than on browsing: this is the search an
// operator runs when correcting what something IS, and it spends a request
// against a third party on every call.
// SearchToRequest is Search for an account that may only request (ADR-0069):
// films and series, the kinds a request can be for.
func (svc *Service) SearchToRequest(ctx context.Context, q Query) ([]Match, error) {
	if err := authz.RequirePermission(ctx, authz.PermSubmitRequest); err != nil {
		return nil, err
	}
	if q.Kind != KindSeries {
		q.Kind = KindMovie
	}
	p := svc.Provider()
	if p == nil {
		return nil, ErrNoProvider
	}
	return p.Search(ctx, q)
}

func (svc *Service) Search(ctx context.Context, q Query) ([]Match, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return nil, err
	}
	p := svc.Provider()
	if p == nil {
		return nil, ErrNoProvider
	}
	return p.Search(ctx, q)
}

// Details proxies a provider lookup.
func (svc *Service) Details(ctx context.Context, kind Kind, id int64) (Details, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return Details{}, err
	}
	p := svc.Provider()
	if p == nil {
		return Details{}, ErrNoProvider
	}
	return p.Details(ctx, kind, id)
}

func (svc *Service) write(ctx context.Context, actor *authz.Principal, detail string) {
	if svc.audit == nil || actor == nil {
		return
	}
	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &actor.UserID,
		ActorLabel:  actor.Username,
		Action:      audit.ActionSystemSettingChanged,
		TargetKind:  "setting",
		TargetID:    settingKey,
		Detail:      detail,
		// No Before/After: the value is a credential, and the audit log is
		// read by more people than the setting is.
	})
}

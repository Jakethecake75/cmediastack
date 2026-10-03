package indexer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
	"github.com/jakethecake75/cmediastack/internal/platform/secrets"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	database, err := db.Open(db.Options{Path: t.TempDir() + "/test.db", BusyTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}

	key, err := secrets.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipherFromBase64(key)
	if err != nil {
		t.Fatal(err)
	}
	return NewStore(database, cipher, time.Now)
}

func adminCtx(t *testing.T) context.Context {
	t.Helper()
	var role authz.Role
	for _, r := range authz.BuiltinRoles() {
		if r.Name == authz.RoleAdmin {
			role = r
		}
	}
	return authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: 1, Username: "admin", Role: role,
		State: authz.StateActive, MFASatisfied: true, SessionID: "s",
	})
}

func userCtx(t *testing.T) context.Context {
	t.Helper()
	var role authz.Role
	for _, r := range authz.BuiltinRoles() {
		if r.Name == authz.RoleUser {
			role = r
		}
	}
	return authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: 2, Username: "user", Role: role,
		State: authz.StateActive, MFASatisfied: true, SessionID: "s",
	})
}

func TestRoundTripWithASealedKey(t *testing.T) {
	s := newStore(t)
	ctx := adminCtx(t)

	id, err := s.Create(ctx, Definition{
		Name: "Tracker", Kind: KindTorznab, BaseURL: "https://t.example.com",
		APIKey: "the-secret-key", Categories: []int{2000, 5000}, Enabled: true, Priority: 10,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	enabled, err := s.Enabled(t.Context())
	if err != nil {
		t.Fatalf("enabled: %v", err)
	}
	if len(enabled) != 1 {
		t.Fatalf("got %d enabled indexers", len(enabled))
	}
	if enabled[0].APIKey != "the-secret-key" {
		t.Errorf("the key did not survive the round trip: %q", enabled[0].APIKey)
	}
	if len(enabled[0].Categories) != 2 {
		t.Errorf("categories = %v", enabled[0].Categories)
	}
	_ = id
}

// The admin listing must not decrypt the key at all — not merely omit it from
// the JSON. If it never exists in the value, it cannot be encoded by accident.
func TestListNeverCarriesTheAPIKey(t *testing.T) {
	s := newStore(t)
	ctx := adminCtx(t)

	if _, err := s.Create(ctx, Definition{
		Name: "Tracker", Kind: KindTorznab, BaseURL: "https://t.example.com",
		APIKey: "the-secret-key", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	list, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range list {
		if d.APIKey != "" {
			t.Errorf("List returned an API key: %q", d.APIKey)
		}
	}
}

// The key is sealed against the indexer's id, so a blob lifted from one row
// into another must fail to decrypt rather than authenticate to the wrong
// tracker.
func TestASealedKeyCannotBeMovedBetweenIndexers(t *testing.T) {
	s := newStore(t)
	ctx := adminCtx(t)

	first, err := s.Create(ctx, Definition{
		Name: "First", Kind: KindTorznab, BaseURL: "https://a.example.com",
		APIKey: "first-key", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Create(ctx, Definition{
		Name: "Second", Kind: KindTorznab, BaseURL: "https://b.example.com",
		APIKey: "second-key", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate tampering: copy the first indexer's sealed key onto the second.
	if _, err := s.db.ExecContext(t.Context(),
		`UPDATE indexer SET api_key_enc = (SELECT api_key_enc FROM indexer WHERE id = ?) WHERE id = ?`,
		first, second); err != nil {
		t.Fatal(err)
	}

	_, err = s.Enabled(t.Context())
	if err == nil {
		t.Fatal("a relocated key decrypted successfully")
	}
	if !strings.Contains(err.Error(), "will not decrypt") {
		t.Errorf("got %v", err)
	}
}

// An empty key on update means "leave it alone". Treating it as "clear it"
// would break every indexer the moment somebody renamed one.
func TestUpdatingWithoutAKeyKeepsTheStoredOne(t *testing.T) {
	s := newStore(t)
	ctx := adminCtx(t)

	id, err := s.Create(ctx, Definition{
		Name: "Tracker", Kind: KindTorznab, BaseURL: "https://t.example.com",
		APIKey: "original-key", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Update(ctx, Definition{
		ID: id, Name: "Renamed", Kind: KindTorznab,
		BaseURL: "https://t.example.com", APIKey: "", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	enabled, err := s.Enabled(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if enabled[0].APIKey != "original-key" {
		t.Errorf("the key was lost on rename: %q", enabled[0].APIKey)
	}
	if enabled[0].Name != "Renamed" {
		t.Errorf("the rename did not take: %q", enabled[0].Name)
	}
}

// Authorization is enforced at the data layer, not only on the route.
func TestIndexerManagementRequiresThePermission(t *testing.T) {
	s := newStore(t)
	user := userCtx(t)

	if _, err := s.Create(user, Definition{
		Name: "x", Kind: KindTorznab, BaseURL: "https://x.example.com",
	}); !authz.IsDenied(err) {
		t.Errorf("create: %v, want a denial", err)
	}
	if _, err := s.List(user); !authz.IsDenied(err) {
		t.Errorf("list: %v, want a denial", err)
	}
	if err := s.Delete(user, 1); !authz.IsDenied(err) {
		t.Errorf("delete: %v, want a denial", err)
	}
	if err := s.Update(user, Definition{
		ID: 1, Name: "x", Kind: KindTorznab, BaseURL: "https://x.example.com",
	}); !authz.IsDenied(err) {
		t.Errorf("update: %v, want a denial", err)
	}
}

func TestHealthIsRecordedAndCleared(t *testing.T) {
	s := newStore(t)
	ctx := adminCtx(t)

	id, err := s.Create(ctx, Definition{
		Name: "Tracker", Kind: KindTorznab, BaseURL: "https://t.example.com", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.RecordResult(t.Context(), id, errors.New("connection refused")); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult(t.Context(), id, errors.New("connection refused")); err != nil {
		t.Fatal(err)
	}

	health, err := s.HealthOf(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health[id].ConsecutiveFailures != 2 {
		t.Errorf("failures = %d, want 2", health[id].ConsecutiveFailures)
	}
	if health[id].LastError == "" {
		t.Error("the error was not recorded")
	}

	if err := s.RecordResult(t.Context(), id, nil); err != nil {
		t.Fatal(err)
	}
	health, _ = s.HealthOf(ctx)
	if health[id].ConsecutiveFailures != 0 || health[id].LastError != "" {
		t.Errorf("a success did not clear the failure state: %+v", health[id])
	}
}

// An indexer's error message is its own text. It must be bounded before it
// reaches a column and a UI.
func TestRecordedErrorsAreBounded(t *testing.T) {
	s := newStore(t)
	ctx := adminCtx(t)

	id, err := s.Create(ctx, Definition{
		Name: "Tracker", Kind: KindTorznab, BaseURL: "https://t.example.com", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordResult(t.Context(), id, errors.New(strings.Repeat("A", 100_000))); err != nil {
		t.Fatal(err)
	}

	health, _ := s.HealthOf(ctx)
	if len(health[id].LastError) > 512 {
		t.Errorf("a %d-byte error was stored", len(health[id].LastError))
	}
}

func TestDeletingAnUnknownIndexerIsNotFound(t *testing.T) {
	s := newStore(t)
	if err := s.Delete(adminCtx(t), 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// A Cardigann indexer keeps its definition: required to create one, kept by
// an update that leaves it empty, dropped when the kind changes, and handed
// to the search (ADR-0058, decision 1).
func TestACardigannDefinitionIsKeptWithItsIndexer(t *testing.T) {
	s := newStore(t)
	ctx := adminCtx(t)
	def := cardigannIndexer(publicDefinition)
	def.ID = 0
	noDef := def
	noDef.Cardigann = ""
	if _, err := s.Create(ctx, noDef); err == nil {
		t.Error("created a Cardigann indexer without its definition")
	}
	id, err := s.Create(ctx, def)
	if err != nil {
		t.Fatal(err)
	}
	read := func() Definition {
		t.Helper()
		all, err := s.Enabled(ctx)
		if err != nil || len(all) != 1 {
			t.Fatalf("%v %v", all, err)
		}
		return all[0]
	}
	if got := read(); got.Kind != KindCardigann || got.Cardigann != publicDefinition {
		t.Errorf("read back %q %d bytes", got.Kind, len(got.Cardigann))
	}
	noDef.ID, noDef.Name = id, "Renamed"
	if err := s.Update(ctx, noDef); err != nil {
		t.Fatal(err)
	}
	if listed, _ := s.List(ctx); len(listed) != 1 || listed[0].Cardigann != publicDefinition || listed[0].Name != "Renamed" {
		t.Errorf("an update without a definition lost it: %+v", listed)
	}
	torznab := Definition{ID: id, Name: "Renamed", Kind: KindTorznab, BaseURL: "https://tracker.example.org", Enabled: true}
	if err := s.Update(ctx, torznab); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.Cardigann != "" {
		t.Error("a Torznab indexer kept a definition")
	}
	if err := s.Update(ctx, noDef); err == nil {
		t.Error("turned into a Cardigann indexer with no definition")
	}
}

// A signing-in tracker's settings are sealed under its id, never listed,
// kept by an update that gives none, checked against the stored definition,
// and dropped with the kind (ADR-0059, decision 1).
func TestCardigannSettingsAreSealed(t *testing.T) {
	s := newStore(t)
	ctx := adminCtx(t)
	d := Definition{Name: "Private", Kind: KindCardigann, BaseURL: "https://private.example",
		Cardigann: privateDefinition, Settings: map[string]string{"username": "jacob", "password": "s3cret"}, Enabled: true}
	id, err := s.Create(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := s.db.QueryRowContext(ctx, `SELECT settings_enc FROM indexer WHERE id = ?`, id).Scan(&raw); err != nil ||
		len(raw) == 0 || strings.Contains(string(raw), "s3cret") {
		t.Fatalf("stored %q %v", raw, err)
	}
	listed, _ := s.List(ctx)
	if len(listed) != 1 || !listed[0].HasSettings || listed[0].Settings != nil {
		t.Errorf("listed %+v", listed)
	}
	searched := func() map[string]string {
		t.Helper()
		all, err := s.Enabled(ctx)
		if err != nil || len(all) != 1 {
			t.Fatalf("%v %v", all, err)
		}
		return all[0].Settings
	}
	if got := searched(); got["password"] != "s3cret" || got["username"] != "jacob" {
		t.Errorf("for the search: %v", got)
	}
	// Sealed under its own context: moved into the API key's column, the
	// settings do not open as a key.
	if _, err := s.db.ExecContext(ctx, `UPDATE indexer SET api_key_enc = settings_enc WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enabled(ctx); err == nil {
		t.Error("the settings opened as the indexer's API key")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE indexer SET api_key_enc = NULL WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	keep := Definition{ID: id, Name: "Private", Kind: KindCardigann, BaseURL: "https://private.example", Enabled: true}
	if err := s.Update(ctx, keep); err != nil {
		t.Fatal(err)
	}
	if got := searched(); got["password"] != "s3cret" {
		t.Errorf("an update without settings lost them: %v", got)
	}
	keep.Settings = map[string]string{"username": "jacob", "password": "n3w"}
	if err := s.Update(ctx, keep); err != nil {
		t.Fatal(err)
	}
	if got := searched(); got["password"] != "n3w" {
		t.Errorf("new settings: %v", got)
	}
	keep.Settings = map[string]string{"pasword": "typo"}
	if err := s.Update(ctx, keep); err == nil || !strings.Contains(err.Error(), "pasword") {
		t.Errorf("an undeclared setting beside the stored definition: %v", err)
	}
	if got := searched(); got["password"] != "n3w" {
		t.Errorf("a refused update changed the settings: %v", got)
	}
	if err := s.Update(ctx, Definition{ID: id, Name: "Private", Kind: KindTorznab, BaseURL: "https://private.example",
		Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if got := searched(); got != nil {
		t.Errorf("a Torznab indexer kept settings: %v", got)
	}
}

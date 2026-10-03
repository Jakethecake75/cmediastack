package release

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

func newProfileStore(t *testing.T) *ProfileStore {
	t.Helper()
	database, err := db.Open(db.Options{Path: t.TempDir() + "/p.db", BusyTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	return NewProfileStore(database, time.Now)
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

func TestDefaultsAreSeededAndCompile(t *testing.T) {
	s := newProfileStore(t)
	if err := s.EnsureDefaults(t.Context()); err != nil {
		t.Fatal(err)
	}

	got, err := s.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(DefaultProfiles()) {
		t.Fatalf("seeded %d profiles, want %d", len(got), len(DefaultProfiles()))
	}
	for _, sp := range got {
		if !sp.Builtin {
			t.Errorf("%q was not marked built-in", sp.Profile.Name)
		}
		// Compiling happened in scanProfile; this proves it is usable.
		if sp.Profile.Rank(Parse("Film.2020.1080p.WEB-DL.x264-G")) == -1 &&
			sp.Profile.Name != "HD-720p" {
			t.Errorf("%q cannot rank a 1080p WEB-DL", sp.Profile.Name)
		}
	}
}

// Re-seeding on every boot would quietly undo an operator's edits and
// resurrect profiles they deleted. Both are the kind of behaviour people find
// infuriating and cannot explain.
func TestSeedingIsIdempotentAndDoesNotUndoEdits(t *testing.T) {
	s := newProfileStore(t)
	ctx := adminCtx(t)

	if err := s.EnsureDefaults(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, _ := s.List(t.Context())

	// Edit one, delete another.
	edited := before[0]
	edited.Profile.Name = "My Renamed Profile"
	if _, err := s.Save(ctx, edited); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, before[1].ID); err != nil {
		t.Fatal(err)
	}

	// Boot again.
	if err := s.EnsureDefaults(t.Context()); err != nil {
		t.Fatal(err)
	}

	after, _ := s.List(t.Context())
	if len(after) != len(before)-1 {
		t.Errorf("re-seeding resurrected a deleted profile: %d -> %d", len(before), len(after))
	}
	if after[0].Profile.Name != "My Renamed Profile" {
		t.Errorf("re-seeding undid an edit: %q", after[0].Profile.Name)
	}
}

// An invalid profile must be refused when it is SAVED, not stored and then
// silently reject every release.
func TestInvalidProfilesAreRefusedOnSave(t *testing.T) {
	s := newProfileStore(t)
	ctx := adminCtx(t)

	_, err := s.Save(ctx, StoredProfile{Profile: Profile{
		Name: "broken", Allowed: []string{"Bluray-1081p"},
	}})
	if err == nil {
		t.Fatal("an invalid profile was stored")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("the error does not name the profile: %v", err)
	}

	list, _ := s.List(t.Context())
	if len(list) != 0 {
		t.Error("the invalid profile was written anyway")
	}
}

func TestProfilesRoundTripIncludingTerms(t *testing.T) {
	s := newProfileStore(t)
	ctx := adminCtx(t)

	id, err := s.Save(ctx, StoredProfile{Profile: Profile{
		Name: "mine", Allowed: []string{"WEBDL-1080p", "Bluray-1080p"},
		Cutoff:    "Bluray-1080p",
		Preferred: []ScoredTerm{{Term: "Atmos", Score: 10}, {Term: "/x26[45]/", Score: -3}},
		Required:  []string{"MULTi"},
		Forbidden: []string{"CAM"},
	}})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Profile.Preferred) != 2 {
		t.Fatalf("preferred terms = %+v", got.Profile.Preferred)
	}
	if got.Profile.Preferred[0].Term != "Atmos" || got.Profile.Preferred[0].Score != 10 {
		t.Errorf("term = %+v", got.Profile.Preferred[0])
	}
	// And the compiled pattern works after the round trip, which is the thing
	// that would silently be lost: the compiled regexp is unexported and is not
	// serialised, so Get has to recompile.
	if got.Profile.Score(Parse("Film.2020.1080p.WEB-DL.Atmos.MULTi-G")) != 10 {
		t.Error("the preferred term did not survive the round trip in usable form")
	}
	if ok, _ := got.Profile.Accepts(Parse("Film.2020.CAM.MULTi-G")); ok {
		t.Error("the forbidden term did not survive")
	}
	if ok, _ := got.Profile.Accepts(Parse("Film.2020.1080p.WEB-DL.x264-G")); ok {
		t.Error("the required term did not survive")
	}
}

func TestProfileManagementRequiresThePermission(t *testing.T) {
	s := newProfileStore(t)
	var userRole authz.Role
	for _, r := range authz.BuiltinRoles() {
		if r.Name == authz.RoleUser {
			userRole = r
		}
	}
	user := authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: 2, Username: "u", Role: userRole,
		State: authz.StateActive, MFASatisfied: true, SessionID: "s",
	})

	if _, err := s.Save(user, StoredProfile{Profile: Profile{
		Name: "x", Allowed: []string{"WEBDL-1080p"},
	}}); !authz.IsDenied(err) {
		t.Errorf("save: %v, want a denial", err)
	}
	if err := s.Delete(user, 1); !authz.IsDenied(err) {
		t.Errorf("delete: %v, want a denial", err)
	}
}

func TestUnknownProfileIsNotFound(t *testing.T) {
	s := newProfileStore(t)
	if _, err := s.Get(t.Context(), 9999); !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("got %v, want ErrProfileNotFound", err)
	}
	if err := s.Delete(adminCtx(t), 9999); !errors.Is(err, ErrProfileNotFound) {
		t.Errorf("delete: got %v, want ErrProfileNotFound", err)
	}
}

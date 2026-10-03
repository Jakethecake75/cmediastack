package release

import (
	"errors"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// The profile that judges a search when none is chosen (ADR-0027).

func seeded(t *testing.T) *ProfileStore {
	t.Helper()
	s := newProfileStore(t)
	if err := s.EnsureDefaults(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s
}

func byName(t *testing.T, s *ProfileStore, name string) StoredProfile {
	t.Helper()
	all, err := s.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range all {
		if sp.Profile.Name == name {
			return sp
		}
	}
	t.Fatalf("no profile %q", name)
	return StoredProfile{}
}

func defaults(t *testing.T, s *ProfileStore) []string {
	t.Helper()
	all, err := s.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, sp := range all {
		if sp.Default {
			out = append(out, sp.Profile.Name)
		}
	}
	return out
}

// A new instance judges searches by HD-1080p, and by that alone.
func TestANewInstanceIsJudgedByHD1080p(t *testing.T) {
	s := seeded(t)
	sp, ok, err := s.Default(t.Context())
	if err != nil || !ok || sp.Profile.Name != DefaultProfileName || !sp.Default {
		t.Fatalf("default = %+v, %v, %v; want %s", sp, ok, err, DefaultProfileName)
	}
	if got := defaults(t, s); len(got) != 1 {
		t.Errorf("defaults = %v, want exactly one", got)
	}
	// And it is the profile that refuses camera recordings.
	if ok, _ := sp.Profile.Accepts(Parse("Dune.2021.HDCAM.x264-CAMGRP")); ok {
		t.Error("the default profile accepts a camera recording")
	}
}

// Choosing another moves the flag; choosing none clears it; the store says what
// it was each time.
func TestTheDefaultCanBeChangedAndCleared(t *testing.T) {
	s := seeded(t)
	hd720 := byName(t, s, "HD-720p")
	was, err := s.SetDefault(adminCtx(t), hd720.ID)
	if err != nil {
		t.Fatal(err)
	}
	if was != DefaultProfileName {
		t.Errorf("previous = %q, want %s", was, DefaultProfileName)
	}
	if got := defaults(t, s); len(got) != 1 || got[0] != "HD-720p" {
		t.Errorf("defaults = %v, want [HD-720p]", got)
	}

	was, err = s.SetDefault(adminCtx(t), 0)
	if err != nil || was != "HD-720p" {
		t.Fatalf("clearing: previous %q, %v", was, err)
	}
	if _, ok, err := s.Default(t.Context()); ok || err != nil {
		t.Errorf("after clearing there is still a default (%v, %v)", ok, err)
	}
}

// Seeding is once: an operator who cleared the default is not given one back.
func TestSeedingDoesNotRestoreADefaultSomebodyCleared(t *testing.T) {
	s := seeded(t)
	if _, err := s.SetDefault(adminCtx(t), 0); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureDefaults(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Default(t.Context()); ok {
		t.Error("starting again restored a default the operator had cleared")
	}
}

// A profile that does not exist cannot become the default — and asking does
// not cost the instance the default it had.
func TestAnUnknownProfileCannotBeTheDefault(t *testing.T) {
	s := seeded(t)
	for _, id := range []int64{999, -1} {
		if _, err := s.SetDefault(adminCtx(t), id); !errors.Is(err, ErrProfileNotFound) {
			t.Errorf("%d: err = %v, want ErrProfileNotFound", id, err)
		}
	}
	if sp, ok, _ := s.Default(t.Context()); !ok || sp.Profile.Name != DefaultProfileName {
		t.Errorf("a refused change left the default as %+v, %v", sp.Profile.Name, ok)
	}
}

// The database holds at most one default whatever the code does.
func TestTheDatabaseRefusesASecondDefault(t *testing.T) {
	s := seeded(t)
	if _, err := s.db.ExecContext(t.Context(),
		`UPDATE quality_profile SET is_default = 1 WHERE name = 'HD-720p'`); err == nil {
		t.Error("a second default was stored")
	}
}

// Choosing the default is an administrator's.
func TestChoosingTheDefaultNeedsSystemSettings(t *testing.T) {
	s := seeded(t)
	var userRole authz.Role
	for _, r := range authz.BuiltinRoles() {
		if r.Name == authz.RoleManager {
			userRole = r
		}
	}
	manager := authz.WithPrincipal(t.Context(), &authz.Principal{
		UserID: 2, Username: "m", Role: userRole,
		State: authz.StateActive, MFASatisfied: true, SessionID: "s",
	})
	if _, err := s.SetDefault(manager, 0); !authz.IsDenied(err) {
		t.Errorf("a Manager changed the default: %v", err)
	}
	if _, ok, _ := s.Default(t.Context()); !ok {
		t.Error("a refused change cleared the default")
	}
}

// The default cannot be deleted from under the searches; once it is not the
// default, it can.
func TestTheDefaultProfileCannotBeDeleted(t *testing.T) {
	s := seeded(t)
	hd := byName(t, s, DefaultProfileName)
	if err := s.Delete(adminCtx(t), hd.ID); !errors.Is(err, ErrDefaultProfile) {
		t.Fatalf("err = %v, want ErrDefaultProfile", err)
	}
	if _, err := s.Get(t.Context(), hd.ID); err != nil {
		t.Fatalf("the default was deleted anyway: %v", err)
	}
	if _, err := s.SetDefault(adminCtx(t), byName(t, s, "Ultra-HD").ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(adminCtx(t), hd.ID); err != nil {
		t.Errorf("once not the default it could not be deleted: %v", err)
	}
}

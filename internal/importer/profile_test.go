package importer

import (
	"context"
	"errors"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// A title names a quality profile, or none for the default (ADR-0035).
func TestATitleCanNameItsProfile(t *testing.T) {
	r := newRig(t)
	item := r.followed("Severance", r.seriesRoot().ID)
	res, err := r.store.db.ExecContext(r.ctx, `INSERT INTO quality_profile
		(name, allowed, cutoff, created_at, updated_at)
		VALUES ('Ultra-HD', '["WEBDL-2160p"]', 'WEBDL-2160p', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	uhd, _ := res.LastInsertId()

	if item.QualityProfileID != 0 {
		t.Fatalf("a new title names profile %d; it starts on the default", item.QualityProfileID)
	}
	got, err := r.store.SetQualityProfile(r.ctx, item.ID, uhd)
	if err != nil || got.QualityProfileID != uhd {
		t.Fatalf("set: %+v %v", got, err)
	}
	if read, _ := r.store.GetItem(r.ctx, item.ID); read.QualityProfileID != uhd {
		t.Errorf("read back %d, want %d", read.QualityProfileID, uhd)
	}

	if _, err := r.store.SetQualityProfile(r.ctx, item.ID, 99999); !errors.Is(err, ErrNoSuchProfile) {
		t.Errorf("an unknown profile: %v, want ErrNoSuchProfile", err)
	}
	if _, err := r.store.SetQualityProfile(r.ctx, 424242, uhd); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("an unknown title: %v, want ErrItemNotFound", err)
	}
	nobody := authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 9, State: authz.StateActive, MFASatisfied: true, Role: authz.Role{ID: 9, Rank: 0}})
	if _, err := r.store.SetQualityProfile(nobody, item.ID, 0); err == nil {
		t.Error("a principal who may not edit the library chose a title's profile")
	}

	// Deleting the profile returns the title to the default.
	if _, err := r.store.db.ExecContext(r.ctx, `DELETE FROM quality_profile WHERE id = ?`, uhd); err != nil {
		t.Fatal(err)
	}
	if read, _ := r.store.GetItem(r.ctx, item.ID); read.QualityProfileID != 0 {
		t.Errorf("after its profile was deleted the title names %d, want the default", read.QualityProfileID)
	}

	if got, err := r.store.SetQualityProfile(r.ctx, item.ID, 0); err != nil || got.QualityProfileID != 0 {
		t.Errorf("back to the default: %+v %v", got, err)
	}
}

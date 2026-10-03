package identify

import (
	"errors"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// A title a person chose from the provider when adding it (ADR-0025, decision
// 5) is recorded as their decision — with everything a confirmation leaves
// behind, because each piece is relied on elsewhere.

func (r *storeRig) choose(itemID int64, c Candidate) error {
	r.t.Helper()
	return r.db.InTx(r.ctx, func(tx db.Execer) error {
		return r.store.RecordChosen(r.ctx, tx, itemID, c, r.userID)
	})
}

func severanceCandidate() Candidate {
	return Candidate{Provider: "tmdb", ProviderID: 95396, Title: "Severance",
		OriginalTitle: "Severance", Year: 2022, Overview: "Work-life balance, surgically.",
		PosterPath: "pqzjCxPVc9TkVgGRWeAoMmyqkZV.jpg"}
}

func TestAChosenTitleIsAPersonsDecision(t *testing.T) {
	r := newStoreRig(t)
	id := r.addItem("Severance", 2022)
	if err := r.choose(id, severanceCandidate()); err != nil {
		t.Fatal(err)
	}

	got := r.get(id)
	if got.State != StateConfirmed || got.Provider != "tmdb" || got.ProviderID != 95396 {
		t.Errorf("recorded %s %s/%d", got.State, got.Provider, got.ProviderID)
	}
	if got.DecidedBy == nil || *got.DecidedBy != r.userID || got.DecidedAt == nil {
		t.Errorf("decided by %v at %v; want the person who added it", got.DecidedBy, got.DecidedAt)
	}
	if got.Verdict != VerdictChosen {
		t.Errorf("verdict = %q; nothing was scored, and the record should not suggest it was", got.Verdict)
	}
	if got.ParsedTitle != "Severance" || got.ParsedYear != 2022 {
		t.Errorf("the title to put back on undo is %q (%d)", got.ParsedTitle, got.ParsedYear)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].ProviderID != 95396 || got.Candidates[0].Rank != 0 {
		t.Fatalf("candidates = %+v; the chosen title should be stored as the one candidate", got.Candidates)
	}
}

// Each thing the record is for, checked where it is used.
func TestAChosenTitleIsWhatTheRestOfIdentificationExpects(t *testing.T) {
	r := newStoreRig(t)
	id := r.addItem("Severance", 2022)
	if err := r.choose(id, severanceCandidate()); err != nil {
		t.Fatal(err)
	}

	// The pass does not re-propose it...
	ids, err := r.store.UnidentifiedItemIDs(r.ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range ids {
		if other == id {
			t.Error("the identification pass would search for a title a person chose")
		}
	}
	// ...and could not overwrite it if it tried.
	err = r.store.SaveProposal(r.ctx, id, Item{Title: "Severance", Year: 2022, Kind: metadata.KindSeries},
		Decide(Item{Title: "Severance", Year: 2022, Kind: metadata.KindSeries}, []metadata.Match{
			{ProviderID: 1, Kind: metadata.KindSeries, Title: "Severance", Year: 2022, Popularity: 99},
		}))
	if !errors.Is(err, ErrDecidedByAPerson) {
		t.Errorf("err = %v, want ErrDecidedByAPerson", err)
	}

	// Its poster is one this instance may fetch.
	if p, err := r.store.RecordedPosterPath(r.ctx, "tmdb", 95396); err != nil || p != "pqzjCxPVc9TkVgGRWeAoMmyqkZV.jpg" {
		t.Errorf("poster = %q, %v", p, err)
	}

	// And a person can reopen it like any other decision.
	if err := r.store.Reopen(r.ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := r.get(id); got.State != StateUnidentified || got.DecidedBy != nil {
		t.Errorf("after reopening: %s, decided by %v", got.State, got.DecidedBy)
	}
}

// It never overwrites. An item that already has an identification was not just
// added, and failing the add is better than rewriting somebody's decision.
func TestAChosenTitleNeverOverwritesAnIdentification(t *testing.T) {
	r := newStoreRig(t)
	id := r.addItem("Severance", 2022)
	if err := r.choose(id, severanceCandidate()); err != nil {
		t.Fatal(err)
	}
	other := severanceCandidate()
	other.ProviderID = 12345
	if err := r.choose(id, other); err == nil {
		t.Fatal("a second chosen title overwrote the first")
	}
	if got := r.get(id); got.ProviderID != 95396 {
		t.Errorf("provider id = %d", got.ProviderID)
	}
}

func TestAChosenTitleNeedsSomebodyToHaveChosenIt(t *testing.T) {
	r := newStoreRig(t)
	id := r.addItem("Severance", 2022)
	for name, c := range map[string]Candidate{
		"no provider":    {ProviderID: 95396, Title: "Severance"},
		"no provider id": {Provider: "tmdb", Title: "Severance"},
	} {
		if err := r.choose(id, c); err == nil {
			t.Errorf("%s: recorded", name)
		}
	}
	err := r.db.InTx(r.ctx, func(tx db.Execer) error {
		return r.store.RecordChosen(r.ctx, tx, id, severanceCandidate(), 0)
	})
	if err == nil {
		t.Error("a choice by nobody was recorded")
	}
}

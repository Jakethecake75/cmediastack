package identify

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

type storeRig struct {
	t     *testing.T
	db    *db.DB
	store *Store
	ctx   context.Context
	// rootID is a root folder the items belong to.
	rootID int64
	// userID is a real account, because decided_by is a foreign key — which is
	// the point: a decision attributed to an account that does not exist is not
	// an attribution.
	userID int64
}

func newStoreRig(t *testing.T) *storeRig {
	t.Helper()
	database, err := db.Open(db.Options{Path: filepath.Join(t.TempDir(), "id.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	// The pass's context: the store's reads are scoped to the caller
	// (ADR-0037), and background work sees the whole library.
	r := &storeRig{t: t, db: database, ctx: authz.SystemPrincipal(t.Context(), authz.TaskIdentify),
		store: NewStore(database, func() time.Time { return now })}

	res, err := database.ExecContext(r.ctx, `
		INSERT INTO root_folder (path, kind, label, created_at, updated_at)
		VALUES ('/media/movies', 'movies', 'Films', ?, ?)`,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if r.rootID, err = res.LastInsertId(); err != nil {
		t.Fatal(err)
	}

	if _, err := database.ExecContext(r.ctx, `
		INSERT INTO role (name, rank, builtin, created_at, updated_at)
		VALUES ('Admin', 100, 1, ?, ?)`,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	res, err = database.ExecContext(r.ctx, `
		INSERT INTO app_user (username, email, password_hash, state, role_id,
		                      rating_ceiling, created_at, updated_at)
		VALUES ('jacob', 'jacob@example.com', 'x', 'active', 1, 0, ?, ?)`,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if r.userID, err = res.LastInsertId(); err != nil {
		t.Fatal(err)
	}
	return r
}

// addItem creates a library item and returns its id.
func (r *storeRig) addItem(title string, year int) int64 {
	r.t.Helper()
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	res, err := r.db.ExecContext(r.ctx, `
		INSERT INTO media_item (kind, title, year, sort_title, root_folder_id,
		                        folder, added_at, updated_at)
		VALUES ('movie', ?, ?, ?, ?, ?, ?, ?)`,
		title, nullYear(year), title, r.rootID, title+" folder", now, now)
	if err != nil {
		r.t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		r.t.Fatal(err)
	}
	return id
}

func (r *storeRig) get(itemID int64) *Identification {
	r.t.Helper()
	got, err := r.store.Get(r.ctx, itemID)
	if err != nil {
		r.t.Fatalf("get %d: %v", itemID, err)
	}
	return got
}

func arrivalCandidates() []metadata.Match {
	return []metadata.Match{
		{ProviderID: 329865, Kind: metadata.KindMovie, Title: "Arrival",
			Year: 2016, Popularity: 41, PosterPath: "abc.jpg", Overview: "linguist"},
		{ProviderID: 472349, Kind: metadata.KindMovie, Title: "Arrival",
			Year: 2016, Popularity: 1.4},
	}
}

// ---------------------------------------------------------------------------
// The refusal that matters
// ---------------------------------------------------------------------------

// An automatic pass over a library must be safe to re-run — that is the whole
// point of it being automatic. A re-run that quietly undid yesterday's
// corrections would make the feature worse than useless: the operator would fix
// the same items forever and never work out why.
func TestAnAutomaticPassNeverOverwritesAPerson(t *testing.T) {
	r := newStoreRig(t)
	id := r.addItem("Arrival", 2016)

	if err := r.store.SaveProposal(r.ctx, id, item("Arrival", 2016),
		Decide(item("Arrival", 2016), arrivalCandidates())); err != nil {
		t.Fatal(err)
	}
	if got := r.get(id); got.State != StateProposed {
		t.Fatalf("state = %q, want proposed", got.State)
	}

	// A person chooses.
	if _, err := r.store.Confirm(r.ctx, id, 329865, r.userID); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	// The pass runs again, and now the provider's data has "improved" in a way
	// that would pick the other one.
	err := r.store.SaveProposal(r.ctx, id, item("Arrival", 2016),
		Decide(item("Arrival", 2016), []metadata.Match{
			{ProviderID: 472349, Kind: metadata.KindMovie, Title: "Arrival", Year: 2016},
		}))
	if !errors.Is(err, ErrDecidedByAPerson) {
		t.Fatalf("err = %v, want ErrDecidedByAPerson", err)
	}

	got := r.get(id)
	if got.ProviderID != 329865 {
		t.Errorf("provider id = %d — the pass overwrote a person's choice", got.ProviderID)
	}
	if got.State != StateConfirmed || !got.ByAPerson() {
		t.Errorf("state = %q, byPerson = %v", got.State, got.ByAPerson())
	}
}

// A REJECTION is a decision too. An operator who has looked at an item and
// concluded the provider does not have it must not be asked again on every
// pass.
func TestARejectionAlsoStandsAgainstTheNextPass(t *testing.T) {
	r := newStoreRig(t)
	id := r.addItem("Home Video 1998", 0)

	if err := r.store.SaveProposal(r.ctx, id, item("Home Video 1998", 0),
		Decide(item("Home Video 1998", 0), arrivalCandidates())); err != nil {
		t.Fatal(err)
	}
	if err := r.store.Reject(r.ctx, id, r.userID, "this is a family video"); err != nil {
		t.Fatalf("reject: %v", err)
	}

	err := r.store.SaveProposal(r.ctx, id, item("Home Video 1998", 0),
		Decide(item("Home Video 1998", 0), arrivalCandidates()))
	if !errors.Is(err, ErrDecidedByAPerson) {
		t.Errorf("a rejected item was re-proposed: %v", err)
	}
	if got := r.get(id); got.VerdictWhy != "this is a family video" {
		t.Errorf("the operator's reason was lost: %q", got.VerdictWhy)
	}
}

// The software's OWN automatic decision is not a wall — a later pass may
// revisit it when the provider's data improves. Only a person's is.
func TestTheSoftwareMayRevisitItsOwnDecision(t *testing.T) {
	r := newStoreRig(t)
	id := r.addItem("The Matrix", 1999)
	only := []metadata.Match{{ProviderID: 603, Kind: metadata.KindMovie,
		Title: "The Matrix", Year: 1999}}

	if err := r.store.SaveProposal(r.ctx, id, item("The Matrix", 1999),
		Decide(item("The Matrix", 1999), only)); err != nil {
		t.Fatal(err)
	}
	got := r.get(id)
	if got.State != StateConfirmed {
		t.Fatalf("state = %q, want confirmed (an unambiguous match)", got.State)
	}
	if got.ByAPerson() {
		t.Fatal("an automatic acceptance was recorded as a person's decision")
	}

	// Running again is fine, and that is the point.
	if err := r.store.SaveProposal(r.ctx, id, item("The Matrix", 1999),
		Decide(item("The Matrix", 1999), only)); err != nil {
		t.Errorf("the pass could not revisit its own decision: %v", err)
	}
}

// Reopen is the explicit act. It exists so SaveProposal never needs an override
// flag — a caller that wants to redo a person's decision has to say so by name.
func TestReopenIsTheOnlyWayPastAPersonsDecision(t *testing.T) {
	r := newStoreRig(t)
	id := r.addItem("Arrival", 2016)
	res := Decide(item("Arrival", 2016), arrivalCandidates())
	if err := r.store.SaveProposal(r.ctx, id, item("Arrival", 2016), res); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.Confirm(r.ctx, id, 329865, r.userID); err != nil {
		t.Fatal(err)
	}

	if err := r.store.Reopen(r.ctx, id); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := r.get(id)
	if got.ByAPerson() || got.State != StateUnidentified || got.ProviderID != 0 {
		t.Errorf("reopen left %+v", got)
	}

	// And now the pass may run.
	if err := r.store.SaveProposal(r.ctx, id, item("Arrival", 2016), res); err != nil {
		t.Errorf("the pass is still blocked after reopen: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Confirming what was shown
// ---------------------------------------------------------------------------

// A person confirms what they were SHOWN. Accepting an arbitrary id would make
// the confirmation screen advisory.
func TestOnlyAnOfferedCandidateCanBeConfirmed(t *testing.T) {
	r := newStoreRig(t)
	id := r.addItem("Arrival", 2016)
	if err := r.store.SaveProposal(r.ctx, id, item("Arrival", 2016),
		Decide(item("Arrival", 2016), arrivalCandidates())); err != nil {
		t.Fatal(err)
	}

	if _, err := r.store.Confirm(r.ctx, id, 999999, r.userID); !errors.Is(err, ErrNoSuchCandidate) {
		t.Errorf("err = %v, want ErrNoSuchCandidate", err)
	}
	if got := r.get(id); got.State != StateProposed {
		t.Errorf("a refused confirmation changed the state to %q", got.State)
	}
}

// The candidates are what was shown, and a later search replaces them
// wholesale. Merging would leave a person choosing between options from two
// different searches, one of which this software no longer considers plausible.
func TestANewSearchReplacesTheCandidatesRatherThanMerging(t *testing.T) {
	r := newStoreRig(t)
	id := r.addItem("Arrival", 2016)

	if err := r.store.SaveProposal(r.ctx, id, item("Arrival", 2016),
		Decide(item("Arrival", 2016), arrivalCandidates())); err != nil {
		t.Fatal(err)
	}
	if n := len(r.get(id).Candidates); n != 2 {
		t.Fatalf("%d candidates, want 2", n)
	}

	if err := r.store.SaveProposal(r.ctx, id, item("Arrival", 2016),
		Decide(item("Arrival", 2016), []metadata.Match{
			{ProviderID: 329865, Kind: metadata.KindMovie, Title: "Arrival", Year: 2016},
			{ProviderID: 472349, Kind: metadata.KindMovie, Title: "Arrival", Year: 2016},
			{ProviderID: 401867, Kind: metadata.KindMovie, Title: "The Arrival", Year: 2016},
		})); err != nil {
		t.Fatal(err)
	}
	got := r.get(id)
	if len(got.Candidates) != 3 {
		t.Errorf("%d candidates after a second search, want 3", len(got.Candidates))
	}
	// And ordered as they were ranked, so the list a person sees is the list
	// that was produced — popularity ordering included, which the score alone
	// does not recover.
	for i, c := range got.Candidates {
		if c.Rank != i {
			t.Errorf("candidate %d has rank %d", i, c.Rank)
		}
	}
}

// ---------------------------------------------------------------------------
// What is preserved
// ---------------------------------------------------------------------------

// A confirmation has to be undoable, and undoing it must put back a real
// previous value rather than re-parsing a name that may no longer be on disk.
func TestTheParsedTitleSurvivesAConfirmation(t *testing.T) {
	r := newStoreRig(t)
	id := r.addItem("matrix", 0)

	if err := r.store.SaveProposal(r.ctx, id, item("matrix", 0),
		Decide(item("matrix", 0), []metadata.Match{
			{ProviderID: 603, Kind: metadata.KindMovie, Title: "The Matrix", Year: 1999},
		})); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.Confirm(r.ctx, id, 603, r.userID); err != nil {
		t.Fatal(err)
	}

	got := r.get(id)
	if got.ParsedTitle != "matrix" {
		t.Errorf("parsed title = %q, want the pre-identification value", got.ParsedTitle)
	}
	if got.ProviderID != 603 {
		t.Errorf("provider id = %d", got.ProviderID)
	}
}

// The reasoning is stored because a person reviewing a proposal a week later
// needs it, and re-deriving it would need both the candidates and the code to
// be unchanged.
func TestTheReasoningIsStoredWithTheProposal(t *testing.T) {
	r := newStoreRig(t)
	id := r.addItem("Arrival", 2016)
	res := Decide(item("Arrival", 2016), arrivalCandidates())
	if err := r.store.SaveProposal(r.ctx, id, item("Arrival", 2016), res); err != nil {
		t.Fatal(err)
	}

	got := r.get(id)
	if got.VerdictWhy == "" {
		t.Error("the verdict's reasoning was not stored")
	}
	if got.Verdict != VerdictPropose {
		t.Errorf("verdict = %q", got.Verdict)
	}
	for _, c := range got.Candidates {
		if c.Why == "" {
			t.Errorf("candidate %d carries no reasoning", c.ProviderID)
		}
	}
}

// ---------------------------------------------------------------------------
// The queue
// ---------------------------------------------------------------------------

// An item created before this table existed — or a moment ago by an import —
// has no row at all, which is the commonest case on a first run over an
// existing library.
func TestItemsWithNoRowAtAllAreFound(t *testing.T) {
	r := newStoreRig(t)
	a := r.addItem("Arrival", 2016)
	b := r.addItem("Dune", 2021)

	ids, err := r.store.UnidentifiedItemIDs(r.ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("found %v, want both items", ids)
	}

	// Once searched, an item leaves the queue — including one where the search
	// found nothing, so a large library is not re-searched forever to learn the
	// same nothing.
	if err := r.store.SaveProposal(r.ctx, a, item("Arrival", 2016),
		Decide(item("Arrival", 2016), arrivalCandidates())); err != nil {
		t.Fatal(err)
	}
	if err := r.store.SaveProposal(r.ctx, b, item("Dune", 2021),
		Decide(item("Dune", 2021), nil)); err != nil {
		t.Fatal(err)
	}
	if got := r.get(b); got.State != StateNone {
		t.Errorf("an empty search left state %q, want none", got.State)
	}

	ids, err = r.store.UnidentifiedItemIDs(r.ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Errorf("%v are still queued after being searched", ids)
	}
}

// The review queue is oldest first: a person working through it should not have
// yesterday's items pushed down by today's.
func TestTheReviewQueueIsOldestFirst(t *testing.T) {
	r := newStoreRig(t)
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

	var ids []int64
	for i := 0; i < 3; i++ {
		id := r.addItem("Film "+string(rune('A'+i)), 2016)
		ids = append(ids, id)
		// A fresh store per item so each gets a distinct updated_at.
		s := NewStore(r.db, func() time.Time { return base.Add(time.Duration(i) * time.Hour) })
		if err := s.SaveProposal(r.ctx, id, item("Arrival", 2016),
			Decide(item("Arrival", 2016), arrivalCandidates())); err != nil {
			t.Fatal(err)
		}
	}

	got, err := r.store.ListByState(r.ctx, StateProposed, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("%d proposals, want 3", len(got))
	}
	for i, g := range got {
		if g.ItemID != ids[i] {
			t.Errorf("position %d is item %d, want %d", i, g.ItemID, ids[i])
		}
	}
}

func TestGettingSomethingThatWasNeverIdentified(t *testing.T) {
	r := newStoreRig(t)
	if _, err := r.store.Get(r.ctx, 4242); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if err := r.store.Reject(r.ctx, 4242, r.userID, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("reject err = %v, want ErrNotFound", err)
	}
	if err := r.store.Reopen(r.ctx, 4242); !errors.Is(err, ErrNotFound) {
		t.Errorf("reopen err = %v, want ErrNotFound", err)
	}
}

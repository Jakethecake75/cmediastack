package identify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/artwork"
	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// ---------------------------------------------------------------------------
// Doubles
// ---------------------------------------------------------------------------

// fakeProvider answers searches from a table.
type fakeProvider struct {
	byTitle  map[string][]metadata.Match
	searches []string
	err      error
	// What Details is asked for and what it answers. Recorded because the
	// endpoint a provider is asked for depends on the item's KIND, and getting
	// that wrong fails silently — a film's endpoint simply has no series.
	detailsFor []detailsCall
	imdbID     string
	detailsErr error
}

func (f *fakeProvider) Name() string { return "tmdb" }
func (f *fakeProvider) Check(context.Context) (metadata.Health, error) {
	return metadata.Health{OK: true}, nil
}
func (f *fakeProvider) Search(_ context.Context, q metadata.Query) ([]metadata.Match, error) {
	f.searches = append(f.searches, q.Title)
	if f.err != nil {
		return nil, f.err
	}
	return f.byTitle[q.Title], nil
}
func (f *fakeProvider) Details(_ context.Context, k metadata.Kind, id int64) (metadata.Details, error) {
	f.detailsFor = append(f.detailsFor, detailsCall{Kind: k, ProviderID: id})
	if f.detailsErr != nil {
		return metadata.Details{}, f.detailsErr
	}
	return metadata.Details{IMDbID: f.imdbID}, nil
}

// Episodes is part of the Provider interface and is not exercised here:
// identification attaches an id, and the episode list is a separate concern
// with its own store and its own tests (ADR-0022). Returning nothing is the
// honest stand-in — inventing episodes in a fake would be the same mistake the
// real code refuses to make.
func (f *fakeProvider) Episodes(context.Context, int64, int) ([]metadata.Episode, error) {
	return nil, nil
}

func (f *fakeProvider) AlternativeTitles(context.Context, int64) ([]string, error) {
	return nil, nil
}

func (f *fakeProvider) FilmTitles(context.Context, int64) ([]string, error) {
	return nil, nil
}

type detailsCall struct {
	Kind       metadata.Kind
	ProviderID int64
}

// fakeItems stands in for the library.
//
// writes records EVERY call, including ones that changed nothing, so a test
// asserting "the title was not rewritten" cannot pass merely because the write
// path was never reached.
type fakeItems struct {
	items map[int64]Item
	// attached and relabelled are separate because the whole point of the
	// ItemSource split is that one path cannot reach the other. A single list
	// would make "it did not relabel" and "it never wrote at all" the same
	// observation.
	attached   []attachCall
	adopted    []relabelCall
	relabelled []relabelCall
	err        error
}

type attachCall struct {
	ItemID int64
	TMDBID int64
	IMDbID string
}

type relabelCall struct {
	ItemID int64
	Title  string
	Year   int
}

func (f *fakeItems) ItemForIdentification(_ context.Context, id int64) (Item, error) {
	it, ok := f.items[id]
	if !ok {
		return Item{}, errors.New("no such item")
	}
	return it, nil
}

func (f *fakeItems) AttachIdentity(_ context.Context, id, tmdbID int64, imdbID string) error {
	f.attached = append(f.attached, attachCall{ItemID: id, TMDBID: tmdbID, IMDbID: imdbID})
	return f.err
}

func (f *fakeItems) AdoptCanonicalTitle(_ context.Context, id int64, title string) error {
	f.adopted = append(f.adopted, relabelCall{ItemID: id, Title: title})
	return nil
}

func (f *fakeItems) Relabel(_ context.Context, id int64, title string, year int) error {
	f.relabelled = append(f.relabelled, relabelCall{ItemID: id, Title: title, Year: year})
	return f.err
}

type fakeArt struct {
	fetched []artwork.Ref
	err     error
}

func (f *fakeArt) Fetch(_ context.Context, _ string, r artwork.Ref) (string, error) {
	f.fetched = append(f.fetched, r)
	if f.err != nil {
		return "", f.err
	}
	return "poster/tmdb/x.jpg", nil
}

type svcRig struct {
	*storeRig
	svc      *Service
	provider *fakeProvider
	items    *fakeItems
	art      *fakeArt
}

func newSvcRig(t *testing.T) *svcRig {
	t.Helper()
	sr := newStoreRig(t)
	r := &svcRig{
		storeRig: sr,
		provider: &fakeProvider{byTitle: map[string][]metadata.Match{}},
		items:    &fakeItems{items: map[int64]Item{}},
		art:      &fakeArt{},
	}
	r.svc = NewService(sr.store, func() metadata.Provider { return r.provider },
		r.items, r.art, audit.New(sr.db, time.Now),
		slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	return r
}

// asSystem is the context the scheduler runs the pass in: browse and nothing
// else (authz.TaskIdentify).
func asSystem() context.Context {
	return authz.SystemPrincipal(context.Background(), authz.TaskIdentify)
}

// asEditor is a person who may edit library items.
func (r *svcRig) asEditor() context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: r.userID, Username: "jacob", State: authz.StateActive,
		MFASatisfied: true,
		Role: authz.Role{ID: 1, Name: "Admin", Rank: 100,
			Permissions: authz.NewPermissionSet(authz.AllPermissions...)},
	})
}

// ---------------------------------------------------------------------------
// The pass
// ---------------------------------------------------------------------------

// The property the exact-title rule buys: an automatic acceptance can never
// relabel anything, because it required the title to agree already.
func TestAnAutomaticAcceptanceNeverRewritesATitle(t *testing.T) {
	r := newSvcRig(t)
	id := r.addItem("The Matrix", 1999)
	r.items.items[id] = item("The Matrix", 1999)
	r.provider.byTitle["The Matrix"] = []metadata.Match{
		{ProviderID: 603, Kind: metadata.KindMovie, Title: "The Matrix",
			Year: 1999, PosterPath: "matrix.jpg"},
	}

	got, err := r.svc.RunPass(asSystem(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if got.Accepted != 1 {
		t.Fatalf("accepted %d, want 1: %+v", got.Accepted, got)
	}

	if len(r.items.attached) != 1 || r.items.attached[0].TMDBID != 603 {
		t.Fatalf("attached %+v, want one id 603", r.items.attached)
	}
	// The assertion that matters — and the automatic path cannot even express
	// the alternative, because AttachIdentity takes no title.
	if len(r.items.relabelled) != 0 {
		t.Errorf("an automatic acceptance relabelled the item: %+v — Relabel is "+
			"a person's decision and the pass does not hold its permission",
			r.items.relabelled)
	}
	// It DOES adopt the provider's spelling, which is a different thing: the
	// method refuses anything whose normalised form differs, so it cannot
	// change what the item is.
	if len(r.items.adopted) != 1 || r.items.adopted[0].Title != "The Matrix" {
		t.Errorf("the canonical spelling was not adopted: %+v", r.items.adopted)
	}

	// Artwork, though, is the point of accepting at all.
	if len(r.art.fetched) != 1 || r.art.fetched[0].ID != 603 {
		t.Errorf("artwork fetched: %+v", r.art.fetched)
	}
}

// The pass PROPOSES for anything ambiguous, and writes nothing to the library.
func TestTheePassWritesNothingForAProposal(t *testing.T) {
	r := newSvcRig(t)
	id := r.addItem("Arrival", 2016)
	r.items.items[id] = item("Arrival", 2016)
	r.provider.byTitle["Arrival"] = arrivalCandidates()

	got, err := r.svc.RunPass(asSystem(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if got.Proposed != 1 || got.Accepted != 0 {
		t.Fatalf("%+v", got)
	}
	if n := len(r.items.attached) + len(r.items.relabelled) + len(r.items.adopted); n != 0 {
		t.Errorf("a proposal touched the library %d time(s)", n)
	}
	if len(r.art.fetched) != 0 {
		t.Errorf("a proposal fetched artwork before anybody agreed to it")
	}
	if st := r.get(id).State; st != StateProposed {
		t.Errorf("state = %q", st)
	}
}

// A per-item failure must not stop the pass — a library half-examined because
// one title errored is worse than a slow one.
func TestOneBadItemDoesNotStopThePass(t *testing.T) {
	r := newSvcRig(t)
	good := r.addItem("The Matrix", 1999)
	missing := r.addItem("Broken", 2000) // deliberately absent from fakeItems
	r.items.items[good] = item("The Matrix", 1999)
	r.provider.byTitle["The Matrix"] = []metadata.Match{
		{ProviderID: 603, Kind: metadata.KindMovie, Title: "The Matrix", Year: 1999},
	}
	_ = missing

	got, err := r.svc.RunPass(asSystem(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if got.Failed != 1 {
		t.Errorf("failed = %d, want 1", got.Failed)
	}
	if got.Accepted != 1 {
		t.Errorf("the good item was not processed: %+v", got)
	}
}

// A rate limit or a bad key is about the PROVIDER, not this item. Carrying on
// would spend the rest of a library learning the same thing.
func TestAProviderLevelFailureStopsThePass(t *testing.T) {
	for _, providerErr := range []error{metadata.ErrRateLimited, metadata.ErrUnauthorized} {
		r := newSvcRig(t)
		for i := 0; i < 5; i++ {
			id := r.addItem("Film "+string(rune('A'+i)), 2016)
			r.items.items[id] = item("Film", 2016)
		}
		r.provider.err = providerErr

		got, err := r.svc.RunPass(asSystem(), 50)
		if !errors.Is(err, providerErr) {
			t.Errorf("%v: err = %v", providerErr, err)
		}
		if len(r.provider.searches) != 1 {
			t.Errorf("%v: made %d searches after the provider said stop",
				providerErr, len(r.provider.searches))
		}
		_ = got
	}
}

// The pass runs with browse and nothing else. If it ever needed more, this is
// where that would show up.
func TestThePassRunsWithTheSystemGrantItIsGiven(t *testing.T) {
	r := newSvcRig(t)
	id := r.addItem("The Matrix", 1999)
	r.items.items[id] = item("The Matrix", 1999)
	r.provider.byTitle["The Matrix"] = []metadata.Match{
		{ProviderID: 603, Kind: metadata.KindMovie, Title: "The Matrix", Year: 1999},
	}
	if _, err := r.svc.RunPass(asSystem(), 50); err != nil {
		t.Fatalf("the pass cannot run with authz.TaskIdentify's grant: %v", err)
	}

	// And an anonymous context cannot run it at all.
	if _, err := r.svc.RunPass(context.Background(), 50); err == nil {
		t.Error("the pass ran with no principal")
	}
}

// ---------------------------------------------------------------------------
// The human half
// ---------------------------------------------------------------------------

// The one path that may change what an operator browses to — with a person
// looking at both titles when it happens.
func TestAPersonsConfirmationMayRewriteTheTitle(t *testing.T) {
	r := newSvcRig(t)
	id := r.addItem("matrix", 0)
	r.items.items[id] = item("matrix", 0)
	r.provider.byTitle["matrix"] = []metadata.Match{
		{ProviderID: 603, Kind: metadata.KindMovie, Title: "The Matrix",
			Year: 1999, PosterPath: "m.jpg"},
	}

	if _, err := r.svc.RunPass(asSystem(), 50); err != nil {
		t.Fatal(err)
	}
	if st := r.get(id).State; st != StateProposed {
		t.Fatalf("state = %q, want proposed (no year)", st)
	}
	if n := len(r.items.attached) + len(r.items.relabelled) + len(r.items.adopted); n != 0 {
		t.Fatal("the pass wrote to the library")
	}

	chosen, err := r.svc.Confirm(r.asEditor(), id, 603)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if chosen.ProviderID != 603 {
		t.Errorf("confirmed %d", chosen.ProviderID)
	}

	if len(r.items.attached) != 1 || r.items.attached[0].TMDBID != 603 {
		t.Fatalf("attached %+v", r.items.attached)
	}
	if len(r.items.relabelled) != 1 {
		t.Fatalf("%d relabels, want 1", len(r.items.relabelled))
	}
	if got := r.items.relabelled[0]; got.Title != "The Matrix" || got.Year != 1999 {
		t.Errorf("relabelled to %+v", got)
	}

	// The parsed title survives, so the decision is reversible.
	if got := r.get(id); got.ParsedTitle != "matrix" {
		t.Errorf("parsed title = %q", got.ParsedTitle)
	}
}

// A confirmation that changes nothing must not look in the audit log like one
// that did.
func TestAConfirmationThatChangesNothingSaysSo(t *testing.T) {
	r := newSvcRig(t)
	id := r.addItem("Arrival", 2016)
	r.items.items[id] = item("Arrival", 2016)
	r.provider.byTitle["Arrival"] = arrivalCandidates()

	if _, err := r.svc.RunPass(asSystem(), 50); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Confirm(r.asEditor(), id, 329865); err != nil {
		t.Fatal(err)
	}

	if len(r.items.relabelled) != 0 {
		t.Errorf("the item was relabelled when its title already agreed: %+v",
			r.items.relabelled)
	}
	if len(r.items.attached) != 1 || r.items.attached[0].TMDBID != 329865 {
		t.Errorf("attached %+v", r.items.attached)
	}
}

// A missing picture is cosmetic; an identification that failed because a CDN
// was slow is not. The two must not be able to become each other.
func TestArtworkFailureNeverFailsAnIdentification(t *testing.T) {
	r := newSvcRig(t)
	r.art.err = errors.New("the CDN is on fire")
	id := r.addItem("The Matrix", 1999)
	r.items.items[id] = item("The Matrix", 1999)
	r.provider.byTitle["The Matrix"] = []metadata.Match{
		{ProviderID: 603, Kind: metadata.KindMovie, Title: "The Matrix",
			Year: 1999, PosterPath: "m.jpg"},
	}

	got, err := r.svc.RunPass(asSystem(), 50)
	if err != nil {
		t.Fatalf("an artwork failure failed the pass: %v", err)
	}
	if got.Accepted != 1 {
		t.Errorf("accepted = %d", got.Accepted)
	}
	if r.get(id).State != StateConfirmed {
		t.Error("the identification was not recorded")
	}
}

// Only somebody who may edit library items may confirm one — because that is
// exactly what a confirmation does.
func TestConfirmingNeedsThePermissionToEditLibraryItems(t *testing.T) {
	r := newSvcRig(t)
	id := r.addItem("Arrival", 2016)
	r.items.items[id] = item("Arrival", 2016)
	r.provider.byTitle["Arrival"] = arrivalCandidates()
	if _, err := r.svc.RunPass(asSystem(), 50); err != nil {
		t.Fatal(err)
	}

	browser := authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: r.userID, Username: "sam", State: authz.StateActive,
		MFASatisfied: true,
		Role: authz.Role{ID: 3, Name: "User", Rank: 10,
			Permissions: authz.NewPermissionSet(authz.PermLogin, authz.PermBrowse)},
	})
	for _, call := range []struct {
		what string
		err  error
	}{
		{"confirm", func() error { _, e := r.svc.Confirm(browser, id, 329865); return e }()},
		{"reject", r.svc.Reject(browser, id, "no")},
		{"reopen", r.svc.Reopen(browser, id)},
		{"pending", func() error { _, e := r.svc.Pending(browser, 10); return e }()},
	} {
		if call.err == nil {
			t.Errorf("a browse-only account could %s", call.what)
		}
	}
	if n := len(r.items.attached) + len(r.items.relabelled) + len(r.items.adopted); n != 0 {
		t.Errorf("a refused confirmation still wrote to the library %d time(s)", n)
	}
}

// ---------------------------------------------------------------------------
// External identifiers
// ---------------------------------------------------------------------------

// The IMDb id is the one every other tool in this ecosystem agrees on, and the
// provider only carries it on the details endpoint. An item that came out of a
// pass holding a TMDB id and nothing else is one that will cost a provider
// request later, while somebody waits.
func TestAnIdentifiedItemGetsTheIdentifierOtherToolsUse(t *testing.T) {
	r := newSvcRig(t)
	r.provider.imdbID = "tt0133093"
	id := r.addItem("The Matrix", 1999)
	r.items.items[id] = item("The Matrix", 1999)
	r.provider.byTitle["The Matrix"] = []metadata.Match{
		{ProviderID: 603, Kind: metadata.KindMovie, Title: "The Matrix", Year: 1999},
	}

	if _, err := r.svc.RunPass(asSystem(), 50); err != nil {
		t.Fatal(err)
	}

	var withIMDb []attachCall
	for _, a := range r.items.attached {
		if a.IMDbID != "" {
			withIMDb = append(withIMDb, a)
		}
	}
	if len(withIMDb) != 1 {
		t.Fatalf("attached %+v, want one call carrying an IMDb id", r.items.attached)
	}
	if withIMDb[0].IMDbID != "tt0133093" {
		t.Errorf("IMDb id = %q", withIMDb[0].IMDbID)
	}
	// The provider id goes in alongside it, so a row can never end up holding
	// an IMDb id next to a different provider id.
	if withIMDb[0].TMDBID != 603 {
		t.Errorf("attached IMDb id without its provider id: %+v", withIMDb[0])
	}
}

// A series' details live behind a different provider endpoint than a film's.
// Asking for the wrong one fails silently — the film endpoint simply has no
// series with that id — so nothing would go wrong except that every series an
// operator confirmed would quietly have no artwork and no IMDb id.
func TestConfirmingASeriesAsksTheProviderAboutASeries(t *testing.T) {
	r := newSvcRig(t)
	r.provider.imdbID = "tt11280740"
	id := r.addItem("severance", 0)
	r.items.items[id] = Item{Title: "severance", Kind: metadata.KindSeries}
	r.provider.byTitle["severance"] = []metadata.Match{
		{ProviderID: 95396, Kind: metadata.KindSeries, Title: "Severance",
			Year: 2022, PosterPath: "s.jpg"},
	}

	if _, err := r.svc.RunPass(asSystem(), 50); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Confirm(r.asEditor(), id, 95396); err != nil {
		t.Fatalf("confirm: %v", err)
	}

	if len(r.provider.detailsFor) != 1 {
		t.Fatalf("details calls = %+v, want 1", r.provider.detailsFor)
	}
	if got := r.provider.detailsFor[0].Kind; got != metadata.KindSeries {
		t.Errorf("asked the provider for a %q, want a series", got)
	}
}

// The identification is the valuable thing; the extra identifier is an
// enrichment. A details request that fails must not cost an item the id it was
// already correctly given.
func TestAFailedDetailsLookupDoesNotCostAnItemItsIdentification(t *testing.T) {
	r := newSvcRig(t)
	r.provider.detailsErr = errors.New("the provider is having a bad day")
	id := r.addItem("The Matrix", 1999)
	r.items.items[id] = item("The Matrix", 1999)
	r.provider.byTitle["The Matrix"] = []metadata.Match{
		{ProviderID: 603, Kind: metadata.KindMovie, Title: "The Matrix", Year: 1999},
	}

	res, err := r.svc.RunPass(asSystem(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 1 {
		t.Fatalf("accepted %d, want 1: a details failure failed the identification", res.Accepted)
	}
	if len(r.items.attached) != 1 || r.items.attached[0].TMDBID != 603 {
		t.Fatalf("attached %+v, want the provider id despite the failure", r.items.attached)
	}
	if st := r.get(id).State; st != StateConfirmed {
		t.Errorf("state = %q, want confirmed", st)
	}
}

// ---------------------------------------------------------------------------
// Fetching a poster on demand
// ---------------------------------------------------------------------------

// The review screen's posters are fetched when somebody first looks, which
// means a request can cause this instance to reach out to a CDN. What keeps
// that from being a proxy is that the URL comes from a stored candidate row:
// the request chooses BETWEEN recorded posters and cannot introduce one.
func TestAPosterIsOnlyFetchedForATitleThisInstanceRecorded(t *testing.T) {
	r := newSvcRig(t)
	id := r.addItem("Arrival", 2016)
	r.items.items[id] = item("Arrival", 2016)
	r.provider.byTitle["Arrival"] = arrivalCandidates()
	if _, err := r.svc.RunPass(asSystem(), 50); err != nil {
		t.Fatal(err)
	}
	r.art.fetched = nil // the pass proposed, so it cached nothing

	// An id nobody offered.
	_, err := r.svc.CachePoster(asSystem(), "tmdb", 999999)
	if !errors.Is(err, ErrNotRecorded) {
		t.Fatalf("err = %v, want ErrNotRecorded", err)
	}
	if len(r.art.fetched) != 0 {
		t.Fatalf("an unrecorded id caused an outbound fetch: %+v — this route "+
			"would be an open proxy", r.art.fetched)
	}
}

// And the recorded ones do work, using the path that was stored rather than
// anything reconstructed from the id.
func TestACandidatesPosterIsFetchedFromTheRecordedPath(t *testing.T) {
	r := newSvcRig(t)
	id := r.addItem("Arrival", 2016)
	r.items.items[id] = item("Arrival", 2016)
	r.provider.byTitle["Arrival"] = []metadata.Match{
		{ProviderID: 329865, Kind: metadata.KindMovie, Title: "Arrival",
			Year: 2016, PosterPath: "the-stored-one.jpg"},
		{ProviderID: 472349, Kind: metadata.KindMovie, Title: "Arrival",
			Year: 2016, PosterPath: "a-different-one.jpg"},
	}
	if _, err := r.svc.RunPass(asSystem(), 50); err != nil {
		t.Fatal(err)
	}
	r.art.fetched = nil

	if _, err := r.svc.CachePoster(asSystem(), "tmdb", 329865); err != nil {
		t.Fatalf("cache poster: %v", err)
	}
	if len(r.art.fetched) != 1 {
		t.Fatalf("fetched %+v, want one", r.art.fetched)
	}
	got := r.art.fetched[0]
	if got.RemotePath != "the-stored-one.jpg" {
		t.Errorf("fetched %q, want the path stored with that candidate", got.RemotePath)
	}
	if got.ID != 329865 || got.Provider != "tmdb" {
		t.Errorf("fetched the wrong reference: %+v", got)
	}
}

// Browsing is a permission. An instance where anyone who can reach the port can
// make it fetch images is not one that has "no anonymous access to anything".
func TestFetchingAPosterNeedsThePermissionToBrowse(t *testing.T) {
	r := newSvcRig(t)
	id := r.addItem("Arrival", 2016)
	r.items.items[id] = item("Arrival", 2016)
	r.provider.byTitle["Arrival"] = arrivalCandidates()
	if _, err := r.svc.RunPass(asSystem(), 50); err != nil {
		t.Fatal(err)
	}
	r.art.fetched = nil

	// A principal with no permissions at all.
	ctx := authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: r.userID, Username: "nobody", State: authz.StateActive,
		MFASatisfied: true,
		Role:         authz.Role{ID: 9, Name: "Pending", Rank: 0},
	})
	if _, err := r.svc.CachePoster(ctx, "tmdb", 329865); err == nil {
		t.Fatal("a principal with no permissions fetched a poster")
	}
	if len(r.art.fetched) != 0 {
		t.Errorf("it fetched anyway: %+v", r.art.fetched)
	}
}

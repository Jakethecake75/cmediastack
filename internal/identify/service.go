package identify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jakethecake75/cmediastack/internal/artwork"
	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// Service runs identification and records its outcome.
//
// # The property that makes the automatic half safe
//
// Automatic acceptance requires an EXACT title match (ADR-0019). So an
// automatically identified item's title already agrees with the provider's —
// which means automatic acceptance can never relabel anything. It attaches an
// id and artwork to a title that was already right.
//
// Relabelling only ever happens when a PERSON confirms a candidate whose title
// differs from the parsed one, and then it is their decision, taken while
// looking at both strings. That is not a coincidence of the implementation; it
// is what the exact-title rule buys, and it is why the pass needs no permission
// to edit library items.
type Service struct {
	store    *Store
	provider ProviderSource
	items    ItemSource
	art      ArtworkFetcher
	audit    *audit.Logger
	log      *slog.Logger
	now      func() time.Time
}

// ProviderSource hands over the configured metadata provider, or nil.
//
// A function rather than a provider, because the credential can be set, changed
// and removed while the process runs: holding a provider captured at startup
// would mean an operator configuring one has to restart to use it.
type ProviderSource func() metadata.Provider

// ItemSource is the slice of the library this package needs.
//
// TWO writes, not one, and the split is the design. AttachIdentity takes ids
// and no title, so the automatic path — which holds only this — cannot relabel
// anything by any argument it could pass. Relabel takes the title, needs the
// permission to edit library items, and is reached only from Confirm, where a
// person is looking at both strings.
//
// A single SetIdentity taking an optional title would work and would be worse:
// the guarantee would live in a comment and in which fields the caller happened
// to fill, and the first person to add a field would have to notice.
type ItemSource interface {
	// ItemForIdentification returns the title, year and kind of one item.
	ItemForIdentification(ctx context.Context, itemID int64) (Item, error)
	// AttachIdentity records which provider title this is. Ids only.
	AttachIdentity(ctx context.Context, itemID, tmdbID int64, imdbID string) error
	// AdoptCanonicalTitle takes the provider's SPELLING of a title already
	// agreed to be the same one. It refuses anything that would change meaning,
	// which is why the automatic path may call it and Relabel remains a
	// person's decision.
	AdoptCanonicalTitle(ctx context.Context, itemID int64, title string) error
	// Relabel changes what the item is called.
	Relabel(ctx context.Context, itemID int64, title string, year int) error
}

// ArtworkFetcher caches a poster. Optional: an instance with no artwork cache
// identifies perfectly well and simply has no pictures.
type ArtworkFetcher interface {
	Fetch(ctx context.Context, base string, r artwork.Ref) (string, error)
}

// NewService builds the service.
func NewService(store *Store, provider ProviderSource, items ItemSource,
	art ArtworkFetcher, auditLog *audit.Logger, log *slog.Logger,
	now func() time.Time) *Service {

	if log == nil {
		log = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, provider: provider, items: items, art: art,
		audit: auditLog, log: log, now: now}
}

// Store exposes the repository for wiring.
func (svc *Service) Store() *Store { return svc.store }

// PassResult reports what a run of the pass did.
type PassResult struct {
	Considered   int
	Proposed     int
	Accepted     int
	NothingFound int
	Skipped      int
	Failed       int
}

// RunPass identifies unidentified items.
//
// Called from the scheduler with authz.TaskIdentify, whose grant is PermBrowse
// and nothing else. It writes proposals; it does not touch a library row.
//
// A per-item failure does not stop the pass. A provider that is rate-limiting
// or a single title that produces an error must not leave the rest of a library
// unexamined — and the counts say what happened, so a pass that achieved
// nothing is visible as such rather than as a silent success.
func (svc *Service) RunPass(ctx context.Context, limit int) (PassResult, error) {
	var res PassResult

	p := svc.provider()
	if p == nil {
		return res, metadata.ErrNoProvider
	}
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return res, err
	}

	ids, err := svc.store.UnidentifiedItemIDs(ctx, limit)
	if err != nil {
		return res, err
	}

	for _, id := range ids {
		if ctx.Err() != nil {
			// A cancelled pass stops where it is. Everything already recorded
			// stands; the next run picks up the rest, which is why the queue is
			// a query rather than a cursor.
			return res, ctx.Err()
		}
		res.Considered++

		item, err := svc.items.ItemForIdentification(ctx, id)
		if err != nil {
			res.Failed++
			svc.log.Warn("could not read a library item for identification",
				slog.Int64("item", id), slog.String("error", err.Error()))
			continue
		}

		matches, err := p.Search(ctx, metadata.Query{
			Kind: item.Kind, Title: item.Title, Year: item.Year,
		})
		if err != nil {
			res.Failed++
			svc.log.Warn("a provider search failed",
				slog.Int64("item", id), slog.String("title", item.Title),
				slog.String("error", err.Error()))
			if errors.Is(err, metadata.ErrRateLimited) ||
				errors.Is(err, metadata.ErrUnauthorized) {
				// These are about the PROVIDER, not this item. Carrying on
				// would spend the rest of the library learning the same thing.
				return res, err
			}
			continue
		}

		decision := Decide(item, matches)
		if err := svc.store.SaveProposal(ctx, id, item, decision); err != nil {
			if errors.Is(err, ErrDecidedByAPerson) {
				res.Skipped++
				continue
			}
			res.Failed++
			svc.log.Warn("could not record an identification",
				slog.Int64("item", id), slog.String("error", err.Error()))
			continue
		}

		switch decision.Verdict {
		case VerdictAccept:
			res.Accepted++
			svc.applyAutomatic(ctx, id, item, decision)
		case VerdictPropose:
			res.Proposed++
		default:
			res.NothingFound++
		}
	}
	return res, nil
}

// applyAutomatic writes back what an automatic acceptance is allowed to write.
//
// Ids and artwork. NOT the title — see the type comment: an exact title match
// has nothing to rewrite, so there is no case in which this would change what
// an operator browses to, and the code says so rather than relying on it.
func (svc *Service) applyAutomatic(ctx context.Context, itemID int64, item Item, res Result) {
	best := res.Best()
	if best == nil {
		return
	}
	// AttachIdentity, not Relabel — and there is no Relabel call reachable from
	// here. The acceptance rule required the title to match already, so there
	// is nothing to rewrite, and the signature makes that unarguable.
	if err := svc.items.AttachIdentity(ctx, itemID, best.Match.ProviderID, ""); err != nil {
		svc.log.Warn("could not attach an identification to a library item",
			slog.Int64("item", itemID), slog.String("error", err.Error()))
		return
	}

	// The provider's SPELLING of a title this software already agreed is the
	// same one: "the matrix" becomes "The Matrix". Not a rename — the method
	// refuses anything whose normalised form differs — so a library stops
	// carrying whatever casing a release group happened to use without the
	// pass ever being able to change what an item is.
	if err := svc.items.AdoptCanonicalTitle(ctx, itemID, best.Match.Title); err != nil {
		svc.log.Info("kept the existing spelling of a title",
			slog.Int64("item", itemID), slog.String("error", err.Error()))
	}
	svc.attachExternalIDs(ctx, itemID, item.Kind, best.Match.ProviderID)
	svc.fetchArtwork(ctx, best.Match)
}

// attachExternalIDs fills in the identifier the search endpoint does not carry.
//
// The provider's search results hold its own id and nothing else; the IMDb id —
// the one release groups, indexers and every other tool in this ecosystem agree
// on — is only on the details endpoint. So it costs one extra provider request
// per identified item.
//
// Spent here, deliberately. It is a background pass that already spends one
// request per item, it happens once in an item's life, and the alternative is
// to spend the same request later, one item at a time, while somebody waits for
// a search to come back.
//
// Never fails the identification. An item with a provider id and no IMDb id is
// identified; an item that LOST its identification because a details request
// timed out would not be, and that trade is not worth an identifier.
func (svc *Service) attachExternalIDs(ctx context.Context, itemID int64, kind metadata.Kind, providerID int64) {
	p := svc.provider()
	if p == nil {
		return
	}
	d, err := p.Details(ctx, kind, providerID)
	if err != nil {
		svc.log.Info("could not read external ids for an identified item",
			slog.Int64("item", itemID), slog.Int64("provider_id", providerID),
			slog.String("error", err.Error()))
		return
	}
	if d.IMDbID == "" {
		return
	}
	// The provider id goes in again alongside it. AttachIdentity only writes
	// the arguments it is given, and passing both means this cannot leave a row
	// holding an IMDb id that belongs to a different provider id than the one
	// that was attached.
	if err := svc.items.AttachIdentity(ctx, itemID, providerID, d.IMDbID); err != nil {
		svc.log.Warn("could not attach an external id to a library item",
			slog.Int64("item", itemID), slog.String("error", err.Error()))
	}
}

// fetchArtwork caches a poster, and never fails anything.
//
// A missing picture is a cosmetic problem; an identification that failed
// because a CDN was slow is a real one. The two must not be able to become each
// other.
func (svc *Service) fetchArtwork(ctx context.Context, m metadata.Match) {
	if svc.art == nil || m.PosterPath == "" {
		return
	}
	base := metadata.FallbackImageBase
	if t, ok := svc.provider().(*metadata.TMDB); ok && t.ImageBase() != "" {
		base = t.ImageBase()
	}
	if _, err := svc.art.Fetch(ctx, base, artwork.Ref{
		Kind: artwork.KindPoster, Provider: "tmdb",
		ID: m.ProviderID, Size: "w342", RemotePath: m.PosterPath,
	}); err != nil {
		svc.log.Info("a poster could not be cached",
			slog.Int64("provider_id", m.ProviderID), slog.String("error", err.Error()))
	}
}

// ---------------------------------------------------------------------------
// The human half
// ---------------------------------------------------------------------------

// Pending returns proposals waiting for a person.
func (svc *Service) Pending(ctx context.Context, limit int) ([]*Identification, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return nil, err
	}
	return svc.store.ListByState(ctx, StateProposed, limit)
}

// Get returns one identification.
func (svc *Service) Get(ctx context.Context, itemID int64) (*Identification, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return nil, err
	}
	return svc.store.Get(ctx, itemID)
}

// Confirm attaches a candidate on a person's authority.
//
// This is the ONE path that may change what an operator browses to, and it is
// the path with a person looking at both titles when it happens.
func (svc *Service) Confirm(ctx context.Context, itemID, providerID int64) (Candidate, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return Candidate{}, err
	}
	actor := authz.FromContext(ctx)

	before, err := svc.store.Get(ctx, itemID)
	if err != nil {
		return Candidate{}, err
	}

	chosen, err := svc.store.Confirm(ctx, itemID, providerID, actor.UserID)
	if err != nil {
		return Candidate{}, err
	}

	if err := svc.items.AttachIdentity(ctx, itemID, chosen.ProviderID, ""); err != nil {
		return chosen, fmt.Errorf("identify: recorded the decision but could not "+
			"attach it to the library item: %w", err)
	}

	// The title is rewritten only when it actually differs, so a confirmation
	// that changes nothing does not look — in the row's updated_at, or in the
	// audit log — like one that did.
	var newTitle string
	var newYear int
	if chosen.Title != "" && chosen.Title != before.ParsedTitle {
		newTitle = chosen.Title
	}
	if chosen.Year > 0 && chosen.Year != before.ParsedYear {
		newYear = chosen.Year
	}
	if newTitle != "" || newYear > 0 {
		if err := svc.items.Relabel(ctx, itemID, newTitle, newYear); err != nil {
			return chosen, fmt.Errorf("identify: recorded the decision but could "+
				"not rename the library item: %w", err)
		}
	}

	// The item's own kind, not an assumption. A series' artwork and its details
	// live behind different provider endpoints than a film's, so hard-coding
	// "movie" here would have quietly fetched nothing for every series an
	// operator confirmed.
	kind := metadata.KindMovie
	if it, err := svc.items.ItemForIdentification(ctx, itemID); err == nil {
		kind = it.Kind
	}
	svc.attachExternalIDs(ctx, itemID, kind, chosen.ProviderID)
	svc.fetchArtwork(ctx, chosen.Match(kind))

	detail := fmt.Sprintf("identified as %q (%d, tmdb %d)",
		chosen.Title, chosen.Year, chosen.ProviderID)
	if newTitle != "" {
		detail += fmt.Sprintf("; the title changed from %q", before.ParsedTitle)
	}
	svc.write(ctx, actor, audit.ActionMediaIdentified, itemID, detail,
		map[string]any{"title": before.ParsedTitle, "state": string(before.State)},
		map[string]any{"title": chosen.Title, "tmdb_id": chosen.ProviderID})
	return chosen, nil
}

// Reject records that none of the candidates is right.
func (svc *Service) Reject(ctx context.Context, itemID int64, why string) error {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return err
	}
	actor := authz.FromContext(ctx)
	if _, err := svc.store.Get(ctx, itemID); err != nil {
		return err
	}
	if err := svc.store.Reject(ctx, itemID, actor.UserID, why); err != nil {
		return err
	}
	svc.write(ctx, actor, audit.ActionMediaIdentified, itemID,
		"none of the candidates was right: "+why, nil, nil)
	return nil
}

// Reopen clears a decision so the item can be identified again.
func (svc *Service) Reopen(ctx context.Context, itemID int64) error {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return err
	}
	actor := authz.FromContext(ctx)
	if _, err := svc.store.Get(ctx, itemID); err != nil {
		return err
	}
	if err := svc.store.Reopen(ctx, itemID); err != nil {
		return err
	}
	svc.write(ctx, actor, audit.ActionMediaIdentified, itemID,
		"reopened for identification", nil, nil)
	return nil
}

func (svc *Service) write(ctx context.Context, actor *authz.Principal,
	action audit.Action, itemID int64, detail string, before, after any) {

	if svc.audit == nil || actor == nil {
		return
	}
	_ = svc.audit.Write(ctx, audit.Event{
		ActorUserID: &actor.UserID,
		ActorLabel:  actor.Username,
		Action:      action,
		TargetKind:  "media_item",
		TargetID:    fmt.Sprintf("%d", itemID),
		Detail:      detail,
		Before:      before,
		After:       after,
	})
}

// CachePoster fetches a poster this software has already recorded a path for,
// and reports where it landed in the cache.
//
// # Why a browse-permission user may cause an outbound request
//
// The review screen is a person choosing between candidates by LOOKING at them,
// so its posters have to exist. Fetching all of them when a proposal is saved
// would mean downloading five images for every ambiguous item in a library
// whether or not anybody ever opens the screen; fetching on first view spends
// nothing until somebody actually looks.
//
// What keeps that safe is where the URL comes from. The caller passes a
// provider and an id; the remote path is read from the candidate row this
// software stored when the provider answered a search. An id with no such row
// is ErrNotRecorded and nothing is fetched. So a request can choose BETWEEN
// posters this instance already knows about, and cannot introduce one — and
// internal/artwork still refuses anything that is not a plain filename, and
// still writes through os.Root.
func (svc *Service) CachePoster(ctx context.Context, provider string, providerID int64) (string, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return "", err
	}
	if svc.art == nil {
		return "", metadata.ErrNoProvider
	}
	remote, err := svc.store.RecordedPosterPath(ctx, provider, providerID)
	if err != nil {
		return "", err
	}

	base := metadata.FallbackImageBase
	if t, ok := svc.provider().(*metadata.TMDB); ok && t.ImageBase() != "" {
		base = t.ImageBase()
	}
	return svc.art.Fetch(ctx, base, artwork.Ref{
		Kind: artwork.KindPoster, Provider: provider, ID: providerID,
		Size: "w342", RemotePath: remote,
	})
}

// OfferPosters records the posters a title search showed the caller
// (ADR-0070). The search itself checked what the caller may do.
func (svc *Service) OfferPosters(ctx context.Context, provider string, posters map[int64]string) error {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return err
	}
	return svc.store.OfferPosters(ctx, authz.FromContext(ctx).UserID, provider, posters)
}

// PosterOffered reports whether a title search showed the caller that poster.
func (svc *Service) PosterOffered(ctx context.Context, provider string, providerID int64) bool {
	p := authz.FromContext(ctx)
	if p == nil {
		return false
	}
	ok, err := svc.store.PosterOffered(ctx, p.UserID, provider, providerID)
	return err == nil && ok
}

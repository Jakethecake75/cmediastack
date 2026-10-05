package request

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// Service is the request surface with its authorization.
//
// # Who may see whose request
//
// This is the whole of the interesting authorization here, and it is not a
// permission check — it is a SCOPE. An ordinary user may list requests, and the
// listing they get must contain only their own; an approver's listing contains
// everybody's. Expressed as a permission alone ("may list requests") the
// difference disappears and everyone sees everything, which leaks what the
// other people on this instance are watching. So the scope is computed here,
// from the actor, and passed to the store as a filter the store cannot forget
// to apply — because without it the store returns everything and the caller
// would have to remember.
type Service struct {
	store *Store
	audit *audit.Logger
	now   func() time.Time
}

// NewService builds the service.
func NewService(store *Store, auditLog *audit.Logger, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, audit: auditLog, now: now}
}

// Store exposes the repository for wiring.
func (svc *Service) Store() *Store { return svc.store }

// MaxTitleLength bounds a submitted title. Long enough for any real title and
// short enough that the queue stays readable; a request is displayed to other
// people, so an unbounded string is somebody else's problem.
const MaxTitleLength = 200

// MaxNoteLength bounds the free-text note.
const MaxNoteLength = 500

// Submitted reports what happened to a submission.
type Submitted struct {
	Request *Request
	// Created is false when the submitter joined an existing open request.
	Created bool
	// AutoApproved is true when the submitter's own role approved it outright.
	AutoApproved bool
}

// Submit records a request.
//
// A holder of PermAutoApproveOwn has their own request approved on the spot.
// That is the permission's entire meaning and it is worth being precise about:
// it approves requests they SUBMIT, not requests they are looking at. Somebody
// who may skip the queue for themselves has not thereby been given authority
// over anybody else's request.
func (svc *Service) Submit(ctx context.Context, nr NewRequest) (Submitted, error) {
	if err := authz.RequirePermission(ctx, authz.PermSubmitRequest); err != nil {
		return Submitted{}, err
	}
	actor := authz.FromContext(ctx)

	nr.By = actor.UserID
	nr.Title = strings.TrimSpace(nr.Title)
	nr.Note = strings.TrimSpace(nr.Note)

	switch {
	case nr.Kind != KindMovie && nr.Kind != KindSeries:
		return Submitted{}, fmt.Errorf("%w: kind must be movie or series, got %q", ErrInvalid, nr.Kind)
	case nr.Title == "":
		return Submitted{}, fmt.Errorf("%w: a request needs a title", ErrNotAskable)
	case len(nr.Title) > MaxTitleLength:
		return Submitted{}, fmt.Errorf("%w: that title is longer than %d characters", ErrInvalid, MaxTitleLength)
	case len(nr.Note) > MaxNoteLength:
		return Submitted{}, fmt.Errorf("%w: that note is longer than %d characters", ErrInvalid, MaxNoteLength)
	case nr.Action != "" && nr.Action != ActionAdd && nr.Action != ActionRemove:
		return Submitted{}, fmt.Errorf("%w: action must be add or remove, got %q", ErrInvalid, nr.Action)
	case nr.Action == ActionRemove && nr.MediaItemID <= 0:
		return Submitted{}, fmt.Errorf("%w: a removal names the library item to remove", ErrInvalid)
	case nr.Scope != "" && nr.Kind != KindSeries:
		return Submitted{}, fmt.Errorf("%w: only a series has seasons and episodes to choose", ErrInvalid)
	}
	// A year outside this range is a typo, and a typo in the year is the one
	// field that silently splits a request off from the one it should have
	// joined. The upper bound is deliberately generous: people request films
	// that have been announced but not released.
	if nr.Year != 0 && (nr.Year < 1888 || nr.Year > svc.now().Year()+5) {
		return Submitted{}, fmt.Errorf("%w: %d does not look like a release year", ErrInvalid, nr.Year)
	}

	r, created, err := svc.store.Create(ctx, nr)
	if err != nil {
		return Submitted{}, err
	}

	out := Submitted{Request: r, Created: created}

	// Never a removal (ADR-0075): approving one deletes files, which is a
	// decision for whoever may delete them.
	if created && nr.Action != ActionRemove && actor.Role.Permissions.Has(authz.PermAutoApproveOwn) {
		if err := svc.store.Decide(ctx, r.ID, StateApproved, actor.UserID,
			"auto-approved: the requester's role may approve its own requests"); err != nil {
			return Submitted{}, err
		}
		out.AutoApproved = true
		if r, err = svc.store.ByID(ctx, r.ID); err != nil {
			return Submitted{}, err
		}
		out.Request = r
	}

	svc.write(ctx, audit.Event{
		Action:     audit.ActionRequestSubmitted,
		TargetKind: "media_request",
		TargetID:   fmt.Sprintf("%d", r.ID),
		Detail: fmt.Sprintf("%s %q (%s)", r.Kind, r.Title,
			describeSubmission(out)),
	})
	return out, nil
}

func describeSubmission(s Submitted) string {
	switch {
	case !s.Created:
		return "joined an existing open request"
	case s.AutoApproved:
		return "auto-approved"
	default:
		return "pending"
	}
}

// List returns the requests this actor may see.
func (svc *Service) List(ctx context.Context, states []State, limit int) ([]*Request, error) {
	if err := authz.RequirePermission(ctx, authz.PermSubmitRequest); err != nil {
		return nil, err
	}
	actor := authz.FromContext(ctx)

	f := Filter{States: states, Limit: limit}
	if !actor.Role.Permissions.Has(authz.PermApproveRequests) {
		// The scope, not a permission. Everything this actor opened or joined,
		// and nothing else.
		id := actor.UserID
		f.OnlyUser = &id
	}
	return svc.store.List(ctx, f)
}

// Visible loads one request if this actor may see it.
//
// Returns ErrNotFound rather than a denial for a request belonging to somebody
// else, matching the rest of the codebase: "you may not" and "there is no such
// thing" are the same answer, so the id space cannot be walked to learn what
// other people are watching.
func (svc *Service) Visible(ctx context.Context, id int64) (*Request, error) {
	if err := authz.RequirePermission(ctx, authz.PermSubmitRequest); err != nil {
		return nil, err
	}
	actor := authz.FromContext(ctx)

	r, err := svc.store.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if actor.Role.Permissions.Has(authz.PermApproveRequests) {
		return r, nil
	}
	if r.RequestedBy != nil && *r.RequestedBy == actor.UserID {
		return r, nil
	}
	for _, f := range r.Followers {
		if f.UserID == actor.UserID {
			return r, nil
		}
	}
	return nil, ErrNotFound
}

// Approve marks a request as something this instance is willing to acquire.
//
// It downloads nothing. See the migration's header comment: automatic
// fulfilment means other people's words causing acquisitions with no human
// choosing the release, and §13 puts the legality of what this instance
// acquires on the operator. Approval makes a request GRABBABLE; a person then
// searches and grabs for it.
func (svc *Service) Approve(ctx context.Context, id int64) (*Request, error) {
	return svc.decide(ctx, id, StateApproved, "")
}

// Deny closes a request with a reason.
//
// The reason is required. A denial with no explanation is the answer that makes
// people ask again, and the requester can see it.
func (svc *Service) Deny(ctx context.Context, id int64, reason string) (*Request, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("%w: a denial needs a reason; the requester sees it", ErrInvalid)
	}
	return svc.decide(ctx, id, StateDenied, reason)
}

func (svc *Service) decide(ctx context.Context, id int64, state State, reason string) (*Request, error) {
	if err := authz.RequirePermission(ctx, authz.PermApproveRequests); err != nil {
		return nil, err
	}
	actor := authz.FromContext(ctx)

	before, err := svc.store.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := svc.store.Decide(ctx, id, state, actor.UserID, reason); err != nil {
		return nil, err
	}
	after, err := svc.store.ByID(ctx, id)
	if err != nil {
		return nil, err
	}

	action := audit.ActionRequestApproved
	if state == StateDenied {
		action = audit.ActionRequestDenied
	}
	svc.write(ctx, audit.Event{
		Action:     action,
		TargetKind: "media_request",
		TargetID:   fmt.Sprintf("%d", id),
		Detail:     fmt.Sprintf("%s %q: %s", after.Kind, after.Title, reason),
		Before:     map[string]any{"state": string(before.State)},
		After:      map[string]any{"state": string(after.State), "reason": reason},
	})
	return after, nil
}

// CompleteRemoval closes an approved removal once its files are in the trash
// (ADR-0075). Gated on deleting media, the permission the removal used.
func (svc *Service) CompleteRemoval(ctx context.Context, id int64, detail string) error {
	if err := authz.RequirePermission(ctx, authz.PermDeleteMediaFiles); err != nil {
		return err
	}
	if err := svc.store.MarkFulfilled(ctx, id); err != nil {
		return err
	}
	svc.write(ctx, audit.Event{
		Action:     audit.ActionRequestFulfilled,
		TargetKind: "media_request",
		TargetID:   fmt.Sprintf("%d", id),
		Detail:     "removed: " + detail,
	})
	return nil
}

// LinkGrab records that an approved request is being downloaded for.
//
// Gated on PermManageQueue rather than on approving requests, because that is
// the permission that causes the instance to acquire something (ADR-0014) — and
// this is called from the grab path, where that has just happened.
func (svc *Service) LinkGrab(ctx context.Context, id int64, infoHash string) error {
	if err := authz.RequirePermission(ctx, authz.PermManageQueue); err != nil {
		return err
	}
	if err := svc.store.LinkGrab(ctx, id, infoHash); err != nil {
		return err
	}
	svc.write(ctx, audit.Event{
		Action:     audit.ActionRequestGrabbed,
		TargetKind: "media_request",
		TargetID:   fmt.Sprintf("%d", id),
		Detail:     "grabbed " + infoHash,
	})
	return nil
}

// Linked is what linking a request to a library item did.
type Linked struct {
	Request *Request
	// Previous is the item the request was linked to before, if it was.
	Previous *int64
	// Label is the item's title and year.
	Label string
	// Fulfilled is set when the item already had a file, so linking it
	// fulfilled the request.
	Fulfilled bool
}

// Link says which library item satisfies an approved request (ADR-0028).
//
// Gated on approving requests: saying which film a request meant is part of
// deciding it. It adds nothing to the library and acquires nothing — the item
// must already exist, added by somebody holding library.edit — and it moves a
// request to fulfilled only when the item already has a file, which is then
// simply true.
func (svc *Service) Link(ctx context.Context, id, itemID int64) (Linked, error) {
	if err := authz.RequirePermission(ctx, authz.PermApproveRequests); err != nil {
		return Linked{}, err
	}
	if itemID <= 0 {
		return Linked{}, fmt.Errorf("%w: media_item_id must be a library item's id", ErrInvalid)
	}
	li, err := svc.store.Link(ctx, id, itemID)
	if err != nil {
		return Linked{}, err
	}
	r, err := svc.store.ByID(ctx, id)
	if err != nil {
		return Linked{}, err
	}

	detail := fmt.Sprintf("%s %q is satisfied by library item %d, %s", r.Kind, r.Title, itemID, li.Label)
	var was any
	if li.Previous != nil {
		was = *li.Previous
		if *li.Previous != itemID {
			detail += fmt.Sprintf(" (it was linked to item %d)", *li.Previous)
		}
	}
	svc.write(ctx, audit.Event{
		Action:     audit.ActionRequestLinked,
		TargetKind: "media_request",
		TargetID:   fmt.Sprintf("%d", id),
		Detail:     detail,
		Before:     map[string]any{"media_item_id": was},
		After:      map[string]any{"media_item_id": itemID},
	})
	if li.Fulfilled {
		svc.write(ctx, audit.Event{
			Action:     audit.ActionRequestFulfilled,
			TargetKind: "media_request",
			TargetID:   fmt.Sprintf("%d", id),
			Detail:     fmt.Sprintf("fulfilled on linking: %s already has a file", li.Label),
		})
	}
	return Linked{Request: r, Previous: li.Previous, Label: li.Label, Fulfilled: li.Fulfilled}, nil
}

// write records an event with the acting principal filled in.
//
// A failure is swallowed for the same reason it is everywhere else in this
// package: a request is not a credential operation, and losing the log line is
// preferable to failing the operation the person asked for. Break-glass
// recovery is the one place that judgement goes the other way.
func (svc *Service) write(ctx context.Context, e audit.Event) {
	p := authz.FromContext(ctx)
	if svc.audit == nil || p == nil {
		return
	}
	e.ActorUserID = &p.UserID
	e.ActorLabel = p.Username
	_ = svc.audit.Write(ctx, e)
}

// FulfilFromImport closes out every request waiting on a completed download.
//
// # Why this takes no principal and checks no permission
//
// It is called by the importer, which runs from the scheduler with no person
// behind it. Unlike break-glass recovery — the other principal-free path, which
// is confined to the host console by a structural test — this one needs no such
// confinement, and the reason is what it can do rather than who calls it:
//
//   - It only ever moves approved -> fulfilled, and only for a request whose
//     info hash a person already grabbed. Both of those were human decisions
//     that already happened.
//   - It cannot create a request, approve one, acquire anything, or change who
//     may see what.
//
// So the worst an attacker who could drive it would achieve is marking an
// already-approved, already-downloading request as finished slightly early,
// which is a display error rather than an authority one. Guarding it with a
// permission would mean granting the importer authority over this table, and
// that grant would be worth more to an attacker than the thing it protects.
//
// Since ADR-0028 it also closes the requests an approver linked to the item the
// download became — whichever grab brought it — and the reasoning above holds
// unchanged: a linked request was approved by a person and linked by a person,
// and the import is the fact that its item now has a file.
func (svc *Service) FulfilFromImport(ctx context.Context, infoHash string, mediaItemID int64) (int64, error) {
	n, err := svc.store.Fulfil(ctx, infoHash, mediaItemID)
	if err != nil || n == 0 {
		return n, err
	}
	if svc.audit != nil {
		_ = svc.audit.Write(ctx, audit.Event{
			// No ActorUserID, for the same reason console recovery has none:
			// there is no account behind this, and attributing it to the person
			// who happened to grab would be a lie in the audit log.
			ActorLabel: "system:import",
			Action:     audit.ActionRequestFulfilled,
			TargetKind: "media_request",
			TargetID:   strings.ToLower(infoHash),
			Detail: fmt.Sprintf("%d request(s) fulfilled by media item %d",
				n, mediaItemID),
		})
	}
	return n, nil
}

// FulfilOnDisk closes every approved request linked to an item that now has a
// file (ADR-0028, decision 3). The library scan task calls it after each scan,
// for the file an import did not bring: one put in the item's folder by hand.
//
// It takes no principal, for FulfilFromImport's reasons: it only moves approved
// to fulfilled, for a request a person approved and a person linked, when a
// file is there — and it cannot create, approve, link or acquire anything.
func (svc *Service) FulfilOnDisk(ctx context.Context) (int64, error) {
	ids, err := svc.store.FulfilOnDisk(ctx)
	if err != nil {
		return 0, err
	}
	if svc.audit != nil {
		for _, id := range ids {
			_ = svc.audit.Write(ctx, audit.Event{
				// No account behind this, as for the import's line.
				ActorLabel: "system:scan",
				Action:     audit.ActionRequestFulfilled,
				TargetKind: "media_request",
				TargetID:   fmt.Sprintf("%d", id),
				Detail:     "fulfilled: a scan recorded a file of the library item it is linked to",
			})
		}
	}
	return int64(len(ids)), nil
}

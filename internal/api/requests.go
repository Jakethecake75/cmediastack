package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/request"
)

// The acquisition request surface: the Jellyseerr half of the brief.

// requestJSON renders a request.
//
// Note the key "note" carries the REQUESTER's words, not the server's. Every
// other endpoint in this API uses "note" for its own explanation to the
// operator, and this one cannot: a request object already has a note field that
// the person typed. The first version did use it for both, and the server's
// message silently overwrote the requester's — caught against the binary, where
// a request submitted with "rewatch night" came back saying "Waiting for
// approval." as though the requester had written it. The server's commentary is
// "message" here, and only here.
func requestJSON(r *request.Request, actor *authz.Principal) map[string]any {
	out := map[string]any{
		"id":           r.ID,
		"kind":         string(r.Kind),
		"title":        r.Title,
		"state":        string(r.State),
		"note":         r.Note,
		"requested_by": r.RequesterName,
		"requested_at": r.RequestedAt,
		"waiting":      len(r.Followers),
		"action":       string(r.Action),
	}
	if r.TMDBID > 0 {
		out["tmdb_id"] = r.TMDBID
	}
	if parts, err := request.ParseScope(r.Scope); err == nil && len(parts) > 0 {
		out["scope"] = parts
	}
	if r.Year > 0 {
		out["year"] = r.Year
	}
	if r.DecidedAt != nil {
		out["decided_at"] = r.DecidedAt
		out["decided_by"] = r.DeciderName
		// The requester sees why. A denial with no visible reason is the answer
		// that makes people ask again.
		out["decision_reason"] = r.DecisionReason
	}
	if r.InfoHash != "" {
		// Present means something was grabbed for it, which is the difference
		// between "approved and nothing is happening" and "approved and it is
		// downloading" — the question this whole surface exists to answer.
		out["info_hash"] = r.InfoHash
		out["grabbed_at"] = r.GrabbedAt
	}
	// The item an approver linked the request to (ADR-0028), or the one it was
	// fulfilled by: the state says which. Its NAME is added by withItem, for a
	// caller who may browse the library; the id alone says that there is one
	// and nothing about what it is.
	if r.MediaItemID != nil {
		out["media_item_id"] = *r.MediaItemID
	}
	if r.FulfilledAt != nil {
		out["fulfilled_at"] = r.FulfilledAt
	}

	// Who else is waiting is shown only to somebody who can act on the queue.
	// To everybody else it is a list of what the other people on this instance
	// are watching, which is not theirs to read.
	if actor != nil && actor.Role.Permissions.Has(authz.PermApproveRequests) {
		names := make([]string, 0, len(r.Followers))
		for _, f := range r.Followers {
			names = append(names, f.Username)
		}
		out["followers"] = names
	}
	return out
}

// withItem names the library item a request is linked to or was fulfilled by
// (ADR-0028, decision 4) — for a caller who may browse the library, because the
// name is a read of the library and a request must not be a way around that.
func withItem(ctx context.Context, body map[string]any, r *request.Request,
	actor *authz.Principal, names *itemNames) {

	if r.MediaItemID == nil || actor == nil || !actor.Role.Permissions.Has(authz.PermBrowse) {
		return
	}
	what := "series"
	if r.Kind == request.KindMovie {
		what = "film"
	}
	name, known := names.name(ctx, *r.MediaItemID, what)
	body["item"] = map[string]any{"id": *r.MediaItemID, "name": name, "known": known}
}

type submitRequest struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Year  int    `json:"year"`
	Note  string `json:"note"`
	// The provider's title chosen from a search, the seasons and episodes
	// wanted, and a removal of a library item (ADR-0075).
	TMDBID      int64          `json:"tmdb_id"`
	Scope       []request.Part `json:"scope"`
	Action      string         `json:"action"`
	MediaItemID int64          `json:"media_item_id"`
}

// SubmitRequest records a request to acquire something.
func (h *Handlers) SubmitRequest(w http.ResponseWriter, r *http.Request) {
	if h.requests == nil {
		writeProblem(w, http.StatusNotImplemented, "requests are not wired")
		return
	}
	var in submitRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	nr := request.NewRequest{
		Kind:  request.Kind(strings.ToLower(strings.TrimSpace(in.Kind))),
		Title: in.Title, Year: in.Year, Note: in.Note,
		TMDBID: in.TMDBID, Action: request.Action(strings.ToLower(strings.TrimSpace(in.Action))),
	}
	scope, err := request.FormatScope(in.Scope)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}
	nr.Scope = scope
	// A removal names a library item the asker can see, and is for what that
	// item is: its kind, title and year come from the library, not the body.
	if nr.Action == request.ActionRemove {
		if h.media == nil {
			writeProblem(w, http.StatusNotImplemented, "no library is wired")
			return
		}
		it, gerr := h.media.GetItem(r.Context(), in.MediaItemID)
		if errors.Is(gerr, importer.ErrItemNotFound) {
			writeProblem(w, http.StatusNotFound, "there is no such title in the library")
			return
		} else if gerr != nil {
			writeAuthzAware(w, gerr)
			return
		}
		if it.Kind != importer.KindMovie && it.Kind != importer.KindSeries {
			writeProblem(w, http.StatusBadRequest, "only a film or a series can be asked to be removed")
			return
		}
		nr.Kind, nr.Title, nr.Year, nr.MediaItemID, nr.TMDBID = request.Kind(it.Kind), it.Title, it.Year, it.ID, 0
	}

	res, err := h.requests.Submit(r.Context(), nr)
	switch {
	case errors.Is(err, request.ErrTooManyOpen):
		// 429 rather than 403: the actor is allowed, they have simply used
		// their share. The difference matters to somebody reading the message.
		writeProblem(w, http.StatusTooManyRequests,
			"you already have "+strconv.Itoa(request.MaxOpenPerUser)+
				" requests waiting. Ask about one of those, or wait for them to be decided.")
		return
	case errors.Is(err, request.ErrNotAskable), errors.Is(err, request.ErrInvalid):
		// 400, not 500. A missing field is the caller's mistake; rendering it as
		// a server fault sends them looking for an outage and buries a real one
		// among the noise.
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	actor := authz.FromContext(r.Context())
	body := requestJSON(res.Request, actor)
	withItem(r.Context(), body, res.Request, actor, h.itemNames())
	switch {
	case !res.Created:
		// 200, not 201: nothing was created. Saying so is the difference
		// between somebody understanding they joined a queue and somebody
		// re-requesting the same film four times.
		body["message"] = "Somebody had already asked for this, so you have been added " +
			"to that request rather than a second one being opened."
		writeJSON(w, http.StatusOK, body)
	case res.AutoApproved:
		body["message"] = "Approved on submission, because your role may approve its own requests. " +
			h.addForRequest(r.Context(), res.Request, 0)
		writeJSON(w, http.StatusCreated, body)
	case res.Request.Action == request.ActionRemove:
		body["message"] = "Asked. Whoever may delete files decides; nothing is removed until then."
		writeJSON(w, http.StatusCreated, body)
	default:
		body["message"] = "Waiting for approval."
		writeJSON(w, http.StatusCreated, body)
	}
}

// ListRequests returns the requests the caller may see.
func (h *Handlers) ListRequests(w http.ResponseWriter, r *http.Request) {
	if h.requests == nil {
		writeProblem(w, http.StatusNotImplemented, "requests are not wired")
		return
	}
	var states []request.State
	for _, s := range strings.Split(r.URL.Query().Get("state"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			states = append(states, request.State(s))
		}
	}

	items, err := h.requests.List(r.Context(), states, 200)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}

	actor := authz.FromContext(r.Context())
	names := h.itemNames()
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		body := requestJSON(it, actor)
		withItem(r.Context(), body, it, actor, names)
		out = append(out, body)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"requests": out,
		"count":    len(out),
		// Said explicitly rather than left to be inferred from the row count,
		// so a user does not wonder whether the list is short because nobody
		// has asked for anything or because they cannot see the rest.
		"scope": scopeWord(actor),
	})
}

func scopeWord(actor *authz.Principal) string {
	if actor != nil && actor.Role.Permissions.Has(authz.PermApproveRequests) {
		return "all requests on this instance"
	}
	return "your own requests"
}

type denyRequestBody struct {
	Reason string `json:"reason"`
	// RootFolderID is where an approved title is added when several root
	// folders could hold it (ADR-0075).
	RootFolderID int64 `json:"root_folder_id"`
}

// ApproveRequest marks a request as something this instance will acquire.
func (h *Handlers) ApproveRequest(w http.ResponseWriter, r *http.Request) {
	h.decideRequest(w, r, true)
}

// DenyRequest closes a request with a reason.
func (h *Handlers) DenyRequest(w http.ResponseWriter, r *http.Request) {
	h.decideRequest(w, r, false)
}

func (h *Handlers) decideRequest(w http.ResponseWriter, r *http.Request, approve bool) {
	if h.requests == nil {
		writeProblem(w, http.StatusNotImplemented, "requests are not wired")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "not an id")
		return
	}
	var in denyRequestBody
	if r.ContentLength > 0 && !decodeJSON(w, r, &in) {
		return
	}

	// A removal deletes files when it is approved (ADR-0075), so only somebody
	// who may delete them approves one. Asked before anything changes.
	if approve {
		if rq, verr := h.requests.Visible(r.Context(), id); verr == nil && rq.Action == request.ActionRemove &&
			!authz.FromContext(r.Context()).Has(authz.PermDeleteMediaFiles) {
			writeProblem(w, http.StatusForbidden,
				"approving a removal deletes files, which your role may not do; ask an administrator")
			return
		}
	}

	var out *request.Request
	if approve {
		out, err = h.requests.Approve(r.Context(), id)
	} else {
		out, err = h.requests.Deny(r.Context(), id, in.Reason)
	}
	switch {
	case errors.Is(err, request.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, request.ErrAlreadyDecided):
		// 409: somebody else got there first. Two approvers clicking at once is
		// ordinary, and the second should be told what happened rather than
		// shown a failure.
		writeProblem(w, http.StatusConflict,
			"that request has already been decided; reload to see by whom")
		return
	case errors.Is(err, request.ErrInvalid):
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	var msg string
	switch {
	case approve && out.Action == request.ActionRemove:
		var rerr error
		if msg, rerr = h.removeForRequest(r, out); rerr != nil {
			msg = "Approved, but the files could not be removed: " + rerr.Error()
		}
	case approve:
		msg = h.addForRequest(r.Context(), out, in.RootFolderID)
	}
	if fresh, ferr := h.requests.Visible(r.Context(), id); ferr == nil {
		out = fresh
	}
	body := requestJSON(out, authz.FromContext(r.Context()))
	withItem(r.Context(), body, out, authz.FromContext(r.Context()), h.itemNames())
	if approve {
		body["message"] = msg
	} else {
		body["message"] = "Denied. The requester can see the reason."
	}
	writeJSON(w, http.StatusOK, body)
}

type linkRequestBody struct {
	MediaItemID int64 `json:"media_item_id"`
}

// LinkRequest says which library item satisfies an approved request
// (ADR-0028). It adds nothing and acquires nothing: the item already exists,
// and the request is fulfilled when a file of it arrives — at once, if one
// already has.
func (h *Handlers) LinkRequest(w http.ResponseWriter, r *http.Request) {
	if h.requests == nil {
		writeProblem(w, http.StatusNotImplemented, "requests are not wired")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "not an id")
		return
	}
	var in linkRequestBody
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.MediaItemID <= 0 {
		writeProblem(w, http.StatusBadRequest,
			"media_item_id is required: the library item that satisfies this request")
		return
	}

	// Only to a title the approver can see (ADR-0037): a link names the title
	// back, and a hidden title must answer as a missing one does.
	if h.media != nil {
		if _, gerr := h.media.GetItem(r.Context(), in.MediaItemID); errors.Is(gerr, importer.ErrItemNotFound) {
			writeProblem(w, http.StatusUnprocessableEntity,
				"there is no library item "+strconv.FormatInt(in.MediaItemID, 10)+
					"; add the title to the library first")
			return
		}
	}

	res, err := h.requests.Link(r.Context(), id, in.MediaItemID)
	switch {
	case errors.Is(err, request.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case errors.Is(err, request.ErrNoSuchItem):
		writeProblem(w, http.StatusUnprocessableEntity,
			"there is no library item "+strconv.FormatInt(in.MediaItemID, 10)+
				"; add the title to the library first")
		return
	case errors.Is(err, request.ErrNotApproved):
		writeProblem(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, request.ErrWrongKind), errors.Is(err, request.ErrInvalid):
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	actor := authz.FromContext(r.Context())
	names := h.itemNames()
	body := requestJSON(res.Request, actor)
	withItem(r.Context(), body, res.Request, actor, names)
	// The sentence names the item only to an approver who may browse the
	// library, like the request's own answer does (ADR-0028, decision 4): a
	// link must not be a way to read titles by id.
	browse := actor != nil && actor.Role.Permissions.Has(authz.PermBrowse)
	label := res.Label
	if !browse {
		label = "library item " + strconv.FormatInt(in.MediaItemID, 10)
	}
	msg := "Linked to " + label + ". The request is fulfilled when a file of it " +
		"arrives: search for it from its page or from Wanted."
	if res.Fulfilled {
		msg = "Linked to " + label + ", which is already on disk, so the request is fulfilled."
	}
	if res.Previous != nil && *res.Previous != in.MediaItemID {
		was := "library item " + strconv.FormatInt(*res.Previous, 10)
		if browse {
			what := "series"
			if res.Request.Kind == request.KindMovie {
				what = "film"
			}
			was, _ = names.name(r.Context(), *res.Previous, what)
		}
		msg += " It was linked to " + was + " before."
	}
	body["message"] = msg
	writeJSON(w, http.StatusOK, body)
}

// RequestService is the acquisition-request surface the API needs.
//
// Scoping lives behind this interface, not in the handlers: List returns what
// the CALLER may see, decided from the principal in the context. A handler that
// had to remember to filter would eventually forget, and the failure would be
// every user reading every other user's watchlist.
type RequestService interface {
	Submit(ctx context.Context, nr request.NewRequest) (request.Submitted, error)
	List(ctx context.Context, states []request.State, limit int) ([]*request.Request, error)
	Visible(ctx context.Context, id int64) (*request.Request, error)
	Approve(ctx context.Context, id int64) (*request.Request, error)
	Deny(ctx context.Context, id int64, reason string) (*request.Request, error)
	LinkGrab(ctx context.Context, id int64, infoHash string) error
	// Link says which library item satisfies an approved request (ADR-0028).
	Link(ctx context.Context, id, itemID int64) (request.Linked, error)
	// CompleteRemoval closes an approved removal once it is done (ADR-0075).
	CompleteRemoval(ctx context.Context, id int64, detail string) error
}

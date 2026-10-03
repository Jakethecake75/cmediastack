package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/indexer"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// TicketSealer mints and opens the opaque identity of a search candidate.
type TicketSealer interface {
	Seal(c search.Candidate, userID int64) (string, error)
	Open(token string, userID int64) (search.Ticket, error)
}

// GrabService turns an opened ticket into bytes or a magnet.
type GrabService interface {
	Grab(ctx context.Context, tk search.Ticket) (search.Grabbed, error)
}

type grabRequest struct {
	Ticket string `json:"ticket"`
	// RequestID optionally links this grab to an approved acquisition request,
	// so the person who asked for it learns that something is happening. Zero
	// means an ordinary grab with nobody waiting on it.
	RequestID int64 `json:"request_id"`
}

// GrabRelease downloads the release a ticket identifies.
//
// The client sends a ticket, never a URL. That is the whole design: if this
// endpoint took a URL, anyone who could reach it could make the server fetch a
// destination of their choosing through the egress-guarded HTTP client, and
// every check upstream — the indexer allowlist, the profile, the quality
// ladder, the audit line naming a release — would describe something that was
// never downloaded.
//
// The ticket proves the client did not CHANGE what an indexer offered. It is
// not a claim that the URL is safe, so the fetch path validates it again and
// revalidates every redirect hop; and it is not a standing capability, so the
// indexer is re-resolved against present state before anything is fetched.
func (h *Handlers) GrabRelease(w http.ResponseWriter, r *http.Request) {
	switch {
	case h.tickets == nil:
		writeProblem(w, http.StatusNotImplemented, "no ticket sealer is wired")
		return
	case h.grabs == nil:
		writeProblem(w, http.StatusNotImplemented, "no search service is wired")
		return
	case h.downloads == nil:
		writeProblem(w, http.StatusNotImplemented,
			"the download engine is not running (download.enabled is false in the configuration)")
		return
	}

	p := authz.FromContext(r.Context())
	if p == nil {
		// Unreachable through the router, which authenticates first. Checked
		// anyway because everything below attributes an action to this user.
		writeProblem(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var in grabRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	tk, err := h.tickets.Open(in.Ticket, p.UserID)
	switch {
	case errors.Is(err, search.ErrTicketExpired):
		writeProblem(w, http.StatusGone,
			"these search results have expired; run the search again")
		return
	case err != nil:
		writeProblem(w, http.StatusBadRequest, "that is not a valid grab ticket")
		return
	}

	grabbed, err := h.grabs.Grab(r.Context(), tk)
	if err != nil {
		h.auditGrab(r, p, tk, audit.OutcomeFailure, "", err.Error())
		writeGrabError(w, err)
		return
	}

	// Handed to the engine as a payload the SERVER fetched, never as a
	// caller-supplied URI. The metadata rides along so the queue can say what
	// this is and who asked for it without re-deriving either.
	meta := download.Meta{
		Title:       tk.Title,
		IndexerID:   grabbed.IndexerID,
		IndexerName: grabbed.IndexerName,
		AddedBy:     &p.UserID,
		AddedLabel:  p.Username,
		// Copied onto the row rather than joined at seeding time. The
		// obligation was incurred under the rules in force at the grab, and
		// editing an indexer afterwards must not silently rewrite what was
		// already promised to a tracker.
		SeedRatio: grabbed.SeedRatio,
		SeedTime:  grabbed.SeedTime,
	}
	// What the release was grabbed FOR, straight from the sealed ticket — not
	// from the request body, where a client could attach any release to any
	// episode or film (ADR-0023, ADR-0026). The importer files the download
	// under this item.
	if tk.Target != nil {
		meta.Target = &download.Target{
			ItemID: tk.Target.ItemID, Season: tk.Target.Season, Episode: tk.Target.Episode,
			Film: tk.Target.Film, Pack: tk.Target.Pack, LastSeason: tk.Target.LastSeason,
			Album: tk.Target.Album, Book: tk.Target.Book,
		}
	}
	var transfer download.Transfer
	if grabbed.Magnet != "" {
		transfer, err = h.downloads.AddMagnet(r.Context(), grabbed.Magnet, meta)
	} else {
		transfer, err = h.downloads.AddTorrent(grabbed.Torrent, meta)
	}
	if err != nil {
		h.auditGrab(r, p, tk, audit.OutcomeFailure, "", err.Error())
		if errors.Is(err, download.ErrEngineClosed) {
			writeProblem(w, http.StatusServiceUnavailable, "the download engine is shutting down")
			return
		}
		writeProblem(w, http.StatusBadRequest, "the release could not be added to the queue")
		return
	}

	if err := h.downloads.Start(transfer.InfoHash); err != nil {
		// Not an error for the caller. A magnet has no metadata yet, so there
		// is nothing to start downloading until it resolves; the transfer is
		// queued and the engine begins when it can.
		_ = err
	}

	h.auditGrab(r, p, tk, audit.OutcomeSuccess, transfer.InfoHash, grabbed.IndexerName)

	body := map[string]any{
		"info_hash":     transfer.InfoHash,
		"title":         tk.Title,
		"indexer":       grabbed.IndexerName,
		"have_metadata": transfer.MetadataGot,
		"note": "The transfer is queued. A magnet link has no metadata until peers " +
			"supply it, so size and progress appear once it resolves.",
	}
	if meta.Target != nil {
		body["for"] = h.queueNames().forDownload(r.Context(), meta.Target)
	}

	// Linked AFTER the transfer exists, and a failure here does not fail the
	// grab: the download is already running, and reporting failure would invite
	// the operator to grab again and acquire the same thing twice. The
	// link is reported in the body instead, so a link that did not happen is
	// visible rather than assumed.
	if in.RequestID > 0 && h.requests != nil {
		if err := h.requests.LinkGrab(r.Context(), in.RequestID, transfer.InfoHash); err != nil {
			body["request_link"] = "failed: " + err.Error()
			body["note"] = "The transfer is queued, but it could not be linked to " +
				"request " + strconv.FormatInt(in.RequestID, 10) +
				". The download is running; the requester will not be told automatically."
		} else {
			body["request_id"] = in.RequestID
			body["request_link"] = "linked"
		}
	}

	writeJSON(w, http.StatusAccepted, body)
}

// writeGrabError maps a grab failure onto a status an operator can act on.
//
// The distinctions matter: "your indexer is disabled" and "that indexer
// refused us" and "the link was not safe to follow" are three different jobs
// for whoever is reading, and collapsing them into 500 sends them looking in
// the wrong place.
func writeGrabError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, search.ErrIndexerGone):
		writeProblem(w, http.StatusConflict,
			"the indexer this release came from is no longer enabled")
	case errors.Is(err, search.ErrGrabUnavailable):
		writeProblem(w, http.StatusNotImplemented, "this instance cannot grab")
	case errors.Is(err, indexer.ErrUnsafeURL):
		writeProblem(w, http.StatusBadGateway,
			"the indexer's download link was not safe to follow and was refused")
	case errors.Is(err, indexer.ErrIndexerAuth):
		writeProblem(w, http.StatusBadGateway, "the indexer rejected our API key")
	case errors.Is(err, indexer.ErrIndexerRefused):
		writeProblem(w, http.StatusBadGateway, "the indexer refused the download")
	case errors.Is(err, indexer.ErrResponseTooLarge):
		writeProblem(w, http.StatusBadGateway, "the indexer's response exceeded the size cap")
	case errors.Is(err, indexer.ErrMalformed):
		writeProblem(w, http.StatusBadGateway, "the indexer did not return a torrent")
	default:
		writeProblem(w, http.StatusBadGateway, "the release could not be fetched")
	}
}

// auditGrab records who caused a download and what it was.
//
// A grab is the moment this software reaches out and acquires something, which
// makes it the single most important line in the log for an operator who is
// answerable for what their instance holds. It is written on failure too: an
// attempt that was refused is exactly what someone reviewing the log wants to
// see.
func (h *Handlers) auditGrab(r *http.Request, p *authz.Principal, tk search.Ticket,
	outcome audit.Outcome, infoHash, detail string) {

	if h.audit == nil || p == nil {
		return
	}
	target := infoHash
	if target == "" {
		target = tk.InfoHash
	}
	_ = h.audit.Write(r.Context(), audit.Event{
		ActorUserID: &p.UserID,
		ActorLabel:  p.Username,
		Action:      audit.ActionReleaseGrabbed,
		Outcome:     outcome,
		TargetKind:  "release",
		TargetID:    target,
		SourceIP:    ClientIP(r.Context()),
		UserAgent:   r.UserAgent(),
		// The release title, not the download URL: the URL carries the
		// indexer's API key in its query string on many trackers, and an audit
		// log is the least-guarded copy of anything.
		Detail: tk.Title + " | " + detail,
	})
}

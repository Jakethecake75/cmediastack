package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identity"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// Who may see what of the library (ADR-0037): a title's rating, set by a
// person, and an account's libraries and ceiling, changed by an administrator.

type ratingRequest struct {
	// Certification is the rating ("PG-13", "TV-MA"), or null for the
	// provider's.
	Certification *string `json:"certification"`
}

// ratingJSON says a title's rating, or that it has none — which hides it from
// every account with a ceiling, and a person deciding whether to rate it by
// hand needs to know that.
func ratingJSON(it importer.Item) map[string]any {
	if it.RatingRank == 0 {
		return map[string]any{"rated": false, "rank": 0,
			"note": "Unrated: hidden from every account with a rating ceiling."}
	}
	return map[string]any{"rated": true, "certification": it.Certification,
		"rank": it.RatingRank, "source": it.RatingSource}
}

// SetTitleRating is a person rating a title by hand, or giving it back to the
// provider's rating. Audited: a rating decides who can see a title.
func (h *Handlers) SetTitleRating(w http.ResponseWriter, r *http.Request) {
	if h.media == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in ratingRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	cert := ""
	if in.Certification != nil {
		cert = strings.TrimSpace(*in.Certification)
	}

	before, item, err := h.media.SetRating(r.Context(), id, cert)
	switch {
	case errors.Is(err, importer.ErrUnknownCertification):
		writeProblem(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "importer: "))
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	was, now := describeRating(before), describeRating(item)
	if h.audit != nil {
		if p := authz.FromContext(r.Context()); p != nil {
			_ = h.audit.Write(r.Context(), audit.Event{
				ActorUserID: &p.UserID, ActorLabel: p.Username,
				Action:     audit.ActionMediaRatingChanged,
				Outcome:    audit.OutcomeSuccess,
				TargetKind: "media_item", TargetID: fmt.Sprint(item.ID),
				SourceIP:  ClientIP(r.Context()),
				UserAgent: r.UserAgent(),
				Detail:    fmt.Sprintf("%s: %s → %s", filmName(item), was, now),
				Before: map[string]any{"certification": before.Certification,
					"source": before.RatingSource},
				After: map[string]any{"certification": item.Certification,
					"source": item.RatingSource},
			})
		}
	}
	note := "Rated " + item.Certification + " by hand. The provider's rating will not replace it."
	if cert == "" {
		note = "Given back to the provider: it is asked on the next run of metadata.ratings, " +
			"and until then the title is unrated."
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": item.ID, "name": filmName(item),
		"rating": ratingJSON(item), "note": note})
}

func describeRating(it importer.Item) string {
	if it.RatingRank == 0 {
		return "unrated"
	}
	if it.RatingSource == "person" {
		return it.Certification + " (by hand)"
	}
	return it.Certification
}

type accessRequest struct {
	// AllLibraries is required: this is the one form where the choice is
	// always made explicitly.
	AllLibraries  *bool   `json:"all_libraries"`
	LibraryIDs    []int64 `json:"library_ids"`
	RatingCeiling int     `json:"rating_ceiling"`
}

// SetUserAccess changes which libraries an existing account sees and its
// rating ceiling (ADR-0037, decision 5).
func (h *Handlers) SetUserAccess(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in accessRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.AllLibraries == nil {
		writeProblem(w, http.StatusBadRequest,
			"all_libraries is required: true for every library, or false with library_ids")
		return
	}
	grant, err := identity.NormaliseGrant(in.AllLibraries, in.LibraryIDs, in.RatingCeiling)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "identity: "))
		return
	}
	err = h.svc.SetAccess(r.Context(), id, grant, ClientIP(r.Context()), r.UserAgent())
	switch {
	case errors.Is(err, identity.ErrNoSuchRootFolder):
		writeProblem(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "identity: "))
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"updated":        id,
		"all_libraries":  grant.AllLibraries,
		"library_ids":    nonNilIDs(grant.RootFolderIDs),
		"rating_ceiling": grant.RatingCeiling,
		"summary":        identity.DescribeGrant(grant),
		"note": "Applies from the account's next request, to its sessions and its API tokens. " +
			"A title outside it reads as not in the library.",
	})
}

// RatingCeilings lists the ceilings an approver can choose from, with what each
// admits.
func ratingCeilingsJSON() []map[string]any {
	out := make([]map[string]any, 0, authz.MaxRatingRank+1)
	for rank := 0; rank <= authz.MaxRatingRank; rank++ {
		out = append(out, map[string]any{"rank": rank, "label": library.CeilingLabels[rank]})
	}
	return out
}

func nonNilIDs(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}

package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
)

// A title's own quality profile (ADR-0035).

type titleProfileRequest struct {
	// ProfileID is the profile, or null (or 0) for the instance's default.
	ProfileID *int64 `json:"profile_id"`
}

// SetTitleProfile names the profile a title's searches, and automatic
// acquisition, judge it by — or puts it back on the default. Audited with what
// it was and what it became: it changes what the instance downloads with nobody
// looking.
func (h *Handlers) SetTitleProfile(w http.ResponseWriter, r *http.Request) {
	if h.media == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in titleProfileRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	var want int64
	if in.ProfileID != nil {
		want = *in.ProfileID
	}
	if want < 0 {
		writeProblem(w, http.StatusBadRequest, "profile_id is a profile's id, or null for the default")
		return
	}
	before, err := h.media.GetItem(r.Context(), id)
	if errors.Is(err, importer.ErrItemNotFound) {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeAuthzAware(w, err)
		return
	}

	item, err := h.media.SetQualityProfile(r.Context(), id, want)
	switch {
	case errors.Is(err, importer.ErrItemNotFound), errors.Is(err, importer.ErrNoSuchProfile):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	was, now := h.profileName(r, before.QualityProfileID), h.profileName(r, item.QualityProfileID)
	if h.audit != nil {
		if p := authz.FromContext(r.Context()); p != nil {
			_ = h.audit.Write(r.Context(), audit.Event{
				ActorUserID: &p.UserID, ActorLabel: p.Username,
				Action:     audit.ActionMediaProfileChanged,
				Outcome:    audit.OutcomeSuccess,
				TargetKind: "media_item", TargetID: fmt.Sprint(item.ID),
				SourceIP:  ClientIP(r.Context()),
				UserAgent: r.UserAgent(),
				Detail:    fmt.Sprintf("%s: %s → %s", filmName(item), was, now),
				Before:    map[string]any{"quality_profile_id": before.QualityProfileID},
				After:     map[string]any{"quality_profile_id": item.QualityProfileID},
			})
		}
	}
	body := map[string]any{"id": item.ID, "name": filmName(item), "profile": now,
		"note": "Its searches, and automatic acquisition, judge it by " + now + "."}
	if item.QualityProfileID > 0 {
		body["quality_profile_id"] = item.QualityProfileID
	}
	writeJSON(w, http.StatusOK, body)
}

// profileName is what a title's profile is called, as a person reads it: the
// default is said to be the default.
func (h *Handlers) profileName(r *http.Request, id int64) string {
	if id > 0 && h.profiles != nil {
		if sp, err := h.profiles.Get(r.Context(), id); err == nil {
			return sp.Profile.Name
		}
		return fmt.Sprintf("profile %d", id)
	}
	if h.profiles != nil {
		if sp, found, err := h.profiles.Default(r.Context()); err == nil && found {
			return "the default (" + sp.Profile.Name + ")"
		}
	}
	return "the default"
}

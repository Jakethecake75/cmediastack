package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/release"
	"github.com/jakethecake75/cmediastack/internal/search"
)

// SearchService is the subset of the search subsystem the API needs.
type SearchService interface {
	Search(ctx context.Context, req search.Request) (search.Response, error)
	// SearchEpisode searches for one episode and judges every candidate
	// against it (ADR-0023).
	SearchEpisode(ctx context.Context, es search.EpisodeSearch) (search.Response, error)
	// SearchFilm searches for one film and judges every candidate against it
	// (ADR-0026).
	SearchFilm(ctx context.Context, fs search.FilmSearch) (search.Response, error)
	// SearchSeason searches for one season and judges every candidate
	// against it (ADR-0033).
	SearchSeason(ctx context.Context, ss search.SeasonSearch) (search.Response, error)
	// SearchAlbum searches for one album and judges every candidate against
	// it (ADR-0046).
	SearchAlbum(ctx context.Context, as search.AlbumSearch) (search.Response, error)
	// SearchBook searches for one book and judges every candidate against it
	// (ADR-0049).
	SearchBook(ctx context.Context, bs search.BookSearch) (search.Response, error)
}

// ProfileSource loads quality profiles, and the default one (ADR-0027).
type ProfileSource interface {
	Get(ctx context.Context, id int64) (release.StoredProfile, error)
	List(ctx context.Context) ([]release.StoredProfile, error)
	Default(ctx context.Context) (release.StoredProfile, bool, error)
	SetDefault(ctx context.Context, id int64) (string, error)
}

type releaseSearchRequest struct {
	Term       string  `json:"term"`
	Season     *int    `json:"season"`
	Episode    int     `json:"episode"`
	IMDBID     string  `json:"imdb_id"`
	TVDBID     string  `json:"tvdb_id"`
	Categories []int   `json:"categories"`
	IndexerIDs []int64 `json:"indexer_ids"`
	// ProfileID judges the results: absent means the instance's default
	// profile, 0 means none — the person judges — and anything else that
	// profile (ADR-0027). A pointer, because absent and 0 mean different
	// things.
	ProfileID *int64 `json:"profile_id"`
}

// SearchReleases runs an interactive search across the configured indexers.
//
// This is the first place the whole acquisition path is visible to a person:
// indexers answer, release names are parsed, a profile judges, and the answer
// says what was refused and why.
func (h *Handlers) SearchReleases(w http.ResponseWriter, r *http.Request) {
	if h.search == nil {
		writeProblem(w, http.StatusNotImplemented, "no search service is wired")
		return
	}
	var in releaseSearchRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	// A film has no season. The field is a pointer so that "absent" and
	// "season zero" — which is real, it is where specials live — stay
	// distinguishable.
	season := -1
	if in.Season != nil {
		season = *in.Season
	}

	req := search.Request{
		Term: in.Term, Season: season, Episode: in.Episode,
		IMDBID: in.IMDBID, TVDBID: in.TVDBID,
		Categories: in.Categories, IndexerIDs: in.IndexerIDs,
	}

	judged, ok := h.resolveProfile(w, r, in.ProfileID, 0)
	if !ok {
		return
	}
	req.Profile = judged.profile

	p := authz.FromContext(r.Context())

	resp, err := h.search.Search(r.Context(), req)
	switch {
	case errors.Is(err, search.ErrNoIndexers):
		writeProblem(w, http.StatusConflict, "no indexers are enabled")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	candidates := make([]map[string]any, 0, len(resp.Candidates))
	for _, c := range resp.Candidates {
		entry := candidateJSON(c)
		// The download URL is deliberately absent. A grab happens through a
		// separate, audited endpoint that takes the candidate's identity, not a
		// URL the client hands back — otherwise the client chooses what gets
		// downloaded and every check above becomes advisory.
		//
		// That identity is this ticket: the URL sealed under the instance key
		// and bound to this user, so it can be handed back unchanged and
		// nothing else can be. Only accepted candidates get one — offering a
		// ticket for a release the profile refused invites a UI to provide a
		// button that quietly overrides the policy.
		if c.Accepted && h.tickets != nil && p != nil {
			if tok, terr := h.tickets.Seal(c, p.UserID); terr == nil {
				entry["ticket"] = tok
			}
		}
		candidates = append(candidates, entry)
	}

	outcomes := outcomesJSON(resp.Outcomes)

	body := map[string]any{
		"candidates": candidates,
		"count":      len(candidates),
		"accepted":   len(resp.Accepted()),
		"indexers":   outcomes,
		"queried":    resp.Queried,
		"failed":     resp.Failed,
		"partial":    resp.Partial(),
		"elapsed_ms": resp.Elapsed.Milliseconds(),
	}
	judged.describe(body)
	if resp.Partial() {
		// Said in words as well as in a boolean. A caller that renders the list
		// and ignores the flag shows an operator four results without
		// mentioning that three indexers timed out, and they conclude the
		// release does not exist.
		body["warning"] = "some indexers did not answer; these results are incomplete"
	}
	writeJSON(w, http.StatusOK, body)
}

// candidateJSON is the wire shape of one search candidate, without its ticket:
// whoever renders it decides whether it may carry one.
func candidateJSON(c search.Candidate) map[string]any {
	entry := map[string]any{
		"title":        c.Title,
		"indexer":      c.IndexerName,
		"seen_on":      c.SeenOn,
		"size":         c.Size,
		"seeders":      c.Seeders,
		"leechers":     c.Leechers,
		"published_at": c.PublishedAt,
		"info_url":     c.InfoURL,
		"quality":      c.Quality.Name,
		"accepted":     c.Accepted,
		"score":        c.Score,
		"parsed": map[string]any{
			"title":       c.Parsed.Title,
			"year":        c.Parsed.Year,
			"season":      c.Parsed.Season,
			"episodes":    c.Parsed.Episodes,
			"full_season": c.Parsed.FullSeason,
			"resolution":  string(c.Parsed.Resolution),
			"source":      string(c.Parsed.Source),
			"codec":       string(c.Parsed.Codec),
			"audio":       string(c.Parsed.Audio),
			"hdr":         c.Parsed.HDR,
			"editions":    c.Parsed.Editions,
			"revision":    c.Parsed.Revision,
			"proper":      c.Parsed.Proper,
			"group":       c.Parsed.Group,
			"confidence":  string(c.Parsed.Confidence),
			// What the parser did NOT understand. An operator looking at a
			// wrong decision needs this more than they need the fields it
			// did get right.
			"unmatched": c.Parsed.Unmatched,
		},
	}
	if c.Rejection != nil {
		entry["rejected_because"] = c.Rejection.Detail
		entry["rejection_reason"] = c.Rejection.Reason
	}
	return entry
}

// outcomesJSON is what each indexer did.
func outcomesJSON(outs []search.IndexerOutcome) []map[string]any {
	outcomes := make([]map[string]any, 0, len(outs))
	for _, o := range outs {
		entry := map[string]any{
			"indexer": o.IndexerName, "results": o.Results,
			"duration_ms": o.Duration.Milliseconds(),
		}
		if o.Err != "" {
			entry["error"] = o.Err
		}
		outcomes = append(outcomes, entry)
	}
	return outcomes
}

// ListQualityProfiles returns the configured profiles.
func (h *Handlers) ListQualityProfiles(w http.ResponseWriter, r *http.Request) {
	if h.profiles == nil {
		writeProblem(w, http.StatusNotImplemented, "no profile store is wired")
		return
	}
	profiles, err := h.profiles.List(r.Context())
	if err != nil {
		writeAuthzAware(w, err)
		return
	}

	out := make([]map[string]any, 0, len(profiles))
	var defaultID int64
	for _, sp := range profiles {
		terms := make([]map[string]any, 0, len(sp.Profile.Preferred))
		for _, t := range sp.Profile.Preferred {
			terms = append(terms, map[string]any{"term": t.Term, "score": t.Score})
		}
		if sp.Default {
			defaultID = sp.ID
		}
		out = append(out, map[string]any{
			"id": sp.ID, "name": sp.Profile.Name, "builtin": sp.Builtin,
			"default": sp.Default,
			"allowed": sp.Profile.Allowed, "cutoff": sp.Profile.Cutoff,
			"preferred": terms,
			"required":  sp.Profile.Required, "forbidden": sp.Profile.Forbidden,
		})
	}
	// default_id is 0 when the instance has no default: every search is then
	// unjudged unless a profile is chosen (ADR-0027).
	writeJSON(w, http.StatusOK, map[string]any{"profiles": out, "count": len(out), "default_id": defaultID})
}

// judgedBy is the profile a search was judged by, and how it came to be.
type judgedBy struct {
	profile   *release.Profile
	id        int64
	name      string
	isDefault bool
	// asked is false when the request named no profile, so the title's
	// profile, the default, or their absence decided.
	asked bool
	// fromTitle is true when it was the title's own profile (ADR-0035).
	fromTitle bool
}

// describe says in the answer which profile judged it, and whether that was
// the default, so a screen can show it rather than leave it to be guessed.
func (j judgedBy) describe(body map[string]any) {
	body["profile"] = j.name
	body["profile_id"] = j.id
	body["profile_default"] = j.isDefault
	body["profile_title"] = j.fromTitle
	if j.profile == nil && !j.asked {
		body["profile_note"] = "No profile judged these: the instance has no default " +
			"profile and none was chosen, so every release that matches can be grabbed."
	}
}

// resolveProfile reads a search's profile_id by ADR-0027's rule, one step
// longer for a search for a title (ADR-0035): absent is the title's profile
// when it names one, otherwise the default; 0 is none; anything else is that
// profile. titleProfile is zero for the general search and for a title on the
// default. ok is false when the answer has already been written.
func (h *Handlers) resolveProfile(w http.ResponseWriter, r *http.Request, requested *int64,
	titleProfile int64) (judgedBy, bool) {
	switch {
	case requested != nil && *requested == 0:
		return judgedBy{asked: true}, true
	case requested != nil && *requested < 0:
		writeProblem(w, http.StatusBadRequest, "profile_id is a profile's id, or 0 for none")
		return judgedBy{}, false
	case h.profiles == nil && requested == nil:
		// Nothing to take a default from: unjudged, and said so.
		return judgedBy{}, true
	case h.profiles == nil:
		writeProblem(w, http.StatusNotImplemented, "no profile store is wired")
		return judgedBy{}, false
	}

	var sp release.StoredProfile
	var err error
	if requested == nil && titleProfile > 0 {
		sp, err = h.profiles.Get(r.Context(), titleProfile)
		switch {
		case err == nil:
			return judgedBy{profile: &sp.Profile, id: sp.ID, name: sp.Profile.Name,
				isDefault: sp.Default, fromTitle: true}, true
		case !errors.Is(err, release.ErrProfileNotFound):
			writeAuthzAware(w, err)
			return judgedBy{}, false
		}
		// Gone since the title was read: the default, as deleting it would
		// have made it.
	}
	if requested == nil {
		var found bool
		sp, found, err = h.profiles.Default(r.Context())
		if err == nil && !found {
			return judgedBy{}, true
		}
	} else {
		sp, err = h.profiles.Get(r.Context(), *requested)
	}
	switch {
	case errors.Is(err, release.ErrProfileNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
		return judgedBy{}, false
	case err != nil:
		writeAuthzAware(w, err)
		return judgedBy{}, false
	}
	return judgedBy{profile: &sp.Profile, id: sp.ID, name: sp.Profile.Name,
		isDefault: sp.Default, asked: requested != nil}, true
}

type defaultProfileRequest struct {
	// ProfileID is the profile to make the default, or 0 for none. Required:
	// an empty body is not a choice.
	ProfileID *int64 `json:"profile_id"`
}

// SetDefaultProfile chooses the profile every search is judged by when none is
// chosen — or, with 0, none (ADR-0027). Audited as a changed setting, with what
// it was and what it became.
func (h *Handlers) SetDefaultProfile(w http.ResponseWriter, r *http.Request) {
	if h.profiles == nil {
		writeProblem(w, http.StatusNotImplemented, "no profile store is wired")
		return
	}
	var in defaultProfileRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.ProfileID == nil {
		writeProblem(w, http.StatusBadRequest, "profile_id is required: a profile's id, or 0 for none")
		return
	}

	was, err := h.profiles.SetDefault(r.Context(), *in.ProfileID)
	switch {
	case errors.Is(err, release.ErrProfileNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	now := "none"
	if *in.ProfileID > 0 {
		if sp, gerr := h.profiles.Get(r.Context(), *in.ProfileID); gerr == nil {
			now = sp.Profile.Name
		}
	}
	if was == "" {
		was = "none"
	}
	if p := authz.FromContext(r.Context()); h.audit != nil && p != nil {
		_ = h.audit.Write(r.Context(), audit.Event{
			ActorUserID: &p.UserID,
			ActorLabel:  p.Username,
			Action:      audit.ActionSystemSettingChanged,
			Outcome:     audit.OutcomeSuccess,
			TargetKind:  "setting",
			TargetID:    "quality_profile.default",
			SourceIP:    ClientIP(r.Context()),
			UserAgent:   r.UserAgent(),
			Detail:      "the default quality profile is now " + now + " (was " + was + ")",
		})
	}

	note := "Searches that name no profile are judged by " + now + "."
	if now == "none" {
		note = "Searches that name no profile are now unjudged: every release that matches " +
			"can be grabbed unless a profile is chosen."
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"default_id": *in.ProfileID, "default": now, "previous": was, "note": note,
	})
}

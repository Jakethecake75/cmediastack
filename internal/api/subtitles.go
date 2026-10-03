package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/subtitles"
)

// Fetching a subtitle from OpenSubtitles (ADR-0055).

// SubtitleService is what the subtitle routes need.
type SubtitleService interface {
	Status(ctx context.Context) (subtitles.Status, error)
	Configure(ctx context.Context, c subtitles.Config) (subtitles.Status, error)
	Languages(ctx context.Context) ([]string, error)
	Fetch(ctx context.Context, fileID int64, language string) (subtitles.Fetched, error)
}

func subtitleStatusJSON(st subtitles.Status) map[string]any {
	return map[string]any{"configured": st.HasKey, "has_api_key": st.HasKey, "username": st.Username,
		"has_password": st.HasPassword, "languages": st.Languages}
}

func subtitleProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, subtitles.ErrBadLanguage):
		writeProblem(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, importer.ErrFileNotFound), errors.Is(err, importer.ErrItemNotFound):
		writeProblem(w, http.StatusNotFound, "not found")
	case errors.Is(err, subtitles.ErrNotConfigured), errors.Is(err, subtitles.ErrHave),
		errors.Is(err, subtitles.ErrNoHash):
		writeProblem(w, http.StatusConflict, err.Error())
	case errors.Is(err, subtitles.ErrNoMatch):
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, subtitles.ErrQuota):
		writeProblem(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, subtitles.ErrKeyRefused), errors.Is(err, subtitles.ErrUnavailable),
		errors.Is(err, subtitles.ErrNotASubtitle):
		writeProblem(w, http.StatusBadGateway, err.Error())
	default:
		writeAuthzAware(w, err)
	}
}

// SubtitleStatus says what is configured, never a secret.
func (h *Handlers) SubtitleStatus(w http.ResponseWriter, r *http.Request) {
	if h.subtitles == nil {
		writeProblem(w, http.StatusNotImplemented, "subtitles are not wired")
		return
	}
	st, err := h.subtitles.Status(r.Context())
	if err != nil {
		subtitleProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, subtitleStatusJSON(st))
}

type subtitleConfigRequest struct {
	APIKey    *string  `json:"api_key"`
	Username  *string  `json:"username"`
	Password  *string  `json:"password"`
	Languages []string `json:"languages"`
}

// ConfigureSubtitles stores OpenSubtitles' key, account and the languages.
func (h *Handlers) ConfigureSubtitles(w http.ResponseWriter, r *http.Request) {
	if h.subtitles == nil {
		writeProblem(w, http.StatusNotImplemented, "subtitles are not wired")
		return
	}
	var in subtitleConfigRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	st, err := h.subtitles.Configure(r.Context(), subtitles.Config{APIKey: in.APIKey, Username: in.Username,
		Password: in.Password, Languages: in.Languages})
	if err != nil {
		subtitleProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, subtitleStatusJSON(st))
}

// SubtitleLanguages are the languages a file's subtitles are fetched in.
func (h *Handlers) SubtitleLanguages(w http.ResponseWriter, r *http.Request) {
	if h.subtitles == nil {
		writeJSON(w, http.StatusOK, map[string]any{"languages": []string{}})
		return
	}
	langs, err := h.subtitles.Languages(r.Context())
	if err != nil {
		subtitleProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"languages": langs})
}

type subtitleFetchRequest struct {
	Language string `json:"language"`
}

// FetchSubtitle finds a file's subtitle in a language and writes it beside
// the file.
func (h *Handlers) FetchSubtitle(w http.ResponseWriter, r *http.Request) {
	if h.subtitles == nil {
		writeProblem(w, http.StatusNotImplemented, "subtitles are not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in subtitleFetchRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	res, err := h.subtitles.Fetch(r.Context(), id, in.Language)
	if err != nil {
		subtitleProblem(w, err)
		return
	}
	how := "matched by title"
	if res.HashMatch {
		how = "matched to this exact file"
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": res.Path, "release": res.Release, "hash_match": res.HashMatch,
		"downloads_remaining": res.Remaining,
		"note":                "Written beside the file, " + how + ". It is offered when the file is played."})
}

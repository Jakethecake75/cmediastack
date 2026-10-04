package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/playback"
)

// The playback surface: what a file contains, whether it will play, and the
// bytes themselves.

// PlaybackService is what the API needs to play something.
type PlaybackService interface {
	// Serve writes the file to the response, honouring range requests.
	Serve(w http.ResponseWriter, r *http.Request, fileID int64) error
	// ServeAttachment writes the file as a download (ADR-0038).
	ServeAttachment(w http.ResponseWriter, r *http.Request, fileID int64) error
	// Inspect returns a current probe, taking one if the stored probe no
	// longer describes the file.
	Inspect(ctx context.Context, fileID int64) (playback.Probe, error)
	// Resume reports where the caller should start, given the file's current
	// duration, and whether that is a resume rather than a fresh start.
	Resume(ctx context.Context, fileID int64, current time.Duration) (time.Duration, bool, playback.Position)
	// SavePosition records where the caller got to.
	SavePosition(ctx context.Context, fileID int64, position, duration time.Duration) (playback.Position, error)
	// ForgetPosition removes the caller's place in a file.
	ForgetPosition(ctx context.Context, fileID int64) error
	// InProgress is the caller's unfinished places, newest first (ADR-0069).
	InProgress(ctx context.Context, limit int) ([]playback.InProgressItem, error)
	// PlanConversion reports whether converting a file would make it playable.
	// hevc is the browser saying it decodes HEVC Main10 and HDR (ADR-0072).
	PlanConversion(ctx context.Context, fileID int64, target playback.AudioTarget, hevc bool) (playback.RemuxPlan, playback.Probe, error)
	// Convert streams a converted version of a file.
	Convert(w http.ResponseWriter, r *http.Request, fileID int64, target playback.AudioTarget, opts playback.StreamOptions) error
	// ListSubtitles reports every subtitle track a file has, usable or not.
	ListSubtitles(ctx context.Context, fileID int64) ([]playback.SubtitleTrack, error)
	// ServeSubtitle converts one of them to WebVTT and writes it.
	ServeSubtitle(w http.ResponseWriter, r *http.Request, fileID int64, id string) error
}

// subtitleJSON is one row of the track list.
//
// The id is opaque and the only handle a client gets. There is no path and no
// stream index in it, which is what keeps a request from naming a file of its
// own (ADR-0020).
func subtitleJSON(t playback.SubtitleTrack) map[string]any {
	row := map[string]any{
		"id": t.ID, "codec": t.Codec, "embedded": t.Embedded,
		"forced": t.Forced, "default": t.Default, "usable": t.Usable,
	}
	if t.Language != "" {
		row["language"] = t.Language
	}
	if t.Title != "" {
		row["title"] = t.Title
	}
	if !t.Usable {
		// The reason travels with the refusal. A track listed as unusable with
		// no explanation is a bug report waiting to be filed.
		row["why"] = t.Why
	}
	return row
}

func streamJSON(s playback.VideoStream) map[string]any {
	out := map[string]any{
		"index": s.Index, "codec": s.Codec,
		"width": s.Width, "height": s.Height,
		"bit_depth": s.BitDepth,
	}
	if s.Profile != "" {
		out["profile"] = s.Profile
	}
	if s.FrameRate > 0 {
		out["frame_rate"] = s.FrameRate
	}
	if s.HDR {
		// Named rather than flagged: "HDR10" and "HLG" are different things and
		// a viewer who has to ask which is which has been told nothing.
		out["hdr"] = true
		out["transfer"] = s.ColorTransfer
	}
	return out
}

func probeJSON(p playback.Probe, plan playback.Plan) map[string]any {
	video := make([]map[string]any, 0, len(p.Video))
	for _, v := range p.Video {
		video = append(video, streamJSON(v))
	}
	audio := make([]map[string]any, 0, len(p.Audio))
	for _, a := range p.Audio {
		row := map[string]any{
			"index": a.Index, "codec": a.Codec, "channels": a.Channels,
			"default": a.Default,
		}
		if a.ChannelLayout != "" {
			row["layout"] = a.ChannelLayout
		}
		if a.Language != "" {
			row["language"] = a.Language
		}
		if a.Title != "" {
			row["title"] = a.Title
		}
		audio = append(audio, row)
	}
	subs := make([]map[string]any, 0, len(p.Subtitles))
	for _, s := range p.Subtitles {
		row := map[string]any{
			"index": s.Index, "codec": s.Codec, "forced": s.Forced,
			"default": s.Default,
			// Whether a browser can be handed this track at all. A bitmap track
			// has to be drawn onto the picture, which is a transcode.
			"text": s.Text,
		}
		if s.Language != "" {
			row["language"] = s.Language
		}
		if s.Title != "" {
			row["title"] = s.Title
		}
		subs = append(subs, row)
	}

	blockers := make([]map[string]any, 0, len(plan.Blockers))
	for _, b := range plan.Blockers {
		blockers = append(blockers, map[string]any{
			"code": b.Code, "what": b.What, "says": b.Says,
		})
	}

	out := map[string]any{
		"container":   p.Container,
		"duration_ms": p.Duration.Milliseconds(),
		"bitrate":     p.Bitrate,
		"size_bytes":  p.SizeBytes,
		"video":       video,
		"audio":       audio,
		"subtitles":   subs,
		"direct_play": plan.DirectPlay,
		"blockers":    blockers,
		"summary":     plan.Summary,
		// Whether the parser ran in the jail. Reported because an operator
		// auditing later deserves to know which guarantee this answer was taken
		// under (ADR-0020).
		"sandboxed": p.Sandboxed,
	}
	if len(plan.Caveats) > 0 {
		out["caveats"] = plan.Caveats
	}
	return out
}

// PlaybackInfo reports what is in a file and whether it will play.
//
// The capability set is the server's conservative default rather than anything
// the client sent. A GET with a body is not a thing, and a capability set in a
// query string is a long, cacheable, mistypeable mess — while the answer this
// endpoint gives is the one a library listing wants, where there is no browser
// in the conversation at all.
//
// The player refines it in the browser, where `canPlayType` is available and
// free. This is the server's honest first opinion, not the last word.
func (h *Handlers) PlaybackInfo(w http.ResponseWriter, r *http.Request) {
	if h.playback == nil {
		writeProblem(w, http.StatusNotImplemented, "playback is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	probe, err := h.playback.Inspect(r.Context(), id)
	switch {
	case errors.Is(err, playback.ErrNoSuchFile), errors.Is(err, importer.ErrFileNotFound):
		writeProblem(w, http.StatusNotFound, "no such file")
		return
	case errors.Is(err, playback.ErrToolMissing):
		// 503 and the reason, because the alternative was observed: a container
		// shipped with no ffmpeg in it answered every playback request with a
		// bare 500 and logged nothing, so the library browsed, the app looked
		// healthy, and nothing could be played. An operator deserves to be told
		// which program is missing.
		writeProblem(w, http.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, playback.ErrSandboxUnavailable):
		// 503, not 500: nothing is broken, and the condition is about this
		// host rather than this request. The message says what an operator can
		// do about it.
		writeProblem(w, http.StatusServiceUnavailable,
			"this host will not create the sandbox media parsing runs in, and "+
				"parsing outside it is not permitted; see ADR-0020")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	plan := playback.Decide(probe, playback.ChromeLike)
	body := probeJSON(probe, plan)

	// The position comes back with the probe rather than from a second request.
	// The player needs both before it can do anything — where to start is part
	// of "can this play" from a viewer's point of view — and a second round
	// trip would mean the video element either starts at zero and jumps, or
	// waits on a request it did not have to make.
	if at, resumed, rec := h.playback.Resume(r.Context(), id, probe.Duration); resumed {
		body["resume_ms"] = at.Milliseconds()
		body["watched_ms"] = rec.Position.Milliseconds()
	} else if rec.UpdatedAt.IsZero() {
		// Nothing recorded. Said as an absence rather than as a zero, so a
		// client can tell "never watched" from "watched, start again".
		body["resume_ms"] = nil
	} else {
		body["resume_ms"] = 0
		body["finished"] = rec.Finished
	}

	// The subtitle tracks come back with the probe for the same reason the
	// position does: the player needs them before it can build the <video>
	// element, and a second round trip would mean either a player with no
	// subtitle menu for a moment or one that waits on a request it could have
	// avoided.
	//
	// This is a different list from probe["subtitles"], and deliberately so.
	// That one describes what is IN the file, stream indices and all. This one
	// is what can be ASKED FOR: it includes sidecars, which are not in the file
	// at all, and it names each track by an opaque id rather than by an index a
	// client has no business sending back.
	if tracks, terr := h.playback.ListSubtitles(r.Context(), id); terr == nil {
		rows := make([]map[string]any, 0, len(tracks))
		for _, t := range tracks {
			rows = append(rows, subtitleJSON(t))
		}
		body["subtitle_tracks"] = rows
	}

	// When it will not direct-play, say whether converting would help — and if
	// not, why not. A viewer looking at a refusal wants to know whether there
	// is anything to try next, and "no, because the picture itself is the
	// problem" is a better answer than an absent button.
	if !plan.DirectPlay {
		conv, _, cerr := h.playback.PlanConversion(r.Context(), id, playback.AudioAAC, false)
		if cerr == nil {
			body["can_convert"] = conv.Possible
			if !conv.Possible {
				body["convert_why_not"] = conv.Why
			} else {
				body["convert_reencodes_audio"] = conv.ReencodeAudio
				// A transcode costs a hundred times what a remux does, and
				// the viewer is told which this is (ADR-0071).
				body["convert_transcodes_video"] = conv.TranscodeVideo
				body["convert_tone_maps"] = conv.ToneMap
			}
		}
		// A browser that decodes HEVC (hevc=1) is told when it can have the
		// original picture copied instead of transcoded (ADR-0072).
		if cerr == nil && conv.TranscodeVideo && r.URL.Query().Get("hevc") == "1" {
			if hc, _, err := h.playback.PlanConversion(r.Context(), id, playback.AudioAAC, true); err == nil && hc.Possible && !hc.TranscodeVideo {
				body["convert_copies_hevc"] = true
			}
		}
	}

	writeJSON(w, http.StatusOK, body)
}

// ConvertFile streams a converted version of a file.
//
// The output codec is named by the client and matched against a fixed list
// before anything reaches ffmpeg. Which codec is right genuinely depends on the
// browser — AAC everywhere that ships it, Opus on a Chromium built without the
// proprietary codecs — and the browser is the only thing that knows.
func (h *Handlers) ConvertFile(w http.ResponseWriter, r *http.Request) {
	if h.playback == nil {
		writeProblem(w, http.StatusNotImplemented, "playback is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	// Default rather than required: a client that says nothing gets the answer
	// that is right for most browsers.
	target := playback.AudioAAC
	if raw := r.URL.Query().Get("audio"); raw != "" {
		parsed, ok := playback.ParseAudioTarget(raw)
		if !ok {
			writeProblem(w, http.StatusBadRequest,
				"this server converts audio to aac or opus, and nothing else")
			return
		}
		target = parsed
	}

	opts, problem := convertOptions(r.URL.Query())
	if problem != "" {
		writeProblem(w, http.StatusBadRequest, problem)
		return
	}

	err := h.playback.Convert(w, r, id, target, opts)
	switch {
	case err == nil:
		return
	case errors.Is(err, playback.ErrNoSuchFile), errors.Is(err, importer.ErrFileNotFound):
		writeProblem(w, http.StatusNotFound, "no such file")
	case errors.Is(err, playback.ErrRemuxWouldNotHelp):
		// 409, not 400: the request was well formed and the file is real. What
		// is wrong is that this particular conversion would not achieve
		// anything, and the message says what would be needed instead.
		writeProblem(w, http.StatusConflict, err.Error())
	case errors.Is(err, playback.ErrToolMissing):
		writeProblem(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, playback.ErrTooManyRemuxes):
		// 503 with Retry-After, because it is a capacity answer rather than a
		// refusal: the same request will work shortly.
		w.Header().Set("Retry-After", "30")
		writeProblem(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeAuthzAware(w, err)
	}
}

// convertOptions reads where a converted stream starts, in whole seconds, and
// how tall a transcode is (ADR-0071), and whether the browser decodes HEVC
// (ADR-0072), or says what is wrong with them.
func convertOptions(q url.Values) (playback.StreamOptions, string) {
	opts := playback.StreamOptions{HEVC: q.Get("hevc") == "1"}
	if raw := q.Get("start"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			return opts, "start is a whole number of seconds"
		}
		opts.Start = time.Duration(n) * time.Second
	}
	if raw := q.Get("height"); raw != "" {
		h, ok := playback.ParseHeight(raw)
		if !ok {
			return opts, "a transcode is 720 or 1080 pixels tall, and nothing else"
		}
		opts.Height = h
	}
	return opts, ""
}

type positionRequest struct {
	// Milliseconds, because a float of seconds from a <video> element is
	// exactly the sort of number that arrives as 4271.9999999999995.
	PositionMS int64 `json:"position_ms"`
	DurationMS int64 `json:"duration_ms"`
}

// SavePosition records where the caller got to in a file.
//
// PUT rather than POST: it is idempotent and there is one position per person
// per file, so sending it twice means the same thing as sending it once. That
// matters here more than usual — the player sends this on a throttle AND again
// as the page unloads, so duplicates are the normal case rather than an error.
func (h *Handlers) SavePosition(w http.ResponseWriter, r *http.Request) {
	if h.playback == nil {
		writeProblem(w, http.StatusNotImplemented, "playback is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in positionRequest
	if !decodeJSON(w, r, &in) {
		return
	}

	rec, err := h.playback.SavePosition(r.Context(), id,
		time.Duration(in.PositionMS)*time.Millisecond,
		time.Duration(in.DurationMS)*time.Millisecond)
	switch {
	case errors.Is(err, playback.ErrNoSuchFile), errors.Is(err, importer.ErrFileNotFound):
		writeProblem(w, http.StatusNotFound, "no such file")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"position_ms": rec.Position.Milliseconds(),
		"finished":    rec.Finished,
	})
}

// ForgetPosition removes the caller's place in a file.
//
// The counterpart to keeping a history at all: somebody who watched something
// can un-record that without waiting out the retention window.
func (h *Handlers) ForgetPosition(w http.ResponseWriter, r *http.Request) {
	if h.playback == nil {
		writeProblem(w, http.StatusNotImplemented, "playback is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := h.playback.ForgetPosition(r.Context(), id); err != nil {
		writeAuthzAware(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"file_id": id,
		"note":    "Forgotten. This file will start from the beginning next time.",
	})
}

// StreamFile serves the bytes.
//
// Authorized by the session like every other route: the player is same-origin,
// so the cookie is sent with a <video> request the same as with any other
// (ADR-0020). There is no signed URL and no token in a query string, which
// means there is one authorization model rather than two and revoking a session
// revokes playback with it.
func (h *Handlers) StreamFile(w http.ResponseWriter, r *http.Request) {
	if h.playback == nil {
		writeProblem(w, http.StatusNotImplemented, "playback is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	err := h.playback.Serve(w, r, id)
	switch {
	case err == nil:
		return
	case errors.Is(err, playback.ErrNoSuchFile), errors.Is(err, importer.ErrFileNotFound):
		writeProblem(w, http.StatusNotFound, "no such file")
	default:
		// Serve writes headers before it writes bytes, so a failure after that
		// point cannot be turned into a status code. writeAuthzAware handles
		// the permission case, which happens before anything is written.
		writeAuthzAware(w, err)
	}
}

// ListSubtitles reports the subtitle tracks a file has.
//
// Including the ones this server will not serve, each with its reason. A
// viewer who can see that a PGS track exists and why it is not offered has
// been told something true; a list that silently omits it invites the question
// of where the subtitles went.
func (h *Handlers) ListSubtitles(w http.ResponseWriter, r *http.Request) {
	if h.playback == nil {
		writeProblem(w, http.StatusNotImplemented, "playback is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	tracks, err := h.playback.ListSubtitles(r.Context(), id)
	switch {
	case errors.Is(err, playback.ErrNoSuchFile), errors.Is(err, importer.ErrFileNotFound):
		writeProblem(w, http.StatusNotFound, "no such file")
		return
	case err != nil:
		writeAuthzAware(w, err)
		return
	}

	rows := make([]map[string]any, 0, len(tracks))
	for _, t := range tracks {
		rows = append(rows, subtitleJSON(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"subtitles": rows})
}

// ServeSubtitle writes one track as WebVTT.
//
// The id is a handle this server issued, resolved against a freshly built list
// before anything is opened. It is not a filename and it is not a stream index,
// so there is nothing in the request that can select a file the server did not
// already offer.
func (h *Handlers) ServeSubtitle(w http.ResponseWriter, r *http.Request) {
	if h.playback == nil {
		writeProblem(w, http.StatusNotImplemented, "playback is not wired")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	err := h.playback.ServeSubtitle(w, r, id, r.PathValue("sid"))
	switch {
	case err == nil:
		return
	case errors.Is(err, playback.ErrNoSuchFile), errors.Is(err, importer.ErrFileNotFound):
		writeProblem(w, http.StatusNotFound, "no such file")
	case errors.Is(err, playback.ErrNoSuchSubtitle):
		// 404 and not 403: the id names nothing this file has, which is the
		// same answer whether it was mistyped, stale, or invented. Saying
		// anything more precise would confirm which.
		writeProblem(w, http.StatusNotFound, err.Error())
	case errors.Is(err, playback.ErrToolMissing):
		writeProblem(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, playback.ErrEmptySubtitle):
		// 422, not 404 and not 500: the track is real and the request named it
		// correctly, and what failed is the CONTENT. The message says what is
		// wrong with the file, which is the only thing an operator can act on.
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, playback.ErrSandboxUnavailable):
		writeProblem(w, http.StatusServiceUnavailable,
			"this host will not create the sandbox media parsing runs in, and "+
				"parsing outside it is not permitted; see ADR-0020")
	default:
		writeAuthzAware(w, err)
	}
}

// ContinueWatching is the caller's unfinished places, newest first, each with
// the title it belongs to — Home's first row (ADR-0069).
func (h *Handlers) ContinueWatching(w http.ResponseWriter, r *http.Request) {
	if h.playback == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	items, err := h.playback.InProgress(r.Context(), 20)
	if err != nil {
		writeAuthzAware(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		row := map[string]any{
			"file_id": it.FileID, "item_id": it.ItemID, "kind": it.Kind, "title": it.Title,
			"position_ms": it.Position.Milliseconds(), "duration_ms": it.Duration.Milliseconds(),
			"updated_at": it.UpdatedAt,
		}
		if it.TMDBID > 0 { // as the library list: only an identified title has artwork
			row["poster"] = artworkPath(it.ItemID)
		}
		if it.Year > 0 {
			row["year"] = it.Year
		}
		if it.Season > 0 || it.Episode > 0 {
			row["season"], row["episode"] = it.Season, it.Episode
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

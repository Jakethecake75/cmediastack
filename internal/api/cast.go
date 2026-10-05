package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
)

// Casting to a Chromecast (ADR-0077). The Chromecast fetches the video from
// this server itself and cannot sign in, so the player is given a link that
// works without a session: signed, for one file, acting as the account that
// cast it, for a few hours.

// castLife is how long a cast link works: a long film and a pause.
const castLife = 6 * time.Hour

// newCastKey is the key cast links are signed with. Made at start-up and kept
// nowhere, so a restart ends every cast link; the player asks for a new one
// whenever it casts.
func newCastKey() []byte {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic("api: no randomness for the cast key: " + err.Error())
	}
	return k
}

func (h *Handlers) castMAC(payload string) string {
	m := hmac.New(sha256.New, h.castKey)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// castToken is "file.user.expiry.mac".
func (h *Handlers) castToken(fileID, userID int64, until time.Time) string {
	payload := fmt.Sprintf("%d.%d.%d", fileID, userID, until.Unix())
	return payload + "." + h.castMAC(payload)
}

// readCastToken returns the file and account a link names, if it is one this
// process signed and it has not expired.
func (h *Handlers) readCastToken(token string, now time.Time) (fileID, userID int64, ok bool) {
	i := strings.LastIndexByte(token, '.')
	if i < 0 || !hmac.Equal([]byte(token[i+1:]), []byte(h.castMAC(token[:i]))) {
		return 0, 0, false
	}
	parts := strings.Split(token[:i], ".")
	if len(parts) != 3 {
		return 0, 0, false
	}
	fileID, e1 := strconv.ParseInt(parts[0], 10, 64)
	userID, e2 := strconv.ParseInt(parts[1], 10, 64)
	until, e3 := strconv.ParseInt(parts[2], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || now.Unix() > until {
		return 0, 0, false
	}
	return fileID, userID, true
}

// CastLink gives the player the addresses a Chromecast plays this file from,
// for a file the caller may see. The Chromecast's fetch is asked again, as
// that account, so a link outlives nothing the account loses.
func (h *Handlers) CastLink(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if h.media == nil {
		writeProblem(w, http.StatusNotImplemented, "no library is wired")
		return
	}
	// A file the caller cannot see answers as a missing one (ADR-0037).
	if _, err := h.media.FileForPlayback(r.Context(), id); errors.Is(err, importer.ErrFileNotFound) {
		writeProblem(w, http.StatusNotFound, "no such file")
		return
	} else if err != nil {
		writeAuthzAware(w, err)
		return
	}
	p := authz.FromContext(r.Context())
	until := time.Now().Add(castLife)
	base := "/api/v1/cast/" + h.castToken(id, p.UserID, until)
	writeJSON(w, http.StatusOK, map[string]any{
		"convert":    base + "/convert",
		"stream":     base + "/stream",
		"expires_at": until.UTC(),
	})
}

// CastConvert and CastStream are /files/{id}/convert and /stream for a
// Chromecast holding a cast link.
func (h *Handlers) CastConvert(w http.ResponseWriter, r *http.Request) {
	h.castServe(w, r, h.ConvertFile)
}
func (h *Handlers) CastStream(w http.ResponseWriter, r *http.Request) {
	h.castServe(w, r, h.StreamFile)
}

func (h *Handlers) castServe(w http.ResponseWriter, r *http.Request, serve http.HandlerFunc) {
	fileID, userID, ok := h.readCastToken(r.PathValue("token"), time.Now())
	if !ok {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	p, err := h.svc.CastPrincipal(r.Context(), userID)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	// The Chromecast's player is a page on Google's origin: it may read this
	// response, which the token alone authorises.
	w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "X-Stream-Start")
	r = r.WithContext(authz.WithPrincipal(r.Context(), p))
	r.SetPathValue("id", strconv.FormatInt(fileID, 10))
	serve(w, r)
}

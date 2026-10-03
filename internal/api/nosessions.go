package api

import (
	"net/http"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// NoSessions is an Authenticator that never resolves a principal.
//
// It is the safe placeholder for any wiring that does not yet have a session
// store: an unfinished authenticator that denied nothing would be far worse
// than one that denies everything. Tests also use it to exercise the anonymous
// path without building a database.
//
// The real implementation is SessionAuthenticator in sessionauth.go.
type NoSessions struct{}

// Authenticate always reports anonymous.
func (NoSessions) Authenticate(http.ResponseWriter, *http.Request) (*authz.Principal, error) {
	return nil, nil
}

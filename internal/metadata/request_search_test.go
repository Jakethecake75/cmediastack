package metadata

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

type searchOnly struct {
	Provider
	got Query
}

func (s *searchOnly) Search(_ context.Context, q Query) ([]Match, error) {
	s.got = q
	return []Match{{Title: "Dune"}}, nil
}

func holding(perms ...authz.Permission) context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: 7, Username: "sam", State: authz.StateActive, MFASatisfied: true,
		Role: authz.Role{ID: 3, Name: "User", Rank: 10, Permissions: authz.NewPermissionSet(perms...)},
	})
}

// ADR-0069: an account that may only request searches the provider to
// request — films and series — and still may not use the editor's search.
func TestSearchingToRequestNeedsTheRequestPermission(t *testing.T) {
	svc := NewService(settingsAnswer{}, nil, nil, func() *http.Client { return http.DefaultClient }, time.Now)
	p := &searchOnly{}
	svc.provider = p

	requester := holding(authz.PermBrowse, authz.PermSubmitRequest)
	if got, err := svc.SearchToRequest(requester, Query{Kind: "artist", Title: "dune"}); err != nil || len(got) != 1 {
		t.Fatalf("a requester's search: %v %v", got, err)
	}
	if p.got.Kind != KindMovie {
		t.Errorf("searched as %q, want a request's kind (movie) for anything else", p.got.Kind)
	}
	if _, err := svc.Search(requester, Query{Kind: KindMovie, Title: "dune"}); !authz.IsDenied(err) {
		t.Errorf("a requester used the editor's search: %v", err)
	}
	if _, err := svc.SearchToRequest(holding(authz.PermBrowse), Query{Title: "dune"}); !authz.IsDenied(err) {
		t.Errorf("an account that may not request searched to request: %v", err)
	}
}

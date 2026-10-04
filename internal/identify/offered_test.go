package identify

import (
	"context"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/authz"
)

// A poster a title search showed an account is fetched from the path that
// search recorded (ADR-0070), and is offered to that account only.
func TestAnOfferedPosterIsFetchedFromTheRecordedPath(t *testing.T) {
	r := newSvcRig(t)
	ctx := r.asEditor()
	if r.svc.PosterOffered(ctx, "tmdb", 438631) {
		t.Fatal("offered before any search")
	}
	if err := r.svc.OfferPosters(ctx, "tmdb", map[int64]string{438631: "dune.jpg"}); err != nil {
		t.Fatal(err)
	}
	if !r.svc.PosterOffered(ctx, "tmdb", 438631) || r.svc.PosterOffered(ctx, "tmdb", 841) {
		t.Error("PosterOffered does not answer for exactly what was offered")
	}
	if _, err := r.svc.CachePoster(ctx, "tmdb", 438631); err != nil {
		t.Fatalf("cache poster: %v", err)
	}
	if len(r.art.fetched) != 1 || r.art.fetched[0].RemotePath != "dune.jpg" {
		t.Errorf("fetched %+v, want the offered path", r.art.fetched)
	}
	if err := r.svc.OfferPosters(asNobody(r), "tmdb", map[int64]string{1: "x.jpg"}); err == nil {
		t.Error("an account that may not browse recorded a poster")
	}
}

func asNobody(r *svcRig) context.Context {
	return authz.WithPrincipal(context.Background(), &authz.Principal{
		UserID: r.userID, Username: "nobody", State: authz.StateActive,
		MFASatisfied: true, Role: authz.Role{ID: 9, Name: "Pending", Rank: 0},
	})
}

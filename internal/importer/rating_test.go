package importer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/metadata"
)

type fakeCertifier struct {
	certs map[int64]string
	errs  map[int64]error
	asked []int64
}

func (f *fakeCertifier) Certification(_ context.Context, _ metadata.Kind, id int64) (string, error) {
	f.asked = append(f.asked, id)
	if err := f.errs[id]; err != nil {
		return "", err
	}
	return f.certs[id], nil
}

// ADR-0037, decision 3: the task rates identified titles, asks again about an
// unrated one only after a month, and never replaces what a person set; a
// person's rating, or its removal, is theirs.
func TestRatingsAreFetchedAndAPersonsIsKept(t *testing.T) {
	r := newRig(t)
	series := r.followed("Severance", r.seriesRoot().ID)
	if _, err := r.store.db.ExecContext(r.ctx, `UPDATE media_item SET tmdb_id = 95396 WHERE id = ?`, series.ID); err != nil {
		t.Fatal(err)
	}
	unknown := r.followed("Home Video", r.seriesRoot().ID)
	if _, err := r.store.db.ExecContext(r.ctx, `UPDATE media_item SET tmdb_id = 7 WHERE id = ?`, unknown.ID); err != nil {
		t.Fatal(err)
	}
	r.followed("Not Identified", r.seriesRoot().ID) // no provider id: never asked

	task := authz.SystemPrincipal(t.Context(), authz.TaskRatings)
	c := &fakeCertifier{certs: map[int64]string{95396: "TV-MA", 7: "NR"}}
	pass, err := RateTitles(task, r.store, c, 50)
	if err != nil || pass.Asked != 2 || pass.Rated != 1 || pass.Unrated != 1 {
		t.Fatalf("first pass: %+v, %v (asked %v)", pass, err, c.asked)
	}
	got, _ := r.store.GetItem(r.ctx, series.ID)
	if got.Certification != "TV-MA" || got.RatingRank != 4 || got.RatingSource != "provider" {
		t.Errorf("the provider's rating was recorded as %+v", got)
	}
	if got, _ := r.store.GetItem(r.ctx, unknown.ID); got.RatingRank != 0 || got.Certification != "" {
		t.Errorf("an unknown certification was recorded as %+v", got)
	}

	// Asked once: nothing is due again at once, and an unrated title is due
	// after a month.
	if due, _ := r.store.TitlesToRate(task, 50); len(due) != 0 {
		t.Errorf("due again at once: %+v", due)
	}
	old := time.Now().UTC().Add(-UnratedRecheck - time.Hour).Format(timeLayout)
	if _, err := r.store.db.ExecContext(r.ctx, `UPDATE media_item SET rating_checked_at = ?`, old); err != nil {
		t.Fatal(err)
	}
	due, _ := r.store.TitlesToRate(task, 50)
	if len(due) != 1 || due[0].ID != unknown.ID {
		t.Errorf("after a month, due: %+v — only the unrated title", due)
	}

	// A person rates it; the task leaves that alone even when asked to write.
	before, after, err := r.store.SetRating(r.ctx, series.ID, "tv-14")
	if err != nil || before.Certification != "TV-MA" || after.Certification != "TV-14" || after.RatingSource != "person" {
		t.Fatalf("a person's rating: %+v → %+v, %v", before, after, err)
	}
	if err := r.store.RecordProviderRating(task, series.ID, "TV-MA"); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.store.GetItem(r.ctx, series.ID); got.Certification != "TV-14" || got.RatingRank != 3 {
		t.Errorf("the task replaced a person's rating: %+v", got)
	}
	if due, _ := r.store.TitlesToRate(task, 50); len(due) != 1 || due[0].ID != unknown.ID {
		t.Errorf("a person's title is due for the task: %+v", due)
	}

	// Given back to the provider: unrated, and due.
	if _, after, err := r.store.SetRating(r.ctx, series.ID, ""); err != nil || after.RatingRank != 0 || after.RatingSource != "" {
		t.Fatalf("clearing: %+v, %v", after, err)
	}
	if due, _ := r.store.TitlesToRate(task, 50); len(due) != 2 {
		t.Errorf("a cleared title is not due: %+v", due)
	}

	if _, _, err := r.store.SetRating(r.ctx, series.ID, "15"); !errors.Is(err, ErrUnknownCertification) {
		t.Errorf("an unknown certification was accepted: %v", err)
	}
	if _, _, err := r.store.SetRating(task, series.ID, "G"); err == nil {
		t.Error("the task rated a title as a person would")
	}
	if _, _, err := r.store.SetRating(r.ctx, 424242, "G"); !errors.Is(err, ErrItemNotFound) {
		t.Errorf("an unknown title: %v", err)
	}

	// A provider that refuses stops the pass; the rest are asked next time.
	c = &fakeCertifier{certs: map[int64]string{}, errs: map[int64]error{95396: metadata.ErrRateLimited}}
	if _, err := RateTitles(task, r.store, c, 50); !errors.Is(err, metadata.ErrRateLimited) {
		t.Errorf("a rate limit did not stop the pass: %v", err)
	}
	// One the provider does not know is unrated, not a failure.
	c = &fakeCertifier{errs: map[int64]error{95396: metadata.ErrNotFound, 7: metadata.ErrNotFound}}
	if pass, err := RateTitles(task, r.store, c, 50); err != nil || pass.Unrated != 2 {
		t.Errorf("titles the provider does not know: %+v, %v", pass, err)
	}
}

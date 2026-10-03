package importer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/identify"
	"github.com/jakethecake75/cmediastack/internal/metadata"
	"github.com/jakethecake75/cmediastack/internal/playback"
	"github.com/jakethecake75/cmediastack/internal/release"
	"github.com/jakethecake75/cmediastack/internal/tv"
)

// ErrNotTheSameTitle means a spelling change would have altered the title.
var ErrNotTheSameTitle = errors.New("importer: that is a different title")

// The library's side of identification.
//
// internal/identify decides WHAT an item is; this decides what that does to the
// row an operator browses to. Deliberately here rather than there: rewriting
// somebody's library is the library's business, and a package that could reach
// in and do it would be one more place to look when a title changed
// unexpectedly.
//
// The two halves are two methods, and that is the whole design. AttachIdentity
// takes ids and no title, so a caller holding only it cannot relabel anything —
// enforced by the signature rather than by intent. Relabel takes the title,
// needs a different permission, and is reached only by a person's confirmation.

// ItemForIdentification reports what this software currently believes about an
// item, in the form the identifier wants.
//
// Read-only and permission-free at this layer for the same reason the rest of
// this store is: the caller holds the permission. The identification pass runs
// with PermBrowse, which ListItems already requires.
func (s *Store) ItemForIdentification(ctx context.Context, itemID int64) (identify.Item, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return identify.Item{}, err
	}
	it, err := s.GetItem(ctx, itemID)
	if err != nil {
		return identify.Item{}, err
	}
	kind := metadata.KindMovie
	if it.Kind == "series" {
		kind = metadata.KindSeries
	}
	return identify.Item{Title: it.Title, Year: it.Year, Kind: kind}, nil
}

// AttachIdentity records which provider title an item is.
//
// Ids only — it takes no title and no year, so a caller holding this method
// cannot change what an operator browses to. That is not a convention, it is
// the signature: the identification pass runs with PermBrowse and calls this,
// and there is no argument it could pass that would relabel anything.
//
// The permission is browse rather than edit for the same reason: attaching a
// provider id is a fact recorded ABOUT an item, not a change to it. Requiring
// edit here would mean granting the background pass the authority to rename
// things, in order that it might record an id — which is the trade ADR-0019
// exists to refuse.
func (s *Store) AttachIdentity(ctx context.Context, itemID, tmdbID int64, imdbID string) error {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return err
	}

	sets := []string{"updated_at = ?"}
	args := []any{s.now().UTC().Format(timeLayout)}
	if tmdbID > 0 {
		sets = append(sets, "tmdb_id = ?")
		args = append(args, tmdbID)
	}
	if imdbID != "" {
		sets = append(sets, "imdb_id = ?")
		args = append(args, imdbID)
	}
	return s.updateItem(ctx, itemID, sets, args)
}

// Relabel changes what an item is called.
//
// A SEPARATE method from AttachIdentity, and separate on purpose. Relabelling
// somebody's library is the consequential half of identification, so it is
// reached by a different name, guarded by a different permission, and
// unreachable from a caller that only has the other one.
//
// Only a person's confirmation reaches here (identify.Service.Confirm), taken
// while they are looking at both titles.
func (s *Store) Relabel(ctx context.Context, itemID int64, title string, year int) error {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return err
	}
	if strings.TrimSpace(title) == "" && year <= 0 {
		return nil
	}

	sets := []string{"updated_at = ?"}
	args := []any{s.now().UTC().Format(timeLayout)}
	if t := strings.TrimSpace(title); t != "" {
		// sort_title is stored rather than computed per query (migration 0008),
		// so it has to move with the title or the library sorts by a name
		// nothing displays.
		sets = append(sets, "title = ?", "sort_title = ?")
		args = append(args, t, SortTitle(t))
	}
	if year > 0 {
		sets = append(sets, "year = ?")
		args = append(args, year)
	}
	return s.updateItem(ctx, itemID, sets, args)
}

// AdoptCanonicalTitle takes the provider's spelling of a title this software
// already agrees is the same title.
//
// # Why this is safe for a background pass, and Relabel is not
//
// The refusal is inside the method, not in the caller's discipline: it compares
// release.NormaliseTitle on both sides and refuses unless they are identical.
// So "the matrix" may become "The Matrix" — same title, better spelled — and
// "Arrival" can never become "The Arrival", because those normalise
// differently. The pass cannot change what an item IS by calling this; it can
// only change how the same thing is written.
//
// That is the distinction the automatic-identification rule was always about.
// ADR-0019's constraint is that a background task must not relabel an item as a
// DIFFERENT film; it was never that a library should keep the lowercase,
// dot-separated spelling a release group happened to use. Enforcing the real
// constraint at the point of writing is stronger than enforcing a broader one
// at the call site.
//
// Browse, therefore, and not edit: nothing reachable through here alters
// meaning.
func (s *Store) AdoptCanonicalTitle(ctx context.Context, itemID int64, title string) error {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}

	it, err := s.GetItem(ctx, itemID)
	if err != nil {
		return err
	}
	if it.Title == title {
		return nil // already spelled that way
	}
	if release.NormaliseTitle(it.Title) != release.NormaliseTitle(title) {
		return fmt.Errorf("%w: %q and %q are not the same title, so this is a "+
			"rename rather than a spelling; use Relabel, which needs a person",
			ErrNotTheSameTitle, it.Title, title)
	}

	return s.updateItem(ctx, itemID,
		[]string{"updated_at = ?", "title = ?", "sort_title = ?"},
		[]any{s.now().UTC().Format(timeLayout), title, SortTitle(title)})
}

func (s *Store) updateItem(ctx context.Context, itemID int64, sets []string, args []any) error {
	args = append(args, itemID)
	res, err := s.db.ExecContext(ctx,
		`UPDATE media_item SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("importer: updating a library item: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrItemNotFound
	}
	return nil
}

// FileForPlayback resolves a file id to where it lives.
//
// Browse, because it is a read of the library. It returns a root-folder id and
// a RELATIVE path — never an absolute one — so that the caller has to go
// through a vault to reach the bytes and cannot assemble a host path from what
// this returns (ADR-0015).
func (s *Store) FileForPlayback(ctx context.Context, fileID int64) (playback.FileRef, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return playback.FileRef{}, err
	}
	files, err := s.queryFiles(ctx, visibleTo(ctx), `WHERE id = ?`, fileID)
	if err != nil {
		return playback.FileRef{}, err
	}
	if len(files) == 0 {
		return playback.FileRef{}, ErrFileNotFound
	}
	f := files[0]
	return playback.FileRef{
		ID:           f.ID,
		RootFolderID: f.RootFolderID,
		RelativePath: f.RelPath,
		SizeBytes:    f.SizeBytes,
	}, nil
}

// FileSubject is what a subtitle search needs about a file (ADR-0055): where
// it is, and what it is of.
type FileSubject struct {
	FileID       int64
	RootFolderID int64
	RelPath      string
	SizeBytes    int64
	Kind         string
	Title        string
	// TMDBID is the film's, or the series' for an episode.
	TMDBID  int64
	Season  int
	Episode int
}

// FileSubject reads a file and its title through the caller's scope.
func (s *Store) FileSubject(ctx context.Context, fileID int64) (FileSubject, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return FileSubject{}, err
	}
	files, err := s.queryFiles(ctx, visibleTo(ctx), `WHERE id = ?`, fileID)
	if err != nil {
		return FileSubject{}, err
	}
	if len(files) == 0 {
		return FileSubject{}, ErrFileNotFound
	}
	f := files[0]
	it, err := s.GetItem(ctx, f.ItemID)
	if err != nil {
		return FileSubject{}, err
	}
	out := FileSubject{FileID: f.ID, RootFolderID: f.RootFolderID, RelPath: f.RelPath, SizeBytes: f.SizeBytes,
		Kind: it.Kind, Title: it.Title, TMDBID: it.TMDBID}
	if f.Season != nil {
		out.Season = *f.Season
	}
	if f.Episode != nil {
		out.Episode = *f.Episode
	}
	return out, nil
}

// SeriesForRefresh reports the little an episode refresh needs about an item.
//
// Read-only and browse, like the rest of this store's read path: the caller
// holds the permission. It returns the KIND as well as the id, because the
// refresher must refuse a film rather than ask a series endpoint about one —
// TMDB answers /tv/{id} for an id that is really a film's, with somebody else's
// series.
func (s *Store) SeriesForRefresh(ctx context.Context, itemID int64) (tv.SeriesRef, error) {
	if err := authz.RequirePermission(ctx, authz.PermBrowse); err != nil {
		return tv.SeriesRef{}, err
	}
	it, err := s.GetItem(ctx, itemID)
	if err != nil {
		return tv.SeriesRef{}, err
	}
	return tv.SeriesRef{
		ID: it.ID, Title: it.Title, Kind: it.Kind, TMDBID: it.TMDBID,
	}, nil
}

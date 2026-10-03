package music

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jakethecake75/cmediastack/internal/authz"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/library"
	"github.com/jakethecake75/cmediastack/internal/platform/audit"
	"github.com/jakethecake75/cmediastack/internal/platform/db"
)

// Catalogue is what the service asks MusicBrainz — an interface so tests can
// answer without the network.
type Catalogue interface {
	SearchArtists(ctx context.Context, name string) ([]ArtistMatch, error)
	Artist(ctx context.Context, mbid string) (ArtistMatch, error)
	Albums(ctx context.Context, artist string) ([]ReleaseGroup, error)
	Tracks(ctx context.Context, releaseGroup string) (string, []TrackInfo, error)
}

// RootLister is the slice of the root-folder store adding needs.
type RootLister interface {
	List(ctx context.Context) ([]library.RootFolder, error)
}

// Errors of adding.
var (
	// ErrNoRootFolder means no music root folder is configured, or the caller
	// may see none.
	ErrNoRootFolder = errors.New("music: no root folder holds music")
	// ErrChooseRootFolder means several could, and one must be named.
	ErrChooseRootFolder = errors.New("music: more than one root folder holds music; choose one")
	// ErrWrongRootFolder means the named one does not exist or holds something else.
	ErrWrongRootFolder = errors.New("music: that root folder does not hold music")
)

// Service adds artists and keeps their catalogue.
type Service struct {
	db        *db.DB
	store     *Store
	roots     RootLister
	catalogue Catalogue
	audit     *audit.Logger
}

// NewService builds one.
func NewService(database *db.DB, store *Store, roots RootLister, catalogue Catalogue,
	auditLog *audit.Logger) *Service {
	return &Service{db: database, store: store, roots: roots, catalogue: catalogue, audit: auditLog}
}

// Store exposes the catalogue store.
func (svc *Service) Store() *Store { return svc.store }

// SearchArtists asks MusicBrainz. It spends a request, so it is library.edit,
// as searching the film and series provider is.
func (svc *Service) SearchArtists(ctx context.Context, name string) ([]ArtistMatch, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return nil, err
	}
	return svc.catalogue.SearchArtists(ctx, name)
}

// AddRequest is an artist to follow.
type AddRequest struct {
	MBID         string
	RootFolderID int64
	Folder       string
	Monitor      string
	SourceIP     string
	UserAgent    string
}

// AddResult is what was added.
type AddResult struct {
	Item   importer.Item
	Albums int
	Wanted int
}

// Add follows an artist: the artist, every album and EP, and the monitoring
// chosen, in one transaction or not at all (ADR-0044, decision 3). Nothing is
// written to disk. Track lists are fetched later, when first needed.
func (svc *Service) Add(ctx context.Context, req AddRequest) (AddResult, error) {
	if err := authz.RequirePermission(ctx, authz.PermEditLibraryItems); err != nil {
		return AddResult{}, err
	}
	switch req.Monitor {
	case MonitorAll, MonitorFuture, MonitorLatest, MonitorNone:
	default:
		return AddResult{}, ErrNoSuchMonitoring
	}
	root, err := svc.chooseRoot(ctx, req.RootFolderID)
	if err != nil {
		return AddResult{}, err
	}
	artist, err := svc.catalogue.Artist(ctx, req.MBID)
	if err != nil {
		return AddResult{}, err
	}
	albums, err := svc.catalogue.Albums(ctx, artist.MBID)
	if err != nil {
		return AddResult{}, err
	}
	folder := strings.TrimSpace(req.Folder)
	if folder == "" {
		if folder, err = importer.FolderFor(artist.Name, 0); err != nil {
			return AddResult{}, err
		}
	}

	var id int64
	err = svc.db.InTx(ctx, func(tx db.Execer) error {
		var err error
		if id, err = svc.store.AddArtist(ctx, tx, ArtistInput{MBID: artist.MBID, Name: artist.Name,
			SortName: artist.SortName, RootFolderID: root.ID, Folder: folder}); err != nil {
			return err
		}
		if err := svc.store.UpsertAlbums(ctx, tx, id, albums); err != nil {
			return err
		}
		if err := svc.store.ApplyMonitoring(ctx, tx, id, req.Monitor); err != nil {
			return err
		}
		return svc.store.MarkRefreshed(ctx, tx, id)
	})
	if err != nil {
		return AddResult{}, err
	}
	res := AddResult{Item: importer.Item{ID: id, Kind: KindArtist, Title: artist.Name,
		RootFolderID: root.ID, Folder: folder}, Albums: len(albums)}
	if list, err := svc.store.Albums(ctx, id); err == nil {
		for _, a := range list {
			if a.Monitored && released(a.Released, svc.store.now()) {
				res.Wanted++
			}
		}
	}
	if p := authz.FromContext(ctx); p != nil && svc.audit != nil {
		_ = svc.audit.Write(ctx, audit.Event{
			ActorUserID: &p.UserID, ActorLabel: p.Username, Action: audit.ActionMediaAdded,
			TargetKind: "media_item", TargetID: fmt.Sprint(id), SourceIP: req.SourceIP, UserAgent: req.UserAgent,
			Detail: fmt.Sprintf("%s (artist) added with %d album(s), monitoring %s, into %s/%s",
				artist.Name, len(albums), req.Monitor, root.Path, folder),
		})
	}
	return res, nil
}

func (svc *Service) chooseRoot(ctx context.Context, id int64) (library.RootFolder, error) {
	all, err := svc.roots.List(ctx)
	if err != nil {
		return library.RootFolder{}, err
	}
	var fitting []library.RootFolder
	for _, r := range all {
		if id != 0 && r.ID == id {
			if r.Kind != library.KindMusic {
				return library.RootFolder{}, fmt.Errorf("%w: %s holds %s", ErrWrongRootFolder, r.Path, r.Kind)
			}
			return r, nil
		}
		if r.Kind == library.KindMusic {
			fitting = append(fitting, r)
		}
	}
	switch {
	case id != 0:
		return library.RootFolder{}, fmt.Errorf("%w: there is no root folder %d", ErrWrongRootFolder, id)
	case len(fitting) == 0:
		return library.RootFolder{}, ErrNoRootFolder
	case len(fitting) > 1:
		return library.RootFolder{}, ErrChooseRootFolder
	}
	return fitting[0], nil
}

// Album returns an album with its tracks, fetching the track list the first
// time it is asked for (ADR-0044, decision 3).
func (svc *Service) Album(ctx context.Context, albumID int64) (Album, error) {
	a, err := svc.store.Album(ctx, albumID)
	if err != nil || a.TracksKnown {
		return a, err
	}
	release, tracks, err := svc.catalogue.Tracks(ctx, a.MBID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// An album with no official release has no track list to wait
			// for; recording an empty one stops it being asked every time.
			tracks = nil
		} else {
			return a, nil //nolint:nilerr // the album is still worth showing; the tracks come later
		}
	}
	if err := svc.store.SetTracks(ctx, albumID, release, tracks); err != nil {
		return a, err
	}
	return svc.store.Album(ctx, albumID)
}

// RefreshPass is what one run of the music refresh did.
type RefreshPass struct {
	Artists, Albums, TrackLists int
}

// Summary says it for the tasks screen.
func (p RefreshPass) Summary() string {
	return fmt.Sprintf("%d artist(s) refreshed (%d album(s)); %d track list(s) fetched",
		p.Artists, p.Albums, p.TrackLists)
}

// Refresh asks MusicBrainz for the albums of artists not asked for in a week,
// and the track lists of monitored albums that have none. It stops at the
// first error: MusicBrainz asked to be left alone, and what was not asked is
// asked next time.
func (svc *Service) Refresh(ctx context.Context, artists, trackLists int) (RefreshPass, error) {
	var pass RefreshPass
	refs, err := svc.store.ArtistsToRefresh(ctx, 7*24*time.Hour, artists)
	if err != nil {
		return pass, err
	}
	for _, a := range refs {
		groups, err := svc.catalogue.Albums(ctx, a.MBID)
		if err != nil {
			return pass, fmt.Errorf("asking about %s: %w", a.Name, err)
		}
		if err := svc.db.InTx(ctx, func(tx db.Execer) error {
			if err := svc.store.UpsertAlbums(ctx, tx, a.ID, groups); err != nil {
				return err
			}
			return svc.store.MarkRefreshed(ctx, tx, a.ID)
		}); err != nil {
			return pass, err
		}
		pass.Artists++
		pass.Albums += len(groups)
	}
	albums, err := svc.store.AlbumsWithoutTracks(ctx, trackLists)
	if err != nil {
		return pass, err
	}
	for _, a := range albums {
		release, tracks, err := svc.catalogue.Tracks(ctx, a.MBID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return pass, fmt.Errorf("asking about %s: %w", a.Title, err)
		}
		if err := svc.store.SetTracks(ctx, a.ID, release, tracks); err != nil {
			return pass, err
		}
		pass.TrackLists++
	}
	return pass, nil
}

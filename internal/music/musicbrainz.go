// Package music is the Lidarr half of Phase 5 (ADR-0044): artists followed as
// titles, their albums and tracks from MusicBrainz, and what is missing.
package music

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultMusicBrainzBase is the public MusicBrainz web service.
const DefaultMusicBrainzBase = "https://musicbrainz.org/ws/2"

// Errors callers distinguish.
var (
	// ErrNotFound means MusicBrainz has no such artist or album.
	ErrNotFound = errors.New("music: MusicBrainz has no such entry")
	// ErrUnavailable means MusicBrainz could not be asked, or refused.
	ErrUnavailable = errors.New("music: MusicBrainz is unavailable")
)

// ArtistMatch is one candidate from a search.
type ArtistMatch struct {
	MBID           string
	Name           string
	SortName       string
	Disambiguation string
	Country        string
	Type           string
	Begin          string
}

// ReleaseGroup is an album or EP of an artist.
type ReleaseGroup struct {
	MBID     string
	Title    string
	Type     string // album | ep
	Released string // a date, a month or a year; "" unknown
}

// TrackInfo is one track of a release.
type TrackInfo struct {
	Disc     int
	Number   int
	Title    string
	LengthMS int
	MBID     string // the recording
}

// MusicBrainz is a client for its JSON web service.
//
// MusicBrainz needs no key, and asks two things of every client: a User-Agent
// that names it and says how to reach whoever runs it, and at most one request
// a second. Both are done here, so no caller can forget them (ADR-0044).
type MusicBrainz struct {
	client    *http.Client
	base      string
	userAgent string

	mu       sync.Mutex
	last     time.Time
	interval time.Duration
}

// NewMusicBrainz builds a client. version goes into the User-Agent.
func NewMusicBrainz(client *http.Client, base, version string) *MusicBrainz {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if base == "" {
		base = DefaultMusicBrainzBase
	}
	return &MusicBrainz{client: client, base: strings.TrimRight(base, "/"),
		userAgent: "CMediaStack/" + version + " ( https://github.com/jakethecake75/cmediastack )",
		interval:  time.Second}
}

// wait holds a request until a second has passed since the last one.
func (m *MusicBrainz) wait(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d := m.interval - time.Since(m.last); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	m.last = time.Now()
	return nil
}

func (m *MusicBrainz) get(ctx context.Context, path string, q url.Values, out any) error {
	if err := m.wait(ctx); err != nil {
		return err
	}
	if q == nil {
		q = url.Values{}
	}
	q.Set("fmt", "json")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", m.userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("%w: reading the answer: %w", ErrUnavailable, err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest:
		return ErrNotFound
	case resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: it asked this instance to slow down (%d)", ErrUnavailable, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("%w: it answered %d", ErrUnavailable, resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: the answer was not the expected shape: %w", ErrUnavailable, err)
	}
	return nil
}

type mbArtist struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	SortName       string `json:"sort-name"`
	Disambiguation string `json:"disambiguation"`
	Country        string `json:"country"`
	Type           string `json:"type"`
	LifeSpan       struct {
		Begin string `json:"begin"`
	} `json:"life-span"`
}

func (a mbArtist) match() ArtistMatch {
	return ArtistMatch{MBID: a.ID, Name: a.Name, SortName: a.SortName,
		Disambiguation: a.Disambiguation, Country: a.Country, Type: a.Type, Begin: a.LifeSpan.Begin}
}

// SearchArtists finds artists by name, best first.
func (m *MusicBrainz) SearchArtists(ctx context.Context, name string) ([]ArtistMatch, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("music: a search needs a name")
	}
	q := url.Values{}
	q.Set("query", name)
	q.Set("limit", "10")
	var page struct {
		Artists []mbArtist `json:"artists"`
	}
	if err := m.get(ctx, "/artist", q, &page); err != nil {
		return nil, err
	}
	out := make([]ArtistMatch, 0, len(page.Artists))
	for _, a := range page.Artists {
		if a.ID != "" && a.Name != "" {
			out = append(out, a.match())
		}
	}
	return out, nil
}

// Artist looks one artist up by id.
func (m *MusicBrainz) Artist(ctx context.Context, mbid string) (ArtistMatch, error) {
	if !validMBID(mbid) {
		return ArtistMatch{}, ErrNotFound
	}
	var a mbArtist
	if err := m.get(ctx, "/artist/"+mbid, nil, &a); err != nil {
		return ArtistMatch{}, err
	}
	if a.ID == "" || a.Name == "" {
		return ArtistMatch{}, fmt.Errorf("%w: the answer named no artist", ErrUnavailable)
	}
	return a.match(), nil
}

// Albums lists an artist's albums and EPs: release groups whose primary type
// is Album or EP and which have no secondary type — no compilations, live
// albums, remixes or soundtracks (ADR-0044, decision 2).
func (m *MusicBrainz) Albums(ctx context.Context, artist string) ([]ReleaseGroup, error) {
	if !validMBID(artist) {
		return nil, ErrNotFound
	}
	var out []ReleaseGroup
	for offset := 0; offset < 500; offset += 100 {
		q := url.Values{}
		q.Set("artist", artist)
		q.Set("type", "album|ep")
		q.Set("limit", "100")
		q.Set("offset", fmt.Sprint(offset))
		var page struct {
			Groups []struct {
				ID             string   `json:"id"`
				Title          string   `json:"title"`
				PrimaryType    string   `json:"primary-type"`
				SecondaryTypes []string `json:"secondary-types"`
				FirstRelease   string   `json:"first-release-date"`
			} `json:"release-groups"`
			Count int `json:"release-group-count"`
		}
		if err := m.get(ctx, "/release-group", q, &page); err != nil {
			return nil, err
		}
		for _, g := range page.Groups {
			kind := strings.ToLower(g.PrimaryType)
			if g.ID == "" || g.Title == "" || len(g.SecondaryTypes) > 0 || (kind != "album" && kind != "ep") {
				continue
			}
			out = append(out, ReleaseGroup{MBID: g.ID, Title: g.Title, Type: kind, Released: g.FirstRelease})
		}
		if offset+100 >= page.Count || len(page.Groups) == 0 {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return releasedKey(out[i].Released) < releasedKey(out[j].Released) })
	return out, nil
}

// Tracks returns the track list of an album's earliest official release, and
// which release that was.
func (m *MusicBrainz) Tracks(ctx context.Context, releaseGroup string) (string, []TrackInfo, error) {
	if !validMBID(releaseGroup) {
		return "", nil, ErrNotFound
	}
	q := url.Values{}
	q.Set("release-group", releaseGroup)
	q.Set("status", "official")
	q.Set("inc", "recordings+media")
	q.Set("limit", "100")
	var page struct {
		Releases []struct {
			ID    string `json:"id"`
			Date  string `json:"date"`
			Media []struct {
				Position int `json:"position"`
				Tracks   []struct {
					Position  int    `json:"position"`
					Title     string `json:"title"`
					Length    int    `json:"length"`
					Recording struct {
						ID string `json:"id"`
					} `json:"recording"`
				} `json:"tracks"`
			} `json:"media"`
		} `json:"releases"`
	}
	if err := m.get(ctx, "/release", q, &page); err != nil {
		return "", nil, err
	}
	if len(page.Releases) == 0 {
		return "", nil, ErrNotFound
	}
	sort.SliceStable(page.Releases, func(i, j int) bool {
		a, b := releasedKey(page.Releases[i].Date), releasedKey(page.Releases[j].Date)
		if a != b {
			return a < b
		}
		return page.Releases[i].ID < page.Releases[j].ID
	})
	rel := page.Releases[0]
	var tracks []TrackInfo
	for i, medium := range rel.Media {
		disc := medium.Position
		if disc <= 0 {
			disc = i + 1
		}
		for _, t := range medium.Tracks {
			if t.Position <= 0 || strings.TrimSpace(t.Title) == "" {
				continue
			}
			tracks = append(tracks, TrackInfo{Disc: disc, Number: t.Position, Title: t.Title,
				LengthMS: t.Length, MBID: t.Recording.ID})
		}
	}
	return rel.ID, tracks, nil
}

// releasedKey sorts dates with the unknown last.
func releasedKey(d string) string {
	if d == "" {
		return "9999"
	}
	return d
}

// validMBID is the shape of a MusicBrainz id: a UUID. Anything else is not
// sent — an id is interpolated into a path.
func validMBID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				return false
			}
		}
	}
	return true
}

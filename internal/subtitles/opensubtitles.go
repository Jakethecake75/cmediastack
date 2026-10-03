// Package subtitles fetches a subtitle for a file from OpenSubtitles.com
// (ADR-0055): the Bazarr half of the stack.
package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBase is OpenSubtitles.com's REST API.
const DefaultBase = "https://api.opensubtitles.com/api/v1"

// Errors callers distinguish.
var (
	// ErrNotConfigured means no API key has been entered.
	ErrNotConfigured = errors.New("subtitles: OpenSubtitles is not configured; enter an API key on the Metadata screen")
	// ErrKeyRefused means OpenSubtitles refused the key or the account.
	ErrKeyRefused = errors.New("subtitles: OpenSubtitles refused the API key or the account")
	// ErrQuota means the day's downloads are spent.
	ErrQuota = errors.New("subtitles: today's OpenSubtitles downloads are used up")
	// ErrUnavailable means OpenSubtitles could not be asked, or failed.
	ErrUnavailable = errors.New("subtitles: OpenSubtitles is unavailable")
	// ErrNotASubtitle means what was downloaded is not a subtitle.
	ErrNotASubtitle = errors.New("subtitles: what OpenSubtitles sent is not an SRT subtitle")
)

// MaxSubtitleBytes bounds a downloaded subtitle.
const MaxSubtitleBytes = 2 << 20

// Query is one search.
type Query struct {
	Language string
	// Hash is the file's OpenSubtitles hash; empty when it could not be read.
	Hash string
	// TMDBID is a film's; ParentTMDBID a series' with its season and episode.
	TMDBID       int64
	ParentTMDBID int64
	Season       int
	Episode      int
}

// Result is one subtitle on offer.
type Result struct {
	FileID        int64
	FileName      string
	Language      string
	Release       string
	Downloads     int
	HashMatch     bool
	Translated    bool // machine- or AI-translated
	HearingImpair bool
	// The title it says it is for, when it says: a film's TMDB id, or a
	// series' with a season and an episode.
	TMDBID       int64
	ParentTMDBID int64
	Season       int
	Episode      int
}

// Download is a link to fetch, and what OpenSubtitles says of the day's quota.
type Download struct {
	Link      string
	Remaining int
	ResetTime string
}

// Client asks OpenSubtitles.com.
type Client struct {
	http      *http.Client
	base      string
	userAgent string

	mu    sync.Mutex
	token string
	until time.Time
	now   func() time.Time
}

// NewClient builds one. version goes into the User-Agent OpenSubtitles asks
// for ("Name vX").
func NewClient(client *http.Client, base, version string) *Client {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if base == "" {
		base = DefaultBase
	}
	return &Client{http: client, base: strings.TrimRight(base, "/"), userAgent: "CMediaStack v" + version,
		now: time.Now}
}

// Credentials are what a request is made with.
type Credentials struct {
	APIKey   string
	Username string
	Password string
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any, cred Credentials,
	token string, out any) (int, error) {
	u := c.base + path
	if len(q) > 0 {
		// Sorted and lower-case, as OpenSubtitles asks, so it answers rather
		// than redirects.
		keys := make([]string, 0, len(q))
		for k := range q {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(strings.ToLower(q.Get(k))))
		}
		u += "?" + strings.Join(parts, "&")
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Api-Key", cred.APIKey)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, fmt.Errorf("%w: reading the answer: %w", ErrUnavailable, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return resp.StatusCode, ErrKeyRefused
	case resp.StatusCode == http.StatusNotAcceptable:
		return resp.StatusCode, ErrQuota
	case resp.StatusCode != http.StatusOK:
		return resp.StatusCode, fmt.Errorf("%w: it answered %d", ErrUnavailable, resp.StatusCode)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return resp.StatusCode, fmt.Errorf("%w: the answer was not the expected shape: %w", ErrUnavailable, err)
	}
	return resp.StatusCode, nil
}

// login signs the account in, when one is configured, and keeps the token in
// memory for most of a day.
func (c *Client) login(ctx context.Context, cred Credentials) (string, error) {
	if cred.Username == "" || cred.Password == "" {
		return "", nil
	}
	c.mu.Lock()
	if c.token != "" && c.now().Before(c.until) {
		t := c.token
		c.mu.Unlock()
		return t, nil
	}
	c.mu.Unlock()
	var out struct {
		Token string `json:"token"`
	}
	if _, err := c.do(ctx, http.MethodPost, "/login", nil,
		map[string]string{"username": cred.Username, "password": cred.Password}, cred, "", &out); err != nil {
		return "", err
	}
	c.mu.Lock()
	c.token, c.until = out.Token, c.now().Add(20*time.Hour)
	c.mu.Unlock()
	return out.Token, nil
}

// Forget drops the session token, so the next request signs in afresh.
func (c *Client) Forget() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

type osResult struct {
	Attributes struct {
		Language          string `json:"language"`
		DownloadCount     int    `json:"download_count"`
		HearingImpaired   bool   `json:"hearing_impaired"`
		AITranslated      bool   `json:"ai_translated"`
		MachineTranslated bool   `json:"machine_translated"`
		MoviehashMatch    bool   `json:"moviehash_match"`
		Release           string `json:"release"`
		FeatureDetails    struct {
			TMDBID        int64 `json:"tmdb_id"`
			ParentTMDBID  int64 `json:"parent_tmdb_id"`
			SeasonNumber  int   `json:"season_number"`
			EpisodeNumber int   `json:"episode_number"`
		} `json:"feature_details"`
		Files []struct {
			FileID   int64  `json:"file_id"`
			FileName string `json:"file_name"`
		} `json:"files"`
	} `json:"attributes"`
}

// Search asks for subtitles.
func (c *Client) Search(ctx context.Context, cred Credentials, q Query) ([]Result, error) {
	v := url.Values{}
	v.Set("languages", q.Language)
	if q.Hash != "" {
		v.Set("moviehash", q.Hash)
	}
	switch {
	case q.ParentTMDBID > 0:
		v.Set("parent_tmdb_id", strconv.FormatInt(q.ParentTMDBID, 10))
		v.Set("season_number", strconv.Itoa(q.Season))
		v.Set("episode_number", strconv.Itoa(q.Episode))
	case q.TMDBID > 0:
		v.Set("tmdb_id", strconv.FormatInt(q.TMDBID, 10))
	}
	var page struct {
		Data []osResult `json:"data"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/subtitles", v, nil, cred, "", &page); err != nil {
		return nil, err
	}
	var out []Result
	for _, d := range page.Data {
		a := d.Attributes
		if len(a.Files) == 0 || a.Files[0].FileID <= 0 {
			continue
		}
		out = append(out, Result{
			FileID: a.Files[0].FileID, FileName: a.Files[0].FileName, Language: strings.ToLower(a.Language),
			Release: a.Release, Downloads: a.DownloadCount, HashMatch: a.MoviehashMatch,
			Translated: a.AITranslated || a.MachineTranslated, HearingImpair: a.HearingImpaired,
			TMDBID: a.FeatureDetails.TMDBID, ParentTMDBID: a.FeatureDetails.ParentTMDBID,
			Season: a.FeatureDetails.SeasonNumber, Episode: a.FeatureDetails.EpisodeNumber,
		})
	}
	return out, nil
}

// Download asks for one subtitle's link, signed in when an account is set.
func (c *Client) Download(ctx context.Context, cred Credentials, fileID int64) (Download, error) {
	token, err := c.login(ctx, cred)
	if err != nil {
		return Download{}, err
	}
	var out struct {
		Link      string `json:"link"`
		Remaining int    `json:"remaining"`
		ResetTime string `json:"reset_time"`
	}
	if _, err := c.do(ctx, http.MethodPost, "/download", nil,
		map[string]any{"file_id": fileID, "sub_format": "srt"}, cred, token, &out); err != nil {
		if errors.Is(err, ErrKeyRefused) {
			c.Forget()
		}
		return Download{}, err
	}
	return Download{Link: out.Link, Remaining: out.Remaining, ResetTime: out.ResetTime}, nil
}

// Fetch reads a download link: https only, at most MaxSubtitleBytes, and only
// what looks like an SRT (ADR-0055, decision 4).
func (c *Client) Fetch(ctx context.Context, link string) ([]byte, error) {
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("%w: the download link %q is not an https address", ErrNotASubtitle, link)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: the download answered %d", ErrUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxSubtitleBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err := CheckSRT(body); err != nil {
		return nil, err
	}
	return body, nil
}

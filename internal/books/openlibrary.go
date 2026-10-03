// Package books is the Readarr half of Phase 5 (ADR-0048): a book added from
// Open Library on its own, as a film is, and wanted until a file holds it.
package books

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultOpenLibraryBase is the public Open Library.
const DefaultOpenLibraryBase = "https://openlibrary.org"

// Errors callers distinguish.
var (
	// ErrNotFound means Open Library has no such work.
	ErrNotFound = errors.New("books: Open Library has no such work")
	// ErrUnavailable means Open Library could not be asked, or refused.
	ErrUnavailable = errors.New("books: Open Library is unavailable")
	// ErrNotAWork means the id is not an Open Library work id.
	ErrNotAWork = errors.New("books: that is not an Open Library work id (OL…W)")
)

// workID is an Open Library work: OL, digits, W.
var workID = regexp.MustCompile(`^OL[1-9][0-9]{0,11}W$`)

// ValidWorkID reports whether id names a work.
func ValidWorkID(id string) bool { return workID.MatchString(id) }

// Work is one book as Open Library knows it.
type Work struct {
	ID      string // OL59800W
	Title   string
	Authors []string
	Year    int // first published; 0 unknown
	CoverID int64
	// Editions is how many editions Open Library lists: a hint, in a search,
	// of which of several namesakes is the book everybody means.
	Editions int
}

// Author is the first author, or "".
func (w Work) Author() string {
	if len(w.Authors) == 0 {
		return ""
	}
	return w.Authors[0]
}

// OpenLibrary is a client for its search API.
//
// Open Library needs no key, and asks that a client name itself and not
// hammer it: a User-Agent saying what this is and where it comes from, and at
// most one request a second, done here so no caller can forget them
// (ADR-0048, decision 2).
type OpenLibrary struct {
	client    *http.Client
	base      string
	userAgent string

	mu       sync.Mutex
	last     time.Time
	interval time.Duration
}

// Timeout is how long one request to Open Library may take. Its search answers
// in two seconds or in forty for the same question, so it is given longer than
// the other providers (ADR-0048, decision 2).
const Timeout = 45 * time.Second

// NewOpenLibrary builds a client. version goes into the User-Agent.
func NewOpenLibrary(client *http.Client, base, version string) *OpenLibrary {
	if client == nil {
		client = &http.Client{Timeout: Timeout}
	}
	if base == "" {
		base = DefaultOpenLibraryBase
	}
	return &OpenLibrary{client: client, base: strings.TrimRight(base, "/"),
		userAgent: "CMediaStack/" + version + " ( https://github.com/jakethecake75/cmediastack )",
		interval:  time.Second}
}

// wait holds a request until a second has passed since the last one.
func (o *OpenLibrary) wait(ctx context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if d := o.interval - time.Since(o.last); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
	o.last = time.Now()
	return nil
}

type olDoc struct {
	Key       string   `json:"key"`
	Title     string   `json:"title"`
	Authors   []string `json:"author_name"`
	FirstYear int      `json:"first_publish_year"`
	CoverID   int64    `json:"cover_i"`
	Editions  int      `json:"edition_count"`
}

const searchFields = "key,title,author_name,first_publish_year,cover_i,edition_count"

// connectionTries is how many times a request is sent when the connection
// itself fails: Open Library resets a fair share of new connections, and a
// person's search should not fail on one of them. An answer — any status —
// is never retried (ADR-0048, decision 2).
const connectionTries = 2

// search asks /search.json and returns the works it found.
func (o *OpenLibrary) search(ctx context.Context, q string, limit int) ([]Work, error) {
	v := url.Values{}
	v.Set("q", q)
	v.Set("fields", searchFields)
	v.Set("limit", fmt.Sprint(limit))
	var resp *http.Response
	for try := 1; ; try++ {
		if err := o.wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.base+"/search.json?"+v.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", o.userAgent)
		req.Header.Set("Accept", "application/json")
		resp, err = o.client.Do(req)
		if err == nil {
			break
		}
		// A timeout is not retried: the wait was already spent, and a second
		// one would only double it.
		var nerr net.Error
		if try >= connectionTries || ctx.Err() != nil || (errors.As(err, &nerr) && nerr.Timeout()) {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: reading the answer: %w", ErrUnavailable, err)
	}
	switch {
	case resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: it asked this instance to slow down (%d)", ErrUnavailable, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: it answered %d", ErrUnavailable, resp.StatusCode)
	}
	var page struct {
		Docs []olDoc `json:"docs"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("%w: the answer was not the expected shape: %w", ErrUnavailable, err)
	}
	out := make([]Work, 0, len(page.Docs))
	for _, d := range page.Docs {
		id := strings.TrimPrefix(d.Key, "/works/")
		if !ValidWorkID(id) || strings.TrimSpace(d.Title) == "" {
			continue
		}
		out = append(out, Work{ID: id, Title: strings.TrimSpace(d.Title), Authors: d.Authors,
			Year: d.FirstYear, CoverID: d.CoverID, Editions: d.Editions})
	}
	return out, nil
}

// SearchBooks finds works by title, author or both, in Open Library's order.
func (o *OpenLibrary) SearchBooks(ctx context.Context, q string) ([]Work, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, errors.New("books: a search needs words")
	}
	return o.search(ctx, q, 10)
}

// Work reads one work by its id: one search request, by key, which carries
// the authors' names and the first year that the work record does not.
func (o *OpenLibrary) Work(ctx context.Context, id string) (Work, error) {
	if !ValidWorkID(id) {
		return Work{}, ErrNotAWork
	}
	found, err := o.search(ctx, "key:/works/"+id, 2)
	if err != nil {
		return Work{}, err
	}
	for _, w := range found {
		if w.ID == id {
			return w, nil
		}
	}
	return Work{}, ErrNotFound
}

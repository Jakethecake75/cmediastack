package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jakethecake75/cmediastack/internal/books"
	"github.com/jakethecake75/cmediastack/internal/download"
	"github.com/jakethecake75/cmediastack/internal/importer"
	"github.com/jakethecake75/cmediastack/internal/music"
)

type fakeAlbums struct {
	dir     string
	files   []string
	albumID int64
	calls   int
	res     music.ImportResult
	err     error
}

func (f *fakeAlbums) ImportDownload(_ context.Context, dir string, files []string, albumID int64,
	_ string) (music.ImportResult, error) {
	f.calls++
	f.dir, f.files, f.albumID = dir, files, albumID
	return f.res, f.err
}

// ADR-0046, decision 6: a download grabbed for an album goes to the music
// library, never to the video importer, and what came of it is recorded
// against the download like every import.
func TestADownloadForAnAlbumGoesToTheMusicLibrary(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, tc := range map[string]struct {
		albums  *fakeAlbums
		outcome string
		detail  string
		summary string
	}{
		"placed": {&fakeAlbums{res: music.ImportResult{Placed: []string{"a"}, Skipped: []importer.Rejection{{}}}},
			importer.OutcomeImported, "1 track(s) placed", "imported 1"},
		"replaced": {&fakeAlbums{res: music.ImportResult{Replaced: []string{"a"}}},
			importer.OutcomeImported, "1 replaced", "imported 1"},
		"nothing filed": {&fakeAlbums{res: music.ImportResult{Kept: []string{"a"}}},
			importer.OutcomeSkipped, "no track of the album was filed", "skipped 1"},
		"failed": {&fakeAlbums{err: errors.New("the disk is full")},
			importer.OutcomeFailed, "the disk is full", "skipped 1"},
		"no music": {nil, importer.OutcomeSkipped, "music is not wired", "skipped 1"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newImportRig(t)
			hash := strings.Repeat("ef", 20)
			d := &engineless{data: r.downloads, recs: []download.Record{{InfoHash: hash,
				Title: "Portishead - Dummy (1994) [FLAC]", Status: download.StatusComplete,
				Target: &download.Target{ItemID: 3, Album: 30}}}}
			d.onDisk = func(string) ([]download.TransferFile, error) {
				return []download.TransferFile{{Path: "Dummy/01 - Mysterons.flac", Bytes: 3 << 20}}, nil
			}
			var albums albumImporter
			if tc.albums != nil {
				albums = tc.albums
			}
			summary, err := runImports(r.ctx, d, r.store, r.imp, albums, nil, quiet)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(summary, tc.summary) {
				t.Errorf("summary %q, want %q", summary, tc.summary)
			}
			if tc.albums != nil {
				if tc.albums.calls != 1 || tc.albums.albumID != 30 || tc.albums.dir != filepath.Join(r.downloads, hash) ||
					len(tc.albums.files) != 1 || tc.albums.files[0] != "Dummy/01 - Mysterons.flac" {
					t.Errorf("the music library was asked %+v", tc.albums)
				}
			}
			recs, err := r.store.RecordsFor(r.ctx, hash)
			if err != nil || len(recs) != 1 {
				t.Fatalf("records %+v %v", recs, err)
			}
			if recs[0].Outcome != tc.outcome || !strings.Contains(recs[0].Detail, tc.detail) {
				t.Errorf("recorded %s %q, want %s %q", recs[0].Outcome, recs[0].Detail, tc.outcome, tc.detail)
			}
		})
	}
}

type fakeShelf struct {
	itemID int64
	calls  int
	res    books.ImportResult
	err    error
}

func (f *fakeShelf) ImportDownload(_ context.Context, _ string, _ []string, itemID int64,
	_ string) (books.ImportResult, error) {
	f.calls++
	f.itemID = itemID
	return f.res, f.err
}

// ADR-0049, decision 5: a download grabbed for a book goes to the books
// library, and what came of it is recorded against the download.
func TestADownloadForABookGoesToTheBooksLibrary(t *testing.T) {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, tc := range map[string]struct {
		shelf   *fakeShelf
		outcome string
		detail  string
	}{
		"filed":     {&fakeShelf{res: books.ImportResult{Placed: "B/Dune - Frank Herbert.epub"}}, importer.OutcomeImported, "filed B/Dune"},
		"kept":      {&fakeShelf{res: books.ImportResult{Kept: "B/Dune.epub"}}, importer.OutcomeSkipped, "not better"},
		"no book":   {&fakeShelf{}, importer.OutcomeSkipped, "no book file"},
		"failed":    {&fakeShelf{err: errors.New("the disk is full")}, importer.OutcomeFailed, "the disk is full"},
		"not wired": {nil, importer.OutcomeSkipped, "books are not wired"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newImportRig(t)
			hash := strings.Repeat("ab", 20)
			d := &engineless{data: r.downloads, recs: []download.Record{{InfoHash: hash,
				Title: "Frank Herbert - Dune [EPUB]", Status: download.StatusComplete,
				Target: &download.Target{ItemID: 9, Book: true}}}}
			d.onDisk = func(string) ([]download.TransferFile, error) {
				return []download.TransferFile{{Path: "Dune.epub", Bytes: 1 << 20}}, nil
			}
			var shelf bookImporter
			if tc.shelf != nil {
				shelf = tc.shelf
			}
			if _, err := runImports(r.ctx, d, r.store, r.imp, nil, shelf, quiet); err != nil {
				t.Fatal(err)
			}
			if tc.shelf != nil && (tc.shelf.calls != 1 || tc.shelf.itemID != 9) {
				t.Errorf("the books library was asked %+v", tc.shelf)
			}
			recs, err := r.store.RecordsFor(r.ctx, hash)
			if err != nil || len(recs) != 1 || recs[0].Outcome != tc.outcome || !strings.Contains(recs[0].Detail, tc.detail) {
				t.Errorf("recorded %+v %v, want %s %q", recs, err, tc.outcome, tc.detail)
			}
		})
	}
}

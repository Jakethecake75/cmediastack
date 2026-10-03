# ADR-0049: A book's file — found, searched for, grabbed and imported

**Status:** accepted
**Date:** 2026-09-30
**Related:** [ADR-0015](0015-library-path-containment.md),
[ADR-0026](0026-adding-a-film-and-searching-for-it.md),
[ADR-0046](0046-searching-for-and-grabbing-an-album.md), [ADR-0048](0048-books-from-open-library.md)

## The problem

A book can be added and wanted (ADR-0048), and nothing can give it a file. A
book needs what music got in two increments (ADR-0045, ADR-0046): files on
disk recognised, and a release searched for, grabbed and imported. A book is
simpler than an album, because it is one file, not twelve.

## Decisions

### 1. A book file, and the format ladder

A book file is `.epub`, `.azw3`, `.mobi` or `.pdf`. Its quality is its format,
ranked **EPUB > AZW3 > MOBI > PDF**. EPUB reflows and is open; AZW3 and MOBI
reflow on fewer readers; a PDF is a page image of somebody else's layout. A
book has **one** file. One in a better format replaces it, and the old one
goes to the trash, as an upgraded film's does. A worse or equal one is left
where it is. Audiobooks (`.m4b`, `.mp3`) are not books here.

### 2. Where it is filed

`<book folder>/<Title> - <Author>.<ext>`, every part made safe by the rules
that make a film's name safe (ADR-0015).

### 3. The scan: only a book's own folder

A `books` root is scanned by the books library, not the video importer. A book
file inside a book's folder is that book's. With several formats there, the
best is recorded and the others are reported as lesser formats. A file outside
every book's folder is reported, never guessed at, because the folder is what
the person chose when they added the book. Nothing on disk changes. Most of a
root vanishing is refused as for video.

### 4. Which release is the book

A release name is not parsed. Bracketed parts are set aside: a series, an
edition, a format in `[...]` or `(...)`. So is the trailing `-GROUP` of a scene
name, which has no spaces. In a name with spaces, a hyphen is part of a word.
The rest is folded as every title is, and then:

- the title must appear as **one run** of its words;
- every word **outside** that run must be a word of the author's name, *by*, a
  year or a tag. Tags are formats, `retail`, `ebook`, `kindle` and the like;
- the author's **surname** must be among those words. *Dune* by someone else
  is another book.

So `Frank Herbert - Dune (1965) [EPUB]`, `Dune by Frank Herbert epub` and
`Herbert, Frank - Dune.azw3` are *Dune*. `Frank Herbert - Dune Messiah` is not,
because *messiah* is outside the run, and nor is `Dune - Brian Herbert`, because
*brian* is not a word of Frank Herbert's name. A title that contains its
author's name, such as *Oscar Wilde: A Life* by Oscar Wilde, is found as its
own run. `Frank Herbert - Dune-Messiah` keeps its *messiah*, so it is not *Dune*.

### 5. Searching, the target, the import

- **Searching.** The book's existing search route, `POST /api/v1/media/{id}/search`
  (`acquisition.search`), searches for a book when the title is one. It asks
  Torznab category **7000 (Books)** for `<author surname> <title>`, folded.
  Only a release that is the book and names its format carries a ticket. The
  best format comes first, then seeders. A name with no format is refused.
- **The target.** The book is sealed in the ticket and on the queue row as a
  fifth kind of target, `book`, beside episode, film, season and album.
  Migration 0029 widens the column as 0019 and 0027 did. A book target is its
  item and nothing else.
- **The import.** A completed download with a book target goes to the books
  library, never the video importer. Its best book file is filed by decision 2,
  hard-linked or copied across filesystems, and the outcome is recorded against
  the download. A download holding no book file is skipped, saying so.

### 6. Serving

A book is served as its file. The *Download* the file list already offers is
`media.download_original` (ADR-0038), and reading in the browser is not
planned. A book's file row has no *Play*.

## Not decided here

Fetching wanted books without a person is the next increment.

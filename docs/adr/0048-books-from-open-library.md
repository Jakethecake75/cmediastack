# ADR-0048: Books — a book added from Open Library, and wanted until a file holds it

**Status:** accepted
**Date:** 2026-09-30
**Related:** [ADR-0026](0026-adding-a-film-and-searching-for-it.md),
[ADR-0037](0037-libraries-and-rating-ceilings.md), [ADR-0044](0044-music-artists-albums-and-tracks.md)

## The problem

Phase 5 is music and books, and the operator chose both. Music is followed by
artist (ADR-0044). `media_item` has accepted the kind `book`, with an
`openlibrary_id` and an `author`, since migration 0026, and root folders have
accepted `books` since Phase 3. Nothing adds one.

## Decisions

### 1. A book is added on its own, as a film is, not by following an author

Readarr follows authors. Open Library's list of an author's works is not a
bibliography. It holds study guides, omnibus editions, translations listed as
separate works, and other people's books about the author. A search for *The
Left Hand of Darkness* returns Le Guin's novel and also *Ursula K. Le Guin's The
Left Hand of Darkness* by Harold Bloom. Monitoring "all" of that would want
dozens of things nobody asked for. A person picks the book, as they pick a
film. Following an author is recorded as not done.

### 2. Open Library, politely

Open Library needs no key. As with MusicBrainz, the client sends a User-Agent
that names this software and how to reach it, makes at most one request a
second, and goes through the egress guard's `metadata` profile. Open Library
resets a fair share of new connections, so a request whose **connection**
fails is sent once more, after the same one-second wait. An answer, whatever
its status, is never retried, and neither is a timeout. Its search answered
the same question in two seconds and in thirty-eight within a minute while
this was written, so a request is given 45 seconds, not the 20 the other
providers get. A book is
identified by its **work** id (`OL59800W`), refused unless it has that shape.
It is read with one search request, `q=key:/works/<id>`, which returns the
title, the authors and the first year of publication together.

### 3. Adding a book

`POST /api/v1/media` with `kind: book` and an `openlibrary_id`, under
`library.edit`, as a film is added:

- It lives in a root folder of kind `books`. With one, that one; with several,
  the request names one.
- Its folder is `<Author> - <Title> (<Year>)`, one component, made safe by the
  rules that make a film's folder safe (ADR-0015). It can be named instead.
- It is refused, nothing written, when the book is already in the library or
  another title holds the folder.
- It is **monitored**, so on the Wanted list until a file holds it, and the
  film's switch (`PUT /api/v1/media/{id}/monitored`) turns that off for a book
  as for a film.
- It is audited as `media.added`. Nothing is written to disk.

`GET /api/v1/books/search?q=` asks Open Library, under `library.edit`, as
searching any provider is, because it spends a request.

### 4. Scope

A book is a title like any other: an account limited to some root folders sees
the books in those and no other, and a hidden book answers exactly as a missing
one (ADR-0037). The rating ceiling does not apply. Open Library carries no
certification, and ADR-0044 already confined the ceiling to films and series.

## Not decided here

A book's files (EPUB, AZW3, MOBI, PDF): scanning them, importing a download,
searching for and grabbing a release, and fetching wanted books automatically.
Those are the next increments. Reading in the browser is not planned: a book
is served as its file, which the original-download route already does.

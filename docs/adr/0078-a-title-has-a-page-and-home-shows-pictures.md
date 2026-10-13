# ADR-0078: A title has a page, Home and the library show pictures, and old requests are added

**Status:** accepted
**Date:** 2026-10-12
**Related:** [ADR-0018](0018-metadata-and-artwork.md), [ADR-0022](0022-episode-tracking.md), [ADR-0038](0038-the-library-routes-left-unbuilt.md), [ADR-0069](0069-home-request-settings.md), [ADR-0075](0075-requests-add-themselves-seasons-removals.md)

## The problems

The operator asked for these, after v0.5.0, with three screens of Emby beside
ours:

- **Opening a title went to a list of files.** A series opened on its
  monitoring switches and file paths. It should open on what it is: its
  picture, what it is about, its seasons and episodes, and a Play button.
- **Home and the library should look like a media library**: pictures, wide
  rows, the whole window.
- **Seasons were called "Series 1", "Series 2".**
- **Requests approved before v0.5.0 were still "approved — not in the library
  yet".** Approving has added the title since ADR-0075, and these were
  approved before it.

## Why seasons were called Series

The page showed the provider's own name for a season. For a British series
TMDB's name is "Series 1". The page now says **Season** and the number, and
**Specials** for season 0. The provider's name is still stored and is no
longer shown.

## Decisions

### 1. A title has its own page

`#title/<id>` is a view of its own, reached from every poster. It has its own
address, so Back from the player returns to it and a reload stays on it. It
shows:

- the backdrop, dimmed, behind the poster, the title, and a line of facts: the
  provider's score, the year, how many seasons, the certification, the genres;
- **Play**: a film's file; for a series, the episode the account is part-way
  through (**Resume**), otherwise the first one on disk;
- the overview;
- for a series, one row per season of episode cards, each with its still, its
  number and title, when it aired and how long it runs. A card plays the
  episode when it is on disk, and says *missing*, *airs* or *announced* when
  it is not.

Everything the old page did is kept, under **Files and options** below: the
quality profile, the rating, reporting a problem, asking for a removal,
monitoring, searches, the files themselves and deleting the title. It is open
already for a film with no file, which is when its Search button is the point
of the page.

### 2. Where the page's words and pictures come from

- **Overview, genres, score, runtime, backdrop.** `GET
  /api/v1/media/{id}/details` asks the provider for a title this instance has
  identified, and keeps the answer in memory for a day. It is read in the
  caller's scope: a title the account may not see is a 404, and nothing is
  asked. A title with no provider id answers with nothing, as does a provider
  that cannot be reached; the page is then drawn without them.
- **Backdrop.** `GET /api/v1/media/{id}/artwork?kind=backdrop`, the title's
  existing artwork route. The file name comes from the provider's details and
  the image through the artwork cache (ADR-0018): one filename, one allowed
  host, the bytes checked to be a picture.
- **Episode stills.** The provider's still for each episode is stored with the
  episode (`episode.still_path`, migration 0040) when its season is read, and
  served by `GET /api/v1/episodes/{id}/still` in the caller's scope.

Kept in memory rather than stored, for the title's details: they are read in
one place, and storing them would mean writing them wherever a title is
added, identified or refreshed. The cost is one provider request per title a
day, made when somebody opens it.

### 3. Seasons already read are read once more, for their stills

A settled season is not asked about again (ADR-0022), so it would never gain
stills. `season.stills_read` says whether a season was read since stills were
stored; a season that was not is worth one more request. The migration clears
every series' last refresh time, so the next scheduled refresh starts on them.

### 4. Home and the library

- **Home**: *Continue Watching* as wide cards with the title's backdrop and
  how far through it is, then *Latest Movies* and *Latest TV Shows*, newest
  first, then music and books. A heading opens the library on that kind.
- **Library**: a grid of posters. A poster opens the title's page.
- **Everywhere**: a neutral dark palette, and Home, the library and a title's
  page use the width of the window. Settings and Request keep their narrow
  column, which suits forms.

### 5. A request approved before v0.5.0 is added when an approver next looks

Such a request has no provider id. When an account that may approve requests
and edit the library opens the requests list, each one is completed as
approval now would have: the provider is searched for the request's title,
and when exactly one result has that title and year, it is added, linked to
the request, and searched for. This runs once per request per page load.

It is done in the page, with the approver's own session and the three routes
**Add** already uses, rather than by a background task: no task holds the
authority to add titles to the library, and that is kept.

A request with no year, no exact match, or more than one root folder to
choose from is left as it was, with **Add to the library…**.

## Consequences

- Opening a title can now ask the provider one question. It could already
  cause a poster to be fetched (ADR-0038).
- Two new routes: a title's details and an episode's still.
- Not built: cast and crew, trailers, and a mark on what has been watched.
- Tests: `TestATitlesDetailsAreReadInItsScope`,
  `TestABackdropIsServedFromTheCache`, `TestAnEpisodesStillIsServedInScope`,
  `TestDetailsCarryTheBackdropAndScore`, `TestEpisodesCarryTheirStill`,
  `TestASeasonReadBeforeStillsIsReadOnceMore`,
  `TestTitleDetailsAreKeptForADay`.

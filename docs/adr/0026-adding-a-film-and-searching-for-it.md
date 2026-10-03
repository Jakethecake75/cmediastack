# ADR-0026: Adding a film, and searching for it into the library

**Status:** accepted
**Date:** 2026-09-26
**Related:** [ADR-0019](0019-identification.md), [ADR-0022](0022-episode-tracking.md),
[ADR-0023](0023-searching-for-a-wanted-episode.md),
[ADR-0025](0025-adding-a-series-before-it-is-on-disk.md)
**Built on by:** [ADR-0030](0030-automatic-acquisition.md), which adds the monitored switch
this record rejected, now that there is an automatic search for it to stop.

## The problem

ADR-0025 added series before any of them is on disk and deliberately stopped
short of films (its decision 9). The reason is the one that shaped ADR-0023: a
film acquired through the general search is filed under a folder built from
its **release name**. An added film would acquire a twin the first time a
release named it differently from the provider — `Star.Wars.Episode.IV.A.New.Hope.1977`
is filed as *Star Wars Episode IV A New Hope (1977)*, beside the *Star Wars
(1977)* somebody added. So adding a film is only safe together with a search
that knows which film it is looking for and seals that film into the grab.
This ADR is both.

## Decisions

### 1. A film is added the way a series is, less what a film does not have

`POST /api/v1/media` with `kind: "movie"` follows ADR-0025 decisions 1–6 and 8
unchanged: the provider's id names it, and the title, year and IMDb id come from
the provider's answer; the folder is `Title (Year)` through the same table, or
a name the operator typed, refused rather than rewritten; one item per provider
id and one per folder, with the loser of a race told it lost; the add is
recorded as a person's identification; nothing is created on disk; a
duplicate costs no request. It lives in a root folder for **films**: the only
one when there is one, the operator's choice among several.

What a film does not have is episodes, so it has no monitoring choice. A
`monitor` sent with a film is **refused, not ignored** — the rule the endpoint
already applies to a `title`: a field that means nothing here is either a
caller's mistake or a misunderstanding, and dropping it silently hides both.
One request to the provider, for the film's details.

### 2. Adding a film is wanting it

A film in the library with no file is **wanted**. There is no per-film
monitoring switch: a series' switches choose among hundreds of episodes; a film
is one thing, and whether it is wanted is whether it is in the library. To stop
wanting a film, remove it.

The Wanted list does not consult a release date. The provider's date is its
answer at the moment the film was added, and nothing refreshes a film
(ADR-0022's refresh is for series), so a rule built on it would go stale in the
direction that hides things: a film added before it had a date would never
become wanted. Adding one that is not out yet is a legitimate thing to do —
TMDB lists *Avengers: Secret Wars* with a date in December 2027 and the status
*Planned* — and the Wanted row shows its year. What exists is the search's
answer, not the list's.

### 3. A film search judges every release against the film

The checks run in the order an operator would ask them, and the first that
fails is the answer:

| Check | Refused as | Example refused, for *Dune* (2021) |
|---|---|---|
| The name says what it is | `unparsed` | |
| It is not television: no season, episode or air date | `television` | `Dune.Prophecy.S01E01.1080p.WEB` |
| Its title is one of the film's names | `not_this_film` | `Dune.Part.Two.2024.2160p.WEB-DL` |
| It names a year | `no_year` | `Dune.1080p.WEB.H264` |
| The year is within one of the film's | `not_this_film` | `Dune.1984.1080p.BluRay` |

**The film's names** are its title in the library, the provider's title, its
**original** title and every alternative title the provider lists, compared in
the folded form ADR-0023 uses. The original title has to be added by name: TMDB
does not list it among the alternatives (checked live: *Amélie*, whose original
is *Le Fabuleux Destin d'Amélie Poulain*, and *Spirited Away*). The
alternatives carry the scene's spellings: *Star Wars* (1977) is released as
`Star.Wars.Episode.IV.A.New.Hope.1977`, and *Star Wars: Episode IV - A New Hope*
is one of the alternative titles TMDB lists for it. All of it comes from **one** request,
`/movie/{id}?append_to_response=alternative_titles`, whose film shape
(`alternative_titles.titles`) differs from the series one (`results`) —
checked live, not assumed.

**A year is required**, unlike for an episode. An episode has a season and a
number to tell it from a namesake; a film has only its title and year, and
titles recur — TMDB lists four films titled *Dune*, from 1984, 1989, 2020 and
2021 (checked live). A release without a year cannot be told apart from the
others, so it is refused with that reason. The cost is a year-less release grabbed from the
general search by a person who has looked at it; the alternative is grabbing the
1984 film for the 2021 one.

**One year either way**, the tolerance ADR-0023 uses for series. TMDB's date is
the film's earliest release anywhere — *Dune* is 15 September 2021, five weeks
before its American release — and a film first shown in one country in December
is commonly released as the next year's.

A film with **no year** in the library cannot match anything by these rules.
Its search is refused before any indexer is asked, with that reason; a person
can identify it, or use the general search.

### 4. What the indexers are asked: the title, folded, and the year

`t=search` with `dune 2021`: the title folded as it is compared (accents,
case and punctuation off, so *Amélie* is asked for as `amelie 2001`, the way it
is released) and the year.

- **Not the title alone.** A popular title returns its newer namesakes first —
  *Dune* brings *Part Two* and *Prophecy* ahead of the 2021 film — and an
  indexer's page limit can leave the film off the page entirely. The year
  costs recall only for a release named a year out, and:
- **the term is the person's to change**, as for an episode. A different term
  changes what the indexers are asked, never what can match: every candidate is
  still judged by decision 3.
- **Not the IMDb id** (`t=movie&imdbid=`). Which indexers support it is declared
  in each one's capabilities document, which this software does not read yet;
  sending an indexer a search it does not support is a search that fails, or
  that quietly answers a different question.

### 5. The film travels sealed, as the episode does

A candidate that matches carries a target — the film's item — sealed into its
grab ticket (ADR-0023's mechanism, with a film form beside the episode one). The
grab copies it onto the queue row, and the import files the download into that
film's folder, on that film's root, named from the film's own title:
`Dune (2021)/Dune (2021) [Bluray-1080p].mkv`, whatever the release was called.

The queue row gains a `target_kind` (`episode` or `film`; migration 0015). A
film's target is its item with no season or episode. The kind is a column
rather than inferred from which columns are NULL, because a row should say what
it is for, and "an item and nothing else" reads as half an episode to anyone who
has not been told the convention. Rows already targeted are episodes, and the
migration says so.

The import refuses, with the reason, a film target whose item is gone (it is not
re-created from the release name — the operator deleted it), whose item is not a
film, or whose download turns out to be television. Whether a film already on
disk is replaced is the import's own rule, unchanged: better quality or a newer
revision replaces it; anything else is skipped.

### 6. A transfer says what it is for, by name

The grab's answer and the queue name the item a transfer is for — *Dune (2021)*,
*Severance (2022) S02E03* — looked up from the item when shown rather than
carried in the ticket, which stays as small as it was. An episode's code alone
said which episode and not of what; a film has no code at all. An item deleted
since the grab is named as that — *a film no longer in the library* — which is
also what its import will say.

### 7. Permissions are the existing ones

Adding a film is `library.edit`, like adding a series; searching for it is
`acquisition.search`; grabbing is `acquisition.queue`; the Wanted list is
`library.browse`. Nothing new.

### 8. The general search is unchanged

A film grabbed from the general search is filed by its release name, as before.
That search is how something *not* in the library is acquired; the film search
is how an added film is acquired without a twin.

## Rejected alternatives

**A monitored switch per film.** Decision 2. It would be a second way of
saying "I do not want this" beside removing it, and the one worth having — "keep
it, but stop searching" — needs an automatic search, which does not exist.

**A wanted rule on the release date.** Decision 2: the date is not refreshed,
and a stale date hides films.

**Accepting a year-less release when only one film has the title.** "Only one"
is a statement about the provider's catalogue today, and false the day a remake
is announced.

**Asking by IMDb id, or by the title alone.** Decision 4.

**Inferring a film target from NULL season and episode.** Decision 5.

**Applying a quality profile by default.** There is no instance-wide default
profile to apply, and inventing one here would make the film page judge quality
differently from the episode page. Both search with no profile unless one is
named through the API, and every result shows its quality.

## Known limitations

- **No profile is applied from the film's page**, as from an episode's. A film
  still in cinemas turns up camera recordings, and they can be grabbed: the
  result says `CAM` or `TELESYNC`, and every built-in profile except *Any*
  forbids them — but only when a profile is chosen, which the screen does not
  offer yet. *Addressed by [ADR-0027](0027-a-default-quality-profile.md): every
  search is now judged by the instance's default profile unless another is
  chosen, and the film's panel offers the choice.*
- **A release named in a language the provider does not list** is refused as a
  different film. The general search remains.
- **A year-less release is never grabbable from here** (decision 3).
- **An added film is not refreshed**: its title and year are the provider's at
  the moment it was added.
- **Approving a request still creates nothing** (ADR-0017). Both kinds can now be
  added, so offering *add to the library* from an approved request is a small
  step — and the next one for that screen. *Addressed by
  [ADR-0028](0028-an-approved-request-is-satisfied-by-a-library-item.md): an
  approved request is added from the Requests screen and linked to the title,
  and fulfilled when a file of it arrives.*

## Addendum — what building it found

**A finished download could stay complete and never be imported.** Not this
increment's code, and older than every target: the import pass asked the torrent
engine for a download's files, and the engine lets a finished transfer go — at
the seeding task's next tick when seeding is off, and every finished transfer at
a restart. When that happened before the import's next pass, the pass was told
"no such transfer" and moved on, under a comment calling it "nothing alarming":
complete in the queue, absent from the library, nothing in the history. Found
checking this ADR end to end, when a film grabbed from the browser finished
sixteen seconds before a restart. The import now reads what the download left on
disk when the engine no longer holds it. The directory is the one the engine's
data directory and the validated hash name, walked through `os.Root`, regular
files only, capped at ten thousand; the importer's contained source still
stands between that list and the library. A download whose files are gone is
recorded as skipped, with the reason, and retried hourly. The stranded film then
imported, hardlinked, into its own folder.

**The first `target_kind` was NULL for every episode.** A constant named for the
episode kind was shadowed by a local variable of the same name in the function
that writes the row, so the column received the variable. The round trip still
worked — the read side takes an unmarked row with an item, a season and an
episode for the episode such a row was before this migration — so only a test
reading the column itself caught it. The constants are renamed, and the tests
for decision 5 read the column.

**The queue said a finished download was waiting for metadata.** A row the engine
no longer runs has no live progress, and the screen gave it the magnet sentence.
It now says the download finished, or stopped.

**Checked against the live API**, through the binary, from an empty database, with
a Torznab indexer on the same machine offering eight releases for any query:

| | |
|---|---|
| Added *Dune* (438631), *Star Wars* (11), *Amélie* (194), *Avengers: Secret Wars* | folders `Dune (2021)`, `Star Wars (1977)`, `Amélie (2001)`, `Avengers - Secret Wars (2027)`; all four wanted |
| The search for *Dune* (2021) | asked `t=search&q=dune 2021`; the names used include TMDB's *Dune: Part One* |
| Grabbable, with no profile | `Dune.Part.One.2021.1080p.BluRay` (fewest seeders, listed first) and `Dune.2021.HDCAM`, which the *HD-1080p* profile refuses when named |
| Refused | *Part Two* and the 1984 film as different films, *Prophecy* S01E01 as television, a year-less *Dune* as naming no year |
| *Star Wars*, *Amélie* | only `Star.Wars.Episode.IV.A.New.Hope.1977` and `Le.Fabuleux.Destin.d'Amelie.Poulain.2001` grabbable |
| Grab and import | `Dune (2021)/Dune (2021) [Bluray-1080p].mkv`, hardlinked; no second *Dune*; off the wanted list |

# ADR-0025: Adding a series before any of it is on disk

**Status:** accepted
**Date:** 2026-09-26
**Related:** [ADR-0017](0017-acquisition-requests.md),
[ADR-0019](0019-identification.md), [ADR-0021](0021-migrating-from-radarr.md),
[ADR-0022](0022-episode-tracking.md), [ADR-0023](0023-searching-for-a-wanted-episode.md)

## The problem

The operator is starting a new library rather than migrating one (stated
2026-09-26). That exposes a gap every earlier increment could step around,
because each assumed a library that already existed:

- A library item comes into being in exactly two ways: a **scan** finds files
  on disk (3c), or an **import** files a download under a folder built from its
  release name (3b).
- Episode tracking (ADR-0022) needs a series that is **identified** — it carries
  a TMDB id — and identification (ADR-0019) works on items that already exist.
- Approving a request (ADR-0017) deliberately creates nothing.

So on an empty instance the only way to follow a show is backwards: search the
indexers for one episode by hand, grab it, let the import create an item named
after the release, wait for the identification pass to propose a match, confirm
it, refresh the episode list — and only then does the wanted list, and the
episode search built on it (ADR-0023), exist for that series.

Sonarr's answer is *Add Series*: choose the show, where it lives, and which of
its episodes you want. Nothing is downloaded; the series simply exists, with
its episodes, from that moment. This ADR is that, for series.

## Decisions

### 1. The provider's id is the only thing a request names

`POST /api/v1/media` takes a kind, a TMDB id, a root folder, a monitoring
choice and — optionally — a folder name. It does **not** take a title or a
year. The item's title, year, IMDb id and default folder all come from the
provider's answer for that id.

A request that could supply the title could label anything as anything, and the
label is what every later step — the episode search's matching above all —
trusts. The one free-text field, the folder name, is a place on disk, not a
claim about what the item is.

### 2. Nothing is created on disk

Adding a series writes rows. The series' folder is a name in the database until
the first import places a file under it, at which point the vault creates it
(`Vault.Link` already creates parent directories, inside the root, through
`os.Root`).

That keeps adding a **database** operation. It has no filesystem effect, so it
needs no path-mutation authority (ADR-0016's per-effect model), and removing an
added series before anything arrives leaves no empty directory behind. A scan
never sees an added series either way: the scan works from files, and an item
with none is outside everything it compares.

### 3. The folder is named for the title, or by the operator

By default the folder is `Title (Year)` — the layout the importer already uses
and every media server reads — built from the provider's title with a small,
fixed table of substitutions and then `library.SafeComponent`:

| In the title | In the name | Why |
|---|---|---|
| `: ` (and ` : `) | ` - ` | how a subtitle is written in a filename: *Star Trek - Discovery (2017)* |
| `:` elsewhere | `-` | *Re-Zero* |
| `/` `\` | `-` | a separator inside a title (*Face/Off*) is punctuation, not a directory |
| `?` `*` | removed | *What If... (2021)*, *MASH (1972)* |
| `"` | `'` | |

Everything else `SafeComponent` already handles. Without the table,
`SafeComponent` alone turns each of those into `_`: *Star Trek_ Discovery*,
*What If..._*. All four titles are real TMDB titles, checked against the live
API. The same table names the **files** an episode search imports into the
series (ADR-0023's `PlanEpisodeIn`), so a series and its files agree.

An operator may name the folder instead. That value is **refused, not
rewritten**, if it is not already a usable single directory name: silently
changing a name somebody typed is how they end up with two folders.

### 4. One item per title, and one per folder — and nothing is adopted silently

- **The same TMDB id twice** is a `409` naming the item that already has it. The
  check runs before any request to the provider, so a duplicate costs nothing,
  and again inside the insert, as one statement, so of two concurrent adds the
  second waits for the first and is then told it lost (see the addendum for why
  that, and not "a duplicate", is what the single statement prevents).
- **A folder already occupied** by another item — typically one a scan found
  from files — is a `409` naming that item. It is not adopted: attaching this
  identity to an item somebody else created, and renaming it, is exactly the
  decision ADR-0019 reserves for a person looking at both titles. The answer
  says so: identify that item instead, or name another folder.

### 5. Adding is a person's identification, and is recorded as one

The item is written already identified, and its identification row says so:
**confirmed, by the person who added it**, with the chosen title stored as the
candidate they chose. Three things follow, and each would otherwise be a bug:

- the identification pass never re-proposes it — a person's decision is a wall
  (ADR-0019);
- the Identify screen can show it and **reopen** it like any other decision;
- its poster is one this instance may fetch, because the poster route fetches
  only for titles it has a recorded candidate for (ADR-0018's bound).

### 6. Everything is read first, then written at once — or nothing is written

Every season and every episode is read from the provider **before** anything is
written. Then one transaction writes the item, its identification, every season
and episode, and the monitoring choice. If any season cannot be read, **nothing
is added** and the answer says why.

The alternative — create the item, then refresh it — fails badly in the one case
that matters. The monitoring choice (decision 7) can only be applied to episodes
that exist; a season that failed would be read later by the scheduled refresh
and arrive with the *default* flags, putting a back catalogue the operator
declined onto the wanted list. An add that half-happened would also leave an
item the operator must notice and remove. All-or-nothing has neither problem,
and its cost is a retry.

The price is time, since TMDB is one request per season. Measured against the
live API: *The Simpsons* is 40 requests and 885 episodes, **7.2 seconds**
sequentially. Acceptable for a button that says what it is doing; seasons are
not fetched concurrently, which would be faster and ruder to a third party.

### 7. Which episodes are wanted is chosen when adding, and has no default

| Choice | Regular seasons | Episodes in them |
|---|---|---|
| `all` | all on | all on — every aired episode is wanted |
| `future` | on from the **latest season that has aired**, off before it | on only if not yet aired, or dated today or later |
| `latest` | on from the latest season that has aired, off before it | all on |
| `none` | all off | all off |

Specials start off under every choice (ADR-0022, decision 5). A series nothing
of which has aired yet has no "latest aired season", and every regular season
starts on under every choice except `none`.

Each choice is expressed with the season and episode flags that already exist,
and **each leaves ADR-0022's renewal rule doing what was chosen**: a new season
of a series with a regular season on arrives on; a new season of a series with
every regular season off arrives off. So `all`, `future` and `latest` follow a
renewal and `none` does not — without a series-level switch, which ADR-0022
declined. That is why `future` keeps the current season **on** with its aired
episodes off, rather than switching the whole season off: off would read as
"stopped following" and the next season would arrive off.

"Today or later" rather than "after now": an air date is a date, not a time, and
the provider's date is usually the broadcast day in the origin country — often
already yesterday wherever the server is. Adding a show the day an episode airs
should include that episode.

There is no default. `all` puts an entire back catalogue on the wanted list;
`future` quietly leaves it off. Neither is safe to assume on somebody's behalf,
so the request is refused without a choice. (The screen pre-selects nothing
either.)

### 8. Who may add, and what it costs

`library.edit` — Managers and Admins — the permission the monitoring switches
and the provider search on the same screen already need. Adding is bounded:
one details request plus one per season, and a duplicate costs none. The same
people can already spend a provider request per search.

Removing an added series is the existing delete, which is an administrator's
(`library.delete`); with nothing on disk it moves nothing to trash and forgets
the rows.

### 9. Films: designed here, built next

A film would be added the same way, minus episodes and monitoring. It is not
built in this increment, deliberately.

An added film can only be downloaded **into** if the grab carries it to the
import, as ADR-0023 does for episodes. Today a film is acquired through the
general search, whose import works out the film from the release name and
builds a folder from that — the exact mechanism that created a second
*Severance* before ADR-0023. An added film would acquire a twin the first time
its release named it differently from TMDB (`Star.Wars.Episode.IV.A.New.Hope.1977`
is not *Star Wars (1977)*). So adding a film waits for a film search that seals
its target into the grab, and the two ship together. Until then `kind: movie`
is refused with that reason, and films are acquired from the general search as
before.

*Built in the next increment, as designed here: see
[ADR-0026](0026-adding-a-film-and-searching-for-it.md).*

## Also in this increment

**A provider 404 means "no such title".** ADR-0022 recorded that a TMDB 404 was
reported as *the provider's response was not the expected shape*. Adding needs
the difference — a mistyped id is the operator's to fix, a broken provider is
not — so `metadata.ErrNotFound` now carries it. The body is the real one,
checked against the live API: `{"success":false,"status_code":34,
"status_message":"The resource you requested could not be found."}`. The
single-series refresh uses it too: a provider failure there is now a 502 that
names the provider, and a series the provider no longer has is a 409 saying to
identify it again — both were a 500 "internal error".

## Rejected alternatives

**Create the folder when the series is added.** Makes adding a filesystem effect
that needs root-folder authority, and leaves empty directories behind whenever
an added series is removed before anything arrives.

**Take the title and year from the request.** Decision 1.

**Add, then refresh.** Decision 6: a partial refresh cannot honour the
monitoring choice, and a failure leaves half an add behind.

**A series-level "monitor new seasons" switch.** The four choices do not need
one (decision 7), and ADR-0022 declined it for reasons that still hold.

**A default monitoring choice.** Decision 7.

**Adopt the unidentified item already in the folder.** Decision 4.

**Fetch seasons concurrently.** Seven seconds for the longest common case does
not justify parallel requests to somebody else's API.

## Known limitations

- **`none` on a series whose provider lists no regular season yet** — a show
  announced with nothing in it — does not survive the first season's arrival:
  "stopped following" is expressed by every regular season being off, and there
  is no season yet to be off. The first season arrives monitored.
- **A provider listing that contradicts itself** — a season in the series that
  answers 404 on its own — cannot be added, because the add is all or nothing.
  The error names the season.
- **The search on the Add screen shows no posters.** The poster route fetches
  only for titles this instance has recorded (ADR-0018), and a search result is
  not recorded until it is added. The poster appears once it is.
- **Films** — decision 9; since built (ADR-0026).
- **Approving a request still creates nothing** (ADR-0017). Offering *add to the
  library* from an approved request is the natural next step for that screen.

## Addendum — what building it found

**The race the insert is shaped around does not do what one would guess.**
Decision 4 says the duplicate check and the insert are one statement "so two
concurrent adds cannot both succeed". Measured by hand, a check-first,
insert-second version cannot make both succeed either, under SQLite's WAL: the
second add's read opens a snapshot, the first commits, and the second's insert
is refused with `SQLITE_BUSY_SNAPSHOT` — "database is locked" — which would
reach the operator as an internal error for what is only "already added". So
the single statement's real job is that **the loser is told it lost**. The first
test written for this, eight goroutines adding one title at once, passed thirty
runs out of thirty against the check-first version as well: the scheduler never
opened the window. The test that holds it, `importer.TestAnAddThatLosesARaceIsToldSo`,
interleaves two adds by hand.

**The specials rule was written twice, and one copy was dead.** Decision 7's
SQL excluded season 0 explicitly *and* because the first followed season is
never below 1. A mutation removing the explicit half changed nothing, so it is
gone, and the rule is the one that does the work.

**Checked against the live API**, through the binary, on an empty database:

| Added | Choice | What it did |
|---|---|---|
| *Severance* | future | 4 seasons, 20 episodes, nothing wanted. Specials and season 1 off; season 2 **on**, its ten aired episodes off; the announced season 3 on — so its episodes will arrive wanted |
| *The Simpsons* | latest | 39 seasons, 885 episodes, **4.9 s**. Season 37 and season 38, which begins the day after this was written, followed; 15 wanted |
| *Star Trek: Discovery* | none | folder `Star Trek - Discovery (2017)`; 136 episodes, nothing wanted |
| *What If...?* | all | folder `What If... (2021)`; 26 wanted |

A second add of *Severance* named the first; a film was refused with decision
9's reason; an id TMDB does not have was a 422 carrying the provider's own 404;
the TV root folder held nothing afterwards; and each add was identified as
*chosen by a person*, with its poster served.

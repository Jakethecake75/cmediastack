# ADR-0030: Automatic acquisition — the wanted list, fetched without a person, within limits

**Status:** accepted
**Date:** 2026-09-27
**Related:** [ADR-0017](0017-acquisition-requests.md),
[ADR-0022](0022-episode-tracking.md), [ADR-0023](0023-searching-for-a-wanted-episode.md),
[ADR-0026](0026-adding-a-film-and-searching-for-it.md),
[ADR-0027](0027-a-default-quality-profile.md), [ADR-0014](0014-download-engine.md)

## The problem

Everything this software acquires today, a person chose: they opened an episode
or a film, searched, read the candidates and pressed Grab. ADR-0023 decided that
on purpose ("automatic grabbing of monitored episodes — deferred, not refused"),
and three records since have noted what an automatic search would need when it
came: alternative titles it can reach without a person (0023), a way to keep a
film without searching for it (0026), and a quality profile to judge by when
nobody is watching (0027).

The operator has now asked for it. It is what Sonarr and Radarr are for: add a
series, and its aired episodes arrive; follow it, and each new one arrives the
day it is released.

A machine that fetches things on its own is a different kind of risk from a
person pressing a button, and the decisions below are mostly about that
difference:

- **It can be wrong with nobody looking.** The wrong episode, the wrong film of
  the same name, a camera recording.
- **It can be relentless.** Indexers ban accounts for request volume; a loop
  that re-grabs a broken release, or grabs everything at once, fills a disk.
- **It acts on the operator's behalf.** The operator is answerable for what the
  instance holds and seeds (§13). Every automatic grab has to be as attributable
  as a person's.

## Decisions

### 1. Off until the operator turns it on

`acquisition.automatic` defaults to `false`, as `download.enabled` does: an
instance whose egress, indexers and profile have not been set up should not
inherit a machine that fetches on its own. The configuration lint refuses it on
with the download engine off — nothing could be fetched, and saying so beats a
task that fails every fifteen minutes.

It is configuration, not a switch on a screen. It changes what the instance
does with nobody watching; the file is where such decisions are reviewed, and
changing it takes a restart and leaves a diff.

### 2. The wanted list, and nothing else

Automatic acquisition acts on exactly what the Wanted screen lists:

- **episodes**: monitored, aired, and not on disk (ADR-0022);
- **films**: in the library, **monitored**, and not on disk.

Films gain a monitored flag, which ADR-0026 rejected for want of anything it
would change — "keep it, but stop searching" needs an automatic search, which
did not exist. Now it does. A film is added monitored; unmonitoring it keeps it
in the library and off the Wanted list.

What it does **not** do:

- **grab anything not in the library.** A release in an indexer's feed is never
  the reason a title is added; adding stays a person's act (ADR-0025, ADR-0026).
- **upgrade.** An item with a file is not wanted, however far below the
  profile's cutoff. Replacing a file a person has is a person's decision.
- **season packs.** The importer takes single episodes (ADR-0023); a pack would
  download a season and import nothing.

### 3. The same matching as a person's search, and a stricter choice

Every candidate goes through `search.MatchEpisode` or `search.MatchFilm` — the
same code the targeted searches run, not a copy — so what automatic acquisition
can grab is never wider than what a person's search would offer a ticket for.

It must then be **accepted by the instance's default quality profile.** A search
nobody is watching must know what to accept (ADR-0027); with no default profile,
automatic acquisition does nothing and says why, rather than grabbing unjudged.
The best candidate is the one the profile ranks highest, as on screen.

And six things a person may choose and a machine may not:

- **At least one seeder.** A person can grab a dead torrent on purpose. A
  machine grabbing one leaves the item in flight forever. (It also refuses a
  Usenet result, which reports none and which this engine cannot download.)
- **Never a release already in the queue**, whatever became of it: finished,
  failed to import, or removed by a person. The queue is the blocklist. Removing
  a download is how an operator says "not that one", and the next pass looks
  for another. When an indexer's feed does not carry a release's info hash, the
  hash is worked out from what was fetched (`download.HashOf`) and checked
  before anything is queued.
- **Never while a download for the same item is in flight**: queued,
  downloading, or finished and not yet imported. One download per wanted item.
  One exception: a finished download the import **skipped** — not the episode
  it was grabbed for, a disc image, nothing playable — frees its item, because
  the release was the problem; the next pass looks for another. One the import
  **failed** — a missing root folder, a full disk — keeps it: that is the
  instance's problem, the importer retries it, and another release would fail
  the same way.
- **Never a release whose name fits more than one title in the library.**
  "The.Office.S01E02" is two series; with both in the library, and no year in
  the name to tell them apart, it is grabbed for neither — even when only one
  of them is wanted. A person looking at the search results can tell; a
  machine cannot.
- **A double episode only when every episode it holds is wanted** and none is
  already downloading. It is grabbed once, for its first episode, and the
  import files it across both; a download counts as in flight for every
  episode its name spans.
- **Never a release named in a language** — FRENCH, GERMAN, KOREAN, DUBBED and
  the rest; MULTi and dual-audio releases, which carry the original too, are
  not refused. By scene convention a release in English names no language, so
  one that does is a dub, or the original of a title not in English, and the
  matching cannot tell which: a title the provider also lists in German
  matches `Die.Simpsons.S30E01.German`. This is the "no alternative titles that
  are only translations" that DROPPED-FEATURES said automatic grabbing would
  need, done on the release rather than on the title list, where a language
  cannot be read. The cost is stated under *Known limitations*.

Whatever was decided at the start of a pass, the item is asked about again
just before a grab: a person who unmonitored it while its search was running
has the last word.

### 4. Two ways of finding

**The indexers' recent releases.** Every `acquisition.rss_interval` (15
minutes; the lint refuses under 10), each enabled indexer is asked once for
what it has most recently — a search with no term, in its own categories, 100
results — and every release is matched against the whole wanted list at once,
through an index of folded titles. One request per indexer per interval,
however large the library. This is how a new episode arrives the day it is
released.

**Searching.** Every `acquisition.search_interval` (15 minutes), up to
`acquisition.searches_per_run` (3) wanted items are searched for, one targeted
search each — which is one request per enabled indexer. Items never searched go
first — a series just added, a film just added — newest first; then whichever
has waited longest. An item whose search found nothing grabbable is not
searched again for 6 hours, then 12, 24 and 48, up to a week; the recent-release
pass keeps watching for it in between. A search that failed — every indexer down
— is retried in an hour and does not lengthen the wait.

The budget is the point. With the defaults, a library searches at most twelve
items an hour, however many it wants; a new series of fifty aired episodes
takes a few hours to search through, and the indexers see a steady trickle
rather than a burst.

Three more limits keep the machine from being relentless:

- **A release whose fetch failed is left alone for six hours.** The feed shows
  the same release every fifteen minutes until it scrolls away, and asking for
  it each time is how an indexer account is lost.
- **Alternative titles are read at most a hundred a pass**, cached for a day,
  and a failure to read them is not retried for an hour. A title whose names
  have not been read yet is matched by its own name meanwhile, and the task's
  summary says how many were.
- **Neither pass runs at start.** The first passes come one interval after the
  process starts: a process restarting in a loop must not become a loop of
  indexer requests. The two passes never run at once — both could otherwise
  find the same episode and grab two releases of it.

### 5. A ceiling on what a machine fetches

At most `acquisition.max_grabs_per_run` (5) grabs per pass. A bug, a flood of
mislabelled releases, or a hostile indexer could otherwise queue the whole
wanted list in one pass and fill the disk before anybody looked. What is not
grabbed waits for the next pass.

### 6. Not while the tunnel is down

When egress is enforced and the tunnel is not verified, both passes stop before
asking any indexer, and say so — rather than making requests the guard would
refuse and recording each as an indexer failure.

### 7. Its own authority, and on the record

The passes run as a new background principal, `system:acquire`, holding browse,
search and queue management — no library editing, no deletion, no path
changes, no settings (the per-task grants of ADR-0016). Every automatic grab is
audited as `acquisition.grabbed`, by `system:acquire`, saying how it was found
and what it was for. The queue says *automatic*. Every search's outcome is kept
per item — when it was last searched, what it found or why nothing was
grabbable, when it is next due — and the Wanted screen shows it.

### 8. What a person still controls

- Unmonitor an episode, a season or a film, and it is not searched for. The
  Wanted screen has the switch beside every row, because with automatic
  acquisition on the Wanted list is what gets downloaded; a film's page has it
  too (`PUT /api/v1/media/{id}/monitored`, library editing).
- Remove a download from the queue: that release is never grabbed again for
  anything, and the item is searched again. The answer to the removal says so,
  because the obvious reading of "remove" is "I do not want this", and the item
  is still wanted until it is unmonitored.
- Search and grab by hand at any time; a person's grab counts as in flight.
- Turn `acquisition.automatic` off.

The Wanted screen says, beside every item, what automatic acquisition last did
about it — downloading, grabbed, searched and found nothing (and why: "3 of 40
results were Severance S02E03, and none could be grabbed: 2 refused by the
default profile…"), a search that failed — and when it will search again. "Why
has this not arrived?" is answered where the question is asked.

### 9. Requests

ADR-0017's rule stands: approving a request downloads nothing. What changes is
the step after it. An approver who adds the requested title to the library
(ADR-0028) makes it wanted, and with automatic acquisition on, wanted is
fetched. That is what adding it means, and the approver sees the choice of
seasons to monitor before anything is added.

## Rejected alternatives

**Searching every wanted item on every pass.** It is what "automatic" sounds
like and what gets indexer accounts banned: a library wanting two thousand
episodes would make two thousand requests per indexer per pass.

**Recent releases only**, as Sonarr does by default. It never fetches anything
already released when it was added — which, for a library starting from
nothing, is everything.

**A switch on the admin screen.** Decision 1.

**Upgrading automatically to the profile's cutoff.** Decision 2. It can be
added on top: the import already supersedes into the trash — and was, off until
chosen: [ADR-0036](0036-upgrades-to-the-cutoff.md).

**A profile per series and per film.** ADR-0027's deferral stands; the default
judges. The per-item choice can be added without changing this — and was:
[ADR-0035](0035-a-quality-profile-per-title.md).

**Adding a title because its release appeared.** Decision 2. It would make an
indexer's feed decide what the library holds.

**A table of alternative titles**, which ADR-0023 expected to arrive with an
automatic search. A day's cache in memory, read within the per-pass budget, does
the same job without a migration; after a restart the names are read again, a
hundred a pass.

**Trusting the release name to say which of two same-named series it is.** It
often does not ("The.Office.S01E02"). Decision 3 refuses what fits two titles in
the library rather than choosing one.

**A per-series "fetch automatically" switch beside monitoring**, which
DROPPED-FEATURES listed as a prerequisite. Monitoring is that switch, at a finer
grain: per season, per episode, per film, chosen with no default when a series
is added (ADR-0025). A second flag — "wanted, but not fetched" — would make the
Wanted list hold two kinds of thing, and "why has this not arrived?" another
answer. The configuration's switch is the operator's gate for the whole
instance; what passes it is exactly the Wanted list.

**A stricter match: no year tolerance**, also listed there. The provider's
year and a release's differ by one routinely — *Battlestar Galactica* is 2004
to the provider and `Battlestar.Galactica.2003` to the scene; a film's year
differs by country — so dropping the tolerance would refuse the right release
and never fetch it. The risk it carries, a namesake a year apart, is the
namesake rule's.

## Known limitations

- **Season packs** were never grabbed, and older seasons are often released
  only as packs. Since [ADR-0033](0033-season-packs.md) a pack is grabbed for a
  settled season every episode of which is wanted, and a person can grab one
  from a season's search; multi-season packs still cannot be.
- **Numbering the scene and TMDB disagree on** — anime, some reality shows — is
  refused as a different episode; **daily shows** cannot be matched at all
  (ADR-0023).
- **A name missing from TMDB's alternative titles** is refused as a different
  series or film. Alternative titles are cached for a day.
- **A download that never finishes blocked its item** until a person removed it
  from the queue. Since [ADR-0034](0034-stalled-downloads.md) one that makes no
  progress for a day is given up, and its item looked for again.
- **A recent-release page is finite.** A busy indexer can publish more than a
  hundred releases in fifteen minutes; one that scrolls past between passes is
  still found by searching, later.
- **An unreleased film is searched for on the back-off schedule** until it is
  released; camera recordings are refused by the default profile, and a film
  whose default profile allows them will fetch one.
- **A person's grab from the general search carries no target**, so it does not
  count as in flight, and automatic acquisition may fetch the same episode
  again. The import files the second as an upgrade or a duplicate.
- **A namesake not in the library cannot be seen.** With only the American
  "The Office" in the library, a British "The.Office.S01E02" matches it by name,
  as it would in a person's search. A year in the release name, or a scene name
  the provider lists ("The Office US"), is what tells them apart; a release
  with neither is taken for the one in the library.
- **An import that failed blocks its item** until the cause is fixed (the
  importer retries it every minute) or the download is removed.
- **A title not in English is not fetched automatically** when its releases are
  named in its language (`Parasite.2019.KOREAN…`), which by scene convention
  they are. A person grabs it from the item's Search. Telling a dub from an
  original needs the title's original language, which is not stored.

# ADR-0023: Searching for a wanted episode

**Status:** accepted
**Date:** 2026-09-26
**Related:** [ADR-0014](0014-download-engine.md),
[ADR-0016](0016-import-pipeline.md), [ADR-0017](0017-acquisition-requests.md),
[ADR-0022](0022-episode-tracking.md)
**Built on by:** [ADR-0030](0030-automatic-acquisition.md), which adds the automatic
grabbing this record deferred (decision 1) — through the same matching, never
wider than what a person's search would offer a ticket for.

## The problem

ADR-0022 produced a list of what a series is missing. Nothing acts on it. An
operator who wants `Severance S02E03` has to type it into the general search,
pick a release by eye, and then hope the importer files it under the series they
meant.

That last part is not a figure of speech. The importer decides which series a
finished download belongs to by **re-deriving it from the release name**: it
parses the name, builds a folder name from the title and year, and looks for an
item in that folder on whichever root has the most free space. So:

- `Severance.2022.S02E03…` builds the folder `Severance (2022)`. The series the
  operator is following lives in `Severance`, found there by a scan. The import
  creates a **second Severance**, files the episode under it, and the episode
  the operator grabbed stays on the wanted list of the first.
- With two TV roots, an episode can land on the other disk for the same reason,
  and the series is now split in two.

A search that starts from a wanted episode already knows which series and which
episode it is for. The design below keeps hold of that knowledge all the way
to the import, instead of discarding it at the grab and guessing it back from a
stranger's filename afterwards.

## Decisions

### 1. A person starts each search, one episode at a time, and a person chooses

`POST /api/v1/episodes/{id}/search` searches the indexers for one episode and
returns the candidates, judged. It does not grab. There is no "search
everything wanted" and no automatic grabbing.

This is ADR-0017's rule — the machine narrows, a person chooses — applied to
episodes, for the same three reasons:

- **§13 makes the operator answerable for what the instance acquires.**
  Automatic fulfilment means the machine matching a title to a release, which
  is the step with the longest list of known failures in this category of
  software (decision 3 lists the ones this design knows about).
- **Indexers ban for volume.** A wanted list of two hundred episodes is two
  hundred searches across every indexer, and "search all" is how an operator
  loses an account on a private tracker in an afternoon.
- **It is reversible to add and not to remove.** Automatic grabbing can be
  built on this later, behind its own decision. Once an instance has been
  grabbing on its own, the downloads it made are already on disk.

Automatic acquisition for monitored series is Sonarr's defining feature, so this
is a deliberate gap, recorded in DROPPED-FEATURES as *not yet* rather than
refused. What it would need is written down there.

### 2. The server decides what matches, and the grab ticket carries the decision

A grab already takes a sealed ticket rather than a URL (ADR-0014), so the client
cannot change *what* is downloaded. An episode search extends the same idea to
*what it is for*: a candidate the server has matched to the episode gets a
ticket that also seals the **target** — the series item, the season and the
episode. The grab copies the target onto the queue row, and the import reads it
from there.

The alternative — a grab request carrying an `episode_id` beside the ticket, the
way a request id is carried today — would let any ticket from any search be
attached to any episode. The server's matching would become advice. Sealing
costs one field.

Only matching candidates get a ticket at all, for the same reason only
profile-accepted candidates do today: a UI offered a ticket for a release the
server refused will eventually grow a button that grabs it.

### 3. What "matches" means

A candidate matches the episode when all of these hold, and is returned with the
first one that fails otherwise — the rejection is the answer to "why is there
nothing to grab", and belongs in the response rather than a debug log:

| Check | Refused as |
|---|---|
| The release names a series | *unparsed* |
| That name is the series' title **or one of its alternative titles**, compared after folding case, punctuation, `&`/`and`, apostrophes and accents | *a different series* — `Severance Pay` is not `Severance` |
| If both carry a year, they are within a year of each other | *a different series* — `Doctor Who (1963)` is not `Doctor Who (2005)` |
| It is numbered by season and episode | *daily* or *absolute numbering*: not supported |
| The season is the episode's season | *a different episode* |
| It is not a whole-season pack | *season pack*: listed, never grabbable |
| Its episode, or episode range, covers the wanted one | *a different episode* — a file holding `S02E03-E04` **does** cover `S02E03` |

Three of those deserve a sentence each.

**Alternative titles come from the provider, at search time.** Release names
follow scene convention, not TMDB's: *The Office* is released as
`The.Office.US`, *Law & Order: Special Victims Unit* as `Law.and.Order.SVU`,
*Marvel's Daredevil* as `Marvels.Daredevil`. Checked against the live API before
this was written: TMDB's alternative titles carry `The Office (US)`,
`Law and Order SVU` and `Daredevil` respectively. They are fetched when a search
runs — one request per interactive search — rather than stored, because nothing
yet needs them outside a search. A provider that cannot be reached leaves the
primary title only, and the response says so.

**The year tolerance is one year, not zero.** *Battlestar Galactica* first aired
in 2004 according to TMDB and is released as `Battlestar.Galactica.2003`, after
the miniseries. Exact years would refuse the right show; no year check at all
would accept the wrong *Doctor Who*.

**A season pack is shown and never grabbable.** The importer takes single
episodes (a pack is refused, not split — see DROPPED-FEATURES), so grabbing one
would download a whole season to import nothing. It is listed with that reason
because "the only release anybody has is a pack" is itself worth knowing.

### 4. The import attaches to the target, and checks it

When a completed download carries a target, the importer:

- files it under the **target item** — its root folder and its folder — instead
  of building a folder from the release name and choosing a root by free space;
- names the file from the target's title rather than the release's;
- and **refuses** it, with the reason recorded, when the file's own numbering
  does not cover the target episode. A release sealed as `S02E03` whose files
  turn out to be `S02E04` is not attached to anything.

A target that no longer exists — the operator deleted the series while the
episode downloaded — is refused too, rather than re-created from the release
name. The operator removed that series; importing into a new copy of it would
undo their decision behind their back.

A download without a target imports exactly as it did before this ADR.

### 5. The search asks by title, season and episode

The query is the series' title with the season and episode, sent as a
`tvsearch`. A request may substitute another search term — one of the
alternative titles, typically — and the matching in decision 3 is applied
whatever the term was, so a term cannot widen what is grabbable.

Searching by TVDB or IMDb id is more precise on indexers that support it, and is
not done yet. It needs the indexer's capabilities read first, and I have not
verified how Jackett and Prowlarr combine an id with a free-text term on an
indexer that supports one but not the other. A search that silently returns
everything would be worse than one that matches by title.

## Rejected alternatives

**Automatic grabbing of monitored episodes.** Decision 1. Deferred, not refused.

**An `episode_id` on the grab request.** Decision 2: it makes the server's
matching advisory.

**Matching on title alone and letting the person check the numbers.** The
person does check — but a list of forty releases of *Severance* where the one
wanted is the fourth from the bottom is how the wrong episode gets grabbed.

**Storing alternative titles on identification.** No consumer needs them
outside a search yet. When automatic search exists it will, and the table
arrives with it.

## Known limitations

- **Numbering that differs between the scene and TMDB** is not reconciled. For
  most shows they agree; for some — anime above all, and a few reality shows —
  they do not, and a correct release is refused as *a different episode*.
- **Daily shows and absolute-numbered anime** cannot be matched or imported.
- **A name missing from TMDB's alternative titles** is refused as a different
  series. The general search still reaches it, without the target.
- **Indexers on the operator's own network were refused** when this was
  written: the indexer egress profile denied private addresses, which also
  denied a Prowlarr or Jackett at `192.168.x.x` or on a Docker network.
  Resolved by [ADR-0024](0024-indexers-on-the-operators-network.md).

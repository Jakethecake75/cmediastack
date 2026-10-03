# ADR-0034: Stalled downloads — a machine gives up on its own, a person is told

**Status:** accepted
**Date:** 2026-09-29
**Related:** [ADR-0030](0030-automatic-acquisition.md), [ADR-0033](0033-season-packs.md),
[ADR-0014](0014-download-engine.md), [ADR-0032](0032-notifications-to-discord.md)

## The problem

A torrent nobody seeds never finishes. ADR-0030 listed the consequence among
its limitations: *"a download that never finishes blocks its item until a person
removes it. There is no stall detection yet."* One download per wanted item is
the rule that keeps automatic acquisition from fetching the same episode twice,
so a dead release holds its item for ever; since ADR-0033 a dead season pack
holds a whole season. Nothing says so anywhere — the queue shows 0% and the
Wanted screen shows *downloading*.

## Decisions

### 1. What stalled means

A download is **stalled** when it has made **no progress** for
`download.stall_after` — 24 hours by default. Progress is bytes verified: the
engine's count of completed bytes going up. A magnet link whose metadata has not
arrived has made none.

The clock runs **only while this process does.** A download is judged from
whichever is later, its last progress or the moment the engine started: an
instance that was off for a week has not watched anything fail to arrive, and a
restart must not give up on everything in the queue at once. The cost is that
an instance restarted more often than `stall_after` never calls anything
stalled; the log says when it started.

`0` turns detection off. The lint refuses anything under an hour: a swarm that
is slow to find is not a dead one.

Only transfers the queue says are **queued or downloading** are judged —
never a finished one that is seeding, never one a person stopped.

### 2. Progress is kept in the queue

The row gains `progress_bytes`, `progressed_at` and `stalled_at` (migration
0020). A task, `download.stalls`, runs every five minutes: it reads the engine,
records any progress, and judges the rest. Kept on the row rather than in
memory so the answer to *"when did this last move?"* survives a restart and can
be shown.

### 3. A machine's download is given up; a person's is reported

Who grabbed it decides what happens, because it decides whose decision would be
undone.

- **Automatic acquisition's** (`system:acquire`): **given up.** The transfer is
  stopped, the row marked stopped with `stalled_at` set, and nothing on disk is
  touched. The queue is the blocklist (ADR-0030, decision 3), so that release is
  never grabbed again; the item stops being in flight, so the next pass looks
  for another release. A machine undoing its own choice is not overriding
  anyone.
- **A person's**: **reported and left running.** `stalled_at` is set, the queue
  says *no progress since …*, and it keeps holding its item. A person may be
  waiting on a rare release on purpose; removing it is their call, one button
  away, and removing it releases the item as it always did. If it moves again,
  the mark is cleared.

### 4. On the record, and told

Each stall is written once — when it is first found — to the audit log as
`acquisition.stalled`, by `system:download`: the release, what it was for, how
long it had not moved, and whether it was given up. It reaches Discord under
**Library**, off by default, because the message names a release (ADR-0032's
rule for titles). The Queue screen says it whatever the notifications are.

## Rejected alternatives

**Giving up a person's download too.** It would make the software override a
choice somebody made on purpose, silently, on a timer.

**Judging by peers or seeders instead of bytes.** A swarm's numbers come and go
by the minute and a tracker can lie; bytes that verified are the only fact.

**A per-indexer or per-size threshold.** One number an operator can reason about;
a 60 GB pack that moved a single piece in a day has not stalled.

**Deleting the partial data.** Unlinking is destroy authority this path does not
hold (ADR-0016); the bytes stay until the download is removed and cleaned up by
hand, as for any stopped transfer.

## Known limitations

- A download whose peers trickle — a piece a day — is never stalled, however
  long it takes.
- An instance restarted more often than `stall_after` never finds a stall.
- The partial data of a download given up stays in the download directory.
- The Wanted screen shows a given-up item's last search until the next pass
  searches it again, which it does at once.

## Verification

| Claim | Test |
|---|---|
| Stalled is no verified progress for the threshold, counted from the later of the last progress and the engine's start; off at 0 | `download.TestAStallIsNoProgressForTheThreshold` |
| Progress is recorded, and clears a person's stalled mark | `download.TestProgressIsKeptAndClearsTheMark` |
| A machine's stalled download is stopped and marked; a person's is only marked; each once | `download.TestAMachinesStallIsGivenUpAPersonsIsReported` |
| A given-up download stops holding its item and its release is not grabbed again | `acquire.TestAGivenUpDownloadFreesItsItem` |
| Only automatic acquisition's downloads are given up — a person called `system:acquire` is a person | `acquire.TestOnlyAutomaticAcquisitionsDownloadsAreAutomatic` |
| Each stall is on the record once, by `system:download`, saying what was done | `main.TestAStallIsWrittenToTheAuditLog` |
| It reaches Discord only as Library news, off unless chosen | `notify.TestAStalledDownloadIsLibraryNews` |
| The threshold is linted | `config.TestTheStallThresholdIsChecked` |
| The queue says so | `api.TestTheQueueSaysADownloadStalled` |

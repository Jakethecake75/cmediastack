# ADR-0056: Fetching wanted subtitles without a person

**Status:** accepted
**Date:** 2026-10-01
**Related:** [ADR-0030](0030-automatic-acquisition.md), [ADR-0055](0055-fetching-a-subtitle.md)

## The problem

A person can fetch a file's subtitle (ADR-0055). Bazarr's other half is the
sweep: every film and episode in the library is given a subtitle in each
wanted language, without anyone asking for each file.

## Decisions

### 1. A scheduled task with browse authority alone

`subtitles.fetch` runs every six hours as `system:subtitles`. When
OpenSubtitles has no key, or no language is wanted, the pass asks nothing and
says so.

The task holds **browse and nothing else**. No background task in this
software holds `library.edit`: a task that could rewrite titles or monitoring
is what ADR-0019 and ADR-0030 refuse. A sidecar beside a video changes no
title, no monitoring and no existing file, which is the same reasoning that
lets identification record proposals with browse alone. The sweep is a method
no route reaches. The person's fetch keeps its own `library.edit` check.

### 2. What is wanted

A film's or an episode's file is wanted in a language when **none** of these
exists:

- a sidecar beside it in that language, in any subtitle format;
- an embedded **text** subtitle in that language, as its probe recorded it.

The probe's three-letter codes (`eng`, `fre`/`fra`, …) are matched to the
two-letter ones. Music and books have no subtitles.

### 3. Budgeted, and backing off

- At most **ten searches a pass**, never-searched first and newest file first,
  then the longest waiting.
- A search that finds nothing waits **a day**, doubling to **thirty days**.
  Subtitles appear long after a release, so a file is looked for again, but
  rarely.
- A search that fails because OpenSubtitles could not be asked waits an hour.
- **The day's quota ends the pass.** The file it was asking for waits a day.
  The rest stay due, so a later pass asks once, is told the same, and stops
  again. That is one request every six hours while the quota is spent, rather
  than a second store of when it resets.
- A refused key ends the pass too, and waits an hour for the key to be fixed.

What each search came to is kept in `subtitle_search`, one row per file and
language, forgotten with the file. Each fetch is audited, as a person's is, by
`system:subtitles`.

### 4. The same fetch

The sweep fetches through the same code a person's request does: the same
matching, the same refusal of anything that is not an SRT, the same
never-over-a-sidecar rule. Only the authority it runs with differs.

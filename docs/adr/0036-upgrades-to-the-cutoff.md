# ADR-0036: Upgrades to the cutoff — off until chosen, after what is wanted, never worse

**Status:** accepted
**Date:** 2026-09-29
**Related:** [ADR-0030](0030-automatic-acquisition.md), [ADR-0035](0035-a-quality-profile-per-title.md),
[ADR-0027](0027-a-default-quality-profile.md), [ADR-0016](0016-import-pipeline.md)

## The problem

Automatic acquisition fetches what is missing and stops (ADR-0030, decision 2:
*"an item with a file is not wanted, however far below the profile's cutoff"*).
A library started from nothing is full of first copies: the 720p that was all
there was the week an episode aired, the WEB-DL before the Blu-ray came out.
Every profile names a **cutoff** — the quality at which to stop looking — and
nothing ever looks. ADR-0030 left it for later: *"It can be added on top: the
import already supersedes into the trash."*

Replacing a file somebody has is a different act from fetching one they do not,
and the decisions are about that difference.

## Decisions

### 1. Off until the operator turns it on

`acquisition.upgrades`, `false` by default, and refused by the lint unless
`acquisition.automatic` is on. Configuration, not a switch on a screen, for
ADR-0030's reason: it changes what the instance does with nobody watching, and
now what it does to files already in the library.

### 2. What is upgraded

An episode or a film that **has a file** and is **monitored**, whose file is
**below its title's profile's cutoff** (ADR-0035: the title's own profile, or
the default). A file whose quality the profile does not allow at all — the
title's profile was changed from *HD-720p* to *Ultra-HD* — is below it.

**Not** a file that covers two episodes. A release of one of them would
supersede the file both are in and leave the other episode missing; a double
episode is upgraded by hand. Nor, the other way round, is a release of two
episodes taken as an upgrade of one whose neighbour has a file of its own — the
rule every grab already follows (ADR-0030: *every episode it holds is wanted*);
when the neighbour is missing, the release fills it as well.

### 3. Only better, by the profile's own rule

A release is an upgrade only when `release.Profile.ShouldUpgrade` says so
against the file held: acceptable to the profile, then a higher quality, or the
same quality as a PROPER or REPACK, or the same quality with a better preferred
score — never *equal*, which is how a library flaps between two releases for
ever. Everything else automatic acquisition requires of a grab still applies
(ADR-0030, decision 3): the same matching, seeders, never a release that was in
the queue, one download per item, no namesake, no named language.

### 4. After what is wanted, and seldom

The search pass spends its budget on the Wanted list first; what remains goes
to upgrades. Each file is searched for an upgrade **at most once a week** — from
its last search, which for a file that has just arrived is the search that
fetched it — so a release a day behind the one it replaced is not looked for the
next morning. The recent-release pass matches upgrades too: a better release
appearing in a feed costs no extra request.

The per-pass limits hold, and an upgrade counts as a search and as a grab.

### 5. The import replaces, into the trash

The download is grabbed for the episode or film, as any other grab. The import
already replaces a worse file (ADR-0016): the old one is superseded **into the
trash**, restorable for the trash's retention. The audit line says it was an
upgrade and from what.

The import judges *better* by the default quality ladder rather than the
title's profile (a limitation ADR-0035 carries). For the built-in profiles the
two orders agree, and a test says so; a custom profile that ranks qualities
differently from the ladder could fetch a release the import then declines to
replace with, which costs one download and is recorded.

## Rejected alternatives

**On whenever automatic acquisition is.** An operator who turned on fetching
what is missing did not thereby agree to replacing what is there.

**Upgrading past the cutoff.** The cutoff is the operator's *enough*.

**Searching upgrades as often as wanted items.** A missing episode is a gap; a
720p one is a preference. Weekly is enough for the Blu-ray to appear.

**Deleting the replaced file.** The import has no destroy authority (ADR-0016),
and an unwelcome upgrade must be one restore away.

## Known limitations

- A file covering two episodes is never upgraded automatically.
- The import's *better* is the default ladder's; a custom profile that disagrees
  with it can fetch a release that is then not imported.
- An upgrade moves only the old video to the trash (`library.Vault.Supersede`
  takes one path); subtitles placed beside it stay, under the old file's name,
  beside the new one.

## Verification

| Claim | Test |
|---|---|
| Off unless chosen, and only with automatic acquisition | `acquire.TestUpgradesAreOffUnlessChosen`, `config.TestUpgradesNeedAutomaticAcquisition` |
| A file below its title's cutoff is upgraded, by a release the profile says is better; one at its cutoff is left alone | `acquire.TestAFileBelowItsCutoffIsUpgraded`, `acquire.TestAFileAtItsCutoffIsLeftAlone`, `acquire.TestAnUpgradeMustBeBetter` |
| What is wanted comes first; an upgrade is looked for weekly | `acquire.TestWhatIsWantedComesBeforeUpgrades`, `acquire.TestAnUpgradeIsLookedForWeekly` |
| The recent releases carry upgrades too | `acquire.TestAnUpgradeFromTheRecentReleases` |
| A double-episode file is not upgraded, nor one episode by a release of two | `acquire.TestADoubleEpisodeFileIsNotUpgraded`, `acquire.TestAReleaseOfTwoEpisodesIsNotAnUpgradeOfOne` |
| A person's change during a pass wins | `acquire.TestAPersonsChangeStopsAnUpgrade` |
| The built-in profiles rank as the import does | `release.TestTheBuiltinProfilesRankAsTheImportDoes` |

# ADR-0027: One quality profile judges every search unless another is chosen

**Status:** accepted
**Date:** 2026-09-26
**Related:** [ADR-0005](0005-transcode-policy-skylake.md),
[ADR-0023](0023-searching-for-a-wanted-episode.md),
[ADR-0026](0026-adding-a-film-and-searching-for-it.md)
**Built on by:** [ADR-0030](0030-automatic-acquisition.md): automatic acquisition judges
every release by the default profile, and with none it fetches nothing.

## The problem

A quality profile decides which releases are acceptable: which qualities, which
terms are forbidden. Four ship with the instance, and every built-in one except
*Any* forbids camera recordings and telesyncs. But a profile only judges a
search that names one, and nothing names one by default:

- the general search starts on *No profile — show everything unjudged*;
- the episode search (ADR-0023) and the film search (ADR-0026) are started from
  a button, send no profile, and offer no way to choose one.

So everything that matches is grabbable. For an episode that rarely matters. For
a film still in cinemas it means the release a person is offered, with a Grab
button, is a camera recording of it — the gap ADR-0026 recorded as its first
limitation.

## Decisions

### 1. The instance has a default profile

One of the stored profiles is the **default**, or none is. It is a flag on the
profile's row, and the database holds at most one: a partial unique index on the
flag, so two defaults cannot exist whatever the code does.

A new instance's default is *HD-1080p* — the profile the built-in set already
calls the default default, because on the i5-6500T a 2160p HDR library is one
that will not play (ADR-0005). An instance created before this ADR gets the same,
set once by the migration if its built-in *HD-1080p* still exists.

### 2. One rule for every interactive search

The general search, the episode search and the film search all read
`profile_id` the same way:

| `profile_id` | Judged by |
|---|---|
| absent | the default profile — or nothing, if there is no default |
| `0` | nothing: every release is acceptable and the person judges |
| a profile's id | that profile |

The general search changes meaning here: absent used to mean *unjudged*. It
changes because the risk is the same there — a camera recording one click
away — and because two meanings for "no profile given" is how a screen ends up
applying the wrong one. *No profile* is still one choice away, on every search,
and a release a profile refuses still comes back with the reason; it only loses
its Grab button.

Every answer says which profile judged it, and whether that was the default.

### 3. Choosing the default is an administrator's

`PUT /api/v1/admin/quality-profiles/default` with a profile's id, or `0` for none,
on `admin.system` — the permission that already governs editing a profile. It is
audited as a changed setting, with what it was and what it became.

*None* is allowed: an operator who wants every search unjudged unless they say
otherwise can have that. It is a choice somebody made on purpose and the audit
log says who.

### 4. The default profile cannot be deleted out from under the searches

Deleting the default is refused until another is chosen (or none). Deleting it
quietly would turn every search back to *unjudged* — reopening the gap this ADR
closes — without anybody deciding that.

### 5. On screen, the choice is where the search is

The Search screen and the episode and film search panels each show *Judged by*
with the default preselected and marked, and every other profile plus *No
profile — judge them yourself*. Changing it searches again. An administrator
sees *Make this the default* beside a profile that is not.

## Rejected alternatives

**A profile per series and per film**, as Sonarr and Radarr keep. It is what an
automatic search will need — a search nobody is watching must know what to
accept — and what the import's upgrade rule should one day consult instead of
the default ladder (ADR-0016). Today every search is started by a person who can
see and change the profile, and one instance-wide default closes the gap that
exists. The per-item choice can be added on top without changing this one.

**Refusing camera recordings in the matching.** Whether a release *is* the film
(ADR-0026) and whether it is *good enough* are different questions with
different owners. Folding quality into identity would make *No profile* unable
to grab a camera recording even when a person deliberately wants one.

**A default for the targeted searches only.** Decision 2.

**A setting in the configuration file.** The profiles live in the database and
are chosen at run time; the default belongs beside them, where changing it is
audited.

## Known limitations

- **No screen edits a profile.** The four built-in profiles are what there is;
  choosing between them is now possible, changing one is not.
- **The import still ranks upgrades by the default ladder**, not by the profile
  that grabbed a release (ADR-0016's known limitation, unchanged).

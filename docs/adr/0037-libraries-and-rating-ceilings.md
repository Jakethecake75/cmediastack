# ADR-0037: Libraries are root folders, and a rating ceiling hides what is above it

**Status:** accepted
**Date:** 2026-09-29
**Related:** [ADR-0015](0015-library-path-containment.md),
[ADR-0017](0017-acquisition-requests.md),
[ADR-0018](0018-metadata-and-artwork.md),
[ADR-0028](0028-an-approved-request-is-satisfied-by-a-library-item.md)

## The problem

Since Phase 1 an approver has been able to send library ids and a rating
ceiling for a new account, and both were stored and carried on every request's
principal. Nothing that lists or serves media applied either one. The `library`
table the grants referenced never held a row, the UI always sent none, and a
media item carried no rating. **Every account that may browse saw the whole
library**, while the approval form suggested otherwise. The records deferred it
because it needed a decision about what a library is. The operator decided on
2026-09-29: a library is a root folder, and ratings come from the metadata
provider.

## Decisions

### 1. A library is a root folder

An operator already separates what they keep by where they keep it: *Kids'
films* and *Films* as two roots. A grant names **root folders**, and nothing
else can be granted: `library_grant` and the empty `library` table are dropped
(migration 0022). A new `root_folder_grant` references `root_folder`, and
forgetting a root removes its grants.

### 2. All libraries unless restricted, so nothing changes by itself

An account has `all_libraries`, **true for every account that exists**. That is
what every account has actually had, and a migration that took the whole library
away from everybody would be a surprise. It is false only when somebody chooses
a list. An account restricted to a list whose roots have all been forgotten sees
nothing: it fails closed. An administrator (`admin.system`) and background work
always see everything.

### 3. A rating is the US certification, and unrated is above every ceiling

A film's rating is its US certification from TMDB (`/movie/{id}/release_dates`,
theatrical first). A series' is its US content rating (`/tv/{id}/content_ratings`),
one rating for the whole series. Each maps to a rank:

| Rank | Films | Television |
|---|---|---|
| 1 | G | TV-Y, TV-G |
| 2 | PG | TV-Y7, TV-PG |
| 3 | PG-13 | TV-14 |
| 4 | R | TV-MA |
| 5 | NC-17 | — |

An account's ceiling is 0 (none) or a rank, and it sees a title only when the
title's rank is at or below it. **A title with no rating is hidden from an
account with a ceiling**: an unidentified home video and an unrated horror film
look the same to the instance, and the account with a ceiling is the one where
guessing wrong matters.

A task, `metadata.ratings`, asks the provider hourly for up to 50 identified
titles that have never been asked, and again after 30 days for one that came
back unrated. Its grant is browse only: a rating is a fact recorded about a
title, like its provider id (ADR-0019).

A person with `library.edit` may set a title's rating by hand
(`PUT /api/v1/media/{id}/rating`), or clear it back to the provider's. The task
never overwrites a person's. It is audited as `media.rating.changed`, because a
rating decides who can see a title.

### 4. Enforced where the rows are read, and a title out of scope does not exist

Every read of the library made for a person is filtered by the principal's scope
in SQL: the title list, a title and its files, a series' seasons and episodes,
the Wanted list, every playback route, the searches for a title (and so what can
be grabbed from them), the monitoring and profile switches, the identification
review, the root folders a person may browse, a request's library item, and a
poster to anyone who may not edit the library. A title outside the scope answers
**404**, never 403: *hidden* must not be distinguishable from *absent*. The
filter is a required argument of the importer's item query, so a new read has to
choose it or say it is unscoped.

**Not filtered:** the queue and a download's import history (`acquisition.queue`),
whose release names already say what a download is; the audit log; and
notifications. They are operations surfaces, gated by their own permissions.

### 5. Who sets it, and never wider than their own

The approver sets both when approving an account or issuing an invite, as the
form always said. **A grant can be no wider than the grantor's own scope**: a
restricted approver may grant only roots they can see and a ceiling at or below
their own, and cannot grant all libraries or no ceiling.

An administrator changes an existing account with
`PUT /api/v1/admin/users/{id}/access`: `admin.users`, only on an account the
administrator outranks, and audited as `user.grants.changed` with before and
after. Principals are rebuilt on every request, so the change applies to the
account's next request, including its API tokens.

## Rejected alternatives

**A library as a separate object holding root folders.** It is one more thing to
name and manage, and it adds nothing a household instance needs: one root per
library is what people already do.

**Ratings from the release name or the file.** Neither carries one reliably.

**Showing unrated titles to capped accounts.** That would be convenient for home
videos and wrong for an unrated horror film. A person can rate a home video by
hand.

**403 for a title out of scope.** A 403 tells a restricted account that the title
exists.

**Certification systems other than the US.** Each country's system has its own
ranks. The provider's US rating is used for every instance, and a person can set
a title's rating by hand.

## Known limitations

- **Adding a title that already exists outside the adder's scope is refused as
  already in the library.** That tells them it exists, which a restricted account
  with `library.edit` could use to probe. Accepted: `library.edit` is a
  Manager's permission.
- **Queue rows name what is downloading**, whoever the title is hidden from.
- **One rating per series**: an episode is not rated on its own.
- **A newly identified title is hidden from capped accounts for up to an hour**,
  until the task rates it. The task can be run by hand. That includes a title a
  capped account with `library.edit` has just added: it adds it, and cannot see
  it until it is rated.

## Verification

| Claim | Test |
|---|---|
| A root folder is what is granted; every existing account keeps all libraries | `identity.TestAGrantNamesRootFolders` |
| The scope is read in SQL: a root not granted and a rating above the ceiling are not returned, unrated is hidden from a ceiling | `library.TestTheScopeIsAppliedInSQL` |
| Every route that names a title, episode or file answers 404 for one out of scope | `api.TestATitleOutOfScopeDoesNotExist` |
| Lists, the Wanted list and root folders show only what is in scope | `api.TestListsShowOnlyWhatIsInScope` |
| No grant is wider than the grantor's | `authz.TestAGrantIsNoWiderThanTheGrantors` |
| An administrator changes an account's access, audited, applied on the next request | `api.TestAnAccountsAccessIsChangedAndAudited` |
| Certifications map to ranks; unknown ones are unrated | `library.TestCertificationsRank` |
| The provider's rating is read for films and series | `metadata.TestCertificationIsRead` |
| The task rates what is unrated and never overwrites a person's | `importer.TestRatingsAreFetchedAndAPersonsIsKept` |
| A person sets or clears a rating, audited | `api.TestARatingIsSetByAPerson` |
| Every read of `media_item` chooses a scope | `library.TestEveryItemReadChoosesAScope` |

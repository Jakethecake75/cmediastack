# ADR-0042: Reporting a problem with a title

**Status:** accepted
**Date:** 2026-09-29
**Related:** [ADR-0017](0017-acquisition-requests.md),
[ADR-0037](0037-libraries-and-rating-ceilings.md),
[ADR-0032](0032-notifications-to-discord.md)

## The problem

`POST /api/v1/issues` has answered 501 since Phase 1. Jellyseerr's *issues* are
how a household tells whoever runs the server that the sound is out of sync, the
subtitles are missing or the film is the wrong one. Without them the report
arrives by text message and is forgotten. A person who may ask for a title
should be able to say that the one they got is wrong, where the person who can
fix it will see it.

## Decisions

### 1. An issue is a problem with one title, of one kind

`POST /api/v1/issues` with the title (`media_item_id`), what is wrong — `video`,
`audio`, `subtitles`, `wrong_title` or `other` — optionally the season and
episode, and a note of at most 500 characters. **`request.submit`**, the
permission that asks for titles, and only for a title the reporter can see
(ADR-0037): a hidden title answers as a missing one does.

**One open issue per problem.** A second report of the same kind on the same
title and episode, while the first is open, is the first: the answer says so
and adds nothing. A household of five noticing the same broken subtitle is one
problem.

### 2. Who sees them, and who closes them

`GET /api/v1/issues` lists them, newest first, open unless asked otherwise. A
person sees their own; whoever holds **`library.edit`** — the permission that
can fix a title — sees every one on a title they can see.

`POST /api/v1/issues/{id}/resolve` closes one with a sentence saying what was
done. `library.edit`. A resolved issue stays, with who resolved it and when, so
the reporter can read the answer.

### 3. Audited, and a notification if the operator chooses

`media.issue.reported` and `media.issue.resolved` are audit lines, and they go
to Discord in the *Requests* category, which is off by default because it names
titles (ADR-0032). The reporter's note is somebody else's text: it travels as
the notifications' untrusted text always does, in a code span with mentions off.

## Rejected alternatives

**A comment thread per issue.** The person who can fix it answers once, when it
is fixed. A conversation belongs somewhere people already talk.

**Reporting against a file.** A person watching knows the title and the episode,
not which file played.

**Issues for titles not in the library.** That is a request.

## Known limitations

- Nobody is told individually: there is no mail transport. The reporter sees
  the resolution on their list.
- An issue does not re-search or replace anything. Fixing is the editor's.

## Verification

| Claim | Test |
|---|---|
| A report names a visible title and a kind; a second of the same is the first; bad input is refused | `api.TestAProblemIsReportedOnce` |
| A reporter sees their own; an editor sees every one in scope and resolves it; others cannot | `api.TestIssuesAreSeenAndResolvedByTheRightPeople` |
| Both are audited | `api.TestAProblemIsReportedOnce`, `api.TestIssuesAreSeenAndResolvedByTheRightPeople` |

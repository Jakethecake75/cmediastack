# ADR-0017 — Acquisition requests: who may ask, who decides, and what a decision does

**Status:** Accepted · 2026-09-13 · Builds on [ADR-0014](0014-download-engine.md), [ADR-0016](0016-import-pipeline.md)

**Built on by:** [ADR-0030](0030-automatic-acquisition.md). Approving a request still
downloads nothing; adding the requested title to the library makes it wanted,
and with automatic acquisition on, what is wanted is fetched.

## Context

This is the Jellyseerr half of the brief: somebody who is *not* the operator says
"I would like The Matrix", and somebody who is trusted decides.

Everything interesting here follows from one sentence in §13:

> Assume I am the sole operator, responsible for the legality of whatever
> content my instance indexes and stores.

A request surface is, structurally, a way for other people to cause an
acquisition on a machine somebody else is answerable for. That is the thing to
design around.

## Decision

### 1. Approving a request downloads nothing

Overseerr and Jellyseerr approve-and-fetch: approval hands the title to the
\*arr, which searches, picks a release and grabs it. It is the obvious design and
it is the wrong default here.

Approval means *"this instance is willing to acquire this"*. It does **not**
choose a release. A person then searches, looks at what came back, and grabs one
— and that grab is linked to the request, so the person who asked finds out.

The reason is not caution for its own sake. Automatic fulfilment means the
machine matching a title to a release, and that match is exactly the step this
project has already found to be unreliable: ADR-0016 records a sample rule that
discarded *Free Samples (2012)*, a language tag that read `En.Route.2019` as
English, and a parser that reads a folder and a filename differently. A wrong
match here does not produce a wrong label — it produces an acquisition, on a
machine whose operator is answerable for it.

So the machine narrows and a human chooses. The cost is honest and stated in the
UI: an approved request with no release chosen says so, in words, on the row.

**Not done, and named as a gap:** a request for something not yet released has
nowhere to wait. The "keep looking until it appears" task is the watchlist
behaviour people will expect, and it needs its own decision about how often an
instance may query an indexer unprompted.

### 2. Who sees whose request is a SCOPE, not a permission

An ordinary user may list requests — their own are in there. An approver's
listing is everybody's. Expressed as a permission alone ("may list requests")
that difference disappears and every user reads every other user's watchlist.

So the route is gated on `request.submit`, and the *scope* is computed from the
principal inside `request.Service.List`, which passes the store a filter. The
store holds no permission checks at all and says so in its header comment: a
store that silently filtered by principal would make the scoping invisible at
the call site, which is how a listing ends up returning everything.

Two smaller consequences, both deliberate:

- **The follower list is shown only to an approver.** To anyone else it is a
  list of what the other people on this instance are watching.
- **The listing says which scope it is.** A short list has two very different
  causes — nobody has asked for anything, or you can only see your own — and a
  user who cannot tell them apart concludes the feature is broken.

`TestAUserSeesOnlyTheirOwnRequests` is the guard. Disabling the scope makes it
fail with every user reading every other user's list.

### 3. Duplicate detection is deliberately crude, and the asymmetry is why

With no metadata provider (ADR-0016) there is no id to compare, so "is this the
same film?" is answered by comparing words. Every normalisation rule makes the
comparison looser, and the two errors are not equal:

| | What happens | Cost |
|---|---|---|
| **Missed** duplicate | Two rows in a queue a human reads | A moment of annoyance |
| **False** duplicate | Somebody is silently attached to a request for a *different* film; their actual request is never made | The software quietly did not do what they asked, and nothing says so |

So `MatchKey` normalises spelling and punctuation, and refuses to normalise
meaning. `Dune` is not `Dune: Part Two`; `Dune (1984)` is not `Dune (2021)`;
`Dune` with no year is not `Dune (2021)`, because nobody can tell which film the
yearless one meant. `Rocky II` and `Rocky 2` are an accepted **miss**, recorded
as such in the test rather than left as a surprise.

The rule's own test found the one real bug in it: `WALL·E` and `WALL-E`, the two
commonest spellings of one film, did not collide, because the middle dot vanished
while the hyphen became a separator.

A title that normalises to nothing (`!!!`) is refused at the door rather than
stored, because an empty match key would collide with every other empty one —
the worst possible false merge.

### 4. A second person asking JOINS the first request

Rather than opening a rival one. Enforced by a **partial unique index** on
`match_key WHERE state IN ('pending','approved')`, so the database refuses a
second open request for the same thing whatever the code does.

Partial, because once a request is denied or fulfilled the question becomes
askable again: a film that was declined last year may be fine now, and one that
was fulfilled and later deleted is a legitimate new request.

The join is reported as **200, not 201**, with a sentence saying what happened.
A silent 201 is how somebody re-requests the same film four times.

### 5. A cap on outstanding requests per account, not a rate limit

`MaxOpenPerUser = 20`. A rate limit bounds how fast one person fills a queue;
this bounds how much of it they can occupy at once, which is the thing that
makes the queue useless to everybody else. Counted inside the insert's
transaction, so two concurrent submissions cannot both see room for the last
one. Answered **429**, not 403: they are allowed, they have used their share.

### 6. A denial must carry a reason, and the requester sees it

Refused at the API, not just in the UI. A denial with no visible explanation is
the answer that makes people ask again tomorrow.

### 7. Fulfilment is closed by the importer, through a deliberately narrow hook

`importer.RequestCloser` is one method: *this info hash became this library
item*. The importer has no business knowing that requests exist as a concept,
who asked for what, or how approval works.

Three properties, each with a test:

- **Only a real import closes anything.** A skip or a failure means the thing
  they asked for is *not* in the library, and saying otherwise is worse than
  saying nothing.
- **It hands over the library ITEM, not the file.** A request is for a film; a
  film may acquire more files later.
- **A failure to close does not fail the import.** The file is on disk either
  way, and failing here would invite an operator to grab the same release twice.

`FulfilFromImport` takes no principal and checks no permission — the second
such path in the codebase after break-glass recovery, which is confined to the
host console by a structural test. This one needs no confinement, and the
reason is what it *can do* rather than who calls it: it only ever moves
approved → fulfilled for a hash a person already grabbed, and it cannot create,
approve, acquire, or change who sees what. Guarding it would mean granting the
importer authority over this table, and that grant would be worth more to an
attacker than the thing it protects.

## Consequences

**Accepted.**

- **No automatic fulfilment and no watchlist** (§1 above).
- **No metadata, so no poster, no canonical title and no library check by id.**
  A request for something already held is not detected automatically; an
  approver denies it with a reason.
- **A request cannot be edited or withdrawn.** An approver can deny it, which
  is the same outcome with a record attached.
- **Nothing notifies anybody.** A requester learns their request was fulfilled
  by looking. There is no mail transport (SECURITY.md) and no other channel yet;
  Discord was named in §13 as a later want.
- **`POST /api/v1/issues` is still unimplemented.** "This file is broken" is a
  different workflow from "please get this", and pretending one endpoint serves
  both would produce a queue nobody can triage.

---

## Addendum — what the browser found that the tests did not

Two defects survived a green test suite and were caught by driving the real UI.
Both are the same shape: a value that was *correct in the database* and wrong by
the time a person read it.

**The requester's own note was being overwritten by the server's message.**
Every other endpoint in this API uses `note` for its own explanation to the
operator; a request object already has a `note` the person typed. The handler
wrote its status message into the same key, so a request submitted with
"rewatch night" came back saying "Waiting for approval." — and the UI displayed
that as though the requester had written it. The server's commentary is
`message` on this endpoint, and only here, for exactly that reason.

**Every client-side validation message claimed the server was unreachable.**
`fail({status: 0}, 'a denial needs a reason')` reuses the error banner and looks
harmless, but `failure()` maps status 0 to "could not reach the server" — so an
operator who left a field blank was told their server was down. It had been
wrong in three places since the approvals view was written.
`TestNoValidationMessageMasqueradesAsANetworkFailure` now fails the build on it.

## Addendum — 2026-09-26: the grab link no screen ever made

Decision 1's "that grab is linked to the request" was true of the API and never
of the browser: the grab route takes `request_id`, and no screen sent one. A
request approved in the browser therefore stayed *approved — no release chosen
yet* after its film had arrived, and went on counting against its requester's
twenty open requests (decision 5). Found writing
[ADR-0028](0028-an-approved-request-is-satisfied-by-a-library-item.md), which
replaces the grab link as the way a request ends: an approver links an approved
request to the library item that satisfies it, and the request is fulfilled
when that item has a file — whichever grab, or scan, brought it. Decision 7's
hook is unchanged; what it closes is wider.

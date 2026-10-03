# ADR-0028: An approved request is satisfied by a library item

**Status:** accepted
**Date:** 2026-09-26
**Related:** [ADR-0017](0017-acquisition-requests.md),
[ADR-0019](0019-identification.md),
[ADR-0025](0025-adding-a-series-before-it-is-on-disk.md),
[ADR-0026](0026-adding-a-film-and-searching-for-it.md)

## The problem

ADR-0017 designed a request's life as: approved → a person grabs a release
*for it*, the grab naming the request → the import of that download fulfils it.
Two things have changed since, and together they leave an approved request with
no way to end.

- **No screen names a request when it grabs.** The grab API takes `request_id`;
  nothing in the browser sends one. The Search screen's Grab button, and the
  episode and film panels', send the ticket alone. So a request approved in the
  browser stays *approved — no release chosen yet* after its film has been
  downloaded, imported and watched, and it goes on occupying one of its
  requester's twenty open requests (ADR-0017, decision 5). This has been true
  since the Requests screen was built; it was found reading the code for this
  ADR.
- **Acquisition now goes through the library.** Since ADR-0025 and ADR-0026,
  the reliable way to acquire a title is to add it from the provider and search
  from its page, so that the grab is sealed to the item and the import files it
  there whatever the release calls itself. Grabbing from the general search —
  the path ADR-0017 assumed — is the path those ADRs exist to avoid, because its
  import names the title from the release.

And a smaller one: an approver who wants to act on a request has to go to the
Add screen and type again what the request already says.

## Decisions

### 1. A request is satisfied by a library item, and an approver says which

An approved request can be **linked** to one library item of the same kind:
`POST /api/v1/requests/{id}/item {media_item_id}`, on `request.approve`.

The link is an identification, and a person makes it (ADR-0019). The request is
words — *Dune*, 2021, "the new one" — and the item is the provider's; the
approver is the one who can tell which *Dune* was meant. Nothing proposes a
link on its own.

- **Only an approved request.** A pending one has not been agreed to; a denied
  or fulfilled one is finished.
- **The same kind.** A film request is satisfied by a film, a series request by
  a series. The kind decides which library a title belongs in (migration 0009),
  and a film request closed by a series is the mistake that column exists to
  prevent.
- **One item per request; several requests per item.** Two people who asked for
  one film in words that did not collide — `Se7en`, `Seven (1995)` — both see it
  arrive.
- **A link can be changed while the request is open.** A wrong identification
  must be correctable. Linking again replaces it, and the audit line says what
  it replaced.
- **Audited** as `request.linked`: who, which request, which item, and the item
  it was linked to before, if any.

### 2. Adding from the request

The Requests screen offers an approved request **Add to the library…**, which
opens the Add screen searching the provider for the request's kind, title and
year. The approver chooses the match and adds it as any title is added
(ADR-0025, ADR-0026: the provider's id names it, and nothing is written to
disk), and the request is linked to it. If the title is already in the library,
the refusal names that item, as it did, and the screen offers to link the
request to it instead.

The add and the link are two calls, each on its own permission —
`library.edit` and `request.approve`. An add whose link then fails is still an
add, and the screen says the link did not happen.

### 3. Fulfilment follows the item

A request linked to an item is fulfilled when that item has a file:

- **when a file is imported into it**, whichever grab brought the file. The
  importer's hook is unchanged — *this download became this item* (ADR-0017,
  decision 7) — and the request side now closes the requests waiting on that
  download *and* the requests linked to that item;
- **when the link is made**, if the item already has a file: the thing asked for
  is in the library, which is all *fulfilled* says;
- **when a scan records one** — a file put in the item's folder by hand. The
  hourly scan task closes linked requests whose item now has a file.

A series request is fulfilled by the series' first file, not by the last
episode anybody wanted. What "every episode they wanted" would mean is the
monitoring choice made when the series was added, and a request records none;
the requester finds the series in the library and can see what it has.

The two paths with no person behind them — the import's and the scan's — are
justified as ADR-0017 justified the first: each only moves *approved* to
*fulfilled*, for a request a person approved and a person linked, when a file is
there. Neither can create, approve, link, acquire, or change who sees what.

### 4. What the requester sees

A linked request says what it is linked to — *In the library as Dune (2021);
nothing on disk yet* — to anyone who may see the request **and may browse the
library**. Without `media.browse` the request says it is linked and not to what.
The answer to linking follows the same rule: an approver who may not browse is
told the item's id back, not its title, so linking cannot be used to read the
library one id at a time.

## Rejected alternatives

**Approval that adds.** Jellyseerr's approve-and-add. Approving here is agreeing
that the instance may acquire something (ADR-0017, decision 1); adding needs an
identification the request's words cannot supply, and a different permission.

**Requests by provider id, chosen by the requester.** It would make a request
precise from the start. But searching the provider is gated on `library.edit` —
it is an editor's lookup, and every search spends a request against a third
party on the operator's credential. Opening it to every account to fill in a
request form is a larger change than this one, and the approver would still have
to check the choice.

**Linking by matching the request's title against the library.** The same
comparison of words ADR-0017 (decision 3) declined to trust for duplicates, now
deciding which item closes a request. A false match fulfils a request with the
wrong film and tells the requester it is in the library.

**Sending `request_id` from the Grab buttons.** It would make ADR-0017's path
work from the screen — but only for a grab made while looking at the request,
and a grab from a film's page, the one ADR-0026 made reliable, would still close
nothing unless somebody remembered the request.

## Known limitations

- **A request still cannot be withdrawn or closed without a file.** An approved
  request for something that never turns up stays open, and counts towards its
  requester's cap (ADR-0017).
- **A fulfilled request stays fulfilled** if its file is deleted later, as
  before.
- **Nothing notifies the requester** (ADR-0017).
- **Per-library visibility does not exist yet.** When it does, the name of the
  item a request is linked to must be subject to it, like every other read of an
  item.

# ADR-0031: The audit log gets a screen, and anonymous noise a ceiling

**Status:** accepted
**Date:** 2026-09-27
**Related:** [ADR-0012](0012-root-redirect-carve-out.md),
[ADR-0030](0030-automatic-acquisition.md), SECURITY.md (*Authorization model*,
*Not yet enforced*), docs/RUNBOOK.md (*What to watch*)

## The problem

Everything this software does that an operator is answerable for is written to
`audit_event`: sign-ins and failures, denials, account and role changes, every
grab — and since ADR-0030, every grab a machine made in the operator's name.
Nothing reads it back. `GET /api/v1/admin/audit` has answered 501 since the
route table was written; the runbook tells the operator to open the database
with `sqlite3`; SECURITY.md lists "no audit-log viewer" under *Not yet
enforced* and, a few sections later, asks the operator to "watch the audit log
for `authz.denied` bursts" — which they cannot do.

Building the reader turned up the second half of this record. **Any anonymous
client on the internet can add a row to the log with one request**: a request
for a protected route without a session is answered 404 and recorded as an
`authz.denied` by `anonymous`, with its address and user agent. That is by
design — a probe of the administration routes is exactly what the log is for —
but it has no ceiling. A scanner making ten requests a second writes 864,000
rows a day: the disk fills, every backup carries it, and a screen showing the
newest hundred rows shows only the scanner.

## Decisions

### 1. Administrators read it, through the permission the data layer already checks

`admin.audit`, which only the built-in Admin role holds (a Manager does not;
requirements §7). The two routes are administration routes: invisible — 404,
not 403 — to everyone else (ADR-0012's rule). The data layer checks the
permission again, as `audit.Logger.List` always has, so no future handler can
reach the rows without it.

An API token scoped with `admin.audit` may read it, as a token may reach any
route its scope and its owner's current role allow. That is how a log is
shipped to a SIEM; an administrator who mints such a token has decided to.

### 2. Reading is not audited

Only the administrator can read it, reading changes nothing, and every page
viewed would add a row to the thing being read. The instance has one
administrator (SECURITY.md, *Break-glass recovery*); an audit line saying the
administrator looked at the audit log tells them nothing.

### 3. Every column, newest first, filtered on the server

Each event is shown whole: when, who (the actor's label and, for a person,
their id), what, the outcome, the target, the source address and user agent,
the detail, and the before and after values. Nothing is summarised away —
the point of the screen is that the operator sees what was recorded.

- **Newest first, a page at a time**: 100 by default, 200 at most, continued
  with a cursor on the event's id (`before=`). Not an offset: the log grows
  while it is read, and an offset would show a row twice or skip one.
- **Filters**: a category (the action's first word — `auth`, `authz`,
  `account`, `user`, `indexer`, `egress`, `request`, `acquisition`, `media`,
  `system`), an exact action, the outcome, the actor, the target, a time range,
  and free text matched in the detail, the target, the actor, the address and
  the user agent. Every parameter is validated and an unknown one is refused,
  so a misspelt filter is an error rather than an unfiltered page that looks
  filtered.
- **A summary**: counts by action and outcome over the last seven days (up to
  ninety), so a burst of denials is visible before scrolling. The window ends
  at the log's own clock, the one every row was stamped by.

Every value is shown as text. Much of the log was written by strangers — user
agents, usernames at signup, release names — and the page builds no markup
from any of it, as no page in this software does.

### 4. Denials get a ceiling, and a count of what was not written

`authz.denied` rows are written individually up to a ceiling, and counted
beyond it:

- **Anonymous** — no session, or a session that is not usable: at most **20 an
  hour from one address**, and **120 an hour in all**.
- **A signed-in person**: at most **60 an hour** each. Their denials are never
  cut by the anonymous ceiling, so an anonymous flood cannot be used to hide an
  account probing the administration routes.

What is over the ceiling is counted in memory, and once the hour is over one
row is written — `authz.denied.suppressed`, by `system:audit` — saying how many
were not written one by one and from where (the ten busiest sources by name,
the rest as a count). A scheduled task, `audit.denials`, writes it within five
minutes of the hour ending, so a scanner that stops does not leave its count
unwritten; a server that is stopping writes the hour so far. The denial metric
(`cms_authz_denials_total`) is counted before the ceiling and still counts
every one.

The counting is bounded: at most 4,096 addresses and 4,096 accounts are told
apart in an hour. Addresses and accounts are counted apart, so a flood from
many addresses — an IPv6 prefix holds more than anybody could count — cannot
use up the counting that holds an account to its ceiling. Past the bound,
sources are counted together as "other sources" and held to the ceiling for
every address together.

The worst an anonymous flood can now add is 121 rows an hour — under 3,000 a
day — however many addresses it comes from, and the evidence that it happened
is kept: its first rows whole, and the count of the rest.

Denials decided inside a service — a refused escalation, a refused suspension
(SECURITY.md, *Authorization model*) — are written by their handlers, not
through this path, and have no ceiling: they need an account, and each is the
kind of event the log exists for.

## Rejected alternatives

**Not recording anonymous denials at all.** It is where probing of the
administration routes shows, and §8 lists authorization denials among what the
log must hold. A ceiling keeps the evidence and bounds the cost.

**Retention: deleting old rows.** The log is append-only, and a test fails the
build if the audit package gains a method that deletes. With the ceiling, what
remains grows with what people and tasks do — hundreds of rows a day, a few
megabytes a year. Archiving and trimming it, if it is ever needed, is a
decision with its own record.

**Export (CSV or JSON Lines).** The paginated API is the export for a script,
and backups carry the whole log, encrypted. A CSV adds the formula-injection
problem — user agents are written by strangers, and a spreadsheet will run
`=HYPERLINK(...)` — for no reader this instance has.

**Auditing reads.** Decision 2.

**Offset pagination.** Decision 3.

## Known limitations

- **The suppressed counts of an hour that has not ended are lost if the
  process dies.** They live in memory until the hour is over; a stop the
  process is told about (`docker stop`, Ctrl-C) writes them first.
- **The ceiling is per process.** One instance is the design (§2); a second
  would have its own.
- **Addresses are as the client-address resolution saw them** — the reverse
  proxy's, when `CMS_TRUSTED_PROXIES` is wrong (RUNBOOK §3). A ceiling per
  address is then a ceiling on everyone at once, and the summary says so by
  naming one address.
- **Free-text search reads every row it filters.** At the scale this instance
  is sized for it is milliseconds; it is not indexed.

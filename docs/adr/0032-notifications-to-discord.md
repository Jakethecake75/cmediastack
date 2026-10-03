# ADR-0032: Notifications go to one Discord webhook, read from the audit log

**Status:** accepted
**Date:** 2026-09-27
**Related:** [ADR-0013](0013-egress-guard-design.md) (the `notification` profile),
[ADR-0017](0017-acquisition-requests.md) (*Nothing notifies anybody*),
[ADR-0018](0018-metadata-and-artwork.md) (a credential set on a screen),
[ADR-0031](0031-reading-the-audit-log.md), requirements §13

## The problem

Nothing this software does reaches the operator unless they look. A backup that
has failed for a week, a tunnel that went down, a flood of denials, somebody
entering the right password and the wrong authenticator code, an account
request waiting for approval — each is on a screen, and none of them says so.
§13 named Discord as the channel, for later. ADR-0013 already reserved an egress
profile for it, `notification`, exempt from the kill switch: a kill switch that
also silences the alert saying it fired is one you discover from an empty
library.

## Decisions

### 1. One Discord webhook, and nothing else

A Discord *incoming webhook* — a link the operator creates in a channel's
settings — is the transport. It needs no bot, no account and no OAuth: the link
is the whole credential. One webhook per instance; everything goes to it.

Other transports are not built. A generic "POST JSON to any URL" is an SSRF
primitive with a settings screen, and the `notification` profile deliberately
does not refuse private addresses (ADR-0013) — a stolen administrator session
would get a way to make the server send requests into the operator's network.
Email has no transport here. The transport sits behind one small interface, so
a second one is a file, and a decision with its own record.

### 2. The webhook link is a credential

Anyone holding it can post into the channel as the instance. So it is handled
as the metadata key is (ADR-0018):

- **Stored sealed** in the `setting` table (AES-256-GCM, bound to the context
  `setting:notification.discord_webhook`), set on a screen, and never read
  back over HTTP — not whole, not masked. No configuration-file or environment
  setting: configuration files get pasted into forums.
- **Only a Discord webhook link is accepted**: `https`, a Discord host
  (`discord.com`, `discordapp.com`, `canary.` and `ptb.discord.com`), the path
  `/api/webhooks/<id>/<token>` (optionally versioned), nothing else — no user
  information, no query, no fragment. It is stored in one normal form, on
  `discord.com`. A redirect is followed only to a Discord host.
- **Checked before it is kept**: Discord is asked about the webhook — a `GET`,
  which posts nothing — and a link Discord does not know is not stored. The
  answer names the webhook, so the screen can say where messages will land.
- **Never in a log line, an error or the audit log.** The token is in the URL
  path, and Go's transport errors print the URL; every error from the transport
  is reduced to its cause before anything sees it. Storing, replacing and
  removing it are audited as `system.setting.changed`, without the link.
- **Leaving a channel is said in it.** Turning notifications off, or pointing
  them at another webhook, posts one last message to the channel they leave:
  who did it, and what the new webhook calls itself. Somebody holding the
  administrator's session who silences the operator's channel, or moves it to
  their own, does so in front of the operator. Best effort: a webhook Discord
  has forgotten cannot be told, and the change is made regardless.

### 3. What is sent is read from the audit log

The audit log is already the one stream of what happened (ADR-0031). A task,
`notify.discord`, reads it every minute from where it last stopped — a cursor
kept in the `setting` table — and sends what the operator chose. Reading needs
`admin.audit`, so the task runs as its own principal, `system:notify`, holding
that and nothing else. Configuring what goes out needs `admin.audit` as well as
`admin.system`: choosing what leaves the log is reading it.

One thing that is not an audit line is sent too, from memory: **a scheduled
task that starts failing, and one that recovers** — the first failure after a
success, and the first success after failures, never each repeat.
`egress.health` failing is the tunnel going down; `database.backup` failing is
backups stopping.

The first configuration starts from the newest line: the history is not sent.

A file arriving in the library was not an audit line. It is now,
`media.imported` by `system:import`, because "it arrived" is the one library
event most worth hearing about and because the log, which records every other
change to the library, was missing it.

### 4. Categories, and what each one sends

The operator turns categories on and off; lines are never chosen one by one.

| Category | Default | Sends |
|---|---|---|
| **Security** | on | Denials over the audit log's ceiling; a signed-in account refused a route; sign-ins throttled; a correct password with a refused authenticator code; a recovery code used; an authenticator enrolled; a password changed, or a reset link made; an API token issued; recovery from the host; roles, suspensions, grants; settings and indexers changed; a restore |
| **Accounts** | on | An account requested and waiting for approval; an invite redeemed; a request that expired |
| **Operations** | on | A scheduled task failing or recovering — the tunnel, backups, acquisition; the kill switch engaging |
| **Library** | off | Grabs, a person's or automatic acquisition's, and failed ones; downloads that stopped moving (ADR-0034); files arriving; deletions, restores, the trash emptied |
| **Requests** | off | Requests made, approved, denied and fulfilled |

Library and requests are off because they send titles — what the library holds
and who asked for what — to a third party. The operator turns them on knowing
that; the screen says it beside them.

Never sent, whatever is on: a person's address or user agent (addresses appear
only for anonymous sources, in flood and throttling lines), an email address,
anything the log does not hold. Successful sign-ins and sign-outs are not sent:
a notification for every one teaches the reader to ignore the channel.

### 5. How it is written

Much of what is sent was written by strangers — a username chosen at signup, a
release name, the addresses in a flood line. Discord renders markdown, links
and mentions, so a username of `@everyone` or `[Fix your server](https://…)`
would ping the server or show a disguised link in the operator's own channel.

- **Everything not written by this program goes inside a code span**, where
  Discord renders no markdown, no link and no mention; backticks in it are
  replaced, control and bidirectional-override characters removed, whitespace
  folded, and each value clipped.
- **`allowed_mentions` is empty** on every message, so nothing pings even if
  something got past the above; **embeds are suppressed**, so no link is
  previewed.
- A line is an icon for its category, a title in this program's words, who and
  what in code spans, and the time as a Discord timestamp (shown in the
  reader's own timezone). No links to the instance: Discord fetches links to
  preview them, and a link would name the instance's address to a third party.

### 6. Delivery: bounded, and at least once

- Every minute; **at most three messages a run** — well under the rate
  Discord allows a webhook — of at most 2,000 characters. Identical lines in a
  run are one line with a count. What does not fit is one closing line —
  "…and 212 more — the Audit log has them all." — and is not sent later.
- Lines older than a day when they are sent — the notifier was failing, or the
  instance was off — are counted in one line, not listed.
- **At least once.** The cursor moves after Discord accepts, so a crash between
  the two, or a failure partway through a run, sends something twice. Nothing
  is lost to a Discord outage: the next run starts where the last one stopped.
- **Discord's answer is obeyed.** A 429 waits as long as Discord asks. A webhook
  Discord no longer knows (401, 404) stops delivery until the link is replaced
  — it is not retried every minute — and says so on the Notifications screen
  and as a failing task. A message Discord refuses as malformed (another 4xx)
  is passed over rather than retried for ever, since the same lines would be
  refused again and hold up everything after them. Anything else — Discord
  out of reach, or failing — is retried next minute.
- A test message can be sent from the screen, at most five a minute.

## Rejected alternatives

**Sending from the write path** (a hook in `audit.Write`). A write would wait on
Discord or queue in memory, and a queue in memory is lost with the process. The
log is already durable; reading it with a cursor costs one indexed query a
minute.

**A notification per user** (a requester told their request was fulfilled). It
needs a per-person address — a Discord user id, a DM channel, a bot — and that
is a different feature. A household channel with *Requests* on covers the
common case.

**Links back to the instance, and rich embeds.** Above: a link is fetched by
Discord and names the host, and an embed is more places for a stranger's text
to be interpreted.

**Per-line choices.** Twenty-odd switches that nobody sets. Five categories,
with what each sends written beside it.

## Known limitations

- **With the whole application inside gluetun's namespace** (RUNBOOK §4),
  nothing leaves while the tunnel is down — this message included. The failure
  and the recovery arrive together when the tunnel returns. The exemption from
  the kill switch helps only when the application has a route of its own.
- **A task's failure noted in memory is lost if the process stops** before the
  next run sends it.
- **Discord sees what is sent**: usernames, the addresses of anonymous sources,
  and — with library and requests on — titles. A failing task's message is sent
  as the task wrote it, the text the Tasks screen shows, and can name hosts,
  addresses and paths on the operator's network.
- **One webhook, one channel.** No routing by category.

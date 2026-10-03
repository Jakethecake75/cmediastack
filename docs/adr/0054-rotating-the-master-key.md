# ADR-0054: Rotating the master key

**Status:** accepted
**Date:** 2026-10-01
**Related:** [ADR-0029](0029-encrypted-verified-backups.md), SECURITY.md *The master key*

## The problem

Every stored secret is sealed under `CMS_MASTER_KEY` with AES-256-GCM, and the
storage context is bound as additional data:

- each account's TOTP secret;
- each indexer's API key;
- the metadata provider's token;
- the Discord webhook.

SECURITY.md has described rotation as "envelope re-encryption … tracked in
PROGRESS.md" since Phase 1, and it was never built. A key that may have leaked
cannot be replaced without re-entering every secret by hand, and a TOTP secret
cannot be re-entered at all short of every account re-enrolling.

## Decisions

### 1. On the host, with the server stopped

`cmediastack -rotate-key`, beside `-recover` and `-restore-backup`, for the
same reason: it needs the master key, which is stronger authentication than
anything a route can offer, and it adds no reachable surface. The old key is
the one the server reads, `CMS_MASTER_KEY` (or whatever `secrets.master_key_env`
names). The new key is read from the same name with `_NEW` added. Both come
from the environment and never from a flag, because flags land in shell
history and in `ps`.

It refuses to run while the server answers its readiness probe. A running
server holds the old key and would fail to read every value the rotation had
just re-sealed. It also refuses when the two keys are the same, or when the
new one is not 32 base64-encoded bytes.

### 2. Every sealed value, in one transaction, or none

Each value is opened with the old key and its own context, then re-sealed with
the new key under the same context, all in one transaction. If any value will
not open under the old key, nothing is changed and the row is named. That
happens when the key given is not the one the database was sealed with, or a
value was sealed under another. A half-rotated database, some values under
each key, could not be read with either. The audit log records the rotation
and how many values of each kind were re-sealed, never a key. That record goes
in the same transaction, so there is no rotation without its line.

### 3. A sealed value cannot be added without being rotated

A test reads every call that seals with the cipher. Each one must be either
in the rotation's list or one of the two that are deliberately not:

- grab tickets, which last fifteen minutes;
- signup's proof-of-work challenges, which last ten.

Both simply expire, and one in flight during a rotation is refused, which only
means searching or fetching a challenge again. A new secret stored without
being added to the rotation fails the build.

### 4. Backups keep the old key

A backup is encrypted to a passphrase derived from the master key (ADR-0029).
Rotating does not re-encrypt backups already written: they are files outside
the database, possibly offline. **The old key must be kept with the old
backups** until they age out. `-verify-backup` and `-restore-backup` read a
backup with whichever key is in `CMS_MASTER_KEY`. The command says so when it
finishes, along with what to do next: put the new key where the server reads
it, then start the server.

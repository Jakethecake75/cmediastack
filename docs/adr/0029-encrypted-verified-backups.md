# ADR-0029: Backups are encrypted snapshots, checked before they are kept, and restored only from the host

**Status:** accepted
**Date:** 2026-09-27
**Related:** [ADR-0004](0004-sqlite-only-v1.md),
[ADR-0016](0016-import-pipeline.md), SECURITY.md (*The master key*,
*Backups*), docs/RUNBOOK.md (*Backups and restores*)

## The problem

Requirements §8 asks for encrypted, verifiable backups. What existed was a
primitive — `db.BackupTo`, a `VACUUM INTO` — an audit action,
`system.backup.created`, that nothing wrote, and
`POST /api/v1/admin/system/backup` answering 501. Nothing took a backup.

Everything the instance knows lives in one SQLite file: the accounts and their
authenticators, which title each folder is, what was wanted and requested and
grabbed, the audit log. Lose the file and the media survive on disk with none of
that attached to them — and the operator is about to start a library from
nothing, which is exactly when a backup regime should begin rather than after
the first loss.

## Decisions

### 1. The database is backed up, and nothing else

The media are the operator's files, on the operator's storage and its
snapshots. The configuration file is one the operator wrote. The artwork cache
refills itself from the provider; downloads are in flight. And the master key is
deliberately in **no** backup: SECURITY.md already says a backup holding both
the database and the key is a backup of plaintext secrets.

### 2. A consistent snapshot, checked before it is kept

A backup starts as a `VACUUM INTO` a private file while the application keeps
running — SQLite takes a read transaction, and writers carry on. The snapshot
is written **beside the live database**, never into the backup directory: that
directory may be a NAS, and the plaintext should not travel. It is created
empty and `0600` before SQLite writes into it, because a file SQLite creates
itself is `0644` less the umask, and this one holds everything the database
does.

The snapshot is then opened immutable — no locks, no `-wal` or `-shm` beside
it, no writes — and must pass:

- SQLite's `integrity_check`;
- SQLite's `foreign_key_check`, for damage the integrity check cannot see: a
  structurally sound page holding a reference to nothing;
- a comparison of its recorded migrations with this binary's, version for
  version and checksum for checksum, with none missing and no gap.

Only then is it encrypted. The plaintext is removed when the backup finishes,
kept or not; one left by a crash is removed before the next backup starts, as
is a half-written backup. Only files with those exact names are ever removed.

### 3. Encrypted with age, to a passphrase derived from the master key

Each backup is an [age](https://age-encryption.org/v1) file, written with
`filippo.io/age` v1.3.2 to a scrypt recipient whose passphrase is derived from
the master key:

```
passphrase = unpadded base64url( HKDF-SHA256( master key, salt: none,
                                              info: "cmediastack backup v1",
                                              length: 32 bytes ) )
```

- **A backup is useless without the master key**, and the master key is
  already the one thing the operator must keep apart from the database. One
  secret to keep, not two — and a restore needs that key anyway, because the
  credentials inside the database are sealed under it.
- **The passphrase is not the key.** It can be handed to the standard `age`
  tool without handing over the key that seals every stored credential.
- **The format is not ours.** Given the passphrase — derivable with `openssl
  kdf` or a Python one-liner, both in the runbook — the standard `age` tool
  decrypts a backup. A restore does not depend on this software still building.
  Tested: the distribution's `age` 1.1.1 decrypts a backup written by the
  library's 1.3.2, with a passphrase from the `openssl` recipe.
- **Symmetric, so a backup cannot be forged.** age's own documentation
  recommends native X25519 keys over passphrases for machines. For backups the
  passphrase is the better fit, for a reason specific to restoring: anyone who
  knows an X25519 *public* key can write a file that decrypts with the private
  one, so a file planted in the backup directory — an administrator account
  with a password the attacker chose — would restore cleanly. Only a holder of
  the passphrase can write a file the passphrase opens. It is also
  post-quantum as it stands: 256 symmetric bits.
- **Work factor 2^15.** scrypt's cost exists to slow down guessing a *person's*
  passphrase; this one is 256 random bits, so age's default of 2^18 — a
  quarter of a gigabyte per backup — would cost a small machine for nothing.
  A file demanding more than 2^18 is refused before any of that work is done.

The derivation is pinned by a test to an answer computed outside Go — Python's
`hmac` module and `openssl kdf`, which agree — because a change to it would
leave every existing backup undecryptable and nothing else would notice.

Why encrypt at all, when the credentials inside are already sealed: the file
also holds password hashes, email addresses, what people watched and asked
for, and the audit log — and a backup exists to be copied somewhere else,
where this host's protections do not reach.

### 4. Confirmed after writing, and verifiable later

The snapshot is hashed as it is encrypted, into a file named so no listing
counts it as a backup. That file is then read back from disk: its bytes must
hash to what was written, and they must decrypt — every chunk authenticated —
to the snapshot that passed its checks. Only then is it renamed to a backup's
name, `cmediastack-YYYYMMDDTHHMMSS.mmmZ.db.age` (no colons: backups get copied
to SMB shares, and Windows refuses them). The file, its size, the SHA-256 of
the encrypted file and the schema version go into the audit log.

`cmediastack -verify-backup FILE` does the same later, against any copy — the
one on the NAS included: it decrypts into a private temporary directory, runs
the checks, reports what the backup holds and its SHA-256 (to compare with the
audit log's record), and removes the plaintext. A backup nobody has checked is
a hope; this makes checking one a single command. It says which of three
things is wrong when one is: not a backup at all, not this key's, or damaged.

### 5. Taken on a clock that restarts cannot reset

A check runs hourly, **and once as the scheduler starts**, and takes a backup
when the newest one on disk is older than `backup.interval` (default 24 hours).
The scheduler's timers start again at every restart, so a daily task on an
instance restarted more often than daily would never run at all — and an
hourly one would never run on an instance restarted more often than hourly.
The disk, not the process, remembers when the last backup was.

A newest backup that claims to be from more than an interval in the future —
the clock went back, or a name lies — does not stop the schedule: waiting for
the clock to catch up could mean no backup for a year, so one is taken.

`backup.interval: 0s` turns scheduled backups off, for an operator whose own
snapshots cover the database; pruning continues, and a backup can still be
taken from the admin screen. The configuration lint refuses an interval under
an hour, which the hourly check could not honour.

### 6. Kept by age, never below a floor, and pruned only by the schedule

A backup older than `backup.keep` (default 7 days) is deleted by the hourly
check, but never one of the newest `backup.keep_min` (default 3; the lint
refuses 0): a schedule that has stopped working must not delete the last good
backups on its way out — they are the ones worth having. Pruning runs even when
the backup it follows failed, and the floor is what makes that safe.

Pruning by count was rejected for a specific reason. Anyone who can take a
backup — a stolen administrator session included — could then take a few
quickly and push every older backup out. By age, taking backups deletes
nothing; the admin screen's button does not prune at all. Each deletion is
audited, and only regular files named as backups are ever deleted: a symlink
or a directory named like one is not followed, and nothing else in the
directory is touched.

### 7. Taken from the host or the admin screen; never downloaded, never restored over HTTP

`POST /api/v1/admin/system/backup` takes one now and
`GET /api/v1/admin/system/backups` lists them with the policy — both
`admin.system`, hidden from everyone else, and the first audited with who and
from where. There is **no route that serves a backup and none that restores
one**, and a test fails the build if one appears:

- a download route would make a stolen administrator session a way to carry off
  the whole database in one request;
- a restore route would make it a way to roll the instance back — suspended
  accounts live again, old passwords valid again, the audit log rewound.

The operator copies the directory off the host with their own tooling, and
restores on the host, where the master key is:

```
cmediastack -restore-backup FILE -restore-to PATH
```

decrypts and checks into `PATH`, a new file. It refuses a `PATH` that exists —
or whose `-wal`, `-shm` or `-journal` does, because a `-wal` left beside a
restored database is replayed into it when it is opened — and a backup whose
migrations this binary does not know. An older backup is accepted and brought
up to date by the server when it starts on it.

Before it hands the database over, the restore **fences** it: every session in
it is ended, every API token revoked, and a `system.backup.restored` record
written into its audit log naming the file and its hash. A backup holds who was
signed in and which tokens worked when it was taken; restored as it is, a
session stolen and ended since, or a token that leaked and was revoked since,
would work again. Everyone signs in again, with the authenticator the backup
holds; token owners issue new ones.

Putting it in place is the operator's, and the command prints the steps with
the configured paths: stop the service, move the old database and its `-wal`
and `-shm` aside, move the restored one in, start the service.

### 8. Where the files go

`backup.dir` defaults to `backups` beside the database — `/config/backups` in
the container. The directory is created `0700` and each file `0600`.

**Only the default directory is ever created.** A directory the operator named
is expected to exist already, because a named directory that has vanished is
far more often a disk that is not mounted than one they forgot to create — and
creating it would put the backups on this host's own disk, under the mount
point, while the operator believed they were going somewhere else. The listing
says when the backups share the database's filesystem, because that is the one
thing about the default an operator most needs to hear.

## What building it changed

This record was written before the code, and the code changed four things.

- **The backup directory may be inside a library root.** The first draft
  refused that. It had no reason that survived being written down: nothing
  scans, serves or purges a `.age` file, and the NAS the media live on is one
  of the best places a backup can go — off the database's disk. The refusal
  was replaced by the never-create rule above, which addresses the real hazard.
- **The snapshot is written beside the database, not in the backup
  directory**, so the plaintext never reaches a NAS.
- **A restore fences what it restores** (decision 7). The first draft only
  noted, as a limitation, that a restore brings back old sessions.
- **The foreign-key check** joined the integrity check.

It also found two defects outside backups, both fixed:

- `db.Options.ReadOnly` could never have worked. `Open` sets WAL mode, a write
  to the file's header, and a read-only connection refuses it — so it failed on
  any database not already in WAL mode, a snapshot included. Nothing used it,
  which is how it survived. It is gone; a file to be checked is opened with
  `db.OpenSnapshot`.
- The database path was put into a SQLite URI unescaped. A directory named
  with a `#` or `?` in it made SQLite open — and create — a different file:
  everything before that character, in the parent directory. The path is now
  escaped and made absolute.

## Rejected alternatives

**A streaming format of our own** — AES-GCM over chunks. It is the one thing
here that is easy to get subtly wrong, and age is a specified, reviewed format
with a reference implementation in Go.

**An age X25519 recipient, with the identity kept offline**, so the server
could write backups it cannot read. It adds a second secret for the operator to
keep and lose, and buys little: whoever holds the server holds the live
database and the master key already, and a restore needs the master key
regardless. Deriving the identity from the master key instead would remove the
second secret, and still leave backups forgeable by anyone with the public
half, and underivable with standard tools (the identity is bech32).

**Leaving encryption to the operator's backup tool.** §8 asks for encrypted
backups, and a plaintext snapshot would sit beside the database, with the
database's contents, until something copied it away.

**Refusing a backup directory inside a library root.** See *What building it
changed*.

## Known limitations

- **The backups share a disk with the database** until the operator copies them
  off or points `backup.dir` elsewhere. They protect against a bad migration, a
  mistake and a corrupt file; not against the disk. The admin screen says so
  when it is true. The signal is one-sided: two ZFS datasets on one pool are
  different filesystems on the same disks.
- **A restore rolls back everything else**, the audit log included: accounts
  suspended since the backup are active again, passwords changed since are the
  old ones. The restored audit log records the restore as its newest line, and
  the command says to review the accounts.
- **No point-in-time recovery.** Up to `backup.interval` of changes can be lost.
- **Master-key rotation** (not built) will leave older backups under the old
  key; the rotation will have to say so, and keep the old key available.
- **Backups are checked, not test-restored into a running server.** The checks
  are SQLite's own and the migrations', and a restore into a new file is one
  command; starting a second server on it is the operator's.

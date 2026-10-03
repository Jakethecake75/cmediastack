# ADR-0015 — Library path containment, and why hardlinks rather than moves

**Status:** Accepted · 2026-09-12 · Enables Phase 3

## Context

Phase 3 is the first time CMediaStack writes to the operator's filesystem. It
writes using names chosen by strangers:

- a torrent's `name` field is a bencode string the uploader filled in;
- a release title comes from an indexer's XML;
- a film or series title will come from a metadata provider's JSON.

`../../etc/cron.d/x` is a legal value for every one of those, and this software
runs with write access to the operator's media storage. Requirements §2 asks
for deny-by-default everywhere and for destructive operations to be reversible
or audited; neither means anything if a path can be made to point outside the
library at all.

There is a second, less dramatic failure that matters just as much in practice:
a name that is *contained* but *unusable*. A filename with a `\r` in it cannot
be safely printed to a terminal, which is how an operator would try to deal with
it. A name ending in a dot silently becomes a different name on Windows, so two
films collide and one overwrites the other. A 300-byte title is refused by the
filesystem outright.

## Decision

### 1. Containment is the kernel's job, not a string comparison

Three approaches were considered and two rejected:

| Approach | Why it fails |
|---|---|
| `filepath.Join(root, name)` | `Join` **cleans** its result. `Join("/lib", "../../etc")` is not under `/lib` — it is `/etc`. There is no containment here at all. |
| `Join` then `strings.HasPrefix` | Catches `..`, misses a **symlink**. A link at `<root>/x` pointing to `/etc` has a perfectly innocent prefix. |
| `filepath.EvalSymlinks` then check the prefix | Time-of-check-to-time-of-use. The link can be created between the check and the open. |

What is used instead is **`os.Root`** (Go 1.24+). It holds a file descriptor to
the root directory and resolves every path component relative to it using
`openat2(RESOLVE_BENEATH)` on Linux. Containment is enforced by the kernel, per
component, at the moment of use. There is no window to race and no string
comparison to get wrong.

This is deliberately *not* a hand-rolled containment layer. §12 says not to
reinvent solved problems, and this one is solved in the standard library by
people who thought about it harder than a media-server author would.

### 2. The claim is measured, not asserted

`TestNoPathEscapesItsRoot` plants real symlinks — one to a sibling directory,
one to `/etc` — inside the root and tries eight shapes of escape through eight
operations. A companion test implements the naive Join+prefix version beside it
and demonstrates it leaking:

```
the naive Join+prefix implementation leaked 3 of 4 paths:
  escape/secret.txt     -> SECRET
  etc/passwd            -> root:x
  ./escape/./secret.txt -> SECRET
```

`os.Root` refuses all four. That comparison is in the test suite rather than in
this document so that it stays true.

### 3. Holding a Vault *is* the capability

`Vault` is the only way this software touches library files. A function that has
one may write inside that root and nowhere else; a function that does not have
one cannot write to the library at all. The admin API's `RootFolderService`
interface deliberately exposes no method that returns one: the admin surface
configures *where* the library lives, it does not touch files inside it.

### 4. Nothing is allowed to go around it

`TestNothingWritesOutsideAVault` reads the source of this package and the import
pipeline and fails the build on `os.Create`, `os.OpenFile`, `os.Rename`,
`os.Remove`, `os.RemoveAll`, `os.MkdirAll`, `os.WriteFile`, `os.Link`,
`os.Symlink`, `os.Truncate` — and on `filepath.Join`, which is the one that
looks harmless and is not.

Same technique, and the same reasoning, as `egress.TestNoPackageDialsDirectly`:
the failure that matters is not a check returning the wrong answer, it is a code
path that never consults the check at all. One `os.Rename` written by somebody
thinking about episode numbering is invisible in review, because it looks
exactly like ordinary Go — and it takes a stranger-supplied name straight to the
filesystem. A runtime check cannot prove a code path does not exist.

**Two files are exempt**, and the exemptions are narrow and written down:

- `vault.go`, which is the containment layer itself.
- `hostfs.go`, which operates on paths that no root contains — because the path
  *is* the root, and is not yet configured. **The invariant that makes that safe
  is provenance, not containment**: every path it handles comes from the
  operator through configuration or an authenticated admin request, never from a
  torrent, an indexer or a metadata provider.

### 5. The effect is checked where the effect happens

`Vault.Remove`, `RemoveAll` and `Rename` call `authz.RequireEffect` themselves,
in the filesystem layer, not in an HTTP handler. That is the design
`internal/authz` was built around: "delete media files: admin only" is
meaningless if a Manager can reach the same effect by removing a torrent with
its data, repointing a root folder, or renaming a file into a void.

`TestEveryDestructiveMethodChecksAnEffect` parses the vault's own source to
confirm each one does, because a new destructive method added without the check
would be invisible in review.

`RemoveAll(".")` is refused outright. It would delete an operator's entire
library in one call; nothing here needs that, and no permission should imply it.

### 6. Containment and sanitisation are separate jobs

Neither substitutes for the other. A perfectly contained path can still be a
filename an operator cannot delete from a shell; a perfectly tidy name can still
be `..`.

`SafeComponent` handles the second job, and its decisions:

- **Unicode is kept.** A film's title in its own script is its correct name.
  Transliterating to ASCII produces a library nobody can search.
- **255 bytes, not characters**, cut on a rune boundary — a Japanese title
  reaches the limit in 85 characters, and a name split mid-rune is invalid UTF-8
  on disk.
- **Extensions are preserved** when truncating. Losing the tail of a long title
  is cosmetic; losing `.mkv` stops the file being playable by anything that
  dispatches on it.
- **Windows reserved device names** (`CON`, `NUL`, `COM1`…) get an underscore.
  CMediaStack runs on Linux, but an operator's library outlives the software
  that filled it, and a library containing a file called `CON` cannot be copied
  to a Windows machine or served over SMB to one.
- **Trailing dots and spaces are trimmed**, because Windows strips them
  silently, so `Film.` and `Film` become the same file there and one overwrites
  the other.
- **A name that was entirely illegal characters is refused**, not turned into
  `___`. `/` and `\x00` would both become `_`, colliding with each other and
  telling an operator nothing. Refusing lets the caller fall back to something
  meaningful, such as the info hash.

Each component is sanitised **individually** before joining. Building the whole
path and sanitising afterwards is the version of this that has a traversal bug
in it.

## Hardlink, not move

An import **hardlinks** the completed download into the library; it does not
move it. If a hardlink is impossible it **copies**, and says so loudly. It never
moves by default.

The reasoning is the same as the seeding policy in ADR-0014: a move breaks
seeding, because the torrent client loses the file it is serving, and on a
private tracker that is how an account is lost. A hardlink gives the library its
own name for the same bytes at no extra disk cost.

**Hardlink viability is checked when a root folder is added, by trying it.** The
obvious alternative — comparing `st_dev` — is wrong often enough to matter: bind
mounts, overlayfs and btrfs subvolumes all produce surprises in both directions.
The result is recorded rather than enforced, because a root on a different
filesystem is perfectly usable; it just costs twice the disk. That is the
operator's decision, and the point of checking at configuration time is that
they get to make it knowingly:

```
/dev/shm/cms-library   hardlinks=False
  "hardlinks are not possible from the download directory to here, so imports
   will COPY, using twice the disk space: invalid cross-device link"
```

### Root folders may not nest, or overlap the download directory

- **Nesting**: if root A contains root B, "which root owns this file" has two
  answers, and a delete authorised against A reaches into B.
- **Overlapping the downloads**: the seeded torrent and the library copy would
  be the same path, so removing one destroys the other, and an import would
  hardlink a file onto itself.

Both are refused at configuration time with a reason, because "that path was
refused" without a reason is how an operator ends up disabling a check.

Ancestry is compared component-wise, not with a string prefix: `/media/movies`
is not an ancestor of `/media/movies-4k`, and a check that gets that wrong
refuses a perfectly ordinary layout.

### Removing a root folder never deletes files

Removing a root from the configuration and deleting a library are wildly
different intentions, and the destructive one must never be a side effect of the
administrative one. The API response says so in words.

## Consequences

**Accepted.**

- `os.Root` requires Go 1.24+. Already satisfied (Go 1.26, ADR-0002).
- `Vault.Link` is the one operation not purely kernel-contained, because
  `os.Root.Link` requires both names inside the root and the source is the
  completed download, outside by design. It is handled by creating the parent
  through the root, linking, then **verifying the result back through the root**
  and removing it if it is not the expected inode. That detects rather than
  prevents a race in which a directory component is swapped for a symlink — and
  reaching even that requires write access to the library already. This is
  documented on the method rather than left to be discovered.
- `os.Root` does not prohibit traversal of bind mounts or access to `/proc`
  special files. Irrelevant here: a root folder is an operator-chosen media
  directory, and the nesting checks stop it being somewhere absurd.
- Free space uses `Bavail`, not `Bfree` — `Bfree` counts blocks reserved for
  root, which this process is not and must never be (§2).

**Not solved here.** Nothing imports. There is no media database, no
identification, and nothing that moves a finished download into a root folder.
This ADR is the ground that work stands on, built first and on purpose — exactly
as the egress guard was built before the download engine.

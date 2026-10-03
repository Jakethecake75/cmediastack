-- Library root folders.
--
-- A root folder is where the library physically lives, and it is the boundary
-- every destructive operation is checked against. `internal/library` opens each
-- one as an os.Root — a kernel-enforced containment handle — so a path derived
-- from anything a stranger chose (a torrent's declared name, a release title, a
-- metadata provider's answer) cannot reach outside it. Not by "..", not by an
-- absolute path, and not by a symlink planted inside the tree, which is the
-- vector filepath.Clean does not catch and EvalSymlinks races on.
--
-- Roots MUST NOT nest. If root A contains root B then "which root owns this
-- file" has two answers, and a delete authorised against A reaches into B. The
-- store refuses a nested root at creation rather than leaving that ambiguity to
-- be discovered by a deletion.
CREATE TABLE root_folder (
    id        INTEGER PRIMARY KEY,

    -- The absolute, symlink-resolved path. Resolved at creation so that two
    -- rows cannot describe the same directory by different names, which would
    -- defeat the nesting check.
    path      TEXT    NOT NULL UNIQUE,

    -- movies | series | music | books
    --
    -- What lives here decides how a file inside is named and organised. Text
    -- rather than an integer, for the same reason status is: a column read by a
    -- person during an incident should not need a lookup table in their head.
    kind      TEXT    NOT NULL,

    -- What the operator calls it. Distinct from the path: "4K Films" is more
    -- use in a dropdown than /mnt/tank/media/movies-uhd.
    label     TEXT    NOT NULL DEFAULT '',

    -- Whether a hardlink from the download directory into this root was
    -- possible when it was added.
    --
    -- Recorded because the alternative is finding out at 3am. A hardlink needs
    -- the same filesystem; when it is unavailable an import must fall back to
    -- copying, which doubles the bytes on disk and is a surprise nobody wants
    -- to receive as a full-disk alert.
    hardlinks_ok   INTEGER NOT NULL DEFAULT 0,
    hardlink_note  TEXT    NOT NULL DEFAULT '',

    -- Free space at the last check, for display. Deliberately not a constraint:
    -- refusing to add a root because it is full today would be wrong, and this
    -- number is stale the moment it is written.
    free_bytes     INTEGER NOT NULL DEFAULT 0,
    checked_at     TEXT,

    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,

    CHECK (kind IN ('movies', 'series', 'music', 'books')),
    -- Relative paths cannot be contained, because there is nothing to contain
    -- them against. Enforced in Go as well; here so the database cannot hold a
    -- row that the containment layer would refuse to open.
    CHECK (path LIKE '/%' OR path LIKE '_:\%' ESCAPE '\')
);

CREATE INDEX idx_root_folder_kind ON root_folder(kind);

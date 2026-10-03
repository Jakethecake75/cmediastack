-- The library itself: what this instance holds, and where each file is.
--
-- # What is deliberately NOT here
--
-- There is no episode table listing episodes the instance does NOT have. That
-- table is what drives "wanted", a calendar, and season-completion percentages,
-- and it cannot be populated without a metadata provider telling us which
-- episodes exist. Inventing rows from the files we happen to hold would produce
-- a season that is always 100% complete, which is worse than having no answer.
-- It arrives with the metadata increment.
--
-- There is no season table either, for now. A season is
-- `SELECT DISTINCT season FROM media_file WHERE item_id = ?`. It earns a table
-- when it has attributes of its own — a poster, an overview, a monitored flag —
-- and all three come from metadata.

-- A thing a person browses to: one film, or one series.
CREATE TABLE media_item (
    id         INTEGER PRIMARY KEY,

    -- movie | series
    kind       TEXT    NOT NULL,

    -- The title as this software understands it. Until a metadata provider is
    -- wired, that means the title the release-name parser extracted, which is
    -- why an operator can edit it: the parser is good, not omniscient.
    title      TEXT    NOT NULL,
    year       INTEGER,

    -- sort_title is the title with a leading article moved, lowercased, so
    -- "The Matrix" files under M. Stored rather than computed per query: the
    -- rule is language-dependent and an operator may want to override one.
    sort_title TEXT    NOT NULL,

    -- Where this item's files live. The folder name inside the root, NOT an
    -- absolute path: an operator who moves a library by remounting it should
    -- not have to rewrite every row.
    root_folder_id INTEGER NOT NULL REFERENCES root_folder(id),
    folder         TEXT    NOT NULL,

    -- External identifiers, all nullable. Nothing populates these yet; they
    -- exist so the metadata increment does not need a migration that rewrites
    -- every row of a library that has grown in the meantime.
    tmdb_id    INTEGER,
    tvdb_id    INTEGER,
    imdb_id    TEXT,

    added_at   TEXT    NOT NULL,
    updated_at TEXT    NOT NULL,

    CHECK (kind IN ('movie', 'series')),

    -- One item per folder per root. Two items claiming the same directory would
    -- each believe they own what is inside it, and a delete on one would take
    -- the other's files.
    UNIQUE (root_folder_id, folder)
);

CREATE INDEX idx_media_item_sort ON media_item(kind, sort_title);
CREATE INDEX idx_media_item_root ON media_item(root_folder_id);

-- One file on disk that this instance considers part of the library.
CREATE TABLE media_file (
    id        INTEGER PRIMARY KEY,
    item_id   INTEGER NOT NULL REFERENCES media_item(id) ON DELETE CASCADE,

    -- NULL for a film. For television, which episode this file holds. A file
    -- covering several episodes (a double-length pilot released as one file)
    -- records the first; episode_last carries the range.
    season       INTEGER,
    episode      INTEGER,
    episode_last INTEGER,

    -- Where the file is: a root folder and a path relative to it. Relative for
    -- the same reason media_item.folder is.
    root_folder_id INTEGER NOT NULL REFERENCES root_folder(id),
    relative_path  TEXT    NOT NULL,

    size_bytes INTEGER NOT NULL DEFAULT 0,

    -- What was imported, as the quality ladder in internal/release names it.
    -- Stored as text for the same reason quality_profile stores names: the
    -- ladder is defined in code, and a database that disagreed with the code
    -- would be the worse authority.
    quality    TEXT    NOT NULL DEFAULT '',
    revision   INTEGER NOT NULL DEFAULT 0,

    -- Provenance. "Where did this file come from" is the question an operator
    -- asks when something is wrong with it, and the answer must survive the
    -- download being removed from the queue.
    release_title TEXT NOT NULL DEFAULT '',
    release_group TEXT NOT NULL DEFAULT '',
    info_hash     TEXT,

    -- Whether the bytes are shared with a still-seeding download. An operator
    -- deleting a file needs to know that the same inode is being served to
    -- strangers, and that unlinking here does not stop that.
    hardlinked INTEGER NOT NULL DEFAULT 0,

    imported_at TEXT NOT NULL,

    -- Two records cannot claim the same file. Without this, a re-import that
    -- picked the same destination would leave two rows, and deleting one would
    -- leave the other pointing at nothing.
    UNIQUE (root_folder_id, relative_path)
);

CREATE INDEX idx_media_file_item ON media_file(item_id, season, episode);
CREATE INDEX idx_media_file_hash ON media_file(info_hash);

-- What happened to a completed download, whether or not it worked.
--
-- A failed import is the thing an operator most needs to see and the thing this
-- category of software is worst at showing. "It downloaded and then nothing
-- happened" is the complaint; this table is the answer.
CREATE TABLE import_record (
    id          INTEGER PRIMARY KEY,
    info_hash   TEXT    NOT NULL,

    -- imported | skipped | failed
    outcome     TEXT    NOT NULL,
    -- Why, in words an operator can act on.
    detail      TEXT    NOT NULL DEFAULT '',

    -- The file that was chosen from the download, relative to the download
    -- directory, and where it ended up. Both empty when nothing was imported.
    source_path TEXT    NOT NULL DEFAULT '',
    media_file_id INTEGER REFERENCES media_file(id) ON DELETE SET NULL,

    occurred_at TEXT    NOT NULL,

    CHECK (outcome IN ('imported', 'skipped', 'failed'))
);

CREATE INDEX idx_import_record_hash ON import_record(info_hash, occurred_at);

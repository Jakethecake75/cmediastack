-- The download queue, persisted.
--
-- Without this table a restart orphans every partial transfer: the bytes stay
-- on disk under the data directory, named by info hash, and nothing left alive
-- knows what they were or that anyone asked for them. That is the worst kind
-- of data loss — silent, invisible, and only noticed when the disk fills.
--
-- What is stored is what is needed to RE-ADD the transfer without contacting
-- anybody. The magnet URI or the .torrent bytes are kept verbatim, so a restart
-- replays from local state: the indexer may be down, rate-limiting, or gone,
-- and none of that should cost an operator their queue.
--
-- What is deliberately NOT stored is the download URL. On a great many trackers
-- it carries the indexer's API key in its query string, and a row that is read
-- by every queue listing is the wrong place to keep a credential. The bytes it
-- would have fetched are already here, so the URL has no remaining use.
CREATE TABLE download_queue (
    info_hash   TEXT    PRIMARY KEY,

    -- The release name as the INDEXER published it, which is what the operator
    -- searched for and recognises. The torrent's own declared name is chosen by
    -- the uploader and is often neither.
    title       TEXT    NOT NULL,

    -- Which indexer it came from, for display and for an operator who needs to
    -- know what their instance has been pulling from. Not a foreign key: an
    -- indexer may be deleted, and that must not cascade into deleting the
    -- history of what was taken from it.
    indexer_id   INTEGER,
    indexer_name TEXT    NOT NULL DEFAULT '',

    -- Exactly one of these is set. Both are capped well below SQLite's limits
    -- by the fetch path (indexer.MaxTorrentBytes, indexer.MaxMagnetBytes).
    magnet      TEXT,
    torrent     BLOB,

    -- Who caused this. A grab is the moment the instance acquires something,
    -- and the operator answerable for it needs the queue itself to say who
    -- asked, not only the audit log.
    added_by    INTEGER REFERENCES app_user(id) ON DELETE SET NULL,
    added_label TEXT    NOT NULL DEFAULT '',

    -- queued | downloading | complete | stopped
    --
    -- Text rather than an integer: a status column read by a human during an
    -- incident should not need a lookup table held in someone's head.
    status      TEXT    NOT NULL DEFAULT 'queued',

    added_at    TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL,
    completed_at TEXT,

    -- Seeding policy captured at grab time, from the indexer that supplied it.
    -- Copied rather than joined for the same reason indexer_id is not a foreign
    -- key: the obligation was incurred under the rules in force then, and
    -- editing an indexer must not silently rewrite what was already promised.
    seed_ratio  REAL    NOT NULL DEFAULT 0,
    seed_secs   INTEGER NOT NULL DEFAULT 0,

    CHECK (status IN ('queued', 'downloading', 'complete', 'stopped')),
    -- One payload, not none and not both. A row with neither cannot be
    -- replayed, and a row with both is ambiguous about which was used.
    CHECK ((magnet IS NOT NULL) <> (torrent IS NOT NULL))
);

-- The restore query at startup: everything not finished, oldest first.
CREATE INDEX idx_download_queue_status ON download_queue(status, added_at);

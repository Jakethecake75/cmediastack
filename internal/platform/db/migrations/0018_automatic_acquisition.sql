-- Automatic acquisition (ADR-0030).
--
-- A film can be kept without being wanted. ADR-0026 rejected this switch
-- because nothing would change when it was flipped: there was no automatic
-- search to stop. There is now. Films only — a series is monitored episode by
-- episode and season by season (ADR-0022), and this column means nothing on
-- one. Every existing film stays wanted.
ALTER TABLE media_item ADD COLUMN monitored INTEGER NOT NULL DEFAULT 1
    CHECK (monitored IN (0, 1));

-- What automatic acquisition last did about each wanted item: when it last
-- searched for it, what came of it, and when it is due again. One row per
-- episode or per film, never both; gone with the episode or film.
--
-- It records searches, not grabs: what was grabbed is the download queue's,
-- which is also the blocklist (ADR-0030, decision 3).
CREATE TABLE acquire_state (
    id          INTEGER PRIMARY KEY,
    episode_id  INTEGER REFERENCES episode(id)    ON DELETE CASCADE,
    item_id     INTEGER REFERENCES media_item(id) ON DELETE CASCADE,

    -- When a pass last searched for it, and when it may next.
    searched_at TEXT,
    next_at     TEXT,
    -- Consecutive searches that found nothing grabbable: the back-off doubles
    -- with each, from six hours to a week, and a grab resets it.
    fruitless   INTEGER NOT NULL DEFAULT 0 CHECK (fruitless >= 0),

    -- grabbed | nothing | failed
    outcome     TEXT CHECK (outcome IS NULL OR outcome IN ('grabbed', 'nothing', 'failed')),
    -- In words: what was grabbed, or why nothing could be.
    detail      TEXT NOT NULL DEFAULT '',

    CHECK ((episode_id IS NULL) <> (item_id IS NULL))
);

CREATE UNIQUE INDEX idx_acquire_state_episode ON acquire_state(episode_id) WHERE episode_id IS NOT NULL;
CREATE UNIQUE INDEX idx_acquire_state_item    ON acquire_state(item_id)    WHERE item_id IS NOT NULL;

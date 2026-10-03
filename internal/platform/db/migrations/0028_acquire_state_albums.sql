-- Automatic acquisition keeps what it did about an album too (ADR-0047).
--
-- One row per episode, per film or per album, never two of them. SQLite cannot
-- change the CHECK that said "an episode or a film" in place, so the table is
-- rebuilt: nothing references it, and every row is copied as it was.
CREATE TABLE acquire_state_v28 (
    id          INTEGER PRIMARY KEY,
    episode_id  INTEGER REFERENCES episode(id)    ON DELETE CASCADE,
    item_id     INTEGER REFERENCES media_item(id) ON DELETE CASCADE,
    album_id    INTEGER REFERENCES album(id)      ON DELETE CASCADE,

    searched_at TEXT,
    next_at     TEXT,
    fruitless   INTEGER NOT NULL DEFAULT 0 CHECK (fruitless >= 0),
    outcome     TEXT CHECK (outcome IS NULL OR outcome IN ('grabbed', 'nothing', 'failed')),
    detail      TEXT NOT NULL DEFAULT '',

    CHECK ((episode_id IS NOT NULL) + (item_id IS NOT NULL) + (album_id IS NOT NULL) = 1)
);

INSERT INTO acquire_state_v28 (id, episode_id, item_id, searched_at, next_at, fruitless, outcome, detail)
    SELECT id, episode_id, item_id, searched_at, next_at, fruitless, outcome, detail FROM acquire_state;

DROP TABLE acquire_state;

ALTER TABLE acquire_state_v28 RENAME TO acquire_state;

CREATE UNIQUE INDEX idx_acquire_state_episode ON acquire_state(episode_id) WHERE episode_id IS NOT NULL;
CREATE UNIQUE INDEX idx_acquire_state_item    ON acquire_state(item_id)    WHERE item_id IS NOT NULL;
CREATE UNIQUE INDEX idx_acquire_state_album   ON acquire_state(album_id)   WHERE album_id IS NOT NULL;

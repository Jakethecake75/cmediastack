-- Migration 0012: where each person got to.
--
-- A media server without this is a toy: every session starts at zero, and a
-- film watched over three evenings is three first acts.

-- One row per person per file.
--
-- PER PERSON, and that is the whole shape of the table. Two people watching the
-- same film are in different places, and a position stored against the file
-- alone would have each of them dragging the other back. The primary key says
-- so rather than an application rule saying so.
CREATE TABLE playback_position (
    user_id       INTEGER NOT NULL REFERENCES app_user(id)   ON DELETE CASCADE,
    media_file_id INTEGER NOT NULL REFERENCES media_file(id) ON DELETE CASCADE,

    position_ms INTEGER NOT NULL,

    -- The duration AS IT WAS when the position was recorded.
    --
    -- Kept so a position can be disbelieved. A file can be replaced underneath
    -- the library — a re-encode, an extended cut, a different release entirely
    -- — and 01:12:30 into the old file is not 01:12:30 into the new one. Without
    -- this, a resume would silently drop somebody into the wrong scene and
    -- there would be no way to tell that had happened.
    duration_ms INTEGER NOT NULL,

    -- Whether this counts as watched. A DECISION, recorded, rather than a
    -- comparison recomputed at query time: the rule for "near enough the end"
    -- involves both a percentage and a cap, and a second implementation of it
    -- in SQL would be free to disagree with the first.
    finished INTEGER NOT NULL DEFAULT 0,

    updated_at TEXT NOT NULL,

    PRIMARY KEY (user_id, media_file_id)
);

-- For the retention purge. Playback history is a record of what somebody
-- watched and when, which the threat model lists as an asset in its own right
-- (docs/THREAT-MODEL.md) — it is purged on a schedule rather than kept forever,
-- honouring config's playback_history_retention.
CREATE INDEX idx_playback_position_age ON playback_position(updated_at);

-- For "continue watching": this person's unfinished films, most recent first.
CREATE INDEX idx_playback_position_resume
    ON playback_position(user_id, finished, updated_at DESC);

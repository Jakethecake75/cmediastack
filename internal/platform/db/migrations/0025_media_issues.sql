-- A problem with a title, reported by somebody watching it (ADR-0042).
--
-- One title, one kind of problem, optionally one episode. At most one OPEN
-- issue per problem: a second report of the same is the first, so a household
-- noticing one broken subtitle is one line, not five.
CREATE TABLE media_issue (
    id             INTEGER PRIMARY KEY,
    item_id        INTEGER NOT NULL REFERENCES media_item(id) ON DELETE CASCADE,
    -- video | audio | subtitles | wrong_title | other
    kind           TEXT    NOT NULL,
    -- 0 for a film, or a problem with the whole series.
    season         INTEGER NOT NULL DEFAULT 0,
    episode        INTEGER NOT NULL DEFAULT 0,
    note           TEXT    NOT NULL DEFAULT '',
    reported_by    INTEGER REFERENCES app_user(id) ON DELETE SET NULL,
    reported_at    TEXT    NOT NULL,
    -- open | resolved
    state          TEXT    NOT NULL DEFAULT 'open',
    resolved_by    INTEGER REFERENCES app_user(id) ON DELETE SET NULL,
    resolved_at    TEXT,
    resolution     TEXT    NOT NULL DEFAULT '',

    CHECK (kind IN ('video', 'audio', 'subtitles', 'wrong_title', 'other')),
    CHECK (state IN ('open', 'resolved'))
);

CREATE UNIQUE INDEX idx_media_issue_open
    ON media_issue(item_id, kind, season, episode) WHERE state = 'open';
CREATE INDEX idx_media_issue_state ON media_issue(state, reported_at);

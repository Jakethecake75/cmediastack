-- What the subtitle sweep last did about each file and language (ADR-0056):
-- when it searched, what came of it, and when it may search again. Forgotten
-- with the file.
CREATE TABLE subtitle_search (
    media_file_id INTEGER NOT NULL REFERENCES media_file(id) ON DELETE CASCADE,
    language      TEXT    NOT NULL CHECK (length(language) = 2),
    searched_at   TEXT    NOT NULL,
    next_at       TEXT    NOT NULL,
    -- Consecutive searches that found nothing: the wait doubles with each.
    fruitless     INTEGER NOT NULL DEFAULT 0 CHECK (fruitless >= 0),
    outcome       TEXT    NOT NULL CHECK (outcome IN ('fetched', 'nothing', 'failed')),
    detail        TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (media_file_id, language)
);

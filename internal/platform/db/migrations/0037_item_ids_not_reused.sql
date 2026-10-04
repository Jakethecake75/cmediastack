-- cms: foreign_keys=off
-- A title's id is never used again (ADR-0066).
--
-- With a plain INTEGER PRIMARY KEY, SQLite gives a new row the largest rowid
-- plus one, so deleting the newest title hands its id to the next. A queue row,
-- request or issue that outlived the deleted title then named a different one:
-- found when a season grabbed for a deleted series said it was for a film.
-- AUTOINCREMENT never hands an id out twice. SQLite cannot add it to a table,
-- so the table is rebuilt as 0026 rebuilt it — foreign keys off, every column
-- carried, the migrator checking every reference before it commits.
CREATE TABLE media_item_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    -- movie | series | artist | book
    kind       TEXT    NOT NULL,
    title      TEXT    NOT NULL,
    year       INTEGER,
    sort_title TEXT    NOT NULL,
    root_folder_id INTEGER NOT NULL REFERENCES root_folder(id),
    folder         TEXT    NOT NULL,
    tmdb_id    INTEGER,
    tvdb_id    INTEGER,
    imdb_id    TEXT,
    added_at   TEXT    NOT NULL,
    updated_at TEXT    NOT NULL,
    episodes_refreshed_at TEXT,
    monitored INTEGER NOT NULL DEFAULT 1 CHECK (monitored IN (0, 1)),
    quality_profile_id INTEGER REFERENCES quality_profile(id) ON DELETE SET NULL,
    certification TEXT,
    rating_rank INTEGER CHECK (rating_rank IS NULL OR rating_rank BETWEEN 1 AND 5),
    rating_source TEXT CHECK (rating_source IS NULL OR rating_source IN ('provider', 'person')),
    rating_checked_at TEXT,
    musicbrainz_id TEXT,
    openlibrary_id TEXT,
    author         TEXT,
    follow_new_seasons INTEGER NOT NULL DEFAULT 1 CHECK (follow_new_seasons IN (0, 1)),
    season_folders INTEGER NOT NULL DEFAULT 1 CHECK (season_folders IN (0, 1)),
    daily INTEGER NOT NULL DEFAULT 0 CHECK (daily IN (0, 1)),

    CHECK (kind IN ('movie', 'series', 'artist', 'book')),
    UNIQUE (root_folder_id, folder)
);

INSERT INTO media_item_new (id, kind, title, year, sort_title, root_folder_id, folder,
    tmdb_id, tvdb_id, imdb_id, added_at, updated_at, episodes_refreshed_at, monitored,
    quality_profile_id, certification, rating_rank, rating_source, rating_checked_at,
    musicbrainz_id, openlibrary_id, author, follow_new_seasons, season_folders, daily)
SELECT id, kind, title, year, sort_title, root_folder_id, folder,
    tmdb_id, tvdb_id, imdb_id, added_at, updated_at, episodes_refreshed_at, monitored,
    quality_profile_id, certification, rating_rank, rating_source, rating_checked_at,
    musicbrainz_id, openlibrary_id, author, follow_new_seasons, season_folders, daily
FROM media_item;

DROP TABLE media_item;
ALTER TABLE media_item_new RENAME TO media_item;

CREATE INDEX idx_media_item_root ON media_item(root_folder_id);
CREATE INDEX idx_media_item_sort ON media_item(kind, sort_title);
CREATE INDEX idx_media_item_musicbrainz ON media_item(musicbrainz_id) WHERE musicbrainz_id IS NOT NULL;

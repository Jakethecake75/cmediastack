-- cms: foreign_keys=off
-- Music: artists, albums and tracks (ADR-0044).
--
-- A followed artist is a media_item of the kind 'artist', and a book will be
-- one of the kind 'book', so that scope, search, requests, issues and deletion
-- work on them unchanged. SQLite cannot alter a CHECK constraint, so the table
-- is rebuilt — with foreign keys off, because eight tables reference it and
-- dropping it with them on would cascade-delete their rows. The migrator
-- refuses to commit this unless PRAGMA foreign_key_check then finds nothing.
--
-- Every column is carried as it was. Three are new: musicbrainz_id (an
-- artist's), openlibrary_id (a book's work) and author (a book's, as shown).
CREATE TABLE media_item_new (
    id         INTEGER PRIMARY KEY,
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

    CHECK (kind IN ('movie', 'series', 'artist', 'book')),
    UNIQUE (root_folder_id, folder)
);

INSERT INTO media_item_new (id, kind, title, year, sort_title, root_folder_id, folder,
    tmdb_id, tvdb_id, imdb_id, added_at, updated_at, episodes_refreshed_at, monitored,
    quality_profile_id, certification, rating_rank, rating_source, rating_checked_at)
SELECT id, kind, title, year, sort_title, root_folder_id, folder,
    tmdb_id, tvdb_id, imdb_id, added_at, updated_at, episodes_refreshed_at, monitored,
    quality_profile_id, certification, rating_rank, rating_source, rating_checked_at
FROM media_item;

DROP TABLE media_item;
ALTER TABLE media_item_new RENAME TO media_item;

CREATE INDEX idx_media_item_root ON media_item(root_folder_id);
CREATE INDEX idx_media_item_sort ON media_item(kind, sort_title);
CREATE INDEX idx_media_item_musicbrainz ON media_item(musicbrainz_id) WHERE musicbrainz_id IS NOT NULL;

-- An album is a MusicBrainz release group of an artist: an Album or an EP.
-- Its track list is that of one official release, the earliest, fetched when
-- it is first needed (tracks_refreshed_at NULL until then).
CREATE TABLE album (
    id              INTEGER PRIMARY KEY,
    item_id         INTEGER NOT NULL REFERENCES media_item(id) ON DELETE CASCADE,
    musicbrainz_id  TEXT    NOT NULL,
    release_id      TEXT,
    title           TEXT    NOT NULL,
    album_type      TEXT    NOT NULL,
    -- The first release date, as MusicBrainz gives it: a date, a month or a
    -- year. NULL is unknown, which is an announced album, not a released one.
    released_at     TEXT,
    monitored       INTEGER NOT NULL DEFAULT 1 CHECK (monitored IN (0, 1)),
    tracks_refreshed_at TEXT,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL,

    CHECK (album_type IN ('album', 'ep')),
    UNIQUE (item_id, musicbrainz_id)
);
CREATE INDEX idx_album_item ON album(item_id);

-- A track of an album's chosen release. file_id is the library file that holds
-- it, once one does.
CREATE TABLE track (
    id              INTEGER PRIMARY KEY,
    album_id        INTEGER NOT NULL REFERENCES album(id) ON DELETE CASCADE,
    disc            INTEGER NOT NULL,
    number          INTEGER NOT NULL,
    title           TEXT    NOT NULL,
    length_ms       INTEGER,
    musicbrainz_id  TEXT,
    file_id         INTEGER REFERENCES media_file(id) ON DELETE SET NULL,

    UNIQUE (album_id, disc, number)
);
CREATE INDEX idx_track_file ON track(file_id) WHERE file_id IS NOT NULL;

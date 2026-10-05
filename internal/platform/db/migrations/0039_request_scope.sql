-- A request names the provider's title, may ask for part of a series, and may
-- ask for a title to be removed (ADR-0075).
--
-- tmdb_id: the title the requester chose from the provider's answer, so an
-- approval can add it to the library without anybody searching again. NULL for
-- a request made in words, and for every request made before this.
ALTER TABLE media_request ADD COLUMN tmdb_id INTEGER;

-- scope: the seasons and episodes asked for, canonical ("S1,S2E5"); empty for
-- the whole title. Part of the match key, so two people asking for different
-- seasons hold two requests.
ALTER TABLE media_request ADD COLUMN scope TEXT NOT NULL DEFAULT '';

-- action: add (fetch it) or remove (delete its files, to the trash).
ALTER TABLE media_request ADD COLUMN action TEXT NOT NULL DEFAULT 'add'
    CHECK (action IN ('add', 'remove'));

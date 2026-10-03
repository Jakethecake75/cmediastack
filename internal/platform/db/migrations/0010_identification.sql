-- What this instance believes a library item actually is, and how it came to
-- believe it.
--
-- # Why this is a table and not three columns on media_item
--
-- media_item already carries tmdb_id, tvdb_id and imdb_id — the ANSWER. What it
-- cannot carry is the reasoning: which candidates were considered, why one was
-- chosen, whether a person chose it, and what the title was before. All of that
-- is what makes an identification reviewable and reversible, and none of it
-- belongs on a row that every library listing reads.
--
-- # Why candidates are stored rather than re-searched
--
-- Identification runs as a background pass over a whole library; review happens
-- later, by a person, item by item. Re-searching at review time would mean a
-- provider request per item glanced at — and worse, the thing confirmed might
-- not be the thing proposed, because the provider's results moved in between.
-- A person must confirm what they were shown.

CREATE TABLE media_identification (
    item_id INTEGER PRIMARY KEY REFERENCES media_item(id) ON DELETE CASCADE,

    -- unidentified | proposed | confirmed | none
    --
    -- "none" is distinct from "unidentified": it means a search ran and found
    -- nothing worth showing. Collapsing them would make every pass re-search
    -- the items it already knows it cannot help with.
    state TEXT NOT NULL,

    -- Which provider the ids below belong to. Stored rather than assumed:
    -- an id is meaningless without it, and TVDB is a foreseeable second.
    provider    TEXT    NOT NULL DEFAULT '',
    provider_id INTEGER,

    -- What the release-name parser thought before any of this. Preserved so a
    -- confirmation is REVERSIBLE: undoing an identification has to put back a
    -- real previous value rather than re-parsing a name that may no longer be
    -- on disk.
    parsed_title TEXT    NOT NULL,
    parsed_year  INTEGER,

    -- Who decided, and NULL means the software did. That distinction is the
    -- load-bearing one in this table: an automatic pass must never overwrite a
    -- person's decision, and this column is how it knows.
    decided_by INTEGER REFERENCES app_user(id) ON DELETE SET NULL,
    decided_at TEXT,

    -- The verdict in words, as internal/identify produced it. Kept because a
    -- person reviewing a proposal a week later needs the reasoning, and
    -- re-deriving it would need the candidates AND the code to be unchanged.
    verdict     TEXT NOT NULL DEFAULT '',
    verdict_why TEXT NOT NULL DEFAULT '',

    -- When the candidates below were gathered, so a stale proposal is visible
    -- as stale rather than as current.
    searched_at TEXT,
    updated_at  TEXT NOT NULL,

    CHECK (state IN ('unidentified', 'proposed', 'confirmed', 'none'))
);

CREATE INDEX idx_media_identification_state ON media_identification(state);

-- The candidates a person chooses between.
--
-- Deliberately a copy rather than a reference to anything: this is what was
-- SHOWN, and it must not change underneath the person deciding. A poster path
-- that moved or a title the provider edited would otherwise silently rewrite
-- the question.
CREATE TABLE media_identification_candidate (
    item_id     INTEGER NOT NULL REFERENCES media_item(id) ON DELETE CASCADE,
    provider    TEXT    NOT NULL,
    provider_id INTEGER NOT NULL,

    -- Display order as internal/identify ranked it, so the list a person sees
    -- is the list that was produced — including the popularity ordering, which
    -- is not recoverable from the score alone.
    rank INTEGER NOT NULL,

    title          TEXT    NOT NULL,
    original_title TEXT    NOT NULL DEFAULT '',
    year           INTEGER,
    overview       TEXT    NOT NULL DEFAULT '',
    poster_path    TEXT    NOT NULL DEFAULT '',

    -- The evidence score, NOT including the popularity nudge (ADR-0019): the
    -- nudge orders, and `rank` is where its effect lives.
    score REAL NOT NULL DEFAULT 0,
    -- Per-candidate reasoning in words, for the person deciding.
    why TEXT NOT NULL DEFAULT '',

    PRIMARY KEY (item_id, provider, provider_id)
);

CREATE INDEX idx_media_ident_candidate_rank ON media_identification_candidate(item_id, rank);

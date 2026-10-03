-- Seasons and episodes: what a series HAS, and what it is missing.
--
-- # Why these tables exist now and did not before
--
-- 0008_media.sql refused to create them, and named the condition:
--
--   "it cannot be populated without a metadata provider telling us which
--    episodes exist. Inventing rows from the files we happen to hold would
--    produce a season that is always 100% complete, which is worse than having
--    no answer."
--
-- Phase 3 wired a provider. This is that increment (ADR-0022).
--
-- The rule these tables are built around: a row here exists because a PROVIDER
-- says the episode exists. Nothing creates one from a filename, a release name,
-- or a file on disk. A season assembled from what is on disk is complete by
-- construction, and a "missing" list built the same way is always empty.

-- One season of a series.
--
-- 0008 said a season "earns a table when it has attributes of its own — a
-- poster, an overview, a monitored flag — and all three come from metadata".
-- The deciding one is monitored: an episode that has not aired yet has no row
-- to inherit from, so the operator's intent has to live somewhere that exists
-- before the episode does.
CREATE TABLE season (
    id      INTEGER PRIMARY KEY,
    item_id INTEGER NOT NULL REFERENCES media_item(id) ON DELETE CASCADE,

    -- Season 0 is Specials, and is kept: operators' files are frequently in it
    -- and dropping it would make those files permanently unmatchable.
    number   INTEGER NOT NULL,
    name     TEXT    NOT NULL DEFAULT '',
    overview TEXT    NOT NULL DEFAULT '',

    -- What the provider says this season contains. Stored so that a refresh can
    -- tell "this season gained an episode" from "this season is unchanged"
    -- without fetching it — one HTTP call per season, and most seasons never
    -- change again.
    episode_count INTEGER NOT NULL DEFAULT 0,

    -- NULL when the provider has no date, which is normal for an announced
    -- season that has not started.
    aired_at TEXT,

    -- Specials default to unmonitored: "everything ever released, including
    -- recap episodes and convention panels" is not what somebody means by
    -- following a show. Every other season defaults to monitored, which the
    -- writer sets rather than a DEFAULT here, because the rule depends on the
    -- season number.
    monitored INTEGER NOT NULL DEFAULT 1,

    updated_at TEXT NOT NULL,

    CHECK (number >= 0),
    CHECK (monitored IN (0, 1)),
    UNIQUE (item_id, number)
);

-- One episode, as the provider knows it — whether or not this instance has it.
--
-- season_number is carried here as well as on the season row. It is
-- denormalised deliberately: every query that matters joins episodes to
-- media_file on (item_id, season, episode range), and forcing that through the
-- season table adds a join to the one query — "what is missing across the whole
-- library" — that this table exists to serve.
CREATE TABLE episode (
    id        INTEGER PRIMARY KEY,
    item_id   INTEGER NOT NULL REFERENCES media_item(id) ON DELETE CASCADE,
    season_id INTEGER NOT NULL REFERENCES season(id) ON DELETE CASCADE,

    season_number INTEGER NOT NULL,
    number        INTEGER NOT NULL,

    title    TEXT NOT NULL DEFAULT '',
    overview TEXT NOT NULL DEFAULT '',

    -- NULL when the provider lists the episode with no date. That is an
    -- ANNOUNCED episode, and it is never "wanted": treating a date we do not
    -- have as "already aired" would put every unannounced episode of every
    -- running show on the wanted list on day one.
    aired_at TEXT,

    runtime_minutes INTEGER NOT NULL DEFAULT 0,

    -- The provider's own episode id, kept so a renumbering can be recognised
    -- rather than guessed at.
    provider_episode_id INTEGER,

    monitored INTEGER NOT NULL DEFAULT 1,

    updated_at TEXT NOT NULL,

    CHECK (number >= 0),
    CHECK (season_number >= 0),
    CHECK (monitored IN (0, 1)),
    UNIQUE (item_id, season_number, number)
);

-- The wanted query reads episodes by item and by air date, and the
-- whole-library form reads by air date alone.
CREATE INDEX idx_episode_item ON episode(item_id, season_number, number);
CREATE INDEX idx_episode_aired ON episode(aired_at) WHERE monitored = 1;

-- When a series was last asked about, so the refresh task can skip series whose
-- answer cannot have changed. On media_item rather than in a table of its own:
-- it is one nullable timestamp about an item, not an entity.
ALTER TABLE media_item ADD COLUMN episodes_refreshed_at TEXT;

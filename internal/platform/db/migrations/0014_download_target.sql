-- What a download was grabbed FOR (ADR-0023).
--
-- Set when a release is grabbed from an episode search: the series item, the
-- season and the episode the server matched it to. The importer files the
-- download under exactly that series — its root, its folder — instead of
-- working a series out from the release name, which is how a release spelled
-- "Severance.2022" used to create a second Severance beside the one being
-- followed. A download grabbed from the general search names nothing, and all
-- three stay NULL.
--
-- Not a foreign key, deliberately, for the reason indexer_id is not one. If the
-- series is deleted while the episode downloads, a cascade or SET NULL would
-- turn "grabbed for a series that no longer exists" into "grabbed for nothing",
-- and the import would then re-create the series from the release name — the
-- one outcome the operator's deletion ruled out. Kept, the importer finds the
-- item gone and refuses, with that reason.
--
-- All three are set together or not at all. SQLite cannot add a table CHECK to
-- an existing table, so download.Store.Put enforces it.
ALTER TABLE download_queue ADD COLUMN target_item_id INTEGER
    CHECK (target_item_id IS NULL OR target_item_id > 0);
ALTER TABLE download_queue ADD COLUMN target_season INTEGER
    CHECK (target_season IS NULL OR target_season >= 0);
ALTER TABLE download_queue ADD COLUMN target_episode INTEGER
    CHECK (target_episode IS NULL OR target_episode > 0);

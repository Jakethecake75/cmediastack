-- A download can be for several seasons of a series (ADR-0057).
--
--   target_kind  target_item_id  target_season  target_last_season
--   'season'     set (series)    first season   last season, or NULL for one
--
-- download.Store.Put enforces that only a season target has one. The CHECK
-- names target_season and not target_kind: the kind's CHECK is widened by
-- dropping its column (0019, 0027, 0029), which a CHECK naming it would block.
ALTER TABLE download_queue ADD COLUMN target_last_season INTEGER
    CHECK (target_last_season IS NULL
           OR (target_season IS NOT NULL AND target_last_season > target_season));

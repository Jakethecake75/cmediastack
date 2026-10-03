-- A download can be for a whole season (ADR-0033).
--
-- The queue's target gains a third kind beside 'episode' and 'film':
--
--   target_kind  target_item_id  target_season  target_episode
--   'season'     set             set            NULL             one season of a series
--
-- download.Store.Put enforces the shapes, as for the other two. The column's
-- CHECK allowed only 'episode' and 'film', and SQLite cannot change a CHECK in
-- place, so the column is rebuilt: a new one with the wider CHECK, every value
-- copied, the old one dropped and the new one given its name. Nothing indexes
-- or references the column, which is what lets it be dropped.
ALTER TABLE download_queue ADD COLUMN target_kind_v19 TEXT
    CHECK (target_kind_v19 IS NULL OR target_kind_v19 IN ('episode', 'film', 'season'));

UPDATE download_queue SET target_kind_v19 = target_kind;

ALTER TABLE download_queue DROP COLUMN target_kind;

ALTER TABLE download_queue RENAME COLUMN target_kind_v19 TO target_kind;

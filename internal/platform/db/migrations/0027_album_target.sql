-- A download can be for an album (ADR-0046).
--
-- The queue's target gains a fourth kind:
--
--   target_kind  target_item_id  target_season  target_episode  target_album_id
--   'album'      set (artist)    NULL           NULL            set
--
-- download.Store.Put enforces the shapes. The CHECK on target_kind cannot be
-- changed in place, so the column is rebuilt as migration 0019 rebuilt it.
ALTER TABLE download_queue ADD COLUMN target_kind_v27 TEXT
    CHECK (target_kind_v27 IS NULL OR target_kind_v27 IN ('episode', 'film', 'season', 'album'));

UPDATE download_queue SET target_kind_v27 = target_kind;

ALTER TABLE download_queue DROP COLUMN target_kind;

ALTER TABLE download_queue RENAME COLUMN target_kind_v27 TO target_kind;

ALTER TABLE download_queue ADD COLUMN target_album_id INTEGER
    CHECK (target_album_id IS NULL OR target_album_id > 0);

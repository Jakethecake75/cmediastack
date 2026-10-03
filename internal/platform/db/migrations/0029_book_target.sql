-- A download can be for a book (ADR-0049).
--
--   target_kind  target_item_id  target_season  target_episode  target_album_id
--   'book'       set (the book)  NULL           NULL            NULL
--
-- download.Store.Put enforces the shapes. The CHECK is widened as migrations
-- 0019 and 0027 widened it: a new column, every value copied, the old dropped.
ALTER TABLE download_queue ADD COLUMN target_kind_v29 TEXT
    CHECK (target_kind_v29 IS NULL OR target_kind_v29 IN ('episode', 'film', 'season', 'album', 'book'));

UPDATE download_queue SET target_kind_v29 = target_kind;

ALTER TABLE download_queue DROP COLUMN target_kind;

ALTER TABLE download_queue RENAME COLUMN target_kind_v29 TO target_kind;

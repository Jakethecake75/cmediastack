-- WHAT KIND of thing a download was grabbed for (ADR-0026).
--
-- Migration 0014 gave the queue a target — a series item, a season and an
-- episode — for a release grabbed from an episode search. A release grabbed
-- from a FILM search is for a film: its item, and no season or episode. This
-- column says which of the two a row is, rather than leaving the reader to
-- infer a film from which columns happen to be NULL: "an item and nothing else"
-- would read as half an episode to anyone who had not been told the convention,
-- and the next kind of target would have to be squeezed into the same NULLs.
--
-- The shapes, which download.Store.Put enforces (SQLite cannot add a table
-- CHECK to an existing table, and a column CHECK sees only its own column):
--
--   target_kind  target_item_id  target_season  target_episode
--   NULL         NULL            NULL           NULL             grabbed for nothing in particular
--   'episode'    set             set            set              one episode of a series
--   'film'       set             NULL           NULL             one film
--
-- Every row already carrying a target was grabbed from an episode search, the
-- only kind that existed, and is marked so here.
ALTER TABLE download_queue ADD COLUMN target_kind TEXT
    CHECK (target_kind IS NULL OR target_kind IN ('episode', 'film'));

UPDATE download_queue SET target_kind = 'episode' WHERE target_item_id IS NOT NULL;

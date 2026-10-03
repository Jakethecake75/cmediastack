-- Cumulative seeding progress.
--
-- The torrent client's upload counter lives in memory and resets when the
-- process restarts. Judging a seeding obligation against it alone means every
-- restart silently forgives what was already uploaded, so a long-lived instance
-- can seed indefinitely and still report a ratio near zero — or, worse, an
-- operator raises the bar, restarts, and the obligation is quietly reset below
-- what a private tracker already recorded.
--
-- These columns are the durable half. The scheduler adds the in-process delta
-- to them on each tick, so the totals survive restarts and are what the seeding
-- policy is judged against.
ALTER TABLE download_queue ADD COLUMN uploaded_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE download_queue ADD COLUMN seeded_secs    INTEGER NOT NULL DEFAULT 0;

-- The in-process counter as of the last tick, so a delta can be computed
-- without double-counting. It is reset to zero when the process restarts and
-- the transfer is re-added, which the sync detects by the counter going
-- BACKWARDS — the only reliable signal available, since the engine cannot know
-- it is a different process than the one that wrote the row.
ALTER TABLE download_queue ADD COLUMN uploaded_mark  INTEGER NOT NULL DEFAULT 0;

-- When seeding is finished the row says so, and says why. "It stopped" and "it
-- stopped because the ratio was met" are different facts to an operator whose
-- tracker account depends on the second one.
ALTER TABLE download_queue ADD COLUMN seeding_done_reason TEXT NOT NULL DEFAULT '';

-- Stalled downloads (ADR-0034).
--
-- What a transfer had verified when it last moved, and when that was: kept on
-- the row, not in memory, so "when did this last move?" survives a restart and
-- can be shown. progressed_at is NULL until the first progress is seen; the
-- judgement then counts from added_at.
ALTER TABLE download_queue ADD COLUMN progress_bytes INTEGER NOT NULL DEFAULT 0
    CHECK (progress_bytes >= 0);
ALTER TABLE download_queue ADD COLUMN progressed_at TEXT;

-- When it was found stalled: set once, when first found, and cleared if it
-- moves again. A download automatic acquisition grabbed is stopped at the same
-- moment; a person's keeps running with the mark on it.
ALTER TABLE download_queue ADD COLUMN stalled_at TEXT;

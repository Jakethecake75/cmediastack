-- An episode's picture, for a series' page (ADR-0078).
--
-- still_path: the provider's filename for the episode's still, empty when it
-- has none. A filename and nothing else; internal/artwork refuses anything
-- more before it fetches.
ALTER TABLE episode ADD COLUMN still_path TEXT NOT NULL DEFAULT '';

-- stills_read: whether this season's episodes were read since stills were
-- stored. A settled season is not asked about again (ADR-0022), so without
-- this the seasons already here would never gain their pictures. One more
-- request each, once.
ALTER TABLE season ADD COLUMN stills_read INTEGER NOT NULL DEFAULT 0
    CHECK (stills_read IN (0, 1));

-- Every series is due at the next scheduled refresh, rather than within a week.
UPDATE media_item SET episodes_refreshed_at = NULL WHERE kind = 'series';

-- Whether a series takes on the seasons its provider lists for the first time
-- (ADR-0061). On unless the operator switches it off.
ALTER TABLE media_item ADD COLUMN follow_new_seasons INTEGER NOT NULL DEFAULT 1
    CHECK (follow_new_seasons IN (0, 1));

-- Whether a series' episodes are filed in season folders (ADR-0063). On
-- unless the operator switches it off; it moves no file already placed.
ALTER TABLE media_item ADD COLUMN season_folders INTEGER NOT NULL DEFAULT 1
    CHECK (season_folders IN (0, 1));

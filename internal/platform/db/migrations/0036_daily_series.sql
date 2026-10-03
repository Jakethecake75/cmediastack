-- Whether a series is released by date and so searched by date (ADR-0064).
-- Off unless the operator switches it on.
ALTER TABLE media_item ADD COLUMN daily INTEGER NOT NULL DEFAULT 0 CHECK (daily IN (0, 1));

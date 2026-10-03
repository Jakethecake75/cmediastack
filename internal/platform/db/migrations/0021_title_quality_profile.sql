-- A quality profile per title (ADR-0035).
--
-- A series or a film may name the profile its searches, and automatic
-- acquisition, judge it by. NULL — every title that exists, and every new one —
-- is the instance's default profile (ADR-0027). Deleting a profile a title
-- names returns the title to the default rather than refusing the deletion or
-- deleting the title.
ALTER TABLE media_item ADD COLUMN quality_profile_id INTEGER
    REFERENCES quality_profile(id) ON DELETE SET NULL;

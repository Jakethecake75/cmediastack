-- The quality profile that judges an interactive search when none is chosen
-- (ADR-0027).
--
-- A flag on the profile's row rather than a setting elsewhere: the default is
-- one of the profiles, and a setting that named a profile by id could go on
-- naming one that no longer exists. The partial unique index is the rule that
-- there is at most ONE default — enforced by the database, whatever the code
-- does. None at all is allowed, and means every search is unjudged unless a
-- profile is chosen.
ALTER TABLE quality_profile ADD COLUMN is_default INTEGER NOT NULL DEFAULT 0
    CHECK (is_default IN (0, 1));

CREATE UNIQUE INDEX idx_quality_profile_one_default
    ON quality_profile(is_default) WHERE is_default = 1;

-- An instance created before this migration gets the default a new one gets:
-- the built-in HD-1080p, if it still exists. A new instance has no profiles
-- yet at this point; release.ProfileStore.EnsureDefaults marks the default
-- when it seeds them.
UPDATE quality_profile SET is_default = 1
 WHERE id = (SELECT id FROM quality_profile
              WHERE builtin = 1 AND name = 'HD-1080p' ORDER BY id LIMIT 1);

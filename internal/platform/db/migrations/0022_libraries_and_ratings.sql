-- Libraries are root folders, and a rating ceiling hides what is above it
-- (ADR-0037).
--
-- The grants an approver has always been able to send named rows of a
-- `library` table that never held one. A library is now a root folder, so the
-- grant names a root folder and the empty table goes.
DROP TABLE library_grant;
DROP TABLE library;

-- The root folders a restricted account may see. Forgetting a root forgets its
-- grants: an account restricted to roots that are all gone sees nothing, which
-- is the failure that is safe.
CREATE TABLE root_folder_grant (
    user_id        INTEGER NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    root_folder_id INTEGER NOT NULL REFERENCES root_folder(id) ON DELETE CASCADE,
    granted_at     TEXT    NOT NULL,
    PRIMARY KEY (user_id, root_folder_id)
);

-- Whether an account sees every library, whatever is in root_folder_grant. 1
-- for every account that exists, because every account has in fact seen the
-- whole library until now, and a migration that took it away from everybody
-- would be a surprise. It becomes 0 only when a person chooses a list.
ALTER TABLE app_user ADD COLUMN all_libraries INTEGER NOT NULL DEFAULT 1;

-- The same choice, made when an invite is issued. library_ids_json now holds
-- root folder ids.
ALTER TABLE invite ADD COLUMN all_libraries INTEGER NOT NULL DEFAULT 1;

-- A title's rating: the US certification ("PG-13", "TV-MA") and its rank, 1
-- (G, TV-Y, TV-G) to 5 (NC-17). NULL is unrated, and an unrated title is hidden
-- from every account with a ceiling.
--
-- rating_source is 'provider' or 'person'. The ratings task never overwrites a
-- person's. rating_checked_at is when the provider was last asked, so an
-- unrated title is asked again after a while and a rated one is not asked
-- every hour.
ALTER TABLE media_item ADD COLUMN certification TEXT;
ALTER TABLE media_item ADD COLUMN rating_rank INTEGER
    CHECK (rating_rank IS NULL OR rating_rank BETWEEN 1 AND 5);
ALTER TABLE media_item ADD COLUMN rating_source TEXT
    CHECK (rating_source IS NULL OR rating_source IN ('provider', 'person'));
ALTER TABLE media_item ADD COLUMN rating_checked_at TEXT;

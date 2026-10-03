-- Feed tokens (ADR-0041).
--
-- A calendar app and a feed reader are given an address and nothing else, so
-- the token in the address is the credential. One per account: minting again
-- replaces it. Only its SHA-256 is kept, so a copy of the database does not
-- yield a working address.
CREATE TABLE feed_token (
    user_id      INTEGER PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
    token_hash   TEXT    NOT NULL UNIQUE,
    created_at   TEXT    NOT NULL,
    last_used_at TEXT
);

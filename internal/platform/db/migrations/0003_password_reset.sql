-- 0003_password_reset.sql — single-use, expiring password reset tokens.
--
-- Only the SHA-256 of the token is stored. The tokens are 256-bit random
-- values, so a fast hash is correct here: there is nothing to brute force, and
-- argon2 would only make verification slow on an anonymous endpoint.

CREATE TABLE password_reset (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    token_hash  TEXT    NOT NULL UNIQUE,
    created_at  TEXT    NOT NULL,
    expires_at  TEXT    NOT NULL,
    used_at     TEXT,
    -- Who asked. Recorded so an operator can see a reset being probed, and
    -- purged with the row.
    source_ip   TEXT,
    -- Set when an administrator minted the token on the user's behalf, which
    -- is the working delivery path until an email transport exists.
    issued_by_user_id INTEGER REFERENCES app_user(id)
);

CREATE INDEX idx_password_reset_user ON password_reset(user_id) WHERE used_at IS NULL;
CREATE INDEX idx_password_reset_expires ON password_reset(expires_at) WHERE used_at IS NULL;

-- Invites carry a note so an issuer can record who the code was meant for
-- without that becoming a second identity record.
ALTER TABLE invite ADD COLUMN issuer_rank INTEGER NOT NULL DEFAULT 0;

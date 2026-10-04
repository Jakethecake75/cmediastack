-- The posters a title search showed an account (ADR-0070). The poster route
-- fetches only paths this instance recorded, and serves an account that may
-- not edit the library only a poster it can see or was itself offered here.
CREATE TABLE offered_poster (
    user_id     INTEGER NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    provider    TEXT    NOT NULL,
    provider_id INTEGER NOT NULL,
    poster_path TEXT    NOT NULL,
    offered_at  TEXT    NOT NULL,
    PRIMARY KEY (user_id, provider, provider_id)
);
CREATE INDEX idx_offered_poster_id ON offered_poster(provider, provider_id);

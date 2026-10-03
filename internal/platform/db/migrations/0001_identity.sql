-- 0001_identity.sql — Phase 1 schema: roles, users, account requests, invites,
-- sessions, API tokens, library grants, audit log, auth throttling, settings.
--
-- Conventions:
--   * Timestamps are RFC3339 UTC strings. SQLite has no date type and text
--     sorts correctly in this format.
--   * "app_user" rather than "user" to avoid any reserved-word ambiguity.
--   * Every foreign key is declared; PRAGMA foreign_keys is ON per connection.

CREATE TABLE role (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE,
    rank        INTEGER NOT NULL,
    builtin     INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
);

CREATE TABLE role_permission (
    role_id     INTEGER NOT NULL REFERENCES role(id) ON DELETE CASCADE,
    permission  TEXT    NOT NULL,
    PRIMARY KEY (role_id, permission)
);

CREATE TABLE library (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE,
    kind        TEXT    NOT NULL,           -- movie|series|music|book
    created_at  TEXT    NOT NULL
);

CREATE TABLE app_user (
    id                  INTEGER PRIMARY KEY,
    username            TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    email               TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash       TEXT    NOT NULL,
    state               TEXT    NOT NULL,   -- awaiting_mfa|active|suspended|disabled
    role_id             INTEGER NOT NULL REFERENCES role(id),
    -- TOTP secret is AES-256-GCM sealed under context "user:<id>:totp".
    totp_secret_enc     BLOB,
    totp_enrolled_at    TEXT,
    rating_ceiling      INTEGER NOT NULL DEFAULT 0,  -- 0 = uncapped
    created_at          TEXT    NOT NULL,
    updated_at          TEXT    NOT NULL,
    approved_by_user_id INTEGER REFERENCES app_user(id),
    last_login_at       TEXT
);

CREATE INDEX idx_app_user_state ON app_user(state);

-- Recovery codes are hashed exactly like passwords. With mandatory MFA they
-- are the only path back into a locked-out account, so they are single-use and
-- their consumption is audited.
CREATE TABLE recovery_code (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    code_hash   TEXT    NOT NULL,
    created_at  TEXT    NOT NULL,
    used_at     TEXT
);

CREATE INDEX idx_recovery_code_user ON recovery_code(user_id) WHERE used_at IS NULL;

CREATE TABLE invite (
    id                  INTEGER PRIMARY KEY,
    code_hash           TEXT    NOT NULL UNIQUE,
    role_id             INTEGER NOT NULL REFERENCES role(id),
    library_ids_json    TEXT    NOT NULL DEFAULT '[]',
    rating_ceiling      INTEGER NOT NULL DEFAULT 0,
    auto_approve        INTEGER NOT NULL DEFAULT 1,
    issued_by_user_id   INTEGER NOT NULL REFERENCES app_user(id),
    note                TEXT,
    created_at          TEXT    NOT NULL,
    expires_at          TEXT    NOT NULL,
    redeemed_at         TEXT,
    redeemed_by_user_id INTEGER REFERENCES app_user(id),
    revoked_at          TEXT
);

-- An account request is NOT a user. It holds no session, no token and no
-- permission, and it cannot authenticate. The password is Argon2id-hashed at
-- submission time and never stored reversibly.
--
-- This table is a small PII store (email, source IP, user agent) and is purged
-- on expiry rather than archived.
CREATE TABLE account_request (
    id                  INTEGER PRIMARY KEY,
    username            TEXT    NOT NULL COLLATE NOCASE,
    email               TEXT    NOT NULL COLLATE NOCASE,
    password_hash       TEXT    NOT NULL,
    note                TEXT,
    source_ip           TEXT,
    user_agent          TEXT,
    invite_id           INTEGER REFERENCES invite(id),
    state               TEXT    NOT NULL,   -- pending|approved|denied
    created_at          TEXT    NOT NULL,
    expires_at          TEXT    NOT NULL,
    decided_at          TEXT,
    decided_by_user_id  INTEGER REFERENCES app_user(id),
    deny_reason         TEXT
);

-- Only one outstanding request per email. Enforced as a partial unique index
-- so that denied and approved rows do not block a later resubmission.
CREATE UNIQUE INDEX idx_account_request_pending_email
    ON account_request(email) WHERE state = 'pending';
CREATE INDEX idx_account_request_state ON account_request(state);
CREATE INDEX idx_account_request_expires ON account_request(expires_at) WHERE state = 'pending';

CREATE TABLE library_grant (
    user_id     INTEGER NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    library_id  INTEGER NOT NULL REFERENCES library(id) ON DELETE CASCADE,
    granted_at  TEXT    NOT NULL,
    PRIMARY KEY (user_id, library_id)
);

CREATE TABLE session (
    id                  TEXT    PRIMARY KEY,
    user_id             INTEGER NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    -- Refresh tokens rotate. We store only the hash of the current one, plus a
    -- generation counter used to detect replay of a superseded token.
    refresh_hash        TEXT    NOT NULL,
    refresh_generation  INTEGER NOT NULL DEFAULT 0,
    mfa_satisfied       INTEGER NOT NULL DEFAULT 0,
    device_label        TEXT,
    source_ip           TEXT,
    user_agent          TEXT,
    created_at          TEXT    NOT NULL,
    last_seen_at        TEXT    NOT NULL,
    idle_expires_at     TEXT    NOT NULL,
    absolute_expires_at TEXT    NOT NULL,
    revoked_at          TEXT,
    revoked_reason      TEXT
);

CREATE INDEX idx_session_user ON session(user_id) WHERE revoked_at IS NULL;

CREATE TABLE api_token (
    id              INTEGER PRIMARY KEY,
    user_id         INTEGER NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    name            TEXT    NOT NULL,
    token_hash      TEXT    NOT NULL UNIQUE,
    permissions_json TEXT   NOT NULL DEFAULT '[]',  -- subset of the user's permissions
    created_at      TEXT    NOT NULL,
    expires_at      TEXT,
    last_used_at    TEXT,
    revoked_at      TEXT
);

CREATE INDEX idx_api_token_user ON api_token(user_id) WHERE revoked_at IS NULL;

-- Append-only. No repository method issues UPDATE or DELETE against this
-- table, and a test asserts that.
CREATE TABLE audit_event (
    id              INTEGER PRIMARY KEY,
    occurred_at     TEXT    NOT NULL,
    actor_user_id   INTEGER,            -- NULL for anonymous or system actors
    actor_label     TEXT    NOT NULL,
    action          TEXT    NOT NULL,
    outcome         TEXT    NOT NULL,   -- success|denied|failure
    target_kind     TEXT,
    target_id       TEXT,
    source_ip       TEXT,
    user_agent      TEXT,
    detail          TEXT,
    before_json     TEXT,
    after_json      TEXT
);

CREATE INDEX idx_audit_occurred ON audit_event(occurred_at);
CREATE INDEX idx_audit_actor ON audit_event(actor_user_id, occurred_at);
CREATE INDEX idx_audit_action ON audit_event(action, occurred_at);

-- Throttling ledger for login and signup. Keyed by "ip:<addr>" or "user:<id>".
CREATE TABLE auth_attempt (
    id          INTEGER PRIMARY KEY,
    key         TEXT    NOT NULL,
    kind        TEXT    NOT NULL,       -- login|signup|reset
    occurred_at TEXT    NOT NULL,
    success     INTEGER NOT NULL
);

CREATE INDEX idx_auth_attempt_key ON auth_attempt(kind, key, occurred_at);

CREATE TABLE setting (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

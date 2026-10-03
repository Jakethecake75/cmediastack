-- Indexers.
--
-- The API key is stored SEALED, never as plaintext, under the context
-- "indexer:<id>:apikey". The context is bound into the AEAD as additional data,
-- so a sealed key cannot be relocated: lifting the blob out of one indexer's
-- row and into another's produces a decryption failure rather than a working
-- credential for the wrong tracker.
--
-- The id is part of the context, which is why api_key_enc is filled in by a
-- second statement after the INSERT: the row has no id until it exists.

CREATE TABLE indexer (
    id              INTEGER PRIMARY KEY,
    name            TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    kind            TEXT    NOT NULL,          -- torznab | newznab
    base_url        TEXT    NOT NULL,
    api_key_enc     BLOB,                      -- AES-256-GCM, context-bound
    categories      TEXT    NOT NULL DEFAULT '',  -- comma-separated ints
    enabled         INTEGER NOT NULL DEFAULT 1,
    priority        INTEGER NOT NULL DEFAULT 25,
    seed_ratio      REAL    NOT NULL DEFAULT 0,
    seed_time_secs  INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL,
    -- Health, written by the indexer sync task. Kept on the row rather than in
    -- a separate table because there is exactly one current state per indexer
    -- and the UI always wants it alongside the definition.
    last_checked_at TEXT,
    last_error      TEXT,
    consecutive_failures INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_indexer_enabled ON indexer(enabled, priority);

-- Quality profiles.
--
-- The allowed list and the cutoff are stored as text rather than as foreign
-- keys to a quality table: the ladder is defined in code (internal/release),
-- and a database that disagreed with the code would be the worse authority.
-- Profile.Compile() validates the names on the way in, so a row can only hold
-- names the code recognises.
CREATE TABLE quality_profile (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    allowed     TEXT    NOT NULL,   -- JSON array of quality names, worst first
    cutoff      TEXT    NOT NULL,
    preferred   TEXT    NOT NULL DEFAULT '[]',  -- JSON [{term,score}]
    required    TEXT    NOT NULL DEFAULT '[]',  -- JSON array
    forbidden   TEXT    NOT NULL DEFAULT '[]',  -- JSON array
    builtin     INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
);

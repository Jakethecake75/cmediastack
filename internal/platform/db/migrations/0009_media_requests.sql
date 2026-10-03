-- What people have asked this instance to acquire.
--
-- This is the Jellyseerr half of the brief: somebody who is not the operator
-- says "I would like The Matrix", and somebody who IS trusted decides.
--
-- # What a request can and cannot be, today
--
-- There is no metadata provider (ADR-0016), so a request cannot be a TMDB id
-- with a poster and a canonical title. It is WORDS: a kind, a title, a year and
-- a note. That has a real consequence worth stating rather than hiding — two
-- people asking for the same film in different words produce two rows unless
-- the normalisation below happens to agree, and a request cannot be matched to
-- a library item by id, only by title and year.
--
-- The alternative was to wait for metadata before building this at all. That is
-- worse: a suggestion box that works is more useful than a catalogue that does
-- not exist, and the external-id columns on media_item are already there for
-- the day the ids arrive.
--
-- # What approval does NOT do
--
-- Approving a request does not download anything. It marks the request as
-- something the operator is willing to acquire; a human then searches and grabs
-- for it, and that grab is linked back here. Automatic fulfilment — the machine
-- choosing which release satisfies which request — is a separate decision and
-- is not built. §13 puts the legality of what this instance acquires on the
-- operator, and a design where other people's words cause downloads without a
-- human choosing the release is the wrong default for that.

CREATE TABLE media_request (
    id       INTEGER PRIMARY KEY,

    -- movie | series. Deliberately not free text: it decides which root folder
    -- the result belongs in, and a typo would put a film in the TV library.
    kind     TEXT    NOT NULL,

    -- What was asked for, as the person typed it. Kept verbatim for display,
    -- because "Se7en" is what they will recognise in their own list.
    title    TEXT    NOT NULL,
    year     INTEGER,
    note     TEXT    NOT NULL DEFAULT '',

    -- The normalised form used ONLY for duplicate detection: lowercased,
    -- punctuation stripped, a leading article removed, and the year appended.
    -- Stored rather than computed per query so that the partial unique index
    -- below can exist at all — and so that changing the normalisation rule is a
    -- migration, which is honest, rather than a silent change in what collides.
    match_key TEXT   NOT NULL,

    -- pending | approved | denied | fulfilled
    state     TEXT   NOT NULL,

    -- Who first asked. Nullable and SET NULL rather than CASCADE: a request is
    -- part of the record of what this instance acquired and why, and a record
    -- that can be erased by deleting its subject is not a record. A row whose
    -- requester is gone still says what was asked for and what came of it.
    requested_by INTEGER REFERENCES app_user(id) ON DELETE SET NULL,
    requested_at TEXT    NOT NULL,

    decided_by      INTEGER REFERENCES app_user(id) ON DELETE SET NULL,
    decided_at      TEXT,
    decision_reason TEXT NOT NULL DEFAULT '',

    -- What was grabbed to satisfy this, once something was. Not a foreign key
    -- to download_queue: the queue row is removed when the download completes
    -- or is cancelled, and the request must outlive it — "what did we grab for
    -- this" is exactly the question asked after the queue has forgotten.
    info_hash    TEXT,
    grabbed_at   TEXT,

    -- What it finally became. SET NULL so that deleting a library item does not
    -- erase the request that asked for it; the request then correctly reads as
    -- fulfilled-and-since-removed rather than vanishing.
    media_item_id INTEGER REFERENCES media_item(id) ON DELETE SET NULL,
    fulfilled_at  TEXT,

    updated_at TEXT NOT NULL,

    CHECK (kind IN ('movie', 'series')),
    CHECK (state IN ('pending', 'approved', 'denied', 'fulfilled'))
);

-- Two OPEN requests for the same thing cannot coexist: the second person to ask
-- joins the first request rather than creating a rival one. A partial index,
-- because once a request is denied or fulfilled the question becomes askable
-- again — a film that was denied last year may be fine now, and one that was
-- fulfilled and then deleted is a legitimate new request.
CREATE UNIQUE INDEX idx_media_request_open
    ON media_request(match_key) WHERE state IN ('pending', 'approved');

CREATE INDEX idx_media_request_state ON media_request(state, requested_at);
CREATE INDEX idx_media_request_user  ON media_request(requested_by, requested_at);
CREATE INDEX idx_media_request_hash  ON media_request(info_hash);

-- Everyone who wants a given request, including the person who opened it.
--
-- Separate from requested_by because "who asked first" and "who is waiting" are
-- different questions: the first orders the queue and attributes the decision,
-- the second decides who should be told when it arrives. Collapsing them would
-- mean a second requester either overwrote the first or was lost.
CREATE TABLE media_request_follower (
    request_id  INTEGER NOT NULL REFERENCES media_request(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    followed_at TEXT    NOT NULL,
    PRIMARY KEY (request_id, user_id)
);

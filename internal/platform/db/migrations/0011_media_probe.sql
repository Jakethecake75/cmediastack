-- Migration 0011: what is actually inside a media file.
--
-- Everything before this knows a file by its NAME — the release title a
-- stranger chose, parsed for a quality and a resolution. That is a claim.
-- This is the file itself, read by ffprobe in the sandbox described in
-- ADR-0020, and it is what every playback decision is made from.

-- A probe is cached per file, and invalidated by the file changing.
--
-- The temptation is to probe once at import and never again. That is wrong,
-- because a file CAN change underneath the library: a re-download, an
-- operator's own re-encode, a restore from a backup that was not the same
-- file. A stale probe then produces a playback failure that looks like a bug
-- in the player and is impossible to diagnose from the outside.
--
-- So size_bytes and mod_time record the file's identity AT THE MOMENT IT WAS
-- PROBED. Anything that does not match means re-probe. It is not a hash — a
-- hash would be exact and would mean reading every byte of a 40 GB remux to
-- find out whether anything changed, which costs far more than the case it
-- catches.
CREATE TABLE media_probe (
    -- One probe per file, so the file's id IS the key. A separate surrogate
    -- would permit two probes of one file, which has no meaning.
    media_file_id INTEGER PRIMARY KEY REFERENCES media_file(id) ON DELETE CASCADE,

    size_bytes INTEGER NOT NULL,
    mod_time   TEXT    NOT NULL,
    probed_at  TEXT    NOT NULL,

    -- Whether the parser ran inside the namespace jail. A probe taken without
    -- it is still a probe; it was taken under a weaker guarantee, and an
    -- operator auditing later deserves to know which (ADR-0020).
    sandboxed INTEGER NOT NULL DEFAULT 0,

    -- ffprobe's format_name, kept whole. It is a comma-separated list of every
    -- format that could demux this file — "mov,mp4,m4a,3gp,3g2,mj2" is one
    -- answer, not six — and picking a member would invent precision.
    container TEXT NOT NULL DEFAULT '',

    -- Milliseconds rather than a float: durations are compared and summed, and
    -- a float that is 7199.999999 makes a two-hour film not two hours.
    duration_ms INTEGER NOT NULL DEFAULT 0,
    bitrate     INTEGER NOT NULL DEFAULT 0
);

-- Streams, normalised rather than stored as a JSON blob on the probe.
--
-- Because of the question ADR-0005 creates and an operator will certainly ask:
-- "what in my library will not play on this box?" That is
--
--   SELECT ... WHERE codec = 'hevc' AND bit_depth > 8
--
-- against this table, and it is a JSON scan of every row in the other design.
-- The library is the thing being queried, so it is modelled.
CREATE TABLE media_stream (
    id            INTEGER PRIMARY KEY,
    media_file_id INTEGER NOT NULL REFERENCES media_file(id) ON DELETE CASCADE,

    -- 'video', 'audio' or 'subtitle'. One table rather than three: the columns
    -- that differ are few, and a join that had to union three tables to answer
    -- "what is in this file" would be worse than a few nullable columns.
    kind TEXT NOT NULL,

    -- The stream's index WITHIN THE FILE, which is how ffmpeg is told to select
    -- it later. Not a display position: a file may begin at index 1.
    stream_index INTEGER NOT NULL,

    codec   TEXT    NOT NULL DEFAULT '',
    profile TEXT    NOT NULL DEFAULT '',
    level   INTEGER NOT NULL DEFAULT 0,

    -- Video.
    width           INTEGER,
    height          INTEGER,
    bit_depth       INTEGER,
    pixel_format    TEXT,
    frame_rate      REAL,
    color_transfer  TEXT,
    color_primaries TEXT,
    -- Stored as a decided boolean rather than recomputed from color_transfer
    -- at query time, so that "which files are HDR" has one answer and it is
    -- the one the code made. Recomputing in SQL would be a second
    -- implementation of the rule, free to disagree with the first.
    hdr INTEGER NOT NULL DEFAULT 0,

    -- Audio.
    channels       INTEGER,
    channel_layout TEXT,
    sample_rate    INTEGER,

    -- Audio and subtitle.
    language  TEXT NOT NULL DEFAULT '',
    title     TEXT NOT NULL DEFAULT '',
    is_forced INTEGER NOT NULL DEFAULT 0,

    -- Subtitle. Whether the track is text rather than pictures: a text track
    -- can be converted to WebVTT and handed to a browser, while a bitmap track
    -- (PGS, VobSub) can only be drawn onto the picture — which is a transcode,
    -- and on the target hardware may mean it cannot be shown at all.
    is_text INTEGER NOT NULL DEFAULT 0,

    is_default INTEGER NOT NULL DEFAULT 0,

    UNIQUE (media_file_id, stream_index)
);

CREATE INDEX idx_media_stream_file ON media_stream(media_file_id, kind);

-- The index that makes the operator's question cheap.
CREATE INDEX idx_media_stream_playability ON media_stream(kind, codec, bit_depth);

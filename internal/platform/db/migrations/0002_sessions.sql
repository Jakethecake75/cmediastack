-- 0002_sessions.sql — session rotation with reuse detection, and TOTP replay
-- prevention.

-- Rotation keeps the previous secret's hash for a short grace window so that
-- concurrent in-flight requests do not fail during a rotation. Presenting the
-- previous secret AFTER that window is not a race, it is a replay of a stolen
-- token, and it revokes the whole session.
ALTER TABLE session ADD COLUMN prev_refresh_hash TEXT;
ALTER TABLE session ADD COLUMN rotated_at TEXT;

-- A TOTP code stays valid for its whole time step, so verifying it is not
-- enough: the consumed counter must be recorded and a repeat refused. Without
-- this, a code observed over the shoulder or captured in a proxy log can be
-- replayed for up to 90 seconds (the step plus the skew window).
ALTER TABLE app_user ADD COLUMN totp_last_counter INTEGER NOT NULL DEFAULT 0;

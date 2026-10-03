-- Roles an administrator has edited (ADR-0039).
--
-- The seed rewrites every built-in role's permissions at start-up, which made
-- an edit last until the next restart. permissions_chosen_at is when a person
-- last chose a role's permissions; while it is set, the seed leaves that role
-- alone. NULL is the built-in defaults, and putting them back sets it to NULL.
-- Admin is rewritten whatever this says: it holds every permission, always.
ALTER TABLE role ADD COLUMN permissions_chosen_at TEXT;

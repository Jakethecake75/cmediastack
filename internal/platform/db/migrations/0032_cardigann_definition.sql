-- An indexer can be a tracker searched by its Cardigann definition (ADR-0058).
--
-- The definition is the YAML the operator pasted, kept with its indexer: never
-- vendored, never fetched. NULL for a Torznab or Newznab indexer.
ALTER TABLE indexer ADD COLUMN definition TEXT;

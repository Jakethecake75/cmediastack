-- A Cardigann indexer's settings: the operator's username, password or
-- cookie for a tracker that signs in (ADR-0059). One sealed JSON object per
-- indexer, AES-256-GCM under "indexer:<id>:settings", never returned.
ALTER TABLE indexer ADD COLUMN settings_enc BLOB;

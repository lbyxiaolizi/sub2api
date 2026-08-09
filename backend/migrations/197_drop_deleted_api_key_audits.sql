-- Deleted API keys are tombstoned in api_keys; retaining the original secret in
-- a second table defeats deletion and turns database backups into credential stores.
DROP TABLE IF EXISTS deleted_api_key_audits;

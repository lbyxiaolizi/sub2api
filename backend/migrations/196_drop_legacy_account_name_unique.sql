-- Remove the legacy full-table uniqueness left by older account schemas.
-- Account names are display labels and the current Ent schema allows duplicates;
-- a soft-deleted row must not block creating a replacement with the same name.
ALTER TABLE accounts DROP CONSTRAINT IF EXISTS accounts_name_key;
DROP INDEX IF EXISTS accounts_name_key;
DROP INDEX IF EXISTS account_name_key;

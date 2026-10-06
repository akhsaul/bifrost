-- Purge the model catalog's derived tables so the next force-sync rebuilds them
-- from the configured datasheet files.
--
-- Why this is needed: the datasheet sync is upsert-only. SyncFromURL
-- (framework/modelcatalog/datasheet/sync.go) calls UpsertModelPricesBatch, an
-- ON CONFLICT (model, provider, mode) DO UPDATE -- there is no DELETE anywhere in
-- the sync path. So a row that stops appearing in the source file stays in the
-- catalog forever, keeping its old values.
--
-- That is why the orphan rows 'DeepSeek-V4.1-Flash' and 'GLM-5.3-Flash' (provider
-- bitdeer) survive: no datasheet key can produce them, and no sync removes them.
-- See debug/bug-model-catalog-duplicate-entry.md.
--
-- Both tables are pure caches of the datasheet files. Deleting all rows loses
-- nothing that is not about to be re-read from those files.
--
-- Source files actually in use (from ~/.my-bifrost/config.json -> framework.pricing):
--   pricing_url          = file:///mnt/data/Projects/bifrost/editor/model_pricing.json
--   model_parameters_url = file:///mnt/data/Projects/bifrost/editor/model_parameters.json
-- Verified: every key in both files has a provider segment whose value matches the
-- entry body's own "provider" field, so a resync cannot recreate a mangled key.
--
-- Run as root (the DB is root-owned and bifrost-http runs as root):
--   sudo sqlite3 /home/akhsaul/.my-bifrost/config.db < debug/purge-pricing-and-parameters.sql
--
-- The server may stay running. It holds no pricing rows in a transaction, and the
-- force-sync you run afterwards calls LoadFromDB, which rebuilds the in-memory
-- catalog from these tables.

PRAGMA busy_timeout = 60000;

-- Verification before: expect 4707 / 12603 rows, and 4 bitdeer rows of which
-- 'DeepSeek-V4.1-Flash' and 'GLM-5.3-Flash' are the unreachable orphans.
SELECT 'BEFORE pricing', count(*) FROM governance_model_pricing;
SELECT 'BEFORE params',  count(*) FROM governance_model_parameters;
SELECT 'BEFORE bitdeer', model, provider, max_input_tokens, max_output_tokens
  FROM governance_model_pricing WHERE provider = 'bitdeer' ORDER BY model;

BEGIN IMMEDIATE;
DELETE FROM governance_model_pricing;
DELETE FROM governance_model_parameters;
COMMIT;

-- Verification after: both must be 0. Then trigger a force-sync from the UI
-- (or: curl -X POST http://127.0.0.1:<port>/api/pricing/force-sync) and expect
-- 4767 pricing rows (the file's entry count; lower than 4767 only if two keys
-- collide on (model, provider, mode), which the batch dedup drops) and 12599
-- parameter rows. bitdeer must come back as exactly 2 rows:
--   deepseek-ai/DeepSeek-V4.1-Flash
--   zai-org/GLM-5.3-Flash
SELECT 'AFTER pricing', count(*) FROM governance_model_pricing;
SELECT 'AFTER params',  count(*) FROM governance_model_parameters;

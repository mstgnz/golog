-- 0003: service name and JSON attributes per log.
-- Constant defaults make both ADD COLUMNs metadata-only on PostgreSQL 11+:
-- no table rewrite, existing rows read as '' and {}.
-- Rollback: ALTER TABLE logs DROP COLUMN attrs, DROP COLUMN service;

ALTER TABLE logs ADD COLUMN IF NOT EXISTS service text NOT NULL DEFAULT '';
ALTER TABLE logs ADD COLUMN IF NOT EXISTS attrs jsonb NOT NULL DEFAULT '{}';

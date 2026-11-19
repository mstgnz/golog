-- golog:no-transaction
-- 0002: index for the list query (ORDER BY timestamp DESC, id DESC LIMIT n).
-- CONCURRENTLY so upgrading a large logs table does not block inserts; that
-- is why this file runs outside a transaction and holds a single statement.
-- No IF NOT EXISTS: a failed concurrent build leaves an INVALID index that
-- IF NOT EXISTS would silently keep. Rollback: DROP INDEX CONCURRENTLY logs_timestamp_id_idx;
CREATE INDEX CONCURRENTLY logs_timestamp_id_idx ON logs (timestamp DESC, id DESC);

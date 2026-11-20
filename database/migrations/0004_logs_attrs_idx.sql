-- golog:no-transaction
-- 0004: GIN index for attr filters (attrs @> '{"trace_id":"..."}').
-- jsonb_path_ops: smaller and faster for @>, which is the only operator used.
-- Rollback: DROP INDEX CONCURRENTLY logs_attrs_idx;
CREATE INDEX CONCURRENTLY logs_attrs_idx ON logs USING gin (attrs jsonb_path_ops);

-- 0001: logs table, NOTIFY trigger.
-- Safe on a v1.x database: the table is kept and the trigger function is
-- replaced so NOTIFY carries only the id (payloads are capped at 8000 bytes,
-- so the old full-row payload made large inserts fail).
-- Rollback: DROP TRIGGER log_notify_trigger ON logs; DROP FUNCTION notify_log_change();

CREATE TABLE IF NOT EXISTS logs (
    id SERIAL PRIMARY KEY,
    timestamp TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    level VARCHAR(10) NOT NULL,
    type VARCHAR(50) NOT NULL,
    message TEXT NOT NULL
);

CREATE OR REPLACE FUNCTION notify_log_change()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM pg_notify('log_channel', NEW.id::text);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS log_notify_trigger ON logs;
CREATE TRIGGER log_notify_trigger
AFTER INSERT ON logs
FOR EACH ROW
EXECUTE FUNCTION notify_log_change();

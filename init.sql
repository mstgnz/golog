CREATE TABLE IF NOT EXISTS logs (
    id SERIAL PRIMARY KEY,
    timestamp TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    level VARCHAR(10) NOT NULL,
    type VARCHAR(50) NOT NULL,
    message TEXT NOT NULL
);

-- Create function to notify on new log entries
CREATE OR REPLACE FUNCTION notify_log_change()
RETURNS TRIGGER AS $$
BEGIN
    PERFORM pg_notify('log_channel', json_build_object(
        'id', NEW.id,
        'timestamp', NEW.timestamp,
        'level', NEW.level,
        'type', NEW.type,
        'message', NEW.message
    )::text);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- Create trigger to call the notify function
DROP TRIGGER IF EXISTS log_notify_trigger ON logs;
CREATE TRIGGER log_notify_trigger
AFTER INSERT ON logs
FOR EACH ROW
EXECUTE FUNCTION notify_log_change();

-- Insert some sample logs
INSERT INTO logs (level, type, message) VALUES
('INFO', 'SYSTEM', 'System started'),
('WARNING', 'AUTH', 'Failed login attempt'),
('ERROR', 'DATABASE', 'Connection timeout'),
('INFO', 'USER', 'User profile updated'),
('DEBUG', 'API', 'Request received: GET /api/users'); 
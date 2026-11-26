DROP INDEX IF EXISTS idx_publisher_outbox_request_id;
ALTER TABLE publisher_outbox DROP COLUMN IF EXISTS caller_request_id;

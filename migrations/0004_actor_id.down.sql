DROP INDEX IF EXISTS idx_publisher_outbox_actor;
ALTER TABLE publisher_outbox DROP COLUMN IF EXISTS actor_id;

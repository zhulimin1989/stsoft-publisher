-- This rollback intentionally fails while rows with op values longer than
-- 16 characters still exist, preventing silent truncation of audit data.
ALTER TABLE publisher_outbox
    ALTER COLUMN op TYPE VARCHAR(16);

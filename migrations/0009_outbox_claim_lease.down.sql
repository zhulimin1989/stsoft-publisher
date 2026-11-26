DROP INDEX IF EXISTS idx_publisher_outbox_lease;

DROP INDEX IF EXISTS idx_publisher_outbox_due;

ALTER TABLE publisher_outbox
    DROP CONSTRAINT IF EXISTS publisher_outbox_status_check;

UPDATE publisher_outbox
SET status = 'failed',
    next_attempt_at = coalesce(lease_until, now())
WHERE status = 'in_progress';

ALTER TABLE publisher_outbox
    ADD CONSTRAINT publisher_outbox_status_check CHECK (status IN
        ('pending','delivered','failed','dead_letter'));

ALTER TABLE publisher_outbox
    DROP COLUMN IF EXISTS lease_owner;

ALTER TABLE publisher_outbox
    DROP COLUMN IF EXISTS lease_until;

CREATE INDEX IF NOT EXISTS idx_publisher_outbox_due
    ON publisher_outbox (next_attempt_at)
    WHERE status IN ('pending','failed');

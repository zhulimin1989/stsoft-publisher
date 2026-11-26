-- Persist worker claims so concurrent workers cannot re-claim and deliver the
-- same pending row while the first worker is still executing Redis operations.

ALTER TABLE publisher_outbox
    ADD COLUMN IF NOT EXISTS lease_owner TEXT;

ALTER TABLE publisher_outbox
    ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ;

ALTER TABLE publisher_outbox
    DROP CONSTRAINT IF EXISTS publisher_outbox_status_check;

ALTER TABLE publisher_outbox
    ADD CONSTRAINT publisher_outbox_status_check CHECK (status IN
        ('pending','in_progress','delivered','failed','dead_letter'));

DROP INDEX IF EXISTS idx_publisher_outbox_due;

CREATE INDEX IF NOT EXISTS idx_publisher_outbox_due
    ON publisher_outbox (next_attempt_at)
    WHERE status IN ('pending','failed','in_progress');

CREATE INDEX IF NOT EXISTS idx_publisher_outbox_lease
    ON publisher_outbox (lease_until)
    WHERE status = 'in_progress';

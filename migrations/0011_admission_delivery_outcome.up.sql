-- Persist the Redis admission CAS result on the outbox row so Billing can
-- distinguish accepted-but-pending from applied, superseded, deterministic
-- conflict, and dead-letter delivery. NULL remains the value for all
-- non-admission operations and pre-migration rows.

ALTER TABLE publisher_outbox
    ADD COLUMN IF NOT EXISTS delivery_outcome VARCHAR(16);

ALTER TABLE publisher_outbox
    DROP CONSTRAINT IF EXISTS publisher_outbox_delivery_outcome_check;

ALTER TABLE publisher_outbox
    ADD CONSTRAINT publisher_outbox_delivery_outcome_check CHECK (
        delivery_outcome IS NULL OR delivery_outcome IN
        ('applied','superseded','idempotent','conflict')
    ) NOT VALID;

-- NOT VALID still enforces the constraint for new/updated rows without
-- scanning and locking the potentially large historical outbox here. Run
-- VALIDATE CONSTRAINT in a separate maintenance window after rollout.

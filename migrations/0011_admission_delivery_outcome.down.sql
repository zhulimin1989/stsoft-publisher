ALTER TABLE publisher_outbox
    DROP CONSTRAINT IF EXISTS publisher_outbox_delivery_outcome_check;

ALTER TABLE publisher_outbox
    DROP COLUMN IF EXISTS delivery_outcome;

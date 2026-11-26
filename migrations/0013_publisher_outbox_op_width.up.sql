-- Composite Publisher operations use descriptive names longer than the
-- original Redis primitive op values. Keep enough room for the longest
-- supported operation before accepting key_activate_atomic writes.
ALTER TABLE publisher_outbox
    ALTER COLUMN op TYPE VARCHAR(32);

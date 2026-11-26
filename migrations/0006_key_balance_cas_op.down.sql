-- Revert to pre-key_balance_cas allowlist. Run only when also reverting
-- impl.go::buildKeyBalanceRows back to plain HSet rows; otherwise the
-- next SetKeyBalance call after rollback will 500.

ALTER TABLE publisher_outbox
    DROP CONSTRAINT publisher_outbox_op_check;

ALTER TABLE publisher_outbox
    ADD CONSTRAINT publisher_outbox_op_check CHECK (op IN
        ('set','setnx','hset','hsetnx','sadd','srem','del','hdel','incr','publish'));

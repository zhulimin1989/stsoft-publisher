-- 2026-05-26 HA Commit B-2 follow-up: the OpKeyBalanceCAS Op type
-- ('key_balance_cas') was added to outbox.go but the matching CHECK
-- constraint on publisher_outbox.op was not widened — so every
-- SetKeyBalance call from impl.go (which now emits this op type) was
-- rejected at INSERT time with OUTBOX_UNAVAILABLE.
--
-- Caught by integration verification 2026-05-26: 500 + OUTBOX_UNAVAILABLE
-- on every PUT /billing/api/v1/key-balance/{key_id} call until this
-- migration runs.

ALTER TABLE publisher_outbox
    DROP CONSTRAINT publisher_outbox_op_check;

ALTER TABLE publisher_outbox
    ADD CONSTRAINT publisher_outbox_op_check CHECK (op IN
        ('set','setnx','hset','hsetnx','sadd','srem','del','hdel','incr','publish','key_balance_cas'));

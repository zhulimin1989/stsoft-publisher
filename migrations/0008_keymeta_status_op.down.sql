-- Revert to the pre-keymeta_status allowlist. Only run when also reverting
-- SRE key lifecycle methods back to revoked-set-only writes.

ALTER TABLE publisher_outbox
    DROP CONSTRAINT publisher_outbox_op_check;

ALTER TABLE publisher_outbox
    ADD CONSTRAINT publisher_outbox_op_check CHECK (op IN
        ('set','setnx','hset','hsetnx','sadd','srem','del','hdel','incr','publish','key_balance_cas'));

-- Allow SRE key lifecycle updates to patch gw:keymeta.status atomically
-- while also updating gw:revoked_keys in the same outbox batch.

ALTER TABLE publisher_outbox
    DROP CONSTRAINT publisher_outbox_op_check;

ALTER TABLE publisher_outbox
    ADD CONSTRAINT publisher_outbox_op_check CHECK (op IN
        ('set','setnx','hset','hsetnx','sadd','srem','del','hdel','incr','publish','key_balance_cas','keymeta_status'));

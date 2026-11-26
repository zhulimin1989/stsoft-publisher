-- Active KeyMeta and its Billing admission decision must be one Redis atomic
-- unit. A multi-row outbox batch is insufficient because independent workers
-- can claim different rows from the same batch.
ALTER TABLE publisher_outbox
    DROP CONSTRAINT publisher_outbox_op_check;

ALTER TABLE publisher_outbox
    ADD CONSTRAINT publisher_outbox_op_check CHECK (op IN
        ('set','setnx','hset','hsetnx','sadd','srem','del','hdel','incr','publish',
         'key_balance_cas','keymeta_status','policy_set_cas','policy_del_cas',
         'key_activate_atomic'));

ALTER TABLE publisher_outbox
    DROP CONSTRAINT publisher_outbox_op_check;

ALTER TABLE publisher_outbox
    ADD CONSTRAINT publisher_outbox_op_check CHECK (op IN
        ('set','setnx','hset','hsetnx','sadd','srem','del','hdel','publish'));

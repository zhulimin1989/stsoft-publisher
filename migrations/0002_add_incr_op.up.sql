-- Widen publisher_outbox.op CHECK constraint to accept 'incr'.
-- Used by the worker to INCR a domain-revision key (e.g. gw:config:rev:credential)
-- and propagate the result into the same batch's PUBLISH message — driving
-- gateway-side monotonic-revision filtering of config updates.

ALTER TABLE publisher_outbox
    DROP CONSTRAINT publisher_outbox_op_check;

ALTER TABLE publisher_outbox
    ADD CONSTRAINT publisher_outbox_op_check CHECK (op IN
        ('set','setnx','hset','hsetnx','sadd','srem','del','hdel','incr','publish'));

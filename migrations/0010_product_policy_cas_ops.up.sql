-- Add product policy revision-CAS worker ops.
--
-- policy_set_cas writes gw:product_policies[field] only when the payload
-- revision is newer than the per-product sidecar/tombstone revision.
-- policy_del_cas HDELs gw:product_policies[field] and records a tombstone
-- revision so an older delayed upsert cannot resurrect stale policy data.

ALTER TABLE publisher_outbox
    DROP CONSTRAINT publisher_outbox_op_check;

ALTER TABLE publisher_outbox
    ADD CONSTRAINT publisher_outbox_op_check CHECK (op IN
        ('set','setnx','hset','hsetnx','sadd','srem','del','hdel','incr','publish','key_balance_cas','keymeta_status','policy_set_cas','policy_del_cas'));

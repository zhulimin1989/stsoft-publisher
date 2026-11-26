-- publisher_outbox is the at-least-once delivery buffer between
-- caller-facing Publisher methods (sync path) and the Redis-side worker
-- (async path).
--
-- Lifecycle:
--     pending  → (worker tick) → delivered           (success path)
--                              ↘ failed → (retries) → delivered
--                                                   ↘ dead_letter (max attempts)
--
-- Indexes:
--   idx_publisher_outbox_due   — the worker's primary read predicate
--                                 (status, next_attempt_at)
--   idx_publisher_outbox_batch — Replay-by-batch / audit-by-batch
--   idx_publisher_outbox_dedup — unique on dedup_key (idempotency)
--   idx_publisher_outbox_created — cleanup janitor
CREATE TABLE IF NOT EXISTS publisher_outbox (
    id               BIGSERIAL   PRIMARY KEY,
    batch_id         UUID        NOT NULL,
    seq_in_batch     INT         NOT NULL DEFAULT 0,
    op               VARCHAR(16) NOT NULL CHECK (op IN
                       ('set','setnx','hset','hsetnx','sadd','srem','del','hdel','publish')),
    redis_key        TEXT,
    redis_field      TEXT,
    redis_member     TEXT,
    payload          BYTEA,
    schema_version   INT         NOT NULL DEFAULT 1,
    envelope_version INT         NOT NULL DEFAULT 1,
    ttl_seconds      INT         NOT NULL DEFAULT 0,
    notify_channel   TEXT,
    notify_message   JSONB,
    method_name      VARCHAR(64) NOT NULL,
    dedup_key        TEXT,
    caller_service   TEXT,
    caller_trace_id  TEXT,
    status           VARCHAR(16) NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending','delivered','failed','dead_letter')),
    attempt_count    INT         NOT NULL DEFAULT 0,
    last_error       TEXT,
    next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_publisher_outbox_due
    ON publisher_outbox (next_attempt_at)
    WHERE status IN ('pending','failed');

CREATE INDEX IF NOT EXISTS idx_publisher_outbox_batch
    ON publisher_outbox (batch_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_publisher_outbox_dedup
    ON publisher_outbox (dedup_key)
    WHERE dedup_key IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_publisher_outbox_created
    ON publisher_outbox (created_at, status);

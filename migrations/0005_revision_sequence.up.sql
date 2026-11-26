-- 2026-05-26: globally-monotonic revision source for HA-safe writes.
--
-- Background: publisher previously auto-filled `revision` via
-- time.Now().UnixMicro(). In single-instance deployment that's fine —
-- one process, one monotonic clock. In multi-instance HA (Commit A
-- 2026-05-26 documented the topology change), per-pod clocks drift
-- (NTP, container time skew); two pods writing the same key within
-- the drift window can emit (revision_A, revision_B) NOT in business-
-- time order.
--
-- Currently masked because the worker doesn't revision-CAS — every
-- HSet just writes whatever the row carries (last-finisher wins).
-- When the worker switches to revision-CAS on gw:key_balance writes
-- (Commit B follow-up: worker Lua CAS), the revision source MUST be
-- globally monotonic across pods, or stale writes will be incorrectly
-- accepted / fresh writes incorrectly rejected.
--
-- Choice: PG sequence. Alternatives considered:
--   - Redis INCR — adds a Redis dependency to a PG-only sync path
--     (sync path currently doesn't touch Redis at all; outbox worker
--     is the only Redis writer). Keeping the boundary clean.
--   - HLC (hybrid logical clock) — overkill for this scale, and
--     reintroduces per-pod state that the deployment topology change
--     was trying to eliminate.
--
-- A single shared sequence is enough: SetKeyBalance, PublishKey, and
-- the other config-change methods all read this same sequence. Per-
-- entity sequencing isn't necessary because the worker's CAS only
-- compares revisions for the SAME (key, hash-field) tuple — different
-- keys can interleave freely.
--
-- Production behavior: nextval() is ~50-100ns on a warm sequence cache,
-- effectively zero-cost added latency. Sequence values are bigint so
-- 2^63 limit is unreachable (would need ~9e18 calls).

CREATE SEQUENCE IF NOT EXISTS publisher_revision_seq
    AS BIGINT
    START WITH 1
    INCREMENT BY 1
    MINVALUE 1
    CACHE 100;  -- per-connection cache of 100 values; safe to lose on crash

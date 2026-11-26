# Publisher Deployment and HA Safety

> 2026-08-07 safety update — supersedes the earlier multi-instance approval

## TL;DR

Publisher is currently **NO-GO for multiple active replicas**. Deploy exactly
one `cmd/publisher` pod (`replicas: 1`) with a `Recreate` strategy. The binary
always starts an outbox worker, and `ClaimDue` leases rows rather than whole
batches. Two workers can therefore claim disjoint fragments of one multi-row
batch and execute a later `PUBLISH`, `DEL`, or newer batch before its required
predecessor.

`FOR UPDATE SKIP LOCKED`, transactional inserts, and idempotent Redis commands
prevent duplicate row ownership and make retries converge, but they do **not**
provide batch-atomic claim or cross-worker ordering.

The single worker has two local ordering protections:

1. Claimed batch groups execute by their smallest persisted outbox row ID, so
   map iteration cannot let a newer claimed batch overtake an older fragment in
   the same tick.
2. Rows inside each claimed group execute by `(seq_in_batch, id)`.

These protections deliberately do not claim to solve a batch split across
workers or replicas. Multi-replica rollout remains blocked until batch-atomic
claim/serialization is implemented and exercised end to end.

## Required deployment configuration

### Replicas

Required: `replicas: 1`. Use `strategy.type: Recreate`; do not use a rolling
update with `maxSurge > 0`, because even a short old/new overlap creates two
active workers. There is currently no supported switch that leaves HTTP active
while disabling the worker in extra pods.

### Shared state

- **PostgreSQL** — single instance OR HA cluster (Patroni / RDS Multi-AZ). All publisher pods point at the same primary.
- **Redis** — single instance OR Sentinel / Cluster. All publisher pods point at the same Redis endpoint.

### Load balancer

Any L7 LB may front the single active pod. Consistent-hash routing does not make
multiple embedded workers safe and must not be used as a substitute for the
replica restriction.

## Liveness / readiness

Publisher does not currently ship `/healthz` or `/readyz` (TODO). For K8s, use a TCP probe on the API port until those land.

## Historical multi-instance considerations (not deployment approval)

The controls below remain useful hardening, but none overrides the one-worker
restriction caused by row-granular outbox claims.

### `balanceDedupe` cache and multi-instance correctness

`SetKeyBalance` short-circuits redundant writes via an in-process cache (`internal/publisher/impl.go::Impl.balanceDedupe`). With N pods and round-robin LB, the same `key_id` may land on different pods on successive calls — each pod sees only `1/N` of the call history for that key.

**Correctness concern (subtle, 2026-05-26 audit)**: blind cache hit in a multi-pod environment is NOT safe — Pod A's local cache says "I wrote (k, true) 30s ago" but Pod B may have written (k, false) in between. If A's next call is again (k, true) and A blindly trusts its cache, `gw:key_balance` stays stuck at `false` until A's TTL expires or a real state change forces a write.

**Mitigation (HA Commit B-3)**: every cache hit performs an HGet against `gw:key_balance[key_id]` to verify the canonical Redis value matches the local cache before short-circuiting. Mismatch → invalidate cache + execute the real write path.

  - cache + Redis agree → skip outbox (the dedupe win) ✓
  - cache says "1", Redis says "0" → cache is stale, fall through and rewrite ✓
  - HGet returns `redis.Nil` (field absent) → trust cache; nothing has landed yet ✓
  - HGet errors (Redis blip) → trust cache; outbox writes would also fail in this scenario, no benefit to amplifying failure ✓
  - `p.redis == nil` (verify client not wired) → trust cache; acceptable only for the current single-instance deployment ⚠

The verify costs one Redis HGet (~0.5ms) on cache hit. Still far cheaper than the 3 outbox HSet rows + audit row + downstream worker write the dedupe replaces.

**Wire it in production**: `cmd/publisher/main.go` calls `pub.SetRedisForBalanceVerify(rdb)` immediately after `publisher.New(store)`. Tests / single-pod dev deployments may leave it nil. This cache verification is necessary for any future HA design but is not sufficient to enable one today.

**Future hit-rate optimization (orthogonal to correctness)**: after batch-atomic claiming exists, an HA design may use consistent-hash routing keyed on `X-Dedupe-Key: <key_id>`. Sticky routing alone does not make the current worker safe.

### Cross-batch ordering on `gw:key_balance`

Worker writes `gw:key_balance[key_id]` via one Lua `OpKeyBalanceCAS` that atomically:

1. compares Billing's per-key `decision_version` against `gw:key_admission_decision_version`;
2. verifies equal-version identity and state fingerprints;
3. updates the main value, reason, decision sidecars, or versioned delete tombstone;
4. publishes `gw:admission:update` only after an `applied` write.

The CAS and PUBLISH are one Redis operation because workers claim outbox rows independently; correctness does not depend on two rows from a batch being claimed together. Old decisions return `superseded`, exact replays return `idempotent`, and equal-version conflicts return `conflict` without retry or mutation.

Legacy calls use a separate `gw:key_admission_legacy_version` watermark. Once an explicit Billing decision exists, later legacy PUT/DELETE operations are always superseded, regardless of Publisher's local revision. `publisher_revision_seq` remains an independent audit/cache-invalidation sequence and never orders explicit Billing decisions. Sequence fetch failure fails the HTTP write; there is no wall-clock fallback that could poison the monotonic watermark.

Other config-domain writes (`PublishKey`, `PublishProvider`, etc.) still use `time.Now().UnixMicro()`. Their low frequency does not remove the outbox split-batch risk and does not lift the HA NO-GO.

## Operational notes

### What kills the cluster

- **PG outage** — every publisher pod returns 503 INSERT_FAILED until PG recovers. K8s liveness probe will NOT restart pods (the process is still alive, dependency is down). Fix: PG HA on the infrastructure side.
- **Redis outage** — same model: publisher accepts writes (PG is fine), outbox rows queue up, worker retries with exponential backoff. When Redis recovers, the worker drains the backlog. This is by design — outbox decouples client-facing writes from Redis availability.
- **Network partition** — the single active pod may keep its process alive, but writes or delivery fail/queue according to which shared dependency is unreachable.

### What does NOT kill the cluster

- Single pod panic (recoverPanic middleware catches handler panics, returns 500, pod keeps serving other requests).
- Single pod restart (LB removes it from rotation; K8s restarts in 5-15s).
- A worker spending too long on one row does not kill the HTTP process, but it can delay the single supported drain loop and must alert.

## Safe migration / rollout drain conditions

Do not perform a rolling update. For the credential-evidence contract rollout:

1. Pause RuntimeIntent, provider, keypool, dedicated-key, replay, and other
   credential-bearing writes at ingress. Confirm callers are ready to send
   RuntimeIntent v2 plus stable `provider_account_ref` values.
2. Stop **all** old Publisher pods. Because each pod embeds a worker, leaving an
   API pod alive also leaves an old worker alive.
3. Wait for old leases to expire and verify there is no active in-flight lease:

   ```sql
   SELECT count(*)
   FROM publisher_outbox
   WHERE status = 'in_progress'
     AND lease_until > now();
   ```

   The result must be `0`. Also confirm no old process can renew or acquire a
   lease after this check.
4. Inventory every non-terminal credential-bearing batch (`pending`, `failed`,
   or expired `in_progress`). Repair or quarantine legacy batches that lack the
   required account evidence using an approved, audited procedure. Do not start
   the new worker while an unreviewed legacy batch can still execute a leading
   `DEL`/`INCR`.
5. Start exactly one new pod with `Recreate`, verify only one worker identity is
   claiming rows, and observe the outbox drain plus the produced Redis artifacts.
6. Resume v2 credential writes only after a realistic request has produced a
   Redis payload containing the expected account ref and no quarantine alert.

Scaling above one active worker remains **NO-GO** after this migration. The exit
criterion is a batch-atomic claim/serialization design with real PostgreSQL and
Redis multi-worker tests, not merely green unit tests or idempotent commands.

## TODO (HA hardening)

- **Batch-atomic claim or per-batch serialization** across workers, with real
  PostgreSQL/Redis concurrency tests; this is the prerequisite for replicas > 1.
- ~~**Commit B**: Worker `gw:key_balance` write → Lua revision-CAS; `revision` source → PG sequence~~ (B-1 + B-2 shipped 2026-05-26).
- ~~**Commit B-3**: `balanceDedupe` cache hit verifies against canonical Redis~~ (shipped 2026-05-26).
- **`/healthz` + `/readyz`** endpoints for K8s probes.
- **Per-pod metrics** with `instance_id` label so dashboards can split traffic distribution / hit rate.
- **Integration tests** for `OpKeyBalanceCAS` Lua path (needs miniredis dep added to the test target).
- **Migrate remaining `fillXxxDefaults` revision sources to PG sequence** if production indicates the wall-clock race is non-trivial for `PublishKey` / `PublishProvider` / etc.

// Package outbox is the persistence layer for publisher's at-least-once
// delivery guarantee.
//
// Lifecycle of one Publisher API call:
//
//  1. Sync path (this file's Insert*): validate input, build a slice of
//     OutboxRow values, INSERT them in one transaction. Return the
//     generated BatchID to the caller. This is the only PG write the
//     caller-facing path performs.
//
//  2. Async path (worker.go): poll for rows where status='pending' AND
//     next_attempt_at <= now(), claim them under FOR UPDATE SKIP LOCKED,
//     execute the Redis ops in seq order, mark delivered or failed (with
//     backoff) on completion.
//
// Delivery is at-least-once. If a worker stalls longer than its lease,
// another worker may reclaim and deliver the same row. Every op must be
// idempotent or have downstream duplicate suppression.
//
// Schema is defined in migrations/0001_outbox.sql.
package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tokenplatform/publisher/internal/types"
)

// Status enumerates the lifecycle of a single outbox row.
type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusDelivered  Status = "delivered"
	StatusFailed     Status = "failed"
	StatusDeadLetter Status = "dead_letter"
)

const defaultClaimLeaseDuration = 30 * time.Second

// Op enumerates the Redis primitives a row may execute. Mirrors the CHECK
// constraint on publisher_outbox.op so adding a value here requires a
// migration.
type Op string

const (
	OpSet    Op = "set"
	OpSetNX  Op = "setnx"
	OpHSet   Op = "hset"
	OpHSetNX Op = "hsetnx"
	OpSAdd   Op = "sadd"
	OpSRem   Op = "srem"
	OpDel    Op = "del"
	OpHDel   Op = "hdel"
	// OpIncr increments redis_key and stashes the result on the worker so
	// the next OpPublish in the same batch can inject it as the notify
	// message revision. It is only for publisher config revision notices;
	// duplicate publishes are absorbed by gateway's monotonic revision
	// filter. Do not use OpIncr for billing or balance counters; balances
	// must use OpKeyBalanceCAS.
	OpIncr Op = "incr"
	// OpKeyBalanceCAS is a publisher-domain composite op that atomically
	// updates gw:key_balance + gw:key_balance_reason + gw:key_balance_revision
	// for one key_id with revision Compare-And-Swap. Used to prevent the
	// cross-batch ordering race when multiple publisher pods enqueue
	// back-to-back SetKeyBalance calls for the same key. See worker.go
	// luaKeyBalanceCAS for the Lua body. 2026-05-26 HA Commit B-2.
	//
	// RedisField = key_id (UUID v4). RedisKey unused (the 3 hash keys are
	// hardcoded in the worker, mirroring publisher_keys.go constants).
	// Payload is JSON-encoded keyBalanceCASPayload.
	OpKeyBalanceCAS Op = "key_balance_cas"
	// OpKeyActivateAtomic installs an active KeyMeta together with its
	// Billing-owned admission decision and clears any stale revoked marker in
	// one Redis Lua transaction. Keeping the activation in one outbox row is
	// intentional: workers claim rows independently, so a multi-row batch
	// cannot provide a cross-pod visibility barrier.
	OpKeyActivateAtomic Op = "key_activate_atomic"
	// OpKeyMetaStatus atomically patches the status/revision fields inside
	// one gw:keymeta JSON hash value. Used by SRE key enable/disable so the
	// KeyMeta status and revoked set cannot drift.
	OpKeyMetaStatus Op = "keymeta_status"
	// OpProductPolicySetCAS atomically writes gw:product_policies[field] only
	// when payload.revision is newer than the per-product revision sidecar.
	OpProductPolicySetCAS Op = "policy_set_cas"
	// OpProductPolicyDelCAS atomically HDELs gw:product_policies[field] and
	// records a tombstone revision only when the delete revision is newer.
	OpProductPolicyDelCAS Op = "policy_del_cas"
	// OpPublish sends the notify message after preceding batch writes have
	// landed. If an OpIncr precedes it in the same batch, the worker injects
	// that counter value as the message revision.
	OpPublish Op = "publish"
)

// OutboxRow is one Redis op (or one PUBLISH notification) belonging to a
// caller-issued batch. Seq orders ops within a batch so the worker
// preserves write-before-notify semantics.
type OutboxRow struct {
	ID              int64
	BatchID         types.BatchID
	Seq             int
	Op              Op
	RedisKey        string
	RedisField      string
	RedisMember     string
	Payload         []byte // marshalled JSON for SET/HSET values; raw bytes for PUBLISH messages
	SchemaVersion   int
	EnvelopeVersion int
	TTLSeconds      int
	NotifyChannel   string
	NotifyMessage   json.RawMessage
	MethodName      string // "PublishKey" / "SetKeyBalance" / ...
	DedupKey        string
	CallerService   string
	CallerTraceID   string
	// CallerRequestID is the per-HTTP-request unique id minted by the
	// publisher auth middleware (UUID v7). All outbox rows produced by a
	// single API call share the same value. Echoed back to the client in
	// the X-Request-Id response header so caller-side logs correlate to
	// publisher-side audit. 2026-05-21.
	CallerRequestID string
	// ActorID is the per-row attribution of "who triggered this write" at
	// the user-account granularity (vs CallerService which is just the
	// admin / billing / user / sre service tag). Currently filled with
	// the first 10 chars of the bearer token by the auth middleware —
	// minimal viable signal that can dedup distinct actors without
	// exposing the full token. 2026-05-22.
	ActorID         string
	Status          Status
	AttemptCount    int
	LastError       string
	DeliveryOutcome string
	NextAttemptAt   time.Time
	LeaseOwner      string
	LeaseUntil      *time.Time
	CreatedAt       time.Time
	DeliveredAt     *time.Time
}

// Store wraps the PG connection pool and exposes the outbox primitives
// the Publisher implementation (sync path) and the retry worker (async
// path) both need.
//
// All exported methods are safe for concurrent use.
type Store struct {
	pool *pgxpool.Pool
}

// BatchInsert is one logical outbox batch to persist. InsertBatches writes
// several of these under a single PG transaction while preserving each batch's
// own batch_id and seq ordering.
type BatchInsert struct {
	BatchID types.BatchID
	Rows    []OutboxRow
}

// NewStore wires a Store to an existing pgxpool. Caller owns the pool's
// lifecycle; Store does not close it.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// InsertBatch atomically writes all rows to publisher_outbox under one
// shared batch_id. If any row fails to insert, the entire batch is rolled
// back and ErrOutboxUnavailable is returned (HTTP layer maps to 500
// OUTBOX_UNAVAILABLE, caller is contractually required to abort its
// upstream transaction).
//
// The returned BatchID is the value persisted on every row in the batch
// and is what the caller hands back to its own audit log / replay flow.
//
// Each row's NextAttemptAt is set to now() so the retry worker picks it
// up on its very next tick.
func (s *Store) InsertBatch(ctx context.Context, batchID types.BatchID, rows []OutboxRow) error {
	if len(rows) == 0 {
		return fmt.Errorf("outbox: refusing to insert empty batch")
	}
	return s.insertBatches(ctx, []BatchInsert{{BatchID: batchID, Rows: rows}}, false)
}

// InsertBatches atomically writes multiple logical outbox batches in one PG
// transaction. Each batch keeps its own batch_id, seq_in_batch sequence, and
// dedup_key contract; if any row in any batch fails, none of the batches are
// persisted.
func (s *Store) InsertBatches(ctx context.Context, batches []BatchInsert) error {
	if len(batches) == 0 {
		return fmt.Errorf("outbox: refusing to insert empty batch group")
	}
	for i, batch := range batches {
		if len(batch.Rows) == 0 {
			return fmt.Errorf("outbox: refusing to insert empty batch at index %d", i)
		}
	}
	return s.insertBatches(ctx, batches, true)
}

func (s *Store) insertBatches(ctx context.Context, batches []BatchInsert, includeBatchIndex bool) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("%w: begin: %v", ErrOutboxUnavailable, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	now := time.Now().UTC()
	for batchIndex, batch := range batches {
		for i, r := range batch.Rows {
			r.BatchID = batch.BatchID
			r.Seq = i
			if r.Status == "" {
				r.Status = StatusPending
			}
			if r.NextAttemptAt.IsZero() {
				r.NextAttemptAt = now
			}
			if r.CreatedAt.IsZero() {
				r.CreatedAt = now
			}
			tag, err := tx.Exec(ctx, insertSQL,
				r.BatchID, r.Seq, string(r.Op), nullStr(r.RedisKey), nullStr(r.RedisField), nullStr(r.RedisMember),
				r.Payload, r.SchemaVersion, r.EnvelopeVersion, r.TTLSeconds,
				nullStr(r.NotifyChannel), notifyMessageOrNil(r.NotifyMessage),
				r.MethodName, nullStr(r.DedupKey), nullStr(r.CallerService), nullStr(r.CallerTraceID),
				nullStr(r.CallerRequestID), nullStr(r.ActorID),
				string(r.Status), r.AttemptCount, nullStr(r.LastError),
				r.NextAttemptAt, r.CreatedAt,
			)
			if err != nil {
				if includeBatchIndex {
					return fmt.Errorf("%w: insert batch %d row %d: %v", ErrOutboxUnavailable, batchIndex, i, err)
				}
				return fmt.Errorf("%w: insert row %d: %v", ErrOutboxUnavailable, i, err)
			}
			if r.DedupKey != "" && tag.RowsAffected() == 0 {
				if includeBatchIndex {
					return fmt.Errorf("%w: insert batch %d row %d", ErrDedupConflict, batchIndex, i)
				}
				return fmt.Errorf("%w: insert row %d", ErrDedupConflict, i)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%w: commit: %v", ErrOutboxUnavailable, err)
	}
	return nil
}

// FindByDedupKey looks up an existing batch by Idempotency-Key. Returns the
// batch id + true if a hit, uuid.Nil + false if no match. Callers use this
// to short-circuit duplicate writes.
func (s *Store) FindByDedupKey(ctx context.Context, dedupKey string) (types.BatchID, bool, error) {
	row := s.pool.QueryRow(ctx, `SELECT batch_id FROM publisher_outbox WHERE dedup_key = $1 LIMIT 1`, dedupKey)
	var id uuid.UUID
	if err := row.Scan(&id); err != nil {
		if err == pgx.ErrNoRows {
			return uuid.Nil, false, nil
		}
		return uuid.Nil, false, err
	}
	return id, true, nil
}

// ClaimDue picks up to `limit` rows ripe for delivery (pending or failed,
// next_attempt_at <= now) and persists an in_progress lease before returning.
// FOR UPDATE SKIP LOCKED so multiple worker instances can run in parallel
// without lock contention.
func (s *Store) ClaimDue(ctx context.Context, limit int) ([]OutboxRow, error) {
	return s.ClaimDueWithLease(ctx, limit, uuid.NewString(), defaultClaimLeaseDuration)
}

// ClaimDueWithLease is ClaimDue with test/ops-visible lease knobs. Expired
// in_progress leases are claimable again so a crashed worker cannot strand
// rows forever.
func (s *Store) ClaimDueWithLease(ctx context.Context, limit int, leaseOwner string, leaseDuration time.Duration) ([]OutboxRow, error) {
	if leaseOwner == "" {
		leaseOwner = uuid.NewString()
	}
	if leaseDuration <= 0 {
		leaseDuration = defaultClaimLeaseDuration
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	leaseUntil := time.Now().UTC().Add(leaseDuration)
	rows, err := tx.Query(ctx, claimDueSQL, limit, leaseOwner, leaseUntil)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OutboxRow
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// MarkDelivered flips status='delivered' and stamps delivered_at, fenced by
// the active lease owner. It returns false when the row was already reclaimed
// by another worker; that is not a delivery error.
func (s *Store) MarkDelivered(ctx context.Context, id int64, leaseOwner string) (bool, error) {
	return s.MarkDeliveredWithOutcome(ctx, id, leaseOwner, "")
}

// MarkDeliveredWithOutcome persists the terminal Redis admission CAS result.
// Non-admission operations pass an empty outcome and retain NULL.
func (s *Store) MarkDeliveredWithOutcome(ctx context.Context, id int64, leaseOwner, outcome string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE publisher_outbox
         SET status='delivered', delivered_at=now(), delivery_outcome=NULLIF($3,''), lease_owner=NULL, lease_until=NULL
         WHERE id=$1 AND status='in_progress' AND lease_owner=$2`,
		id, leaseOwner, outcome,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// NextRevision returns the next value from publisher_revision_seq —
// a PG-side globally-monotonic counter shared across all publisher pods.
// Used as the revision source for write paths where the worker performs
// revision-CAS on the gateway side (currently gw:key_balance; expanding
// to other domains is a follow-up). One PG round-trip (~50-100µs on a
// warm sequence cache; ~50ns if the connection already holds cached
// values via CACHE 100).
//
// Sequence failure is returned to the synchronous caller. A wall-clock
// fallback is unsafe: one ~1e15 timestamp accepted as a legacy watermark
// would make every subsequent normal sequence value look stale indefinitely.
// The same PG dependency is required for the outbox insert immediately after
// this call, so failing closed does not introduce a new availability boundary.
//
// 2026-05-26 HA Commit B. Migration: 0005_revision_sequence.up.sql.
func (s *Store) NextRevision(ctx context.Context) (int64, error) {
	var rev int64
	if err := s.pool.QueryRow(ctx, `SELECT nextval('publisher_revision_seq')`).Scan(&rev); err != nil {
		return 0, fmt.Errorf("%w: next publisher revision: %v", ErrOutboxUnavailable, err)
	}
	return rev, nil
}

// MarkFailed records a transient failure and schedules the next retry per
// the supplied backoff, fenced by the active lease owner. attemptCount is
// incremented; if it reaches maxAttempts the row is moved to dead_letter and
// DeadLetterAlert hook fires (registered at worker setup). It returns false
// when the row was already reclaimed by another worker.
func (s *Store) MarkFailed(ctx context.Context, id int64, leaseOwner string, attempt int, maxAttempts int, errMsg string, nextAttempt time.Time) (bool, error) {
	status := StatusFailed
	if attempt >= maxAttempts {
		status = StatusDeadLetter
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE publisher_outbox
         SET status=$1, attempt_count=$2, last_error=$3, next_attempt_at=$4,
             lease_owner=NULL, lease_until=NULL
         WHERE id=$5 AND status='in_progress' AND lease_owner=$6`,
		string(status), attempt, errMsg, nextAttempt, id, leaseOwner,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// DeadLetterBatch quarantines every non-terminal row in a structurally invalid
// batch. Unlike MarkFailed, this is intentionally not lease-fenced: once a
// credential-bearing payload fails the immutable evidence contract, no worker
// may safely execute any sibling operation (especially a preceding DEL) from
// that batch.
// Delivered rows remain delivered so the audit history is not rewritten.
func (s *Store) DeadLetterBatch(ctx context.Context, batchID types.BatchID, errMsg string) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE publisher_outbox
	         SET status='dead_letter', attempt_count=GREATEST(attempt_count, 1),
	             last_error=$2, next_attempt_at=now(), delivery_outcome=NULL,
	             lease_owner=NULL, lease_until=NULL
	         WHERE batch_id=$1 AND status IN ('pending','failed','in_progress')`,
		batchID, errMsg,
	)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// PendingStats returns the current pending-row count and the age of the
// oldest pending row. Used by the metrics poller to keep the two outbox
// gauges current. When no rows are pending, oldestAge is 0.
//
// The query is a single round-trip against an index on (status,
// next_attempt_at), so cost stays bounded regardless of outbox size.
func (s *Store) PendingStats(ctx context.Context) (pending int64, oldestAge time.Duration, err error) {
	row := s.pool.QueryRow(ctx,
		`SELECT count(*),
		        coalesce(extract(epoch FROM (now() - min(created_at))), 0)::float8
		   FROM publisher_outbox
		  WHERE status = 'pending'`)
	var ageSec float64
	if err = row.Scan(&pending, &ageSec); err != nil {
		return 0, 0, err
	}
	if pending == 0 || ageSec < 0 {
		return pending, 0, nil
	}
	return pending, time.Duration(ageSec * float64(time.Second)), nil
}

// janitorLockKey namespaces the advisory lock that serialises janitor sweeps
// across worker replicas. Every replica runs the same loop, and two of them
// deleting concurrently would hold row locks on the same terminal rows for no
// extra throughput.
const janitorLockKey = "publisher_outbox_janitor"

// Cleanup deletes at most limit terminal rows in one bounded statement:
// delivered rows older than retainDelivered and dead_letter rows older than
// retainDeadLetter. It returns the rows deleted, so a janitor can keep
// sweeping until a short batch proves the backlog is drained, and reports
// locked=false when another replica already holds the sweep.
//
// The bound is not cosmetic. This function had no caller at all until the
// janitor was wired up, so the first sweep meets whatever accumulated since
// the table was created -- 166,848 rows were already eligible at a 7-day
// window in one production environment. An unbounded DELETE would make that a
// single long transaction and one WAL burst.
//
// Rows are taken in id order: the primary key is monotonic, so it approximates
// insertion order and lets the scan stop as soon as limit old rows are found,
// instead of sorting the whole eligible set.
func (s *Store) Cleanup(
	ctx context.Context,
	retainDelivered, retainDeadLetter time.Duration,
	limit int,
) (deleted int64, locked bool, err error) {
	if limit <= 0 {
		return 0, true, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var acquired bool
	if err := tx.QueryRow(ctx,
		`SELECT pg_try_advisory_xact_lock(hashtext($1))`, janitorLockKey,
	).Scan(&acquired); err != nil {
		return 0, false, err
	}
	if !acquired {
		return 0, false, nil
	}

	res, err := tx.Exec(ctx,
		`DELETE FROM publisher_outbox
         WHERE id IN (
             SELECT id FROM publisher_outbox
             WHERE (status='delivered' AND delivered_at < $1)
                OR (status='dead_letter' AND created_at < $2)
             ORDER BY id
             LIMIT $3
         )`,
		time.Now().Add(-retainDelivered),
		time.Now().Add(-retainDeadLetter),
		limit,
	)
	if err != nil {
		return 0, true, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, true, err
	}
	return res.RowsAffected(), true, nil
}

// QueryBatch returns the persisted rows for one outbox batch in delivery
// order. Admission delivery receipts aggregate this durable state rather than
// treating the synchronous 202 response as proof Redis was updated.
func (s *Store) QueryBatch(ctx context.Context, batchID types.BatchID) ([]OutboxRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT
    id, batch_id, seq_in_batch, op,
    coalesce(redis_key,''), coalesce(redis_field,''), coalesce(redis_member,''),
    payload, schema_version, envelope_version, ttl_seconds,
    coalesce(notify_channel,''), coalesce(notify_message::text, ''),
    method_name, coalesce(dedup_key,''), coalesce(caller_service,''), coalesce(caller_trace_id,''),
    coalesce(caller_request_id,''), coalesce(actor_id,''),
    status, attempt_count, coalesce(last_error,''), coalesce(delivery_outcome,''),
    next_attempt_at, coalesce(lease_owner,''), lease_until, created_at, delivered_at
FROM publisher_outbox
WHERE batch_id=$1
ORDER BY seq_in_batch, id`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []OutboxRow
	for rows.Next() {
		row, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// ----- helpers -----

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func notifyMessageOrNil(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

const insertSQL = `
INSERT INTO publisher_outbox (
    batch_id, seq_in_batch, op,
    redis_key, redis_field, redis_member, payload,
    schema_version, envelope_version, ttl_seconds,
    notify_channel, notify_message,
    method_name, dedup_key, caller_service, caller_trace_id, caller_request_id, actor_id,
    status, attempt_count, last_error,
    next_attempt_at, created_at
) VALUES (
    $1, $2, $3,
    $4, $5, $6, $7,
    $8, $9, $10,
    $11, $12,
    $13, $14, $15, $16, $17, $18,
    $19, $20, $21,
    $22, $23
)
ON CONFLICT (dedup_key) WHERE dedup_key IS NOT NULL DO NOTHING
`

const claimDueSQL = `
WITH due AS (
    SELECT id
    FROM publisher_outbox
    WHERE (
        status IN ('pending','failed') AND next_attempt_at <= now()
    ) OR (
        status = 'in_progress' AND coalesce(lease_until, '-infinity'::timestamptz) <= now()
    )
    ORDER BY id
    FOR UPDATE SKIP LOCKED
    LIMIT $1
)
UPDATE publisher_outbox p
SET status='in_progress',
    lease_owner=$2,
    lease_until=$3,
    next_attempt_at=$3
FROM due
WHERE p.id = due.id
RETURNING
    p.id, p.batch_id, p.seq_in_batch, p.op,
    coalesce(p.redis_key,''), coalesce(p.redis_field,''), coalesce(p.redis_member,''),
    p.payload, p.schema_version, p.envelope_version, p.ttl_seconds,
    coalesce(p.notify_channel,''), coalesce(p.notify_message::text, ''),
    p.method_name, coalesce(p.dedup_key,''), coalesce(p.caller_service,''), coalesce(p.caller_trace_id,''),
    coalesce(p.caller_request_id,''), coalesce(p.actor_id,''),
    p.status, p.attempt_count, coalesce(p.last_error,''), coalesce(p.delivery_outcome,''),
    p.next_attempt_at, coalesce(p.lease_owner,''), p.lease_until, p.created_at, p.delivered_at
`

func scanRow(rows pgx.Rows) (OutboxRow, error) {
	var r OutboxRow
	var op string
	var status string
	var notifyMsg string
	var leaseUntil sql.NullTime
	var deliveredAt sql.NullTime
	err := rows.Scan(
		&r.ID, &r.BatchID, &r.Seq, &op,
		&r.RedisKey, &r.RedisField, &r.RedisMember,
		&r.Payload, &r.SchemaVersion, &r.EnvelopeVersion, &r.TTLSeconds,
		&r.NotifyChannel, &notifyMsg,
		&r.MethodName, &r.DedupKey, &r.CallerService, &r.CallerTraceID,
		&r.CallerRequestID, &r.ActorID,
		&status, &r.AttemptCount, &r.LastError, &r.DeliveryOutcome,
		&r.NextAttemptAt, &r.LeaseOwner, &leaseUntil, &r.CreatedAt, &deliveredAt,
	)
	if err != nil {
		return r, err
	}
	r.Op = Op(op)
	r.Status = Status(status)
	if leaseUntil.Valid {
		t := leaseUntil.Time
		r.LeaseUntil = &t
	}
	if notifyMsg != "" {
		r.NotifyMessage = json.RawMessage(notifyMsg)
	}
	if deliveredAt.Valid {
		t := deliveredAt.Time
		r.DeliveredAt = &t
	}
	return r, nil
}

// ErrOutboxUnavailable signals the sync path could not persist the batch.
// The HTTP layer maps it to 500 OUTBOX_UNAVAILABLE; the caller is
// contractually required to abort its upstream transaction.
var ErrOutboxUnavailable = errOutboxUnavailable{}

// ErrDedupConflict signals row0's Idempotency-Key already belongs to another
// persisted batch. Publisher commit resolves it back to the original batch id.
var ErrDedupConflict = errors.New("outbox: dedup_key already exists")

type errOutboxUnavailable struct{}

func (errOutboxUnavailable) Error() string {
	return "outbox: persistence layer unavailable; caller must abort upstream tx"
}

package outbox

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tokenplatform/publisher/internal/dbinit"
	"github.com/tokenplatform/publisher/internal/types"
	"github.com/tokenplatform/publisher/migrations"
)

func TestInsertBatch_DedupConflictDoesNotCreateOrphanRows(t *testing.T) {
	store := newIntegrationStore(t)
	ctx := context.Background()
	dedupKey := "idem-" + uuid.NewString()
	originalBatch := types.BatchID(uuid.New())
	duplicateBatch := types.BatchID(uuid.New())

	if err := store.InsertBatch(ctx, originalBatch, integrationRows(dedupKey)); err != nil {
		t.Fatalf("insert original batch: %v", err)
	}
	if err := store.InsertBatch(ctx, duplicateBatch, integrationRows(dedupKey)); err == nil {
		t.Fatal("duplicate dedup_key batch insert returned nil; want conflict error")
	}
	existing, hit, err := store.FindByDedupKey(ctx, dedupKey)
	if err != nil {
		t.Fatalf("find original batch by dedup key: %v", err)
	}
	if !hit {
		t.Fatal("dedup lookup missed original batch")
	}
	if existing != originalBatch {
		t.Fatalf("dedup lookup returned batch %s, want original %s", existing, originalBatch)
	}

	var duplicateRows int
	if err := store.pool.QueryRow(ctx,
		`SELECT count(*) FROM publisher_outbox WHERE batch_id=$1`, duplicateBatch,
	).Scan(&duplicateRows); err != nil {
		t.Fatalf("count duplicate rows: %v", err)
	}
	if duplicateRows != 0 {
		t.Fatalf("duplicate batch left %d orphan rows; want 0", duplicateRows)
	}

	var orphanBatches int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT batch_id
			FROM publisher_outbox
			GROUP BY batch_id
			HAVING count(*) FILTER (WHERE seq_in_batch = 0) = 0
		) orphaned
	`).Scan(&orphanBatches); err != nil {
		t.Fatalf("count orphan batches: %v", err)
	}
	if orphanBatches != 0 {
		t.Fatalf("found %d batch(es) without seq0", orphanBatches)
	}
}

func TestDeadLetterBatchQuarantinesEveryNonTerminalSibling(t *testing.T) {
	store := newIntegrationStore(t)
	ctx := context.Background()
	batchID := types.BatchID(uuid.New())
	rows := integrationRows("")
	rows = append(rows, OutboxRow{
		Op:              OpHSet,
		RedisKey:        "gw:keypool:zhipu",
		RedisField:      "legacy",
		Payload:         []byte(`{"key_id":"legacy"}`),
		SchemaVersion:   1,
		EnvelopeVersion: 1,
		MethodName:      "PublishKeyPool",
	})
	for i := range rows {
		rows[i].MethodName = "PublishKeyPool"
	}
	if err := store.InsertBatch(ctx, batchID, rows); err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	if _, err := store.pool.Exec(ctx, `
		UPDATE publisher_outbox
		SET status = CASE seq_in_batch WHEN 0 THEN 'in_progress' WHEN 1 THEN 'failed' ELSE 'pending' END,
		    lease_owner = CASE seq_in_batch WHEN 0 THEN 'worker-a' ELSE NULL END,
		    lease_until = CASE seq_in_batch WHEN 0 THEN now() + interval '1 minute' ELSE NULL END
		WHERE batch_id=$1
	`, batchID); err != nil {
		t.Fatalf("seed mixed batch states: %v", err)
	}

	marked, err := store.DeadLetterBatch(ctx, batchID, "missing provider_account_ref")
	if err != nil {
		t.Fatalf("DeadLetterBatch: %v", err)
	}
	if marked != int64(len(rows)) {
		t.Fatalf("quarantined rows=%d want %d", marked, len(rows))
	}
	var nonDeadLetter int
	if err := store.pool.QueryRow(ctx, `
		SELECT count(*) FROM publisher_outbox
		WHERE batch_id=$1 AND (status <> 'dead_letter' OR lease_owner IS NOT NULL OR lease_until IS NOT NULL)
	`, batchID).Scan(&nonDeadLetter); err != nil {
		t.Fatalf("read quarantined rows: %v", err)
	}
	if nonDeadLetter != 0 {
		t.Fatalf("batch retained %d non-quarantined or leased rows", nonDeadLetter)
	}
}

func TestClaimDue_LeasesRowsAndPreventsDuplicateClaims(t *testing.T) {
	store := newIntegrationStore(t)
	ctx := context.Background()
	batchID := types.BatchID(uuid.New())
	if err := store.InsertBatch(ctx, batchID, integrationRows("")[:1]); err != nil {
		t.Fatalf("insert batch: %v", err)
	}

	first, err := store.ClaimDue(ctx, 1)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first claim returned %d rows, want 1", len(first))
	}
	second, err := store.ClaimDue(ctx, 1)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second claim returned row id %d while first lease is active", second[0].ID)
	}

	var status string
	if err := store.pool.QueryRow(ctx,
		`SELECT status FROM publisher_outbox WHERE id=$1`, first[0].ID,
	).Scan(&status); err != nil {
		t.Fatalf("read claimed row status: %v", err)
	}
	if status != "in_progress" {
		t.Fatalf("claimed row status = %q, want in_progress", status)
	}
}

func TestClaimDue_ExpiredLeaseCanBeReclaimed(t *testing.T) {
	store := newIntegrationStore(t)
	ctx := context.Background()
	batchID := types.BatchID(uuid.New())
	if err := store.InsertBatch(ctx, batchID, integrationRows("")[:1]); err != nil {
		t.Fatalf("insert batch: %v", err)
	}

	first, err := store.ClaimDue(ctx, 1)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first claim returned %d rows, want 1", len(first))
	}
	if _, err := store.pool.Exec(ctx, `
		UPDATE publisher_outbox
		SET lease_until=now() - interval '1 second',
		    next_attempt_at=now() - interval '1 second'
		WHERE id=$1
	`, first[0].ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	reclaimed, err := store.ClaimDue(ctx, 1)
	if err != nil {
		t.Fatalf("reclaim expired lease: %v", err)
	}
	if len(reclaimed) != 1 {
		t.Fatalf("expired lease claim returned %d rows, want 1", len(reclaimed))
	}
	if reclaimed[0].ID != first[0].ID {
		t.Fatalf("reclaimed row id = %d, want %d", reclaimed[0].ID, first[0].ID)
	}
}

func TestMarkDeliveredAndFailedRequireActiveLeaseOwner(t *testing.T) {
	store := newIntegrationStore(t)
	ctx := context.Background()

	t.Run("delivered", func(t *testing.T) {
		leased := claimRowThroughLeaseReclaim(t, ctx, store)

		marked, err := store.MarkDelivered(ctx, leased.row.ID, leased.staleOwner)
		if err != nil {
			t.Fatalf("stale owner mark delivered: %v", err)
		}
		if marked {
			t.Fatal("stale owner mark delivered reported success")
		}
		state := readOutboxRowState(t, ctx, store, leased.row.ID)
		if state.status != StatusInProgress || state.leaseOwner != leased.freshOwner || state.delivered {
			t.Fatalf("stale owner changed row: %+v, want in_progress leased to fresh owner", state)
		}

		marked, err = store.MarkDelivered(ctx, leased.row.ID, leased.freshOwner)
		if err != nil {
			t.Fatalf("fresh owner mark delivered: %v", err)
		}
		if !marked {
			t.Fatal("fresh owner mark delivered reported no rows")
		}
		state = readOutboxRowState(t, ctx, store, leased.row.ID)
		if state.status != StatusDelivered || state.leaseOwner != "" || !state.delivered {
			t.Fatalf("fresh owner state = %+v, want delivered with cleared lease", state)
		}
	})

	t.Run("failed", func(t *testing.T) {
		leased := claimRowThroughLeaseReclaim(t, ctx, store)
		nextAttemptAt := time.Now().UTC().Add(time.Minute)

		marked, err := store.MarkFailed(ctx, leased.row.ID, leased.staleOwner, 1, 3, "boom", nextAttemptAt)
		if err != nil {
			t.Fatalf("stale owner mark failed: %v", err)
		}
		if marked {
			t.Fatal("stale owner mark failed reported success")
		}
		state := readOutboxRowState(t, ctx, store, leased.row.ID)
		if state.status != StatusInProgress || state.leaseOwner != leased.freshOwner || state.attemptCount != 0 || state.lastError != "" {
			t.Fatalf("stale owner changed row: %+v, want in_progress leased to fresh owner", state)
		}

		marked, err = store.MarkFailed(ctx, leased.row.ID, leased.freshOwner, 1, 3, "boom", nextAttemptAt)
		if err != nil {
			t.Fatalf("fresh owner mark failed: %v", err)
		}
		if !marked {
			t.Fatal("fresh owner mark failed reported no rows")
		}
		state = readOutboxRowState(t, ctx, store, leased.row.ID)
		if state.status != StatusFailed || state.leaseOwner != "" || state.attemptCount != 1 || state.lastError != "boom" {
			t.Fatalf("fresh owner state = %+v, want failed attempt with cleared lease", state)
		}
	})
}

func integrationRows(dedupKey string) []OutboxRow {
	return []OutboxRow{
		{
			Op:              OpSet,
			RedisKey:        "gw:test:key",
			Payload:         []byte("v1"),
			SchemaVersion:   1,
			EnvelopeVersion: 1,
			MethodName:      "IntegrationTest",
			DedupKey:        dedupKey,
		},
		{
			Op:              OpPublish,
			NotifyChannel:   "gw:test:update",
			NotifyMessage:   []byte(`{"type":"test_update"}`),
			SchemaVersion:   1,
			EnvelopeVersion: 1,
			MethodName:      "IntegrationTest",
		},
	}
}

type reclaimedLeaseRow struct {
	row        OutboxRow
	staleOwner string
	freshOwner string
}

func claimRowThroughLeaseReclaim(t *testing.T, ctx context.Context, store *Store) reclaimedLeaseRow {
	t.Helper()
	batchID := types.BatchID(uuid.New())
	if err := store.InsertBatch(ctx, batchID, integrationRows("")[:1]); err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	if _, err := store.pool.Exec(ctx, `
		UPDATE publisher_outbox
		SET next_attempt_at=now() - interval '1 second'
		WHERE batch_id=$1
	`, batchID); err != nil {
		t.Fatalf("make row due: %v", err)
	}
	staleOwner := "stale-" + uuid.NewString()
	freshOwner := "fresh-" + uuid.NewString()

	stale, err := store.ClaimDueWithLease(ctx, 1, staleOwner, time.Minute)
	if err != nil {
		t.Fatalf("stale claim: %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("stale claim returned %d rows, want 1", len(stale))
	}
	if stale[0].LeaseOwner != staleOwner {
		t.Fatalf("stale claim lease_owner = %q, want %q", stale[0].LeaseOwner, staleOwner)
	}

	if _, err := store.pool.Exec(ctx, `
		UPDATE publisher_outbox
		SET lease_until=now() - interval '1 second',
		    next_attempt_at=now() - interval '1 second'
		WHERE id=$1
	`, stale[0].ID); err != nil {
		t.Fatalf("expire stale lease: %v", err)
	}

	fresh, err := store.ClaimDueWithLease(ctx, 1, freshOwner, time.Minute)
	if err != nil {
		t.Fatalf("fresh claim: %v", err)
	}
	if len(fresh) != 1 {
		t.Fatalf("fresh claim returned %d rows, want 1", len(fresh))
	}
	if fresh[0].ID != stale[0].ID {
		t.Fatalf("fresh claim row id = %d, want %d", fresh[0].ID, stale[0].ID)
	}
	if fresh[0].LeaseOwner != freshOwner {
		t.Fatalf("fresh claim lease_owner = %q, want %q", fresh[0].LeaseOwner, freshOwner)
	}
	return reclaimedLeaseRow{row: fresh[0], staleOwner: staleOwner, freshOwner: freshOwner}
}

type outboxRowState struct {
	status       Status
	leaseOwner   string
	attemptCount int
	lastError    string
	delivered    bool
}

func readOutboxRowState(t *testing.T, ctx context.Context, store *Store, id int64) outboxRowState {
	t.Helper()
	var state outboxRowState
	var status string
	if err := store.pool.QueryRow(ctx, `
		SELECT status, coalesce(lease_owner,''), attempt_count, coalesce(last_error,''), delivered_at IS NOT NULL
		FROM publisher_outbox
		WHERE id=$1
	`, id).Scan(&status, &state.leaseOwner, &state.attemptCount, &state.lastError, &state.delivered); err != nil {
		t.Fatalf("read row state: %v", err)
	}
	state.status = Status(status)
	return state
}

func newIntegrationStore(t *testing.T) *Store {
	t.Helper()
	baseDSN := os.Getenv("TP_PUBLISHER_PG_DSN")
	if baseDSN == "" {
		t.Skip("set TP_PUBLISHER_PG_DSN to run publisher outbox Postgres integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	cfg, err := pgx.ParseConfig(baseDSN)
	if err != nil {
		t.Fatalf("parse TP_PUBLISHER_PG_DSN: %v", err)
	}
	prefix := cfg.Database
	if prefix == "" {
		prefix = "tokenplatform"
	}
	cfg.Database = fmt.Sprintf("%s_%s", sanitizeDBIdent(prefix), strings.ReplaceAll(uuid.NewString(), "-", "_"))
	testDSN := cfg.ConnString()
	if err := dbinit.EnsureDatabase(ctx, testDSN); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() { dropTestDatabase(t, testDSN) })
	if err := dbinit.Migrate(ctx, testDSN, migrations.FS); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	pool, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewStore(pool)
}

func dropTestDatabase(t *testing.T, dsn string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Logf("parse cleanup dsn: %v", err)
		return
	}
	dbName := cfg.Database
	cfg.Database = "postgres"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Logf("connect cleanup database: %v", err)
		return
	}
	defer func() { _ = conn.Close(ctx) }()
	_, _ = conn.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1`, dbName)
	if _, err := conn.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, dbName)); err != nil {
		t.Logf("drop cleanup database %q: %v", dbName, err)
	}
}

func sanitizeDBIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "tokenplatform"
	}
	if len(out) > 24 {
		return out[:24]
	}
	return out
}

package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/tokenplatform/publisher/internal/types"
)

// ReplayFilter selects rows to re-attempt. BatchID is the precise mode
// (fetches one specific batch regardless of status); Since/Until +
// StatusIn is the window-scan mode. Exactly one of the two modes must be
// populated — the HTTP layer enforces that contract before calling.
type ReplayFilter struct {
	BatchID  *types.BatchID
	Since    *time.Time
	Until    *time.Time
	StatusIn []Status
	Limit    int
}

// QueryReplay fetches rows matching the filter without touching their
// status. Caller is expected to feed each row through Worker.runOne
// (effectively forcing re-delivery) and then MarkDelivered on success.
//
// Sorted by id ASC so a multi-row batch is delivered in seq order.
func (s *Store) QueryReplay(ctx context.Context, f ReplayFilter) ([]OutboxRow, error) {
	if f.Limit <= 0 || f.Limit > 10000 {
		return nil, fmt.Errorf("outbox: replay limit %d out of range [1,10000]", f.Limit)
	}
	args := []any{}
	where := ""
	switch {
	case f.BatchID != nil:
		where = "batch_id = $1"
		args = append(args, *f.BatchID)
	case f.Since != nil || f.Until != nil:
		clauses := []string{}
		if f.Since != nil {
			clauses = append(clauses, fmt.Sprintf("created_at >= $%d", len(args)+1))
			args = append(args, *f.Since)
		}
		if f.Until != nil {
			clauses = append(clauses, fmt.Sprintf("created_at < $%d", len(args)+1))
			args = append(args, *f.Until)
		}
		if len(f.StatusIn) > 0 {
			ph := make([]string, len(f.StatusIn))
			for i, st := range f.StatusIn {
				ph[i] = fmt.Sprintf("$%d", len(args)+1)
				args = append(args, string(st))
			}
			clauses = append(clauses, "status = ANY(ARRAY["+joinStrs(ph, ",")+"])")
		}
		where = joinStrs(clauses, " AND ")
	default:
		return nil, fmt.Errorf("outbox: replay filter requires batch_id or time window")
	}

	// IMPORTANT: column list MUST match scanRow's Scan(...) call in
	// outbox.go exactly — caller_request_id + actor_id were added to
	// scanRow (fb5cdc7 + 2a6de72) but this SELECT wasn't updated, which
	// made every replay query fail with "expected 25 destination arguments
	// in Scan, not 23".
	q := `
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
WHERE ` + where + `
ORDER BY id
LIMIT $` + fmt.Sprintf("%d", len(args)+1)
	args = append(args, f.Limit)

	rows, err := s.pool.Query(ctx, q, args...)
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
	return out, rows.Err()
}

// MarkPendingNow resets a row to pending + next_attempt_at=now so the
// worker picks it up immediately. Used by Replay when dry_run=false.
func (s *Store) MarkPendingNow(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE publisher_outbox SET status='pending', delivery_outcome=NULL, next_attempt_at=now(), lease_owner=NULL, lease_until=NULL WHERE id=$1`, id)
	return err
}

// CollectBatchIDs is a small helper for the Replay summary response.
func CollectBatchIDs(rows []OutboxRow) []types.BatchID {
	seen := make(map[uuid.UUID]struct{}, len(rows))
	out := make([]types.BatchID, 0, len(rows))
	for _, r := range rows {
		if _, ok := seen[r.BatchID]; ok {
			continue
		}
		seen[r.BatchID] = struct{}{}
		out = append(out, r.BatchID)
	}
	return out
}

func joinStrs(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += sep + p
	}
	return out
}

package publisher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/tokenplatform/publisher/internal/outbox"
	"github.com/tokenplatform/publisher/internal/types"
)

// PublishIntent persists every section produced by one compiled runtime intent
// in a single PG transaction. The worker-facing unit stays unchanged: each
// entity gets the same logical batch, method_name, revision row, and derived
// Idempotency-Key it would have received through the single-entity Publish*
// endpoint.
func (p *Impl) PublishIntent(ctx context.Context, req IntentPublishRequest) (IntentPublishResult, error) {
	caller := callerInfoFromCtx(ctx)
	if strings.TrimSpace(caller.IdempotencyID) == "" {
		return IntentPublishResult{}, fmt.Errorf("%w: idempotency key required", ErrInvalidArgument)
	}

	var result IntentPublishResult
	pending := make([]intentPendingBatch, 0,
		len(req.Providers)+len(req.KeyPools)+len(req.ModelCatalog)+len(req.Routes)+len(req.ProductPolicies)+len(req.ProductPolicyDeletes))

	add := func(dst *[]types.BatchID, method, section, entityID string, domain Domain, rows []outbox.OutboxRow) error {
		dedupKey := intentDedupKey(caller.IdempotencyID, section, entityID)
		if existing, hit, err := p.store.FindByDedupKey(ctx, dedupKey); err == nil && hit {
			slog.WarnContext(ctx, "publisher intent commit dedup hit",
				"event", "publisher_intent_commit_dedup_hit",
				"method", method,
				"dedup_key", dedupKey,
				"existing_batch_id", existing,
				"caller_request_id", caller.RequestID,
				"caller_trace_id", caller.TraceID,
				"actor_id", caller.ActorID,
			)
			*dst = append(*dst, existing)
			return nil
		} else if err != nil {
			slog.WarnContext(ctx, "publisher intent commit dedup lookup failed",
				"event", "publisher_intent_commit_dedup_lookup_failed",
				"method", method,
				"dedup_key", dedupKey,
				"caller_request_id", caller.RequestID,
				"caller_trace_id", caller.TraceID,
				"error", err,
			)
		}

		withRevision, err := rowsWithRevision(domain, rows)
		if err != nil {
			return err
		}
		batchID := uuid.New()
		stampIntentOutboxRows(withRevision, method, caller, dedupKey)
		resultIndex := len(*dst)
		*dst = append(*dst, batchID)
		pending = append(pending, intentPendingBatch{
			insert:      outbox.BatchInsert{BatchID: batchID, Rows: withRevision},
			method:      method,
			dedupKey:    dedupKey,
			resultSlice: dst,
			resultIndex: resultIndex,
		})
		return nil
	}

	for i := range req.Providers {
		provider := &req.Providers[i]
		rows, err := buildValidatedPublishProviderRows(provider.ID, provider)
		if err != nil {
			return result, err
		}
		if err := add(&result.ProviderBatches, "PublishProvider", "provider", provider.ID, DomainTopology, rows); err != nil {
			return result, err
		}
	}
	for i := range req.KeyPools {
		pool := &req.KeyPools[i]
		rows, err := buildPublishKeyPoolRows(pool.Meta.ProviderID, pool)
		if err != nil {
			return result, err
		}
		if err := add(&result.KeyPoolBatches, "PublishKeyPool", "keypool", pool.Meta.ProviderID, DomainCredential, rows); err != nil {
			return result, err
		}
	}
	for i := range req.ModelCatalog {
		entry := &req.ModelCatalog[i]
		rows, err := buildPublishModelCatalogRows(entry.ModelID, entry)
		if err != nil {
			return result, err
		}
		if err := add(&result.ModelCatalogBatches, "PublishModelCatalog", "model_catalog", entry.ModelID, DomainTopology, rows); err != nil {
			return result, err
		}
	}
	for i := range req.Routes {
		route := &req.Routes[i]
		rows, err := buildPublishModelRouteRows(ctx, route.ModelID, route)
		if err != nil {
			return result, err
		}
		if err := add(&result.ModelRouteBatches, "PublishModelRoute", "model_route", route.ModelID, DomainTopology, rows); err != nil {
			return result, err
		}
	}
	for i := range req.ProductPolicies {
		policy := &req.ProductPolicies[i]
		rows, err := buildPublishProductRuntimePolicyRows(policy.ProductID, policy)
		if err != nil {
			return result, err
		}
		if err := add(&result.ProductPolicyBatches, "PublishProductRuntimePolicy", "product_policy", policy.ProductID, DomainPlatform, rows); err != nil {
			return result, err
		}
	}
	for i := range req.ProductPolicyDeletes {
		deleted := req.ProductPolicyDeletes[i]
		rows, err := buildDeleteProductRuntimePolicyRows(deleted.ProductID, deleted.Revision)
		if err != nil {
			return result, err
		}
		if err := add(&result.ProductPolicyDeleteBatches, "DeleteProductRuntimePolicy", "product_policy_delete", deleted.ProductID, DomainPlatform, rows); err != nil {
			return result, err
		}
	}

	if len(pending) == 0 {
		return result, nil
	}
	inserts := intentPendingInserts(pending)

	slog.InfoContext(ctx, "publisher intent commit insert batches",
		"event", "publisher_intent_commit_insert_batches",
		"batch_count", len(pending),
		"row_count", intentPendingRowCount(inserts),
		"caller_request_id", caller.RequestID,
		"caller_trace_id", caller.TraceID,
		"actor_id", caller.ActorID,
	)
	if err := p.store.InsertBatches(ctx, inserts); err != nil {
		if errors.Is(err, outbox.ErrDedupConflict) && p.resolveIntentDedupConflict(ctx, pending, caller) {
			return result, nil
		}
		slog.ErrorContext(ctx, "publisher intent commit insert batches failed",
			"event", "publisher_intent_commit_insert_batches_failed",
			"batch_count", len(pending),
			"row_count", intentPendingRowCount(inserts),
			"caller_request_id", caller.RequestID,
			"caller_trace_id", caller.TraceID,
			"error", err,
		)
		return result, err
	}

	return result, nil
}

type intentPendingBatch struct {
	insert      outbox.BatchInsert
	method      string
	dedupKey    string
	resultSlice *[]types.BatchID
	resultIndex int
}

func intentDedupKey(base, section, entityID string) string {
	return base + "/" + section + "/" + entityID
}

func stampIntentOutboxRows(rows []outbox.OutboxRow, method string, caller CallerInfo, dedupKey string) {
	for i := range rows {
		rows[i].MethodName = method
		rows[i].CallerService = caller.Service
		rows[i].CallerTraceID = caller.TraceID
		rows[i].CallerRequestID = caller.RequestID
		rows[i].ActorID = caller.ActorID
		if i == 0 {
			rows[i].DedupKey = dedupKey
		}
	}
}

func intentPendingRowCount(batches []outbox.BatchInsert) int {
	total := 0
	for _, batch := range batches {
		total += len(batch.Rows)
	}
	return total
}

func intentPendingInserts(pending []intentPendingBatch) []outbox.BatchInsert {
	out := make([]outbox.BatchInsert, 0, len(pending))
	for _, batch := range pending {
		out = append(out, batch.insert)
	}
	return out
}

func (p *Impl) resolveIntentDedupConflict(ctx context.Context, pending []intentPendingBatch, caller CallerInfo) bool {
	resolved := make([]types.BatchID, 0, len(pending))
	for _, batch := range pending {
		existing, hit, err := p.store.FindByDedupKey(ctx, batch.dedupKey)
		if err != nil || !hit {
			if err != nil {
				slog.WarnContext(ctx, "publisher intent conflict lookup failed",
					"event", "publisher_intent_conflict_lookup_failed",
					"method", batch.method,
					"dedup_key", batch.dedupKey,
					"attempted_batch_id", batch.insert.BatchID,
					"caller_request_id", caller.RequestID,
					"caller_trace_id", caller.TraceID,
					"error", err,
				)
			}
			return false
		}
		resolved = append(resolved, existing)
	}
	for i, existing := range resolved {
		batch := pending[i]
		(*batch.resultSlice)[batch.resultIndex] = existing
		slog.WarnContext(ctx, "publisher intent insert dedup conflict",
			"event", "publisher_intent_insert_dedup_conflict",
			"method", batch.method,
			"dedup_key", batch.dedupKey,
			"attempted_batch_id", batch.insert.BatchID,
			"existing_batch_id", existing,
			"caller_request_id", caller.RequestID,
			"caller_trace_id", caller.TraceID,
		)
	}
	return true
}

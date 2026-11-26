package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/tokenplatform/publisher/internal/dbinit"
	intentcompiler "github.com/tokenplatform/publisher/internal/intent"
	"github.com/tokenplatform/publisher/internal/outbox"
	"github.com/tokenplatform/publisher/internal/publisher"
	"github.com/tokenplatform/publisher/internal/types"
	"github.com/tokenplatform/publisher/migrations"
)

func TestAdminPublishIntentDerivesIdempotencyPerEntityWithRealOutbox(t *testing.T) {
	store, pool := newAPIIntegrationStore(t)
	ctx := context.Background()
	h := newRealPublisherAdminServer(t, store)
	body := multiObjectIntentBody()
	baseKey := "f026-" + uuid.NewString()

	first := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", body, baseKey)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first intent publish status=%d body=%s", first.Code, first.Body.String())
	}
	firstResp := decodeIntentBatchResponse(t, first)
	assertIntentBatchShape(t, firstResp, 2, 2, 1, 1, 1)

	wantBatchesByMethod := map[string]int{
		"PublishProvider":             2,
		"PublishKeyPool":              2,
		"PublishModelCatalog":         1,
		"PublishModelRoute":           1,
		"PublishProductRuntimePolicy": 1,
	}
	if got := outboxBatchCountsByMethod(t, ctx, pool); !reflect.DeepEqual(got, wantBatchesByMethod) {
		t.Fatalf("outbox batch counts by method mismatch:\n got=%v\nwant=%v", got, wantBatchesByMethod)
	}

	wantDedupKeys := []string{
		baseKey + "/keypool/anthropic-prod",
		baseKey + "/keypool/openai-prod",
		baseKey + "/model_catalog/gpt-4o-mini",
		baseKey + "/model_route/gpt-4o-mini",
		baseKey + "/product_policy/chat-pro",
		baseKey + "/provider/anthropic-prod",
		baseKey + "/provider/openai-prod",
	}
	if got := outboxDedupKeys(t, ctx, pool); !reflect.DeepEqual(got, wantDedupKeys) {
		t.Fatalf("outbox dedup keys mismatch:\n got=%v\nwant=%v", got, wantDedupKeys)
	}

	rowsBeforeReplay := countOutboxRows(t, ctx, pool)
	replay := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", body, baseKey)
	if replay.Code != http.StatusAccepted {
		t.Fatalf("replay intent publish status=%d body=%s", replay.Code, replay.Body.String())
	}
	replayResp := decodeIntentBatchResponse(t, replay)
	if !reflect.DeepEqual(replayResp, firstResp) {
		t.Fatalf("replay batch response mismatch:\n got=%+v\nwant=%+v", replayResp, firstResp)
	}
	if rowsAfterReplay := countOutboxRows(t, ctx, pool); rowsAfterReplay != rowsBeforeReplay {
		t.Fatalf("replay added outbox rows: before=%d after=%d", rowsBeforeReplay, rowsAfterReplay)
	}
}

func TestAdminPublishIntentProductPolicyDeleteWithRealOutbox(t *testing.T) {
	store, pool := newAPIIntegrationStore(t)
	ctx := context.Background()
	h := newRealPublisherAdminServer(t, store)
	baseKey := "delete-policy-" + uuid.NewString()

	resp := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", productPolicyDeleteIntentBody(), baseKey)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("delete intent status=%d body=%s", resp.Code, resp.Body.String())
	}
	decoded := decodeIntentBatchResponse(t, resp)
	if len(decoded.ProductPolicyDeleteBatches) != 1 {
		t.Fatalf("delete batches=%v want one", decoded.ProductPolicyDeleteBatches)
	}

	wantBatchesByMethod := map[string]int{
		"DeleteProductRuntimePolicy": 1,
	}
	if got := outboxBatchCountsByMethod(t, ctx, pool); !reflect.DeepEqual(got, wantBatchesByMethod) {
		t.Fatalf("outbox batch counts by method mismatch:\n got=%v\nwant=%v", got, wantBatchesByMethod)
	}
	wantDedupKeys := []string{
		baseKey + "/product_policy_delete/prod_coding",
	}
	if got := outboxDedupKeys(t, ctx, pool); !reflect.DeepEqual(got, wantDedupKeys) {
		t.Fatalf("outbox dedup keys mismatch:\n got=%v\nwant=%v", got, wantDedupKeys)
	}
}

func TestAdminPublishIntentProductPolicyDeleteDrainsToRedis(t *testing.T) {
	store, _ := newAPIIntegrationStore(t)
	rdb := newAPIIntegrationRedis(t)
	ctx := context.Background()
	h := newRealPublisherAdminServer(t, store)
	productID := "prod_delete_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	cleanupRedisProductPolicy(t, ctx, rdb, productID)

	workerCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	worker := outbox.NewWorker(store, rdb, outbox.WorkerConfig{
		ScanInterval: 10 * time.Millisecond,
		BatchPerTick: 100,
		BaseDelay:    10 * time.Millisecond,
		MaxDelay:     50 * time.Millisecond,
	}, nil)
	go worker.Run(workerCtx)

	revBefore := redisIntKey(t, ctx, rdb, publisher.KeyPlatformRevision)
	upsert := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", productPolicyUpsertIntentBody(productID, 100), "redis-upsert-"+productID)
	if upsert.Code != http.StatusAccepted {
		t.Fatalf("upsert intent status=%d body=%s", upsert.Code, upsert.Body.String())
	}
	upsertState := waitForRedisState(t, 5*time.Second, func() (bool, string) {
		raw, err := rdb.HGet(ctx, publisher.KeyProductPolicies, productID).Result()
		if err != nil {
			return false, fmt.Sprintf("hget product policy: %v", err)
		}
		rev := redisIntKey(t, ctx, rdb, publisher.KeyPlatformRevision)
		sidecar := redisHashValue(t, ctx, rdb, publisher.KeyProductPolicyRevisions, productID)
		return rev > revBefore && sidecar == "100", fmt.Sprintf("exists=true platform_rev=%d sidecar=%s policy=%s", rev, sidecar, raw)
	})
	revAfterUpsert := redisIntKey(t, ctx, rdb, publisher.KeyPlatformRevision)
	t.Logf("after upsert: product_id=%s %s", productID, upsertState)

	deleteBody := productPolicyDeleteIntentBodyFor(productID, 101)
	deleteKey := "redis-delete-" + productID
	deleted := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", deleteBody, deleteKey)
	if deleted.Code != http.StatusAccepted {
		t.Fatalf("delete intent status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	deleteState := waitForRedisState(t, 5*time.Second, func() (bool, string) {
		exists := redisHashExists(t, ctx, rdb, publisher.KeyProductPolicies, productID)
		rev := redisIntKey(t, ctx, rdb, publisher.KeyPlatformRevision)
		sidecar := redisHashValue(t, ctx, rdb, publisher.KeyProductPolicyRevisions, productID)
		return !exists && rev > revAfterUpsert && sidecar == "101", fmt.Sprintf("exists=%v platform_rev=%d sidecar=%s", exists, rev, sidecar)
	})
	revAfterDelete := redisIntKey(t, ctx, rdb, publisher.KeyPlatformRevision)
	t.Logf("after delete: product_id=%s %s", productID, deleteState)

	replayDelete := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", deleteBody, deleteKey)
	if replayDelete.Code != http.StatusAccepted {
		t.Fatalf("delete replay status=%d body=%s", replayDelete.Code, replayDelete.Body.String())
	}
	time.Sleep(50 * time.Millisecond)
	if rev := redisIntKey(t, ctx, rdb, publisher.KeyPlatformRevision); rev != revAfterDelete {
		t.Fatalf("idempotent delete replay changed platform revision: before=%d after=%d", revAfterDelete, rev)
	}
	if exists := redisHashExists(t, ctx, rdb, publisher.KeyProductPolicies, productID); exists {
		t.Fatalf("idempotent delete replay resurrected product policy %s", productID)
	}
	t.Logf("after idempotent replay: product_id=%s exists=false platform_rev=%d sidecar=%s", productID, revAfterDelete, redisHashValue(t, ctx, rdb, publisher.KeyProductPolicyRevisions, productID))

	staleUpsert := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", productPolicyUpsertIntentBody(productID, 200), "redis-upsert-new-"+productID)
	if staleUpsert.Code != http.StatusAccepted {
		t.Fatalf("new upsert intent status=%d body=%s", staleUpsert.Code, staleUpsert.Body.String())
	}
	waitForRedisState(t, 5*time.Second, func() (bool, string) {
		exists := redisHashExists(t, ctx, rdb, publisher.KeyProductPolicies, productID)
		sidecar := redisHashValue(t, ctx, rdb, publisher.KeyProductPolicyRevisions, productID)
		return exists && sidecar == "200", fmt.Sprintf("exists=%v sidecar=%s", exists, sidecar)
	})
	revAfterNewUpsert := redisIntKey(t, ctx, rdb, publisher.KeyPlatformRevision)

	staleDelete := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", productPolicyDeleteIntentBodyFor(productID, 150), "redis-stale-delete-"+productID)
	if staleDelete.Code != http.StatusAccepted {
		t.Fatalf("stale delete intent status=%d body=%s", staleDelete.Code, staleDelete.Body.String())
	}
	staleState := waitForRedisState(t, 5*time.Second, func() (bool, string) {
		exists := redisHashExists(t, ctx, rdb, publisher.KeyProductPolicies, productID)
		rev := redisIntKey(t, ctx, rdb, publisher.KeyPlatformRevision)
		sidecar := redisHashValue(t, ctx, rdb, publisher.KeyProductPolicyRevisions, productID)
		return exists && rev > revAfterNewUpsert && sidecar == "200", fmt.Sprintf("exists=%v platform_rev=%d sidecar=%s", exists, rev, sidecar)
	})
	t.Logf("after stale delete: product_id=%s %s", productID, staleState)
}

func TestAdminPublishIntentOutboxFailureRollsBackAllSectionsAndRetrySucceeds(t *testing.T) {
	store, pool := newAPIIntegrationStore(t)
	ctx := context.Background()
	installOneShotModelCatalogOutboxFailure(t, ctx, pool)
	h := newRealPublisherAdminServer(t, store)
	body := goldenIntentBody()
	baseKey := "intent-atomic-" + uuid.NewString()

	first := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", body, baseKey)
	t.Logf("first injected-failure response: status=%d body=%s", first.Code, strings.TrimSpace(first.Body.String()))
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first intent publish status=%d want 500 body=%s", first.Code, first.Body.String())
	}
	assertErrorEnvelope(t, first, "OUTBOX_UNAVAILABLE", "publisher persistence layer error")

	firstDedupKeys := outboxDedupKeys(t, ctx, pool)
	t.Logf("dedup keys after first request: %s", mustJSON(t, firstDedupKeys))
	if len(firstDedupKeys) != 0 {
		t.Fatalf("first request should have rolled back all dedup keys, got=%v", firstDedupKeys)
	}
	if rowsAfterFirst := countOutboxRows(t, ctx, pool); rowsAfterFirst != 0 {
		t.Fatalf("first request should have rolled back all outbox rows, got=%d", rowsAfterFirst)
	}

	retry := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", body, baseKey)
	if retry.Code != http.StatusAccepted {
		t.Fatalf("retry intent publish status=%d body=%s", retry.Code, retry.Body.String())
	}
	retryResp := decodeIntentBatchResponse(t, retry)
	assertIntentBatchShape(t, retryResp, 1, 1, 1, 1, 1)
	assertNoEmptyIntentBatchIDs(t, retryResp)

	wantFinalDedupKeys := []string{
		baseKey + "/keypool/openai-prod",
		baseKey + "/model_catalog/gpt-4o-mini",
		baseKey + "/model_route/gpt-4o-mini",
		baseKey + "/product_policy/chat-pro",
		baseKey + "/provider/openai-prod",
	}
	finalDedupKeys := outboxDedupKeys(t, ctx, pool)
	t.Logf("retry response: status=%d body=%s", retry.Code, strings.TrimSpace(retry.Body.String()))
	t.Logf("dedup keys after retry: %s", mustJSON(t, finalDedupKeys))
	if !reflect.DeepEqual(finalDedupKeys, wantFinalDedupKeys) {
		t.Fatalf("dedup keys after retry mismatch:\n got=%v\nwant=%v", finalDedupKeys, wantFinalDedupKeys)
	}
	wantFinalBatchesByMethod := map[string]int{
		"PublishKeyPool":              1,
		"PublishModelCatalog":         1,
		"PublishModelRoute":           1,
		"PublishProductRuntimePolicy": 1,
		"PublishProvider":             1,
	}
	if got := outboxBatchCountsByMethod(t, ctx, pool); !reflect.DeepEqual(got, wantFinalBatchesByMethod) {
		t.Fatalf("method batch counts after retry mismatch:\n got=%v\nwant=%v", got, wantFinalBatchesByMethod)
	}

	artifacts := collectRuntimeArtifactsForBatches(t, ctx, pool, retryResp.allBatches())
	assertGoldenRuntimeArtifactsPresent(t, artifacts)
}

func TestAdminPublishIntentRejectsInvalidRequestsBeforeOutboxWrite(t *testing.T) {
	store, pool := newAPIIntegrationStore(t)
	ctx := context.Background()
	h := newRealPublisherAdminServer(t, store)

	cases := []struct {
		name            string
		body            string
		idempotencyKey  string
		messageContains string
	}{
		{
			name:            "missing_idempotency_key",
			body:            validIntentBody(),
			messageContains: "Idempotency-Key",
		},
		{
			name:            "unknown_provider_endpoint_field",
			body:            strings.Replace(validIntentBody(), `"key_strategy": "priority"`, `"key_strategy": "priority", "bogus_provider_endpoint": true`, 1),
			idempotencyKey:  "invalid-provider-field",
			messageContains: "bogus_provider_endpoint",
		},
		{
			name:            "unknown_provider_credential_field",
			body:            strings.Replace(validIntentBody(), `"key_id": "key-a",`, `"key_id": "key-a", "bogus_provider_credential": true,`, 1),
			idempotencyKey:  "invalid-credential-field",
			messageContains: "bogus_provider_credential",
		},
		{
			name:            "unknown_product_policy_field",
			body:            strings.Replace(validIntentBody(), `"billing_mode": "token_plan_quota",`, `"billing_mode": "token_plan_quota", "bogus_product_policy": true,`, 1),
			idempotencyKey:  "invalid-policy-field",
			messageContains: "bogus_product_policy",
		},
		{
			name:            "future_schema_version",
			body:            strings.Replace(validIntentBody(), `"schema_version": 2`, `"schema_version": 3`, 1),
			idempotencyKey:  "invalid-schema-2",
			messageContains: "unsupported intent schema_version 3",
		},
		{
			name:            "default_schema_version_zero",
			body:            strings.Replace(validIntentBody(), `"schema_version": 2,`, ``, 1),
			idempotencyKey:  "invalid-schema-zero",
			messageContains: "unsupported intent schema_version 0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rowsBefore := countOutboxRows(t, ctx, pool)
			w := adminPostMaybeIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", tc.body, tc.idempotencyKey)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want 400 body=%s", w.Code, w.Body.String())
			}
			assertInvalidArgumentEnvelope(t, w, tc.messageContains)
			if rowsAfter := countOutboxRows(t, ctx, pool); rowsAfter != rowsBefore {
				t.Fatalf("invalid request wrote outbox rows: before=%d after=%d", rowsBefore, rowsAfter)
			}
		})
	}
}

func TestAdminPublishIntentAndFinishedConfigArtifactsAreEquivalent(t *testing.T) {
	store, pool := newAPIIntegrationStore(t)
	ctx := context.Background()
	h := newRealPublisherAdminServer(t, store)

	firstIntent := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", goldenIntentBody(), "golden-intent-1")
	if firstIntent.Code != http.StatusAccepted {
		t.Fatalf("first intent status=%d body=%s", firstIntent.Code, firstIntent.Body.String())
	}
	firstArtifacts := collectRuntimeArtifactsForBatches(t, ctx, pool, decodeIntentBatchResponse(t, firstIntent).allBatches())

	legacyBatchIDs := publishLegacyFinishedConfig(t, h, "golden-legacy")
	legacyArtifacts := collectRuntimeArtifactsForBatches(t, ctx, pool, legacyBatchIDs)
	if !reflect.DeepEqual(firstArtifacts, legacyArtifacts) {
		t.Fatalf("intent and legacy artifacts differ:\nintent=%s\nlegacy=%s", mustJSON(t, firstArtifacts), mustJSON(t, legacyArtifacts))
	}

	secondIntent := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", goldenIntentBody(), "golden-intent-2")
	if secondIntent.Code != http.StatusAccepted {
		t.Fatalf("second intent status=%d body=%s", secondIntent.Code, secondIntent.Body.String())
	}
	secondArtifacts := collectRuntimeArtifactsForBatches(t, ctx, pool, decodeIntentBatchResponse(t, secondIntent).allBatches())
	if !reflect.DeepEqual(legacyArtifacts, secondArtifacts) {
		t.Fatalf("intent -> legacy -> intent drifted artifacts:\nlegacy=%s\nfinal=%s", mustJSON(t, legacyArtifacts), mustJSON(t, secondArtifacts))
	}
}

type intentBatchResponse struct {
	Status                     string   `json:"status"`
	SchemaVersion              int      `json:"schema_version"`
	ProviderBatches            []string `json:"provider_batches"`
	KeyPoolBatches             []string `json:"keypool_batches"`
	ModelCatalogBatches        []string `json:"model_catalog_batches"`
	ModelRouteBatches          []string `json:"model_route_batches"`
	ProductPolicyBatches       []string `json:"product_policy_batches"`
	ProductPolicyDeleteBatches []string `json:"product_policy_delete_batches"`
}

func (r intentBatchResponse) allBatches() []string {
	out := make([]string, 0, len(r.ProviderBatches)+len(r.KeyPoolBatches)+len(r.ModelCatalogBatches)+len(r.ModelRouteBatches)+len(r.ProductPolicyBatches)+len(r.ProductPolicyDeleteBatches))
	out = append(out, r.ProviderBatches...)
	out = append(out, r.KeyPoolBatches...)
	out = append(out, r.ModelCatalogBatches...)
	out = append(out, r.ModelRouteBatches...)
	out = append(out, r.ProductPolicyBatches...)
	out = append(out, r.ProductPolicyDeleteBatches...)
	return out
}

func decodeIntentBatchResponse(t *testing.T, w *httptest.ResponseRecorder) intentBatchResponse {
	t.Helper()
	var resp intentBatchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode intent response: %v body=%s", err, w.Body.String())
	}
	return resp
}

func assertIntentBatchShape(t *testing.T, resp intentBatchResponse, providers, keyPools, catalog, routes, policies int) {
	t.Helper()
	if resp.Status != "accepted" || resp.SchemaVersion != intentcompiler.CurrentIntentSchemaVersion {
		t.Fatalf("unexpected response status/schema: %+v", resp)
	}
	if len(resp.ProviderBatches) != providers || len(resp.KeyPoolBatches) != keyPools ||
		len(resp.ModelCatalogBatches) != catalog || len(resp.ModelRouteBatches) != routes ||
		len(resp.ProductPolicyBatches) != policies {
		t.Fatalf("unexpected batch shape: %+v", resp)
	}
}

func assertNoEmptyIntentBatchIDs(t *testing.T, resp intentBatchResponse) {
	t.Helper()
	for _, id := range resp.allBatches() {
		if id == "" {
			t.Fatalf("response included an empty batch id: %+v", resp)
		}
	}
}

func assertErrorEnvelope(t *testing.T, w *httptest.ResponseRecorder, code, message string) {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error envelope: %v body=%s", err, w.Body.String())
	}
	if body.Error.Code != code {
		t.Fatalf("error.code=%q want %q body=%s", body.Error.Code, code, w.Body.String())
	}
	if message != "" && body.Error.Message != message {
		t.Fatalf("error.message=%q want %q", body.Error.Message, message)
	}
}

func newRealPublisherAdminServer(t *testing.T, store *outbox.Store) http.Handler {
	t.Helper()
	return NewServer(publisher.New(store), fixtureAuth(t)).Handler()
}

func adminPostMaybeIdempotencyKey(t *testing.T, h http.Handler, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	if key != "" {
		return adminPostWithIdempotencyKey(t, h, path, body, key)
	}
	return adminPost(t, h, path, body)
}

func adminPutWithIdempotencyKey(t *testing.T, h http.Handler, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func publishLegacyFinishedConfig(t *testing.T, h http.Handler, keyPrefix string) []string {
	t.Helper()
	calls := []struct {
		path string
		body string
		key  string
	}{
		{"/admin/api/v1/providers/openai-prod", legacyProviderBody(), keyPrefix + "/provider/openai-prod"},
		{"/admin/api/v1/key-pools/openai-prod", legacyKeyPoolBody(), keyPrefix + "/keypool/openai-prod"},
		{"/admin/api/v1/models/gpt-4o-mini/catalog", legacyModelCatalogBody(), keyPrefix + "/model_catalog/gpt-4o-mini"},
		{"/admin/api/v1/models/gpt-4o-mini/route", legacyModelRouteBody(), keyPrefix + "/model_route/gpt-4o-mini"},
		{"/admin/api/v1/product-policies/chat-pro", legacyProductPolicyBody(), keyPrefix + "/product_policy/chat-pro"},
	}

	batchIDs := make([]string, 0, len(calls))
	for _, call := range calls {
		w := adminPutWithIdempotencyKey(t, h, call.path, call.body, call.key)
		if w.Code != http.StatusAccepted {
			t.Fatalf("legacy publish %s status=%d body=%s", call.path, w.Code, w.Body.String())
		}
		var resp struct {
			Status  string `json:"status"`
			BatchID string `json:"batch_id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode legacy response %s: %v body=%s", call.path, err, w.Body.String())
		}
		if resp.Status != "accepted" || resp.BatchID == "" {
			t.Fatalf("unexpected legacy response %s: %+v", call.path, resp)
		}
		batchIDs = append(batchIDs, resp.BatchID)
	}
	return batchIDs
}

func outboxBatchCountsByMethod(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT method_name, count(DISTINCT batch_id) FROM publisher_outbox GROUP BY method_name`)
	if err != nil {
		t.Fatalf("query outbox method counts: %v", err)
	}
	defer rows.Close()

	got := make(map[string]int)
	for rows.Next() {
		var method string
		var count int
		if err := rows.Scan(&method, &count); err != nil {
			t.Fatalf("scan method count: %v", err)
		}
		got[method] = count
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate method counts: %v", err)
	}
	return got
}

func outboxDedupKeys(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT dedup_key FROM publisher_outbox WHERE dedup_key IS NOT NULL AND dedup_key <> '' ORDER BY dedup_key`)
	if err != nil {
		t.Fatalf("query outbox dedup keys: %v", err)
	}
	defer rows.Close()

	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan dedup key: %v", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate dedup keys: %v", err)
	}
	return keys
}

func countOutboxRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM publisher_outbox`).Scan(&count); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	return count
}

type runtimeArtifacts struct {
	Provider     types.ProviderInfo         `json:"provider"`
	KeyPool      types.KeyPoolConfig        `json:"key_pool"`
	ModelCatalog types.ModelCatalogEntry    `json:"model_catalog"`
	Route        types.RouteConfig          `json:"route"`
	Policy       types.ProductRuntimePolicy `json:"policy"`
}

func collectRuntimeArtifactsForBatches(t *testing.T, ctx context.Context, pool *pgxpool.Pool, batchIDs []string) runtimeArtifacts {
	t.Helper()
	if len(batchIDs) == 0 {
		t.Fatal("cannot collect runtime artifacts for empty batch set")
	}
	rows, err := pool.Query(ctx, `
		SELECT redis_key, redis_field, payload
		FROM publisher_outbox
		WHERE batch_id::text = ANY($1) AND payload IS NOT NULL
		ORDER BY id
	`, batchIDs)
	if err != nil {
		t.Fatalf("query runtime artifact rows: %v", err)
	}
	defer rows.Close()

	var artifacts runtimeArtifacts
	for rows.Next() {
		var redisKey, redisField string
		var payload []byte
		if err := rows.Scan(&redisKey, &redisField, &payload); err != nil {
			t.Fatalf("scan artifact row: %v", err)
		}
		switch redisKey {
		case publisher.KeyProviders:
			if redisField == "openai-prod" {
				mustUnmarshalPayload(t, payload, &artifacts.Provider)
			}
		case publisher.KeyKeyPoolMeta:
			if redisField == "openai-prod" {
				mustUnmarshalPayload(t, payload, &artifacts.KeyPool.Meta)
			}
		case publisher.KeyPoolKey("openai-prod"):
			var entry types.KeyPoolEntry
			mustUnmarshalPayload(t, payload, &entry)
			artifacts.KeyPool.Entries = append(artifacts.KeyPool.Entries, entry)
		case publisher.KeyModelCatalog:
			if redisField == "gpt-4o-mini" {
				mustUnmarshalPayload(t, payload, &artifacts.ModelCatalog)
			}
		case publisher.KeyRoutes:
			if redisField == "gpt-4o-mini" {
				mustUnmarshalPayload(t, payload, &artifacts.Route)
			}
		case publisher.KeyProductPolicies:
			if redisField == "chat-pro" {
				mustUnmarshalPayload(t, payload, &artifacts.Policy)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate artifact rows: %v", err)
	}
	normalizeRuntimeArtifacts(&artifacts)
	return artifacts
}

func assertGoldenRuntimeArtifactsPresent(t *testing.T, artifacts runtimeArtifacts) {
	t.Helper()
	if artifacts.Provider.ID != "openai-prod" {
		t.Fatalf("missing provider artifact: %s", mustJSON(t, artifacts))
	}
	if artifacts.KeyPool.Meta.ProviderID != "openai-prod" || len(artifacts.KeyPool.Entries) != 1 {
		t.Fatalf("missing keypool artifact: %s", mustJSON(t, artifacts))
	}
	if artifacts.ModelCatalog.ModelID != "gpt-4o-mini" {
		t.Fatalf("missing model catalog artifact: %s", mustJSON(t, artifacts))
	}
	if artifacts.Route.ModelID != "gpt-4o-mini" {
		t.Fatalf("missing model route artifact: %s", mustJSON(t, artifacts))
	}
	if artifacts.Policy.ProductID != "chat-pro" {
		t.Fatalf("missing product policy artifact: %s", mustJSON(t, artifacts))
	}
}

func mustUnmarshalPayload(t *testing.T, payload []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(payload, dst); err != nil {
		t.Fatalf("unmarshal payload into %T: %v payload=%s", dst, err, string(payload))
	}
}

func normalizeRuntimeArtifacts(a *runtimeArtifacts) {
	a.Provider.Revision = 0
	sortProtocolBindings(a.Provider.ProtocolBindings)
	a.KeyPool.Meta.Revision = 0
	for i := range a.KeyPool.Entries {
		a.KeyPool.Entries[i].Revision = 0
		sort.Strings(a.KeyPool.Entries[i].AllowedModels)
	}
	sort.Slice(a.KeyPool.Entries, func(i, j int) bool {
		return a.KeyPool.Entries[i].KeyID < a.KeyPool.Entries[j].KeyID
	})
	a.ModelCatalog.Revision = 0
	sort.Strings(a.ModelCatalog.Capabilities)
	a.Route.Revision = 0
	for i := range a.Route.Providers {
		sort.Strings(a.Route.Providers[i].Capabilities)
	}
	a.Policy.Revision = 0
	sort.Strings(a.Policy.AllowedProviderIDs)
	sort.Strings(a.Policy.AllowedChannelIDs)
	sort.Strings(a.Policy.AllowedModels)
	sort.Strings(a.Policy.AllowedProviderProfiles)
}

func sortProtocolBindings(bindings []types.ProtocolBinding) {
	sort.Slice(bindings, func(i, j int) bool {
		return bindings[i].Name < bindings[j].Name
	})
	for i := range bindings {
		sort.Slice(bindings[i].Accepts, func(a, b int) bool {
			left, right := bindings[i].Accepts[a], bindings[i].Accepts[b]
			if left.Protocol != right.Protocol {
				return left.Protocol < right.Protocol
			}
			return left.RequestContract < right.RequestContract
		})
		for j := range bindings[i].Accepts {
			sort.Strings(bindings[i].Accepts[j].Capabilities)
			sort.Strings(bindings[i].Accepts[j].Models)
		}
		sort.Strings(bindings[i].CapabilityProfiles)
		sort.Strings(bindings[i].SupportedFeatures)
		sort.Strings(bindings[i].UnsupportedFeatures)
		sort.Strings(bindings[i].SupportedParams)
		sort.Strings(bindings[i].UnsupportedParams)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(body)
}

func newAPIIntegrationStore(t *testing.T) (*outbox.Store, *pgxpool.Pool) {
	t.Helper()
	baseDSN := os.Getenv("TP_PUBLISHER_PG_DSN")
	if baseDSN == "" {
		t.Skip("set TP_PUBLISHER_PG_DSN to run publisher API Postgres integration tests")
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
	cfg.Database = fmt.Sprintf("%s_%s", sanitizeDBIdentForAPITest(prefix), strings.ReplaceAll(uuid.NewString(), "-", "_"))
	testDSN := cfg.ConnString()
	if err := dbinit.EnsureDatabase(ctx, testDSN); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() { dropAPIIntegrationDatabase(t, testDSN) })
	if err := dbinit.Migrate(ctx, testDSN, migrations.FS); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	pool, err := pgxpool.New(ctx, testDSN)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return outbox.NewStore(pool), pool
}

func newAPIIntegrationRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("TP_PUBLISHER_REDIS_ADDR")
	if addr == "" {
		t.Skip("set TP_PUBLISHER_REDIS_ADDR to run publisher API Redis integration tests")
	}
	var (
		opt *redis.Options
		err error
	)
	if strings.HasPrefix(addr, "redis://") || strings.HasPrefix(addr, "rediss://") {
		opt, err = redis.ParseURL(addr)
		if err != nil {
			t.Fatalf("parse TP_PUBLISHER_REDIS_ADDR: %v", err)
		}
	} else {
		opt = &redis.Options{Addr: addr}
	}
	rdb := redis.NewClient(opt)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		t.Fatalf("ping redis %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func cleanupRedisProductPolicy(t *testing.T, ctx context.Context, rdb *redis.Client, productID string) {
	t.Helper()
	if err := rdb.HDel(ctx, publisher.KeyProductPolicies, productID).Err(); err != nil {
		t.Fatalf("cleanup product policy: %v", err)
	}
	if err := rdb.HDel(ctx, publisher.KeyProductPolicyRevisions, productID).Err(); err != nil {
		t.Fatalf("cleanup product policy revision: %v", err)
	}
	t.Cleanup(func() {
		_ = rdb.HDel(context.Background(), publisher.KeyProductPolicies, productID).Err()
		_ = rdb.HDel(context.Background(), publisher.KeyProductPolicyRevisions, productID).Err()
	})
}

func waitForRedisState(t *testing.T, timeout time.Duration, check func() (bool, string)) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		ok, state := check()
		last = state
		if ok {
			return state
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for redis state; last=%s", last)
	return ""
}

func redisIntKey(t *testing.T, ctx context.Context, rdb *redis.Client, key string) int64 {
	t.Helper()
	value, err := rdb.Get(ctx, key).Int64()
	if err == redis.Nil {
		return 0
	}
	if err != nil {
		t.Fatalf("GET %s: %v", key, err)
	}
	return value
}

func redisHashValue(t *testing.T, ctx context.Context, rdb *redis.Client, key, field string) string {
	t.Helper()
	value, err := rdb.HGet(ctx, key, field).Result()
	if err == redis.Nil {
		return ""
	}
	if err != nil {
		t.Fatalf("HGET %s[%s]: %v", key, field, err)
	}
	return value
}

func redisHashExists(t *testing.T, ctx context.Context, rdb *redis.Client, key, field string) bool {
	t.Helper()
	exists, err := rdb.HExists(ctx, key, field).Result()
	if err != nil {
		t.Fatalf("HEXISTS %s[%s]: %v", key, field, err)
	}
	return exists
}

func dropAPIIntegrationDatabase(t *testing.T, dsn string) {
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

func sanitizeDBIdentForAPITest(s string) string {
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
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "tokenplatform"
	}
	if len(out) > 30 {
		return out[:30]
	}
	return out
}

func installOneShotModelCatalogOutboxFailure(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		CREATE SEQUENCE api_test_fail_model_catalog_once_seq;

		CREATE OR REPLACE FUNCTION api_test_fail_model_catalog_once()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			IF NEW.method_name = 'PublishModelCatalog' AND nextval('api_test_fail_model_catalog_once_seq') = 1 THEN
				RAISE EXCEPTION 'api test injected one-shot outbox failure for %', NEW.method_name
					USING ERRCODE = 'P0001';
			END IF;
			RETURN NEW;
		END;
		$$;

		CREATE TRIGGER api_test_fail_model_catalog_once
		BEFORE INSERT ON publisher_outbox
		FOR EACH ROW
		EXECUTE FUNCTION api_test_fail_model_catalog_once();
	`)
	if err != nil {
		t.Fatalf("install one-shot model catalog outbox failure trigger: %v", err)
	}
}

func productPolicyDeleteIntentBody() string {
	return `{
		"schema_version": 2,
		"product_policy_deletes": [{
			"product_id": "prod_coding",
			"revision": 1700000000000456
		}]
	}`
}

func productPolicyUpsertIntentBody(productID string, revision int64) string {
	return fmt.Sprintf(`{
		"schema_version": 2,
		"product_policies": [{
			"product_id": %q,
			"revision": %d,
			"status": "active",
			"credential_source": "keypool",
			"model_scope_mode": "plan_configured",
			"fallback_policy": "within_product",
			"billing_mode": "token_plan_quota"
		}]
	}`, productID, revision)
}

func productPolicyDeleteIntentBodyFor(productID string, revision int64) string {
	return fmt.Sprintf(`{
		"schema_version": 2,
		"product_policy_deletes": [{
			"product_id": %q,
			"revision": %d
		}]
	}`, productID, revision)
}

func multiObjectIntentBody() string {
	return `{
		"schema_version": 2,
		"provider_endpoints": [{
			"id": "anthropic-prod",
			"provider_profile": "anthropic_native",
			"base_url": "https://api.anthropic.example",
			"region": "us",
			"status": "active",
			"name": "Anthropic production",
			"key_strategy": "priority"
		}, {
			"id": "openai-prod",
			"provider_profile": "openai_compatible",
			"base_url": "https://api.openai.example",
			"region": "us",
			"status": "active",
			"name": "OpenAI production",
			"key_strategy": "priority"
		}],
		"provider_credentials": [{
			"provider_id": "anthropic-prod",
			"key_ref": "secret://providers/anthropic-prod/key-a",
			"key_id": "key-a",
			"provider_account_ref": "pa_550e8400-e29b-41d4-a716-446655440100",
			"status": "enabled",
			"allowed_models": ["claude-3-5-sonnet"],
			"rpm_limit": 300,
			"tpm_limit": 60000,
			"priority": 20,
			"weight": 1
		}, {
			"provider_id": "openai-prod",
			"key_ref": "secret://providers/openai-prod/key-a",
			"key_id": "key-a",
			"provider_account_ref": "pa_550e8400-e29b-41d4-a716-446655440101",
			"status": "enabled",
			"allowed_models": ["gpt-4o-mini"],
			"rpm_limit": 600,
			"tpm_limit": 120000,
			"priority": 10,
			"weight": 1
		}],
		"client_exposures": [{
			"provider_id": "anthropic-prod",
			"served_inbound_protocols": ["anthropic"],
			"capabilities": ["chat"],
			"protocol_path_prefix": "/v1"
		}, {
			"provider_id": "openai-prod",
			"served_inbound_protocols": ["anthropic", "responses", "openai"],
			"capabilities": ["chat"],
			"token_count_strategy": "unsupported",
			"protocol_path_prefix": "/v1"
		}],
		"model_routes": [{
			"logical_model": "gpt-4o-mini",
			"capabilities": ["chat"],
			"context_window": 128000,
			"max_output": 16384,
			"owned_by": "openai-prod",
			"provider_strategy": "priority",
			"routes": [{
				"provider_id": "openai-prod",
				"upstream_model": "gpt-4o-mini",
				"priority": 10,
				"weight": 1,
				"status": "active"
			}, {
				"provider_id": "anthropic-prod",
				"upstream_model": "claude-3-5-sonnet",
				"priority": 20,
				"weight": 1,
				"status": "active"
			}]
		}],
		"product_policies": [{
			"product_id": "chat-pro",
			"status": "active",
			"credential_source": "dedicated_upstream",
			"allowed_models": ["gpt-4o-mini"],
			"model_scope_mode": "product_allowlist",
			"fallback_policy": "within_product",
			"billing_mode": "payg_balance",
			"route_strategy": "priority",
			"route_graph_required": true,
			"route_graph_enforcement": "enforce",
			"route_graph": {
				"schema_version": 1,
				"default_target": {"target_type": "GROUP", "target_id": "primary"},
				"groups": [{
					"id": "primary",
					"code": "primary",
					"status": "active",
					"strategy": "priority_failover",
					"members": [{
						"product_channel_id": "openai-prod",
						"channel_id": "openai-prod",
						"provider_id": "openai-prod",
						"priority": 10,
						"weight": 1,
						"role": "primary"
					}, {
						"product_channel_id": "anthropic-prod",
						"channel_id": "anthropic-prod",
						"provider_id": "anthropic-prod",
						"priority": 20,
						"weight": 1,
						"role": "fallback"
					}]
				}],
				"product_channels": [{
					"id": "openai-prod",
					"channel_id": "openai-prod",
					"provider_id": "openai-prod",
					"allowed_models": ["gpt-4o-mini"],
					"denied_models": [],
					"priority": 10,
					"weight": 1,
					"max_rpm": 600,
					"max_tpm": 120000,
					"status": "active"
				}, {
					"id": "anthropic-prod",
					"channel_id": "anthropic-prod",
					"provider_id": "anthropic-prod",
					"allowed_models": ["gpt-4o-mini"],
					"denied_models": [],
					"priority": 20,
					"weight": 1,
					"max_rpm": 300,
					"max_tpm": 60000,
					"status": "active"
				}],
				"assignments": [{
					"id": "asg_chat_pro",
					"product_id": "chat-pro",
					"subject_type": "USER_SEGMENT",
					"subject_id": "seg_default",
					"target_type": "GROUP",
					"target_id": "primary",
					"mode": "PREFER",
					"priority": 10
				}]
			}
		}]
	}`
}

func goldenIntentBody() string {
	return `{
		"schema_version": 2,
		"provider_endpoints": [{
			"id": "openai-prod",
			"provider_profile": "openai_compatible",
			"base_url": "https://api.openai.example",
			"region": "us",
			"status": "active",
			"name": "OpenAI production",
			"key_strategy": "priority"
		}],
		"provider_credentials": [{
			"provider_id": "openai-prod",
			"key_ref": "secret://providers/openai-prod/key-a",
			"key_id": "key-a",
			"provider_account_ref": "pa_550e8400-e29b-41d4-a716-446655440101",
			"status": "enabled",
			"allowed_models": ["gpt-4o-mini"],
			"rpm_limit": 600,
			"tpm_limit": 120000,
			"priority": 10,
			"weight": 1
		}],
		"client_exposures": [{
			"provider_id": "openai-prod",
			"served_inbound_protocols": ["anthropic", "responses", "openai"],
			"capabilities": ["chat"],
			"token_count_strategy": "unsupported",
			"protocol_path_prefix": "/v1"
		}],
		"model_routes": [{
			"logical_model": "gpt-4o-mini",
			"display_name": "gpt-4o-mini",
			"capabilities": ["chat"],
			"context_window": 128000,
			"max_output": 16384,
			"owned_by": "openai-prod",
			"provider_strategy": "priority",
			"routes": [{
				"provider_id": "openai-prod",
				"upstream_model": "gpt-4o-mini",
				"priority": 10,
				"weight": 1,
				"status": "active"
			}]
		}],
		"product_policies": [{
			"product_id": "chat-pro",
			"status": "active",
			"credential_source": "dedicated_upstream",
			"allowed_models": ["gpt-4o-mini"],
			"model_scope_mode": "product_allowlist",
			"fallback_policy": "within_product",
			"billing_mode": "payg_balance",
			"route_strategy": "priority",
			"route_graph_required": true,
			"route_graph_enforcement": "enforce",
			"route_graph": {
				"schema_version": 1,
				"default_target": {"target_type": "GROUP", "target_id": "primary"},
				"groups": [{
					"id": "primary",
					"code": "primary",
					"status": "active",
					"strategy": "priority_failover",
					"members": [{
						"product_channel_id": "openai-prod",
						"channel_id": "openai-prod",
						"provider_id": "openai-prod",
						"priority": 10,
						"weight": 1,
						"role": "primary"
					}]
				}],
				"product_channels": [{
					"id": "openai-prod",
					"channel_id": "openai-prod",
					"provider_id": "openai-prod",
					"allowed_models": ["gpt-4o-mini"],
					"denied_models": [],
					"priority": 10,
					"weight": 1,
					"max_rpm": 600,
					"max_tpm": 120000,
					"status": "active"
				}],
				"assignments": [{
					"id": "asg_chat_pro",
					"product_id": "chat-pro",
					"subject_type": "USER_SEGMENT",
					"subject_id": "seg_default",
					"target_type": "GROUP",
					"target_id": "primary",
					"mode": "PREFER",
					"priority": 10
				}]
			}
		}]
	}`
}

func legacyProviderBody() string {
	return `{
		"id": "openai-prod",
		"code": "openai-prod",
		"provider_profile": "openai_compatible",
		"client_compatibility": ["claude_code", "codex", "cursor"],
		"token_count_strategy": "unsupported",
		"path_prefix": "/v1",
		"base_url": "https://api.openai.example",
		"status": "active",
		"name": "OpenAI production",
		"key_strategy": "priority"
	}`
}

func legacyKeyPoolBody() string {
	return `{
		"meta": {
			"provider_id": "openai-prod",
			"strategy": "priority"
		},
		"entries": [{
			"key_id": "key-a",
			"provider_account_ref": "pa_550e8400-e29b-41d4-a716-446655440101",
			"ciphertext": "secret://providers/openai-prod/key-a",
			"status": "enabled",
			"allowed_models": ["gpt-4o-mini"],
			"rpm_limit": 600,
			"tpm_limit": 120000,
			"priority": 10,
			"weight": 1
		}]
	}`
}

func legacyModelCatalogBody() string {
	return `{
		"model_id": "gpt-4o-mini",
		"display_name": "gpt-4o-mini",
		"capabilities": ["chat"],
		"context_window": 128000,
		"max_output": 16384,
		"owned_by": "openai-prod"
	}`
}

func legacyModelRouteBody() string {
	return `{
		"model_id": "gpt-4o-mini",
		"providers": [{
			"provider_id": "openai-prod",
			"upstream_model": "gpt-4o-mini",
			"priority": 10,
			"weight": 1,
			"status": "active"
		}],
		"provider_strategy": "priority"
	}`
}

func legacyProductPolicyBody() string {
	return `{
		"product_id": "chat-pro",
		"status": "active",
		"credential_source": "dedicated_upstream",
		"allowed_provider_ids": ["openai-prod"],
		"allowed_channel_ids": ["openai-prod"],
		"allowed_models": ["gpt-4o-mini"],
			"model_scope_mode": "product_allowlist",
			"fallback_policy": "within_product",
			"billing_mode": "payg_balance",
			"route_strategy": "priority",
			"route_graph_required": true,
			"route_graph_enforcement": "enforce",
			"route_graph": {
				"schema_version": 1,
				"default_target": {"target_type": "GROUP", "target_id": "primary"},
				"groups": [{
					"id": "primary",
					"code": "primary",
					"status": "active",
					"strategy": "priority_failover",
					"members": [{
						"product_channel_id": "openai-prod",
						"channel_id": "openai-prod",
						"provider_id": "openai-prod",
						"priority": 10,
						"weight": 1,
						"role": "primary"
					}]
				}],
				"product_channels": [{
					"id": "openai-prod",
					"channel_id": "openai-prod",
					"provider_id": "openai-prod",
					"allowed_models": ["gpt-4o-mini"],
					"denied_models": [],
					"priority": 10,
					"weight": 1,
					"max_rpm": 600,
					"max_tpm": 120000,
					"status": "active"
				}],
				"assignments": [{
					"id": "asg_chat_pro",
					"product_id": "chat-pro",
					"subject_type": "USER_SEGMENT",
					"subject_id": "seg_default",
					"target_type": "GROUP",
					"target_id": "primary",
					"mode": "PREFER",
					"priority": 10
				}]
			}
		}`
}

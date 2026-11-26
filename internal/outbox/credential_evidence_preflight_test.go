package outbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/tokenplatform/publisher/internal/types"
)

func TestWorkerPreflightQuarantinesLegacyKeyPoolReplayBeforeDelete(t *testing.T) {
	ctx := context.Background()
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	const redisKey = "gw:keypool:zhipu"
	if err := rdb.HSet(ctx, redisKey, "preserved", `{"provider_account_ref":"pa_550e8400-e29b-41d4-a716-446655440000"}`).Err(); err != nil {
		t.Fatalf("seed keypool: %v", err)
	}
	if err := rdb.Set(ctx, "gw:config:rev:credential", "41", 0).Err(); err != nil {
		t.Fatalf("seed revision: %v", err)
	}

	legacy := mustJSON(t, types.KeyPoolEntry{KeyID: "legacy-without-account-ref", Status: "enabled"})
	rows := []OutboxRow{
		{Seq: 0, Op: OpDel, RedisKey: redisKey, MethodName: "PublishKeyPool"},
		{Seq: 1, Op: OpHSet, RedisKey: redisKey, RedisField: "legacy", Payload: legacy, MethodName: "PublishKeyPool"},
		{Seq: 2, Op: OpIncr, RedisKey: "gw:config:rev:credential", MethodName: "PublishKeyPool"},
	}

	var alert string
	worker := NewWorker(nil, rdb, WorkerConfig{}, func(_ types.BatchID, _ string, lastErr string) {
		alert = lastErr
	})
	loadCount := 0
	worker.loadBatchFn = func(_ context.Context, _ types.BatchID) ([]OutboxRow, error) {
		loadCount++
		return rows, nil
	}
	// Replay/ClaimDue can cut a batch after its destructive prefix. Pass only
	// the claimed DEL and require preflight to load the later legacy HSET.
	worker.deliverBatch(ctx, uuid.New(), rows[:1])

	if loadCount != 1 {
		t.Fatalf("complete replay batch load count=%d want 1", loadCount)
	}
	if got, err := rdb.HGet(ctx, redisKey, "preserved").Result(); err != nil || got == "" {
		t.Fatalf("legacy replay mutated existing keypool before quarantine: value=%q err=%v", got, err)
	}
	if got, err := rdb.Get(ctx, "gw:config:rev:credential").Result(); err != nil || got != "41" {
		t.Fatalf("legacy replay incremented revision before quarantine: value=%q err=%v", got, err)
	}
	if !strings.Contains(alert, "KeyPoolEntry.provider_account_ref") {
		t.Fatalf("dead-letter alert=%q, want missing KeyPoolEntry provider_account_ref", alert)
	}
}

func TestWorkerPreflightQuarantinesLegacyDedicatedKeyMetaBeforeRedis(t *testing.T) {
	ctx := context.Background()
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	const field = "sha256:dedicated"
	if err := rdb.HSet(ctx, keyMetaHashKey, field, `{"status":"active","marker":"preserved"}`).Err(); err != nil {
		t.Fatalf("seed keymeta: %v", err)
	}
	legacy := mustJSON(t, types.KeyMeta{
		KeyHash: field,
		DedicatedUpstream: &types.DedicatedUpstreamCredential{
			UpstreamKeyID: "upstream-key-without-account-ref",
		},
	})
	rows := []OutboxRow{{
		Seq:        0,
		Op:         OpHSet,
		RedisKey:   keyMetaHashKey,
		RedisField: field,
		Payload:    legacy,
		MethodName: "PublishKey",
	}}

	var alerted bool
	worker := NewWorker(nil, rdb, WorkerConfig{}, func(_ types.BatchID, _ string, _ string) { alerted = true })
	worker.deliverBatch(ctx, uuid.New(), rows)

	if !alerted {
		t.Fatal("legacy dedicated KeyMeta was not quarantined")
	}
	if got, err := rdb.HGet(ctx, keyMetaHashKey, field).Result(); err != nil || !strings.Contains(got, "preserved") {
		t.Fatalf("legacy dedicated KeyMeta reached Redis: value=%q err=%v", got, err)
	}
}

func TestWorkerPreflightQuarantinesLegacyProviderEntitlementBeforeRedis(t *testing.T) {
	ctx := context.Background()
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	const field = "entitlement-provider"
	if err := rdb.HSet(ctx, providerInfoHashKey, field, `{"status":"enabled","marker":"preserved"}`).Err(); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	legacy := mustJSON(t, types.ProviderInfo{
		ID:               field,
		CredentialSource: types.ProviderCredentialSourceProviderEntitlement,
	})
	rows := []OutboxRow{{
		Seq:        0,
		Op:         OpHSet,
		RedisKey:   providerInfoHashKey,
		RedisField: field,
		Payload:    legacy,
		MethodName: "PublishProvider",
	}}

	var alerted bool
	worker := NewWorker(nil, rdb, WorkerConfig{}, func(_ types.BatchID, _ string, _ string) { alerted = true })
	worker.deliverBatch(ctx, uuid.New(), rows)

	if !alerted {
		t.Fatal("legacy provider_entitlement ProviderInfo was not quarantined")
	}
	if got, err := rdb.HGet(ctx, providerInfoHashKey, field).Result(); err != nil || !strings.Contains(got, "preserved") {
		t.Fatalf("legacy provider_entitlement ProviderInfo reached Redis: value=%q err=%v", got, err)
	}
}

func TestDedicatedAndProviderEntitlementAccountRefsReachRedis(t *testing.T) {
	ctx := context.Background()
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	worker := NewWorker(nil, rdb, WorkerConfig{}, nil)
	const accountRef = "overseas-prod-1"

	dedicatedPayload := mustJSON(t, types.KeyMeta{
		SchemaVersion: 2,
		Revision:      1700000000000001,
		KeyID:         "8f0c57d8-77b5-4a83-a89f-26ebc356d910",
		KeyHash:       "sha256:dedicated",
		Status:        "active",
		ExpiresAt:     1893456000,
		IPWhitelist:   []string{},
		AllowedModels: []string{"glm-4.5"},
		DedicatedUpstream: &types.DedicatedUpstreamCredential{
			UpstreamKeyID:      "upstream-key",
			ProviderAccountRef: accountRef,
			Ciphertext:         "kms:projects/demo/key:ciphertext",
			CiphertextVersion:  1,
			CiphertextSHA256:   "sha256:dedicated-ciphertext",
			KMSKeyID:           "kms-key-coding",
			Status:             "active",
			ExpiresAt:          1893456000,
			UpdatedAt:          "2026-06-15T00:00:00Z",
			Endpoints: []types.DedicatedUpstreamEndpoint{{
				ClientProtocol:   "anthropic",
				ProviderID:       "glm-anthropic",
				ProtocolStrategy: "passthrough",
			}},
		},
	})
	if err := worker.runOne(ctx, OutboxRow{
		Op: OpHSet, RedisKey: keyMetaHashKey, RedisField: "sha256:dedicated", Payload: dedicatedPayload,
	}, &batchState{}); err != nil {
		t.Fatalf("deliver dedicated KeyMeta: %v", err)
	}

	providerPayload := mustJSON(t, types.ProviderInfo{
		SchemaVersion:      1,
		Revision:           1700000000000002,
		ID:                 "entitlement-provider",
		Name:               "Entitlement provider",
		BaseURL:            "https://api.entitlement.example",
		Status:             "active",
		ProviderProfile:    "openai_compatible",
		Visibility:         "dedicated_only",
		CredentialSource:   types.ProviderCredentialSourceProviderEntitlement,
		AllowedRouteKinds:  []string{"dedicated_route"},
		ProviderAccountRef: accountRef,
		ProtocolBindings: []types.ProtocolBinding{{
			Name:             "openai-chat",
			OutboundProtocol: "openai",
			AuthScheme:       "bearer",
		}},
	})
	if err := worker.runOne(ctx, OutboxRow{
		Op: OpHSet, RedisKey: providerInfoHashKey, RedisField: "entitlement-provider", Payload: providerPayload,
	}, &batchState{}); err != nil {
		t.Fatalf("deliver provider_entitlement ProviderInfo: %v", err)
	}

	rawDedicated, err := rdb.HGet(ctx, keyMetaHashKey, "sha256:dedicated").Bytes()
	if err != nil {
		t.Fatalf("read dedicated KeyMeta: %v", err)
	}
	var gotMeta types.KeyMeta
	if err := json.Unmarshal(rawDedicated, &gotMeta); err != nil {
		t.Fatalf("decode dedicated KeyMeta: %v", err)
	}
	if gotMeta.DedicatedUpstream == nil || gotMeta.DedicatedUpstream.ProviderAccountRef != accountRef {
		t.Fatalf("dedicated Redis artifact lost provider_account_ref: %s", rawDedicated)
	}

	rawProvider, err := rdb.HGet(ctx, providerInfoHashKey, "entitlement-provider").Bytes()
	if err != nil {
		t.Fatalf("read provider entitlement: %v", err)
	}
	var gotProvider types.ProviderInfo
	if err := json.Unmarshal(rawProvider, &gotProvider); err != nil {
		t.Fatalf("decode provider entitlement: %v", err)
	}
	if gotProvider.ProviderAccountRef != accountRef {
		t.Fatalf("provider entitlement Redis artifact lost provider_account_ref: %s", rawProvider)
	}
	t.Logf("dedicated Redis artifact: %s", rawDedicated)
	t.Logf("provider_entitlement Redis artifact: %s", rawProvider)
}

func TestCredentialEvidencePreflightAllowsOrdinaryKeyMetaAndProviderInfo(t *testing.T) {
	rows := []OutboxRow{
		{Op: OpHSet, RedisKey: keyMetaHashKey, RedisField: "ordinary", Payload: mustJSON(t, types.KeyMeta{KeyHash: "ordinary"})},
		{Op: OpHSet, RedisKey: providerInfoHashKey, RedisField: "ordinary", Payload: mustJSON(t, types.ProviderInfo{ID: "ordinary", CredentialSource: types.ProviderCredentialSourceKeypool})},
	}
	if err := validateCredentialEvidenceOutboxRows(rows); err != nil {
		t.Fatalf("ordinary non-credential-bearing rows rejected: %v", err)
	}
}

func TestCredentialEvidencePreflightRejectsMalformedOrCredentialDerivedProviderAccountRefs(t *testing.T) {
	tests := []struct {
		name string
		row  OutboxRow
	}{
		{
			name: "keypool",
			row: OutboxRow{Op: OpHSet, RedisKey: "gw:keypool:zhipu", RedisField: "key", Payload: mustJSON(t, types.KeyPoolEntry{
				KeyID: "key", ProviderAccountRef: " invalid ref",
			})},
		},
		{
			name: "dedicated keymeta",
			row: OutboxRow{Op: OpHSet, RedisKey: keyMetaHashKey, RedisField: "key", Payload: mustJSON(t, types.KeyMeta{
				KeyHash: "key", DedicatedUpstream: &types.DedicatedUpstreamCredential{ProviderAccountRef: " invalid ref"},
			})},
		},
		{
			name: "provider entitlement",
			row: OutboxRow{Op: OpHSet, RedisKey: providerInfoHashKey, RedisField: "provider", Payload: mustJSON(t, types.ProviderInfo{
				ID: "provider", CredentialSource: types.ProviderCredentialSourceProviderEntitlement, ProviderAccountRef: " invalid ref",
			})},
		},
		{
			name: "keypool reuses key id",
			row: OutboxRow{Op: OpHSet, RedisKey: "gw:keypool:zhipu", RedisField: "key", Payload: mustJSON(t, types.KeyPoolEntry{
				KeyID: "key", ProviderAccountRef: "key",
			})},
		},
		{
			name: "keypool reuses ciphertext reference",
			row: OutboxRow{Op: OpHSet, RedisKey: "gw:keypool:zhipu", RedisField: "key", Payload: mustJSON(t, types.KeyPoolEntry{
				KeyID: "key", Ciphertext: "credential-ref-1", ProviderAccountRef: "credential-ref-1",
			})},
		},
		{
			name: "dedicated reuses upstream key id",
			row: OutboxRow{Op: OpHSet, RedisKey: keyMetaHashKey, RedisField: "key", Payload: mustJSON(t, types.KeyMeta{
				KeyHash: "key", DedicatedUpstream: &types.DedicatedUpstreamCredential{UpstreamKeyID: "upstream-key", ProviderAccountRef: "upstream-key"},
			})},
		},
		{
			name: "dedicated reuses ciphertext reference",
			row: OutboxRow{Op: OpHSet, RedisKey: keyMetaHashKey, RedisField: "key", Payload: mustJSON(t, types.KeyMeta{
				KeyHash: "key", DedicatedUpstream: &types.DedicatedUpstreamCredential{Ciphertext: "credential-ref-1", ProviderAccountRef: "credential-ref-1"},
			})},
		},
		{
			name: "provider entitlement reuses provider id",
			row: OutboxRow{Op: OpHSet, RedisKey: providerInfoHashKey, RedisField: "provider", Payload: mustJSON(t, types.ProviderInfo{
				ID: "provider", CredentialSource: types.ProviderCredentialSourceProviderEntitlement, ProviderAccountRef: "provider",
			})},
		},
		{
			name: "provider entitlement reuses endpoint reference",
			row: OutboxRow{Op: OpHSet, RedisKey: providerInfoHashKey, RedisField: "provider", Payload: mustJSON(t, types.ProviderInfo{
				ID: "provider", BaseURL: "provider-endpoint-1", CredentialSource: types.ProviderCredentialSourceProviderEntitlement, ProviderAccountRef: "provider-endpoint-1",
			})},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateCredentialEvidenceOutboxRows([]OutboxRow{tc.row}); !strings.Contains(errString(err), "provider_account_ref") {
				t.Fatalf("error=%v, want provider_account_ref rejection", err)
			}
		})
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return payload
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

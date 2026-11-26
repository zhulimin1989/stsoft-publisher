// Package publisher is the V2 publisher contract: every caller-facing
// operation produces exactly one outbox batch (the unit of at-least-once
// delivery to Redis). The implementation in this package consists of:
//
//  1. Sync path: validate input → INSERT outbox rows in PG → return BatchID
//  2. Async path: retry worker reads pending rows, executes the Redis ops,
//     publishes notifications, marks rows delivered.
//
// Callers (user-service / billing-service / admin-service) hit the HTTP layer
// in internal/api/, which in turn invokes Publisher methods through the
// interface defined here. The HTTP layer is a thin adapter — business
// rules (revision gating, atomic-unit constraints) live below this line.
//
// Spec: publisher-interface-spec.md (full design rationale)
// API:  publisher-api-reference.md (HTTP wire shape)
package publisher

import (
	"context"

	"github.com/tokenplatform/publisher/internal/types"
)

// Publisher is the V2 contract — one method per atomic unit per operation
// (write / bulk-write / delete / bulk-delete / set-flag). Every method
// returns a BatchID identifying the outbox batch that records the write,
// suitable for passing back to /ops/api/v1/replay.
//
// Hard rules (enforced by the implementation):
//   - Routine methods touch exactly one atomic unit. Lifecycle methods
//     DisableKey/EnableKey are the explicit exception: they intentionally
//     update gw:keymeta.status and gw:revoked_keys together so the two
//     gateway-facing disable mechanisms cannot drift.
//   - All KeyHash inputs are validated 64-hex + non-raw before persisting.
//   - All KeyID inputs are validated UUID v4 before persisting.
//   - Bulk inputs are capped at types.MaxBulkItems (100).
//
// Concurrency: implementations must be safe for concurrent calls. Outbox
// INSERT inside the sync path is the only PG write; everything else moves
// to the retry worker.
type Publisher interface {
	// --- Unit 1: KeyMeta ---
	PublishKey(ctx context.Context, meta *types.KeyMeta) (types.BatchID, error)
	// ActivateKey makes an active KeyMeta, its explicit admission value and
	// revoked-state cleanup visible as one Redis transaction.
	ActivateKey(ctx context.Context, meta *types.KeyMeta, hasBalance bool, reason string, decision types.KeyAdmissionDecision) (types.BatchID, error)
	PublishKeyBulk(ctx context.Context, metas []*types.KeyMeta) (types.BatchID, error)
	DeleteKey(ctx context.Context, hash types.KeyHash) (types.BatchID, error)
	DeleteKeyBulk(ctx context.Context, hashes []types.KeyHash) (types.BatchID, error)

	// --- Unit 2: KeyBalance ---
	SetKeyBalance(ctx context.Context, id types.KeyID, hasBalance bool, reason string, revision int64) (types.BatchID, error)
	SetKeyBalanceBulk(ctx context.Context, items []types.KeyBalanceItem, reason string) (types.BatchID, error)
	DeleteKeyBalance(ctx context.Context, id types.KeyID) (types.BatchID, error)
	SetKeyAdmission(ctx context.Context, id types.KeyID, hasBalance bool, reason string, decision types.KeyAdmissionDecision) (types.BatchID, error)
	SetKeyAdmissionBulk(ctx context.Context, items []types.KeyAdmissionItem) (types.BatchID, error)
	DeleteKeyAdmission(ctx context.Context, id types.KeyID, decision types.KeyAdmissionDecision) (types.BatchID, error)
	GetKeyAdmissionDelivery(ctx context.Context, batchID types.BatchID) (AdmissionDeliveryReceipt, error)

	// --- Unit 3: RevokedKeys ---
	AddRevokedKey(ctx context.Context, hash types.KeyHash) (types.BatchID, error)
	AddRevokedKeysBulk(ctx context.Context, hashes []types.KeyHash) (types.BatchID, error)
	RemoveRevokedKey(ctx context.Context, hash types.KeyHash) (types.BatchID, error)
	RemoveRevokedKeysBulk(ctx context.Context, hashes []types.KeyHash) (types.BatchID, error)
	PublishRevokedKeys(ctx context.Context, hashes []types.KeyHash) (types.BatchID, error)
	DisableKey(ctx context.Context, hash types.KeyHash) (types.BatchID, error)
	DisableKeysBulk(ctx context.Context, hashes []types.KeyHash) (types.BatchID, error)
	EnableKey(ctx context.Context, hash types.KeyHash) (types.BatchID, error)
	EnableKeysBulk(ctx context.Context, hashes []types.KeyHash) (types.BatchID, error)

	// --- Unit 4: BlockedIPs ---
	AddBlockedIP(ctx context.Context, ip string) (types.BatchID, error)
	AddBlockedIPsBulk(ctx context.Context, ips []string) (types.BatchID, error)
	RemoveBlockedIP(ctx context.Context, ip string) (types.BatchID, error)
	RemoveBlockedIPsBulk(ctx context.Context, ips []string) (types.BatchID, error)
	PublishBlockedIPs(ctx context.Context, ips []string) (types.BatchID, error)

	// --- Unit 5: ModelCatalog ---
	PublishModelCatalog(ctx context.Context, modelID string, entry *types.ModelCatalogEntry) (types.BatchID, error)
	PublishModelCatalogBulk(ctx context.Context, items []types.ModelCatalogItem) (types.BatchID, error)
	DeleteModelCatalog(ctx context.Context, modelID string) (types.BatchID, error)

	// --- Unit 6: ModelRoute ---
	PublishModelRoute(ctx context.Context, modelID string, route *types.RouteConfig) (types.BatchID, error)
	PublishModelRouteBulk(ctx context.Context, items []types.ModelRouteItem) (types.BatchID, error)
	DeleteModelRoute(ctx context.Context, modelID string) (types.BatchID, error)

	// --- Unit 7: KeyPool ---
	// Whole-pool replace (DEL existing hash → HSET each entry + meta).
	// Use for initial sync or disaster recovery; per-entry ops are cheaper
	// for routine add/remove/disable of one upstream key.
	PublishKeyPool(ctx context.Context, providerID string, pool *types.KeyPoolConfig) (types.BatchID, error)
	PublishKeyPoolBulk(ctx context.Context, items []types.KeyPoolItem) (types.BatchID, error)
	DeleteKeyPool(ctx context.Context, providerID string) (types.BatchID, error)
	// Per-entry surface: one upstream key inside a provider's pool. Touches
	// only field=KeyID in gw:keypool:{provider_id}, never the whole pool.
	PublishKeyPoolEntry(ctx context.Context, providerID string, entry *types.KeyPoolEntry) (types.BatchID, error)
	PublishKeyPoolEntryBulk(ctx context.Context, items []types.KeyPoolEntryItem) (types.BatchID, error)
	DeleteKeyPoolEntry(ctx context.Context, providerID, keyID string) (types.BatchID, error)
	DeleteKeyPoolEntryBulk(ctx context.Context, items []types.KeyPoolEntryDeleteItem) (types.BatchID, error)

	// --- Unit 8: Provider ---
	PublishProvider(ctx context.Context, providerID string, info *types.ProviderInfo) (types.BatchID, error)
	DeleteProvider(ctx context.Context, providerID string) (types.BatchID, error)

	// --- Unit 9: PlatformConfig ---
	PublishPlatformConfig(ctx context.Context, cfg *types.PlatformConfig) (types.BatchID, error)
	PublishProductRuntimePolicy(ctx context.Context, productID string, policy *types.ProductRuntimePolicy) (types.BatchID, error)
	DeleteProductRuntimePolicy(ctx context.Context, productID string) (types.BatchID, error)

	// --- Unit 10: ProviderOverride ---
	PublishProviderOverride(ctx context.Context, scope string, override *types.ProviderOverride) (types.BatchID, error)
	PublishProviderOverrideBulk(ctx context.Context, items []types.ProviderOverrideItem) (types.BatchID, error)
	DeleteProviderOverride(ctx context.Context, scope string) (types.BatchID, error)
	DeleteProviderOverrideBulk(ctx context.Context, scopes []string) (types.BatchID, error)

	// --- Intent aggregate ---
	PublishIntent(ctx context.Context, req IntentPublishRequest) (IntentPublishResult, error)

	// --- Ops ---
	Replay(ctx context.Context, req ReplayRequest) (ReplayResult, error)
}

// IntentPublishRequest is the compiled runtime payload for one
// /providers:from-intent request. Each element is still persisted as the same
// logical outbox batch its single-entity Publish* method would produce, but the
// full set is inserted under one PG transaction.
type IntentPublishRequest struct {
	Providers            []types.ProviderInfo
	KeyPools             []types.KeyPoolConfig
	ModelCatalog         []types.ModelCatalogEntry
	Routes               []types.RouteConfig
	ProductPolicies      []types.ProductRuntimePolicy
	ProductPolicyDeletes []types.ProductPolicyDelete
}

// IntentPublishResult mirrors the per-section batch ids returned by the
// intent HTTP endpoint.
type IntentPublishResult struct {
	ProviderBatches            []types.BatchID
	KeyPoolBatches             []types.BatchID
	ModelCatalogBatches        []types.BatchID
	ModelRouteBatches          []types.BatchID
	ProductPolicyBatches       []types.BatchID
	ProductPolicyDeleteBatches []types.BatchID
}

// ReplayRequest is the wire shape of the /ops/api/v1/replay POST body. One
// of (BatchID) or (Since+Until) must be present; both is a 400 conflict.
type ReplayRequest struct {
	BatchID *types.BatchID
	Since   *int64       // unix seconds inclusive (nil = unbounded lower)
	Until   *int64       // unix seconds exclusive (nil = unbounded upper)
	Status  ReplayStatus // pending / failed / undelivered (default) / all
	Limit   int          // default 100, max 10000
	DryRun  bool
}

// ReplayResult mirrors the JSON body returned by /ops/api/v1/replay.
type ReplayResult struct {
	Attempted int             `json:"attempted"`
	Succeeded int             `json:"succeeded"`
	Failed    int             `json:"failed"`
	BatchIDs  []types.BatchID `json:"batch_ids"`
}

type AdmissionDeliveryStatus string

const (
	AdmissionDeliveryPending    AdmissionDeliveryStatus = "pending"
	AdmissionDeliveryApplied    AdmissionDeliveryStatus = "applied"
	AdmissionDeliveryIdempotent AdmissionDeliveryStatus = "idempotent"
	AdmissionDeliverySuperseded AdmissionDeliveryStatus = "superseded"
	AdmissionDeliveryConflict   AdmissionDeliveryStatus = "conflict"
	AdmissionDeliveryDeadLetter AdmissionDeliveryStatus = "dead_letter"
	AdmissionDeliveryUnverified AdmissionDeliveryStatus = "unverified"
	AdmissionDeliveryMixed      AdmissionDeliveryStatus = "mixed"
)

// AdmissionDeliveryReceipt is Publisher delivery state, not a financial
// settlement receipt. A bulk batch may contain more than one terminal CAS
// outcome, so counts remain visible even when Status reports the highest
// priority outcome.
type AdmissionDeliveryReceipt struct {
	BatchID         types.BatchID           `json:"batch_id"`
	Status          AdmissionDeliveryStatus `json:"status"`
	Total           int                     `json:"total"`
	PendingCount    int                     `json:"pending_count"`
	AppliedCount    int                     `json:"applied_count"`
	IdempotentCount int                     `json:"idempotent_count"`
	SupersededCount int                     `json:"superseded_count"`
	ConflictCount   int                     `json:"conflict_count"`
	DeadLetterCount int                     `json:"dead_letter_count"`
	UnverifiedCount int                     `json:"unverified_count"`
	LastError       string                  `json:"last_error,omitempty"`
}

// ReplayStatus is the filter used by the time-window replay path.
type ReplayStatus string

const (
	ReplayStatusPending     ReplayStatus = "pending"
	ReplayStatusFailed      ReplayStatus = "failed"
	ReplayStatusUndelivered ReplayStatus = "undelivered" // pending+failed
	ReplayStatusAll         ReplayStatus = "all"
)

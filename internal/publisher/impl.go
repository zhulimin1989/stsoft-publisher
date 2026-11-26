package publisher

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/tokenplatform/publisher/internal/outbox"
	"github.com/tokenplatform/publisher/internal/routegraph"
	"github.com/tokenplatform/publisher/internal/types"
)

// ErrInvalidArgument signals an input-shape violation. HTTP layer maps to
// 400 INVALID_ARGUMENT.
var ErrInvalidArgument = errors.New("publisher: invalid argument")

var ErrAdmissionDeliveryNotFound = errors.New("publisher: admission delivery not found")

// Impl is the concrete Publisher V2 implementation. Every method validates,
// builds outbox rows, and persists them in one PG transaction. No Redis
// write is attempted on the synchronous caller path — that's the retry
// worker's job.
type Impl struct {
	store *outbox.Store

	// redis is the optional Redis client used to VERIFY balanceDedupe cache
	// hits against the canonical Redis state before short-circuiting the
	// outbox write. Without it, cache hits trust the local map blindly —
	// SAFE only in single-instance deployments or behind sticky-key LB
	// routing. In multi-instance + round-robin LB the local cache can be
	// stale (Pod B wrote a different value while Pod A was idle), and a
	// blind skip leaves Redis stuck at the wrong value until the next
	// state-change call or 5min TTL.
	//
	// Wire via SetRedisForBalanceVerify; nil → degraded "trust cache" mode.
	// 2026-05-26 HA Commit B-3.
	redis *redis.Client

	// runtimeProviderLookup reads current gw:providers rows for dedicated
	// KeyMeta validation. It must not cache; write-time validation should
	// see Redis snapshot truth before accepting endpoint provider refs.
	runtimeProviderLookup RuntimeProviderLookup

	dedicatedProviderValidation DedicatedProviderValidationConfig

	// callerCtx is a per-request override surface populated by the HTTP
	// middleware. Lets handlers stamp caller_service / caller_trace_id /
	// dedup_key on outbox rows without threading extra args through every
	// signature.
	//
	// Read via callerInfoFromCtx; never read directly.

	// balanceDedupe is the in-process write-through cache for SetKeyBalance.
	// Skips outbox write + audit row + downstream Redis fan-out when the
	// (HasBalance, Reason) tuple for a key_id is unchanged since the last
	// successful write less than balanceDedupeTTL ago.
	//
	// Why: billing consumers call SetKeyBalance after every per-request
	// token deduction; the bool stays "1" for 99%+ of those calls. Without
	// dedupe each call writes 3 outbox rows + audit + gateway HSet — pure
	// waste. With dedupe only genuine state transitions reach persistence.
	//
	// TTL refresh: even on identical state, force a re-write every TTL so
	// Redis cache drift / replica resync edge cases self-heal. Default
	// 5 minutes — short enough that drift can't accumulate, long enough
	// that dedupe kills 99%+ of redundant writes.
	//
	// Process restart: map is empty → first call per key always writes.
	// Acceptable: at most N "wake-up" writes per restart, then steady
	// state resumes.
	//
	// Multi-instance HA (2026-05-26): the cache is in-process, so an
	// N-pod publisher deployment with round-robin LB sees hit rate ÷ N
	// (each pod sees only its share of calls for a given key_id). The
	// CORRECTNESS is not affected — PG dedup_key + outbox idempotency
	// catch any "real" duplicate. To restore single-instance hit rate
	// in HA, configure the LB with consistent-hash routing on a header
	// like `X-Dedupe-Key: <key_id>` so the same key_id always lands on
	// the same pod. See docs/HA_DEPLOYMENT.md.
	balanceDedupeMu      sync.Mutex
	balanceDedupe        map[types.KeyID]balanceDedupeEntry
	balanceDedupeTTL     time.Duration
	balanceDedupeSkipped atomic.Uint64 // diagnostic counter; cumulative since process start
}

// balanceDedupeEntry tracks the last successful SetKeyBalance write for
// one key_id. Compared by exact (HasBalance, Reason) tuple — partial
// matches do NOT count as deduplicable (a reason change is a real
// state transition even if HasBalance stayed the same).
type balanceDedupeEntry struct {
	HasBalance      bool
	Reason          string
	DecisionVersion int64
	WrittenAt       time.Time
	BatchID         types.BatchID // returned on dedup hit so caller's request_id correlation still works
}

const defaultBalanceDedupeTTL = 5 * time.Minute

// verifyBalanceCacheLocked HGets gw:key_balance[id] from Redis and returns
// true when the canonical Redis value matches the local cache (HasBalance
// flag). Returns true on miss-or-error too, biasing toward "let the
// skip happen" — a transient Redis blip should not force a flood of
// outbox writes. The 99% case is a cluster operating normally where
// HGet returns the expected value (or HGet returns redis.Nil meaning
// "publisher hasn't written this key yet, no other pod has either" —
// equivalent to the cache being authoritative).
//
// Specifically:
//   - HGet success, value == cache.HasBalance → true (cache is correct)
//   - HGet success, value != cache.HasBalance → false (some other pod
//     wrote it; cache is stale; force real write)
//   - HGet returns redis.Nil → true (field absent; equivalent to "no
//     write has ever landed for this key", cache will land the first one)
//   - HGet errors (Redis down) → true (don't punish the cache for a
//     Redis blip; outbox writes also fail-fast in this scenario, so
//     forcing the skip-path is the lesser disruption)
//
// Best-effort: 50ms timeout. balanceDedupe is a write-volume optimization,
// not a correctness primitive in the multi-instance failure modes; the
// worker's Lua CAS is what protects Redis state. The verify here narrows
// the window where publisher's in-process view diverges from Redis.
func (p *Impl) verifyBalanceCacheLocked(ctx context.Context, id types.KeyID, hasBalance bool) bool {
	if p.redis == nil {
		return true
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	got, err := p.redis.HGet(verifyCtx, "gw:key_balance", string(id)).Result()
	if err == redis.Nil {
		// Field absent. Cache's prior write hasn't landed yet (outbox
		// hasn't replayed) or never landed (worker failure). Either way,
		// skipping this duplicate call doesn't make state worse — the
		// previously-queued outbox row will land the same value.
		return true
	}
	if err != nil {
		// Redis down or transient error. Don't burn outbox capacity on a
		// blip; the caller's call would also fail at commit() / worker
		// stage anyway.
		return true
	}
	expected := boolToFlag(hasBalance)
	return got == expected
}

func boolToFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func verifyModeLabel(verified bool) string {
	if verified {
		return "redis_verified"
	}
	return "trust_cache"
}

// CallerInfo carries the per-request audit annotations the HTTP middleware
// extracts from headers (Authorization service id, X-Request-ID,
// Idempotency-Key) plus the server-minted per-request UUID v7 (RequestID).
// Threaded through ctx with ctxCaller as the key.
type CallerInfo struct {
	Service       string
	TraceID       string
	IdempotencyID string
	// RequestID is the publisher-side unique id minted on every
	// authenticated HTTP request (UUID v7, server-generated, NEVER
	// client-supplied — see middleware.Authenticate). Stamped on every
	// outbox row of the batch this request produces; echoed in the
	// X-Request-Id response header so caller-side logs correlate to
	// publisher audit. 2026-05-21.
	RequestID string
	// ActorID is the per-request attribution at user-account granularity
	// (vs Service which is just admin/billing/user/sre). Currently set
	// to the first 10 chars of the bearer token by Authenticate
	// middleware (BearerTokenPrefix). Lands on every outbox row so
	// audit can answer "上周哪个 admin 把 keypool 改成 X". Switching to
	// a real user_id field claim later is a non-breaking change — the
	// column stays TEXT. 2026-05-22.
	ActorID string
}

type ctxCallerKey struct{}

// WithCaller stamps caller annotations onto ctx. Used by middleware.
func WithCaller(ctx context.Context, info CallerInfo) context.Context {
	return context.WithValue(ctx, ctxCallerKey{}, info)
}

func callerInfoFromCtx(ctx context.Context) CallerInfo {
	v, _ := ctx.Value(ctxCallerKey{}).(CallerInfo)
	return v
}

// CallerFromContext is the exported reader for the CallerInfo stamped by
// the HTTP auth middleware. Used by the api-layer access log so each
// request line can be attributed to a service identity without re-
// parsing the bearer. Returns the zero value when ctx carries no caller
// (handler invoked outside the HTTP path — tests, replay, etc.).
func CallerFromContext(ctx context.Context) CallerInfo {
	return callerInfoFromCtx(ctx)
}

// New wires a Publisher Impl.
func New(store *outbox.Store) *Impl {
	p := &Impl{
		store:                       store,
		dedicatedProviderValidation: DefaultDedicatedProviderValidationConfig(),
		balanceDedupe:               make(map[types.KeyID]balanceDedupeEntry),
		balanceDedupeTTL:            defaultBalanceDedupeTTL,
	}
	return p
}

// SetBalanceDedupeTTLForTest overrides the dedupe TTL. Test-only — production
// uses the default. Setting to 0 disables dedupe (every call writes).
func (p *Impl) SetBalanceDedupeTTLForTest(d time.Duration) {
	p.balanceDedupeMu.Lock()
	defer p.balanceDedupeMu.Unlock()
	p.balanceDedupeTTL = d
}

// SetRedisForBalanceVerify wires the Redis client used to verify
// balanceDedupe cache hits against canonical Redis state. REQUIRED for
// multi-instance HA deployments without sticky-key LB routing — without
// it a cross-pod cache staleness window of up to balanceDedupeTTL (5min
// default) can leave gw:key_balance stuck at a wrong value after rapid
// cross-pod state flips. See docs/HA_DEPLOYMENT.md.
//
// Single-instance deployments may leave it nil (no cross-pod staleness
// possible by definition) but verifying still costs nothing on cache
// miss (the verify path only runs on cache hit, which is exactly the
// case where the local map says "no change" and we want to confirm).
func (p *Impl) SetRedisForBalanceVerify(rdb *redis.Client) {
	p.redis = rdb
}

// BalanceDedupeSkippedCount returns the cumulative number of SetKeyBalance
// calls that the dedupe layer short-circuited. Exposed for metrics / ops
// dashboards — a healthy billing flow should show >99% hit rate (every per-
// request deduction shouldn't make it to PG).
func (p *Impl) BalanceDedupeSkippedCount() uint64 {
	return p.balanceDedupeSkipped.Load()
}

// =============================================================
// Unit 1: KeyMeta
// =============================================================

func (p *Impl) PublishKey(ctx context.Context, meta *types.KeyMeta) (types.BatchID, error) {
	fillKeyMetaDefaults(meta)
	if err := p.validateKeyMetaForPublish(ctx, meta); err != nil {
		return uuid.Nil, err
	}
	rows, err := buildPublishKeyRows(meta)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commit(ctx, "PublishKey", rows)
}

// ActivateKey is the sole activation boundary for locally metered API keys.
// One outbox row is important here: outbox rows can be claimed by different
// workers, while one composite row is applied by one Redis Lua transaction.
func (p *Impl) ActivateKey(ctx context.Context, meta *types.KeyMeta, hasBalance bool, reason string, decision types.KeyAdmissionDecision) (types.BatchID, error) {
	fillKeyMetaDefaults(meta)
	if err := p.validateKeyMetaForPublish(ctx, meta); err != nil {
		return uuid.Nil, err
	}
	if meta.Status != "active" {
		return uuid.Nil, fmt.Errorf("%w: activation requires keymeta.status=active", ErrInvalidArgument)
	}
	if err := validateBalanceReason(hasBalance, reason); err != nil {
		return uuid.Nil, err
	}
	if err := validateAdmissionDecision(decision); err != nil {
		return uuid.Nil, err
	}
	publisherRevision, err := p.store.NextRevision(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	rows, err := buildActivateKeyRows(meta, hasBalance, reason, decision, publisherRevision)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commit(ctx, "ActivateKey", rows)
}

func buildActivateKeyRows(meta *types.KeyMeta, hasBalance bool, reason string, decision types.KeyAdmissionDecision, publisherRevision int64) ([]outbox.OutboxRow, error) {
	if meta == nil {
		return nil, fmt.Errorf("%w: keymeta nil", ErrInvalidArgument)
	}
	if meta.Status != "active" {
		return nil, fmt.Errorf("%w: activation requires keymeta.status=active", ErrInvalidArgument)
	}
	metaPayload, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal keymeta: %v", ErrInvalidArgument, err)
	}
	configNotify, _ := json.Marshal(map[string]any{
		"version":  2,
		"type":     "key_update",
		"key_hash": meta.KeyHash,
		"key_id":   meta.KeyID,
		"revision": meta.Revision,
	})
	admissionNotify, _ := json.Marshal(map[string]any{
		"version":          1,
		"type":             "key_admission_update",
		"key_id":           meta.KeyID,
		"allowed":          hasBalance,
		"reason":           reason,
		"decision_id":      decision.ID,
		"decision_version": decision.Version,
	})
	value := "0"
	if hasBalance {
		value = "1"
	}
	payload, err := json.Marshal(map[string]any{
		"value":                 value,
		"reason":                reason,
		"has_balance":           hasBalance,
		"deleted":               false,
		"decision_id":           decision.ID,
		"decision_version":      decision.Version,
		"legacy":                decision.Legacy,
		"publisher_revision":    publisherRevision,
		"fingerprint":           outbox.ActivationFingerprint(hasBalance, reason, meta.KeyHash, metaPayload),
		"key_hash":              meta.KeyHash,
		"key_meta":              json.RawMessage(metaPayload),
		"config_notify_message": json.RawMessage(configNotify),
	})
	if err != nil {
		return nil, fmt.Errorf("%w: marshal activation: %v", ErrInvalidArgument, err)
	}
	return []outbox.OutboxRow{{
		Op:              outbox.OpKeyActivateAtomic,
		RedisField:      meta.KeyID,
		Payload:         payload,
		NotifyChannel:   ChannelAdmissionUpdate,
		NotifyMessage:   admissionNotify,
		SchemaVersion:   CurrentKeyMetaSchemaVersion,
		EnvelopeVersion: 1,
	}}, nil
}

func buildPublishKeyRows(meta *types.KeyMeta) ([]outbox.OutboxRow, error) {
	payload, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal: %v", ErrInvalidArgument, err)
	}
	notify, _ := json.Marshal(map[string]any{
		"version":  2,
		"type":     "key_update",
		"key_hash": meta.KeyHash,
		"key_id":   meta.KeyID,
		"revision": meta.Revision,
	})
	return []outbox.OutboxRow{
		{Op: outbox.OpHSet, RedisKey: KeyKeymeta, RedisField: meta.KeyHash, Payload: payload},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	}, nil
}

func (p *Impl) PublishKeyBulk(ctx context.Context, metas []*types.KeyMeta) (types.BatchID, error) {
	if err := validateBulkLen(len(metas)); err != nil {
		return uuid.Nil, err
	}
	rows := make([]outbox.OutboxRow, 0, len(metas)+1)
	for i, m := range metas {
		fillKeyMetaDefaults(m)
		if err := p.validateKeyMetaForPublish(ctx, m); err != nil {
			return uuid.Nil, fmt.Errorf("item %d: %w", i, err)
		}
		body, err := json.Marshal(m)
		if err != nil {
			return uuid.Nil, fmt.Errorf("%w: item %d marshal: %v", ErrInvalidArgument, i, err)
		}
		rows = append(rows, outbox.OutboxRow{
			Op: outbox.OpHSet, RedisKey: KeyKeymeta, RedisField: m.KeyHash, Payload: body,
		})
	}
	notify, _ := json.Marshal(map[string]any{
		"version": 2,
		"type":    "key_update_bulk",
		"count":   len(metas),
	})
	rows = append(rows, outbox.OutboxRow{
		Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify,
	})
	return p.commit(ctx, "PublishKeyBulk", rows)
}

func (p *Impl) DeleteKey(ctx context.Context, hash types.KeyHash) (types.BatchID, error) {
	if err := hash.Valid(); err != nil {
		return uuid.Nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "key_delete", "key_hash": string(hash)})
	return p.commit(ctx, "DeleteKey", []outbox.OutboxRow{
		{Op: outbox.OpHDel, RedisKey: KeyKeymeta, RedisField: string(hash)},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	})
}

func (p *Impl) DeleteKeyBulk(ctx context.Context, hashes []types.KeyHash) (types.BatchID, error) {
	if err := validateBulkLen(len(hashes)); err != nil {
		return uuid.Nil, err
	}
	rows := make([]outbox.OutboxRow, 0, len(hashes)+1)
	for i, h := range hashes {
		if err := h.Valid(); err != nil {
			return uuid.Nil, fmt.Errorf("item %d: %w", i, err)
		}
		rows = append(rows, outbox.OutboxRow{Op: outbox.OpHDel, RedisKey: KeyKeymeta, RedisField: string(h)})
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "key_delete_bulk", "count": len(hashes)})
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify})
	return p.commit(ctx, "DeleteKeyBulk", rows)
}

// =============================================================
// Unit 2: KeyBalance
// =============================================================

// keyBalanceReasons is the allow-list per publisher-api-reference.md §9.2.
// Empty reason is accepted only when hasBalance=true.
//
// 唯一上游事实源是 codesphere-billing 的 `PublisherBalanceReason`
// (apps/api/src/publisher/publisher.types.ts) —— 顺序与之逐行对齐,便于 diff。
// 这里缺一个值的后果不是「该 reason 降级」而是该 key 的 admission 投影恒 400:
// billing 消费 gateway.usage_events 时对 400/422 抛异常,整条 Kafka 分区队头阻塞,
// 同时 key 冻结在上一次成功投影的准入态(2026-09-08 国内 prod 实测,见下方 settlement_review)。
// 所以 billing 侧新增枚举必须与本表同车发布,且 publisher 先行。
var keyBalanceReasons = map[string]struct{}{
	"balance_depleted":           {},
	"key_spend_limit_exhausted":  {},
	"workspace_budget_exhausted": {},
	"rolling_quota_exhausted":    {},
	"manual_freeze":              {},
	"account_debt":               {},
	// settlement_risk_limit_exceeded ← pending_usage_exposure_limit
	"settlement_risk_limit_exceeded": {},
	// settlement_review ← settlement_policy_review。billing 把它从 billing_review 里单独拆出来,
	// 是因为客户拿到的 402 code 就是这个字符串,笼统的 billing_review 会让客户以为自己欠费。
	"settlement_review": {},
	// billing_review 同时是 billing 侧遇到未知限制原因（schema 漂移）时的 fail-closed 兜底。
	"billing_review": {},
}

func (p *Impl) SetKeyBalance(ctx context.Context, id types.KeyID, hasBalance bool, reason string, revision int64) (types.BatchID, error) {
	decision := types.KeyAdmissionDecision{Legacy: true}
	if revision > 0 {
		decision.ID = fmt.Sprintf("legacy-revision:%d", revision)
		decision.Version = revision
	}
	return p.SetKeyAdmission(ctx, id, hasBalance, reason, decision)
}

// SetKeyAdmission persists a Billing-owned admission decision. Versioned
// decisions always reach the Redis Lua CAS; the in-process balance cache is
// only an optimization for the pre-contract, unversioned legacy flow.
func (p *Impl) SetKeyAdmission(ctx context.Context, id types.KeyID, hasBalance bool, reason string, decision types.KeyAdmissionDecision) (types.BatchID, error) {
	if err := id.Valid(); err != nil {
		return uuid.Nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	if err := validateBalanceReason(hasBalance, reason); err != nil {
		return uuid.Nil, err
	}
	if err := validateAdmissionDecision(decision); err != nil {
		return uuid.Nil, err
	}
	dedupeEligible := balanceDedupeEligible(decision)
	// 2026-05-26: write-through cache dedupe. Billing consumers fire
	// SetKeyBalance after every per-request deduction; the bool flips
	// only across thresholds. Skipping unchanged-state calls eliminates
	// 99%+ of outbox writes, audit rows, and downstream Redis fan-out.
	// Idempotency-Key shortcut in commit() handles HTTP-level retries;
	// THIS shortcut handles the much higher-volume "logically redundant
	// state push" case. The two paths are independent — a fresh
	// Idempotency-Key with identical (key_id, value) is still dedup'd.
	p.balanceDedupeMu.Lock()
	cached, hit := p.balanceDedupe[id]
	ttl := p.balanceDedupeTTL
	p.balanceDedupeMu.Unlock()
	if dedupeEligible && ttl > 0 && hit && cached.HasBalance == hasBalance && cached.Reason == reason &&
		cached.DecisionVersion == decision.Version &&
		time.Since(cached.WrittenAt) < ttl {
		// 2026-05-26 HA Commit B-3: cross-pod cache staleness check.
		// Local cache says "I last wrote (hasBalance, reason) at t < TTL ago,
		// so Redis should still be that value". In a multi-instance HA
		// deployment another pod may have written a DIFFERENT value in
		// the meantime — the local cache has no way to know. Verify
		// against Redis before short-circuiting.
		//
		// On verify failure (Redis returns a different value, or HGet
		// errors): fall through and let the normal write path execute.
		// This restores correctness; the worst case is dedupe degrades
		// to no-op for this call (1 extra outbox write).
		//
		// On verify success (Redis matches local cache): proceed with
		// skip. The cache wasn't stale.
		//
		// p.redis == nil (single-instance / not wired) → trust the cache.
		// docs/HA_DEPLOYMENT.md documents this trade-off.
		if p.redis == nil || p.verifyBalanceCacheLocked(ctx, id, hasBalance) {
			p.balanceDedupeSkipped.Add(1)
			slog.Debug("SetKeyBalance skipped: state unchanged within TTL",
				"key_id", string(id),
				"has_balance", hasBalance,
				"reason", reason,
				"since", time.Since(cached.WrittenAt),
				"prior_batch", cached.BatchID,
				"total_skipped", p.balanceDedupeSkipped.Load(),
				"verify_mode", verifyModeLabel(p.redis != nil),
			)
			return cached.BatchID, nil
		}
		// Verify failed — Redis has a value different from our cache.
		// Another pod (or out-of-band Redis manipulation) wrote it.
		// Invalidate the local entry so we don't trip on it again, then
		// fall through to the real write path.
		p.balanceDedupeMu.Lock()
		delete(p.balanceDedupe, id)
		p.balanceDedupeMu.Unlock()
		slog.Info("SetKeyBalance dedupe verify failed: redis canonical value disagrees with local cache; will rewrite",
			"key_id", string(id),
			"local_cache_value", boolToFlag(hasBalance),
		)
	}

	// 2026-05-16: revision auto-fill — publisher fills when caller passes
	// 0; caller may still pass > 0 to preserve DR-replay ordering.
	//
	// 2026-05-26 HA Commit B: revision source is publisher_revision_seq
	// (PG sequence) instead of time.Now().UnixMicro(). The sequence is
	// globally monotonic across all publisher pods so the worker's
	// revision-CAS on gw:key_balance correctly rejects stale writes
	// regardless of cross-pod clock drift. Fetched here (after dedupe
	// miss) so that the common dedup-hit path doesn't burn a PG RTT.
	publisherRevision, err := p.store.NextRevision(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	rows := buildKeyAdmissionRows(id, hasBalance, reason, false, decision, publisherRevision)
	// Keep the historical method_name so existing outbox dashboards and
	// dead-letter filters remain continuous across the wire-contract upgrade.
	batchID, err := p.commit(ctx, "SetKeyBalance", rows)
	if err == nil && dedupeEligible && ttl > 0 {
		// Cache only on successful commit. On failure leave the cache
		// untouched so the next retry can still try a real write.
		p.balanceDedupeMu.Lock()
		p.balanceDedupe[id] = balanceDedupeEntry{
			HasBalance:      hasBalance,
			Reason:          reason,
			DecisionVersion: decision.Version,
			WrittenAt:       time.Now(),
			BatchID:         batchID,
		}
		p.balanceDedupeMu.Unlock()
	}
	return batchID, err
}

func (p *Impl) SetKeyBalanceBulk(ctx context.Context, items []types.KeyBalanceItem, reason string) (types.BatchID, error) {
	return p.SetKeyAdmissionBulk(ctx, mapLegacyBalanceItems(items, reason))
}

func mapLegacyBalanceItems(items []types.KeyBalanceItem, reason string) []types.KeyAdmissionItem {
	admissions := make([]types.KeyAdmissionItem, len(items))
	for i, item := range items {
		decision := types.KeyAdmissionDecision{Legacy: true}
		if item.Revision > 0 {
			decision.ID = fmt.Sprintf("legacy-revision:%d", item.Revision)
			decision.Version = item.Revision
		}
		itemReason := reason
		if item.HasBalance {
			itemReason = ""
		}
		admissions[i] = types.KeyAdmissionItem{
			KeyID:      item.KeyID,
			HasBalance: item.HasBalance,
			Reason:     itemReason,
			Decision:   decision,
		}
	}
	return admissions
}

func (p *Impl) SetKeyAdmissionBulk(ctx context.Context, items []types.KeyAdmissionItem) (types.BatchID, error) {
	if err := validateBulkLen(len(items)); err != nil {
		return uuid.Nil, err
	}
	rows := make([]outbox.OutboxRow, 0, len(items))
	bulkRevision, err := p.store.NextRevision(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	for i, it := range items {
		if err := it.KeyID.Valid(); err != nil {
			return uuid.Nil, fmt.Errorf("item %d: %w", i, err)
		}
		if err := validateBalanceReason(it.HasBalance, it.Reason); err != nil {
			return uuid.Nil, fmt.Errorf("item %d: %w", i, err)
		}
		if err := validateAdmissionDecision(it.Decision); err != nil {
			return uuid.Nil, fmt.Errorf("item %d: %w", i, err)
		}
		rows = append(rows, buildKeyAdmissionRows(it.KeyID, it.HasBalance, it.Reason, false, it.Decision, bulkRevision)...)
	}
	return p.commit(ctx, "SetKeyBalanceBulk", rows)
}

func (p *Impl) DeleteKeyBalance(ctx context.Context, id types.KeyID) (types.BatchID, error) {
	return p.DeleteKeyAdmission(ctx, id, types.KeyAdmissionDecision{Legacy: true})
}

func (p *Impl) DeleteKeyAdmission(ctx context.Context, id types.KeyID, decision types.KeyAdmissionDecision) (types.BatchID, error) {
	if err := id.Valid(); err != nil {
		return uuid.Nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	if err := validateAdmissionDecision(decision); err != nil {
		return uuid.Nil, err
	}
	publisherRevision, err := p.store.NextRevision(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commit(ctx, "DeleteKeyBalance", buildKeyAdmissionRows(id, false, "", true, decision, publisherRevision))
}

func (p *Impl) GetKeyAdmissionDelivery(ctx context.Context, batchID types.BatchID) (AdmissionDeliveryReceipt, error) {
	rows, err := p.store.QueryBatch(ctx, batchID)
	if err != nil {
		return AdmissionDeliveryReceipt{}, err
	}
	if len(rows) == 0 {
		return AdmissionDeliveryReceipt{}, ErrAdmissionDeliveryNotFound
	}
	for _, row := range rows {
		if row.Op != outbox.OpKeyBalanceCAS {
			return AdmissionDeliveryReceipt{}, ErrAdmissionDeliveryNotFound
		}
	}
	return summarizeAdmissionDelivery(batchID, rows)
}

func summarizeAdmissionDelivery(batchID types.BatchID, rows []outbox.OutboxRow) (AdmissionDeliveryReceipt, error) {
	if len(rows) == 0 {
		return AdmissionDeliveryReceipt{}, ErrAdmissionDeliveryNotFound
	}
	receipt := AdmissionDeliveryReceipt{BatchID: batchID, Total: len(rows)}
	for _, row := range rows {
		if row.LastError != "" {
			receipt.LastError = row.LastError
		}
		switch row.Status {
		case outbox.StatusDeadLetter:
			receipt.DeadLetterCount++
		case outbox.StatusPending, outbox.StatusInProgress, outbox.StatusFailed:
			// failed is transient until the worker exhausts attempts.
			receipt.PendingCount++
		case outbox.StatusDelivered:
			switch row.DeliveryOutcome {
			case outbox.DeliveryOutcomeApplied:
				receipt.AppliedCount++
			case "":
				receipt.UnverifiedCount++
			case outbox.DeliveryOutcomeIdempotent:
				receipt.IdempotentCount++
			case outbox.DeliveryOutcomeSuperseded:
				receipt.SupersededCount++
			case outbox.DeliveryOutcomeConflict:
				receipt.ConflictCount++
			default:
				return AdmissionDeliveryReceipt{}, fmt.Errorf("unknown admission delivery outcome %q", row.DeliveryOutcome)
			}
		default:
			return AdmissionDeliveryReceipt{}, fmt.Errorf("unknown outbox status %q", row.Status)
		}
	}
	switch {
	case receipt.DeadLetterCount > 0:
		receipt.Status = AdmissionDeliveryDeadLetter
	case receipt.PendingCount > 0:
		receipt.Status = AdmissionDeliveryPending
	case receipt.UnverifiedCount > 0:
		receipt.Status = AdmissionDeliveryUnverified
	case receipt.ConflictCount > 0:
		receipt.Status = AdmissionDeliveryConflict
	case terminalAdmissionOutcomeKinds(receipt) > 1:
		receipt.Status = AdmissionDeliveryMixed
	case receipt.AppliedCount > 0:
		receipt.Status = AdmissionDeliveryApplied
	case receipt.IdempotentCount > 0:
		receipt.Status = AdmissionDeliveryIdempotent
	case receipt.SupersededCount > 0:
		receipt.Status = AdmissionDeliverySuperseded
	default:
		return AdmissionDeliveryReceipt{}, fmt.Errorf("admission delivery batch has no classifiable rows")
	}
	return receipt, nil
}

func terminalAdmissionOutcomeKinds(receipt AdmissionDeliveryReceipt) int {
	kinds := 0
	for _, count := range []int{receipt.AppliedCount, receipt.IdempotentCount, receipt.SupersededCount} {
		if count > 0 {
			kinds++
		}
	}
	return kinds
}

func balanceDedupeEligible(decision types.KeyAdmissionDecision) bool {
	return decision.Legacy && decision.Version == 0
}

func validateAdmissionDecision(decision types.KeyAdmissionDecision) error {
	if decision.Version < 0 {
		return fmt.Errorf("%w: decision_version must be > 0 when supplied", ErrInvalidArgument)
	}
	if decision.Legacy {
		if decision.Version > 0 && strings.TrimSpace(decision.ID) == "" {
			return fmt.Errorf("%w: legacy revision mapping requires decision_id", ErrInvalidArgument)
		}
		return nil
	}
	if decision.Version <= 0 {
		return fmt.Errorf("%w: decision_version must be > 0", ErrInvalidArgument)
	}
	if _, err := uuid.Parse(decision.ID); err != nil {
		return fmt.Errorf("%w: decision_id must be UUID: %v", ErrInvalidArgument, err)
	}
	return nil
}

func buildKeyAdmissionRows(id types.KeyID, hasBalance bool, reason string, deleted bool, decision types.KeyAdmissionDecision, publisherRevision int64) []outbox.OutboxRow {
	value := "0"
	if hasBalance {
		value = "1"
	}
	payload, _ := json.Marshal(map[string]any{
		"value":              value,
		"reason":             reason,
		"has_balance":        hasBalance,
		"deleted":            deleted,
		"decision_id":        decision.ID,
		"decision_version":   decision.Version,
		"legacy":             decision.Legacy,
		"publisher_revision": publisherRevision,
		"fingerprint":        outbox.AdmissionFingerprint(hasBalance, reason, deleted),
	})
	notifyVersion := decision.Version
	if notifyVersion <= 0 {
		// Legacy calls have no Billing watermark. Use Publisher's monotonic
		// revision solely as a positive cache-invalidation version; Redis CAS
		// ordering still treats the request as legacy and never lets it
		// override an explicit Billing decision.
		notifyVersion = publisherRevision
	}
	notify, _ := json.Marshal(map[string]any{
		"version":          1,
		"type":             "key_admission_update",
		"key_id":           string(id),
		"decision_version": notifyVersion,
		"allowed":          hasBalance,
		"deleted":          deleted,
	})
	return []outbox.OutboxRow{
		{
			Op:            outbox.OpKeyBalanceCAS,
			RedisKey:      KeyKeyBalance,
			RedisField:    string(id),
			Payload:       payload,
			NotifyChannel: ChannelAdmissionUpdate,
			NotifyMessage: notify,
		},
	}
}

// =============================================================
// Unit 3: RevokedKeys
// =============================================================

func (p *Impl) AddRevokedKey(ctx context.Context, hash types.KeyHash) (types.BatchID, error) {
	if err := hash.Valid(); err != nil {
		return uuid.Nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "revoked_key_added", "key_hash": string(hash)})
	return p.commitWithRevision(ctx, "AddRevokedKey", DomainSecurity, []outbox.OutboxRow{
		{Op: outbox.OpSAdd, RedisKey: KeyRevokedKeys, RedisMember: string(hash)},
		{Op: outbox.OpPublish, NotifyChannel: ChannelSecurityUpdate, NotifyMessage: notify},
	})
}

func (p *Impl) AddRevokedKeysBulk(ctx context.Context, hashes []types.KeyHash) (types.BatchID, error) {
	rows, err := buildSetMemberBulk(hashes, "RevokedKeys", KeyRevokedKeys, outbox.OpSAdd, validateKeyHash)
	if err != nil {
		return uuid.Nil, err
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "revoked_keys_added_bulk", "count": len(hashes)})
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpPublish, NotifyChannel: ChannelSecurityUpdate, NotifyMessage: notify})
	return p.commitWithRevision(ctx, "AddRevokedKeysBulk", DomainSecurity, rows)
}

func (p *Impl) RemoveRevokedKey(ctx context.Context, hash types.KeyHash) (types.BatchID, error) {
	if err := hash.Valid(); err != nil {
		return uuid.Nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "revoked_key_removed", "key_hash": string(hash)})
	return p.commitWithRevision(ctx, "RemoveRevokedKey", DomainSecurity, []outbox.OutboxRow{
		{Op: outbox.OpSRem, RedisKey: KeyRevokedKeys, RedisMember: string(hash)},
		{Op: outbox.OpPublish, NotifyChannel: ChannelSecurityUpdate, NotifyMessage: notify},
	})
}

func (p *Impl) RemoveRevokedKeysBulk(ctx context.Context, hashes []types.KeyHash) (types.BatchID, error) {
	rows, err := buildSetMemberBulk(hashes, "RevokedKeys", KeyRevokedKeys, outbox.OpSRem, validateKeyHash)
	if err != nil {
		return uuid.Nil, err
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "revoked_keys_removed_bulk", "count": len(hashes)})
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpPublish, NotifyChannel: ChannelSecurityUpdate, NotifyMessage: notify})
	return p.commitWithRevision(ctx, "RemoveRevokedKeysBulk", DomainSecurity, rows)
}

func (p *Impl) DisableKey(ctx context.Context, hash types.KeyHash) (types.BatchID, error) {
	rows, err := buildKeyLifecycleRows([]types.KeyHash{hash}, false)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commitWithRevision(ctx, "DisableKey", DomainSecurity, rows)
}

func (p *Impl) DisableKeysBulk(ctx context.Context, hashes []types.KeyHash) (types.BatchID, error) {
	rows, err := buildKeyLifecycleRows(hashes, false)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commitWithRevision(ctx, "DisableKeysBulk", DomainSecurity, rows)
}

func (p *Impl) EnableKey(ctx context.Context, hash types.KeyHash) (types.BatchID, error) {
	rows, err := buildKeyLifecycleRows([]types.KeyHash{hash}, true)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commitWithRevision(ctx, "EnableKey", DomainSecurity, rows)
}

func (p *Impl) EnableKeysBulk(ctx context.Context, hashes []types.KeyHash) (types.BatchID, error) {
	rows, err := buildKeyLifecycleRows(hashes, true)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commitWithRevision(ctx, "EnableKeysBulk", DomainSecurity, rows)
}

// PublishRevokedKeys reseeds the set: DEL then SADD all members. Mostly
// used for cold-restore / disaster recovery.
func (p *Impl) PublishRevokedKeys(ctx context.Context, hashes []types.KeyHash) (types.BatchID, error) {
	if len(hashes) > types.MaxBulkItems {
		return uuid.Nil, fmt.Errorf("%w: bulk too large", ErrInvalidArgument)
	}
	rows := []outbox.OutboxRow{{Op: outbox.OpDel, RedisKey: KeyRevokedKeys}}
	for i, h := range hashes {
		if err := h.Valid(); err != nil {
			return uuid.Nil, fmt.Errorf("item %d: %w", i, err)
		}
		rows = append(rows, outbox.OutboxRow{Op: outbox.OpSAdd, RedisKey: KeyRevokedKeys, RedisMember: string(h)})
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "revoked_keys_replaced", "count": len(hashes)})
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpPublish, NotifyChannel: ChannelSecurityUpdate, NotifyMessage: notify})
	return p.commitWithRevision(ctx, "PublishRevokedKeys", DomainSecurity, rows)
}

// =============================================================
// Unit 4: BlockedIPs (same pattern as RevokedKeys, different validator)
// =============================================================

func (p *Impl) AddBlockedIP(ctx context.Context, ip string) (types.BatchID, error) {
	if err := validateIP(ip); err != nil {
		return uuid.Nil, err
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "blocked_ip_added"})
	return p.commitWithRevision(ctx, "AddBlockedIP", DomainSecurity, []outbox.OutboxRow{
		{Op: outbox.OpSAdd, RedisKey: KeyBlockedIPs, RedisMember: ip},
		{Op: outbox.OpPublish, NotifyChannel: ChannelSecurityUpdate, NotifyMessage: notify},
	})
}

func (p *Impl) AddBlockedIPsBulk(ctx context.Context, ips []string) (types.BatchID, error) {
	rows, err := buildStringSetMemberBulk(ips, "BlockedIPs", KeyBlockedIPs, outbox.OpSAdd, validateIP)
	if err != nil {
		return uuid.Nil, err
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "blocked_ips_added_bulk", "count": len(ips)})
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpPublish, NotifyChannel: ChannelSecurityUpdate, NotifyMessage: notify})
	return p.commitWithRevision(ctx, "AddBlockedIPsBulk", DomainSecurity, rows)
}

func (p *Impl) RemoveBlockedIP(ctx context.Context, ip string) (types.BatchID, error) {
	if err := validateIP(ip); err != nil {
		return uuid.Nil, err
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "blocked_ip_removed"})
	return p.commitWithRevision(ctx, "RemoveBlockedIP", DomainSecurity, []outbox.OutboxRow{
		{Op: outbox.OpSRem, RedisKey: KeyBlockedIPs, RedisMember: ip},
		{Op: outbox.OpPublish, NotifyChannel: ChannelSecurityUpdate, NotifyMessage: notify},
	})
}

func (p *Impl) RemoveBlockedIPsBulk(ctx context.Context, ips []string) (types.BatchID, error) {
	rows, err := buildStringSetMemberBulk(ips, "BlockedIPs", KeyBlockedIPs, outbox.OpSRem, validateIP)
	if err != nil {
		return uuid.Nil, err
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "blocked_ips_removed_bulk", "count": len(ips)})
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpPublish, NotifyChannel: ChannelSecurityUpdate, NotifyMessage: notify})
	return p.commitWithRevision(ctx, "RemoveBlockedIPsBulk", DomainSecurity, rows)
}

func (p *Impl) PublishBlockedIPs(ctx context.Context, ips []string) (types.BatchID, error) {
	if len(ips) > types.MaxBulkItems {
		return uuid.Nil, fmt.Errorf("%w: bulk too large", ErrInvalidArgument)
	}
	rows := []outbox.OutboxRow{{Op: outbox.OpDel, RedisKey: KeyBlockedIPs}}
	for i, ip := range ips {
		if err := validateIP(ip); err != nil {
			return uuid.Nil, fmt.Errorf("item %d: %w", i, err)
		}
		rows = append(rows, outbox.OutboxRow{Op: outbox.OpSAdd, RedisKey: KeyBlockedIPs, RedisMember: ip})
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "blocked_ips_replaced", "count": len(ips)})
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpPublish, NotifyChannel: ChannelSecurityUpdate, NotifyMessage: notify})
	return p.commitWithRevision(ctx, "PublishBlockedIPs", DomainSecurity, rows)
}

// =============================================================
// Unit 5: ModelCatalog
// =============================================================

func (p *Impl) PublishModelCatalog(ctx context.Context, modelID string, entry *types.ModelCatalogEntry) (types.BatchID, error) {
	rows, err := buildPublishModelCatalogRows(modelID, entry)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commitWithRevision(ctx, "PublishModelCatalog", DomainTopology, rows)
}

func buildPublishModelCatalogRows(modelID string, entry *types.ModelCatalogEntry) ([]outbox.OutboxRow, error) {
	if strings.TrimSpace(modelID) == "" || entry == nil {
		return nil, fmt.Errorf("%w: model_id + entry required", ErrInvalidArgument)
	}
	fillModelCatalogEntryDefaults(entry)
	payload, _ := json.Marshal(entry)
	// notify 不预填 "revision" — worker.injectRevision 是 commitWithRevision flow
	// 里 revision 字段的唯一注入点(用 OpIncr 计数器值)。预填 entry.Revision
	// (UnixMicro auto-fill)会与 Incr 值冲突;若 worker 重启 mid-batch、batchState
	// 丢失,injectRevision 不触发,带着 1.7e15 的 UnixMicro 发到 gateway 会污染
	// gateway local revision tracker,后续正常 publish(Incr 计数器值)永久 skip。
	// 走"无 revision"路径让 gateway 自己 GET 当前计数器值兜底。
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "model_catalog_update", "model_id": modelID})
	return []outbox.OutboxRow{
		{Op: outbox.OpHSet, RedisKey: KeyModelCatalog, RedisField: modelID, Payload: payload},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	}, nil
}

func (p *Impl) PublishModelCatalogBulk(ctx context.Context, items []types.ModelCatalogItem) (types.BatchID, error) {
	if err := validateBulkLen(len(items)); err != nil {
		return uuid.Nil, err
	}
	rows := make([]outbox.OutboxRow, 0, len(items)+1)
	for i, it := range items {
		if strings.TrimSpace(it.ModelID) == "" || it.Entry == nil {
			return uuid.Nil, fmt.Errorf("%w: item %d missing fields", ErrInvalidArgument, i)
		}
		fillModelCatalogEntryDefaults(it.Entry)
		body, _ := json.Marshal(it.Entry)
		rows = append(rows, outbox.OutboxRow{Op: outbox.OpHSet, RedisKey: KeyModelCatalog, RedisField: it.ModelID, Payload: body})
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "model_catalog_update_bulk", "count": len(items)})
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify})
	return p.commitWithRevision(ctx, "PublishModelCatalogBulk", DomainTopology, rows)
}

func (p *Impl) DeleteModelCatalog(ctx context.Context, modelID string) (types.BatchID, error) {
	if strings.TrimSpace(modelID) == "" {
		return uuid.Nil, fmt.Errorf("%w: model_id required", ErrInvalidArgument)
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "model_catalog_delete", "model_id": modelID})
	return p.commitWithRevision(ctx, "DeleteModelCatalog", DomainTopology, []outbox.OutboxRow{
		{Op: outbox.OpHDel, RedisKey: KeyModelCatalog, RedisField: modelID},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	})
}

// =============================================================
// Unit 6: ModelRoute
// =============================================================

func (p *Impl) PublishModelRoute(ctx context.Context, modelID string, route *types.RouteConfig) (types.BatchID, error) {
	rows, err := buildPublishModelRouteRows(ctx, modelID, route)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commitWithRevision(ctx, "PublishModelRoute", DomainTopology, rows)
}

func buildPublishModelRouteRows(ctx context.Context, modelID string, route *types.RouteConfig) ([]outbox.OutboxRow, error) {
	if strings.TrimSpace(modelID) == "" || route == nil {
		return nil, fmt.Errorf("%w: model_id + route required", ErrInvalidArgument)
	}
	for i, entry := range route.Providers {
		if err := validateEnum(fmt.Sprintf("route.providers[%d].status", i), entry.Status, routeEntryStatuses); err != nil {
			return nil, err
		}
	}
	fillRouteConfigDefaults(route)
	payload, _ := json.Marshal(route)
	payloadHash := sha256.Sum256(payload)
	slog.InfoContext(ctx, "publish model route pre commit",
		"event", "publish_model_route_pre_commit",
		"model_id", modelID,
		"provider_ids", routeConfigProviderIDs(route.Providers),
		"provider_count", len(route.Providers),
		"provider_strategy", route.ProviderStrategy,
		"redis_key", KeyRoutes,
		"redis_field", modelID,
		"payload_len", len(payload),
		"payload_sha256", fmt.Sprintf("%x", payloadHash[:8]),
	)
	// see PublishModelCatalog: revision 由 worker.injectRevision 唯一注入,不在此预填
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "model_route_update", "model_id": modelID})
	return []outbox.OutboxRow{
		{Op: outbox.OpHSet, RedisKey: KeyRoutes, RedisField: modelID, Payload: payload},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	}, nil
}

func (p *Impl) PublishModelRouteBulk(ctx context.Context, items []types.ModelRouteItem) (types.BatchID, error) {
	if err := validateBulkLen(len(items)); err != nil {
		return uuid.Nil, err
	}
	rows := make([]outbox.OutboxRow, 0, len(items)+1)
	for i, it := range items {
		if strings.TrimSpace(it.ModelID) == "" || it.Route == nil {
			return uuid.Nil, fmt.Errorf("%w: item %d missing fields", ErrInvalidArgument, i)
		}
		for j, entry := range it.Route.Providers {
			if err := validateEnum(fmt.Sprintf("items[%d].route.providers[%d].status", i, j), entry.Status, routeEntryStatuses); err != nil {
				return uuid.Nil, err
			}
		}
		fillRouteConfigDefaults(it.Route)
		body, _ := json.Marshal(it.Route)
		rows = append(rows, outbox.OutboxRow{Op: outbox.OpHSet, RedisKey: KeyRoutes, RedisField: it.ModelID, Payload: body})
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "model_route_update_bulk", "count": len(items)})
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify})
	return p.commitWithRevision(ctx, "PublishModelRouteBulk", DomainTopology, rows)
}

func (p *Impl) DeleteModelRoute(ctx context.Context, modelID string) (types.BatchID, error) {
	if strings.TrimSpace(modelID) == "" {
		return uuid.Nil, fmt.Errorf("%w: model_id required", ErrInvalidArgument)
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "model_route_delete", "model_id": modelID})
	return p.commitWithRevision(ctx, "DeleteModelRoute", DomainTopology, []outbox.OutboxRow{
		{Op: outbox.OpHDel, RedisKey: KeyRoutes, RedisField: modelID},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	})
}

// =============================================================
// Unit 7: KeyPool
// =============================================================

// PublishKeyPool: whole-pool replace. Wipes gw:keypool:{provider_id}, then
// HSETs every entry, then writes meta to gw:keypool_meta. Designed for
// initial sync and disaster recovery — routine add/remove of a single
// upstream key should use PublishKeyPoolEntry / DeleteKeyPoolEntry which
// touch only one Hash field.
func (p *Impl) PublishKeyPool(ctx context.Context, providerID string, pool *types.KeyPoolConfig) (types.BatchID, error) {
	rows, err := buildPublishKeyPoolRows(providerID, pool)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commitWithRevision(ctx, "PublishKeyPool", DomainCredential, rows)
}

func buildPublishKeyPoolRows(providerID string, pool *types.KeyPoolConfig) ([]outbox.OutboxRow, error) {
	if strings.TrimSpace(providerID) == "" || pool == nil {
		return nil, fmt.Errorf("%w: provider_id + pool required", ErrInvalidArgument)
	}
	fillKeyPoolMetaDefaults(&pool.Meta)
	poolKey := KeyPoolKey(providerID)
	rows := make([]outbox.OutboxRow, 0, len(pool.Entries)+3)
	// 1. wipe existing per-key hash
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpDel, RedisKey: poolKey})
	// 2. one HSET per entry
	for i := range pool.Entries {
		entry := &pool.Entries[i]
		if entry.KeyID == "" {
			return nil, fmt.Errorf("%w: entry %d missing key_id", ErrInvalidArgument, i)
		}
		if err := validateKeyPoolEntryProviderAccountRef(fmt.Sprintf("entries[%d]", i), entry); err != nil {
			return nil, err
		}
		if err := validateEnum(fmt.Sprintf("entries[%d].status", i), entry.Status, keyPoolEntryStatuses); err != nil {
			return nil, err
		}
		fillKeyPoolEntryDefaults(entry)
		body, _ := json.Marshal(entry)
		rows = append(rows, outbox.OutboxRow{Op: outbox.OpHSet, RedisKey: poolKey, RedisField: entry.KeyID, Payload: body})
	}
	// 3. pool meta (Strategy + Revision)
	metaBody, _ := json.Marshal(pool.Meta)
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpHSet, RedisKey: KeyKeyPoolMeta, RedisField: providerID, Payload: metaBody})
	// 4. one notify for the whole batch — see PublishModelCatalog: revision 由
	// worker.injectRevision 唯一注入,不在此预填
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "keypool_replace", "provider_id": providerID, "count": len(pool.Entries)})
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify})
	return rows, nil
}

func (p *Impl) PublishKeyPoolBulk(ctx context.Context, items []types.KeyPoolItem) (types.BatchID, error) {
	if err := validateBulkLen(len(items)); err != nil {
		return uuid.Nil, err
	}
	rows := make([]outbox.OutboxRow, 0, len(items)*4+1)
	for i, it := range items {
		if strings.TrimSpace(it.ProviderID) == "" || it.Pool == nil {
			return uuid.Nil, fmt.Errorf("%w: item %d missing fields", ErrInvalidArgument, i)
		}
		fillKeyPoolMetaDefaults(&it.Pool.Meta)
		poolKey := KeyPoolKey(it.ProviderID)
		rows = append(rows, outbox.OutboxRow{Op: outbox.OpDel, RedisKey: poolKey})
		for j := range it.Pool.Entries {
			entry := &it.Pool.Entries[j]
			if entry.KeyID == "" {
				return uuid.Nil, fmt.Errorf("%w: item %d entry %d missing key_id", ErrInvalidArgument, i, j)
			}
			if err := validateKeyPoolEntryProviderAccountRef(fmt.Sprintf("items[%d].entries[%d]", i, j), entry); err != nil {
				return uuid.Nil, err
			}
			if err := validateEnum(fmt.Sprintf("items[%d].entries[%d].status", i, j), entry.Status, keyPoolEntryStatuses); err != nil {
				return uuid.Nil, err
			}
			fillKeyPoolEntryDefaults(entry)
			body, _ := json.Marshal(entry)
			rows = append(rows, outbox.OutboxRow{Op: outbox.OpHSet, RedisKey: poolKey, RedisField: entry.KeyID, Payload: body})
		}
		metaBody, _ := json.Marshal(it.Pool.Meta)
		rows = append(rows, outbox.OutboxRow{Op: outbox.OpHSet, RedisKey: KeyKeyPoolMeta, RedisField: it.ProviderID, Payload: metaBody})
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "keypool_replace_bulk", "count": len(items)})
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify})
	return p.commitWithRevision(ctx, "PublishKeyPoolBulk", DomainCredential, rows)
}

func (p *Impl) DeleteKeyPool(ctx context.Context, providerID string) (types.BatchID, error) {
	if strings.TrimSpace(providerID) == "" {
		return uuid.Nil, fmt.Errorf("%w: provider_id required", ErrInvalidArgument)
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "keypool_delete", "provider_id": providerID})
	return p.commitWithRevision(ctx, "DeleteKeyPool", DomainCredential, []outbox.OutboxRow{
		{Op: outbox.OpDel, RedisKey: KeyPoolKey(providerID)},
		{Op: outbox.OpHDel, RedisKey: KeyKeyPoolMeta, RedisField: providerID},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	})
}

// defaultKeyPoolMetaJSON returns the placeholder meta written by entry-only
// add paths via HSETNX. Strategy="" lets gateway fall back to its planner
// default selector; Revision uses the current micro-timestamp so the
// initial meta carries a meaningful version even when caller skipped the
// PublishKeyPool integration path. HSETNX semantics guarantee this is a
// no-op when a real meta (written by PublishKeyPool/Bulk) already exists.
func defaultKeyPoolMetaJSON(providerID string) []byte {
	body, _ := json.Marshal(types.KeyPoolMeta{
		ProviderID: providerID,
		Strategy:   "", // empty → gateway planner default key selector
		Revision:   time.Now().UnixMicro(),
	})
	return body
}

// PublishKeyPoolEntry upserts one upstream key inside a provider's pool.
// Only touches one Hash field — never rewrites the whole pool.
//
// 2026-05-18 fix: the entry-only path used to skip gw:keypool_meta entirely.
// Gateway's loadAllKeyPools enumerates providers via HGETALL gw:keypool_meta,
// so a provider whose pool was created entirely through PublishKeyPoolEntry
// (never via PublishKeyPool) had no meta entry → gateway never read its keys
// at startup → the whole pool was effectively invisible. We now HSETNX a
// placeholder meta alongside each entry write; if PublishKeyPool later
// writes a real meta it overrides via HSET (HSETNX is idempotent + non-
// destructive, so it's safe to call every time).
func (p *Impl) PublishKeyPoolEntry(ctx context.Context, providerID string, entry *types.KeyPoolEntry) (types.BatchID, error) {
	rows, err := buildPublishKeyPoolEntryRows(providerID, entry)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commitWithRevision(ctx, "PublishKeyPoolEntry", DomainCredential, rows)
}

func buildPublishKeyPoolEntryRows(providerID string, entry *types.KeyPoolEntry) ([]outbox.OutboxRow, error) {
	if strings.TrimSpace(providerID) == "" || entry == nil || strings.TrimSpace(entry.KeyID) == "" {
		return nil, fmt.Errorf("%w: provider_id + entry.key_id required", ErrInvalidArgument)
	}
	if err := validateKeyPoolEntryProviderAccountRef("entry", entry); err != nil {
		return nil, err
	}
	if err := validateEnum("entry.status", entry.Status, keyPoolEntryStatuses); err != nil {
		return nil, err
	}
	fillKeyPoolEntryDefaults(entry)
	body, _ := json.Marshal(entry)
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "keypool_entry_update", "provider_id": providerID, "key_id": entry.KeyID})
	return []outbox.OutboxRow{
		// HSETNX a placeholder meta so gateway's keypool_meta-driven
		// provider enumeration always covers this provider, even if
		// PublishKeyPool was never called for it. No-op when real meta
		// already exists.
		{Op: outbox.OpHSetNX, RedisKey: KeyKeyPoolMeta, RedisField: providerID, Payload: defaultKeyPoolMetaJSON(providerID)},
		{Op: outbox.OpHSet, RedisKey: KeyPoolKey(providerID), RedisField: entry.KeyID, Payload: body},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	}, nil
}

func (p *Impl) PublishKeyPoolEntryBulk(ctx context.Context, items []types.KeyPoolEntryItem) (types.BatchID, error) {
	rows, err := buildPublishKeyPoolEntryBulkRows(items)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commitWithRevision(ctx, "PublishKeyPoolEntryBulk", DomainCredential, rows)
}

func buildPublishKeyPoolEntryBulkRows(items []types.KeyPoolEntryItem) ([]outbox.OutboxRow, error) {
	if err := validateBulkLen(len(items)); err != nil {
		return nil, err
	}
	// 2N+1 worst case: one HSETNX placeholder meta + one HSET entry per
	// item, plus the trailing PUBLISH. Dedup HSETNX per provider so a
	// bulk batch touching the same provider 100 times only emits one
	// meta-placeholder row.
	rows := make([]outbox.OutboxRow, 0, len(items)*2+1)
	seenMeta := make(map[string]bool, len(items))
	for i, it := range items {
		if strings.TrimSpace(it.ProviderID) == "" || it.Entry == nil || strings.TrimSpace(it.Entry.KeyID) == "" {
			return nil, fmt.Errorf("%w: item %d missing provider_id or entry.key_id", ErrInvalidArgument, i)
		}
		if err := validateKeyPoolEntryProviderAccountRef(fmt.Sprintf("items[%d].entry", i), it.Entry); err != nil {
			return nil, err
		}
		if err := validateEnum(fmt.Sprintf("items[%d].entry.status", i), it.Entry.Status, keyPoolEntryStatuses); err != nil {
			return nil, err
		}
		if !seenMeta[it.ProviderID] {
			// Same placeholder-meta HSETNX as PublishKeyPoolEntry; see that
			// method's comment for the rationale (gateway enumerates
			// providers via gw:keypool_meta and would otherwise miss any
			// provider whose pool was created entirely via entry-only writes).
			rows = append(rows, outbox.OutboxRow{Op: outbox.OpHSetNX, RedisKey: KeyKeyPoolMeta, RedisField: it.ProviderID, Payload: defaultKeyPoolMetaJSON(it.ProviderID)})
			seenMeta[it.ProviderID] = true
		}
		fillKeyPoolEntryDefaults(it.Entry)
		body, _ := json.Marshal(it.Entry)
		rows = append(rows, outbox.OutboxRow{Op: outbox.OpHSet, RedisKey: KeyPoolKey(it.ProviderID), RedisField: it.Entry.KeyID, Payload: body})
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "keypool_entry_update_bulk", "count": len(items)})
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify})
	return rows, nil
}

func validateKeyPoolEntryProviderAccountRef(path string, entry *types.KeyPoolEntry) error {
	if entry == nil {
		return nil
	}
	if entry.ProviderAccountRef == "" {
		return fmt.Errorf("%w: %s.provider_account_ref required for new writes", ErrInvalidArgument, path)
	}
	if err := types.ValidateProviderAccountRef(entry.ProviderAccountRef); err != nil {
		return fmt.Errorf("%w: %s.provider_account_ref invalid: %v", ErrInvalidArgument, path, err)
	}
	if entry.ProviderAccountRef == entry.KeyID {
		return fmt.Errorf("%w: %s.provider_account_ref must not reuse key_id", ErrInvalidArgument, path)
	}
	if entry.ProviderAccountRef == entry.Ciphertext {
		return fmt.Errorf("%w: %s.provider_account_ref must not reuse credential material or reference", ErrInvalidArgument, path)
	}
	return nil
}

func (p *Impl) DeleteKeyPoolEntry(ctx context.Context, providerID, keyID string) (types.BatchID, error) {
	if strings.TrimSpace(providerID) == "" || strings.TrimSpace(keyID) == "" {
		return uuid.Nil, fmt.Errorf("%w: provider_id + key_id required", ErrInvalidArgument)
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "keypool_entry_delete", "provider_id": providerID, "key_id": keyID})
	return p.commitWithRevision(ctx, "DeleteKeyPoolEntry", DomainCredential, []outbox.OutboxRow{
		{Op: outbox.OpHDel, RedisKey: KeyPoolKey(providerID), RedisField: keyID},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	})
}

func (p *Impl) DeleteKeyPoolEntryBulk(ctx context.Context, items []types.KeyPoolEntryDeleteItem) (types.BatchID, error) {
	if err := validateBulkLen(len(items)); err != nil {
		return uuid.Nil, err
	}
	rows := make([]outbox.OutboxRow, 0, len(items)+1)
	for i, it := range items {
		if strings.TrimSpace(it.ProviderID) == "" || strings.TrimSpace(it.KeyID) == "" {
			return uuid.Nil, fmt.Errorf("%w: item %d missing provider_id or key_id", ErrInvalidArgument, i)
		}
		rows = append(rows, outbox.OutboxRow{Op: outbox.OpHDel, RedisKey: KeyPoolKey(it.ProviderID), RedisField: it.KeyID})
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "keypool_entry_delete_bulk", "count": len(items)})
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify})
	return p.commitWithRevision(ctx, "DeleteKeyPoolEntryBulk", DomainCredential, rows)
}

// =============================================================
// Unit 8: Provider
// =============================================================

func (p *Impl) PublishProvider(ctx context.Context, providerID string, info *types.ProviderInfo) (types.BatchID, error) {
	rows, err := buildValidatedPublishProviderRows(providerID, info)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commitWithRevision(ctx, "PublishProvider", DomainTopology, rows)
}

func buildValidatedPublishProviderRows(providerID string, info *types.ProviderInfo) ([]outbox.OutboxRow, error) {
	if strings.TrimSpace(providerID) == "" {
		return nil, fmt.Errorf("%w: provider_id required", ErrInvalidArgument)
	}
	if err := validateProviderInfo(info); err != nil {
		return nil, err
	}
	fillProviderInfoDefaults(info)
	return buildPublishProviderRows(providerID, info), nil
}

func buildPublishProviderRows(providerID string, info *types.ProviderInfo) []outbox.OutboxRow {
	payload, _ := json.Marshal(info)
	// see PublishModelCatalog: revision 由 worker.injectRevision 唯一注入,不在此预填
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "provider_update", "provider_id": providerID})
	return []outbox.OutboxRow{
		{Op: outbox.OpHSet, RedisKey: KeyProviders, RedisField: providerID, Payload: payload},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	}
}

func (p *Impl) DeleteProvider(ctx context.Context, providerID string) (types.BatchID, error) {
	if strings.TrimSpace(providerID) == "" {
		return uuid.Nil, fmt.Errorf("%w: provider_id required", ErrInvalidArgument)
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "provider_delete", "provider_id": providerID})
	return p.commitWithRevision(ctx, "DeleteProvider", DomainTopology, []outbox.OutboxRow{
		{Op: outbox.OpHDel, RedisKey: KeyProviders, RedisField: providerID},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	})
}

// =============================================================
// Unit 9: PlatformConfig
// =============================================================

func (p *Impl) PublishPlatformConfig(ctx context.Context, cfg *types.PlatformConfig) (types.BatchID, error) {
	if cfg == nil {
		return uuid.Nil, fmt.Errorf("%w: cfg required", ErrInvalidArgument)
	}
	if err := validateRetryBudget(cfg.RetryBudget); err != nil {
		return uuid.Nil, err
	}
	if err := validateIngressProductAssertions(cfg.IngressProductAssertions); err != nil {
		return uuid.Nil, err
	}
	fillPlatformConfigDefaults(cfg)
	payload, _ := json.Marshal(cfg)
	// see PublishModelCatalog: revision 由 worker.injectRevision 唯一注入,不在此预填
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "platform_update"})
	return p.commitWithRevision(ctx, "PublishPlatformConfig", DomainPlatform, []outbox.OutboxRow{
		{Op: outbox.OpSet, RedisKey: KeyPlatformConfig, Payload: payload},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	})
}

func (p *Impl) PublishProductRuntimePolicy(ctx context.Context, productID string, policy *types.ProductRuntimePolicy) (types.BatchID, error) {
	rows, err := buildPublishProductRuntimePolicyRows(productID, policy)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commitWithRevision(ctx, "PublishProductRuntimePolicy", DomainPlatform, rows)
}

func buildPublishProductRuntimePolicyRows(productID string, policy *types.ProductRuntimePolicy) ([]outbox.OutboxRow, error) {
	productID = strings.TrimSpace(productID)
	if productID == "" || policy == nil {
		return nil, fmt.Errorf("%w: product_id + policy required", ErrInvalidArgument)
	}
	if policy.ProductID == "" {
		policy.ProductID = productID
	}
	if policy.ProductID != productID {
		return nil, fmt.Errorf("%w: policy.product_id must match path product_id", ErrInvalidArgument)
	}
	fillProductRuntimePolicyDefaults(policy)
	if err := validateProductRuntimePolicy(policy); err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(policy)
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "product_policy_update", "product_id": productID})
	return []outbox.OutboxRow{
		{Op: outbox.OpProductPolicySetCAS, RedisKey: KeyProductPolicies, RedisField: productID, Payload: payload},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	}, nil
}

func (p *Impl) DeleteProductRuntimePolicy(ctx context.Context, productID string) (types.BatchID, error) {
	rows, err := buildDeleteProductRuntimePolicyRows(productID, 0)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commitWithRevision(ctx, "DeleteProductRuntimePolicy", DomainPlatform, rows)
}

func buildDeleteProductRuntimePolicyRows(productID string, revision int64) ([]outbox.OutboxRow, error) {
	productID = strings.TrimSpace(productID)
	if productID == "" {
		return nil, fmt.Errorf("%w: product_id required", ErrInvalidArgument)
	}
	if revision == 0 {
		revision = time.Now().UnixMicro()
	}
	if revision <= 0 {
		return nil, fmt.Errorf("%w: product policy delete revision must be > 0", ErrInvalidArgument)
	}
	deleted := types.ProductPolicyDelete{ProductID: productID, Revision: revision}
	payload, _ := json.Marshal(deleted)
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "product_policy_delete", "product_id": productID})
	return []outbox.OutboxRow{
		{Op: outbox.OpProductPolicyDelCAS, RedisKey: KeyProductPolicies, RedisField: productID, Payload: payload},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	}, nil
}

// =============================================================
// Unit 10: ProviderOverride
// =============================================================

func (p *Impl) PublishProviderOverride(ctx context.Context, scope string, override *types.ProviderOverride) (types.BatchID, error) {
	if err := validateOverrideScope(scope); err != nil {
		return uuid.Nil, err
	}
	if override == nil {
		return uuid.Nil, fmt.Errorf("%w: override required", ErrInvalidArgument)
	}
	if err := validateUnixSeconds("override.effective_from", override.EffectiveFrom); err != nil {
		return uuid.Nil, err
	}
	if override.EffectiveUntil != nil {
		if err := validateUnixSeconds("override.effective_until", *override.EffectiveUntil); err != nil {
			return uuid.Nil, err
		}
	}
	// CreatedAt is publisher-authoritative — it records *when publisher
	// received this override*, not when the caller drafted it. Overwrite
	// any caller-supplied value so the wire shape stays consistent
	// regardless of caller clock skew or auto-fill behaviour.
	override.CreatedAt = time.Now().Unix()
	if override.Revision == 0 {
		override.Revision = time.Now().UnixMicro() // 2026-05-16 auto-fill (§7.10)
	}
	payload, _ := json.Marshal(override)
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "override_update", "scope": scope, "revision": override.Revision})
	return p.commit(ctx, "PublishProviderOverride", []outbox.OutboxRow{
		{Op: outbox.OpHSet, RedisKey: KeyProviderOverrides, RedisField: scope, Payload: payload},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	})
}

func (p *Impl) PublishProviderOverrideBulk(ctx context.Context, items []types.ProviderOverrideItem) (types.BatchID, error) {
	if err := validateBulkLen(len(items)); err != nil {
		return uuid.Nil, err
	}
	now := time.Now().Unix()
	nowMicro := time.Now().UnixMicro()
	rows := make([]outbox.OutboxRow, 0, len(items)+1)
	for i, it := range items {
		if err := validateOverrideScope(it.Scope); err != nil {
			return uuid.Nil, fmt.Errorf("item %d: %w", i, err)
		}
		if it.Override == nil {
			return uuid.Nil, fmt.Errorf("%w: item %d override required", ErrInvalidArgument, i)
		}
		it.Override.CreatedAt = now // server-fill; see PublishProviderOverride
		if it.Override.Revision == 0 {
			it.Override.Revision = nowMicro // 2026-05-16 auto-fill
		}
		body, _ := json.Marshal(it.Override)
		rows = append(rows, outbox.OutboxRow{Op: outbox.OpHSet, RedisKey: KeyProviderOverrides, RedisField: it.Scope, Payload: body})
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "override_update_bulk", "count": len(items)})
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify})
	return p.commit(ctx, "PublishProviderOverrideBulk", rows)
}

func (p *Impl) DeleteProviderOverride(ctx context.Context, scope string) (types.BatchID, error) {
	if err := validateOverrideScope(scope); err != nil {
		return uuid.Nil, err
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "override_delete", "scope": scope})
	return p.commit(ctx, "DeleteProviderOverride", []outbox.OutboxRow{
		{Op: outbox.OpHDel, RedisKey: KeyProviderOverrides, RedisField: scope},
		{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify},
	})
}

func (p *Impl) DeleteProviderOverrideBulk(ctx context.Context, scopes []string) (types.BatchID, error) {
	if err := validateBulkLen(len(scopes)); err != nil {
		return uuid.Nil, err
	}
	rows := make([]outbox.OutboxRow, 0, len(scopes)+1)
	for i, s := range scopes {
		if err := validateOverrideScope(s); err != nil {
			return uuid.Nil, fmt.Errorf("item %d: %w", i, err)
		}
		rows = append(rows, outbox.OutboxRow{Op: outbox.OpHDel, RedisKey: KeyProviderOverrides, RedisField: s})
	}
	notify, _ := json.Marshal(map[string]any{"version": 2, "type": "override_delete_bulk", "count": len(scopes)})
	rows = append(rows, outbox.OutboxRow{Op: outbox.OpPublish, NotifyChannel: ChannelConfigUpdate, NotifyMessage: notify})
	return p.commit(ctx, "DeleteProviderOverrideBulk", rows)
}

// =============================================================
// Replay
// =============================================================

func (p *Impl) Replay(ctx context.Context, req ReplayRequest) (ReplayResult, error) {
	// Two modes per publisher-api-reference.md §7.1:
	//   - precise: req.BatchID set, time window empty
	//   - window: req.Since/req.Until set, batch_id empty
	if req.BatchID != nil && (req.Since != nil || req.Until != nil) {
		return ReplayResult{}, fmt.Errorf("%w: batch_id and time-window are mutually exclusive", ErrInvalidArgument)
	}
	if req.BatchID == nil && req.Since == nil && req.Until == nil {
		// Without a filter we'd scan the entire outbox — outbox.QueryReplay
		// rejects this with a persistence-flavoured 500 today. Surface as
		// 400 so the caller gets actionable feedback (the doc says "二选一",
		// and "neither" needs to fall in the same validation surface).
		return ReplayResult{}, fmt.Errorf("%w: replay requires either batch_id or a time window (since/until)", ErrInvalidArgument)
	}
	if req.Limit <= 0 {
		req.Limit = 100
	}
	if req.Limit > 10000 {
		return ReplayResult{}, fmt.Errorf("%w: limit > 10000", ErrInvalidArgument)
	}
	f := buildReplayFilter(req)
	rows, err := p.store.QueryReplay(ctx, f)
	if err != nil {
		return ReplayResult{}, err
	}
	result := ReplayResult{
		Attempted: len(rows),
		BatchIDs:  collectBatchIDs(rows),
	}
	if req.DryRun {
		// In dry-run we only count what would be retried; rows untouched.
		return result, nil
	}
	for _, r := range rows {
		if err := p.store.MarkPendingNow(ctx, r.ID); err != nil {
			result.Failed++
			continue
		}
		result.Succeeded++
	}
	return result, nil
}

// buildReplayFilter normalizes the spec-level ReplayRequest into the
// storage-level outbox.ReplayFilter, including the default status set
// for window-scan mode.
func buildReplayFilter(req ReplayRequest) outbox.ReplayFilter {
	f := outbox.ReplayFilter{BatchID: req.BatchID, Limit: req.Limit}
	if req.Since != nil {
		t := time.Unix(*req.Since, 0).UTC()
		f.Since = &t
	}
	if req.Until != nil {
		t := time.Unix(*req.Until, 0).UTC()
		f.Until = &t
	}
	// Translate the spec-level status filter into one or two outbox.Status
	// values. Default ("undelivered") = pending+failed.
	if req.BatchID == nil {
		switch req.Status {
		case ReplayStatusPending:
			f.StatusIn = []outbox.Status{outbox.StatusPending}
		case ReplayStatusFailed:
			f.StatusIn = []outbox.Status{outbox.StatusFailed}
		case ReplayStatusAll:
			f.StatusIn = nil // no filter
		default: // empty or "undelivered"
			f.StatusIn = []outbox.Status{outbox.StatusPending, outbox.StatusFailed}
		}
	}
	return f
}

func collectBatchIDs(rows []outbox.OutboxRow) []types.BatchID {
	return outbox.CollectBatchIDs(rows)
}

// =============================================================
// Internal helpers
// =============================================================

// commitWithRevision is the same as commit but injects an OpIncr row on
// the given domain's revision counter immediately before the trailing
// OpPublish row. The worker captures the INCR result and stamps it as
// the publish message's "revision" field — that's what gateway uses for
// monotonic-revision filtering of cache reload events.
//
// Callers must ensure rows already end in an OpPublish row; if not, the
// INCR still runs but no message will carry the new revision. All current
// callers do.
func (p *Impl) commitWithRevision(ctx context.Context, method string, domain Domain, rows []outbox.OutboxRow) (types.BatchID, error) {
	withIncr, err := rowsWithRevision(domain, rows)
	if err != nil {
		return uuid.Nil, err
	}
	return p.commit(ctx, method, withIncr)
}

func rowsWithRevision(domain Domain, rows []outbox.OutboxRow) ([]outbox.OutboxRow, error) {
	revKey := RevisionKeyFor(domain)
	if revKey == "" {
		return nil, fmt.Errorf("commit: unknown domain %q", domain)
	}
	// Find the trailing OpPublish; insert OpIncr immediately before it so
	// the worker's per-batch state has the INCR result ready when the
	// publish row runs.
	publishIdx := -1
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Op == outbox.OpPublish {
			publishIdx = i
			break
		}
	}
	if publishIdx < 0 {
		// No publish row — revision has nowhere to go. Just commit as-is.
		return rows, nil
	}
	incrRow := outbox.OutboxRow{Op: outbox.OpIncr, RedisKey: revKey}
	// Insert incrRow at publishIdx (shifting publish one step right).
	withIncr := make([]outbox.OutboxRow, 0, len(rows)+1)
	withIncr = append(withIncr, rows[:publishIdx]...)
	withIncr = append(withIncr, incrRow)
	withIncr = append(withIncr, rows[publishIdx:]...)
	return withIncr, nil
}

func (p *Impl) commit(ctx context.Context, method string, rows []outbox.OutboxRow) (types.BatchID, error) {
	caller := callerInfoFromCtx(ctx)

	// Idempotency-Key short-circuit (publisher-api-reference.md §2.5):
	// when the caller supplies an Idempotency-Key and we've seen it
	// before in the past retention window, return the original BatchID
	// instead of writing a fresh batch. Avoids double-writes when
	// callers retry transient HTTP failures.
	if caller.IdempotencyID != "" {
		if existing, hit, err := p.store.FindByDedupKey(ctx, caller.IdempotencyID); err == nil && hit {
			slog.WarnContext(ctx, "publisher commit dedup hit",
				"event", "publisher_commit_dedup_hit",
				"method", method,
				"dedup_key", caller.IdempotencyID,
				"existing_batch_id", existing,
				"caller_request_id", caller.RequestID,
				"caller_trace_id", caller.TraceID,
				"actor_id", caller.ActorID,
			)
			return existing, nil
		} else if err != nil {
			slog.WarnContext(ctx, "publisher commit dedup lookup failed",
				"event", "publisher_commit_dedup_lookup_failed",
				"method", method,
				"dedup_key", caller.IdempotencyID,
				"caller_request_id", caller.RequestID,
				"caller_trace_id", caller.TraceID,
				"error", err,
			)
		}
		// Fall through on lookup error — InsertBatch's unique index will
		// catch the race; we'd rather risk an extra INSERT-then-conflict
		// roundtrip than fail the caller on PG hiccup.
	}

	for i := range rows {
		rows[i].MethodName = method
		rows[i].CallerService = caller.Service
		rows[i].CallerTraceID = caller.TraceID
		// Server-minted per-request UUID v7. Same value on every row of
		// this batch so audit queries can pull all rows produced by one
		// HTTP request via WHERE caller_request_id = $X. 2026-05-21.
		rows[i].CallerRequestID = caller.RequestID
		// Per-row attribution at user-account granularity. Same value on
		// every row of this batch (one HTTP request = one actor).
		// 2026-05-22.
		rows[i].ActorID = caller.ActorID
		// Only stamp dedup_key on row 0 — the unique index is per-row,
		// and the seq>0 rows of the same batch would otherwise all
		// share the key and collide with each other on insert.
		if i == 0 {
			rows[i].DedupKey = caller.IdempotencyID
		}
	}
	batchID := uuid.New()
	slog.InfoContext(ctx, "publisher commit insert batch",
		"event", "publisher_commit_insert_batch",
		"method", method,
		"batch_id", batchID,
		"dedup_key", caller.IdempotencyID,
		"row_count", len(rows),
		"rows", outboxRowLogSummaries(rows),
		"caller_request_id", caller.RequestID,
		"caller_trace_id", caller.TraceID,
		"actor_id", caller.ActorID,
	)
	if err := p.store.InsertBatch(ctx, batchID, rows); err != nil {
		// On unique-index conflict the row 0 insert no-ops (ON CONFLICT
		// DO NOTHING in the SQL). Re-resolve via FindByDedupKey so we
		// return the original BatchID instead of leaving the caller with
		// a UUID that points to nothing.
		if caller.IdempotencyID != "" && errors.Is(err, outbox.ErrDedupConflict) {
			if existing, hit, err2 := p.store.FindByDedupKey(ctx, caller.IdempotencyID); err2 == nil && hit {
				slog.WarnContext(ctx, "publisher commit insert dedup conflict",
					"event", "publisher_commit_insert_dedup_conflict",
					"method", method,
					"dedup_key", caller.IdempotencyID,
					"attempted_batch_id", batchID,
					"existing_batch_id", existing,
					"caller_request_id", caller.RequestID,
					"caller_trace_id", caller.TraceID,
				)
				return existing, nil
			} else if err2 != nil {
				slog.WarnContext(ctx, "publisher commit conflict lookup failed",
					"event", "publisher_commit_conflict_lookup_failed",
					"method", method,
					"dedup_key", caller.IdempotencyID,
					"attempted_batch_id", batchID,
					"caller_request_id", caller.RequestID,
					"caller_trace_id", caller.TraceID,
					"error", err2,
				)
			}
		}
		slog.ErrorContext(ctx, "publisher commit insert batch failed",
			"event", "publisher_commit_insert_batch_failed",
			"method", method,
			"batch_id", batchID,
			"dedup_key", caller.IdempotencyID,
			"row_count", len(rows),
			"rows", outboxRowLogSummaries(rows),
			"caller_request_id", caller.RequestID,
			"caller_trace_id", caller.TraceID,
			"error", err,
		)
		return uuid.Nil, err
	}
	return batchID, nil
}

func routeConfigProviderIDs(entries []types.RouteEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ProviderID)
	}
	return ids
}

func outboxRowLogSummaries(rows []outbox.OutboxRow) []map[string]any {
	summaries := make([]map[string]any, 0, len(rows))
	for seq, row := range rows {
		summaries = append(summaries, map[string]any{
			"seq":            seq,
			"op":             row.Op,
			"redis_key":      row.RedisKey,
			"redis_field":    row.RedisField,
			"notify_channel": row.NotifyChannel,
		})
	}
	return summaries
}

func validateBulkLen(n int) error {
	if n == 0 {
		return fmt.Errorf("%w: empty bulk", ErrInvalidArgument)
	}
	if n > types.MaxBulkItems {
		return fmt.Errorf("%w: bulk size %d exceeds max %d", ErrInvalidArgument, n, types.MaxBulkItems)
	}
	return nil
}

func validateKeyHash(h types.KeyHash) error {
	if err := h.Valid(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	return nil
}

func validateIP(ip string) error {
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("%w: invalid IP %q", ErrInvalidArgument, ip)
	}
	return nil
}

func validateOverrideScope(scope string) error {
	parts := strings.Split(scope, "|")
	if len(parts) != 3 || strings.TrimSpace(parts[0]) == "" {
		return fmt.Errorf("%w: scope must be `provider_code|base_url|upstream_model`", ErrInvalidArgument)
	}
	return nil
}

func validateBalanceReason(hasBalance bool, reason string) error {
	if hasBalance {
		if reason != "" {
			return fmt.Errorf("%w: reason must be empty when has_balance=true", ErrInvalidArgument)
		}
		return nil
	}
	if _, ok := keyBalanceReasons[reason]; !ok {
		return fmt.Errorf("%w: unknown reason %q", ErrInvalidArgument, reason)
	}
	return nil
}

// Current*SchemaVersion are the contract version constants publisher
// stamps on each domain payload when caller leaves SchemaVersion zero.
// All values MUST stay in sync with the matching const in gateway-side
// `internal/configsync/schema_versions.go`; bumping any of these is a
// coordinated cross-repo rollout per gateway-first发布顺序 (gateway
// reader 必须先支持新版本,publisher 才能开始写)。
const (
	CurrentKeyMetaSchemaVersion              = 2
	CurrentModelCatalogEntrySchemaVersion    = 1
	CurrentRouteConfigSchemaVersion          = 1
	CurrentProviderInfoSchemaVersion         = 1
	CurrentKeyPoolMetaSchemaVersion          = 1
	CurrentKeyPoolEntrySchemaVersion         = 1
	CurrentPlatformConfigSchemaVersion       = 1
	CurrentProductRuntimePolicySchemaVersion = 1
)

// fillKeyMetaDefaults applies publisher-side defaults BEFORE validation
// so callers don't have to synthesize `revision` or `schema_version`
// themselves. Caller-supplied non-zero values are honoured — that path
// is reserved for DR replay scenarios where preserving the original
// `revision` matters (e.g. restoring a write order from a backup).
//
// Rationale:
//   - schema_version: publisher↔gateway 契约版本号,与 caller 业务无关。
//     Caller 没业务理由知道当前版本号,改成 publisher 单边管理。
//   - revision: caller 用 time.Now().UnixMicro(),publisher 用同函数,
//     语义等价;publisher 单实例,server clock 更权威。
//
// Spec: docs/publisher-api-reference.md §4.1.x
func fillKeyMetaDefaults(m *types.KeyMeta) {
	if m == nil {
		return
	}
	if m.Revision == 0 {
		m.Revision = time.Now().UnixMicro()
	}
	if m.SchemaVersion == 0 {
		m.SchemaVersion = CurrentKeyMetaSchemaVersion
	}
}

// fillModelCatalogEntryDefaults stamps SchemaVersion + Revision when
// caller leaves them zero. Same pattern as fillKeyMetaDefaults.
func fillModelCatalogEntryDefaults(e *types.ModelCatalogEntry) {
	if e == nil {
		return
	}
	if e.Revision == 0 {
		e.Revision = time.Now().UnixMicro()
	}
	if e.SchemaVersion == 0 {
		e.SchemaVersion = CurrentModelCatalogEntrySchemaVersion
	}
}

// fillRouteConfigDefaults stamps SchemaVersion + Revision when caller
// leaves them zero.
func fillRouteConfigDefaults(r *types.RouteConfig) {
	if r == nil {
		return
	}
	if r.Revision == 0 {
		r.Revision = time.Now().UnixMicro()
	}
	if r.SchemaVersion == 0 {
		r.SchemaVersion = CurrentRouteConfigSchemaVersion
	}
}

// fillProviderInfoDefaults stamps SchemaVersion + Revision when caller
// leaves them zero.
func fillProviderInfoDefaults(p *types.ProviderInfo) {
	if p == nil {
		return
	}
	if p.Revision == 0 {
		p.Revision = time.Now().UnixMicro()
	}
	if p.SchemaVersion == 0 {
		p.SchemaVersion = CurrentProviderInfoSchemaVersion
	}
}

// fillKeyPoolMetaDefaults stamps SchemaVersion + Revision when caller
// leaves them zero.
func fillKeyPoolMetaDefaults(m *types.KeyPoolMeta) {
	if m == nil {
		return
	}
	if m.Revision == 0 {
		m.Revision = time.Now().UnixMicro()
	}
	if m.SchemaVersion == 0 {
		m.SchemaVersion = CurrentKeyPoolMetaSchemaVersion
	}
}

// fillKeyPoolEntryDefaults stamps SchemaVersion + Revision when caller
// leaves them zero. Revision on entries is new per Wave A (2026-05-22).
func fillKeyPoolEntryDefaults(e *types.KeyPoolEntry) {
	if e == nil {
		return
	}
	if e.Revision == 0 {
		e.Revision = time.Now().UnixMicro()
	}
	if e.SchemaVersion == 0 {
		e.SchemaVersion = CurrentKeyPoolEntrySchemaVersion
	}
}

// fillPlatformConfigDefaults stamps SchemaVersion + Revision when caller
// leaves them zero.
func fillPlatformConfigDefaults(c *types.PlatformConfig) {
	if c == nil {
		return
	}
	if c.Revision == 0 {
		c.Revision = time.Now().UnixMicro()
	}
	if c.SchemaVersion == 0 {
		c.SchemaVersion = CurrentPlatformConfigSchemaVersion
	}
}

// fillProductRuntimePolicyDefaults stamps SchemaVersion + Revision when caller
// leaves them zero. Billing normally supplies a business revision; publisher
// only fills the field for direct admin writes and tests.
func fillProductRuntimePolicyDefaults(p *types.ProductRuntimePolicy) {
	if p == nil {
		return
	}
	if p.Revision == 0 {
		p.Revision = time.Now().UnixMicro()
	}
	if p.SchemaVersion == 0 {
		p.SchemaVersion = CurrentProductRuntimePolicySchemaVersion
	}
}

func validateKeyMeta(m *types.KeyMeta) error {
	if m == nil {
		return fmt.Errorf("%w: keymeta nil", ErrInvalidArgument)
	}
	if err := types.KeyHash(m.KeyHash).Valid(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	if err := types.KeyID(m.KeyID).Valid(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	if m.Revision <= 0 {
		// fillKeyMetaDefaults must run before validateKeyMeta — if we hit
		// this branch it's a wiring bug, not a caller error.
		return fmt.Errorf("%w: keymeta revision must be > 0 (unix microseconds)", ErrInvalidArgument)
	}
	if m.Status == "" {
		return fmt.Errorf("%w: keymeta status required", ErrInvalidArgument)
	}
	if err := validateEnum("keymeta.status", m.Status, keyMetaStatuses); err != nil {
		return err
	}
	if err := validateUnixSeconds("keymeta.expires_at", m.ExpiresAt); err != nil {
		return err
	}
	if len(m.ScopeAttrs) > MaxScopeAttrs {
		return fmt.Errorf("%w: scope_attrs accepts at most %d key/value pairs, got %d", ErrInvalidArgument, MaxScopeAttrs, len(m.ScopeAttrs))
	}
	if m.EnforceModelWhitelist && len(m.AllowedModels) == 0 {
		return fmt.Errorf("%w: enforce_model_whitelist=true requires allowed_models non-empty; for full deny use status=suspended/revoked", ErrInvalidArgument)
	}
	if m.DedicatedUpstream != nil {
		if err := validateDedicatedUpstream(m.DedicatedUpstream); err != nil {
			return err
		}
	}
	return nil
}

func validateDedicatedUpstream(cred *types.DedicatedUpstreamCredential) error {
	if cred == nil {
		return nil
	}
	if strings.TrimSpace(cred.UpstreamKeyID) == "" {
		return fmt.Errorf("%w: keymeta.dedicated_upstream.upstream_key_id required", ErrInvalidArgument)
	}
	if cred.ProviderAccountRef == "" {
		return fmt.Errorf("%w: keymeta.dedicated_upstream.provider_account_ref required for new writes", ErrInvalidArgument)
	}
	if err := types.ValidateProviderAccountRef(cred.ProviderAccountRef); err != nil {
		return fmt.Errorf("%w: keymeta.dedicated_upstream.provider_account_ref invalid: %v", ErrInvalidArgument, err)
	}
	if cred.ProviderAccountRef == cred.UpstreamKeyID {
		return fmt.Errorf("%w: keymeta.dedicated_upstream.provider_account_ref must not reuse upstream_key_id", ErrInvalidArgument)
	}
	if cred.ProviderAccountRef == cred.Ciphertext {
		return fmt.Errorf("%w: keymeta.dedicated_upstream.provider_account_ref must not reuse credential material or reference", ErrInvalidArgument)
	}
	if strings.TrimSpace(cred.Ciphertext) == "" {
		return fmt.Errorf("%w: keymeta.dedicated_upstream.ciphertext required", ErrInvalidArgument)
	}
	if !strings.HasPrefix(cred.Ciphertext, "kms:") {
		return fmt.Errorf("%w: keymeta.dedicated_upstream.ciphertext must use kms: prefix", ErrInvalidArgument)
	}
	if cred.CiphertextVersion <= 0 {
		return fmt.Errorf("%w: keymeta.dedicated_upstream.ciphertext_version must be > 0", ErrInvalidArgument)
	}
	if strings.TrimSpace(cred.CiphertextSHA256) == "" {
		return fmt.Errorf("%w: keymeta.dedicated_upstream.ciphertext_sha256 required", ErrInvalidArgument)
	}
	if strings.TrimSpace(cred.KMSKeyID) == "" {
		return fmt.Errorf("%w: keymeta.dedicated_upstream.kms_key_id required", ErrInvalidArgument)
	}
	if strings.TrimSpace(cred.Status) == "" {
		return fmt.Errorf("%w: keymeta.dedicated_upstream.status required", ErrInvalidArgument)
	}
	if err := validateUnixSeconds("keymeta.dedicated_upstream.expires_at", cred.ExpiresAt); err != nil {
		return err
	}
	if strings.TrimSpace(cred.UpdatedAt) == "" {
		return fmt.Errorf("%w: keymeta.dedicated_upstream.updated_at required", ErrInvalidArgument)
	}
	if _, err := time.Parse(time.RFC3339, cred.UpdatedAt); err != nil {
		return fmt.Errorf("%w: keymeta.dedicated_upstream.updated_at must be RFC3339: %v", ErrInvalidArgument, err)
	}
	if len(cred.Endpoints) == 0 {
		return fmt.Errorf("%w: keymeta.dedicated_upstream.endpoints required", ErrInvalidArgument)
	}
	seenProtocols := map[string]struct{}{}
	for i, endpoint := range cred.Endpoints {
		label := fmt.Sprintf("keymeta.dedicated_upstream.endpoints[%d]", i)
		if err := validateEnum(label+".client_protocol", endpoint.ClientProtocol, dedicatedClientProtocols); err != nil {
			return err
		}
		if _, ok := seenProtocols[endpoint.ClientProtocol]; ok {
			return fmt.Errorf("%w: %s.client_protocol duplicate: %s", ErrInvalidArgument, label, endpoint.ClientProtocol)
		}
		seenProtocols[endpoint.ClientProtocol] = struct{}{}
		if strings.TrimSpace(endpoint.ProviderID) == "" {
			return fmt.Errorf("%w: %s.provider_id required", ErrInvalidArgument, label)
		}
		if endpoint.ProtocolStrategy != "" {
			if err := validateEnum(label+".protocol_strategy", endpoint.ProtocolStrategy, dedicatedEndpointProtocolStrategies); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateProductRuntimePolicy(policy *types.ProductRuntimePolicy) error {
	if policy == nil {
		return fmt.Errorf("%w: product runtime policy nil", ErrInvalidArgument)
	}
	if strings.TrimSpace(policy.ProductID) == "" {
		return fmt.Errorf("%w: product_id required", ErrInvalidArgument)
	}
	if policy.Revision <= 0 {
		return fmt.Errorf("%w: product runtime policy revision must be > 0", ErrInvalidArgument)
	}
	if err := validateEnum("product_policy.status", policy.Status, productRuntimePolicyStatuses); err != nil {
		return err
	}
	if err := validateEnum("product_policy.credential_source", policy.CredentialSource, productCredentialSources); err != nil {
		return err
	}
	if err := validateEnum("product_policy.model_scope_mode", policy.ModelScopeMode, productModelScopeModes); err != nil {
		return err
	}
	if err := validateEnum("product_policy.fallback_policy", policy.FallbackPolicy, productFallbackPolicies); err != nil {
		return err
	}
	if err := validateEnum("product_policy.billing_mode", policy.BillingMode, productRuntimeBillingModes); err != nil {
		return err
	}
	if policy.RouteStrategy != "" {
		if err := validateEnum("product_policy.route_strategy", policy.RouteStrategy, productRouteStrategies); err != nil {
			return err
		}
	}
	if policy.RouteGraphEnforcement != "" {
		if err := validateEnum("product_policy.route_graph_enforcement", policy.RouteGraphEnforcement, productRouteGraphEnforcements); err != nil {
			return err
		}
	}
	if policy.RouteGraphRequired && policy.RouteGraph == nil {
		return fmt.Errorf("%w: route_graph_required=true requires route_graph", ErrInvalidArgument)
	}
	if policy.RouteGraphEnforcement != "" && policy.RouteGraph == nil {
		return fmt.Errorf("%w: route_graph_enforcement requires route_graph", ErrInvalidArgument)
	}
	if policy.RouteGraph != nil {
		if err := routegraph.Validate(policy.RouteGraph, routegraph.ValidationOptions{
			Prefix:                        "route_graph",
			ProductID:                     policy.ProductID,
			ProductAllowedProviderIDs:     policy.AllowedProviderIDs,
			ProductAllowedChannelIDs:      policy.AllowedChannelIDs,
			ProductAllowedModels:          policy.AllowedModels,
			CheckTargetPolicyIntersection: true,
		}); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidArgument, err)
		}
	}
	if policy.ModelScopeMode == "product_allowlist" && len(policy.AllowedModels) == 0 {
		return fmt.Errorf("%w: product_allowlist requires allowed_models non-empty", ErrInvalidArgument)
	}
	if policy.CredentialSource == types.ProviderCredentialSourceDedicatedUpstream && len(policy.AllowedProviderIDs) == 0 && len(policy.AllowedChannelIDs) == 0 {
		return fmt.Errorf("%w: dedicated_upstream requires allowed_provider_ids or allowed_channel_ids", ErrInvalidArgument)
	}
	if policy.CredentialSource == types.ProviderCredentialSourceDedicatedUpstream && policy.FallbackPolicy == "platform_default" {
		return fmt.Errorf("%w: dedicated_upstream cannot use platform_default fallback", ErrInvalidArgument)
	}
	return nil
}

// MaxScopeAttrs caps the number of key/value pairs in KeyMeta.ScopeAttrs.
// Each entry adds size to every UsageEvent the key emits; 10 is enough to
// carry standard business identity dimensions plus a handful of caller-
// defined ones without bloating downstream event size.
const MaxScopeAttrs = 10

// ============================================================
// Enum + unit-defense validators (2026-05-16 audit P2)
// ============================================================
//
// These tighten the wire surface so a caller typo doesn't slip past
// publisher and land as bad data in Redis (gateway typically falls
// back silently to a default, so the bad value never reports as an
// error to the original caller — silent failure).
//
// Spec: docs/2026-05-16-api-field-ambiguity-audit.md §5 P2.

var keyMetaStatuses = map[string]struct{}{
	"active":    {},
	"suspended": {},
	"revoked":   {},
}

var providerStatuses = map[string]struct{}{
	"active":   {},
	"disabled": {},
}

var keyPoolEntryStatuses = map[string]struct{}{
	"enabled":  {},
	"disabled": {},
	"cooling":  {},
}

var routeEntryStatuses = map[string]struct{}{
	"active":   {},
	"disabled": {},
}

var productRuntimePolicyStatuses = map[string]struct{}{
	"active":   {},
	"disabled": {},
}

var productCredentialSources = map[string]struct{}{
	types.ProviderCredentialSourceKeypool:           {},
	types.ProviderCredentialSourceDedicatedUpstream: {},
}

var productModelScopeModes = map[string]struct{}{
	"plan_configured":    {},
	"product_allowlist":  {},
	"upstream_inherited": {},
}

var productFallbackPolicies = map[string]struct{}{
	"none":             {},
	"within_product":   {},
	"within_provider":  {},
	"platform_default": {},
}

var productRuntimeBillingModes = map[string]struct{}{
	"payg_balance":         {},
	"subscription_credits": {},
	"token_plan_quota":     {},
	"included_upstream":    {},
}

var productRouteStrategies = map[string]struct{}{
	"priority":   {},
	"weight":     {},
	"latency":    {},
	"least_busy": {},
}

var productRouteGraphEnforcements = map[string]struct{}{
	"shadow":  {},
	"enforce": {},
}

var dedicatedClientProtocols = stringSet(types.KnownDedicatedClientProtocols())

// dedicatedEndpointProtocolStrategies validates a dedicated upstream endpoint's
// protocol_strategy. Per ADR-0014 this is the explicit CHANNEL selector
// (passthrough / responses_to_chat / anthropic_to_chat), NOT the retired §10
// product/route loss policy (auto / passthrough_only, removed from the
// contract in ADR-0015 S5b). The legacy transition values were retired from
// this enum in ADR-0015 S5a and stay rejected.
var dedicatedEndpointProtocolStrategies = stringSet(types.KnownDedicatedProtocolChannels())

func validateEnum(label, value string, allowed map[string]struct{}) error {
	if _, ok := allowed[value]; !ok {
		opts := make([]string, 0, len(allowed))
		for k := range allowed {
			opts = append(opts, k)
		}
		return fmt.Errorf("%w: %s=%q not in allowed set %v", ErrInvalidArgument, label, value, opts)
	}
	return nil
}

// validateUnixSeconds rejects values that look like millisecond timestamps.
// Unix seconds ≤ 2 billion through year 2033; anything > 1e12 (year ~33658
// in seconds) is overwhelmingly a millisecond-timestamp mistake. Zero is
// allowed (most callers use 0 for "no expiry" / "immediate").
func validateUnixSeconds(label string, v int64) error {
	if v > 0 && v > 1_000_000_000_000 {
		return fmt.Errorf("%w: %s=%d looks like a millisecond timestamp — wire is Unix seconds (max ~2 billion for the next decade)", ErrInvalidArgument, label, v)
	}
	return nil
}

// validateRetryBudget enforces the all-or-nothing semantic documented in
// the wire-type spec: all three axes zero ⇒ gateway in-code defaults; any
// non-zero ⇒ all three must be non-zero. Mixed (some zero, some not) is
// a foot-gun — caller probably meant to set one axis and use defaults for
// the others, but the wire contract treats zero-on-a-mixed-config as
// "unlimited for that axis", which is the opposite of "use default".
//
// Spec: docs/2026-05-16-api-field-ambiguity-audit.md §5 P1 #7.
func validateRetryBudget(rb types.RetryBudgetConfig) error {
	nonZero := 0
	if rb.MaxKeysPerProvider != 0 {
		nonZero++
	}
	if rb.MaxProviders != 0 {
		nonZero++
	}
	if rb.TotalWallTimeMs != 0 {
		nonZero++
	}
	if nonZero != 0 && nonZero != 3 {
		return fmt.Errorf("%w: retry_budget is all-or-nothing — set all three axes (max_keys_per_provider, max_providers, total_wall_time_ms) or leave all zero; got %d non-zero of 3", ErrInvalidArgument, nonZero)
	}
	return nil
}

// validateIngressProductAssertions — STRUCTURAL checks only (G7 Phase B,
// design G7-product-context-admission.md §4). The assertion layer is
// fail-open by construction: an unlisted gate is unasserted and passes,
// and the gateway falls back to warn on absent/unknown modes. So the
// publisher deliberately does NO business-level gatekeeping here — no
// pattern whitelist, no product_id reference check against
// gw:product_policies (ADR-0016 remind-don't-guard). It only rejects
// entries that carry no usable information or make the publish ambiguous:
// blank pattern, empty product set, blank product ids, a mode outside
// {"", warn, enforce} (typos must not silently degrade to warn at the
// input boundary), and two entries claiming the same pattern (the wire
// contract is a set keyed by pattern; gateway compiles it into a map).
func validateIngressProductAssertions(assertions []types.IngressProductAssertion) error {
	seen := make(map[string]struct{}, len(assertions))
	for i, a := range assertions {
		if strings.TrimSpace(a.Pattern) == "" {
			return fmt.Errorf("%w: ingress_product_assertions[%d].pattern required", ErrInvalidArgument, i)
		}
		if _, dup := seen[a.Pattern]; dup {
			return fmt.Errorf("%w: ingress_product_assertions[%d] duplicates pattern %q", ErrInvalidArgument, i, a.Pattern)
		}
		seen[a.Pattern] = struct{}{}
		if len(a.AllowedProducts) == 0 {
			return fmt.Errorf("%w: ingress_product_assertions[%d] (%q): allowed_products must be a non-empty array", ErrInvalidArgument, i, a.Pattern)
		}
		for j, product := range a.AllowedProducts {
			if strings.TrimSpace(product) == "" {
				return fmt.Errorf("%w: ingress_product_assertions[%d] (%q): allowed_products[%d] is blank", ErrInvalidArgument, i, a.Pattern, j)
			}
		}
		switch a.Mode {
		case "", types.IngressAssertionModeWarn, types.IngressAssertionModeEnforce:
		default:
			return fmt.Errorf("%w: ingress_product_assertions[%d] (%q): mode %q not in {warn, enforce} (empty = warn)", ErrInvalidArgument, i, a.Pattern, a.Mode)
		}
	}
	return nil
}

func buildSetMemberBulk(hashes []types.KeyHash, label, redisKey string, op outbox.Op, validator func(types.KeyHash) error) ([]outbox.OutboxRow, error) {
	if err := validateBulkLen(len(hashes)); err != nil {
		return nil, err
	}
	rows := make([]outbox.OutboxRow, 0, len(hashes)+1)
	for i, h := range hashes {
		if err := validator(h); err != nil {
			return nil, fmt.Errorf("%s item %d: %w", label, i, err)
		}
		rows = append(rows, outbox.OutboxRow{Op: op, RedisKey: redisKey, RedisMember: string(h)})
	}
	return rows, nil
}

func buildKeyLifecycleRows(hashes []types.KeyHash, enable bool) ([]outbox.OutboxRow, error) {
	if err := validateBulkLen(len(hashes)); err != nil {
		return nil, err
	}
	status := "suspended"
	setOp := outbox.OpSAdd
	eventType := "revoked_keys_added_bulk"
	if enable {
		status = "active"
		setOp = outbox.OpSRem
		eventType = "revoked_keys_removed_bulk"
	}
	if len(hashes) == 1 {
		if enable {
			eventType = "revoked_key_removed"
		} else {
			eventType = "revoked_key_added"
		}
	}

	revision := time.Now().UnixMicro()
	statusPayload, _ := json.Marshal(map[string]any{
		"status":   status,
		"revision": revision,
	})
	rows := make([]outbox.OutboxRow, 0, len(hashes)*2+1)
	for i, h := range hashes {
		if err := validateKeyHash(h); err != nil {
			return nil, fmt.Errorf("KeyLifecycle item %d: %w", i, err)
		}
		hash := string(h)
		rows = append(rows,
			outbox.OutboxRow{
				Op:         outbox.OpKeyMetaStatus,
				RedisKey:   KeyKeymeta,
				RedisField: hash,
				Payload:    statusPayload,
			},
			outbox.OutboxRow{
				Op:          setOp,
				RedisKey:    KeyRevokedKeys,
				RedisMember: hash,
			},
		)
	}

	notifyBody := map[string]any{"version": 2, "type": eventType, "count": len(hashes)}
	if len(hashes) == 1 {
		notifyBody["key_hash"] = string(hashes[0])
		delete(notifyBody, "count")
	}
	notify, _ := json.Marshal(notifyBody)
	rows = append(rows, outbox.OutboxRow{
		Op:            outbox.OpPublish,
		NotifyChannel: ChannelSecurityUpdate,
		NotifyMessage: notify,
	})
	return rows, nil
}

func buildStringSetMemberBulk(items []string, label, redisKey string, op outbox.Op, validator func(string) error) ([]outbox.OutboxRow, error) {
	if err := validateBulkLen(len(items)); err != nil {
		return nil, err
	}
	rows := make([]outbox.OutboxRow, 0, len(items)+1)
	for i, x := range items {
		if err := validator(x); err != nil {
			return nil, fmt.Errorf("%s item %d: %w", label, i, err)
		}
		rows = append(rows, outbox.OutboxRow{Op: op, RedisKey: redisKey, RedisMember: x})
	}
	return rows, nil
}

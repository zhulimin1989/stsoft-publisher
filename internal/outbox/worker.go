package outbox

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/tokenplatform/publisher/internal/metrics"
	"github.com/tokenplatform/publisher/internal/types"
)

// Worker is the retry loop that drains publisher_outbox into Redis.
//
// Lifecycle: a single Worker goroutine ticks every cfg.ScanInterval,
// claiming up to cfg.BatchPerTick rows under FOR UPDATE SKIP LOCKED,
// grouping them by batch_id, and executing each batch's ops in seq order.
// On any op error inside a batch, the worker stops that batch (subsequent
// ops are not attempted) and marks the leading failed row with the next
// backoff. The remaining rows wait — they will be re-claimed alongside
// the retried leader on the next tick.
//
// Within one claimed batch fragment, notify rows (op=publish) sort last per
// the seq numbering chosen in the sync path. Claiming remains row-granular,
// so multiple workers can split a batch and are not covered by that guarantee.
type Worker struct {
	store        *Store
	redis        *redis.Client
	cfg          WorkerConfig
	deadLetterFn DeadLetterAlert
	loadBatchFn  func(context.Context, types.BatchID) ([]OutboxRow, error)
	// cleanupFn is the retention seam, wired from store.Cleanup the same way
	// loadBatchFn is wired from store.QueryBatch, so the sweep loop is
	// testable without a live PostgreSQL.
	cleanupFn func(ctx context.Context, retainDelivered, retainDeadLetter time.Duration, limit int) (int64, bool, error)
}

// WorkerConfig knobs.
type WorkerConfig struct {
	ScanInterval       time.Duration // default 1s
	BatchPerTick       int           // default 100
	MaxAttempts        int           // default 12 (≈15 min cap with default backoff)
	BaseDelay          time.Duration // default 1s
	MaxDelay           time.Duration // default 60s
	DeliveredRetention time.Duration // default 7d
	DeadLetterRetain   time.Duration // default 30d
	// JanitorInterval is how often retention runs. Zero disables the janitor
	// entirely, which restores the pre-wiring behaviour of never deleting.
	JanitorInterval time.Duration // default 1h
	// JanitorBatch bounds one DELETE statement.
	JanitorBatch int // default 5000
	// JanitorMaxBatches bounds one sweep, so a misconfigured retention window
	// cannot turn into an unbounded production delete.
	JanitorMaxBatches int // default 20
}

// DeadLetterAlert is invoked when a row crosses MaxAttempts. The handler
// is responsible for routing the alert (Sentry / chat / pager).
type DeadLetterAlert func(batchID types.BatchID, method, lastErr string)

// NewWorker wires a Worker to its dependencies. Pass DeadLetterAlert=nil
// to fall back to slog.Error logging.
func NewWorker(store *Store, redisClient *redis.Client, cfg WorkerConfig, deadLetter DeadLetterAlert) *Worker {
	if cfg.ScanInterval == 0 {
		cfg.ScanInterval = time.Second
	}
	if cfg.BatchPerTick == 0 {
		cfg.BatchPerTick = 100
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 12
	}
	if cfg.BaseDelay == 0 {
		cfg.BaseDelay = time.Second
	}
	if cfg.MaxDelay == 0 {
		cfg.MaxDelay = 60 * time.Second
	}
	if cfg.DeliveredRetention == 0 {
		cfg.DeliveredRetention = 7 * 24 * time.Hour
	}
	if cfg.DeadLetterRetain == 0 {
		cfg.DeadLetterRetain = 30 * 24 * time.Hour
	}
	// A zero JanitorInterval is honoured as "off" rather than defaulted, so the
	// janitor stays explicitly disableable. NewWorker callers that want the
	// default pass it through from config, which supplies 1h.
	if cfg.JanitorBatch == 0 {
		cfg.JanitorBatch = 5000
	}
	if cfg.JanitorMaxBatches == 0 {
		cfg.JanitorMaxBatches = 20
	}
	if deadLetter == nil {
		deadLetter = func(b types.BatchID, m, e string) {
			slog.Error("publisher outbox dead-letter", "batch_id", b, "method", m, "error", e)
		}
	}
	worker := &Worker{store: store, redis: redisClient, cfg: cfg, deadLetterFn: deadLetter}
	if store != nil {
		worker.loadBatchFn = store.QueryBatch
		worker.cleanupFn = store.Cleanup
	}
	return worker
}

// Run drives the worker loop until ctx is cancelled. Returns when the
// context is done, after the in-flight tick (if any) completes.
func (w *Worker) Run(ctx context.Context) {
	tick := time.NewTicker(w.cfg.ScanInterval)
	defer tick.Stop()

	// Retention shares this loop rather than owning a goroutine: delivery and
	// the janitor both belong to the worker's lifetime, and one select keeps
	// them from overlapping on the same pool during a large first sweep.
	var janitorC <-chan time.Time
	if w.cfg.JanitorInterval > 0 && w.cleanupFn != nil {
		janitor := time.NewTicker(w.cfg.JanitorInterval)
		defer janitor.Stop()
		janitorC = janitor.C
	} else {
		slog.Warn("publisher outbox janitor disabled; delivered and dead_letter rows are retained forever",
			"delivered_retention", w.cfg.DeliveredRetention,
			"dead_letter_retention", w.cfg.DeadLetterRetain)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := w.tick(ctx); err != nil {
				slog.Warn("publisher outbox tick", "error", err)
			}
		case <-janitorC:
			if err := w.janitorTick(ctx); err != nil {
				slog.Warn("publisher outbox janitor", "error", err)
			}
		}
	}
}

// janitorTick applies the configured retention in bounded batches, stopping on
// the first short batch (backlog drained), when another replica holds the
// sweep, or at the per-sweep cap. Hitting the cap is logged: a permanently
// capped sweep must not look the same as a finished one.
func (w *Worker) janitorTick(ctx context.Context) error {
	var total int64
	for batch := 0; batch < w.cfg.JanitorMaxBatches; batch++ {
		deleted, locked, err := w.cleanupFn(
			ctx,
			w.cfg.DeliveredRetention,
			w.cfg.DeadLetterRetain,
			w.cfg.JanitorBatch,
		)
		if err != nil {
			metrics.JanitorSweep("failed")
			if total > 0 {
				slog.Info("publisher outbox janitor partial", "deleted", total)
			}
			return err
		}
		if !locked {
			metrics.JanitorSweep("contended")
			return nil
		}
		total += deleted
		metrics.JanitorDeleted(deleted)
		if int(deleted) < w.cfg.JanitorBatch {
			metrics.JanitorSweep("drained")
			if total > 0 {
				slog.Info("publisher outbox janitor", "deleted", total)
			}
			return nil
		}
	}
	metrics.JanitorSweep("capped")
	slog.Warn("publisher outbox janitor hit the per-sweep cap; rows remain eligible",
		"deleted", total,
		"batches", w.cfg.JanitorMaxBatches,
		"batch_size", w.cfg.JanitorBatch)
	return nil
}

// tick claims one chunk of due rows, groups by batch, and delivers oldest
// persisted batch fragment first. ClaimDue's UPDATE ... RETURNING order is not
// defined, and ranging a map would otherwise let a newer batch prefix overtake
// an older batch tail inside one worker tick.
func (w *Worker) tick(ctx context.Context) error {
	rows, err := w.store.ClaimDue(ctx, w.cfg.BatchPerTick)
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}
	for _, batch := range orderedClaimedBatches(rows) {
		w.deliverBatch(ctx, batch.batchID, batch.rows)
	}
	return nil
}

type claimedBatch struct {
	batchID  types.BatchID
	rows     []OutboxRow
	minRowID int64
}

// orderedClaimedBatches returns detached, deterministically ordered groups.
// The smallest persisted row ID is the durable batch-age signal available in
// one claim chunk; BatchID only breaks an impossible-for-PG equal-ID tie. This
// fixes single-worker, same-tick overtaking. It does not make claims atomic
// across multiple workers or replicas.
func orderedClaimedBatches(rows []OutboxRow) []claimedBatch {
	groupCapacity := len(rows)/4 + 1
	indices := make(map[types.BatchID]int, groupCapacity)
	groups := make([]claimedBatch, 0, groupCapacity)
	for _, row := range rows {
		index, ok := indices[row.BatchID]
		if !ok {
			index = len(groups)
			indices[row.BatchID] = index
			groups = append(groups, claimedBatch{batchID: row.BatchID, minRowID: row.ID})
		}
		groups[index].rows = append(groups[index].rows, row)
		if row.ID < groups[index].minRowID {
			groups[index].minRowID = row.ID
		}
	}

	for i := range groups {
		groups[i].rows = orderedBatchRows(groups[i].rows)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].minRowID != groups[j].minRowID {
			return groups[i].minRowID < groups[j].minRowID
		}
		return groups[i].batchID.String() < groups[j].batchID.String()
	})
	return groups
}

// batchState carries per-batch context across ops — specifically the most
// recent OpIncr result so the next OpPublish can stamp it as the message's
// revision field (driving gateway-side monotonic-revision filtering).
type batchState struct {
	lastIncr        int64
	deliveryOutcome string
}

var errInvalidCredentialEvidenceOutboxPayload = errors.New("invalid credential-evidence outbox payload")

// deliverBatch executes the ops of one batch in seq order, stopping at
// the first error. Per outbox.go semantics, a partial-success batch is
// re-delivered in full on the next retry (Redis ops being idempotent
// makes this safe).
func (w *Worker) deliverBatch(ctx context.Context, batchID types.BatchID, batchRows []OutboxRow) {
	orderedRows := orderedBatchRows(batchRows)
	if err := w.preflightBatch(ctx, batchID, orderedRows); err != nil {
		if errors.Is(err, errInvalidCredentialEvidenceOutboxPayload) {
			w.deadLetterInvalidBatch(ctx, batchID, orderedRows, err)
		} else if len(orderedRows) > 0 {
			w.markRowFailed(ctx, batchID, orderedRows[0], err)
		}
		return
	}

	state := &batchState{}
	for _, r := range orderedRows {
		if err := w.runOne(ctx, r, state); err != nil {
			w.markRowFailed(ctx, batchID, r, err)
			return
		}
		metrics.WorkerDelivered()
		if shouldLogDeliveredRow(r) {
			slog.InfoContext(ctx, "publisher outbox row delivered",
				"event", "publisher_outbox_row_delivered",
				"batch_id", batchID,
				"row_id", r.ID,
				"seq", r.Seq,
				"method", r.MethodName,
				"op", r.Op,
				"redis_key", r.RedisKey,
				"redis_field", r.RedisField,
				"notify_channel", r.NotifyChannel,
			)
		}
		if w.store == nil {
			continue
		}
		marked, err := w.store.MarkDeliveredWithOutcome(ctx, r.ID, r.LeaseOwner, state.deliveryOutcome)
		if err != nil {
			slog.Error("outbox mark delivered",
				"batch_id", batchID,
				"row_id", r.ID,
				"method", r.MethodName,
				"err", err,
			)
		} else if !marked {
			slog.Warn("outbox mark delivered skipped stale lease",
				"batch_id", batchID,
				"row_id", r.ID,
				"method", r.MethodName,
				"lease_owner", r.LeaseOwner,
				"reason", "lease reclaimed; new owner will redeliver and mark",
			)
		}
	}
}

// orderedBatchRows returns a sorted copy because UPDATE ... RETURNING does not
// guarantee row order. Never sort the caller's slice in place: tests, alerting,
// and future retry bookkeeping may still hold it.
func orderedBatchRows(rows []OutboxRow) []OutboxRow {
	ordered := append([]OutboxRow(nil), rows...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Seq != ordered[j].Seq {
			return ordered[i].Seq < ordered[j].Seq
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered
}

// preflightBatch validates the complete persisted credential-bearing batch
// before any Redis operation runs. ClaimDue may return only a leading INCR,
// DEL, or another prefix when BatchPerTick cuts through a batch, so validating
// only batchRows could mutate Redis before a later legacy HSET is inspected.
func (w *Worker) preflightBatch(ctx context.Context, batchID types.BatchID, batchRows []OutboxRow) error {
	if !mayContainCredentialEvidenceWrite(batchRows) {
		return nil
	}
	rows := batchRows
	if w.loadBatchFn != nil {
		persisted, err := w.loadBatchFn(ctx, batchID)
		if err != nil {
			return fmt.Errorf("load complete batch for credential-evidence preflight: %w", err)
		}
		if len(persisted) == 0 {
			return fmt.Errorf("load complete batch for credential-evidence preflight: batch %s not found", batchID)
		}
		rows = persisted
	}
	return validateCredentialEvidenceOutboxRows(rows)
}

func mayContainCredentialEvidenceWrite(rows []OutboxRow) bool {
	for _, row := range rows {
		if isCredentialEvidencePublishMethod(row.MethodName) ||
			strings.HasPrefix(row.RedisKey, "gw:keypool:") ||
			row.RedisKey == keyMetaHashKey ||
			row.RedisKey == providerInfoHashKey {
			return true
		}
	}
	return false
}

func isCredentialEvidencePublishMethod(method string) bool {
	if strings.HasPrefix(method, "PublishKeyPool") {
		return true
	}
	switch method {
	case "PublishKey", "PublishKeyBulk", "PublishProvider":
		return true
	default:
		return false
	}
}

func validateCredentialEvidenceOutboxRows(rows []OutboxRow) error {
	for _, row := range rows {
		if row.Op != OpHSet {
			continue
		}

		switch {
		case strings.HasPrefix(row.RedisKey, "gw:keypool:"):
			var entry types.KeyPoolEntry
			if err := json.Unmarshal(row.Payload, &entry); err != nil {
				return invalidCredentialEvidence(row, "KeyPoolEntry", err)
			}
			if err := requireProviderAccountRef(entry.ProviderAccountRef); err != nil {
				return invalidCredentialEvidence(row, "KeyPoolEntry.provider_account_ref", err)
			}
			if entry.ProviderAccountRef == entry.KeyID || entry.ProviderAccountRef == entry.Ciphertext {
				return invalidCredentialEvidence(row, "KeyPoolEntry.provider_account_ref", errors.New("reuses credential identity or reference"))
			}

		case row.RedisKey == keyMetaHashKey:
			var meta types.KeyMeta
			if err := json.Unmarshal(row.Payload, &meta); err != nil {
				return invalidCredentialEvidence(row, "KeyMeta", err)
			}
			if meta.DedicatedUpstream == nil {
				continue
			}
			if err := requireProviderAccountRef(meta.DedicatedUpstream.ProviderAccountRef); err != nil {
				return invalidCredentialEvidence(row, "KeyMeta.dedicated_upstream.provider_account_ref", err)
			}
			if meta.DedicatedUpstream.ProviderAccountRef == meta.DedicatedUpstream.UpstreamKeyID ||
				meta.DedicatedUpstream.ProviderAccountRef == meta.DedicatedUpstream.Ciphertext {
				return invalidCredentialEvidence(row, "KeyMeta.dedicated_upstream.provider_account_ref", errors.New("reuses credential identity or reference"))
			}

		case row.RedisKey == providerInfoHashKey:
			var info types.ProviderInfo
			if err := json.Unmarshal(row.Payload, &info); err != nil {
				return invalidCredentialEvidence(row, "ProviderInfo", err)
			}
			if info.CredentialSource != types.ProviderCredentialSourceProviderEntitlement {
				continue
			}
			if err := requireProviderAccountRef(info.ProviderAccountRef); err != nil {
				return invalidCredentialEvidence(row, "ProviderInfo.provider_account_ref", err)
			}
			if info.ProviderAccountRef == info.ID || info.ProviderAccountRef == info.BaseURL {
				return invalidCredentialEvidence(row, "ProviderInfo.provider_account_ref", errors.New("reuses provider identity or endpoint"))
			}
		}
	}
	return nil
}

func requireProviderAccountRef(ref string) error {
	if ref == "" {
		return errors.New("missing")
	}
	return types.ValidateProviderAccountRef(ref)
}

func invalidCredentialEvidence(row OutboxRow, contract string, cause error) error {
	return fmt.Errorf("%w: row %d key %q field %q invalid %s: %v",
		errInvalidCredentialEvidenceOutboxPayload,
		row.ID,
		row.RedisKey,
		row.RedisField,
		contract,
		cause,
	)
}

func (w *Worker) deadLetterInvalidBatch(ctx context.Context, batchID types.BatchID, rows []OutboxRow, cause error) {
	method := "credential-evidence-publish"
	if len(rows) > 0 && rows[0].MethodName != "" {
		method = rows[0].MethodName
	}
	metrics.WorkerFailed()
	metrics.DeadLetter()
	slog.ErrorContext(ctx, "publisher outbox credential-evidence batch quarantined before Redis mutation",
		"event", "publisher_credential_evidence_batch_preflight_quarantined",
		"batch_id", batchID,
		"method", method,
		"claimed_row_count", len(rows),
		"error", cause.Error(),
	)
	if w.deadLetterFn != nil {
		w.deadLetterFn(batchID, method, cause.Error())
	}
	if w.store == nil {
		return
	}
	marked, err := w.store.DeadLetterBatch(ctx, batchID, cause.Error())
	if err != nil {
		slog.ErrorContext(ctx, "publisher outbox credential-evidence batch quarantine failed",
			"event", "publisher_credential_evidence_batch_preflight_quarantine_failed",
			"batch_id", batchID,
			"method", method,
			"error", err,
		)
		return
	}
	slog.WarnContext(ctx, "publisher outbox credential-evidence batch quarantined",
		"event", "publisher_credential_evidence_batch_preflight_quarantine_complete",
		"batch_id", batchID,
		"method", method,
		"quarantined_row_count", marked,
	)
}

func (w *Worker) markRowFailed(ctx context.Context, batchID types.BatchID, r OutboxRow, cause error) {
	attempt := r.AttemptCount + 1
	delay := w.backoff(attempt)
	deadLettering := attempt >= w.cfg.MaxAttempts
	nextAttemptAt := time.Now().Add(delay)

	logLevel := slog.LevelWarn
	if deadLettering {
		logLevel = slog.LevelError
	}
	slog.Log(ctx, logLevel, "publisher outbox row attempt failed",
		"batch_id", batchID,
		"row_id", r.ID,
		"seq", r.Seq,
		"method", r.MethodName,
		"op", r.Op,
		"redis_key", r.RedisKey,
		"redis_field", r.RedisField,
		"notify_channel", r.NotifyChannel,
		"attempt", attempt,
		"max_attempts", w.cfg.MaxAttempts,
		"next_attempt_at", nextAttemptAt.Format(time.RFC3339Nano),
		"dead_lettering", deadLettering,
		"error", cause.Error(),
	)

	metrics.WorkerFailed()
	if deadLettering {
		metrics.DeadLetter()
		if w.deadLetterFn != nil {
			w.deadLetterFn(batchID, r.MethodName, cause.Error())
		}
	}
	if w.store == nil {
		return
	}
	marked, err := w.store.MarkFailed(ctx, r.ID, r.LeaseOwner, attempt, w.cfg.MaxAttempts, cause.Error(), nextAttemptAt)
	if err != nil {
		slog.Error("outbox mark failed",
			"batch_id", batchID,
			"row_id", r.ID,
			"method", r.MethodName,
			"err", err,
		)
	} else if !marked {
		slog.Warn("outbox mark failed skipped stale lease",
			"batch_id", batchID,
			"row_id", r.ID,
			"method", r.MethodName,
			"lease_owner", r.LeaseOwner,
			"reason", "lease reclaimed; new owner will redeliver and mark",
		)
	}
}

func shouldLogDeliveredRow(r OutboxRow) bool {
	if r.NotifyChannel != "" {
		return true
	}
	return r.RedisKey == "gw:routes" ||
		r.RedisKey == "gw:model_catalog" ||
		r.RedisKey == "gw:providers" ||
		r.RedisKey == "gw:keypool_meta" ||
		strings.HasPrefix(r.RedisKey, "gw:keypool:")
}

// runOne dispatches one row to the right Redis primitive. The state arg
// carries per-batch context (currently just the latest OpIncr result, used
// by OpPublish to stamp message.revision).
func (w *Worker) runOne(ctx context.Context, r OutboxRow, state *batchState) error {
	state.deliveryOutcome = ""
	switch r.Op {
	case OpSet:
		var ttl time.Duration
		if r.TTLSeconds > 0 {
			ttl = time.Duration(r.TTLSeconds) * time.Second
		}
		return w.redis.Set(ctx, r.RedisKey, string(r.Payload), ttl).Err()
	case OpSetNX:
		var ttl time.Duration
		if r.TTLSeconds > 0 {
			ttl = time.Duration(r.TTLSeconds) * time.Second
		}
		_, err := w.redis.SetNX(ctx, r.RedisKey, string(r.Payload), ttl).Result()
		return err
	case OpHSet:
		if r.RedisKey == keyMetaHashKey {
			return w.runKeyMetaHSet(ctx, r)
		}
		return w.redis.HSet(ctx, r.RedisKey, r.RedisField, string(r.Payload)).Err()
	case OpHSetNX:
		_, err := w.redis.HSetNX(ctx, r.RedisKey, r.RedisField, string(r.Payload)).Result()
		return err
	case OpSAdd:
		return w.redis.SAdd(ctx, r.RedisKey, r.RedisMember).Err()
	case OpSRem:
		return w.redis.SRem(ctx, r.RedisKey, r.RedisMember).Err()
	case OpDel:
		return w.redis.Del(ctx, r.RedisKey).Err()
	case OpHDel:
		return w.redis.HDel(ctx, r.RedisKey, r.RedisField).Err()
	case OpKeyBalanceCAS:
		outcome, err := w.runKeyBalanceCAS(ctx, r)
		state.deliveryOutcome = outcome
		return err
	case OpKeyActivateAtomic:
		outcome, err := w.runKeyActivateAtomic(ctx, r)
		state.deliveryOutcome = outcome
		return err
	case OpKeyMetaStatus:
		return w.runKeyMetaStatus(ctx, r)
	case OpProductPolicySetCAS:
		return w.runProductPolicySetCAS(ctx, r)
	case OpProductPolicyDelCAS:
		return w.runProductPolicyDelCAS(ctx, r)
	case OpIncr:
		v, err := w.redis.Incr(ctx, r.RedisKey).Result()
		if err != nil {
			return err
		}
		state.lastIncr = v
		return nil
	case OpPublish:
		msg := r.NotifyMessage
		if state.lastIncr > 0 {
			msg = injectRevision(msg, state.lastIncr, r.BatchID)
		}
		return w.redis.Publish(ctx, r.NotifyChannel, string(msg)).Err()
	default:
		return fmt.Errorf("unknown op %q", r.Op)
	}
}

// keyBalanceCASPayload is the JSON shape stored in OutboxRow.Payload for
// OpKeyBalanceCAS rows. Decoded by runKeyBalanceCAS before invoking the
// Lua CAS script.
//
// Field encoding rationale: bool gets a separate field rather than being
// inferred from `value` so future schema changes (e.g. adding a third
// has_balance state) don't require fan-out parsing across worker code.
type keyBalanceCASPayload struct {
	Value             string `json:"value"`
	Reason            string `json:"reason"`
	HasBalance        bool   `json:"has_balance"`
	Deleted           bool   `json:"deleted"`
	DecisionID        string `json:"decision_id"`
	DecisionVersion   int64  `json:"decision_version"`
	Legacy            bool   `json:"legacy"`
	PublisherRevision int64  `json:"publisher_revision"`
	Fingerprint       string `json:"fingerprint"`
	// PreVersionedRevision is read only for OpKeyBalanceCAS rows written by
	// publisher versions that predate the decision identity contract.
	PreVersionedRevision *int64 `json:"revision,omitempty"`
	// Activation-only fields. They remain empty for OpKeyBalanceCAS.
	KeyHash             string          `json:"key_hash,omitempty"`
	KeyMeta             json.RawMessage `json:"key_meta,omitempty"`
	ConfigNotifyMessage json.RawMessage `json:"config_notify_message,omitempty"`
}

const (
	DeliveryOutcomeApplied    = "applied"
	DeliveryOutcomeSuperseded = "superseded"
	DeliveryOutcomeIdempotent = "idempotent"
	DeliveryOutcomeConflict   = "conflict"
)

// AdmissionFingerprint binds the ordering identity to the complete projected
// state. It lets Redis distinguish an at-least-once replay from a conflicting
// reuse of the same decision_version.
func AdmissionFingerprint(hasBalance bool, reason string, deleted bool) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(fmt.Sprintf("%t\n%s\n%t", hasBalance, reason, deleted))))
}

// ActivationFingerprint binds a Billing decision to both admission state and
// the exact KeyMeta installed by that decision. Reusing one decision version
// with different metadata must be a deterministic conflict, not an
// idempotent replay.
func ActivationFingerprint(hasBalance bool, reason string, keyHash string, keyMeta []byte) string {
	material := fmt.Sprintf("%t\n%s\nfalse\n%s\n%s", hasBalance, reason, keyHash, keyMeta)
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(material)))
}

// Redis hash keys touched by OpKeyBalanceCAS. Mirror constants in
// internal/publisher/publisher_keys.go::KeyKeyBalance{,Reason,Revision}.
// Hardcoded here because internal/outbox cannot import internal/publisher
// (publisher already imports outbox; that direction is the dependency
// graph contract). Manually keep these in sync if you rename — the
// constant names mirror the Go-side names in publisher_keys.go for
// grep-ability. Rename frequency is effectively zero (these are stable
// schema keys baked into the gateway), but documenting the coupling.
const (
	keyBalanceMainHashKey              = "gw:key_balance"
	keyBalanceReasonHashKey            = "gw:key_balance_reason"
	keyBalanceRevisionHashKey          = "gw:key_balance_revision"
	keyAdmissionDecisionVersionHashKey = "gw:key_admission_decision_version"
	keyAdmissionDecisionIDHashKey      = "gw:key_admission_decision_id"
	keyAdmissionFingerprintHashKey     = "gw:key_admission_fingerprint"
	keyAdmissionTombstoneHashKey       = "gw:key_admission_tombstone"
	keyAdmissionLegacyVersionHashKey   = "gw:key_admission_legacy_version"
	keyAdmissionLegacyIDHashKey        = "gw:key_admission_legacy_id"
)

// luaKeyBalanceCAS atomically performs admission CAS, state/tombstone update,
// and cache invalidation publish. Keeping PUBLISH inside the same Lua script
// is required because outbox rows are claimed independently: a separate
// notify row could otherwise be claimed by another worker before the CAS.
const luaKeyBalanceCAS = `
local field = ARGV[1]
local value = ARGV[2]
local reason = ARGV[3]
local has_balance = ARGV[4]
local deleted = ARGV[5]
local decision_id = ARGV[6]
local decision_version = tonumber(ARGV[7]) or 0
local legacy = ARGV[8]
local publisher_revision = tonumber(ARGV[9]) or 0
local fingerprint = ARGV[10]
local notify_channel = ARGV[11]
local notify_message = ARGV[12]
local key_hash = ARGV[13]
local key_meta = ARGV[14]
local config_notify_channel = ARGV[15]
local config_notify_message = ARGV[16]
local security_notify_channel = ARGV[17]

if publisher_revision <= 0 then
    return redis.error_reply('publisher_revision must be > 0')
end
if fingerprint == '' then
    return redis.error_reply('admission fingerprint required')
end
if key_hash ~= '' or key_meta ~= '' then
    if key_hash == '' or key_meta == '' or config_notify_channel == '' or config_notify_message == '' then
        return redis.error_reply('incomplete key activation payload')
    end
end

local current_decision_version = tonumber(redis.call('HGET', KEYS[4], field) or '0') or 0
local current_fingerprint = redis.call('HGET', KEYS[6], field) or ''
local outcome = 1

if legacy == '1' then
    -- Once an explicit Billing decision exists, no legacy write/delete may
    -- override it, even if Publisher happens to assign a larger local rev.
    if current_decision_version > 0 then
        return 0
    end
    local incoming_legacy_version = decision_version
    if incoming_legacy_version <= 0 then
        incoming_legacy_version = publisher_revision
    end
    local current_legacy_version_raw = redis.call('HGET', KEYS[8], field)
    local current_legacy_version = tonumber(current_legacy_version_raw or '0') or 0
    local pre_versioned_watermark = false
    if not current_legacy_version_raw then
        current_legacy_version = tonumber(redis.call('HGET', KEYS[3], field) or '0') or 0
        pre_versioned_watermark = current_legacy_version > 0
    end
    if incoming_legacy_version < current_legacy_version then
        return 0
    end
    if incoming_legacy_version == current_legacy_version then
        if pre_versioned_watermark and current_fingerprint == '' then
            local current_value = redis.call('HGET', KEYS[1], field)
            local current_reason = redis.call('HGET', KEYS[2], field) or ''
            local state_matches = current_value == value
            if has_balance == '1' then
                state_matches = state_matches and current_reason == ''
            else
                state_matches = state_matches and current_reason == reason
            end
            if not state_matches then
                return 3
            end
            outcome = 2
        else
            local current_legacy_id = redis.call('HGET', KEYS[9], field) or ''
            if current_fingerprint ~= fingerprint or current_legacy_id ~= decision_id then
                return 3
            end
            outcome = 2
        end
    end
else
    if decision_version <= 0 or decision_id == '' then
        return redis.error_reply('explicit admission decision requires id and version')
    end
    if decision_version < current_decision_version then
        return 0
    end
    if decision_version == current_decision_version then
        local current_decision_id = redis.call('HGET', KEYS[5], field) or ''
        if current_fingerprint ~= fingerprint or current_decision_id ~= decision_id then
            return 3
        end
        outcome = 2
    end
end

if outcome == 1 then
    if deleted == '1' then
        redis.call('HDEL', KEYS[1], field)
        redis.call('HDEL', KEYS[2], field)
        redis.call('HSET', KEYS[7], field, '1')
    else
        redis.call('HSET', KEYS[1], field, value)
        if has_balance == '1' then
            redis.call('HDEL', KEYS[2], field)
        else
            redis.call('HSET', KEYS[2], field, reason)
        end
        redis.call('HDEL', KEYS[7], field)
    end
    redis.call('HSET', KEYS[3], field, ARGV[9])
    redis.call('HSET', KEYS[6], field, fingerprint)
    if legacy == '1' then
        local legacy_version = decision_version
        if legacy_version <= 0 then
            legacy_version = publisher_revision
        end
        redis.call('HSET', KEYS[8], field, tostring(legacy_version))
        if decision_id == '' then
            redis.call('HDEL', KEYS[9], field)
        else
            redis.call('HSET', KEYS[9], field, decision_id)
        end
    else
        redis.call('HSET', KEYS[4], field, tostring(decision_version))
        redis.call('HSET', KEYS[5], field, decision_id)
    end

    -- Activation fields are all-or-none. The KeyMeta write, explicit
    -- admission state, revoked-set cleanup and invalidations are one Redis
    -- transaction, so Gateway can never observe an active key without its
    -- admission field.
    if key_hash ~= '' or key_meta ~= '' then
        redis.call('HSET', KEYS[10], key_hash, key_meta)
        local removed = redis.call('SREM', KEYS[11], key_hash)
        redis.call('PUBLISH', config_notify_channel, config_notify_message)
        if removed > 0 and security_notify_channel ~= '' then
            local security_revision = redis.call('INCR', KEYS[12])
            local security_message = cjson.encode({
                version = 2,
                type = 'revoked_key_removed',
                key_hash = key_hash,
                revision = security_revision
            })
            redis.call('PUBLISH', security_notify_channel, security_message)
        end
    end
end

if outcome == 1 and notify_channel ~= '' and notify_message ~= '' then
    redis.call('PUBLISH', notify_channel, notify_message)
end
return outcome
`

func (w *Worker) runKeyBalanceCAS(ctx context.Context, r OutboxRow) (string, error) {
	return w.runAdmissionCAS(ctx, r, false)
}

func (w *Worker) runKeyActivateAtomic(ctx context.Context, r OutboxRow) (string, error) {
	return w.runAdmissionCAS(ctx, r, true)
}

func (w *Worker) runAdmissionCAS(ctx context.Context, r OutboxRow, requireActivation bool) (string, error) {
	var p keyBalanceCASPayload
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		// Malformed payload is a publisher bug (impl.go invariant
		// violated). Surface so it goes to dead-letter, doesn't keep
		// failing forever in retry loop.
		return "", fmt.Errorf("decode OpKeyBalanceCAS payload: %w", err)
	}
	if p.PreVersionedRevision != nil {
		ambiguous := requireActivation ||
			*p.PreVersionedRevision <= 0 ||
			p.Legacy ||
			p.Deleted ||
			p.DecisionID != "" ||
			p.DecisionVersion != 0 ||
			p.PublisherRevision != 0 ||
			p.Fingerprint != "" ||
			p.KeyHash != "" ||
			len(p.KeyMeta) != 0 ||
			len(p.ConfigNotifyMessage) != 0
		if ambiguous {
			return "", fmt.Errorf("decode OpKeyBalanceCAS payload: ambiguous pre-versioned fields")
		}
		p.Legacy = true
		p.PublisherRevision = *p.PreVersionedRevision
		p.Fingerprint = AdmissionFingerprint(p.HasBalance, p.Reason, false)
	}
	expectedValue := "0"
	if p.HasBalance {
		expectedValue = "1"
	}
	if p.Value != expectedValue || (p.HasBalance && p.Reason != "") || (!p.HasBalance && !p.Deleted && strings.TrimSpace(p.Reason) == "") {
		return "", fmt.Errorf("decode OpKeyBalanceCAS payload: inconsistent admission state")
	}
	expectedFingerprint := AdmissionFingerprint(p.HasBalance, p.Reason, p.Deleted)
	if requireActivation {
		if p.Deleted || p.KeyHash == "" || len(p.KeyMeta) == 0 || len(p.ConfigNotifyMessage) == 0 {
			return "", fmt.Errorf("decode OpKeyActivateAtomic payload: incomplete activation")
		}
		var lifecycle keyMetaLifecyclePayload
		if err := json.Unmarshal(p.KeyMeta, &lifecycle); err != nil {
			return "", fmt.Errorf("decode OpKeyActivateAtomic key_meta: %w", err)
		}
		if lifecycle.KeyHash != p.KeyHash || lifecycle.Status != "active" {
			return "", fmt.Errorf("decode OpKeyActivateAtomic payload: key_meta identity/status mismatch")
		}
		expectedFingerprint = ActivationFingerprint(p.HasBalance, p.Reason, p.KeyHash, p.KeyMeta)
	} else if p.KeyHash != "" || len(p.KeyMeta) != 0 || len(p.ConfigNotifyMessage) != 0 {
		return "", fmt.Errorf("decode OpKeyBalanceCAS payload: unexpected activation fields")
	}
	if p.Fingerprint != expectedFingerprint {
		return "", fmt.Errorf("decode OpKeyBalanceCAS payload: fingerprint mismatch")
	}
	hasBalanceFlag := "0"
	if p.HasBalance {
		hasBalanceFlag = "1"
	}
	deletedFlag := "0"
	if p.Deleted {
		deletedFlag = "1"
	}
	legacyFlag := "0"
	if p.Legacy {
		legacyFlag = "1"
	}
	result, err := w.redis.Eval(ctx, luaKeyBalanceCAS,
		[]string{
			keyBalanceMainHashKey,
			keyBalanceReasonHashKey,
			keyBalanceRevisionHashKey,
			keyAdmissionDecisionVersionHashKey,
			keyAdmissionDecisionIDHashKey,
			keyAdmissionFingerprintHashKey,
			keyAdmissionTombstoneHashKey,
			keyAdmissionLegacyVersionHashKey,
			keyAdmissionLegacyIDHashKey,
			keyMetaHashKey,
			revokedKeysSetKey,
			securityRevisionKey,
		},
		r.RedisField,
		p.Value,
		p.Reason,
		hasBalanceFlag,
		deletedFlag,
		p.DecisionID,
		p.DecisionVersion,
		legacyFlag,
		p.PublisherRevision,
		p.Fingerprint,
		r.NotifyChannel,
		string(r.NotifyMessage),
		p.KeyHash,
		string(p.KeyMeta),
		configUpdateChannelForActivation,
		string(p.ConfigNotifyMessage),
		securityUpdateChannel,
	).Int64()
	if err != nil {
		return "", err
	}
	switch result {
	case 0:
		return DeliveryOutcomeSuperseded, nil
	case 1:
		return DeliveryOutcomeApplied, nil
	case 2:
		return DeliveryOutcomeIdempotent, nil
	case 3:
		return DeliveryOutcomeConflict, nil
	default:
		return "", fmt.Errorf("unexpected admission CAS outcome %d", result)
	}
}

type productPolicyRevisionPayload struct {
	Revision int64 `json:"revision"`
}

const productPolicyRevisionHashKey = "gw:product_policy_revisions"

const luaProductPolicySetCAS = `
local field = ARGV[1]
local payload = ARGV[2]
local new_rev = tonumber(ARGV[3]) or 0
if new_rev <= 0 then
    return redis.error_reply('product policy revision must be > 0')
end

local current_rev = 0
local current_rev_str = redis.call('HGET', KEYS[2], field)
if current_rev_str then
    current_rev = tonumber(current_rev_str) or 0
end
if current_rev == 0 then
    local raw = redis.call('HGET', KEYS[1], field)
    if raw then
        local ok, policy = pcall(cjson.decode, raw)
        if ok and policy['revision'] then
            current_rev = tonumber(policy['revision']) or 0
        end
    end
end
if new_rev <= current_rev then
    return 0
end

redis.call('HSET', KEYS[1], field, payload)
redis.call('HSET', KEYS[2], field, ARGV[3])
return 1
`

const luaProductPolicyDelCAS = `
local field = ARGV[1]
local new_rev = tonumber(ARGV[2]) or 0
if new_rev <= 0 then
    return redis.error_reply('product policy revision must be > 0')
end

local current_rev = 0
local current_rev_str = redis.call('HGET', KEYS[2], field)
if current_rev_str then
    current_rev = tonumber(current_rev_str) or 0
end
if current_rev == 0 then
    local raw = redis.call('HGET', KEYS[1], field)
    if raw then
        local ok, policy = pcall(cjson.decode, raw)
        if ok and policy['revision'] then
            current_rev = tonumber(policy['revision']) or 0
        end
    end
end
if new_rev <= current_rev then
    return 0
end

redis.call('HDEL', KEYS[1], field)
redis.call('HSET', KEYS[2], field, ARGV[2])
return 1
`

func (w *Worker) runProductPolicySetCAS(ctx context.Context, r OutboxRow) error {
	revision, err := productPolicyRevisionFromPayload(r.Payload)
	if err != nil {
		return err
	}
	_, err = w.redis.Eval(ctx, luaProductPolicySetCAS,
		[]string{r.RedisKey, productPolicyRevisionHashKey},
		r.RedisField, string(r.Payload), fmt.Sprint(revision),
	).Result()
	return err
}

func (w *Worker) runProductPolicyDelCAS(ctx context.Context, r OutboxRow) error {
	revision, err := productPolicyRevisionFromPayload(r.Payload)
	if err != nil {
		return err
	}
	_, err = w.redis.Eval(ctx, luaProductPolicyDelCAS,
		[]string{r.RedisKey, productPolicyRevisionHashKey},
		r.RedisField, fmt.Sprint(revision),
	).Result()
	return err
}

func productPolicyRevisionFromPayload(payload []byte) (int64, error) {
	var p productPolicyRevisionPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return 0, fmt.Errorf("decode product policy revision payload: %w", err)
	}
	if p.Revision <= 0 {
		return 0, fmt.Errorf("product policy revision must be > 0")
	}
	return p.Revision, nil
}

type keyMetaStatusPayload struct {
	Status   string `json:"status"`
	Revision int64  `json:"revision"`
}

const (
	keyMetaHashKey                   = "gw:keymeta"
	providerInfoHashKey              = "gw:providers"
	revokedKeysSetKey                = "gw:revoked_keys"
	securityRevisionKey              = "gw:config:rev:security"
	securityUpdateChannel            = "gw:security:update"
	configUpdateChannelForActivation = "gw:config:update"
)

type keyMetaLifecyclePayload struct {
	KeyHash string `json:"key_hash"`
	Status  string `json:"status"`
}

// decodeKeyMetaUnrevokeTarget extracts the key hash for the narrow case where
// an active KeyMeta write should also reconcile gw:revoked_keys. Any other
// status leaves the revoked set untouched.
func decodeKeyMetaUnrevokeTarget(payload []byte, field string) (string, bool, error) {
	var meta keyMetaLifecyclePayload
	if err := json.Unmarshal(payload, &meta); err != nil {
		return "", false, fmt.Errorf("decode keymeta payload: %w", err)
	}
	if meta.KeyHash == "" {
		meta.KeyHash = field
	}
	if meta.KeyHash == "" {
		return "", false, fmt.Errorf("decode keymeta payload: missing key_hash")
	}
	if field != "" && meta.KeyHash != field {
		return "", false, fmt.Errorf("decode keymeta payload: key_hash %q != field %q", meta.KeyHash, field)
	}
	return meta.KeyHash, meta.Status == "active", nil
}

// runKeyMetaHSet writes gw:keymeta and, only when the write re-activates a
// currently revoked key, removes the hash from gw:revoked_keys and emits the
// corresponding P0 security update. This keeps publisher-compatible callers
// working even if they only rewrite KeyMeta on enable.
func (w *Worker) runKeyMetaHSet(ctx context.Context, r OutboxRow) error {
	keyHash, shouldUnrevoke, err := decodeKeyMetaUnrevokeTarget(r.Payload, r.RedisField)
	if err != nil {
		return err
	}
	if err := w.redis.HSet(ctx, r.RedisKey, r.RedisField, string(r.Payload)).Err(); err != nil {
		return err
	}
	if !shouldUnrevoke {
		return nil
	}
	removed, err := w.redis.SRem(ctx, revokedKeysSetKey, keyHash).Result()
	if err != nil {
		return err
	}
	if removed == 0 {
		return nil
	}
	revision, err := w.redis.Incr(ctx, securityRevisionKey).Result()
	if err != nil {
		return err
	}
	msg, _ := json.Marshal(map[string]any{
		"version":  2,
		"type":     "revoked_key_removed",
		"key_hash": keyHash,
		"revision": revision,
	})
	return w.redis.Publish(ctx, securityUpdateChannel, string(msg)).Err()
}

const luaKeyMetaStatus = `
local raw = redis.call('HGET', KEYS[1], ARGV[1])
if not raw then
    return redis.error_reply('keymeta not found: ' .. ARGV[1])
end

local ok, meta = pcall(cjson.decode, raw)
if not ok then
    return redis.error_reply('keymeta json decode failed: ' .. tostring(meta))
end

meta['status'] = ARGV[2]
local rev = tonumber(ARGV[3]) or 0
if rev > 0 then
    meta['revision'] = rev
end

redis.call('HSET', KEYS[1], ARGV[1], cjson.encode(meta))
return 1
`

func (w *Worker) runKeyMetaStatus(ctx context.Context, r OutboxRow) error {
	var p keyMetaStatusPayload
	if err := json.Unmarshal(r.Payload, &p); err != nil {
		return fmt.Errorf("decode OpKeyMetaStatus payload: %w", err)
	}
	_, err := w.redis.Eval(ctx, luaKeyMetaStatus, []string{r.RedisKey}, r.RedisField, p.Status, p.Revision).Result()
	return err
}

// injectRevision sets the "revision" field on the publish-message JSON to
// the most recent OpIncr result in this batch. This is the **only** place
// commitWithRevision-domain notify messages should get their revision —
// callers in publisher/impl.go deliberately omit "revision" from the JSON
// they hand to the outbox so the field never carries a stale UnixMicro
// timestamp into the gateway revision tracker.
//
// Contract:
//   - state.lastIncr > 0  → revision field added/overwritten with that value
//   - state.lastIncr == 0 → injectRevision is NOT called (worker.runOne
//     guards on this); message is published with revision absent (= 0),
//     and gateway falls back to GET gw:config:rev:<domain> to resolve
//     the live counter value.
//
// Failure mode this prevents (F4, 2026-05-21):
//   - Pre-2026-05-21, publisher pre-filled "revision": entry.Revision (auto-
//     filled to time.Now().UnixMicro() ≈ 1.7e15) into the notify JSON.
//   - Normal flow: injectRevision overwrote it with the counter value (small
//     int). Correct.
//   - Worker-restart flow: HSet+Incr MarkDelivered, Publish failed → retry
//     fresh batchState{lastIncr:0} → injectRevision skipped → publish went
//     out with the UnixMicro fallback.
//   - Gateway saw revision=1.7e15, stored as local; every later normal
//     publish (revision=counter, small int) was permanently SKIPPED as
//     stale. Only 30s periodic refresh saved the system from being stuck.
//
// Falls back to returning the message unchanged on parse failure —
// preferable to dropping the publish.
func injectRevision(msg json.RawMessage, rev int64, batchID types.BatchID) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(msg, &m); err != nil {
		slog.Warn("outbox: revision injection skipped, msg not a JSON object",
			"batch_id", batchID,
			"len", len(msg),
		)
		return msg
	}
	m["revision"] = rev
	out, err := json.Marshal(m)
	if err != nil {
		return msg
	}
	return out
}

// backoff is exponential with jitter, capped at cfg.MaxDelay. Jitter is
// ±20% so a Redis outage recovery doesn't refire every failing batch in
// lockstep.
func (w *Worker) backoff(attempt int) time.Duration {
	base := w.cfg.BaseDelay << (attempt - 1)
	if base > w.cfg.MaxDelay {
		base = w.cfg.MaxDelay
	}
	jitter := float64(base) * (0.8 + rand.Float64()*0.4)
	return time.Duration(jitter)
}

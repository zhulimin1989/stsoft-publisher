package refunddrain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

const ProtocolVersion = 1
const maxKeyHashes = 10000

var (
	ErrInvalidArgument = errors.New("refund drain invalid argument")
	ErrNotFound        = errors.New("refund drain not found")
	ErrBindingConflict = errors.New("refund drain binding conflict")
	ErrStateConflict   = errors.New("refund drain state conflict")
	ErrStateMissing    = errors.New("refund drain state missing")
	keyHashPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const claimLeaseScript = `
local current = redis.call('GET', KEYS[1])
if current and current ~= ARGV[1] then
  return 0
end
if not current then
  redis.call('SET', KEYS[1], ARGV[1])
end
return 1
`

const establishKeyScript = `
local current = redis.call('GET', KEYS[1])
if current and current ~= ARGV[1] then
  return {-1, 0, 0}
end
if not current then
  redis.call('SET', KEYS[1], ARGV[1])
end
local inflight = tonumber(redis.call('GET', KEYS[2]) or '0')
local request_seen = redis.call('EXISTS', KEYS[3])
return {1, inflight, request_seen}
`

const inspectKeyScript = `
local current = redis.call('GET', KEYS[1])
if not current then
  return {0, 0, 0}
end
if current ~= ARGV[1] then
  return {-1, 0, 0}
end
local inflight = tonumber(redis.call('GET', KEYS[2]) or '0')
local request_seen = redis.call('EXISTS', KEYS[3])
return {1, inflight, request_seen}
`

const releaseKeyScript = `
local current = redis.call('GET', KEYS[1])
if not current then
  return 0
end
if current ~= ARGV[1] then
  return -1
end
redis.call('DEL', KEYS[1])
return 1
`

const releaseLeaseScript = `
local current = redis.call('GET', KEYS[1])
if not current then
  return 0
end
if current ~= ARGV[1] then
  return -1
end
redis.call('DEL', KEYS[1])
return 1
`

type Snapshot struct {
	ProtocolVersion int    `json:"protocol_version"`
	DrainID         string `json:"drain_id"`
	KeyCount        int    `json:"key_count"`
	Inflight        int64  `json:"in_flight"`
	RequestSeenKeys int    `json:"request_seen_key_count"`
	Ready           bool   `json:"ready"`
}

type Service interface {
	Establish(ctx context.Context, drainID string, keyHashes []string) (Snapshot, error)
	Status(ctx context.Context, drainID string) (Snapshot, error)
	Release(ctx context.Context, drainID string) error
}

type Coordinator struct {
	redis redis.Cmdable
}

func New(client redis.Cmdable) *Coordinator {
	return &Coordinator{redis: client}
}

func (c *Coordinator) Establish(ctx context.Context, drainID string, keyHashes []string) (Snapshot, error) {
	drainID, err := normalizeDrainID(drainID)
	if err != nil {
		return Snapshot{}, err
	}
	hashes, payload, err := normalizeKeyHashes(keyHashes)
	if err != nil {
		return Snapshot{}, err
	}
	if c == nil || c.redis == nil {
		return Snapshot{}, fmt.Errorf("refund drain redis is not configured")
	}

	claimed, err := c.redis.Eval(ctx, claimLeaseScript, []string{leaseKey(drainID)}, payload).Int64()
	if err != nil {
		return Snapshot{}, fmt.Errorf("claim refund drain lease: %w", err)
	}
	if claimed != 1 {
		return Snapshot{}, fmt.Errorf("%w: drain_id %q is already bound to another key set", ErrBindingConflict, drainID)
	}

	snapshot := newSnapshot(drainID, len(hashes))
	for _, keyHash := range hashes {
		values, err := c.redis.Eval(
			ctx,
			establishKeyScript,
			[]string{stateKey(keyHash), inflightKey(keyHash), requestSeenKey(keyHash)},
			drainID,
		).Slice()
		if err != nil {
			return Snapshot{}, fmt.Errorf("establish refund drain key: %w", err)
		}
		state, inflight, requestSeen, err := parseKeySnapshot(values)
		if err != nil {
			return Snapshot{}, err
		}
		if state != 1 {
			return Snapshot{}, fmt.Errorf("%w: key hash is claimed by another drain", ErrStateConflict)
		}
		snapshot.Inflight += inflight
		if requestSeen {
			snapshot.RequestSeenKeys++
		}
	}
	snapshot.Ready = snapshot.Inflight == 0 && snapshot.RequestSeenKeys == 0
	return snapshot, nil
}

func (c *Coordinator) Status(ctx context.Context, drainID string) (Snapshot, error) {
	drainID, err := normalizeDrainID(drainID)
	if err != nil {
		return Snapshot{}, err
	}
	if c == nil || c.redis == nil {
		return Snapshot{}, fmt.Errorf("refund drain redis is not configured")
	}
	payload, err := c.redis.Get(ctx, leaseKey(drainID)).Result()
	if errors.Is(err, redis.Nil) {
		return Snapshot{}, fmt.Errorf("%w: %s", ErrNotFound, drainID)
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("read refund drain lease: %w", err)
	}
	hashes, err := decodeLeasePayload(payload)
	if err != nil {
		return Snapshot{}, err
	}

	snapshot := newSnapshot(drainID, len(hashes))
	for _, keyHash := range hashes {
		values, err := c.redis.Eval(
			ctx,
			inspectKeyScript,
			[]string{stateKey(keyHash), inflightKey(keyHash), requestSeenKey(keyHash)},
			drainID,
		).Slice()
		if err != nil {
			return Snapshot{}, fmt.Errorf("inspect refund drain key: %w", err)
		}
		state, inflight, requestSeen, err := parseKeySnapshot(values)
		if err != nil {
			return Snapshot{}, err
		}
		switch state {
		case -1:
			return Snapshot{}, fmt.Errorf("%w: key hash is claimed by another drain", ErrStateConflict)
		case 0:
			return Snapshot{}, fmt.Errorf("%w: key hash marker is absent", ErrStateMissing)
		}
		snapshot.Inflight += inflight
		if requestSeen {
			snapshot.RequestSeenKeys++
		}
	}
	snapshot.Ready = snapshot.Inflight == 0 && snapshot.RequestSeenKeys == 0
	return snapshot, nil
}

func (c *Coordinator) Release(ctx context.Context, drainID string) error {
	drainID, err := normalizeDrainID(drainID)
	if err != nil {
		return err
	}
	if c == nil || c.redis == nil {
		return fmt.Errorf("refund drain redis is not configured")
	}
	payload, err := c.redis.Get(ctx, leaseKey(drainID)).Result()
	if errors.Is(err, redis.Nil) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read refund drain lease for release: %w", err)
	}
	hashes, err := decodeLeasePayload(payload)
	if err != nil {
		return err
	}
	for _, keyHash := range hashes {
		released, err := c.redis.Eval(ctx, releaseKeyScript, []string{stateKey(keyHash)}, drainID).Int64()
		if err != nil {
			return fmt.Errorf("release refund drain key: %w", err)
		}
		if released < 0 {
			return fmt.Errorf("%w: key hash is claimed by another drain", ErrStateConflict)
		}
	}
	released, err := c.redis.Eval(ctx, releaseLeaseScript, []string{leaseKey(drainID)}, payload).Int64()
	if err != nil {
		return fmt.Errorf("release refund drain lease: %w", err)
	}
	if released < 0 {
		return fmt.Errorf("%w: lease changed during release", ErrBindingConflict)
	}
	return nil
}

func normalizeDrainID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "/\\\r\n\t") {
		return "", fmt.Errorf("%w: drain_id is invalid", ErrInvalidArgument)
	}
	return value, nil
}

func normalizeKeyHashes(values []string) ([]string, string, error) {
	if len(values) > maxKeyHashes {
		return nil, "", fmt.Errorf("%w: key_hashes exceeds %d items", ErrInvalidArgument, maxKeyHashes)
	}
	unique := make(map[string]struct{}, len(values))
	for index, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if !keyHashPattern.MatchString(value) {
			return nil, "", fmt.Errorf("%w: key_hashes[%d] must be a 64-character hex digest", ErrInvalidArgument, index)
		}
		unique[value] = struct{}{}
	}
	hashes := make([]string, 0, len(unique))
	for value := range unique {
		hashes = append(hashes, value)
	}
	sort.Strings(hashes)
	encoded, err := json.Marshal(hashes)
	if err != nil {
		return nil, "", fmt.Errorf("encode refund drain lease: %w", err)
	}
	return hashes, string(encoded), nil
}

func decodeLeasePayload(payload string) ([]string, error) {
	var hashes []string
	if err := json.Unmarshal([]byte(payload), &hashes); err != nil {
		return nil, fmt.Errorf("decode refund drain lease: %w", err)
	}
	normalized, _, err := normalizeKeyHashes(hashes)
	if err != nil {
		return nil, fmt.Errorf("invalid stored refund drain lease: %w", err)
	}
	if len(normalized) != len(hashes) {
		return nil, fmt.Errorf("invalid stored refund drain lease: duplicate key hash")
	}
	return normalized, nil
}

func parseKeySnapshot(values []any) (int64, int64, bool, error) {
	if len(values) != 3 {
		return 0, 0, false, fmt.Errorf("refund drain key script returned %d values", len(values))
	}
	state, err := redisInt64(values[0])
	if err != nil {
		return 0, 0, false, err
	}
	inflight, err := redisInt64(values[1])
	if err != nil || inflight < 0 {
		return 0, 0, false, fmt.Errorf("invalid refund drain in-flight count: %v", values[1])
	}
	seen, err := redisInt64(values[2])
	if err != nil || (seen != 0 && seen != 1) {
		return 0, 0, false, fmt.Errorf("invalid refund drain request-seen flag: %v", values[2])
	}
	return state, inflight, seen == 1, nil
}

func redisInt64(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		parsed, err := strconv.ParseInt(typed, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid Redis integer %q: %w", typed, err)
		}
		return parsed, nil
	case []byte:
		return redisInt64(string(typed))
	default:
		return 0, fmt.Errorf("invalid Redis integer type %T", value)
	}
}

func newSnapshot(drainID string, keyCount int) Snapshot {
	return Snapshot{
		ProtocolVersion: ProtocolVersion,
		DrainID:         drainID,
		KeyCount:        keyCount,
	}
}

func leaseKey(drainID string) string {
	return "gw:refund_drain:lease:" + drainID
}

func stateKey(keyHash string) string {
	return "gw:refund_drain:{" + keyHash + "}:state"
}

func inflightKey(keyHash string) string {
	return "gw:refund_drain:{" + keyHash + "}:inflight"
}

func requestSeenKey(keyHash string) string {
	return "gw:refund_drain:{" + keyHash + "}:request_seen"
}

var _ Service = (*Coordinator)(nil)

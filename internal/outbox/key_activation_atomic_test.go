package outbox

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestKeyActivationMakesMetaAdmissionAndUnrevokeVisibleTogether(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	w := NewWorker(nil, rdb, WorkerConfig{}, nil)

	keyID := "5ec09f5b-5476-4f9b-95fb-a008114aa605"
	keyHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	keyMeta := json.RawMessage(`{"schema_version":2,"revision":99,"key_id":"` + keyID + `","key_hash":"` + keyHash + `","status":"active"}`)
	configNotify := json.RawMessage(`{"version":2,"type":"key_update","key_id":"` + keyID + `","key_hash":"` + keyHash + `"}`)
	admissionNotify := json.RawMessage(`{"version":1,"type":"key_admission_update","key_id":"` + keyID + `","allowed":false}`)

	if err := rdb.SAdd(ctx, revokedKeysSetKey, keyHash).Err(); err != nil {
		t.Fatal(err)
	}
	channels := []string{configUpdateChannelForActivation, "gw:admission:update", securityUpdateChannel}
	pubsub := rdb.Subscribe(ctx, channels...)
	t.Cleanup(func() { _ = pubsub.Close() })
	if _, err := pubsub.Receive(ctx); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	payload := keyBalanceCASPayload{
		Value:               "0",
		Reason:              "account_debt",
		HasBalance:          false,
		DecisionID:          "4aa89247-e38b-44ad-bd13-557308515c01",
		DecisionVersion:     9,
		PublisherRevision:   101,
		KeyHash:             keyHash,
		KeyMeta:             keyMeta,
		ConfigNotifyMessage: configNotify,
	}
	payload.Fingerprint = ActivationFingerprint(payload.HasBalance, payload.Reason, keyHash, keyMeta)
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := w.runKeyActivateAtomic(ctx, OutboxRow{
		Op:              OpKeyActivateAtomic,
		RedisField:      keyID,
		Payload:         body,
		NotifyChannel:   "gw:admission:update",
		NotifyMessage:   admissionNotify,
		SchemaVersion:   2,
		EnvelopeVersion: 1,
	})
	if err != nil || outcome != DeliveryOutcomeApplied {
		t.Fatalf("activate outcome=%q err=%v", outcome, err)
	}

	if got, err := rdb.HGet(ctx, keyMetaHashKey, keyHash).Result(); err != nil || got != string(keyMeta) {
		t.Fatalf("keymeta=%q err=%v", got, err)
	}
	if got, err := rdb.HGet(ctx, keyBalanceMainHashKey, keyID).Result(); err != nil || got != "0" {
		t.Fatalf("admission=%q err=%v", got, err)
	}
	if got, err := rdb.HGet(ctx, keyBalanceReasonHashKey, keyID).Result(); err != nil || got != "account_debt" {
		t.Fatalf("admission reason=%q err=%v", got, err)
	}
	if member, err := rdb.SIsMember(ctx, revokedKeysSetKey, keyHash).Result(); err != nil || member {
		t.Fatalf("revoked member=%v err=%v", member, err)
	}

	seen := map[string]bool{}
	deadline := time.After(time.Second)
	for len(seen) < len(channels) {
		select {
		case msg := <-pubsub.Channel():
			seen[msg.Channel] = true
		case <-deadline:
			t.Fatalf("missing activation invalidations: seen=%v", seen)
		}
	}
}

func TestKeyActivationReplayDoesNotUnrevokeAKeyRevokedAfterActivation(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	ctx := context.Background()
	w := NewWorker(nil, rdb, WorkerConfig{}, nil)

	keyID := "932769d1-789d-4c57-9ea9-a8ed24a46258"
	keyHash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	keyMeta := json.RawMessage(`{"schema_version":2,"revision":99,"key_id":"` + keyID + `","key_hash":"` + keyHash + `","status":"active"}`)
	p := keyBalanceCASPayload{
		Value: "1", HasBalance: true,
		DecisionID: "cddfbc99-2163-4ced-bdb3-9605f044e96f", DecisionVersion: 10,
		PublisherRevision: 102, KeyHash: keyHash, KeyMeta: keyMeta,
		ConfigNotifyMessage: json.RawMessage(`{"type":"key_update"}`),
	}
	p.Fingerprint = ActivationFingerprint(true, "", keyHash, keyMeta)
	body, _ := json.Marshal(p)
	row := OutboxRow{RedisField: keyID, Payload: body, NotifyChannel: "gw:admission:update", NotifyMessage: json.RawMessage(`{"type":"key_admission_update"}`)}

	if got, err := w.runKeyActivateAtomic(ctx, row); err != nil || got != DeliveryOutcomeApplied {
		t.Fatalf("first activation outcome=%q err=%v", got, err)
	}
	if err := rdb.SAdd(ctx, revokedKeysSetKey, keyHash).Err(); err != nil {
		t.Fatal(err)
	}
	if got, err := w.runKeyActivateAtomic(ctx, row); err != nil || got != DeliveryOutcomeIdempotent {
		t.Fatalf("replay outcome=%q err=%v", got, err)
	}
	if member, err := rdb.SIsMember(ctx, revokedKeysSetKey, keyHash).Result(); err != nil || !member {
		t.Fatalf("idempotent replay removed a newer revoke: member=%v err=%v", member, err)
	}
}

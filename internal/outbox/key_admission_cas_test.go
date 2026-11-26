package outbox

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestKeyAdmissionCASRejectsOldSetAndDeleteAndAdvancesSameStateWatermark(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	w := NewWorker(nil, rdb, WorkerConfig{}, nil)
	ctx := context.Background()
	field := "5ec09f5b-5476-4f9b-95fb-a008114aa605"

	apply := func(t *testing.T, p keyBalanceCASPayload) string {
		t.Helper()
		body, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := w.runKeyBalanceCAS(ctx, OutboxRow{
			Op:            OpKeyBalanceCAS,
			RedisField:    field,
			Payload:       body,
			NotifyChannel: "gw:admission:update",
			NotifyMessage: json.RawMessage(`{"type":"key_admission_update","key_id":"` + field + `"}`),
		})
		if err != nil {
			t.Fatalf("run CAS: %v", err)
		}
		return got
	}

	if got := apply(t, admissionPayload(false, "account_debt", false, "d2", 2, 200)); got != DeliveryOutcomeApplied {
		t.Fatalf("new deny outcome=%q", got)
	}
	if got := apply(t, admissionPayload(true, "", false, "d1", 1, 300)); got != DeliveryOutcomeSuperseded {
		t.Fatalf("old allow outcome=%q", got)
	}
	if got := apply(t, admissionPayload(false, "", true, "delete-1", 1, 400)); got != DeliveryOutcomeSuperseded {
		t.Fatalf("old delete outcome=%q", got)
	}
	if got := apply(t, admissionPayload(false, "account_debt", false, "d3", 3, 500)); got != DeliveryOutcomeApplied {
		t.Fatalf("same-state higher watermark outcome=%q", got)
	}

	if got, err := rdb.HGet(ctx, keyBalanceMainHashKey, field).Result(); err != nil || got != "0" {
		t.Fatalf("canonical balance=%q err=%v want deny", got, err)
	}
	if got, err := rdb.HGet(ctx, keyAdmissionDecisionVersionHashKey, field).Int64(); err != nil || got != 3 {
		t.Fatalf("decision watermark=%d err=%v want 3", got, err)
	}
	if got, err := rdb.HGet(ctx, keyBalanceRevisionHashKey, field).Int64(); err != nil || got != 500 {
		t.Fatalf("publisher revision=%d err=%v want independent 500", got, err)
	}

	if got := apply(t, admissionPayload(false, "", true, "delete-4", 4, 600)); got != DeliveryOutcomeApplied {
		t.Fatalf("new delete outcome=%q", got)
	}
	if _, err := rdb.HGet(ctx, keyBalanceMainHashKey, field).Result(); err != redis.Nil {
		t.Fatalf("delete must remove main value, got err=%v", err)
	}
	if got, err := rdb.HGet(ctx, keyAdmissionTombstoneHashKey, field).Result(); err != nil || got != "1" {
		t.Fatalf("tombstone=%q err=%v", got, err)
	}
	if got := apply(t, admissionPayload(true, "", false, "d3-old", 3, 700)); got != DeliveryOutcomeSuperseded {
		t.Fatalf("old set resurrected tombstone; outcome=%q", got)
	}
	if _, err := rdb.HGet(ctx, keyBalanceMainHashKey, field).Result(); err != redis.Nil {
		t.Fatalf("old set resurrected deleted admission, err=%v", err)
	}
	legacy := admissionPayload(true, "", false, "", 0, 9999)
	legacy.Legacy = true
	if got := apply(t, legacy); got != DeliveryOutcomeSuperseded {
		t.Fatalf("legacy write overrode explicit watermark; outcome=%q", got)
	}
}

func TestKeyAdmissionCASPublishesOnlyAfterAppliedStateIsVisible(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	w := NewWorker(nil, rdb, WorkerConfig{}, nil)
	ctx := context.Background()
	channel := "gw:admission:update"
	field := "932769d1-789d-4c57-9ea9-a8ed24a46258"

	pubsub := rdb.Subscribe(ctx, channel)
	t.Cleanup(func() { _ = pubsub.Close() })
	if _, err := pubsub.Receive(ctx); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	messages := pubsub.Channel()

	run := func(p keyBalanceCASPayload) string {
		body, _ := json.Marshal(p)
		outcome, err := w.runKeyBalanceCAS(ctx, OutboxRow{
			RedisField:    field,
			Payload:       body,
			NotifyChannel: channel,
			NotifyMessage: json.RawMessage(`{"type":"key_admission_update","key_id":"` + field + `"}`),
		})
		if err != nil {
			t.Fatalf("CAS: %v", err)
		}
		return outcome
	}

	if got := run(admissionPayload(false, "account_debt", false, "decision-2", 2, 10)); got != DeliveryOutcomeApplied {
		t.Fatalf("apply outcome=%q", got)
	}
	select {
	case msg := <-messages:
		if msg.Channel != channel {
			t.Fatalf("channel=%q want %q", msg.Channel, channel)
		}
		// The notification and all sidecar writes are one Lua transaction.
		// By the time a subscriber observes it, the new canonical state must
		// already be readable.
		if got, err := rdb.HGet(ctx, keyBalanceMainHashKey, field).Result(); err != nil || got != "0" {
			t.Fatalf("notification preceded main state: value=%q err=%v", got, err)
		}
		if got, err := rdb.HGet(ctx, keyAdmissionDecisionVersionHashKey, field).Int64(); err != nil || got != 2 {
			t.Fatalf("notification preceded decision sidecar: version=%d err=%v", got, err)
		}
	case <-time.After(time.Second):
		t.Fatal("applied admission did not publish invalidation")
	}

	noMessage := func(label string) {
		t.Helper()
		select {
		case msg := <-messages:
			t.Fatalf("%s unexpectedly published %+v", label, msg)
		case <-time.After(50 * time.Millisecond):
		}
	}

	if got := run(admissionPayload(true, "", false, "decision-1", 1, 11)); got != DeliveryOutcomeSuperseded {
		t.Fatalf("superseded outcome=%q", got)
	}
	noMessage("superseded")
	if got := run(admissionPayload(false, "account_debt", false, "decision-2", 2, 12)); got != DeliveryOutcomeIdempotent {
		t.Fatalf("idempotent outcome=%q", got)
	}
	noMessage("idempotent")
	if got := run(admissionPayload(false, "account_debt", false, "other-id", 2, 13)); got != DeliveryOutcomeConflict {
		t.Fatalf("conflict outcome=%q", got)
	}
	noMessage("conflict")
}

func TestKeyAdmissionCASRejectsEqualVersionWithDifferentFingerprint(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	w := NewWorker(nil, rdb, WorkerConfig{}, nil)
	ctx := context.Background()
	field := "a5352474-129c-48cc-bf66-98ef1e16ae59"

	first := admissionPayload(false, "account_debt", false, "decision", 7, 100)
	body, _ := json.Marshal(first)
	if _, err := w.runKeyBalanceCAS(ctx, OutboxRow{RedisField: field, Payload: body}); err != nil {
		t.Fatalf("first CAS: %v", err)
	}
	conflict := admissionPayload(true, "", false, "decision", 7, 101)
	body, _ = json.Marshal(conflict)
	if got, err := w.runKeyBalanceCAS(ctx, OutboxRow{RedisField: field, Payload: body}); err != nil || got != DeliveryOutcomeConflict {
		t.Fatalf("equal version with different content outcome=%q err=%v, want conflict without retry", got, err)
	}
	sameContentDifferentID := admissionPayload(false, "account_debt", false, "other-decision", 7, 102)
	body, _ = json.Marshal(sameContentDifferentID)
	if got, err := w.runKeyBalanceCAS(ctx, OutboxRow{RedisField: field, Payload: body}); err != nil || got != DeliveryOutcomeConflict {
		t.Fatalf("equal version with different identity outcome=%q err=%v, want conflict without retry", got, err)
	}
	if got, err := rdb.HGet(ctx, keyBalanceMainHashKey, field).Result(); err != nil || got != "0" {
		t.Fatalf("conflict changed canonical admission value=%q err=%v", got, err)
	}
}

func TestKeyAdmissionCASDeliversPreVersionedPendingOutboxPayload(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	w := NewWorker(nil, rdb, WorkerConfig{}, nil)
	ctx := context.Background()
	field := "1b5e7468-1f17-476e-b84f-7eb3611a52fa"

	preExistingNewer := field + ":newer"
	if err := rdb.HSet(ctx, keyBalanceMainHashKey, preExistingNewer, "1").Err(); err != nil {
		t.Fatal(err)
	}
	if err := rdb.HSet(ctx, keyBalanceRevisionHashKey, preExistingNewer, "50").Err(); err != nil {
		t.Fatal(err)
	}
	olderBody := []byte(`{"value":"0","reason":"account_debt","has_balance":false,"revision":41}`)
	if got, err := w.runKeyBalanceCAS(ctx, OutboxRow{
		Op: OpKeyBalanceCAS, RedisField: preExistingNewer, Payload: olderBody,
	}); err != nil || got != DeliveryOutcomeSuperseded {
		t.Fatalf("older pre-versioned CAS outcome=%q err=%v, want superseded", got, err)
	}
	if value, err := rdb.HGet(ctx, keyBalanceMainHashKey, preExistingNewer).Result(); err != nil || value != "1" {
		t.Fatalf("older CAS overrode pre-existing state: value=%q err=%v", value, err)
	}

	preExistingSame := field + ":same"
	if err := rdb.HSet(ctx, keyBalanceMainHashKey, preExistingSame, "0").Err(); err != nil {
		t.Fatal(err)
	}
	if err := rdb.HSet(ctx, keyBalanceReasonHashKey, preExistingSame, "account_debt").Err(); err != nil {
		t.Fatal(err)
	}
	if err := rdb.HSet(ctx, keyBalanceRevisionHashKey, preExistingSame, "41").Err(); err != nil {
		t.Fatal(err)
	}
	if got, err := w.runKeyBalanceCAS(ctx, OutboxRow{
		Op: OpKeyBalanceCAS, RedisField: preExistingSame, Payload: olderBody,
	}); err != nil || got != DeliveryOutcomeIdempotent {
		t.Fatalf("same pre-versioned CAS outcome=%q err=%v, want idempotent", got, err)
	}
	conflictingBody := []byte(`{"value":"1","reason":"","has_balance":true,"revision":41}`)
	if got, err := w.runKeyBalanceCAS(ctx, OutboxRow{
		Op: OpKeyBalanceCAS, RedisField: preExistingSame, Payload: conflictingBody,
	}); err != nil || got != DeliveryOutcomeConflict {
		t.Fatalf("conflicting pre-versioned CAS outcome=%q err=%v, want conflict", got, err)
	}

	legacyBody := []byte(`{"value":"0","reason":"account_debt","has_balance":false,"revision":41}`)
	got, err := w.runKeyBalanceCAS(ctx, OutboxRow{
		Op:         OpKeyBalanceCAS,
		RedisField: field,
		Payload:    legacyBody,
	})
	if err != nil || got != DeliveryOutcomeApplied {
		t.Fatalf("pre-versioned CAS outcome=%q err=%v, want applied", got, err)
	}
	if value, err := rdb.HGet(ctx, keyBalanceMainHashKey, field).Result(); err != nil || value != "0" {
		t.Fatalf("canonical balance=%q err=%v, want deny", value, err)
	}
	if revision, err := rdb.HGet(ctx, keyBalanceRevisionHashKey, field).Int64(); err != nil || revision != 41 {
		t.Fatalf("publisher revision=%d err=%v, want 41", revision, err)
	}
	if version, err := rdb.HGet(ctx, keyAdmissionLegacyVersionHashKey, field).Int64(); err != nil || version != 41 {
		t.Fatalf("legacy version=%d err=%v, want 41", version, err)
	}
	if fingerprint, err := rdb.HGet(ctx, keyAdmissionFingerprintHashKey, field).Result(); err != nil || fingerprint != AdmissionFingerprint(false, "account_debt", false) {
		t.Fatalf("legacy fingerprint=%q err=%v", fingerprint, err)
	}

	explicitBody, err := json.Marshal(admissionPayload(true, "", false, "decision-1", 1, 42))
	if err != nil {
		t.Fatal(err)
	}
	if got, err = w.runKeyBalanceCAS(ctx, OutboxRow{
		Op: OpKeyBalanceCAS, RedisField: field, Payload: explicitBody,
	}); err != nil || got != DeliveryOutcomeApplied {
		t.Fatalf("explicit CAS outcome=%q err=%v, want applied", got, err)
	}

	staleLegacyBody := []byte(`{"value":"0","reason":"account_debt","has_balance":false,"revision":9999}`)
	if got, err = w.runKeyBalanceCAS(ctx, OutboxRow{
		Op: OpKeyBalanceCAS, RedisField: field, Payload: staleLegacyBody,
	}); err != nil || got != DeliveryOutcomeSuperseded {
		t.Fatalf("legacy CAS after explicit decision outcome=%q err=%v, want superseded", got, err)
	}
	if value, err := rdb.HGet(ctx, keyBalanceMainHashKey, field).Result(); err != nil || value != "1" {
		t.Fatalf("legacy CAS overrode explicit allow: value=%q err=%v", value, err)
	}
}

func TestKeyAdmissionCASRejectsAmbiguousPreVersionedPayload(t *testing.T) {
	mini := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	w := NewWorker(nil, rdb, WorkerConfig{}, nil)

	validExplicit := admissionPayload(true, "", false, "decision-1", 1, 42)
	explicitWithZeroRevision, err := json.Marshal(map[string]any{
		"value":              validExplicit.Value,
		"reason":             validExplicit.Reason,
		"has_balance":        validExplicit.HasBalance,
		"deleted":            validExplicit.Deleted,
		"decision_id":        validExplicit.DecisionID,
		"decision_version":   validExplicit.DecisionVersion,
		"legacy":             validExplicit.Legacy,
		"publisher_revision": validExplicit.PublisherRevision,
		"fingerprint":        validExplicit.Fingerprint,
		"revision":           0,
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		body []byte
	}{
		{
			name: "positive old revision mixed with new fields",
			body: []byte(`{"value":"1","reason":"","has_balance":true,"revision":41,"publisher_revision":42}`),
		},
		{
			name: "zero old revision mixed with valid new fields",
			body: explicitWithZeroRevision,
		},
		{
			name: "zero old revision",
			body: []byte(`{"value":"1","reason":"","has_balance":true,"revision":0}`),
		},
		{
			name: "negative old revision",
			body: []byte(`{"value":"1","reason":"","has_balance":true,"revision":-1}`),
		},
		{
			name: "old value disagrees with has balance",
			body: []byte(`{"value":"1","reason":"account_debt","has_balance":false,"revision":41}`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := w.runKeyBalanceCAS(context.Background(), OutboxRow{
				Op: OpKeyBalanceCAS, RedisField: "ambiguous", Payload: tt.body,
			}); err == nil {
				t.Fatal("invalid pre-versioned payload was accepted")
			}
		})
	}
}

func admissionPayload(allowed bool, reason string, deleted bool, decisionID string, decisionVersion, publisherRevision int64) keyBalanceCASPayload {
	value := "0"
	if allowed {
		value = "1"
	}
	return keyBalanceCASPayload{
		Value:             value,
		Reason:            reason,
		HasBalance:        allowed,
		Deleted:           deleted,
		DecisionID:        decisionID,
		DecisionVersion:   decisionVersion,
		PublisherRevision: publisherRevision,
		Fingerprint:       AdmissionFingerprint(allowed, reason, deleted),
	}
}

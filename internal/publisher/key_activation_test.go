package publisher

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/tokenplatform/publisher/internal/outbox"
	"github.com/tokenplatform/publisher/internal/types"
)

func TestBuildActivateKeyUsesOneAtomicOutboxRow(t *testing.T) {
	meta := &types.KeyMeta{
		KeyID:    uuid.NewString(),
		KeyHash:  string(validHash()),
		Revision: 1700000000000000,
		Status:   "active",
	}
	decision := types.KeyAdmissionDecision{ID: uuid.NewString(), Version: 41}

	rows, err := buildActivateKeyRows(meta, false, "account_debt", decision, 77)
	if err != nil {
		t.Fatalf("build activation: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("activation rows=%d want exactly one Redis atomic unit", len(rows))
	}
	row := rows[0]
	if row.Op != outbox.OpKeyActivateAtomic {
		t.Fatalf("activation op=%q want %q", row.Op, outbox.OpKeyActivateAtomic)
	}
	if row.RedisField != meta.KeyID {
		t.Fatalf("activation field=%q want key_id %q", row.RedisField, meta.KeyID)
	}

	var payload map[string]any
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		t.Fatalf("decode activation payload: %v", err)
	}
	if payload["key_hash"] != meta.KeyHash {
		t.Fatalf("activation dropped key hash: %+v", payload)
	}
	if payload["key_meta"] == nil {
		t.Fatalf("activation dropped KeyMeta: %+v", payload)
	}
	if payload["value"] != "0" || payload["reason"] != "account_debt" {
		t.Fatalf("activation dropped explicit admission state: %+v", payload)
	}
	if payload["decision_id"] != decision.ID || int64(payload["decision_version"].(float64)) != decision.Version {
		t.Fatalf("activation dropped Billing decision identity: %+v", payload)
	}
	if row.NotifyChannel != ChannelAdmissionUpdate || len(row.NotifyMessage) == 0 {
		t.Fatalf("activation must invalidate admission cache: %+v", row)
	}
}

func TestBuildActivateKeyRequiresActiveMeta(t *testing.T) {
	meta := &types.KeyMeta{
		KeyID:    uuid.NewString(),
		KeyHash:  string(validHash()),
		Revision: 1700000000000000,
		Status:   "suspended",
	}
	decision := types.KeyAdmissionDecision{ID: uuid.NewString(), Version: 42}
	if _, err := buildActivateKeyRows(meta, true, "", decision, 78); err == nil {
		t.Fatal("atomic activation accepted a non-active KeyMeta")
	}
}

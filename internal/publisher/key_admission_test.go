package publisher

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/tokenplatform/publisher/internal/outbox"
	"github.com/tokenplatform/publisher/internal/types"
)

func TestBuildVersionedKeyAdmissionRowIsAtomicCASAndInvalidation(t *testing.T) {
	id := validKeyID()
	decision := types.KeyAdmissionDecision{ID: uuid.NewString(), Version: 17}
	rows := buildKeyAdmissionRows(id, false, "account_debt", false, decision, 101)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want one atomic CAS+notify row", len(rows))
	}
	row := rows[0]
	if row.Op != outbox.OpKeyBalanceCAS {
		t.Fatalf("op=%s want %s", row.Op, outbox.OpKeyBalanceCAS)
	}
	if row.NotifyChannel != ChannelAdmissionUpdate {
		t.Fatalf("notify channel=%q want %q", row.NotifyChannel, ChannelAdmissionUpdate)
	}
	var payload map[string]any
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["decision_id"] != decision.ID || int64(payload["decision_version"].(float64)) != decision.Version {
		t.Fatalf("decision was dropped: %+v", payload)
	}
	if int64(payload["publisher_revision"].(float64)) != 101 {
		t.Fatalf("publisher revision was not kept independent: %+v", payload)
	}
	if payload["deleted"] != false {
		t.Fatalf("set row marked deleted: %+v", payload)
	}
	var notify map[string]any
	if err := json.Unmarshal(row.NotifyMessage, &notify); err != nil {
		t.Fatalf("decode notify: %v", err)
	}
	if notify["type"] != "key_admission_update" || notify["key_id"] != string(id) || notify["allowed"] != false {
		t.Fatalf("unexpected invalidation payload: %+v", notify)
	}
}

func TestBuildVersionedKeyAdmissionDeleteIsCASTombstone(t *testing.T) {
	id := validKeyID()
	decision := types.KeyAdmissionDecision{ID: uuid.NewString(), Version: 22}
	rows := buildKeyAdmissionRows(id, false, "", true, decision, 102)
	if len(rows) != 1 || rows[0].Op != outbox.OpKeyBalanceCAS {
		t.Fatalf("delete must be one CAS row: %+v", rows)
	}
	var payload map[string]any
	if err := json.Unmarshal(rows[0].Payload, &payload); err != nil {
		t.Fatalf("decode delete payload: %v", err)
	}
	if payload["deleted"] != true || int64(payload["decision_version"].(float64)) != 22 {
		t.Fatalf("delete lost tombstone/version: %+v", payload)
	}
}

func TestVersionedAdmissionBypassesLocalStateDedupe(t *testing.T) {
	decision := types.KeyAdmissionDecision{ID: uuid.NewString(), Version: 2}
	if balanceDedupeEligible(decision) {
		t.Fatal("versioned decisions must reach Redis CAS even when state/reason are unchanged")
	}
	if !balanceDedupeEligible(types.KeyAdmissionDecision{Legacy: true}) {
		t.Fatal("unversioned legacy balance writes should retain the optimization")
	}
}

func TestLegacyAdmissionInvalidationUsesPositivePublisherRevision(t *testing.T) {
	rows := buildKeyAdmissionRows(validKeyID(), true, "", false, types.KeyAdmissionDecision{Legacy: true}, 321)
	var notify map[string]any
	if err := json.Unmarshal(rows[0].NotifyMessage, &notify); err != nil {
		t.Fatalf("decode notify: %v", err)
	}
	if got := int64(notify["decision_version"].(float64)); got != 321 {
		t.Fatalf("legacy notification decision_version=%d want positive publisher revision", got)
	}
}

func TestLegacyBulkReasonOnlyAppliesToDeniedItems(t *testing.T) {
	items := mapLegacyBalanceItems([]types.KeyBalanceItem{
		{KeyID: validKeyID(), HasBalance: false},
		{KeyID: validKeyID(), HasBalance: true},
	}, "account_debt")
	if items[0].Reason != "account_debt" || items[1].Reason != "" {
		t.Fatalf("legacy bulk reasons=%+v", items)
	}
}

func TestAdmissionReasonsIncludeReconciliationHolds(t *testing.T) {
	for _, reason := range []string{"account_debt", "settlement_risk_limit_exceeded", "billing_review"} {
		if err := validateBalanceReason(false, reason); err != nil {
			t.Fatalf("reason %q rejected: %v", reason, err)
		}
	}
}

func TestSummarizeAdmissionDeliveryDistinguishesTerminalOutcomesAndFailures(t *testing.T) {
	batchID := types.BatchID(uuid.New())
	cases := []struct {
		name string
		rows []outbox.OutboxRow
		want AdmissionDeliveryStatus
	}{
		{
			name: "pending after 202",
			rows: []outbox.OutboxRow{{BatchID: batchID, Status: outbox.StatusPending}},
			want: AdmissionDeliveryPending,
		},
		{
			name: "transient redis failure remains pending",
			rows: []outbox.OutboxRow{{BatchID: batchID, Status: outbox.StatusFailed, LastError: "redis unavailable"}},
			want: AdmissionDeliveryPending,
		},
		{
			name: "superseded",
			rows: []outbox.OutboxRow{{BatchID: batchID, Status: outbox.StatusDelivered, DeliveryOutcome: outbox.DeliveryOutcomeSuperseded}},
			want: AdmissionDeliverySuperseded,
		},
		{
			name: "deterministic conflict",
			rows: []outbox.OutboxRow{{BatchID: batchID, Status: outbox.StatusDelivered, DeliveryOutcome: outbox.DeliveryOutcomeConflict}},
			want: AdmissionDeliveryConflict,
		},
		{
			name: "historical delivered row without outcome is unverified",
			rows: []outbox.OutboxRow{{BatchID: batchID, Status: outbox.StatusDelivered}},
			want: AdmissionDeliveryUnverified,
		},
		{
			name: "dead letter",
			rows: []outbox.OutboxRow{{BatchID: batchID, Status: outbox.StatusDeadLetter, LastError: "redis unavailable"}},
			want: AdmissionDeliveryDeadLetter,
		},
		{
			name: "mixed applied and superseded is not reported fully applied",
			rows: []outbox.OutboxRow{
				{BatchID: batchID, Status: outbox.StatusDelivered, DeliveryOutcome: outbox.DeliveryOutcomeApplied},
				{BatchID: batchID, Status: outbox.StatusDelivered, DeliveryOutcome: outbox.DeliveryOutcomeSuperseded},
			},
			want: AdmissionDeliveryMixed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := summarizeAdmissionDelivery(batchID, tc.rows)
			if err != nil {
				t.Fatalf("summarize: %v", err)
			}
			if got.Status != tc.want {
				t.Fatalf("status=%q want %q (receipt=%+v)", got.Status, tc.want, got)
			}
		})
	}
}

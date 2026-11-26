package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/types"
)

func TestKeyPoolConfigRequestPreservesProviderAccountRef(t *testing.T) {
	var req KeyPoolConfigRequest
	if err := json.Unmarshal([]byte(`{
		"meta":{"provider_id":"zhipu","strategy":"priority"},
		"entries":[{
			"key_id":"key-primary",
			"provider_account_ref":"pa_550e8400-e29b-41d4-a716-446655440000",
			"ciphertext":"vault://providers/zhipu/key-primary",
			"priority":1,
			"weight":100,
			"rpm_limit":500,
			"tpm_limit":100000,
			"status":"enabled"
		}]
	}`), &req); err != nil {
		t.Fatalf("decode KeyPoolConfigRequest: %v", err)
	}
	pool := req.ToKeyPoolConfig()
	if pool == nil || len(pool.Entries) != 1 || pool.Entries[0].ProviderAccountRef != "pa_550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("whole-pool admin request dropped provider_account_ref: %+v", pool)
	}
}

func TestKeyMetaRequest_ToKeyMetaPassesDedicatedUpstream(t *testing.T) {
	req := &KeyMetaRequest{
		KeyID:     "8f0c57d8-77b5-4a83-a89f-26ebc356d910",
		KeyHash:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Status:    "active",
		ExpiresAt: 1893456000,
		DedicatedUpstream: &types.DedicatedUpstreamCredential{
			UpstreamKeyID:      "coding-plan-key-001",
			ProviderAccountRef: "pa_550e8400-e29b-41d4-a716-446655440030",
			Ciphertext:         "kms:projects/demo/locations/global/keyRings/ring/cryptoKeys/key:ciphertext",
			CiphertextVersion:  1,
			CiphertextSHA256:   "sha256:dedicated-ciphertext",
			KMSKeyID:           "kms-key-coding",
			Status:             "active",
			ExpiresAt:          1893456000,
			UpdatedAt:          "2026-06-15T00:00:00Z",
			Endpoints: []types.DedicatedUpstreamEndpoint{
				{
					ClientProtocol:   types.ClientProtocolAnthropic,
					ProviderID:       "glm-anthropic",
					ProtocolStrategy: types.DedicatedProtocolChannelPassthrough,
				},
				{
					ClientProtocol:   types.ClientProtocolOpenAI,
					ProviderID:       "glm-openai",
					ProtocolStrategy: types.DedicatedProtocolChannelPassthrough,
				},
			},
		},
	}

	meta := req.ToKeyMeta()
	if meta.DedicatedUpstream == nil {
		t.Fatal("dedicated_upstream was dropped by KeyMetaRequest.ToKeyMeta")
	}
	if meta.DedicatedUpstream.ProviderAccountRef != req.DedicatedUpstream.ProviderAccountRef ||
		meta.DedicatedUpstream.Ciphertext != req.DedicatedUpstream.Ciphertext ||
		len(meta.DedicatedUpstream.Endpoints) != 2 ||
		meta.DedicatedUpstream.Endpoints[0].ProviderID != "glm-anthropic" ||
		meta.DedicatedUpstream.Endpoints[1].ClientProtocol != types.ClientProtocolOpenAI {
		t.Fatalf("dedicated upstream mismatch: got %+v want %+v", meta.DedicatedUpstream, req.DedicatedUpstream)
	}
}

func TestProviderInfoRequest_ToProviderInfoPreservesProviderAccountRef(t *testing.T) {
	req := &ProviderInfoRequest{
		ID:                 "pc_entitlement",
		ProviderAccountRef: "pa_550e8400-e29b-41d4-a716-446655440040",
		Name:               "Entitlement provider",
		BaseURL:            "https://example.invalid",
		Status:             "active",
		Visibility:         types.ProviderVisibilityDedicatedOnly,
		CredentialSource:   types.ProviderCredentialSourceProviderEntitlement,
		AllowedRouteKinds:  []string{types.ProviderRouteKindDedicated},
		OwnerScope:         &types.OwnerScope{EntitlementID: "entitlement_demo_001"},
	}
	info, err := req.ToProviderInfo()
	if err != nil {
		t.Fatalf("ToProviderInfo: %v", err)
	}
	if info.ProviderAccountRef != req.ProviderAccountRef ||
		info.CredentialSource != types.ProviderCredentialSourceProviderEntitlement ||
		len(info.AllowedRouteKinds) != 1 ||
		info.OwnerScope == nil || info.OwnerScope.EntitlementID != "entitlement_demo_001" {
		t.Fatalf("provider entitlement fields lost: got=%+v request=%+v", info, req)
	}
}

// ADR-0015 S5b: the product/route loss-strategy switch (auto /
// passthrough_only) is retired from the contract. The admin API decode path
// (decodeJSON) is intentionally lenient, so a stale client that still sends
// the retired field is silently ignored — the field must never survive onto
// the published wire payload.
func TestRouteConfigRequest_IgnoresRetiredProtocolStrategyOverride(t *testing.T) {
	payload := `{
		"model_id": "glm-5.1",
		"providers": [{
			"provider_id": "pc_openai",
			"upstream_model": "glm-5.1",
			"priority": 10,
			"weight": 100,
			"status": "active",
			"protocol_strategy_override": "auto"
		}]
	}`
	var req RouteConfigRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("lenient admin decode must tolerate retired field: %v", err)
	}

	route := req.ToRouteConfig()
	raw, err := json.Marshal(route)
	if err != nil {
		t.Fatalf("marshal route config: %v", err)
	}
	if strings.Contains(string(raw), "protocol_strategy") {
		t.Fatalf("retired protocol_strategy_override leaked onto wire: %s", raw)
	}
}

func TestProductRuntimePolicyRequest_IgnoresRetiredProtocolStrategy(t *testing.T) {
	payload := `{
		"product_id": "prod-coding",
		"status": "active",
		"credential_source": "keypool",
		"model_scope_mode": "plan_configured",
		"fallback_policy": "within_product",
		"billing_mode": "token_plan_quota",
		"protocol_strategy": "auto",
		"allow_high_loss_bridge_when_no_native": true,
		"allow_bridge_on_native_failure": true
	}`
	var req ProductRuntimePolicyRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("lenient admin decode must tolerate retired field: %v", err)
	}

	policy := req.ToProductRuntimePolicy()
	if !policy.AllowHighLossBridgeWhenNoNative || !policy.AllowBridgeOnNativeFailure {
		t.Fatalf("allow_high_loss_bridge_* fields must survive: %+v", policy)
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("marshal product policy: %v", err)
	}
	if strings.Contains(string(raw), `"protocol_strategy"`) {
		t.Fatalf("retired protocol_strategy leaked onto wire: %s", raw)
	}
}

func TestProviderInfoRequest_ToProviderInfoRejectsDuplicateProtocolBindingKey(t *testing.T) {
	req := &ProviderInfoRequest{
		ID:      "pc_manual",
		Name:    "manual provider",
		BaseURL: "https://api.example.com",
		Status:  "active",
		ProtocolBindings: []types.ProtocolBinding{
			{
				Name:     "manual-openai-chat-a",
				Priority: 1,
				Accepts: []types.ProtocolBindingAccept{{
					Protocol:        "openai",
					Capabilities:    []string{"chat"},
					RequestContract: "chat_completions",
				}},
				OutboundProtocol: "openai",
				AuthScheme:       "bearer",
			},
			{
				Name:     "manual-openai-chat-b",
				Priority: 100,
				Accepts: []types.ProtocolBindingAccept{{
					Protocol:        "openai",
					Capabilities:    []string{"chat"},
					RequestContract: "chat_completions",
				}},
				OutboundProtocol: "openai",
				AuthScheme:       "bearer",
			},
		},
	}

	_, err := req.ToProviderInfo()
	if err == nil {
		t.Fatal("ToProviderInfo succeeded, want duplicate protocol binding key error")
	}
	msg := err.Error()
	for _, want := range []string{"manual-openai-chat-a", "manual-openai-chat-b", "protocol=openai", "capability=chat"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", msg, want)
		}
	}
}

// G7 Phase B: the admin platform-config entry point (PUT
// /admin/api/v1/platform-config) must carry ingress_product_assertions
// through to the published PlatformConfig — same input pattern as every
// other platform field, no new entry point.
func TestPlatformConfigRequest_ToPlatformConfigCarriesIngressAssertions(t *testing.T) {
	body := `{
		"platform_rpm": 60000,
		"ingress_product_assertions": [
			{"pattern": "POST /api/coding/paas/v4/chat/completions",
			 "allowed_products": ["pc_glm_coding"],
			 "mode": "warn"},
			{"pattern": "POST /v1/messages",
			 "allowed_products": ["pc_payg", "pc_glm_coding"],
			 "mode": "enforce"}
		]
	}`
	var req PlatformConfigRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decode admin body: %v", err)
	}
	cfg := req.ToPlatformConfig()
	want := []types.IngressProductAssertion{
		{Pattern: "POST /api/coding/paas/v4/chat/completions", AllowedProducts: []string{"pc_glm_coding"}, Mode: "warn"},
		{Pattern: "POST /v1/messages", AllowedProducts: []string{"pc_payg", "pc_glm_coding"}, Mode: "enforce"},
	}
	if !reflect.DeepEqual(cfg.IngressProductAssertions, want) {
		t.Fatalf("assertions not carried through admin request.\n got: %#v\nwant: %#v", cfg.IngressProductAssertions, want)
	}
}

func TestPlatformConfigRequest_ToPlatformConfigWithoutAssertionsStaysNil(t *testing.T) {
	var req PlatformConfigRequest
	if err := json.Unmarshal([]byte(`{"platform_rpm": 60000}`), &req); err != nil {
		t.Fatalf("decode admin body: %v", err)
	}
	if cfg := req.ToPlatformConfig(); cfg.IngressProductAssertions != nil {
		t.Fatalf("unconfigured assertions must stay nil (default laziness), got %#v", cfg.IngressProductAssertions)
	}
}

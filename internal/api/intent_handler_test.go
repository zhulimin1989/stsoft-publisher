package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/tokenplatform/publisher/internal/publisher"
	"github.com/tokenplatform/publisher/internal/types"
)

func TestAdminIntentEndpointStrictDecodeAndPublishesCompiledRuntimeConfig(t *testing.T) {
	var providerIDs []string
	var firstProvider *types.ProviderInfo
	var keypoolIDs []string
	var routeID string
	var productID string

	pub := &stubPublisher{
		publishProviderFunc: func(_ context.Context, id string, info *types.ProviderInfo) (types.BatchID, error) {
			providerIDs = append(providerIDs, id)
			if id == "pc_101" {
				firstProvider = info
			}
			return uuid.New(), nil
		},
		publishKeyPoolFunc: func(_ context.Context, id string, pool *types.KeyPoolConfig) (types.BatchID, error) {
			keypoolIDs = append(keypoolIDs, id)
			if len(pool.Entries) != 1 || pool.Entries[0].Ciphertext == "" {
				t.Fatalf("unexpected keypool: %#v", pool)
			}
			return uuid.New(), nil
		},
		publishModelRouteFunc: func(_ context.Context, id string, route *types.RouteConfig) (types.BatchID, error) {
			routeID = id
			if len(route.Providers) != 2 {
				t.Fatalf("unexpected route: %#v", route)
			}
			return uuid.New(), nil
		},
		publishModelCatalogFunc: func(_ context.Context, id string, entry *types.ModelCatalogEntry) (types.BatchID, error) {
			if id != "kimi-k2.6" || entry.ModelID != "kimi-k2.6" {
				t.Fatalf("unexpected catalog publish id=%q entry=%#v", id, entry)
			}
			return uuid.New(), nil
		},
		publishProductRuntimePolicyFunc: func(_ context.Context, id string, policy *types.ProductRuntimePolicy) (types.BatchID, error) {
			productID = id
			if len(policy.AllowedProviderIDs) != 2 || policy.AllowedProviderIDs[0] != "pc_101" || policy.AllowedProviderIDs[1] != "pc_102" {
				t.Fatalf("route_graph did not compile fallback provider allowlist: %#v", policy)
			}
			if policy.RouteGraph == nil || len(policy.RouteGraph.ProductChannels) != 2 {
				t.Fatalf("route_graph was not published in product runtime policy: %#v", policy)
			}
			return uuid.New(), nil
		},
	}
	h := newAdminBoundaryTestServer(t, pub)

	w := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", validIntentBody(), "intent-test")
	if w.Code != http.StatusAccepted {
		t.Fatalf("intent endpoint status=%d body=%s", w.Code, w.Body.String())
	}
	for _, want := range []string{"provider_batches", "keypool_batches", "model_route_batches", "product_policy_batches"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("response missing %q: %s", want, w.Body.String())
		}
	}
	if !strings.Contains(w.Body.String(), "shared_credential_group") || !strings.Contains(w.Body.String(), "effective_policies") {
		t.Fatalf("response missing warnings/effective policies: %s", w.Body.String())
	}
	if len(providerIDs) != 2 || providerIDs[0] != "pc_101" || providerIDs[1] != "pc_102" || len(keypoolIDs) != 2 || keypoolIDs[0] != "pc_101" || keypoolIDs[1] != "pc_102" || routeID != "kimi-k2.6" || productID != "prod_coding" {
		t.Fatalf("publish ids providers=%v keypools=%v route=%q product=%q", providerIDs, keypoolIDs, routeID, productID)
	}
	if firstProvider == nil || !containsProtocolBridge(firstProvider.ProtocolBindings, "anthropic_to_openai_chat") {
		t.Fatalf("provider publish lost bridge: %#v", firstProvider)
	}

	w = adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", strings.Replace(validIntentBody(), `"schema_version": 2,`, `"schema_version": 2, "unexpected": true,`, 1), "intent-test-unknown")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown intent field status=%d want 400 body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unknown field") {
		t.Fatalf("unknown field response=%s", w.Body.String())
	}

	w = adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", strings.Replace(validIntentBody(), `"schema_version": 2`, `"schema_version": 99`, 1), "intent-test-schema")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unsupported schema version status=%d want 400 body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unsupported intent schema_version") {
		t.Fatalf("schema version response=%s", w.Body.String())
	}
}

// ADR-0015 S5b: product/route protocol_strategy (auto / passthrough_only) is
// retired from the intent contract. The intent endpoint decodes strictly
// (decodeStrictJSON → DisallowUnknownFields), so a stale client still sending
// the retired fields must fail fast with 400 "unknown field" instead of the
// dead switch being silently accepted.
func TestAdminIntentEndpointRejectsRetiredProtocolStrategyFields(t *testing.T) {
	pub := &stubPublisher{}
	h := newAdminBoundaryTestServer(t, pub)

	productPolicyBody := strings.Replace(validIntentBody(),
		`"billing_mode": "token_plan_quota",`,
		`"billing_mode": "token_plan_quota", "protocol_strategy": "auto",`, 1)
	w := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", productPolicyBody, "intent-test-retired-product-strategy")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("retired product protocol_strategy status=%d want 400 body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unknown field") {
		t.Fatalf("retired product protocol_strategy response=%s", w.Body.String())
	}

	routeOverrideBody := strings.Replace(validIntentBody(),
		`"upstream_model": "kimi-k2.6",
					"priority": 1,`,
		`"upstream_model": "kimi-k2.6",
					"protocol_strategy_override": "auto",
					"priority": 1,`, 1)
	if routeOverrideBody == validIntentBody() {
		t.Fatal("route override fixture injection did not apply")
	}
	w = adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", routeOverrideBody, "intent-test-retired-route-override")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("retired route protocol_strategy_override status=%d want 400 body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "unknown field") {
		t.Fatalf("retired route protocol_strategy_override response=%s", w.Body.String())
	}
}

func TestAdminIntentEndpointAcceptsClientExposureWithoutCapabilities(t *testing.T) {
	body := `{
		"schema_version": 2,
		"provider_endpoints": [{
			"id": "openai-prod",
			"provider_profile": "openai_compatible",
			"base_url": "https://api.openai.example",
			"status": "active",
			"path_prefix": "/v1"
		}],
		"provider_credentials": [{
			"provider_id": "openai-prod",
			"key_ref": "secret://providers/openai-prod/key-a",
			"provider_account_ref": "pa_550e8400-e29b-41d4-a716-446655440020",
			"status": "enabled",
			"allowed_models": ["gpt-4o-mini"]
		}],
		"client_exposures": [{
			"provider_id": "openai-prod",
			"served_inbound_protocols": ["openai"],
			"token_count_strategy": "unsupported"
		}],
		"model_routes": [{
			"logical_model": "gpt-4o-mini",
			"routes": [{
				"provider_id": "openai-prod",
				"upstream_model": "gpt-4o-mini",
				"status": "active"
			}]
		}]
	}`
	var captured publisher.IntentPublishRequest
	pub := &stubPublisher{
		publishIntentFunc: func(_ context.Context, req publisher.IntentPublishRequest) (publisher.IntentPublishResult, error) {
			captured = req
			return publisher.IntentPublishResult{
				ProviderBatches:     []types.BatchID{uuid.New()},
				KeyPoolBatches:      []types.BatchID{uuid.New()},
				ModelCatalogBatches: []types.BatchID{uuid.New()},
				ModelRouteBatches:   []types.BatchID{uuid.New()},
			}, nil
		},
	}
	h := newAdminBoundaryTestServer(t, pub)

	w := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", body, "intent-no-client-capabilities")
	if w.Code != http.StatusAccepted {
		t.Fatalf("intent endpoint status=%d body=%s", w.Code, w.Body.String())
	}
	if len(captured.Providers) != 1 || len(captured.KeyPools) != 1 || len(captured.ModelCatalog) != 1 || len(captured.Routes) != 1 {
		t.Fatalf("compiled runtime shape providers=%d keypools=%d catalog=%d routes=%d",
			len(captured.Providers), len(captured.KeyPools), len(captured.ModelCatalog), len(captured.Routes))
	}
	provider := captured.Providers[0]
	binding, ok := protocolBindingByName(provider.ProtocolBindings, "openai-chat")
	if !ok {
		t.Fatalf("openai-chat binding missing from published ProviderInfo: %#v", provider.ProtocolBindings)
	}
	if binding.Bridge != "" || binding.TranslatorKey != "passthrough" {
		t.Fatalf("same-wire tools-bearing chat request would not use passthrough binding: %#v", binding)
	}
	if !hasCapabilityProfile(binding, "openai.chat_completions.full") {
		t.Fatalf("same-wire chat binding lost full chat-completions profile: %#v", binding)
	}
	if len(binding.UnsupportedParams) != 0 {
		t.Fatalf("same-wire binding should not publish a client_exposure-derived param denylist: %#v", binding.UnsupportedParams)
	}
	if len(captured.ModelCatalog[0].Capabilities) != 1 || captured.ModelCatalog[0].Capabilities[0] != "chat" {
		t.Fatalf("model_route empty capabilities should retain chat fallback: %#v", captured.ModelCatalog[0])
	}
	if len(captured.Routes[0].Providers[0].Capabilities) != 0 {
		t.Fatalf("route target should not inherit client_exposure capabilities: %#v", captured.Routes[0].Providers[0])
	}

	t.Logf("intent_without_client_exposure_capabilities=%s", body)
	t.Logf("published_runtime_evidence=%s", mustJSON(t, map[string]any{
		"provider_id":              provider.ID,
		"default_protocol_binding": provider.DefaultProtocolBinding,
		"same_wire_tools_request": map[string]any{
			"protocol":         "openai",
			"request_contract": "chat_completions",
			"payload_fields":   []string{"model", "messages", "tools", "tool_choice"},
			"decision":         "publisher emitted passthrough binding; no client_exposure capability gate",
		},
		"binding":       binding,
		"model_catalog": captured.ModelCatalog[0],
		"route":         captured.Routes[0],
	}))
}

func TestAdminIntentEndpointPublishesProductPolicyDeletes(t *testing.T) {
	body := `{
		"schema_version": 2,
		"product_policy_deletes": [{
			"product_id": "prod_coding",
			"revision": 1700000000000456
		}]
	}`
	var captured publisher.IntentPublishRequest
	pub := &stubPublisher{
		publishIntentFunc: func(_ context.Context, req publisher.IntentPublishRequest) (publisher.IntentPublishResult, error) {
			captured = req
			return publisher.IntentPublishResult{
				ProductPolicyDeleteBatches: []types.BatchID{uuid.New()},
			}, nil
		},
	}
	h := newAdminBoundaryTestServer(t, pub)

	w := adminPostWithIdempotencyKey(t, h, "/admin/api/v1/providers:from-intent", body, "intent-delete-policy")
	if w.Code != http.StatusAccepted {
		t.Fatalf("intent endpoint status=%d body=%s", w.Code, w.Body.String())
	}
	if len(captured.ProductPolicyDeletes) != 1 {
		t.Fatalf("captured deletes=%v want one", captured.ProductPolicyDeletes)
	}
	got := captured.ProductPolicyDeletes[0]
	if got.ProductID != "prod_coding" || got.Revision != 1700000000000456 {
		t.Fatalf("captured delete=%+v", got)
	}
	if !strings.Contains(w.Body.String(), "product_policy_delete_batches") {
		t.Fatalf("response missing product_policy_delete_batches: %s", w.Body.String())
	}
}

func TestAdminIntentEndpointRequiresIdempotencyKey(t *testing.T) {
	pub := &stubPublisher{
		publishProviderFunc: func(context.Context, string, *types.ProviderInfo) (types.BatchID, error) {
			t.Fatal("from-intent must reject missing Idempotency-Key before publishing")
			return uuid.Nil, nil
		},
	}
	h := newAdminBoundaryTestServer(t, pub)

	w := adminPost(t, h, "/admin/api/v1/providers:from-intent", validIntentBody())
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key status=%d want 400 body=%s", w.Code, w.Body.String())
	}
	assertInvalidArgumentEnvelope(t, w, "Idempotency-Key")
}

func TestAdminProviderFinishedConfigEndpointKeepsLegacyUnknownFieldTolerance(t *testing.T) {
	pub := &stubPublisher{}
	h := newAdminBoundaryTestServer(t, pub)
	w := adminPut(t, h, "/admin/api/v1/providers/openai", `{
		"name": "openai",
		"base_url": "https://api.openai.example",
		"status": "active",
		"protocol_bindings": [{
			"name": "openai-chat",
			"accepts": [{"protocol": "openai", "capabilities": ["chat"], "request_contract": "chat_completions"}],
			"outbound_protocol": "openai",
			"auth_scheme": "bearer"
		}],
		"legacy_extra_field": true
	}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("legacy endpoint unknown-field tolerance changed: status=%d body=%s", w.Code, w.Body.String())
	}
}

func validIntentBody() string {
	return `{
		"schema_version": 2,
		"provider_endpoints": [
			{
				"id": "pc_101",
				"provider_profile": "openai_compatible",
				"base_url": "https://api.moonshot.example",
				"region": "cn",
				"status": "active",
				"name": "Kimi coding channel",
				"code": "kimi-main",
				"key_strategy": "priority",
				"path_prefix": "/v1"
			},
			{
				"id": "pc_102",
				"provider_profile": "openai_compatible",
				"base_url": "https://api.moonshot.example",
				"region": "cn",
				"status": "active",
				"name": "Kimi chat channel",
				"code": "kimi-main",
				"key_strategy": "priority",
				"path_prefix": "/v1"
			}
		],
		"provider_credentials": [
			{
				"provider_id": "pc_101",
				"key_ref": "secret://channels/kimi-main/key-a",
				"key_id": "key-a",
				"provider_account_ref": "pa_550e8400-e29b-41d4-a716-446655440000",
				"status": "enabled",
				"allowed_models": ["kimi-k2.6"],
				"rpm_limit": 500,
				"tpm_limit": 100000,
				"priority": 1,
				"weight": 100
			},
			{
				"provider_id": "pc_102",
				"key_ref": "secret://channels/kimi-main/key-a",
				"key_id": "key-a",
				"provider_account_ref": "pa_550e8400-e29b-41d4-a716-446655440000",
				"status": "enabled",
				"allowed_models": ["kimi-k2.6"],
				"rpm_limit": 200,
				"tpm_limit": 50000,
				"priority": 1,
				"weight": 100
			}
		],
		"client_exposures": [
			{
				"provider_id": "pc_101",
				"served_inbound_protocols": ["anthropic", "responses"],
				"capabilities": ["chat"],
				"token_count_strategy": "unsupported",
				"protocol_path_prefix": "/v1"
			},
			{
				"provider_id": "pc_102",
				"served_inbound_protocols": ["openai"],
				"capabilities": ["chat"],
				"token_count_strategy": "unsupported",
				"protocol_path_prefix": "/v1"
			}
		],
		"model_routes": [{
			"logical_model": "kimi-k2.6",
			"capabilities": ["chat"],
			"context_window": 128000,
			"max_output": 16384,
			"owned_by": "kimi-main",
			"provider_strategy": "priority",
			"routes": [
				{
					"provider_id": "pc_101",
					"upstream_model": "kimi-k2.6",
					"priority": 1,
					"weight": 100,
					"status": "active"
				},
				{
					"provider_id": "pc_102",
					"upstream_model": "kimi-k2.6",
					"priority": 2,
					"weight": 50,
					"status": "active"
				}
			]
		}],
		"product_policies": [{
			"product_id": "prod_coding",
			"status": "active",
			"credential_source": "dedicated_upstream",
			"allowed_models": ["kimi-k2.6"],
			"model_scope_mode": "product_allowlist",
			"fallback_policy": "within_product",
			"billing_mode": "token_plan_quota",
			"route_strategy": "priority",
			"allowed_provider_ids": ["pc_101"],
			"route_graph": {
				"schema_version": 1,
				"default_target": {"target_type": "GROUP", "target_id": "primary"},
				"groups": [{
					"id": "primary",
					"code": "primary",
					"status": "active",
					"strategy": "weighted_failover",
					"members": [
						{
							"product_channel_id": "pc_101",
							"channel_id": "kimi-main",
							"provider_id": "pc_101",
							"priority": 1,
							"weight": 100,
							"role": "primary",
							"max_rpm": 9999,
							"max_tpm": 888888
						},
						{
							"product_channel_id": "pc_102",
							"channel_id": "kimi-main",
							"provider_id": "pc_102",
							"priority": 2,
							"weight": 50,
							"role": "fallback"
						}
					]
				}],
				"product_channels": [
					{
						"id": "pc_101",
						"channel_id": "kimi-main",
						"provider_id": "pc_101",
						"allowed_models": ["kimi-k2.6"],
						"denied_models": [],
						"priority": 1,
						"weight": 100,
						"max_rpm": 500,
						"max_tpm": 100000,
						"status": "active"
					},
					{
						"id": "pc_102",
						"channel_id": "kimi-main",
						"provider_id": "pc_102",
						"allowed_models": ["kimi-k2.6"],
						"denied_models": [],
						"priority": 2,
						"weight": 50,
						"max_rpm": 200,
						"max_tpm": 50000,
						"status": "testing"
					}
				],
				"assignments": [
					{
						"id": "asg_vip",
						"product_id": "prod_coding",
						"subject_type": "USER_SEGMENT",
						"subject_id": "seg_vip",
						"model_pattern": "kimi-*",
						"client_compatibility": "claude_code",
						"target_type": "GROUP",
						"target_id": "primary",
						"mode": "PREFER",
						"priority": 10
					},
					{
						"id": "asg_user",
						"product_id": "prod_coding",
						"subject_type": "USER",
						"subject_id": "usr_123",
						"target_type": "PRODUCT_CHANNEL",
						"target_id": "pc_101",
						"mode": "ONLY",
						"priority": 20
					}
				]
			}
		}]
	}`
}

func containsProtocolBridge(bindings []types.ProtocolBinding, bridge string) bool {
	for _, binding := range bindings {
		if binding.Bridge == bridge {
			return true
		}
	}
	return false
}

func protocolBindingByName(bindings []types.ProtocolBinding, name string) (types.ProtocolBinding, bool) {
	for _, binding := range bindings {
		if binding.Name == name {
			return binding, true
		}
	}
	return types.ProtocolBinding{}, false
}

func hasCapabilityProfile(binding types.ProtocolBinding, profile string) bool {
	for _, value := range binding.CapabilityProfiles {
		if value == profile {
			return true
		}
	}
	return false
}

func adminPostWithIdempotencyKey(t *testing.T, h http.Handler, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func assertInvalidArgumentEnvelope(t *testing.T, w *httptest.ResponseRecorder, messageContains string) {
	t.Helper()
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error envelope: %v body=%s", err, w.Body.String())
	}
	if body.Error.Code != "INVALID_ARGUMENT" {
		t.Fatalf("error.code=%q want INVALID_ARGUMENT body=%s", body.Error.Code, w.Body.String())
	}
	if messageContains != "" && !strings.Contains(body.Error.Message, messageContains) {
		t.Fatalf("error.message=%q does not contain %q", body.Error.Message, messageContains)
	}
}

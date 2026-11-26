package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/types"
)

func TestProviderInfoRequest_RejectsLegacyAPITypeFields(t *testing.T) {
	for _, body := range []string{
		`{"api_type":"openai","status":"active"}`,
		`{"api_types":["openai"],"status":"active"}`,
		`{"api_type":null,"protocol_bindings":[]}`,
		`{"api_types":null,"protocol_bindings":[]}`,
	} {
		t.Run(body, func(t *testing.T) {
			var req ProviderInfoRequest
			err := json.Unmarshal([]byte(body), &req)
			if err == nil {
				t.Fatal("legacy api_type/api_types field must be rejected")
			}
			if !strings.Contains(err.Error(), "protocol_bindings") {
				t.Fatalf("error should point callers to protocol_bindings, got %q", err.Error())
			}
		})
	}
}

// The admin wire body carries protocol_bindings[] + default_protocol_binding;
// the DTO must decode them and ToProviderInfo() must copy them through verbatim.
func TestProviderInfoRequest_DecodesProtocolBindings(t *testing.T) {
	body := []byte(`{
		"id": "zhipu-prod",
		"name": "Zhipu",
		"base_url": "https://open.bigmodel.cn",
		"status": "active",
		"key_strategy": "priority",
		"protocol_bindings": [
			{
				"name": "anthropic-to-openai",
				"priority": 100,
				"accepts": [
					{"protocol": "anthropic", "capabilities": ["chat"], "request_contract": "anthropic_messages"}
				],
				"outbound_protocol": "openai",
				"path_prefix": "/v1",
				"query_template": "api-version={api_version}",
				"auth_scheme": "bearer",
				"translator": "standard_to_openai",
				"bridge": "anthropic_to_openai_chat",
				"compat_api_type": "openai",
				"capability_profiles": ["openai.chat_completions.full"],
				"supported_params": ["tools"],
				"param_constraints": {"tool_choice": ["auto", "none"]},
				"combination_constraints": [
					{"when": {"stream_options.include_usage": "true"}, "requires": {"stream": "true"}}
				],
				"request_param_policy": {
					"temperature": {"category": "sampling", "upstream_behavior": "fixed", "fixed_value": "1", "on_equal": "omit", "on_mismatch": "normalize_with_warning"}
				},
				"field_disposition": {
					"request": {"thinking": "preserve"},
					"response": {"reasoning_details": "preserve_vendor_extra"}
				}
			}
		],
		"default_protocol_binding": "anthropic-to-openai"
	}`)

	var req ProviderInfoRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(req.ProtocolBindings) != 1 {
		t.Fatalf("want 1 binding decoded, got %d", len(req.ProtocolBindings))
	}
	if req.DefaultProtocolBinding != "anthropic-to-openai" {
		t.Fatalf("default_protocol_binding decode mismatch: %q", req.DefaultProtocolBinding)
	}

	info, err := req.ToProviderInfo()
	if err != nil {
		t.Fatalf("ToProviderInfo: %v", err)
	}
	want := []types.ProtocolBinding{
		{
			Name:     "anthropic-to-openai",
			Priority: 100,
			Accepts: []types.ProtocolBindingAccept{
				{Protocol: "anthropic", Capabilities: []string{"chat"}, RequestContract: "anthropic_messages"},
			},
			OutboundProtocol: "openai",
			PathPrefix:       "/v1",
			QueryTemplate:    "api-version={api_version}",
			AuthScheme:       "bearer",
			TranslatorKey:    "standard_to_openai",
			Bridge:           "anthropic_to_openai_chat",
			CompatAPIType:    "openai",
			CapabilityProfiles: []string{
				"openai.chat_completions.full",
			},
			SupportedParams:  []string{"tools"},
			ParamConstraints: map[string][]string{"tool_choice": {"auto", "none"}},
			CombinationConstraints: []types.CombinationConstraint{{
				When:     map[string]string{"stream_options.include_usage": "true"},
				Requires: map[string]string{"stream": "true"},
			}},
			RequestParamPolicy: map[string]types.RequestParamPolicy{
				"temperature": {
					Category:         "sampling",
					UpstreamBehavior: "fixed",
					FixedValue:       "1",
					OnEqual:          "omit",
					OnMismatch:       "normalize_with_warning",
				},
			},
			FieldDisposition: &types.FieldDispositionConfig{
				Request:  map[string]string{"thinking": "preserve"},
				Response: map[string]string{"reasoning_details": "preserve_vendor_extra"},
			},
		},
	}
	if !reflect.DeepEqual(info.ProtocolBindings, want) {
		t.Errorf("ToProviderInfo protocol_bindings mismatch:\n got %+v\nwant %+v", info.ProtocolBindings, want)
	}
	if info.DefaultProtocolBinding != "anthropic-to-openai" {
		t.Errorf("ToProviderInfo default_protocol_binding mismatch: %q", info.DefaultProtocolBinding)
	}
}

func TestRouteConfigRequest_DecodesRouteCapabilities(t *testing.T) {
	body := []byte(`{
		"model_id": "gpt-4o",
		"providers": [
			{
				"provider_id": "openai",
				"upstream_model": "gpt-4o-2024-08-06",
				"priority": 1,
				"weight": 100,
				"status": "active",
				"api_version": "2024-10-22",
				"anthropic_beta": "prompt-caching-2024-07-31",
				"capabilities": ["chat", "tools"],
				"compliance_tags": ["zdr"],
				"request_param_policy": {
					"temperature": {"category": "sampling", "upstream_behavior": "pass_through"}
				}
			}
		],
		"provider_strategy": "fixed_order"
	}`)

	var req RouteConfigRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	route := req.ToRouteConfig()
	if route == nil {
		t.Fatal("ToRouteConfig returned nil")
	}
	if len(route.Providers) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(route.Providers))
	}
	got := route.Providers[0]
	if !reflect.DeepEqual(got.Capabilities, []string{"chat", "tools"}) {
		t.Fatalf("capabilities not passed through: %+v", got.Capabilities)
	}
	if got.APIVersion != "2024-10-22" || got.AnthropicBeta != "prompt-caching-2024-07-31" {
		t.Fatalf("route metadata not passed through: %+v", got)
	}
	if got.RequestParamPolicy["temperature"].UpstreamBehavior != "pass_through" {
		t.Fatalf("request_param_policy not passed through: %+v", got.RequestParamPolicy)
	}
}

func TestProviderInfoRequest_NoProtocolBindingsDoesNotSynthesizeLegacy(t *testing.T) {
	req := &ProviderInfoRequest{ID: "p", Status: "active"}
	info, err := req.ToProviderInfo()
	if err != nil {
		t.Fatalf("ToProviderInfo: %v", err)
	}
	if len(info.ProtocolBindings) != 0 {
		t.Fatalf("must not synthesize protocol_bindings, got %+v", info.ProtocolBindings)
	}
}

func TestProviderInfoRequest_CompilesClientCompatibilityBindings(t *testing.T) {
	req := &ProviderInfoRequest{
		ID:                  "openai",
		Status:              "active",
		OwnedBy:             "zhipu",
		ProviderProfile:     "openai_compatible",
		ClientCompatibility: []string{"all"},
	}
	info, err := req.ToProviderInfo()
	if err != nil {
		t.Fatalf("ToProviderInfo: %v", err)
	}
	if info.DefaultProtocolBinding != "openai-chat" {
		t.Fatalf("default binding=%q", info.DefaultProtocolBinding)
	}
	if info.OwnedBy != "zhipu" || info.ProviderProfile != types.ProviderProfileOpenAICompatible {
		t.Fatalf("provider planner facts mismatch: owned_by=%q provider_profile=%q", info.OwnedBy, info.ProviderProfile)
	}
	got := map[string]bool{}
	bridges := map[string]string{}
	for _, binding := range info.ProtocolBindings {
		got[binding.Name] = true
		bridges[binding.Name] = binding.Bridge
	}
	for _, want := range []string{
		"openai-chat",
		"openai-embeddings",
		"anthropic-messages-to-openai",
		"responses-to-openai-chat",
	} {
		if !got[want] {
			t.Fatalf("compiled bindings missing %q: %+v", want, info.ProtocolBindings)
		}
	}
	if got["gemini-generate-to-openai"] {
		t.Fatalf("compiled bindings must not include gemini-generate-to-openai: %+v", info.ProtocolBindings)
	}
	if bridges["anthropic-messages-to-openai"] != "anthropic_to_openai_chat" {
		t.Fatalf("anthropic bridge=%q", bridges["anthropic-messages-to-openai"])
	}
	if bridges["responses-to-openai-chat"] != "responses_to_chat" {
		t.Fatalf("responses bridge=%q", bridges["responses-to-openai-chat"])
	}
	wantInbound, wantCompatibilities := types.DeriveCompatibilityView(info.ProtocolBindings)
	if len(wantInbound) < 2 {
		t.Fatalf("fixture must cover multiple inbound protocols, got %v", wantInbound)
	}
	if len(info.InboundProtocols) == 0 {
		t.Fatal("ToProviderInfo did not derive inbound_protocols")
	}
	if len(info.ClientCompatibilities) == 0 {
		t.Fatal("ToProviderInfo did not derive client_compatibilities")
	}
	if !reflect.DeepEqual(info.InboundProtocols, wantInbound) {
		t.Fatalf("inbound_protocols=%v want %v", info.InboundProtocols, wantInbound)
	}
	if !reflect.DeepEqual(info.ClientCompatibilities, wantCompatibilities) {
		t.Fatalf("client_compatibilities=%v want %v", info.ClientCompatibilities, wantCompatibilities)
	}
}

func TestProviderInfoRequest_CompilesAnthropicNativeBindings(t *testing.T) {
	req := &ProviderInfoRequest{
		ID:                  "anthropic",
		Status:              "active",
		ProviderProfile:     "anthropic_native",
		ClientCompatibility: []string{"claude_code"},
		TokenCountStrategy:  "native",
		PathPrefix:          "/v1",
	}
	info, err := req.ToProviderInfo()
	if err != nil {
		t.Fatalf("ToProviderInfo: %v", err)
	}
	if info.DefaultProtocolBinding != "anthropic-messages" {
		t.Fatalf("default binding=%q", info.DefaultProtocolBinding)
	}
	got := map[string]types.ProtocolBinding{}
	for _, binding := range info.ProtocolBindings {
		got[binding.Name] = binding
	}
	for _, want := range []string{"anthropic-messages", "anthropic-count-tokens"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("compiled bindings missing %q: %+v", want, info.ProtocolBindings)
		}
	}
	if got["anthropic-count-tokens"].Accepts[0].RequestContract != "anthropic_count_tokens" {
		t.Fatalf("count_tokens contract mismatch: %+v", got["anthropic-count-tokens"])
	}
}

func TestProviderInfoRequest_RejectsUnknownClientCompatibility(t *testing.T) {
	req := &ProviderInfoRequest{ProviderProfile: "openai_compatible", ClientCompatibility: []string{"unknown"}}
	if _, err := req.ToProviderInfo(); err == nil || !strings.Contains(err.Error(), "client_compatibility unsupported") {
		t.Fatalf("unknown client must be rejected, got %v", err)
	}
}

func TestModelCatalogEntryRequest_CopiesRequestParamPolicy(t *testing.T) {
	req := &ModelCatalogEntryRequest{
		ModelID: "moonshot-k2.6",
		RequestParamPolicy: map[string]types.RequestParamPolicy{
			"temperature": {
				Category:         "sampling",
				UpstreamBehavior: "fixed",
				FixedValue:       "0.6",
				OnEqual:          "omit",
				OnMismatch:       "normalize_with_warning",
			},
		},
	}
	entry := req.ToModelCatalogEntry()
	if !reflect.DeepEqual(entry.RequestParamPolicy, req.RequestParamPolicy) {
		t.Fatalf("request_param_policy mismatch:\n got %+v\nwant %+v", entry.RequestParamPolicy, req.RequestParamPolicy)
	}
}

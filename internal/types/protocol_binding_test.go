package types

import (
	"encoding/json"
	"testing"
)

// The protocol_bindings wire shape is the publisher↔gateway provider routing
// contract (design §21.4). JSON tags MUST match gateway model.ProtocolBinding
// exactly once gateway PR0 lands — these tests pin the publisher side.

func TestProtocolBinding_RequiredAndOptionalJSONTags(t *testing.T) {
	raw, err := json.Marshal(ProtocolBinding{
		Name:             "openai-compat-to-glm",
		Accepts:          []ProtocolBindingAccept{{Protocol: "openai", Capabilities: []string{"chat"}}},
		OutboundProtocol: "glm",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, k := range []string{"name", "accepts", "outbound_protocol"} {
		if _, ok := m[k]; !ok {
			t.Errorf("required json key %q missing: %s", k, raw)
		}
	}
	// Optional fields must be omitted when zero so the wire stays minimal.
	for _, k := range []string{
		"priority", "path_prefix", "query_template", "auth_scheme", "translator",
		"bridge", "compat_api_type", "capability_profiles", "supported_features",
		"unsupported_features", "supported_params", "unsupported_params",
		"param_constraints", "feature_constraints", "combination_constraints",
		"request_param_policy", "compatibility_mode", "field_disposition",
		"request_correlation_mode",
	} {
		if _, ok := m[k]; ok {
			t.Errorf("optional json key %q should be omitted when empty: %s", k, raw)
		}
	}
}

func TestProtocolBinding_ExtensionJSONTags(t *testing.T) {
	enabled := true
	raw, err := json.Marshal(ProtocolBinding{
		Name:                   "minimax-openai",
		Accepts:                []ProtocolBindingAccept{{Protocol: "openai", Capabilities: []string{"chat"}, RequestContract: "chat_completions"}},
		OutboundProtocol:       "openai",
		QueryTemplate:          "api-version={api_version}",
		Bridge:                 "anthropic_to_openai_chat",
		CapabilityProfiles:     []string{"openai.chat_completions.full", "minimax.openai_chat.thinking"},
		SupportedFeatures:      []string{"reasoning"},
		UnsupportedParams:      []string{"parallel_tool_calls"},
		ParamConstraints:       map[string][]string{"tool_choice": {"auto", "none"}},
		FeatureConstraints:     map[string]string{"streaming_usage": "requires_stream"},
		CombinationConstraints: []CombinationConstraint{{When: map[string]string{"stream_options.include_usage": "true"}, Requires: map[string]string{"stream": "true"}}},
		RequestParamPolicy: map[string]RequestParamPolicy{
			"temperature": {Category: "sampling", UpstreamBehavior: "fixed", FixedValue: "1", OnEqual: "omit", OnMismatch: "normalize_with_warning"},
		},
		CompatibilityMode: &CompatibilityMode{OpenAIClientDefaults: &enabled},
		FieldDisposition: &FieldDispositionConfig{
			Request:  map[string]string{"thinking": "preserve"},
			Response: map[string]string{"reasoning_details": "preserve_vendor_extra"},
		},
		RequestCorrelationMode: "gateway_header_and_user_agent",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, k := range []string{
		"query_template", "capability_profiles", "supported_features",
		"bridge", "unsupported_params", "param_constraints", "feature_constraints",
		"combination_constraints", "request_param_policy", "compatibility_mode",
		"field_disposition", "request_correlation_mode",
	} {
		if _, ok := m[k]; !ok {
			t.Errorf("json key %q missing: %s", k, raw)
		}
	}
	var out ProtocolBinding
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal binding: %v", err)
	}
	if out.RequestParamPolicy["temperature"].OnMismatch != "normalize_with_warning" {
		t.Fatalf("request_param_policy round-trip mismatch: %+v", out.RequestParamPolicy)
	}
	if out.Bridge != "anthropic_to_openai_chat" {
		t.Fatalf("bridge round-trip mismatch: %q", out.Bridge)
	}
	if out.FieldDisposition.Response["reasoning_details"] != "preserve_vendor_extra" {
		t.Fatalf("field_disposition round-trip mismatch: %+v", out.FieldDisposition)
	}
}

func TestProtocolBindingAccept_JSONTags(t *testing.T) {
	raw, err := json.Marshal(ProtocolBindingAccept{
		Protocol:        "openai",
		Capabilities:    []string{"chat", "embed"},
		RequestContract: "chat_completions",
		Models:          []string{"glm-4.5"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, k := range []string{"protocol", "capabilities", "request_contract", "models"} {
		if _, ok := m[k]; !ok {
			t.Errorf("json key %q missing: %s", k, raw)
		}
	}
	// request_contract and models are omitted on the raw wire when empty;
	// publisher validation still rejects request_contract before publish.
	raw2, _ := json.Marshal(ProtocolBindingAccept{Protocol: "glm", Capabilities: []string{"chat"}})
	var m2 map[string]json.RawMessage
	_ = json.Unmarshal(raw2, &m2)
	for _, k := range []string{"request_contract", "models"} {
		if _, ok := m2[k]; ok {
			t.Errorf("optional json key %q should be omitted when empty: %s", k, raw2)
		}
	}
}

func TestProviderInfo_ProtocolBindings_RoundTrip(t *testing.T) {
	in := ProviderInfo{
		ID:      "openai-bridge",
		Name:    "OpenAI Bridge",
		BaseURL: "https://api.openai.com",
		Status:  "active",
		ProtocolBindings: []ProtocolBinding{
			{
				Name:     "anthropic-messages-to-openai",
				Priority: 100,
				Accepts: []ProtocolBindingAccept{
					{
						Protocol:        "anthropic",
						Capabilities:    []string{"chat"},
						RequestContract: "anthropic_messages",
						Models:          []string{"claude-3-5-sonnet-20241022"},
					},
				},
				OutboundProtocol: "openai",
				PathPrefix:       "/v1",
				AuthScheme:       "bearer",
				TranslatorKey:    "standard_to_openai",
				Bridge:           "anthropic_to_openai_chat",
				CompatAPIType:    "openai",
			},
		},
		DefaultProtocolBinding: "anthropic-messages-to-openai",
	}

	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out ProviderInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.DefaultProtocolBinding != "anthropic-messages-to-openai" {
		t.Fatalf("default_protocol_binding round-trip mismatch: %q", out.DefaultProtocolBinding)
	}
	if len(out.ProtocolBindings) != 1 {
		t.Fatalf("want 1 binding, got %d", len(out.ProtocolBindings))
	}
	b := out.ProtocolBindings[0]
	if b.Name != "anthropic-messages-to-openai" || b.Priority != 100 ||
		b.OutboundProtocol != "openai" || b.PathPrefix != "/v1" ||
		b.AuthScheme != "bearer" || b.Bridge != "anthropic_to_openai_chat" ||
		b.CompatAPIType != "openai" {
		t.Fatalf("binding round-trip mismatch: %+v", b)
	}
	if len(b.Accepts) != 1 {
		t.Fatalf("want 1 accept, got %d", len(b.Accepts))
	}
	a := b.Accepts[0]
	if a.Protocol != "anthropic" || a.RequestContract != "anthropic_messages" ||
		len(a.Capabilities) != 1 || len(a.Models) != 1 {
		t.Fatalf("accept round-trip mismatch: %+v", a)
	}
	if !hasBridgeTranslation(EmbeddedProtocolBindingsContract().TranslationMatrix, a.Protocol, b.OutboundProtocol, "chat", a.RequestContract, b.TranslatorKey, b.Bridge) {
		t.Fatalf("round-trip sample uses bridge tuple absent from contract: inbound=%s outbound=%s request_contract=%s translator=%s bridge=%s",
			a.Protocol, b.OutboundProtocol, a.RequestContract, b.TranslatorKey, b.Bridge)
	}
}

package publisher

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tokenplatform/publisher/internal/types"
)

func TestValidateProviderInfo_RejectsMissingProtocolBindings(t *testing.T) {
	info := &types.ProviderInfo{ID: "p1", Status: "active"}
	err := validateProviderInfo(info)
	if err == nil {
		t.Fatal("missing protocol_bindings must be rejected")
	}
	if !strings.Contains(err.Error(), "protocol_bindings") {
		t.Fatalf("error must mention protocol_bindings, got %q", err.Error())
	}
}

func TestValidateProviderInfo_AcceptsValidProtocolBindingsOnly(t *testing.T) {
	info := validProvider()
	if err := validateProviderInfo(info); err != nil {
		t.Fatalf("valid provider must pass: %v", err)
	}
}

func TestValidateProviderInfo_ProviderEntitlementRequiresOpaqueProviderAccountRef(t *testing.T) {
	info := validProvider()
	info.CredentialSource = types.ProviderCredentialSourceProviderEntitlement
	if err := validateProviderInfo(info); err == nil || !strings.Contains(err.Error(), "provider_account_ref required") {
		t.Fatalf("provider_entitlement without provider_account_ref error=%v", err)
	}
	info.ProviderAccountRef = " provider-account-invalid"
	if err := validateProviderInfo(info); err == nil || !strings.Contains(err.Error(), "provider_account_ref invalid") {
		t.Fatalf("provider_entitlement with invalid provider_account_ref error=%v", err)
	}
	info.ProviderAccountRef = "overseas-prod-1"
	if err := validateProviderInfo(info); err != nil {
		t.Fatalf("provider_entitlement with opaque provider_account_ref: %v", err)
	}
}

func TestValidateProviderInfo_RejectsProviderIdentityDerivedAccountRef(t *testing.T) {
	info := validProvider()
	info.CredentialSource = types.ProviderCredentialSourceProviderEntitlement
	info.ProviderAccountRef = info.ID
	if err := validateProviderInfo(info); err == nil || !strings.Contains(err.Error(), "must not reuse provider.id") {
		t.Fatalf("provider ID-derived provider_account_ref error=%v", err)
	}
	info.BaseURL = "provider-endpoint-1"
	info.ProviderAccountRef = info.BaseURL
	if err := validateProviderInfo(info); err == nil || !strings.Contains(err.Error(), "must not reuse provider.base_url") {
		t.Fatalf("base URL-derived provider_account_ref error=%v", err)
	}
}

func TestBuildValidatedPublishProviderRows_ProviderEntitlementPreservesProviderAccountRef(t *testing.T) {
	info := validProvider()
	info.CredentialSource = types.ProviderCredentialSourceProviderEntitlement
	info.ProviderAccountRef = "overseas-prod-1"
	rows, err := buildValidatedPublishProviderRows(info.ID, info)
	if err != nil {
		t.Fatalf("build provider rows: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("provider write produced no outbox rows")
	}
	var payload types.ProviderInfo
	if err := json.Unmarshal(rows[0].Payload, &payload); err != nil {
		t.Fatalf("decode provider outbox payload: %v", err)
	}
	if payload.ProviderAccountRef != info.ProviderAccountRef {
		t.Fatalf("provider_account_ref=%q want %q payload=%s", payload.ProviderAccountRef, info.ProviderAccountRef, rows[0].Payload)
	}
}

func TestValidateProviderInfo_AcceptsCompiledClientCompatibilityBindings(t *testing.T) {
	bindings, defaultBinding, err := types.CompileClientProtocolBindings(types.ProviderProfileOpenAICompatible, []string{types.ClientCompatibilityAll})
	if err != nil {
		t.Fatalf("CompileClientProtocolBindings: %v", err)
	}
	bridges := map[string]string{}
	for _, binding := range bindings {
		if binding.Bridge != "" {
			bridges[binding.Name] = binding.Bridge
		}
	}
	if bridges["anthropic-messages-to-openai"] != "anthropic_to_openai_chat" {
		t.Fatalf("anthropic bridge=%q", bridges["anthropic-messages-to-openai"])
	}
	if bridges["responses-to-openai-chat"] != "responses_to_chat" {
		t.Fatalf("responses bridge=%q", bridges["responses-to-openai-chat"])
	}
	info := validProvider()
	info.ProtocolBindings = bindings
	info.DefaultProtocolBinding = defaultBinding
	if err := validateProviderInfo(info); err != nil {
		t.Fatalf("compiled client compatibility bindings must pass: %v", err)
	}
}

func TestValidateProviderInfo_AcceptsCompiledAnthropicNativeBindings(t *testing.T) {
	bindings, defaultBinding, err := types.CompileClientProtocolBindings(types.ProviderProfileAnthropicNative, []string{types.ClientCompatibilityClaudeCode})
	if err != nil {
		t.Fatalf("CompileClientProtocolBindings: %v", err)
	}
	info := validProvider()
	info.ProtocolBindings = bindings
	info.DefaultProtocolBinding = defaultBinding
	if err := validateProviderInfo(info); err != nil {
		t.Fatalf("compiled anthropic_native bindings must pass: %v", err)
	}
}

func TestValidateProviderInfo_RejectsInvalidProtocolBindingShape(t *testing.T) {
	info := validProvider()
	info.ProtocolBindings = []types.ProtocolBinding{{
		Name:             "",
		OutboundProtocol: "",
		Accepts: []types.ProtocolBindingAccept{{
			Protocol:        "",
			Capabilities:    []string{""},
			RequestContract: "",
		}},
	}}
	err := validateProviderInfo(info)
	if err == nil {
		t.Fatal("invalid protocol_bindings must be rejected")
	}
	joined := err.Error()
	for _, want := range []string{"name required", "outbound_protocol required", "auth_scheme required", "request_contract required"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("error missing %q:\n%s", want, joined)
		}
	}
}

func TestValidateProviderInfo_RejectsInvalidProtocolBindingExtensions(t *testing.T) {
	info := validProvider()
	info.ProtocolBindings[0].CapabilityProfiles = []string{"openai.chat.typo"}
	info.ProtocolBindings[0].SupportedFeatures = []string{"bad_feature"}
	info.ProtocolBindings[0].SupportedParams = []string{"bad_param"}
	info.ProtocolBindings[0].CombinationConstraints = []types.CombinationConstraint{{}}
	info.ProtocolBindings[0].RequestParamPolicy = map[string]types.RequestParamPolicy{
		"temperature": {Category: "bad", UpstreamBehavior: "fixed", OnMismatch: "explode"},
		"bad_param":   {Category: "sampling"},
	}
	info.ProtocolBindings[0].FieldDisposition = &types.FieldDispositionConfig{
		Request: map[string]string{"temperature": "explode"},
	}
	err := validateProviderInfo(info)
	if err == nil {
		t.Fatal("invalid binding extensions must be rejected")
	}
	joined := err.Error()
	for _, want := range []string{"unknown profile", "unknown feature", "unknown param", "combination_constraints", "category unknown", "fixed_value required", "unknown action"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("error missing %q:\n%s", want, joined)
		}
	}
}

func TestValidateProviderInfo_AcceptsValidProtocolBindingExtensions(t *testing.T) {
	info := validProvider()
	info.ProtocolBindings[0].CapabilityProfiles = []string{"openai.chat_completions.base"}
	info.ProtocolBindings[0].SupportedFeatures = []string{"function_tools"}
	info.ProtocolBindings[0].UnsupportedFeatures = []string{"streaming_tools"}
	info.ProtocolBindings[0].SupportedParams = []string{"tools", "tool_choice"}
	info.ProtocolBindings[0].UnsupportedParams = []string{"reasoning_effort"}
	info.ProtocolBindings[0].ParamConstraints = map[string][]string{"tool_choice": {"auto", "none"}}
	info.ProtocolBindings[0].FeatureConstraints = map[string]string{"streaming_tools": "false"}
	info.ProtocolBindings[0].CombinationConstraints = []types.CombinationConstraint{{
		When:     map[string]string{"stream_options.include_usage": "true"},
		Requires: map[string]string{"stream": "true"},
	}}
	info.ProtocolBindings[0].RequestParamPolicy = map[string]types.RequestParamPolicy{
		"temperature": {
			Category:         "sampling",
			UpstreamBehavior: "fixed",
			FixedValue:       "1",
			OnEqual:          "omit",
			OnMismatch:       "reject",
		},
	}
	info.ProtocolBindings[0].FieldDisposition = &types.FieldDispositionConfig{
		Request:  map[string]string{"thinking": "preserve"},
		Response: map[string]string{"reasoning_details": "preserve_vendor_extra"},
	}
	if err := validateProviderInfo(info); err != nil {
		t.Fatalf("valid binding extensions must pass: %v", err)
	}
}

func TestValidateProviderInfo_RequestCorrelationModeUsesGatewayContract(t *testing.T) {
	info := validProvider()
	info.ProtocolBindings[0].RequestCorrelationMode = "gateway_header_and_user_agent"
	if err := validateProviderInfo(info); err != nil {
		t.Fatalf("valid request correlation mode rejected: %v", err)
	}
	rows, err := buildValidatedPublishProviderRows(info.ID, info)
	if err != nil || len(rows) == 0 {
		t.Fatalf("publish provider rows=%d err=%v", len(rows), err)
	}
	var wire types.ProviderInfo
	if err := json.Unmarshal(rows[0].Payload, &wire); err != nil {
		t.Fatalf("decode outbox payload: %v", err)
	}
	if wire.ProtocolBindings[0].RequestCorrelationMode != "gateway_header_and_user_agent" {
		t.Fatalf("outbox payload lost request correlation: %s", rows[0].Payload)
	}

	info.ProtocolBindings[0].RequestCorrelationMode = "tokenhub-special-case"
	err = validateProviderInfo(info)
	if err == nil || !strings.Contains(err.Error(), "request_correlation_mode unknown") {
		t.Fatalf("unknown request correlation mode error=%v", err)
	}
}

func TestValidateProviderInfo_RejectsIncompleteRequestParamPolicy(t *testing.T) {
	info := validProvider()
	info.ProtocolBindings[0].RequestParamPolicy = map[string]types.RequestParamPolicy{
		"temperature": {UpstreamBehavior: "enum"},
	}

	err := validateProviderInfo(info)
	if err == nil {
		t.Fatal("incomplete request_param_policy must be rejected")
	}
	joined := err.Error()
	for _, want := range []string{"category unknown", "on_equal unknown action", "on_mismatch unknown action"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("error missing %q:\n%s", want, joined)
		}
	}
}

func TestValidateProviderInfo_RejectsInvalidProtocolBindingRegistryValues(t *testing.T) {
	info := validProvider()
	info.ProtocolBindings[0].Accepts[0].Protocol = "openai_responses"
	info.ProtocolBindings[0].Accepts[0].Capabilities = []string{"embed"}
	info.ProtocolBindings[0].Accepts[0].RequestContract = "chat"
	info.ProtocolBindings[0].OutboundProtocol = "api-compatible"
	info.ProtocolBindings[0].AuthScheme = "api-key-header"
	info.ProtocolBindings[0].TranslatorKey = "magic"
	info.ProtocolBindings[0].CompatAPIType = "qwen"

	err := validateProviderInfo(info)
	if err == nil {
		t.Fatal("invalid registry values must be rejected")
	}
	joined := err.Error()
	for _, want := range []string{
		"protocol unknown",
		"capabilities[0] unknown",
		"request_contract unknown",
		"outbound_protocol unknown",
		"auth_scheme unknown",
		"translator unknown",
		"compat_api_type unknown",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("error missing %q:\n%s", want, joined)
		}
	}
}

func TestValidateProviderInfo_RejectsUnsupportedProtocolBindingSemantics(t *testing.T) {
	t.Run("outbound path unsupported", func(t *testing.T) {
		info := validProvider()
		info.ProtocolBindings[0].Accepts[0].Capabilities = []string{"count_tokens"}
		info.ProtocolBindings[0].Accepts[0].RequestContract = "anthropic_count_tokens"
		err := validateProviderInfo(info)
		if err == nil || !strings.Contains(err.Error(), "cannot express capability=count_tokens") {
			t.Fatalf("unsupported path must be rejected, got %v", err)
		}
	})

	t.Run("translator mismatch", func(t *testing.T) {
		info := validProvider()
		info.ProtocolBindings[0].Name = "anthropic-to-openai"
		info.ProtocolBindings[0].Accepts[0].Protocol = "anthropic"
		info.ProtocolBindings[0].Accepts[0].RequestContract = "anthropic_messages"
		info.ProtocolBindings[0].TranslatorKey = "standard_to_anthropic"
		info.DefaultProtocolBinding = "anthropic-to-openai"
		err := validateProviderInfo(info)
		if err == nil || !strings.Contains(err.Error(), "translator mismatch") {
			t.Fatalf("translator mismatch must be rejected, got %v", err)
		}
	})
}

func TestValidateProviderInfo_EmptyTranslatorCrossProtocolRejected(t *testing.T) {
	info := validProvider()
	info.ProtocolBindings[0].Name = "anthropic-to-openai"
	info.ProtocolBindings[0].Accepts[0].Protocol = "anthropic"
	info.ProtocolBindings[0].Accepts[0].RequestContract = "anthropic_messages"
	info.ProtocolBindings[0].TranslatorKey = ""
	info.DefaultProtocolBinding = "anthropic-to-openai"

	err := validateProviderInfo(info)
	if err == nil || !strings.Contains(err.Error(), "cross_protocol_bridge_required") {
		t.Fatalf("cross-protocol chat without bridge must be rejected, got %v", err)
	}
}

func TestValidateProviderInfo_SameWireWithBridgeRejected(t *testing.T) {
	info := validProvider()
	info.ProtocolBindings[0].Bridge = "anthropic_to_openai_chat"

	err := validateProviderInfo(info)
	if err == nil || !strings.Contains(err.Error(), "cross_protocol_bridge_kind_mismatch") {
		t.Fatalf("same-wire binding with bridge must be rejected, got %v", err)
	}
}

func TestValidateProviderInfo_WhitelistedBridgeAccepted(t *testing.T) {
	info := validProvider()
	info.ProtocolBindings[0].Name = "anthropic-to-openai"
	info.ProtocolBindings[0].Accepts[0].Protocol = "anthropic"
	info.ProtocolBindings[0].Accepts[0].RequestContract = "anthropic_messages"
	info.ProtocolBindings[0].TranslatorKey = "standard_to_openai"
	info.ProtocolBindings[0].Bridge = "anthropic_to_openai_chat"
	info.DefaultProtocolBinding = "anthropic-to-openai"

	if err := validateProviderInfo(info); err != nil {
		t.Fatalf("whitelisted bridge must pass: %v", err)
	}
}

func TestValidateProviderInfo_BridgeTranslatorMismatchRejected(t *testing.T) {
	info := validProvider()
	info.ProtocolBindings[0].Name = "anthropic-to-openai"
	info.ProtocolBindings[0].Accepts[0].Protocol = "anthropic"
	info.ProtocolBindings[0].Accepts[0].RequestContract = "anthropic_messages"
	info.ProtocolBindings[0].TranslatorKey = "responses_to_chat_synthesis"
	info.ProtocolBindings[0].Bridge = "anthropic_to_openai_chat"
	info.DefaultProtocolBinding = "anthropic-to-openai"

	err := validateProviderInfo(info)
	if err == nil || !strings.Contains(err.Error(), "translator mismatch for bridge=anthropic_to_openai_chat") {
		t.Fatalf("bridge translator mismatch must be rejected, got %v", err)
	}
}

func TestValidateProviderInfo_NonChatCapabilitiesDoNotRequireBridge(t *testing.T) {
	cases := []struct {
		name             string
		outboundProtocol string
		authScheme       string
		protocol         string
		capability       string
		requestContract  string
	}{
		{
			name:             "rerank",
			outboundProtocol: "cohere_v2",
			authScheme:       "bearer",
			protocol:         "openai",
			capability:       "rerank",
			requestContract:  "rerank",
		},
		{
			name:             "embeddings",
			outboundProtocol: "openai",
			authScheme:       "bearer",
			protocol:         "openai",
			capability:       "embeddings",
			requestContract:  "embeddings",
		},
		{
			name:             "count_tokens",
			outboundProtocol: "anthropic",
			authScheme:       "x-api-key",
			protocol:         "anthropic",
			capability:       "count_tokens",
			requestContract:  "anthropic_count_tokens",
		},
		{
			name:             "image_generation",
			outboundProtocol: "openai",
			authScheme:       "bearer",
			protocol:         "openai",
			capability:       "image_generation",
			requestContract:  "image_generations",
		},
		{
			name:             "video_generation",
			outboundProtocol: "ark",
			authScheme:       "bearer",
			protocol:         "ark",
			capability:       "video_generation",
			requestContract:  "ark_video_task_create",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info := validProvider()
			info.ProtocolBindings[0].Name = tc.name
			info.ProtocolBindings[0].OutboundProtocol = tc.outboundProtocol
			info.ProtocolBindings[0].AuthScheme = tc.authScheme
			info.ProtocolBindings[0].TranslatorKey = "passthrough"
			info.ProtocolBindings[0].Accepts[0].Protocol = tc.protocol
			info.ProtocolBindings[0].Accepts[0].Capabilities = []string{tc.capability}
			info.ProtocolBindings[0].Accepts[0].RequestContract = tc.requestContract
			info.DefaultProtocolBinding = tc.name

			if err := validateProviderInfo(info); err != nil {
				t.Fatalf("non-chat capability should not require bridge: %v", err)
			}
		})
	}
}

func TestValidateProviderInfo_NonChatCapabilityWithBridgeRejected(t *testing.T) {
	info := validProvider()
	info.ProtocolBindings[0].Name = "cohere-rerank"
	info.ProtocolBindings[0].OutboundProtocol = "cohere_v2"
	info.ProtocolBindings[0].AuthScheme = "bearer"
	info.ProtocolBindings[0].TranslatorKey = "passthrough"
	info.ProtocolBindings[0].Bridge = "responses_to_chat"
	info.ProtocolBindings[0].Accepts[0].Protocol = "openai"
	info.ProtocolBindings[0].Accepts[0].Capabilities = []string{"rerank"}
	info.ProtocolBindings[0].Accepts[0].RequestContract = "rerank"
	info.DefaultProtocolBinding = "cohere-rerank"

	err := validateProviderInfo(info)
	if err == nil || !strings.Contains(err.Error(), "cross_protocol_bridge_kind_mismatch") {
		t.Fatalf("non-chat binding with bridge must be rejected, got %v", err)
	}
}

func TestProtocolWireProfilesCoverContractProtocols(t *testing.T) {
	seen := map[string]struct{}{}
	for _, protocol := range protocolBindingContract.InboundProtocols {
		seen[protocol] = struct{}{}
	}
	for _, protocol := range protocolBindingContract.OutboundProtocols {
		seen[protocol] = struct{}{}
	}
	for protocol := range seen {
		if wireProfileForProtocol(protocol) == "" {
			t.Fatalf("wire profile missing for protocol %q", protocol)
		}
	}
}

func TestValidateProviderInfo_RejectsDuplicateBindingName(t *testing.T) {
	info := validProvider()
	info.ProtocolBindings = append(info.ProtocolBindings, info.ProtocolBindings[0])
	if err := validateProviderInfo(info); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate binding name must be rejected, got %v", err)
	}
}

func TestValidateProviderInfo_RejectsBadDefaultProtocolBinding(t *testing.T) {
	info := validProvider()
	info.DefaultProtocolBinding = "missing"
	if err := validateProviderInfo(info); err == nil || !strings.Contains(err.Error(), "default_protocol_binding") {
		t.Fatalf("bad default binding must be rejected, got %v", err)
	}
}

func TestValidateProviderInfo_RejectsInvalidStatus(t *testing.T) {
	info := validProvider()
	info.Status = "totally-made-up"
	if err := validateProviderInfo(info); err == nil {
		t.Fatal("invalid status must be rejected")
	}
}

func TestValidateProviderInfo_RejectsNilInfo(t *testing.T) {
	if err := validateProviderInfo(nil); err == nil {
		t.Fatal("nil info must be rejected")
	}
}

func validProvider() *types.ProviderInfo {
	return &types.ProviderInfo{
		ID:                     "p1",
		Name:                   "Provider",
		BaseURL:                "https://example.com",
		Status:                 "active",
		ProtocolBindings:       validProtocolBindings(),
		DefaultProtocolBinding: "openai-chat",
	}
}

func validProtocolBindings() []types.ProtocolBinding {
	return []types.ProtocolBinding{{
		Name:             "openai-chat",
		OutboundProtocol: "openai",
		AuthScheme:       "bearer",
		Accepts: []types.ProtocolBindingAccept{{
			Protocol:        "openai",
			Capabilities:    []string{"chat"},
			RequestContract: "chat_completions",
		}},
	}}
}
